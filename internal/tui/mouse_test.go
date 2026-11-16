package tui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/eddiectc/aharness/internal/agent"
)

func wheelMsg(up bool) tea.MouseWheelMsg {
	if up {
		return tea.MouseWheelMsg{Button: tea.MouseWheelUp}
	}
	return tea.MouseWheelMsg{Button: tea.MouseWheelDown}
}

// scrollableApp returns an app whose transcript is taller than the viewport.
func scrollableApp(t *testing.T) *App {
	t.Helper()
	host := newManualHost()
	for i := range 60 {
		host.history = append(host.history, agent.HistoryItem{
			Kind: agent.HistoryAssistant,
			Text: fmt.Sprintf("history line %d", i),
		})
	}
	app := newTestApp(t, host)
	app.ensureContent()
	if !app.follow {
		t.Fatal("a fresh transcript should follow the tail")
	}
	if app.viewport.YOffset() == 0 {
		t.Fatal("test transcript is not scrollable")
	}
	return app
}

func TestMouseWheelScrollsTheTranscript(t *testing.T) {
	app := scrollableApp(t)
	bottom := app.viewport.YOffset()

	updateApp(t, app, wheelMsg(true))
	if app.follow {
		t.Fatal("scrolling up must stop following the live tail")
	}
	if got := app.viewport.YOffset(); got != bottom-mouseWheelLines {
		t.Fatalf("offset after wheel up = %d, want %d", got, bottom-mouseWheelLines)
	}

	// Wheel down returns to the bottom and resumes following.
	for range 100 {
		if app.follow {
			break
		}
		updateApp(t, app, wheelMsg(false))
	}
	if !app.follow || !app.viewport.AtBottom() {
		t.Fatalf("wheel down did not resume following (follow=%v offset=%d)", app.follow, app.viewport.YOffset())
	}
}

func TestMouseWheelUpStopsAtTheTop(t *testing.T) {
	app := scrollableApp(t)
	for range 1000 {
		updateApp(t, app, wheelMsg(true))
	}
	if got := app.viewport.YOffset(); got != 0 {
		t.Fatalf("offset = %d, want the top", got)
	}
}

func TestMouseClicksAndMotionAreInert(t *testing.T) {
	app := scrollableApp(t)
	offset := app.viewport.YOffset()
	follow := app.follow
	updateApp(t, app, tea.MouseClickMsg{Button: tea.MouseLeft})
	updateApp(t, app, tea.MouseReleaseMsg{Button: tea.MouseLeft})
	updateApp(t, app, tea.MouseMotionMsg{Button: tea.MouseLeft})
	updateApp(t, app, tea.MouseWheelMsg{Button: tea.MouseWheelLeft})
	if app.viewport.YOffset() != offset || app.follow != follow {
		t.Fatalf("a non-vertical mouse event moved the transcript: offset %d -> %d", offset, app.viewport.YOffset())
	}
	if app.composer.Value() != "" {
		t.Fatalf("a mouse event reached the composer: %q", app.composer.Value())
	}
}

func TestMouseWheelScrollsHelpOverlayWithoutTheTranscript(t *testing.T) {
	app := scrollableApp(t)
	offset := app.viewport.YOffset()
	press(t, app, "f1")
	if app.overlay.kind != OverlayHelp {
		t.Fatalf("overlay = %q", app.overlay.kind)
	}
	updateApp(t, app, wheelMsg(false))
	if got := app.overlay.scroll; got != mouseWheelLines {
		t.Fatalf("help scroll down = %d, want %d", got, mouseWheelLines)
	}
	// Wheel up clamps at the top instead of going negative.
	updateApp(t, app, wheelMsg(true))
	updateApp(t, app, wheelMsg(true))
	if got := app.overlay.scroll; got != 0 {
		t.Fatalf("help scroll up = %d, want 0", got)
	}
	if app.viewport.YOffset() != offset {
		t.Fatalf("the wheel leaked into the transcript: %d -> %d", offset, app.viewport.YOffset())
	}
	press(t, app, "esc")
}

