package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

func TestResumedFromSeqDerivedOncePerRun(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{Text: "one", FinishReason: model.FinishStop}},
		{resp: model.Response{Text: "two", FinishReason: model.FinishStop}},
		{resp: model.Response{Text: "three", FinishReason: model.FinishStop}},
	}}
	store := &fakeStore{}
	runner := newRunner(cfg, comp, &fakeToolset{}, store)
	startTestSession(t, runner)
	if _, err := runner.Run(t.Context(), defaultRunOptions()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	first := decodePayload[runStartData](t, eventsByType(store, session.TypeRunStart)[0])
	if first.ResumedFromSeq != 0 {
		t.Fatalf("fresh ResumedFromSeq = %d, want 0", first.ResumedFromSeq)
	}
	previousLast := store.events[len(store.events)-1].Seq
	if _, err := runner.Run(t.Context(), defaultRunOptions()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	runs := eventsByType(store, session.TypeRunStart)
	second := decodePayload[runStartData](t, runs[1])
	if second.ResumedFromSeq != previousLast {
		t.Fatalf("second ResumedFromSeq = %d, want prior last %d", second.ResumedFromSeq, previousLast)
	}
	opts := defaultRunOptions()
	opts.ResumedFromSeq = 42
	opts.ResumedFromSeqSet = true
	if _, err := runner.Run(t.Context(), opts); err != nil {
		t.Fatalf("third Run: %v", err)
	}
	runs = eventsByType(store, session.TypeRunStart)
	third := decodePayload[runStartData](t, runs[2])
	if third.ResumedFromSeq != 42 {
		t.Fatalf("explicit ResumedFromSeq = %d, want 42", third.ResumedFromSeq)
	}
}

func TestRunCompleted(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{{
		resp: model.Response{
			Text:         "all done",
			FinishReason: model.FinishStop,
			Usage:        model.Usage{InputTokens: 10, OutputTokens: 5},
		},
	}}}
	ts := &fakeToolset{}
	store, res, err := runTask(t, cfg, comp, ts)
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %q, want %q", res.Status, StopCompleted)
	}
	if res.FinalText != "all done" {
		t.Fatalf("final text = %q, want %q", res.FinalText, "all done")
	}
	if res.CumulativeTurns != 1 {
		t.Fatalf("turns = %d, want 1", res.CumulativeTurns)
	}
	if res.CumulativeUsage.Total() != 15 {
		t.Fatalf("usage total = %d, want 15", res.CumulativeUsage.Total())
	}
	if res.ContextTokens != 15 {
		t.Fatalf("context tokens = %d, want 15", res.ContextTokens)
	}
	if comp.calls != 1 {
		t.Fatalf("model calls = %d, want 1", comp.calls)
	}
	if ts.calls != nil {
		t.Fatalf("tool calls = %v, want none", ts.calls)
	}

	wantTypes := []session.EventType{
		session.TypeSessionStart,
		session.TypeRunStart,
		session.TypeUserMessage,
		session.TypeModelResponse,
		session.TypeFinal,
		session.TypeRunEnd,
	}
	if len(store.events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d (%v)", len(store.events), len(wantTypes), wantTypes)
	}
	for i, want := range wantTypes {
		if got := store.events[i].Type; got != want {
			t.Fatalf("event[%d].type = %q, want %q", i, got, want)
		}
	}

	start := decodePayload[sessionStartData](t, store.events[0])
	if start.Workspace != cfg.Workspace || start.Model != cfg.Model || start.PromptVersion != PromptVersion || start.ToolsetVersion != tools.Version {
		t.Fatalf("session_start = %+v", start)
	}
	runStart := decodePayload[runStartData](t, store.events[1])
	if runStart.Model != cfg.Model || runStart.BaseURL != cfg.BaseURL || runStart.ResumedFromSeq != 0 || runStart.PromptVersion != PromptVersion {
		t.Fatalf("run_start = %+v", runStart)
	}
	user := decodePayload[userMessageData](t, store.events[2])
	if user.Text != "do the task" || user.Source != "-task" {
		t.Fatalf("user_message = %+v", user)
	}
	response := decodePayload[modelResponseData](t, store.events[3])
	if response.Attempt != 1 || response.Text != "all done" || response.FinishReason != model.FinishStop || response.Usage.Total() != 15 {
		t.Fatalf("model_response = %+v", response)
	}
	final := decodePayload[finalData](t, store.events[4])
	if final.Status != StopCompleted || final.Text != "all done" {
		t.Fatalf("final = %+v", final)
	}
	end := decodePayload[runEndData](t, store.events[5])
	if res.Reason != end.Reason || res.Duration.Milliseconds() != end.DurationMS {
		t.Fatalf("result reason/duration = %q/%s, run_end = %q/%dms", res.Reason, res.Duration, end.Reason, end.DurationMS)
	}
	if end.Status != StopCompleted || end.RunTurns != 1 || end.Turns != 1 || end.RunUsage.Total() != 15 || end.Usage.Total() != 15 || end.Reason == "" {
		t.Fatalf("run_end = %+v", end)
	}
	if countEvents(store, session.TypeFinal) != 1 || countEvents(store, session.TypeRunEnd) != 1 {
		t.Fatalf("final/run_end counts = %d/%d",
			countEvents(store, session.TypeFinal), countEvents(store, session.TypeRunEnd))
	}
	if store.syncs == 0 {
		t.Fatal("store was never synced")
	}

	req := comp.requests[0]
	if req.Model != cfg.Model || req.MaxOutputTokens != cfg.Limits.MaxOutputTokens || len(req.Tools) != len(tools.Schemas()) {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != model.RoleSystem || !strings.Contains(req.Messages[0].Content, cfg.Workspace) {
		t.Fatalf("request messages = %+v", req.Messages)
	}
	if req.Messages[1].Role != model.RoleUser || req.Messages[1].Content != "do the task" {
		t.Fatalf("task message = %+v", req.Messages[1])
	}
}

