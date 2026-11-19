package aharness_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/eddiectc/aharness"
	"github.com/eddiectc/aharness/agent"
)

func TestIdentityParentAndChildPrompts(t *testing.T) {
	const hostText = "Workspace rules:\n- Read AGENTS.md before editing."
	// plainChildPromptSHA256 pins the no-Instructions child prompt with the
	// workspace normalized to <workspace>. A deliberate prompt change updates it.
	const plainChildPromptSHA256 = "0e45862c20e35654b0655fce79d44920b0104982e88503be86076c81e96624ea"
	var plainChild struct{ prompt, workspace string }
	for _, tc := range []struct {
		name         string
		instructions string
	}{
		{name: "without instructions"},
		{name: "with instructions", instructions: hostText},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				Workspace:    workspace,
				StateDir:     stateDir,
				UserAgent:    "aharness-test/1.0",
				Identity:     "acode",
				Model:        modelCfg,
				Tools:        tools,
				Instructions: tc.instructions,
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
			if state.AppendedPrompt != tc.instructions {
				t.Fatalf("parent AppendedPrompt = %q, want %q", state.AppendedPrompt, tc.instructions)
			}
			if tc.instructions != "" && !strings.HasSuffix(state.SystemPrompt, "\n\n"+tc.instructions) {
				t.Fatalf("parent prompt does not end with the instructions: %q", state.SystemPrompt)
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
			if childState.AppendedPrompt != "" {
				t.Fatalf("child recorded appended_prompt = %q, want empty", childState.AppendedPrompt)
			}
			normalize := func(prompt, workspace string) string {
				return strings.ReplaceAll(prompt, workspace, "<workspace>")
			}
			if tc.instructions == "" {
				if strings.Contains(childState.SystemPrompt, hostText) {
					t.Fatalf("child prompt carries host text without Instructions: %q", childState.SystemPrompt)
				}
				// The no-Instructions child prompt is pinned by hash: v3 body,
				// identity line, and role body unchanged.
				if sum := fmt.Sprintf("%x", sha256.Sum256([]byte(normalize(childState.SystemPrompt, ag.Workspace())))); sum != plainChildPromptSHA256 {
					t.Fatalf("no-Instructions child prompt sha256 = %s, want the golden %s", sum, plainChildPromptSHA256)
				}
				plainChild.prompt, plainChild.workspace = childState.SystemPrompt, ag.Workspace()
				return
			}
			if got := strings.Count(childState.SystemPrompt, tc.instructions); got != 1 {
				t.Fatalf("child prompt carries the instructions %d times: %q", got, childState.SystemPrompt)
			}
			roleAt := strings.Index(childState.SystemPrompt, "## Your role")
			if roleAt < 0 || strings.Index(childState.SystemPrompt, tc.instructions) < roleAt {
				t.Fatalf("child instructions are not after the role body: %q", childState.SystemPrompt)
			}
			// The companion case is byte-identical to this prompt without the
			// appended tail, once each subtest's workspace path is normalized.
			got := strings.TrimSuffix(childState.SystemPrompt, "\n\n"+tc.instructions)
			if normalize(got, ag.Workspace()) != normalize(plainChild.prompt, plainChild.workspace) {
				t.Fatalf("Instructions changed more than the appended tail:\n got %q\nwant %q", got, plainChild.prompt)
			}
		})
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
