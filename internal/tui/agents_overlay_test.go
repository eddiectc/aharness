package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/tools"
)

// agentHost is the manual host plus the definitions and settings capabilities.
type agentHost struct {
	*manualHost
	defs      []AgentDefView
	overrides map[string]agent.AgentOverride
	saves     []agentSave
	clears    []string
	saveErr   error
}

type agentSave struct {
	name     string
	override agent.AgentOverride
}

func (h *agentHost) Agents() []AgentDefView {
	out := append([]AgentDefView(nil), h.defs...)
	for i := range out {
		out[i].Enabled = true
	}
	for i := range out {
		row := &out[i]
		if row.Status == "invalid" {
			continue
		}
		override, ok := h.overrides[row.Name]
		if !ok {
			continue
		}
		row.Override = &override
		if override.Model != nil {
			row.Model = *override.Model
			row.ModelSource = "override"
			if override.ThinkingEffort == nil {
				row.Effort = ""
				row.EffortSource = "model default"
			}
		}
		if override.ThinkingEffort != nil {
			row.Effort = *override.ThinkingEffort
			row.EffortSource = "override"
		}
		if override.MaxTurns != nil {
			row.MaxTurns = *override.MaxTurns
			row.MaxTurnsSource = "override"
		}
		if override.Enabled != nil {
			row.Enabled = *override.Enabled
			if *override.Enabled {
				row.Status = "active"
			} else {
				row.Status = "disabled"
			}
		}
	}
	return out
}

func (h *agentHost) SetAgentOverride(name string, override agent.AgentOverride) error {
	if h.saveErr != nil {
		return h.saveErr
	}
	h.saves = append(h.saves, agentSave{name: name, override: override})
	if override.Empty() {
		delete(h.overrides, name)
	} else {
		h.overrides[name] = override
	}
	return nil
}

func (h *agentHost) ClearAgentOverrides(name string) error {
	if h.saveErr != nil {
		return h.saveErr
	}
	h.clears = append(h.clears, name)
	delete(h.overrides, name)
	return nil
}

func newAgentHost() *agentHost {
	return &agentHost{manualHost: newManualHost(), overrides: make(map[string]agent.AgentOverride)}
}

// pressRune sends one printable key press, matching the event a terminal
// delivers for an editing key such as m or d.
func pressRune(t *testing.T, app *App, r rune) tea.Cmd {
	t.Helper()
	return updateApp(t, app, tea.KeyPressMsg{Code: r, Text: string(r)})
}

// startAgentRun starts a run through the agent host without the manual host
// type assertion the shared helper uses.
func startAgentRun(t *testing.T, app *App, text string) {
	t.Helper()
	typeText(t, app, text)
	press(t, app, "enter")
	if !app.run.active {
		t.Fatal("run did not start")
	}
}

func startedEvent(index int, session, agentName, intent string) agent.SubagentStarted {
	return agent.SubagentStarted{
		Turn: 1, CallID: "call-1", Index: index, Agent: agentName, Intent: intent,
		Session: session, Model: "deepseek-v4.1-flash", Effort: "high",
		Tools: []string{"read_file", "search"},
	}
}

