package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/internal/model"
	"github.com/eddiectc/aharness/internal/session"
	"github.com/eddiectc/aharness/internal/tools"
)

func delegateToolResult(turns, input, output int, cost int64, complete bool) tools.Result {
	details := tools.DelegateDetails{
		Tasks: []tools.DelegateTaskDetails{{
			Agent: "explore", Status: tools.SubagentCompleted, Turns: turns,
			Usage:        model.Usage{InputTokens: input, OutputTokens: output},
			CostMicroUSD: cost, CostComplete: complete,
		}},
		Status:               tools.SubagentCompleted,
		SubagentTurns:        turns,
		SubagentUsage:        model.Usage{InputTokens: input, OutputTokens: output},
		SubagentCostMicroUSD: cost,
		SubagentCostComplete: complete,
	}
	data, err := json.Marshal(details)
	if err != nil {
		panic(err)
	}
	return tools.Result{OK: true, Code: tools.CodeOK, Output: "delegated", Details: data}
}

func TestSessionStartRecordsTheRunContract(t *testing.T) {
	cfg := testConfig()
	cfg.SystemPrompt = "recorded prompt"
	cfg.Agent = "reviewer"
	cfg.Tools = []string{"read_file", "search"}
	store := &fakeStore{}
	runner := newRunner(cfg, &fakeCompleter{steps: []scriptStep{completedStep("done", model.Usage{})}}, &fakeToolset{}, store)
	if err := runner.StartSession(&ParentRef{Session: "parent", Seq: 3, CallID: "call-9"}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	start := decodePayload[sessionStartData](t, store.events[0])
	if start.SystemPrompt != "recorded prompt" || start.Agent != "reviewer" {
		t.Fatalf("start = %+v", start)
	}
	if strings.Join(start.Tools, ",") != "read_file,search" {
		t.Fatalf("tools = %v", start.Tools)
	}
	if start.ParentSession != "parent" || start.ParentSeq != 3 || start.ParentCallID != "call-9" {
		t.Fatalf("parent ref = %+v", start)
	}
}

func TestRunnerFoldsSubagentAccounting(t *testing.T) {
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("d1", "delegate", `{"intent":"go","tasks":[{"agent":"explore","task":"t"}]}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 10, OutputTokens: 2, Reported: true},
		}},
		completedStep("done", model.Usage{InputTokens: 10, OutputTokens: 2, Reported: true}),
	}}
	ts := &fakeToolset{exec: func(_ context.Context, name string, _ json.RawMessage) tools.Result {
		if name == "delegate" {
			return delegateToolResult(4, 400, 100, 900, true)
		}
		return tools.Success("ok")
	}}
	cfg := testConfig()
	cfg.MaxTotalTokens = 0
	store, res, err := runTask(t, cfg, comp, ts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	if res.SubagentTurns != 4 || res.SubagentUsage.Total() != 500 || res.SubagentCostMicroUSD != 900 || !res.SubagentCostComplete {
		t.Fatalf("result subagent totals = %+v", res)
	}
	if res.RunTurns != 2 {
		t.Fatalf("run turns = %d, want the parent's own two responses", res.RunTurns)
	}
	end := decodePayload[runEndData](t, eventsByType(store, session.TypeRunEnd)[0])
	if end.SubagentTurns != 4 || end.SubagentUsage.Total() != 500 || end.SubagentCostMicroUSD != 900 || !end.SubagentCostComplete {
		t.Fatalf("run_end subagent totals = %+v", end)
	}

	state, err := Inspect(store.events)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.SubagentTurns != 4 || state.SubagentUsage.Total() != 500 || state.SubagentCostMicroUSD != 900 || !state.SubagentCostComplete {
		t.Fatalf("Inspect subagent totals = %+v", state)
	}
	if state.Usage.Total() != res.CumulativeUsage.Total() {
		t.Fatalf("Inspect usage = %+v, want the parent's own usage", state.Usage)
	}
}

func TestRunnerSubagentTokensCountAgainstTheBudget(t *testing.T) {
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("d1", "delegate", `{"intent":"go","tasks":[{"agent":"explore","task":"t"}]}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 100, OutputTokens: 20, Reported: true},
		}},
		completedStep("should not run", model.Usage{InputTokens: 1, OutputTokens: 1}),
	}}
	ts := &fakeToolset{exec: func(_ context.Context, name string, _ json.RawMessage) tools.Result {
		if name == "delegate" {
			return delegateToolResult(2, 300, 50, 0, true)
		}
		return tools.Success("ok")
	}}
	cfg := testConfig()
	cfg.MaxTotalTokens = 400
	cfg.MaxTurns = 0
	_, res, err := runTask(t, cfg, comp, ts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopTokenLimit {
		t.Fatalf("status = %s, want token_limit", res.Status)
	}
	if comp.calls != 1 {
		t.Fatalf("model calls = %d, want the delegate call only", comp.calls)
	}
}

