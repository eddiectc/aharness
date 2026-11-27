package agent

import (
	"strings"
	"testing"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// TestResolveAgentCarriesImageInput pins the child-model capability plumbing.
// A child on a different model must report that model's capability so its
// registry gate follows its own model, not the parent's.
func TestResolveAgentCarriesImageInput(t *testing.T) {
	no := false
	yes := true
	tests := []struct {
		name     string
		resolved *bool
	}{
		{name: "known text-only", resolved: &no},
		{name: "known image", resolved: &yes},
		{name: "unknown", resolved: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spawner := &Spawner{opts: SpawnerOptions{
				Config: Config{Model: "parent-model"},
				Resolve: func(string, string) (ModelResolution, error) {
					return ModelResolution{Dialect: "custom", ImageInput: tc.resolved}, nil
				},
			}}
			_, _, resolution, err := spawner.resolveAgent(AgentDefinition{Name: "helper", Model: "child-model"})
			if err != nil {
				t.Fatalf("resolveAgent: %v", err)
			}
			switch {
			case tc.resolved == nil && resolution.ImageInput != nil:
				t.Errorf("ImageInput = %v, want nil", *resolution.ImageInput)
			case tc.resolved != nil && resolution.ImageInput == nil:
				t.Errorf("ImageInput = nil, want %v", *tc.resolved)
			case tc.resolved != nil && *resolution.ImageInput != *tc.resolved:
				t.Errorf("ImageInput = %v, want %v", *resolution.ImageInput, *tc.resolved)
			}
		})
	}
}

func TestSpawnerModelResolution(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "switcher.md", `name: switcher
description: Runs on another model.
model: other-model
tools: read_file
---
Body.
`)
	f := newSpawnFixture(t, testConfig())
	f.spawner.opts.Config.ThinkingEffort = "high"
	f.catalog = LoadAgents(testAgentDefinitions(), stateDir)
	f.spawner.opts.Definitions = f.catalog
	f.completer.steps = []scriptStep{completedStep("ok", model.Usage{InputTokens: 1, OutputTokens: 1})}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "switcher", Task: "t"}))
	if report.Status != tools.SubagentCompleted {
		t.Fatalf("report = %+v", report)
	}
	if len(f.resolved) != 1 || f.resolved[0].model != "other-model" || f.resolved[0].effort != "" {
		t.Fatalf("resolution = %+v, want the switched model with the default effort", f.resolved)
	}
	if report.Effort != "" {
		t.Fatalf("recorded effort = %q, want the model default", report.Effort)
	}
}

func TestSpawnerInheritedModelKeepsEffort(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	f.spawner.opts.Config.ThinkingEffort = "high"
	f.completer.steps = []scriptStep{completedStep("ok", model.Usage{InputTokens: 1, OutputTokens: 1})}
	if got := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "t"})); got.Status != tools.SubagentCompleted {
		t.Fatalf("report = %+v", got)
	}
	if len(f.resolved) != 1 || f.resolved[0].model != "test-model" || f.resolved[0].effort != "high" {
		t.Fatalf("resolution = %+v, want the inherited model and effort", f.resolved)
	}
}

func TestSpawnerUnusableModelFailsTheTask(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "broken.md", `name: broken
description: Names a model the catalog rejects.
model: bad-model
tools: read_file
---
Body.
`)
	f := newSpawnFixture(t, testConfig())
	f.catalog = LoadAgents(testAgentDefinitions(), stateDir)
	f.spawner.opts.Definitions = f.catalog
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "broken", Task: "t"}))
	if report.Status != tools.SubagentFailed || report.Code != tools.CodeInvalidArgs {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Text, "broken") || !strings.Contains(report.Text, "bad-model") {
		t.Fatalf("failure message = %q, want the agent and the rejected value", report.Text)
	}
	if len(f.completer.requests) != 0 {
		t.Fatal("a failed task still called the model")
	}
}

func TestSpawnerRejectsUnknownAgent(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "missing", Task: "t"}))
	if report.Status != tools.SubagentFailed || report.Code != tools.CodeNotFound {
		t.Fatalf("report = %+v", report)
	}
}

func TestSpawnerAgentsCatalog(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	infos := f.spawner.Agents()
	if len(infos) != len(testAgentDefinitions()) {
		t.Fatalf("agents = %d, want the %d fixture names", len(infos), len(testAgentDefinitions()))
	}
	for i := 1; i < len(infos); i++ {
		if infos[i].Name <= infos[i-1].Name {
			t.Fatalf("agents are not sorted: %v", infos)
		}
	}
}

