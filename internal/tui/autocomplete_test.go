package tui

import (
	"strings"
	"testing"
)

func TestSlashPrefixAndMatching(t *testing.T) {
	prefixes := []struct {
		value  string
		prefix string
		ok     bool
	}{
		{"/", "", true},
		{"/mod", "mod", true},
		{"/MODEL", "MODEL", true},
		{"/model ", "", false},
		{"/model id", "", false},
		{"/model\n", "", false},
		{"hello", "", false},
		{"hello /model", "", false},
	}
	for _, tc := range prefixes {
		prefix, ok := slashPrefix(tc.value)
		if prefix != tc.prefix || ok != tc.ok {
			t.Errorf("slashPrefix(%q) = %q, %v; want %q, %v", tc.value, prefix, ok, tc.prefix, tc.ok)
		}
	}

	if got := len(matchSlashCommands("")); got != len(slashCommands) {
		t.Fatalf("empty prefix matches = %d, want %d", got, len(slashCommands))
	}
	matches := matchSlashCommands("ri")
	if len(matches) != 1 || matches[0].names[0] != "rich" {
		t.Fatalf("ri matches = %+v, want rich", matches)
	}
	// "file" is an alias of "files"; the command is listed once.
	matches = matchSlashCommands("f")
	if len(matches) != 1 || matches[0].names[0] != "files" {
		t.Fatalf("f matches = %+v, want files once", matches)
	}
	if matches := matchSlashCommands("zz"); len(matches) != 0 {
		t.Fatalf("zz matches = %+v, want none", matches)
	}
}

func TestAutocompleteMenuAndEnter(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)

	typeText(t, app, "/")
	if !app.suggestionsVisible() {
		t.Fatal("typing / did not open the menu")
	}
	if len(app.suggestions) != len(slashCommands) {
		t.Fatalf("menu entries = %d, want %d", len(app.suggestions), len(slashCommands))
	}
	if got := app.suggestions[app.suggestionIndex].names[0]; got != "help" {
		t.Fatalf("initial selection = %q, want help", got)
	}
	if lines := strings.Count(app.baseFrame(), "\n") + 1; lines != 24 {
		t.Fatalf("base frame has %d lines, want 24", lines)
	}

	press(t, app, "enter")
	if app.overlay.kind != OverlayHelp {
		t.Fatalf("overlay = %q, want help", app.overlay.kind)
	}
	if _, _, ok := host.last(); ok {
		t.Fatal("autocomplete Enter started a run")
	}
}

func TestAutocompleteSelectionAndEnter(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)

	typeText(t, app, "/")
	if app.suggestionIndex != 0 {
		t.Fatalf("initial index = %d, want 0", app.suggestionIndex)
	}
	press(t, app, "up")
	if app.suggestionIndex != 0 {
		t.Fatalf("up at the top moved to %d", app.suggestionIndex)
	}
	press(t, app, "down")
	if got := app.suggestions[app.suggestionIndex].names[0]; got != "model" {
		t.Fatalf("selection after down = %q, want model", got)
	}
	press(t, app, "enter")
	if app.overlay.kind != OverlayModel {
		t.Fatalf("overlay = %q, want model", app.overlay.kind)
	}
	if _, _, ok := host.last(); ok {
		t.Fatal("autocomplete Enter started a run")
	}
}

func TestAutocompleteWindowKeepsSelectionVisible(t *testing.T) {
	app := newTestApp(t, newManualHost())
	typeText(t, app, "/")
	if got := app.visibleSuggestionCount(); got != 8 {
		t.Fatalf("visible suggestions = %d, want 8", got)
	}
	for range len(slashCommands) - 1 {
		press(t, app, "down")
	}
	if got := app.suggestionIndex; got != len(slashCommands)-1 {
		t.Fatalf("index = %d, want %d", got, len(slashCommands)-1)
	}
	start := app.suggestionWindowStart(app.visibleSuggestionCount())
	if start != len(slashCommands)-app.visibleSuggestionCount() {
		t.Fatalf("window start = %d, want the last page", start)
	}
	if start+app.visibleSuggestionCount() <= app.suggestionIndex {
		t.Fatalf("selection %d is outside window %d..%d", app.suggestionIndex, start, start+app.visibleSuggestionCount())
	}
}

func TestAutocompleteTabCompletes(t *testing.T) {
	host := newManualHost()
	host.models[0].ReasoningEfforts = []string{"low", "high"}
	app := newTestApp(t, host)

	typeText(t, app, "/he")
	press(t, app, "tab")
	if got := app.composer.Value(); got != "/help" {
		t.Fatalf("completion = %q, want /help", got)
	}
	if app.suggestionsVisible() {
		t.Fatal("menu stayed open after completion")
	}
	press(t, app, "enter")
	if app.overlay.kind != OverlayHelp {
		t.Fatalf("overlay = %q, want help", app.overlay.kind)
	}
	press(t, app, "esc")

	typeText(t, app, "/eff")
	press(t, app, "tab")
	if got := app.composer.Value(); got != "/effort " {
		t.Fatalf("argument completion = %q, want /effort ", got)
	}
	typeText(t, app, "high")
	press(t, app, "enter")
	if app.nextEffort != "high" {
		t.Fatalf("nextEffort = %q, want high", app.nextEffort)
	}
}