func TestRunToolLoopPairsEveryCall(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			Text:         "using tools",
			ToolCalls:    []model.ToolCall{toolCall("a", "read_file", `{"path":"a.txt"}`), toolCall("b", "search", `{"pattern":"x"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 20, OutputTokens: 4},
		}},
		{resp: model.Response{Text: "done", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 30, OutputTokens: 7}}},
	}}
	ts := &fakeToolset{exec: func(_ context.Context, name string, args json.RawMessage) tools.Result {
		return tools.Success(name + ":" + string(args))
	}}
	store, res, err := runTask(t, cfg, comp, ts)
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopCompleted || res.CumulativeTurns != 2 || res.FinalText != "done" {
		t.Fatalf("result = %+v", res)
	}
	if len(ts.calls) != 2 || ts.calls[0].name != "read_file" || ts.calls[1].name != "search" {
		t.Fatalf("tool order = %+v", ts.calls)
	}
	results := eventsByType(store, session.TypeToolResult)
	if len(results) != 2 {
		t.Fatalf("tool_result events = %d, want 2", len(results))
	}
	for i, wantID := range []string{"a", "b"} {
		data := decodePayload[toolResultData](t, results[i])
		if data.CallID != wantID || !data.OK || data.Code != tools.CodeOK {
			t.Fatalf("tool_result[%d] = %+v", i, data)
		}
	}
	responses := eventsByType(store, session.TypeModelResponse)
	if len(responses) != 2 {
		t.Fatalf("model_response events = %d, want 2", len(responses))
	}
	if got := decodePayload[modelResponseData](t, responses[0]); len(got.ToolCalls) != 2 {
		t.Fatalf("first response tool calls = %+v", got.ToolCalls)
	}

	// The second request must replay the assistant call followed by both
	// results, in response order.
	second := comp.requests[1]
	if len(second.Messages) != 5 {
		t.Fatalf("second request message count = %d, want 5", len(second.Messages))
	}
	if second.Messages[2].Role != model.RoleAssistant || len(second.Messages[2].ToolCalls) != 2 {
		t.Fatalf("assistant replay = %+v", second.Messages[2])
	}
	for i, wantID := range []string{"a", "b"} {
		msg := second.Messages[3+i]
		if msg.Role != model.RoleTool || msg.ToolCallID != wantID || msg.Name == "" {
			t.Fatalf("tool replay[%d] = %+v", i, msg)
		}
	}
	if res.CumulativeUsage.Total() != 61 {
		t.Fatalf("usage total = %d, want 61", res.CumulativeUsage.Total())
	}
	// Context occupancy is the latest response (30+7), not the cumulative sum
	// over both responses, so it stays comparable to a context window.
	if res.ContextTokens != 37 {
		t.Fatalf("context tokens = %d, want 37", res.ContextTokens)
	}
}

func TestRunMaxTurns(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTurns = 2
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("a", "read_file", `{}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 1, OutputTokens: 1},
		}},
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("b", "read_file", `{}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 1, OutputTokens: 1},
		}},
	}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopMaxTurns || res.CumulativeTurns != 2 {
		t.Fatalf("result = %+v", res)
	}
	if comp.calls != 2 {
		t.Fatalf("model calls = %d, want 2", comp.calls)
	}
	if got := len(eventsByType(store, session.TypeToolResult)); got != 2 {
		t.Fatalf("tool_result events = %d, want 2", got)
	}
	limits := eventsByType(store, session.TypeLimit)
	if len(limits) != 1 {
		t.Fatalf("limit events = %d, want 1", len(limits))
	}
	data := decodePayload[limitData](t, limits[0])
	if data.Kind != limitMaxTurns || data.Configured != 2 || data.Observed != 2 {
		t.Fatalf("limit = %+v", data)
	}
	assertExactlyOne(t, store, session.TypeFinal)
	assertExactlyOne(t, store, session.TypeRunEnd)
	end := decodePayload[runEndData](t, eventsByType(store, session.TypeRunEnd)[0])
	if !strings.Contains(end.Reason, "-max-turns 2") || !strings.Contains(end.Reason, "turns 2") {
		t.Fatalf("run_end reason = %q, want the flag and observed turn", end.Reason)
	}
}

func TestRunTotalTokenLimit(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTotalTokens = 10
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		ToolCalls:    []model.ToolCall{toolCall("a", "read_file", `{}`)},
		FinishReason: model.FinishToolCalls,
		Usage:        model.Usage{InputTokens: 6, OutputTokens: 6},
	}}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopTokenLimit || res.CumulativeTurns != 1 || res.CumulativeUsage.Total() != 12 {
		t.Fatalf("result = %+v", res)
	}
	limits := eventsByType(store, session.TypeLimit)
	if len(limits) != 1 {
		t.Fatalf("limit events = %d, want 1", len(limits))
	}
	data := decodePayload[limitData](t, limits[0])
	if data.Kind != limitMaxTotalTokens || data.Configured != 10 || data.Observed != 12 {
		t.Fatalf("limit = %+v", data)
	}
	assertExactlyOne(t, store, session.TypeRunEnd)
	end := decodePayload[runEndData](t, eventsByType(store, session.TypeRunEnd)[0])
	if !strings.Contains(end.Reason, "-max-total-tokens 10") || !strings.Contains(end.Reason, "tokens 12") {
		t.Fatalf("run_end reason = %q, want the flag and observed tokens", end.Reason)
	}
}

func TestRunUnlimitedBudgetsExceedOldDefaults(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTurns = 0
	cfg.MaxTotalTokens = 0
	cfg.SessionTimeout = 0
	cfg.ModelTimeout = 0

	var steps []scriptStep
	for i := range 31 {
		steps = append(steps, scriptStep{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall(fmt.Sprintf("call_%d", i), "read_file", `{}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 70_000},
		}})
	}
	steps = append(steps, scriptStep{resp: model.Response{
		Text:         "done",
		FinishReason: model.FinishStop,
	}})

	comp := &fakeCompleter{steps: steps}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopCompleted || res.CumulativeTurns != 32 {
		t.Fatalf("result = %+v, want a completed 32-turn run", res)
	}
	if res.CumulativeUsage.Total() <= 2_000_000 {
		t.Fatalf("cumulative usage = %d, want more than the old 2,000,000 budget", res.CumulativeUsage.Total())
	}
	if comp.calls != 32 {
		t.Fatalf("model calls = %d, want 32", comp.calls)
	}
	if got := countEvents(store, session.TypeLimit); got != 0 {
		t.Fatalf("limit events = %d, want none", got)
	}
	starts := eventsByType(store, session.TypeRunStart)
	if len(starts) != 1 {
		t.Fatalf("run_start events = %d, want 1", len(starts))
	}
	start := decodePayload[runStartData](t, starts[0])
	if start.MaxTurns != 0 || start.MaxTotalTokens != 0 || start.SessionTimeoutMS != 0 || start.ModelTimeoutMS != 0 {
		t.Fatalf("run_start budgets = %d/%d/%d/%d, want zeros",
			start.MaxTurns, start.MaxTotalTokens, start.SessionTimeoutMS, start.ModelTimeoutMS)
	}
	assertExactlyOne(t, store, session.TypeFinal)
	assertExactlyOne(t, store, session.TypeRunEnd)
}

