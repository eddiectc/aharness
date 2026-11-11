package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
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
	source    model.CatalogSource
	catalog   map[string]model.CatalogModel
	models    []tui.ModelChoice

	mu             sync.Mutex
	info           tui.Info
	active         *hostRun
	closed         bool
	lastStatus     agent.StopReason
	lastReason     string
	pendingWarning string
}

func (h *tuiHost) Info() tui.Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	info := h.info
	info.ThinkingEffort = h.cfg.ThinkingEffort
	return info
}

func (h *tuiHost) Models() []tui.ModelChoice {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]tui.ModelChoice(nil), h.models...)
}

func (h *tuiHost) History() ([]agent.HistoryItem, error) {
	return agent.History(h.store.Events())
}

// modelChoice converts a normalized catalog model into picker metadata.
func modelChoice(m model.CatalogModel) tui.ModelChoice {
	name := m.Name
	if name == "" {
		name = m.ID
	}
	return tui.ModelChoice{
		ID:               m.ID,
		Name:             name,
		Dialect:          m.Dialect,
		ContextWindow:    m.ContextWindow,
		InputLimit:       m.InputLimit,
		OutputLimit:      m.OutputLimit,
		Status:           m.Status,
		Translated:       m.Translated,
		Switchable:       true,
		ReasoningEfforts: append([]string(nil), m.ReasoningEfforts...),
	}
}

// recordedChoiceLocked describes the session's current model from recorded
// values. It is the offline picker fallback and is never switchable then.
func (h *tuiHost) recordedChoiceLocked(switchable bool) tui.ModelChoice {
	name := h.cfg.Model
	if name == "" {
		name = h.info.Model
	}
	return tui.ModelChoice{
		ID:            name,
		Name:          name,
		Dialect:       h.cfg.Dialect,
		ContextWindow: h.cfg.MaxContextTokens,
		InputLimit:    h.cfg.InputLimit,
		Switchable:    switchable,
	}
}

// setCatalogLocked replaces the picker entries. A current recorded model that
// is absent from the catalog stays visible as the current entry.
func (h *tuiHost) setCatalogLocked(cat model.Catalog) {
	choices := make([]tui.ModelChoice, 0, len(cat.Models)+1)
	for _, m := range cat.Usable() {
		choices = append(choices, modelChoice(m))
	}
	if _, ok := cat.Models[h.cfg.Model]; !ok {
		choices = append(choices, h.recordedChoiceLocked(true))
	}
	sort.Slice(choices, func(i, j int) bool { return choices[i].ID < choices[j].ID })
	h.catalog = cat.Models
	h.models = choices
}

// RefreshModels performs the explicit picker refresh (Ctrl-R). A failure
// leaves the loaded entries untouched.
func (h *tuiHost) RefreshModels(ctx context.Context) ([]tui.ModelChoice, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("session host is closed")
	}
	cfg := h.cfg
	source := h.source
	h.mu.Unlock()

	if source == nil {
		var err error
		source, err = newCatalogSource(cfg)
		if err != nil {
			return nil, err
		}
	}
	cat, err := source.Refresh(ctx, cfg.Provider)
	if err != nil {
		if !errors.Is(err, model.ErrCatalogCache) || len(cat.Models) == 0 {
			return nil, err
		}
		// The fetch succeeded but the cache write did not. Use the refreshed
		// in-memory catalog and report the persistence failure as a notice.
		h.mu.Lock()
		defer h.mu.Unlock()
		h.source = source
		h.setCatalogLocked(cat)
		return append([]tui.ModelChoice(nil), h.models...), err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.source = source
	h.setCatalogLocked(cat)
	return append([]tui.ModelChoice(nil), h.models...), nil
}

// SelectModel resolves a picker selection and applies its dialect, base URL,
// context window, and input ceiling to the next run.
func (h *tuiHost) SelectModel(id string) (tui.ModelChoice, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return tui.ModelChoice{}, errors.New("session host is closed")
	}
	h.pendingWarning = ""
	if id == h.cfg.Model {
		// Keep the recorded fallback's exact limits, but prefer loaded catalog
		// metadata so a re-selection still carries the advertised efforts.
		if m, ok := h.catalog[id]; ok && m.Unavailable == "" {
			return modelChoice(m), nil
		}
		return h.recordedChoiceLocked(true), nil
	}
	m, ok := h.catalog[id]
	if !ok {
		return tui.ModelChoice{}, fmt.Errorf("model %q is not in the loaded catalog", id)
	}
	if m.Unavailable != "" {
		return tui.ModelChoice{}, fmt.Errorf("model %q is not usable: %s", id, m.Unavailable)
	}
	if err := applyCatalogModel(&h.cfg, m, func(text string) { h.pendingWarning = text }); err != nil {
		return tui.ModelChoice{}, err
	}
	if err := validateEffective(&h.cfg); err != nil {
		return tui.ModelChoice{}, err
	}
	return modelChoice(m), nil
}