func TestAutocompleteEscDismissesMenu(t *testing.T) {
	app := newTestApp(t, newManualHost())
	typeText(t, app, "/mod")
	if !app.suggestionsVisible() {
		t.Fatal("menu did not open")
	}

	press(t, app, "esc")
	if app.composer.Value() != "/mod" {
		t.Fatalf("dismissal changed the composer to %q", app.composer.Value())
	}
	if app.suggestionsVisible() {
		t.Fatal("menu survived Esc")
	}
	press(t, app, "esc")
	if app.composer.Value() != "" {
		t.Fatalf("second Esc left %q", app.composer.Value())
	}
}

func TestAutocompleteEscDuringRunDismissesBeforeInterrupt(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	run, _ := startTestRun(t, app, "task")

	typeText(t, app, "/mod")
	press(t, app, "esc")
	if app.composer.Value() != "/mod" || app.suggestionsVisible() {
		t.Fatalf("dismissal: composer=%q suggestions=%v", app.composer.Value(), app.suggestionsVisible())
	}
	if got := run.interrupt.Load(); got != 0 {
		t.Fatalf("interrupt calls = %d, want 0 while dismissing the menu", got)
	}

	press(t, app, "esc")
	if got := run.interrupt.Load(); got != 1 {
		t.Fatalf("interrupt calls = %d, want 1 after the menu closed", got)
	}
	if app.composer.Value() != "/mod" {
		t.Fatalf("active-run interrupt changed the composer to %q", app.composer.Value())
	}
}

func TestAutocompleteEnterCommandDuringRun(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	startTestRun(t, app, "task")

	typeText(t, app, "/mod")
	press(t, app, "enter")
	if !strings.Contains(app.notice, "disabled while a run is active") {
		t.Fatalf("notice = %q", app.notice)
	}
	if len(app.steer) != 0 {
		t.Fatalf("autocomplete queued steering: %v", app.steer)
	}
	if !app.run.active {
		t.Fatal("command ended the run")
	}
}

func TestAutocompleteHiddenOutsideCommandName(t *testing.T) {
	app := newTestApp(t, newManualHost())

	typeText(t, app, "/model ")
	if app.suggestionsVisible() {
		t.Fatal("menu opened while typing arguments")
	}
	typeText(t, app, "id")
	if app.suggestionsVisible() {
		t.Fatal("menu opened for an argument")
	}

	app.clearComposer()
	typeText(t, app, "hello /model")
	if app.suggestionsVisible() {
		t.Fatal("menu opened for a slash later in the line")
	}

	app.clearComposer()
	typeText(t, app, "/zz")
	if app.suggestionsVisible() {
		t.Fatal("menu opened with no matches")
	}
}

func TestAutocompleteUpdatesAfterBackspace(t *testing.T) {
	app := newTestApp(t, newManualHost())
	typeText(t, app, "/modx")
	if app.suggestionsVisible() {
		t.Fatal("menu opened with no matches")
	}

	press(t, app, "backspace")
	if !app.suggestionsVisible() {
		t.Fatal("backspace did not recompute the menu")
	}
	if got := app.suggestions[app.suggestionIndex].names[0]; got != "model" {
		t.Fatalf("selection = %q, want model", got)
	}
}

func TestAutocompleteLayoutAtMinimumSize(t *testing.T) {
	app := newTestApp(t, newManualHost())
	app.setSize(40, 10)
	typeText(t, app, "/")

	if got := app.visibleSuggestionCount(); got != 6 {
		t.Fatalf("visible suggestions = %d, want 6", got)
	}
	if got := app.composerHeight(); got != 7 {
		t.Fatalf("composer height = %d, want 7", got)
	}
	if lines := strings.Count(app.baseFrame(), "\n") + 1; lines != 10 {
		t.Fatalf("base frame has %d lines, want 10", lines)
	}
}

func TestAutocompleteHistoryRecallDoesNotCaptureArrows(t *testing.T) {
	app := newTestApp(t, newManualHost())

	typeText(t, app, "/help")
	press(t, app, "enter")
	press(t, app, "esc")

	press(t, app, "up")
	if got := app.composer.Value(); got != "/help" {
		t.Fatalf("recalled %q, want /help", got)
	}
	if app.suggestionsVisible() {
		t.Fatal("recall immediately reopened the menu")
	}

	// Editing the recalled command re-enables the menu.
	press(t, app, "backspace")
	if !app.suggestionsVisible() {
		t.Fatal("editing the recalled command did not reopen the menu")
	}
	if got := app.suggestions[app.suggestionIndex].names[0]; got != "help" {
		t.Fatalf("selection = %q, want help", got)
	}
}
