package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// Spawn executes the call's tasks and returns one report per task in array
// order. A call whose every task holds a read-only grant runs its tasks
// concurrently; any other call keeps array order and runs sequentially. A
// canceled parent context stops the loop and reports the remaining tasks as
// not started.
func (s *Spawner) Spawn(ctx context.Context, call tools.Call, specs []tools.SubagentSpec) tools.SpawnResult {
	reports := make([]tools.SubagentReport, len(specs))
	execution, reason := s.executionMode(specs)
	if execution == tools.ExecutionConcurrent && !s.reserveFits(specs) {
		// Keep array order and deterministic budget assignment when the call
		// cannot reserve every fresh child.
		execution = tools.ExecutionSequential
		reason = fmt.Sprintf("the call needs more than the remaining -max-subagents budget (%d)", s.opts.MaxSubagents)
	}
	if execution != tools.ExecutionConcurrent {
		for i, spec := range specs {
			if ctx.Err() != nil {
				reports[i] = notStartedReport(spec)
				s.finishedWithoutStart(call, i, spec, reports[i])
				continue
			}
			reports[i] = s.spawnOne(ctx, call, i, spec)
		}
		return tools.SpawnResult{Reports: reports, Execution: tools.ExecutionSequential, ExecutionReason: reason}
	}

	// Every task writes a distinct report index, so the slice needs no lock.
	// maxConcurrentChildren currently equals the schema cap, but the semaphore
	// keeps the bound explicit if either changes.
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxConcurrentChildren)
	for i, spec := range specs {
		if ctx.Err() != nil {
			reports[i] = notStartedReport(spec)
			s.finishedWithoutStart(call, i, spec, reports[i])
			continue
		}
		wg.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				reports[i] = notStartedReport(spec)
				s.finishedWithoutStart(call, i, spec, reports[i])
				return
			}
			if ctx.Err() != nil {
				reports[i] = notStartedReport(spec)
				s.finishedWithoutStart(call, i, spec, reports[i])
				return
			}
			reports[i] = s.spawnOne(ctx, call, i, spec)
		})
	}
	wg.Wait()
	return tools.SpawnResult{Reports: reports, Execution: tools.ExecutionConcurrent}
}

// notStartedReport reports one task skipped because the parent operation was
// already canceled.
func notStartedReport(spec tools.SubagentSpec) tools.SubagentReport {
	return failedReport(spec, tools.CodeCanceled, tools.SubagentInterrupted,
		"not started: the run was canceled")
}

