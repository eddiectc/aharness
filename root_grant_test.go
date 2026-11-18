package aharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/eddiectc/aharness"
)

// childGrantModel calls dcf_solve once, then answers.
type childGrantModel struct {
	mu       sync.Mutex
	calls    int
	requests []aharness.Request
}

func (m *childGrantModel) Complete(_ context.Context, req aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	m.calls++
	if m.calls == 1 {
		return aharness.Response{
			ToolCalls:    []aharness.ToolCall{{ID: "child-call", Name: "dcf_solve", Arguments: json.RawMessage(`{"intent":"solve"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		}, nil
	}
	return aharness.Response{Text: "child done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}}, nil
}

func toolNames(schemas []aharness.ToolSchema) []string {
	names := make([]string, len(schemas))
	for i, schema := range schemas {
		names[i] = schema.Name
	}
	return names
}

func TestRootGrantNarrowsRootWithoutNarrowingChildren(t *testing.T) {
	var solves int
	dcf := aharness.Tool{
		Name:        "dcf_solve",
		Description: "Solve a DCF.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"intent":{"type":"string"}},"required":["intent"],"additionalProperties":false}`),
		Run: func(context.Context, aharness.ToolCall) (aharness.ToolResult, error) {
			solves++
			return aharness.TextResult("solved"), nil
		},
	}
	tools := func(root []aharness.ToolName) *aharness.ToolsConfig {
		return &aharness.ToolsConfig{
			Extra:     []aharness.Tool{dcf},
			RootGrant: root,
			Delegation: &aharness.DelegationConfig{Agents: []aharness.AgentDefinition{{
				Name:        "analyst",
				Description: "Broad analyst.",
				Prompt:      "Solve the task.",
				Tools:       []aharness.ToolName{"dcf_solve"},
			}}},
		}
	}
	parentModel := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls:    []aharness.ToolCall{{ID: "edit-call", Name: "edit_file", Arguments: json.RawMessage(`{"path":"x","old_string":"","new_string":"x"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "delegate-call",
				Name:      "delegate",
				Arguments: delegateArgs(map[string]string{"agent": "analyst", "task": "solve it"}),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	childModel := &childGrantModel{}
	modelCfg := testModel(parentModel)
	modelCfg.NewCompleter = func(string) aharness.Model { return childModel }
	workspace, stateDir := t.TempDir(), t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     modelCfg,
		Tools:     tools([]aharness.ToolName{aharness.ToolReadFile, aharness.ToolDelegate}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var (
		editCode   string
		childID    string
		delegateOK bool
	)
	run, err := ag.Start(context.Background(), "delegate the work", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		switch ev := ev.(type) {
		case aharness.SubagentStarted:
			childID = ev.ChildSession
		case aharness.ToolCompleted:
			switch ev.Name {
			case "edit_file":
				editCode = ev.Code
			case "delegate":
				delegateOK = ev.OK
			}
		}
	}
	if editCode != "unknown_tool" {
		t.Fatalf("root edit_file code = %q, want unknown_tool", editCode)
	}
	if !delegateOK {
		t.Fatal("root delegate failed")
	}
	if solves == 0 {
		t.Fatal("child never executed dcf_solve")
	}
	if childID == "" {
		t.Fatal("no child session")
	}

	// The parent only ever saw the root grant.
	gotRootTools := toolNames(parentModel.requests[0].Tools)
	if len(gotRootTools) != 2 || gotRootTools[0] != "read_file" || gotRootTools[1] != "delegate" {
		t.Fatalf("root tools = %v, want [read_file delegate]", gotRootTools)
	}
	// The child saw its own definition grant, not the root grant.
	childTools := toolNames(childModel.requests[0].Tools)
	if len(childTools) != 1 || childTools[0] != "dcf_solve" {
		t.Fatalf("child tools = %v, want [dcf_solve]", childTools)
	}

	state, err := ag.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if len(state.RootTools) != 2 || state.RootTools[0] != aharness.ToolReadFile || state.RootTools[1] != aharness.ToolDelegate {
		t.Fatalf("State.RootTools = %v", state.RootTools)
	}
	if !containsTool(state.Tools, aharness.ToolEditFile) || !containsTool(state.Tools, "dcf_solve") {
		t.Fatalf("State.Tools = %v, want the full session grant", state.Tools)
	}
	childState, err := aharness.InspectSession(context.Background(), stateDir, childID)
	if err != nil {
		t.Fatalf("InspectSession: %v", err)
	}
	if len(childState.Tools) != 1 || childState.Tools[0] != "dcf_solve" {
		t.Fatalf("child Tools = %v, want [dcf_solve]", childState.Tools)
	}
	if childState.RootTools != nil {
		t.Fatalf("child RootTools = %v, want nil", childState.RootTools)
	}

	sessionID := ag.ID()
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Resume without RootGrant inherits the recorded one.
	resumed, err := aharness.Resume(context.Background(), aharness.Config{
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     tools(nil),
	}, sessionID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	resumedState, err := resumed.State()
	if err != nil {
		t.Fatalf("resumed State: %v", err)
	}
	if len(resumedState.RootTools) != 2 || resumedState.RootTools[0] != aharness.ToolReadFile {
		t.Fatalf("resumed RootTools = %v", resumedState.RootTools)
	}
	_ = resumed.Close()
	// Resume with a different RootGrant is a mismatch.
	_, err = aharness.Resume(context.Background(), aharness.Config{
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     tools([]aharness.ToolName{aharness.ToolReadFile}),
	}, sessionID)
	if !errors.Is(err, aharness.ErrToolMismatch) {
		t.Fatalf("Resume with a different RootGrant = %v, want ErrToolMismatch", err)
	}
}

func containsTool(names []aharness.ToolName, want aharness.ToolName) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestRootGrantValidation(t *testing.T) {
	model := testModel(&scriptedModel{})
	cases := []struct {
		name  string
		tools *aharness.ToolsConfig
	}{
		{name: "unknown", tools: &aharness.ToolsConfig{RootGrant: []aharness.ToolName{"nope"}}},
		{name: "duplicate", tools: &aharness.ToolsConfig{RootGrant: []aharness.ToolName{aharness.ToolReadFile, aharness.ToolReadFile}}},
		{name: "delegate without delegation", tools: &aharness.ToolsConfig{RootGrant: []aharness.ToolName{aharness.ToolDelegate}}},
		{name: "disabled", tools: &aharness.ToolsConfig{Disable: []aharness.ToolName{aharness.ToolEditFile}, RootGrant: []aharness.ToolName{aharness.ToolEditFile}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := aharness.New(context.Background(), aharness.Config{
				Workspace: t.TempDir(),
				StateDir:  t.TempDir(),
				UserAgent: "aharness-test/1.0",
				Model:     model,
				Tools:     tc.tools,
			})
			var configErr *aharness.ConfigError
			if !errors.As(err, &configErr) || configErr.Field != "Tools.RootGrant" {
				t.Fatalf("New = %v, want ConfigError for Tools.RootGrant", err)
			}
		})
	}
}
