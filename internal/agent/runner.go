package agent

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/eddiectc/aharness/internal/model"
	"github.com/eddiectc/aharness/internal/session"
	"github.com/eddiectc/aharness/internal/tools"
)

// Runner owns one bounded model/tool loop over a durable session.
type Runner struct {
	cfg            Config
	model          completer
	tools          toolset
	store          eventStore
	jitter         func(time.Duration) time.Duration
	steeringClosed bool
}

// New returns a runner wired to the production dialect adapter, tool
// registry, and session store. The caller owns closing the store.
func New(cfg Config, c model.Completer, reg *tools.Registry, store *session.Store) *Runner {
	return newRunner(cfg, c, reg, store)
}

// newRunner is the consumer-interface constructor used by loop tests.
func newRunner(cfg Config, c completer, ts toolset, store eventStore) *Runner {
	return &Runner{cfg: cfg, model: c, tools: ts, store: store, jitter: addJitter}
}

// errContinueRun tells executeTurn that an interactive interruption was
// handled and the outer loop should rebuild the projection and continue.
var errContinueRun = errors.New("continue run")

// sessionWriter is the write side needed to start a session.
type sessionWriter interface {
	Append(session.Event) error
	Sync() error
	Events() []session.Event
}

// WriteSessionStart writes and syncs the once-per-session session_start event
// for a new or forked session. The store must have no events.
func WriteSessionStart(store sessionWriter, cfg Config, parent *ParentRef) error {
	if len(store.Events()) != 0 {
		return errors.New("start session: session already has events")
	}
	data := sessionStartData{
		PromptVersion:    promptVersion(cfg),
		ToolsetVersion:   toolsetVersion(cfg),
		Workspace:        cfg.Workspace,
		StateDir:         cfg.StateDir,
		Provider:         cfg.Provider,
		Dialect:          cfg.Dialect,
		Model:            cfg.Model,
		BaseURL:          cfg.BaseURL,
		Limits:           cfg.Limits,
		MaxTurns:         cfg.MaxTurns,
		MaxTotalTokens:   cfg.MaxTotalTokens,
		SessionTimeoutMS: cfg.SessionTimeout.Milliseconds(),
		ModelTimeoutMS:   cfg.ModelTimeout.Milliseconds(),
		HarnessVersion:   cfg.HarnessVersion,
		ThinkingEffort:   cfg.ThinkingEffort,
		SystemPrompt:     systemPromptText(cfg),
		Agent:            cfg.Agent,
		Tools:            cloneGrant(cfg.Tools),
		CustomTools:      recordedCustomTools(cfg.CustomTools),
		ContractOverride: cfg.ContractOverride,
	}
	if parent != nil {
		data.ParentSession = parent.Session
		data.ParentSeq = parent.Seq
		data.ParentCallID = parent.CallID
	}
	ev, err := session.NewEvent(session.TypeSessionStart, 0, "", data)
	if err != nil {
		return fmt.Errorf("build session_start event: %w", err)
	}
	if err := store.Append(ev); err != nil {
		return fmt.Errorf("append session_start event: %w", err)
	}
	return store.Sync()
}

func promptVersion(cfg Config) string {
	if cfg.PromptVersion != "" {
		return cfg.PromptVersion
	}
	return PromptVersion
}

func toolsetVersion(cfg Config) string {
	if cfg.ToolsetVersion != "" {
		return cfg.ToolsetVersion
	}
	return tools.Version
}

// StartSession writes the once-per-session session_start event for a new or
// forked session and syncs it. It must be called before Run and only when the
// store has no events.
func (r *Runner) StartSession(parent *ParentRef) error {
	return WriteSessionStart(r.store, r.cfg, parent)
}

// runState is per-Run mutable state. It never escapes the loop.
type runState struct {
	start            time.Time
	turns            int
	usage            model.Usage
	contextTokens    int
	runTurns         int
	runUsage         model.Usage
	cost             int64
	runCost          int64
	costComplete     bool
	runCostComplete  bool
	lastText         string
	lastElided       int
	compactionTarget int
	lengthStreak     int
	failKey          string
	failCount        int
	finalized        bool
	// Delegated children spend inside this run's call. Their tokens and cost
	// fold into the run's totals; their turns never do.
	subagentTurns        int
	subagentUsage        model.Usage
	subagentCost         int64
	subagentCostComplete bool
}

