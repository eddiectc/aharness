package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// retainedTurns is the number of most recent model-response turns whose
// transcript is preserved verbatim.
const retainedTurns = 4

// Projection accounting constants.
const (
	tokensPerMessage  = 8
	tokensPerTool     = 16
	bytesPerTokenBits = 2 // ceil(bytes / 4)
)

// Project rebuilds the canonical model input from the event log. It is a pure
// function of events and limits: the system prompt and tool schemas are
// selected from the versions recorded in session_start. A session that records
// custom tools cannot be projected here; use ProjectWithTools so the caller
// either supplies matching implementations or fails closed.
func Project(events []session.Event, limits Limits) (Projection, error) {
	return project(events, limits, nil)
}

// ProjectWithTools rebuilds the canonical model input and appends the caller
// tool declarations recorded in session_start. Every recorded custom tool must
// be supplied with an identical name, description, canonical parameters, and
// concurrency declaration; a partial or changed set is an ErrToolMismatch.
// Custom schemas are appended before the recorded grant filter so replay sends
// exactly the set the model saw.
func ProjectWithTools(events []session.Event, limits Limits, available []CustomTool) (Projection, error) {
	return project(events, limits, available)
}

func project(events []session.Event, limits Limits, available []CustomTool) (Projection, error) {
	start, err := projectSessionStart(events)
	if err != nil {
		return Projection{}, err
	}
	prompt, err := projectPrompt(start)
	if err != nil {
		return Projection{}, err
	}
	schemas, err := tools.SchemasFor(start.ToolsetVersion)
	if err != nil {
		return Projection{}, fmt.Errorf("project: %w", err)
	}
	recorded := customToolsFromData(start.CustomTools)
	if len(recorded) > 0 {
		if err := ValidateCustomTools(recorded); err != nil {
			return Projection{}, fmt.Errorf("%w: recorded custom tools: %w", ErrToolMismatch, err)
		}
		if available == nil {
			return Projection{}, fmt.Errorf("%w: session records %d custom tool(s) but none were supplied", ErrToolMismatch, len(recorded))
		}
		if err := VerifyCustomTools(recorded, available); err != nil {
			return Projection{}, err
		}
	}
	for _, tool := range recorded {
		schemas = append(schemas, model.ToolSchema{
			Name:        tool.Schema.Name,
			Description: tool.Schema.Description,
			Parameters:  append(json.RawMessage(nil), tool.Schema.Parameters...),
		})
	}
	grant := start.Tools
	if start.RootTools != nil {
		grant = start.RootTools
	}
	if grant != nil {
		schemas, err = tools.Filter(schemas, grant)
		if err != nil {
			return Projection{}, fmt.Errorf("project: recorded tool grant: %w", err)
		}
	}

	projected := []projectedMessage{{
		kind: projectionSystem,
		msg:  model.Message{Role: model.RoleSystem, Content: prompt},
	}}
	nodes := make([]*assistantNode, 0, 8)
	var current *assistantNode
	assistantTurns := 0

	for _, ev := range events {
		switch ev.Type {
		case session.TypeUserMessage:
			var data userMessageData
			if err := decodeEventData(ev, &data); err != nil {
				return Projection{}, err
			}
			current = nil
			projected = append(projected, projectedMessage{
				kind: projectionUser,
				msg:  model.Message{Role: model.RoleUser, Content: cleanText(data.Text)},
			})
		case session.TypeModelResponse:
			var data modelResponseData
			if err := decodeEventData(ev, &data); err != nil {
				return Projection{}, err
			}
			node := &assistantNode{turn: assistantTurns}
			assistantTurns++
			msg := model.Message{Role: model.RoleAssistant, Content: cleanText(data.Text)}
			for _, call := range data.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{
					ID:        cleanText(call.ID),
					Name:      cleanText(call.Name),
					Arguments: call.Arguments,
				})
			}
			node.msgIndex = len(projected)
			node.calls = make([]assistantCall, len(msg.ToolCalls))
			for i, call := range msg.ToolCalls {
				node.calls[i].id = call.ID
				node.calls[i].name = call.Name
			}
			nodes = append(nodes, node)
			projected = append(projected, projectedMessage{kind: projectionAssistant, msg: msg})
			current = node
		case session.TypeToolResult:
			var data toolResultData
			if err := decodeEventData(ev, &data); err != nil {
				return Projection{}, err
			}
			if current == nil {
				continue
			}
			callID := cleanText(data.CallID)
			call := current.unpairedCall(callID)
			if call == nil {
				continue
			}
			content := formatToolResult(data)
			call.paired = true
			projected = append(projected, projectedMessage{
				kind:          projectionTool,
				assistantTurn: current.turn,
				originalBytes: len(content),
				msg: model.Message{
					Role:       model.RoleTool,
					Name:       cleanText(call.name),
					ToolCallID: callID,
					Content:    content,
				},
			})
		}
	}

	// Every assistant tool call must have received its matching result. A
	// log with an unpaired call is corrupt: do not silently drop the call or
	// let the next model request violate the pairing contract.
	for _, node := range nodes {
		for _, call := range node.calls {
			if !call.paired {
				return Projection{}, fmt.Errorf("project: unpaired tool call %q", call.id)
			}
		}
	}

	messages := make([]model.Message, 0, len(projected))
	meta := make([]projectedMessage, 0, len(projected))
	for _, msg := range projected {
		messages = append(messages, msg.msg)
		meta = append(meta, msg)
	}

	before := estimateTokens(messages, len(schemas))
	target := limits.CompactionTarget
	if target <= 0 || (limits.MaxInputTokens > 0 && target > limits.MaxInputTokens) {
		target = limits.MaxInputTokens
	}

	elided := 0
	estimated := before
	if target > 0 && estimated > target {
		cutoff := assistantTurns - retainedTurns
		for i := range messages {
			if estimated <= target {
				break
			}
			if meta[i].kind != projectionTool || meta[i].assistantTurn >= cutoff {
				continue
			}
			stub := elidedToolResult(messages[i].Name, meta[i].originalBytes)
			if len(stub) >= len(messages[i].Content) {
				continue
			}
			messages[i].Content = stub
			elided++
			estimated = estimateTokens(messages, len(schemas))
		}
	}

	projection := Projection{
		Messages:             messages,
		Schemas:              schemas,
		EstimatedInputTokens: estimated,
		BeforeTokens:         before,
		AfterTokens:          estimated,
		ElidedResults:        elided,
	}
	if limits.MaxInputTokens > 0 && estimated > limits.MaxInputTokens {
		return projection, fmt.Errorf("%w: estimated %d input tokens exceeds limit %d", ErrProjectionTooLarge, estimated, limits.MaxInputTokens)
	}
	return projection, nil
}

