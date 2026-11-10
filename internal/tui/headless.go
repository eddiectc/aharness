package tui

import (
	"bufio"
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/vt"

	"aharness/internal/model"
)

// HeadlessOptions configures the -ui-json driver.
type HeadlessOptions struct {
	Host          Host
	Models        []ModelChoice
	Model         string
	Dialect       model.Dialect
	Seed          *Submission
	Width         int
	Height        int
	Stdin         io.Reader
	Stdout        io.Writer
	RawInput      io.Reader
	ColorProfile  string
	NoColor       bool
	ImageProtocol string
	Terminal      TerminalInfo
}

type emuWriter struct {
	mu  sync.Mutex
	emu *vt.SafeEmulator
}

func (w *emuWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.emu.Write(p)
}

func (w *emuWriter) lock()   { w.mu.Lock() }
func (w *emuWriter) unlock() { w.mu.Unlock() }

var (
	errProgramDone = errors.New("bubbletea program exited")
	errSignal      = errors.New("signal received")
)

type headless struct {
	program *tea.Program
	app     *App
	emu     *vt.SafeEmulator
	writer  *emuWriter
	out     io.Writer

	events chan any
	acks   chan int
	done   chan struct{}

	finalModel tea.Model
	runErr     error

	frameSeq int
	ackSeq   int
	sig      <-chan os.Signal
}

type commandResult struct {
	cmd Command
	err error
	eof bool
}

// RunHeadless runs the TUI without a PTY and speaks the versioned JSONL
// protocol on stdout. It returns the process exit code.
func RunHeadless(opts HeadlessOptions) (int, error) {
	if opts.Width <= 0 || opts.Height <= 0 {
		opts.Width, opts.Height = 100, 30
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.ImageProtocol == "" {
		opts.ImageProtocol = "glyph"
	}
	if opts.ColorProfile == "" {
		opts.ColorProfile = "truecolor"
	}
	events := make(chan any, 1024)
	acks := make(chan int, 16)
	app := New(Options{
		Host:          opts.Host,
		Models:        opts.Models,
		Model:         opts.Model,
		Dialect:       opts.Dialect,
		Seed:          opts.Seed,
		Headless:      true,
		Events:        events,
		Acks:          acks,
		ColorProfile:  opts.ColorProfile,
		NoColor:       opts.NoColor,
		ImageProtocol: opts.ImageProtocol,
		Terminal:      opts.Terminal,
	})
	emu := vt.NewSafeEmulator(opts.Width, opts.Height)
	// x/vt answers terminal queries on its input pipe. Drain it so a query
	// emitted by the renderer can never block the emulator.
	go func() { _, _ = io.Copy(io.Discard, emu) }()
	// Bubble Tea enables newline mapping when the input is not a TTY. Put the
	// virtual terminal in the matching LNM mode so a bare LF also returns the
	// carriage, exactly as the renderer assumes.
	_, _ = emu.Write([]byte("\x1b[20h"))
	writer := &emuWriter{emu: emu}
	rawInput := opts.RawInput
	if rawInput == nil {
		pr, _ := io.Pipe()
		rawInput = pr
	}
	var output io.Writer = writer
	if opts.NoColor {
		output = &noColorWriter{w: writer}
	}
	program := tea.NewProgram(app,
		tea.WithInput(rawInput),
		tea.WithOutput(output),
		tea.WithWindowSize(opts.Width, opts.Height),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}),
		tea.WithColorProfile(colorprofile.TrueColor),
		tea.WithoutSignalHandler(),
	)
	h := &headless{
		program: program,
		app:     app,
		emu:     emu,
		writer:  writer,
		out:     opts.Stdout,
		events:  events,
		acks:    acks,
		done:    make(chan struct{}),
	}
	go func() {
		h.finalModel, h.runErr = program.Run()
		close(h.done)
	}()

	cmds := make(chan commandResult)
	stopReader := make(chan struct{})
	defer close(stopReader)
	go readCommands(opts.Stdin, cmds, stopReader)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
	defer signal.Stop(sigs)
	h.sig = sigs

	if err := h.emitFrame(); err != nil {
		_ = h.shutdown(1)
		return 1, err
	}
	if opts.Seed != nil {
		program.Send(seedMsg{})
	}

	for {
		select {
		case result := <-cmds:
			if result.eof {
				return h.shutdown(0), nil
			}
			if result.err != nil {
				_ = h.writeJSON(errorLine("protocol_error", result.err.Error()))
				_ = h.shutdown(0)
				return 2, nil
			}
			switch result.cmd.Type {
			case "quit":
				return h.shutdown(0), nil
			case "snapshot":
				if err := h.emitSnapshot(); err != nil {
					if errors.Is(err, errProgramDone) {
						return h.finish(), nil
					}
					_ = h.writeJSON(errorLine("internal_error", err.Error()))
					return 1, err
				}
			case "wait":
				if err := h.wait(result.cmd); err != nil {
					if errors.Is(err, errProgramDone) {
						return h.finish(), nil
					}
					if errors.Is(err, errSignal) {
						return h.shutdown(130), nil
					}
					_ = h.writeJSON(errorLine("internal_error", err.Error()))
					return 1, err
				}
				if err := h.emitFrame(); err != nil {
					if errors.Is(err, errProgramDone) {
						return h.finish(), nil
					}
					return 1, err
				}
			default:
				if err := h.sendCommand(result.cmd); err != nil {
					if errors.Is(err, errProgramDone) {
						return h.finish(), nil
					}
					return 1, err
				}
				if err := h.emitFrame(); err != nil {
					if errors.Is(err, errProgramDone) {
						return h.finish(), nil
					}
					return 1, err
				}
			}
		case ev := <-h.events:
			if err := h.writeEvent(ev); err != nil {
				return 1, err
			}
			if err := h.emitFrame(); err != nil {
				if errors.Is(err, errProgramDone) {
					return h.finish(), nil
				}
				return 1, err
			}
		case <-h.done:
			return h.finish(), nil
		case <-sigs:
			return h.shutdown(130), nil
		}
	}
}