func (s *Spawner) spawnOne(ctx context.Context, call tools.Call, index int, spec tools.SubagentSpec) (report tools.SubagentReport) {
	emitted := false
	defer func() {
		if !emitted {
			s.finishedWithoutStart(call, index, spec, report)
		}
	}()
	if spec.Session != "" {
		// continueChild owns its finished event.
		emitted = true
		return s.continueChild(ctx, call, index, spec)
	}
	def, ok := s.opts.Definitions.Lookup(spec.Agent)
	if !ok {
		return failedReport(spec, tools.CodeNotFound, tools.SubagentFailed,
			fmt.Sprintf("unknown agent %q", spec.Agent))
	}
	grant, denied := s.childGrant(def)
	if denied != "" {
		return failedReport(spec, tools.CodeSpawnFailed, tools.SubagentFailed,
			fmt.Sprintf("agent %q requests tool %q, which is not available to the parent session", def.Name, denied))
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

	childCfg := s.childConfig(def, modelID, effort, resolution, call, grant)
	sessionID := session.NewID()
	steering := s.registerSteering(sessionID)
	defer s.unregisterSteering(sessionID)
	childCfg.Steering = steering
	childCfg.SteeringSource = SourceSubagentSteer
	childCfg.Display = nil
	adapter := s.newChildDisplay(call, index, def.Name, sessionID, modelID, effort, childCfg.Tools, resolution.Pricing).withIntent(spec.Task)
	if err := adapter.observe(); err != nil {
		report = failedReport(spec, tools.CodeInternal, tools.SubagentInternalError, err.Error())
		adapter.finished(report)
		emitted = true
		return report
	}
	if err := adapter.started(); err != nil {
		report = failedReport(spec, tools.CodeInternal, tools.SubagentFailed, err.Error())
		adapter.finished(report)
		emitted = true
		return report
	}
	report = s.runChildSession(ctx, childCfg, adapter, sessionID, childRunOptions(spec.Task), s.childCompleter(def, resolution, sessionID), resolution.ImageInput)
	adapter.finished(report)
	emitted = true
	return report
}

// continueChild resumes a child session recorded by an earlier delegate call.
// The recorded prompt and grant are reused, so editing the agent file never
// changes an existing child session.
func (s *Spawner) continueChild(ctx context.Context, call tools.Call, index int, spec tools.SubagentSpec) (report tools.SubagentReport) {
	emitted := false
	defer func() {
		if !emitted {
			s.finishedWithoutStart(call, index, spec, report)
		}
	}()
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
	if start.Agent != "" {
		if def, ok := s.opts.Definitions.LookupDefinition(start.Agent); ok && def.Disabled {
			return failedReport(spec, tools.CodeNotFound, tools.SubagentFailed,
				fmt.Sprintf("agent %q is disabled by an operator override", start.Agent))
		}
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
	steering := s.registerSteering(spec.Session)
	defer s.unregisterSteering(spec.Session)
	childCfg.Steering = steering
	childCfg.SteeringSource = SourceSubagentSteer
	childCfg.Display = nil
	agentName := state.Agent
	if agentName == "" {
		agentName = spec.Agent
	}
	adapter := s.newChildDisplay(call, index, agentName, spec.Session, state.Model, state.ThinkingEffort, childCfg.Tools, state.Pricing).withIntent(spec.Task)
	if err := adapter.observe(); err != nil {
		_ = store.Close()
		report = failedReport(spec, tools.CodeInternal, tools.SubagentInternalError, err.Error())
		adapter.finished(report)
		emitted = true
		return report
	}
	if err := adapter.started(); err != nil {
		_ = store.Close()
		report = failedReport(spec, tools.CodeInternal, tools.SubagentFailed, err.Error())
		adapter.finished(report)
		emitted = true
		return report
	}
	def, _ := s.opts.Definitions.LookupDefinition(state.Agent)
	resolution := ModelResolution{Dialect: state.Dialect, BaseURL: state.BaseURL, Limits: state.Limits, Pricing: state.Pricing}
	completerDef := def
	if state.Model != "" {
		completerDef.Model = state.Model
	}
	if state.ThinkingEffort != "" {
		completerDef.ThinkingEffort = state.ThinkingEffort
	}
	report = s.runChildOnStore(ctx, childCfg, store, adapter, spec.Session, RunOptions{
		Task:           spec.Task,
		TaskSet:        true,
		TaskFrom:       taskFromDelegate,
		ResumedFromSeq: state.LastEventSeq,
	}, s.childCompleter(completerDef, resolution, spec.Session), resolution.ImageInput)
	adapter.finished(report)
	emitted = true
	return report
}

// runChildSession creates the child log, runs the child, and removes its work
// directory.
func (s *Spawner) runChildSession(ctx context.Context, cfg Config, adapter *childDisplay, sessionID string, opts RunOptions, completer model.Completer, imageInput *bool) tools.SubagentReport {
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
	return s.runChildOnStore(ctx, cfg, store, adapter, sessionID, opts, completer, imageInput)
}

// runChildOnStore runs one child over an open store, closes the store, and
// maps the run result to a report. The store is synced and closed before the
// report is returned, so a failed child stays resumable and inspectable.
func (s *Spawner) runChildOnStore(ctx context.Context, cfg Config, store *session.Store, adapter *childDisplay, sessionID string, opts RunOptions, completer model.Completer, imageInput *bool) tools.SubagentReport {
	workDir, tmpDir := s.workPaths(sessionID)
	if err := prepareChildWorkDir(workDir, tmpDir); err != nil {
		_ = store.Close()
		return failedReport(tools.SubagentSpec{Agent: cfg.Agent, Task: opts.Task},
			tools.CodeInternal, tools.SubagentFailed, fmt.Sprintf("prepare child work directory: %v", err))
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	registry, err := s.childRegistry(sessionID, workDir, tmpDir)
	if err != nil {
		_ = store.Close()
		return failedReport(tools.SubagentSpec{Agent: cfg.Agent, Task: opts.Task},
			tools.CodeInternal, tools.SubagentFailed, fmt.Sprintf("prepare child tool registry: %v", err))
	}
	registry.SetGrant(cfg.Tools)
	// A child on a different model follows that model's capability; nil keeps
	// the value inherited from the parent's options.
	if imageInput != nil {
		registry.SetImageInput(imageInput)
	}
	cfg.Display = adapter.display
	cfg.SessionID = sessionID
	runner := New(cfg, completer, registry, store)
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
	if report.Text == "" && adapter.sinkErr != nil {
		// An observation sink failure terminates that child as internal_error.
		// The child log already records an error event; report the cause too, so
		// the parent's SubagentFinished and delegate message name it instead of
		// showing an empty report. The run's joined error repeats the same cause
		// once per terminating display attempt, so the first one is used.
		report.Text = adapter.sinkErr.Error()
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
func (s *Spawner) childConfig(def AgentDefinition, modelID, effort string, resolution ModelResolution, call tools.Call, grant []string) Config {
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
		Identity:       parent.Identity,
		PromptVersion:  parent.PromptVersion,
		ToolsetVersion: parent.ToolsetVersion,
		HarnessVersion: parent.HarnessVersion,
		SystemPrompt:   childSystemPrompt(def, parent.Workspace, parent.Identity, promptVersion(parent), parent.AppendedPrompt),
		Agent:          def.Name,
		Tools:          cloneGrant(grant),
		CustomTools:    append([]CustomTool(nil), parent.CustomTools...),
		ToolPolicy:     parent.ToolPolicy,
		MaxTurns:       maxTurns,
		MaxTotalTokens: s.remainingBudget(call),
		Limits:         resolution.Limits,
		SessionTimeout: 0,
		ModelTimeout:   parent.ModelTimeout,
		RequireUsage:   parent.RequireUsage,
		Pricing:        resolution.Pricing,
		ThinkingEffort: effort,
		Interactive:    false,
		// A child streams only when a host observes it: streaming costs the
		// retry budget after the first assistant-text delta, and an unobserved
		// child's partial text has no consumer.
		Stream: s.opts.Observe != nil,
	}
}

// continuedConfig builds a resumed child's configuration from its own recorded
// state. The recorded model, effort, prompt, and grant win; the grant is
// narrowed by the current parent's authority, and only the budget comes from
// the parent. Nothing is appended: the recorded prompt already holds any host
// text, so re-recording it would double the text on the next continuation.
func (s *Spawner) continuedConfig(state State, call tools.Call) Config {
	parent := s.opts.Config
	grant := s.narrowChildGrant(state.Tools, state.ToolsetVersion)
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
		Identity:       parent.Identity,
		PromptVersion:  state.PromptVersion,
		ToolsetVersion: state.ToolsetVersion,
		HarnessVersion: state.HarnessVersion,
		SystemPrompt:   state.SystemPrompt,
		Agent:          state.Agent,
		Tools:          grant,
		CustomTools:    append([]CustomTool(nil), parent.CustomTools...),
		ToolPolicy:     parent.ToolPolicy,
		MaxTurns:       maxTurns,
		MaxTotalTokens: s.remainingBudget(call),
		Limits:         state.Limits,
		SessionTimeout: 0,
		ModelTimeout:   parent.ModelTimeout,
		RequireUsage:   parent.RequireUsage,
		Pricing:        state.Pricing,
		ThinkingEffort: state.ThinkingEffort,
		Interactive:    false,
		// A child streams only when a host observes it: streaming costs the
		// retry budget after the first assistant-text delta, and an unobserved
		// child's partial text has no consumer.
		Stream: s.opts.Observe != nil,
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

// childRegistry builds the child's registry. The injected factory owns custom
// tool registration; the default factory provides built-ins only. The parent
// grant is applied by the caller after construction.
func (s *Spawner) childRegistry(sessionID, workDir, tmpDir string) (*tools.Registry, error) {
	if s.opts.NewRegistry != nil {
		return s.opts.NewRegistry(s.opts.Workspace, sessionID, workDir, tmpDir)
	}
	return tools.NewRegistry(s.opts.Workspace, s.childToolOptions(sessionID, workDir, tmpDir)), nil
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

// childCompleter builds the child's provider adapter, preferring the
// resolved-definition factory.
func (s *Spawner) childCompleter(def AgentDefinition, resolution ModelResolution, sessionID string) model.Completer {
	if s.opts.NewChildCompleter != nil {
		return s.opts.NewChildCompleter(def, resolution, sessionID)
	}
	if s.opts.NewCompleter == nil {
		return failingCompleter{}
	}
	return s.opts.NewCompleter(model.Dialect(resolution.Dialect), resolution.BaseURL, sessionID)
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

// childRunOptions builds the child run options for a delegated task.
func childRunOptions(task string) RunOptions {
	return RunOptions{Task: task, TaskSet: true, TaskFrom: taskFromDelegate}
}
