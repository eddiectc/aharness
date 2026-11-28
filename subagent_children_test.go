package aharness_test

import (
	"context"
	"strings"
	"testing"

	"github.com/eddiectc/aharness"
)

func readOnlyDelegation(max int, names ...string) *aharness.DelegationConfig {
	def := aharness.AgentDefinition{
		Name:        "analyst",
		Description: "Read-only analyst.",
		Prompt:      "Do the task.",
		Tools:       []aharness.ToolName{},
	}
	for _, name := range names {
		def.Tools = append(def.Tools, aharness.ToolName(name))
	}
	return &aharness.DelegationConfig{Agents: []aharness.AgentDefinition{def}, MaxSubagents: max}
}

func TestCompletedDelegateCallFoldsChildOutcomes(t *testing.T) {
	parentModel := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:   "delegate-1",
				Name: "delegate",
				Arguments: delegateArgs(
					map[string]string{"agent": "analyst", "task": "one"},
					map[string]string{"agent": "analyst", "task": "two"},
					map[string]string{"agent": "analyst", "task": "three"},
				),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	childUsage := aharness.Usage{
		InputTokens: 2, OutputTokens: 1,
		CachedInputTokens: 1, CacheWriteInputTokens: 1,
		ReasoningOutputTokens: 1, Reported: true,
	}
	modelCfg := testModel(parentModel)
	modelCfg.NewCompleter = func(string) aharness.Model {
		return &scriptedModel{responses: []aharness.Response{{
			Text: "child done", FinishReason: aharness.FinishStop,
			Usage: childUsage,
		}}}
	}
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
		Model:     modelCfg,
		Tools:     &aharness.ToolsConfig{Delegation: readOnlyDelegation(4)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	result, err := ag.Run(context.Background(), "delegate", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Children) != 3 {
		t.Fatalf("Children = %+v, want 3 rows", result.Children)
	}
	var turns int
	sessions := map[string]bool{}
	for _, child := range result.Children {
		if child.CallID != "delegate-1" {
			t.Fatalf("child CallID = %q", child.CallID)
		}
		if child.Status != aharness.SubagentCompleted {
			t.Fatalf("child status = %q", child.Status)
		}
		if child.Error != "" {
			t.Fatalf("completed child error = %q", child.Error)
		}
		if child.Turns != 1 || child.Usage != childUsage {
			t.Fatalf("child outcome = %+v, want usage %+v", child, childUsage)
		}
		turns += child.Turns
		sessions[child.Session] = true
	}
	wantSubagentUsage := aharness.Usage{
		InputTokens: 6, OutputTokens: 3,
		CachedInputTokens: 3, CacheWriteInputTokens: 3,
		ReasoningOutputTokens: 3, Reported: true,
	}
	if turns != result.SubagentTurns || result.SubagentUsage != wantSubagentUsage {
		t.Fatalf("children turns %d and subagent usage %+v do not match %d/%+v",
			turns, result.SubagentUsage, result.SubagentTurns, wantSubagentUsage)
	}
	if len(sessions) != 3 {
		t.Fatalf("child sessions = %v, want 3 distinct", sessions)
	}
	state, err := ag.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	wantOwnUsage := aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true}
	wantTotalUsage := aharness.Usage{
		InputTokens: 8, OutputTokens: 5,
		CachedInputTokens: 3, CacheWriteInputTokens: 3,
		ReasoningOutputTokens: 3, Reported: true,
	}
	if state.Usage != wantOwnUsage || state.SubagentUsage != wantSubagentUsage {
		t.Fatalf("state usage = %+v + %+v, want %+v + %+v",
			state.Usage, state.SubagentUsage, wantOwnUsage, wantSubagentUsage)
	}
	if got := state.TotalUsage(); got != wantTotalUsage {
		t.Fatalf("TotalUsage = %+v, want %+v", got, wantTotalUsage)
	}
}

func TestPreStartFailureEmitsFinishedWithError(t *testing.T) {
	parentModel := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:   "delegate-1",
				Name: "delegate",
				Arguments: delegateArgs(
					map[string]string{"agent": "analyst", "task": "one"},
					map[string]string{"agent": "analyst", "task": "two"},
				),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	modelCfg := testModel(parentModel)
	modelCfg.NewCompleter = func(string) aharness.Model {
		return &scriptedModel{responses: []aharness.Response{{
			Text: "child done", FinishReason: aharness.FinishStop,
			Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		}}}
	}
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
		Model:     modelCfg,
		Tools:     &aharness.ToolsConfig{Delegation: readOnlyDelegation(1)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	run, err := ag.Start(context.Background(), "delegate", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := map[int]aharness.SubagentFinished{}
	var result aharness.Result
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		switch ev := ev.(type) {
		case aharness.SubagentFinished:
			if _, exists := finished[ev.Index]; exists {
				t.Fatalf("duplicate finished event for index %d", ev.Index)
			}
			finished[ev.Index] = ev
		case aharness.RunFinished:
			result = ev.Result
		}
	}
	if len(finished) != 2 {
		t.Fatalf("finished events = %d, want 2", len(finished))
	}
	if got := finished[0]; got.Status != aharness.SubagentCompleted || got.ChildSession == "" {
		t.Fatalf("started child event = %+v", got)
	}
	failed := finished[1]
	if failed.Status != aharness.SubagentFailed {
		t.Fatalf("pre-start status = %q", failed.Status)
	}
	if failed.ChildSession != "" || failed.ChildTurns != 0 || failed.Usage.Total() != 0 {
		t.Fatalf("pre-start event = %+v, want empty session and zero counters", failed)
	}
	if !strings.Contains(failed.Error, "subagent limit") {
		t.Fatalf("pre-start error = %q", failed.Error)
	}
	if len(result.Children) != 2 {
		t.Fatalf("Children = %+v", result.Children)
	}
	if !strings.Contains(result.Children[1].Error, "subagent limit") {
		t.Fatalf("failed child row = %+v", result.Children[1])
	}
}