// Run executes one run to a terminal state. Every initialized run emits
// exactly one final event followed by one run_end event.
func (r *Runner) Run(ctx context.Context, opts RunOptions) (Result, error) {
	st := &runState{start: time.Now()}
	res := Result{Status: StopCompleted}

	runCtx := ctx
	var cancel context.CancelFunc
	if r.cfg.SessionTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, r.cfg.SessionTimeout)
		defer cancel()
	}

	state, err := Inspect(r.store.Events())
	if err != nil {
		res.Status = StopInternalError
		res.Err = err
		return res, err
	}
	st.turns = state.Turns
	st.usage = state.Usage
	st.contextTokens = state.ContextTokens
	st.cost = state.CostMicroUSD
	st.costComplete = state.CostComplete
	st.runCostComplete = true
	st.subagentTurns = state.SubagentTurns
	st.subagentUsage = state.SubagentUsage
	st.subagentCost = state.SubagentCostMicroUSD
	st.subagentCostComplete = true
	if state.SubagentUsage.Total() > 0 || state.SubagentTurns > 0 {
		st.subagentCostComplete = state.SubagentCostComplete
	}
	if !st.subagentCostComplete {
		// The cumulative estimate includes the children, so an unpriced child
		// from an earlier run keeps the session total incomplete.
		st.costComplete = false
	}

	// Repair every unpaired call before the new run starts so the pairing
	// invariant holds even after a crash.
	repaired, err := r.repairPending(state.Pending)
	if err != nil {
		res.Status = StopInternalError
		res.Err = err
		return res, err
	}

	if err := r.recordRunStart(opts.ResumedFromSeq); err != nil {
		res.Status = StopInternalError
		res.Err = err
		return res, err
	}
	if err := r.sync(); err != nil {
		res.Status = StopInternalError
		res.Err = err
		return res, err
	}
	if err := r.emit(RunStarted{
		Turn:           st.turns,
		ResumedFromSeq: opts.ResumedFromSeq,
		Model:          r.cfg.Model,
		Dialect:        r.cfg.Dialect,
		ThinkingEffort: r.cfg.ThinkingEffort,
	}); err != nil {
		if rerr := r.recordError(st.turns, errorClassInternal, err.Error()); rerr != nil {
			err = errors.Join(err, rerr)
		}
		res.Err = err
		return r.finishResult(&res, st, StopInternalError, "failed to display the run start")
	}
	for _, call := range repaired {
		if err := r.emit(ToolCompleted{
			Turn:   call.Turn,
			CallID: call.CallID,
			Name:   call.Name,
			OK:     false,
			Code:   tools.CodeCanceled,
			Error:  repairMessage,
		}); err != nil {
			if rerr := r.recordError(st.turns, errorClassInternal, err.Error()); rerr != nil {
				err = errors.Join(err, rerr)
			}
			res.Err = err
			return r.finishResult(&res, st, StopInternalError, "failed to display a repaired tool result")
		}
		if err := r.notice(call.Turn, NoticeRepair, fmt.Sprintf("repaired unpaired tool call %s (%s) with a canceled result", call.CallID, call.Name)); err != nil {
			if rerr := r.recordError(st.turns, errorClassInternal, err.Error()); rerr != nil {
				err = errors.Join(err, rerr)
			}
			res.Err = err
			return r.finishResult(&res, st, StopInternalError, "failed to display a repair notice")
		}
	}

	if opts.TaskSet {
		if err := r.recordUserMessage(0, opts.Task, opts.TaskFrom); err != nil {
			res.Err = err
			return r.finishResult(&res, st, StopInternalError, "failed to record the task")
		}
	}

	for {
		status, reason, err := r.checkLimits(runCtx, st)
		if err != nil {
			res.Err = err
			return r.finishResult(&res, st, StopInternalError, "failed to record a limit event")
		}
		if status != "" {
			return r.finishResult(&res, st, status, reason)
		}
		if _, err := r.consumeSteering(st); err != nil {
			res.Err = err
			return r.finishResult(&res, st, StopInternalError, "failed to record a steering message")
		}

		limits := r.currentLimits(st)
		proj, err := ProjectWithTools(r.store.Events(), limits, r.cfg.CustomTools)
		if err != nil {
			if errors.Is(err, ErrProjectionTooLarge) {
				if proj.ElidedResults != st.lastElided {
					if cerr := r.recordCompaction(st.turns+1, proj, policyCompaction); cerr != nil {
						res.Err = cerr
						return r.finishResult(&res, st, StopInternalError, "failed to record compaction")
					}
					st.lastElided = proj.ElidedResults
				}
				if lerr := r.recordLimit(st.turns, limitMaxInputTokens, int64(limits.MaxInputTokens), int64(proj.EstimatedInputTokens)); lerr != nil {
					res.Err = lerr
					return r.finishResult(&res, st, StopInternalError, "failed to record a limit event")
				}
				return r.finishResult(&res, st, StopTokenLimit, "context projection cannot fit the input budget")
			}
			res.Err = err
			return r.finishResult(&res, st, StopInternalError, "context projection failed")
		}
		if proj.ElidedResults != st.lastElided {
			if err := r.recordCompaction(st.turns+1, proj, policyCompaction); err != nil {
				res.Err = err
				return r.finishResult(&res, st, StopInternalError, "failed to record compaction")
			}
			st.lastElided = proj.ElidedResults
		}

		turn := st.turns + 1
		done, err := r.executeTurn(runCtx, &res, st, proj, turn)
		if done {
			return res, err
		}
	}
}

// repairPending pairs unpaired tool calls with a canceled result before a new
// run starts. The message does not claim the side effect did not happen. It
// returns the repaired calls so the display can note them after the run
// starts.
func (r *Runner) repairPending(pending []PendingCall) ([]PendingCall, error) {
	for _, call := range pending {
		payload := toolResultData{
			CallID: call.CallID,
			Name:   call.Name,
			OK:     false,
			Code:   tools.CodeCanceled,
			Error:  repairMessage,
		}
		if err := r.recordToolResult(call.Turn, payload); err != nil {
			return nil, err
		}
	}
	return pending, nil
}

func (r *Runner) recordRunStart(resumedFromSeq uint64) error {
	data := runStartData{
		RunID:            session.NewID(),
		ResumedFromSeq:   resumedFromSeq,
		Provider:         r.cfg.Provider,
		Model:            r.cfg.Model,
		Dialect:          r.cfg.Dialect,
		BaseURL:          r.cfg.BaseURL,
		Limits:           r.cfg.Limits,
		MaxTurns:         r.cfg.MaxTurns,
		MaxTotalTokens:   r.cfg.MaxTotalTokens,
		SessionTimeoutMS: r.cfg.SessionTimeout.Milliseconds(),
		ModelTimeoutMS:   r.cfg.ModelTimeout.Milliseconds(),
		PromptVersion:    promptVersion(r.cfg),
		ToolsetVersion:   toolsetVersion(r.cfg),
		HarnessVersion:   r.cfg.HarnessVersion,
		Interactive:      r.cfg.Interactive,
		Stream:           r.cfg.Stream,
		ThinkingEffort:   r.cfg.ThinkingEffort,
		RequireUsage:     r.cfg.RequireUsage,
		Pricing:          r.cfg.Pricing,
	}
	return r.record(session.TypeRunStart, 0, "", data)
}

// currentLimits applies a tightened compaction target after a provider
// context-overflow retry, so elision remains monotonic for the session.
func (r *Runner) currentLimits(st *runState) Limits {
	limits := r.cfg.Limits
	if st.compactionTarget > 0 && (limits.CompactionTarget <= 0 || st.compactionTarget < limits.CompactionTarget) {
		limits.CompactionTarget = st.compactionTarget
	}
	return limits
}

