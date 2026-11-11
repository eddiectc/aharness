package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"aharness/internal/model"
	"aharness/internal/tools"
)

func submitComposer(t *testing.T, app *App, text string) tea.Cmd {
	t.Helper()
	typeText(t, app, text)
	return press(t, app, "enter")
}

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		text string
		name string
		args string
		ok   bool
	}{
		{"/model", "model", "", true},
		{"/MODEL", "model", "", true},
		{"/model   crouton-alpha-free  ", "model", "crouton-alpha-free", true},
		{"/effort high", "effort", "high", true},
		{"/file", "files", "", true},
		{"/exit", "quit", "", true},
		{"/unknown", "", "", false},
		{"/", "", "", false},
		{" /model", "", "", false},
		{"model", "", "", false},
	}
	for _, tc := range cases {
		command, args, ok := parseSlashCommand(tc.text)
		if ok != tc.ok {
			t.Errorf("parseSlashCommand(%q) ok = %v, want %v", tc.text, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if command.names[0] != tc.name || args != tc.args {
			t.Errorf("parseSlashCommand(%q) = %q, %q; want %q, %q",
				tc.text, command.names[0], args, tc.name, tc.args)
		}
	}
}

func TestSlashHelpListsCommandsAndDoesNotRun(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)

	if cmd := submitComposer(t, app, "/help"); cmd != nil {
		t.Fatalf("/help returned %v, want no command", cmd)
	}
	if app.overlay.kind != OverlayHelp {
		t.Fatalf("overlay = %q, want help", app.overlay.kind)
	}
	text := strings.Join(app.overlay.help, "\n")
	for _, want := range []string{
		"commands", "/help", "/model [id]", "/provider", "/effort [value]",
		"/files", "/table", "/image", "/rich", "/raw", "/repaint", "/refresh", "/quit",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help is missing %q:\n%s", want, text)
		}
	}
	if _, _, ok := host.last(); ok {
		t.Fatal("/help started a run")
	}
}

func TestSlashModelPickerAndDirectSelection(t *testing.T) {
	host := &selectingHost{manualHost: newManualHost()}
	app, events := newTestAppWithEvents(t, host)

	submitComposer(t, app, "/MODEL")
	if app.overlay.kind != OverlayModel {
		t.Fatalf("overlay = %q, want model", app.overlay.kind)
	}
	press(t, app, "esc")

	submitComposer(t, app, "/model crouton-alpha-free")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("selection left overlay %q", app.overlay.kind)
	}
	if app.nextModel != "crouton-alpha-free" {
		t.Fatalf("nextModel = %q, want the requested model", app.nextModel)
	}
	if len(host.selected) != 1 || host.selected[0] != "crouton-alpha-free" {
		t.Fatalf("selected = %v", host.selected)
	}
	select {
	case event := <-events:
		changed, ok := event.(EventModelChanged)
		if !ok || changed.Model != "crouton-alpha-free" {
			t.Fatalf("event = %+v, want model_changed", event)
		}
	default:
		t.Fatal("no model_changed event")
	}

	submitComposer(t, app, "/model nope")
	if app.nextModel != "crouton-alpha-free" {
		t.Fatalf("unknown ID changed the model: %q", app.nextModel)
	}
	if !strings.Contains(app.notice, "unknown model") {
		t.Fatalf("notice = %q", app.notice)
	}
	if _, _, ok := host.last(); ok {
		t.Fatal("/model started a run")
	}
}

