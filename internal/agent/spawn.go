package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

// ModelResolution is the catalog-derived adapter and budget set a child run
// needs for one model.
type ModelResolution struct {
	Dialect string
	BaseURL string
	Limits  Limits
	Pricing model.Pricing
}

// ModelResolver resolves one agent model and optional effort through the model
// catalog. It is supplied by cmd, which owns the catalog. An unknown or
// unusable model, or an effort the model does not support, is an error; the
// spawner reports it per task instead of ending the parent run.
type ModelResolver func(modelID, effort string) (ModelResolution, error)

// CompleterFunc builds the provider adapter for a child run. cmd supplies it so
// the spawner never needs the provider token.
type CompleterFunc func(dialect model.Dialect, baseURL, sessionID string) model.Completer

// ToolOptionsFunc builds the child registry options from the parent's
// effective tool configuration. The spawner clears the spawn interface on the
// result, so a child can never delegate.
type ToolOptionsFunc func(sessionID, workDir, tmpDir string) tools.Options

// SpawnerOptions configures one per-run spawner. The spawner is created with
// the host's effective configuration and reused for every delegate call in the
// run, so its child counters are per-run.
type SpawnerOptions struct {
	Config         Config
	Workspace      *tools.Workspace
	Definitions    *AgentCatalog
	Resolve        ModelResolver
	NewCompleter   CompleterFunc
	NewToolOptions ToolOptionsFunc
	Logger         *slog.Logger
	// Display is the parent's display callback. Child progress is forwarded
	// through it, serialized by the spawner.
	Display func(DisplayEvent) error
	// ParentSession is the delegating session's ID. A continuation must record
	// it as its parent_session.
	ParentSession string
	// ParentSeq returns the parent's current last sequence number at spawn
	// time; used for the child's parent_seq.
	ParentSeq func() uint64
	// MaxSubagents bounds fresh children per run; 0 means unlimited.
	MaxSubagents int
	// Now is the clock used to throttle progress events; nil uses time.Now.
	Now func() time.Time
}

// Spawner runs delegated tasks as child sessions. Tasks execute in order in
// this milestone; the parent run is suspended for the whole call.
type Spawner struct {
	opts SpawnerOptions

	displayMu sync.Mutex
	mu        sync.Mutex
	spawned   int
	childUse  model.Usage
}

// NewSpawner builds the per-run spawner. A nil definition set yields a spawner
// that reports every task as failed with the available names.
func NewSpawner(opts SpawnerOptions) *Spawner {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Spawner{opts: opts}
}

// Agents returns the discovered catalog the delegate tool validates against.
func (s *Spawner) Agents() []tools.AgentInfo {
	return s.opts.Definitions.SpawnInfos()
}

// Spawn executes the call's tasks in array order and returns one report per
// task. A canceled parent context stops the loop: the remaining tasks report
// that they were not started.
func (s *Spawner) Spawn(ctx context.Context, call tools.Call, specs []tools.SubagentSpec) []tools.SubagentReport {
	reports := make([]tools.SubagentReport, len(specs))
	for i, spec := range specs {
		if ctx.Err() != nil {
			reports[i] = failedReport(spec, tools.CodeCanceled, tools.SubagentInterrupted,
				"not started: the run was canceled")
			continue
		}
		reports[i] = s.spawnOne(ctx, call, i, spec)
	}
	return reports
}

