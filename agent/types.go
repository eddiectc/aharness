// Package agent implements the bounded model/tool loop, context projection and
// compaction, retry policy, streaming display, interrupt/steering control, and
// stop policy.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

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
	// SystemPrompt is the resolved prompt text recorded in session_start. It
	// is authoritative for projection; an empty value renders the configured
	// PromptVersion instead. A child session sets it to the agent's prompt.
	SystemPrompt string
	// AppendSystemPrompt is appended to the rendered or explicit system prompt
	// when session_start is written. It is a new/fork input only; resume
	// inherits the recorded prompt and rejects it.
	AppendSystemPrompt string
	// Agent is the definition name for a child run; empty for a parent.
	Agent string
	// SessionID is the durable session identifier for this run. It is
	// runtime-only and never recorded here; the store owns the recorded ID.
	SessionID string
	// Tools is the recorded tool-name grant. Nil means every tool of the
	// recorded ToolsetVersion; a non-nil list narrows it, and a non-nil empty
	// list means no tools.
	Tools []string
	// RootTools is the recorded root-only grant written to session_start. Nil
	// means the root uses the session grant; a non-nil empty list means the
	// root is offered no tools. It never changes Tools, which remains the
	// authority for child intersection.
	RootTools []string
	// CustomTools are the caller-registered tools available to this run. They
	// are recorded in session_start on a new session and must match the
	// recorded set on resume.
	CustomTools []CustomTool
	// ContractOverride records, on a forked session_start, which parts of the
	// inherited contract the fork replaced. It is nil for every other session.
	ContractOverride *ContractOverride
	// Agents is the catalog rendered into the current parent prompt.
	Agents []AgentInfo

	// ToolPolicy runs before every tool execution, including delegated
	// children. A returned error denies the call with a permission_denied
	// result unless it is a context cancellation, which interrupts the
	// operation.
	ToolPolicy func(ctx context.Context, req ToolPolicyRequest) error

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
	// RequireUsage makes a completed model response without provider usage a
	// protocol error before any tool executes.
	RequireUsage bool
	// Pricing is the effective catalog price snapshot for this run. An
	// unavailable snapshot leaves usage uncharged.
	Pricing model.Pricing
	// ThinkingEffort is the effective reasoning effort for every model call
	// in this run. Empty means the provider/model default.
	ThinkingEffort string

	Interactive bool
	Stream      bool
	// Display receives display-only progress. The runner calls it on its own
	// goroutine; a spawner serializes delegated children's callbacks before
	// forwarding them, so the callback never runs concurrently even while
	// read-only child tasks fan out. It must not block, call the runner, or
	// write the session log. A nil callback disables display events.
	Display func(DisplayEvent) error
	// Steering delivers host steering lines consumed at turn boundaries. A nil
	// channel disables steering. The ID is echoed on MessageRecorded (and the
	// facade's SubagentMessageRecorded for children) so a host can acknowledge
	// the exact line even when text repeats.
	Steering <-chan SteeringLine
	// SteeringSource is the user_message source recorded for each steering
	// line. Empty means "steer"; a delegated child uses "subagent-steer" so its
	// log distinguishes a human line addressed to the child.
	SteeringSource string
	// Controller registers the in-flight operation so an interactive first
	// SIGINT can cancel it. A nil controller disables interactivity.
	Controller Controller

	Logger *slog.Logger
}

// SteeringLine is one host steering submission. IDs are assigned by the host
// and are unique within a run; they are echoed on MessageRecorded.
type SteeringLine struct {
	ID   uint64
	Text string
}

// Controller mediates interactive interruption. BeginOperation returns a
// context canceled only by an interactive interrupt and a cleanup that
// unregisters the operation.
type Controller interface {
	BeginOperation(ctx context.Context) (context.Context, func())
}

// CustomTool is the single internal carrier for a caller tool: its declaration
// and its recorded concurrency-safe claim. Registration, projection, recording,
// and the spawner all read this one value so they cannot disagree.
type CustomTool struct {
	Schema          model.ToolSchema
	ConcurrencySafe bool
}

// ToolPolicyRequest is the policy input for one tool execution. It is
// available for built-in and custom tools, parent and child.
type ToolPolicyRequest struct {
	Call    tools.Call
	Name    string
	Args    json.RawMessage
	Turn    int
	Session string
	Agent   string
	// Path is the absolute, lexically cleaned target of a built-in file tool:
	// read_file, edit_file, or search. It is empty for other tools and when the
	// arguments do not carry a usable path. It is not symlink-resolved; the
	// tool remains responsible for containment and symlink checks.
	Path string
}