type deadlineProbe struct {
	hadDeadline bool
}

func (p *deadlineProbe) Complete(ctx context.Context, _ model.Request) (model.Response, error) {
	_, p.hadDeadline = ctx.Deadline()
	return model.Response{Text: "done", FinishReason: model.FinishStop}, nil
}

func TestRunModelTimeoutZeroHasNoDeadline(t *testing.T) {
	cfg := testConfig()
	cfg.SessionTimeout = 0
	cfg.ModelTimeout = 0
	probe := &deadlineProbe{}
	_, res, err := runTask(t, cfg, probe, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if probe.hadDeadline {
		t.Fatal("model call received a harness deadline with -model-timeout=0")
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %q, want %q", res.Status, StopCompleted)
	}
}

func TestRunTerminalFinishReasons(t *testing.T) {
	tests := []struct {
		name       string
		response   model.Response
		wantStatus StopReason
		wantClass  string
	}{
		{
			name:       "empty finish completes",
			response:   model.Response{Text: "plain"},
			wantStatus: StopCompleted,
		},
		{
			name:       "content filter",
			response:   model.Response{FinishReason: model.FinishContentFilter},
			wantStatus: StopProviderError,
			wantClass:  errorClassContentFilter,
		},
		{
			name:       "other finish",
			response:   model.Response{FinishReason: model.FinishOther},
			wantStatus: StopProtocolError,
			wantClass:  errorClassProtocol,
		},
		{
			name:       "tool_calls without calls",
			response:   model.Response{FinishReason: model.FinishToolCalls},
			wantStatus: StopProtocolError,
			wantClass:  errorClassProtocol,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			comp := &fakeCompleter{steps: []scriptStep{{resp: tt.response}}}
			store, res, err := runTask(t, cfg, comp, &fakeToolset{})
			if res.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (err %v)", res.Status, tt.wantStatus, err)
			}
			if tt.wantStatus == StopCompleted {
				if err != nil {
					t.Fatalf("Run error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Run error = nil, want fatal error")
			}
			errEvents := eventsByType(store, session.TypeError)
			if len(errEvents) != 1 {
				t.Fatalf("error events = %d, want 1", len(errEvents))
			}
			data := decodePayload[errorData](t, errEvents[0])
			if data.Class != tt.wantClass {
				t.Fatalf("error class = %q, want %q", data.Class, tt.wantClass)
			}
			assertExactlyOne(t, store, session.TypeFinal)
			assertExactlyOne(t, store, session.TypeRunEnd)
		})
	}
}