func TestRunnerUnpricedChildClearsCostCompleteness(t *testing.T) {
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("d1", "delegate", `{"intent":"go","tasks":[{"agent":"explore","task":"t"}]}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		}},
		completedStep("done", model.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}),
	}}
	ts := &fakeToolset{exec: func(_ context.Context, name string, _ json.RawMessage) tools.Result {
		if name == "delegate" {
			return delegateToolResult(1, 10, 5, 0, false)
		}
		return tools.Success("ok")
	}}
	store, res, err := runTask(t, testConfig(), comp, ts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SubagentCostComplete || res.RunCostComplete || res.CostComplete {
		t.Fatalf("completeness = subagent %v run %v cumulative %v, want all false",
			res.SubagentCostComplete, res.RunCostComplete, res.CostComplete)
	}
	state, err := Inspect(store.events)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.SubagentCostComplete {
		t.Fatal("Inspect reports a complete subagent cost after an unpriced child")
	}
}

func TestResumeKeepsIncompleteChildCost(t *testing.T) {
	store := &fakeStore{}
	events := []session.Event{
		startEvent(t, "/workspace"),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
		newTestEvent(t, session.TypeToolResult, 1, string(model.RoleTool), toolResultData{
			CallID: "d1", Name: "delegate", OK: true, Code: tools.CodeOK,
			Details: json.RawMessage(`{"tasks":[{"agent":"explore","status":"completed","usage":{"input_tokens":5,"output_tokens":5},"cost_complete":false}],"status":"completed","subagent_turns":1,"subagent_usage":{"input_tokens":5,"output_tokens":5},"subagent_cost_complete":false}`),
		}),
	}
	store.events = events
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{completedStep("done", model.Usage{InputTokens: 1, OutputTokens: 1, Reported: true})}}
	runner := newRunner(cfg, comp, &fakeToolset{}, store)
	res, err := runner.Run(t.Context(), RunOptions{Task: "continue", TaskSet: true, TaskFrom: "-task", ResumedFromSeq: uint64(len(events))})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SubagentTurns != 1 || res.SubagentUsage.Total() != 10 {
		t.Fatalf("subagent totals = %d/%+v, want the recorded child", res.SubagentTurns, res.SubagentUsage)
	}
	if res.SubagentCostComplete || res.CostComplete {
		t.Fatalf("completeness = subagent %v cumulative %v, want both false", res.SubagentCostComplete, res.CostComplete)
	}
}

// callRecordingToolset captures the parent-side Call the runner passes to a
// delegate-capable toolset.
type callRecordingToolset struct {
	fakeToolset
	calls []tools.Call
}

func (f *callRecordingToolset) ExecuteCall(ctx context.Context, call tools.Call, name string, args json.RawMessage) tools.Result {
	f.calls = append(f.calls, call)
	return f.Execute(ctx, name, args)
}

func TestDelegateCallSeesPriorChildSpend(t *testing.T) {
	store := &fakeStore{}
	store.events = []session.Event{
		startEvent(t, "/workspace"),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
		newTestEvent(t, session.TypeModelResponse, 1, string(model.RoleAssistant), modelResponseData{
			Text: "prior", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 10, OutputTokens: 5},
		}),
		newTestEvent(t, session.TypeToolResult, 1, string(model.RoleTool), toolResultData{
			CallID: "d0", Name: "delegate", OK: true, Code: tools.CodeOK,
			Details: json.RawMessage(`{"tasks":[{"agent":"explore","status":"completed","usage":{"input_tokens":40,"output_tokens":10},"cost_complete":true}],"status":"completed","subagent_turns":1,"subagent_usage":{"input_tokens":40,"output_tokens":10},"subagent_cost_complete":true}`),
		}),
	}
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("d1", "delegate", `{"intent":"go","tasks":[{"agent":"explore","task":"t"}]}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 10, OutputTokens: 5},
		}},
		completedStep("done", model.Usage{InputTokens: 10, OutputTokens: 5}),
	}}
	ts := &callRecordingToolset{fakeToolset: fakeToolset{exec: func(_ context.Context, name string, _ json.RawMessage) tools.Result {
		if name == "delegate" {
			return delegateToolResult(2, 20, 10, 0, true)
		}
		return tools.Success("ok")
	}}}
	runner := newRunner(testConfig(), comp, ts, store)
	res, err := runner.Run(t.Context(), RunOptions{Task: "continue", TaskSet: true, TaskFrom: "-task"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	if len(ts.calls) != 1 {
		t.Fatalf("delegate calls = %d, want 1", len(ts.calls))
	}
	// The call sees the parent's own usage (15), the prior child's usage (50),
	// and this run's new response (15) recorded before the tool executes.
	if got := ts.calls[0].ParentUsage.Total(); got != 80 {
		t.Fatalf("ParentUsage = %d, want 80 (parent own + prior child usage)", got)
	}
	if ts.calls[0].ID != "d1" || ts.calls[0].Turn == 0 {
		t.Fatalf("call meta = %+v", ts.calls[0])
	}
}

func TestInspectIgnoresMalformedDelegateDetails(t *testing.T) {
	events := []session.Event{
		startEvent(t, "/workspace"),
		newTestEvent(t, session.TypeToolResult, 1, string(model.RoleTool), toolResultData{
			CallID: "d1", Name: "delegate", OK: true, Code: tools.CodeOK,
			Details: json.RawMessage(`{"tasks":"not a list"}`),
		}),
	}
	state, err := Inspect(events)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.SubagentTurns != 0 || state.SubagentUsage.Total() != 0 {
		t.Fatalf("malformed details were folded: %+v", state)
	}
}

func TestInspectFoldsDelegateDetails(t *testing.T) {
	events := []session.Event{
		startEvent(t, "/workspace"),
		newTestEvent(t, session.TypeToolResult, 1, string(model.RoleTool), toolResultData{
			CallID: "d1", Name: "delegate", OK: true, Code: tools.CodeOK,
			Details: json.RawMessage(`{"tasks":[{"agent":"explore","status":"completed"}],"status":"completed","subagent_turns":3,"subagent_usage":{"input_tokens":90,"output_tokens":10},"subagent_cost_micro_usd":42,"subagent_cost_complete":true}`),
		}),
	}
	state, err := Inspect(events)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.SubagentTurns != 3 || state.SubagentUsage.Total() != 100 || state.SubagentCostMicroUSD != 42 || !state.SubagentCostComplete {
		t.Fatalf("state = %+v", state)
	}
}
