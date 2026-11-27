package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
)

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
	res.Children = append([]ChildOutcome(nil), st.children...)
	res.Reason = reason
	res.Duration = time.Since(st.start)
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
		DurationMS:           res.Duration.Milliseconds(),
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
	if err := r.emit(RunFinished{RunID: st.runID, Result: *res, Reason: reason, Duration: res.Duration}); err != nil {
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

func (r *Runner) recordUserMessage(turn int, text, source string, id uint64) error {
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
	return r.emit(MessageRecorded{Turn: turn, ID: id, Source: cappedSource, Text: cappedText})
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