func TestRunRejectsBadToolCallIDs(t *testing.T) {
	tests := []struct {
		name  string
		calls []model.ToolCall
	}{
		{
			name:  "missing",
			calls: []model.ToolCall{toolCall("a", "read_file", `{}`), toolCall("", "search", `{}`)},
		},
		{
			name:  "duplicate",
			calls: []model.ToolCall{toolCall("dup", "read_file", `{}`), toolCall("dup", "search", `{}`)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
				ToolCalls:    tt.calls,
				FinishReason: model.FinishToolCalls,
			}}}}
			ts := &fakeToolset{}
			store, res, err := runTask(t, cfg, comp, ts)
			if res.Status != StopProtocolError {
				t.Fatalf("status = %q, want %q", res.Status, StopProtocolError)
			}
			if err == nil {
				t.Fatal("Run error = nil, want protocol error")
			}
			if len(ts.calls) != 0 {
				t.Fatalf("executed tool calls = %+v, want none", ts.calls)
			}
			if got := len(eventsByType(store, session.TypeToolResult)); got != 0 {
				t.Fatalf("tool_result events = %d, want 0", got)
			}
			errs := eventsByType(store, session.TypeError)
			if len(errs) != 1 || decodePayload[errorData](t, errs[0]).Class != errorClassProtocol {
				t.Fatalf("error events = %+v", errs)
			}
			assertExactlyOne(t, store, session.TypeFinal)
		})
	}
}

func TestRunPanicPairsRemainingCalls(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		ToolCalls: []model.ToolCall{
			toolCall("a", "read_file", `{}`),
			toolCall("b", "search", `{}`),
			toolCall("c", "run_command", `{}`),
		},
		FinishReason: model.FinishToolCalls,
	}}}}
	ts := &fakeToolset{exec: func(_ context.Context, name string, _ json.RawMessage) tools.Result {
		if name == "search" {
			panic("boom")
		}
		return tools.Success("fine")
	}}
	store, res, err := runTask(t, cfg, comp, ts)
	if res.Status != StopInternalError {
		t.Fatalf("status = %q, want %q", res.Status, StopInternalError)
	}
	if err == nil {
		t.Fatal("Run error = nil, want internal error")
	}
	if len(ts.calls) != 2 || ts.calls[0].name != "read_file" || ts.calls[1].name != "search" {
		t.Fatalf("executed calls = %+v", ts.calls)
	}
	results := eventsByType(store, session.TypeToolResult)
	if len(results) != 3 {
		t.Fatalf("tool_result events = %d, want 3", len(results))
	}
	wantCodes := []tools.Code{tools.CodeOK, tools.CodePanic, tools.CodeInternal}
	for i, want := range wantCodes {
		data := decodePayload[toolResultData](t, results[i])
		if data.Code != want {
			t.Fatalf("result[%d].code = %q, want %q", i, data.Code, want)
		}
		if data.CallID != []string{"a", "b", "c"}[i] {
			t.Fatalf("result[%d].call_id = %q", i, data.CallID)
		}
	}
	assertExactlyOne(t, store, session.TypeFinal)
	assertExactlyOne(t, store, session.TypeRunEnd)
}