// checkLimits enforces the cumulative turn/token and per-run wall-clock
// budgets before a model call. A non-empty status means the run must terminate.
func (r *Runner) checkLimits(ctx context.Context, st *runState) (StopReason, string, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			reason := "context deadline reached"
			if r.cfg.SessionTimeout > 0 {
				observed := time.Since(st.start)
				if rerr := r.recordLimit(st.turns, limitSessionTimeout, r.cfg.SessionTimeout.Milliseconds(), observed.Milliseconds()); rerr != nil {
					return StopInternalError, "", rerr
				}
				reason = fmt.Sprintf("session deadline reached (-session-timeout %s, elapsed %s)", r.cfg.SessionTimeout, observed.Round(time.Millisecond))
			}
			return StopTimeLimit, reason, nil
		}
		return StopInterrupted, "interrupted by signal", nil
	}
	if r.cfg.MaxTurns > 0 && st.turns >= r.cfg.MaxTurns {
		if err := r.recordLimit(st.turns, limitMaxTurns, int64(r.cfg.MaxTurns), int64(st.turns)); err != nil {
			return StopInternalError, "", err
		}
		return StopMaxTurns, fmt.Sprintf("turn limit reached (-max-turns %d, turns %d)", r.cfg.MaxTurns, st.turns), nil
	}
	if r.cfg.SessionTimeout > 0 && time.Since(st.start) >= r.cfg.SessionTimeout {
		observed := time.Since(st.start)
		if err := r.recordLimit(st.turns, limitSessionTimeout, r.cfg.SessionTimeout.Milliseconds(), observed.Milliseconds()); err != nil {
			return StopInternalError, "", err
		}
		return StopTimeLimit, fmt.Sprintf("session deadline reached (-session-timeout %s, elapsed %s)", r.cfg.SessionTimeout, observed.Round(time.Millisecond)), nil
	}
	if total := st.totalTokens(); r.cfg.MaxTotalTokens > 0 && total >= r.cfg.MaxTotalTokens {
		if err := r.recordLimit(st.turns, limitMaxTotalTokens, int64(r.cfg.MaxTotalTokens), int64(total)); err != nil {
			return StopInternalError, "", err
		}
		return StopTokenLimit, fmt.Sprintf("total token budget reached (-max-total-tokens %d, tokens %d)", r.cfg.MaxTotalTokens, total), nil
	}
	return "", "", nil
}

// consumeSteering appends every already-pending stdin steering line as a
// user_message. It never blocks and never interrupts a tool round.
func (r *Runner) consumeSteering(st *runState) (int, error) {
	if r.cfg.Steering == nil || r.steeringClosed {
		return 0, nil
	}
	count := 0
	for {
		select {
		case line, ok := <-r.cfg.Steering:
			if !ok {
				r.steeringClosed = true
				return count, nil
			}
			text := cleanText(line)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if len(text) > maxSteeringBytes {
				text = capText(text, maxSteeringBytes)
			}
			source := r.cfg.SteeringSource
			if source == "" {
				source = sourceSteer
			}
			if err := r.recordUserMessage(st.turns, text, source); err != nil {
				return count, err
			}
			count++
		default:
			return count, nil
		}
	}
}

func (r *Runner) beginOperation(ctx context.Context) (context.Context, func()) {
	if r.cfg.Controller == nil {
		return ctx, func() {}
	}
	return r.cfg.Controller.BeginOperation(ctx)
}

// emit delivers one display-only event. A nil display callback is disabled.
func (r *Runner) emit(ev DisplayEvent) error {
	if r.cfg.Display == nil {
		return nil
	}
	if err := r.cfg.Display(sanitizeDisplayEvent(ev)); err != nil {
		return fmt.Errorf("%w: %w", errDisplay, err)
	}
	return nil
}

// notice emits a capped display-only notice.
func (r *Runner) notice(turn int, kind NoticeKind, text string) error {
	return r.emit(Notice{Turn: turn, Kind: kind, Text: capText(text, maxErrorEventBytes)})
}

// failDisplay records a display failure when possible and makes it terminal.
func (r *Runner) failDisplay(res *Result, st *runState, err error, action string) (bool, error) {
	if rerr := r.recordError(st.turns, errorClassInternal, err.Error()); rerr != nil {
		res.Err = errors.Join(err, rerr)
	} else {
		res.Err = err
	}
	return r.finishTerminal(res, st, StopInternalError, action)
}

// executeTurn performs one model call and any resulting tool calls. It reports
// done when the run reached a terminal state and the final events were written.
func (r *Runner) executeTurn(ctx context.Context, res *Result, st *runState, proj Projection, turn int) (bool, error) {
	req := model.Request{
		Model:           r.cfg.Model,
		Messages:        proj.Messages,
		Tools:           proj.Schemas,
		MaxOutputTokens: r.cfg.Limits.MaxOutputTokens,
		ThinkingEffort:  r.cfg.ThinkingEffort,
	}

	opCtx, finish := r.beginOperation(ctx)
	comp, err := r.callModel(opCtx, req, turn, proj.EstimatedInputTokens)
	interrupted := err != nil && ctx.Err() == nil && errors.Is(opCtx.Err(), context.Canceled)
	finish()

	if err != nil {
		if errors.Is(err, errEventStore) {
			res.Err = err
			return r.finishTerminal(res, st, StopInternalError, "failed to record a model event")
		}
		if errors.Is(err, errDisplay) {
			return r.failDisplay(res, st, err, "failed to display model progress")
		}
		if ctx.Err() != nil {
			return r.finishContext(res, st, ctx)
		}
		if interrupted {
			return r.handleModelInterrupt(res, st, err)
		}
		if errors.Is(err, model.ErrContextOverflow) {
			partialText, partialBytes := partialFromError(err)
			if partialText == "" && partialBytes == 0 {
				comp, done, retryErr := r.overflowRetry(ctx, res, st, proj, turn)
				if done {
					return true, retryErr
				}
				if errors.Is(retryErr, errContinueRun) {
					return false, nil
				}
				if retryErr != nil {
					return false, retryErr
				}
				return r.processCompletion(ctx, res, st, comp, turn)
			}
		}
		return r.failModel(res, st, err)
	}
	return r.processCompletion(ctx, res, st, comp, turn)
}

// handleModelInterrupt records an interactive interruption of a model call
// and the hint that tells the next call why no response was recorded.
func (r *Runner) handleModelInterrupt(res *Result, st *runState, err error) (bool, error) {
	partialText, _ := partialFromError(err)
	reason := "interrupted by SIGINT"
	if partialText != "" {
		reason = "interrupted by SIGINT after partial output"
	}
	if rerr := r.record(session.TypeInterrupt, st.turns, "", interruptData{
		Operation:   operationModel,
		PartialText: partialText,
		Reason:      reason,
	}); rerr != nil {
		res.Err = rerr
		return r.finishTerminal(res, st, StopInternalError, "failed to record a model interrupt")
	}
	if rerr := r.sync(); rerr != nil {
		res.Err = rerr
		return r.finishTerminal(res, st, StopInternalError, "failed to sync the session log")
	}
	if rerr := r.notice(st.turns, NoticeInterrupt, "interrupted the model call before it completed"); rerr != nil {
		return r.failDisplay(res, st, rerr, "failed to display a model interrupt")
	}
	if rerr := r.recordUserMessage(st.turns, interruptHint, sourceInterruptHint); rerr != nil {
		res.Err = rerr
		return r.finishTerminal(res, st, StopInternalError, "failed to record the interrupt hint")
	}
	st.failKey = ""
	st.failCount = 0
	return false, nil
}

