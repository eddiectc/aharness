package aharness

import (
	"context"
	"encoding/json"
	"time"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/engine"
)

// StopReason is the typed terminal status of one run.
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
func (s StopReason) Failure() bool { return agent.StopReason(s).Failure() }

// Label returns a short human-readable status.
func (s StopReason) Label() string { return agent.StopReason(s).Label() }

// Failure reports whether a notice should use failure styling.
func (k NoticeKind) Failure() bool { return agent.NoticeKind(k).Failure() }

// Result is the authoritative outcome of one run.
type Result struct {
	Status          StopReason `json:"status"`
	Session         string     `json:"session,omitempty"`
	Err             error      `json:"-"`
	FinalText       string     `json:"final_text,omitempty"`
	RunTurns        int        `json:"run_turns,omitempty"`
	RunUsage        Usage      `json:"run_usage,omitempty"`
	CumulativeTurns int        `json:"cumulative_turns,omitempty"`
	CumulativeUsage Usage      `json:"cumulative_usage,omitempty"`
	ContextTokens   int        `json:"context_tokens,omitempty"`
	RunCostMicroUSD int64      `json:"run_cost_micro_usd,omitempty"`
	CostMicroUSD    int64      `json:"cost_micro_usd,omitempty"`
	RunCostComplete bool       `json:"run_cost_complete,omitempty"`
	CostComplete    bool       `json:"cost_complete,omitempty"`
	// Reason is the terminal explanation; Duration is the run's wall-clock
	// time. Both match RunFinished.
	Reason   string        `json:"reason,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`

	SubagentTurns        int   `json:"subagent_turns,omitempty"`
	SubagentUsage        Usage `json:"subagent_usage,omitempty"`
	SubagentCostMicroUSD int64 `json:"subagent_cost_micro_usd,omitempty"`
	SubagentCostComplete bool  `json:"subagent_cost_complete,omitempty"`
	// Children carries one row per delegated task folded before the run
	// terminated, in delegate array order. A task canceled before its delegate
	// result returned may be absent; SubagentFinished covers the pre-start
	// case.
	Children []ChildOutcome `json:"children,omitempty"`
}

