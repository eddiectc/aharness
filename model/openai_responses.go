package model

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIResponses is the dialect adapter for the OpenAI Responses API.
type OpenAIResponses struct {
	transport    *httpTransport
	continuation continuationCache[[]jsontext.Value]
}

// NewOpenAIResponses constructs a Responses adapter. An empty baseURL uses
// DefaultBaseURL. An empty token omits the Authorization header. A
// non-positive timeout selects DefaultHTTPTimeout.
func NewOpenAIResponses(baseURL, token, sessionID, version string, timeout time.Duration) *OpenAIResponses {
	return newOpenAIResponses(baseURL, token, sessionID, version, timeout, nil)
}

// NewOpenAIResponsesWithClient constructs a Responses adapter that uses the
// caller's HTTP client. A nil client selects the default bounded client.
func NewOpenAIResponsesWithClient(baseURL, token, sessionID, version string, timeout time.Duration, client *http.Client) *OpenAIResponses {
	return newOpenAIResponses(baseURL, token, sessionID, version, timeout, client)
}

func newOpenAIResponses(baseURL, token, sessionID, version string, timeout time.Duration, client *http.Client) *OpenAIResponses {
	return &OpenAIResponses{
		transport:    newHTTPTransportWithClient(baseURL, "/responses", token, sessionID, version, timeout, client),
		continuation: newContinuationCache(responsesReasoningBytes),
	}
}

// WithSessionID returns a copy of the adapter that sends the given session ID
// in its provider header. The continuation cache is not shared, so a caller
// must build one adapter per session.
func (a *OpenAIResponses) WithSessionID(sessionID string) *OpenAIResponses {
	transport := *a.transport
	transport.sessionID = sessionID
	return &OpenAIResponses{
		transport:    &transport,
		continuation: newContinuationCache(responsesReasoningBytes),
	}
}

// WithUserAgent returns a copy of the adapter that sends the given User-Agent
// verbatim. An empty value keeps the default "aharness/<version>" identity.
func (a *OpenAIResponses) WithUserAgent(userAgent string) *OpenAIResponses {
	if userAgent == "" {
		return a
	}
	transport := *a.transport
	transport.userAgent = userAgent
	return &OpenAIResponses{
		transport:    &transport,
		continuation: newContinuationCache(responsesReasoningBytes),
	}
}

