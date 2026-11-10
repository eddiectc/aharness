package model

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newSSEServer serves one SSE body with the event-stream content type.
func newSSEServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write SSE body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenAIChatCompleteStream(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"hel"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readRequestBody(t, r)
		if !ok {
			return
		}
		var request map[string]json.RawMessage
		if err := jsonv2.Unmarshal(raw, &request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if got := string(request["stream"]); got != "true" {
			t.Errorf("stream = %s, want true", got)
		}
		var options struct {
			IncludeUsage bool `json:"include_usage"`
		}
		if err := jsonv2.Unmarshal(request["stream_options"], &options); err != nil {
			t.Errorf("decode stream_options: %v", err)
		}
		if !options.IncludeUsage {
			t.Errorf("stream_options.include_usage = false, want true")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	var emitted strings.Builder
	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion)
	resp, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, func(delta string) error {
		emitted.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if emitted.String() != "hello" {
		t.Errorf("emitted = %q, want %q", emitted.String(), "hello")
	}
	if !resp.Streamed {
		t.Error("Streamed = false, want true")
	}
	if resp.Text != "hello" {
		t.Errorf("Text = %q, want %q", resp.Text, "hello")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	call := resp.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "read_file" || string(call.Arguments) != `{"path":"a.go"}` {
		t.Errorf("tool call = %+v", call)
	}
	if resp.FinishReason != FinishToolCalls {
		t.Errorf("finish = %q, want %q", resp.FinishReason, FinishToolCalls)
	}
	if resp.Usage != (Usage{InputTokens: 11, OutputTokens: 7}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestOpenAIChatCompleteStreamNonSSEFallback(t *testing.T) {
	srv := newStaticServer(t, staticResponse{
		status: http.StatusOK,
		body:   []byte(`{"choices":[{"message":{"content":"plain"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`),
	})
	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion)
	var emitted strings.Builder
	resp, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, func(delta string) error {
		emitted.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if emitted.Len() != 0 {
		t.Errorf("emitted = %q, want none for a JSON fallback", emitted.String())
	}
	if resp.Streamed {
		t.Error("Streamed = true, want false for a JSON fallback")
	}
	if resp.Text != "plain" || resp.FinishReason != FinishStop {
		t.Errorf("response = %+v", resp)
	}
}

func TestOpenAIChatStreamProtocolErrors(t *testing.T) {
	noUsage := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	malformed := "data: {\"choices\":[\n\n"
	duplicateIDs := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"dup","function":{"name":"read_file","arguments":"{}"}},{"index":1,"id":"dup","function":{"name":"search","arguments":"{}"}}]}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	cases := []struct {
		name string
		body string
	}{
		{name: "missing usage", body: noUsage},
		{name: "malformed json", body: malformed},
		{name: "duplicate ids", body: duplicateIDs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSSEServer(t, tc.body)
			adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion)
			_, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, func(string) error { return nil })
			if err == nil {
				t.Fatal("CompleteStream() error = nil, want error")
			}
			requireProtocolError(t, err)
		})
	}
}

func TestOpenAIChatStreamPartialError(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		``,
	}, "\n")
	srv := newSSEServer(t, body)
	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion)
	_, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, func(string) error { return nil })
	if err == nil {
		t.Fatal("CompleteStream() error = nil, want error")
	}
	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want *PartialError", err, err)
	}
	if partial.Text != "partial" || partial.Bytes != len("partial") {
		t.Errorf("partial = %+v", partial)
	}
	requireProtocolError(t, err)
}

func TestOpenAIChatStreamCaps(t *testing.T) {
	t.Run("assembled response", func(t *testing.T) {
		huge := strings.Repeat("x", MaxCanonicalResponse+1)
		body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + huge + "\"}}]}\n\n"
		srv := newSSEServer(t, body)
		adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion)
		_, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, func(string) error { return nil })
		if err == nil {
			t.Fatal("CompleteStream() error = nil, want error")
		}
		requireProtocolError(t, err)
	})
	t.Run("sse event", func(t *testing.T) {
		huge := strings.Repeat("x", maxSSEEvent+1)
		body := "data: " + huge + "\n\n"
		srv := newSSEServer(t, body)
		adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion)
		_, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, func(string) error { return nil })
		if err == nil {
			t.Fatal("CompleteStream() error = nil, want error")
		}
		requireProtocolError(t, err)
	})
}