// estimateTokens applies the projection contract: ceil(bytes/4) per message
// plus 8 tokens per message and 16 tokens per tool schema. Message bytes are
// the UTF-8 bytes of the role, content, and tool-call metadata/arguments.
func estimateTokens(messages []model.Message, toolCount int) int {
	tokens := 0
	for _, msg := range messages {
		n := len(msg.Role) + len(msg.Content) + len(msg.ToolCallID) + len(msg.Name)
		for _, call := range msg.ToolCalls {
			n += len(call.ID) + len(call.Name) + len(call.Arguments)
		}
		tokens += ((n + (1 << bytesPerTokenBits) - 1) >> bytesPerTokenBits) + tokensPerMessage
	}
	return tokens + toolCount*tokensPerTool
}

func elidedToolResult(name string, originalBytes int) string {
	if name == "" {
		name = "tool"
	}
	return fmt.Sprintf("[elided tool result %q: %d bytes]", name, originalBytes)
}

func formatToolResult(data toolResultData) string {
	var b strings.Builder
	if data.OK {
		b.WriteString("ok")
		if data.ExitCode != nil {
			fmt.Fprintf(&b, " (exit %d)", *data.ExitCode)
		}
	} else {
		b.WriteString("error")
		if data.Code != "" {
			fmt.Fprintf(&b, " [%s]", data.Code)
		}
		if data.Error != "" {
			b.WriteString(": ")
			b.WriteString(cleanText(data.Error))
		}
	}
	if data.Output != "" {
		b.WriteByte('\n')
		b.WriteString(cleanText(data.Output))
	}
	if data.TimedOut {
		b.WriteString("\n[timed out]")
	}
	if data.Truncated {
		b.WriteString("\n[output truncated]")
	}
	return b.String()
}

type projectionKind int

const (
	projectionSystem projectionKind = iota
	projectionUser
	projectionAssistant
	projectionTool
)

type projectedMessage struct {
	kind          projectionKind
	msg           model.Message
	assistantTurn int
	originalBytes int
}

type assistantCall struct {
	id     string
	name   string
	paired bool
}

type assistantNode struct {
	turn     int
	msgIndex int
	calls    []assistantCall
}

func (n *assistantNode) unpairedCall(id string) *assistantCall {
	if id == "" {
		return nil
	}
	for i := range n.calls {
		if n.calls[i].id == id && !n.calls[i].paired {
			return &n.calls[i]
		}
	}
	return nil
}

