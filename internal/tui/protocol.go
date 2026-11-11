package tui

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"aharness/internal/model"
)

const (
	protocolVersion = 1
	// Headless frame bounds. They match the -ui-size flag range.
	minUISizeWidth  = 20
	minUISizeHeight = 5
	maxUISizeWidth  = 500
	maxUISizeHeight = 200

	maxCommandLine     = 1 << 20
	maxProtocolText    = 64 << 10
	maxFrameLine       = 1 << 20
	maxSnapshotPayload = 512 << 10
)

// Command is one decoded -ui-json input command.
type Command struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	Key       string `json:"key"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Until     string `json:"until"`
	TimeoutMS int    `json:"timeout_ms"`
	Format    string `json:"format"`
}

func decodeCommand(line []byte) (Command, error) {
	if len(line) > maxCommandLine {
		return Command{}, fmt.Errorf("command line exceeds %d bytes", maxCommandLine)
	}
	var cmd Command
	if err := jsonv2.Unmarshal(line, &cmd); err != nil {
		return Command{}, fmt.Errorf("invalid command JSON: %w", err)
	}
	if cmd.V != protocolVersion {
		return Command{}, fmt.Errorf("unsupported protocol version %d", cmd.V)
	}
	switch cmd.Type {
	case "text", "paste":
		if len(cmd.Text) > maxProtocolText {
			return Command{}, fmt.Errorf("%s text exceeds %d bytes", cmd.Type, maxProtocolText)
		}
	case "key":
		if _, err := protocolKey(cmd.Key); err != nil {
			return Command{}, err
		}
	case "resize":
		if cmd.Width < minUISizeWidth || cmd.Width > maxUISizeWidth ||
			cmd.Height < minUISizeHeight || cmd.Height > maxUISizeHeight {
			return Command{}, fmt.Errorf("resize %dx%d out of range (%dx%d-%dx%d)",
				cmd.Width, cmd.Height, minUISizeWidth, minUISizeHeight, maxUISizeWidth, maxUISizeHeight)
		}
	case "wait":
		switch cmd.Until {
		case "idle", "run_started", "run_finished":
		default:
			return Command{}, fmt.Errorf("unknown wait condition %q", cmd.Until)
		}
		if cmd.TimeoutMS == 0 {
			cmd.TimeoutMS = 30_000
		}
		if cmd.TimeoutMS < 1 || cmd.TimeoutMS > 300_000 {
			return Command{}, fmt.Errorf("wait timeout_ms must be 1-300000, got %d", cmd.TimeoutMS)
		}
	case "frame":
	case "snapshot":
		if cmd.Format == "" {
			cmd.Format = "svg"
		}
		if cmd.Format != "svg" {
			return Command{}, fmt.Errorf("unsupported snapshot format %q", cmd.Format)
		}
	case "quit":
	default:
		return Command{}, fmt.Errorf("unknown command type %q", cmd.Type)
	}
	return cmd, nil
}

var errUnknownKey = errors.New("unknown key")

func protocolKey(name string) (tea.KeyPressMsg, error) {
	code := rune(0)
	mod := tea.KeyMod(0)
	switch name {
	case "enter":
		code = tea.KeyEnter
	case "esc":
		code = tea.KeyEscape
	case "tab":
		code = tea.KeyTab
	case "shift-tab":
		code = tea.KeyTab
		mod = tea.ModShift
	case "up":
		code = tea.KeyUp
	case "down":
		code = tea.KeyDown
	case "left":
		code = tea.KeyLeft
	case "right":
		code = tea.KeyRight
	case "home":
		code = tea.KeyHome
	case "end":
		code = tea.KeyEnd
	case "pgup":
		code = tea.KeyPgUp
	case "pgdown":
		code = tea.KeyPgDown
	case "backspace":
		code = tea.KeyBackspace
	case "delete":
		code = tea.KeyDelete
	case "f1":
		code = tea.KeyF1
	case "f2":
		code = tea.KeyF2
	case "ctrl-c", "ctrl-d", "ctrl-l", "ctrl-u", "ctrl-w", "ctrl-a", "ctrl-e",
		"ctrl-k", "ctrl-p", "ctrl-o", "ctrl-r", "ctrl-t", "ctrl-g", "ctrl-x":
		code = rune(name[len(name)-1])
		mod = tea.ModCtrl
	default:
		return tea.KeyPressMsg{}, fmt.Errorf("%w %q", errUnknownKey, name)
	}
	return tea.KeyPressMsg{Code: code, Mod: mod}, nil
}

// ErrorLine is a protocol error or UI error.
type ErrorLine struct {
	V       int    `json:"v"`
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func errorLine(code, message string) ErrorLine {
	return ErrorLine{V: protocolVersion, Type: "error", Code: code, Message: SanitizeLine(message)}
}

// UsageLine is protocol usage.
type UsageLine struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func usageLine(u model.Usage) UsageLine {
	return UsageLine{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
}

type runStartedLine struct {
	V              int    `json:"v"`
	Type           string `json:"type"`
	Event          string `json:"event"`
	Run            int    `json:"run"`
	Model          string `json:"model"`
	Dialect        string `json:"dialect"`
	ThinkingEffort string `json:"thinking_effort,omitempty"`
}

type runFinishedLine struct {
	V        int       `json:"v"`
	Type     string    `json:"type"`
	Event    string    `json:"event"`
	Status   string    `json:"status"`
	Reason   string    `json:"reason"`
	RunTurns int       `json:"run_turns"`
	RunUsage UsageLine `json:"run_usage"`
	Turns    int       `json:"turns"`
	Usage    UsageLine `json:"usage"`
}

type toolStartedLine struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	Event     string `json:"event"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

