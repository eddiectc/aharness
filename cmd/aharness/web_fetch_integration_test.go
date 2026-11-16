//go:build unix

package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/internal/tools"
)

func chatWebFetchToolCallResponse(rawURL string) map[string]any {
	return map[string]any{
		"id": "chatcmpl-fetch", "object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_fetch", "type": "function",
					"function": map[string]any{
						"name":      "web_fetch",
						"arguments": `{"url":"` + rawURL + `"}`,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
	}
}

// fakeCrawl4AI returns an envelope with rendered HTML for any target.
func fakeCrawl4AI(t *testing.T, status int, envelope string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crawl" {
			t.Errorf("provider path = %q, want /crawl", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer integration-secret" {
			t.Errorf("provider authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, envelope)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestBinaryWebFetchCrawl4AI(t *testing.T) {
	bin := buildHarness(t)
	provider := fakeCrawl4AI(t, http.StatusOK, `{
		"success": true,
		"results": [{
			"success": true,
			"html": "<html><head><title>Rendered Docs</title></head><body><article><p>Rendered article body.</p><script>SCRIPT_MARKER</script></article></body></html>",
			"status_code": 200,
			"response_headers": {"content-type": "text/html; charset=utf-8"}
		}]
	}`)

	var sawTool bool
	model := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatWebFetchToolCallResponse("https://example.com/docs"))
			return
		}
		if !bytes.Contains(body, []byte("Rendered article body.")) || !bytes.Contains(body, []byte("Provider: crawl4ai")) {
			t.Errorf("second provider request missing the extracted result:\n%s", body)
		}
		if bytes.Contains(body, []byte("SCRIPT_MARKER")) {
			t.Errorf("script text reached the transcript:\n%s", body)
		}
		sawTool = true
		writeJSON(w, 200, chatFinalResponse("fetch done"))
	})

	run := runHarnessEnv(t, bin, t.TempDir(), filepath.Join(t.TempDir(), "state"), model.url, nil,
		"-task", "fetch the docs",
		"-model", "deepseek-v4.1-flash",
		"-max-turns", "5",
		"-web-fetch-provider", "crawl4ai",
		"-web-fetch-url", provider.URL,
		"-web-fetch-key", "integration-secret",
		"-web-timeout", "20s",
	)
	if run.exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
	}
	if !sawTool {
		t.Errorf("the model never saw the fetch result")
	}
	if !strings.Contains(run.stdout, "fetch done") {
		t.Errorf("stdout = %q, want the final assistant text", run.stdout)
	}
	for _, want := range []string{
		`"type":"tool_result"`,
		`"name":"web_fetch"`,
		`"code":"ok"`,
		`"provider":"crawl4ai"`,
		`"extraction":"trafilatura"`,
	} {
		if !strings.Contains(run.log, want) {
			t.Errorf("session log missing %q:\n%s", want, run.log)
		}
	}
	if got := strings.Count(run.log, `"type":"tool_result"`); got != 1 {
		t.Errorf("tool_result events = %d, want 1", got)
	}
	if !strings.Contains(run.log, `"toolset_version":"`+tools.Version+`"`) {
		t.Errorf("session log does not record %s:\n%s", tools.Version, run.log)
	}
	for _, want := range []string{
		"web_timeout_ms=20000",
		"web_fetch_provider=crawl4ai",
		"web_fetch_host=127.0.0.1:",
	} {
		if !strings.Contains(run.stderr, want) {
			t.Errorf("startup log missing %q:\n%s", want, run.stderr)
		}
	}
	if strings.Contains(run.stderr, "integration-secret") {
		t.Errorf("startup log leaked the provider key:\n%s", run.stderr)
	}
}