func TestSpawnerChildIgnoresParentDisplayText(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	f.completer.steps = []scriptStep{{resp: model.Response{
		Text:         "child text",
		Reasoning:    "child reasoning",
		FinishReason: model.FinishStop,
		Usage:        model.Usage{InputTokens: 1, OutputTokens: 1},
	}}}
	if got := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "t"})); got.Status != tools.SubagentCompleted {
		t.Fatalf("report = %+v", got)
	}
	for _, ev := range f.display {
		switch ev := ev.(type) {
		case TextDelta, ReasoningDelta:
			t.Fatalf("child text crossed into the parent display: %+v", ev)
		case SubagentProgress:
			if ev.LastTool != "" && strings.Contains(ev.LastTool, "child text") {
				t.Fatalf("progress leaked child text: %+v", ev)
			}
		}
	}
}

func TestSpawnerContinuationKeepsRecordedPromptAndGrant(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	f.completer.steps = []scriptStep{
		completedStep("first", model.Usage{InputTokens: 1, OutputTokens: 1}),
		completedStep("second", model.Usage{InputTokens: 1, OutputTokens: 1}),
	}
	first := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "one"}))
	before := decodePayload[sessionStartData](t, f.readLog(t, first.Session)[0])

	// Replace the definition with different settings; the recorded child wins.
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "explore.md", `name: explore
description: A changed agent.
model: other-model
tools: edit_file
---
Changed body.
`)
	f.catalog = LoadAgents(testAgentDefinitions(), stateDir)
	f.spawner.opts.Definitions = f.catalog

	second := onlyReport(t, f.spawn(t, tools.Call{ID: "call-2"},
		tools.SubagentSpec{Task: "two", Session: first.Session}))
	if second.Status != tools.SubagentCompleted {
		t.Fatalf("continuation = %+v", second)
	}
	var start sessionStartData
	for _, ev := range f.readLog(t, first.Session) {
		if ev.Type == session.TypeSessionStart {
			start = decodePayload[sessionStartData](t, ev)
		}
	}
	if start.SystemPrompt != before.SystemPrompt || strings.Join(start.Tools, ",") != "read_file,search" {
		t.Fatalf("continuation changed the recorded prompt or grant: %+v", start)
	}
	last := f.completer.requests[len(f.completer.requests)-1]
	for _, schema := range last.Tools {
		if schema.Name == "edit_file" || schema.Name == "delegate" {
			t.Fatalf("continuation offered %q", schema.Name)
		}
	}
}

func TestSpawnerCarriesAppendedPromptToChildren(t *testing.T) {
	cfg := testConfig()
	host := "Workspace rules:\n- Read AGENTS.md before editing."
	cfg.AppendedPrompt = host
	f := newSpawnFixture(t, cfg)
	f.completer.steps = []scriptStep{
		completedStep("first", model.Usage{InputTokens: 1, OutputTokens: 1}),
		completedStep("second", model.Usage{InputTokens: 1, OutputTokens: 1}),
	}
	first := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "one"}))
	start := decodePayload[sessionStartData](t, f.readLog(t, first.Session)[0])
	if got := strings.Count(start.SystemPrompt, host); got != 1 {
		t.Fatalf("fresh child prompt carries the host text %d times: %q", got, start.SystemPrompt)
	}
	roleAt := strings.Index(start.SystemPrompt, "## Your role")
	if roleAt < 0 || strings.Index(start.SystemPrompt, host) < roleAt {
		t.Fatalf("fresh child prompt lost the host text position: %q", start.SystemPrompt)
	}
	if start.AppendedPrompt != "" {
		t.Fatalf("fresh child recorded appended_prompt = %q, want empty", start.AppendedPrompt)
	}

	second := onlyReport(t, f.spawn(t, tools.Call{ID: "call-2"}, tools.SubagentSpec{Task: "two", Session: first.Session}))
	if second.Status != tools.SubagentCompleted {
		t.Fatalf("continuation = %+v", second)
	}
	request := f.completer.requests[len(f.completer.requests)-1]
	if len(request.Messages) == 0 || request.Messages[0].Content != start.SystemPrompt {
		t.Fatalf("continued child prompt changed: %+v", request.Messages)
	}
	if got := strings.Count(request.Messages[0].Content, host); got != 1 {
		t.Fatalf("continued child carries the host text %d times", got)
	}
	for _, ev := range f.readLog(t, second.Session) {
		if ev.Type != session.TypeSessionStart {
			continue
		}
		if continued := decodePayload[sessionStartData](t, ev); continued.AppendedPrompt != "" {
			t.Fatalf("continued child recorded appended_prompt = %q, want empty", continued.AppendedPrompt)
		}
	}
}
