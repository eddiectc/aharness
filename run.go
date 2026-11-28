package aharness

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/engine"
	"github.com/eddiectc/aharness/model"
)

// RunOptions carries per-run overrides that do not rewrite the session.
type RunOptions struct {
	// Model overrides the agent's model for this run. The spec must be complete.
	Model *ModelConfig
	// ThinkingEffort overrides the effort for this run. nil inherits the
	// agent's current value; "" means the provider default.
	ThinkingEffort *string
	// MaxTurns and MaxTotalTokens override the agent's budgets for this run.
	MaxTurns       int
	MaxTotalTokens int
	// Timeout overrides Config.RunTimeout for this run.
	Timeout time.Duration
	// OnEvent receives every display event that the blocking Run consumes, in
	// order. It is ignored by Start, must not block, and cannot change the run.
	// A blocking consumer should use Start.
	OnEvent func(Event)
}

// Run is one in-flight or terminal run over an Agent's session.
type Run struct {
	agent      *Agent
	ctx        context.Context
	cancel     context.CancelFunc
	stopSends  chan struct{}
	stopOnce   sync.Once
	events     chan Event
	done       chan struct{}
	steering   chan agent.SteeringLine
	steerSeq   atomic.Uint64
	controller *runController
	spawner    *agent.Spawner
	sessionID  string
	task       string
	startedAt  time.Time

	mu              sync.Mutex
	result          Result
	resultSet       bool
	finishedSent    bool
	deliveredResult *Result
	pendingFinished *RunFinished

	eventsTaken atomic.Bool
	rangeActive atomic.Bool
	drainOnce   sync.Once
	closeOnce   sync.Once
}

// Run executes one run to a terminal state and returns the result. It is the
// blocking convenience over Start. The returned error is Result.Err.
func (a *Agent) Run(ctx context.Context, task string, opts RunOptions) (Result, error) {
	run, err := a.start(ctx, task, opts, false)
	if err != nil {
		return Result{Status: StopInternalError, Session: a.ID(), Err: err}, err
	}
	defer func() { _ = run.Close() }()
	var (
		result  Result
		iterErr error
	)
	for ev, err := range run.Events() {
		if err != nil {
			iterErr = err
			continue
		}
		if opts.OnEvent != nil {
			opts.OnEvent(ev)
		}
		if finished, ok := ev.(RunFinished); ok {
			result = finished.Result
		}
	}
	if result.Status == "" {
		if res, ok := run.Result(); ok {
			result = res
		}
	}
	if result.Status == "" {
		result.Status = StopInternalError
		if result.Err == nil {
			if iterErr != nil {
				result.Err = iterErr
			} else {
				result.Err = errors.New("aharness: run produced no result")
			}
		}
	}
	if iterErr != nil && result.Err == nil {
		result.Err = iterErr
	}
	return result, result.Err
}

// Start starts one run and returns a handle. The run begins immediately; its
// event stream applies backpressure, so the caller must consume Events or call
// Close.
func (a *Agent) Start(ctx context.Context, task string, opts RunOptions) (*Run, error) {
	return a.start(ctx, task, opts, true)
}