func readCommands(r io.Reader, out chan<- commandResult, stop <-chan struct{}) {
	send := func(result commandResult) bool {
		select {
		case out <- result:
			return true
		case <-stop:
			return false
		}
	}
	if r == nil {
		send(commandResult{eof: true})
		return
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		cmd, err := decodeCommand(bytes.Clone(line))
		if !send(commandResult{cmd: cmd, err: err}) {
			return
		}
		if err != nil {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		send(commandResult{err: fmt.Errorf("read command: %w", err)})
		return
	}
	send(commandResult{eof: true})
}

func (h *headless) sendCommand(cmd Command) error {
	switch cmd.Type {
	case "text":
		h.program.Send(inputTextMsg{text: cmd.Text})
	case "paste":
		h.program.Send(inputPasteMsg{text: cmd.Text})
	case "key":
		key, err := protocolKey(cmd.Key)
		if err != nil {
			return err
		}
		h.program.Send(key)
	case "resize":
		h.writer.lock()
		h.emu.Resize(cmd.Width, cmd.Height)
		h.writer.unlock()
		h.program.Send(tea.WindowSizeMsg{Width: cmd.Width, Height: cmd.Height})
	case "frame":
		return nil
	}
	return nil
}

func (h *headless) sync() error {
	id := h.ackSeq
	h.ackSeq++
	h.program.Send(syncMsg{id: id})
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ack := <-h.acks:
			if ack == id {
				return nil
			}
		case <-h.done:
			return errProgramDone
		case <-timer.C:
			return errors.New("renderer did not acknowledge the frame in 10s")
		}
	}
}

func (h *headless) queryFrameState() (StateSnapshot, []imageRef, error) {
	reply := make(chan frameQueryReply, 1)
	h.program.Send(frameQueryMsg{reply: reply})
	select {
	case result := <-reply:
		return result.state, result.images, nil
	case <-h.done:
		return StateSnapshot{}, nil, errProgramDone
	}
}

func (h *headless) queryState() (StateSnapshot, error) {
	reply := make(chan StateSnapshot, 1)
	h.program.Send(stateQueryMsg{reply: reply})
	select {
	case state := <-reply:
		return state, nil
	case <-h.done:
		return StateSnapshot{}, errProgramDone
	}
}

