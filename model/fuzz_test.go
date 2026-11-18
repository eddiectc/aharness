package model

import (
	"bytes"
	"testing"
)

func FuzzSSEDecoder(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("data: [DONE]\n\n"))
	f.Add([]byte(": comment\r\n\r\ndata: {\"x\":1}\r\n\r\n"))
	f.Add([]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		dec := newSSEDecoder(bytes.NewReader(data))
		for range 64 {
			if _, err := dec.Next(); err != nil {
				return
			}
		}
	})
}

func FuzzChatStream(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("data: [DONE]\n\n"))
	f.Add([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
	f.Add([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))
	f.Add([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"prompt_tokens_details\":{\"cached_tokens\":3,\"cache_write_tokens\":2},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\ndata: [DONE]\n\n"))
	f.Add([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cached_tokens\":9}}}\n\ndata: [DONE]\n\n"))
	f.Add([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"completion_tokens_details\":{\"reasoning_tokens\":9}}}\n\ndata: [DONE]\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = consumeChatStream(bytes.NewReader(data), StreamHandler{})
	})
}

func FuzzResponsesStream(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n"))
	f.Add([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"input_tokens_details\":{\"cached_tokens\":3,\"cache_write_tokens\":2},\"output_tokens_details\":{\"reasoning_tokens\":1}}}}\n\n"))
	f.Add([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"input_tokens_details\":{\"cached_tokens\":9}}}}\n\n"))
	f.Add([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = consumeResponsesStream(bytes.NewReader(data), StreamHandler{}, "token")
	})
}
