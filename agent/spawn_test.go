package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

type spawnFixture struct {
	stateDir  string
	workspace *tools.Workspace
	catalog   *AgentCatalog
	completer *fakeCompleter
	display   []DisplayEvent
	spawner   *Spawner
	resolved  []struct{ model, effort string }

	resolvedMu sync.Mutex
}

func newSpawnFixture(t *testing.T, cfg Config) *spawnFixture {
	t.Helper()
	stateDir := t.TempDir()
	ws, err := tools.OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	f := &spawnFixture{stateDir: stateDir, workspace: ws, catalog: LoadAgents(testAgentDefinitions(), t.TempDir())}
	cfg.StateDir = stateDir
	cfg.Workspace = ws.Dir()
	cfg.Provider = "opencode-go"
	cfg.Dialect = string(model.DialectChat)
	cfg.Model = "test-model"
	if cfg.Limits.MaxInputTokens == 0 {
		cfg.Limits = testConfig().Limits
	}
	f.completer = &fakeCompleter{}
	f.spawner = NewSpawner(SpawnerOptions{
		Config:      cfg,
		Workspace:   ws,
		Definitions: f.catalog,
		Resolve: func(modelID, effort string) (ModelResolution, error) {
			f.resolvedMu.Lock()
			f.resolved = append(f.resolved, struct{ model, effort string }{modelID, effort})
			f.resolvedMu.Unlock()
			if modelID == "bad-model" {
				return ModelResolution{}, fmt.Errorf("model %q is not usable", modelID)
			}
			return ModelResolution{
				Dialect: string(model.DialectChat),
				BaseURL: "http://127.0.0.1:1",
				Limits:  testConfig().Limits,
				Pricing: model.Pricing{},
			}, nil
		},
		NewCompleter: func(model.Dialect, string, string) model.Completer { return f.completer },
		NewToolOptions: func(string, string, string) tools.Options {
			return tools.Options{}
		},
		Display:       func(ev DisplayEvent) error { f.display = append(f.display, ev); return nil },
		ParentSession: "parent-session",
		ParentSeq:     func() uint64 { return 7 },
		MaxSubagents:  8,
	})
	return f
}

func (f *spawnFixture) spawn(t *testing.T, call tools.Call, specs ...tools.SubagentSpec) []tools.SubagentReport {
	t.Helper()
	return f.spawnOutcome(t, call, specs...).Reports
}

func (f *spawnFixture) spawnOutcome(t *testing.T, call tools.Call, specs ...tools.SubagentSpec) tools.SpawnResult {
	t.Helper()
	return f.spawner.Spawn(t.Context(), call, specs)
}

func (f *spawnFixture) readLog(t *testing.T, sessionID string) []session.Event {
	t.Helper()
	events, err := session.ReadHead(session.SessionPath(f.stateDir, sessionID), 1000)
	if err != nil {
		t.Fatalf("read child log: %v", err)
	}
	return events
}

func onlyReport(t *testing.T, reports []tools.SubagentReport) tools.SubagentReport {
	t.Helper()
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	return reports[0]
}

func completedStep(text string, usage model.Usage) scriptStep {
	return scriptStep{resp: model.Response{Text: text, FinishReason: model.FinishStop, Usage: usage}}
}