func (a *Agent) start(ctx context.Context, task string, opts RunOptions, stream bool) (*Run, error) {
	if strings.TrimSpace(task) == "" {
		return nil, ErrEmptyTask
	}
	if opts.MaxTurns < 0 {
		return nil, &ConfigError{Field: "RunOptions.MaxTurns", Detail: "must not be negative"}
	}
	if opts.MaxTotalTokens < 0 {
		return nil, &ConfigError{Field: "RunOptions.MaxTotalTokens", Detail: "must not be negative"}
	}
	if opts.Timeout < 0 {
		return nil, &ConfigError{Field: "RunOptions.Timeout", Detail: "must not be negative"}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrClosed
	}
	if a.active != nil && !a.active.isDone() {
		return nil, ErrBusy
	}
	effective, err := a.effectiveModelForRun(opts)
	if err != nil {
		return nil, err
	}
	limits, err := a.runLimits(effective, opts)
	if err != nil {
		return nil, err
	}
	delegation, err := a.buildDelegation(effective, limits)
	if err != nil {
		return nil, err
	}
	if err := validateEffortValue(opts.ThinkingEffort, effective.Spec); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	controller := &runController{}
	steering := make(chan agent.SteeringLine, 16)
	run := &Run{
		agent:      a,
		ctx:        runCtx,
		cancel:     cancel,
		stopSends:  make(chan struct{}),
		events:     make(chan Event),
		done:       make(chan struct{}),
		steering:   steering,
		controller: controller,
		sessionID:  a.ID(),
		task:       task,
		startedAt:  time.Now(),
	}
	display := func(ev agent.DisplayEvent) error {
		pub, ok := eventFromInternal(ev, run.sessionID, time.Now())
		if !ok {
			return nil
		}
		run.deliver(pub)
		return nil
	}

	runSpec := engine.RunSpec{
		ThinkingEffort: opts.ThinkingEffort,
		MaxTurns:       opts.MaxTurns,
		MaxTotalTokens: opts.MaxTotalTokens,
		RunTimeout:     opts.Timeout,
		Stream:         stream,
		NewCompleter: func(dialect model.Dialect, baseURL, sessionID string) model.Completer {
			return newModelAdapter(modelForSession(effective, sessionID))
		},
	}
	if opts.Model != nil {
		runSpec.Model = effective.Spec.ID
		runSpec.Dialect = string(effective.Spec.Dialect)
		runSpec.BaseURL = effective.Spec.BaseURL
		runSpec.Limits = toInternalLimits(limits)
		runSpec.Pricing = toInternalPricing(effective.Spec.Pricing)
		runSpec.ImageInput = effective.Spec.ImageInput
	}
	if delegation != nil {
		runSpec.ResolveModel = delegation.internalResolver()
		runSpec.NewChildCompleter = delegation.internalChildCompleter()
		runSpec.ObserveChild = delegation.internalObserve()
		maxSubagents := delegation.maxSubagents
		runSpec.MaxSubagents = &maxSubagents
	}
	runner, spawner, err := a.host.BuildRunnerWithSpawner(runSpec, display, steering, controller)
	if err != nil {
		cancel()
		return nil, publicError(err)
	}
	run.spawner = spawner
	a.active = run
	go run.execute(runner)
	return run, nil
}

// execute drives the internal runner and closes the event stream after the
// result is set.
func (r *Run) execute(runner *agent.Runner) {
	var (
		result agent.Result
		err    error
	)
	func() {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("aharness: run panicked: %v", v)
				result = agent.Result{Status: agent.StopInternalError, Err: err}
			}
		}()
		result, err = runner.Run(r.ctx, agent.RunOptions{Task: r.task, TaskSet: true})
	}()
	public := fromInternalResult(result, r.sessionID, publicError(err))
	r.mu.Lock()
	delivered := r.deliveredResult
	sent := r.finishedSent
	r.mu.Unlock()
	if delivered != nil {
		public = *delivered
	}
	if public.Status == "" {
		public.Status = StopInternalError
		if public.Err == nil {
			public.Err = err
		}
		if public.Err == nil {
			public.Err = errors.New("aharness: run produced no result")
		}
	}
	if !sent {
		finished := RunFinished{Time: time.Now(), Session: r.sessionID, Result: public, Reason: "", Duration: time.Since(r.startedAt)}
		if !r.deliver(finished) {
			r.mu.Lock()
			r.pendingFinished = &finished
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	r.result = public
	r.resultSet = true
	r.mu.Unlock()
	close(r.done)
	close(r.events)
}

// deliver sends one event with backpressure. It reports whether the event was
// handed to a consumer. A canceled run or a closed receive side drops the
// event; the terminal RunFinished is retained for the iterator in that case.
func (r *Run) deliver(ev Event) bool {
	select {
	case r.events <- ev:
		r.markFinished(ev)
		return true
	default:
	}
	select {
	case r.events <- ev:
		r.markFinished(ev)
		return true
	case <-r.ctx.Done():
		return false
	case <-r.stopSends:
		return false
	}
}

func (r *Run) markFinished(ev Event) {
	finished, ok := ev.(RunFinished)
	if !ok {
		return
	}
	r.mu.Lock()
	r.finishedSent = true
	result := finished.Result
	r.deliveredResult = &result
	r.mu.Unlock()
}

// Events returns the run's event stream. It is single-use; a second call yields
// ErrIteratorConsumed. Breaking out of the range cancels the run and drains it.
func (r *Run) Events() iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if !r.eventsTaken.CompareAndSwap(false, true) {
			yield(nil, ErrIteratorConsumed)
			return
		}
		r.rangeActive.Store(true)
		defer r.rangeActive.Store(false)
		complete := false
		defer func() {
			if !complete {
				r.stopAndDrain()
			}
		}()
		for ev := range r.events {
			if !yield(ev, nil) {
				return
			}
		}
		r.mu.Lock()
		pending := r.pendingFinished
		r.pendingFinished = nil
		resultSet := r.resultSet
		r.mu.Unlock()
		if pending != nil {
			if !yield(*pending, nil) {
				return
			}
		}
		if !resultSet {
			yield(nil, errors.New("aharness: run produced no result"))
			return
		}
		complete = true
	}
}

