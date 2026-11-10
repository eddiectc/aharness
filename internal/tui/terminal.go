package tui

import (
	"errors"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"aharness/internal/model"
)

// TerminalOptions configures the real-terminal TUI driver.
type TerminalOptions struct {
	Host          Host
	Models        []ModelChoice
	Model         string
	Dialect       model.Dialect
	Seed          *Submission
	Stdin         io.Reader
	Stdout        io.Writer
	ColorProfile  string
	NoColor       bool
	ImageProtocol string
	Terminal      TerminalInfo
}

// RunTerminal runs the TUI on a real terminal. Bubble Tea owns raw mode, the
// alternate screen, paste, resize, and terminal restoration.
func RunTerminal(opts TerminalOptions) (int, error) {
	app := New(Options{
		Host:          opts.Host,
		Models:        opts.Models,
		Model:         opts.Model,
		Dialect:       opts.Dialect,
		Seed:          opts.Seed,
		Headless:      false,
		ColorProfile:  opts.ColorProfile,
		NoColor:       opts.NoColor,
		ImageProtocol: opts.ImageProtocol,
		Terminal:      opts.Terminal,
	})
	output := opts.Stdout
	if opts.NoColor {
		output = &noColorWriter{w: output}
	}
	programOpts := []tea.ProgramOption{
		tea.WithInput(opts.Stdin),
		tea.WithOutput(output),
		tea.WithFilter(filterTerminalMsg),
		tea.WithoutSignalHandler(),
	}
	if profile, ok := profileValue(opts.ColorProfile); ok {
		programOpts = append(programOpts, tea.WithColorProfile(profile))
	}
	program := tea.NewProgram(app, programOpts...)

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	stopSignals := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopSignals:
				return
			case sig := <-sigs:
				switch sig {
				case os.Interrupt:
					program.Send(terminalInterruptMsg{})
				default:
					program.Send(shutdownMsg{code: 130})
				}
			}
		}
	}()

	final, err := program.Run()
	signal.Stop(sigs)
	close(stopSignals)

	if model, ok := final.(*App); ok && model != nil {
		if err != nil && errors.Is(err, tea.ErrProgramPanic) {
			return 1, err
		}
		return model.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// filterTerminalMsg replaces an implausible zero window size (some PTY and
// multiplexer setups report 0x0) with a usable fallback before the renderer
// resizes to zero cells.
func filterTerminalMsg(_ tea.Model, msg tea.Msg) tea.Msg {
	size, ok := msg.(tea.WindowSizeMsg)
	if !ok || (size.Width > 0 && size.Height > 0) {
		return msg
	}
	width, height := 80, 24
	if raw := os.Getenv("COLUMNS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			width = n
		}
	}
	if raw := os.Getenv("LINES"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			height = n
		}
	}
	return tea.WindowSizeMsg{Width: width, Height: height}
}

func profileValue(name string) (colorprofile.Profile, bool) {
	switch name {
	case "truecolor":
		return colorprofile.TrueColor, true
	case "256":
		return colorprofile.ANSI256, true
	case "16":
		return colorprofile.ANSI, true
	case "8", "none":
		return colorprofile.ASCII, true
	default:
		return 0, false
	}
}
