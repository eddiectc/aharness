package aharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/eddiectc/aharness"
)

// scriptedModel returns a fixed sequence of responses.
type scriptedModel struct {
	mu        sync.Mutex
	responses []aharness.Response
	errs      []error
	requests  []aharness.Request
}

func (m *scriptedModel) Complete(_ context.Context, req aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	index := len(m.requests) - 1
	if index < len(m.errs) && m.errs[index] != nil {
		return aharness.Response{}, m.errs[index]
	}
	if index >= len(m.responses) {
		return aharness.Response{Text: "done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
	}
	return m.responses[index], nil
}

func customTools(t *testing.T, called *int) *aharness.ToolsConfig {
	t.Helper()
	return &aharness.ToolsConfig{
		Extra: []aharness.Tool{{
			Name:        "lookup",
			Description: "Look up one record by id.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
			Run: func(_ context.Context, call aharness.ToolCall) (aharness.ToolResult, error) {
				*called++
				var args struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(call.Arguments, &args); err != nil {
					return aharness.ToolResult{}, err
				}
				return aharness.TextResult("record:" + args.ID), nil
			},
		}},
	}
}

func testModel(m aharness.Model) *aharness.ModelConfig {
	return aharness.CustomModel(aharness.ModelSpec{
		ID:            "test-model",
		Provider:      "test",
		Dialect:       aharness.DialectCustom,
		ContextWindow: 100_000,
	}, m)
}

func newTestAgent(t *testing.T, tools *aharness.ToolsConfig, model aharness.Model) (*aharness.Agent, string, string) {
	t.Helper()
	workspace := t.TempDir()
	stateDir := t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		Model:     testModel(model),
		Tools:     tools,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })
	return ag, workspace, stateDir
}

func TestRunCustomToolAndState(t *testing.T) {
	called := 0
	model := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls:    []aharness.ToolCall{{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"id":"42"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 10, OutputTokens: 4, Reported: true},
		},
		{Text: "finished", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 12, OutputTokens: 2, Reported: true}},
	}}
	ag, _, _ := newTestAgent(t, customTools(t, &called), model)

	run, err := ag.Start(context.Background(), "find the record", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = run.Close() }()
	var kinds []string
	var finished aharness.Result
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		switch ev := ev.(type) {
		case aharness.RunStarted:
			kinds = append(kinds, "run_started")
		case aharness.ResponseCompleted:
			kinds = append(kinds, "response")
		case aharness.ToolStarted:
			kinds = append(kinds, "tool_started")
			if ev.Call.Name != "lookup" {
				t.Fatalf("tool name = %q", ev.Call.Name)
			}
		case aharness.ToolCompleted:
			kinds = append(kinds, "tool_completed")
			if !ev.OK || ev.Code != "ok" {
				t.Fatalf("tool result = %+v", ev)
			}
		case aharness.RunFinished:
			kinds = append(kinds, "run_finished")
			finished = ev.Result
		}
	}
	if called != 1 {
		t.Fatalf("custom tool calls = %d, want 1", called)
	}
	if finished.Status != aharness.StopCompleted {
		t.Fatalf("status = %q (err %v)", finished.Status, finished.Err)
	}
	if finished.FinalText != "finished" {
		t.Fatalf("final text = %q", finished.FinalText)
	}
	if len(kinds) < 5 {
		t.Fatalf("events = %v", kinds)
	}
	state, err := ag.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if len(state.CustomTools) != 1 || state.CustomTools[0].Name != "lookup" {
		t.Fatalf("recorded custom tools = %+v", state.CustomTools)
	}
	if state.Tools == nil {
		t.Fatal("new library session must record an explicit grant")
	}
	if state.Turns != 2 {
		t.Fatalf("turns = %d, want 2", state.Turns)
	}
}

func TestInspectSessionRejectsInvalidID(t *testing.T) {
	if _, err := aharness.InspectSession(context.Background(), t.TempDir(), "../../etc/passwd"); !errors.Is(err, aharness.ErrInvalidSessionID) {
		t.Fatalf("InspectSession error = %v, want ErrInvalidSessionID", err)
	}
	if _, err := aharness.InspectSession(context.Background(), t.TempDir(), "01a1e4be-0000-7000-8000-000000000000"); !errors.Is(err, aharness.ErrSessionNotFound) {
		t.Fatalf("missing session error = %v, want ErrSessionNotFound", err)
	}
	sessions, err := aharness.Sessions(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %+v", sessions)
	}
}

func TestResumeCustomToolAndInspect(t *testing.T) {
	called := 0
	tools := customTools(t, &called)
	first := &scriptedModel{responses: []aharness.Response{
		{Text: "first", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	ag, workspace, stateDir := newTestAgent(t, tools, first)
	if _, err := ag.Run(context.Background(), "one", aharness.RunOptions{}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	sessionID := ag.ID()
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	state, err := aharness.InspectSession(context.Background(), stateDir, sessionID)
	if err != nil {
		t.Fatalf("InspectSession: %v", err)
	}
	if state.ID != sessionID || state.Workspace == "" || state.Model.ID != "test-model" {
		t.Fatalf("inspect state = %+v", state)
	}
	if len(state.CustomTools) != 1 {
		t.Fatalf("inspect custom tools = %+v", state.CustomTools)
	}

	if _, err := aharness.Resume(context.Background(), aharness.Config{
		StateDir: stateDir,
		Model:    testModel(&scriptedModel{}),
	}, sessionID); !errors.Is(err, aharness.ErrToolMismatch) {
		t.Fatalf("Resume without custom tools error = %v, want ErrToolMismatch", err)
	}

	second := &scriptedModel{responses: []aharness.Response{
		{Text: "second", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	resumed, err := aharness.Resume(context.Background(), aharness.Config{
		StateDir:  stateDir,
		Workspace: workspace,
		Model:     testModel(second),
		Tools:     customTools(t, &called),
	}, sessionID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = resumed.Close() }()
	result, err := resumed.Run(context.Background(), "two", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	if result.Status != aharness.StopCompleted || result.CumulativeTurns < 2 {
		t.Fatalf("resumed result = %+v", result)
	}
	sessions, err := aharness.Sessions(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != sessionID {
		t.Fatalf("sessions = %+v", sessions)
	}
}
