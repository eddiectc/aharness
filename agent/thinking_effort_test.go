package agent

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

func thinkingStartEvent(t *testing.T, effort string) session.Event {
	t.Helper()
	return newTestEvent(t, session.TypeSessionStart, 0, "", sessionStartData{
		PromptVersion:  PromptVersion,
		ToolsetVersion: tools.Version,
		Workspace:      "/w",
		Provider:       "opencode-go",
		Model:          "test-model",
		Dialect:        "openai-chat",
		ThinkingEffort: effort,
	})
}

func TestRunCopiesThinkingEffortIntoRequestAndLog(t *testing.T) {
	cfg := testConfig()
	cfg.ThinkingEffort = "high"
	cfg.LogRequests = true
	var started RunStarted
	cfg.Display = func(ev DisplayEvent) error {
		if rs, ok := ev.(RunStarted); ok {
			started = rs
		}
		return nil
	}
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{Text: "done", FinishReason: model.FinishStop}}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %q, want %q", res.Status, StopCompleted)
	}
	if len(comp.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(comp.requests))
	}
	if got := comp.requests[0].ThinkingEffort; got != "high" {
		t.Errorf("request ThinkingEffort = %q, want high", got)
	}

	start := decodePayload[sessionStartData](t, eventsByType(store, session.TypeSessionStart)[0])
	if start.ThinkingEffort != "high" {
		t.Errorf("session_start thinking_effort = %q, want high", start.ThinkingEffort)
	}
	runStart := decodePayload[runStartData](t, eventsByType(store, session.TypeRunStart)[0])
	if runStart.ThinkingEffort != "high" {
		t.Errorf("run_start thinking_effort = %q, want high", runStart.ThinkingEffort)
	}
	requests := eventsByType(store, session.TypeModelRequest)
	if len(requests) != 1 {
		t.Fatalf("model_request events = %d, want 1", len(requests))
	}
	logged := decodePayload[modelRequestData](t, requests[0])
	if logged.ThinkingEffort != "high" || logged.Truncated {
		t.Errorf("model_request = %+v, want thinking_effort high and not truncated", logged)
	}
	if started.ThinkingEffort != "high" {
		t.Errorf("RunStarted.ThinkingEffort = %q, want high", started.ThinkingEffort)
	}
}

func TestRecordModelRequestTruncatedKeepsThinkingEffort(t *testing.T) {
	cfg := testConfig()
	cfg.LogRequests = true
	store := &fakeStore{}
	runner := newRunner(cfg, &fakeCompleter{}, &fakeToolset{}, store)

	messages := make([]model.Message, 0, 17)
	for range 17 {
		messages = append(messages, model.Message{Role: model.RoleUser, Content: exactOutput(maxEventTextBytes)})
	}
	req := model.Request{Model: "test-model", Messages: messages, ThinkingEffort: "max"}
	if err := runner.recordModelRequest(req, 1, 1); err != nil {
		t.Fatalf("recordModelRequest: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1", len(store.events))
	}
	data := decodePayload[modelRequestData](t, store.events[0])
	if data.ThinkingEffort != "max" {
		t.Errorf("truncated thinking_effort = %q, want max", data.ThinkingEffort)
	}
	if !data.Truncated {
		t.Error("Truncated = false, want true")
	}
	if data.Messages != nil {
		t.Errorf("truncated payload retained %d messages", len(data.Messages))
	}
}

func TestInspectRestoresThinkingEffort(t *testing.T) {
	tests := []struct {
		name   string
		events []session.Event
		want   string
	}{
		{
			name: "session start only",
			events: []session.Event{
				thinkingStartEvent(t, "low"),
			},
			want: "low",
		},
		{
			name: "latest run start wins",
			events: []session.Event{
				thinkingStartEvent(t, "low"),
				newTestEvent(t, session.TypeRunStart, 0, "", runStartData{Model: "test-model", PromptVersion: PromptVersion, ToolsetVersion: tools.Version, ThinkingEffort: "high"}),
				newTestEvent(t, session.TypeRunStart, 0, "", runStartData{Model: "test-model", PromptVersion: PromptVersion, ToolsetVersion: tools.Version, ThinkingEffort: "medium"}),
			},
			want: "medium",
		},
		{
			name: "later empty run start clears",
			events: []session.Event{
				thinkingStartEvent(t, "low"),
				newTestEvent(t, session.TypeRunStart, 0, "", runStartData{Model: "test-model", PromptVersion: PromptVersion, ToolsetVersion: tools.Version, ThinkingEffort: "high"}),
				newTestEvent(t, session.TypeRunStart, 0, "", runStartData{Model: "test-model", PromptVersion: PromptVersion, ToolsetVersion: tools.Version}),
			},
			want: "",
		},
		{
			name: "old log without the field",
			events: []session.Event{
				thinkingStartEvent(t, ""),
				newTestEvent(t, session.TypeRunStart, 0, "", runStartData{Model: "test-model", PromptVersion: PromptVersion, ToolsetVersion: tools.Version}),
			},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, err := Inspect(tc.events)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if state.ThinkingEffort != tc.want {
				t.Errorf("ThinkingEffort = %q, want %q", state.ThinkingEffort, tc.want)
			}
		})
	}
}