func TestAgentHubRosterLifecycle(t *testing.T) {
	host := newAgentHost()
	app := newTestApp(t, host)
	startAgentRun(t, app, "delegate the work")

	deliver(app, agent.RunStarted{Turn: 0, Model: "deepseek-v4.1-flash", Dialect: "openai-chat", ThinkingEffort: "high"})
	deliver(app, startedEvent(0, "child-1", "explore", "find the projection"))
	deliver(app, startedEvent(1, "child-2", "reviewer", "review the change"))
	if len(app.children) != 2 {
		t.Fatalf("roster rows = %d, want 2", len(app.children))
	}
	deliver(app, agent.SubagentProgress{
		CallID: "call-1", Index: 0, Agent: "explore", Session: "child-1",
		ChildTurns: 3, ContextTokens: 18240,
		Usage:        model.Usage{InputTokens: 41230, OutputTokens: 3110},
		CostMicroUSD: 9210, CostComplete: true, LastTool: "read_file",
	})
	app.ensureContent()
	view := ansi.Strip(app.agentHubView(120, 10))
	for _, want := range []string{"AGENT", "INTENT", "MODEL", "EFFORT", "ELAPSED", "CTX", "TURNS", "COST", "STATUS",
		"explore", "find the projection", "deepseek-v4.1-flash", "high", "3", "18.2k", "$0.009210", "running"} {
		if !strings.Contains(view, want) {
			t.Fatalf("roster view missing %q:\n%s", want, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 120 {
			t.Fatalf("roster row exceeds the width: %q", line)
		}
	}

	deliver(app, agent.SubagentFinished{
		CallID: "call-1", Index: 0, Agent: "explore", Session: "child-1",
		Status: "completed", ChildTurns: 7, ContextTokens: 18240,
		Usage: model.Usage{InputTokens: 41230, OutputTokens: 3110}, CostMicroUSD: 9210,
		CostComplete: true, Duration: 3 * time.Second,
	})
	view = ansi.Strip(app.agentHubView(120, 10))
	if !strings.Contains(view, "completed") {
		t.Fatalf("finished row lost its terminal status:\n%s", view)
	}
	if !strings.Contains(view, "3s") {
		t.Fatalf("finished row lost its duration:\n%s", view)
	}

	// A new run starts with an empty roster.
	deliver(app, agent.RunStarted{Turn: 0, Model: "m", Dialect: "openai-chat"})
	if len(app.children) != 0 {
		t.Fatalf("roster rows after a new run = %d, want 0", len(app.children))
	}
}

func TestAgentHubIdleAndOverlay(t *testing.T) {
	host := newAgentHost()
	app := newTestApp(t, host)
	app.ensureContent()
	cmd := submitComposer(t, app, "/agent-hub extra words")
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil", cmd)
	}
	if app.overlay.kind != OverlayAgentHub {
		t.Fatalf("overlay = %q, want %q", app.overlay.kind, OverlayAgentHub)
	}
	if !strings.Contains(app.notice, "no child is running") {
		t.Fatalf("notice = %q", app.notice)
	}
	view := ansi.Strip(app.agentHubView(80, 10))
	if !strings.Contains(view, "no child is running") {
		t.Fatalf("idle roster = %q", view)
	}
	press(t, app, "esc")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay after Esc = %q", app.overlay.kind)
	}
}

func TestAgentHubSanitizesIntent(t *testing.T) {
	host := newAgentHost()
	app := newTestApp(t, host)
	startAgentRun(t, app, "delegate")
	deliver(app, startedEvent(0, "child-1", "explore", "read\x1b[31m the\x00 file\nsecond line"))
	app.ensureContent()
	view := app.agentHubView(120, 10)
	if strings.Contains(view, "\x1b[31m") || strings.ContainsRune(view, 0) {
		t.Fatalf("roster leaked a terminal control: %q", view)
	}
	if !strings.Contains(ansi.Strip(view), "read␛[31m") {
		t.Fatalf("roster did not render the sanitized intent: %q", ansi.Strip(view))
	}
	row := app.children[0]
	if strings.Contains(row.intent, "\n") {
		t.Fatalf("intent is not one line: %q", row.intent)
	}
}

