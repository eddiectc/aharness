package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/colorprofile"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
	"aharness/internal/tui"
)

// hostRun is one run started by the TUI host.
type hostRun struct {
	ctx      context.Context
	cancel   context.CancelFunc
	control  *runController
	done     chan agent.Result
	finished chan struct{}
}

func (r *hostRun) Interrupt() {
	if r.control != nil {
		r.control.Interrupt()
	}
}

func (r *hostRun) Terminate() {
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *hostRun) Done() <-chan agent.Result { return r.done }

// tuiHost implements tui.Host over the session store, workspace, provider
// adapter, and runner. It owns the session lock for the process lifetime.
type tuiHost struct {
	cfg       config
	store     *session.Store
	ws        *tools.Workspace
	sessionID string
	models    []tui.ModelChoice

	mu         sync.Mutex
	info       tui.Info
	active     *hostRun
	closed     bool
	lastStatus agent.StopReason
	lastReason string
}

func (h *tuiHost) Info() tui.Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info
}

func (h *tuiHost) Models() []tui.ModelChoice { return h.models }

func (h *tuiHost) History() ([]agent.HistoryItem, error) {
	return agent.History(h.store.Events())
}

func (h *tuiHost) Workspace() *tools.Workspace { return h.ws }

func (h *tuiHost) Details() tui.Details {
	return tui.Details{
		TracePath:        h.store.Path(),
		StateDir:         h.cfg.StateDir,
		BaseURL:          h.cfg.BaseURL,
		Provider:         h.cfg.Provider,
		PromptVersion:    h.cfg.PromptVersion,
		ToolsetVersion:   h.cfg.ToolsetVersion,
		HarnessVersion:   h.cfg.HarnessVersion,
		MaxContextTokens: h.cfg.MaxContextTokens,
		MaxOutputTokens:  h.cfg.MaxOutputTokens,
		SessionTimeout:   h.cfg.SessionTimeout,
		ModelTimeout:     h.cfg.ModelTimeout,
	}
}

// Start launches one run and returns its handle. The app supplies the display
// callback and steering channel; the host owns the goroutine and cleanup.
func (h *tuiHost) Start(sub tui.Submission, opts tui.RunOptions, steer <-chan string, display func(agent.DisplayEvent) error) (tui.Run, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("session host is closed")
	}
	if h.active != nil {
		h.mu.Unlock()
		return nil, errors.New("a run is already active")
	}
	modelName := opts.Model
	if modelName == "" {
		modelName = h.info.Model
	}
	if modelName == "" {
		modelName = h.cfg.Model
	}
	dialect, err := dialectForModel(modelName)
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	control := newRunController(cancel)
	workDir, homeDir, tmpDir := workPaths(h.cfg.StateDir, h.sessionID)
	if err := resetWorkDirs(workDir, homeDir, tmpDir); err != nil {
		cancel()
		h.mu.Unlock()
		return nil, err
	}

	rcfg := runnerConfig(h.cfg)
	rcfg.Model = modelName
	rcfg.Dialect = string(dialect)
	rcfg.Workspace = h.info.Workspace
	rcfg.Stream = true
	rcfg.Interactive = false
	rcfg.Controller = control
	rcfg.Steering = steer
	rcfg.Logger = nil
	if display != nil {
		rcfg.Display = func(ev agent.DisplayEvent) error {
			select {
			case <-runCtx.Done():
				return nil
			default:
			}
			return display(ev)
		}
	}
	registry := tools.NewRegistry(h.ws, tools.Options{HomeDir: homeDir, TmpDir: tmpDir})
	runner := agent.New(rcfg, newCompleterFor(dialect, h.cfg, h.sessionID), registry, h.store)
	run := &hostRun{
		ctx:      runCtx,
		cancel:   cancel,
		control:  control,
		done:     make(chan agent.Result, 1),
		finished: make(chan struct{}),
	}
	h.active = run
	resumedFrom := lastSeq(h.store.Events())
	h.mu.Unlock()

	go h.run(run, runner, agent.RunOptions{
		Task:           sub.Text,
		TaskSet:        true,
		TaskFrom:       sub.Source,
		ResumedFromSeq: resumedFrom,
	}, workDir, modelName, dialect)
	return run, nil
}

