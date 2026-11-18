package model

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// responsesContinuationWire is the request shape the tests inspect. Input is
// kept raw so a reasoning item can be checked without normalizing it.
type responsesContinuationWire struct {
	Include []string          `json:"include"`
	Input   []json.RawMessage `json:"input"`
}

type responsesInputProbe struct {
	Type             string `json:"type"`
	CallID           string `json:"call_id"`
	EncryptedContent string `json:"encrypted_content"`
	Content          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func TestResponsesContinuationReplaysEncryptedReasoning(t *testing.T) {
	item := `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"cipher"}`
	testResponsesContinuation(t, item, func(t *testing.T, probe responsesInputProbe) {
		t.Helper()
		if probe.EncryptedContent != "cipher" {
			t.Errorf("encrypted_content = %q, want %q", probe.EncryptedContent, "cipher")
		}
	})
}

func TestResponsesContinuationReplaysPlaintextReasoning(t *testing.T) {
	item := `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"look at the file"}]}`
	testResponsesContinuation(t, item, func(t *testing.T, probe responsesInputProbe) {
		t.Helper()
		if len(probe.Content) != 1 || probe.Content[0].Type != "reasoning_text" || probe.Content[0].Text != "look at the file" {
			t.Errorf("content = %#v, want the replayed reasoning_text", probe.Content)
		}
	})
}

func testResponsesContinuation(t *testing.T, reasoningItem string, check func(*testing.T, responsesInputProbe)) {
	t.Helper()
	first := `{
		"id": "resp_1", "object": "response", "status": "completed",
		"output": [
			` + reasoningItem + `,
			{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_file", "arguments": "{}"}
		],
		"usage": {"input_tokens": 3, "output_tokens": 4}
	}`
	final := `{
		"id": "resp_2", "object": "response", "status": "completed",
		"output": [{"type": "message", "content": [{"type": "output_text", "text": "done"}]}],
		"usage": {"input_tokens": 5, "output_tokens": 2}
	}`
	var (
		mu       sync.Mutex
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := readRequestBody(t, r)
		if !ok {
			return
		}
		mu.Lock()
		firstRequest := requests == 0
		requests++
		mu.Unlock()
		if firstRequest {
			serveJSON(t, w, http.StatusOK, first)
			return
		}
		checkResponsesContinuationRequest(t, body, check)
		serveJSON(t, w, http.StatusOK, final)
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion, 0)
	tools := []ToolSchema{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}
	firstResp, err := adapter.Complete(t.Context(), Request{
		Model:    "crouton-alpha-free",
		Messages: []Message{{Role: RoleUser, Content: "read a.go"}},
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("first Complete() error = %v", err)
	}
	_, err = adapter.Complete(t.Context(), Request{
		Model: "crouton-alpha-free",
		Messages: []Message{
			{Role: RoleUser, Content: "read a.go"},
			{Role: RoleAssistant, ToolCalls: firstResp.ToolCalls},
			{Role: RoleTool, Name: "read_file", ToolCallID: "call_1", Content: "package main"},
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("second Complete() error = %v", err)
	}
}

func checkResponsesContinuationRequest(t *testing.T, body []byte, check func(*testing.T, responsesInputProbe)) {
	t.Helper()
	var request responsesContinuationWire
	if err := jsonv2.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode replayed request: %v", err)
	}
	if len(request.Include) != 1 || request.Include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %#v, want reasoning.encrypted_content", request.Include)
	}
	reasoningIndex, callIndex := -1, -1
	for i, raw := range request.Input {
		var probe responsesInputProbe
		if err := jsonv2.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("decode input[%d]: %v", i, err)
		}
		switch probe.Type {
		case "reasoning":
			if reasoningIndex < 0 {
				reasoningIndex = i
				check(t, probe)
			}
		case "function_call":
			if probe.CallID == "call_1" {
				callIndex = i
			}
		}
	}
	if reasoningIndex < 0 {
		t.Fatalf("replayed request has no reasoning item: %s", body)
	}
	if callIndex < 0 || reasoningIndex >= callIndex {
		t.Fatalf("reasoning index %d must precede function_call index %d: %s", reasoningIndex, callIndex, body)
	}
}

func TestResponsesContinuationStreamed(t *testing.T) {
	const reasoningItem = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"streamed-cipher"}`
	first := strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[` + reasoningItem + `,{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":4}}}`,
		``,
	}, "\n")
	final := strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":5,"output_tokens":2}}}`,
		``,
	}, "\n")
	var (
		mu       sync.Mutex
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := readRequestBody(t, r)
		if !ok {
			return
		}
		mu.Lock()
		firstRequest := requests == 0
		requests++
		mu.Unlock()
		if !firstRequest {
			checkResponsesContinuationRequest(t, body, func(t *testing.T, probe responsesInputProbe) {
				t.Helper()
				if probe.EncryptedContent != "streamed-cipher" {
					t.Errorf("encrypted_content = %q, want %q", probe.EncryptedContent, "streamed-cipher")
				}
			})
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if firstRequest {
			_, _ = io.WriteString(w, first)
		} else {
			_, _ = io.WriteString(w, final)
		}
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion, 0)
	tools := []ToolSchema{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}
	firstResp, err := adapter.CompleteStream(t.Context(), Request{
		Model:    "crouton-alpha-free",
		Messages: []Message{{Role: RoleUser, Content: "read a.go"}},
		Tools:    tools,
	}, StreamHandler{})
	if err != nil {
		t.Fatalf("first CompleteStream() error = %v", err)
	}
	_, err = adapter.CompleteStream(t.Context(), Request{
		Model: "crouton-alpha-free",
		Messages: []Message{
			{Role: RoleUser, Content: "read a.go"},
			{Role: RoleAssistant, ToolCalls: firstResp.ToolCalls},
			{Role: RoleTool, Name: "read_file", ToolCallID: "call_1", Content: "package main"},
		},
		Tools: tools,
	}, StreamHandler{})
	if err != nil {
		t.Fatalf("second CompleteStream() error = %v", err)
	}
}

