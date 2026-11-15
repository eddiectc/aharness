package agent

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"time"

	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

// PendingCall is a model tool call with no durable tool_result.
type PendingCall struct {
	CallID string
	Name   string
	Turn   int
}

// State is the durable session state reconstructed from the event log. A run
// starts with an empty per-run output but inherits cumulative turns and usage.
type State struct {
	Workspace      string
	StateDir       string
	Provider       string
	PromptVersion  string
	ToolsetVersion string
	HarnessVersion string
	ParentSession  string
	ParentSeq      uint64
	// ParentCallID distinguishes a delegated child from a fork: a child has a
	// parent call identifier, a fork does not.
	ParentCallID string
	// SystemPrompt is the prompt recorded in session_start. Empty for a log
	// written before the field existed.
	SystemPrompt string
	// Agent is the definition name recorded for a child session.
	Agent string
	// Tools is the recorded tool grant; nil means every tool of the version.
	Tools          []string
	InitialModel   string
	InitialDialect string
	Model          string
	Dialect        string
	BaseURL        string
	Limits         Limits
	MaxTurns       int
	MaxTotalTokens int
	SessionTimeout time.Duration
	ModelTimeout   time.Duration
	// ThinkingEffort is the latest recorded effective effort. Empty means the
	// provider default, including for old logs without the field.
	ThinkingEffort string
	Interactive    bool
	Stream         bool
	Turns          int
	Usage          model.Usage
	// ContextTokens is the size of the context after the latest recorded model
	// response: that response's input plus output tokens. It is the occupancy
	// the next call starts from, not a session-cumulative sum.
	ContextTokens int
	// CostMicroUSD is the cumulative estimated session cost; CostComplete is
	// false when any recorded model response lacked a price.
	CostMicroUSD int64
	CostComplete bool
	// Pricing is the most recent recorded run_start price snapshot, used to
	// keep cost estimation working on resume without a catalog fetch.
	Pricing       model.Pricing
	LastRunEnded  bool
	LastRunStatus StopReason
	LastEventSeq  uint64
	Pending       []PendingCall
	// Subagent totals are folded from every recorded delegate result, so a
	// resumed parent keeps the spend and the token budget it implies.
	SubagentTurns        int
	SubagentUsage        model.Usage
	SubagentCostMicroUSD int64
	SubagentCostComplete bool
}

// Inspect reconstructs the session state used for resume, fork, and the next
// run. Unknown event types and fields within v2 are ignored. A malformed known
// payload is a corrupt-log error.
func Inspect(events []session.Event) (State, error) {
	var (
		state                State
		startFound           bool
		pending              []PendingCall
		costComplete         = true
		subagentCostComplete = true
	)
	for _, ev := range events {
		state.LastEventSeq = ev.Seq
		switch ev.Type {
		case session.TypeSessionStart:
			if startFound {
				return State{}, fmt.Errorf("%w: session_start appears more than once", session.ErrCorrupt)
			}
			var data sessionStartData
			if err := decodeEventData(ev, &data); err != nil {
				return State{}, fmt.Errorf("%w: %w", session.ErrCorrupt, err)
			}
			startFound = true
			state.Workspace = data.Workspace
			state.StateDir = data.StateDir
			state.Provider = data.Provider
			state.PromptVersion = data.PromptVersion
			state.ToolsetVersion = data.ToolsetVersion
			state.HarnessVersion = data.HarnessVersion
			state.ParentSession = data.ParentSession
			state.ParentSeq = data.ParentSeq
			state.ParentCallID = data.ParentCallID
			state.SystemPrompt = data.SystemPrompt
			state.Agent = data.Agent
			state.Tools = append([]string(nil), data.Tools...)
			state.InitialModel = data.Model
			state.InitialDialect = data.Dialect
			state.Model = data.Model
			state.Dialect = data.Dialect
			state.BaseURL = data.BaseURL
			state.Limits = data.Limits
			state.MaxTurns = data.MaxTurns
			state.MaxTotalTokens = data.MaxTotalTokens
			state.SessionTimeout = time.Duration(data.SessionTimeoutMS) * time.Millisecond
			state.ModelTimeout = time.Duration(data.ModelTimeoutMS) * time.Millisecond
			state.ThinkingEffort = data.ThinkingEffort
		case session.TypeRunStart:
			var data runStartData
			if err := decodeEventData(ev, &data); err != nil {
				return State{}, fmt.Errorf("%w: %w", session.ErrCorrupt, err)
			}
			state.Model = data.Model
			state.Dialect = data.Dialect
			state.BaseURL = data.BaseURL
			state.Limits = data.Limits
			state.MaxTurns = data.MaxTurns
			state.MaxTotalTokens = data.MaxTotalTokens
			state.SessionTimeout = time.Duration(data.SessionTimeoutMS) * time.Millisecond
			state.ModelTimeout = time.Duration(data.ModelTimeoutMS) * time.Millisecond
			state.PromptVersion = data.PromptVersion
			state.ToolsetVersion = data.ToolsetVersion
			state.ThinkingEffort = data.ThinkingEffort
			state.Pricing = data.Pricing
			state.Interactive = data.Interactive
			state.Stream = data.Stream
			state.LastRunEnded = false
			state.LastRunStatus = ""
		case session.TypeRunEnd:
			var data runEndData
			if err := decodeEventData(ev, &data); err != nil {
				return State{}, fmt.Errorf("%w: %w", session.ErrCorrupt, err)
			}
			state.LastRunEnded = true
			state.LastRunStatus = data.Status
		case session.TypeModelResponse:
			var data modelResponseData
			if err := decodeEventData(ev, &data); err != nil {
				return State{}, fmt.Errorf("%w: %w", session.ErrCorrupt, err)
			}
			state.Turns++
			state.Usage.InputTokens += data.Usage.InputTokens
			state.Usage.OutputTokens += data.Usage.OutputTokens
			// A response that reported no usage leaves the last known context
			// occupancy in place rather than resetting it to zero.
			if total := data.Usage.Total(); total > 0 {
				state.ContextTokens = total
			}
			if data.CostMicroUSD != nil {
				state.CostMicroUSD += *data.CostMicroUSD
			} else {
				costComplete = false
			}
			for _, call := range data.ToolCalls {
				if call.ID == "" {
					continue
				}
				pending = append(pending, PendingCall{CallID: call.ID, Name: call.Name, Turn: ev.Turn})
			}
		case session.TypeToolResult:
			var data toolResultData
			if err := decodeEventData(ev, &data); err != nil {
				return State{}, fmt.Errorf("%w: %w", session.ErrCorrupt, err)
			}
			pending = removePending(pending, data.CallID)
			if data.Name == "delegate" {
				foldDelegateDetails(&state, data.Details, &subagentCostComplete)
			}
		}
	}
	if !startFound {
		return State{}, fmt.Errorf("%w: session_start event missing", session.ErrCorrupt)
	}
	if state.PromptVersion == "" || state.ToolsetVersion == "" {
		return State{}, fmt.Errorf("%w: session_start is missing prompt or toolset version", session.ErrCorrupt)
	}
	state.CostComplete = costComplete
	if state.SubagentUsage.Total() > 0 || state.SubagentTurns > 0 {
		state.SubagentCostComplete = subagentCostComplete
	}
	state.Pending = pending
	return state, nil
}

