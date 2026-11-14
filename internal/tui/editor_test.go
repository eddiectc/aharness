package tui

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"aharness/internal/model"
)

func newEditorTestApp(t *testing.T, host Host) *App {
	t.Helper()
	info := host.Info()
	app := New(Options{
		Host: host, Models: host.Models(), Model: info.Model,
		Dialect: model.Dialect(info.Dialect), ImageProtocol: "glyph",
	})
	app.setSize(80, 24)
	return app
}

func TestShiftEnterInsertsComposerNewline(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	typeText(t, app, "first")
	press(t, app, "shift+enter")
	typeText(t, app, "second")
	press(t, app, "alt+enter")
	typeText(t, app, "third")

	want := "first\nsecond\nthird"
	if got := app.composer.Value(); got != want {
		t.Fatalf("composer = %q, want %q", got, want)
	}
	if app.run.active {
		t.Fatal("modified Enter started a run")
	}
	if got := app.editorHeight(); got != 3 {
		t.Fatalf("editor height = %d, want 3", got)
	}
	press(t, app, "enter")
	run, call, ok := host.last()
	if !ok || run == nil {
		t.Fatal("plain Enter did not start a run")
	}
	if call.sub.Text != want {
		t.Fatalf("submitted %q, want %q", call.sub.Text, want)
	}
	if got := app.composer.Value(); got != "" {
		t.Fatalf("composer not cleared: %q", got)
	}
}

func TestShiftEnterDoesNotRunSlashSuggestion(t *testing.T) {
	app := newTestApp(t, newManualHost())
	typeText(t, app, "/help")
	if !app.suggestionsVisible() {
		t.Fatal("slash menu is not visible")
	}
	press(t, app, "shift+enter")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("Shift-Enter opened overlay %q", app.overlay.kind)
	}
	if got := app.composer.Value(); got != "/help\n" {
		t.Fatalf("composer = %q, want newline after command", got)
	}
}

func TestExternalEditorReplacesComposer(t *testing.T) {
	host := newManualHost()
	app := newEditorTestApp(t, host)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "fake-editor --wait")
	typeText(t, app, "draft")

	var path string
	app.execEditor = func(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		if len(cmd.Args) < 2 || cmd.Args[0] != "/bin/sh" {
			t.Errorf("editor command = %q", cmd.Args)
			return nil
		}
		path = cmd.Args[len(cmd.Args)-1]
		if !strings.HasSuffix(path, ".md") {
			t.Errorf("editor temp path = %q", path)
		}
		if err := os.WriteFile(path, []byte("edited\r\nbody\n"), 0o600); err != nil {
			t.Errorf("write edited draft: %v", err)
		}
		return func() tea.Msg { return fn(nil) }
	}
	cmd := press(t, app, "f4")
	if cmd == nil {
		t.Fatal("F4 returned no command")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("F4 command returned no message")
	}
	updateApp(t, app, msg)

	if got := app.composer.Value(); got != "edited\nbody" {
		t.Fatalf("composer = %q, want edited draft", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("editor temp file was not removed: %v", err)
	}
}

func TestExternalEditorFailureKeepsComposer(t *testing.T) {
	host := newManualHost()
	app := newEditorTestApp(t, host)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "false")
	typeText(t, app, "draft")

	var path string
	app.execEditor = func(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		path = cmd.Args[len(cmd.Args)-1]
		return func() tea.Msg { return fn(errors.New("exit status 1")) }
	}
	cmd := press(t, app, "f4")
	if cmd == nil {
		t.Fatal("F4 returned no command")
	}
	updateApp(t, app, cmd())
	if got := app.composer.Value(); got != "draft" {
		t.Fatalf("composer = %q, want the original draft", got)
	}
	if !strings.Contains(app.notice, "exit status 1") {
		t.Fatalf("notice = %q, want editor failure", app.notice)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("editor temp file was not removed: %v", err)
	}
}

func TestExternalEditorRejectsOverCapResult(t *testing.T) {
	host := newManualHost()
	app := newEditorTestApp(t, host)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true")
	typeText(t, app, "draft")

	app.execEditor = func(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		path := cmd.Args[len(cmd.Args)-1]
		if err := os.WriteFile(path, []byte(strings.Repeat("x", maxSubmissionBytes+1)), 0o600); err != nil {
			t.Errorf("write over-cap draft: %v", err)
		}
		return func() tea.Msg { return fn(nil) }
	}
	cmd := press(t, app, "f4")
	if cmd == nil {
		t.Fatal("F4 returned no command")
	}
	updateApp(t, app, cmd())
	if got := app.composer.Value(); got != "draft" {
		t.Fatalf("composer = %q, want the original draft", got)
	}
	if !strings.Contains(app.notice, "64 KiB") {
		t.Fatalf("notice = %q, want the cap error", app.notice)
	}
}

func TestExternalEditorWithoutEnvironmentShowsNotice(t *testing.T) {
	app := newEditorTestApp(t, newManualHost())
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	typeText(t, app, "draft")
	if cmd := press(t, app, "f4"); cmd != nil {
		t.Fatal("F4 returned a command without an editor")
	}
	if !strings.Contains(app.notice, "$EDITOR") {
		t.Fatalf("notice = %q, want an editor hint", app.notice)
	}
	if got := app.composer.Value(); got != "draft" {
		t.Fatalf("composer = %q, want the original draft", got)
	}
}

func TestExternalEditorUnavailableHeadless(t *testing.T) {
	app := newTestApp(t, newManualHost())
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true")
	if cmd := press(t, app, "f4"); cmd != nil {
		t.Fatal("headless F4 returned a command")
	}
	if !strings.Contains(app.notice, "unavailable") {
		t.Fatalf("notice = %q, want headless hint", app.notice)
	}
}

func TestEditorCommandKeepsPathLiteral(t *testing.T) {
	cmd := editorCommand("vim -f", "/tmp/draft with spaces.md")
	if len(cmd.Args) == 0 || cmd.Args[len(cmd.Args)-1] != "/tmp/draft with spaces.md" {
		t.Fatalf("editor command = %q", cmd.Args)
	}
	if got := cmd.Args[2]; !strings.Contains(got, "vim -f") || !strings.Contains(got, `"$1"`) {
		t.Fatalf("editor shell command = %q", got)
	}
}

func TestReadComposerTempAllowsFinalNewlineAtCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "draft.md")
	content := strings.Repeat("x", maxSubmissionBytes)
	if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readComposerTemp(path)
	if err != nil {
		t.Fatalf("readComposerTemp: %v", err)
	}
	if got != content {
		t.Fatalf("draft length = %d, want %d", len(got), len(content))
	}
}
