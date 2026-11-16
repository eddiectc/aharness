package agent

import (
	"errors"
	"testing"

	"github.com/eddiectc/aharness/internal/session"
	"github.com/eddiectc/aharness/internal/tools"
)

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
