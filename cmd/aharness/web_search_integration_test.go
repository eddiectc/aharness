//go:build unix

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func chatWebSearchToolCallResponse() map[string]any {
	return map[string]any{
		"id": "chatcmpl-search", "object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_search", "type": "function",
					"function": map[string]any{
						"name":      "web_search",
						"arguments": `{"query":"aharness search","max_results":3}`,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
	}
}

func newFakeSearx(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestBinaryWebSearch(t *testing.T) {
	t.Parallel()
	bin := buildHarness(t)

	t.Run("configured", func(t *testing.T) {
		requests := make(chan *http.Request, 1)
		searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- r.Clone(context.Background())
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"results": [
					{"title": "Example <b>Result</b>", "url": "https://example.com/result#section", "content": "A &amp; B snippet."}
				],
				"unresponsive_engines": [["google", "timeout"]]
			}`)
		}))
		t.Cleanup(searx.Close)

		provider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
			if call == 1 {
				writeJSON(w, 200, chatWebSearchToolCallResponse())
				return
			}
			for _, want := range []string{"Example Result", "https://example.com/result", "A & B snippet."} {
				if !bytes.Contains(body, []byte(want)) {
					t.Errorf("second provider request missing %q:\n%s", want, body)
				}
			}
			writeJSON(w, 200, chatFinalResponse("search done"))
		})

		run := runHarnessEnv(t, bin, t.TempDir(), filepath.Join(t.TempDir(), "state"), provider.url,
			[]string{"AHARNESS_SEARXNG_URL=" + searx.URL},
			"-task", "search the web",
			"-model", "deepseek-v4.1-flash",
			"-max-turns", "5",
		)
		if run.exit != 0 {
			t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
		}
		if !strings.Contains(run.stdout, "search done") {
			t.Errorf("stdout = %q, want the final assistant text", run.stdout)
		}
		for _, want := range []string{
			`"type":"tool_result"`,
			`"name":"web_search"`,
			`"code":"ok"`,
			`"results":1`,
			`"unresponsive":["google: timeout"]`,
		} {
			if !strings.Contains(run.log, want) {
				t.Errorf("session log missing %q:\n%s", want, run.log)
			}
		}
		if got := strings.Count(run.log, `"type":"tool_result"`); got != 1 {
			t.Errorf("tool_result events = %d, want 1", got)
		}
		for _, want := range []string{"search_configured=true", "search_timeout_ms=15000"} {
			if !strings.Contains(run.stderr, want) {
				t.Errorf("startup log missing %q:\n%s", want, run.stderr)
			}
		}
		if strings.Contains(run.stderr, searx.URL) {
			t.Errorf("startup log leaked the SearXNG URL:\n%s", run.stderr)
		}

		request := <-requests
		if request.URL.Path != "/search" {
			t.Errorf("SearXNG path = %q, want /search", request.URL.Path)
		}
		query := request.URL.Query()
		if got := query.Get("q"); got != "aharness search" {
			t.Errorf("q = %q, want the model query", got)
		}
		if got := query.Get("format"); got != "json" {
			t.Errorf("format = %q, want json", got)
		}
	})

	t.Run("unconfigured", func(t *testing.T) {
		provider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
			if call == 1 {
				writeJSON(w, 200, chatWebSearchToolCallResponse())
				return
			}
			if !bytes.Contains(body, []byte("[unavailable]")) || !bytes.Contains(body, []byte("AHARNESS_SEARXNG_URL")) {
				t.Errorf("second provider request missing the unavailable result:\n%s", body)
			}
			writeJSON(w, 200, chatFinalResponse("search unavailable"))
		})
		run := runHarnessEnv(t, bin, t.TempDir(), filepath.Join(t.TempDir(), "state"), provider.url, nil,
			"-task", "search the web",
			"-model", "deepseek-v4.1-flash",
			"-max-turns", "5",
		)
		if run.exit != 0 {
			t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
		}
		if !strings.Contains(run.stdout, "search unavailable") {
			t.Errorf("stdout = %q, want the final assistant text", run.stdout)
		}
		if !strings.Contains(run.log, `"code":"unavailable"`) {
			t.Errorf("session log missing the unavailable result:\n%s", run.log)
		}
		if !strings.Contains(run.stderr, "search_configured=false") {
			t.Errorf("startup log missing search_configured=false:\n%s", run.stderr)
		}
	})

	t.Run("http error", func(t *testing.T) {
		searx := newFakeSearx(t, http.StatusInternalServerError, "boom")
		provider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
			if call == 1 {
				writeJSON(w, 200, chatWebSearchToolCallResponse())
				return
			}
			if !bytes.Contains(body, []byte("[http_error]")) || !bytes.Contains(body, []byte("500")) {
				t.Errorf("second provider request missing the HTTP error result:\n%s", body)
			}
			writeJSON(w, 200, chatFinalResponse("search failed"))
		})
		run := runHarnessEnv(t, bin, t.TempDir(), filepath.Join(t.TempDir(), "state"), provider.url,
			[]string{"AHARNESS_SEARXNG_URL=" + searx.URL},
			"-task", "search the web",
			"-model", "deepseek-v4.1-flash",
			"-max-turns", "5",
		)
		if run.exit != 0 {
			t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
		}
		if !strings.Contains(run.log, `"code":"http_error"`) {
			t.Errorf("session log missing the http_error result:\n%s", run.log)
		}
	})
}
