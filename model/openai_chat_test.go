package model

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// tinyPNG is a 1x1 PNG; the wire tests only care that the bytes survive base64.
var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\xcf\xc0\x00\x00\x03\x01\x01\x00\x18\xdd\x8d\xb0\x00\x00\x00\x00IEND\xaeB`\x82")

// captureChatPayload returns the decoded raw request body captured by a fake
// provider that answers with a minimal completion.
func captureChatPayload(t *testing.T, req Request) []byte {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if _, err := r.Body.Read(body); err != nil && err.Error() != "EOF" {
			t.Errorf("read request body: %v", err)
		}
		captured = body
		serveJSON(t, w, http.StatusOK, `{
			"choices": [{"message": {"content": "ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1}
		}`)
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIChat(srv.URL, "token", "session", "aharness/test", 0)
	if _, err := adapter.Complete(t.Context(), req); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	return captured
}

func TestOpenAIChatToolImageWire(t *testing.T) {
	wire := captureChatPayload(t, Request{
		Model: "deepseek-v4.1-flash",
		Messages: []Message{
			{Role: RoleUser, Content: "look"},
			{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{{
				ID: "call_1", Name: "read_image", Arguments: json.RawMessage(`{"path":"a.png"}`),
			}}},
			{
				Role:       RoleTool,
				ToolCallID: "call_1",
				Name:       "read_image",
				Content:    "a.png: image/png 1x1 (0 KiB, passthrough)",
				Images: []ImagePart{{
					MediaType: MediaTypePNG, Data: tinyPNG, Width: 1, Height: 1,
				}},
			},
		},
	})

	var got struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := jsonv2.Unmarshal(wire, &got); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (tool image follows in a user message)", len(got.Messages))
	}
	// A text-only message must still marshal content as a JSON string.
	if s := string(got.Messages[0].Content); s != `"look"` {
		t.Errorf("user content = %s, want a JSON string", s)
	}
	// The assistant tool-call message keeps the empty string member.
	if s := string(got.Messages[1].Content); s != `""` {
		t.Errorf("assistant content = %s, want an empty string", s)
	}
	// The tool message itself stays a plain string so gateways that reject
	// image parts in a tool message still accept the request.
	if s := string(got.Messages[2].Content); !strings.HasPrefix(s, `"a.png:`) {
		t.Errorf("tool content = %s, want the caption as a JSON string", s)
	}
	if got.Messages[3].Role != "user" {
		t.Errorf("messages[3].role = %q, want user", got.Messages[3].Role)
	}

	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := jsonv2.Unmarshal([]byte(got.Messages[3].Content), &parts); err != nil {
		t.Fatalf("user content is not a parts array: %s", got.Messages[3].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("user parts = %d, want text then image", len(parts))
	}
	if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "untrusted data") {
		t.Errorf("parts[0] = %+v, want the untrusted-data caption", parts[0])
	}
	if parts[1].Type != "image_url" {
		t.Errorf("parts[1].type = %q, want image_url", parts[1].Type)
	}
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	if parts[1].ImageURL.URL != wantURL {
		t.Errorf("image url does not round-trip the bytes")
	}
}

func TestOpenAIChatRejectsUnsupportedImageType(t *testing.T) {
	adapter := NewOpenAIChat("http://127.0.0.1:1", "token", "session", "aharness/test", 0)
	_, err := adapter.Complete(t.Context(), Request{
		Model: "m",
		Messages: []Message{{
			Role:    RoleTool,
			Content: "x",
			Images:  []ImagePart{{MediaType: "image/webp", Data: []byte("x"), Width: 1, Height: 1}},
		}},
	})
	if err == nil {
		t.Fatal("Complete() accepted an unsupported image media type")
	}
}

func TestOpenAIResponsesToolImageWire(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		captured = buf.Bytes()
		serveJSON(t, w, http.StatusOK, `{
			"status": "completed",
			"output": [{"type": "message", "content": [{"type": "output_text", "text": "ok"}]}],
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`)
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIResponses(srv.URL, "token", "session", "aharness/test", 0)
	if _, err := adapter.Complete(t.Context(), Request{
		Model: "m",
		Messages: []Message{{
			Role:       RoleTool,
			ToolCallID: "call_1",
			Content:    "a.png: image/png 1x1",
			Images:     []ImagePart{{MediaType: MediaTypePNG, Data: tinyPNG, Width: 1, Height: 1}},
		}},
	}); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	var got struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Output  string `json:"output"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := jsonv2.Unmarshal(captured, &got); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(got.Input) != 2 {
		t.Fatalf("input = %d entries, want function output then user image", len(got.Input))
	}
	if got.Input[0].Type != "function_call_output" || !strings.HasPrefix(got.Input[0].Output, "a.png:") {
		t.Fatalf("input[0] = %+v, want a string function_call_output", got.Input[0])
	}
	if got.Input[1].Role != "user" {
		t.Fatalf("input[1].role = %q, want user", got.Input[1].Role)
	}
	parts := got.Input[1].Content
	if len(parts) != 2 || parts[0].Type != "input_text" || parts[1].Type != "input_image" {
		t.Fatalf("content parts = %+v, want input_text then input_image", parts)
	}
	if !strings.HasPrefix(parts[1].ImageURL, "data:image/png;base64,") {
		t.Errorf("image_url = %q, want a PNG data URL", parts[1].ImageURL)
	}
}

type chatRequestWire struct {
	Model    string            `json:"model"`
	Messages []chatMessageWire `json:"messages"`
	Tools    []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	MaxTokens int `json:"max_tokens"`
}

type chatMessageWire struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCallID       string `json:"tool_call_id"`
	ToolCalls        []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func TestOpenAIChatCompleteCanonical(t *testing.T) {
	req := Request{
		Model: "deepseek-v4.1-flash",
		Messages: []Message{
			{Role: RoleSystem, Content: "you are a harness"},
			{Role: RoleUser, Content: "read the file"},
			{
				Role:    RoleAssistant,
				Content: "reading",
				ToolCalls: []ToolCall{{
					ID:        "call_1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path":"main.go"}`),
				}},
			},
			{Role: RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "package main"},
		},
		Tools: []ToolSchema{{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		MaxOutputTokens: 1024,
	}

	responseBody := `{
		"id": "chatcmpl-1",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "hello",
				"tool_calls": [
					{"id": "call_a", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"a.go\"}"}},
					{"id": "call_b", "type": "function", "function": {"name": "search", "arguments": "{}"}}
				]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
		"system_fingerprint": "fp_unknown"
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/chat/completions"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		checkRequiredHeaders(t, r.Header, true)

		body, ok := readRequestBody(t, r)
		if ok {
			var got chatRequestWire
			if err := jsonv2.Unmarshal(body, &got); err != nil {
				t.Errorf("decode chat request: %v", err)
			} else {
				checkChatRequest(t, got)
			}
		}
		serveJSON(t, w, http.StatusOK, responseBody)
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
	got, err := adapter.Complete(t.Context(), req)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	want := Response{
		Text: "hello",
		ToolCalls: []ToolCall{
			{ID: "call_a", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.go"}`)},
			{ID: "call_b", Name: "search", Arguments: json.RawMessage(`{}`)},
		},
		FinishReason: FinishToolCalls,
		Usage:        Usage{InputTokens: 11, OutputTokens: 7, Reported: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Response = %#v, want %#v", got, want)
	}
}

