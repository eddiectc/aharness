package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"aharness/internal/model"
	"aharness/internal/tools"
)

// Event payloads produced by the runner. internal/session owns the envelope;
// the agent owns the JSON object stored in Event.Data for each event type.

type sessionStartData struct {
	PromptVersion    string `json:"prompt_version"`
	ToolsetVersion   string `json:"toolset_version"`
	Workspace        string `json:"workspace"`
	StateDir         string `json:"state_dir"`
	Provider         string `json:"provider"`
	Dialect          string `json:"dialect"`
	Model            string `json:"model"`
	BaseURL          string `json:"base_url,omitempty"`
	Limits           Limits `json:"limits"`
	MaxTurns         int    `json:"max_turns"`
	MaxTotalTokens   int    `json:"max_total_tokens"`
	SessionTimeoutMS int64  `json:"session_timeout_ms"`
	ModelTimeoutMS   int64  `json:"model_timeout_ms"`
	HarnessVersion   string `json:"harness_version"`
	ThinkingEffort   string `json:"thinking_effort,omitempty"`
	ParentSession    string `json:"parent_session,omitempty"`
	ParentSeq        uint64 `json:"parent_seq,omitzero"`
}

type runStartData struct {
	RunID            string        `json:"run_id"`
	ResumedFromSeq   uint64        `json:"resumed_from_seq"`
	Provider         string        `json:"provider"`
	Model            string        `json:"model"`
	Dialect          string        `json:"dialect"`
	BaseURL          string        `json:"base_url"`
	Limits           Limits        `json:"limits"`
	MaxTurns         int           `json:"max_turns"`
	MaxTotalTokens   int           `json:"max_total_tokens"`
	SessionTimeoutMS int64         `json:"session_timeout_ms"`
	ModelTimeoutMS   int64         `json:"model_timeout_ms"`
	PromptVersion    string        `json:"prompt_version"`
	ToolsetVersion   string        `json:"toolset_version"`
	HarnessVersion   string        `json:"harness_version"`
	Interactive      bool          `json:"interactive"`
	Stream           bool          `json:"stream"`
	ThinkingEffort   string        `json:"thinking_effort,omitempty"`
	RequireUsage     bool          `json:"require_usage"`
	Pricing          model.Pricing `json:"pricing"`
}

type userMessageData struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type modelRequestData struct {
	Model                string             `json:"model"`
	Messages             []model.Message    `json:"messages,omitempty"`
	Tools                []model.ToolSchema `json:"tools,omitempty"`
	MaxOutputTokens      int                `json:"max_output_tokens,omitzero"`
	ThinkingEffort       string             `json:"thinking_effort,omitempty"`
	EstimatedInputTokens int                `json:"estimated_input_tokens,omitzero"`
	MessageCount         int                `json:"message_count,omitzero"`
	Truncated            bool               `json:"truncated,omitzero"`
}

type loggedToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type modelResponseData struct {
	Text         string             `json:"text,omitempty"`
	ToolCalls    []loggedToolCall   `json:"tool_calls,omitempty"`
	FinishReason model.FinishReason `json:"finish_reason"`
	Usage        model.Usage        `json:"usage"`
	// CostMicroUSD is present only when the run's pricing snapshot could
	// price this response; nil distinguishes an unpriced response from a
	// priced zero-cost response.
	CostMicroUSD *int64 `json:"cost_micro_usd,omitempty"`
	LatencyMS    int64  `json:"latency_ms"`
	Attempt      int    `json:"attempt"`
	Streamed     bool   `json:"streamed"`
}

type modelRetryData struct {
	Attempt int    `json:"attempt"`
	Class   string `json:"class"`
	Status  int    `json:"status,omitzero"`
	DelayMS int64  `json:"delay_ms"`
}