// overflowRetry rebuilds the projection against a target 10% below the
// per-call input limit and makes one model call outside the transport retry
// budget.
func (r *Runner) overflowRetry(ctx context.Context, res *Result, st *runState, failed Projection, turn int) (completion, bool, error) {
	target := max(r.cfg.Limits.MaxInputTokens*9/10, 1)
	limits := r.currentLimits(st)
	limits.CompactionTarget = target
	next, err := ProjectWithTools(r.store.Events(), limits, r.cfg.CustomTools)
	if err != nil && !errors.Is(err, ErrProjectionTooLarge) {
		res.Err = err
		done, ferr := r.finishTerminal(res, st, StopInternalError, "context projection failed")
		return completion{}, done, ferr
	}
	if cerr := r.recordCompaction(turn, next, policyProviderOverflow); cerr != nil {
		res.Err = cerr
		done, ferr := r.finishTerminal(res, st, StopInternalError, "failed to record compaction")
		return completion{}, done, ferr
	}
	st.lastElided = next.ElidedResults
	if err != nil {
		if lerr := r.recordLimit(st.turns, limitMaxInputTokens, int64(limits.MaxInputTokens), int64(next.EstimatedInputTokens)); lerr != nil {
			res.Err = lerr
			done, ferr := r.finishTerminal(res, st, StopInternalError, "failed to record a limit event")
			return completion{}, done, ferr
		}
		done, ferr := r.finishTerminal(res, st, StopTokenLimit, "context overflow could not be compacted")
		return completion{}, done, ferr
	}
	if next.ElidedResults <= failed.ElidedResults {
		if lerr := r.recordLimit(st.turns, limitContextOverflow, int64(limits.MaxInputTokens), int64(next.EstimatedInputTokens)); lerr != nil {
			res.Err = lerr
			done, ferr := r.finishTerminal(res, st, StopInternalError, "failed to record a limit event")
			return completion{}, done, ferr
		}
		done, ferr := r.finishTerminal(res, st, StopTokenLimit, "no tool results could be elided after context overflow")
		return completion{}, done, ferr
	}
	st.compactionTarget = target

	req := model.Request{
		Model:           r.cfg.Model,
		Messages:        next.Messages,
		Tools:           next.Schemas,
		MaxOutputTokens: r.cfg.Limits.MaxOutputTokens,
		ThinkingEffort:  r.cfg.ThinkingEffort,
	}
	opCtx, finish := r.beginOperation(ctx)
	comp, err := r.callModelOnce(opCtx, req, turn, next.EstimatedInputTokens)
	interrupted := err != nil && ctx.Err() == nil && errors.Is(opCtx.Err(), context.Canceled)
	finish()
	if err != nil {
		if errors.Is(err, errEventStore) {
			res.Err = err
			done, ferr := r.finishTerminal(res, st, StopInternalError, "failed to record a model event")
			return completion{}, done, ferr
		}
		if errors.Is(err, errDisplay) {
			done, ferr := r.failDisplay(res, st, err, "failed to display model progress")
			return completion{}, done, ferr
		}
		if ctx.Err() != nil {
			done, ferr := r.finishContext(res, st, ctx)
			return completion{}, done, ferr
		}
		if interrupted {
			done, ferr := r.handleModelInterrupt(res, st, err)
			if ferr != nil {
				return completion{}, done, ferr
			}
			return completion{}, false, errContinueRun
		}
		partialText, partialBytes := partialFromError(err)
		if eerr := r.recordErrorWithPartial(st.turns, errorClassOverflow, fmt.Sprintf("context overflow retry failed: %v", err), partialText, partialBytes); eerr != nil {
			res.Err = eerr
			done, ferr := r.finishTerminal(res, st, StopInternalError, "failed to record an error event")
			return completion{}, done, ferr
		}
		if lerr := r.recordLimit(st.turns, limitContextOverflow, int64(limits.MaxInputTokens), int64(next.EstimatedInputTokens)); lerr != nil {
			res.Err = lerr
			done, ferr := r.finishTerminal(res, st, StopInternalError, "failed to record a limit event")
			return completion{}, done, ferr
		}
		done, ferr := r.finishTerminal(res, st, StopTokenLimit, "context overflow retry failed")
		return completion{}, done, ferr
	}
	return comp, false, nil
}

// failModel records a fatal provider or protocol error and terminates.
func (r *Runner) failModel(res *Result, st *runState, err error) (bool, error) {
	status := StopProviderError
	class, _ := retryErrorClass(err)
	if errors.Is(err, ErrModelPanic) {
		status = StopInternalError
		class = errorClassInternal
	} else if _, ok := errors.AsType[*model.ProtocolError](err); ok {
		status = StopProtocolError
		class = errorClassProtocol
	}
	partialText, partialBytes := partialFromError(err)
	if rerr := r.recordErrorWithPartial(st.turns, class, err.Error(), partialText, partialBytes); rerr != nil {
		res.Err = rerr
		return r.finishTerminal(res, st, StopInternalError, "failed to record an error event")
	}
	res.Err = fmt.Errorf("model call failed: %w", err)
	reason := fmt.Sprintf("model call failed: %v", err)
	return r.finishTerminal(res, st, status, reason)
}

