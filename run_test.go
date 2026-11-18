package aharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eddiectc/aharness"
)

type interruptibleModel struct {
	mu      sync.Mutex
	started chan struct{}
	once    sync.Once
	calls   int
}

func newInterruptibleModel() *interruptibleModel {
	return &interruptibleModel{started: make(chan struct{})}
}

func (m *interruptibleModel) Complete(ctx context.Context, _ aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		m.once.Do(func() { close(m.started) })
		<-ctx.Done()
		return aharness.Response{}, ctx.Err()
	}
	return aharness.Response{Text: "after interrupt", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}

func TestRunOnEventMatchesStartOrder(t *testing.T) {
	tool := aharness.Tool{
		Name:        "lookup",
		Description: "Look up a record.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"intent":{"type":"string"}},"required":["intent"],"additionalProperties":false}`),
		Run: func(context.Context, aharness.ToolCall) (aharness.ToolResult, error) {
			return aharness.TextResult("record"), nil
		},
	}
	responses := func() []aharness.Response {
		return []aharness.Response{
			{
				ToolCalls:    []aharness.ToolCall{{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"intent":"look it up"}`)}},
				FinishReason: aharness.FinishToolCalls,
				Usage:        aharness.Usage{InputTokens: 2, OutputTokens: 1, Reported: true},
			},
			{Text: "done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 3, OutputTokens: 2, Reported: true}},
		}
	}
	newAgent := func() *aharness.Agent {
		ag, err := aharness.New(context.Background(), aharness.Config{
			Workspace: t.TempDir(),
			StateDir:  t.TempDir(),
			UserAgent: "aharness-test/1.0",
			Model:     testModel(&scriptedModel{responses: responses()}),
			Tools:     &aharness.ToolsConfig{Extra: []aharness.Tool{tool}},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { _ = ag.Close() })
		return ag
	}

	startAgent := newAgent()
	run, err := startAgent.Start(context.Background(), "work", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var startResult aharness.Result
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		if ev, ok := ev.(aharness.RunFinished); ok {
			startResult = ev.Result
		}
	}

	runAgent := newAgent()
	var hookKinds []string
	var hookResult aharness.Result
	result, err := runAgent.Run(context.Background(), "work", aharness.RunOptions{
		OnEvent: func(ev aharness.Event) {
			hookKinds = append(hookKinds, fmt.Sprintf("%T", ev))
			if ev, ok := ev.(aharness.RunFinished); ok {
				hookResult = ev.Result
			}
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(hookKinds, ",") != "aharness.RunStarted,aharness.MessageRecorded,aharness.ResponseCompleted,aharness.ToolStarted,aharness.ToolCompleted,aharness.ResponseCompleted,aharness.RunFinished" {
		t.Fatalf("OnEvent kinds = %v", hookKinds)
	}
	if hookKinds[len(hookKinds)-1] != "aharness.RunFinished" {
		t.Fatalf("last OnEvent = %s", hookKinds[len(hookKinds)-1])
	}
	if hookResult.Status != startResult.Status || hookResult.RunTurns != startResult.RunTurns ||
		hookResult.RunUsage != startResult.RunUsage || hookResult.FinalText != startResult.FinalText {
		t.Fatalf("hook result = %+v, start result = %+v", hookResult, startResult)
	}
	if result.Status != startResult.Status || result.RunTurns != startResult.RunTurns ||
		result.RunUsage != startResult.RunUsage || result.FinalText != startResult.FinalText {
		t.Fatalf("Run result = %+v, start result = %+v", result, startResult)
	}
}

func TestInterruptContinuesRun(t *testing.T) {
	model := newInterruptibleModel()
	ag, _, _ := newTestAgent(t, nil, model)
	run, err := ag.Start(context.Background(), "work", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	go func() {
		<-model.started
		run.Interrupt()
	}()
	var finished aharness.Result
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		if event, ok := ev.(aharness.RunFinished); ok {
			finished = event.Result
		}
	}
	if finished.Status != aharness.StopCompleted {
		t.Fatalf("status = %q (err %v)", finished.Status, finished.Err)
	}
	if finished.FinalText != "after interrupt" {
		t.Fatalf("final text = %q", finished.FinalText)
	}
}

func TestCancelTerminatesRun(t *testing.T) {
	model := newInterruptibleModel()
	ag, _, _ := newTestAgent(t, nil, model)
	run, err := ag.Start(context.Background(), "work", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	go func() {
		<-model.started
		run.Cancel()
	}()
	var statuses []aharness.StopReason
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		if event, ok := ev.(aharness.RunFinished); ok {
			statuses = append(statuses, event.Result.Status)
		}
	}
	result, ok := run.Result()
	if !ok {
		t.Fatal("Result not available after Done")
	}
	if result.Status != aharness.StopInterrupted {
		t.Fatalf("status = %q, want interrupted", result.Status)
	}
	if len(statuses) != 1 || statuses[0] != aharness.StopInterrupted {
		t.Fatalf("RunFinished statuses = %v", statuses)
	}
}

func TestRunBusyAndEmptyTask(t *testing.T) {
	model := newInterruptibleModel()
	ag, _, _ := newTestAgent(t, nil, model)
	if _, err := ag.Start(context.Background(), "  ", aharness.RunOptions{}); err == nil {
		t.Fatal("empty task accepted")
	} else if !errors.Is(err, aharness.ErrEmptyTask) {
		t.Fatalf("empty task error = %v", err)
	}
	run, err := ag.Start(context.Background(), "work", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := ag.Start(context.Background(), "second", aharness.RunOptions{}); !errors.Is(err, aharness.ErrBusy) {
		t.Fatalf("second Start error = %v, want ErrBusy", err)
	}
	run.Cancel()
	_ = run.Close()
}

func TestSteerConsumedAtTurnBoundary(t *testing.T) {
	model := &steerModel{firstStarted: make(chan struct{}), release: make(chan struct{})}
	ag, _, _ := newTestAgent(t, nil, model)
	run, err := ag.Start(context.Background(), "work", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	go func() {
		<-model.firstStarted
		if err := run.Steer("one more thing"); err != nil {
			t.Errorf("Steer: %v", err)
		}
		close(model.release)
	}()
	var finished aharness.Result
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		if event, ok := ev.(aharness.RunFinished); ok {
			finished = event.Result
		}
	}
	if finished.Status != aharness.StopCompleted || finished.FinalText != "steered" {
		t.Fatalf("result = %+v", finished)
	}
	if finished.RunTurns != 2 {
		t.Fatalf("run turns = %d, want 2", finished.RunTurns)
	}
	if err := run.Steer("late"); !errors.Is(err, aharness.ErrClosed) {
		t.Fatalf("late Steer error = %v, want ErrClosed", err)
	}
}

type steerModel struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	release      chan struct{}
}

func (m *steerModel) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		close(m.firstStarted)
		<-m.release
		return aharness.Response{Text: "first", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
	}
	return aharness.Response{Text: "steered", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}

func TestPolicyDenialIsModelVisibleAndNotStuck(t *testing.T) {
	called := 0
	tools := customTools(t, &called)
	responses := make([]aharness.Response, 0, 7)
	for i := range 6 {
		responses = append(responses, aharness.Response{
			ToolCalls:    []aharness.ToolCall{{ID: fmt.Sprintf("call-%d", i), Name: "lookup", Arguments: json.RawMessage(`{"id":"x"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		})
	}
	responses = append(responses, aharness.Response{Text: "allowed", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}})
	model := &scriptedModel{responses: responses}
	workspace := t.TempDir()
	stateDir := t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(model),
		Tools:     tools,
		Authorize: func(_ context.Context, req aharness.ToolRequest) error {
			if req.Tool == "lookup" {
				return &aharness.Deny{Reason: "lookup is disabled"}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	run, err := ag.Start(context.Background(), "policy", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	denials := 0
	var finished aharness.Result
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		switch ev := ev.(type) {
		case aharness.ToolCompleted:
			if ev.Code == "permission_denied" {
				denials++
			}
		case aharness.RunFinished:
			finished = ev.Result
		}
	}
	if finished.Status != aharness.StopCompleted {
		t.Fatalf("status = %q (err %v)", finished.Status, finished.Err)
	}
	if called != 0 {
		t.Fatalf("denied tool ran %d time(s)", called)
	}
	if denials != 6 {
		t.Fatalf("denials = %d, want 6", denials)
	}
}

type panicModel struct{}

func (panicModel) Complete(context.Context, aharness.Request) (aharness.Response, error) {
	panic("boom")
}

func TestModelPanicIsInternalError(t *testing.T) {
	ag, _, _ := newTestAgent(t, nil, panicModel{})
	result, err := ag.Run(context.Background(), "panic", aharness.RunOptions{})
	if result.Status != aharness.StopInternalError {
		t.Fatalf("status = %q (err %v)", result.Status, err)
	}
	if err == nil || !strings.Contains(err.Error(), "model panicked") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunStartedResumedFromSeq(t *testing.T) {
	model := &scriptedModel{responses: []aharness.Response{
		{Text: "one", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
		{Text: "two", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	ag, _, _ := newTestAgent(t, nil, model)
	collect := func() (uint64, aharness.RunFinished) {
		run, err := ag.Start(context.Background(), "task", aharness.RunOptions{})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		var (
			seq      uint64
			finished aharness.RunFinished
		)
		for ev, err := range run.Events() {
			if err != nil {
				t.Fatalf("event error: %v", err)
			}
			switch ev := ev.(type) {
			case aharness.RunStarted:
				seq = ev.ResumedFromSeq
			case aharness.RunFinished:
				finished = ev
			}
		}
		return seq, finished
	}
	firstSeq, firstFinished := collect()
	if firstSeq != 0 {
		t.Fatalf("first ResumedFromSeq = %d, want 0", firstSeq)
	}
	if firstFinished.Reason == "" || firstFinished.Duration <= 0 {
		t.Fatalf("first RunFinished = %+v", firstFinished)
	}
	secondSeq, secondFinished := collect()
	if secondSeq == 0 {
		t.Fatalf("second ResumedFromSeq = 0, want the prior last sequence")
	}
	if secondFinished.Result.Status != aharness.StopCompleted {
		t.Fatalf("second status = %q", secondFinished.Result.Status)
	}
}

type streamingFake struct{}

func (m *streamingFake) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
	return aharness.Response{Text: "streamed", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}

func (m *streamingFake) CompleteStream(_ context.Context, _ aharness.Request, stream aharness.StreamHandler) (aharness.Response, error) {
	if err := stream.Text("str"); err != nil {
		return aharness.Response{}, err
	}
	if err := stream.Text("eamed"); err != nil {
		return aharness.Response{}, err
	}
	return aharness.Response{Text: "streamed", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}

func TestStartStreamsDeltasAndRunDoesNot(t *testing.T) {
	ag, _, _ := newTestAgent(t, nil, &streamingFake{})
	run, err := ag.Start(context.Background(), "stream", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	deltas := ""
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		if delta, ok := ev.(aharness.TextDelta); ok {
			deltas += delta.Text
		}
	}
	if deltas != "streamed" {
		t.Fatalf("streamed deltas = %q", deltas)
	}
	result, err := ag.Run(context.Background(), "blocking", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinalText != "streamed" {
		t.Fatalf("blocking final text = %q", result.FinalText)
	}
}

func TestToolCallsWithinOneResponseAreSequential(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	var orderMu sync.Mutex
	var order []string
	tools := &aharness.ToolsConfig{Extra: []aharness.Tool{{
		Name:        "sequential",
		Description: "Record ordering.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
		Run: func(_ context.Context, call aharness.ToolCall) (aharness.ToolResult, error) {
			current := active.Add(1)
			if current > maxActive.Load() {
				maxActive.Store(current)
			}
			var args struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			orderMu.Lock()
			order = append(order, "start:"+args.ID)
			orderMu.Unlock()
			time.Sleep(5 * time.Millisecond)
			orderMu.Lock()
			order = append(order, "end:"+args.ID)
			orderMu.Unlock()
			active.Add(-1)
			return aharness.TextResult("ok"), nil
		},
	}}}
	model := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{
				{ID: "1", Name: "sequential", Arguments: json.RawMessage(`{"id":"a"}`)},
				{ID: "2", Name: "sequential", Arguments: json.RawMessage(`{"id":"b"}`)},
				{ID: "3", Name: "sequential", Arguments: json.RawMessage(`{"id":"c"}`)},
			},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	ag, _, _ := newTestAgent(t, tools, model)
	result, err := ag.Run(context.Background(), "sequential", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != aharness.StopCompleted {
		t.Fatalf("status = %q", result.Status)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("max concurrent tool calls = %d, want 1", maxActive.Load())
	}
	want := []string{"start:a", "end:a", "start:b", "end:b", "start:c", "end:c"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestCloseDuringRunWithoutConsumingEvents(t *testing.T) {
	model := newInterruptibleModel()
	ag, _, _ := newTestAgent(t, nil, model)
	run, err := ag.Start(context.Background(), "work", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	result, ok := run.Result()
	if !ok {
		t.Fatal("run result missing after Close")
	}
	if result.Status != aharness.StopInterrupted {
		t.Fatalf("status = %q, want interrupted", result.Status)
	}
	var iteratorErr error
	run.Events()(func(_ aharness.Event, err error) bool {
		iteratorErr = err
		return false
	})
	if !errors.Is(iteratorErr, aharness.ErrIteratorConsumed) {
		t.Fatalf("Events after Close error = %v, want ErrIteratorConsumed", iteratorErr)
	}
}