func TestOpenAIChatStreamCommentsCRLFAndMultiline(t *testing.T) {
	body := ": keepalive\r\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":\r\n" +
		"data: {\"content\":\"ok\"}}]}\r\n" +
		"\r\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"
	srv := newSSEServer(t, body)
	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion)
	var emitted strings.Builder
	resp, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, func(delta string) error {
		emitted.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if resp.Text != "ok" || emitted.String() != "ok" {
		t.Errorf("text = %q, emitted = %q", resp.Text, emitted.String())
	}
}

func TestOpenAIResponsesCompleteStream(t *testing.T) {
	body := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hel"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		``,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":0}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"path\":\"a.go\"}"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}"}],"usage":{"input_tokens":11,"output_tokens":7}}}`,
		``,
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readRequestBody(t, r)
		if !ok {
			return
		}
		var request map[string]json.RawMessage
		if err := jsonv2.Unmarshal(raw, &request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if got := string(request["stream"]); got != "true" {
			t.Errorf("stream = %s, want true", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	var emitted strings.Builder
	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion)
	resp, err := adapter.CompleteStream(t.Context(), Request{Model: "crouton-alpha-free"}, func(delta string) error {
		emitted.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if emitted.String() != "hello" {
		t.Errorf("emitted = %q, want %q", emitted.String(), "hello")
	}
	if !resp.Streamed {
		t.Error("Streamed = false, want true")
	}
	if resp.Text != "hello" {
		t.Errorf("Text = %q, want %q", resp.Text, "hello")
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || string(resp.ToolCalls[0].Arguments) != `{"path":"a.go"}` {
		t.Errorf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.FinishReason != FinishToolCalls {
		t.Errorf("finish = %q, want %q", resp.FinishReason, FinishToolCalls)
	}
	if resp.Usage != (Usage{InputTokens: 11, OutputTokens: 7}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestOpenAIResponsesCompleteStreamNonSSEFallback(t *testing.T) {
	srv := newStaticServer(t, staticResponse{
		status: http.StatusOK,
		body:   []byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"plain"}]}],"usage":{"input_tokens":2,"output_tokens":3}}`),
	})
	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion)
	var emitted strings.Builder
	resp, err := adapter.CompleteStream(t.Context(), Request{Model: "crouton-alpha-free"}, func(delta string) error {
		emitted.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if emitted.Len() != 0 {
		t.Errorf("emitted = %q, want none for a JSON fallback", emitted.String())
	}
	if resp.Streamed {
		t.Error("Streamed = true, want false for a JSON fallback")
	}
	if resp.Text != "plain" || resp.FinishReason != FinishStop {
		t.Errorf("response = %+v", resp)
	}
}

func TestOpenAIResponsesStreamProtocolErrors(t *testing.T) {
	missingUsage := strings.Join([]string{
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}}`,
		``,
	}, "\n")
	noTerminal := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		``,
	}, "\n")
	failed := strings.Join([]string{
		`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"exploded"}}}`,
		``,
	}, "\n")
	invalidUTF8 := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"\xff\"}\n\n"
	cases := []struct {
		name string
		body string
	}{
		{name: "missing usage", body: missingUsage},
		{name: "no terminal event", body: noTerminal},
		{name: "failed response", body: failed},
		{name: "invalid utf8", body: invalidUTF8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSSEServer(t, tc.body)
			adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion)
			_, err := adapter.CompleteStream(t.Context(), Request{Model: "crouton-alpha-free"}, func(string) error { return nil })
			if err == nil {
				t.Fatal("CompleteStream() error = nil, want error")
			}
			requireProtocolError(t, err)
		})
	}
}

func TestOpenAIResponsesStreamPartialError(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		``,
	}, "\n")
	srv := newSSEServer(t, body)
	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion)
	_, err := adapter.CompleteStream(t.Context(), Request{Model: "crouton-alpha-free"}, func(string) error { return nil })
	if err == nil {
		t.Fatal("CompleteStream() error = nil, want error")
	}
	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want *PartialError", err, err)
	}
	if partial.Text != "partial" || partial.Bytes != len("partial") {
		t.Errorf("partial = %+v", partial)
	}
}

func TestSSEDecoderTransportErrorAfterDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	t.Cleanup(srv.Close)
	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion)
	_, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, func(string) error { return nil })
	if err == nil {
		t.Fatal("CompleteStream() error = nil, want error")
	}
	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want *PartialError", err, err)
	}
	if partial.Text != "partial" {
		t.Errorf("partial text = %q", partial.Text)
	}
	if _, ok := errors.AsType[*ProtocolError](err); ok {
		t.Errorf("error = %v, want a transport failure wrapped in PartialError", err)
	}
	if _, ok := errors.AsType[*TransportError](err); !ok {
		t.Errorf("error = %v, want wrapped *TransportError", err)
	}
}
