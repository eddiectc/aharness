package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/model"
	"github.com/eddiectc/aharness/internal/session"
	"github.com/eddiectc/aharness/internal/tools"
	"github.com/eddiectc/aharness/internal/tui"
)

// hostRun is one run started by the TUI host.
type hostRun struct {
	ctx      context.Context
	cancel   context.CancelFunc
	control  *runController
	spawner  *agent.Spawner
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
	cfg              config
	store            *session.Store
	ws               *tools.Workspace
	sessionID        string
	source           model.CatalogSource
	catalog          map[string]model.CatalogModel
	catalogSource    string
	catalogFetchedAt time.Time
	models           []tui.ModelChoice
	// agents is the effective catalog loaded for this process, with the
	// startup overrides applied. It is what a new run's spawner uses.
	agents *agent.AgentCatalog
	// baseAgents is the discovered catalog before overrides. The definitions
	// browser applies the current overrides to a copy for display, so a saved
	// setting is visible immediately without changing what this process runs.
	baseAgents     *agent.AgentCatalog
	agentOverrides map[string]agent.AgentOverride

	mu             sync.Mutex
	info           tui.Info
	active         *hostRun
	lastSpawner    *agent.Spawner
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
	h.catalogSource = cat.Source
	h.catalogFetchedAt = cat.FetchedAt
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
			h.cfg.Pricing = m.Pricing(h.catalogSource, h.catalogFetchedAt)
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
	h.cfg.Pricing = m.Pricing(h.catalogSource, h.catalogFetchedAt)
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

// Agents returns the definitions the browser should show: the declared set
// with the current operator overrides applied for display. The process keeps
// the catalog it loaded for running children, so a saved override takes effect
// on the next process. It never re-reads the definition directory.
func (h *tuiHost) Agents() []tui.AgentDefView {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agentViews(h.overriddenAgentsLocked())
}

// overriddenAgentsLocked applies the current overrides to a copy of the
// declared catalog for the browser. The startup catalog already validated
// them and a save validates its candidate before writing, so a failure here is
// unreachable; falling back to the declared set keeps the browser usable.
func (h *tuiHost) overriddenAgentsLocked() *agent.AgentCatalog {
	if len(h.agentOverrides) == 0 {
		return h.baseAgents
	}
	catalog, err := agent.ApplyAgentOverrides(h.baseAgents, h.agentOverrides)
	if err != nil {
		return h.baseAgents
	}
	return catalog
}

// SetAgentOverride replaces one agent's operator override and persists it to
// the active config file. An empty override removes the entry. The definition
// file still owns the tool grant and the prompt body; this surface only routes
// the model, effort, turn cap, and enabled state. A nil base catalog means no
// definitions were loaded, so every name is unknown.
func (h *tuiHost) SetAgentOverride(name string, override agent.AgentOverride) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := h.cfg.ConfigPath
	if path == "" {
		return errors.New("the config file is disabled (-config \"\"); agent overrides cannot be saved")
	}
	if _, ok := h.baseAgents.LookupDefinition(name); !ok {
		return fmt.Errorf("agent_overrides[%q]: no agent definition with that name", name)
	}
	if withinWorkspace(h.ws, path) {
		return errors.New("the config file is inside the workspace; agent overrides are operator state and are never written there")
	}
	// Re-read so an external edit to another key, or to another agent's
	// override, is preserved by the rewrite.
	file, err := loadFileConfig(path, false)
	if err != nil {
		return err
	}
	candidate := file.agentOverrides()
	if override.Empty() {
		if _, ok := candidate[name]; !ok {
			return nil
		}
		delete(candidate, name)
	} else {
		if candidate == nil {
			candidate = make(map[string]agent.AgentOverride, 1)
		}
		candidate[name] = override
	}
	if _, err := agent.ApplyAgentOverrides(h.baseAgents, candidate); err != nil {
		return err
	}
	if len(candidate) == 0 {
		file.AgentOverrides = nil
	} else {
		file.AgentOverrides = make(map[string]fileAgentOverride, len(candidate))
		for key, value := range candidate {
			file.AgentOverrides[key] = fromAgent(value)
		}
	}
	if err := writeFileConfig(path, file); err != nil {
		return err
	}
	h.agentOverrides = candidate
	return nil
}

// ClearAgentOverrides removes every operator override for one agent.
func (h *tuiHost) ClearAgentOverrides(name string) error {
	return h.SetAgentOverride(name, agent.AgentOverride{})
}

// withinWorkspace reports whether path is the workspace root or inside it. A
// relative config path is resolved against the process working directory, which
// is normally the workspace. Symlinks are resolved too, so a path that reaches
// the workspace through another directory is still refused; the lexical and the
// resolved forms are both checked.
func withinWorkspace(ws *tools.Workspace, path string) bool {
	if ws == nil {
		return false
	}
	root, err := filepath.Abs(ws.Dir())
	if err != nil {
		return false
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if pathWithin(root, target) {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		return pathWithin(root, resolved)
	}
	// The file may not exist yet, so resolve the directory it will be created
	// in and check the joined path.
	if resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(target)); err == nil {
		return pathWithin(root, filepath.Join(resolvedDir, filepath.Base(target)))
	}
	return false
}

