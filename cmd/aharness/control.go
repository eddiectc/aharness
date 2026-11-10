//go:build unix

package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// operation is one in-flight model call or tool execution.
type operation struct {
	cancel context.CancelFunc
}

// runController tracks the active operation for one run so the TUI can
// interrupt only that operation or terminate the whole run. It never performs
// I/O and never mutates session state.
type runController struct {
	mu        sync.Mutex
	runCancel context.CancelFunc
	active    *operation
}

// newRunController returns a controller whose Terminate cancels the run
// context.
func newRunController(runCancel context.CancelFunc) *runController {
	return &runController{runCancel: runCancel}
}

// BeginOperation implements agent.Controller.
func (c *runController) BeginOperation(ctx context.Context) (context.Context, func()) {
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

// Interrupt cancels the in-flight operation, if any, and reports whether one
// was active.
func (c *runController) Interrupt() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil {
		return false
	}
	c.active.cancel()
	return true
}

// Terminate cancels the whole run.
func (c *runController) Terminate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runCancel != nil {
		c.runCancel()
	}
}

// setupSignals returns a batch run context canceled by SIGINT or SIGTERM, and
// a stop function that stops signal delivery and waits for the handler.
func setupSignals() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