// processCompletion handles one successful model response.
func (r *Runner) processCompletion(ctx context.Context, res *Result, st *runState, comp completion, turn int) (bool, error) {
	if bytes := responseBytes(comp.resp); bytes > maxModelResponseBytes {
		message := fmt.Sprintf("model response is %d bytes, limit %d", bytes, maxModelResponseBytes)
		if rerr := r.recordError(st.turns, errorClassProtocol, message); rerr != nil {
			res.Err = rerr
			return r.finishTerminal(res, st, StopInternalError, "failed to record an error event")
		}
		res.Err = errors.New(message)
		return r.finishTerminal(res, st, StopProtocolError, message)
	}
	// A streamed response that produced no text deltas is displayed once here,
	// before the canonical ResponseCompleted event. Thinking text is displayed
	// when the provider did not stream it (a buffered call or a provider that
	// only reports a reasoning summary on the completed response).
	if comp.resp.Reasoning != "" && !comp.reasoningEmitted {
		if err := r.emit(ReasoningDelta{Text: comp.resp.Reasoning}); err != nil {
			return r.failDisplay(res, st, err, "failed to display the model reasoning")
		}
	}
	if r.cfg.Stream && !comp.emitted && comp.resp.Text != "" {
		if err := r.emit(TextDelta{Text: comp.resp.Text}); err != nil {
			return r.failDisplay(res, st, err, "failed to display the model response")
		}
	}
	cost, priced := r.cfg.Pricing.Cost(comp.resp.Usage)
	var costMicroUSD *int64
	if priced {
		costMicroUSD = &cost
	}
	if err := r.recordResponse(comp, turn, costMicroUSD); err != nil {
		res.Err = err
		return r.finishTerminal(res, st, StopInternalError, "failed to record the model response")
	}
	if err := r.emit(ResponseCompleted{
		Turn:         turn,
		Text:         cleanText(comp.resp.Text),
		Reasoning:    cleanText(comp.resp.Reasoning),
		ToolCalls:    comp.resp.ToolCalls,
		FinishReason: comp.resp.FinishReason,
		Usage:        comp.resp.Usage,
		Latency:      comp.latency,
		Streamed:     comp.streamed,
	}); err != nil {
		return r.failDisplay(res, st, err, "failed to display the model response")
	}
	st.usage.InputTokens += comp.resp.Usage.InputTokens
	st.usage.OutputTokens += comp.resp.Usage.OutputTokens
	if total := comp.resp.Usage.Total(); total > 0 {
		st.contextTokens = total
	}
	st.runUsage.InputTokens += comp.resp.Usage.InputTokens
	st.runUsage.OutputTokens += comp.resp.Usage.OutputTokens
	if priced {
		st.cost += cost
		st.runCost += cost
	} else {
		st.costComplete = false
		st.runCostComplete = false
	}
	if comp.resp.Text != "" {
		st.lastText = comp.resp.Text
	}
	st.turns++
	st.runTurns++
	if r.cfg.Logger != nil {
		r.cfg.Logger.Info("model response",
			"turn", st.turns,
			"attempt", comp.attempt,
			"finish_reason", comp.resp.FinishReason,
			"tool_calls", len(comp.resp.ToolCalls),
			"streamed", comp.streamed,
			"latency_ms", comp.latency.Milliseconds(),
		)
	}
	if r.cfg.RequireUsage && !comp.resp.Usage.Reported {
		const message = "model response did not report token usage (-require-usage)"
		if err := r.recordError(st.turns, errorClassProtocol, message); err != nil {
			res.Err = err
			return r.finishTerminal(res, st, StopInternalError, "failed to record an error event")
		}
		res.Err = errors.New(message)
		return r.finishTerminal(res, st, StopProtocolError, message)
	}

	if len(comp.resp.ToolCalls) == 0 {
		switch comp.resp.FinishReason {
		case model.FinishStop, model.FinishReason(""):
			// A steering line already pending at completion changes the next
			// call instead of ending the run.
			if n, err := r.consumeSteering(st); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a steering message")
			} else if n > 0 {
				return false, nil
			}
			return r.finishTerminal(res, st, StopCompleted, "model completed the task")
		case model.FinishLength:
			st.lengthStreak++
			if st.lengthStreak >= 2 {
				if lerr := r.recordLimit(st.turns, limitConsecutiveLength, 2, int64(st.lengthStreak)); lerr != nil {
					res.Err = lerr
					return r.finishTerminal(res, st, StopInternalError, "failed to record a limit event")
				}
				return r.finishTerminal(res, st, StopTokenLimit, "repeated output length truncation")
			}
			if err := r.recordUserMessage(turn, continuationHint, sourceContinuationHint); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record the continuation hint")
			}
			return false, nil
		case model.FinishContentFilter:
			message := "provider content filter stopped the response"
			if rerr := r.recordError(st.turns, errorClassContentFilter, message); rerr != nil {
				res.Err = rerr
				return r.finishTerminal(res, st, StopInternalError, "failed to record an error event")
			}
			res.Err = errors.New(message)
			return r.finishTerminal(res, st, StopProviderError, message)
		default:
			message := fmt.Sprintf("unexpected finish reason %q", comp.resp.FinishReason)
			if rerr := r.recordError(st.turns, errorClassProtocol, message); rerr != nil {
				res.Err = rerr
				return r.finishTerminal(res, st, StopInternalError, "failed to record an error event")
			}
			res.Err = errors.New(message)
			return r.finishTerminal(res, st, StopProtocolError, message)
		}
	}

	st.lengthStreak = 0
	if err := validateToolCalls(comp.resp.ToolCalls); err != nil {
		if rerr := r.recordError(st.turns, errorClassProtocol, err.Error()); rerr != nil {
			res.Err = rerr
			return r.finishTerminal(res, st, StopInternalError, "failed to record an error event")
		}
		res.Err = err
		return r.finishTerminal(res, st, StopProtocolError, err.Error())
	}
	// Make the model response durable before any side-effecting tool call.
	if err := r.sync(); err != nil {
		res.Err = err
		return r.finishTerminal(res, st, StopInternalError, "failed to sync the session log")
	}
	return r.executeTools(ctx, res, st, comp.resp.ToolCalls, turn)
}

