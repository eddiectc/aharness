package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

// seededStore returns a fake store with durable events already assigned
// envelope fields, as if they came from a previous process.
func seededStore(t *testing.T, events ...session.Event) *fakeStore {
	t.Helper()
	store := &fakeStore{}
	for i, ev := range events {
		ev.V = session.Version
		ev.Seq = uint64(i + 1)
		ev.Session = "test-session"
		if ev.TS.IsZero() {
			ev.TS = time.Unix(int64(i+1), 0).UTC()
		}
		store.events = append(store.events, ev)
	}
	return store
}

func testRunStart(t *testing.T) session.Event {
	t.Helper()
	cfg := testConfig()
	return newTestEvent(t, session.TypeRunStart, 0, "", runStartData{
		RunID:            "run-1",
		Model:            cfg.Model,
		Dialect:          cfg.Dialect,
		BaseURL:          cfg.BaseURL,
		Limits:           cfg.Limits,
		MaxTurns:         cfg.MaxTurns,
		MaxTotalTokens:   cfg.MaxTotalTokens,
		SessionTimeoutMS: cfg.SessionTimeout.Milliseconds(),
		ModelTimeoutMS:   cfg.ModelTimeout.Milliseconds(),
		PromptVersion:    PromptVersion,
		ToolsetVersion:   tools.Version,
		HarnessVersion:   "test-harness",
	})
}

func testResponseEvent(t *testing.T, turn int, text string, usage model.Usage, calls ...model.ToolCall) session.Event {
	t.Helper()
	return newTestEvent(t, session.TypeModelResponse, turn, "assistant", modelResponseData{
		Text:         text,
		ToolCalls:    loggableToolCalls(calls),
		FinishReason: model.FinishToolCalls,
		Usage:        usage,
	})
}

func testRunEnd(t *testing.T, status StopReason, turns int, usage model.Usage) session.Event {
	t.Helper()
	return newTestEvent(t, session.TypeRunEnd, turns, "", runEndData{
		Status:   status,
		Reason:   "prior run",
		RunTurns: turns,
		RunUsage: usage,
		Turns:    turns,
		Usage:    usage,
	})
}

func TestInspectReconstructsCumulativeStateAndPending(t *testing.T) {
	events := []session.Event{
		startEvent(t, "/tmp/workspace"),
		testRunStart(t),
		newTestEvent(t, session.TypeUserMessage, 0, "user", userMessageData{Text: "task", Source: "-task"}),
		testResponseEvent(t, 1, "using tools", model.Usage{InputTokens: 10, OutputTokens: 4},
			toolCall("a", "read_file", `{"path":"a"}`),
			toolCall("b", "search", `{"pattern":"x"}`)),
		newTestEvent(t, session.TypeToolResult, 1, "tool", toolResultData{CallID: "a", Name: "read_file", OK: true, Code: tools.CodeOK, Output: "ok"}),
		testRunEnd(t, StopInterrupted, 1, model.Usage{InputTokens: 10, OutputTokens: 4}),
	}
	store := seededStore(t, events...)

	state, err := Inspect(store.Events())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.Turns != 1 || state.Usage.Total() != 14 {
		t.Fatalf("turns/usage = %d/%d", state.Turns, state.Usage.Total())
	}
	if !state.LastRunEnded || state.LastRunStatus != StopInterrupted {
		t.Fatalf("last run = ended %v status %q", state.LastRunEnded, state.LastRunStatus)
	}
	if len(state.Pending) != 1 || state.Pending[0].CallID != "b" || state.Pending[0].Turn != 1 {
		t.Fatalf("pending = %+v", state.Pending)
	}
	if state.LastEventSeq != 6 || state.PromptVersion != PromptVersion || state.ToolsetVersion != tools.Version {
		t.Fatalf("state = %+v", state)
	}
	if state.MaxTurns != 10 || state.MaxTotalTokens != 1_000_000 ||
		state.SessionTimeout != time.Hour || state.ModelTimeout != 10*time.Second {
		t.Fatalf("recorded budgets = %d/%d/%s/%s, want the historical values",
			state.MaxTurns, state.MaxTotalTokens, state.SessionTimeout, state.ModelTimeout)
	}
}

