package agent

import (
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
	Interactive    bool
	Stream         bool
	Turns          int
	Usage          model.Usage
	LastRunEnded   bool
	LastRunStatus  StopReason
	LastEventSeq   uint64
	Pending        []PendingCall
}

// Inspect reconstructs the session state used for resume, fork, and the next
// run. Unknown event types and fields within v2 are ignored. A malformed known
// payload is a corrupt-log error.
func Inspect(events []session.Event) (State, error) {
	var (
		state      State
		startFound bool
		pending    []PendingCall
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
		}
	}
	if !startFound {
		return State{}, fmt.Errorf("%w: session_start event missing", session.ErrCorrupt)
	}
	if state.PromptVersion == "" || state.ToolsetVersion == "" {
		return State{}, fmt.Errorf("%w: session_start is missing prompt or toolset version", session.ErrCorrupt)
	}
	state.Pending = pending
	return state, nil
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
func SupportsPrompt(version string) bool { return version == PromptVersion }

// SupportsToolset reports whether this binary can replay a toolset version.
func SupportsToolset(version string) bool { return version == tools.Version }
