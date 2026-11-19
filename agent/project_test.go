package agent

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

func appendToolTurn(t *testing.T, events []session.Event, turn int, id, text, output string) []session.Event {
	t.Helper()
	events = append(events, newTestEvent(t, session.TypeModelResponse, turn, string(model.RoleAssistant), modelResponseData{
		Text:         text,
		ToolCalls:    []loggedToolCall{{ID: id, Name: "read_file", Arguments: []byte(`{"path":"a.txt"}`)}},
		FinishReason: model.FinishToolCalls,
		Usage:        model.Usage{InputTokens: 1, OutputTokens: 1},
	}))
	events = append(events, newTestEvent(t, session.TypeToolResult, turn, string(model.RoleTool), toolResultData{
		CallID: id,
		Name:   "read_file",
		OK:     true,
		Code:   tools.CodeOK,
		Output: output,
	}))
	return events
}

func TestProjectSystemPromptAndEstimate(t *testing.T) {
	workspace := "/workspace/project"
	events := []session.Event{
		startEvent(t, workspace),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
	}
	limits := Limits{MaxInputTokens: 1_000_000, CompactionTarget: 1_000_000}
	proj, err := Project(events, limits)
	if err != nil {
		t.Fatalf("Project error = %v", err)
	}
	if len(proj.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(proj.Messages))
	}
	if proj.Messages[0].Role != model.RoleSystem {
		t.Fatalf("first role = %q, want system", proj.Messages[0].Role)
	}
	if !strings.Contains(proj.Messages[0].Content, workspace) {
		t.Fatalf("system prompt does not include workspace %q: %q", workspace, proj.Messages[0].Content)
	}
	if proj.Messages[1].Role != model.RoleUser || proj.Messages[1].Content != "task" {
		t.Fatalf("task message = %+v", proj.Messages[1])
	}

	systemTokens := (len(string(model.RoleSystem))+len(testSystemPrompt(t, workspace))+3)/4 + tokensPerMessage
	userTokens := (len(string(model.RoleUser))+len("task")+3)/4 + tokensPerMessage
	want := systemTokens + userTokens + len(proj.Schemas)*tokensPerTool
	if proj.BeforeTokens != want || proj.AfterTokens != want || proj.EstimatedInputTokens != want {
		t.Fatalf("estimate = before %d after %d input %d, want %d",
			proj.BeforeTokens, proj.AfterTokens, proj.EstimatedInputTokens, want)
	}
	if !reflect.DeepEqual(proj.Schemas, tools.Schemas()) {
		t.Fatalf("projection schemas do not match the current toolset")
	}
	if proj.ElidedResults != 0 {
		t.Fatalf("elided = %d, want 0", proj.ElidedResults)
	}
}

func TestProjectSelectsToolsetSchemas(t *testing.T) {
	workspace := "/workspace/project"
	limits := Limits{MaxInputTokens: 1_000_000, CompactionTarget: 1_000_000}
	for _, version := range []string{"toolset-v0", "toolset-v1"} {
		t.Run(version, func(t *testing.T) {
			events := []session.Event{
				startEventForToolset(t, workspace, version),
				newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
			}
			proj, err := Project(events, limits)
			if err != nil {
				t.Fatalf("Project(%s) error = %v", version, err)
			}
			want, err := tools.SchemasFor(version)
			if err != nil {
				t.Fatalf("SchemasFor(%s): %v", version, err)
			}
			if !reflect.DeepEqual(proj.Schemas, want) {
				t.Fatalf("projection schemas = %v, want the frozen %s set", schemaNames(proj.Schemas), version)
			}
			systemTokens := (len(string(model.RoleSystem))+len(testSystemPrompt(t, workspace))+3)/4 + tokensPerMessage
			userTokens := (len(string(model.RoleUser))+len("task")+3)/4 + tokensPerMessage
			wantTokens := systemTokens + userTokens + len(want)*tokensPerTool
			if proj.EstimatedInputTokens != wantTokens {
				t.Fatalf("estimate = %d, want %d using %d selected schemas", proj.EstimatedInputTokens, wantTokens, len(want))
			}
		})
	}

	t.Run("unknown version", func(t *testing.T) {
		events := []session.Event{
			startEventForToolset(t, workspace, "toolset-v9"),
			newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
		}
		_, err := Project(events, limits)
		if err == nil {
			t.Fatal("Project(unknown toolset) error = nil, want error")
		}
		if !strings.Contains(err.Error(), "toolset-v9") {
			t.Fatalf("Project error = %v, want the unknown version", err)
		}
	})
}

