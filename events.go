package aharness

import (
	"encoding/json"
	"time"

	"github.com/eddiectc/aharness/agent"
)

// Event is one observation from a run. The interface is sealed; consumers
// should have a default branch for events added in a later version.
type Event interface{ event() }

// EventMeta is embedded in every event. Time is the library's observation
// time, not a durable timestamp; Session is the session that produced it.
type EventMeta struct {
	Time    time.Time
	Session string
}

// RunStarted is emitted after run_start is durable.
type RunStarted struct {
	EventMeta
	Turn           int
	ResumedFromSeq uint64
	Model          string
	Dialect        string
	ThinkingEffort string
}

// TextDelta is display-only assistant text from a StreamingModel.
type TextDelta struct {
	EventMeta
	Text string
}

// ReasoningDelta is display-only reasoning text from a StreamingModel.
type ReasoningDelta struct {
	EventMeta
	Text string
}

// ResponseCompleted is emitted after model_response is durable.
type ResponseCompleted struct {
	EventMeta
	Turn         int
	Text         string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason FinishReason
	Usage        Usage
	Latency      time.Duration
	Streamed     bool
}

// ToolStarted is emitted immediately before one tool call executes.
type ToolStarted struct {
	EventMeta
	Turn int
	Call ToolCall
}

// ToolCompleted is emitted after tool_result is durable and synced.
type ToolCompleted struct {
	EventMeta
	Turn      int
	CallID    string
	Name      string
	OK        bool
	Code      string
	Output    string
	Error     string
	ExitCode  int
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
	Details   json.RawMessage
}

// MessageRecorded acknowledges a task, steering line, or hint.
type MessageRecorded struct {
	EventMeta
	Turn   int
	Source string
	Text   string
}

// Subagent status values reported by SubagentFinished and ChildOutcome. The
// fields stay string-typed so a host may compare them with recorded data.
const (
	SubagentCompleted     = "completed"
	SubagentMaxTurns      = "max_turns"
	SubagentTokenLimit    = "token_limit"
	SubagentTimeLimit     = "time_limit"
	SubagentStuck         = "stuck"
	SubagentInterrupted   = "interrupted"
	SubagentProviderError = "provider_error"
	SubagentProtocolError = "protocol_error"
	SubagentInternalError = "internal_error"
	SubagentFailed        = "failed"
)

// NoticeKind classifies a display notice.
type NoticeKind string

const (
	NoticeRetry      NoticeKind = "retry"
	NoticeCompaction NoticeKind = "compaction"
	NoticeLimit      NoticeKind = "limit"
	NoticeInterrupt  NoticeKind = "interrupt"
	NoticeRepair     NoticeKind = "repair"
	NoticeWarning    NoticeKind = "warning"
	NoticeError      NoticeKind = "error"
)

// Notice is a display-only retry, compaction, limit, interrupt, repair, or
// warning message.
type Notice struct {
	EventMeta
	Turn int
	Kind NoticeKind
	Text string
}

// SubagentStarted is emitted before a delegated child run starts.
type SubagentStarted struct {
	EventMeta
	Turn         int
	CallID       string
	Index        int
	Agent        string
	Intent       string
	ChildSession string
	Model        string
	Effort       string
	Tools        []string
}

// SubagentProgress is emitted at child model-response boundaries.
type SubagentProgress struct {
	EventMeta
	Turn          int
	CallID        string
	Index         int
	Agent         string
	ChildSession  string
	ChildTurns    int
	ContextTokens int
	Usage         Usage
	CostMicroUSD  int64
	CostComplete  bool
	LastTool      string
}

// SubagentFinished is emitted after a delegated child finishes.
type SubagentFinished struct {
	EventMeta
	Turn          int
	CallID        string
	Index         int
	Agent         string
	ChildSession  string
	Status        string
	ChildTurns    int
	ContextTokens int
	Usage         Usage
	CostMicroUSD  int64
	CostComplete  bool
	Duration      time.Duration
	Error         string
}

// RunFinished carries the terminal Result.
type RunFinished struct {
	EventMeta
	Result   Result
	Reason   string
	Duration time.Duration
}

func (RunStarted) event()        {}
func (TextDelta) event()         {}
func (ReasoningDelta) event()    {}
func (ResponseCompleted) event() {}
func (ToolStarted) event()       {}
func (ToolCompleted) event()     {}
func (MessageRecorded) event()   {}
func (Notice) event()            {}
func (SubagentStarted) event()   {}
func (SubagentProgress) event()  {}
func (SubagentFinished) event()  {}
func (RunFinished) event()       {}

