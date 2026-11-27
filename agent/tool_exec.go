package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

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
		meta := tools.Call{
			ID:          call.ID,
			Turn:        turn,
			ParentUsage: st.cumulativeUsage(),
			Session:     r.cfg.SessionID,
			Agent:       r.cfg.Agent,
		}
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
			if err := r.recordUserMessage(turn, hint, sourceStuckHint, 0); err != nil {
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
		Path:    tools.FileArgumentPath(r.cfg.Workspace, call.Name, call.Arguments),
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
	for _, task := range details.Tasks {
		outcome := ChildOutcome{
			Agent:         task.Agent,
			Session:       task.Session,
			CallID:        call.ID,
			Intent:        task.Intent,
			Status:        task.Status,
			Turns:         task.Turns,
			ContextTokens: task.ContextTokens,
			Usage:         task.Usage,
			CostMicroUSD:  task.CostMicroUSD,
			CostComplete:  task.CostComplete,
			Error:         task.Message,
		}
		if outcome.Error == "" && childFailureStatus(task.Status) {
			outcome.Error = result.Error
		}
		st.children = append(st.children, outcome)
	}
}

// childFailureStatus reports whether a task status is a failure rather than a
// normal or limit stop. The aggregate delegate error is a useful fallback only
// for these statuses.
func childFailureStatus(status string) bool {
	switch status {
	case tools.SubagentCompleted, tools.SubagentMaxTurns, tools.SubagentTokenLimit, tools.SubagentTimeLimit, tools.SubagentInterrupted:
		return false
	default:
		return true
	}
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
		Images:     loggableImages(result.Images),
		Details:    details,
	}
}

// loggableImages encodes tool images for the session log. An image that cannot
// be recorded is dropped rather than written as a broken payload: the text
// caption still tells the model what the tool returned.
func loggableImages(images []tools.Image) []toolImageData {
	if len(images) == 0 {
		return nil
	}
	out := make([]toolImageData, 0, len(images))
	for _, img := range images {
		if len(img.Data) == 0 || img.Width <= 0 || img.Height <= 0 {
			continue
		}
		if img.MediaType != model.MediaTypePNG && img.MediaType != model.MediaTypeJPEG {
			continue
		}
		out = append(out, toolImageData{
			MediaType: img.MediaType,
			Data:      base64.StdEncoding.EncodeToString(img.Data),
			Width:     img.Width,
			Height:    img.Height,
			Bytes:     len(img.Data),
			SHA256:    img.SHA256,
		})
	}
	return out
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
