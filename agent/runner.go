package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
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

// runState is per-Run mutable state. It never escapes the loop.
type runState struct {
	start            time.Time
	runID            string
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
	children             []ChildOutcome
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

	events := r.store.Events()
	state, err := Inspect(events)
	if err != nil {
		res.Status = StopInternalError
		res.Err = err
		return res, err
	}
	resumedFromSeq := opts.ResumedFromSeq
	if !opts.ResumedFromSeqSet {
		resumedFromSeq = deriveResumedFromSeq(events)
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

	st.runID = session.NewID()
	if err := r.recordRunStart(st.runID, resumedFromSeq); err != nil {
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
		RunID:          st.runID,
		Turn:           st.turns,
		ResumedFromSeq: resumedFromSeq,
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
		if err := r.recordUserMessage(0, opts.Task, opts.TaskFrom, 0); err != nil {
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

// deriveResumedFromSeq returns zero before the first recorded run and the last
// durable sequence after one. A fork copies its parent's run_start events, so
// the copied log's last sequence is the parent's boundary.
func deriveResumedFromSeq(events []session.Event) uint64 {
	for _, ev := range events {
		if ev.Type == session.TypeRunStart {
			return events[len(events)-1].Seq
		}
	}
	return 0
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

func (r *Runner) recordRunStart(runID string, resumedFromSeq uint64) error {
	data := runStartData{
		RunID:            runID,
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
			text := cleanText(line.Text)
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
			if err := r.recordUserMessage(st.turns, text, source, line.ID); err != nil {
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

const (
	continuationHint = "Your previous response was truncated by the output length limit. Continue the task from where you left off without repeating completed work, and reply without a tool call when complete."

	interruptHint = "The previous model call was interrupted by the user before it completed. No assistant response from that attempt is in the transcript. Continue the task from the durable transcript, or stop by replying without a tool call if you are blocked."

	repairMessage = "the harness restarted before this tool call returned; its side effect may or may not have occurred; verify the outcome before retrying"

	toolInterruptMessage = "the tool call was interrupted by SIGINT; its side effect may or may not have occurred; verify the outcome before retrying"

	toolNotStartedMessage = "the tool call was not started because the operation was interrupted"

	canceledBeforeMessage = "run canceled before tool execution"
)