// testSystemPrompt renders the current prompt for a workspace without a
// catalog, which is what a start event without a recorded system prompt
// projects.
func testSystemPrompt(t *testing.T, workspace string) string {
	t.Helper()
	text, err := renderPrompt(PromptVersion, workspace, "aharness", nil)
	if err != nil {
		t.Fatalf("renderPrompt: %v", err)
	}
	return text
}

func startEventForToolset(t *testing.T, workspace, version string) session.Event {
	t.Helper()
	cfg := testConfig()
	return newTestEvent(t, session.TypeSessionStart, 0, "", sessionStartData{
		PromptVersion:    PromptVersion,
		ToolsetVersion:   version,
		Workspace:        workspace,
		StateDir:         "/tmp/state",
		Provider:         "opencode-go",
		Dialect:          "openai-chat",
		Model:            "test-model",
		Limits:           cfg.Limits,
		MaxTurns:         cfg.MaxTurns,
		MaxTotalTokens:   cfg.MaxTotalTokens,
		SessionTimeoutMS: cfg.SessionTimeout.Milliseconds(),
		ModelTimeoutMS:   cfg.ModelTimeout.Milliseconds(),
		HarnessVersion:   "test-harness",
	})
}

func schemaNames(schemas []model.ToolSchema) []string {
	out := make([]string, len(schemas))
	for i, schema := range schemas {
		out[i] = schema.Name
	}
	return out
}

func TestProjectRetentionAndElision(t *testing.T) {
	events := []session.Event{
		startEvent(t, "/workspace"),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
	}
	const outputSize = 5000
	for i := range 6 {
		events = appendToolTurn(t, events, i+1, "c"+strconv.Itoa(i), "assistant "+strconv.Itoa(i), exactOutput(outputSize))
	}

	limits := Limits{MaxInputTokens: 0, CompactionTarget: 1}
	proj, err := Project(events, limits)
	if err != nil {
		t.Fatalf("Project error = %v", err)
	}
	if proj.ElidedResults != 2 {
		t.Fatalf("elided results = %d, want 2 (only turns older than the last four)", proj.ElidedResults)
	}
	if proj.BeforeTokens <= proj.AfterTokens {
		t.Fatalf("before %d <= after %d, want a reduction", proj.BeforeTokens, proj.AfterTokens)
	}
	if proj.EstimatedInputTokens != proj.AfterTokens {
		t.Fatalf("estimated input = %d, want after = %d", proj.EstimatedInputTokens, proj.AfterTokens)
	}
	if len(proj.Messages) != 14 {
		t.Fatalf("messages = %d, want 14", len(proj.Messages))
	}

	toolMessages := make(map[string]model.Message)
	assistantCalls := make(map[string]bool)
	for _, msg := range proj.Messages {
		if msg.Role == model.RoleTool {
			toolMessages[msg.ToolCallID] = msg
		}
		if msg.Role == model.RoleAssistant {
			for _, call := range msg.ToolCalls {
				assistantCalls[call.ID] = true
			}
		}
	}
	if len(toolMessages) != 6 || len(assistantCalls) != 6 {
		t.Fatalf("tool messages = %d, assistant calls = %d, want 6/6", len(toolMessages), len(assistantCalls))
	}
	for id := range toolMessages {
		if !assistantCalls[id] {
			t.Fatalf("tool result %q has no assistant call", id)
		}
	}
	for _, id := range []string{"c0", "c1"} {
		msg := toolMessages[id]
		if !strings.Contains(msg.Content, "elided tool result") {
			t.Fatalf("%s content = %q, want elision stub", id, msg.Content)
		}
		if !strings.Contains(msg.Content, strconv.Itoa(len(formatToolResult(toolResultData{
			CallID: id, Name: "read_file", OK: true, Code: tools.CodeOK, Output: exactOutput(outputSize),
		})))) {
			t.Fatalf("%s stub lacks original byte count: %q", id, msg.Content)
		}
		if msg.Name != "read_file" {
			t.Fatalf("%s name = %q, want read_file", id, msg.Name)
		}
	}
	for i := 2; i < 6; i++ {
		id := "c" + strconv.Itoa(i)
		want := formatToolResult(toolResultData{
			CallID: id, Name: "read_file", OK: true, Code: tools.CodeOK, Output: exactOutput(outputSize),
		})
		if got := toolMessages[id].Content; got != want {
			t.Fatalf("%s content changed; got %d bytes want %d bytes", id, len(got), len(want))
		}
	}
	for i := 2; i < 6; i++ {
		want := "assistant " + strconv.Itoa(i)
		found := false
		for _, msg := range proj.Messages {
			if msg.Role == model.RoleAssistant && msg.Content == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("recent assistant text %q was not preserved", want)
		}
	}
}