func TestSlashEffortArguments(t *testing.T) {
	host := newManualHost()
	host.models[0].ReasoningEfforts = []string{"low", "high", "max"}
	app, events := newTestAppWithEvents(t, host)

	submitComposer(t, app, "/effort high")
	if app.nextEffort != "high" {
		t.Fatalf("nextEffort = %q, want high", app.nextEffort)
	}
	select {
	case event := <-events:
		notice, ok := event.(EventNotice)
		if !ok || notice.Kind != "thinking_effort" {
			t.Fatalf("event = %+v, want thinking_effort notice", event)
		}
	default:
		t.Fatal("no thinking_effort notice")
	}

	submitComposer(t, app, "/effort impossible")
	if app.nextEffort != "high" {
		t.Fatalf("invalid value changed the effort: %q", app.nextEffort)
	}
	if !strings.Contains(app.notice, "does not advertise") || !strings.Contains(app.notice, "low, high, max") {
		t.Fatalf("notice = %q", app.notice)
	}

	submitComposer(t, app, "/effort default")
	if app.nextEffort != "" {
		t.Fatalf("default left effort %q", app.nextEffort)
	}
	if !strings.Contains(app.notice, "provider default") {
		t.Fatalf("notice = %q", app.notice)
	}
	if _, _, ok := host.last(); ok {
		t.Fatal("/effort started a run")
	}
}

func TestUnknownSlashTextIsSubmittedAsMessage(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	const text = "/etc/hosts is broken"

	_, call := startTestRun(t, app, text)
	if call.sub.Text != text || call.sub.Source != "tui" {
		t.Fatalf("submission = %+v, want the literal text from the composer", call.sub)
	}
	if app.overlay.kind != OverlayNone {
		t.Fatalf("overlay = %q, want none", app.overlay.kind)
	}
}

func TestSlashProviderOpensConfigurationDetails(t *testing.T) {
	host := &detailsHost{
		manualHost: newManualHost(),
		details: Details{
			Provider: "opencode-go",
			BaseURL:  "https://opencode.ai/inference/go/openai/v1",
		},
	}
	app := newTestApp(t, host)

	submitComposer(t, app, "/provider")
	if app.overlay.kind != OverlayDetails {
		t.Fatalf("overlay = %q, want details", app.overlay.kind)
	}
	text := strings.Join(app.overlay.help, "\n")
	if !strings.Contains(text, "provider:   opencode-go") {
		t.Fatalf("details are missing the provider:\n%s", text)
	}
	if !strings.Contains(text, "https://opencode.ai/inference/go/openai/v1") {
		t.Fatalf("details are missing the base URL:\n%s", text)
	}
	if !strings.Contains(app.notice, "provider: opencode-go") {
		t.Fatalf("notice = %q", app.notice)
	}
	if _, _, ok := host.last(); ok {
		t.Fatal("/provider started a run")
	}
}

func TestSlashShortcutSurfaces(t *testing.T) {
	host := newManualHost()
	ws, err := tools.OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	host.ws = ws
	app := newTestApp(t, host)

	submitComposer(t, app, "/files")
	if app.overlay.kind != OverlayFile {
		t.Fatalf("overlay = %q, want file (notice %q)", app.overlay.kind, app.notice)
	}
	press(t, app, "esc")

	submitComposer(t, app, "/raw")
	if app.rich || app.notice != "raw rendering" {
		t.Fatalf("raw rendering: rich=%v notice=%q", app.rich, app.notice)
	}
	submitComposer(t, app, "/rich")
	if !app.rich || app.notice != "rich rendering" {
		t.Fatalf("rich rendering: rich=%v notice=%q", app.rich, app.notice)
	}
	cmd := submitComposer(t, app, "/repaint")
	if cmd == nil {
		t.Fatal("/repaint returned no command")
	}
	if msg := cmd(); msg != tea.ClearScreen() {
		t.Fatalf("/repaint returned %T, want tea.ClearScreen", msg)
	}

	app.addItem(&transcriptItem{kind: itemAssistant, text: "| a | b |\n| --- | --- |\n| 1 | 2 |"})
	submitComposer(t, app, "/table")
	if app.overlay.kind != OverlayTable {
		t.Fatalf("overlay = %q, want table", app.overlay.kind)
	}
	press(t, app, "esc")

	submitComposer(t, app, "/details")
	if app.overlay.kind != OverlayDetails {
		t.Fatalf("overlay = %q, want details", app.overlay.kind)
	}
	press(t, app, "esc")

	submitComposer(t, app, "/image")
	if !strings.Contains(app.notice, "no image block") {
		t.Fatalf("notice = %q", app.notice)
	}
	if _, _, ok := host.last(); ok {
		t.Fatal("a display command started a run")
	}
}