func TestInspectCrashHasNoEndedRun(t *testing.T) {
	store := seededStore(t,
		startEvent(t, "/tmp/workspace"),
		testRunStart(t),
	)
	state, err := Inspect(store.Events())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.LastRunEnded {
		t.Fatal("LastRunEnded = true, want false for a crash")
	}
}

func TestRunnerRepairsPendingBeforeRunStart(t *testing.T) {
	cfg := testConfig()
	store := seededStore(t,
		startEvent(t, cfg.Workspace),
		testRunStart(t),
		newTestEvent(t, session.TypeUserMessage, 0, "user", userMessageData{Text: "task", Source: "-task"}),
		testResponseEvent(t, 1, "using tools", model.Usage{InputTokens: 10, OutputTokens: 4},
			toolCall("unpaired", "edit_file", `{"path":"x"}`)),
	)
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		Text: "done", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 3, OutputTokens: 2},
	}}}}
	runner := newRunner(cfg, comp, &fakeToolset{}, store)

	res, err := runner.Run(t.Context(), RunOptions{TaskSet: false, ResumedFromSeq: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopCompleted || res.FinalText != "done" {
		t.Fatalf("result = %+v", res)
	}
	if res.RunTurns != 1 || res.CumulativeTurns != 2 {
		t.Fatalf("turns = run %d cumulative %d", res.RunTurns, res.CumulativeTurns)
	}

	// The repair result must precede the new run_start.
	if got := store.events[4].Type; got != session.TypeToolResult {
		t.Fatalf("event[4].type = %q, want tool_result repair", got)
	}
	repair := decodePayload[toolResultData](t, store.events[4])
	if repair.CallID != "unpaired" || repair.OK || repair.Code != tools.CodeCanceled {
		t.Fatalf("repair = %+v", repair)
	}
	if !strings.Contains(repair.Error, "restarted") || !strings.Contains(repair.Error, "may or may not") {
		t.Fatalf("repair message = %q", repair.Error)
	}
	if got := store.events[5].Type; got != session.TypeRunStart {
		t.Fatalf("event[5].type = %q, want run_start", got)
	}
	start := decodePayload[runStartData](t, store.events[5])
	if start.ResumedFromSeq != 4 {
		t.Fatalf("resumed_from_seq = %d, want 4", start.ResumedFromSeq)
	}

	// The next model request must pair the original call with the repair.
	req := comp.requests[0]
	foundCall := false
	foundResult := false
	for _, msg := range req.Messages {
		if msg.Role == model.RoleAssistant {
			for _, call := range msg.ToolCalls {
				if call.ID == "unpaired" {
					foundCall = true
				}
			}
		}
		if msg.Role == model.RoleTool && msg.ToolCallID == "unpaired" && strings.Contains(msg.Content, "may or may not") {
			foundResult = true
		}
	}
	if !foundCall || !foundResult {
		t.Fatalf("repaired projection missing call/result: %+v", req.Messages)
	}
}

func TestRunnerCumulativeTokenLimit(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTotalTokens = 15
	usage := model.Usage{InputTokens: 10, OutputTokens: 10}
	store := seededStore(t,
		startEvent(t, cfg.Workspace),
		testRunStart(t),
		testResponseEvent(t, 1, "old", usage),
		testRunEnd(t, StopCompleted, 1, usage),
	)
	comp := &fakeCompleter{}
	runner := newRunner(cfg, comp, &fakeToolset{}, store)
	res, err := runner.Run(t.Context(), RunOptions{TaskSet: false, ResumedFromSeq: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopTokenLimit {
		t.Fatalf("status = %q, want token_limit", res.Status)
	}
	if comp.calls != 0 {
		t.Fatalf("model calls = %d, want 0", comp.calls)
	}
	end := decodePayload[runEndData](t, eventsByType(store, session.TypeRunEnd)[1])
	if end.Turns != 1 || end.Usage.Total() != 20 || end.RunTurns != 0 {
		t.Fatalf("run_end = %+v", end)
	}
}

func TestRunnerResumeOutputIsRunLocal(t *testing.T) {
	cfg := testConfig()
	oldUsage := model.Usage{InputTokens: 4, OutputTokens: 2}
	store := seededStore(t,
		startEvent(t, cfg.Workspace),
		testRunStart(t),
		newTestEvent(t, session.TypeUserMessage, 0, "user", userMessageData{Text: "old task", Source: "-task"}),
		testResponseEvent(t, 1, "old answer", oldUsage),
		newTestEvent(t, session.TypeFinal, 1, "", finalData{Status: StopCompleted, Text: "old answer"}),
		testRunEnd(t, StopCompleted, 1, oldUsage),
	)
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		Text: "new answer", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 6, OutputTokens: 3},
	}}}}
	runner := newRunner(cfg, comp, &fakeToolset{}, store)
	res, err := runner.Run(t.Context(), RunOptions{Task: "new task", TaskSet: true, TaskFrom: "-task", ResumedFromSeq: 6})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopCompleted || res.FinalText != "new answer" {
		t.Fatalf("result = %+v", res)
	}
	if res.RunTurns != 1 || res.CumulativeTurns != 2 {
		t.Fatalf("turns = run %d cumulative %d", res.RunTurns, res.CumulativeTurns)
	}
	if res.RunUsage.Total() != 9 || res.CumulativeUsage.Total() != 15 {
		t.Fatalf("usage = run %d cumulative %d", res.RunUsage.Total(), res.CumulativeUsage.Total())
	}
	final := decodePayload[finalData](t, eventsByType(store, session.TypeFinal)[1])
	if final.Text != "new answer" {
		t.Fatalf("final text = %q, want only the current run text", final.Text)
	}
	end := decodePayload[runEndData](t, eventsByType(store, session.TypeRunEnd)[1])
	if end.RunTurns != 1 || end.RunUsage.Total() != 9 || end.Turns != 2 || end.Usage.Total() != 15 {
		t.Fatalf("run_end = %+v", end)
	}
}