// ContractOverride records that a fork replaced part of the inherited
// contract. It describes the new session relative to its parent; the parent
// log is unchanged.
type ContractOverride struct {
	ParentSession string `json:"parent_session"`
	Prompt        bool   `json:"prompt,omitempty"`
	Tools         bool   `json:"tools,omitempty"`
}

// ErrToolMismatch marks a resume or fork whose caller tool set does not match
// the contract recorded in the session.
var ErrToolMismatch = errors.New("tool set does not match the session")

// ErrModelPanic marks a recovered panic in a caller model implementation. The
// facade reports it as an internal failure, not a provider failure.
var ErrModelPanic = errors.New("model panicked")

// ParentRef identifies the parent session and fork point recorded in a new
// forked or delegated session_start. CallID is set only for a delegated child:
// a fork has a parent session and sequence but no call identifier.
type ParentRef struct {
	Session string
	Seq     uint64
	CallID  string
}

// RunOptions describes one process run over a durable session.
type RunOptions struct {
	// Task is the text for this run. TaskSet distinguishes an intentionally
	// omitted task (resume) from an empty task string.
	Task     string
	TaskSet  bool
	TaskFrom string
	// ResumedFromSeq is the last durable sequence number before this run.
	// It is zero for a fresh session. Leave ResumedFromSeqSet false to derive
	// it from the log: zero when the session has no recorded run yet, otherwise
	// the last durable sequence number.
	ResumedFromSeq    uint64
	ResumedFromSeqSet bool
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

// Failure reports whether the status is an unexpected or fatal outcome rather
// than completion, a budget stop, or a host interrupt.
func (s StopReason) Failure() bool {
	switch s {
	case StopCompleted, StopMaxTurns, StopTokenLimit, StopTimeLimit, StopInterrupted:
		return false
	default:
		return true
	}
}

// Label returns a short human-readable status for a host status line.
func (s StopReason) Label() string {
	switch s {
	case StopCompleted:
		return "completed"
	case StopMaxTurns:
		return "max turns reached"
	case StopTokenLimit:
		return "token limit reached"
	case StopTimeLimit:
		return "time limit reached"
	case StopProviderError:
		return "provider error"
	case StopProtocolError:
		return "protocol error"
	case StopStuck:
		return "stuck"
	case StopInterrupted:
		return "interrupted"
	case StopInternalError:
		return "internal error"
	default:
		return fmt.Sprintf("unknown status %q", string(s))
	}
}

// Failure reports whether a notice should use failure styling. Only
// NoticeError is a failure; limits, retries, and interrupts are ordinary
// display outcomes.
func (k NoticeKind) Failure() bool { return k == NoticeError }

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
// ChildOutcome is one delegated task's folded outcome from this run. Children
// contains every task whose delegate details were folded before termination;
// a task canceled before its delegate result returned may be absent.
type ChildOutcome struct {
	Agent         string
	Session       string
	CallID        string
	Intent        string
	Status        string
	Turns         int
	ContextTokens int
	Usage         model.Usage
	CostMicroUSD  int64
	CostComplete  bool
	Error         string
}

// Result is the terminal outcome of one run.
type Result struct {
	Status          StopReason
	FinalText       string
	RunTurns        int
	RunUsage        model.Usage
	CumulativeTurns int
	CumulativeUsage model.Usage
	// ContextTokens is the last model response's input plus output tokens: the
	// context occupancy the next call would start from. CumulativeUsage is a
	// session-cumulative sum over every response and is not comparable to a
	// context window.
	ContextTokens int
	// Cost fields are observational estimates from the run's pricing
	// snapshot. Completeness is false when any response lacked a price.
	RunCostMicroUSD int64
	CostMicroUSD    int64
	RunCostComplete bool
	CostComplete    bool
	// Subagent totals fold every delegated child's spend into this run. Child
	// turns are not added to RunTurns: -max-turns bounds this run's own loop.
	SubagentTurns        int
	SubagentUsage        model.Usage
	SubagentCostMicroUSD int64
	SubagentCostComplete bool
	// Children carries one row per delegate task folded before termination, in
	// delegate array order. It is not a promise that a cancelled call reported
	// every task.
	Children []ChildOutcome
	Err      error
}

// Projection is the model input rebuilt from the event log.
type Projection struct {
	Messages             []model.Message
	Schemas              []model.ToolSchema
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