func TestAgentHubLayoutDropsColumns(t *testing.T) {
	tests := []struct {
		width   int
		present []string
		absent  []string
	}{
		{120, []string{"INTENT", "MODEL", "EFFORT", "CTX", "TURNS", "COST", "STATUS"}, nil},
		{90, []string{"INTENT", "MODEL", "EFFORT", "CTX", "TURNS", "COST"}, nil},
		{80, []string{"INTENT", "MODEL", "EFFORT", "CTX", "TURNS"}, []string{"COST"}},
		{70, []string{"INTENT", "CTX", "TURNS"}, []string{"MODEL", "EFFORT", "COST"}},
		{60, []string{"INTENT", "TURNS"}, []string{"MODEL", "EFFORT", "CTX", "COST"}},
		{50, []string{"AGENT", "ELAPSED", "STATUS"}, []string{"INTENT", "MODEL", "EFFORT", "CTX", "TURNS", "COST"}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d", tc.width), func(t *testing.T) {
			cols := hubLayout(tc.width)
			var titles []string
			total := 0
			for i, col := range cols {
				if i > 0 {
					total += 2
				}
				total += col.width
				titles = append(titles, col.title)
			}
			joined := strings.Join(titles, ",")
			for _, want := range tc.present {
				if !strings.Contains(joined, want) {
					t.Fatalf("%d columns = %v, missing %s", tc.width, titles, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(joined, absent) {
					t.Fatalf("%d columns = %v, want %s dropped", tc.width, titles, absent)
				}
			}
			if total > tc.width {
				t.Fatalf("%d columns total %d, want <= %d", tc.width, total, tc.width)
			}
		})
	}
}

func TestAgentHubRendersMoreRow(t *testing.T) {
	host := newAgentHost()
	app := newTestApp(t, host)
	startAgentRun(t, app, "delegate")
	for i := range 9 {
		deliver(app, startedEvent(i, fmt.Sprintf("child-%d", i), "explore", fmt.Sprintf("task %d", i)))
	}
	view := ansi.Strip(app.agentHubView(120, 12))
	lines := strings.Split(view, "\n")
	// Header, eight visible rows, and one omitted row.
	if len(lines) != 10 {
		t.Fatalf("roster lines = %d, want 10 (header, eight rows, one omission):\n%s", len(lines), view)
	}
	if !strings.Contains(lines[len(lines)-1], "+1 more") {
		t.Fatalf("last row = %q, want the omission marker", lines[len(lines)-1])
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 120 {
			t.Fatalf("row exceeds the width: %q", line)
		}
	}
}

func TestDelegateBlockRendersRecordedTable(t *testing.T) {
	details := tools.DelegateDetails{
		Tasks: []tools.DelegateTaskDetails{{
			Agent: "explore", Intent: "find it", Session: "child-1", Status: "completed",
			Model: "deepseek-v4.1-flash", Effort: "high", Turns: 7, ContextTokens: 18240,
			Usage:        model.Usage{InputTokens: 41230, OutputTokens: 3110},
			CostMicroUSD: 9210, CostComplete: true, ReportBytes: 2481,
		}},
		Status: "completed", SubagentTurns: 7,
		SubagentUsage:        model.Usage{InputTokens: 41230, OutputTokens: 3110},
		SubagentCostMicroUSD: 9210, SubagentCostComplete: true,
	}
	raw, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	host := newAgentHost()
	host.history = []agent.HistoryItem{
		{Kind: agent.HistoryRun, Run: 1, Model: "deepseek-v4.1-flash"},
		{
			Kind: agent.HistoryTool, Run: 1, Turn: 1, CallID: "call-1", Name: "delegate",
			OK: true, Code: "ok", Arguments: json.RawMessage(`{"intent":"delegate","tasks":[]}`),
			Details: raw,
		},
		{Kind: agent.HistoryRun, Run: 1, Status: agent.StopCompleted},
	}
	app := newTestApp(t, host)
	app.setSize(120, 24)
	app.ensureContent()
	rendered := ansi.Strip(strings.Join(app.transcriptLines(), "\n"))
	for _, want := range []string{"tool delegate", "[1] explore", "completed", "elapsed"[:0], "deepseek-v4.1-flash", "effort high", "7 turns", "ctx 18.2k"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("replayed delegate block missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "ELAPSED") {
		t.Fatal("a replayed block claims live timing")
	}
}

func TestDelegateBlockTracksLiveChild(t *testing.T) {
	host := newAgentHost()
	app := newTestApp(t, host)
	startAgentRun(t, app, "delegate")
	deliver(app, agent.RunStarted{Turn: 0, Model: "m", Dialect: "openai-chat"})
	deliver(app, agent.ResponseCompleted{
		Turn:         1,
		ToolCalls:    []model.ToolCall{{ID: "call-1", Name: "delegate", Arguments: json.RawMessage(`{"intent":"delegate","tasks":[{"agent":"explore","task":"find it"}]}`)}},
		FinishReason: model.FinishToolCalls,
	})
	deliver(app, agent.ToolStarted{Turn: 1, Call: model.ToolCall{ID: "call-1", Name: "delegate", Arguments: json.RawMessage(`{"intent":"delegate","tasks":[{"agent":"explore","task":"find it"}]}`)}})
	deliver(app, startedEvent(0, "child-1", "explore", "find it"))
	deliver(app, agent.SubagentProgress{
		CallID: "call-1", Index: 0, Agent: "explore", Session: "child-1",
		ChildTurns: 2, ContextTokens: 5000,
		Usage: model.Usage{InputTokens: 9000, OutputTokens: 1000}, CostComplete: true,
	})
	app.ensureContent()
	rendered := ansi.Strip(strings.Join(app.transcriptLines(), "\n"))
	if !strings.Contains(rendered, "[1] explore") || !strings.Contains(rendered, "running") {
		t.Fatalf("live delegate block lacks its nested row:\n%s", rendered)
	}
	if !strings.Contains(rendered, "2 turns") {
		t.Fatalf("nested row did not observe progress:\n%s", rendered)
	}
	// One transcript item for the call, not one per child.
	count := 0
	for _, item := range app.items {
		if item.kind == itemTool && item.callID == "call-1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("delegate transcript items = %d, want 1", count)
	}
}

func TestAgentsOverlayProvenanceAndNavigation(t *testing.T) {
	host := newAgentHost()
	host.defs = []AgentDefView{
		{
			Name: "explore", Description: "Read-only exploration.", Source: "bundled",
			Tools: []string{"read_file", "search"}, Status: "active", PromptFirstLine: "Explore the area.",
		},
		{
			Name: "reviewer", Source: "user", Path: "/state/agents/reviewer.md",
			Model: "deepseek-v4.1-flash", ModelSource: "definition",
			Effort: "high", EffortSource: "definition",
			Tools: []string{"read_file"}, MaxTurns: 12, MaxTurnsSource: "definition",
			Status: "active", PromptFirstLine: "Review the change.",
		},
		{
			Name: "broken.md", Source: "user", Path: "/state/agents/broken.md",
			Status: "invalid", Err: "description is required",
		},
	}
	app := newTestApp(t, host)
	app.setSize(120, 30)
	cmd := submitComposer(t, app, "/agents")
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil", cmd)
	}
	if app.overlay.kind != OverlayAgents {
		t.Fatalf("overlay = %q, want agents", app.overlay.kind)
	}
	body := ansi.Strip(app.overlay.agents.view())
	for _, want := range []string{"NAME", "SOURCE", "MODEL", "EFFORT", "TOOLS", "MAX_TURNS", "STATUS",
		"explore", "bundled", "reviewer", "/state/agents/", "deepseek-v4.1", "12", "invalid", "broken.md",
		"m model", "e effort", "t max_turns", "d enabled", "r reset"} {
		if !strings.Contains(body, want) {
			t.Fatalf("definitions browser missing %q:\n%s", want, body)
		}
	}
	app.setSize(80, 24)
	app.overlay.resize(app, 80, 24)
	narrow := ansi.Strip(app.overlay.agents.view())
	for _, line := range strings.Split(narrow, "\n") {
		if ansi.StringWidth(line) > 68 {
			t.Fatalf("definitions row exceeds the 80-column overlay width: %q", line)
		}
	}
	app.setSize(120, 30)
	// Enter expands a row in place. The invalid file shows its parse error and
	// the user definition shows the file path and each setting's provenance.
	// Navigation and expansion never write.
	press(t, app, "home")
	press(t, app, "enter")
	expanded := ansi.Strip(app.overlay.agents.view())
	if !strings.Contains(expanded, "description is required") {
		t.Fatalf("expanded invalid row lacks its parse error:\n%s", expanded)
	}
	press(t, app, "end")
	press(t, app, "enter")
	expanded = ansi.Strip(app.overlay.agents.view())
	for _, want := range []string{"/state/agents/reviewer.md", "model:     deepseek-v4.1-flash  (definition)",
		"effort:    high  (definition)", "max_turns: 12  (definition)", "enabled:   yes  (default)"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded row missing %q:\n%s", want, expanded)
		}
	}
	if len(host.saves) != 0 || len(host.clears) != 0 {
		t.Fatalf("navigation wrote settings: saves=%d clears=%d", len(host.saves), len(host.clears))
	}
	// r with no override is a no-op and does not write.
	pressRune(t, app, 'r')
	if len(host.clears) != 0 {
		t.Fatalf("r with no override wrote: %d clears", len(host.clears))
	}
	if !strings.Contains(app.overlay.agents.view(), "no operator override") {
		t.Fatalf("notice = %q, want the no-override message", app.overlay.agents.notice)
	}
}