func TestRunLengthContinuationThenTokenLimit(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{Text: "partial one", FinishReason: model.FinishLength}},
		{resp: model.Response{Text: "partial two", FinishReason: model.FinishLength}},
	}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopTokenLimit || res.CumulativeTurns != 2 {
		t.Fatalf("result = %+v", res)
	}
	users := eventsByType(store, session.TypeUserMessage)
	if len(users) != 2 {
		t.Fatalf("user_message events = %d, want 2", len(users))
	}
	hint := decodePayload[userMessageData](t, users[1])
	if hint.Source != sourceContinuationHint || hint.Text == "" {
		t.Fatalf("continuation hint = %+v", hint)
	}
	if len(comp.requests) >= 2 {
		found := false
		for _, msg := range comp.requests[1].Messages {
			if msg.Role == model.RoleUser && msg.Content == hint.Text {
				found = true
			}
		}
		if !found {
			t.Fatalf("second request does not include the continuation hint: %+v", comp.requests[1].Messages)
		}
	}
	limits := eventsByType(store, session.TypeLimit)
	if len(limits) != 1 || decodePayload[limitData](t, limits[0]).Kind != limitConsecutiveLength {
		t.Fatalf("limits = %+v", limits)
	}
	if res.FinalText != "partial two" {
		t.Fatalf("final text = %q, want %q", res.FinalText, "partial two")
	}
}

func TestRunLengthThenStopCompletes(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{Text: "partial", FinishReason: model.FinishLength}},
		{resp: model.Response{Text: "done", FinishReason: model.FinishStop}},
	}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopCompleted || res.CumulativeTurns != 2 || res.FinalText != "done" {
		t.Fatalf("result = %+v", res)
	}
	if got := len(eventsByType(store, session.TypeUserMessage)); got != 2 {
		t.Fatalf("user_message events = %d, want 2", got)
	}
}

func TestRunRepeatedFailureHint(t *testing.T) {
	cfg := testConfig()
	toolResp := func(args string) scriptStep {
		return scriptStep{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("call", "read_file", args)},
			FinishReason: model.FinishToolCalls,
		}}
	}
	comp := &fakeCompleter{steps: []scriptStep{
		toolResp(`{"b":1,"a":2}`),
		toolResp(`{ "a": 2, "b": 1 }`),
		toolResp(`{"a":2,"b":1}`),
		{resp: model.Response{Text: "recovered", FinishReason: model.FinishStop}},
	}}
	ts := &fakeToolset{exec: func(context.Context, string, json.RawMessage) tools.Result {
		return tools.Failure(tools.CodeNotFound, "missing")
	}}
	store, res, err := runTask(t, cfg, comp, ts)
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopCompleted || res.CumulativeTurns != 4 {
		t.Fatalf("result = %+v", res)
	}
	users := eventsByType(store, session.TypeUserMessage)
	if len(users) != 2 {
		t.Fatalf("user_message events = %d, want 2", len(users))
	}
	hint := decodePayload[userMessageData](t, users[1])
	if hint.Source != sourceStuckHint || !strings.Contains(hint.Text, "read_file") {
		t.Fatalf("stuck hint = %+v", hint)
	}
	if len(comp.requests) >= 4 {
		found := false
		for _, msg := range comp.requests[3].Messages {
			if msg.Role == model.RoleUser && msg.Content == hint.Text {
				found = true
			}
		}
		if !found {
			t.Fatalf("fourth request does not include the stuck hint")
		}
	}
}

func TestRunRepeatedFailureIgnoresTheDisplayOnlyIntent(t *testing.T) {
	cfg := testConfig()
	toolResp := func(intent string) scriptStep {
		return scriptStep{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("call", "read_file", `{"path":"x","intent":"`+intent+`"}`)},
			FinishReason: model.FinishToolCalls,
		}}
	}
	comp := &fakeCompleter{steps: []scriptStep{
		toolResp("first attempt"),
		toolResp("second attempt"),
		toolResp("third attempt"),
		{resp: model.Response{Text: "recovered", FinishReason: model.FinishStop}},
	}}
	ts := &fakeToolset{exec: func(context.Context, string, json.RawMessage) tools.Result {
		return tools.Failure(tools.CodeNotFound, "missing")
	}}
	store, res, err := runTask(t, cfg, comp, ts)
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopCompleted || res.CumulativeTurns != 4 {
		t.Fatalf("result = %+v", res)
	}
	hints := 0
	for _, ev := range eventsByType(store, session.TypeUserMessage) {
		if decodePayload[userMessageData](t, ev).Source == sourceStuckHint {
			hints++
		}
	}
	if hints != 1 {
		t.Fatalf("stuck hints = %d, want 1: differing intents split one failure", hints)
	}
}

