package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/tools"
)

// agentHost is the manual host plus the optional definitions capability.
type agentHost struct {
	*manualHost
	defs []AgentDefView
}

func (h *agentHost) Agents() []AgentDefView { return h.defs }

func newAgentHost() *agentHost {
	return &agentHost{manualHost: newManualHost()}
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

func TestAgentsOverlayReadOnly(t *testing.T) {
	host := newAgentHost()
	host.defs = []AgentDefView{
		{
			Name: "explore", Description: "Read-only exploration.", Source: "bundled",
			Tools: []string{"read_file", "search"}, Status: "active", PromptFirstLine: "Explore the area.",
		},
		{
			Name: "reviewer", Source: "user", Path: "/state/agents/reviewer.md",
			Model: "deepseek-v4.1-flash", Effort: "high", Tools: []string{"read_file"},
			MaxTurns: 12, Status: "active", PromptFirstLine: "Review the change.",
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
		"explore", "bundled", "reviewer", "/state/agents/", "deepseek-v4.1", "12", "invalid", "broken.md"} {
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
	// the user override shows its file path; nothing is written, and no editing
	// key exists.
	press(t, app, "home")
	press(t, app, "enter")
	expanded := ansi.Strip(app.overlay.agents.view())
	if !strings.Contains(expanded, "description is required") {
		t.Fatalf("expanded invalid row lacks its parse error:\n%s", expanded)
	}
	press(t, app, "end")
	press(t, app, "enter")
	expanded = ansi.Strip(app.overlay.agents.view())
	if !strings.Contains(expanded, "/state/agents/reviewer.md") {
		t.Fatalf("expanded row lacks the file path:\n%s", expanded)
	}
	if strings.Contains(expanded, "edit ") {
		t.Fatalf("the definitions browser exposes an editing key:\n%s", expanded)
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