// VerifyCustomTools compares the recorded custom-tool contract with the set a
// caller supplied. Names, descriptions, canonical parameters, concurrency
// declarations, and declaration order must agree.
func VerifyCustomTools(recorded, supplied []CustomTool) error {
	if len(recorded) != len(supplied) {
		return fmt.Errorf("%w: session records %d custom tool(s), caller supplied %d", ErrToolMismatch, len(recorded), len(supplied))
	}
	for i := range recorded {
		want, got := recorded[i], supplied[i]
		if want.Schema.Name != got.Schema.Name {
			return fmt.Errorf("%w: custom tool %d is %q, caller supplied %q", ErrToolMismatch, i+1, want.Schema.Name, got.Schema.Name)
		}
		if want.Schema.Description != got.Schema.Description {
			return fmt.Errorf("%w: custom tool %q description changed", ErrToolMismatch, want.Schema.Name)
		}
		wantParams, err := canonicalJSON(want.Schema.Parameters)
		if err != nil {
			return fmt.Errorf("%w: recorded custom tool %q parameters: %w", ErrToolMismatch, want.Schema.Name, err)
		}
		gotParams, err := canonicalJSON(got.Schema.Parameters)
		if err != nil {
			return fmt.Errorf("%w: custom tool %q parameters: %w", ErrToolMismatch, want.Schema.Name, err)
		}
		if wantParams != gotParams {
			return fmt.Errorf("%w: custom tool %q parameters changed", ErrToolMismatch, want.Schema.Name)
		}
		if want.ConcurrencySafe != got.ConcurrencySafe {
			return fmt.Errorf("%w: custom tool %q concurrency declaration changed", ErrToolMismatch, want.Schema.Name)
		}
	}
	return nil
}

