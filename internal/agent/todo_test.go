package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"slices"
	"testing"

	"aharness/internal/session"
	"aharness/internal/tools"
)

func todoResultEvent(t *testing.T, name string, ok bool, items []tools.TodoItem) session.Event {
	t.Helper()
	var details json.RawMessage
	if items != nil {
		raw, err := jsonv2.Marshal(struct {
			Items []tools.TodoItem `json:"items"`
		}{Items: items})
		if err != nil {
			t.Fatalf("marshal todo details: %v", err)
		}
		details = raw
	}
	return newTestEvent(t, session.TypeToolResult, 1, "tool", toolResultData{
		CallID:  "call-todo",
		Name:    name,
		OK:      ok,
		Details: details,
	})
}

func TestRestoreTodo(t *testing.T) {
	set := []tools.TodoItem{
		{Content: "read the docs", Status: tools.TodoCompleted},
		{Content: "write the tool", Status: tools.TodoInProgress},
	}
	later := []tools.TodoItem{
		{Content: "read the docs", Status: tools.TodoCompleted},
		{Content: "write the tool", Status: tools.TodoCompleted},
		{Content: "run the tests", Status: tools.TodoPending},
	}

	tests := []struct {
		name   string
		events []session.Event
		want   []tools.TodoItem
	}{
		{
			name:   "no events",
			events: nil,
			want:   nil,
		},
		{
			name:   "no todo result",
			events: []session.Event{newTestEvent(t, session.TypeUserMessage, 0, "user", userMessageData{Text: "task"})},
			want:   nil,
		},
		{
			name:   "set list",
			events: []session.Event{todoResultEvent(t, "todo", true, set)},
			want:   set,
		},
		{
			name:   "last write wins",
			events: []session.Event{todoResultEvent(t, "todo", true, set), todoResultEvent(t, "todo", true, later)},
			want:   later,
		},
		{
			name:   "failed result is ignored",
			events: []session.Event{todoResultEvent(t, "todo", true, set), todoResultEvent(t, "todo", false, nil)},
			want:   set,
		},
		{
			name:   "other tool details are ignored",
			events: []session.Event{todoResultEvent(t, "todo", true, set), todoResultEvent(t, "web_search", true, later)},
			want:   set,
		},
		{
			name:   "clear wins",
			events: []session.Event{todoResultEvent(t, "todo", true, set), todoResultEvent(t, "todo", true, []tools.TodoItem{})},
			want:   []tools.TodoItem{},
		},
		{
			name: "malformed details fall back to the previous result",
			events: []session.Event{
				todoResultEvent(t, "todo", true, set),
				newTestEvent(t, session.TypeToolResult, 2, "tool", toolResultData{
					CallID: "call-bad", Name: "todo", OK: true, Details: json.RawMessage(`{"items":"nope"}`),
				}),
			},
			want: set,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RestoreTodo(tc.events)
			if !slices.Equal(got, tc.want) {
				t.Errorf("RestoreTodo() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