type toolFinishedLine struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	Event     string `json:"event"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Code      string `json:"code"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

type modelChangedLine struct {
	V              int    `json:"v"`
	Type           string `json:"type"`
	Event          string `json:"event"`
	Model          string `json:"model"`
	Dialect        string `json:"dialect"`
	ThinkingEffort string `json:"thinking_effort,omitempty"`
}

type noticeLine struct {
	V     int    `json:"v"`
	Type  string `json:"type"`
	Event string `json:"event"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
}

type eventErrorLine struct {
	V       int    `json:"v"`
	Type    string `json:"type"`
	Event   string `json:"event"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// encodeEvent converts one semantic app event to its protocol JSON line.
func encodeEvent(ev any) ([]byte, error) {
	var out any
	switch ev := ev.(type) {
	case EventRunStarted:
		out = runStartedLine{
			V: protocolVersion, Type: "event", Event: "run_started",
			Run: ev.Run, Model: SanitizeLine(ev.Model), Dialect: SanitizeLine(ev.Dialect),
			ThinkingEffort: SanitizeLine(ev.ThinkingEffort),
		}
	case EventRunFinished:
		out = runFinishedLine{
			V: protocolVersion, Type: "event", Event: "run_finished",
			Status: string(ev.Status), Reason: SanitizeLine(ev.Reason),
			RunTurns: ev.RunTurns, RunUsage: usageLine(ev.RunUsage),
			Turns: ev.Turns, Usage: usageLine(ev.Usage),
		}
	case EventToolStarted:
		out = toolStartedLine{
			V: protocolVersion, Type: "event", Event: "tool_started",
			CallID: SanitizeLine(ev.CallID), Name: SanitizeLine(ev.Name),
			Arguments: protocolArguments(ev.Arguments),
		}
	case EventToolFinished:
		out = toolFinishedLine{
			V: protocolVersion, Type: "event", Event: "tool_finished",
			CallID: SanitizeLine(ev.CallID), Name: SanitizeLine(ev.Name),
			OK: ev.OK, Code: SanitizeLine(ev.Code), ExitCode: ev.ExitCode,
			Truncated: ev.Truncated, TimedOut: ev.TimedOut,
		}
	case EventModelChanged:
		out = modelChangedLine{
			V: protocolVersion, Type: "event", Event: "model_changed",
			Model: SanitizeLine(ev.Model), Dialect: SanitizeLine(ev.Dialect),
			ThinkingEffort: SanitizeLine(ev.ThinkingEffort),
		}
	case EventNotice:
		out = noticeLine{
			V: protocolVersion, Type: "event", Event: "notice",
			Kind: SanitizeLine(ev.Kind), Text: SanitizeLine(ev.Text),
		}
	case EventError:
		out = eventErrorLine{
			V: protocolVersion, Type: "event", Event: "error",
			Code: SanitizeLine(ev.Code), Message: SanitizeLine(ev.Message),
		}
	default:
		return nil, fmt.Errorf("unknown event %T", ev)
	}
	return jsonv2.Marshal(out)
}

func protocolArguments(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	return CapText(SanitizeLine(trimmed), 2<<10)
}

// SnapshotLine is one deterministic SVG snapshot.
type SnapshotLine struct {
	V      int    `json:"v"`
	Type   string `json:"type"`
	Seq    int    `json:"seq"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Format string `json:"format"`
	SVG    string `json:"svg"`
}

// encodeFrame marshals a frame under the protocol line cap.
func encodeFrame(frame Frame) ([]byte, bool) {
	data, err := jsonv2.Marshal(frame)
	if err != nil {
		return nil, false
	}
	if len(data) > maxFrameLine {
		return nil, false
	}
	return data, true
}

// encodeSnapshot marshals a snapshot under the protocol and SVG caps.
func encodeSnapshot(snapshot SnapshotLine) ([]byte, bool) {
	if len(snapshot.SVG) > maxSnapshotPayload {
		return nil, false
	}
	data, err := jsonv2.Marshal(snapshot)
	if err != nil || len(data) > maxFrameLine {
		return nil, false
	}
	return data, true
}
