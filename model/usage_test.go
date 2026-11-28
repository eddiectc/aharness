package model

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

func TestUsageTotalExcludesSubsets(t *testing.T) {
	usage := Usage{
		InputTokens:           100,
		OutputTokens:          20,
		CachedInputTokens:     40,
		CacheWriteInputTokens: 10,
		ReasoningOutputTokens: 5,
		Reported:              true,
	}
	if got, want := usage.Total(), 120; got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
	if got, want := usage.UncachedInputTokens(), 50; got != want {
		t.Errorf("UncachedInputTokens() = %d, want %d", got, want)
	}
}

func TestUsageUncachedClampsNegative(t *testing.T) {
	usage := Usage{InputTokens: 1, CachedInputTokens: 2, CacheWriteInputTokens: 3}
	if got := usage.UncachedInputTokens(); got != 0 {
		t.Errorf("UncachedInputTokens() = %d, want 0", got)
	}
}

func TestUsageAddCarriesEveryField(t *testing.T) {
	tests := []struct {
		name  string
		left  Usage
		right Usage
		want  Usage
	}{
		{
			name: "totals and subsets add",
			left: Usage{
				InputTokens: 1000, OutputTokens: 30,
				CachedInputTokens: 800, CacheWriteInputTokens: 120,
				ReasoningOutputTokens: 20, Reported: true,
			},
			right: Usage{
				InputTokens: 10, OutputTokens: 5,
				CachedInputTokens: 2, CacheWriteInputTokens: 3,
				ReasoningOutputTokens: 1, Reported: true,
			},
			want: Usage{
				InputTokens: 1010, OutputTokens: 35,
				CachedInputTokens: 802, CacheWriteInputTokens: 123,
				ReasoningOutputTokens: 21, Reported: true,
			},
		},
		{
			name:  "reported on the left only",
			left:  Usage{InputTokens: 2, Reported: true},
			right: Usage{OutputTokens: 3},
			want:  Usage{InputTokens: 2, OutputTokens: 3, Reported: true},
		},
		{
			name:  "reported on the right only",
			left:  Usage{InputTokens: 2},
			right: Usage{OutputTokens: 3, Reported: true},
			want:  Usage{InputTokens: 2, OutputTokens: 3, Reported: true},
		},
		{
			name:  "absent on both sides",
			left:  Usage{InputTokens: 2},
			right: Usage{OutputTokens: 3},
			want:  Usage{InputTokens: 2, OutputTokens: 3},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.left.Add(tc.right); got != tc.want {
				t.Fatalf("%+v + %+v = %+v, want %+v", tc.left, tc.right, got, tc.want)
			}
		})
	}
}

func TestValidateUsage(t *testing.T) {
	tests := []struct {
		name    string
		usage   Usage
		wantErr bool
	}{
		{"zero", Usage{}, false},
		{"present zero", Usage{Reported: true}, false},
		{"valid subsets", Usage{InputTokens: 10, CachedInputTokens: 4, CacheWriteInputTokens: 6, OutputTokens: 5, ReasoningOutputTokens: 5, Reported: true}, false},
		{"negative input", Usage{InputTokens: -1}, true},
		{"negative output", Usage{OutputTokens: -1}, true},
		{"negative cached", Usage{CachedInputTokens: -1}, true},
		{"cached exceeds input", Usage{InputTokens: 5, CachedInputTokens: 6}, true},
		{"cached plus write exceeds input", Usage{InputTokens: 5, CachedInputTokens: 3, CacheWriteInputTokens: 3}, true},
		{"reasoning exceeds output", Usage{OutputTokens: 2, ReasoningOutputTokens: 3}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUsage(tc.usage)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateUsage(%+v) error = %v, wantErr %v", tc.usage, err, tc.wantErr)
			}
			if err != nil {
				var protoErr *ProtocolError
				if !errors.As(err, &protoErr) {
					t.Errorf("error type = %T, want *ProtocolError", err)
				}
			}
		})
	}
}

func TestChatUsageMapping(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Usage
	}{
		{
			name: "absent",
			raw:  `{}`,
			want: Usage{},
		},
		{
			name: "present empty",
			raw:  `{"usage":{}}`,
			want: Usage{Reported: true},
		},
		{
			name: "populated details",
			raw: `{"usage":{"prompt_tokens":100,"completion_tokens":20,
				"prompt_tokens_details":{"cached_tokens":40,"cache_write_tokens":10},
				"completion_tokens_details":{"reasoning_tokens":5}}}`,
			want: Usage{
				InputTokens: 100, OutputTokens: 20,
				CachedInputTokens: 40, CacheWriteInputTokens: 10,
				ReasoningOutputTokens: 5, Reported: true,
			},
		},
		{
			name: "detail preferred over cache-hit fallback",
			raw: `{"usage":{"prompt_tokens":100,"completion_tokens":20,
				"prompt_tokens_details":{"cached_tokens":40},
				"prompt_cache_hit_tokens":90}}`,
			want: Usage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 40, Reported: true},
		},
		{
			name: "cache-hit fallback",
			raw:  `{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":40}}`,
			want: Usage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 40, Reported: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const choices = `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]`
			body := choices + `}`
			if tc.raw != `{}` {
				body = choices + `,` + tc.raw[1:]
			}
			resp, err := decodeChatResponse([]byte(body))
			if err != nil {
				t.Fatalf("decodeChatResponse() error = %v", err)
			}
			if resp.Usage != tc.want {
				t.Errorf("usage = %+v, want %+v", resp.Usage, tc.want)
			}
		})
	}
}