func TestAgentsOverlayEditsOverrides(t *testing.T) {
	host := newAgentHost()
	host.defs = []AgentDefView{{
		Name: "reviewer", Source: "user", Path: "/state/agents/reviewer.md",
		Model: "old-model", ModelSource: "definition",
		Effort: "high", EffortSource: "definition",
		Tools: []string{"read_file"}, MaxTurns: 12, MaxTurnsSource: "definition",
		Status: "active", PromptFirstLine: "Review.",
	}}
	app := newTestApp(t, host)
	app.setSize(120, 30)
	submitComposer(t, app, "/agents")

	// Model: an empty editor because no override exists; Enter saves it.
	pressRune(t, app, 'm')
	if !app.overlay.agents.editing || app.overlay.agents.editKey != "model" {
		t.Fatalf("editor = %+v, want the model editor", app.overlay.agents)
	}
	if got := app.overlay.agents.editBuf; got != "" {
		t.Fatalf("model editor prefill = %q, want empty", got)
	}
	pressRune(t, app, 'x')
	if got := app.overlay.agents.editBuf; got != "x" {
		t.Fatalf("printable key buffer = %q, want x", got)
	}
	press(t, app, "ctrl+u")
	typeText(t, app, "new-model")
	press(t, app, "enter")
	if len(host.saves) != 1 {
		t.Fatalf("saves = %d, want 1", len(host.saves))
	}
	if host.saves[0].name != "reviewer" || host.saves[0].override.Model == nil ||
		*host.saves[0].override.Model != "new-model" {
		t.Fatalf("saved override = %+v", host.saves[0])
	}
	view := ansi.Strip(app.overlay.agents.view())
	if !strings.Contains(view, "new-model*") || !strings.Contains(view, "default") {
		t.Fatalf("saved model did not reach the row:\n%s", view)
	}
	if !strings.Contains(view, "applies to the next process") {
		t.Fatalf("save notice missing:\n%s", view)
	}

	// Effort adds to the same override.
	pressRune(t, app, 'e')
	typeText(t, app, "low")
	press(t, app, "enter")
	if len(host.saves) != 2 {
		t.Fatalf("saves = %d, want 2", len(host.saves))
	}
	if host.saves[1].override.ThinkingEffort == nil || *host.saves[1].override.ThinkingEffort != "low" {
		t.Fatalf("effort override = %+v", host.saves[1].override)
	}

	// Max turns adds too.
	pressRune(t, app, 't')
	typeText(t, app, "3")
	press(t, app, "enter")
	if len(host.saves) != 3 {
		t.Fatalf("saves = %d, want 3", len(host.saves))
	}
	if host.saves[2].override.MaxTurns == nil || *host.saves[2].override.MaxTurns != 3 {
		t.Fatalf("max turns override = %+v", host.saves[2].override)
	}
	view = ansi.Strip(app.overlay.agents.view())
	if !strings.Contains(view, "3*") {
		t.Fatalf("saved max turns did not reach the row:\n%s", view)
	}

	// d disables the agent; the row says so.
	pressRune(t, app, 'd')
	if len(host.saves) != 4 {
		t.Fatalf("saves = %d, want 4", len(host.saves))
	}
	if host.saves[3].override.Enabled == nil || *host.saves[3].override.Enabled {
		t.Fatalf("enabled override = %+v", host.saves[3].override)
	}
	view = ansi.Strip(app.overlay.agents.view())
	if !strings.Contains(view, "disabled") {
		t.Fatalf("disabled row not labelled:\n%s", view)
	}

	// r removes every override.
	pressRune(t, app, 'r')
	if len(host.clears) != 1 || host.clears[0] != "reviewer" {
		t.Fatalf("clears = %v, want reviewer", host.clears)
	}
	view = ansi.Strip(app.overlay.agents.view())
	if strings.Contains(view, "new-model*") || strings.Contains(view, "disabled") {
		t.Fatalf("cleared override still shown:\n%s", view)
	}
	if !strings.Contains(view, "old-model") {
		t.Fatalf("definition model not restored after a clear:\n%s", view)
	}
}

