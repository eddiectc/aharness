package model

import (
	"bytes"
	"strings"
	"testing"
)

// TestChatReasoningMapping covers the display-only thinking text that the Chat
// adapter decodes from DeepSeek-compatible reasoning_content and from
// OpenAI-compatible reasoning.
func TestChatReasoningMapping(t *testing.T) {
	t.Run("buffered", func(t *testing.T) {
		srv := newStaticServer(t, staticResponse{
			status: 200,
			body: []byte(`{"choices":[{"message":{"content":"answer","reasoning_content":"step one"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":2}}`),
		})
		adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
		resp, err := adapter.Complete(t.Context(), Request{Model: "deepseek-v4.1-flash"})
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if resp.Reasoning != "step one" || resp.Text != "answer" {
			t.Fatalf("response = %+v, want reasoning and text", resp)
		}
	})

	t.Run("buffered reasoning field", func(t *testing.T) {
		srv := newStaticServer(t, staticResponse{
			status: 200,
			body: []byte(`{"choices":[{"message":{"content":"answer","reasoning":"step one"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":2}}`),
		})
		adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
		resp, err := adapter.Complete(t.Context(), Request{Model: "deepseek-v4.1-flash"})
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if resp.Reasoning != "step one" {
			t.Fatalf("Reasoning = %q, want step one", resp.Reasoning)
		}
	})

	t.Run("reasoning content wins over reasoning", func(t *testing.T) {
		srv := newStaticServer(t, staticResponse{
			status: 200,
			body: []byte(`{"choices":[{"message":{"content":"answer","reasoning_content":"preferred","reasoning":"other"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":2}}`),
		})
		adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
		resp, err := adapter.Complete(t.Context(), Request{Model: "deepseek-v4.1-flash"})
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if resp.Reasoning != "preferred" {
			t.Fatalf("Reasoning = %q, want reasoning_content", resp.Reasoning)
		}
	})

	t.Run("buffered without reasoning", func(t *testing.T) {
		srv := newStaticServer(t, staticResponse{
			status: 200,
			body:   []byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`),
		})
		adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
		resp, err := adapter.Complete(t.Context(), Request{Model: "deepseek-v4.1-flash"})
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if resp.Reasoning != "" {
			t.Fatalf("Reasoning = %q, want empty", resp.Reasoning)
		}
	})
}

func TestChatStreamReasoningDeltas(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"plan "}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"reasoning":"more"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: {"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	srv := newSSEServer(t, body)
	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
	var text, reasoning strings.Builder
	resp, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, StreamHandler{
		Text:      func(delta string) error { text.WriteString(delta); return nil },
		Reasoning: func(delta string) error { reasoning.WriteString(delta); return nil },
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if text.String() != "answer" {
		t.Errorf("text deltas = %q, want answer", text.String())
	}
	if reasoning.String() != "plan more" {
		t.Errorf("reasoning deltas = %q, want %q", reasoning.String(), "plan more")
	}
	if resp.Reasoning != "plan more" || resp.Text != "answer" {
		t.Errorf("response = %+v", resp)
	}
}

func TestChatStreamReasoningIsCapped(t *testing.T) {
	huge := strings.Repeat("r", MaxReasoningText+1024)
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"" + huge + "\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n" +
		"data: [DONE]\n\n"
	srv := newSSEServer(t, body)
	adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
	var emitted strings.Builder
	resp, err := adapter.CompleteStream(t.Context(), Request{Model: "deepseek-v4.1-flash"}, StreamHandler{
		Reasoning: func(delta string) error { emitted.WriteString(delta); return nil },
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if len(resp.Reasoning) != MaxReasoningText || emitted.Len() != MaxReasoningText {
		t.Fatalf("reasoning = %d bytes, emitted = %d, want the %d-byte cap",
			len(resp.Reasoning), emitted.Len(), MaxReasoningText)
	}
}

func TestResponsesReasoningMapping(t *testing.T) {
	t.Run("buffered summary and content", func(t *testing.T) {
		srv := newStaticServer(t, staticResponse{
			status: 200,
			body: []byte(`{"status":"completed","output":[` +
				`{"type":"reasoning","summary":[{"type":"summary_text","text":"checked the file"}],` +
				`"content":[{"type":"reasoning_text","text":"raw thought"}]},` +
				`{"type":"message","content":[{"type":"output_text","text":"answer"}]}],` +
				`"usage":{"input_tokens":1,"output_tokens":2}}`),
		})
		adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion, 0)
		resp, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free"})
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if resp.Text != "answer" {
			t.Fatalf("Text = %q, want answer", resp.Text)
		}
		if resp.Reasoning != "checked the file\n\nraw thought" {
			t.Fatalf("Reasoning = %q", resp.Reasoning)
		}
	})

	t.Run("encrypted content is dropped", func(t *testing.T) {
		srv := newStaticServer(t, staticResponse{
			status: 200,
			body: []byte(`{"status":"completed","output":[` +
				`{"type":"reasoning","summary":[{"type":"summary_text","text":"visible"}],` +
				`"encrypted_content":"ciphertext"},` +
				`{"type":"message","content":[{"type":"output_text","text":"answer"}]}],` +
				`"usage":{"input_tokens":1,"output_tokens":2}}`),
		})
		adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion, 0)
		resp, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free"})
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if resp.Reasoning != "visible" {
			t.Fatalf("Reasoning = %q, want the summary only", resp.Reasoning)
		}
	})
}

func TestResponsesStreamReasoningDeltas(t *testing.T) {
	body := strings.Join([]string{
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"sum "}`,
		``,
		`event: response.reasoning_text.delta`,
		`data: {"type":"response.reasoning_text.delta","delta":"raw"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":2,"output_tokens":3}}}`,
		``,
	}, "\n")
	srv := newSSEServer(t, body)
	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion, 0)
	var text, reasoning strings.Builder
	resp, err := adapter.CompleteStream(t.Context(), Request{Model: "crouton-alpha-free"}, StreamHandler{
		Text:      func(delta string) error { text.WriteString(delta); return nil },
		Reasoning: func(delta string) error { reasoning.WriteString(delta); return nil },
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if text.String() != "answer" || reasoning.String() != "sum raw" {
		t.Fatalf("text = %q, reasoning = %q", text.String(), reasoning.String())
	}
	// The terminal response carried no reasoning item, so the streamed text is
	// kept for display.
	if resp.Reasoning != "sum raw" {
		t.Fatalf("Reasoning = %q, want the streamed deltas", resp.Reasoning)
	}
}

func TestResponsesStreamTerminalReasoningWins(t *testing.T) {
	body := strings.Join([]string{
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"streamed"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[` +
			`{"type":"reasoning","summary":[{"type":"summary_text","text":"final"}]},` +
			`{"type":"message","content":[{"type":"output_text","text":"answer"}]}],` +
			`"usage":{"input_tokens":2,"output_tokens":3}}}`,
		``,
	}, "\n")
	srv := newSSEServer(t, body)
	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion, 0)
	resp, err := adapter.CompleteStream(t.Context(), Request{Model: "crouton-alpha-free"}, StreamHandler{
		Reasoning: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if resp.Reasoning != "final" {
		t.Fatalf("Reasoning = %q, want the terminal summary", resp.Reasoning)
	}
}

func TestResponsesReasoningIsCapped(t *testing.T) {
	// Two summary parts whose concatenation exceeds the cap keep the first part
	// and truncate the second, including its separator.
	first := strings.Repeat("a", MaxReasoningText-4)
	second := strings.Repeat("b", 100)
	body := `{"status":"completed","output":[` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"` + first + `"},{"type":"summary_text","text":"` + second + `"}]},` +
		`{"type":"message","content":[{"type":"output_text","text":"answer"}]}],` +
		`"usage":{"input_tokens":1,"output_tokens":2}}`
	srv := newStaticServer(t, staticResponse{status: 200, body: []byte(body)})
	adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion, 0)
	resp, err := adapter.Complete(t.Context(), Request{Model: "crouton-alpha-free"})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if len(resp.Reasoning) != MaxReasoningText {
		t.Fatalf("Reasoning = %d bytes, want exactly %d", len(resp.Reasoning), MaxReasoningText)
	}
	if !strings.HasPrefix(resp.Reasoning, first+"\n\n") {
		t.Fatal("the first summary part was altered")
	}
	if !strings.ContainsRune(resp.Reasoning, 'b') {
		t.Fatal("the second summary part lost its fitting prefix")
	}
}

func TestReasoningNeverEntersCanonicalResponseBytes(t *testing.T) {
	resp := Response{
		Text:      "answer",
		Reasoning: strings.Repeat("r", MaxReasoningText),
		ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", Arguments: []byte(`{}`)}},
	}
	if got, want := responseContentBytes(resp), len("answer")+len("call_1")+len("read_file")+len("{}"); got != want {
		t.Fatalf("responseContentBytes() = %d, want %d; reasoning must not count", got, want)
	}
	if !bytes.Contains([]byte(capReasoning(resp.Reasoning)), []byte("rrrr")) {
		t.Fatal("capReasoning dropped text under the cap")
	}
}