func (h *tuiHost) run(run *hostRun, runner *agent.Runner, opts agent.RunOptions, workDir, modelName string, dialect model.Dialect) {
	defer close(run.finished)
	var result agent.Result
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				result = agent.Result{
					Status: agent.StopInternalError,
					Err:    fmt.Errorf("run goroutine panicked: %v", sanitizeText(fmt.Sprint(recovered))),
				}
			}
		}()
		var err error
		result, err = runner.Run(run.ctx, opts)
		if err != nil {
			result.Err = errors.Join(result.Err, err)
		}
		if result.Status == "" {
			result.Status = agent.StopInternalError
		}
	}()
	removeWorkDir(workDir, io.Discard)
	if err := h.store.Sync(); err != nil {
		result.Status = agent.StopInternalError
		result.Err = errors.Join(result.Err, err)
	}

	h.mu.Lock()
	if h.active == run {
		h.active = nil
	}
	h.info.Turns = result.CumulativeTurns
	h.info.Usage = result.CumulativeUsage
	h.info.Model = modelName
	h.info.Dialect = string(dialect)
	h.lastStatus = result.Status
	h.lastReason = ""
	if result.Err != nil {
		h.lastReason = sanitizeText(result.Err.Error())
	}
	h.mu.Unlock()

	run.done <- result
	close(run.done)
}

// Close terminates any active run, waits for its durable cleanup, then closes
// the store and workspace.
func (h *tuiHost) Close() error {
	h.mu.Lock()
	h.closed = true
	active := h.active
	h.mu.Unlock()
	if active != nil {
		active.Terminate()
		<-active.finished
	}
	storeErr := h.store.Close()
	wsErr := h.ws.Close()
	return errors.Join(storeErr, wsErr)
}

func (h *tuiHost) summary() (tui.Info, agent.StopReason, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info, h.lastStatus, h.lastReason
}

// openTUIHost opens the session shell used by the TUI and -ui-json. It
// validates resume/fork state before the terminal is touched.
func openTUIHost(cfg config, stderr io.Writer) (*tuiHost, int) {
	var (
		store *session.Store
		ws    *tools.Workspace
		state agent.State
		code  int
	)

	fail := func(code int) (*tuiHost, int) {
		if store != nil {
			_ = store.Close()
		}
		if ws != nil {
			_ = ws.Close()
		}
		return nil, code
	}

	switch {
	case cfg.ResumeID != "":
		if err := prepareStateDir(cfg.StateDir); err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return nil, exitConfig
		}
		var err error
		store, err = session.Open(cfg.StateDir, cfg.ResumeID)
		if err != nil {
			return nil, sessionFailure(err, stderr)
		}
		state, err = agent.Inspect(store.Events())
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: read session %s: %v\n", cfg.ResumeID, tui.SanitizeLine(err.Error()))
			return fail(exitSetup)
		}
		if err := applySessionDefaults(&cfg, state); err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitConfig)
		}
		ws, code = openSessionWorkspace(cfg, state, stderr)
		if ws == nil {
			return fail(code)
		}
		cfg.Workspace = ws.Dir()
		if err := validateSessionRules(&cfg, state); err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitConfig)
		}
	case cfg.ForkID != "":
		if err := prepareStateDir(cfg.StateDir); err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return nil, exitConfig
		}
		source, err := session.OpenShared(cfg.StateDir, cfg.ForkID)
		if err != nil {
			return nil, sessionFailure(err, stderr)
		}
		parentEvents := source.Events()
		parentID := source.ID()
		parentSeq := source.LastSeq()
		state, err = agent.Inspect(parentEvents)
		if err != nil {
			_ = source.Close()
			_, _ = fmt.Fprintf(stderr, "aharness: read parent session %s: %v\n", cfg.ForkID, tui.SanitizeLine(err.Error()))
			return nil, exitSetup
		}
		if err := applySessionDefaults(&cfg, state); err != nil {
			_ = source.Close()
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return nil, exitConfig
		}
		ws, code = openSessionWorkspace(cfg, state, stderr)
		if ws == nil {
			_ = source.Close()
			return nil, code
		}
		cfg.Workspace = ws.Dir()
		if err := validateSessionRules(&cfg, state); err != nil {
			_ = source.Close()
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitConfig)
		}
		sessionID := session.NewID()
		store, err = session.Create(cfg.StateDir, sessionID)
		if err != nil {
			_ = source.Close()
			_, _ = fmt.Fprintf(stderr, "aharness: create forked session log: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitSetup)
		}
		rcfg := runnerConfig(cfg)
		parent := &agent.ParentRef{Session: parentID, Seq: parentSeq}
		if err := agent.WriteSessionStart(store, rcfg, parent); err != nil {
			_ = source.Close()
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitSetup)
		}
		if err := store.CopyHistory(parentEvents); err != nil {
			_ = source.Close()
			_, _ = fmt.Fprintf(stderr, "aharness: copy parent session: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitSetup)
		}
		if err := store.Sync(); err != nil {
			_ = source.Close()
			_, _ = fmt.Fprintf(stderr, "aharness: sync forked session: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitSetup)
		}
		if err := source.Close(); err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: close parent session log: %v\n", tui.SanitizeLine(err.Error()))
		}
	default:
		ws, code = openWorkspace(cfg, stderr)
		if ws == nil {
			return nil, code
		}
		cfg.Workspace = ws.Dir()
		if err := prepareStateDir(cfg.StateDir); err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitConfig)
		}
		sessionID := session.NewID()
		var err error
		store, err = session.Create(cfg.StateDir, sessionID)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: create session log: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitSetup)
		}
		if err := agent.WriteSessionStart(store, runnerConfig(cfg), nil); err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitSetup)
		}
	}

	history, err := agent.History(store.Events())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: read session %s: %v\n", store.ID(), tui.SanitizeLine(err.Error()))
		return fail(exitSetup)
	}
	_ = history
	info := tui.Info{
		SessionID:      store.ID(),
		TracePath:      store.Path(),
		Model:          cfg.Model,
		Dialect:        string(cfg.Dialect),
		Workspace:      cfg.Workspace,
		MaxTurns:       cfg.MaxTurns,
		MaxTotalTokens: cfg.MaxTotalTokens,
		Turns:          state.Turns,
		Usage:          state.Usage,
	}
	models := make([]tui.ModelChoice, 0, len(acceptedModelIDs))
	for _, id := range acceptedModelIDs {
		dialect, err := dialectForModel(id)
		if err != nil {
			continue
		}
		models = append(models, tui.ModelChoice{ID: id, Dialect: dialect, Default: id == defaultModelID})
	}
	host := &tuiHost{
		cfg:       cfg,
		store:     store,
		ws:        ws,
		sessionID: store.ID(),
		models:    models,
		info:      info,
	}
	return host, 0
}