func (s *Spawner) spawnOne(ctx context.Context, call tools.Call, index int, spec tools.SubagentSpec) tools.SubagentReport {
	if spec.Session != "" {
		return s.continueChild(ctx, call, index, spec)
	}
	def, ok := s.opts.Definitions.Lookup(spec.Agent)
	if !ok {
		return failedReport(spec, tools.CodeNotFound, tools.SubagentFailed,
			fmt.Sprintf("unknown agent %q", spec.Agent))
	}
	modelID, effort, resolution, err := s.resolveAgent(def)
	if err != nil {
		return failedReport(spec, tools.CodeInvalidArgs, tools.SubagentFailed,
			fmt.Sprintf("agent %q: %v", def.Name, err))
	}
	if !s.reserveChild() {
		return failedReport(spec, tools.CodeInvalidArgs, tools.SubagentFailed,
			fmt.Sprintf("subagent limit reached (-max-subagents %d); no further tasks were started", s.opts.MaxSubagents))
	}
	if remaining, limited := s.remainingTokens(call); limited && remaining <= 0 {
		return tools.SubagentReport{
			Agent: def.Name, Intent: subagentIntent(spec.Task), Model: modelID, Effort: effort,
			Status: tools.SubagentTokenLimit, Code: tools.CodeSubagentLimit,
			Text: "the parent's total token budget is exhausted",
		}
	}

	childCfg := s.childConfig(def, modelID, effort, resolution, call)
	sessionID := session.NewID()
	childCfg.Display = nil
	adapter := s.newChildDisplay(call, index, def.Name, sessionID, modelID, effort, childCfg.Tools, resolution.Pricing).withIntent(spec.Task)
	if err := adapter.started(); err != nil {
		return failedReport(spec, tools.CodeInternal, tools.SubagentFailed, err.Error())
	}
	report := s.runChildSession(ctx, childCfg, adapter, sessionID, childRunOptions(spec.Task))
	adapter.finished(report)
	return report
}

// continueChild resumes a child session recorded by an earlier delegate call.
// The recorded prompt and grant are reused, so editing the agent file never
// changes an existing child session.
func (s *Spawner) continueChild(ctx context.Context, call tools.Call, index int, spec tools.SubagentSpec) tools.SubagentReport {
	path := session.SessionPath(s.opts.Config.StateDir, spec.Session)
	head, err := session.ReadHead(path, 1)
	if err != nil {
		return failedReport(spec, tools.CodeInvalidArgs, tools.SubagentFailed,
			fmt.Sprintf("session %s is not readable: %v", spec.Session, err))
	}
	var start sessionStartData
	if len(head) == 0 || head[0].Type != session.TypeSessionStart {
		return failedReport(spec, tools.CodeInvalidArgs, tools.SubagentFailed,
			fmt.Sprintf("session %s has no session_start", spec.Session))
	}
	if err := decodeEventData(head[0], &start); err != nil {
		return failedReport(spec, tools.CodeInvalidArgs, tools.SubagentFailed,
			fmt.Sprintf("session %s has an unreadable session_start", spec.Session))
	}
	if start.ParentSession != s.opts.ParentSession || start.ParentCallID == "" {
		return failedReport(spec, tools.CodeInvalidArgs, tools.SubagentFailed,
			fmt.Sprintf("session %s is not a child of this session", spec.Session))
	}
	if spec.Agent != "" && start.Agent != "" && spec.Agent != start.Agent {
		return failedReport(spec, tools.CodeInvalidArgs, tools.SubagentFailed,
			fmt.Sprintf("session %s belongs to agent %q, not %q", spec.Session, start.Agent, spec.Agent))
	}
	store, err := session.Open(s.opts.Config.StateDir, spec.Session)
	if err != nil {
		if errors.Is(err, session.ErrLocked) {
			return failedReport(spec, tools.CodeUnavailable, tools.SubagentFailed,
				fmt.Sprintf("session %s is locked by another process", spec.Session))
		}
		return failedReport(spec, tools.CodeInvalidArgs, tools.SubagentFailed,
			fmt.Sprintf("session %s could not be opened: %v", spec.Session, err))
	}
	state, err := Inspect(store.Events())
	if err != nil {
		_ = store.Close()
		return failedReport(spec, tools.CodeInternal, tools.SubagentFailed,
			fmt.Sprintf("session %s could not be read: %v", spec.Session, err))
	}
	if remaining, limited := s.remainingTokens(call); limited && remaining <= 0 {
		_ = store.Close()
		return tools.SubagentReport{
			Agent: state.Agent, Intent: subagentIntent(spec.Task), Model: state.Model,
			Effort: state.ThinkingEffort, Session: spec.Session,
			Status: tools.SubagentTokenLimit, Code: tools.CodeSubagentLimit,
			Text: "the parent's total token budget is exhausted",
		}
	}
	childCfg := s.continuedConfig(state, call)
	childCfg.Display = nil
	agentName := state.Agent
	if agentName == "" {
		agentName = spec.Agent
	}
	adapter := s.newChildDisplay(call, index, agentName, spec.Session, state.Model, state.ThinkingEffort, childCfg.Tools, state.Pricing).withIntent(spec.Task)
	if err := adapter.started(); err != nil {
		_ = store.Close()
		return failedReport(spec, tools.CodeInternal, tools.SubagentFailed, err.Error())
	}
	report := s.runChildOnStore(ctx, childCfg, store, adapter, spec.Session, RunOptions{
		Task:           spec.Task,
		TaskSet:        true,
		TaskFrom:       taskFromDelegate,
		ResumedFromSeq: state.LastEventSeq,
	})
	adapter.finished(report)
	return report
}