func TestChatUsageSubsetValidation(t *testing.T) {
	bodies := []string{
		`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":4}}}`,
		`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"completion_tokens_details":{"reasoning_tokens":3}}}`,
	}
	for _, body := range bodies {
		if _, err := decodeChatResponse([]byte(body)); err == nil {
			t.Errorf("decodeChatResponse(%s) error = nil, want protocol error", body)
		} else {
			var protoErr *ProtocolError
			if !errors.As(err, &protoErr) {
				t.Errorf("error type = %T, want *ProtocolError", err)
			}
		}
	}
}

func TestChatStreamUsageDetails(t *testing.T) {
	body := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20," +
		"\"prompt_tokens_details\":{\"cached_tokens\":40,\"cache_write_tokens\":10}," +
		"\"completion_tokens_details\":{\"reasoning_tokens\":5}}}\n\ndata: [DONE]\n\n"
	resp, err := consumeChatStream(bytes.NewReader([]byte(body)), StreamHandler{})
	if err != nil {
		t.Fatalf("consumeChatStream() error = %v", err)
	}
	want := Usage{
		InputTokens: 100, OutputTokens: 20,
		CachedInputTokens: 40, CacheWriteInputTokens: 10,
		ReasoningOutputTokens: 5, Reported: true,
	}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestChatStreamUsageSubsetValidation(t *testing.T) {
	body := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2," +
		"\"prompt_tokens_details\":{\"cached_tokens\":9}}}\n\ndata: [DONE]\n\n"
	if _, err := consumeChatStream(bytes.NewReader([]byte(body)), StreamHandler{}); err == nil {
		t.Fatal("consumeChatStream() error = nil, want protocol error")
	}
}

func TestResponsesUsageMapping(t *testing.T) {
	const output = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]`
	tests := []struct {
		name string
		body string
		want Usage
	}{
		{name: "absent", body: output + `}`, want: Usage{}},
		{name: "present empty", body: output + `,"usage":{}}`, want: Usage{Reported: true}},
		{
			name: "populated details",
			body: output + `,"usage":{"input_tokens":100,"output_tokens":20,` +
				`"input_tokens_details":{"cached_tokens":40,"cache_write_tokens":10},` +
				`"output_tokens_details":{"reasoning_tokens":5}}}`,
			want: Usage{
				InputTokens: 100, OutputTokens: 20,
				CachedInputTokens: 40, CacheWriteInputTokens: 10,
				ReasoningOutputTokens: 5, Reported: true,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := decodeResponsesResponse([]byte(tc.body), "token")
			if err != nil {
				t.Fatalf("decodeResponsesResponse() error = %v", err)
			}
			if resp.Usage != tc.want {
				t.Errorf("usage = %+v, want %+v", resp.Usage, tc.want)
			}
		})
	}
}

func TestResponsesUsageSubsetValidation(t *testing.T) {
	bodies := []string{
		`{"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":4}}}`,
		`{"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2,"output_tokens_details":{"reasoning_tokens":3}}}`,
	}
	for _, body := range bodies {
		if _, err := decodeResponsesResponse([]byte(body), "token"); err == nil {
			t.Errorf("decodeResponsesResponse(%s) error = nil, want protocol error", body)
		}
	}
}

func TestResponsesStreamUsageDetails(t *testing.T) {
	body := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]," +
		"\"usage\":{\"input_tokens\":100,\"output_tokens\":20," +
		"\"input_tokens_details\":{\"cached_tokens\":40,\"cache_write_tokens\":10}," +
		"\"output_tokens_details\":{\"reasoning_tokens\":5}}}}\n\n"
	resp, err := consumeResponsesStream(bytes.NewReader([]byte(body)), StreamHandler{}, "token")
	if err != nil {
		t.Fatalf("consumeResponsesStream() error = %v", err)
	}
	want := Usage{
		InputTokens: 100, OutputTokens: 20,
		CachedInputTokens: 40, CacheWriteInputTokens: 10,
		ReasoningOutputTokens: 5, Reported: true,
	}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestResponsesStreamPresentEmptyUsage(t *testing.T) {
	body := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{}}}\n\n"
	resp, err := consumeResponsesStream(bytes.NewReader([]byte(body)), StreamHandler{}, "token")
	if err != nil {
		t.Fatalf("consumeResponsesStream() error = %v", err)
	}
	if !resp.Usage.Reported {
		t.Errorf("Reported = false, want true for a present empty usage object")
	}
}

func TestUsageJSONBackwardCompatible(t *testing.T) {
	var usage Usage
	if err := jsonv2.Unmarshal([]byte(`{"input_tokens":3,"output_tokens":4}`), &usage); err != nil {
		t.Fatalf("decode old usage: %v", err)
	}
	if usage.Reported {
		t.Errorf("Reported = true, want false for an old payload")
	}
	if usage.Total() != 7 {
		t.Errorf("Total() = %d, want 7", usage.Total())
	}
}