// pathWithin reports whether target is root or one of its descendants. Both
// paths must be absolute and clean.
func pathWithin(root, target string) bool {
	return target == root || strings.HasPrefix(target, root+string(filepath.Separator))
}

// SteerSubagent routes one composer line to a running delegated child. The
// child consumes it at its next turn boundary and records it with
// source=subagent-steer.
func (h *tuiHost) SteerSubagent(sessionID, text string) error {
	h.mu.Lock()
	run := h.active
	h.mu.Unlock()
	if run == nil || run.spawner == nil {
		return fmt.Errorf("no run is active")
	}
	return run.spawner.SteerChild(sessionID, text)
}

// SubagentHistory reads one delegated child's display transcript. The latest
// run's spawner is kept after the run ends, so a finished child stays readable
// until the operator leaves the view.
func (h *tuiHost) SubagentHistory(sessionID string) ([]agent.HistoryItem, error) {
	h.mu.Lock()
	spawner := h.lastSpawner
	h.mu.Unlock()
	if spawner == nil {
		return nil, fmt.Errorf("no delegated child has run in this session")
	}
	return spawner.ChildHistory(sessionID)
}

// agentViews converts the loaded catalog into browser rows: active
// definitions first, then the invalid files whose parse errors a user needs to
// see. The model, effort, and cap are the effective values after overrides;
// the declared values and a source label say where each came from.
func agentViews(catalog *agent.AgentCatalog) []tui.AgentDefView {
	if catalog == nil {
		return nil
	}
	views := make([]tui.AgentDefView, 0, len(catalog.Agents)+len(catalog.Invalid))
	for _, def := range catalog.Agents {
		modelSource, effortSource, maxTurnsSource := agentProvenance(def)
		status := "active"
		if def.Disabled {
			status = "disabled"
		}
		views = append(views, tui.AgentDefView{
			Name:             def.Name,
			Description:      def.Description,
			Model:            def.Model,
			Effort:           def.ThinkingEffort,
			ModelSource:      modelSource,
			EffortSource:     effortSource,
			MaxTurnsSource:   maxTurnsSource,
			DeclaredModel:    def.DeclaredModel,
			DeclaredEffort:   def.DeclaredThinkingEffort,
			DeclaredMaxTurns: def.DeclaredMaxTurns,
			Override:         def.Override,
			Enabled:          !def.Disabled,
			Source:           def.Source,
			Path:             def.Path,
			Tools:            append([]string(nil), def.Tools...),
			MaxTurns:         def.MaxTurns,
			Status:           status,
			PromptFirstLine:  firstLine(def.Prompt),
		})
	}
	for _, def := range catalog.Invalid {
		views = append(views, tui.AgentDefView{
			Name:   def.Name,
			Source: def.Source,
			Path:   def.Path,
			Status: "invalid",
			Err:    def.Err,
		})
	}
	return views
}

// agentProvenance labels where an effective routing value came from. The
// browser shows the label in the expanded row and marks an overridden value
// with an asterisk.
func agentProvenance(def agent.AgentDefinition) (model, effort, maxTurns string) {
	model = "inherit"
	if def.Model != "" {
		model = "definition"
	}
	maxTurns = "inherit"
	if def.MaxTurns > 0 {
		maxTurns = "definition"
	}
	effort = "inherit"
	if def.ThinkingEffort != "" {
		effort = "definition"
	}
	if def.Override != nil {
		if def.Override.Model != nil {
			model = "override"
		}
		if def.Override.ThinkingEffort != nil {
			effort = "override"
		} else if def.Override.Model != nil && def.DeclaredModel != def.Model {
			// A model override without an effort resets the child to the new
			// model's default, so the declared effort no longer applies.
			effort = "model default"
		}
		if def.Override.MaxTurns != nil {
			maxTurns = "override"
		}
	}
	return model, effort, maxTurns
}

// firstLine collapses the first non-empty prompt line for the expanded row.
func firstLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		collapsed := strings.Join(strings.Fields(line), " ")
		if collapsed != "" {
			return tui.CapText(tui.SanitizeLine(collapsed), 200)
		}
	}
	return ""
}