func TestProjectPairingInvariants(t *testing.T) {
	events := []session.Event{
		startEvent(t, "/workspace"),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
		newTestEvent(t, session.TypeModelResponse, 1, string(model.RoleAssistant), modelResponseData{
			Text: "first",
			ToolCalls: []loggedToolCall{
				{ID: "a", Name: "read_file", Arguments: []byte(`{}`)},
			},
			FinishReason: model.FinishToolCalls,
		}),
		newTestEvent(t, session.TypeToolResult, 1, string(model.RoleTool), toolResultData{
			CallID: "a", Name: "read_file", OK: true, Code: tools.CodeOK, Output: "a-output",
		}),
		// Orphan result: no preceding call with this ID.
		newTestEvent(t, session.TypeToolResult, 1, string(model.RoleTool), toolResultData{
			CallID: "orphan", Name: "read_file", OK: true, Code: tools.CodeOK, Output: "orphan-output",
		}),
		newTestEvent(t, session.TypeModelResponse, 2, string(model.RoleAssistant), modelResponseData{
			Text:         "second",
			ToolCalls:    []loggedToolCall{{ID: "c", Name: "read_file", Arguments: []byte(`{}`)}},
			FinishReason: model.FinishToolCalls,
		}),
		newTestEvent(t, session.TypeToolResult, 2, string(model.RoleTool), toolResultData{
			CallID: "c", Name: "read_file", OK: true, Code: tools.CodeOK, Output: "c-output",
		}),
	}
	proj, err := Project(events, Limits{MaxInputTokens: 1_000_000, CompactionTarget: 1_000_000})
	if err != nil {
		t.Fatalf("Project error = %v", err)
	}
	calls := make(map[string]bool)
	results := make(map[string]bool)
	for _, msg := range proj.Messages {
		if msg.Role == model.RoleAssistant {
			for _, call := range msg.ToolCalls {
				calls[call.ID] = true
			}
		}
		if msg.Role == model.RoleTool {
			if !calls[msg.ToolCallID] {
				t.Fatalf("tool result %q was emitted before or without its call", msg.ToolCallID)
			}
			results[msg.ToolCallID] = true
		}
	}
	if calls["orphan"] || results["orphan"] {
		t.Fatal("orphan result was emitted")
	}
	if !calls["a"] || !results["a"] || !calls["c"] || !results["c"] {
		t.Fatalf("paired calls/results missing: calls=%v results=%v", calls, results)
	}
	if got := toolMessagesCount(proj.Messages); got != 2 {
		t.Fatalf("tool messages = %d, want 2", got)
	}
}

func TestProjectRejectsUnpairedToolCall(t *testing.T) {
	events := []session.Event{
		startEvent(t, "/workspace"),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
		newTestEvent(t, session.TypeModelResponse, 1, string(model.RoleAssistant), modelResponseData{
			Text:         "first",
			ToolCalls:    []loggedToolCall{{ID: "missing", Name: "read_file", Arguments: []byte(`{}`)}},
			FinishReason: model.FinishToolCalls,
		}),
	}
	_, err := Project(events, Limits{MaxInputTokens: 1_000_000, CompactionTarget: 1_000_000})
	if err == nil {
		t.Fatal("Project error = nil, want unpaired tool call error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Project error = %v, want the unpaired call id", err)
	}
}