func TestSpawnerRecordsAChildSession(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	f.completer.steps = []scriptStep{completedStep("the child report", model.Usage{InputTokens: 20, OutputTokens: 5, Reported: true})}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1", Turn: 3},
		tools.SubagentSpec{Agent: "explore", Task: "Find the projection code.\nThen report."}))
	if report.Status != tools.SubagentCompleted || report.Text != "the child report" {
		t.Fatalf("report = %+v", report)
	}
	if report.Intent != "Find the projection code." {
		t.Fatalf("intent = %q", report.Intent)
	}
	if canonical, err := session.CanonicalID(report.Session); err != nil || canonical != report.Session {
		t.Fatalf("session = %q, want a canonical UUID", report.Session)
	}
	if report.Turns != 1 || report.Usage.Total() != 25 {
		t.Fatalf("turns/usage = %d/%+v", report.Turns, report.Usage)
	}

	events := f.readLog(t, report.Session)
	if len(events) == 0 || events[0].Type != session.TypeSessionStart {
		t.Fatalf("child log does not start with session_start")
	}
	start := decodePayload[sessionStartData](t, events[0])
	if start.ParentSession != "parent-session" || start.ParentSeq != 7 || start.ParentCallID != "call-1" {
		t.Fatalf("start parent ref = %+v", start)
	}
	if start.Agent != "explore" || start.ToolsetVersion != tools.Version || start.PromptVersion != PromptVersion {
		t.Fatalf("start contract = %+v", start)
	}
	if strings.Join(start.Tools, ",") != "read_file,search" {
		t.Fatalf("child grant = %v", start.Tools)
	}
	if !strings.Contains(start.SystemPrompt, "explore") || !strings.Contains(start.SystemPrompt, f.workspace.Dir()) {
		t.Fatalf("child prompt = %q", start.SystemPrompt)
	}
	final := 0
	for _, ev := range events {
		if ev.Type == session.TypeFinal {
			final++
			if got := decodePayload[finalData](t, ev).Status; got != StopCompleted {
				t.Fatalf("final status = %s", got)
			}
		}
	}
	if final != 1 {
		t.Fatalf("final events = %d, want 1", final)
	}
	// The child's plan never offers delegate: depth is one.
	if len(f.completer.requests) == 0 {
		t.Fatal("the child made no model call")
	}
	for _, schema := range f.completer.requests[0].Tools {
		if schema.Name == "delegate" {
			t.Fatal("the child model request offered delegate")
		}
	}
}

func TestSpawnerChildPromptFollowsParentVersion(t *testing.T) {
	frozenTestRule := "Run the relevant tests after changing code"
	v3TestRule := "Run the relevant tests before reporting the change done"
	tests := []struct {
		name    string
		version string
		want    string
		notWant string
	}{
		{name: "v0", version: PromptVersionV0, want: frozenTestRule, notWant: "Every tool call receives exactly one result"},
		{name: "v1", version: PromptVersionV1, want: frozenTestRule, notWant: "Every tool call receives exactly one result"},
		{name: "v2", version: PromptVersionV2, want: frozenTestRule, notWant: v3TestRule},
		{name: "v3", version: PromptVersion, want: v3TestRule, notWant: frozenTestRule},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.PromptVersion = tc.version
			f := newSpawnFixture(t, cfg)
			f.completer.steps = []scriptStep{completedStep("done", model.Usage{InputTokens: 1, OutputTokens: 1, Reported: true})}
			report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "read"}))
			start := decodePayload[sessionStartData](t, f.readLog(t, report.Session)[0])
			if start.PromptVersion != tc.version {
				t.Fatalf("child prompt version = %q, want %q", start.PromptVersion, tc.version)
			}
			if !strings.Contains(start.SystemPrompt, tc.want) {
				t.Fatalf("%s child prompt lacks %q: %q", tc.name, tc.want, start.SystemPrompt)
			}
			if strings.Contains(start.SystemPrompt, tc.notWant) {
				t.Fatalf("%s child prompt carries %q: %q", tc.name, tc.notWant, start.SystemPrompt)
			}
		})
	}
}

func TestSpawnerChildTodoGuidanceFollowsParentVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{PromptVersionV0, false},
		{PromptVersionV1, false},
		{PromptVersionV2, true},
		{PromptVersion, true},
	}
	for _, tc := range tests {
		t.Run(tc.version, func(t *testing.T) {
			cfg := testConfig()
			cfg.PromptVersion = tc.version
			f := newSpawnFixture(t, cfg)
			catalog := testAgentDefinitions()
			for i := range catalog {
				if catalog[i].Name == "explore" {
					catalog[i].Tools = append(catalog[i].Tools, "todo")
				}
			}
			f.catalog = LoadAgents(catalog, t.TempDir())
			opts := f.spawner.opts
			opts.Definitions = f.catalog
			f.spawner = NewSpawner(opts)
			f.completer.steps = []scriptStep{completedStep("done", model.Usage{InputTokens: 1, OutputTokens: 1, Reported: true})}
			report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "read"}))
			start := decodePayload[sessionStartData](t, f.readLog(t, report.Session)[0])
			if got := strings.Contains(start.SystemPrompt, "`todo` tracks multi-step work"); got != tc.want {
				t.Fatalf("%s child todo guidance = %t, want %t: %q", tc.version, got, tc.want, start.SystemPrompt)
			}
		})
	}
}

