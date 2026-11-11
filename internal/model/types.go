// Package model defines the canonical model request/response types and the
// OpenAI dialect adapters that speak to the opencode-go inference endpoint.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Role is a canonical message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// FinishReason is the normalized reason a model response ended.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
	FinishOther         FinishReason = "other"
)

// ToolCall is a canonical tool invocation. Arguments is raw JSON supplied by
// the provider.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Message is one canonical conversation message. Tool calls are carried only
// on assistant messages; tool results carry ToolCallID and Name.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolSchema is the canonical tool declaration sent to a provider.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request is one canonical model call. The system prompt, when present, is the
// first message.
type Request struct {
	Model           string       `json:"model"`
	Messages        []Message    `json:"messages"`
	Tools           []ToolSchema `json:"tools,omitempty"`
	MaxOutputTokens int          `json:"max_output_tokens,omitempty"`
	// ThinkingEffort is the provider-neutral reasoning effort. Empty means the
	// provider/model default and omits the wire field.
	ThinkingEffort string `json:"thinking_effort,omitempty"`
}

// Usage is normalized token usage for one response. InputTokens and
// OutputTokens are totals; the cached, cache-write, and reasoning counts are
// subsets of them and are never added to Total. Reported distinguishes a
// provider-supplied usage object (including an all-zero object) from an
// absent one.
type Usage struct {
	InputTokens           int  `json:"input_tokens"`
	OutputTokens          int  `json:"output_tokens"`
	CachedInputTokens     int  `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens int  `json:"cache_write_input_tokens,omitempty"`
	ReasoningOutputTokens int  `json:"reasoning_output_tokens,omitempty"`
	Reported              bool `json:"reported"`
}

// Total returns the total tokens reported for the response. The subset counts
// are already included in the totals.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// UncachedInputTokens returns the non-cached, non-cache-write portion of the
// input. The result is clamped at zero so a malformed provider payload cannot
// produce a negative billable count.
func (u Usage) UncachedInputTokens() int {
	uncached := u.InputTokens - u.CachedInputTokens - u.CacheWriteInputTokens
	if uncached < 0 {
		return 0
	}
	return uncached
}

// validateUsage enforces the canonical subset bounds. Any violation is a
// protocol error because the transcript would otherwise record contradictory
// accounting.
func validateUsage(u Usage) error {
	if u.InputTokens < 0 || u.OutputTokens < 0 {
		return &ProtocolError{Err: errors.New("usage totals must not be negative")}
	}
	if u.CachedInputTokens < 0 || u.CacheWriteInputTokens < 0 || u.ReasoningOutputTokens < 0 {
		return &ProtocolError{Err: errors.New("usage subsets must not be negative")}
	}
	if u.CachedInputTokens+u.CacheWriteInputTokens > u.InputTokens {
		return &ProtocolError{Err: fmt.Errorf("cached input tokens %d plus cache-write tokens %d exceed input tokens %d",
			u.CachedInputTokens, u.CacheWriteInputTokens, u.InputTokens)}
	}
	if u.ReasoningOutputTokens > u.OutputTokens {
		return &ProtocolError{Err: fmt.Errorf("reasoning output tokens %d exceed output tokens %d",
			u.ReasoningOutputTokens, u.OutputTokens)}
	}
	return nil
}

// Response is one canonical model response. Streamed reports that the
// response was assembled from an SSE stream; it is display/provenance metadata,
// not transcript content. The completed response remains authoritative.
//
// Reasoning is provider thinking text. It is display-only: it is never logged,
// projected, replayed to the provider, or counted against the canonical
// response cap, and it may be absent when a provider does not expose thinking.
type Response struct {
	Text         string       `json:"text,omitempty"`
	ToolCalls    []ToolCall   `json:"tool_calls,omitempty"`
	FinishReason FinishReason `json:"finish_reason"`
	Usage        Usage        `json:"usage"`
	Reasoning    string       `json:"-"`
	Streamed     bool         `json:"-"`
}

// Completer is the provider-facing contract. Both dialect adapters implement
// it; the agent loop sees only canonical types.
type Completer interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// StreamHandler receives display-only deltas from one streaming response.
// Either callback may be nil, which discards that kind. Text is assistant text;
// Reasoning is provider thinking text. Neither is durable: the returned
// Response is authoritative and is the only value recorded in the transcript.
type StreamHandler struct {
	Text      func(string) error
	Reasoning func(string) error
}

// text and reasoning deliver one delta to the optional callbacks.
func (h StreamHandler) text(delta string) error {
	if h.Text == nil {
		return nil
	}
	return h.Text(delta)
}

func (h StreamHandler) reasoning(delta string) error {
	if h.Reasoning == nil {
		return nil
	}
	return h.Reasoning(delta)
}

// StreamCompleter is the optional streaming contract. The returned Response is
// authoritative and is the only value recorded in the transcript.
type StreamCompleter interface {
	CompleteStream(ctx context.Context, req Request, stream StreamHandler) (Response, error)
}

// MaxCanonicalResponse is the largest assembled canonical response accepted
// before any tool executes.
const MaxCanonicalResponse = 1 << 20

// MaxReasoningText is the largest thinking text retained for display. The
// adapters drop reasoning deltas beyond it so the streamed display and the
// completed response agree.
const MaxReasoningText = 64 << 10

// acceptReasoning returns the prefix of delta that fits the reasoning budget
// after used bytes, at a rune boundary. An empty result means the budget is
// exhausted.
func acceptReasoning(used int, delta string) string {
	room := MaxReasoningText - used
	if room <= 0 {
		return ""
	}
	if len(delta) <= room {
		return delta
	}
	cut := room
	for cut > 0 && !utf8.RuneStart(delta[cut]) {
		cut--
	}
	return delta[:cut]
}

// capReasoning bounds a complete reasoning string for display.
func capReasoning(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= MaxReasoningText {
		return s
	}
	cut := MaxReasoningText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// responseContentBytes counts the response fields that must be replayed
// exactly: text, tool-call IDs, names, and raw arguments.
func responseContentBytes(resp Response) int {
	n := len(resp.Text)
	for _, call := range resp.ToolCalls {
		n += len(call.ID) + len(call.Name) + len(call.Arguments)
	}
	return n
}

// Dialect identifies the OpenAI wire dialect used by a model.
type Dialect string

const (
	DialectChat      Dialect = "openai-chat"
	DialectResponses Dialect = "openai-responses"
)

// DefaultBaseURL is the legacy hosted opencode-go base URL. New sessions
// receive the documented OpenAI base URL from the catalog; this constant is
// only the fallback for an adapter call or a session with no recorded URL.
const DefaultBaseURL = "https://opencode.ai/inference/go/v1"