// executeTools runs tool calls sequentially in response order, appends exactly
// one result per call, and applies repeated-failure detection.
func (r *Runner) executeTools(ctx context.Context, res *Result, st *runState, calls []model.ToolCall, turn int) (bool, error) {
	for i, call := range calls {
		if ctx.Err() != nil {
			if err := r.appendUnexecuted(calls[i:], turn, tools.CodeCanceled, canceledBeforeMessage); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
			}
			return r.finishContext(res, st, ctx)
		}

		if err := r.emit(ToolStarted{Turn: turn, Call: call}); err != nil {
			if aerr := r.appendUnexecuted(calls[i:], turn, tools.CodeCanceled, "run canceled after a display failure"); aerr != nil {
				err = errors.Join(err, aerr)
			}
			return r.failDisplay(res, st, err, "failed to display a tool start")
		}

		started := time.Now()
		meta := tools.Call{ID: call.ID, Turn: turn, ParentUsage: st.cumulativeUsage()}
		opCtx, finish := r.beginOperation(ctx)
		result, panicked, policyCanceled := r.executeGuarded(opCtx, call, meta)
		duration := time.Since(started)
		interrupted := policyCanceled || (ctx.Err() == nil && errors.Is(opCtx.Err(), context.Canceled))
		finish()

		if panicked || result.Code == tools.CodePanic {
			normalizeResultCode(&result)
			payload := toolResultEvent(call, result, duration)
			if err := r.recordToolResult(turn, payload); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
			}
			r.logToolResult(call, result, payload)
			if err := r.emit(toolCompletedEvent(turn, call, payload)); err != nil {
				return r.failDisplay(res, st, err, "failed to display a tool result")
			}
			r.foldSubagent(st, call, result)
			if err := r.appendUnexecuted(calls[i+1:], turn, tools.CodeInternal, "tool call not executed after a recovered panic"); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
			}
			res.Err = fmt.Errorf("tool %q panicked: %s", call.Name, result.Error)
			return r.finishTerminal(res, st, StopInternalError, "recovered tool panic")
		}

		if interrupted && !result.OK {
			result = tools.Result{
				OK:         false,
				Code:       tools.CodeCanceled,
				Error:      toolInterruptMessage,
				DurationMS: duration.Milliseconds(),
			}
		} else {
			interrupted = false
		}
		if !interrupted && result.Code == tools.CodeCanceled && ctx.Err() == nil {
			// A custom tool returned a context cancellation without the
			// operation context being canceled. Treat it as an interrupt of
			// that tool: record the canceled result and continue the run.
			interrupted = true
			result = tools.Result{
				OK:         false,
				Code:       tools.CodeCanceled,
				Error:      toolInterruptMessage,
				DurationMS: duration.Milliseconds(),
			}
		}
		normalizeResultCode(&result)
		payload := toolResultEvent(call, result, duration)
		if err := r.recordToolResult(turn, payload); err != nil {
			res.Err = err
			return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
		}
		r.logToolResult(call, result, payload)
		if err := r.emit(toolCompletedEvent(turn, call, payload)); err != nil {
			if aerr := r.appendUnexecuted(calls[i+1:], turn, tools.CodeCanceled, "run canceled after a display failure"); aerr != nil {
				err = errors.Join(err, aerr)
			}
			return r.failDisplay(res, st, err, "failed to display a tool result")
		}

		r.foldSubagent(st, call, result)
		if interrupted {
			if err := r.record(session.TypeInterrupt, st.turns, "", interruptData{
				Operation: operationTool,
				CallID:    call.ID,
				Name:      call.Name,
				Reason:    "interrupted by SIGINT",
			}); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a tool interrupt")
			}
			if err := r.sync(); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to sync the session log")
			}
			if err := r.notice(st.turns, NoticeInterrupt, fmt.Sprintf("interrupted tool call %s (%s)", call.ID, call.Name)); err != nil {
				return r.failDisplay(res, st, err, "failed to display a tool interrupt")
			}
			if err := r.appendUnexecuted(calls[i+1:], turn, tools.CodeCanceled, toolNotStartedMessage); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
			}
			st.failKey = ""
			st.failCount = 0
			return false, nil
		}

		if ctx.Err() != nil {
			if err := r.appendUnexecuted(calls[i+1:], turn, tools.CodeCanceled, canceledBeforeMessage); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
			}
			return r.finishContext(res, st, ctx)
		}

		if result.OK {
			st.failKey = ""
			st.failCount = 0
			continue
		}
		if result.Code == tools.CodePermissionDenied {
			// A policy denial is a deliberate host outcome, not a tool
			// malfunction. It must never accumulate into StopStuck.
			st.failKey = ""
			st.failCount = 0
			continue
		}

		canonical, canonicalErr := canonicalJSONWithout(call.Arguments, tools.IntentArgument)
		key := failureKey(call.Name, canonical, canonicalErr, string(result.Code), call.Arguments)
		if key == st.failKey {
			st.failCount++
		} else {
			st.failKey = key
			st.failCount = 1
		}
		if st.failCount == 3 {
			hint := fmt.Sprintf("The tool %q has failed %d times with the same arguments and result code %q. Stop repeating the same call; change approach, or reply without a tool call if you are blocked.", call.Name, st.failCount, result.Code)
			if err := r.recordUserMessage(turn, hint, sourceStuckHint); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record the stuck hint")
			}
		}
		if st.failCount >= 5 {
			if err := r.appendUnexecuted(calls[i+1:], turn, tools.CodeInternal, "tool call not executed after repeated identical failures"); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
			}
			res.Err = errors.New("repeated identical tool failures")
			return r.finishTerminal(res, st, StopStuck, "repeated identical tool failures")
		}
	}
	return false, nil
}

// executeGuarded applies the host tool policy and then runs the call. A policy
// error is a model-visible permission_denied result; a context cancellation
// returned by the policy interrupts the operation instead. A panic in host
// policy code is recovered and treated as a denial.
func (r *Runner) executeGuarded(ctx context.Context, call model.ToolCall, meta tools.Call) (tools.Result, bool, bool) {
	if err := r.authorize(ctx, call, meta); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return tools.Result{}, false, true
		}
		return tools.Failure(tools.CodePermissionDenied, capText(cleanText(err.Error()), maxErrorEventBytes)), false, false
	}
	result, panicked := safeExecute(ctx, r.tools, call, meta)
	return result, panicked, false
}

// authorize invokes the configured policy hook. The hook is serialized by the
// engine/spawner when delegated children share it; the runner never calls it
// concurrently itself because tool calls in one run are sequential.
func (r *Runner) authorize(ctx context.Context, call model.ToolCall, meta tools.Call) (err error) {
	if r.cfg.ToolPolicy == nil {
		return nil
	}
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("authorize panicked: %v", v)
		}
	}()
	return r.cfg.ToolPolicy(ctx, ToolPolicyRequest{
		Call:    meta,
		Name:    call.Name,
		Args:    call.Arguments,
		Turn:    meta.Turn,
		Session: r.cfg.SessionID,
		Agent:   r.cfg.Agent,
	})
}

func (r *Runner) logToolResult(call model.ToolCall, result tools.Result, payload toolResultData) {
	if r.cfg.Logger == nil {
		return
	}
	r.cfg.Logger.Info("tool result",
		"tool", call.Name,
		"ok", result.OK,
		"code", result.Code,
		"duration_ms", payload.DurationMS,
	)
}

// totalTokens is the parent's cumulative spend plus every delegated child's
// spend. A child cannot be used to escape -max-total-tokens.
func (st *runState) totalTokens() int {
	return st.usage.Total() + st.subagentUsage.Total()
}

// cumulativeUsage is the parent's own usage plus every delegated child's
// usage. It is what a new child subtracts from the budget, so children from
// earlier runs in the same session are not forgotten.
func (st *runState) cumulativeUsage() model.Usage {
	usage := st.usage
	usage.InputTokens += st.subagentUsage.InputTokens
	usage.OutputTokens += st.subagentUsage.OutputTokens
	return usage
}