func TestRunModelEventLogFailureIsInternal(t *testing.T) {
	cfg := testConfig()
	cfg.LogRequests = true
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{Text: "done", FinishReason: model.FinishStop}}}}
	store := &fakeStore{appendErr: errors.New("disk full"), failAfter: 2}
	runner := newRunner(cfg, comp, &fakeToolset{}, store)
	startTestSession(t, runner)
	res, err := runner.Run(t.Context(), defaultRunOptions())
	if err == nil {
		t.Fatal("Run error = nil, want the log failure")
	}
	if res.Status != StopInternalError {
		t.Fatalf("status = %q, want %q", res.Status, StopInternalError)
	}
	if comp.calls != 0 {
		t.Fatalf("model calls = %d, want 0", comp.calls)
	}
	if got := len(store.events); got != 2 {
		t.Fatalf("events = %d, want session_start and user_message only", got)
	}
}

func TestRunRepeatedFailureStuck(t *testing.T) {
	cfg := testConfig()
	var steps []scriptStep
	for range 5 {
		steps = append(steps, scriptStep{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("call", "read_file", `{"path":"x"}`)},
			FinishReason: model.FinishToolCalls,
		}})
	}
	comp := &fakeCompleter{steps: steps}
	ts := &fakeToolset{exec: func(context.Context, string, json.RawMessage) tools.Result {
		return tools.Failure(tools.CodeNotFound, "missing")
	}}
	store, res, err := runTask(t, cfg, comp, ts)
	if res.Status != StopStuck {
		t.Fatalf("status = %q, want %q", res.Status, StopStuck)
	}
	if err == nil {
		t.Fatal("Run error = nil, want stuck error")
	}
	if res.CumulativeTurns != 5 {
		t.Fatalf("turns = %d, want 5", res.CumulativeTurns)
	}
	if len(ts.calls) != 5 {
		t.Fatalf("tool calls = %d, want 5", len(ts.calls))
	}
	if got := len(eventsByType(store, session.TypeToolResult)); got != 5 {
		t.Fatalf("tool_result events = %d, want 5", got)
	}
	hints := 0
	for _, ev := range eventsByType(store, session.TypeUserMessage) {
		if decodePayload[userMessageData](t, ev).Source == sourceStuckHint {
			hints++
		}
	}
	if hints != 1 {
		t.Fatalf("stuck hints = %d, want 1", hints)
	}
	if got := len(eventsByType(store, session.TypeLimit)); got != 0 {
		t.Fatalf("limit events = %d, want 0", got)
	}
	assertExactlyOne(t, store, session.TypeFinal)
	assertExactlyOne(t, store, session.TypeRunEnd)
}

func TestRunParentCanceledMapsToInterrupted(t *testing.T) {
	cfg := testConfig()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store := &fakeStore{}
	comp := &fakeCompleter{}
	runner := newRunner(cfg, comp, &fakeToolset{}, store)
	startTestSession(t, runner)
	res, err := runner.Run(ctx, defaultRunOptions())
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopInterrupted || res.CumulativeTurns != 0 {
		t.Fatalf("result = %+v", res)
	}
	if comp.calls != 0 {
		t.Fatalf("model calls = %d, want 0", comp.calls)
	}
	if got := countEvents(store, session.TypeModelResponse); got != 0 {
		t.Fatalf("model_response events = %d, want 0", got)
	}
	if got := countEvents(store, session.TypeError); got != 0 {
		t.Fatalf("error events = %d, want 0", got)
	}
	assertExactlyOne(t, store, session.TypeFinal)
	assertExactlyOne(t, store, session.TypeRunEnd)
}

