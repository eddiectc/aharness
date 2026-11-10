//go:build unix

package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"aharness/internal/agent"
)

// operation is one in-flight model call or tool execution.
type operation struct {
	cancel context.CancelFunc
}

// interactiveControl routes signals for -interactive: the first effective
// SIGINT cancels only the active operation, and the second SIGINT or any
// SIGTERM cancels the run. The signal goroutine performs no I/O and mutates no
// session state.
type interactiveControl struct {
	mu          sync.Mutex
	runCancel   context.CancelFunc
	active      *operation
	interrupted bool
}

// setupSignals returns the run context, an optional interactive controller,
// and a stop function that stops signal delivery and waits for the handler.
func setupSignals(interactive bool) (context.Context, agent.Controller, func()) {
	if !interactive {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		return ctx, nil, stop
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctl := &interactiveControl{runCancel: cancel}
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctl.handle(ctx, sigs)
	}()
	stop := func() {
		signal.Stop(sigs)
		cancel()
		<-done
	}
	return ctx, ctl, stop
}

func (c *interactiveControl) handle(ctx context.Context, sigs <-chan os.Signal) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig := <-sigs:
			if sig == syscall.SIGTERM {
				c.cancelRun()
				continue
			}
			c.interrupt()
		}
	}
}

// interrupt cancels the active operation once. A SIGINT with no operation in
// flight is ignored; once an effective interrupt has happened, the next SIGINT
// terminates the run.
func (c *interactiveControl) interrupt() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		if c.interrupted {
			c.cancelRunLocked()
			return
		}
		c.interrupted = true
		c.active.cancel()
		return
	}
	if c.interrupted {
		c.cancelRunLocked()
	}
}

func (c *interactiveControl) cancelRun() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelRunLocked()
}

func (c *interactiveControl) cancelRunLocked() {
	if c.runCancel != nil {
		c.runCancel()
	}
}

// BeginOperation implements agent.Controller.
func (c *interactiveControl) BeginOperation(ctx context.Context) (context.Context, func()) {
	opCtx, cancel := context.WithCancel(ctx)
	op := &operation{cancel: cancel}
	c.mu.Lock()
	c.active = op
	c.mu.Unlock()
	return opCtx, func() {
		c.mu.Lock()
		if c.active == op {
			c.active = nil
		}
		c.mu.Unlock()
		cancel()
	}
}