// foldSubagent adds one delegate result to the run's subagent totals. A
// malformed or foreign details payload is ignored: accounting is advisory and
// must never fail a run.
func (r *Runner) foldSubagent(st *runState, call model.ToolCall, result tools.Result) {
	if call.Name != "delegate" {
		return
	}
	details, ok := tools.ParseDelegateDetails(result.Details)
	if !ok {
		return
	}
	st.subagentTurns += details.SubagentTurns
	st.subagentUsage.InputTokens += details.SubagentUsage.InputTokens
	st.subagentUsage.OutputTokens += details.SubagentUsage.OutputTokens
	st.subagentCost += details.SubagentCostMicroUSD
	if !details.SubagentCostComplete {
		st.subagentCostComplete = false
		st.runCostComplete = false
		st.costComplete = false
	}
}

// finishContext maps context cancellation after an in-flight operation.
func (r *Runner) finishContext(res *Result, st *runState, ctx context.Context) (bool, error) {
	cerr := ctx.Err()
	switch {
	case errors.Is(cerr, context.DeadlineExceeded):
		reason := "context deadline reached"
		if r.cfg.SessionTimeout > 0 {
			observed := time.Since(st.start)
			if err := r.recordLimit(st.turns, limitSessionTimeout, r.cfg.SessionTimeout.Milliseconds(), observed.Milliseconds()); err != nil {
				res.Err = err
				done, ferr := r.finishTerminal(res, st, StopInternalError, "failed to record a limit event")
				return done, ferr
			}
			reason = fmt.Sprintf("session deadline reached (-session-timeout %s, elapsed %s)", r.cfg.SessionTimeout, observed.Round(time.Millisecond))
		}
		done, err := r.finishTerminal(res, st, StopTimeLimit, reason)
		return done, err
	case errors.Is(cerr, context.Canceled):
		done, err := r.finishTerminal(res, st, StopInterrupted, "interrupted by signal")
		return done, err
	default:
		res.Err = errors.New("context stopped without cancellation")
		done, err := r.finishTerminal(res, st, StopInternalError, "context stopped without cancellation")
		return done, err
	}
}

// finishResult sets the terminal fields, emits exactly one final event
// followed by one run_end event, and syncs the log.
func (r *Runner) finishResult(res *Result, st *runState, status StopReason, reason string) (Result, error) {
	res.Status = status
	res.FinalText = st.lastText
	res.RunTurns = st.runTurns
	res.RunUsage = st.runUsage
	res.CumulativeTurns = st.turns
	res.CumulativeUsage = st.usage
	res.ContextTokens = st.contextTokens
	res.RunCostMicroUSD = st.runCost
	res.CostMicroUSD = st.cost
	res.RunCostComplete = st.runCostComplete
	res.CostComplete = st.costComplete
	res.SubagentTurns = st.subagentTurns
	res.SubagentUsage = st.subagentUsage
	res.SubagentCostMicroUSD = st.subagentCost
	res.SubagentCostComplete = st.subagentCostComplete
	if st.finalized {
		return *res, res.Err
	}
	st.finalized = true

	finalErr := r.record(session.TypeFinal, st.turns, "", finalData{
		Status: status,
		Text:   capText(st.lastText, maxEventTextBytes),
	})
	endErr := r.record(session.TypeRunEnd, st.turns, "", runEndData{
		Status:               status,
		Reason:               capText(reason, maxErrorEventBytes),
		RunTurns:             st.runTurns,
		RunUsage:             st.runUsage,
		Turns:                st.turns,
		Usage:                st.usage,
		DurationMS:           time.Since(st.start).Milliseconds(),
		RunCostMicroUSD:      st.runCost,
		CostMicroUSD:         st.cost,
		RunCostComplete:      st.runCostComplete,
		CostComplete:         st.costComplete,
		SubagentTurns:        st.subagentTurns,
		SubagentUsage:        st.subagentUsage,
		SubagentCostMicroUSD: st.subagentCost,
		SubagentCostComplete: st.subagentCostComplete,
	})
	syncErr := r.sync()
	if failures := errors.Join(finalErr, endErr, syncErr); failures != nil {
		res.Status = StopInternalError
		res.Err = errors.Join(res.Err, failures)
		return *res, res.Err
	}
	// A RunFinished display failure is returned to the caller but does not
	// rewrite the already-durable terminal status.
	if err := r.emit(RunFinished{Result: *res, Reason: reason, Duration: time.Since(st.start)}); err != nil {
		res.Err = errors.Join(res.Err, err)
	}
	return *res, res.Err
}

// finishTerminal adapts finishResult for loop paths that report completion
// with a boolean.
func (r *Runner) finishTerminal(res *Result, st *runState, status StopReason, reason string) (bool, error) {
	_, err := r.finishResult(res, st, status, reason)
	return true, err
}

// record appends one producer payload as a session event.
func (r *Runner) record(typ session.EventType, turn int, role string, payload any) error {
	ev, err := session.NewEvent(typ, turn, role, payload)
	if err != nil {
		return fmt.Errorf("build %s event: %w", typ, err)
	}
	if err := r.store.Append(ev); err != nil {
		return fmt.Errorf("append %s event: %w", typ, err)
	}
	return nil
}

func (r *Runner) recordUserMessage(turn int, text, source string) error {
	cappedText := capText(text, maxEventTextBytes)
	cappedSource := capText(source, maxEventTextBytes)
	if err := r.record(session.TypeUserMessage, turn, string(model.RoleUser), userMessageData{
		Text:   cappedText,
		Source: cappedSource,
	}); err != nil {
		return err
	}
	if err := r.sync(); err != nil {
		return err
	}
	return r.emit(MessageRecorded{Turn: turn, Source: cappedSource, Text: cappedText})
}

func (r *Runner) recordToolResult(turn int, payload toolResultData) error {
	if err := r.record(session.TypeToolResult, turn, string(model.RoleTool), payload); err != nil {
		return err
	}
	return r.sync()
}

func (r *Runner) sync() error {
	if err := r.store.Sync(); err != nil {
		return fmt.Errorf("sync session log: %w", err)
	}
	return nil
}

func (r *Runner) recordLimit(turn int, kind string, configured, observed int64) error {
	if err := r.record(session.TypeLimit, turn, "", limitData{Kind: kind, Configured: configured, Observed: observed}); err != nil {
		return err
	}
	return r.notice(turn, NoticeLimit, fmt.Sprintf("limit %s: configured %d, observed %d", kind, configured, observed))
}

