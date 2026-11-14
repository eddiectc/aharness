package model

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// OpenAIChat is the dialect adapter for the OpenAI Chat Completions API.
type OpenAIChat struct {
	transport    *httpTransport
	continuation continuationCache[string]
}

// NewOpenAIChat constructs a Chat Completions adapter. An empty baseURL uses
// DefaultBaseURL. An empty token omits the Authorization header. A
// non-positive timeout selects DefaultHTTPTimeout.
func NewOpenAIChat(baseURL, token, sessionID, version string, timeout time.Duration) *OpenAIChat {
	return &OpenAIChat{
		transport:    newHTTPTransport(baseURL, "/chat/completions", token, sessionID, version, timeout),
		continuation: newContinuationCache(func(s string) int { return len(s) }),
	}
}

type chatRequest struct {
	Model           string             `json:"model"`
	Messages        []chatMessage      `json:"messages"`
	Tools           []chatTool         `json:"tools,omitempty"`
	MaxTokens       int                `json:"max_tokens,omitzero"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
	Stream          bool               `json:"stream,omitempty"`
	StreamOptions   *chatStreamOptions `json:"stream_options,omitempty"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
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
	Usage   *chatUsage   `json:"usage"`
}

type chatChoice struct {
	Message      chatResponseMessage `json:"message"`
	FinishReason string              `json:"finish_reason"`
}

type chatResponseMessage struct {
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content"`
	Reasoning        string         `json:"reasoning"`
	ToolCalls        []chatToolCall `json:"tool_calls"`
}

type chatUsage struct {
	PromptTokens            int                          `json:"prompt_tokens"`
	CompletionTokens        int                          `json:"completion_tokens"`
	PromptTokensDetails     *chatPromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *chatCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	// PromptCacheHitTokens is the DeepSeek-compatible fallback for cached
	// input when prompt_tokens_details is absent.
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens,omitempty"`
}

type chatPromptTokensDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

type chatCompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// canonical maps one wire usage object into the canonical accounting. A nil
// receiver is an absent usage object and is not reported.
func (u *chatUsage) canonical() Usage {
	if u == nil {
		return Usage{}
	}
	usage := Usage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		Reported:     true,
	}
	if details := u.PromptTokensDetails; details != nil {
		usage.CachedInputTokens = details.CachedTokens
		usage.CacheWriteInputTokens = details.CacheWriteTokens
	} else {
		usage.CachedInputTokens = u.PromptCacheHitTokens
	}
	if details := u.CompletionTokensDetails; details != nil {
		usage.ReasoningOutputTokens = details.ReasoningTokens
	}
	return usage
}

type chatChunk struct {
	Choices []chatChunkChoice `json:"choices"`
	Usage   *chatUsage        `json:"usage"`
}

type chatChunkChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type chatDelta struct {
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content"`
	Reasoning        string              `json:"reasoning"`
	ToolCalls        []chatDeltaToolCall `json:"tool_calls"`
}

type chatDeltaToolCall struct {
	Index    int               `json:"index"`
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function chatDeltaFunction `json:"function"`
}

type chatDeltaFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatStreamCall struct {
	id   string
	name string
	args strings.Builder
}

// Complete sends one canonical request through the Chat Completions wire
// format and maps the first choice back into the canonical response.
func (a *OpenAIChat) Complete(ctx context.Context, req Request) (Response, error) {
	body, err := a.transport.post(ctx, a.payload(req))
	if err != nil {
		return Response{}, err
	}
	completion, err := decodeChatCompletion(body)
	if err != nil {
		return Response{}, err
	}
	a.rememberContinuation(completion.Response, completion.reasoning)
	return completion.Response, nil
}

