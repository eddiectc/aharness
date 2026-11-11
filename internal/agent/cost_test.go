package agent

import (
	"encoding/json"
	"testing"

	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

func testPricing() model.Pricing {
	return model.Pricing{
		Available:          true,
		Currency:           model.PricingCurrencyUSD,
		Unit:               model.PricingUnitPerMillionTokens,
		InputMicroUSD:      150000,
		OutputMicroUSD:     600000,
		CacheReadMicroUSD:  3000,
		CacheWriteMicroUSD: 180000,
		Source:             "https://models.dev/api.json",
	}
}

func TestRunnerRecordsPricedResponse(t *testing.T) {
	cfg := testConfig()
	cfg.Pricing = testPricing()
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		Text:         "done",
		FinishReason: model.FinishStop,
		Usage:        model.Usage{InputTokens: 1000, OutputTokens: 200, Reported: true},
	}}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Status != StopCompleted {
		t.Fatalf("status = %s, want completed", res.Status)
	}
	if res.RunCostMicroUSD != 270 || res.CostMicroUSD != 270 {
		t.Errorf("result cost = run %d cumulative %d, want 270/270", res.RunCostMicroUSD, res.CostMicroUSD)
	}
	if !res.RunCostComplete || !res.CostComplete {
		t.Errorf("result completeness = run %v cumulative %v, want true/true", res.RunCostComplete, res.CostComplete)
	}

	responses := eventsByType(store, session.TypeModelResponse)
	if len(responses) != 1 {
		t.Fatalf("model_response events = %d, want 1", len(responses))
	}
	data := decodePayload[modelResponseData](t, responses[0])
	if data.CostMicroUSD == nil || *data.CostMicroUSD != 270 {
		t.Errorf("model_response cost = %v, want 270", data.CostMicroUSD)
	}

	start := decodePayload[runStartData](t, eventsByType(store, session.TypeRunStart)[0])
	if start.Pricing != cfg.Pricing {
		t.Errorf("run_start pricing = %+v, want %+v", start.Pricing, cfg.Pricing)
	}
	if start.RequireUsage {
		t.Error("run_start require_usage = true, want false")
	}

	end := decodePayload[runEndData](t, eventsByType(store, session.TypeRunEnd)[0])
	if end.RunCostMicroUSD != 270 || end.CostMicroUSD != 270 {
		t.Errorf("run_end cost = run %d cumulative %d, want 270/270", end.RunCostMicroUSD, end.CostMicroUSD)
	}
	if !end.RunCostComplete || !end.CostComplete {
		t.Errorf("run_end completeness = run %v cumulative %v, want true/true", end.RunCostComplete, end.CostComplete)
	}

	state, err := Inspect(store.Events())
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if state.CostMicroUSD != 270 || !state.CostComplete {
		t.Errorf("state cost = %d complete %v, want 270/true", state.CostMicroUSD, state.CostComplete)
	}
	if state.Pricing != cfg.Pricing {
		t.Errorf("state pricing = %+v, want %+v", state.Pricing, cfg.Pricing)
	}
}

