package aharness_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eddiectc/aharness"
)

func TestViewEventKindsAndJSON(t *testing.T) {
	meta := aharness.EventMeta{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Session: "s"}
	exit := 2
	events := []struct {
		event aharness.Event
		kind  string
	}{
		{aharness.RunStarted{EventMeta: meta, RunID: "r", Turn: 1}, "run_started"},
		{aharness.TextDelta{EventMeta: meta, Text: "delta"}, "text_delta"},
		{aharness.ReasoningDelta{EventMeta: meta, Text: "thinking"}, "reasoning_delta"},
		{aharness.ResponseCompleted{EventMeta: meta, Turn: 1, Text: "done", Usage: aharness.Usage{InputTokens: 1}}, "response_completed"},
		{aharness.ToolStarted{EventMeta: meta, Turn: 1, Call: aharness.ToolCall{ID: "c", Name: "read_file"}}, "tool_started"},
		{aharness.ToolCompleted{EventMeta: meta, Turn: 1, CallID: "c", Name: "read_file", OK: true, Code: "ok", ExitCode: exit}, "tool_completed"},
		{aharness.MessageRecorded{EventMeta: meta, Turn: 1, ID: 3, Source: "steer", Text: "more"}, "message_recorded"},
		{aharness.SubagentMessageRecorded{EventMeta: meta, Turn: 1, CallID: "c", Index: 0, Agent: "explore", ChildSession: "child", ID: 4, Source: "subagent-steer", Text: "focus"}, "subagent_message_recorded"},
		{aharness.Notice{EventMeta: meta, Turn: 1, Kind: aharness.NoticeWarning, Text: "careful"}, "notice"},
		{aharness.SubagentStarted{EventMeta: meta, Turn: 1, CallID: "c", Index: 0, Agent: "explore", ChildSession: "child"}, "subagent_started"},
		{aharness.SubagentProgress{EventMeta: meta, Turn: 1, CallID: "c", Index: 0, Agent: "explore", ChildSession: "child"}, "subagent_progress"},
		{aharness.SubagentFinished{EventMeta: meta, Turn: 1, CallID: "c", Index: 0, Agent: "explore", ChildSession: "child", Status: aharness.SubagentCompleted}, "subagent_finished"},
		{aharness.RunFinished{EventMeta: meta, RunID: "r", Result: aharness.Result{Status: aharness.StopCompleted}}, "run_finished"},
	}
	seen := map[string]bool{}
	for _, tc := range events {
		view := aharness.ViewEvent(tc.event)
		if view.Kind != tc.kind {
			t.Errorf("ViewEvent(%T).Kind = %q, want %q", tc.event, view.Kind, tc.kind)
		}
		if view.Time.IsZero() || view.Session != "s" {
			t.Errorf("ViewEvent(%T) meta = %v/%q", tc.event, view.Time, view.Session)
		}
		data, err := json.Marshal(view)
		if err != nil {
			t.Fatalf("Marshal(%T): %v", tc.event, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal(%T): %v", tc.event, err)
		}
		if decoded["kind"] != tc.kind {
			t.Errorf("JSON kind for %T = %v, want %s", tc.event, decoded["kind"], tc.kind)
		}
		if decoded["time"] != "2026-01-02T03:04:05Z" {
			t.Errorf("JSON time for %T = %v", tc.event, decoded["time"])
		}
		seen[tc.kind] = true
	}
	if len(seen) != 13 {
		t.Fatalf("kinds covered = %d, want 13", len(seen))
	}
}

func TestViewEventStableShape(t *testing.T) {
	meta := aharness.EventMeta{Time: time.Unix(0, 0).UTC(), Session: "s"}
	view := aharness.ViewEvent(aharness.TextDelta{EventMeta: meta, Text: "hello"})
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"kind":"text_delta","time":"1970-01-01T00:00:00Z","session":"s","text":"hello"}`
	if string(data) != want {
		t.Fatalf("JSON = %s, want %s", data, want)
	}
	// A notice carries the notice class under a distinct key so Kind stays the
	// event discriminator.
	notice := aharness.ViewEvent(aharness.Notice{EventMeta: meta, Kind: aharness.NoticeError, Text: "boom"})
	data, err = json.Marshal(notice)
	if err != nil {
		t.Fatalf("Marshal notice: %v", err)
	}
	want = `{"kind":"notice","time":"1970-01-01T00:00:00Z","session":"s","text":"boom","notice_kind":"error"}`
	if string(data) != want {
		t.Fatalf("notice JSON = %s, want %s", data, want)
	}
}
