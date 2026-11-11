// Package agent implements the bounded model/tool loop, context projection and
// compaction, retry policy, streaming display, interactive control, and stop
// policy.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

// PromptVersion identifies the v1 system prompt and projection contract. v1
// keeps prompt-v0 unchanged.
const PromptVersion = "prompt-v0"

// Limits is the per-call context budget contract.
type Limits struct {
	MaxContextTokens int `json:"max_context_tokens"`
	MaxOutputTokens  int `json:"max_output_tokens"`
	SafetyMargin     int `json:"safety_margin"`
	MaxInputTokens   int `json:"max_input_tokens"`
	CompactionTarget int `json:"compaction_target"`
}

// SafetyMargin returns the default 5% margin, with a 1,000-token floor.
func SafetyMargin(maxContextTokens int) int {
	margin := max(maxContextTokens/20, 1000)
	return margin
}

// Config is the runner's immutable effective configuration for one run.
type Config struct {
	Provider       string
	Dialect        string
	Model          string
	BaseURL        string
	Workspace      string
	StateDir       string
	PromptVersion  string
	ToolsetVersion string
	HarnessVersion string

	// MaxTurns is the cumulative model-turn budget; 0 means unlimited.
	MaxTurns int
	// MaxTotalTokens is the cumulative input+output token budget; 0 means
	// unlimited.
	MaxTotalTokens int

	Limits Limits
	// SessionTimeout bounds one run's wall clock; 0 means no deadline.
	SessionTimeout time.Duration
	// ModelTimeout bounds one model attempt; 0 means no harness deadline.
	ModelTimeout time.Duration
	LogRequests  bool

	Interactive bool
	Stream      bool
	// Display receives display-only progress on the runner goroutine. It must
	// not block, call the runner, or write the session log. A nil callback
	// disables display events.
	Display func(DisplayEvent) error
	// Steering delivers TUI steering lines consumed at turn boundaries. A nil
	// channel disables steering.
	Steering <-chan string
	// Controller registers the in-flight operation so an interactive first
	// SIGINT can cancel it. A nil controller disables interactivity.
	Controller Controller

	Logger *slog.Logger
}

// Controller mediates interactive interruption. BeginOperation returns a
// context canceled only by an interactive interrupt and a cleanup that
// unregisters the operation.
type Controller interface {
	BeginOperation(ctx context.Context) (context.Context, func())
}

// ParentRef identifies the parent session and fork point recorded in a new
// forked session_start.
type ParentRef struct {
	Session string
	Seq     uint64
}

// RunOptions describes one process run over a durable session.
type RunOptions struct {
	// Task is the text for this run. TaskSet distinguishes an intentionally
	// omitted task (resume) from an empty task string.
	Task     string
	TaskSet  bool
	TaskFrom string
	// ResumedFromSeq is the last durable sequence number before this run.
	// It is zero for a fresh session.
	ResumedFromSeq uint64
}

// StopReason is a typed terminal status.
type StopReason string

const (
	StopCompleted     StopReason = "completed"
	StopMaxTurns      StopReason = "max_turns"
	StopTokenLimit    StopReason = "token_limit"
	StopTimeLimit     StopReason = "time_limit"
	StopProviderError StopReason = "provider_error"
	StopProtocolError StopReason = "protocol_error"
	StopStuck         StopReason = "stuck"
	StopInterrupted   StopReason = "interrupted"
	StopInternalError StopReason = "internal_error"
)

// ExitCode maps a terminal status to the CLI exit code.
func (s StopReason) ExitCode() int {
	switch s {
	case StopCompleted:
		return 0
	case StopInternalError:
		return 1
	case StopMaxTurns, StopTokenLimit, StopTimeLimit:
		return 2
	case StopProviderError:
		return 3
	case StopProtocolError, StopStuck:
		return 4
	case StopInterrupted:
		return 130
	default:
		return 1
	}
}

// Result is the outcome of one run.
type Result struct {
	Status          StopReason
	FinalText       string
	RunTurns        int
	RunUsage        model.Usage
	CumulativeTurns int
	CumulativeUsage model.Usage
	Err             error
}

// Projection is the model input rebuilt from the event log.
type Projection struct {
	Messages             []model.Message
	EstimatedInputTokens int
	BeforeTokens         int
	AfterTokens          int
	ElidedResults        int
}

// ErrProjectionTooLarge means compaction could not fit the input budget.
var ErrProjectionTooLarge = errors.New("projection exceeds input budget")

// errDisplay marks a display-callback failure. It is an internal failure, not
// a provider failure.
var errDisplay = errors.New("display output failed")

// completer, toolset, and eventStore are consumer-side interfaces for loop
// tests. Production wires the concrete adapters, registry, and store.
type completer interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

type toolset interface {
	Schemas() []model.ToolSchema
	Execute(ctx context.Context, name string, args json.RawMessage) tools.Result
}

type eventStore interface {
	Append(session.Event) error
	Sync() error
	Events() []session.Event
}