// TakeModelWarning returns and clears the warning from the last successful
// picker selection. It implements tui.ModelWarningProvider.
func (h *tuiHost) TakeModelWarning() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	warning := h.pendingWarning
	h.pendingWarning = ""
	return warning
}

func (h *tuiHost) Workspace() *tools.Workspace { return h.ws }

func (h *tuiHost) Details() tui.Details {
	h.mu.Lock()
	defer h.mu.Unlock()
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
		MaxOutputUnset:   h.cfg.MaxOutputTokens == 0,
		SessionTimeout:   h.cfg.SessionTimeout,
		ModelTimeout:     h.cfg.ModelTimeout,
		ThinkingEffort:   h.cfg.ThinkingEffort,
	}
}

// resolveRunThinkingEffort applies the next run's effort selection and the
// model-switch rule: an explicit option (including the empty provider default)
// replaces the inherited value, and a value the effective catalog model does
// not advertise is cleared with a warning. A model absent from the catalog
// cannot be validated offline, so its recorded value is kept.
func resolveRunThinkingEffort(cfg *config, catalog map[string]model.CatalogModel, modelName string, opts tui.RunOptions, warn func(string)) {
	if opts.ThinkingEffortSet {
		cfg.ThinkingEffort = opts.ThinkingEffort
	}
	if cfg.ThinkingEffort == "" {
		return
	}
	if m, ok := catalog[modelName]; ok {
		clearInheritedThinkingEffort(cfg, m, warn)
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
	var warnings []string
	warn := func(text string) { warnings = append(warnings, text) }
	modelName := opts.Model
	if modelName == "" {
		modelName = h.info.Model
	}
	if modelName == "" {
		modelName = h.cfg.Model
	}
	if modelName != h.cfg.Model {
		m, ok := h.catalog[modelName]
		if !ok {
			h.mu.Unlock()
			return nil, fmt.Errorf("model %q is not in the loaded catalog", modelName)
		}
		if err := applyCatalogModel(&h.cfg, m, warn); err != nil {
			h.mu.Unlock()
			return nil, err
		}
		if m.Deprecated {
			warn("model " + m.ID + " is deprecated")
		}
		if err := validateEffective(&h.cfg); err != nil {
			h.mu.Unlock()
			return nil, err
		}
	}
	// An explicit picker/option value replaces the inherited effort; either
	// way, a value the effective catalog model does not advertise is cleared
	// with a notice so the provider default is used instead of sending an
	// unsupported field.
	resolveRunThinkingEffort(&h.cfg, h.catalog, modelName, opts, warn)
	dialect := h.cfg.Dialect

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

	if display != nil {
		for _, text := range warnings {
			_ = display(agent.Notice{Kind: agent.NoticeWarning, Text: text})
		}
	}
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
		store         *session.Store
		ws            *tools.Workspace
		state         agent.State
		catalogSource model.CatalogSource
		code          int
		startupWarn   []string
	)
	warn := func(text string) { startupWarn = append(startupWarn, text) }

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
		source, err := sessionCatalogSource(cfg)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return fail(exitConfig)
		}
		if err := applySessionDefaults(context.Background(), &cfg, state, source, warn); err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return fail(catalogExitCode(err))
		}
		catalogSource = source
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
		catSource, err := sessionCatalogSource(cfg)
		if err != nil {
			_ = source.Close()
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return nil, exitConfig
		}
		if err := applySessionDefaults(context.Background(), &cfg, state, catSource, warn); err != nil {
			_ = source.Close()
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return nil, catalogExitCode(err)
		}
		catalogSource = catSource
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
		var err error
		catalogSource, err = newCatalogSource(cfg)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return nil, exitConfig
		}
		if err := resolveNewModel(context.Background(), &cfg, catalogSource, stderr); err != nil {
			_, _ = fmt.Fprintf(stderr, "aharness: %v\n", tui.SanitizeLine(err.Error()))
			return nil, catalogExitCode(err)
		}
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
		ThinkingEffort: cfg.ThinkingEffort,
	}
	ctx := context.Background()
	if catalogSource == nil {
		if src, err := newCatalogSource(cfg); err == nil {
			catalogSource = src
		}
	}
	var loaded *model.Catalog
	if catalogSource != nil {
		if cat, err := catalogSource.Load(ctx, cfg.Provider); err == nil {
			loaded = &cat
		}
	}
	host := &tuiHost{
		cfg:            cfg,
		store:          store,
		ws:             ws,
		sessionID:      store.ID(),
		source:         catalogSource,
		info:           info,
		pendingWarning: strings.Join(startupWarn, "; "),
	}
	if loaded != nil {
		warnCatalog(stderr, *loaded)
		host.setCatalogLocked(*loaded)
	} else {
		host.models = []tui.ModelChoice{host.recordedChoiceLocked(false)}
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