func (h *headless) emitFrame() error {
	// Force a full repaint so the captured frame never depends on a
	// renderer diff produced for an earlier frame.
	h.program.Send(tea.ClearScreen())
	state, images, err := h.queryFrameState()
	if err != nil {
		return err
	}
	if err := h.sync(); err != nil {
		return err
	}
	h.writer.lock()
	frame := captureFrame(h.emu, h.frameSeq+1, state.State, state.Overlay, images,
		h.app.colorProfile, h.app.noColor, h.app.imageProtocol)
	h.writer.unlock()
	data, ok := encodeFrame(frame)
	if !ok {
		return h.writeJSON(errorLine("frame_too_large", "frame exceeds the 1 MiB line cap; resize or request a snapshot"))
	}
	h.frameSeq++
	return h.writeJSON(data)
}

func (h *headless) emitSnapshot() error {
	h.program.Send(tea.ClearScreen())
	if err := h.sync(); err != nil {
		return err
	}
	state, _, err := h.queryFrameState()
	if err != nil {
		return err
	}
	h.writer.lock()
	frame := captureFrame(h.emu, h.frameSeq, state.State, state.Overlay, nil,
		h.app.colorProfile, h.app.noColor, h.app.imageProtocol)
	svg := snapshotSVG(h.emu, frame)
	h.writer.unlock()
	snapshot := SnapshotLine{
		V: protocolVersion, Type: "snapshot", Seq: h.frameSeq,
		Width: frame.Width, Height: frame.Height, Format: "svg", SVG: svg,
	}
	data, ok := encodeSnapshot(snapshot)
	if !ok {
		return h.writeJSON(errorLine("snapshot_too_large", "snapshot exceeds the 512 KiB SVG cap; resize or use frame"))
	}
	return h.writeJSON(data)
}

func (h *headless) writeEvent(ev any) error {
	data, err := encodeEvent(ev)
	if err != nil {
		return err
	}
	return h.writeJSON(data)
}

func (h *headless) writeJSON(v any) error {
	var data []byte
	if raw, ok := v.([]byte); ok {
		data = raw
	} else {
		var err error
		data, err = encodeAny(v)
		if err != nil {
			return err
		}
	}
	if len(data) > maxFrameLine {
		return errors.New("protocol line exceeds 1 MiB")
	}
	if _, err := h.out.Write(data); err != nil {
		return err
	}
	_, err := h.out.Write([]byte{'\n'})
	return err
}

func encodeAny(v any) ([]byte, error) {
	return jsonv2.Marshal(v)
}

func (h *headless) wait(cmd Command) error {
	deadline := time.NewTimer(time.Duration(cmd.TimeoutMS) * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	state, err := h.queryState()
	if err != nil {
		return err
	}
	met := func(s StateSnapshot) bool {
		switch cmd.Until {
		case "idle":
			return !s.RunActive
		case "run_started":
			return s.RunStarted > 0
		case "run_finished":
			return s.RunFinished > 0 && !s.RunActive
		default:
			return false
		}
	}
	if met(state) {
		return nil
	}
	for {
		select {
		case ev := <-h.events:
			if err := h.writeEvent(ev); err != nil {
				return err
			}
			if err := h.emitFrame(); err != nil {
				return err
			}
		case <-ticker.C:
			next, err := h.queryState()
			if err != nil {
				return err
			}
			if met(next) {
				return nil
			}
		case <-deadline.C:
			return h.writeJSON(errorLine("timeout", "wait timed out; the run is still active"))
		case <-h.done:
			return errProgramDone
		case <-h.sig:
			return errSignal
		}
	}
}

// shutdown terminates any active run, waits for terminal restoration, and
// returns the app's exit code (or the requested code for a protocol failure).
func (h *headless) shutdown(requested int) int {
	select {
	case <-h.done:
		return h.finish()
	default:
	}
	h.program.Send(shutdownMsg{code: requested})
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-h.events:
			_ = h.writeEvent(ev)
		case <-h.done:
			code := h.finish()
			if requested != 0 {
				return requested
			}
			return code
		case <-timer.C:
			h.program.Kill()
			<-h.done
			return h.finish()
		}
	}
}

func (h *headless) finish() int {
	if h.app != nil {
		return h.app.ExitCode()
	}
	if h.finalModel != nil {
		if app, ok := h.finalModel.(*App); ok {
			return app.ExitCode()
		}
	}
	if h.runErr != nil {
		return 1
	}
	return 0
}
