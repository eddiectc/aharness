package model

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestChatContinuationReplaysBufferedReasoning checks the DeepSeek tool-round
// contract: the reasoning_content that accompanied a tool call is attached to
// the same assistant message when the tool result is sent back.
func TestChatContinuationReplaysBufferedReasoning(t *testing.T) {
	const reasoning = "I need to read the file before editing it."
	var (
		mu       sync.Mutex
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := readRequestBody(t, r)
		if !ok {
			return
		}
		var request chatRequestWire
		if err := jsonv2.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		first := requests == 0
		requests++
		mu.Unlock()
		if !first {
			checkContinuation(t, request, reasoning)
			serveJSON(t, w, http.StatusOK, `{
				"choices": [{"message": {"role": "assistant", "content": "done"}, "finish_reason": "stop"}],
				"usage": {"prompt_tokens": 5, "completion_tokens": 2}
			}`)
			return
		}
		serveJSON(t, w, http.StatusOK, `{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "reading",
					"reasoning_content": "`+reasoning+`",
					"tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{}"}}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 3, "completion_tokens": 4}
		}`)
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
	tools := []ToolSchema{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}
	first, err := adapter.Complete(t.Context(), Request{
		Model:    "deepseek-v4.1-flash",
		Messages: []Message{{Role: RoleUser, Content: "read a.go"}},
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("first Complete() error = %v", err)
	}
	_, err = adapter.Complete(t.Context(), Request{
		Model: "deepseek-v4.1-flash",
		Messages: []Message{
			{Role: RoleUser, Content: "read a.go"},
			{Role: RoleAssistant, Content: first.Text, ToolCalls: first.ToolCalls},
			{Role: RoleTool, Name: "read_file", ToolCallID: "call_1", Content: "package main"},
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("second Complete() error = %v", err)
	}
}

// TestChatContinuationReplaysStreamedReasoning covers the streaming path: the
// completed response is remembered even though the display copy is capped.
func TestChatContinuationReplaysStreamedReasoning(t *testing.T) {
	const (
		partOne = "Check the repository"
		partTwo = " for the entry point."
	)
	var (
		mu       sync.Mutex
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := readRequestBody(t, r)
		if !ok {
			return
		}
		var request chatRequestWire
		if err := jsonv2.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		first := requests == 0
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if !first {
			checkContinuation(t, request, partOne+partTwo)
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
				``,
				`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
				``,
				`data: [DONE]`,
				``,
			}, "\n"))
			return
		}
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"reasoning_content":"` + partOne + `"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{"reasoning_content":"` + partTwo + `"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
	tools := []ToolSchema{{Name: "search", Parameters: json.RawMessage(`{"type":"object"}`)}}
	var emitted strings.Builder
	first, err := adapter.CompleteStream(t.Context(), Request{
		Model:    "deepseek-v4.1-flash",
		Messages: []Message{{Role: RoleUser, Content: "find the entry point"}},
		Tools:    tools,
	}, StreamHandler{Reasoning: func(delta string) error {
		emitted.WriteString(delta)
		return nil
	}})
	if err != nil {
		t.Fatalf("first CompleteStream() error = %v", err)
	}
	if emitted.String() != partOne+partTwo {
		t.Fatalf("streamed reasoning = %q", emitted.String())
	}
	_, err = adapter.CompleteStream(t.Context(), Request{
		Model: "deepseek-v4.1-flash",
		Messages: []Message{
			{Role: RoleUser, Content: "find the entry point"},
			{Role: RoleAssistant, Content: first.Text, ToolCalls: first.ToolCalls},
			{Role: RoleTool, Name: "search", ToolCallID: "call_1", Content: "main.go"},
		},
		Tools: tools,
	}, StreamHandler{})
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
}

func checkContinuation(t *testing.T, request chatRequestWire, want string) {
	t.Helper()
	if len(request.Messages) < 2 {
		t.Fatalf("messages = %d, want the replayed assistant message", len(request.Messages))
	}
	assistant := request.Messages[1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("message[1] = %#v, want the assistant tool call", assistant)
	}
	if assistant.ReasoningContent != want {
		t.Fatalf("reasoning_content = %q, want %q", assistant.ReasoningContent, want)
	}
}
