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
)

// OpenAIResponses is the dialect adapter for the OpenAI Responses API.
type OpenAIResponses struct {
	transport *httpTransport
}

// NewOpenAIResponses constructs a Responses adapter. An empty baseURL uses
// DefaultBaseURL. An empty token omits the Authorization header.
func NewOpenAIResponses(baseURL, token, sessionID, version string) *OpenAIResponses {
	return &OpenAIResponses{
		transport: newHTTPTransport(baseURL, "/responses", token, sessionID, version),
	}
}

type responsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []any           `json:"input"`
	Tools           []responsesTool `json:"tools,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Store           bool            `json:"store"`
	Stream          bool            `json:"stream,omitempty"`
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
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
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

type responsesOutputPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesStreamEvent struct {
	Type     string             `json:"type"`
	Response *responsesResponse `json:"response"`
	Delta    string             `json:"delta"`
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
	return decodeResponsesResponse(body, a.transport.token)
}

// CompleteStream sends a streaming Responses request. Text deltas are
// display-only; the terminal completed response is authoritative.
func (a *OpenAIResponses) CompleteStream(ctx context.Context, req Request, emit func(string) error) (Response, error) {
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
		return decodeResponsesResponse(body, a.transport.token)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	return consumeResponsesStream(resp.Body, emit, a.transport.token)
}

func (a *OpenAIResponses) payload(req Request) responsesRequest {
	payload := responsesRequest{
		Model:           req.Model,
		Input:           make([]any, 0, len(req.Messages)),
		Tools:           make([]responsesTool, 0, len(req.Tools)),
		MaxOutputTokens: req.MaxOutputTokens,
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

func decodeResponsesResponse(body []byte, token string) (Response, error) {
	var decoded responsesResponse
	if err := jsonv2.Unmarshal(body, &decoded); err != nil {
		return Response{}, &ProtocolError{Err: fmt.Errorf("decode responses completion: %w", err)}
	}
	return decoded.canonical(token)
}

func consumeResponsesStream(r io.Reader, emit func(string) error, token string) (Response, error) {
	dec := newSSEDecoder(r)
	var (
		emittedText  strings.Builder
		emittedBytes int
	)
	fail := func(err error) (Response, error) {
		if emittedBytes > 0 {
			return Response{}, &PartialError{Err: err, Text: partialStreamText(emittedText.String()), Bytes: emittedBytes}
		}
		return Response{}, err
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
			if err := emit(decoded.Delta); err != nil {
				return Response{}, err
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
			resp, err := decoded.Response.canonical(token)
			if err != nil {
				return fail(err)
			}
			if responseContentBytes(resp) > MaxCanonicalResponse {
				return fail(&ProtocolError{Err: fmt.Errorf("assembled response exceeds %d bytes", MaxCanonicalResponse)})
			}
			resp.Streamed = true
			return resp, nil
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
// items contribute output_text in order; function_call items are correlated
// by call_id, never by the provider item id.
func (r responsesResponse) canonical(token string) (Response, error) {
	var out Response
	if r.Usage != nil {
		out.Usage = Usage{
			InputTokens:  r.Usage.InputTokens,
			OutputTokens: r.Usage.OutputTokens,
		}
	}

	var text strings.Builder
	seen := make(map[string]struct{})
	for _, raw := range r.Output {
		var kind responsesOutputKind
		if err := jsonv2.Unmarshal([]byte(raw), &kind); err != nil {
			return Response{}, &ProtocolError{Err: fmt.Errorf("decode response output item: %w", err)}
		}
		switch kind.Type {
		case "message":
			var item responsesMessageItem
			if err := jsonv2.Unmarshal([]byte(raw), &item); err != nil {
				return Response{}, &ProtocolError{Err: fmt.Errorf("decode response message item: %w", err)}
			}
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		case "function_call":
			var item responsesFunctionCallItem
			if err := jsonv2.Unmarshal([]byte(raw), &item); err != nil {
				return Response{}, &ProtocolError{Err: fmt.Errorf("decode response function call item: %w", err)}
			}
			if item.CallID == "" {
				return Response{}, &ProtocolError{Err: errors.New("function call is missing a call_id")}
			}
			if _, ok := seen[item.CallID]; ok {
				return Response{}, &ProtocolError{Err: fmt.Errorf("duplicate function call id %q", item.CallID)}
			}
			if item.Name == "" {
				return Response{}, &ProtocolError{Err: fmt.Errorf("function call %q is missing a name", item.CallID)}
			}
			if !jsontext.Value(item.Arguments).IsValid() {
				return Response{}, &ProtocolError{Err: fmt.Errorf("function call %q has invalid json arguments", item.CallID)}
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

	finish, err := r.finishReason(len(out.ToolCalls) > 0, token)
	if err != nil {
		return Response{}, err
	}
	out.FinishReason = finish
	return out, nil
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