func (r *Runner) recordCompaction(turn int, proj Projection, policy string) error {
	if err := r.record(session.TypeCompaction, turn, "", compactionData{
		BeforeTokens:  proj.BeforeTokens,
		AfterTokens:   proj.AfterTokens,
		ElidedResults: proj.ElidedResults,
		Policy:        policy,
	}); err != nil {
		return err
	}
	return r.notice(turn, NoticeCompaction, fmt.Sprintf("context compacted (%s): %d -> %d tokens, %d tool results elided",
		policy, proj.BeforeTokens, proj.AfterTokens, proj.ElidedResults))
}

func (r *Runner) recordError(turn int, class, message string) error {
	return r.recordErrorWithPartial(turn, class, message, "", 0)
}

func (r *Runner) recordErrorWithPartial(turn int, class, message, partial string, partialBytes int) error {
	cappedMessage := capText(message, maxErrorEventBytes)
	if err := r.record(session.TypeError, turn, "", errorData{
		Class:        class,
		Message:      cappedMessage,
		PartialText:  capText(partial, maxPartialTextBytes),
		PartialBytes: partialBytes,
	}); err != nil {
		return err
	}
	return r.notice(turn, NoticeError, cappedMessage)
}

func (r *Runner) recordResponse(comp completion, turn int, costMicroUSD *int64) error {
	return r.record(session.TypeModelResponse, turn, string(model.RoleAssistant), modelResponseData{
		Text:         cleanText(comp.resp.Text),
		ToolCalls:    loggableToolCalls(comp.resp.ToolCalls),
		FinishReason: comp.resp.FinishReason,
		Usage:        comp.resp.Usage,
		CostMicroUSD: costMicroUSD,
		LatencyMS:    comp.latency.Milliseconds(),
		Attempt:      comp.attempt,
		Streamed:     comp.streamed,
	})
}

func validateToolCalls(calls []model.ToolCall) error {
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		if call.ID == "" {
			return errors.New("model response has a tool call with an empty id")
		}
		if seen[call.ID] {
			return fmt.Errorf("model response has duplicate tool call id %q", call.ID)
		}
		seen[call.ID] = true
	}
	return nil
}

// callExecutor is implemented by a toolset that needs the parent tool-call
// identifier and budget. Production wires *tools.Registry; loop tests use the
// smaller toolset interface and fall back to Execute.
type callExecutor interface {
	ExecuteCall(ctx context.Context, call tools.Call, name string, args json.RawMessage) tools.Result
}

func safeExecute(ctx context.Context, ts toolset, call model.ToolCall, meta tools.Call) (result tools.Result, panicked bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicked = true
			message := fmt.Sprintf("tool %q panicked: %v", call.Name, recovered)
			result = tools.Result{
				OK:      false,
				Code:    tools.CodePanic,
				Error:   capText(message, maxPanicMessageBytes),
				Details: panicDetails(),
			}
		}
	}()
	if executor, ok := ts.(callExecutor); ok {
		return executor.ExecuteCall(ctx, meta, call.Name, call.Arguments), false
	}
	result = ts.Execute(ctx, call.Name, call.Arguments)
	return result, false
}

func panicDetails() json.RawMessage {
	stack := capText(string(debug.Stack()), maxPanicStackBytes)
	data, err := jsonv2.Marshal(struct {
		Stack string `json:"stack"`
	}{Stack: stack})
	if err != nil {
		return nil
	}
	return data
}

func normalizeResultCode(result *tools.Result) {
	if result.OK {
		if result.Code == "" {
			result.Code = tools.CodeOK
		}
		return
	}
	if result.Code == "" {
		result.Code = tools.CodeInternal
	}
}

func toolResultEvent(call model.ToolCall, result tools.Result, duration time.Duration) toolResultData {
	durationMS := result.DurationMS
	if durationMS == 0 {
		durationMS = duration.Milliseconds()
	}
	var details json.RawMessage
	if len(result.Details) > 0 {
		details = safeArguments(result.Details)
	}
	return toolResultData{
		CallID:     call.ID,
		Name:       call.Name,
		OK:         result.OK,
		Code:       result.Code,
		Output:     capText(result.Output, maxEventTextBytes),
		Error:      capText(result.Error, maxEventTextBytes),
		ExitCode:   result.ExitCode,
		DurationMS: durationMS,
		Truncated:  result.Truncated,
		TimedOut:   result.TimedOut,
		Details:    details,
	}
}

func toolCompletedEvent(turn int, call model.ToolCall, payload toolResultData) ToolCompleted {
	exitCode := 0
	if payload.ExitCode != nil {
		exitCode = *payload.ExitCode
	}
	return ToolCompleted{
		Turn:      turn,
		CallID:    payload.CallID,
		Name:      payload.Name,
		OK:        payload.OK,
		Code:      payload.Code,
		Output:    payload.Output,
		Error:     payload.Error,
		ExitCode:  exitCode,
		Duration:  time.Duration(payload.DurationMS) * time.Millisecond,
		TimedOut:  payload.TimedOut,
		Truncated: payload.Truncated,
		Details:   payload.Details,
	}
}

func (r *Runner) appendUnexecuted(calls []model.ToolCall, turn int, code tools.Code, message string) error {
	for _, call := range calls {
		payload := toolResultData{
			CallID: call.ID,
			Name:   call.Name,
			OK:     false,
			Code:   code,
			Error:  message,
		}
		if err := r.recordToolResult(turn, payload); err != nil {
			return err
		}
		if err := r.emit(toolCompletedEvent(turn, call, payload)); err != nil {
			return err
		}
	}
	return nil
}

func failureKey(name, canonical string, canonicalErr error, code string, raw json.RawMessage) string {
	if canonicalErr != nil {
		canonical = "raw:" + strings.TrimSpace(string(raw))
	}
	return name + "\x00" + canonical + "\x00" + code
}

func partialFromError(err error) (string, int) {
	if partial, ok := errors.AsType[*model.PartialError](err); ok {
		return capText(partial.Text, maxPartialTextBytes), partial.Bytes
	}
	return "", 0
}

const (
	continuationHint = "Your previous response was truncated by the output length limit. Continue the task from where you left off without repeating completed work, and reply without a tool call when complete."

	interruptHint = "The previous model call was interrupted by the user before it completed. No assistant response from that attempt is in the transcript. Continue the task from the durable transcript, or stop by replying without a tool call if you are blocked."

	repairMessage = "the harness restarted before this tool call returned; its side effect may or may not have occurred; verify the outcome before retrying"

	toolInterruptMessage = "the tool call was interrupted by SIGINT; its side effect may or may not have occurred; verify the outcome before retrying"

	toolNotStartedMessage = "the tool call was not started because the operation was interrupted"

	canceledBeforeMessage = "run canceled before tool execution"
)
