// Package engine is the host-agnostic composition layer shared by the root facade and
// host applications. It owns new/resume/fork resolution, workspace and state
// setup, session_start recording, per-run work directories, and runner wiring.
// It imports no terminal, flag, signal, or os.Exit code.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// Mode selects how Open treats SessionID.
type Mode int

const (
	// ModeNew creates a fresh session.
	ModeNew Mode = iota
	// ModeResume opens an existing session for append.
	ModeResume
	// ModeFork copies an existing session into a new self-contained log.
	ModeFork
)

// ConfigError describes an invalid engine construction input.
type ConfigError struct {
	Field  string
	Detail string
}

func (e *ConfigError) Error() string {
	if e.Field == "" {
		return "aharness: " + e.Detail
	}
	return fmt.Sprintf("aharness: %s: %s", e.Field, e.Detail)
}

func configError(field, format string, args ...any) error {
	return &ConfigError{Field: field, Detail: fmt.Sprintf(format, args...)}
}

// Spec is the resolved construction input for one Host. Public Config and the
// CLI map onto it. Zero model fields inherit the recorded session values on
// resume and fork; zero budgets and limits derive from the model or session.
type Spec struct {
	Workspace string
	StateDir  string
	Mode      Mode
	SessionID string // resume/fork source

	// UserAgent identifies the host on outbound tool requests, for example
	// "acode/1.2.0". It is required. The model completer closure captures the
	// same identity independently; the engine cannot inspect its headers.
	UserAgent string
	// Identity is the host product name used in the parent prompt's identity
	// line and in a delegated child's line, for example "acode". Empty means
	// "aharness". It is trimmed, limited to 64 bytes, and must not contain
	// control characters. It is not recorded, so the host re-supplies it on
	// every construction, including resume and fork.
	Identity string
	// PromptTag attributes the host's prompt template, for example
	// "acode/prompt-v1". It is recorded in session_start beside the SDK
	// PromptVersion and never affects rendering, caps, or replay. Empty means
	// unset; otherwise it is at most 64 bytes of printable ASCII matching
	// [A-Za-z0-9._:/-]+. It is not trimmed. Resume keeps the recorded tag and
	// rejects a different explicit one; Fork prefers an explicit tag and
	// otherwise inherits only when the prompt is unchanged.
	PromptTag string

	Provider string
	Dialect  string
	BaseURL  string
	Model    string
	Agent    string
	Limits   agent.Limits
	Pricing  model.Pricing
	// ThinkingEffort is tri-state: nil inherits/default, a pointer to "" clears
	// the inherited value, and a non-empty pointer sets it.
	ThinkingEffort *string
	Agents         []agent.AgentInfo
	Definitions    []agent.AgentDefinition
	// ResolveModel resolves a delegated child's model. The closure captures the
	// host's model configuration and must use the same host identity that the
	// engine enforces for outbound tool requests; the engine cannot inspect the
	// headers a caller's adapter sends.
	ResolveModel func(modelID, effort string) (agent.ModelResolution, error)

	NewCompleter      func(dialect model.Dialect, baseURL, sessionID string) model.Completer
	NewChildCompleter func(def agent.AgentDefinition, resolution agent.ModelResolution, sessionID string) model.Completer
	NewRegistry       func(ws *tools.Workspace, sessionID, workDir, tmpDir string) (*tools.Registry, error)
	// ObserveChild, when non-nil, registers one live display sink per delegated
	// child. See agent.SpawnerOptions.Observe for the contract.
	ObserveChild   func(agent.ChildObservation) agent.ChildSink
	CustomTools    []agent.CustomTool
	CustomToolsSet bool
	// Grant is the tool-name grant. GrantSet distinguishes "inherit the
	// recorded contract" (resume/fork) from an explicit value; the value itself
	// preserves nil (all tools of the recorded version) versus empty (none).
	Grant    []string
	GrantSet bool
	// RootGrant narrows what the root session is offered and may dispatch this
	// run. Nil keeps the session grant; a non-nil empty grant offers the root
	// no tools. It never widens authority and never changes a child's
	// AgentDefinition.Tools. It is recorded as session_start.root_tools and
	// inherited on resume/fork when nil. Names must be in the session grant;
	// delegate requires Delegation.
	RootGrant []string
	// ToolOptions carries the workspace-independent tool configuration. Work
	// directories are filled per run by the engine.
	ToolOptions tools.Options
	ToolPolicy  func(context.Context, agent.ToolPolicyRequest) error
	// MaxSubagents is the public convention: 0 selects the default of 8 and -1
	// means unlimited. Other negative values are invalid.
	MaxSubagents int

	SystemPrompt    string
	SystemPromptSet bool
	AppendPrompt    string
	AppendPromptSet bool
	PromptVersion   string
	ToolsetVersion  string
	MaxTurns        int
	MaxTotalTokens  int
	RunTimeout      time.Duration
	ModelTimeout    time.Duration
	RequireUsage    bool
	LogRequests     bool
	Version         string
	Logger          *slog.Logger
}

