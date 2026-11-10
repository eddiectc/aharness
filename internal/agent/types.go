// Package agent implements the bounded model/tool loop, context projection and
// compaction, retry policy, and stop policy.
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

// PromptVersion identifies the v0 system prompt and projection contract.
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

// Config is the runner's immutable session configuration.
type Config struct {
	Provider       string
	Dialect        string
	Model          string
	Workspace      string
	StateDir       string
	HarnessVersion string

	MaxTurns       int
	MaxTotalTokens int

	Limits         Limits
	SessionTimeout time.Duration
	ModelTimeout   time.Duration
	LogRequests    bool

	Logger *slog.Logger
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
	Status    StopReason
	FinalText string
	Turns     int
	Usage     model.Usage
	Err       error
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