func TestProjectTooLarge(t *testing.T) {
	events := []session.Event{
		startEvent(t, "/workspace"),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: strings.Repeat("t", 4000), Source: "-task"}),
	}
	proj, err := Project(events, Limits{MaxInputTokens: 100, CompactionTarget: 100})
	if !errors.Is(err, ErrProjectionTooLarge) {
		t.Fatalf("Project error = %v, want ErrProjectionTooLarge", err)
	}
	if proj.ElidedResults != 0 {
		t.Fatalf("elided = %d, want 0", proj.ElidedResults)
	}
	if proj.EstimatedInputTokens <= 100 {
		t.Fatalf("estimated input = %d, want > 100", proj.EstimatedInputTokens)
	}
	if proj.BeforeTokens != proj.AfterTokens {
		t.Fatalf("before = %d after = %d, want equal with no elidable results", proj.BeforeTokens, proj.AfterTokens)
	}
	if len(proj.Messages) == 0 {
		t.Fatal("projection returned no messages with the error")
	}
}

func TestProjectMissingSessionStart(t *testing.T) {
	_, err := Project(nil, Limits{})
	if err == nil {
		t.Fatal("Project error = nil, want missing session_start error")
	}
}

func toolMessagesCount(messages []model.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role == model.RoleTool {
			count++
		}
	}
	return count
}

func TestCanonicalJSON(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "sorts nested objects",
			raw:  `{"b":1,"a":[3,{"z":2,"y":1}]}`,
			want: `{"a":[3,{"y":1,"z":2}],"b":1}`,
		},
		{
			name: "ignores whitespace and preserves numeric literals",
			raw:  " \n { \"a\" : 1.0e2 , \"b\" : 2 } \t",
			want: `{"a":1.0e2,"b":2}`,
		},
		{
			name: "preserves array order",
			raw:  `[3,1,2]`,
			want: `[3,1,2]`,
		},
		{
			name: "nested arrays and nulls",
			raw:  `{"x":[true,false,null,"s"]}`,
			want: `{"x":[true,false,null,"s"]}`,
		},
		{
			name:    "duplicate name",
			raw:     `{"a":1,"a":2}`,
			wantErr: true,
		},
		{
			name:    "duplicate escaped name",
			raw:     `{"a":1,"\u0061":2}`,
			wantErr: true,
		},
		{
			name:    "nested duplicate name",
			raw:     `{"a":{"x":1,"x":2}}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			raw:     `{"a":}`,
			wantErr: true,
		},
		{
			name:    "trailing data",
			raw:     `{"a":1} {"b":2}`,
			wantErr: true,
		},
		{
			name:    "empty",
			raw:     ` `,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := canonicalJSON([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("canonicalJSON(%q) = %q, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonicalJSON(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("canonicalJSON(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}

	first, err := canonicalJSON([]byte(`{ "b": 2, "a": 1 }`))
	if err != nil {
		t.Fatalf("canonicalJSON error = %v", err)
	}
	second, err := canonicalJSON([]byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatalf("canonicalJSON error = %v", err)
	}
	if first != second {
		t.Fatalf("whitespace-insensitive identity mismatch: %q != %q", first, second)
	}

	dropped, err := canonicalJSONWithout([]byte(`{"intent":"why","a":1}`), "intent")
	if err != nil {
		t.Fatalf("canonicalJSONWithout error = %v", err)
	}
	if want := `{"a":1}`; dropped != want {
		t.Fatalf("canonicalJSONWithout = %q, want %q", dropped, want)
	}
	nested, err := canonicalJSONWithout([]byte(`{"a":{"intent":"keep"},"intent":"drop"}`), "intent")
	if err != nil {
		t.Fatalf("canonicalJSONWithout nested error = %v", err)
	}
	if want := `{"a":{"intent":"keep"}}`; nested != want {
		t.Fatalf("canonicalJSONWithout nested = %q, want %q", nested, want)
	}
}