// ChildOutcome is one delegated task's folded outcome from a run.
type ChildOutcome struct {
	Agent         string `json:"agent,omitempty"`
	Session       string `json:"session,omitempty"`
	CallID        string `json:"call_id,omitempty"`
	Intent        string `json:"intent,omitempty"`
	Status        string `json:"status,omitempty"`
	Turns         int    `json:"turns,omitempty"`
	ContextTokens int    `json:"context_tokens,omitempty"`
	Usage         Usage  `json:"usage,omitempty"`
	CostMicroUSD  int64  `json:"cost_micro_usd,omitempty"`
	CostComplete  bool   `json:"cost_complete,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Limits is the per-call context budget.
type Limits struct {
	MaxContextTokens int
	MaxOutputTokens  int
	SafetyMargin     int
	MaxInputTokens   int
	CompactionTarget int
}

// PendingCall is a model tool call with no durable tool_result.
type PendingCall struct {
	CallID string
	Name   string
	Turn   int
}

// ParentRef identifies a fork or delegated-child parent session.
type ParentRef struct {
	Session string
	Seq     uint64
	CallID  string
}

// ContractOverride records that a Fork replaced part of the inherited
// contract.
type ContractOverride struct {
	ParentSession string
	Prompt        bool
	Tools         bool
}

// State is the durable session state reconstructed from the log.
type State struct {
	ID           string
	Workspace    string
	StateDir     string
	Model        ModelSpec
	AgentName    string
	SystemPrompt string
	// AppendedPrompt is the host text recorded as session_start.appended_prompt
	// beside the resolved prompt. Empty means unset, including for logs written
	// before the field and for delegated children. It is provenance only and
	// never a replay key.
	AppendedPrompt string
	// PromptCapped reports whether the recorded system prompt was truncated by
	// the SDK's 32 KiB prompt cap. It is derived from the recorded text, so it
	// is correct for logs written before this field: false for a legacy log with
	// no recorded prompt and false for a prompt that fits. It is never a replay
	// key, and a host never matches the SDK's truncation marker itself.
	PromptCapped bool
	// PromptTag is the host prompt tag recorded beside the SDK prompt template
	// version. Empty means unset. It is attribution only and never a replay
	// key.
	PromptTag        string
	Tools            []ToolName
	RootTools        []ToolName
	CustomTools      []ToolSchema
	ConcurrencySafe  []ToolName
	ContractOverride *ContractOverride
	Limits           Limits
	Turns            int
	Usage            Usage
	ContextTokens    int
	// Subagent totals are folded from every recorded delegate result.
	SubagentTurns        int
	SubagentUsage        Usage
	SubagentCostMicroUSD int64
	SubagentCostComplete bool
	CostMicroUSD         int64
	CostComplete         bool
	ThinkingEffort       string
	LastRunStatus        StopReason
	Pending              []PendingCall
	Parent               *ParentRef
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// SessionInfo is the metadata a session picker or resume screen needs.
type SessionInfo struct {
	ID           string
	Workspace    string
	Model        string
	FirstMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Turns        int
	Status       StopReason
	Parent       *ParentRef
}

// InspectSession reads a session without taking its exclusive lock.
func InspectSession(ctx context.Context, stateDir, sessionID string) (State, error) {
	if stateDir == "" {
		return State{}, &ConfigError{Field: "StateDir", Detail: "is required"}
	}
	snapshot, err := engine.Inspect(ctx, stateDir, sessionID)
	if err != nil {
		return State{}, publicError(err)
	}
	return stateFromSnapshot(snapshot), nil
}

// Sessions lists session metadata under stateDir, newest first.
func Sessions(ctx context.Context, stateDir string) ([]SessionInfo, error) {
	if stateDir == "" {
		return nil, &ConfigError{Field: "StateDir", Detail: "is required"}
	}
	snapshots, err := engine.List(ctx, stateDir)
	if err != nil {
		return nil, publicError(err)
	}
	out := make([]SessionInfo, 0, len(snapshots))
	for _, snapshot := range snapshots {
		out = append(out, sessionInfoFromSnapshot(snapshot))
	}
	return out, nil
}

// TotalUsage returns the session's own usage plus every folded delegated
// child's usage.
func (s State) TotalUsage() Usage {
	usage := s.Usage
	usage.InputTokens += s.SubagentUsage.InputTokens
	usage.OutputTokens += s.SubagentUsage.OutputTokens
	return usage
}

func stateFromSnapshot(snapshot engine.Snapshot) State {
	state := snapshot.State
	out := State{
		ID:        snapshot.ID,
		Workspace: state.Workspace,
		StateDir:  state.StateDir,
		Model: ModelSpec{
			ID:              state.Model,
			Provider:        state.Provider,
			Dialect:         Dialect(state.Dialect),
			BaseURL:         state.BaseURL,
			ContextWindow:   state.Limits.MaxContextTokens,
			MaxOutputTokens: state.Limits.MaxOutputTokens,
			InputLimit:      state.Limits.MaxInputTokens,
			Pricing:         fromInternalPricing(state.Pricing),
		},
		SystemPrompt:         state.SystemPrompt,
		AppendedPrompt:       state.AppendedPrompt,
		PromptCapped:         state.PromptCapped,
		PromptTag:            state.PromptTag,
		AgentName:            state.Agent,
		Limits:               fromInternalLimits(state.Limits),
		Turns:                state.Turns,
		Usage:                fromInternalUsage(state.Usage),
		ContextTokens:        state.ContextTokens,
		SubagentTurns:        state.SubagentTurns,
		SubagentUsage:        fromInternalUsage(state.SubagentUsage),
		SubagentCostMicroUSD: state.SubagentCostMicroUSD,
		SubagentCostComplete: state.SubagentCostComplete,
		CostMicroUSD:         state.CostMicroUSD,
		CostComplete:         state.CostComplete,
		ThinkingEffort:       state.ThinkingEffort,
		LastRunStatus:        StopReason(state.LastRunStatus),
		CreatedAt:            snapshot.CreatedAt,
		UpdatedAt:            snapshot.UpdatedAt,
	}
	if state.Model == "" {
		out.Model.ID = state.InitialModel
	}
	if state.Dialect == "" {
		out.Model.Dialect = Dialect(state.InitialDialect)
	}
	if state.Tools != nil {
		out.Tools = make([]ToolName, len(state.Tools))
		for i, name := range state.Tools {
			out.Tools[i] = ToolName(name)
		}
	}
	if state.RootTools != nil {
		out.RootTools = make([]ToolName, len(state.RootTools))
		for i, name := range state.RootTools {
			out.RootTools[i] = ToolName(name)
		}
	}
	for _, tool := range state.CustomTools {
		out.CustomTools = append(out.CustomTools, ToolSchema{
			Name:        tool.Schema.Name,
			Description: tool.Schema.Description,
			Parameters:  append(json.RawMessage(nil), tool.Schema.Parameters...),
		})
		if tool.ConcurrencySafe {
			out.ConcurrencySafe = append(out.ConcurrencySafe, ToolName(tool.Schema.Name))
		}
	}
	if state.ContractOverride != nil {
		out.ContractOverride = &ContractOverride{
			ParentSession: state.ContractOverride.ParentSession,
			Prompt:        state.ContractOverride.Prompt,
			Tools:         state.ContractOverride.Tools,
		}
	}
	if state.ParentSession != "" {
		out.Parent = &ParentRef{Session: state.ParentSession, Seq: state.ParentSeq, CallID: state.ParentCallID}
	}
	if len(state.Pending) > 0 {
		out.Pending = make([]PendingCall, 0, len(state.Pending))
		for _, pending := range state.Pending {
			out.Pending = append(out.Pending, PendingCall{CallID: pending.CallID, Name: pending.Name, Turn: pending.Turn})
		}
	}
	return out
}

func sessionInfoFromSnapshot(snapshot engine.Snapshot) SessionInfo {
	state := snapshot.State
	modelID := state.Model
	if modelID == "" {
		modelID = state.InitialModel
	}
	info := SessionInfo{
		ID:           snapshot.ID,
		Workspace:    state.Workspace,
		Model:        modelID,
		FirstMessage: snapshot.FirstMessage,
		CreatedAt:    snapshot.CreatedAt,
		UpdatedAt:    snapshot.UpdatedAt,
		Turns:        state.Turns,
		Status:       StopReason(state.LastRunStatus),
	}
	if state.ParentSession != "" {
		info.Parent = &ParentRef{Session: state.ParentSession, Seq: state.ParentSeq, CallID: state.ParentCallID}
	}
	return info
}

func fromInternalLimits(limits agent.Limits) Limits {
	return Limits{
		MaxContextTokens: limits.MaxContextTokens,
		MaxOutputTokens:  limits.MaxOutputTokens,
		SafetyMargin:     limits.SafetyMargin,
		MaxInputTokens:   limits.MaxInputTokens,
		CompactionTarget: limits.CompactionTarget,
	}
}

func toInternalLimits(limits Limits) agent.Limits {
	return agent.Limits{
		MaxContextTokens: limits.MaxContextTokens,
		MaxOutputTokens:  limits.MaxOutputTokens,
		SafetyMargin:     limits.SafetyMargin,
		MaxInputTokens:   limits.MaxInputTokens,
		CompactionTarget: limits.CompactionTarget,
	}
}

func fromInternalResult(result agent.Result, session string, err error) Result {
	return Result{
		Status:               StopReason(result.Status),
		Session:              session,
		Err:                  err,
		FinalText:            result.FinalText,
		RunTurns:             result.RunTurns,
		RunUsage:             fromInternalUsage(result.RunUsage),
		CumulativeTurns:      result.CumulativeTurns,
		CumulativeUsage:      fromInternalUsage(result.CumulativeUsage),
		ContextTokens:        result.ContextTokens,
		RunCostMicroUSD:      result.RunCostMicroUSD,
		CostMicroUSD:         result.CostMicroUSD,
		RunCostComplete:      result.RunCostComplete,
		CostComplete:         result.CostComplete,
		SubagentTurns:        result.SubagentTurns,
		SubagentUsage:        fromInternalUsage(result.SubagentUsage),
		SubagentCostMicroUSD: result.SubagentCostMicroUSD,
		SubagentCostComplete: result.SubagentCostComplete,
		Children:             fromInternalChildren(result.Children),
		Reason:               result.Reason,
		Duration:             result.Duration,
	}
}

func fromInternalChildren(children []agent.ChildOutcome) []ChildOutcome {
	if len(children) == 0 {
		return nil
	}
	out := make([]ChildOutcome, len(children))
	for i, child := range children {
		out[i] = ChildOutcome{
			Agent:         child.Agent,
			Session:       child.Session,
			CallID:        child.CallID,
			Intent:        child.Intent,
			Status:        child.Status,
			Turns:         child.Turns,
			ContextTokens: child.ContextTokens,
			Usage:         fromInternalUsage(child.Usage),
			CostMicroUSD:  child.CostMicroUSD,
			CostComplete:  child.CostComplete,
			Error:         child.Error,
		}
	}
	return out
}

// deriveLimits computes the per-call budget from a model spec.
func deriveLimits(spec ModelSpec) (Limits, error) {
	limits, err := agent.NewLimits(spec.ContextWindow, spec.MaxOutputTokens, spec.InputLimit)
	if err != nil {
		return Limits{}, &ConfigError{Field: "Model.Spec.ContextWindow", Detail: err.Error()}
	}
	return fromInternalLimits(limits), nil
}