type responsesRequest struct {
	Model           string              `json:"model"`
	Instructions    string              `json:"instructions,omitempty"`
	Input           []any               `json:"input"`
	Tools           []responsesTool     `json:"tools,omitempty"`
	MaxOutputTokens int                 `json:"max_output_tokens,omitzero"`
	Reasoning       *responsesReasoning `json:"reasoning,omitempty"`
	Include         []string            `json:"include,omitempty"`
	Store           bool                `json:"store"`
	Stream          bool                `json:"stream,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesInputMessage struct {
	Type    string                 `json:"type,omitempty"`
	Role    string                 `json:"role"`
	Content []responsesContentPart `json:"content"`
}

type responsesFunctionCallInput struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionCallOutputInput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responsesResponse struct {
	Output            []jsontext.Value     `json:"output"`
	Status            string               `json:"status"`
	IncompleteDetails *responsesIncomplete `json:"incomplete_details"`
	Error             *responsesError      `json:"error"`
	Usage             *responsesUsage      `json:"usage"`
}

type responsesIncomplete struct {
	Reason string `json:"reason"`
}

type responsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type responsesUsage struct {
	InputTokens         int                           `json:"input_tokens"`
	InputTokensDetails  *responsesInputTokensDetails  `json:"input_tokens_details,omitempty"`
	OutputTokens        int                           `json:"output_tokens"`
	OutputTokensDetails *responsesOutputTokensDetails `json:"output_tokens_details,omitempty"`
}

type responsesInputTokensDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

type responsesOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// canonical maps one wire usage object into the canonical accounting.
func (u *responsesUsage) canonical() Usage {
	if u == nil {
		return Usage{}
	}
	usage := Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		Reported:     true,
	}
	if details := u.InputTokensDetails; details != nil {
		usage.CachedInputTokens = details.CachedTokens
		usage.CacheWriteInputTokens = details.CacheWriteTokens
	}
	if details := u.OutputTokensDetails; details != nil {
		usage.ReasoningOutputTokens = details.ReasoningTokens
	}
	return usage
}

type responsesOutputKind struct {
	Type string `json:"type"`
}

type responsesMessageItem struct {
	Content []responsesOutputPart `json:"content"`
}

type responsesFunctionCallItem struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesReasoningItem struct {
	Summary []responsesSummaryPart `json:"summary"`
	Content []responsesSummaryPart `json:"content"`
}

type responsesOutputPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesStreamEvent struct {
	Type     string             `json:"type"`
	Response *responsesResponse `json:"response"`
	Delta    string             `json:"delta"`
	Text     string             `json:"text"`
	Error    *responsesError    `json:"error"`
	Code     string             `json:"code"`
	Message  string             `json:"message"`
}

// Complete sends one canonical request through the Responses wire format and
// maps the returned output items back into the canonical response. The full
// projected input is replayed; server-side conversation state is never used.
func (a *OpenAIResponses) Complete(ctx context.Context, req Request) (Response, error) {
	body, err := a.transport.post(ctx, a.payload(req))
	if err != nil {
		return Response{}, err
	}
	completion, err := decodeResponsesCompletion(body, a.transport.token)
	if err != nil {
		return Response{}, err
	}
	a.rememberContinuation(completion.Response, completion.reasoningItems)
	return completion.Response, nil
}

// CompleteStream sends a streaming Responses request. Text and reasoning
// summary deltas are display-only; the terminal completed response is
// authoritative.
func (a *OpenAIResponses) CompleteStream(ctx context.Context, req Request, stream StreamHandler) (Response, error) {
	payload := a.payload(req)
	payload.Stream = true
	resp, err := a.transport.postStream(ctx, payload)
	if err != nil {
		return Response{}, err
	}
	if !isEventStream(resp.Header.Get("Content-Type")) {
		body, err := readBodyCapped(resp)
		if err != nil {
			return Response{}, err
		}
		completion, err := decodeResponsesCompletion(body, a.transport.token)
		if err != nil {
			return Response{}, err
		}
		a.rememberContinuation(completion.Response, completion.reasoningItems)
		return completion.Response, nil
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	completion, err := consumeResponsesStreamFull(resp.Body, stream, a.transport.token)
	if err != nil {
		return Response{}, err
	}
	a.rememberContinuation(completion.Response, completion.reasoningItems)
	return completion.Response, nil
}

func (a *OpenAIResponses) payload(req Request) responsesRequest {
	payload := responsesRequest{
		Model:           req.Model,
		Input:           make([]any, 0, len(req.Messages)),
		Tools:           make([]responsesTool, 0, len(req.Tools)),
		MaxOutputTokens: req.MaxOutputTokens,
		// Stateless replay cannot resolve reasoning item IDs. Ask OpenAI-
		// compatible endpoints for encrypted reasoning so the items can be
		// replayed; servers that return plaintext reasoning ignore this field.
		Include: []string{"reasoning.encrypted_content"},
	}
	if req.ThinkingEffort != "" {
		payload.Reasoning = &responsesReasoning{Effort: req.ThinkingEffort}
	}
	for i, msg := range req.Messages {
		switch msg.Role {
		case RoleSystem:
			if i == 0 {
				payload.Instructions = msg.Content
			}
		case RoleUser:
			payload.Input = append(payload.Input, responsesInputMessage{
				Role: "user",
				Content: []responsesContentPart{{
					Type: "input_text",
					Text: msg.Content,
				}},
			})
		case RoleAssistant:
			if msg.Content != "" || len(msg.ToolCalls) > 0 {
				// A continuation item must precede the assistant output it
				// explains; a trailing reasoning item is not a valid input.
				if items, ok := a.continuation.lookup(continuationKey(msg.Content, msg.ToolCalls)); ok {
					for _, item := range items {
						payload.Input = append(payload.Input, item)
					}
				}
			}
			if msg.Content != "" {
				payload.Input = append(payload.Input, responsesInputMessage{
					Type:    "message",
					Role:    "assistant",
					Content: []responsesContentPart{{Type: "output_text", Text: msg.Content}},
				})
			}
			for _, call := range msg.ToolCalls {
				payload.Input = append(payload.Input, responsesFunctionCallInput{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      call.Name,
					Arguments: string(call.Arguments),
				})
			}
		case RoleTool:
			payload.Input = append(payload.Input, responsesFunctionCallOutputInput{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: msg.Content,
			})
		}
	}
	for _, tool := range req.Tools {
		payload.Tools = append(payload.Tools, responsesTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	return payload
}

// responsesCompletion is one decoded Responses response plus the raw reasoning
// items that a stateless tool round must replay. Response.Reasoning is the
// display text.
type responsesCompletion struct {
	Response       Response
	reasoningItems []jsontext.Value
}

// rememberContinuation retains one response's reasoning items for the next
// tool round.
func (a *OpenAIResponses) rememberContinuation(resp Response, items []jsontext.Value) {
	if len(items) == 0 {
		return
	}
	a.continuation.remember(continuationKey(resp.Text, resp.ToolCalls), items)
}

// responsesReasoningBytes is the cache weight of one response's raw items.
func responsesReasoningBytes(items []jsontext.Value) int {
	n := 0
	for _, item := range items {
		n += len(item)
	}
	return n
}

func decodeResponsesResponse(body []byte, token string) (Response, error) {
	completion, err := decodeResponsesCompletion(body, token)
	if err != nil {
		return Response{}, err
	}
	return completion.Response, nil
}

func decodeResponsesCompletion(body []byte, token string) (responsesCompletion, error) {
	var decoded responsesResponse
	if err := jsonv2.Unmarshal(body, &decoded); err != nil {
		return responsesCompletion{}, &ProtocolError{Err: fmt.Errorf("decode responses completion: %w", err)}
	}
	resp, items, err := decoded.canonical(token)
	if err != nil {
		return responsesCompletion{}, err
	}
	return responsesCompletion{Response: resp, reasoningItems: items}, nil
}

func consumeResponsesStream(r io.Reader, stream StreamHandler, token string) (Response, error) {
	completion, err := consumeResponsesStreamFull(r, stream, token)
	if err != nil {
		return Response{}, err
	}
	return completion.Response, nil
}

func consumeResponsesStreamFull(r io.Reader, stream StreamHandler, token string) (responsesCompletion, error) {
	dec := newSSEDecoder(r)
	var (
		emittedText  strings.Builder
		emittedBytes int
		reasoning    strings.Builder
	)
	fail := func(err error) (responsesCompletion, error) {
		if emittedBytes > 0 {
			return responsesCompletion{}, &PartialError{Err: err, Text: partialStreamText(emittedText.String()), Bytes: emittedBytes}
		}
		return responsesCompletion{}, err
	}

	for {
		event, err := dec.Next()
		if errors.Is(err, io.EOF) {
			return fail(&ProtocolError{Err: errors.New("responses stream ended before a terminal event")})
		}
		if err != nil {
			return fail(err)
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := jsonv2.Unmarshal(event.Data, &head); err != nil {
			return fail(&ProtocolError{Err: fmt.Errorf("decode responses stream event: %w", err)})
		}
		var decoded responsesStreamEvent
		switch head.Type {
		case "response.output_text.delta":
			if err := jsonv2.Unmarshal(event.Data, &decoded); err != nil {
				return fail(&ProtocolError{Err: fmt.Errorf("decode output text delta: %w", err)})
			}
			if decoded.Delta == "" {
				continue
			}
			if emittedBytes+len(decoded.Delta) > MaxCanonicalResponse {
				return fail(&ProtocolError{Err: fmt.Errorf("streamed text exceeds %d bytes", MaxCanonicalResponse)})
			}
			emittedText.WriteString(decoded.Delta)
			emittedBytes += len(decoded.Delta)
			if err := stream.text(decoded.Delta); err != nil {
				return responsesCompletion{}, err
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			// Thinking summaries and raw reasoning text are display-only.
			if err := jsonv2.Unmarshal(event.Data, &decoded); err != nil {
				return fail(&ProtocolError{Err: fmt.Errorf("decode reasoning delta: %w", err)})
			}
			delta := decoded.Delta
			if delta == "" {
				continue
			}
			delta = acceptReasoning(reasoning.Len(), delta)
			if delta == "" {
				continue
			}
			reasoning.WriteString(delta)
			if err := stream.reasoning(delta); err != nil {
				return responsesCompletion{}, err
			}
		case "response.function_call_arguments.delta":
			// Tool calls are assembled from the terminal response event; the
			// fragment is accepted for protocol compatibility.
			if err := jsonv2.Unmarshal(event.Data, &decoded); err != nil {
				return fail(&ProtocolError{Err: fmt.Errorf("decode function call arguments delta: %w", err)})
			}
		case "response.completed", "response.incomplete":
			if err := jsonv2.Unmarshal(event.Data, &decoded); err != nil {
				return fail(&ProtocolError{Err: fmt.Errorf("decode terminal response event: %w", err)})
			}
			if decoded.Response == nil {
				return fail(&ProtocolError{Err: fmt.Errorf("%s event has no response", head.Type)})
			}
			if decoded.Response.Usage == nil {
				return fail(&ProtocolError{Err: fmt.Errorf("%s event has no usage", head.Type)})
			}
			resp, items, err := decoded.Response.canonical(token)
			if err != nil {
				return fail(err)
			}
			// The terminal item text is authoritative; the accumulated deltas
			// are the fallback for servers that summarize without an item.
			if resp.Reasoning == "" {
				resp.Reasoning = reasoning.String()
			}
			if responseContentBytes(resp) > MaxCanonicalResponse {
				return fail(&ProtocolError{Err: fmt.Errorf("assembled response exceeds %d bytes", MaxCanonicalResponse)})
			}
			resp.Streamed = true
			return responsesCompletion{Response: resp, reasoningItems: items}, nil
		case "response.failed":
			if err := jsonv2.Unmarshal(event.Data, &decoded); err != nil {
				return fail(&ProtocolError{Err: fmt.Errorf("decode failed response event: %w", err)})
			}
			detail := "response failed"
			switch {
			case decoded.Response != nil && decoded.Response.Error != nil && decoded.Response.Error.Message != "":
				detail += ": " + decoded.Response.Error.Message
			case decoded.Response != nil && decoded.Response.Error != nil && decoded.Response.Error.Code != "":
				detail += ": " + decoded.Response.Error.Code
			case decoded.Error != nil && decoded.Error.Message != "":
				detail += ": " + decoded.Error.Message
			case decoded.Message != "":
				detail += ": " + decoded.Message
			}
			return fail(&ProtocolError{Err: errors.New(redactBody(detail, token))})
		case "error":
			detail := "provider stream error"
			switch {
			case decoded.Message != "":
				detail += ": " + decoded.Message
			case decoded.Code != "":
				detail += ": " + decoded.Code
			}
			return fail(&ProtocolError{Err: errors.New(redactBody(detail, token))})
		default:
			// Unknown event types are ignored for forward compatibility.
		}
	}
}

// canonical maps Responses output items into a canonical response. Message
// items contribute output_text in order; reasoning items contribute thinking
// text for display only; function_call items are correlated by call_id, never
// by the provider item id.
func (r responsesResponse) canonical(token string) (Response, []jsontext.Value, error) {
	var out Response
	var reasoningItems []jsontext.Value
	if r.Usage != nil {
		out.Usage = r.Usage.canonical()
		if err := validateUsage(out.Usage); err != nil {
			return Response{}, nil, err
		}
	}

	var text, reasoning strings.Builder
	seen := make(map[string]struct{})
	for _, raw := range r.Output {
		var kind responsesOutputKind
		if err := jsonv2.Unmarshal([]byte(raw), &kind); err != nil {
			return Response{}, nil, &ProtocolError{Err: fmt.Errorf("decode response output item: %w", err)}
		}
		switch kind.Type {
		case "message":
			var item responsesMessageItem
			if err := jsonv2.Unmarshal([]byte(raw), &item); err != nil {
				return Response{}, nil, &ProtocolError{Err: fmt.Errorf("decode response message item: %w", err)}
			}
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		case "reasoning":
			var item responsesReasoningItem
			if err := jsonv2.Unmarshal([]byte(raw), &item); err != nil {
				return Response{}, nil, &ProtocolError{Err: fmt.Errorf("decode response reasoning item: %w", err)}
			}
			for _, part := range item.Summary {
				appendReasoningPart(&reasoning, part)
			}
			for _, part := range item.Content {
				appendReasoningPart(&reasoning, part)
			}
			if replayableReasoningItem(raw) {
				reasoningItems = append(reasoningItems, raw)
			}
		case "function_call":
			var item responsesFunctionCallItem
			if err := jsonv2.Unmarshal([]byte(raw), &item); err != nil {
				return Response{}, nil, &ProtocolError{Err: fmt.Errorf("decode response function call item: %w", err)}
			}
			if item.CallID == "" {
				return Response{}, nil, &ProtocolError{Err: errors.New("function call is missing a call_id")}
			}
			if _, ok := seen[item.CallID]; ok {
				return Response{}, nil, &ProtocolError{Err: fmt.Errorf("duplicate function call id %q", item.CallID)}
			}
			if item.Name == "" {
				return Response{}, nil, &ProtocolError{Err: fmt.Errorf("function call %q is missing a name", item.CallID)}
			}
			if !jsontext.Value(item.Arguments).IsValid() {
				return Response{}, nil, &ProtocolError{Err: fmt.Errorf("function call %q has invalid json arguments", item.CallID)}
			}
			seen[item.CallID] = struct{}{}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: json.RawMessage(item.Arguments),
			})
		}
	}
	out.Text = text.String()
	out.Reasoning = capReasoning(reasoning.String())

	finish, err := r.finishReason(len(out.ToolCalls) > 0, token)
	if err != nil {
		return Response{}, nil, err
	}
	out.FinishReason = finish
	return out, reasoningItems, nil
}

// replayableReasoningItem reports whether a reasoning item carries material a
// stateless request can replay. OpenAI requires encrypted content; DeepSeek
// returns plaintext reasoning_text content. A summary alone is display-only and
// cannot restore the model's reasoning.
func replayableReasoningItem(raw jsontext.Value) bool {
	var item struct {
		EncryptedContent string                 `json:"encrypted_content"`
		Content          []responsesSummaryPart `json:"content"`
	}
	if err := jsonv2.Unmarshal([]byte(raw), &item); err != nil {
		return false
	}
	if item.EncryptedContent != "" {
		return true
	}
	for _, part := range item.Content {
		if part.Text != "" {
			return true
		}
	}
	return false
}

// appendReasoningPart adds one reasoning summary or content part under the
// display cap. Non-text parts (for example encrypted content) are ignored.
func appendReasoningPart(b *strings.Builder, part responsesSummaryPart) {
	if part.Text == "" || (part.Type != "" && part.Type != "summary_text" && part.Type != "reasoning_text") {
		return
	}
	separator := ""
	if b.Len() > 0 {
		separator = "\n\n"
	}
	text := acceptReasoning(b.Len()+len(separator), part.Text)
	if text == "" {
		return
	}
	b.WriteString(separator)
	b.WriteString(text)
}

// finishReason normalizes the Responses status and incomplete reason. A failed
// response becomes a protocol error carrying the provider's error text.
func (r responsesResponse) finishReason(hasToolCalls bool, token string) (FinishReason, error) {
	var finish FinishReason
	switch r.Status {
	case "", "completed":
		finish = FinishStop
	case "incomplete":
		reason := ""
		if r.IncompleteDetails != nil {
			reason = r.IncompleteDetails.Reason
		}
		switch reason {
		case "max_output_tokens":
			finish = FinishLength
		case "content_filter":
			finish = FinishContentFilter
		default:
			finish = FinishOther
		}
	case "failed":
		detail := "response failed"
		switch {
		case r.Error != nil && r.Error.Message != "":
			detail += ": " + r.Error.Message
		case r.Error != nil && r.Error.Code != "":
			detail += ": " + r.Error.Code
		}
		return "", &ProtocolError{Err: errors.New(redactBody(detail, token))}
	default:
		finish = FinishOther
	}
	if hasToolCalls {
		finish = FinishToolCalls
	}
	return finish, nil
}