func TestSpawnerEnforcesTheChildGrant(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	dir := f.workspace.Dir()
	path := filepath.Join(dir, "protected.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.completer.steps = []scriptStep{
		{resp: model.Response{
			ToolCalls: []model.ToolCall{
				toolCall("call-a", "edit_file", `{"intent":"edit","path":"protected.txt","old_string":"original","new_string":"changed"}`),
				toolCall("call-b", "delegate", `{"intent":"delegate","tasks":[{"agent":"explore","task":"x"}]}`),
			},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 5, OutputTokens: 2, Reported: true},
		}},
		completedStep("done", model.Usage{InputTokens: 5, OutputTokens: 2, Reported: true}),
	}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "try to write"}))
	if report.Status != tools.SubagentCompleted {
		t.Fatalf("report = %+v", report)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "original" {
		t.Fatalf("workspace file = %q (%v), want it untouched", got, err)
	}
	events := f.readLog(t, report.Session)
	codes := make(map[string]tools.Code)
	for _, ev := range events {
		if ev.Type != session.TypeToolResult {
			continue
		}
		data := decodePayload[toolResultData](t, ev)
		codes[data.Name] = data.Code
	}
	if codes["edit_file"] != tools.CodeUnknownTool {
		t.Fatalf("edit_file code = %s, want unknown_tool", codes["edit_file"])
	}
	if codes["delegate"] != tools.CodeUnknownTool {
		t.Fatalf("delegate code = %s, want unknown_tool", codes["delegate"])
	}
}

func TestSpawnerChildTurnBudget(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "short.md", `name: short
description: A one-turn agent.
tools: read_file
max_turns: 1
---
Do one turn only.
`)
	f := newSpawnFixture(t, testConfig())
	f.catalog = LoadAgents(testAgentDefinitions(), stateDir)
	f.spawner.opts.Definitions = f.catalog
	f.completer.steps = []scriptStep{
		{resp: model.Response{
			Text:         "partial",
			ToolCalls:    []model.ToolCall{toolCall("c1", "read_file", `{"intent":"read","path":"missing.txt"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 5, OutputTokens: 2, Reported: true},
		}},
		{resp: model.Response{
			Text:         "never reached",
			ToolCalls:    []model.ToolCall{toolCall("c2", "read_file", `{"intent":"read","path":"missing2.txt"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 5, OutputTokens: 2, Reported: true},
		}},
	}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "short", Task: "read"}))
	if report.Status != tools.SubagentMaxTurns {
		t.Fatalf("status = %q, want max_turns", report.Status)
	}
	if report.Text != "partial" {
		t.Fatalf("partial text = %q", report.Text)
	}
	if report.Code != tools.CodeSubagentLimit {
		t.Fatalf("code = %s, want subagent_limit", report.Code)
	}
	if len(f.completer.requests) != 1 {
		t.Fatalf("model calls = %d, want 1 (the declared cap)", len(f.completer.requests))
	}
}

func TestSpawnerChildInheritsParentTurnBudget(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTurns = 1
	f := newSpawnFixture(t, cfg)
	f.completer.steps = []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("c1", "search", `{"intent":"s","pattern":"x"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 5, OutputTokens: 2, Reported: true},
		}},
	}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "search"}))
	if report.Status != tools.SubagentMaxTurns {
		t.Fatalf("status = %q, want the inherited max_turns cap", report.Status)
	}
}

func TestSpawnerChildTokenBudget(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTotalTokens = 100
	f := newSpawnFixture(t, cfg)
	f.completer.steps = []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("c1", "read_file", `{"intent":"r","path":"x"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 80, OutputTokens: 30, Reported: true},
		}},
	}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1", ParentUsage: model.Usage{InputTokens: 10, OutputTokens: 10}},
		tools.SubagentSpec{Agent: "explore", Task: "read"}))
	if report.Status != tools.SubagentTokenLimit {
		t.Fatalf("status = %q, want token_limit", report.Status)
	}
	if report.Code != tools.CodeSubagentLimit {
		t.Fatalf("code = %s, want subagent_limit", report.Code)
	}
}