func TestMouseWheelMovesPickerSelection(t *testing.T) {
	app := newTestApp(t, newManualHost())
	press(t, app, "ctrl+p")
	if app.overlay.kind != OverlayModel {
		t.Fatalf("overlay = %q", app.overlay.kind)
	}
	first := selectedModelID(t, app)
	updateApp(t, app, wheelMsg(false))
	if got := selectedModelID(t, app); got == first {
		t.Fatalf("wheel down did not move the selection from %q", first)
	}
	updateApp(t, app, wheelMsg(true))
	if got := selectedModelID(t, app); got != first {
		t.Fatalf("wheel up = %q, want %q", got, first)
	}
}

func TestViewReportsCellMotionMouseAndNoFocusReporting(t *testing.T) {
	app := newTestApp(t, newManualHost())
	view := app.View()
	if view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("MouseMode = %v, want cell motion (wheel only)", view.MouseMode)
	}
	if view.ReportFocus {
		t.Fatal("focus reporting must stay disabled")
	}
}

func TestMouseWheelScrollSticksWhileStreaming(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	startTestRun(t, app, "long answer")
	for i := range 40 {
		deliver(app, agent.TextDelta{Text: strings.Repeat("line ", 20) + fmt.Sprintf("%d\n", i)})
	}
	app.ensureContent()
	app.coalesceScheduled = false
	updateApp(t, app, coalesceMsg{})
	if !app.follow || app.viewport.YOffset() == 0 {
		t.Fatalf("streaming transcript should follow the tail (follow=%v offset=%d)", app.follow, app.viewport.YOffset())
	}

	updateApp(t, app, wheelMsg(true))
	scrolled := app.viewport.YOffset()
	if app.follow {
		t.Fatal("wheel up during a run must detach the tail")
	}

	// A streamed delta must not yank the view back to the bottom.
	deliver(app, agent.TextDelta{Text: "more text\n"})
	app.coalesceScheduled = false
	updateApp(t, app, coalesceMsg{})
	if app.follow {
		t.Fatal("a streamed delta re-attached the tail after the user scrolled up")
	}
	if got := app.viewport.YOffset(); got != scrolled {
		t.Fatalf("offset after a delta = %d, want %d", got, scrolled)
	}
}

// TestRawWheelBytesBecomeWheelMessages drives the real Bubble Tea input
// decoder with SGR wheel sequences, proving the terminal path reaches the app
// and not just the tea.MouseWheelMsg constructed directly by other tests.
func TestRawWheelBytesBecomeWheelMessages(t *testing.T) {
	app := newTestApp(t, newManualHost())
	pr, pw := io.Pipe()
	var mu sync.Mutex
	var seen []tea.MouseButton
	program := tea.NewProgram(app,
		tea.WithInput(pr),
		tea.WithOutput(io.Discard),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignalHandler(),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			// Record what the decoder produced; the app's own Update path is
			// covered by the mouse tests above.
			if wheel, ok := msg.(tea.MouseWheelMsg); ok {
				mu.Lock()
				seen = append(seen, wheel.Button)
				mu.Unlock()
			}
			return msg
		}),
	)
	done := make(chan struct{})
	go func() {
		_, _ = program.Run()
		close(done)
	}()
	defer func() {
		_ = pw.Close()
		program.Quit()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("program did not stop")
		}
	}()

	// Wait for the first frame so the renderer has enabled mouse reporting
	// before the wheel bytes arrive.
	acks := make(chan int, 4)
	app.acks = acks
	program.Send(syncMsg{id: 1})
	select {
	case id := <-acks:
		if id != 1 {
			t.Fatalf("sync ack = %d", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("program did not start")
	}
	// SGR mouse reporting: 64 = wheel up, 65 = wheel down, at column/row 5.
	if _, err := pw.Write([]byte("\x1b[<64;5;5M")); err != nil {
		t.Fatal(err)
	}
	if _, err := pw.Write([]byte("\x1b[<65;5;5M")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "wheel messages decoded", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) >= 2
	})
	mu.Lock()
	defer mu.Unlock()
	if seen[0] != tea.MouseWheelUp || seen[1] != tea.MouseWheelDown {
		t.Fatalf("decoded wheel buttons = %v, want up then down", seen)
	}
}