// CompleteStream sends a streaming Chat Completions request. Assistant-text
// and reasoning deltas are display-only; the assembled response is
// authoritative.
func (a *OpenAIChat) CompleteStream(ctx context.Context, req Request, stream StreamHandler) (Response, error) {
	payload := a.payload(req)
	payload.Stream = true
	payload.StreamOptions = &chatStreamOptions{IncludeUsage: true}
	resp, err := a.transport.postStream(ctx, payload)
	if err != nil {
		return Response{}, err
	}
	if !isEventStream(resp.Header.Get("Content-Type")) {
		body, err := readBodyCapped(resp)
		if err != nil {
			return Response{}, err
		}
		completion, err := decodeChatCompletion(body)
		if err != nil {
			return Response{}, err
		}
		a.rememberContinuation(completion.Response, completion.reasoning)
		return completion.Response, nil
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	completion, err := consumeChatStreamFull(resp.Body, stream)
	if err != nil {
		return Response{}, err
	}
	a.rememberContinuation(completion.Response, completion.reasoning)
	return completion.Response, nil
}

func (a *OpenAIChat) payload(req Request) chatRequest {
	payload := chatRequest{
		Model:           req.Model,
		Messages:        make([]chatMessage, 0, len(req.Messages)),
		Tools:           make([]chatTool, 0, len(req.Tools)),
		MaxTokens:       req.MaxOutputTokens,
		ReasoningEffort: req.ThinkingEffort,
	}
	for _, msg := range req.Messages {
		wire := chatMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}
		if msg.Role == RoleAssistant {
			// DeepSeek-compatible endpoints require the reasoning_content of
			// earlier assistant turns when the request carries tools. Only
			// messages this adapter itself produced have a retained copy.
			if reasoning, ok := a.continuation.lookup(continuationKey(msg.Content, msg.ToolCalls)); ok {
				wire.ReasoningContent = reasoning
			}
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
	return payload
}

// rememberContinuation retains one response's thinking for the next tool round.
func (a *OpenAIChat) rememberContinuation(resp Response, reasoning string) {
	if reasoning == "" {
		return
	}
	a.continuation.remember(continuationKey(resp.Text, resp.ToolCalls), reasoning)
}

// chatCompletion is one decoded Chat response plus the adapter-owned thinking
// copy needed to continue a tool round. Response.Reasoning is the display copy.
type chatCompletion struct {
	Response  Response
	reasoning string
}

func decodeChatResponse(body []byte) (Response, error) {
	completion, err := decodeChatCompletion(body)
	if err != nil {
		return Response{}, err
	}
	return completion.Response, nil
}

func decodeChatCompletion(body []byte) (chatCompletion, error) {
	var decoded chatResponse
	if err := jsonv2.Unmarshal(body, &decoded); err != nil {
		return chatCompletion{}, &ProtocolError{Err: fmt.Errorf("decode chat completion: %w", err)}
	}
	resp, reasoning, err := decoded.canonical()
	if err != nil {
		return chatCompletion{}, err
	}
	return chatCompletion{Response: resp, reasoning: reasoning}, nil
}

// consumeChatStream returns the canonical response for callers that do not
// need the adapter-owned continuation copy.
func consumeChatStream(r io.Reader, stream StreamHandler) (Response, error) {
	completion, err := consumeChatStreamFull(r, stream)
	if err != nil {
		return Response{}, err
	}
	return completion.Response, nil
}

func consumeChatStreamFull(r io.Reader, stream StreamHandler) (chatCompletion, error) {
	dec := newSSEDecoder(r)
	var (
		text          strings.Builder
		emittedText   strings.Builder
		reasoning     strings.Builder
		wireReasoning strings.Builder
		emittedBytes  int
		argBytes      int
		calls         = make(map[int]*chatStreamCall)
		order         []int
		finishReason  string
		usage         *chatUsage
	)
	fail := func(err error) (chatCompletion, error) {
		if emittedBytes > 0 {
			return chatCompletion{}, &PartialError{Err: err, Text: partialStreamText(emittedText.String()), Bytes: emittedBytes}
		}
		return chatCompletion{}, err
	}

	for {
		event, err := dec.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(err)
		}
		data := string(event.Data)
		if data == "[DONE]" {
			break
		}
		var chunk chatChunk
		if err := jsonv2.Unmarshal(event.Data, &chunk); err != nil {
			return fail(&ProtocolError{Err: fmt.Errorf("decode chat stream event: %w", err)})
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			finishReason = *choice.FinishReason
		}
		// DeepSeek-compatible servers stream thinking as reasoning_content;
		// OpenAI-compatible servers use reasoning. The display copy is capped
		// at MaxReasoningText; the adapter-owned copy must stay complete enough
		// to replay in the next tool round.
		for _, delta := range [...]string{choice.Delta.ReasoningContent, choice.Delta.Reasoning} {
			if delta == "" {
				continue
			}
			if raw := acceptText(wireReasoning.Len(), maxContinuationBytes, delta); raw != "" {
				wireReasoning.WriteString(raw)
			}
			delta = acceptReasoning(reasoning.Len(), delta)
			if delta == "" {
				continue
			}
			reasoning.WriteString(delta)
			if err := stream.reasoning(delta); err != nil {
				return chatCompletion{}, err
			}
		}
		if delta := choice.Delta.Content; delta != "" {
			if text.Len()+argBytes+len(delta) > MaxCanonicalResponse {
				return fail(&ProtocolError{Err: fmt.Errorf("assembled response exceeds %d bytes", MaxCanonicalResponse)})
			}
			text.WriteString(delta)
			emittedText.WriteString(delta)
			emittedBytes += len(delta)
			if err := stream.text(delta); err != nil {
				return chatCompletion{}, err
			}
		}
		for _, fragment := range choice.Delta.ToolCalls {
			acc := calls[fragment.Index]
			if acc == nil {
				acc = &chatStreamCall{}
				calls[fragment.Index] = acc
				order = append(order, fragment.Index)
			}
			if fragment.ID != "" {
				if acc.id != "" && acc.id != fragment.ID {
					return fail(&ProtocolError{Err: fmt.Errorf("tool call index %d changed id from %q to %q", fragment.Index, acc.id, fragment.ID)})
				}
				acc.id = fragment.ID
			}
			if fragment.Function.Name != "" {
				if acc.name != "" && acc.name != fragment.Function.Name {
					return fail(&ProtocolError{Err: fmt.Errorf("tool call index %d changed name from %q to %q", fragment.Index, acc.name, fragment.Function.Name)})
				}
				acc.name = fragment.Function.Name
			}
			if fragment.Function.Arguments != "" {
				if text.Len()+argBytes+len(fragment.Function.Arguments) > MaxCanonicalResponse {
					return fail(&ProtocolError{Err: fmt.Errorf("assembled response exceeds %d bytes", MaxCanonicalResponse)})
				}
				acc.args.WriteString(fragment.Function.Arguments)
				argBytes += len(fragment.Function.Arguments)
			}
		}
	}

	if usage == nil {
		return fail(&ProtocolError{Err: errors.New("chat stream completed without usage")})
	}

	usageValue := usage.canonical()
	if err := validateUsage(usageValue); err != nil {
		return fail(err)
	}

	out := Response{
		Text:      text.String(),
		Reasoning: reasoning.String(),
		Usage:     usageValue,
		Streamed:  true,
	}
	seen := make(map[string]struct{}, len(calls))
	for _, index := range order {
		acc := calls[index]
		if acc.id == "" {
			return fail(&ProtocolError{Err: fmt.Errorf("tool call index %d is missing an id", index)})
		}
		if acc.name == "" {
			return fail(&ProtocolError{Err: fmt.Errorf("tool call %q is missing a name", acc.id)})
		}
		if _, ok := seen[acc.id]; ok {
			return fail(&ProtocolError{Err: fmt.Errorf("duplicate tool call id %q", acc.id)})
		}
		seen[acc.id] = struct{}{}
		args := acc.args.String()
		if !jsontext.Value(args).IsValid() {
			return fail(&ProtocolError{Err: fmt.Errorf("tool call %q has invalid json arguments", acc.id)})
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        acc.id,
			Name:      acc.name,
			Arguments: json.RawMessage(args),
		})
	}
	if responseContentBytes(out) > MaxCanonicalResponse {
		return fail(&ProtocolError{Err: fmt.Errorf("assembled response exceeds %d bytes", MaxCanonicalResponse)})
	}
	out.FinishReason = chatFinishReason(finishReason, len(out.ToolCalls) > 0)
	return chatCompletion{Response: out, reasoning: wireReasoning.String()}, nil
}