func TestRunSessionTimeoutMapsToTimeLimit(t *testing.T) {
	synctestTest(t, func(t *testing.T) {
		cfg := testConfig()
		cfg.SessionTimeout = 5 * time.Second
		cfg.ModelTimeout = 0
		comp := &fakeCompleter{steps: []scriptStep{{blockOnContext: true}}}
		store := &fakeStore{}
		runner := newRunner(cfg, comp, &fakeToolset{}, store)
		startTestSession(t, runner)
		outcome := make(chan runOutcome, 1)
		go func() {
			res, err := runner.Run(t.Context(), defaultRunOptions())
			outcome <- runOutcome{res: res, err: err}
		}()
		got := <-outcome
		if got.err != nil {
			t.Fatalf("Run error = %v, want nil", got.err)
		}
		if got.res.Status != StopTimeLimit || got.res.CumulativeTurns != 0 {
			t.Fatalf("result = %+v", got.res)
		}
		limits := eventsByType(store, session.TypeLimit)
		if len(limits) != 1 {
			t.Fatalf("limit events = %d, want 1", len(limits))
		}
		data := decodePayload[limitData](t, limits[0])
		if data.Kind != limitSessionTimeout || data.Configured != 5000 || data.Observed < 5000 {
			t.Fatalf("limit = %+v", data)
		}
		assertExactlyOne(t, store, session.TypeFinal)
		assertExactlyOne(t, store, session.TypeRunEnd)
		end := decodePayload[runEndData](t, eventsByType(store, session.TypeRunEnd)[0])
		if !strings.Contains(end.Reason, "-session-timeout 5s") {
			t.Fatalf("run_end reason = %q, want the flag", end.Reason)
		}
	})
}

func TestRunParentDeadlineMapsToTimeLimit(t *testing.T) {
	synctestTest(t, func(t *testing.T) {
		cfg := testConfig()
		cfg.SessionTimeout = 0
		cfg.ModelTimeout = 0
		comp := &fakeCompleter{steps: []scriptStep{{blockOnContext: true}}}
		store := &fakeStore{}
		runner := newRunner(cfg, comp, &fakeToolset{}, store)
		startTestSession(t, runner)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		outcome := make(chan runOutcome, 1)
		go func() {
			res, err := runner.Run(ctx, defaultRunOptions())
			outcome <- runOutcome{res: res, err: err}
		}()
		got := <-outcome
		if got.err != nil {
			t.Fatalf("Run error = %v, want nil", got.err)
		}
		if got.res.Status != StopTimeLimit {
			t.Fatalf("status = %q, want %q", got.res.Status, StopTimeLimit)
		}
		limits := eventsByType(store, session.TypeLimit)
		if len(limits) != 0 {
			t.Fatalf("limit events = %d, want none without an explicit budget", len(limits))
		}
		assertExactlyOne(t, store, session.TypeFinal)
		assertExactlyOne(t, store, session.TypeRunEnd)
	})
}

func TestRunParentDeadlineExplicitSessionTimeoutEvents(t *testing.T) {
	synctestTest(t, func(t *testing.T) {
		cfg := testConfig()
		cfg.SessionTimeout = 5 * time.Second
		cfg.ModelTimeout = 0
		comp := &fakeCompleter{steps: []scriptStep{{blockOnContext: true}}}
		store := &fakeStore{}
		runner := newRunner(cfg, comp, &fakeToolset{}, store)
		startTestSession(t, runner)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		outcome := make(chan runOutcome, 1)
		go func() {
			res, err := runner.Run(ctx, defaultRunOptions())
			outcome <- runOutcome{res: res, err: err}
		}()
		got := <-outcome
		if got.err != nil {
			t.Fatalf("Run error = %v, want nil", got.err)
		}
		if got.res.Status != StopTimeLimit {
			t.Fatalf("status = %q, want %q", got.res.Status, StopTimeLimit)
		}
		limits := eventsByType(store, session.TypeLimit)
		if len(limits) != 1 || decodePayload[limitData](t, limits[0]).Kind != limitSessionTimeout {
			t.Fatalf("limits = %+v", limits)
		}
	})
}