func TestSpawnerMaxSubagents(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	f.spawner.opts.MaxSubagents = 1
	f.completer.steps = []scriptStep{completedStep("first", model.Usage{InputTokens: 1, OutputTokens: 1})}
	outcome := f.spawnOutcome(t, tools.Call{ID: "call-1"},
		tools.SubagentSpec{Agent: "explore", Task: "one"},
		tools.SubagentSpec{Agent: "explore", Task: "two"})
	reports := outcome.Reports
	if reports[0].Status != tools.SubagentCompleted {
		t.Fatalf("first report = %+v", reports[0])
	}
	if reports[1].Status != tools.SubagentFailed || reports[1].Code != tools.CodeInvalidArgs {
		t.Fatalf("second report = %+v", reports[1])
	}
	if !strings.Contains(reports[1].Text, "-max-subagents 1") {
		t.Fatalf("limit message = %q", reports[1].Text)
	}
	// A call that cannot reserve every fresh child keeps array order instead of
	// racing for the last slot.
	if outcome.Execution != tools.ExecutionSequential || !strings.Contains(outcome.ExecutionReason, "max-subagents") {
		t.Fatalf("execution = %q (%q), want sequential for the limit", outcome.Execution, outcome.ExecutionReason)
	}
}

func TestSpawnerContinuation(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	f.completer.steps = []scriptStep{
		completedStep("first run", model.Usage{InputTokens: 10, OutputTokens: 5}),
		completedStep("second run", model.Usage{InputTokens: 10, OutputTokens: 5}),
	}
	first := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "one"}))
	second := onlyReport(t, f.spawn(t, tools.Call{ID: "call-2"},
		tools.SubagentSpec{Agent: "", Task: "two", Session: first.Session}))
	if second.Status != tools.SubagentCompleted || second.Text != "second run" {
		t.Fatalf("continuation report = %+v", second)
	}
	if second.Session != first.Session {
		t.Fatalf("continuation opened %q, want %q", second.Session, first.Session)
	}
	if second.Turns != 1 || second.Usage.Total() != 15 {
		t.Fatalf("continuation turns/usage = %d/%+v, want this run only", second.Turns, second.Usage)
	}
	runs := 0
	for _, ev := range f.readLog(t, first.Session) {
		if ev.Type == session.TypeRunStart {
			runs++
		}
	}
	if runs != 2 {
		t.Fatalf("child runs = %d, want 2", runs)
	}

	// A session recorded by a different parent is rejected before any call.
	other := newSpawnFixture(t, testConfig())
	other.spawner.opts.ParentSession = "another-parent"
	rejected := onlyReport(t, other.spawn(t, tools.Call{ID: "call-3"},
		tools.SubagentSpec{Agent: "explore", Task: "three", Session: first.Session}))
	if rejected.Status != tools.SubagentFailed || rejected.Code != tools.CodeInvalidArgs {
		t.Fatalf("foreign session report = %+v", rejected)
	}
	if len(other.completer.requests) != 0 {
		t.Fatal("a foreign child session was run")
	}
}

func TestSpawnerContinuationRefusesDisabledAgent(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	f.completer.steps = []scriptStep{completedStep("first run", model.Usage{InputTokens: 1, OutputTokens: 1})}
	first := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "one"}))
	disabled := false
	applied, err := ApplyAgentOverrides(f.catalog, map[string]AgentOverride{
		"explore": {Enabled: &disabled},
	})
	if err != nil {
		t.Fatalf("ApplyAgentOverrides: %v", err)
	}
	f.spawner.opts.Definitions = applied
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-2"},
		tools.SubagentSpec{Task: "two", Session: first.Session}))
	if report.Status != tools.SubagentFailed || report.Code != tools.CodeNotFound {
		t.Fatalf("disabled continuation = %+v, want not_found", report)
	}
	if !strings.Contains(report.Text, "disabled") || !strings.Contains(report.Text, "explore") {
		t.Fatalf("disabled continuation message = %q", report.Text)
	}
	if len(f.completer.requests) != 1 {
		t.Fatalf("model calls = %d, want only the first run", len(f.completer.requests))
	}
}

func TestSpawnerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		f.completer.steps = []scriptStep{{blockOnContext: true}}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan tools.SubagentReport, 1)
		go func() {
			done <- onlyReport(t, f.spawner.Spawn(ctx, tools.Call{ID: "call-1"},
				[]tools.SubagentSpec{{Agent: "explore", Task: "block"}}).Reports)
		}()
		synctest.Wait()
		cancel()
		report := <-done
		if report.Status != tools.SubagentInterrupted {
			t.Fatalf("status = %q, want interrupted", report.Status)
		}
		if report.Code != tools.CodeCanceled {
			t.Fatalf("code = %s, want canceled", report.Code)
		}
		events := f.readLog(t, report.Session)
		if events[len(events)-1].Type != session.TypeRunEnd {
			t.Fatalf("last child event = %s, want run_end", events[len(events)-1].Type)
		}
		final := 0
		for _, ev := range events {
			if ev.Type == session.TypeFinal {
				final++
			}
		}
		if final != 1 {
			t.Fatalf("final events = %d, want 1", final)
		}
	})
}

func TestSpawnerDisplayOrderAndThrottle(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ticks := 0
	f := newSpawnFixture(t, testConfig())
	f.spawner.opts.Now = func() time.Time {
		ticks++
		return base.Add(time.Duration(ticks) * 2 * time.Second)
	}
	f.completer.steps = []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("c1", "read_file", `{"intent":"r","path":"x"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 10, OutputTokens: 2, Reported: true},
		}},
		completedStep("done", model.Usage{InputTokens: 12, OutputTokens: 3, Reported: true}),
	}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1", Turn: 2},
		tools.SubagentSpec{Agent: "explore", Task: "  Look at the projection. More text."}))
	if report.Status != tools.SubagentCompleted {
		t.Fatalf("report = %+v", report)
	}
	var kinds []string
	for _, ev := range f.display {
		switch ev := ev.(type) {
		case SubagentStarted:
			kinds = append(kinds, "started")
			if ev.Intent != "Look at the projection. More text." || ev.Index != 0 || ev.Turn != 2 {
				t.Fatalf("started event = %+v", ev)
			}
			if len(ev.Tools) != 2 || ev.Tools[0] != "read_file" {
				t.Fatalf("started grant = %v", ev.Tools)
			}
		case SubagentProgress:
			kinds = append(kinds, "progress")
			if ev.ChildTurns < 1 || ev.ContextTokens == 0 {
				t.Fatalf("progress event = %+v", ev)
			}
			if ev.LastTool != "" && ev.LastTool != "read_file" {
				t.Fatalf("progress last tool = %q", ev.LastTool)
			}
		case SubagentFinished:
			kinds = append(kinds, "finished")
			if ev.Status != tools.SubagentCompleted || ev.ChildTurns != 2 {
				t.Fatalf("finished event = %+v", ev)
			}
		}
	}
	if got := strings.Join(kinds, ","); got != "started,progress,progress,finished" {
		t.Fatalf("display order = %q", got)
	}

	// Two responses inside one second are throttled to a single progress event.
	throttled := newSpawnFixture(t, testConfig())
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	throttled.spawner.opts.Now = func() time.Time { return fixed }
	throttled.completer.steps = []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("c1", "read_file", `{"intent":"r","path":"x"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 1, OutputTokens: 1},
		}},
		completedStep("done", model.Usage{InputTokens: 1, OutputTokens: 1}),
	}
	if got := onlyReport(t, throttled.spawn(t, tools.Call{ID: "call-2"}, tools.SubagentSpec{Agent: "explore", Task: "again"})); got.Status != tools.SubagentCompleted {
		t.Fatalf("throttled spawn = %+v", got)
	}
	progress := 0
	for _, ev := range throttled.display {
		if _, ok := ev.(SubagentProgress); ok {
			progress++
		}
	}
	if progress != 1 {
		t.Fatalf("progress events = %d, want the first response only", progress)
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

func TestSpawnerRoutesSteeringToChild(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	steered := false
	f.spawner.opts.Display = func(ev DisplayEvent) error {
		progress, ok := ev.(SubagentProgress)
		if !ok || steered {
			return nil
		}
		steered = true
		return f.spawner.SteerChild(progress.Session, SteeringLine{ID: 1, Text: "check the parser instead"})
	}
	f.completer.steps = []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{toolCall("c1", "search", `{"intent":"s","pattern":"projection"}`)},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 5, OutputTokens: 2, Reported: true},
		}},
		completedStep("done", model.Usage{InputTokens: 6, OutputTokens: 3, Reported: true}),
	}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "find the projection"}))
	if report.Status != tools.SubagentCompleted {
		t.Fatalf("report = %+v", report)
	}
	if !steered {
		t.Fatal("no progress boundary to steer at")
	}
	if len(f.completer.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(f.completer.requests))
	}
	found := false
	for _, message := range f.completer.requests[1].Messages {
		if message.Content == "check the parser instead" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("steering did not reach the next child request: %+v", f.completer.requests[1].Messages)
	}

	items, err := f.spawner.ChildHistory(report.Session)
	if err != nil {
		t.Fatalf("ChildHistory: %v", err)
	}
	steers := 0
	for _, item := range items {
		if item.Kind == HistoryUser && item.Source == SourceSubagentSteer {
			steers++
			if item.Text != "check the parser instead" {
				t.Fatalf("recorded steer = %q", item.Text)
			}
		}
	}
	if steers != 1 {
		t.Fatalf("recorded subagent steers = %d, want 1", steers)
	}
	// The channel is gone once the child store closes: a late line cannot be
	// silently queued for a run that no longer exists.
	if err := f.spawner.SteerChild(report.Session, SteeringLine{ID: 2, Text: "too late"}); !errors.Is(err, ErrSubagentNotRunning) {
		t.Fatalf("late SteerChild = %v, want ErrSubagentNotRunning", err)
	}

	// A child of another parent is not readable through this spawner.
	other := newSpawnFixture(t, testConfig())
	other.spawner.opts.ParentSession = "another-parent"
	if _, err := other.spawner.ChildHistory(report.Session); err == nil {
		t.Fatal("ChildHistory accepted a session from another parent")
	}
}