// fakeStreamCompleter implements Completer and StreamCompleter with a script
// of streaming attempts.
type streamStep struct {
	deltas []string
	resp   model.Response
	err    error
}

type fakeStreamCompleter struct {
	steps     []streamStep
	calls     int
	requests  []model.Request
	noPartial bool
}

func (f *fakeStreamCompleter) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("Complete must not be called")
}

func (f *fakeStreamCompleter) CompleteStream(ctx context.Context, req model.Request, emit func(string) error) (model.Response, error) {
	f.requests = append(f.requests, req)
	index := f.calls
	f.calls++
	if index >= len(f.steps) {
		return model.Response{}, errors.New("unexpected stream call")
	}
	step := f.steps[index]
	var emitted strings.Builder
	for _, delta := range step.deltas {
		emitted.WriteString(delta)
		if err := emit(delta); err != nil {
			return model.Response{}, err
		}
	}
	if step.err != nil {
		if emitted.Len() > 0 && !f.noPartial {
			return model.Response{}, &model.PartialError{Err: step.err, Text: emitted.String(), Bytes: emitted.Len()}
		}
		return model.Response{}, step.err
	}
	resp := step.resp
	resp.Streamed = true
	return resp, nil
}

func TestRunnerStreamsDeltasOnce(t *testing.T) {
	cfg := testConfig()
	cfg.Stream = true
	var output strings.Builder
	cfg.Display = func(ev DisplayEvent) error {
		if delta, ok := ev.(TextDelta); ok {
			output.WriteString(delta.Text)
		}
		return nil
	}
	comp := &fakeStreamCompleter{steps: []streamStep{{
		deltas: []string{"a", "b"},
		resp:   model.Response{Text: "ab", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 2}},
	}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %q", res.Status)
	}
	if output.String() != "ab" {
		t.Fatalf("output = %q, want %q (deltas only, no reprint)", output.String(), "ab")
	}
	response := decodePayload[modelResponseData](t, eventsByType(store, session.TypeModelResponse)[0])
	if !response.Streamed {
		t.Fatal("model_response streamed = false, want true")
	}
}

