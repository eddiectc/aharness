package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/eddiectc/aharness/internal/agent"
)

// childHistory is a small delegated-child transcript for the focus tests.
func childHistory() []agent.HistoryItem {
	return []agent.HistoryItem{
		{Kind: agent.HistoryRun, Run: 1, Model: "deepseek-v4.1-flash"},
		{Kind: agent.HistoryUser, Run: 1, Source: "delegate", Text: "Find the projection."},
		{Kind: agent.HistoryAssistant, Run: 1, Text: "The projection lives in context.go."},
	}
}

func TestAgentHubEnterFocusesChildAndSteers(t *testing.T) {
	host := newAgentHost()
	host.childHistory = map[string][]agent.HistoryItem{"child-1": childHistory()}
	app := newTestApp(t, host)
	startAgentRun(t, app, "delegate the work")
	deliver(app, agent.RunStarted{Turn: 0, Model: "deepseek-v4.1-flash", Dialect: "openai-chat"})
	deliver(app, startedEvent(0, "child-1", "explore", "find the projection"))

	submitComposer(t, app, "/agent-hub")
	if app.overlay.kind != OverlayAgentHub {
		t.Fatalf("overlay = %q, want the roster", app.overlay.kind)
	}
	press(t, app, "enter")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay after Enter = %q, want the child view", app.overlay.kind)
	}
	if app.childFocus == nil || app.childFocus.session != "child-1" || app.childFocus.readOnly {
		t.Fatalf("child focus = %+v", app.childFocus)
	}
	view := ansi.Strip(app.childFocus.view.View())
	for _, want := range []string{"task", "Find the projection.", "assistant", "context.go"} {
		if !strings.Contains(view, want) {
			t.Fatalf("child view missing %q:\n%s", want, view)
		}
	}

	parentItems := len(app.items)
	typeText(t, app, "focus on the parser")
	press(t, app, "enter")
	if len(host.steered) != 1 || host.steered[0].session != "child-1" || host.steered[0].text != "focus on the parser" {
		t.Fatalf("steering = %+v", host.steered)
	}
	if len(app.items) != parentItems {
		t.Fatalf("parent transcript gained %d items from a child steer", len(app.items)-parentItems)
	}
	if !strings.Contains(app.notice, "steering queued for explore") {
		t.Fatalf("notice = %q", app.notice)
	}
	view = ansi.Strip(app.childFocus.view.View())
	if !strings.Contains(view, "delegation steer") || !strings.Contains(view, "queued") {
		t.Fatalf("child view does not show the queued delegation steer:\n%s", view)
	}

	// The child records the line at its next turn boundary; the view drops the
	// queued copy and labels the recorded source as a delegation steer.
	updated := append(childHistory(),
		agent.HistoryItem{Kind: agent.HistoryUser, Run: 1, Source: agent.SourceSubagentSteer, Text: "focus on the parser"},
		agent.HistoryItem{Kind: agent.HistoryAssistant, Run: 1, Text: "Looking at the parser now."},
	)
	host.mu.Lock()
	host.childHistory["child-1"] = updated
	host.mu.Unlock()
	deliver(app, agent.SubagentProgress{
		CallID: "call-1", Index: 0, Agent: "explore", Session: "child-1",
		ChildTurns: 2, ContextTokens: 4000,
	})
	view = ansi.Strip(app.childFocus.view.View())
	if !strings.Contains(view, "delegation steer") || !strings.Contains(view, "focus on the parser") {
		t.Fatalf("child view lost the recorded steer:\n%s", view)
	}
	if strings.Contains(view, "queued") {
		t.Fatalf("child view still claims the recorded steer is queued:\n%s", view)
	}

	// Esc leaves the child view without interrupting the child; a second Esc at
	// the parent is the normal interrupt.
	run, _, _ := host.last()
	press(t, app, "esc")
	if app.childFocus != nil {
		t.Fatal("Esc did not return to the parent")
	}
	if got := run.interrupt.Load(); got != 0 {
		t.Fatalf("Esc in the child view interrupted the parent %d times", got)
	}
	press(t, app, "esc")
	if got := run.interrupt.Load(); got != 1 {
		t.Fatalf("second Esc interrupts = %d, want 1", got)
	}
}