func checkChatRequest(t *testing.T, got chatRequestWire) {
	t.Helper()
	if got.Model != "deepseek-v4.1-flash" {
		t.Errorf("model = %q, want %q", got.Model, "deepseek-v4.1-flash")
	}
	if got.MaxTokens != 1024 {
		t.Errorf("max_tokens = %d, want 1024", got.MaxTokens)
	}
	if len(got.Messages) != 4 {
		t.Errorf("len(messages) = %d, want 4", len(got.Messages))
		return
	}

	system := got.Messages[0]
	if system.Role != "system" || system.Content != "you are a harness" {
		t.Errorf("message[0] = %#v, want system message", system)
	}
	user := got.Messages[1]
	if user.Role != "user" || user.Content != "read the file" {
		t.Errorf("message[1] = %#v, want user message", user)
	}

	assistant := got.Messages[2]
	if assistant.Role != "assistant" || assistant.Content != "reading" {
		t.Errorf("message[2] = %#v, want assistant message with content", assistant)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Errorf("len(message[2].tool_calls) = %d, want 1", len(assistant.ToolCalls))
		return
	}
	call := assistant.ToolCalls[0]
	if call.ID != "call_1" || call.Type != "function" {
		t.Errorf("tool call = %#v, want id call_1 and type function", call)
	}
	if call.Function.Name != "read_file" || call.Function.Arguments != `{"path":"main.go"}` {
		t.Errorf("tool call function = %#v, want read_file with raw arguments", call.Function)
	}

	toolMsg := got.Messages[3]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" || toolMsg.Content != "package main" {
		t.Errorf("message[3] = %#v, want tool result", toolMsg)
	}

	if len(got.Tools) != 1 {
		t.Errorf("len(tools) = %d, want 1", len(got.Tools))
		return
	}
	tool := got.Tools[0]
	if tool.Type != "function" {
		t.Errorf("tools[0].type = %q, want function", tool.Type)
	}
	if tool.Function.Name != "read_file" || tool.Function.Description != "Read a file" {
		t.Errorf("tools[0].function = %#v, want read_file description", tool.Function)
	}
	if want := `{"type":"object","properties":{"path":{"type":"string"}}}`; string(tool.Function.Parameters) != want {
		t.Errorf("tools[0].function.parameters = %s, want %s", tool.Function.Parameters, want)
	}
}

func TestOpenAIChatToolCallValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing id",
			body: `{"choices":[{"message":{"content":"","tool_calls":[{"type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		},
		{
			name: "empty id",
			body: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		},
		{
			name: "duplicate ids",
			body: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}},{"id":"call_1","type":"function","function":{"name":"search","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		},
		{
			name: "empty name",
			body: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		},
		{
			name: "invalid arguments",
			body: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{not json"}}]},"finish_reason":"tool_calls"}]}`,
		},
		{
			name: "missing arguments",
			body: `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file"}}]},"finish_reason":"tool_calls"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newStaticServer(t, staticResponse{status: http.StatusOK, body: []byte(tc.body)})
			adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
			_, err := adapter.Complete(t.Context(), Request{Model: "deepseek-v4.1-flash"})
			if err == nil {
				t.Fatal("Complete() error = nil, want error")
			}
			requireProtocolError(t, err)
		})
	}
}

func TestOpenAIChatFinishNormalization(t *testing.T) {
	const toolCalls = `,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]`
	tests := []struct {
		name       string
		finish     string
		setFinish  bool
		withCalls  bool
		wantFinish FinishReason
		wantCalls  int
	}{
		{"stop", "stop", true, false, FinishStop, 0},
		{"absent finish", "", false, false, FinishStop, 0},
		{"length", "length", true, false, FinishLength, 0},
		{"content filter", "content_filter", true, false, FinishContentFilter, 0},
		{"tool calls", "tool_calls", true, false, FinishToolCalls, 0},
		{"function call", "function_call", true, false, FinishToolCalls, 0},
		{"unknown", "something_else", true, false, FinishOther, 0},
		{"stop with calls normalizes", "stop", true, true, FinishToolCalls, 1},
		{"absent finish with calls normalizes", "", false, true, FinishToolCalls, 1},
		{"length with calls stays length", "length", true, true, FinishLength, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			message := `"content":"ok"`
			if tc.withCalls {
				message += toolCalls
			}
			choice := `{"message":{` + message + `}`
			if tc.setFinish {
				choice += `,"finish_reason":"` + tc.finish + `"`
			}
			choice += `}`
			body := `{"choices":[` + choice + `]}`

			srv := newStaticServer(t, staticResponse{status: http.StatusOK, body: []byte(body)})
			adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
			got, err := adapter.Complete(t.Context(), Request{Model: "deepseek-v4.1-flash"})
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if got.FinishReason != tc.wantFinish {
				t.Errorf("FinishReason = %q, want %q", got.FinishReason, tc.wantFinish)
			}
			if len(got.ToolCalls) != tc.wantCalls {
				t.Errorf("len(ToolCalls) = %d, want %d", len(got.ToolCalls), tc.wantCalls)
			}
		})
	}
}

func TestOpenAIChatEmptyChoices(t *testing.T) {
	srv := newStaticServer(t, staticResponse{
		status: http.StatusOK,
		body:   []byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`),
	})
	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
	_, err := adapter.Complete(t.Context(), Request{Model: "deepseek-v4.1-flash"})
	if err == nil {
		t.Fatal("Complete() error = nil, want error")
	}
	requireProtocolError(t, err)
}

func TestOpenAIChatOmitsUnsetOutputBudget(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = readRequestBody(t, r)
		serveJSON(t, w, http.StatusOK, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
	if _, err := adapter.Complete(t.Context(), Request{Model: "deepseek-v4.1-flash"}); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	var raw map[string]json.RawMessage
	if err := jsonv2.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if value, present := raw["max_tokens"]; present {
		t.Errorf("max_tokens = %s, want absent for an unset budget", value)
	}
}