// executeTUI runs the interactive or headless TUI and returns the exit code.
func executeTUI(cfg config, stdin io.Reader, stdout, stderr io.Writer) int {
	host, code := openTUIHost(cfg, stderr)
	if host == nil {
		return code
	}
	seed := (*tui.Submission)(nil)
	if cfg.TaskSet {
		seed = &tui.Submission{Text: cfg.Task, Source: cfg.TaskSource}
	}
	noColor := os.Getenv("NO_COLOR") != ""
	headless := cfg.Mode == ModeUIJSON
	imageProtocol, err := tui.ImageProtocolFromEnv(os.Getenv, headless)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
		_ = host.Close()
		return exitConfig
	}
	colorProfile := "truecolor"
	if !headless {
		colorProfile = detectedProfile(stdout)
	}
	terminal := tui.TerminalInfo{
		Name:    os.Getenv("TERM"),
		Version: strings.TrimSpace(os.Getenv("TERM_PROGRAM") + " " + os.Getenv("TERM_PROGRAM_VERSION")),
	}

	var exit int
	if headless {
		exit, err = tui.RunHeadless(tui.HeadlessOptions{
			Host:          host,
			Models:        host.Models(),
			Model:         host.Info().Model,
			Dialect:       cfg.Dialect,
			Seed:          seed,
			Width:         cfg.UISizeWidth,
			Height:        cfg.UISizeHeight,
			Stdin:         stdin,
			Stdout:        stdout,
			ColorProfile:  colorProfile,
			NoColor:       noColor,
			ImageProtocol: imageProtocol,
			Terminal:      terminal,
		})
	} else {
		exit, err = tui.RunTerminal(tui.TerminalOptions{
			Host:          host,
			Models:        host.Models(),
			Model:         host.Info().Model,
			Dialect:       cfg.Dialect,
			Seed:          seed,
			Stdin:         stdin,
			Stdout:        stdout,
			ColorProfile:  colorProfile,
			NoColor:       noColor,
			ImageProtocol: imageProtocol,
			Terminal:      terminal,
		})
	}
	closeErr := host.Close()
	info, lastStatus, lastReason := host.summary()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
		if exit == 0 {
			exit = exitSetup
		}
	}
	if closeErr != nil {
		_, _ = fmt.Fprintf(stderr, "aharness: close session: %v\n", tui.SanitizeLine(closeErr.Error()))
		exit = exitSetup
	}
	_, _ = fmt.Fprintf(stderr, "aharness: session %s trace %s\n", tui.SanitizeLine(info.SessionID), tui.SanitizeLine(info.TracePath))
	if lastStatus != "" {
		line := "aharness: last run " + string(lastStatus)
		if lastReason != "" {
			line += ": " + lastReason
		}
		_, _ = fmt.Fprintln(stderr, tui.SanitizeLine(line))
	}
	return exit
}

func detectedProfile(stdout io.Writer) string {
	switch colorprofile.Detect(stdout, os.Environ()) {
	case colorprofile.TrueColor:
		return "truecolor"
	case colorprofile.ANSI256:
		return "256"
	case colorprofile.ANSI:
		return "16"
	case colorprofile.ASCII:
		return "8"
	default:
		return "none"
	}
}