func TestBinaryWebFetchBuiltinBlockedAddress(t *testing.T) {
	bin := buildHarness(t)
	model := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatWebFetchToolCallResponse("http://127.0.0.1:1/"))
			return
		}
		if !bytes.Contains(body, []byte("[blocked_address]")) {
			t.Errorf("second provider request missing the blocked result:\n%s", body)
		}
		writeJSON(w, 200, chatFinalResponse("blocked"))
	})

	run := runHarnessEnv(t, bin, t.TempDir(), filepath.Join(t.TempDir(), "state"), model.url, nil,
		"-task", "fetch a private page",
		"-model", "deepseek-v4.1-flash",
		"-max-turns", "5",
	)
	if run.exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
	}
	if !strings.Contains(run.log, `"code":"blocked_address"`) {
		t.Errorf("session log missing blocked_address:\n%s", run.log)
	}
	if !strings.Contains(run.stderr, "web_fetch_provider=builtin") {
		t.Errorf("startup log missing the builtin provider:\n%s", run.stderr)
	}
}

func TestBinaryWebFetchProviderFailure(t *testing.T) {
	bin := buildHarness(t)
	provider := fakeCrawl4AI(t, http.StatusInternalServerError, `{"detail":"boom"}`)
	model := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatWebFetchToolCallResponse("https://example.com/docs"))
			return
		}
		if !bytes.Contains(body, []byte("[fetch_failed]")) {
			t.Errorf("second provider request missing the fetch_failed result:\n%s", body)
		}
		writeJSON(w, 200, chatFinalResponse("provider failed"))
	})

	run := runHarnessEnv(t, bin, t.TempDir(), filepath.Join(t.TempDir(), "state"), model.url, nil,
		"-task", "fetch the docs",
		"-model", "deepseek-v4.1-flash",
		"-max-turns", "5",
		"-web-fetch-provider", "crawl4ai",
		"-web-fetch-url", provider.URL,
		"-web-fetch-key", "integration-secret",
	)
	if run.exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
	}
	if !strings.Contains(run.log, `"code":"fetch_failed"`) {
		t.Errorf("session log missing fetch_failed:\n%s", run.log)
	}
}

func TestBinaryWebFetchResumeKeepsToolsetV1(t *testing.T) {
	bin := buildHarness(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	workspace := t.TempDir()

	var firstRequest []byte
	model := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			firstRequest = append([]byte(nil), body...)
		}
		writeJSON(w, 200, chatFinalResponse("done"))
	})
	run := runHarnessEnv(t, bin, workspace, stateDir, model.url, nil,
		"-task", "hello",
		"-model", "deepseek-v4.1-flash",
		"-max-turns", "2",
	)
	if run.exit != 0 || run.id == "" {
		t.Fatalf("exit = %d id = %q\nstderr:\n%s", run.exit, run.id, run.stderr)
	}
	if !bytes.Contains(firstRequest, []byte(`"name":"web_fetch"`)) {
		t.Errorf("first model request does not advertise web_fetch:\n%s", firstRequest)
	}

	resumeModel := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 && !bytes.Contains(body, []byte(`"name":"web_fetch"`)) {
			t.Errorf("resumed model request does not advertise web_fetch:\n%s", body)
		}
		writeJSON(w, 200, chatFinalResponse("resumed"))
	})
	resumed := runHarnessEnv(t, bin, workspace, stateDir, resumeModel.url, nil,
		"-resume", run.id,
		"-task", "continue",
		"-model", "deepseek-v4.1-flash",
		"-max-turns", "2",
	)
	if resumed.exit != 0 {
		t.Fatalf("resume exit = %d\nstderr:\n%s", resumed.exit, resumed.stderr)
	}
	if got := strings.Count(resumed.log, `"toolset_version":"`+tools.Version+`"`); got == 0 {
		t.Errorf("resumed session does not record %s:\n%s", tools.Version, resumed.log)
	}
	if strings.Contains(resumed.stderr, "unknown toolset version") {
		t.Errorf("resume reported a toolset version error:\n%s", resumed.stderr)
	}
}