// concurrencyRecorder records how many completions overlap in time.
type concurrencyRecorder struct {
	mu        sync.Mutex
	active    int
	maxActive int
	started   int
}

func (r *concurrencyRecorder) begin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active++
	r.started++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
}

func (r *concurrencyRecorder) end() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active--
}

func (r *concurrencyRecorder) maxConcurrent() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxActive
}

func (r *concurrencyRecorder) startedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// recorderCompleter blocks on a barrier or the context, so a test can prove
// that several children were in flight at once.
type recorderCompleter struct {
	rec            *concurrencyRecorder
	barrier        <-chan struct{}
	blockOnContext bool
	text           string
}

func (c *recorderCompleter) Complete(ctx context.Context, _ model.Request) (model.Response, error) {
	c.rec.begin()
	defer c.rec.end()
	if c.blockOnContext {
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	if c.barrier != nil {
		select {
		case <-c.barrier:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	return model.Response{
		Text:         c.text,
		FinishReason: model.FinishStop,
		Usage:        model.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
	}, nil
}

func TestIsReadOnlyGrant(t *testing.T) {
	tests := []struct {
		name  string
		grant []string
		want  bool
	}{
		{"nil means the full toolset", nil, false},
		{"empty is not a grant", []string{}, false},
		{"read_file and search", []string{"read_file", "search"}, true},
		{"web tools", []string{"web_search", "web_fetch"}, true},
		{"mixed read-only", []string{"read_file", "search", "web_search", "web_fetch"}, true},
		{"edit_file", []string{"read_file", "edit_file"}, false},
		{"run_command", []string{"run_command"}, false},
		{"todo", []string{"todo"}, false},
		{"delegate", []string{"delegate"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadOnlyGrant(tc.grant); got != tc.want {
				t.Fatalf("isReadOnlyGrant(%v) = %v, want %v", tc.grant, got, tc.want)
			}
		})
	}
}

func TestSpawnerRunsReadOnlyTasksConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		rec := &concurrencyRecorder{}
		barrier := make(chan struct{})
		f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
			return &recorderCompleter{rec: rec, barrier: barrier, text: "report"}
		}
		// The display contract stays single-threaded: the spawner's mutex must
		// keep concurrent children from entering the callback together. The
		// -race run also exercises the callback from several child goroutines.
		var displayMu sync.Mutex
		displayActive, displayMax := 0, 0
		f.spawner.opts.Display = func(DisplayEvent) error {
			displayMu.Lock()
			displayActive++
			if displayActive > displayMax {
				displayMax = displayActive
			}
			displayMu.Unlock()
			displayMu.Lock()
			displayActive--
			displayMu.Unlock()
			return nil
		}

		done := make(chan tools.SpawnResult, 1)
		go func() {
			done <- f.spawner.Spawn(t.Context(), tools.Call{ID: "call-1"}, []tools.SubagentSpec{
				{Agent: "explore", Task: "one"},
				{Agent: "explore", Task: "two"},
				{Agent: "explore", Task: "three"},
			})
		}()
		synctest.Wait()
		started := rec.startedCount()
		if started != 3 {
			close(barrier)
			t.Fatalf("children in flight = %d, want 3 before any completes", started)
		}
		select {
		case <-done:
			t.Fatal("Spawn returned before its children finished")
		default:
		}
		close(barrier)
		synctest.Wait()
		outcome := <-done

		if outcome.Execution != tools.ExecutionConcurrent {
			t.Fatalf("execution = %q, want concurrent", outcome.Execution)
		}
		if len(outcome.Reports) != 3 {
			t.Fatalf("reports = %d, want 3", len(outcome.Reports))
		}
		seen := make(map[string]bool, 3)
		for _, report := range outcome.Reports {
			if report.Status != tools.SubagentCompleted || report.Text != "report" {
				t.Fatalf("report = %+v", report)
			}
			seen[report.Session] = true
		}
		if len(seen) != 3 {
			t.Fatalf("sessions = %v, want three distinct children", seen)
		}
		if maxActive := rec.maxConcurrent(); maxActive != 3 {
			t.Fatalf("overlapping completions = %d, want 3", maxActive)
		}
		displayMu.Lock()
		gotDisplayMax := displayMax
		displayMu.Unlock()
		if gotDisplayMax != 1 {
			t.Fatalf("display callbacks in flight = %d, want serialized", gotDisplayMax)
		}
	})
}

