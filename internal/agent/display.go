package agent

import (
	"encoding/json"
	"strings"
	"time"

	"aharness/internal/model"
	"aharness/internal/tools"
)

// DisplayEvent is display-only progress from the runner. It is never durable
// state and is not replayed from the session log.
type DisplayEvent interface{ isDisplayEvent() }

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

// RunStarted is emitted after run_start is durable and pending calls are
// repaired. It carries the effective model and dialect recorded in the log.
type RunStarted struct {
	Turn           int
	ResumedFromSeq uint64
	Model          string
	Dialect        string
	ThinkingEffort string
}

// ModelStarted is emitted before each transport attempt.
type ModelStarted struct {
	Turn    int
	Attempt int
}

// TextDelta carries display-only assistant text. It is never logged as a
// delta; ResponseCompleted carries the canonical response.
type TextDelta struct {
	Text string
}

// ReasoningDelta carries display-only model thinking text (provider
// reasoning). It is never logged and is not replayed on resume; the TUI shows
// it as a separate thinking block. ResponseCompleted carries the final text.
type ReasoningDelta struct {
	Text string
}

// ResponseCompleted is emitted after model_response is appended.
type ResponseCompleted struct {
	Turn         int
	Text         string
	Reasoning    string
	ToolCalls    []model.ToolCall
	FinishReason model.FinishReason
	Usage        model.Usage
	Latency      time.Duration
	Streamed     bool
}

// ToolStarted is emitted immediately before a tool call executes, after the
// model response carrying it is durable.
type ToolStarted struct {
	Turn int
	Call model.ToolCall
}

// ToolCompleted is emitted after tool_result is appended and synced.
type ToolCompleted struct {
	Turn      int
	CallID    string
	Name      string
	OK        bool
	Code      tools.Code
	Output    string
	Error     string
	ExitCode  int
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
	// Details is the structured tool result payload recorded in the log (for
	// example, the todo checklist). It is display-only metadata for consumers
	// that can use it; it is never logged by the display path.
	Details json.RawMessage
}

// MessageRecorded acknowledges a task, steering line, or hint after its
// user_message event is durable.
type MessageRecorded struct {
	Turn   int
	Source string
	Text   string
}

// Notice is a display-only retry, compaction, limit, interrupt, repair, or
// warning message.
type Notice struct {
	Turn int
	Kind NoticeKind
	Text string
}

// SubagentStarted is emitted before a child run starts. Fields are resolved
// parent-side so the display never guesses.
type SubagentStarted struct {
	Turn    int
	CallID  string
	Index   int
	Agent   string
	Intent  string
	Session string
	Model   string
	// Effort is the resolved effort; empty means the model default.
	Effort string
	Tools  []string
}

// SubagentProgress is emitted at child model-response boundaries and throttled
// to at most one event per second per child. It carries counters and names
// only, never child text, arguments, or tool output.
type SubagentProgress struct {
	Turn          int
	CallID        string
	Index         int
	Agent         string
	Session       string
	ChildTurns    int
	ContextTokens int
	Usage         model.Usage
	CostMicroUSD  int64
	// CostComplete is false when the child's model is unpriced, so the roster
	// shows an unknown cost instead of a zero one.
	CostComplete bool
	LastTool     string
}

// SubagentFinished is emitted after the child store is synced and closed.
type SubagentFinished struct {
	Turn          int
	CallID        string
	Index         int
	Agent         string
	Session       string
	Status        string
	ChildTurns    int
	ContextTokens int
	Usage         model.Usage
	CostMicroUSD  int64
	CostComplete  bool
	Duration      time.Duration
}

// RunFinished is emitted after final and run_end are durable.
type RunFinished struct {
	Result   Result
	Reason   string
	Duration time.Duration
}

func (RunStarted) isDisplayEvent()        {}
func (ModelStarted) isDisplayEvent()      {}
func (TextDelta) isDisplayEvent()         {}
func (ReasoningDelta) isDisplayEvent()    {}
func (ResponseCompleted) isDisplayEvent() {}
func (ToolStarted) isDisplayEvent()       {}
func (ToolCompleted) isDisplayEvent()     {}
func (MessageRecorded) isDisplayEvent()   {}
func (Notice) isDisplayEvent()            {}
func (SubagentStarted) isDisplayEvent()   {}
func (SubagentProgress) isDisplayEvent()  {}
func (SubagentFinished) isDisplayEvent()  {}
func (RunFinished) isDisplayEvent()       {}

// sanitizeDisplayEvent removes terminal control characters from text fields
// before they reach a display consumer. The session log remains the raw
// record; this sanitization is display-only.
func sanitizeDisplayEvent(ev DisplayEvent) DisplayEvent {
	switch ev := ev.(type) {
	case TextDelta:
		ev.Text = displaySafe(ev.Text)
		return ev
	case ReasoningDelta:
		ev.Text = displaySafe(ev.Text)
		return ev
	case ResponseCompleted:
		ev.Text = displaySafe(ev.Text)
		ev.Reasoning = displaySafe(ev.Reasoning)
		return ev
	case ToolCompleted:
		ev.Output = displaySafe(ev.Output)
		ev.Error = displaySafe(ev.Error)
		return ev
	case MessageRecorded:
		ev.Text = displaySafe(ev.Text)
		return ev
	case Notice:
		ev.Text = displaySafe(ev.Text)
		return ev
	case SubagentStarted:
		ev.Agent = displaySafe(ev.Agent)
		ev.Intent = displaySafe(ev.Intent)
		ev.Session = displaySafe(ev.Session)
		ev.Model = displaySafe(ev.Model)
		ev.Effort = displaySafe(ev.Effort)
		ev.CallID = displaySafe(ev.CallID)
		ev.Tools = displaySafeSlice(ev.Tools)
		return ev
	case SubagentProgress:
		ev.Agent = displaySafe(ev.Agent)
		ev.Session = displaySafe(ev.Session)
		ev.CallID = displaySafe(ev.CallID)
		ev.LastTool = displaySafe(ev.LastTool)
		return ev
	case SubagentFinished:
		ev.Agent = displaySafe(ev.Agent)
		ev.Session = displaySafe(ev.Session)
		ev.CallID = displaySafe(ev.CallID)
		ev.Status = displaySafe(ev.Status)
		return ev
	case RunFinished:
		ev.Reason = displaySafe(ev.Reason)
		return ev
	default:
		return ev
	}
}

// displaySafeSlice sanitizes every name in a display-only list.
func displaySafeSlice(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = displaySafe(value)
	}
	return out
}

func displaySafe(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20:
			b.WriteRune(0x2400 + r)
		case r == 0x7f:
			b.WriteRune(0x2421)
		case r >= 0x80 && r <= 0x9f:
			b.WriteRune('\uFFFD')
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c:
			// bidi controls are dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