func TestChildFocusFinishedIsReadOnly(t *testing.T) {
	host := newAgentHost()
	host.childHistory = map[string][]agent.HistoryItem{"child-1": childHistory()}
	app := newTestApp(t, host)
	startAgentRun(t, app, "delegate the work")
	deliver(app, agent.RunStarted{Turn: 0, Model: "deepseek-v4.1-flash", Dialect: "openai-chat"})
	deliver(app, startedEvent(0, "child-1", "explore", "find the projection"))
	deliver(app, agent.SubagentFinished{
		CallID: "call-1", Index: 0, Agent: "explore", Session: "child-1",
		Status: "completed", ChildTurns: 1, ContextTokens: 100,
	})

	submitComposer(t, app, "/agent-hub")
	press(t, app, "enter")
	if app.childFocus == nil || !app.childFocus.readOnly {
		t.Fatalf("finished child focus = %+v", app.childFocus)
	}
	parentItems := len(app.items)
	typeText(t, app, "one more thing")
	press(t, app, "enter")
	if len(host.steered) != 0 {
		t.Fatalf("a finished child accepted steering: %+v", host.steered)
	}
	if len(app.items) != parentItems {
		t.Fatal("a rejected child steer changed the parent transcript")
	}
	if !strings.Contains(app.notice, "finished") {
		t.Fatalf("notice = %q", app.notice)
	}
	if got := app.composer.Value(); got != "one more thing" {
		t.Fatalf("rejected child steer was lost; composer = %q", got)
	}
	run, _, _ := host.last()
	press(t, app, "esc") // clear the restored draft
	press(t, app, "esc") // leave the read-only view
	if app.childFocus != nil {
		t.Fatal("Esc did not leave the read-only child view")
	}
	if got := run.interrupt.Load(); got != 0 {
		t.Fatalf("Esc in the child view interrupted the parent %d times", got)
	}
}

func TestChildFocusLabelsDelegationSteerAndSanitizesText(t *testing.T) {
	lines := renderChildHistory(newTheme(true), []agent.HistoryItem{
		{Kind: agent.HistoryUser, Run: 1, Source: "delegate", Text: "parent task"},
		{Kind: agent.HistoryUser, Run: 1, Source: agent.SourceSubagentSteer, Text: "steer\x1b[31m the child"},
	}, nil, 80, true, true)
	body := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(body, "task parent task") {
		t.Fatalf("delegation task label missing:\n%s", body)
	}
	if !strings.Contains(body, "delegation steer steer␛[31m the child") {
		t.Fatalf("delegation steer label missing or unsanitized:\n%s", body)
	}
	if strings.Contains(body, "user ") {
		t.Fatalf("child view labels a delegation steer as a human user line:\n%s", body)
	}
	if strings.Contains(body, "\x1b[31m") {
		t.Fatalf("child view leaked a terminal escape: %q", body)
	}
}

func TestChildFocusHistoryErrorStaysInTheView(t *testing.T) {
	host := newAgentHost()
	host.childHistoryErr = agent.ErrSubagentNotRunning
	app := newTestApp(t, host)
	startAgentRun(t, app, "delegate the work")
	deliver(app, agent.RunStarted{Turn: 0, Model: "m", Dialect: "openai-chat"})
	deliver(app, startedEvent(0, "child-1", "explore", "find it"))
	submitComposer(t, app, "/agent-hub")
	press(t, app, "enter")
	if app.childFocus == nil || !strings.Contains(app.childFocus.err, "not running") {
		t.Fatalf("focus error = %+v", app.childFocus)
	}
	view := ansi.Strip(app.childFocus.view.View())
	if !strings.Contains(view, "error:") || !strings.Contains(view, "not running") {
		t.Fatalf("child view did not report the read error:\n%s", view)
	}
}