// runChildSession creates the child log, runs the child, and removes its work
// directory.
func (s *Spawner) runChildSession(ctx context.Context, cfg Config, adapter *childDisplay, sessionID string, opts RunOptions) tools.SubagentReport {
	store, err := session.Create(cfg.StateDir, sessionID)
	if err != nil {
		return failedReport(tools.SubagentSpec{Agent: cfg.Agent, Task: opts.Task},
			tools.CodeInternal, tools.SubagentFailed, fmt.Sprintf("create child session: %v", err))
	}
	parent := &ParentRef{Session: s.opts.ParentSession, Seq: s.parentSeq(), CallID: adapter.call.ID}
	if err := WriteSessionStart(store, cfg, parent); err != nil {
		_ = store.Close()
		return failedReport(tools.SubagentSpec{Agent: cfg.Agent, Task: opts.Task},
			tools.CodeInternal, tools.SubagentFailed, fmt.Sprintf("record child session start: %v", err))
	}
	return s.runChildOnStore(ctx, cfg, store, adapter, sessionID, opts)
}

// runChildOnStore runs one child over an open store, closes the store, and
// maps the run result to a report. The store is synced and closed before the
// report is returned, so a failed child stays resumable and inspectable.
func (s *Spawner) runChildOnStore(ctx context.Context, cfg Config, store *session.Store, adapter *childDisplay, sessionID string, opts RunOptions) tools.SubagentReport {
	workDir, tmpDir := s.workPaths(sessionID)
	if err := prepareChildWorkDir(workDir, tmpDir); err != nil {
		_ = store.Close()
		return failedReport(tools.SubagentSpec{Agent: cfg.Agent, Task: opts.Task},
			tools.CodeInternal, tools.SubagentFailed, fmt.Sprintf("prepare child work directory: %v", err))
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	registry := tools.NewRegistry(s.opts.Workspace, s.childToolOptions(sessionID, workDir, tmpDir))
	registry.SetGrant(cfg.Tools)
	cfg.Display = adapter.display
	runner := New(cfg, s.newCompleter(cfg, sessionID), registry, store)
	result, err := runner.Run(ctx, opts)
	closeErr := store.Close()

	report := tools.SubagentReport{
		Agent:         cfg.Agent,
		Intent:        subagentIntent(opts.Task),
		Model:         cfg.Model,
		Effort:        cfg.ThinkingEffort,
		Session:       sessionID,
		Status:        string(result.Status),
		Code:          reportCode(result.Status),
		Turns:         result.RunTurns,
		ContextTokens: result.ContextTokens,
		Usage:         result.RunUsage,
		CostMicroUSD:  result.RunCostMicroUSD,
		CostComplete:  result.RunCostComplete,
		Text:          result.FinalText,
	}
	if report.Status == "" {
		report.Status = tools.SubagentInternalError
		report.Code = tools.CodeInternal
	}
	if err != nil && report.Status == string(StopCompleted) {
		report.Status = string(StopInternalError)
		report.Code = tools.CodeInternal
	}
	if closeErr != nil {
		report.Status = string(StopInternalError)
		report.Code = tools.CodeInternal
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Info("subagent finished",
			"agent", report.Agent,
			"session", report.Session,
			"status", report.Status,
			"turns", report.Turns,
			"input_tokens", report.Usage.InputTokens,
			"output_tokens", report.Usage.OutputTokens,
		)
	}
	return report
}

// childConfig builds a fresh child's immutable run configuration.
func (s *Spawner) childConfig(def AgentDefinition, modelID, effort string, resolution ModelResolution, call tools.Call) Config {
	parent := s.opts.Config
	maxTurns := def.MaxTurns
	if maxTurns <= 0 {
		maxTurns = parent.MaxTurns
	}
	return Config{
		Provider:       parent.Provider,
		Dialect:        resolution.Dialect,
		Model:          modelID,
		BaseURL:        resolution.BaseURL,
		Workspace:      parent.Workspace,
		StateDir:       parent.StateDir,
		PromptVersion:  parent.PromptVersion,
		ToolsetVersion: parent.ToolsetVersion,
		HarnessVersion: parent.HarnessVersion,
		SystemPrompt:   childSystemPrompt(def, parent.Workspace),
		Agent:          def.Name,
		Tools:          append([]string(nil), def.Tools...),
		MaxTurns:       maxTurns,
		MaxTotalTokens: s.remainingBudget(call),
		Limits:         resolution.Limits,
		SessionTimeout: 0,
		ModelTimeout:   parent.ModelTimeout,
		RequireUsage:   parent.RequireUsage,
		Pricing:        resolution.Pricing,
		ThinkingEffort: effort,
		Interactive:    false,
		Stream:         false,
	}
}

// continuedConfig builds a resumed child's configuration from its own recorded
// state. The recorded model, effort, prompt, and grant win; only the budget
// comes from the parent.
func (s *Spawner) continuedConfig(state State, call tools.Call) Config {
	parent := s.opts.Config
	grant := append([]string(nil), state.Tools...)
	if grant == nil {
		grant = tools.NamesWithout("delegate")
	}
	maxTurns := parent.MaxTurns
	if def, ok := s.opts.Definitions.Lookup(state.Agent); ok && def.MaxTurns > 0 {
		maxTurns = def.MaxTurns
	}
	return Config{
		Provider:       parent.Provider,
		Dialect:        state.Dialect,
		Model:          state.Model,
		BaseURL:        state.BaseURL,
		Workspace:      state.Workspace,
		StateDir:       parent.StateDir,
		PromptVersion:  state.PromptVersion,
		ToolsetVersion: state.ToolsetVersion,
		HarnessVersion: state.HarnessVersion,
		SystemPrompt:   state.SystemPrompt,
		Agent:          state.Agent,
		Tools:          grant,
		MaxTurns:       maxTurns,
		MaxTotalTokens: s.remainingBudget(call),
		Limits:         state.Limits,
		SessionTimeout: 0,
		ModelTimeout:   parent.ModelTimeout,
		RequireUsage:   parent.RequireUsage,
		Pricing:        state.Pricing,
		ThinkingEffort: state.ThinkingEffort,
		Interactive:    false,
		Stream:         false,
	}
}

// resolveAgent applies the resolution order for model and effort. When the
// resolved model differs from the parent's and the definition sets no effort,
// the child uses the model default rather than inheriting a level that model
// may not support.
func (s *Spawner) resolveAgent(def AgentDefinition) (string, string, ModelResolution, error) {
	parent := s.opts.Config
	resolution := ModelResolution{
		Dialect: parent.Dialect,
		BaseURL: parent.BaseURL,
		Limits:  parent.Limits,
		Pricing: parent.Pricing,
	}
	modelID := def.Model
	effort := def.ThinkingEffort
	if modelID == "" || modelID == parent.Model {
		modelID = parent.Model
		if effort == "" {
			effort = parent.ThinkingEffort
		}
	}
	// A model switch without an explicit effort keeps the empty value, which
	// resets the child to the model default rather than inheriting a level the
	// model may not support.
	if modelID == "" {
		return "", "", ModelResolution{}, errors.New("no model is configured for this session")
	}
	if s.opts.Resolve != nil {
		resolved, err := s.opts.Resolve(modelID, effort)
		if err != nil {
			return "", "", ModelResolution{}, err
		}
		if resolved.Dialect == "" {
			resolved.Dialect = parent.Dialect
		}
		if resolved.Limits.MaxContextTokens <= 0 {
			resolved.Limits = parent.Limits
		}
		resolution = resolved
	}
	return modelID, effort, resolution, nil
}

// reserveChild consumes one fresh-child slot, or reports that the limit is
// reached. A zero limit is unlimited.
func (s *Spawner) reserveChild() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opts.MaxSubagents > 0 && s.spawned >= s.opts.MaxSubagents {
		return false
	}
	s.spawned++
	return true
}