// resolveAgentModel resolves an agent's model and effort through the loaded
// catalog without mutating the host configuration.
func (h *tuiHost) resolveAgentModel(modelID, effort string) (agent.ModelResolution, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return agent.ModelResolution{}, errors.New("session host is closed")
	}
	m, ok := h.catalog[modelID]
	if !ok {
		if modelID == h.cfg.Model {
			// Offline resume/fork: the recorded values are authoritative until a
			// catalog loads, so an inherited model stays usable.
			return agent.ModelResolution{
				Dialect: string(h.cfg.Dialect),
				BaseURL: h.cfg.BaseURL,
				Limits:  h.cfg.Limits,
				Pricing: h.cfg.Pricing,
			}, nil
		}
		return agent.ModelResolution{}, fmt.Errorf("model %q is not in the loaded catalog", modelID)
	}
	if m.Unavailable != "" {
		return agent.ModelResolution{}, fmt.Errorf("model %q is not usable: %s", modelID, m.Unavailable)
	}
	if effort != "" {
		if err := validateModelThinkingEffort(effort, m); err != nil {
			return agent.ModelResolution{}, err
		}
	}
	cfg := h.cfg
	if err := applyCatalogModel(&cfg, m, func(string) {}); err != nil {
		return agent.ModelResolution{}, err
	}
	if err := validateEffective(&cfg); err != nil {
		return agent.ModelResolution{}, err
	}
	return agent.ModelResolution{
		Dialect: string(cfg.Dialect),
		BaseURL: cfg.BaseURL,
		Limits:  cfg.Limits,
		Pricing: m.Pricing(h.catalogSource, h.catalogFetchedAt),
	}, nil
}

// TodoItems returns the checklist recorded by the active session log so the
// TUI widget can show it before the next todo call and after a session switch.
func (h *tuiHost) TodoItems() []tools.TodoItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agent.RestoreTodo(h.store.Events())
}

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
		HTTPTimeout:      h.cfg.HTTPTimeout,
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
	definitions := h.agents
	var warnings []string
	warn := func(text string) { warnings = append(warnings, text) }
	modelName, dialect, err := h.prepareRunLocked(opts, warn)
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	control := newRunController(cancel)
	workDir, tmpDir := workPaths(h.cfg.StateDir, h.sessionID)
	if err := resetWorkDirs(workDir, tmpDir); err != nil {
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
		rcfg.Display = display
	}
	forward := display
	if forward != nil {
		forward = func(ev agent.DisplayEvent) error {
			select {
			case <-runCtx.Done():
				return nil
			default:
			}
			return display(ev)
		}
	}
	spawner := agent.NewSpawner(agent.SpawnerOptions{
		Config:      rcfg,
		Workspace:   h.ws,
		Definitions: definitions,
		Resolve:     h.resolveAgentModel,
		NewCompleter: func(dialect model.Dialect, baseURL, sessionID string) model.Completer {
			return newCompleterFor(dialect, h.cfg, sessionID)
		},
		NewToolOptions: func(_, childWorkDir, childTmpDir string) tools.Options {
			return toolOptions(h.cfg, childTmpDir, childWorkDir)
		},
		Display:       forward,
		ParentSession: h.sessionID,
		ParentSeq:     h.store.LastSeq,
		MaxSubagents:  h.cfg.MaxSubagents,
	})
	registryOpts := toolOptions(h.cfg, tmpDir, workDir)
	registryOpts.Spawning = spawner
	registry := tools.NewRegistry(h.ws, registryOpts)
	// A recorded grant is enforced twice: the projection omits ungranted
	// schemas, and dispatch rejects an ungranted name without running it. This
	// keeps a resumed batch session non-delegating and a resumed child on its
	// own grant.
	registry.SetGrant(sessionGrant(rcfg.Tools, rcfg.ToolsetVersion))
	registry.SeedTodo(agent.RestoreTodo(h.store.Events()))
	runner := agent.New(rcfg, newCompleterFor(dialect, h.cfg, h.sessionID), registry, h.store)
	run := &hostRun{
		ctx:      runCtx,
		cancel:   cancel,
		control:  control,
		spawner:  spawner,
		done:     make(chan agent.Result, 1),
		finished: make(chan struct{}),
	}
	h.active = run
	h.lastSpawner = spawner
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
	h.info.ContextTokens = result.ContextTokens
	h.info.CostMicroUSD = result.CostMicroUSD
	h.info.CostComplete = result.CostComplete
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

	// Definitions are loaded once per process from the state directory. The
	// recorded prompt and grant keep a resumed session's replay exact, so this
	// catalog only affects new children and the /agents browser. Operator
	// overrides are applied to a copy; the declared values stay available for
	// the browser's provenance view.
	baseDefinitions := agent.LoadAgents(cfg.StateDir)
	definitions, overrideErr := agent.ApplyAgentOverrides(baseDefinitions, cfg.AgentOverrides)
	if overrideErr != nil {
		prefix := "aharness: agent overrides: "
		if cfg.ConfigPath != "" {
			prefix = fmt.Sprintf("aharness: config file %s: ", cfg.ConfigPath)
		}
		_, _ = fmt.Fprintf(stderr, "%s%s\n", prefix, tui.SanitizeLine(overrideErr.Error()))
		return nil, exitConfig
	}
	for _, warning := range definitions.Warnings {
		warn(warning)
	}
	cfg.Agents = definitions.Infos()

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
	info := sessionInfo(cfg, store, state)
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
		agents:         definitions,
		baseAgents:     baseDefinitions,
		agentOverrides: cfg.AgentOverrides,
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