func TestRunContextOverflowCompactionRetry(t *testing.T) {
	cfg := testConfig()
	cfg.Limits.MaxInputTokens = 11000
	cfg.Limits.CompactionTarget = 11000
	cfg.Limits.MaxContextTokens = 20000

	var steps []scriptStep
	for i := 1; i <= 5; i++ {
		steps = append(steps, scriptStep{resp: model.Response{
			Text:         fmt.Sprintf("turn %d", i),
			ToolCalls:    []model.ToolCall{toolCall(fmt.Sprintf("call-%d", i), "read_file", `{"path":"file"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 1, OutputTokens: 1},
		}})
	}
	steps = append(steps,
		scriptStep{err: &model.ContextOverflowError{Err: errors.New("too many tokens")}},
		scriptStep{resp: model.Response{Text: "finished", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 2, OutputTokens: 3}}},
	)
	comp := &fakeCompleter{steps: steps}
	ts := &fakeToolset{exec: func(context.Context, string, json.RawMessage) tools.Result {
		return tools.Success(exactOutput(8000))
	}}
	store, res, err := runTask(t, cfg, comp, ts)
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopCompleted || res.CumulativeTurns != 6 || res.FinalText != "finished" {
		t.Fatalf("result = %+v", res)
	}
	if comp.calls != 7 {
		t.Fatalf("model calls = %d, want 7", comp.calls)
	}
	if got := len(eventsByType(store, session.TypeModelResponse)); got != 6 {
		t.Fatalf("model_response events = %d, want 6", got)
	}
	if got := len(eventsByType(store, session.TypeToolResult)); got != 5 {
		t.Fatalf("tool_result events = %d, want 5", got)
	}
	compactions := eventsByType(store, session.TypeCompaction)
	foundOverflow := false
	for _, ev := range compactions {
		data := decodePayload[compactionData](t, ev)
		if data.Policy != policyProviderOverflow {
			continue
		}
		foundOverflow = true
		if data.ElidedResults != 1 {
			t.Fatalf("overflow compaction elided = %d, want 1", data.ElidedResults)
		}
		if data.BeforeTokens <= data.AfterTokens {
			t.Fatalf("overflow compaction before=%d after=%d, want before > after", data.BeforeTokens, data.AfterTokens)
		}
		if data.AfterTokens > 9900 {
			t.Fatalf("overflow compaction after = %d, want <= 9900", data.AfterTokens)
		}
	}
	if !foundOverflow {
		t.Fatal("no provider-overflow compaction event")
	}
	retryReq := comp.requests[6]
	foundStub := false
	for _, msg := range retryReq.Messages {
		if msg.Role == model.RoleTool && msg.ToolCallID == "call-1" && strings.Contains(msg.Content, "elided tool result") {
			foundStub = true
		}
	}
	if !foundStub {
		t.Fatalf("overflow retry request does not contain the elided stub: %+v", retryReq.Messages)
	}
}

func TestRunContextOverflowWithoutElisionStops(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{{err: &model.ContextOverflowError{Err: errors.New("too many tokens")}}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopTokenLimit || res.CumulativeTurns != 0 {
		t.Fatalf("result = %+v", res)
	}
	if comp.calls != 1 {
		t.Fatalf("model calls = %d, want 1", comp.calls)
	}
	compactions := eventsByType(store, session.TypeCompaction)
	if len(compactions) != 1 {
		t.Fatalf("compaction events = %d, want 1", len(compactions))
	}
	if got := decodePayload[compactionData](t, compactions[0]); got.Policy != policyProviderOverflow || got.ElidedResults != 0 {
		t.Fatalf("compaction = %+v", got)
	}
	limits := eventsByType(store, session.TypeLimit)
	if len(limits) != 1 || decodePayload[limitData](t, limits[0]).Kind != limitContextOverflow {
		t.Fatalf("limits = %+v", limits)
	}
	assertExactlyOne(t, store, session.TypeFinal)
}

func TestRunContextOverflowRetryFailureStopsWithTokenLimit(t *testing.T) {
	cfg := testConfig()
	cfg.Limits.MaxInputTokens = 11000
	cfg.Limits.CompactionTarget = 11000
	cfg.Limits.MaxContextTokens = 20000

	var steps []scriptStep
	for i := 1; i <= 5; i++ {
		steps = append(steps, scriptStep{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall(fmt.Sprintf("call-%d", i), "read_file", `{"path":"file"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 1, OutputTokens: 1},
		}})
	}
	steps = append(steps,
		scriptStep{err: &model.ContextOverflowError{Err: errors.New("too many tokens")}},
		scriptStep{err: &model.ProtocolError{Err: errors.New("bad retry response")}},
	)
	comp := &fakeCompleter{steps: steps}
	ts := &fakeToolset{exec: func(context.Context, string, json.RawMessage) tools.Result {
		return tools.Success(exactOutput(8000))
	}}
	store, res, err := runTask(t, cfg, comp, ts)
	if err != nil {
		t.Fatalf("Run error = %v, want nil", err)
	}
	if res.Status != StopTokenLimit {
		t.Fatalf("status = %q, want %q", res.Status, StopTokenLimit)
	}
	if res.CumulativeTurns != 5 {
		t.Fatalf("turns = %d, want 5", res.CumulativeTurns)
	}
	errs := eventsByType(store, session.TypeError)
	if len(errs) != 1 || decodePayload[errorData](t, errs[0]).Class != errorClassOverflow {
		t.Fatalf("error events = %+v", errs)
	}
	limits := eventsByType(store, session.TypeLimit)
	if len(limits) != 1 || decodePayload[limitData](t, limits[0]).Kind != limitContextOverflow {
		t.Fatalf("limits = %+v", limits)
	}
}