func TestResponsesContinuationReplaysEveryToolRound(t *testing.T) {
	makeResponse := func(id, callID, cipher string) string {
		return `{
			"id": "` + id + `", "object": "response", "status": "completed",
			"output": [
				{"type": "reasoning", "id": "rs_` + callID + `", "summary": [], "encrypted_content": "` + cipher + `"},
				{"type": "function_call", "id": "fc_` + callID + `", "call_id": "` + callID + `", "name": "read_file", "arguments": "{}"}
			],
			"usage": {"input_tokens": 3, "output_tokens": 4}
		}`
	}
	final := `{
		"id": "resp_3", "object": "response", "status": "completed",
		"output": [{"type": "message", "content": [{"type": "output_text", "text": "done"}]}],
		"usage": {"input_tokens": 9, "output_tokens": 2}
	}`
	var (
		mu       sync.Mutex
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := readRequestBody(t, r)
		if !ok {
			return
		}
		mu.Lock()
		call := requests
		requests++
		mu.Unlock()
		switch call {
		case 0:
			serveJSON(t, w, http.StatusOK, makeResponse("resp_1", "call_1", "cipher-1"))
		case 1:
			serveJSON(t, w, http.StatusOK, makeResponse("resp_2", "call_2", "cipher-2"))
		default:
			var request responsesContinuationWire
			if err := jsonv2.Unmarshal(body, &request); err != nil {
				t.Fatalf("decode replayed request: %v", err)
			}
			assertReasoningBeforeCall(t, request.Input, "call_1", "cipher-1")
			assertReasoningBeforeCall(t, request.Input, "call_2", "cipher-2")
			serveJSON(t, w, http.StatusOK, final)
		}
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion, 0)
	tools := []ToolSchema{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)}}
	messages := []Message{{Role: RoleUser, Content: "read both files"}}
	firstResp, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free", Messages: messages, Tools: tools})
	if err != nil {
		t.Fatalf("first Complete() error = %v", err)
	}
	messages = append(messages,
		Message{Role: RoleAssistant, ToolCalls: firstResp.ToolCalls},
		Message{Role: RoleTool, Name: "read_file", ToolCallID: "call_1", Content: "one"},
	)
	secondResp, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free", Messages: messages, Tools: tools})
	if err != nil {
		t.Fatalf("second Complete() error = %v", err)
	}
	messages = append(messages,
		Message{Role: RoleAssistant, ToolCalls: secondResp.ToolCalls},
		Message{Role: RoleTool, Name: "read_file", ToolCallID: "call_2", Content: "two"},
	)
	if _, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free", Messages: messages, Tools: tools}); err != nil {
		t.Fatalf("third Complete() error = %v", err)
	}
}

func assertReasoningBeforeCall(t *testing.T, input []json.RawMessage, callID, cipher string) {
	t.Helper()
	reasoningIndex, callIndex := -1, -1
	for i, raw := range input {
		var probe responsesInputProbe
		if err := jsonv2.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("decode input[%d]: %v", i, err)
		}
		switch {
		case probe.Type == "reasoning" && probe.EncryptedContent == cipher:
			reasoningIndex = i
		case probe.Type == "function_call" && probe.CallID == callID:
			callIndex = i
		}
	}
	if reasoningIndex < 0 {
		t.Errorf("missing reasoning item %q", cipher)
		return
	}
	if callIndex < 0 || reasoningIndex >= callIndex {
		t.Errorf("reasoning %q index %d must precede %s index %d", cipher, reasoningIndex, callID, callIndex)
	}
}

func TestReplayableReasoningItem(t *testing.T) {
	tests := []struct {
		name string
		item string
		want bool
	}{
		{"encrypted", `{"type":"reasoning","encrypted_content":"cipher"}`, true},
		{"plaintext", `{"type":"reasoning","content":[{"type":"reasoning_text","text":"thought"}]}`, true},
		{"summary only", `{"type":"reasoning","summary":[{"type":"summary_text","text":"summary"}]}`, false},
		{"id only", `{"type":"reasoning","id":"rs_1"}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := replayableReasoningItem(jsontext.Value(tc.item)); got != tc.want {
				t.Errorf("replayableReasoningItem() = %v, want %v", got, tc.want)
			}
		})
	}
}
