package agent

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"aharness/internal/model"
	"aharness/internal/session"
)

// Retry policy constants. The transport attempt budget is the initial call
// plus four retries.
const (
	maxModelAttempts = 5
	retryBaseDelay   = time.Second
	retryMaxDelay    = 30 * time.Second
	maxRetryAfter    = 60 * time.Second
)

// Retry classes recorded in model_retry events.
const (
	retryClassHTTP      = "http"
	retryClassTransport = "transport"
)

// errEventStore marks a failure to append or sync the session log. It must
// terminate the run as internal_error, not as a provider failure.
var errEventStore = errors.New("session event store failure")

// completion is one successful model call.
type completion struct {
	resp     model.Response
	attempt  int
	latency  time.Duration
	streamed bool
	emitted  bool
}

// callModel calls the completer with the transport retry budget. The request
// event is logged once per distinct projection; retries reuse it. A retry is
// allowed only before the first display delta.
func (r *Runner) callModel(ctx context.Context, req model.Request, turn, estimatedInputTokens int) (completion, error) {
	if err := r.recordModelRequest(req, turn, estimatedInputTokens); err != nil {
		return completion{}, fmt.Errorf("%w: %w", errEventStore, err)
	}

	var lastErr error
	for attempt := 1; attempt <= maxModelAttempts; attempt++ {
		if err := r.emit(ModelStarted{Turn: turn, Attempt: attempt}); err != nil {
			return completion{}, err
		}
		resp, latency, emitted, err := r.completeOnce(ctx, req)
		if err == nil {
			return completion{resp: resp, attempt: attempt, latency: latency, streamed: resp.Streamed, emitted: emitted}, nil
		}
		// A canceled operation is never retried. The caller classifies it as
		// an interactive interrupt, a session timeout, or a run signal.
		if ctx.Err() != nil {
			return completion{}, err
		}
		// Display output began; a retry would duplicate stdout. Adapters also
		// wrap this in PartialError, but the emitted flag is the loop's own
		// guard.
		if emitted {
			return completion{}, err
		}
		if errors.Is(err, model.ErrContextOverflow) {
			return completion{}, err
		}
		if !retryableError(err) {
			return completion{}, err
		}
		lastErr = err
		if attempt == maxModelAttempts {
			break
		}

		class, status := retryErrorClass(err)
		delay := r.retryDelay(attempt, err)
		payload := modelRetryData{
			Attempt: attempt,
			Class:   class,
			Status:  status,
			DelayMS: delay.Milliseconds(),
		}
		if err := r.record(session.TypeModelRetry, turn, "", payload); err != nil {
			return completion{}, fmt.Errorf("%w: %w", errEventStore, err)
		}
		if err := r.notice(turn, NoticeRetry, fmt.Sprintf("model call attempt %d failed (%s %d); retrying in %s",
			attempt, class, status, delay)); err != nil {
			return completion{}, err
		}
		if r.cfg.Logger != nil {
			r.cfg.Logger.Warn("retrying model call",
				"attempt", attempt,
				"class", class,
				"status", status,
				"delay", delay,
			)
		}
		if err := sleepContext(ctx, delay); err != nil {
			return completion{}, err
		}
	}
	return completion{}, lastErr
}

// callModelOnce performs one model call without consuming or replenishing the
// transport retry budget. It is used for the provider-overflow compaction
// retry.
func (r *Runner) callModelOnce(ctx context.Context, req model.Request, turn, estimatedInputTokens int) (completion, error) {
	if err := r.recordModelRequest(req, turn, estimatedInputTokens); err != nil {
		return completion{}, fmt.Errorf("%w: %w", errEventStore, err)
	}
	if err := r.emit(ModelStarted{Turn: turn, Attempt: 1}); err != nil {
		return completion{}, err
	}
	resp, latency, emitted, err := r.completeOnce(ctx, req)
	if err != nil {
		return completion{}, err
	}
	return completion{resp: resp, attempt: 1, latency: latency, streamed: resp.Streamed, emitted: emitted}, nil
}

// completeOnce performs one attempt with the per-call timeout. It prefers the
// streaming adapter when -stream is set and falls back to Complete otherwise.
func (r *Runner) completeOnce(ctx context.Context, req model.Request) (model.Response, time.Duration, bool, error) {
	callCtx := ctx
	var cancel context.CancelFunc
	if r.cfg.ModelTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, r.cfg.ModelTimeout)
		defer cancel()
	}
	start := time.Now()
	if r.cfg.Stream {
		if streamer, ok := r.model.(model.StreamCompleter); ok {
			emitted := false
			resp, err := streamer.CompleteStream(callCtx, req, func(delta string) error {
				if delta == "" {
					return nil
				}
				emitted = true
				return r.emit(TextDelta{Text: delta})
			})
			return resp, time.Since(start), emitted, err
		}
	}
	resp, err := r.model.Complete(callCtx, req)
	return resp, time.Since(start), false, err
}