func TestSpawnerWriteCapableTaskForcesSequential(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	rec := &concurrencyRecorder{}
	f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
		return &recorderCompleter{rec: rec, text: "report"}
	}
	outcome := f.spawnOutcome(t, tools.Call{ID: "call-1"},
		tools.SubagentSpec{Agent: "explore", Task: "read"},
		tools.SubagentSpec{Agent: "implementer", Task: "write"})
	if outcome.Execution != tools.ExecutionSequential {
		t.Fatalf("execution = %q, want sequential", outcome.Execution)
	}
	if !strings.Contains(outcome.ExecutionReason, "implementer") || !strings.Contains(outcome.ExecutionReason, "not read-only") {
		t.Fatalf("execution reason = %q", outcome.ExecutionReason)
	}
	if len(outcome.Reports) != 2 || outcome.Reports[0].Status != tools.SubagentCompleted || outcome.Reports[1].Status != tools.SubagentCompleted {
		t.Fatalf("reports = %+v", outcome.Reports)
	}
	if maxActive := rec.maxConcurrent(); maxActive != 1 {
		t.Fatalf("overlapping completions = %d, want a sequential call", maxActive)
	}
}

func TestSpawnerContinuationJoinsReadOnlyFanOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		f.completer.steps = []scriptStep{completedStep("first run", model.Usage{InputTokens: 1, OutputTokens: 1})}
		first := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "one"}))

		rec := &concurrencyRecorder{}
		barrier := make(chan struct{})
		f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
			return &recorderCompleter{rec: rec, barrier: barrier, text: "second"}
		}
		done := make(chan tools.SpawnResult, 1)
		go func() {
			done <- f.spawner.Spawn(t.Context(), tools.Call{ID: "call-2"}, []tools.SubagentSpec{
				{Agent: "explore", Task: "fresh"},
				{Task: "continue", Session: first.Session},
			})
		}()
		synctest.Wait()
		if started := rec.startedCount(); started != 2 {
			close(barrier)
			t.Fatalf("children in flight = %d, want 2", started)
		}
		close(barrier)
		synctest.Wait()
		outcome := <-done
		if outcome.Execution != tools.ExecutionConcurrent {
			t.Fatalf("execution = %q, reason %q, want concurrent", outcome.Execution, outcome.ExecutionReason)
		}
		if outcome.Reports[1].Session != first.Session || outcome.Reports[1].Status != tools.SubagentCompleted {
			t.Fatalf("continuation report = %+v", outcome.Reports[1])
		}
	})
}

