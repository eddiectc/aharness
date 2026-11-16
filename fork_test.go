package aharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/eddiectc/aharness"
)

func TestForkReplaysHistoryWithToolAbsentFromNewGrant(t *testing.T) {
	called := 0
	tools := customTools(t, &called)
	model := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls:    []aharness.ToolCall{{ID: "legacy-1", Name: "lookup", Arguments: json.RawMessage(`{"id":"x"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "first done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	ag, workspace, stateDir := newTestAgent(t, tools, model)
	parentID := ag.ID()
	if _, err := ag.Run(context.Background(), "use lookup", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called != 1 {
		t.Fatalf("lookup called %d", called)
	}
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	forkModel := &scriptedModel{responses: []aharness.Response{
		{Text: "after fork", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	forked, err := aharness.Fork(context.Background(), aharness.Config{
		StateDir:  stateDir,
		Workspace: workspace,
		Model:     testModel(forkModel),
		Tools:     &aharness.ToolsConfig{},
	}, parentID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = forked.Close() }()
	state, err := forked.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if len(state.CustomTools) != 0 {
		t.Fatalf("forked custom tools = %+v", state.CustomTools)
	}
	if state.ContractOverride == nil || !state.ContractOverride.Tools {
		t.Fatalf("contract override = %+v", state.ContractOverride)
	}
	result, err := forked.Run(context.Background(), "continue", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("forked Run: %v", err)
	}
	if result.Status != aharness.StopCompleted || result.FinalText != "after fork" {
		t.Fatalf("forked result = %+v", result)
	}
}

func TestForkContractOverrideAndResumeRejection(t *testing.T) {
	called := 0
	tools := customTools(t, &called)
	model := &scriptedModel{responses: []aharness.Response{
		{Text: "first", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	ag, workspace, stateDir := newTestAgent(t, tools, model)
	parentID := ag.ID()
	if _, err := ag.Run(context.Background(), "one", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	forkModel := &scriptedModel{responses: []aharness.Response{
		{Text: "forked", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	newTools := customTools(t, &called)
	newTools.Disable = []aharness.ToolName{aharness.ToolRunCommand}
	forked, err := aharness.Fork(context.Background(), aharness.Config{
		StateDir:     stateDir,
		Workspace:    workspace,
		Model:        testModel(forkModel),
		Tools:        newTools,
		SystemPrompt: "you are an analyst",
	}, parentID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = forked.Close() }()
	if forked.ID() == parentID {
		t.Fatal("fork reused the parent session ID")
	}
	state, err := forked.State()
	if err != nil {
		t.Fatalf("fork State: %v", err)
	}
	if state.SystemPrompt != "you are an analyst" {
		t.Fatalf("fork prompt = %q", state.SystemPrompt)
	}
	if state.ContractOverride == nil {
		t.Fatal("fork did not record contract_override")
	}
	if !state.ContractOverride.Prompt || !state.ContractOverride.Tools {
		t.Fatalf("contract override = %+v", state.ContractOverride)
	}
	if state.ContractOverride.ParentSession != parentID {
		t.Fatalf("override parent = %q", state.ContractOverride.ParentSession)
	}
	if state.Parent == nil || state.Parent.Session != parentID {
		t.Fatalf("fork parent metadata = %+v", state.Parent)
	}
	for _, name := range state.Tools {
		if name == aharness.ToolRunCommand {
			t.Fatal("forked grant still contains run_command")
		}
	}
	parentState, err := aharness.InspectSession(context.Background(), stateDir, parentID)
	if err != nil {
		t.Fatalf("InspectSession parent: %v", err)
	}
	if parentState.SystemPrompt == "you are an analyst" {
		t.Fatal("fork rewrote the parent prompt")
	}

	if _, err := aharness.Resume(context.Background(), aharness.Config{
		StateDir:     stateDir,
		Model:        testModel(&scriptedModel{}),
		Tools:        tools,
		SystemPrompt: "changed",
	}, parentID); err == nil {
		t.Fatal("Resume accepted a prompt override")
	} else {
		if _, ok := errors.AsType[*aharness.ConfigError](err); !ok {
			t.Fatalf("Resume prompt error = %v, want ConfigError", err)
		}
	}
	changed := customTools(t, &called)
	changed.Disable = []aharness.ToolName{aharness.ToolEditFile}
	if _, err := aharness.Resume(context.Background(), aharness.Config{
		StateDir: stateDir,
		Model:    testModel(&scriptedModel{}),
		Tools:    changed,
	}, parentID); !errors.Is(err, aharness.ErrToolMismatch) {
		t.Fatalf("Resume changed tools error = %v, want ErrToolMismatch", err)
	}
}