func retryableError(err error) bool {
	// Once display output began, a retry would duplicate stdout and could
	// duplicate tool calls. The adapter wraps those failures in PartialError.
	if _, ok := errors.AsType[*model.PartialError](err); ok {
		return false
	}
	if _, ok := errors.AsType[*model.ProtocolError](err); ok {
		return false
	}
	if httpErr, ok := errors.AsType[*model.HTTPError](err); ok {
		return httpErr.Retryable()
	}
	if _, ok := errors.AsType[*model.TransportError](err); ok {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func retryErrorClass(err error) (string, int) {
	if httpErr, ok := errors.AsType[*model.HTTPError](err); ok {
		return retryClassHTTP, httpErr.StatusCode
	}
	if _, ok := errors.AsType[*model.ProtocolError](err); ok {
		return errorClassProtocol, 0
	}
	return retryClassTransport, 0
}

// retryDelay returns Retry-After when present, capped at 60 seconds, otherwise
// exponential backoff with additive jitter.
func (r *Runner) retryDelay(attempt int, err error) time.Duration {
	var httpErr *model.HTTPError
	if errors.As(err, &httpErr) && (httpErr.RetryAfterSet || httpErr.RetryAfter > 0) {
		delay := min(httpErr.RetryAfter, maxRetryAfter)
		return delay
	}
	delay := retryBaseDelay << (attempt - 1)
	if delay <= 0 || delay > retryMaxDelay {
		delay = retryMaxDelay
	}
	if r.jitter != nil {
		delay = r.jitter(delay)
	}
	return delay
}

// sleepContext waits for d without leaking timers. Cancellation wins over the
// timer, and the caller maps the returned context error.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// addJitter spreads retries by up to 25% to avoid synchronized clients.
func addJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d + rand.N(d/4+1)
}

// recordModelRequest stores the exact projected request when request logging
// is enabled. Payloads over the cap are replaced by a truncation marker.
func (r *Runner) recordModelRequest(req model.Request, turn, estimatedInputTokens int) error {
	if !r.cfg.LogRequests {
		return nil
	}
	data := modelRequestData{
		Model:                req.Model,
		Messages:             loggableMessages(req.Messages),
		Tools:                loggableSchemas(req.Tools),
		MaxOutputTokens:      req.MaxOutputTokens,
		ThinkingEffort:       req.ThinkingEffort,
		EstimatedInputTokens: estimatedInputTokens,
		MessageCount:         len(req.Messages),
	}
	raw, err := jsonv2.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal model_request payload: %w", err)
	}
	if len(raw) > maxModelRequestBytes {
		data = modelRequestData{
			Model:                req.Model,
			MaxOutputTokens:      req.MaxOutputTokens,
			ThinkingEffort:       req.ThinkingEffort,
			EstimatedInputTokens: estimatedInputTokens,
			MessageCount:         len(req.Messages),
			Truncated:            true,
		}
	}
	return r.record(session.TypeModelRequest, turn, "", data)
}

func loggableMessages(messages []model.Message) []model.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]model.Message, len(messages))
	for i, msg := range messages {
		out[i] = model.Message{
			Role:       msg.Role,
			Content:    capText(msg.Content, maxEventTextBytes),
			ToolCallID: capText(msg.ToolCallID, maxEventTextBytes),
			Name:       capText(msg.Name, maxEventTextBytes),
		}
		if len(msg.ToolCalls) > 0 {
			out[i].ToolCalls = make([]model.ToolCall, len(msg.ToolCalls))
			for j, call := range msg.ToolCalls {
				out[i].ToolCalls[j] = model.ToolCall{
					ID:        capText(call.ID, maxEventTextBytes),
					Name:      capText(call.Name, maxEventTextBytes),
					Arguments: safeArguments(call.Arguments),
				}
			}
		}
	}
	return out
}

func loggableSchemas(schemas []model.ToolSchema) []model.ToolSchema {
	if len(schemas) == 0 {
		return nil
	}
	out := make([]model.ToolSchema, len(schemas))
	for i, schema := range schemas {
		out[i] = model.ToolSchema{
			Name:        capText(schema.Name, maxEventTextBytes),
			Description: capText(schema.Description, maxEventTextBytes),
			Parameters:  safeArguments(schema.Parameters),
		}
	}
	return out
}
