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
	RunID          string
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
	// Cumulative fields include this response and every earlier turn in the
	// session, not a final total promise.
	CumulativeTurns int
	CumulativeUsage Usage
	ContextTokens   int
	CostMicroUSD    int64
	CostComplete    bool
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
	Turn int
	// ID is nonzero for a steering line and zero for a task or hint.
	ID     uint64
	Source string
	Text   string
}

// SubagentMessageRecorded acknowledges a task, steering line, or hint in a
// child session. ID is nonzero for a child steering line.
type SubagentMessageRecorded struct {
	EventMeta
	Turn         int
	CallID       string
	Index        int
	Agent        string
	ChildSession string
	ID           uint64
	Source       string
	Text         string
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

// RunFinished carries the terminal Result and is the last event. The display
// callback is never invoked after Run returns. If the final append or sync
// failed, the iterator closes with the result instead.
type RunFinished struct {
	EventMeta
	RunID    string
	Result   Result
	Reason   string
	Duration time.Duration
}

func (RunStarted) event()              {}
func (TextDelta) event()               {}
func (ReasoningDelta) event()          {}
func (ResponseCompleted) event()       {}
func (ToolStarted) event()             {}
func (ToolCompleted) event()           {}
func (MessageRecorded) event()         {}
func (SubagentMessageRecorded) event() {}
func (Notice) event()                  {}
func (SubagentStarted) event()         {}
func (SubagentProgress) event()        {}
func (SubagentFinished) event()        {}
func (RunFinished) event()             {}

// Event view kinds. They are the stable discriminators in EventView.Kind.
const (
	EventKindRunStarted              = "run_started"
	EventKindTextDelta               = "text_delta"
	EventKindReasoningDelta          = "reasoning_delta"
	EventKindResponseCompleted       = "response_completed"
	EventKindToolStarted             = "tool_started"
	EventKindToolCompleted           = "tool_completed"
	EventKindMessageRecorded         = "message_recorded"
	EventKindSubagentMessageRecorded = "subagent_message_recorded"
	EventKindNotice                  = "notice"
	EventKindSubagentStarted         = "subagent_started"
	EventKindSubagentProgress        = "subagent_progress"
	EventKindSubagentFinished        = "subagent_finished"
	EventKindRunFinished             = "run_finished"
)

// EventView is a flat, JSON-serialisable projection of one Event. Kind is the
// stable discriminator; fields that do not apply to the kind are omitted. A
// host can forward a run's events over SSE or a structured log without a type
// switch.
type EventView struct {
	Kind    string    `json:"kind"`
	Time    time.Time `json:"time"`
	Session string    `json:"session,omitempty"`

	RunID          string `json:"run_id,omitempty"`
	Turn           int    `json:"turn,omitempty"`
	ResumedFromSeq uint64 `json:"resumed_from_seq,omitempty"`
	Model          string `json:"model,omitempty"`
	Dialect        string `json:"dialect,omitempty"`
	ThinkingEffort string `json:"thinking_effort,omitempty"`

	Text      string     `json:"text,omitempty"`
	Reasoning string     `json:"reasoning,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Call      *ToolCall  `json:"call,omitempty"`
	CallID    string     `json:"call_id,omitempty"`
	Index     int        `json:"index,omitempty"`
	Agent     string     `json:"agent,omitempty"`
	Intent    string     `json:"intent,omitempty"`
	Status    string     `json:"status,omitempty"`
	Tools     []string   `json:"tools,omitempty"`

	FinishReason FinishReason `json:"finish_reason,omitempty"`
	Usage        *Usage       `json:"usage,omitempty"`
	LatencyMS    int64        `json:"latency_ms,omitempty"`
	Streamed     bool         `json:"streamed,omitempty"`

	CumulativeTurns int    `json:"cumulative_turns,omitempty"`
	CumulativeUsage *Usage `json:"cumulative_usage,omitempty"`
	ContextTokens   int    `json:"context_tokens,omitempty"`
	CostMicroUSD    int64  `json:"cost_micro_usd,omitempty"`
	CostComplete    bool   `json:"cost_complete,omitempty"`

	Name       string          `json:"name,omitempty"`
	OK         bool            `json:"ok,omitempty"`
	Code       string          `json:"code,omitempty"`
	Output     string          `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	ExitCode   *int            `json:"exit_code,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	TimedOut   bool            `json:"timed_out,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`

	ID         uint64 `json:"id,omitempty"`
	Source     string `json:"source,omitempty"`
	NoticeKind string `json:"notice_kind,omitempty"`

	ChildSession string `json:"child_session,omitempty"`
	ChildTurns   int    `json:"child_turns,omitempty"`
	LastTool     string `json:"last_tool,omitempty"`

	Result *Result `json:"result,omitempty"`
	Reason string  `json:"reason,omitempty"`
}

// ViewEvent projects any Event into its stable serialisable form. It reports
// the zero view with Kind "unknown" for an event this binary does not know.
func ViewEvent(ev Event) EventView {
	meta := func(m EventMeta) EventView {
		return EventView{Time: m.Time, Session: m.Session}
	}
	switch ev := ev.(type) {
	case RunStarted:
		view := meta(ev.EventMeta)
		view.Kind = EventKindRunStarted
		view.RunID = ev.RunID
		view.Turn = ev.Turn
		view.ResumedFromSeq = ev.ResumedFromSeq
		view.Model = ev.Model
		view.Dialect = ev.Dialect
		view.ThinkingEffort = ev.ThinkingEffort
		return view
	case TextDelta:
		view := meta(ev.EventMeta)
		view.Kind = EventKindTextDelta
		view.Text = ev.Text
		return view
	case ReasoningDelta:
		view := meta(ev.EventMeta)
		view.Kind = EventKindReasoningDelta
		view.Text = ev.Text
		return view
	case ResponseCompleted:
		view := meta(ev.EventMeta)
		view.Kind = EventKindResponseCompleted
		view.Turn = ev.Turn
		view.Text = ev.Text
		view.Reasoning = ev.Reasoning
		view.ToolCalls = ev.ToolCalls
		view.FinishReason = ev.FinishReason
		usage := ev.Usage
		view.Usage = &usage
		view.LatencyMS = ev.Latency.Milliseconds()
		view.Streamed = ev.Streamed
		view.CumulativeTurns = ev.CumulativeTurns
		cumulative := ev.CumulativeUsage
		view.CumulativeUsage = &cumulative
		view.ContextTokens = ev.ContextTokens
		view.CostMicroUSD = ev.CostMicroUSD
		view.CostComplete = ev.CostComplete
		return view
	case ToolStarted:
		view := meta(ev.EventMeta)
		view.Kind = EventKindToolStarted
		view.Turn = ev.Turn
		call := ev.Call
		view.Call = &call
		view.CallID = ev.Call.ID
		view.Name = ev.Call.Name
		return view
	case ToolCompleted:
		view := meta(ev.EventMeta)
		view.Kind = EventKindToolCompleted
		view.Turn = ev.Turn
		view.CallID = ev.CallID
		view.Name = ev.Name
		view.OK = ev.OK
		view.Code = ev.Code
		view.Output = ev.Output
		view.Error = ev.Error
		if ev.ExitCode != 0 {
			exit := ev.ExitCode
			view.ExitCode = &exit
		}
		view.DurationMS = ev.Duration.Milliseconds()
		view.TimedOut = ev.TimedOut
		view.Truncated = ev.Truncated
		view.Details = ev.Details
		return view
	case MessageRecorded:
		view := meta(ev.EventMeta)
		view.Kind = EventKindMessageRecorded
		view.Turn = ev.Turn
		view.ID = ev.ID
		view.Source = ev.Source
		view.Text = ev.Text
		return view
	case SubagentMessageRecorded:
		view := meta(ev.EventMeta)
		view.Kind = EventKindSubagentMessageRecorded
		view.Turn = ev.Turn
		view.CallID = ev.CallID
		view.Index = ev.Index
		view.Agent = ev.Agent
		view.ChildSession = ev.ChildSession
		view.ID = ev.ID
		view.Source = ev.Source
		view.Text = ev.Text
		return view
	case Notice:
		view := meta(ev.EventMeta)
		view.Kind = EventKindNotice
		view.Turn = ev.Turn
		view.NoticeKind = string(ev.Kind)
		view.Text = ev.Text
		return view
	case SubagentStarted:
		view := meta(ev.EventMeta)
		view.Kind = EventKindSubagentStarted
		view.Turn = ev.Turn
		view.CallID = ev.CallID
		view.Index = ev.Index
		view.Agent = ev.Agent
		view.Intent = ev.Intent
		view.ChildSession = ev.ChildSession
		view.Model = ev.Model
		view.ThinkingEffort = ev.Effort
		view.Tools = ev.Tools
		return view
	case SubagentProgress:
		view := meta(ev.EventMeta)
		view.Kind = EventKindSubagentProgress
		view.Turn = ev.Turn
		view.CallID = ev.CallID
		view.Index = ev.Index
		view.Agent = ev.Agent
		view.ChildSession = ev.ChildSession
		view.ChildTurns = ev.ChildTurns
		view.ContextTokens = ev.ContextTokens
		usage := ev.Usage
		view.Usage = &usage
		view.CostMicroUSD = ev.CostMicroUSD
		view.CostComplete = ev.CostComplete
		view.LastTool = ev.LastTool
		return view
	case SubagentFinished:
		view := meta(ev.EventMeta)
		view.Kind = EventKindSubagentFinished
		view.Turn = ev.Turn
		view.CallID = ev.CallID
		view.Index = ev.Index
		view.Agent = ev.Agent
		view.ChildSession = ev.ChildSession
		view.Status = ev.Status
		view.ChildTurns = ev.ChildTurns
		view.ContextTokens = ev.ContextTokens
		usage := ev.Usage
		view.Usage = &usage
		view.CostMicroUSD = ev.CostMicroUSD
		view.CostComplete = ev.CostComplete
		view.DurationMS = ev.Duration.Milliseconds()
		view.Error = ev.Error
		return view
	case RunFinished:
		view := meta(ev.EventMeta)
		view.Kind = EventKindRunFinished
		view.RunID = ev.RunID
		result := ev.Result
		view.Result = &result
		view.Reason = ev.Reason
		view.DurationMS = ev.Duration.Milliseconds()
		return view
	default:
		return EventView{Kind: "unknown"}
	}
}

// eventFromInternal maps one internal display event to the public contract.
// It reports false for transport-level events that are not public.
func eventFromInternal(ev agent.DisplayEvent, session string, now time.Time) (Event, bool) {
	meta := EventMeta{Time: now, Session: session}
	switch ev := ev.(type) {
	case agent.RunStarted:
		return RunStarted{EventMeta: meta, RunID: ev.RunID, Turn: ev.Turn, ResumedFromSeq: ev.ResumedFromSeq, Model: ev.Model, Dialect: ev.Dialect, ThinkingEffort: ev.ThinkingEffort}, true
	case agent.TextDelta:
		return TextDelta{EventMeta: meta, Text: ev.Text}, true
	case agent.ReasoningDelta:
		return ReasoningDelta{EventMeta: meta, Text: ev.Text}, true
	case agent.ResponseCompleted:
		return ResponseCompleted{
			EventMeta:       meta,
			Turn:            ev.Turn,
			Text:            ev.Text,
			Reasoning:       ev.Reasoning,
			ToolCalls:       fromInternalToolCalls(ev.ToolCalls),
			FinishReason:    FinishReason(ev.FinishReason),
			Usage:           fromInternalUsage(ev.Usage),
			Latency:         ev.Latency,
			Streamed:        ev.Streamed,
			CumulativeTurns: ev.CumulativeTurns,
			CumulativeUsage: fromInternalUsage(ev.CumulativeUsage),
			ContextTokens:   ev.ContextTokens,
			CostMicroUSD:    ev.CostMicroUSD,
			CostComplete:    ev.CostComplete,
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
		return MessageRecorded{EventMeta: meta, Turn: ev.Turn, ID: ev.ID, Source: ev.Source, Text: ev.Text}, true
	case agent.SubagentMessageRecorded:
		return SubagentMessageRecorded{
			EventMeta:    meta,
			Turn:         ev.Turn,
			CallID:       ev.CallID,
			Index:        ev.Index,
			Agent:        ev.Agent,
			ChildSession: ev.Session,
			ID:           ev.ID,
			Source:       ev.Source,
			Text:         ev.Text,
		}, true
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
			RunID:     ev.RunID,
			Result:    fromInternalResult(ev.Result, session, publicError(ev.Result.Err)),
			Reason:    ev.Reason,
			Duration:  ev.Duration,
		}, true
	default:
		return nil, false
	}
}