func TestRunnerStreamFallbackPrintsOnce(t *testing.T) {
	cfg := testConfig()
	cfg.Stream = true
	var output strings.Builder
	cfg.Display = func(ev DisplayEvent) error {
		if delta, ok := ev.(TextDelta); ok {
			output.WriteString(delta.Text)
		}
		return nil
	}
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		Text: "plain", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 1},
	}}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %q", res.Status)
	}
	if output.String() != "plain" {
		t.Fatalf("output = %q, want one fallback print", output.String())
	}
	response := decodePayload[modelResponseData](t, eventsByType(store, session.TypeModelResponse)[0])
	if response.Streamed {
		t.Fatal("model_response streamed = true, want false for a fallback")
	}
}

func TestRunnerRetriesOnlyBeforeFirstDelta(t *testing.T) {
	t.Run("retry before delta", func(t *testing.T) {
		cfg := testConfig()
		cfg.Stream = true
		var output strings.Builder
		cfg.Display = func(ev DisplayEvent) error {
			if delta, ok := ev.(TextDelta); ok {
				output.WriteString(delta.Text)
			}
			return nil
		}
		comp := &fakeStreamCompleter{steps: []streamStep{
			{err: &model.HTTPError{StatusCode: 429, Status: "429 Too Many Requests"}},
			{deltas: []string{"ok"}, resp: model.Response{Text: "ok", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 1}}},
		}}
		store := &fakeStore{}
		runner := newRunner(cfg, comp, &fakeToolset{}, store)
		startTestSession(t, runner)
		runner.jitter = func(time.Duration) time.Duration { return 0 }
		res, err := runner.Run(t.Context(), defaultRunOptions())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Status != StopCompleted || comp.calls != 2 {
			t.Fatalf("result = %+v calls = %d", res, comp.calls)
		}
		if output.String() != "ok" {
			t.Fatalf("output = %q, want %q", output.String(), "ok")
		}
		if got := len(eventsByType(store, session.TypeModelRetry)); got != 1 {
			t.Fatalf("model_retry events = %d, want 1", got)
		}
	})

	t.Run("no retry after delta", func(t *testing.T) {
		cfg := testConfig()
		cfg.Stream = true
		var output strings.Builder
		cfg.Display = func(ev DisplayEvent) error {
			if delta, ok := ev.(TextDelta); ok {
				output.WriteString(delta.Text)
			}
			return nil
		}
		comp := &fakeStreamCompleter{steps: []streamStep{{
			deltas: []string{"partial"},
			err:    &model.TransportError{Err: errors.New("connection reset")},
		}}}
		store, res, err := runTask(t, cfg, comp, &fakeToolset{})
		if res.Status != StopProviderError {
			t.Fatalf("status = %q, want provider_error", res.Status)
		}
		if err == nil {
			t.Fatal("Run error = nil, want provider error")
		}
		if comp.calls != 1 {
			t.Fatalf("calls = %d, want 1 (no retry after output)", comp.calls)
		}
		if output.String() != "partial" {
			t.Fatalf("output = %q", output.String())
		}
		if got := len(eventsByType(store, session.TypeModelResponse)); got != 0 {
			t.Fatalf("model_response events = %d, want 0", got)
		}
		errs := eventsByType(store, session.TypeError)
		if len(errs) != 1 {
			t.Fatalf("error events = %d, want 1", len(errs))
		}
		data := decodePayload[errorData](t, errs[0])
		if data.PartialText != "partial" || data.PartialBytes != len("partial") {
			t.Fatalf("error partial = %+v", data)
		}
	})

	t.Run("no retry after delta even without PartialError", func(t *testing.T) {
		cfg := testConfig()
		cfg.Stream = true
		var output strings.Builder
		cfg.Display = func(ev DisplayEvent) error {
			if delta, ok := ev.(TextDelta); ok {
				output.WriteString(delta.Text)
			}
			return nil
		}
		comp := &fakeStreamCompleter{noPartial: true, steps: []streamStep{{
			deltas: []string{"partial"},
			err:    &model.TransportError{Err: errors.New("connection reset")},
		}}}
		store, res, err := runTask(t, cfg, comp, &fakeToolset{})
		if res.Status != StopProviderError {
			t.Fatalf("status = %q, want provider_error", res.Status)
		}
		if err == nil {
			t.Fatal("Run error = nil, want provider error")
		}
		if comp.calls != 1 {
			t.Fatalf("calls = %d, want 1 (no retry after output)", comp.calls)
		}
		if output.String() != "partial" {
			t.Fatalf("output = %q", output.String())
		}
		if got := len(eventsByType(store, session.TypeModelRetry)); got != 0 {
			t.Fatalf("model_retry events = %d, want 0", got)
		}
	})
}

