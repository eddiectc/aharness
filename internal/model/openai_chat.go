package model

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
)

// OpenAIChat is the dialect adapter for the OpenAI Chat Completions API.
type OpenAIChat struct {
	transport *httpTransport
}

// NewOpenAIChat constructs a Chat Completions adapter. An empty baseURL uses
// DefaultBaseURL. An empty token omits the Authorization header.
func NewOpenAIChat(baseURL, token, sessionID, version string) *OpenAIChat {
	return &OpenAIChat{
		transport: newHTTPTransport(baseURL, "/chat/completions", token, sessionID, version),
	}
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	Tools     []chatTool    `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type chatChoice struct {
	Message      chatResponseMessage `json:"message"`
	FinishReason string              `json:"finish_reason"`
}

type chatResponseMessage struct {
	Content   string         `json:"content"`
	ToolCalls []chatToolCall `json:"tool_calls"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// Complete sends one canonical request through the Chat Completions wire
// format and maps the first choice back into the canonical response.
func (a *OpenAIChat) Complete(ctx context.Context, req Request) (Response, error) {
	payload := chatRequest{
		Model:     req.Model,
		Messages:  make([]chatMessage, 0, len(req.Messages)),
		Tools:     make([]chatTool, 0, len(req.Tools)),
		MaxTokens: req.MaxOutputTokens,
	}
	for _, msg := range req.Messages {
		wire := chatMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}
		for _, call := range msg.ToolCalls {
			wire.ToolCalls = append(wire.ToolCalls, chatToolCall{
				ID:   call.ID,
				Type: "function",
				Function: chatFunction{
					Name:      call.Name,
					Arguments: string(call.Arguments),
				},
			})
		}
		payload.Messages = append(payload.Messages, wire)
	}
	for _, tool := range req.Tools {
		payload.Tools = append(payload.Tools, chatTool{
			Type:     "function",
			Function: chatToolFunction(tool),
		})
	}

	body, err := a.transport.post(ctx, payload)
	if err != nil {
		return Response{}, err
	}

	var decoded chatResponse
	if err := jsonv2.Unmarshal(body, &decoded); err != nil {
		return Response{}, &ProtocolError{Err: fmt.Errorf("decode chat completion: %w", err)}
	}
	return decoded.canonical()
}

// canonical maps the first wire choice into a canonical response. A provided
// tool-call ID is trusted only when it is present, unique, and named.
func (r chatResponse) canonical() (Response, error) {
	if len(r.Choices) == 0 {
		return Response{}, &ProtocolError{Err: errors.New("chat completion has no choices")}
	}

	choice := r.Choices[0]
	out := Response{
		Text: choice.Message.Content,
		Usage: Usage{
			InputTokens:  r.Usage.PromptTokens,
			OutputTokens: r.Usage.CompletionTokens,
		},
	}
	seen := make(map[string]struct{}, len(choice.Message.ToolCalls))
	for _, call := range choice.Message.ToolCalls {
		if call.ID == "" {
			return Response{}, &ProtocolError{Err: errors.New("tool call is missing an id")}
		}
		if _, ok := seen[call.ID]; ok {
			return Response{}, &ProtocolError{Err: fmt.Errorf("duplicate tool call id %q", call.ID)}
		}
		if call.Function.Name == "" {
			return Response{}, &ProtocolError{Err: fmt.Errorf("tool call %q is missing a name", call.ID)}
		}
		if !jsontext.Value(call.Function.Arguments).IsValid() {
			return Response{}, &ProtocolError{Err: fmt.Errorf("tool call %q has invalid json arguments", call.ID)}
		}
		seen[call.ID] = struct{}{}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		})
	}

	out.FinishReason = chatFinishReason(choice.FinishReason, len(out.ToolCalls) > 0)
	return out, nil
}

// chatFinishReason normalizes a Chat Completions finish reason. A response
// that carries tool calls but reports stop (or no reason) is normalized to
// tool_calls so the loop executes the calls.
func chatFinishReason(reason string, hasToolCalls bool) FinishReason {
	var finish FinishReason
	switch reason {
	case "", "stop":
		finish = FinishStop
	case "length":
		finish = FinishLength
	case "tool_calls", "function_call":
		finish = FinishToolCalls
	case "content_filter":
		finish = FinishContentFilter
	default:
		finish = FinishOther
	}
	if hasToolCalls && finish == FinishStop {
		finish = FinishToolCalls
	}
	return finish
}
