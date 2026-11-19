package aharness_test

import (
	"context"
	"strings"
	"testing"

	"github.com/eddiectc/aharness"
	"github.com/eddiectc/aharness/agent"
)

func TestIdentityParentAndChildPrompts(t *testing.T) {
	parentModel := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "delegate-1",
				Name:      "delegate",
				Arguments: delegateArgs(map[string]string{"agent": "analyst", "task": "read the data"}),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true},
		},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true}},
	}}
	modelCfg := testModel(parentModel)
	modelCfg.NewCompleter = func(string) aharness.Model {
		return &scriptedModel{}
	}
	tools := &aharness.ToolsConfig{
		Delegation: &aharness.DelegationConfig{Agents: []aharness.AgentDefinition{{
			Name:        "analyst",
			Description: "Read-only data analyst.",
			Prompt:      "Read the assigned data and report.",
			Tools:       []aharness.ToolName{"read_file"},
		}}},
	}
	workspace, stateDir := t.TempDir(), t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Identity:  "acode",
		Model:     modelCfg,
		Tools:     tools,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()

	run, err := ag.Start(context.Background(), "delegate", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var childID string
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		if started, ok := ev.(aharness.SubagentStarted); ok {
			childID = started.ChildSession
		}
	}
	if childID == "" {
		t.Fatal("no child session was observed")
	}

	state, err := ag.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	wantParent := `You are acode, a coding harness working in the workspace "` + ag.Workspace() + `".`
	if first, _, _ := strings.Cut(state.SystemPrompt, "\n"); first != wantParent {
		t.Fatalf("parent prompt first line = %q, want %q", first, wantParent)
	}

	childState, err := aharness.InspectSession(context.Background(), stateDir, childID)
	if err != nil {
		t.Fatalf("InspectSession child: %v", err)
	}
	wantChild := `You are the acode "analyst" agent, working in the workspace "` + ag.Workspace() + `".`
	if first, _, _ := strings.Cut(childState.SystemPrompt, "\n"); first != wantChild {
		t.Fatalf("child prompt first line = %q, want %q", first, wantChild)
	}
	if strings.Contains(childState.SystemPrompt, "You are the aharness") {
		t.Fatalf("child prompt still names aharness: %q", childState.SystemPrompt)
	}
}

func TestComposedSystemPromptKeepsLoopInvariants(t *testing.T) {
	catalog := []agent.AgentInfo{
		{Name: "researcher", Description: "Research a question over the data."},
		{Name: "verifier", Description: "Verify a claim against the data."},
	}
	hostPolicy := []string{
		"- This product answers research questions over the workspace data.",
		"- Cite the files you read and state uncertainty plainly.",
	}
	prompt := agent.LoopRules() + "\n" + strings.Join(hostPolicy, "\n") +
		"\n\nAvailable agents:\n" + agent.AgentCatalogText(catalog)

	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace:    t.TempDir(),
		StateDir:     t.TempDir(),
		UserAgent:    "aharness-test/1.0",
		Identity:     "acode",
		Model:        testModel(&scriptedModel{}),
		SystemPrompt: prompt,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	if _, err := ag.Run(context.Background(), "summarize the data", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	state, err := ag.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	for _, line := range strings.Split(agent.LoopRules(), "\n") {
		if !strings.Contains(state.SystemPrompt, line) {
			t.Fatalf("recorded prompt is missing invariant %q: %q", line, state.SystemPrompt)
		}
	}
	if strings.Contains(state.SystemPrompt, "aharness") {
		t.Fatalf("recorded prompt names aharness: %q", state.SystemPrompt)
	}
	for _, line := range []string{"- researcher: Research a question over the data.\n", "- verifier: Verify a claim against the data.\n"} {
		if !strings.Contains(state.SystemPrompt, line) {
			t.Fatalf("recorded prompt is missing catalog line %q: %q", line, state.SystemPrompt)
		}
	}
}