// foldDelegateDetails adds a recorded delegate result to the cumulative
// subagent totals. Malformed or unknown details are ignored, not fatal: a
// future tool with a different details shape must not break replay.
func foldDelegateDetails(state *State, raw json.RawMessage, costComplete *bool) {
	details, ok := tools.ParseDelegateDetails(raw)
	if !ok || len(details.Tasks) == 0 {
		return
	}
	state.SubagentTurns += details.SubagentTurns
	state.SubagentUsage.InputTokens += details.SubagentUsage.InputTokens
	state.SubagentUsage.OutputTokens += details.SubagentUsage.OutputTokens
	state.SubagentCostMicroUSD += details.SubagentCostMicroUSD
	if !details.SubagentCostComplete {
		*costComplete = false
	}
}

// SessionSummary is the metadata a session picker needs from the head of a
// session log. It is not a replacement for Inspect: it does not validate the
// rest of the log or reconstruct cumulative counters.
type SessionSummary struct {
	Workspace    string
	Provider     string
	Model        string
	FirstMessage string
	// ParentSession is non-empty for a delegated child, which the resume
	// picker excludes.
	ParentSession string
}

// Summarize decodes the session_start event and the first user message from a
// log head produced by session.ReadHead. Unknown event types are ignored. A
// missing session_start or a malformed known payload is a corrupt-log error.
func Summarize(events []session.Event) (SessionSummary, error) {
	if len(events) == 0 || events[0].Type != session.TypeSessionStart {
		return SessionSummary{}, fmt.Errorf("%w: session_start event missing", session.ErrCorrupt)
	}
	var summary SessionSummary
	found := false
	messageFound := false
	for _, ev := range events {
		switch ev.Type {
		case session.TypeSessionStart:
			if found {
				return SessionSummary{}, fmt.Errorf("%w: session_start appears more than once", session.ErrCorrupt)
			}
			var data sessionStartData
			if err := decodeEventData(ev, &data); err != nil {
				return SessionSummary{}, fmt.Errorf("%w: %w", session.ErrCorrupt, err)
			}
			found = true
			summary.Workspace = data.Workspace
			summary.Provider = data.Provider
			summary.Model = data.Model
			summary.ParentSession = data.ParentSession
		case session.TypeUserMessage:
			if messageFound {
				continue
			}
			var data userMessageData
			if err := decodeEventData(ev, &data); err != nil {
				return SessionSummary{}, fmt.Errorf("%w: %w", session.ErrCorrupt, err)
			}
			messageFound = true
			summary.FirstMessage = data.Text
		}
	}
	if !found {
		return SessionSummary{}, fmt.Errorf("%w: session_start event missing", session.ErrCorrupt)
	}
	return summary, nil
}

// RestoreTodo returns the checklist recorded by the last successful todo tool
// result in the log, so a resumed, forked, or follow-up run starts with the
// list the model last set instead of an empty one. The rendered tool output is
// not parsed; the structured details written by the todo tool are used. A log
// with no usable todo result, or with malformed details, yields nil.
func RestoreTodo(events []session.Event) []tools.TodoItem {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != session.TypeToolResult {
			continue
		}
		var data toolResultData
		if err := decodeEventData(ev, &data); err != nil {
			continue
		}
		if data.Name != "todo" || !data.OK || len(data.Details) == 0 {
			continue
		}
		var details struct {
			Items []tools.TodoItem `json:"items"`
		}
		if err := jsonv2.Unmarshal(data.Details, &details); err != nil {
			continue
		}
		return details.Items
	}
	return nil
}

// removePending removes the most recent unpaired call with id.
func removePending(pending []PendingCall, id string) []PendingCall {
	if id == "" {
		return pending
	}
	for i, p := range slices.Backward(pending) {
		if p.CallID == id {
			return append(pending[:i], pending[i+1:]...)
		}
	}
	return pending
}

// SupportsPrompt reports whether this binary can replay a prompt version.
func SupportsPrompt(version string) bool {
	return version == PromptVersionV0 || version == PromptVersion
}

// SupportsToolset reports whether this binary can replay a toolset version.
func SupportsToolset(version string) bool { return tools.SupportsVersion(version) }