// eventFromInternal maps one internal display event to the public contract.
// It reports false for transport-level events that are not public.
func eventFromInternal(ev agent.DisplayEvent, session string, now time.Time) (Event, bool) {
	meta := EventMeta{Time: now, Session: session}
	switch ev := ev.(type) {
	case agent.RunStarted:
		return RunStarted{EventMeta: meta, Turn: ev.Turn, ResumedFromSeq: ev.ResumedFromSeq, Model: ev.Model, Dialect: ev.Dialect, ThinkingEffort: ev.ThinkingEffort}, true
	case agent.TextDelta:
		return TextDelta{EventMeta: meta, Text: ev.Text}, true
	case agent.ReasoningDelta:
		return ReasoningDelta{EventMeta: meta, Text: ev.Text}, true
	case agent.ResponseCompleted:
		return ResponseCompleted{
			EventMeta:    meta,
			Turn:         ev.Turn,
			Text:         ev.Text,
			Reasoning:    ev.Reasoning,
			ToolCalls:    fromInternalToolCalls(ev.ToolCalls),
			FinishReason: FinishReason(ev.FinishReason),
			Usage:        fromInternalUsage(ev.Usage),
			Latency:      ev.Latency,
			Streamed:     ev.Streamed,
		}, true
	case agent.ToolStarted:
		return ToolStarted{EventMeta: meta, Turn: ev.Turn, Call: fromInternalToolCall(ev.Call)}, true
	case agent.ToolCompleted:
		return ToolCompleted{
			EventMeta: meta,
			Turn:      ev.Turn,
			CallID:    ev.CallID,
			Name:      ev.Name,
			OK:        ev.OK,
			Code:      string(ev.Code),
			Output:    ev.Output,
			Error:     ev.Error,
			ExitCode:  ev.ExitCode,
			Duration:  ev.Duration,
			TimedOut:  ev.TimedOut,
			Truncated: ev.Truncated,
			Details:   append(json.RawMessage(nil), ev.Details...),
		}, true
	case agent.MessageRecorded:
		return MessageRecorded{EventMeta: meta, Turn: ev.Turn, Source: ev.Source, Text: ev.Text}, true
	case agent.Notice:
		return Notice{EventMeta: meta, Turn: ev.Turn, Kind: NoticeKind(ev.Kind), Text: ev.Text}, true
	case agent.SubagentStarted:
		return SubagentStarted{
			EventMeta:    meta,
			Turn:         ev.Turn,
			CallID:       ev.CallID,
			Index:        ev.Index,
			Agent:        ev.Agent,
			Intent:       ev.Intent,
			ChildSession: ev.Session,
			Model:        ev.Model,
			Effort:       ev.Effort,
			Tools:        append([]string(nil), ev.Tools...),
		}, true
	case agent.SubagentProgress:
		return SubagentProgress{
			EventMeta:     meta,
			Turn:          ev.Turn,
			CallID:        ev.CallID,
			Index:         ev.Index,
			Agent:         ev.Agent,
			ChildSession:  ev.Session,
			ChildTurns:    ev.ChildTurns,
			ContextTokens: ev.ContextTokens,
			Usage:         fromInternalUsage(ev.Usage),
			CostMicroUSD:  ev.CostMicroUSD,
			CostComplete:  ev.CostComplete,
			LastTool:      ev.LastTool,
		}, true
	case agent.SubagentFinished:
		return SubagentFinished{
			EventMeta:     meta,
			Turn:          ev.Turn,
			CallID:        ev.CallID,
			Index:         ev.Index,
			Agent:         ev.Agent,
			ChildSession:  ev.Session,
			Status:        ev.Status,
			ChildTurns:    ev.ChildTurns,
			ContextTokens: ev.ContextTokens,
			Usage:         fromInternalUsage(ev.Usage),
			CostMicroUSD:  ev.CostMicroUSD,
			CostComplete:  ev.CostComplete,
			Duration:      ev.Duration,
			Error:         ev.Error,
		}, true
	case agent.RunFinished:
		return RunFinished{
			EventMeta: meta,
			Result:    fromInternalResult(ev.Result, session, publicError(ev.Result.Err)),
			Reason:    ev.Reason,
			Duration:  ev.Duration,
		}, true
	default:
		return nil, false
	}
}
