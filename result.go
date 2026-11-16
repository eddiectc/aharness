package aharness

import (
	"context"
	"encoding/json"
	"time"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/engine"
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

// Result is the authoritative outcome of one run.
type Result struct {
	Status          StopReason
	Session         string
	Err             error
	FinalText       string
	RunTurns        int
	RunUsage        Usage
	CumulativeTurns int
	CumulativeUsage Usage
	ContextTokens   int
	RunCostMicroUSD int64
	CostMicroUSD    int64
	RunCostComplete bool
	CostComplete    bool

	SubagentTurns        int
	SubagentUsage        Usage
	SubagentCostMicroUSD int64
	SubagentCostComplete bool
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
	ID               string
	Workspace        string
	StateDir         string
	Model            ModelSpec
	SystemPrompt     string
	Tools            []ToolName
	CustomTools      []ToolSchema
	ConcurrencySafe  []ToolName
	ContractOverride *ContractOverride
	Limits           Limits
	Turns            int
	Usage            Usage
	ContextTokens    int
	CostMicroUSD     int64
	CostComplete     bool
	ThinkingEffort   string
	LastRunStatus    StopReason
	Pending          []PendingCall
	Parent           *ParentRef
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
		SystemPrompt:   state.SystemPrompt,
		Limits:         fromInternalLimits(state.Limits),
		Turns:          state.Turns,
		Usage:          fromInternalUsage(state.Usage),
		ContextTokens:  state.ContextTokens,
		CostMicroUSD:   state.CostMicroUSD,
		CostComplete:   state.CostComplete,
		ThinkingEffort: state.ThinkingEffort,
		LastRunStatus:  StopReason(state.LastRunStatus),
		CreatedAt:      snapshot.CreatedAt,
		UpdatedAt:      snapshot.UpdatedAt,
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
	}
}

// deriveLimits computes the per-call budget from a model spec, matching the
// CLI's validateEffective rule.
func deriveLimits(spec ModelSpec) (Limits, error) {
	if spec.ContextWindow <= 0 {
		return Limits{}, &ConfigError{Field: "Model.Spec.ContextWindow", Detail: "must be positive"}
	}
	margin := agent.SafetyMargin(spec.ContextWindow)
	input := spec.ContextWindow - margin
	if spec.InputLimit > 0 && spec.InputLimit < input {
		input = spec.InputLimit
	}
	if input <= 0 {
		return Limits{}, &ConfigError{Field: "Model.Spec.ContextWindow", Detail: "leaves no positive input budget after the safety margin"}
	}
	return Limits{
		MaxContextTokens: spec.ContextWindow,
		MaxOutputTokens:  spec.MaxOutputTokens,
		SafetyMargin:     margin,
		MaxInputTokens:   input,
		CompactionTarget: input,
	}, nil
}