// remainingBudget returns the token budget a child may spend: the parent's
// remaining budget, or 0 when the parent is unlimited.
func (s *Spawner) remainingBudget(call tools.Call) int {
	remaining, limited := s.remainingTokens(call)
	if !limited {
		return 0
	}
	if remaining < 0 {
		return 0
	}
	return remaining
}

// remainingTokens reports the parent's remaining token budget and whether one
// is configured.
func (s *Spawner) remainingTokens(call tools.Call) (int, bool) {
	limit := s.opts.Config.MaxTotalTokens
	if limit <= 0 {
		return 0, false
	}
	s.mu.Lock()
	used := s.childUse.Total()
	s.mu.Unlock()
	return limit - call.ParentUsage.Total() - used, true
}

// addChildUsage folds one finished task's spend into the run's child total.
func (s *Spawner) addChildUsage(usage model.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.childUse.InputTokens += usage.InputTokens
	s.childUse.OutputTokens += usage.OutputTokens
}

func (s *Spawner) parentSeq() uint64 {
	if s.opts.ParentSeq == nil {
		return 0
	}
	return s.opts.ParentSeq()
}

// workPaths mirrors the parent's per-session work-directory layout.
func (s *Spawner) workPaths(sessionID string) (string, string) {
	workDir := filepath.Join(s.opts.Config.StateDir, "work", sessionID)
	return workDir, filepath.Join(workDir, "tmp")
}

