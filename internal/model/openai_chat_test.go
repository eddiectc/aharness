package model

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

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
