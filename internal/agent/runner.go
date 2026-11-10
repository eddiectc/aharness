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

	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

// Runner owns one bounded model/tool loop.
type Runner struct {
	cfg    Config
	model  completer
	tools  toolset
	store  eventStore
	jitter func(time.Duration) time.Duration
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

// runState is per-Run mutable state. It never escapes the loop.
type runState struct {
	start            time.Time
	turns            int
	usage            model.Usage
	lastText         string
	lastElided       int
	compactionTarget int
	lengthStreak     int
	failKey          string
	failCount        int
	finalized        bool
}

// Run executes one task to a terminal state. Every initialized session emits
// exactly one final event followed by one session_end event.
func (r *Runner) Run(ctx context.Context, task, source string) (Result, error) {
	st := &runState{start: time.Now()}
	res := Result{Status: StopCompleted}

	runCtx := ctx
	var cancel context.CancelFunc
	if r.cfg.SessionTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, r.cfg.SessionTimeout)
		defer cancel()
	}

	startData := sessionStartData{
		PromptVersion:  PromptVersion,
		ToolsetVersion: tools.Version,
		Workspace:      r.cfg.Workspace,
		StateDir:       r.cfg.StateDir,
		Provider:       r.cfg.Provider,
		Dialect:        r.cfg.Dialect,
		Model:          r.cfg.Model,
		Limits:         r.cfg.Limits,
		HarnessVersion: r.cfg.HarnessVersion,
	}
	if err := r.record(session.TypeSessionStart, 0, "", startData); err != nil {
		res.Status = StopInternalError
		res.Err = err
		return res, err
	}
	if err := r.record(session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{
		Text:   cleanText(task),
		Source: capText(source, maxEventTextBytes),
	}); err != nil {
		res.Err = err
		return r.finishResult(&res, st, StopInternalError, "failed to record the task")
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

		limits := r.currentLimits(st)
		proj, err := Project(r.store.Events(), limits)
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

// currentLimits applies a tightened compaction target after a provider
// context-overflow retry, so elision remains monotonic for the session.
func (r *Runner) currentLimits(st *runState) Limits {
	limits := r.cfg.Limits
	if st.compactionTarget > 0 && (limits.CompactionTarget <= 0 || st.compactionTarget < limits.CompactionTarget) {
		limits.CompactionTarget = st.compactionTarget
	}
	return limits
}

// checkLimits enforces the turn, total-token, and wall-clock budgets before a
// model call. A non-empty status means the run must terminate.
func (r *Runner) checkLimits(ctx context.Context, st *runState) (StopReason, string, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			configured := int64(0)
			if r.cfg.SessionTimeout > 0 {
				configured = r.cfg.SessionTimeout.Milliseconds()
			}
			if rerr := r.recordLimit(st.turns, limitSessionTimeout, configured, time.Since(st.start).Milliseconds()); rerr != nil {
				return StopInternalError, "", rerr
			}
			return StopTimeLimit, "session deadline reached", nil
		}
		return StopInterrupted, "interrupted by signal", nil
	}
	if r.cfg.MaxTurns > 0 && st.turns >= r.cfg.MaxTurns {
		if err := r.recordLimit(st.turns, limitMaxTurns, int64(r.cfg.MaxTurns), int64(st.turns)); err != nil {
			return StopInternalError, "", err
		}
		return StopMaxTurns, fmt.Sprintf("turn limit reached (%d)", r.cfg.MaxTurns), nil
	}
	if r.cfg.SessionTimeout > 0 && time.Since(st.start) >= r.cfg.SessionTimeout {
		observed := time.Since(st.start).Milliseconds()
		if err := r.recordLimit(st.turns, limitSessionTimeout, r.cfg.SessionTimeout.Milliseconds(), observed); err != nil {
			return StopInternalError, "", err
		}
		return StopTimeLimit, "session deadline reached", nil
	}
	if r.cfg.MaxTotalTokens > 0 && st.usage.Total() >= r.cfg.MaxTotalTokens {
		if err := r.recordLimit(st.turns, limitMaxTotalTokens, int64(r.cfg.MaxTotalTokens), int64(st.usage.Total())); err != nil {
			return StopInternalError, "", err
		}
		return StopTokenLimit, "total token budget reached", nil
	}
	return "", "", nil
}

// executeTurn performs one model call and any resulting tool calls. It reports
// done when the run reached a terminal state and the final events were written.
func (r *Runner) executeTurn(ctx context.Context, res *Result, st *runState, proj Projection, turn int) (bool, error) {
	req := model.Request{
		Model:           r.cfg.Model,
		Messages:        proj.Messages,
		Tools:           r.tools.Schemas(),
		MaxOutputTokens: r.cfg.Limits.MaxOutputTokens,
	}
	comp, err := r.callModel(ctx, req, turn, proj.EstimatedInputTokens)
	if err != nil {
		if errors.Is(err, errEventStore) {
			res.Err = err
			return r.finishTerminal(res, st, StopInternalError, "failed to record a model event")
		}
		if ctx.Err() != nil {
			return r.finishContext(res, st, ctx)
		}
		if errors.Is(err, model.ErrContextOverflow) {
			var done bool
			comp, done, err = r.overflowRetry(ctx, res, st, proj, turn)
			if done {
				return true, err
			}
		} else {
			return r.failModel(res, st, err)
		}
	}
	return r.processCompletion(ctx, res, st, comp, turn)
}