func TestAgentsOverlayEditValidationAndCancel(t *testing.T) {
	host := newAgentHost()
	host.defs = []AgentDefView{{
		Name: "reviewer", Source: "user", Model: "old-model", ModelSource: "definition",
		Tools: []string{"read_file"}, Status: "active",
	}}
	app := newTestApp(t, host)
	app.setSize(120, 30)
	submitComposer(t, app, "/agents")

	// A non-positive turn cap stays in the editor with an error and no write.
	pressRune(t, app, 't')
	typeText(t, app, "0")
	press(t, app, "enter")
	if !app.overlay.agents.editing {
		t.Fatal("invalid max_turns closed the editor")
	}
	if !strings.Contains(app.overlay.agents.editErr, "positive integer") {
		t.Fatalf("edit error = %q, want a positive-integer message", app.overlay.agents.editErr)
	}
	if len(host.saves) != 0 {
		t.Fatalf("invalid max_turns wrote %d saves", len(host.saves))
	}

	// Esc cancels the edit and leaves the overlay open.
	press(t, app, "esc")
	if app.overlay.kind != OverlayAgents {
		t.Fatalf("overlay after the first Esc = %q, want it open", app.overlay.kind)
	}
	if app.overlay.agents.editing {
		t.Fatal("Esc did not cancel the editor")
	}
	// A second Esc closes the browser.
	press(t, app, "esc")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay after the second Esc = %q, want none", app.overlay.kind)
	}

	// A failed write keeps the editor open with the host's error.
	submitComposer(t, app, "/agents")
	host.saveErr = errors.New("config file is read-only")
	pressRune(t, app, 'm')
	typeText(t, app, "next-model")
	press(t, app, "enter")
	if !app.overlay.agents.editing {
		t.Fatal("failed save closed the editor")
	}
	if !strings.Contains(app.overlay.agents.editErr, "read-only") {
		t.Fatalf("edit error = %q, want the write failure", app.overlay.agents.editErr)
	}
	press(t, app, "esc")
	press(t, app, "esc")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay after failed-save Esc = %q, want none", app.overlay.kind)
	}
}