func TestSlashCommandsDuringRun(t *testing.T) {
	host := newManualHost()
	host.models[0].ReasoningEfforts = []string{"high"}
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "task")

	submitComposer(t, app, "/model")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("model picker opened during a run: %q", app.overlay.kind)
	}
	if !strings.Contains(app.notice, "disabled while a run is active") {
		t.Fatalf("notice = %q", app.notice)
	}

	submitComposer(t, app, "/effort high")
	if app.nextEffort == "high" {
		t.Fatal("effort changed during a run")
	}
	if !strings.Contains(app.notice, "disabled while a run is active") {
		t.Fatalf("notice = %q", app.notice)
	}

	submitComposer(t, app, "/interrupt")
	if got := run.interrupt.Load(); got != 1 {
		t.Fatalf("interrupt calls = %d, want 1", got)
	}
	if app.notice != "interrupt requested" {
		t.Fatalf("notice = %q", app.notice)
	}

	submitComposer(t, app, "/cancel")
	if got := run.terminate.Load(); got != 1 {
		t.Fatalf("terminate calls = %d, want 1", got)
	}
	if app.notice != "canceling the run" || !app.run.active {
		t.Fatalf("cancel: notice=%q active=%v", app.notice, app.run.active)
	}
	if len(app.steer) != 0 {
		t.Fatalf("commands were queued as steering: %v", app.steer)
	}
}

func TestSlashQuitCancelsActiveRun(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "task")

	if cmd := submitComposer(t, app, "/quit"); cmd != nil {
		t.Fatalf("/quit returned %v while active", cmd)
	}
	if !app.quitAfter || app.quitCode != 130 {
		t.Fatalf("quit: after=%v code=%d", app.quitAfter, app.quitCode)
	}
	if got := run.terminate.Load(); got != 1 {
		t.Fatalf("terminate calls = %d, want 1", got)
	}
}

func TestSlashRefreshOpensPickerAndRefreshes(t *testing.T) {
	host := &refreshingHost{manualHost: newManualHost()}
	host.choices = []ModelChoice{catalogChoice("new-model", "New Model", "", true)}
	app := newTestApp(t, host)

	cmd := submitComposer(t, app, "/refresh")
	if app.overlay.kind != OverlayModel {
		t.Fatalf("overlay = %q, want model", app.overlay.kind)
	}
	if cmd == nil {
		t.Fatal("/refresh did not start a catalog refresh")
	}
	updateApp(t, app, cmd())
	if len(app.models) != 1 || app.models[0].ID != "new-model" {
		t.Fatalf("models = %+v, want the refreshed entry", app.models)
	}
	if !strings.Contains(app.notice, "refreshed 1") {
		t.Fatalf("notice = %q", app.notice)
	}
}

func TestSlashCommandNotAppliedToSeedTask(t *testing.T) {
	host := newManualHost()
	app := New(Options{
		Host: host, Models: host.Models(), Model: host.info.Model,
		Dialect: model.Dialect(host.info.Dialect), Headless: true,
		Seed: &Submission{Text: "/help", Source: "-task"}, ImageProtocol: "glyph",
	})
	app.setSize(80, 24)

	updateApp(t, app, seedMsg{})
	if app.overlay.kind != OverlayNone {
		t.Fatalf("seed opened overlay %q", app.overlay.kind)
	}
	_, call, ok := host.last()
	if !ok {
		t.Fatal("seed did not start a run")
	}
	if call.sub.Text != "/help" || call.sub.Source != "-task" {
		t.Fatalf("submission = %+v, want the literal seed task", call.sub)
	}
}

func TestSlashCancelIdleAndInterruptIdle(t *testing.T) {
	app := newTestApp(t, newManualHost())

	submitComposer(t, app, "/cancel")
	if !strings.Contains(app.notice, "no run is active") {
		t.Fatalf("cancel notice = %q", app.notice)
	}
	submitComposer(t, app, "/interrupt")
	if !strings.Contains(app.notice, "no operation to interrupt") {
		t.Fatalf("interrupt notice = %q", app.notice)
	}
}