func TestSpawnerConcurrentCancellationStopsEveryChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		rec := &concurrencyRecorder{}
		f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
			return &recorderCompleter{rec: rec, blockOnContext: true}
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan tools.SpawnResult, 1)
		go func() {
			done <- f.spawner.Spawn(ctx, tools.Call{ID: "call-1"}, []tools.SubagentSpec{
				{Agent: "explore", Task: "one"},
				{Agent: "explore", Task: "two"},
				{Agent: "explore", Task: "three"},
			})
		}()
		synctest.Wait()
		if started := rec.startedCount(); started != 3 {
			t.Fatalf("children in flight = %d, want 3", started)
		}
		cancel()
		outcome := <-done
		if outcome.Execution != tools.ExecutionConcurrent {
			t.Fatalf("execution = %q, want concurrent", outcome.Execution)
		}
		for i, report := range outcome.Reports {
			if report.Status != tools.SubagentInterrupted || report.Code != tools.CodeCanceled {
				t.Fatalf("report %d = %+v, want interrupted/canceled", i, report)
			}
			events := f.readLog(t, report.Session)
			if events[len(events)-1].Type != session.TypeRunEnd {
				t.Fatalf("child %d last event = %s, want run_end", i, events[len(events)-1].Type)
			}
			final := 0
			for _, ev := range events {
				if ev.Type == session.TypeFinal {
					final++
				}
			}
			if final != 1 {
				t.Fatalf("child %d final events = %d, want 1", i, final)
			}
		}
	})
}

func TestDelegateCancellationMidCallPairsCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		rec := &concurrencyRecorder{}
		f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
			return &recorderCompleter{rec: rec, blockOnContext: true}
		}
		registry := tools.NewRegistry(f.workspace, tools.Options{Spawning: f.spawner})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan tools.Result, 1)
		go func() {
			done <- registry.ExecuteCall(ctx, tools.Call{ID: "call-1"}, "delegate",
				json.RawMessage(`{"intent":"fan out","tasks":[`+
					`{"agent":"explore","task":"one"},`+
					`{"agent":"explore","task":"two"},`+
					`{"agent":"explore","task":"three"}]}`))
		}()
		synctest.Wait()
		if started := rec.startedCount(); started != 3 {
			t.Fatalf("children in flight = %d, want 3", started)
		}
		cancel()
		res := <-done
		if res.Code != tools.CodeCanceled {
			t.Fatalf("delegate code = %s (%s), want canceled", res.Code, res.Error)
		}
		details, ok := tools.ParseDelegateDetails(res.Details)
		if !ok || details.Execution != tools.ExecutionConcurrent || len(details.Tasks) != 3 {
			t.Fatalf("delegate details = %+v", details)
		}
		for i, task := range details.Tasks {
			if task.Status != tools.SubagentInterrupted {
				t.Fatalf("task %d status = %q, want interrupted", i+1, task.Status)
			}
		}
	})
}

func TestSpawnerChildInheritsStuckDetection(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	call := model.Response{
		ToolCalls:    []model.ToolCall{toolCall("same", "read_file", `{"intent":"read","path":"missing.txt"}`)},
		FinishReason: model.FinishToolCalls,
		Usage:        model.Usage{InputTokens: 5, OutputTokens: 2, Reported: true},
	}
	f.completer.steps = []scriptStep{{resp: call}, {resp: call}, {resp: call}, {resp: call}, {resp: call}, {resp: call}}
	report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "repeat"}))
	if report.Status != tools.SubagentStuck {
		t.Fatalf("status = %q, want stuck", report.Status)
	}
	if len(f.completer.requests) != 5 {
		t.Fatalf("child model calls = %d, want five identical failures", len(f.completer.requests))
	}
}