func TestAgentsOverlayAcceptsTextEncodedKeys(t *testing.T) {
	host := newAgentHost()
	host.defs = []AgentDefView{{
		Name: "reviewer", Source: "user", Tools: []string{"read_file"}, Status: "active",
	}}
	app := newTestApp(t, host)
	app.setSize(120, 30)
	submitComposer(t, app, "/agents")
	// A -ui-json client can send a single-character text message in place of a
	// key press; the overlay treats it as the same editing command.
	typeText(t, app, "m")
	if !app.overlay.agents.editing || app.overlay.agents.editKey != "model" {
		t.Fatalf("text m did not open the model editor: %+v", app.overlay.agents)
	}
	typeText(t, app, "next-model")
	press(t, app, "enter")
	if len(host.saves) != 1 || host.saves[0].override.Model == nil ||
		*host.saves[0].override.Model != "next-model" {
		t.Fatalf("text-encoded edit did not save: %+v", host.saves)
	}
	typeText(t, app, "d")
	if len(host.saves) != 2 || host.saves[1].override.Enabled == nil || *host.saves[1].override.Enabled {
		t.Fatalf("text d did not disable: %+v", host.saves)
	}
}

func TestAgentsOverlayInputCap(t *testing.T) {
	host := newAgentHost()
	host.defs = []AgentDefView{{
		Name: "reviewer", Source: "user", Tools: []string{"read_file"}, Status: "active",
	}}
	app := newTestApp(t, host)
	app.setSize(120, 30)
	submitComposer(t, app, "/agents")
	pressRune(t, app, 'm')
	typeText(t, app, strings.Repeat("a", maxAgentSettingInputBytes+1))
	if got := len(app.overlay.agents.editBuf); got != 0 {
		t.Fatalf("over-cap input buffer = %d bytes, want 0", got)
	}
	if !strings.Contains(app.overlay.agents.editErr, "capped") {
		t.Fatalf("edit error = %q, want the cap message", app.overlay.agents.editErr)
	}
}

