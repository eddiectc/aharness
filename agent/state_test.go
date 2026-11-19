package agent

import (
	"errors"
	"testing"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

func TestStateTotalUsage(t *testing.T) {
	state := State{
		Usage:         model.Usage{InputTokens: 10, OutputTokens: 5},
		SubagentUsage: model.Usage{InputTokens: 3, OutputTokens: 2},
	}
	total := state.TotalUsage()
	if total.InputTokens != 13 || total.OutputTokens != 7 {
		t.Fatalf("TotalUsage = %+v", total)
	}
	if state.Usage.InputTokens != 10 || state.SubagentUsage.InputTokens != 3 {
		t.Fatal("TotalUsage mutated the state")
	}
}

func TestInspectRecordsAppendedPrompt(t *testing.T) {
	events := []session.Event{
		newTestEvent(t, session.TypeSessionStart, 0, "", sessionStartData{
			PromptVersion: PromptVersion, ToolsetVersion: tools.Version, Workspace: "/workspace",
			Provider: "opencode-go", Model: "model-a", Dialect: "openai-chat",
			Limits: testConfig().Limits, SystemPrompt: "recorded prompt", AppendedPrompt: "host text",
		}),
	}
	state, err := Inspect(events)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.AppendedPrompt != "host text" || state.SystemPrompt != "recorded prompt" {
		t.Fatalf("state = %+v", state)
	}

	// A log written before the field reads empty, exactly like prompt_tag.
	legacy, err := Inspect([]session.Event{startEvent(t, "/workspace")})
	if err != nil {
		t.Fatalf("Inspect legacy: %v", err)
	}
	if legacy.AppendedPrompt != "" {
		t.Fatalf("legacy AppendedPrompt = %q, want empty", legacy.AppendedPrompt)
	}
}

func TestSummarizeSessionHead(t *testing.T) {
	events := []session.Event{
		histEvent(t, 1, session.TypeSessionStart, 0, sessionStartData{
			Workspace: "/workspace", Provider: "opencode-go", PromptVersion: PromptVersion,
			ToolsetVersion: tools.Version, Model: "model-a", Dialect: "openai-chat",
		}),
		histEvent(t, 2, session.TypeRunStart, 0, runStartData{Model: "model-b"}),
		histEvent(t, 3, session.TypeUserMessage, 0, userMessageData{Text: "first task", Source: "-task"}),
		histEvent(t, 4, session.TypeUserMessage, 0, userMessageData{Text: "second task", Source: "tui"}),
	}
	summary, err := Summarize(events)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	want := SessionSummary{
		Workspace: "/workspace", Provider: "opencode-go", Model: "model-a", FirstMessage: "first task",
		CreatedAt: events[0].TS,
	}
	if summary != want {
		t.Fatalf("Summarize = %+v, want %+v", summary, want)
	}
}

func TestSummarizeRequiresSessionStart(t *testing.T) {
	_, err := Summarize([]session.Event{
		histEvent(t, 1, session.TypeUserMessage, 0, userMessageData{Text: "task"}),
	})
	if !errors.Is(err, session.ErrCorrupt) {
		t.Fatalf("Summarize error = %v, want ErrCorrupt", err)
	}
}

func TestSummarizeRejectsMalformedPayload(t *testing.T) {
	bad := histEvent(t, 1, session.TypeSessionStart, 0, sessionStartData{})
	bad.Data = []byte(`{"model":3}`)
	_, err := Summarize([]session.Event{bad})
	if !errors.Is(err, session.ErrCorrupt) {
		t.Fatalf("Summarize error = %v, want ErrCorrupt", err)
	}
}