// childToolOptions builds the child's registry options. The spawn interface is
// cleared so depth stays one even if the parent's options carry it.
func (s *Spawner) childToolOptions(sessionID, workDir, tmpDir string) tools.Options {
	var opts tools.Options
	if s.opts.NewToolOptions != nil {
		opts = s.opts.NewToolOptions(sessionID, workDir, tmpDir)
	}
	opts.WorkDir = workDir
	opts.TmpDir = tmpDir
	opts.Spawning = nil
	return opts
}

// newCompleter builds the child's provider adapter.
func (s *Spawner) newCompleter(cfg Config, sessionID string) model.Completer {
	if s.opts.NewCompleter == nil {
		return failingCompleter{}
	}
	return s.opts.NewCompleter(model.Dialect(cfg.Dialect), cfg.BaseURL, sessionID)
}

// prepareChildWorkDir creates a fresh harness-owned work directory and TMPDIR
// for one child run.
func prepareChildWorkDir(workDir, tmpDir string) error {
	if err := os.RemoveAll(workDir); err != nil {
		return err
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	return os.Chmod(tmpDir, 0o700)
}

// failingCompleter reports a missing child adapter as a provider failure
// instead of panicking.
type failingCompleter struct{}

func (failingCompleter) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("no child model adapter is configured")
}

// reportCode maps a terminal child status to a stable tool result code.
func reportCode(status StopReason) tools.Code {
	switch status {
	case StopCompleted:
		return tools.CodeOK
	case StopMaxTurns, StopTokenLimit, StopTimeLimit:
		return tools.CodeSubagentLimit
	case StopInterrupted:
		return tools.CodeCanceled
	case StopProviderError, StopProtocolError, StopStuck, StopInternalError, "":
		return tools.CodeInternal
	default:
		return tools.CodeInternal
	}
}

// failedReport builds a per-task failure with a code and a short message.
func failedReport(spec tools.SubagentSpec, code tools.Code, status, message string) tools.SubagentReport {
	report := tools.SubagentReport{
		Agent:  spec.Agent,
		Intent: subagentIntent(spec.Task),
		Status: status,
		Code:   code,
		Text:   message,
	}
	if report.Agent == "" {
		report.Agent = "?"
	}
	return report
}

// taskFromDelegate is the user-message source recorded for a delegated task.
const taskFromDelegate = "delegate"