func TestAgentCommandsAreDiscoverable(t *testing.T) {
	lines := strings.Join(slashCommandHelpLines(), "\n")
	for _, want := range []string{"/agent-hub", "/agents"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("help lines missing %q:\n%s", want, lines)
		}
	}
	matches := matchSlashCommands("agent")
	if len(matches) != 2 {
		t.Fatalf("autocomplete matches = %d, want 2", len(matches))
	}
	for _, text := range []string{"/agent-hub extra words", "/agents extra words"} {
		command, args, ok := parseSlashCommand(text)
		if !ok || args != "extra words" {
			t.Fatalf("parseSlashCommand(%q) = %+v, %q, %v", text, command, args, ok)
		}
	}
}

func TestProtocolCarriesSubagentTasks(t *testing.T) {
	tasks := []SubagentTaskLine{{
		Agent: "explore", Intent: "find it", Session: "child-1", Status: "completed",
		Model: "deepseek-v4.1-flash", Effort: "high", Turns: 7, ContextTokens: 18240,
		InputTokens: 41230, OutputTokens: 3110,
	}}
	data, err := encodeEvent(EventToolFinished{CallID: "call-1", Name: "delegate", OK: true, Code: "ok", SubagentTasks: tasks})
	if err != nil {
		t.Fatalf("encodeEvent: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["v"] != float64(protocolVersion) {
		t.Fatalf("protocol version = %v, want %d (no bump)", decoded["v"], protocolVersion)
	}
	raw, ok := decoded["subagent_tasks"].([]any)
	if !ok || len(raw) != 1 {
		t.Fatalf("subagent_tasks = %v", decoded["subagent_tasks"])
	}
	first := raw[0].(map[string]any)
	if first["agent"] != "explore" || first["turns"] != float64(7) {
		t.Fatalf("task line = %v", first)
	}
}

func TestAgentHubElapsedAdvancesWithTheClock(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := &subagentRow{agent: "explore", status: "running", startedAt: base, costKnown: true}
	first := hubCells(row, base.Add(2*time.Second))["ELAPSED"]
	second := hubCells(row, base.Add(3*time.Second))["ELAPSED"]
	if first == second {
		t.Fatalf("elapsed did not advance: %q then %q", first, second)
	}
	if first != "2s" || second != "3s" {
		t.Fatalf("elapsed = %q then %q, want 2s then 3s", first, second)
	}
	// A finished child shows its terminal duration regardless of the clock.
	row.status = "completed"
	row.finished = true
	row.duration = 11 * time.Second
	if got := hourCellsElapsed(row, base.Add(time.Hour)); got != "11s" {
		t.Fatalf("finished elapsed = %q, want the recorded duration", got)
	}
}

func hourCellsElapsed(row *subagentRow, now time.Time) string {
	return hubCells(row, now)["ELAPSED"]
}
