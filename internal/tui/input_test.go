package tui

import (
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startProgram(t *testing.T, app *App) (*tea.Program, *io.PipeWriter, chan struct{}, chan int) {
	t.Helper()
	pr, pw := io.Pipe()
	acks := make(chan int, 4)
	app.acks = acks
	program := tea.NewProgram(app,
		tea.WithInput(pr),
		tea.WithOutput(io.Discard),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignalHandler(),
	)
	done := make(chan struct{})
	go func() {
		_, _ = program.Run()
		close(done)
	}()
	program.Send(syncMsg{id: 1})
	select {
	case id := <-acks:
		if id != 1 {
			t.Fatalf("sync ack = %d", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("program did not start")
	}
	return program, pw, done, acks
}

func queryComposer(t *testing.T, program *tea.Program) string {
	t.Helper()
	reply := make(chan StateSnapshot, 1)
	program.Send(stateQueryMsg{reply: reply})
	select {
	case state := <-reply:
		return state.Composer
	case <-time.After(3 * time.Second):
		t.Fatal("state query timed out")
		return ""
	}
}

func stopProgram(t *testing.T, program *tea.Program, pw *io.PipeWriter, done chan struct{}) {
	t.Helper()
	_ = pw.Close()
	program.Quit()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("program did not stop")
	}
}

func TestRawInputOptionBracketDoesNotInterrupt(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	typeText(t, app, "task")
	press(t, app, "enter")
	run, _, ok := host.last()
	if !ok {
		t.Fatal("run did not start")
	}
	program, pw, done, _ := startProgram(t, app)
	defer stopProgram(t, program, pw, done)

	if _, err := pw.Write([]byte("\x1b[")); err != nil { // Option-[
		t.Fatal(err)
	}
	if _, err := pw.Write([]byte("\x1b")); err != nil { // bare Esc after the 50 ms timeout
		t.Fatal(err)
	}
	eventually(t, "bare Esc interrupt", func() bool { return run.interrupt.Load() >= 1 })
	// Wait past the 50 ms escape timeout to prove Option-[ did not also fire.
	time.Sleep(100 * time.Millisecond)
	if got := run.interrupt.Load(); got != 1 {
		t.Fatalf("interrupts = %d, want only the bare Esc", got)
	}
}

func TestRawInputSplitUTF8AndPaste(t *testing.T) {
	host := newManualHost()
	app := newTestApp(t, host)
	program, pw, done, _ := startProgram(t, app)
	defer stopProgram(t, program, pw, done)

	if _, err := pw.Write([]byte{0xc3}); err != nil {
		t.Fatal(err)
	}
	if _, err := pw.Write([]byte{0xa9}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "split UTF-8 rune", func() bool { return queryComposer(t, program) == "é" })
	if _, err := pw.Write([]byte("\x1b[200~pasted\nline\x1b[201~")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bracketed paste", func() bool {
		return queryComposer(t, program) == "é"+"pasted\nline"
	})
}