// ValidateCustomTools enforces the construction-time custom-tool contract:
// names are unique and distinct from every built-in of the current toolset,
// each schema is within its per-tool cap, and the total recorded schema stays
// within the aggregate cap.
func ValidateCustomTools(custom []CustomTool) error {
	if len(custom) > tools.MaxCustomTools {
		return fmt.Errorf("%d custom tools, limit %d", len(custom), tools.MaxCustomTools)
	}
	builtins := make(map[string]struct{})
	for _, schema := range tools.Schemas() {
		builtins[schema.Name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(custom))
	total := 0
	for _, tool := range custom {
		if err := tools.ValidateCustomTool(tool.Schema); err != nil {
			return err
		}
		name := tool.Schema.Name
		if _, ok := builtins[name]; ok {
			return fmt.Errorf("custom tool %q collides with a built-in tool", name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("custom tool %q is registered more than once", name)
		}
		seen[name] = struct{}{}
		total += len(name) + len(tool.Schema.Description) + len(tool.Schema.Parameters)
		if total > tools.MaxCustomToolBytes {
			return fmt.Errorf("custom tool schemas total %d bytes, limit %d", total, tools.MaxCustomToolBytes)
		}
	}
	return nil
}

// renders the recorded version when the log predates the recorded prompt. The
// recorded text wins verbatim, so editing an agent file never rewrites an
// existing session's prompt.
func projectPrompt(start sessionStartData) (string, error) {
	if start.SystemPrompt != "" {
		return capText(cleanText(start.SystemPrompt), maxSystemPromptBytes), nil
	}
	prompt, err := renderPrompt(start.PromptVersion, start.Workspace, nil)
	if err != nil {
		return "", err
	}
	return capText(prompt, maxSystemPromptBytes), nil
}

func projectSessionStart(events []session.Event) (sessionStartData, error) {
	for _, ev := range events {
		if ev.Type != session.TypeSessionStart {
			continue
		}
		var data sessionStartData
		if err := decodeEventData(ev, &data); err != nil {
			return sessionStartData{}, err
		}
		return data, nil
	}
	return sessionStartData{}, errors.New("project: session_start event missing")
}

func decodeEventData[T any](ev session.Event, out *T) error {
	if err := jsonv2.Unmarshal(ev.Data, out); err != nil {
		return fmt.Errorf("decode %s payload: %w", ev.Type, err)
	}
	return nil
}

// cleanText makes a stored string safe to place in a canonical message.
func cleanText(s string) string { return strings.ToValidUTF8(s, "\uFFFD") }

// canonicalJSON canonicalizes a provider-supplied JSON value: object members
// are sorted by name recursively, array order and numeric literals are
// preserved, duplicate object names are rejected, and insignificant
// whitespace is ignored.
func canonicalJSON(raw json.RawMessage) (string, error) {
	return canonicalJSONWithout(raw, "")
}

// canonicalJSONWithout canonicalizes raw and, when drop is non-empty, removes
// that top-level object member first. Repeated-failure detection uses it to
// ignore display-only tool arguments such as intent, so calls that differ only
// in their title still count as the same failure.
func canonicalJSONWithout(raw json.RawMessage, drop string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	value, err := readJSONValue(dec)
	if err != nil {
		return "", err
	}
	if tok, trailingErr := dec.Token(); trailingErr == nil {
		return "", fmt.Errorf("canonical json: trailing token %v", tok)
	} else if !errors.Is(trailingErr, io.EOF) {
		return "", fmt.Errorf("canonical json: trailing data: %w", trailingErr)
	}
	if drop != "" && value.kind == 'o' {
		kept := value.members[:0]
		for _, member := range value.members {
			if member.name != drop {
				kept = append(kept, member)
			}
		}
		value.members = kept
	}
	var b strings.Builder
	writeCanonical(&b, value)
	return b.String(), nil
}

// jsonValue is one parsed JSON value. Objects retain sorted members; numbers
// retain their literal text.
type jsonValue struct {
	kind    byte // 'o' object, 'a' array, 's' string, 'n' number, 't' true, 'f' false, 'z' null
	str     string
	members []jsonMember
	array   []jsonValue
}

type jsonMember struct {
	name  string
	value jsonValue
}

func readJSONValue(dec *json.Decoder) (jsonValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return jsonValue{}, fmt.Errorf("canonical json: %w", err)
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			members := make([]jsonMember, 0, 8)
			seen := make(map[string]bool)
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return jsonValue{}, fmt.Errorf("canonical json: read member name: %w", err)
				}
				key, ok := keyTok.(string)
				if !ok {
					return jsonValue{}, errors.New("canonical json: object member name is not a string")
				}
				if seen[key] {
					return jsonValue{}, fmt.Errorf("canonical json: duplicate object name %q", key)
				}
				seen[key] = true
				value, err := readJSONValue(dec)
				if err != nil {
					return jsonValue{}, err
				}
				members = append(members, jsonMember{name: key, value: value})
			}
			if _, err := dec.Token(); err != nil {
				return jsonValue{}, fmt.Errorf("canonical json: close object: %w", err)
			}
			sortJSONMembers(members)
			return jsonValue{kind: 'o', members: members}, nil
		case '[':
			values := make([]jsonValue, 0, 8)
			for dec.More() {
				value, err := readJSONValue(dec)
				if err != nil {
					return jsonValue{}, err
				}
				values = append(values, value)
			}
			if _, err := dec.Token(); err != nil {
				return jsonValue{}, fmt.Errorf("canonical json: close array: %w", err)
			}
			return jsonValue{kind: 'a', array: values}, nil
		default:
			return jsonValue{}, fmt.Errorf("canonical json: unexpected delimiter %q", string(v))
		}
	case string:
		return jsonValue{kind: 's', str: v}, nil
	case json.Number:
		return jsonValue{kind: 'n', str: v.String()}, nil
	case bool:
		if v {
			return jsonValue{kind: 't'}, nil
		}
		return jsonValue{kind: 'f'}, nil
	case nil:
		return jsonValue{kind: 'z'}, nil
	default:
		return jsonValue{}, fmt.Errorf("canonical json: unsupported token %T", tok)
	}
}

func sortJSONMembers(members []jsonMember) {
	// Insertion sort keeps the implementation small; argument objects are
	// tiny in practice.
	for i := 1; i < len(members); i++ {
		for j := i; j > 0 && members[j].name < members[j-1].name; j-- {
			members[j], members[j-1] = members[j-1], members[j]
		}
	}
}

func writeCanonical(b *strings.Builder, value jsonValue) {
	switch value.kind {
	case 'o':
		b.WriteByte('{')
		for i, member := range value.members {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, member.name)
			b.WriteByte(':')
			writeCanonical(b, member.value)
		}
		b.WriteByte('}')
	case 'a':
		b.WriteByte('[')
		for i, item := range value.array {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonical(b, item)
		}
		b.WriteByte(']')
	case 's':
		writeJSONString(b, value.str)
	case 'n':
		b.WriteString(value.str)
	case 't':
		b.WriteString("true")
	case 'f':
		b.WriteString("false")
	case 'z':
		b.WriteString("null")
	}
}

func writeJSONString(b *strings.Builder, s string) {
	encoded, err := json.Marshal(s)
	if err != nil {
		b.WriteString(`""`)
		return
	}
	b.Write(encoded)
}