// overflowRetry rebuilds the projection against a target 10% below the
// per-call input limit and makes one model call outside the transport retry
// budget.
func (r *Runner) overflowRetry(ctx context.Context, res *Result, st *runState, failed Projection, turn int) (completion, bool, error) {
	target := max(r.cfg.Limits.MaxInputTokens*9/10, 1)
	limits := r.currentLimits(st)
	limits.CompactionTarget = target
	next, err := Project(r.store.Events(), limits)
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
		Tools:           r.tools.Schemas(),
		MaxOutputTokens: r.cfg.Limits.MaxOutputTokens,
	}
	comp, err := r.callModelOnce(ctx, req, turn, next.EstimatedInputTokens)
	if err != nil {
		if errors.Is(err, errEventStore) {
			res.Err = err
			done, ferr := r.finishTerminal(res, st, StopInternalError, "failed to record a model event")
			return completion{}, done, ferr
		}
		if ctx.Err() != nil {
			done, ferr := r.finishContext(res, st, ctx)
			return completion{}, done, ferr
		}
		if eerr := r.recordError(st.turns, errorClassOverflow, fmt.Sprintf("context overflow retry failed: %v", err)); eerr != nil {
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
	if _, ok := errors.AsType[*model.ProtocolError](err); ok {
		status = StopProtocolError
		class = errorClassProtocol
	}
	if rerr := r.recordError(st.turns, class, err.Error()); rerr != nil {
		res.Err = rerr
		return r.finishTerminal(res, st, StopInternalError, "failed to record an error event")
	}
	res.Err = fmt.Errorf("model call failed: %w", err)
	reason := fmt.Sprintf("model call failed: %v", err)
	return r.finishTerminal(res, st, status, reason)
}

// processCompletion handles one successful model response.
func (r *Runner) processCompletion(ctx context.Context, res *Result, st *runState, comp completion, turn int) (bool, error) {
	if err := r.recordResponse(comp, turn); err != nil {
		res.Err = err
		return r.finishTerminal(res, st, StopInternalError, "failed to record the model response")
	}
	st.usage.InputTokens += comp.resp.Usage.InputTokens
	st.usage.OutputTokens += comp.resp.Usage.OutputTokens
	if comp.resp.Text != "" {
		st.lastText = comp.resp.Text
	}
	st.turns++
	if r.cfg.Logger != nil {
		r.cfg.Logger.Info("model response",
			"turn", st.turns,
			"attempt", comp.attempt,
			"finish_reason", comp.resp.FinishReason,
			"tool_calls", len(comp.resp.ToolCalls),
			"latency_ms", comp.latency.Milliseconds(),
		)
	}

	if len(comp.resp.ToolCalls) == 0 {
		switch comp.resp.FinishReason {
		case model.FinishStop, model.FinishReason(""):
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
			if err := r.record(session.TypeUserMessage, turn, string(model.RoleUser), userMessageData{
				Text:   continuationHint,
				Source: sourceContinuationHint,
			}); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record the continuation hint")
			}
			if err := r.sync(); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to sync the session log")
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
			if err := r.appendUnexecuted(calls[i:], turn, tools.CodeCanceled, "run canceled before tool execution"); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
			}
			return r.finishContext(res, st, ctx)
		}

		started := time.Now()
		result, panicked := safeExecute(ctx, r.tools, call)
		duration := time.Since(started)
		normalizeResultCode(&result)
		payload := toolResultEvent(call, result, duration)
		if err := r.record(session.TypeToolResult, turn, string(model.RoleTool), payload); err != nil {
			res.Err = err
			return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
		}
		if r.cfg.Logger != nil {
			r.cfg.Logger.Info("tool result",
				"tool", call.Name,
				"ok", result.OK,
				"code", result.Code,
				"duration_ms", payload.DurationMS,
			)
		}

		if panicked || result.Code == tools.CodePanic {
			if err := r.appendUnexecuted(calls[i+1:], turn, tools.CodeInternal, "tool call not executed after a recovered panic"); err != nil {
				res.Err = err
				return r.finishTerminal(res, st, StopInternalError, "failed to record a tool result")
			}
			res.Err = fmt.Errorf("tool %q panicked: %s", call.Name, result.Error)
			return r.finishTerminal(res, st, StopInternalError, "recovered tool panic")
		}

		if result.OK {
			st.failKey = ""
			st.failCount = 0
			continue
		}

		canonical, canonicalErr := canonicalJSON(call.Arguments)
		key := failureKey(call.Name, canonical, canonicalErr, string(result.Code), call.Arguments)
		if key == st.failKey {
			st.failCount++
		} else {
			st.failKey = key
			st.failCount = 1
		}
		if st.failCount == 3 {
			hint := fmt.Sprintf("The tool %q has failed %d times with the same arguments and result code %q. Stop repeating the same call; change approach, or reply without a tool call if you are blocked.", call.Name, st.failCount, result.Code)
			if err := r.recordStuckHint(turn, hint); err != nil {
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

	if err := r.sync(); err != nil {
		res.Err = err
		return r.finishTerminal(res, st, StopInternalError, "failed to sync the session log")
	}
	return false, nil
}

// finishContext maps context cancellation after an in-flight operation.
func (r *Runner) finishContext(res *Result, st *runState, ctx context.Context) (bool, error) {
	cerr := ctx.Err()
	switch {
	case errors.Is(cerr, context.DeadlineExceeded):
		configured := int64(0)
		if r.cfg.SessionTimeout > 0 {
			configured = r.cfg.SessionTimeout.Milliseconds()
		}
		if err := r.recordLimit(st.turns, limitSessionTimeout, configured, time.Since(st.start).Milliseconds()); err != nil {
			res.Err = err
			done, ferr := r.finishTerminal(res, st, StopInternalError, "failed to record a limit event")
			return done, ferr
		}
		done, err := r.finishTerminal(res, st, StopTimeLimit, "session deadline reached")
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
// followed by one session_end event, and syncs the log.
func (r *Runner) finishResult(res *Result, st *runState, status StopReason, reason string) (Result, error) {
	res.Status = status
	res.Turns = st.turns
	res.Usage = st.usage
	res.FinalText = st.lastText
	if st.finalized {
		return *res, res.Err
	}
	st.finalized = true

	finalErr := r.record(session.TypeFinal, st.turns, "", finalData{
		Status: status,
		Text:   capText(st.lastText, maxEventTextBytes),
	})
	endErr := r.record(session.TypeSessionEnd, st.turns, "", sessionEndData{
		Status:     status,
		Reason:     capText(reason, maxErrorEventBytes),
		Turns:      st.turns,
		Usage:      st.usage,
		DurationMS: time.Since(st.start).Milliseconds(),
	})
	syncErr := r.sync()
	if failures := errors.Join(finalErr, endErr, syncErr); failures != nil {
		res.Status = StopInternalError
		res.Err = errors.Join(res.Err, failures)
		return *res, res.Err
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

func (r *Runner) sync() error {
	if err := r.store.Sync(); err != nil {
		return fmt.Errorf("sync session log: %w", err)
	}
	return nil
}

func (r *Runner) recordLimit(turn int, kind string, configured, observed int64) error {
	return r.record(session.TypeLimit, turn, "", limitData{Kind: kind, Configured: configured, Observed: observed})
}

func (r *Runner) recordCompaction(turn int, proj Projection, policy string) error {
	return r.record(session.TypeCompaction, turn, "", compactionData{
		BeforeTokens:  proj.BeforeTokens,
		AfterTokens:   proj.AfterTokens,
		ElidedResults: proj.ElidedResults,
		Policy:        policy,
	})
}

func (r *Runner) recordError(turn int, class, message string) error {
	return r.record(session.TypeError, turn, "", errorData{
		Class:   class,
		Message: capText(message, maxErrorEventBytes),
	})
}

func (r *Runner) recordStuckHint(turn int, text string) error {
	return r.record(session.TypeUserMessage, turn, string(model.RoleUser), userMessageData{
		Text:   capText(text, maxErrorEventBytes),
		Source: sourceStuckHint,
	})
}

func (r *Runner) recordResponse(comp completion, turn int) error {
	return r.record(session.TypeModelResponse, turn, string(model.RoleAssistant), modelResponseData{
		Text:         capText(comp.resp.Text, maxEventTextBytes),
		ToolCalls:    loggableToolCalls(comp.resp.ToolCalls),
		FinishReason: comp.resp.FinishReason,
		Usage:        comp.resp.Usage,
		LatencyMS:    comp.latency.Milliseconds(),
		Attempt:      comp.attempt,
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

func safeExecute(ctx context.Context, ts toolset, call model.ToolCall) (result tools.Result, panicked bool) {
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

func (r *Runner) appendUnexecuted(calls []model.ToolCall, turn int, code tools.Code, message string) error {
	for _, call := range calls {
		payload := toolResultData{
			CallID: call.ID,
			Name:   call.Name,
			OK:     false,
			Code:   code,
			Error:  message,
		}
		if err := r.record(session.TypeToolResult, turn, string(model.RoleTool), payload); err != nil {
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

const continuationHint = "Your previous response was truncated by the output length limit. Continue the task from where you left off without repeating completed work, and reply without a tool call when complete."