type toolResultData struct {
	CallID     string          `json:"call_id"`
	Name       string          `json:"name"`
	OK         bool            `json:"ok"`
	Code       tools.Code      `json:"code"`
	Output     string          `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	ExitCode   *int            `json:"exit_code,omitempty"`
	DurationMS int64           `json:"duration_ms,omitzero"`
	Truncated  bool            `json:"truncated,omitzero"`
	TimedOut   bool            `json:"timed_out,omitzero"`
	Details    json.RawMessage `json:"details,omitempty"`
}

type compactionData struct {
	BeforeTokens  int    `json:"before_tokens"`
	AfterTokens   int    `json:"after_tokens"`
	ElidedResults int    `json:"elided_results"`
	Policy        string `json:"policy"`
}

type limitData struct {
	Kind       string `json:"kind"`
	Configured int64  `json:"configured,omitzero"`
	Observed   int64  `json:"observed,omitzero"`
}

type interruptData struct {
	Operation   string `json:"operation"`
	CallID      string `json:"call_id,omitempty"`
	Name        string `json:"name,omitempty"`
	PartialText string `json:"partial_text,omitempty"`
	Reason      string `json:"reason"`
}

type errorData struct {
	Class        string `json:"class"`
	Message      string `json:"message"`
	PartialText  string `json:"partial_text,omitempty"`
	PartialBytes int    `json:"partial_bytes,omitzero"`
}

type finalData struct {
	Status StopReason `json:"status"`
	Text   string     `json:"text,omitempty"`
}

type runEndData struct {
	Status     StopReason  `json:"status"`
	Reason     string      `json:"reason"`
	RunTurns   int         `json:"run_turns"`
	RunUsage   model.Usage `json:"run_usage"`
	Turns      int         `json:"turns"`
	Usage      model.Usage `json:"usage"`
	DurationMS int64       `json:"duration_ms"`
	// Cost fields are observational estimates. Completeness flags are false
	// when any model response in the run or cumulative session lacked a
	// price.
	RunCostMicroUSD int64 `json:"run_cost_micro_usd,omitempty"`
	CostMicroUSD    int64 `json:"cost_micro_usd,omitempty"`
	RunCostComplete bool  `json:"run_cost_complete"`
	CostComplete    bool  `json:"cost_complete"`
}

// User-message sources recorded in the event log.
const (
	sourceContinuationHint = "continuation-hint"
	sourceStuckHint        = "stuck-hint"
	sourceSteer            = "steer"
	sourceInterruptHint    = "interrupt-hint"
)

// Interrupt operations recorded in interrupt events.
const (
	operationModel = "model"
	operationTool  = "tool"
)

// Limit kinds recorded in limit events.
const (
	limitMaxTurns          = "max_turns"
	limitMaxTotalTokens    = "max_total_tokens"
	limitMaxInputTokens    = "max_input_tokens"
	limitSessionTimeout    = "session_timeout"
	limitConsecutiveLength = "consecutive_length"
	limitContextOverflow   = "context_overflow"
)

// Compaction policies recorded in compaction events.
const (
	policyCompaction       = "projection"
	policyProviderOverflow = "provider-overflow"
)

// Stable error classes recorded in error events.
const (
	errorClassHTTP          = "http"
	errorClassTransport     = "transport"
	errorClassProtocol      = "protocol"
	errorClassContentFilter = "content_filter"
	errorClassOverflow      = "context_overflow"
	errorClassInternal      = "internal"
)

const (
	maxEventTextBytes     = 64 << 10
	maxErrorEventBytes    = 2 << 10
	maxModelRequestBytes  = 1 << 20
	maxPanicStackBytes    = 4 << 10
	maxPanicMessageBytes  = 1 << 10
	maxModelResponseBytes = 1 << 20
	maxPartialTextBytes   = 64 << 10
	maxSteeringBytes      = 64 << 10
)

// capText sanitizes invalid UTF-8, truncates at a byte limit on a rune
// boundary, and marks the truncation.
func capText(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n[truncated %d bytes]", len(s)-cut)
}

// safeArguments makes provider-supplied JSON safe to marshal into the session
// log. Valid JSON is canonicalized; invalid or duplicate-name JSON is
// preserved as a JSON string so the log can still record the failed call.
func safeArguments(raw json.RawMessage) json.RawMessage {
	s := strings.ToValidUTF8(string(raw), "\uFFFD")
	if strings.TrimSpace(s) == "" {
		return json.RawMessage("null")
	}
	canonical, err := canonicalJSON(json.RawMessage(s))
	if err == nil {
		return json.RawMessage(canonical)
	}
	quoted, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage("null")
	}
	return quoted
}

func loggableToolCalls(calls []model.ToolCall) []loggedToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]loggedToolCall, len(calls))
	for i, call := range calls {
		out[i] = loggedToolCall{
			ID:        strings.ToValidUTF8(call.ID, "\uFFFD"),
			Name:      strings.ToValidUTF8(call.Name, "\uFFFD"),
			Arguments: safeArguments(call.Arguments),
		}
	}
	return out
}

// responseBytes counts the canonical response content that must be recorded
// so a resumed run can replay exactly what was executed.
func responseBytes(resp model.Response) int {
	n := len(resp.Text)
	for _, call := range resp.ToolCalls {
		n += len(call.ID) + len(call.Name) + len(call.Arguments)
	}
	return n
}