// Interrupt cancels the in-flight model or tool operation only; the run
// continues and records an interrupt hint.
func (r *Run) Interrupt() { r.controller.Interrupt() }

// Cancel terminates the run with StopInterrupted.
func (r *Run) Cancel() { r.cancel() }

// Steer enqueues a user message consumed at the next turn boundary and returns
// its run-unique steering ID. It never blocks. A full queue returns
// ErrSteeringFull and a finished run ErrClosed.
func (r *Run) Steer(text string) (uint64, error) {
	if r.isDone() {
		return 0, ErrClosed
	}
	line := agent.SteeringLine{ID: r.steerSeq.Add(1), Text: text}
	select {
	case r.steering <- line:
		return line.ID, nil
	default:
		return 0, ErrSteeringFull
	}
}

// SteerChild enqueues steering for a live delegated child of this run and
// returns its run-unique steering ID.
func (r *Run) SteerChild(sessionID, text string) (uint64, error) {
	if r.spawner == nil {
		return 0, ErrNoSuchChild
	}
	line := agent.SteeringLine{ID: r.steerSeq.Add(1), Text: text}
	err := r.spawner.SteerChild(sessionID, line)
	switch {
	case err == nil:
		return line.ID, nil
	case errors.Is(err, agent.ErrSubagentNotRunning):
		return 0, ErrNoSuchChild
	case errors.Is(err, agent.ErrSubagentSteeringFull):
		return 0, ErrSteeringFull
	default:
		return 0, err
	}
}

// Done closes when the run reaches a terminal state.
func (r *Run) Done() <-chan struct{} { return r.done }

// Result returns the terminal result once Done is closed.
func (r *Run) Result() (Result, bool) {
	select {
	case <-r.done:
	default:
		return Result{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.resultSet {
		return Result{}, false
	}
	return r.result, true
}

// Close cancels, drains, and waits for the run. It is idempotent and may be
// called without consuming Events.
func (r *Run) Close() error {
	r.closeOnce.Do(func() {
		r.eventsTaken.Store(true)
		r.stopOnce.Do(func() { close(r.stopSends) })
		r.cancel()
		if !r.rangeActive.Load() {
			r.drain()
		} else {
			<-r.done
		}
	})
	return nil
}

// stopAndDrain is the iterator's early-exit path.
func (r *Run) stopAndDrain() {
	r.stopOnce.Do(func() { close(r.stopSends) })
	r.cancel()
	r.drain()
}

func (r *Run) drain() {
	r.drainOnce.Do(func() {
		for range r.events {
		}
	})
	<-r.done
}

// runController implements agent.Controller for public Interrupt.
type runController struct {
	mu      sync.Mutex
	current *operation
}

type operation struct {
	cancel context.CancelFunc
}

func (c *runController) BeginOperation(ctx context.Context) (context.Context, func()) {
	opCtx, cancel := context.WithCancel(ctx)
	op := &operation{cancel: cancel}
	c.mu.Lock()
	c.current = op
	c.mu.Unlock()
	return opCtx, func() {
		c.mu.Lock()
		if c.current == op {
			c.current = nil
		}
		c.mu.Unlock()
		cancel()
	}
}

func (c *runController) Interrupt() {
	c.mu.Lock()
	op := c.current
	c.mu.Unlock()
	if op != nil {
		op.cancel()
	}
}