func TestRunnerUnpricedResponseMarksIncomplete(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		Text:         "done",
		FinishReason: model.FinishStop,
		Usage:        model.Usage{InputTokens: 10, OutputTokens: 2, Reported: true},
	}}}}
	store, res, err := runTask(t, cfg, comp, &fakeToolset{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.RunCostComplete || res.CostComplete {
		t.Errorf("result completeness = run %v cumulative %v, want false/false", res.RunCostComplete, res.CostComplete)
	}
	end := decodePayload[runEndData](t, eventsByType(store, session.TypeRunEnd)[0])
	if end.RunCostComplete || end.CostComplete {
		t.Errorf("run_end completeness = run %v cumulative %v, want false/false", end.RunCostComplete, end.CostComplete)
	}
	if data := decodePayload[modelResponseData](t, eventsByType(store, session.TypeModelResponse)[0]); data.CostMicroUSD != nil {
		t.Errorf("model_response cost = %v, want omitted", data.CostMicroUSD)
	}
}

func TestRunnerRequireUsageStopsBeforeTools(t *testing.T) {
	cfg := testConfig()
	cfg.RequireUsage = true
	comp := &fakeCompleter{steps: []scriptStep{{resp: model.Response{
		Text:         "calling",
		FinishReason: model.FinishToolCalls,
		ToolCalls: []model.ToolCall{{
			ID:        "call_1",
			Name:      "read_file",
			Arguments: json.RawMessage(`{"path":"a.go"}`),
		}},
	}}}}
	ts := &fakeToolset{}
	store, res, err := runTask(t, cfg, comp, ts)
	if err == nil {
		t.Fatal("Run() error = nil, want protocol error")
	}
	if res.Status != StopProtocolError {
		t.Errorf("status = %s, want protocol_error", res.Status)
	}
	if len(ts.calls) != 0 {
		t.Errorf("tool calls = %d, want 0 before the usage check", len(ts.calls))
	}
	if countEvents(store, session.TypeModelResponse) != 1 {
		t.Errorf("model_response events = %d, want 1 durable response", countEvents(store, session.TypeModelResponse))
	}
	if countEvents(store, session.TypeToolResult) != 0 {
		t.Errorf("tool_result events = %d, want 0", countEvents(store, session.TypeToolResult))
	}
	errors := eventsByType(store, session.TypeError)
	if len(errors) != 1 {
		t.Fatalf("error events = %d, want 1", len(errors))
	}
	if data := decodePayload[errorData](t, errors[0]); data.Class != errorClassProtocol {
		t.Errorf("error class = %q, want %q", data.Class, errorClassProtocol)
	}
	start := decodePayload[runStartData](t, eventsByType(store, session.TypeRunStart)[0])
	if !start.RequireUsage {
		t.Error("run_start require_usage = false, want true")
	}
}

func TestRunnerAcceptsMissingUsageByDefault(t *testing.T) {
	cfg := testConfig()
	comp := &fakeCompleter{steps: []scriptStep{
		{resp: model.Response{
			Text:         "calling",
			FinishReason: model.FinishToolCalls,
			ToolCalls: []model.ToolCall{{
				ID:        "call_1",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"a.go"}`),
			}},
		}},
		{resp: model.Response{Text: "done", FinishReason: model.FinishStop}},
	}}
	ts := &fakeToolset{}
	store, res, err := runTask(t, cfg, comp, ts)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Status != StopCompleted {
		t.Errorf("status = %s, want completed", res.Status)
	}
	if len(ts.calls) != 1 || ts.calls[0].name != "read_file" {
		t.Errorf("tool calls = %+v, want one read_file", ts.calls)
	}
	data := decodePayload[modelResponseData](t, eventsByType(store, session.TypeModelResponse)[0])
	if data.Usage.Reported {
		t.Error("first response usage.reported = true, want false")
	}
}

func TestStateOldLogCostIncomplete(t *testing.T) {
	runner := newRunner(testConfig(), &fakeCompleter{}, &fakeToolset{}, &fakeStore{})
	startTestSession(t, runner)
	store := runner.store.(*fakeStore)
	response, err := session.NewEvent(session.TypeModelResponse, 1, "assistant", modelResponseData{
		FinishReason: model.FinishStop,
		Usage:        model.Usage{InputTokens: 3, OutputTokens: 2, Reported: true},
	})
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}
	if err := store.Append(response); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	state, err := Inspect(store.Events())
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if state.CostComplete {
		t.Error("CostComplete = true for an old response without cost, want false")
	}
	if state.CostMicroUSD != 0 {
		t.Errorf("CostMicroUSD = %d, want 0", state.CostMicroUSD)
	}
}

func TestStateNoResponsesCostComplete(t *testing.T) {
	runner := newRunner(testConfig(), &fakeCompleter{}, &fakeToolset{}, &fakeStore{})
	startTestSession(t, runner)
	state, err := Inspect(runner.store.Events())
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if !state.CostComplete {
		t.Error("CostComplete = false for a session with no responses, want true")
	}
}

var _ = tools.Success
