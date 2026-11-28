package agent

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

func TestStateTotalUsage(t *testing.T) {
	state := State{
		Usage: model.Usage{
			InputTokens: 10, OutputTokens: 5,
			CachedInputTokens: 4, CacheWriteInputTokens: 2,
			ReasoningOutputTokens: 1, Reported: true,
		},
		SubagentUsage: model.Usage{
			InputTokens: 3, OutputTokens: 2,
			CachedInputTokens: 1, CacheWriteInputTokens: 1,
			ReasoningOutputTokens: 1, Reported: true,
		},
	}
	want := model.Usage{
		InputTokens: 13, OutputTokens: 7,
		CachedInputTokens: 5, CacheWriteInputTokens: 3,
		ReasoningOutputTokens: 2, Reported: true,
	}
	if total := state.TotalUsage(); total != want {
		t.Fatalf("TotalUsage = %+v, want %+v", total, want)
	}
	if state.Usage.InputTokens != 10 || state.SubagentUsage.InputTokens != 3 {
		t.Fatal("TotalUsage mutated the state")
	}
}

func TestInspectFoldsEveryUsageField(t *testing.T) {
	first := model.Usage{
		InputTokens: 1000, OutputTokens: 30,
		CachedInputTokens: 800, CacheWriteInputTokens: 120,
		ReasoningOutputTokens: 20, Reported: true,
	}
	second := model.Usage{
		InputTokens: 40, OutputTokens: 10,
		CachedInputTokens: 10, CacheWriteInputTokens: 5,
		ReasoningOutputTokens: 4, Reported: true,
	}
	child := model.Usage{
		InputTokens: 25, OutputTokens: 8,
		CachedInputTokens: 5, CacheWriteInputTokens: 3,
		ReasoningOutputTokens: 2, Reported: true,
	}
	events := []session.Event{
		startEvent(t, "/workspace"),
		newTestEvent(t, session.TypeModelResponse, 1, string(model.RoleAssistant), modelResponseData{Usage: first}),
		newTestEvent(t, session.TypeModelResponse, 2, string(model.RoleAssistant), modelResponseData{Usage: second}),
		newTestEvent(t, session.TypeToolResult, 2, string(model.RoleTool), toolResultData{
			CallID: "d1", Name: "delegate", OK: true, Code: tools.CodeOK,
			Details: json.RawMessage(`{"tasks":[{"agent":"explore","status":"completed","usage":{"input_tokens":25,"output_tokens":8,"cached_input_tokens":5,"cache_write_input_tokens":3,"reasoning_output_tokens":2,"reported":true}}],"status":"completed","subagent_turns":1,"subagent_usage":{"input_tokens":25,"output_tokens":8,"cached_input_tokens":5,"cache_write_input_tokens":3,"reasoning_output_tokens":2,"reported":true},"subagent_cost_complete":true}`),
		}),
	}
	state, err := Inspect(events)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	wantUsage := model.Usage{
		InputTokens: 1040, OutputTokens: 40,
		CachedInputTokens: 810, CacheWriteInputTokens: 125,
		ReasoningOutputTokens: 24, Reported: true,
	}
	if state.Usage != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", state.Usage, wantUsage)
	}
	if state.SubagentUsage != child {
		t.Fatalf("SubagentUsage = %+v, want %+v", state.SubagentUsage, child)
	}
	wantTotal := model.Usage{
		InputTokens: 1065, OutputTokens: 48,
		CachedInputTokens: 815, CacheWriteInputTokens: 128,
		ReasoningOutputTokens: 26, Reported: true,
	}
	if total := state.TotalUsage(); total != wantTotal {
		t.Fatalf("TotalUsage = %+v, want %+v", total, wantTotal)
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

// startSessionWithPrompt records a session through the real writer, so the
// PromptCapped tests exercise the path the flag describes.
func startSessionWithPrompt(t *testing.T, prompt string) []session.Event {
	t.Helper()
	cfg := testConfig()
	cfg.SystemPrompt = prompt
	store := &fakeStore{}
	if err := WriteSessionStart(store, cfg, nil); err != nil {
		t.Fatalf("WriteSessionStart: %v", err)
	}
	return store.Events()
}

func TestInspectPromptCapped(t *testing.T) {
	// 40,000 bytes against the 32 KiB cap drops 7,232 bytes.
	const droppedBytes = 7232
	events := startSessionWithPrompt(t, exactOutput(maxSystemPromptBytes+droppedBytes))
	recorded := decodePayload[sessionStartData](t, events[0]).SystemPrompt
	wantRecorded := exactOutput(maxSystemPromptBytes) + "\n[truncated 7232 bytes]"
	if recorded != wantRecorded {
		t.Fatalf("recorded prompt = %d bytes ending %q, want %d bytes with the pinned marker",
			len(recorded), recorded[max(0, len(recorded)-40):], len(wantRecorded))
	}
	state, err := Inspect(events)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !state.PromptCapped {
		t.Fatal("PromptCapped = false for a prompt truncated by WriteSessionStart")
	}

	// The derived flag survives unrelated later events.
	later := append(events,
		newTestEvent(t, session.TypeRunStart, 0, "", runStartData{
			Model: "test-model", PromptVersion: PromptVersion, ToolsetVersion: tools.Version,
		}),
		newTestEvent(t, session.TypeModelResponse, 1, "", modelResponseData{}),
	)
	state, err = Inspect(later)
	if err != nil {
		t.Fatalf("Inspect with later events: %v", err)
	}
	if !state.PromptCapped {
		t.Fatal("PromptCapped = false after unrelated later events")
	}
}

func TestInspectPromptCappedEdges(t *testing.T) {
	tests := []struct {
		name   string
		events func(*testing.T) []session.Event
	}{
		{
			name: "prompt under the cap",
			events: func(t *testing.T) []session.Event {
				return startSessionWithPrompt(t, exactOutput(maxSystemPromptBytes-1))
			},
		},
		{
			name: "prompt exactly at the cap",
			events: func(t *testing.T) []session.Event {
				return startSessionWithPrompt(t, exactOutput(maxSystemPromptBytes))
			},
		},
		{
			name: "legacy log with an empty recorded prompt",
			events: func(t *testing.T) []session.Event {
				return []session.Event{startEvent(t, "/workspace")}
			},
		},
		{
			name: "hand-written marker over the wrong prefix length",
			events: func(t *testing.T) []session.Event {
				// 32,764 bytes is below the [limit-3, limit] window, so this is
				// not something capText can produce.
				prompt := exactOutput(maxSystemPromptBytes-4) + "\n[truncated 500 bytes]"
				return []session.Event{newTestEvent(t, session.TypeSessionStart, 0, "", sessionStartData{
					PromptVersion: PromptVersion, ToolsetVersion: tools.Version, Workspace: "/workspace",
					Provider: "opencode-go", Model: "test-model", Dialect: "openai-chat",
					Limits: testConfig().Limits, SystemPrompt: prompt,
				})}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, err := Inspect(tc.events(t))
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if state.PromptCapped {
				t.Fatalf("PromptCapped = true for a %d-byte prompt, want false", len(state.SystemPrompt))
			}
		})
	}
}

func TestTruncatedBy(t *testing.T) {
	limit := maxSystemPromptBytes
	marker := func(dropped int) string {
		return "\n[truncated " + strconv.Itoa(dropped) + " bytes]"
	}
	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "capText output",
			text: capText(exactOutput(limit+1), limit),
			want: true,
		},
		{
			name: "capText output cut inside a rune",
			text: capText(exactOutput(limit-1)+strings.Repeat("é", 10), limit),
			want: true,
		},
		{
			name: "prefix three bytes short of the cap",
			text: exactOutput(limit-3) + marker(3),
			want: true,
		},
		{
			name: "prefix four bytes short of the cap",
			text: exactOutput(limit-4) + marker(4),
			want: false,
		},
		{
			name: "prefix one byte over the cap",
			text: exactOutput(limit+1) + marker(1),
			want: false,
		},
		{
			name: "zero dropped bytes",
			text: exactOutput(limit) + marker(0),
			want: false,
		},
		{
			name: "negative dropped bytes",
			text: exactOutput(limit) + marker(-1),
			want: false,
		},
		{
			name: "no marker",
			text: exactOutput(limit + 10),
			want: false,
		},
		{
			name: "exactly at the cap",
			text: exactOutput(limit),
			want: false,
		},
		{
			name: "empty text",
			text: "",
			want: false,
		},
		{
			name: "invalid UTF-8 prefix",
			text: exactOutput(limit-1) + "\xff" + marker(10),
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncatedBy(tc.text, limit); got != tc.want {
				t.Fatalf("truncatedBy(%d-byte text, %d) = %t, want %t", len(tc.text), limit, got, tc.want)
			}
		})
	}
}
