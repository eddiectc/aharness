package model

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type responsesRequestWire struct {
	Model        string               `json:"model"`
	Instructions string               `json:"instructions"`
	Input        []responsesInputWire `json:"input"`
	Tools        []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"tools"`
	MaxOutputTokens int   `json:"max_output_tokens"`
	Store           *bool `json:"store"`
}

type responsesInputWire struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Output    string `json:"output"`
}

func TestOpenAIResponsesCompleteCanonical(t *testing.T) {
	req := Request{
		Model: "crouton-alpha-free",
		Messages: []Message{
			{Role: RoleSystem, Content: "be useful"},
			{Role: RoleUser, Content: "do it"},
			{
				Role:    RoleAssistant,
				Content: "on it",
				ToolCalls: []ToolCall{{
					ID:        "call_1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path":"a.go"}`),
				}},
			},
			{Role: RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "contents"},
		},
		Tools: []ToolSchema{{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		MaxOutputTokens: 2048,
	}

	responseBody := `{
		"id": "resp_1",
		"object": "response",
		"status": "completed",
		"output": [
			{"type": "reasoning", "id": "rs_1", "summary": []},
			{"type": "future_item", "id": "future_1", "content": "not-an-array", "call_id": 42, "arguments": {"x": 1}},
			{"type": "message", "id": "msg_1", "role": "assistant", "content": [
				{"type": "output_text", "text": "hel"},
				{"type": "refusal", "text": "ignored"},
				{"type": "output_text", "text": "lo!"}
			]},
			{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_file", "arguments": "{\"path\":\"b.go\"}"}
		],
		"usage": {"input_tokens": 21, "output_tokens": 13, "total_tokens": 34}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/responses"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		checkRequiredHeaders(t, r.Header, true)

		body, ok := readRequestBody(t, r)
		if ok {
			var raw map[string]json.RawMessage
			if err := jsonv2.Unmarshal(body, &raw); err != nil {
				t.Errorf("decode raw responses request: %v", err)
			} else {
				if _, present := raw["previous_response_id"]; present {
					t.Errorf("request contains previous_response_id, want absent")
				}
				store, present := raw["store"]
				if !present {
					t.Errorf("request is missing store, want false")
				} else if string(store) != "false" {
					t.Errorf("store = %s, want false", store)
				}
			}

			var got responsesRequestWire
			if err := jsonv2.Unmarshal(body, &got); err != nil {
				t.Errorf("decode responses request: %v", err)
			} else {
				checkResponsesRequest(t, got)
			}
		}
		serveJSON(t, w, http.StatusOK, responseBody)
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion)
	got, err := adapter.Complete(t.Context(), req)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	want := Response{
		Text: "hello!",
		ToolCalls: []ToolCall{{
			ID:        "call_1",
			Name:      "read_file",
			Arguments: json.RawMessage(`{"path":"b.go"}`),
		}},
		FinishReason: FinishToolCalls,
		Usage:        Usage{InputTokens: 21, OutputTokens: 13},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Response = %#v, want %#v", got, want)
	}
}

func checkResponsesRequest(t *testing.T, got responsesRequestWire) {
	t.Helper()
	if got.Model != "crouton-alpha-free" {
		t.Errorf("model = %q, want %q", got.Model, "crouton-alpha-free")
	}
	if got.Instructions != "be useful" {
		t.Errorf("instructions = %q, want %q", got.Instructions, "be useful")
	}
	if got.MaxOutputTokens != 2048 {
		t.Errorf("max_output_tokens = %d, want 2048", got.MaxOutputTokens)
	}
	if got.Store == nil || *got.Store {
		t.Errorf("store = %v, want false", got.Store)
	}
	if len(got.Input) != 4 {
		t.Errorf("len(input) = %d, want 4", len(got.Input))
		return
	}

	user := got.Input[0]
	if user.Type != "" || user.Role != "user" {
		t.Errorf("input[0] = %#v, want user message", user)
	}
	if len(user.Content) != 1 || user.Content[0].Type != "input_text" || user.Content[0].Text != "do it" {
		t.Errorf("input[0].content = %#v, want input_text %q", user.Content, "do it")
	}

	assistant := got.Input[1]
	if assistant.Type != "message" || assistant.Role != "assistant" {
		t.Errorf("input[1] = %#v, want assistant message", assistant)
	}
	if len(assistant.Content) != 1 || assistant.Content[0].Type != "output_text" || assistant.Content[0].Text != "on it" {
		t.Errorf("input[1].content = %#v, want output_text %q", assistant.Content, "on it")
	}

	call := got.Input[2]
	if call.Type != "function_call" || call.CallID != "call_1" || call.Name != "read_file" {
		t.Errorf("input[2] = %#v, want function_call read_file", call)
	}
	if want := `{"path":"a.go"}`; call.Arguments != want {
		t.Errorf("input[2].arguments = %q, want %q", call.Arguments, want)
	}

	result := got.Input[3]
	if result.Type != "function_call_output" || result.CallID != "call_1" || result.Output != "contents" {
		t.Errorf("input[3] = %#v, want function_call_output contents", result)
	}

	if len(got.Tools) != 1 {
		t.Errorf("len(tools) = %d, want 1", len(got.Tools))
		return
	}
	tool := got.Tools[0]
	if tool.Type != "function" || tool.Name != "read_file" || tool.Description != "Read a file" {
		t.Errorf("tools[0] = %#v, want flat function tool", tool)
	}
	if want := `{"type":"object","properties":{"path":{"type":"string"}}}`; string(tool.Parameters) != want {
		t.Errorf("tools[0].parameters = %s, want %s", tool.Parameters, want)
	}
}

func TestOpenAIResponsesToolCallValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing call id",
			body: `{"status":"completed","output":[{"type":"function_call","id":"fc_1","name":"read_file","arguments":"{}"}]}`,
		},
		{
			name: "empty call id",
			body: `{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"","name":"read_file","arguments":"{}"}]}`,
		},
		{
			name: "duplicate call ids",
			body: `{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{}"},{"type":"function_call","id":"fc_2","call_id":"call_1","name":"search","arguments":"{}"}]}`,
		},
		{
			name: "empty name",
			body: `{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"","arguments":"{}"}]}`,
		},
		{
			name: "invalid arguments",
			body: `{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{not json"}]}`,
		},
		{
			name: "missing arguments",
			body: `{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newStaticServer(t, staticResponse{status: http.StatusOK, body: []byte(tc.body)})
			adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion)
			_, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free"})
			if err == nil {
				t.Fatal("Complete() error = nil, want error")
			}
			requireProtocolError(t, err)
		})
	}
}

func TestOpenAIResponsesOutputItemStrictness(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "duplicate member",
			body: []byte(`{"status":"completed","output":[{"type":"function_call","call_id":"call_1","call_id":"call_2","name":"read_file","arguments":"{}"}]}`),
		},
		{
			name: "invalid utf8 in unknown item",
			body: []byte("{\"status\":\"completed\",\"output\":[{\"type\":\"future_item\",\"value\":\"\xff\"}]}"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newStaticServer(t, staticResponse{status: http.StatusOK, body: tc.body})
			adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion)
			_, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free"})
			if err == nil {
				t.Fatal("Complete() error = nil, want error")
			}
			requireProtocolError(t, err)
		})
	}
}

func TestOpenAIResponsesFinishNormalization(t *testing.T) {
	const functionCall = `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{}"}`
	tests := []struct {
		name        string
		status      string
		setStatus   bool
		incomplete  string
		withCall    bool
		errorObject string
		wantFinish  FinishReason
		wantError   bool
	}{
		{
			name:       "completed",
			status:     "completed",
			setStatus:  true,
			wantFinish: FinishStop,
		},
		{
			name:       "absent status",
			wantFinish: FinishStop,
		},
		{
			name:       "completed with call",
			status:     "completed",
			setStatus:  true,
			withCall:   true,
			wantFinish: FinishToolCalls,
		},
		{
			name:       "incomplete max output tokens",
			status:     "incomplete",
			setStatus:  true,
			incomplete: "max_output_tokens",
			wantFinish: FinishLength,
		},
		{
			name:       "incomplete content filter",
			status:     "incomplete",
			setStatus:  true,
			incomplete: "content_filter",
			wantFinish: FinishContentFilter,
		},
		{
			name:       "incomplete other reason",
			status:     "incomplete",
			setStatus:  true,
			incomplete: "some_other_reason",
			wantFinish: FinishOther,
		},
		{
			name:       "unknown status",
			status:     "queued",
			setStatus:  true,
			wantFinish: FinishOther,
		},
		{
			name:        "failed",
			status:      "failed",
			setStatus:   true,
			errorObject: `{"code":"server_error","message":"provider exploded"}`,
			wantError:   true,
		},
		{
			name:      "failed without error object",
			status:    "failed",
			setStatus: true,
			wantError: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields := make([]string, 0, 4)
			if tc.setStatus {
				fields = append(fields, `"status":"`+tc.status+`"`)
			}
			if tc.incomplete != "" {
				fields = append(fields, `"incomplete_details":{"reason":"`+tc.incomplete+`"}`)
			}
			if tc.errorObject != "" {
				fields = append(fields, `"error":`+tc.errorObject)
			}
			if tc.withCall {
				fields = append(fields, `"output":[`+functionCall+`]`)
			} else {
				fields = append(fields, `"output":[]`)
			}
			body := "{" + strings.Join(fields, ",") + "}"

			srv := newStaticServer(t, staticResponse{status: http.StatusOK, body: []byte(body)})
			adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion)
			got, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free"})
			if tc.wantError {
				if err == nil {
					t.Fatal("Complete() error = nil, want error")
				}
				requireProtocolError(t, err)
				if tc.errorObject != "" && !strings.Contains(err.Error(), "provider exploded") {
					t.Errorf("error = %q, want provider error text", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if got.FinishReason != tc.wantFinish {
				t.Errorf("FinishReason = %q, want %q", got.FinishReason, tc.wantFinish)
			}
			if tc.withCall && len(got.ToolCalls) != 1 {
				t.Errorf("len(ToolCalls) = %d, want 1", len(got.ToolCalls))
			}
		})
	}
}

func TestOpenAIResponsesOmitsUnsetOutputBudget(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = readRequestBody(t, r)
		serveJSON(t, w, http.StatusOK, `{"id":"resp","object":"response","status":"completed","output":[],"usage":{}}`)
	}))
	t.Cleanup(srv.Close)

	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion)
	if _, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free"}); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	var raw map[string]json.RawMessage
	if err := jsonv2.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if value, present := raw["max_output_tokens"]; present {
		t.Errorf("max_output_tokens = %s, want absent for an unset budget", value)
	}
}