// canonical maps the first wire choice into a canonical response plus the
// adapter-owned full reasoning copy. A provided tool-call ID is trusted only
// when it is present, unique, and named.
func (r chatResponse) canonical() (Response, string, error) {
	if len(r.Choices) == 0 {
		return Response{}, "", &ProtocolError{Err: errors.New("chat completion has no choices")}
	}

	choice := r.Choices[0]
	reasoning := choice.Message.ReasoningContent
	if reasoning == "" {
		reasoning = choice.Message.Reasoning
	}
	out := Response{
		Text:      choice.Message.Content,
		Reasoning: capReasoning(reasoning),
		Usage:     r.Usage.canonical(),
	}
	if err := validateUsage(out.Usage); err != nil {
		return Response{}, "", err
	}
	seen := make(map[string]struct{}, len(choice.Message.ToolCalls))
	for _, call := range choice.Message.ToolCalls {
		if call.ID == "" {
			return Response{}, "", &ProtocolError{Err: errors.New("tool call is missing an id")}
		}
		if _, ok := seen[call.ID]; ok {
			return Response{}, "", &ProtocolError{Err: fmt.Errorf("duplicate tool call id %q", call.ID)}
		}
		if call.Function.Name == "" {
			return Response{}, "", &ProtocolError{Err: fmt.Errorf("tool call %q is missing a name", call.ID)}
		}
		if !jsontext.Value(call.Function.Arguments).IsValid() {
			return Response{}, "", &ProtocolError{Err: fmt.Errorf("tool call %q has invalid json arguments", call.ID)}
		}
		seen[call.ID] = struct{}{}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		})
	}

	out.FinishReason = chatFinishReason(choice.FinishReason, len(out.ToolCalls) > 0)
	return out, capContinuationReasoning(reasoning), nil
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