func TestRunnerRejectsOversizedResponseBeforeTools(t *testing.T) {
	cfg := testConfig()
	ts := &fakeToolset{}
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		Text:         strings.Repeat("x", maxModelResponseBytes),
		ToolCalls:    []model.ToolCall{toolCall("a", "read_file", `{}`)},
		FinishReason: model.FinishToolCalls,
		Usage:        model.Usage{InputTokens: 1, OutputTokens: 1},
	}}}}
	store, res, err := runTask(t, cfg, comp, ts)
	if res.Status != StopProtocolError {
		t.Fatalf("status = %q, want protocol_error", res.Status)
	}
	if err == nil {
		t.Fatal("Run error = nil, want protocol error")
	}
	if len(ts.calls) != 0 {
		t.Fatalf("tool calls = %+v, want none", ts.calls)
	}
	if got := len(eventsByType(store, session.TypeModelResponse)); got != 0 {
		t.Fatalf("model_response events = %d, want 0", got)
	}
}

func TestRunnerLogsFullOneMiBResponse(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		Text:         strings.Repeat("x", maxModelResponseBytes),
		FinishReason: model.FinishStop,
		Usage:        model.Usage{InputTokens: 1, OutputTokens: 2},
	}}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %q", res.Status)
	}
	response := decodePayload[modelResponseData](t, eventsByType(store, session.TypeModelResponse)[0])
	if len(response.Text) != maxModelResponseBytes {
		t.Fatalf("logged text = %d bytes, want %d", len(response.Text), maxModelResponseBytes)
	}
}

// testController cancels the most recent operation on interrupt.
type testController struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	starts int
}

func (c *testController) BeginOperation(ctx context.Context) (context.Context, func()) {
	opCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.starts++
	c.mu.Unlock()
	return opCtx, func() {
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
	}
}

func (c *testController) interrupt() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type interruptCompleter struct {
	calls   int
	started chan struct{}
}

