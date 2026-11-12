package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// transcriptText renders the transcript block as plain text.
func transcriptText(app *App) string {
	app.ensureContent()
	return ansi.Strip(strings.Join(app.transcriptLines(), "\n"))
}

// hasHeading reports whether a transcript row is exactly the given heading.
func hasHeading(app *App, heading string) bool {
	app.ensureContent()
	for _, line := range app.transcriptLines() {
		if ansi.Strip(line) == heading {
			return true
		}
	}
	return false
}

func TestEmptyTranscriptListsTips(t *testing.T) {
	app := newTestApp(t, newManualHost())
	text := transcriptText(app)
	for _, want := range []string{
		"No messages yet",
		"F1",
		"Shift+drag",
		"Ctrl+Shift+V",
		"wheel",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("empty transcript is missing %q:\n%s", want, text)
		}
	}
	if !hasHeading(app, "tips") {
		t.Errorf("empty transcript has no tips heading:\n%s", text)
	}
}

// The tips are onboarding only: they vanish once the transcript has content,
// so a live or resumed session never mixes them into its history.
func TestEmptyTranscriptTipsYieldToContent(t *testing.T) {
	app := newTestApp(t, newManualHost())
	startTestRun(t, app, "task")
	if hasHeading(app, "tips") {
		t.Fatalf("tips stayed after the first item:\n%s", transcriptText(app))
	}
}

// The 40x10 minimum terminal has seven transcript rows. The block must fit
// there without clipping words or pushing the composer off-screen.
func TestEmptyTranscriptTipsFitTheMinimumTerminal(t *testing.T) {
	app := newTestApp(t, newManualHost())
	app.setSize(minWidth, minHeight)
	lines := app.transcriptLines()
	if len(lines) > app.viewport.Height() {
		t.Fatalf("tips block has %d rows, viewport height is %d", len(lines), app.viewport.Height())
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > minWidth {
			t.Fatalf("row %q is %d cells wide, the minimum terminal is %d", ansi.Strip(line), width, minWidth)
		}
	}
	if frame := app.baseFrame(); strings.Count(frame, "\n")+1 != minHeight {
		t.Fatalf("composed frame is %d rows, want %d", strings.Count(frame, "\n")+1, minHeight)
	}
}

// A transcript with little room keeps the one-line prompt rather than a tips
// heading whose rows have been cut away.
func TestEmptyTranscriptTipsDropWhenTheTranscriptIsShort(t *testing.T) {
	for _, height := range []int{2, 4, 5, 21} {
		app := newTestApp(t, newManualHost())
		app.viewport.SetHeight(height)
		lines := app.transcriptLines()
		text := ansi.Strip(strings.Join(lines, "\n"))
		if !strings.Contains(text, "No messages yet") {
			t.Errorf("height %d: prompt dropped:\n%s", height, text)
		}
		if len(lines) > height {
			t.Errorf("height %d: %d rows overflow the viewport:\n%s", height, len(lines), text)
		}
		// Five rows hold the prompt, the heading, and two tips: the smallest
		// block worth showing. Below that the prompt stands alone.
		want := height >= 5
		if got := hasHeading(app, "tips"); got != want {
			t.Errorf("height %d: tips heading shown = %v, want %v:\n%s", height, got, want, text)
		}
	}
}

func TestHelpOverlayDocumentsTheTerminalMouseBypass(t *testing.T) {
	app := newTestApp(t, newManualHost())
	press(t, app, "f1")
	lines := app.overlay.help
	text := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"wheel scrolls", "Shift while dragging", "iTerm2", "Ctrl+Shift+V"} {
		if !strings.Contains(text, want) {
			t.Errorf("help is missing %q:\n%s", want, text)
		}
	}
	if slices.ContainsFunc(lines, func(line string) bool {
		return strings.Contains(ansi.Strip(line), "clicks are inert")
	}) {
		t.Errorf("help still claims clicks are inert:\n%s", text)
	}
}
