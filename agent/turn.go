package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
)

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
	if rerr := r.recordUserMessage(st.turns, interruptHint, sourceInterruptHint, 0); rerr != nil {
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
	cumulativeUsage := st.usage
	cumulativeUsage.InputTokens += comp.resp.Usage.InputTokens
	cumulativeUsage.OutputTokens += comp.resp.Usage.OutputTokens
	contextTokens := st.contextTokens
	if total := comp.resp.Usage.Total(); total > 0 {
		contextTokens = total
	}
	cumulativeCost := st.cost
	costComplete := st.costComplete
	if priced {
		cumulativeCost += cost
	} else {
		costComplete = false
	}
	if err := r.emit(ResponseCompleted{
		Turn:            turn,
		Text:            cleanText(comp.resp.Text),
		Reasoning:       cleanText(comp.resp.Reasoning),
		ToolCalls:       comp.resp.ToolCalls,
		FinishReason:    comp.resp.FinishReason,
		Usage:           comp.resp.Usage,
		Latency:         comp.latency,
		Streamed:        comp.streamed,
		CumulativeTurns: st.turns + 1,
		CumulativeUsage: cumulativeUsage,
		ContextTokens:   contextTokens,
		CostMicroUSD:    cumulativeCost,
		CostComplete:    costComplete,
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
			if err := r.recordUserMessage(turn, continuationHint, sourceContinuationHint, 0); err != nil {
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

func partialFromError(err error) (string, int) {
	if partial, ok := errors.AsType[*model.PartialError](err); ok {
		return capText(partial.Text, maxPartialTextBytes), partial.Bytes
	}
	return "", 0
}