func (c *interruptCompleter) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	c.calls++
	if c.calls == 1 {
		close(c.started)
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	return model.Response{Text: "after", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func TestRunnerModelInterruptContinues(t *testing.T) {
	cfg := testConfig()
	ctl := &testController{}
	cfg.Controller = ctl
	comp := &interruptCompleter{started: make(chan struct{})}
	store := &fakeStore{}
	runner := newRunner(cfg, comp, &fakeToolset{}, store)
	startTestSession(t, runner)

	outcome := make(chan runOutcome, 1)
	go func() {
		res, err := runner.Run(t.Context(), defaultRunOptions())
		outcome <- runOutcome{res: res, err: err}
	}()
	<-comp.started
	ctl.interrupt()
	got := <-outcome
	if got.err != nil {
		t.Fatalf("Run: %v", got.err)
	}
	if got.res.Status != StopCompleted || got.res.FinalText != "after" {
		t.Fatalf("result = %+v", got.res)
	}
	if comp.calls != 2 {
		t.Fatalf("model calls = %d, want 2", comp.calls)
	}
	interrupts := eventsByType(store, session.TypeInterrupt)
	if len(interrupts) != 1 {
		t.Fatalf("interrupt events = %d, want 1", len(interrupts))
	}
	data := decodePayload[interruptData](t, interrupts[0])
	if data.Operation != operationModel || data.Reason == "" {
		t.Fatalf("interrupt = %+v", data)
	}
	hints := 0
	for _, ev := range eventsByType(store, session.TypeUserMessage) {
		if decodePayload[userMessageData](t, ev).Source == sourceInterruptHint {
			hints++
		}
	}
	if hints != 1 {
		t.Fatalf("interrupt hints = %d, want 1", hints)
	}
}

func TestRunnerToolInterruptPairsRemaining(t *testing.T) {
	cfg := testConfig()
	ctl := &testController{}
	cfg.Controller = ctl
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			ToolCalls: []model.ToolCall{
				toolCall("a", "read_file", `{}`),
				toolCall("b", "search", `{}`),
			},
			FinishReason: model.FinishToolCalls,
		}},
		{resp: model.Response{Text: "done", FinishReason: model.FinishStop}},
	}}
	started := make(chan struct{})
	ts := &fakeToolset{exec: func(ctx context.Context, name string, _ json.RawMessage) tools.Result {
		if name == "read_file" {
			close(started)
			<-ctx.Done()
			return tools.Failure(tools.CodeCanceled, "canceled")
		}
		return tools.Success("should not run")
	}}
	store := &fakeStore{}
	runner := newRunner(cfg, comp, ts, store)
	startTestSession(t, runner)

	outcome := make(chan runOutcome, 1)
	go func() {
		res, err := runner.Run(t.Context(), defaultRunOptions())
		outcome <- runOutcome{res: res, err: err}
	}()
	<-started
	ctl.interrupt()
	got := <-outcome
	if got.err != nil {
		t.Fatalf("Run: %v", got.err)
	}
	if got.res.Status != StopCompleted || got.res.FinalText != "done" {
		t.Fatalf("result = %+v", got.res)
	}
	if len(ts.calls) != 1 || ts.calls[0].name != "read_file" {
		t.Fatalf("executed tools = %+v, want only the interrupted call", ts.calls)
	}
	results := eventsByType(store, session.TypeToolResult)
	if len(results) != 2 {
		t.Fatalf("tool results = %d, want 2", len(results))
	}
	for i, want := range []string{"a", "b"} {
		data := decodePayload[toolResultData](t, results[i])
		if data.CallID != want || data.Code != tools.CodeCanceled {
			t.Fatalf("tool result[%d] = %+v", i, data)
		}
	}
	interrupts := eventsByType(store, session.TypeInterrupt)
	if len(interrupts) != 1 || decodePayload[interruptData](t, interrupts[0]).Operation != operationTool {
		t.Fatalf("interrupts = %+v", interrupts)
	}
	if comp.calls != 2 {
		t.Fatalf("model calls = %d, want 2", comp.calls)
	}
}

type steeringCompleter struct {
	send     chan<- string
	requests []model.Request
	calls    int
}

func (c *steeringCompleter) Complete(_ context.Context, req model.Request) (model.Response, error) {
	c.requests = append(c.requests, req)
	c.calls++
	if c.calls == 1 {
		c.send <- "also check the README"
		return model.Response{Text: "first", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	}
	return model.Response{Text: "done", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func TestRunnerSteeringChangesNextCall(t *testing.T) {
	cfg := testConfig()
	steer := make(chan string, 1)
	cfg.Steering = steer
	comp := &steeringCompleter{send: steer}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopCompleted || res.FinalText != "done" || comp.calls != 2 {
		t.Fatalf("result = %+v calls = %d", res, comp.calls)
	}
	if len(comp.requests) < 2 {
		t.Fatalf("model requests = %d, want 2", len(comp.requests))
	}
	found := false
	for _, msg := range comp.requests[1].Messages {
		if msg.Role == model.RoleUser && msg.Content == "also check the README" {
			found = true
		}
	}
	if !found {
		t.Fatalf("second request does not include steering: %+v", comp.requests[1].Messages)
	}
	steers := 0
	for _, ev := range eventsByType(store, session.TypeUserMessage) {
		if data := decodePayload[userMessageData](t, ev); data.Source == sourceSteer {
			steers++
			if data.Text != "also check the README" {
				t.Fatalf("steering text = %q", data.Text)
			}
		}
	}
	if steers != 1 {
		t.Fatalf("steering messages = %d, want 1", steers)
	}
}