// childDisplay is the display adapter for one child run. It forwards only
// counters and names: child text, arguments, and tool output stay in the
// child's own transcript.
type childDisplay struct {
	spawner       *Spawner
	call          tools.Call
	index         int
	agent         string
	intent        string
	session       string
	model         string
	effort        string
	tools         []string
	usage         model.Usage
	pricing       model.Pricing
	cost          int64
	lastEmit      time.Time
	announced     bool
	startedAt     time.Time
	childTurns    int
	contextTokens int
	lastTool      string
}

func (s *Spawner) newChildDisplay(call tools.Call, index int, agent, session, modelID, effort string, grant []string, pricing model.Pricing) *childDisplay {
	return &childDisplay{
		spawner: s,
		call:    call,
		index:   index,
		agent:   agent,
		session: session,
		model:   modelID,
		effort:  effort,
		tools:   append([]string(nil), grant...),
		pricing: pricing,
	}
}

// started emits SubagentStarted with the resolved parent-side fields.
func (d *childDisplay) started() error {
	d.announced = true
	d.startedAt = d.spawner.opts.Now()
	return d.emit(SubagentStarted{
		Turn:    d.call.Turn,
		CallID:  d.call.ID,
		Index:   d.index,
		Agent:   d.agent,
		Intent:  d.intent,
		Session: d.session,
		Model:   d.model,
		Effort:  d.effort,
		Tools:   d.tools,
	})
}

// withIntent sets the derived roster intent before the start event.
func (d *childDisplay) withIntent(task string) *childDisplay {
	d.intent = subagentIntent(task)
	return d
}

// display is the child runner's display callback. It accumulates counters and
// emits at most one progress event per second per child.
func (d *childDisplay) display(ev DisplayEvent) error {
	switch ev := ev.(type) {
	case ResponseCompleted:
		d.childTurns++
		if total := ev.Usage.Total(); total > 0 {
			d.contextTokens = total
		}
		d.usage.InputTokens += ev.Usage.InputTokens
		d.usage.OutputTokens += ev.Usage.OutputTokens
		if cost, priced := d.pricing.Cost(ev.Usage); priced {
			d.cost += cost
		}
		now := d.spawner.opts.Now()
		if now.Sub(d.lastEmit) < time.Second {
			return nil
		}
		d.lastEmit = now
		return d.emitProgress()
	case ToolStarted:
		d.lastTool = ev.Call.Name
		return nil
	}
	return nil
}

func (d *childDisplay) emitProgress() error {
	return d.emit(SubagentProgress{
		Turn:          d.call.Turn,
		CallID:        d.call.ID,
		Index:         d.index,
		Agent:         d.agent,
		Session:       d.session,
		ChildTurns:    d.childTurns,
		ContextTokens: d.contextTokens,
		Usage:         d.usage,
		CostMicroUSD:  d.cost,
		CostComplete:  d.priced(),
		LastTool:      d.lastTool,
	})
}

// priced reports whether every observed child response had a price. The
// pricing snapshot itself is unavailable when the model has no catalog prices.
func (d *childDisplay) priced() bool {
	return d.pricing.Available
}

// finished emits SubagentFinished and folds the child's spend into the run's
// child total used for the token budget.
func (d *childDisplay) finished(report tools.SubagentReport) {
	d.spawner.addChildUsage(report.Usage)
	if !d.announced {
		return
	}
	_ = d.emit(SubagentFinished{
		Turn:          d.call.Turn,
		CallID:        d.call.ID,
		Index:         d.index,
		Agent:         d.agent,
		Session:       d.session,
		Status:        report.Status,
		ChildTurns:    report.Turns,
		ContextTokens: report.ContextTokens,
		Usage:         report.Usage,
		CostMicroUSD:  report.CostMicroUSD,
		CostComplete:  report.CostComplete,
		Duration:      d.spawner.opts.Now().Sub(d.startedAt),
	})
}

// emit forwards one event through the parent display, serialized because a
// future milestone runs children on separate goroutines.
func (d *childDisplay) emit(ev DisplayEvent) error {
	d.spawner.displayMu.Lock()
	defer d.spawner.displayMu.Unlock()
	if d.spawner.opts.Display == nil {
		return nil
	}
	return d.spawner.opts.Display(ev)
}

// childRunOptions builds the child run options for a delegated task.
func childRunOptions(task string) RunOptions {
	return RunOptions{Task: task, TaskSet: true, TaskFrom: taskFromDelegate}
}
