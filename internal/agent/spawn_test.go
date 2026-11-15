package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

type spawnFixture struct {
	stateDir  string
	workspace *tools.Workspace
	catalog   *AgentCatalog
	completer *fakeCompleter
	display   []DisplayEvent
	spawner   *Spawner
	resolved  []struct{ model, effort string }
}

func newSpawnFixture(t *testing.T, cfg Config) *spawnFixture {
	t.Helper()
	stateDir := t.TempDir()
	ws, err := tools.OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	f := &spawnFixture{stateDir: stateDir, workspace: ws, catalog: LoadAgents(t.TempDir())}
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
			f.resolved = append(f.resolved, struct{ model, effort string }{modelID, effort})
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
	f.catalog = LoadAgents(stateDir)
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
	reports := f.spawn(t, tools.Call{ID: "call-1"},
		tools.SubagentSpec{Agent: "explore", Task: "one"},
		tools.SubagentSpec{Agent: "explore", Task: "two"})
	if reports[0].Status != tools.SubagentCompleted {
		t.Fatalf("first report = %+v", reports[0])
	}
	if reports[1].Status != tools.SubagentFailed || reports[1].Code != tools.CodeInvalidArgs {
		t.Fatalf("second report = %+v", reports[1])
	}
	if !strings.Contains(reports[1].Text, "-max-subagents 1") {
		t.Fatalf("limit message = %q", reports[1].Text)
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

func TestSpawnerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		f.completer.steps = []scriptStep{{blockOnContext: true}}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan tools.SubagentReport, 1)
		go func() {
			done <- onlyReport(t, f.spawner.Spawn(ctx, tools.Call{ID: "call-1"},
				[]tools.SubagentSpec{{Agent: "explore", Task: "block"}}))
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
	f.catalog = LoadAgents(stateDir)
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
	f.catalog = LoadAgents(stateDir)
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
	if len(infos) != 5 {
		t.Fatalf("agents = %d, want the bundled set", len(infos))
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
	f.catalog = LoadAgents(stateDir)
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

func TestSpawnerRoutesSteeringToChild(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	steered := false
	f.spawner.opts.Display = func(ev DisplayEvent) error {
		progress, ok := ev.(SubagentProgress)
		if !ok || steered {
			return nil
		}
		steered = true
		return f.spawner.SteerChild(progress.Session, "check the parser instead")
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
	if err := f.spawner.SteerChild(report.Session, "too late"); !errors.Is(err, ErrSubagentNotRunning) {
		t.Fatalf("late SteerChild = %v, want ErrSubagentNotRunning", err)
	}

	// A child of another parent is not readable through this spawner.
	other := newSpawnFixture(t, testConfig())
	other.spawner.opts.ParentSession = "another-parent"
	if _, err := other.spawner.ChildHistory(report.Session); err == nil {
		t.Fatal("ChildHistory accepted a session from another parent")
	}
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