// RunSpec carries the per-run overrides that do not rewrite the session.
type RunSpec struct {
	Model   string
	Dialect string
	BaseURL string
	Limits  agent.Limits
	Pricing model.Pricing
	// ImageInput is the image-input capability of the model chosen for this
	// run. It is applied only when Model is set, which is how a host switches
	// models mid-session: the registry is built per run, and without this the
	// construction-time capability would stay in force. Nil means unknown.
	ImageInput        *bool
	ThinkingEffort    *string
	MaxTurns          int
	MaxTotalTokens    int
	RunTimeout        time.Duration
	ModelTimeout      time.Duration
	NewCompleter      func(dialect model.Dialect, baseURL, sessionID string) model.Completer
	ResolveModel      func(modelID, effort string) (agent.ModelResolution, error)
	NewChildCompleter func(def agent.AgentDefinition, resolution agent.ModelResolution, sessionID string) model.Completer
	// ObserveChild overrides Spec.ObserveChild for this run when non-nil.
	ObserveChild func(agent.ChildObservation) agent.ChildSink
	MaxSubagents *int
	Stream       bool
	Interactive  bool
}

// Host owns one durable session, its workspace, and the resolved contract.
type Host struct {
	spec      Spec
	store     *session.Store
	workspace *tools.Workspace
	workDir   string
	tmpDir    string
	// appendedPrompt is the host text recorded in session_start, carried so a
	// delegated child inherits it and a resumed or forked session keeps it.
	appendedPrompt string
	policy         func(context.Context, agent.ToolPolicyRequest) error
	closed         bool
}

func newHost(spec Spec, store *session.Store, ws *tools.Workspace, appendedPrompt string) *Host {
	workDir := filepath.Join(spec.StateDir, "work", store.ID())
	host := &Host{
		spec:           spec,
		store:          store,
		workspace:      ws,
		workDir:        workDir,
		tmpDir:         filepath.Join(workDir, "tmp"),
		appendedPrompt: appendedPrompt,
	}
	if spec.ToolPolicy != nil {
		var mu sync.Mutex
		host.policy = func(ctx context.Context, req agent.ToolPolicyRequest) error {
			mu.Lock()
			defer mu.Unlock()
			return spec.ToolPolicy(ctx, req)
		}
	}
	return host
}

// ID returns the canonical session identifier.
func (h *Host) ID() string { return h.store.ID() }

// Path returns the session log path.
func (h *Host) Path() string { return h.store.Path() }

// Workspace returns the canonical absolute workspace directory.
func (h *Host) Workspace() string { return h.workspace.Dir() }

// StateDir returns the durable state root.
func (h *Host) StateDir() string { return h.spec.StateDir }

// WorkDir returns the per-session work directory. It is created before each
// run by BuildRunner and removed by Close.
func (h *Host) WorkDir() string { return h.workDir }

// CleanupWork removes the per-session work directory and its TMPDIR. Call it
// only when no run is active; the next BuildRunner recreates the directory.
// It is idempotent and safe after Close.
func (h *Host) CleanupWork() error {
	if err := removeTree(h.workDir); err != nil {
		return fmt.Errorf("cleanup work directory: %w", err)
	}
	return nil
}

// Store exposes the session store to the engine's own runner wiring. It is
// internal wiring: hosts should use State, Snapshot, History, Todo, and
// LastSeq instead of reading or mutating the store.
func (h *Host) Store() *session.Store { return h.store }

// WorkspaceHandle exposes the workspace to the engine's own runner wiring. It
// is internal wiring: hosts should use Workspace and the tool registry instead.
func (h *Host) WorkspaceHandle() *tools.Workspace { return h.workspace }

// History reconstructs the durable transcript of this session.
func (h *Host) History() ([]agent.HistoryItem, error) {
	if h.closed {
		return nil, configError("Agent", "is closed")
	}
	return agent.History(h.store.Events())
}

// Todo returns the checklist recorded by the last successful todo tool result,
// or nil when the session has none.
func (h *Host) Todo() []tools.TodoItem {
	return agent.RestoreTodo(h.store.Events())
}

// LastSeq returns the last durable event sequence number.
func (h *Host) LastSeq() uint64 { return h.store.LastSeq() }

// Spec returns a copy of the resolved construction spec.
func (h *Host) Spec() Spec { return h.spec }

// State reconstructs the durable session state. It is the caller's
// responsibility to ensure no run is active; the facade enforces ErrBusy.
func (h *Host) State() (agent.State, error) {
	if h.closed {
		return agent.State{}, configError("Agent", "is closed")
	}
	return agent.Inspect(h.store.Events())
}

// Snapshot reconstructs the durable session view, including timestamps and the
// first user message.
func (h *Host) Snapshot() (Snapshot, error) {
	if h.closed {
		return Snapshot{}, configError("Agent", "is closed")
	}
	return SnapshotFromEvents(h.store.ID(), h.store.Events())
}

// Close syncs and closes the store, closes the workspace, and removes the
// per-session work directory. It is idempotent.
func (h *Host) Close() error {
	if h.closed {
		return nil
	}
	h.closed = true
	storeErr := h.store.Close()
	wsErr := h.workspace.Close()
	if workErr := removeTree(h.workDir); workErr != nil && h.spec.Logger != nil {
		h.spec.Logger.Warn("cleanup leftover work directory", "path", h.workDir, "error", workErr.Error())
	}
	return errors.Join(storeErr, wsErr)
}

// Engine sentinels map directly onto the public facade errors.
var (
	ErrSessionNotFound  = errors.New("session not found")
	ErrSessionLocked    = errors.New("session is locked")
	ErrInvalidSessionID = errors.New("invalid session id")
)