func TestRunRetryReusesThinkingEffort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig()
		cfg.ThinkingEffort = "max"
		cfg.LogRequests = true
		comp := &fakeCompleter{steps: []scriptStep{
			{err: &model.HTTPError{StatusCode: 429, Status: "429 Too Many Requests", Body: "slow down", RetryAfter: time.Second}},
			{resp: model.Response{Text: "recovered", FinishReason: model.FinishStop}},
		}}
		store := &fakeStore{}
		runner := newRunner(cfg, comp, &fakeToolset{}, store)
		startTestSession(t, runner)
		runner.jitter = func(d time.Duration) time.Duration { return d }
		outcome := make(chan runOutcome, 1)
		go func() {
			res, err := runner.Run(t.Context(), defaultRunOptions())
			outcome <- runOutcome{res: res, err: err}
		}()
		got := <-outcome
		if got.err != nil {
			t.Fatalf("Run error = %v, want nil", got.err)
		}
		if got.res.Status != StopCompleted {
			t.Fatalf("status = %q, want %q", got.res.Status, StopCompleted)
		}
		if len(comp.requests) != 2 {
			t.Fatalf("model calls = %d, want 2", len(comp.requests))
		}
		for i, req := range comp.requests {
			if req.ThinkingEffort != "max" {
				t.Errorf("request %d ThinkingEffort = %q, want max", i, req.ThinkingEffort)
			}
		}
		requests := eventsByType(store, session.TypeModelRequest)
		if len(requests) != 1 {
			t.Fatalf("model_request events = %d, want 1 (retries reuse the request)", len(requests))
		}
		if data := decodePayload[modelRequestData](t, requests[0]); data.ThinkingEffort != "max" {
			t.Errorf("logged thinking_effort = %q, want max", data.ThinkingEffort)
		}
	})
}

func TestRunToolLoopReusesThinkingEffort(t *testing.T) {
	cfg := testConfig()
	cfg.ThinkingEffort = "high"
	cfg.LogRequests = true
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("a", "read_file", `{"path":"a.txt"}`)},
			FinishReason: model.FinishToolCalls,
		}},
		{resp: model.Response{Text: "done", FinishReason: model.FinishStop}},
	}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %q, want %q", res.Status, StopCompleted)
	}
	if len(comp.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(comp.requests))
	}
	for i, req := range comp.requests {
		if req.ThinkingEffort != "high" {
			t.Errorf("request %d ThinkingEffort = %q, want high", i, req.ThinkingEffort)
		}
	}
	requests := eventsByType(store, session.TypeModelRequest)
	if len(requests) != 2 {
		t.Fatalf("model_request events = %d, want 2", len(requests))
	}
	for i, ev := range requests {
		if data := decodePayload[modelRequestData](t, ev); data.ThinkingEffort != "high" {
			t.Errorf("request %d logged thinking_effort = %q, want high", i, data.ThinkingEffort)
		}
	}
}

func TestRecordModelRequestOmitsEmptyThinkingEffort(t *testing.T) {
	cfg := testConfig()
	cfg.LogRequests = true
	store := &fakeStore{}
	runner := newRunner(cfg, &fakeCompleter{}, &fakeToolset{}, store)
	if err := runner.recordModelRequest(model.Request{Model: "test-model", Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}}, 1, 1); err != nil {
		t.Fatalf("recordModelRequest: %v", err)
	}
	if got := strings.Contains(string(store.events[0].Data), "thinking_effort"); got {
		t.Errorf("model_request payload = %s, want thinking_effort omitted", store.events[0].Data)
	}
}
