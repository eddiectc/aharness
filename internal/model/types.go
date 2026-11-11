// Package model defines the canonical model request/response types and the
// OpenAI dialect adapters that speak to the opencode-go inference endpoint.
package model

import (
	"context"
	"encoding/json"
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
}

// Usage is normalized token usage for one response.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Total returns the total tokens reported for the response.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// Response is one canonical model response. Streamed reports that the
// response was assembled from an SSE stream; it is display/provenance metadata,
// not transcript content. The completed response remains authoritative.
type Response struct {
	Text         string       `json:"text,omitempty"`
	ToolCalls    []ToolCall   `json:"tool_calls,omitempty"`
	FinishReason FinishReason `json:"finish_reason"`
	Usage        Usage        `json:"usage"`
	Streamed     bool         `json:"-"`
}

// Completer is the provider-facing contract. Both dialect adapters implement
// it; the agent loop sees only canonical types.
type Completer interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// StreamCompleter is the optional streaming contract. emit receives
// display-only assistant-text deltas; the returned Response is authoritative
// and is the only value recorded in the transcript.
type StreamCompleter interface {
	CompleteStream(ctx context.Context, req Request, emit func(string) error) (Response, error)
}

// MaxCanonicalResponse is the largest assembled canonical response accepted
// before any tool executes.
const MaxCanonicalResponse = 1 << 20

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
