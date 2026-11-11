//go:build unix

package main

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

type recordedLimits struct {
	MaxContextTokens int `json:"max_context_tokens"`
	MaxOutputTokens  int `json:"max_output_tokens"`
	SafetyMargin     int `json:"safety_margin"`
	MaxInputTokens   int `json:"max_input_tokens"`
	CompactionTarget int `json:"compaction_target"`
}

type recordedSessionStart struct {
	Provider         string         `json:"provider"`
	Dialect          string         `json:"dialect"`
	Model            string         `json:"model"`
	BaseURL          string         `json:"base_url"`
	Limits           recordedLimits `json:"limits"`
	MaxTurns         int            `json:"max_turns"`
	MaxTotalTokens   int            `json:"max_total_tokens"`
	SessionTimeoutMS int64          `json:"session_timeout_ms"`
	ModelTimeoutMS   int64          `json:"model_timeout_ms"`
}

type recordedRunStart struct {
	Provider         string         `json:"provider"`
	Dialect          string         `json:"dialect"`
	Model            string         `json:"model"`
	BaseURL          string         `json:"base_url"`
	Limits           recordedLimits `json:"limits"`
	MaxTurns         int            `json:"max_turns"`
	MaxTotalTokens   int            `json:"max_total_tokens"`
	SessionTimeoutMS int64          `json:"session_timeout_ms"`
	ModelTimeoutMS   int64          `json:"model_timeout_ms"`
}

// recordedSession decodes the session_start and every run_start payload.
func recordedSession(t *testing.T, path string) (recordedSessionStart, []recordedRunStart) {
	t.Helper()
	events := readSessionEvents(t, path)
	var start recordedSessionStart
	var starts []recordedRunStart
	for _, ev := range events {
		switch ev.Type {
		case session.TypeSessionStart:
			if err := jsonv2.Unmarshal(ev.Data, &start); err != nil {
				t.Fatalf("decode session_start: %v", err)
			}
		case session.TypeRunStart:
			var rs recordedRunStart
			if err := jsonv2.Unmarshal(ev.Data, &rs); err != nil {
				t.Fatalf("decode run_start: %v", err)
			}
			starts = append(starts, rs)
		}
	}
	return start, starts
}

// hasJSONField reports whether a provider request carries the JSON field.
func hasJSONField(t *testing.T, body []byte, field string) bool {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode provider request: %v\n%s", err, body)
	}
	_, ok := decoded[field]
	return ok
}

// decodeJSONField decodes one request field into v.
func decodeJSONField(t *testing.T, body []byte, field string, v any) bool {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode provider request: %v\n%s", err, body)
	}
	raw, ok := decoded[field]
	if !ok {
		return false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode request field %s: %v", field, err)
	}
	return true
}

func assertNoSessionFiles(t *testing.T, stateDir string) {
	t.Helper()
	if files := sessionFiles(t, stateDir); len(files) != 0 {
		t.Fatalf("session files = %v, want none", files)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("sessions directory exists after a terminal action: %v", err)
	}
}

func TestBinaryListModels(t *testing.T) {
	bin := buildHarness(t)
	fake := startFakeModelsDev(t)
	stateDir := filepath.Join(t.TempDir(), "state")

	stdout, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", []string{
		"AHARNESS_MODELS_DEV_URL=" + fake.URL(),
		"AHARNESS_PROVIDER_TOKEN=",
	}, nil, "-list-models")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", exit, stderr)
	}
	for _, diagnostic := range []string{
		"no-tools", "tool calls are not supported",
		"no-context", "missing context limit",
		"exotic-sdk", "@ai-sdk/cohere",
	} {
		if !strings.Contains(stderr, diagnostic) {
			t.Errorf("stderr missing the ineligible-model diagnostic %q:\n%s", diagnostic, stderr)
		}
	}
	if !strings.HasPrefix(stdout, "ID\tNAME\tDIALECT\tCONTEXT\tOUTPUT\tSTATUS\n") {
		t.Fatalf("stdout does not start with the documented header:\n%s", stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 8 {
		t.Fatalf("list rows = %d, want header plus 7 usable models:\n%s", len(lines)-1, stdout)
	}
	wantRows := []string{
		"crouton-alpha-free\tCrouton Alpha Free\topenai-responses\t200000\t8192\t-",
		"deepseek-v4.1-flash\tDeepSeek V4.1 Flash\topenai-chat\t1000000\t384000\t-",
		"gpt-6-luna\tGPT-6 Luna\topenai-responses\t1050000\t128000\t-",
		"kimi-k2.7-code\tKimi K2.7 Code\topenai-chat\t262144\t262144\t-",
		"muse-spark-1.3-contributor\tMuse Spark 1.3 Contributor\topenai-responses\t200000\t8192\t-",
		"old-deprecated\tOld Deprecated\topenai-chat\t100000\t8192\tdeprecated",
		"qwen3.7-plus\tQwen3.7 Plus\topenai-chat\t131072\t8192\t-",
	}
	for i, want := range wantRows {
		if got := lines[i+1]; got != want {
			t.Errorf("row %d = %q, want %q", i+1, got, want)
		}
	}
	for _, omitted := range []string{"no-tools", "no-context", "exotic-sdk"} {
		if strings.Contains(stdout, omitted) {
			t.Errorf("ineligible model %q appears in the list:\n%s", omitted, stdout)
		}
	}
	assertNoSessionFiles(t, stateDir)

	// -list-models revalidates through the conditional path.
	headers := fake.Headers()
	if len(headers) != 1 {
		t.Fatalf("catalog requests = %d, want 1", len(headers))
	}
	if got := headers[0].Get("If-None-Match"); got != "" {
		t.Errorf("first list If-None-Match = %q, want empty without a cache", got)
	}
}

func TestBinaryListModelsUnknownProviderExitsTwo(t *testing.T) {
	bin := buildHarness(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	_, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, "", nil,
		"-list-models", "-provider", "other")
	if exit != exitConfig {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", exit, exitConfig, stderr)
	}
	if !strings.Contains(stderr, "opencode-go") {
		t.Fatalf("stderr does not list the supported provider:\n%s", stderr)
	}
	assertNoSessionFiles(t, stateDir)
}

func TestBinaryRefreshModels(t *testing.T) {
	bin := buildHarness(t)
	fake := startFakeModelsDev(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + fake.URL(), "AHARNESS_PROVIDER_TOKEN="}

	stdout, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-refresh-models")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", exit, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	wantPrefix := "refreshed 7 usable models for opencode-go from " + fake.URL() + " at "
	if !strings.HasPrefix(stdout, wantPrefix) {
		t.Fatalf("stdout = %q, want prefix %q", stdout, wantPrefix)
	}
	if strings.Count(stdout, "\n") != 1 {
		t.Fatalf("stdout = %q, want one summary line", stdout)
	}
	assertNoSessionFiles(t, stateDir)

	headers := fake.Headers()
	if len(headers) != 1 {
		t.Fatalf("catalog requests = %d, want 1", len(headers))
	}
	if got := headers[0].Get("If-None-Match"); got != "" {
		t.Errorf("unconditional refresh sent If-None-Match %q", got)
	}
	if got := headers[0].Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
	if got := headers[0].Get("User-Agent"); !strings.HasPrefix(got, "aharness/") {
		t.Errorf("User-Agent = %q", got)
	}
	cachePath := model.CatalogCachePath(stateDir, providerName)
	cacheBytes, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if !strings.Contains(string(cacheBytes), `"deepseek-v4.1-flash"`) {
		t.Fatalf("cache does not contain the catalog:\n%s", cacheBytes)
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("cache mode = %o, want 600", got)
	}

	t.Run("transport failure preserves the cache", func(t *testing.T) {
		fake.SetFailure(http.StatusInternalServerError, "catalog exploded")
		stdout, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-refresh-models")
		if exit != exitProvider {
			t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", exit, exitProvider, stdout, stderr)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want no summary on failure", stdout)
		}
		if !strings.Contains(stderr, fake.URL()) || !strings.Contains(stderr, cachePath) {
			t.Errorf("stderr does not name the source and cache:\n%s", stderr)
		}
		if got := mustRead(t, cachePath); string(got) != string(cacheBytes) {
			t.Fatal("failed refresh changed the cache")
		}
	})
	t.Run("invalid payload is a protocol error", func(t *testing.T) {
		fake.SetFailure(http.StatusOK, `{"opencode-go":`)
		_, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-refresh-models")
		if exit != exitProtocol {
			t.Fatalf("exit = %d, want %d\nstderr:\n%s", exit, exitProtocol, stderr)
		}
		if got := mustRead(t, cachePath); string(got) != string(cacheBytes) {
			t.Fatal("invalid refresh changed the cache")
		}
	})
	t.Run("unexpected 304 is a protocol error", func(t *testing.T) {
		fake.SetFailure(http.StatusNotModified, "")
		_, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-refresh-models")
		if exit != exitProtocol {
			t.Fatalf("exit = %d, want %d\nstderr:\n%s", exit, exitProtocol, stderr)
		}
		if got := mustRead(t, cachePath); string(got) != string(cacheBytes) {
			t.Fatal("304 refresh changed the cache")
		}
	})
	t.Run("no cache transport failure exits 3", func(t *testing.T) {
		fake.SetFailure(http.StatusBadGateway, "down")
		fresh := filepath.Join(t.TempDir(), "state")
		_, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), fresh, "", extra, nil, "-refresh-models")
		if exit != exitProvider {
			t.Fatalf("exit = %d, want %d\nstderr:\n%s", exit, exitProvider, stderr)
		}
		if _, err := os.Stat(model.CatalogCachePath(fresh, providerName)); !os.IsNotExist(err) {
			t.Fatalf("failed refresh wrote a cache: %v", err)
		}
	})
}

func TestBinaryRefreshThenList(t *testing.T) {
	bin := buildHarness(t)
	fake := startFakeModelsDev(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + fake.URL(), "AHARNESS_PROVIDER_TOKEN="}

	stdout, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-refresh-models", "-list-models")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", exit, stderr)
	}
	if !strings.HasPrefix(stdout, "ID\tNAME\tDIALECT\tCONTEXT\tOUTPUT\tSTATUS\n") {
		t.Fatalf("stdout does not start with the list header:\n%s", stdout)
	}
	if strings.Contains(stdout, "refreshed ") {
		t.Fatalf("refresh summary leaked to stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "refreshed 7 usable models for opencode-go from "+fake.URL()) {
		t.Fatalf("stderr missing the refresh summary:\n%s", stderr)
	}
	if calls := fake.Calls(); calls != 1 {
		t.Fatalf("catalog requests = %d, want exactly the refresh request", calls)
	}
	if got := fake.Headers()[0].Get("If-None-Match"); got != "" {
		t.Errorf("refresh request sent If-None-Match %q", got)
	}
	assertNoSessionFiles(t, stateDir)

	t.Run("refresh failure prevents the list", func(t *testing.T) {
		cachePath := model.CatalogCachePath(stateDir, providerName)
		before := mustRead(t, cachePath)
		fake.SetFailure(http.StatusInternalServerError, "refresh failed")
		stdout, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-refresh-models", "-list-models")
		if exit != exitProvider {
			t.Fatalf("exit = %d, want %d\nstderr:\n%s", exit, exitProvider, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want no list after a failed refresh", stdout)
		}
		if !strings.Contains(stderr, "refresh failed") && !strings.Contains(stderr, "status 500") {
			t.Fatalf("stderr = %q, want the refresh error", stderr)
		}
		if got := mustRead(t, cachePath); string(got) != string(before) {
			t.Fatal("failed combined refresh changed the cache")
		}
	})
}

func TestBinaryListRevalidatesAndFallsBackToCache(t *testing.T) {
	bin := buildHarness(t)
	fake := startFakeModelsDev(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + fake.URL(), "AHARNESS_PROVIDER_TOKEN="}

	// Seed a cache with the explicit refresh path.
	if _, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-refresh-models"); exit != 0 {
		t.Fatalf("seed refresh exit = %d\nstderr:\n%s", exit, stderr)
	}
	seeded := fake.Calls()

	stdout, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-list-models")
	if exit != 0 {
		t.Fatalf("revalidating list exit = %d\nstderr:\n%s", exit, stderr)
	}
	if !strings.HasPrefix(stdout, "ID\tNAME") {
		t.Fatalf("stdout = %q, want the list", stdout)
	}
	if strings.Contains(stderr, "using cached catalog") {
		t.Errorf("304 produced a stale-cache warning:\n%s", stderr)
	}
	headers := fake.Headers()
	if len(headers) != seeded+1 {
		t.Fatalf("catalog requests = %d, want %d", len(headers), seeded+1)
	}
	if got := headers[len(headers)-1].Get("If-None-Match"); got != `"test-catalog-v1"` {
		t.Fatalf("If-None-Match = %q, want the cached ETag", got)
	}

	fake.SetFailure(http.StatusServiceUnavailable, "catalog offline")
	stdout, stderr, exit = runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-list-models")
	if exit != 0 {
		t.Fatalf("stale-fallback list exit = %d\nstderr:\n%s", exit, stderr)
	}
	if !strings.HasPrefix(stdout, "ID\tNAME") {
		t.Fatalf("stale list stdout = %q, want the cached list", stdout)
	}
	if !strings.Contains(stderr, "using cached catalog") {
		t.Fatalf("stderr = %q, want a stale-cache warning", stderr)
	}
}

func TestBinaryFreshCacheSkipsCatalogRequests(t *testing.T) {
	bin := buildHarness(t)
	fake := startFakeModelsDev(t)
	provider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		writeJSON(w, 200, chatFinalResponse("ok"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + fake.URL()}

	if _, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, provider.url, extra, nil,
		"-batch", "-task", "first", "-model", "deepseek-v4.1-flash"); exit != 0 {
		t.Fatalf("seed run exit = %d\nstderr:\n%s", exit, stderr)
	}
	requests := fake.Calls()
	if requests != 1 {
		t.Fatalf("catalog requests after the first run = %d, want 1", requests)
	}
	// A second process must reuse the fresh cache.
	if _, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, provider.url, extra, nil,
		"-batch", "-task", "second", "-model", "kimi-k2.7-code"); exit != 0 {
		t.Fatalf("second run exit = %d\nstderr:\n%s", exit, stderr)
	}
	if got := fake.Calls(); got != requests {
		t.Fatalf("catalog requests = %d, want %d (fresh cache)", got, requests)
	}
	if got := len(sessionFiles(t, stateDir)); got != 2 {
		t.Fatalf("session files = %d, want 2", got)
	}
}

func TestBinaryExplicitOutputBudgetPassThrough(t *testing.T) {
	bin := buildHarness(t)
	cases := []struct {
		model string
		path  string
		field string
		final map[string]any
	}{
		{model: "deepseek-v4.1-flash", path: "/chat/completions", field: "max_tokens", final: chatFinalResponse("chat done")},
		{model: "crouton-alpha-free", path: "/responses", field: "max_output_tokens", final: responsesFinalResponse("responses done")},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
				}
				writeJSON(w, 200, tc.final)
			})
			stateDir := filepath.Join(t.TempDir(), "state")
			run := runHarness(t, bin, t.TempDir(), stateDir, f.url,
				"-task", "finish", "-model", tc.model, "-max-output-tokens", "1234")
			if run.exit != 0 {
				t.Fatalf("exit = %d\nstderr:\n%s", run.exit, run.stderr)
			}
			bodies := providerBodies(f)
			if len(bodies) != 1 {
				t.Fatalf("provider calls = %d, want 1", len(bodies))
			}
			var budget int
			if !decodeJSONField(t, bodies[0], tc.field, &budget) {
				t.Fatalf("request omits %s: %s", tc.field, bodies[0])
			}
			if budget != 1234 {
				t.Fatalf("%s = %d, want the explicit 1234", tc.field, budget)
			}
			start, starts := recordedSession(t, sessionPathFor(stateDir, run.id))
			if start.Limits.MaxOutputTokens != 1234 {
				t.Errorf("session_start output = %d, want 1234", start.Limits.MaxOutputTokens)
			}
			if len(starts) != 1 || starts[0].Limits.MaxOutputTokens != 1234 {
				t.Errorf("run_start output = %+v, want 1234", starts)
			}
		})
	}
}

func providerBodies(f *fakeProvider) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.bodies...)
}

func TestBinaryResumeOutputBudget(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		switch call {
		case 1:
			writeJSON(w, 200, chatFinalResponse("first done"))
		case 2:
			writeJSON(w, 200, chatFinalResponse("resumed done"))
		default:
			writeJSON(w, 200, chatFinalResponse("cleared done"))
		}
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	first := runHarness(t, bin, workspace, stateDir, f.url,
		"-task", "first", "-model", "deepseek-v4.1-flash", "-max-output-tokens", "777")
	if first.exit != 0 {
		t.Fatalf("first exit = %d\nstderr:\n%s", first.exit, first.stderr)
	}

	stdout, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, "", nil,
		"-batch", "-resume", first.id, "-task", "continue")
	if exit != 0 {
		t.Fatalf("resume exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	bodies := providerBodies(f)
	if len(bodies) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(bodies))
	}
	var inherited int
	if !decodeJSONField(t, bodies[1], "max_tokens", &inherited) {
		t.Fatalf("resumed request omits max_tokens: %s", bodies[1])
	}
	if inherited != 777 {
		t.Fatalf("resumed max_tokens = %d, want the recorded 777", inherited)
	}
	_, starts := recordedSession(t, sessionPathFor(stateDir, first.id))
	if len(starts) != 2 || starts[1].Limits.MaxOutputTokens != 777 {
		t.Fatalf("resumed run_start limits = %+v, want output 777", starts)
	}

	stdout, stderr, exit = runHarnessRaw(t, bin, workspace, stateDir, "", nil,
		"-batch", "-resume", first.id, "-task", "clear", "-max-output-tokens=0")
	if exit != 0 {
		t.Fatalf("clearing resume exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	bodies = providerBodies(f)
	if len(bodies) != 3 {
		t.Fatalf("provider calls = %d, want 3", len(bodies))
	}
	if hasJSONField(t, bodies[2], "max_tokens") {
		t.Fatalf("explicit zero still sent max_tokens: %s", bodies[2])
	}
	start, starts := recordedSession(t, sessionPathFor(stateDir, first.id))
	if len(starts) != 3 || starts[2].Limits.MaxOutputTokens != 0 {
		t.Fatalf("cleared run_start = %+v, want output 0", starts)
	}
	if start.Limits.MaxOutputTokens != 777 {
		t.Fatalf("session_start output = %d, want the original 777", start.Limits.MaxOutputTokens)
	}
}

func TestBinaryTranslatedModelUsesChat(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions for a translated model", r.URL.Path)
		}
		writeJSON(w, 200, chatFinalResponse("translated done"))
	})
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarness(t, bin, t.TempDir(), stateDir, f.url,
		"-task", "finish", "-model", "qwen3.7-plus")
	if run.exit != 0 {
		t.Fatalf("exit = %d\nstderr:\n%s", run.exit, run.stderr)
	}
	start, starts := recordedSession(t, sessionPathFor(stateDir, run.id))
	if start.Model != "qwen3.7-plus" || start.Dialect != "openai-chat" {
		t.Fatalf("session_start model/dialect = %q/%q", start.Model, start.Dialect)
	}
	if start.Limits.MaxContextTokens != 131_072 {
		t.Fatalf("session_start context = %d, want 131072", start.Limits.MaxContextTokens)
	}
	if len(starts) != 1 || starts[0].Dialect != "openai-chat" {
		t.Fatalf("run_start = %+v", starts)
	}
}

func TestBinaryDeprecatedModelWarnsAndRuns(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		writeJSON(w, 200, chatFinalResponse("deprecated done"))
	})
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarness(t, bin, t.TempDir(), stateDir, f.url,
		"-task", "finish", "-model", "old-deprecated")
	if run.exit != 0 {
		t.Fatalf("exit = %d\nstderr:\n%s", run.exit, run.stderr)
	}
	if !strings.Contains(run.stderr, "deprecated") {
		t.Fatalf("stderr missing the deprecated-model warning:\n%s", run.stderr)
	}
	start, _ := recordedSession(t, sessionPathFor(stateDir, run.id))
	if start.Model != "old-deprecated" || start.Limits.MaxContextTokens != 100_000 {
		t.Fatalf("session_start = %+v", start)
	}
}

func TestBinaryResumeOfflineWithoutExplicitModel(t *testing.T) {
	bin := buildHarness(t)
	catalog := startFakeModelsDev(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		writeJSON(w, 200, chatFinalResponse("resumed offline"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}
	_, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, f.url, extra, nil,
		"-batch", "-task", "first", "-model", "deepseek-v4.1-flash")
	if exit != 0 {
		t.Fatalf("first exit = %d\nstderr:\n%s", exit, stderr)
	}
	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session files = %v", files)
	}
	id := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
	before := catalog.Calls()
	if before == 0 {
		t.Fatal("the first run did not load the catalog")
	}
	// Remove the cache so any catalog access during resume would hit the
	// failing server instead of a fresh cache.
	if err := os.RemoveAll(filepath.Join(stateDir, "cache")); err != nil {
		t.Fatal(err)
	}
	catalog.SetFailure(http.StatusServiceUnavailable, "catalog offline")
	stdout, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, "", extra, nil,
		"-batch", "-resume", id, "-task", "continue")
	if exit != 0 {
		t.Fatalf("offline resume exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "resumed offline") {
		t.Fatalf("stdout = %q", stdout)
	}
	if got := catalog.Calls(); got != before {
		t.Fatalf("offline resume made %d catalog requests, want none", got-before)
	}
	start, starts := recordedSession(t, sessionPathFor(stateDir, id))
	if start.Model != "deepseek-v4.1-flash" {
		t.Fatalf("session_start model = %q", start.Model)
	}
	if len(starts) != 2 {
		t.Fatalf("run_start events = %d, want 2", len(starts))
	}
	rs := starts[1]
	if rs.Model != "deepseek-v4.1-flash" || rs.Dialect != "openai-chat" || rs.BaseURL != f.url {
		t.Fatalf("resumed run_start = %+v", rs)
	}
	if rs.Limits.MaxContextTokens != 1_000_000 {
		t.Fatalf("resumed context = %d, want recorded 1000000", rs.Limits.MaxContextTokens)
	}
}

func TestBinaryResumeExplicitModelChange(t *testing.T) {
	bin := buildHarness(t)
	catalog := startFakeModelsDev(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		switch call {
		case 1:
			if r.URL.Path != "/chat/completions" {
				t.Errorf("first path = %q, want /chat/completions", r.URL.Path)
			}
			writeJSON(w, 200, chatFinalResponse("first done"))
		default:
			if r.URL.Path != "/responses" {
				t.Errorf("changed-model path = %q, want /responses", r.URL.Path)
			}
			writeJSON(w, 200, responsesFinalResponse("changed done"))
		}
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}
	_, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, f.url, extra, nil,
		"-batch", "-task", "first", "-model", "deepseek-v4.1-flash")
	if exit != 0 {
		t.Fatalf("first exit = %d\nstderr:\n%s", exit, stderr)
	}
	files := sessionFiles(t, stateDir)
	id := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
	before := catalog.Calls()

	stdout, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, f.url, extra, nil,
		"-batch", "-resume", id, "-task", "switch", "-model", "crouton-alpha-free")
	if exit != 0 {
		t.Fatalf("resume exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "changed done") {
		t.Fatalf("stdout = %q", stdout)
	}
	if got := catalog.Calls(); got != before {
		t.Fatalf("explicit model change used %d catalog requests, want the fresh cache", got-before)
	}
	start, starts := recordedSession(t, sessionPathFor(stateDir, id))
	if start.Model != "deepseek-v4.1-flash" || start.Dialect != "openai-chat" {
		t.Fatalf("session_start changed: %+v", start)
	}
	if len(starts) != 2 {
		t.Fatalf("run_start events = %d, want 2", len(starts))
	}
	rs := starts[1]
	if rs.Model != "crouton-alpha-free" || rs.Dialect != "openai-responses" || rs.BaseURL != f.url {
		t.Fatalf("changed run_start = %+v", rs)
	}
	if rs.Limits.MaxContextTokens != 200_000 {
		t.Fatalf("changed context = %d, want 200000", rs.Limits.MaxContextTokens)
	}

	_, stderr, exit = runHarnessRawEnv(t, bin, workspace, stateDir, f.url, extra, nil,
		"-batch", "-resume", id, "-task", "bad", "-model", "no-tools")
	if exit != exitConfig {
		t.Fatalf("ineligible resume exit = %d, want %d\nstderr:\n%s", exit, exitConfig, stderr)
	}
	if !strings.Contains(stderr, "tool calls are not supported") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestBinaryCatalogFailureExitCodes(t *testing.T) {
	bin := buildHarness(t)
	cases := []struct {
		name   string
		body   string
		status int
		exit   int
	}{
		{name: "transport", status: http.StatusBadGateway, body: "down", exit: exitProvider},
		{name: "invalid json", status: http.StatusOK, body: `{"opencode-go":{"models":`, exit: exitProtocol},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog := startFakeModelsDev(t)
			catalog.SetFailure(tc.status, tc.body)
			provider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				t.Error("provider called despite catalog failure")
			})
			stateDir := filepath.Join(t.TempDir(), "state")
			_, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, provider.url,
				[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, nil,
				"-batch", "-task", "work", "-model", "deepseek-v4.1-flash")
			if exit != tc.exit {
				t.Fatalf("exit = %d, want %d\nstderr:\n%s", exit, tc.exit, stderr)
			}
			if !strings.Contains(stderr, catalog.URL()) || !strings.Contains(stderr, model.CatalogCachePath(stateDir, providerName)) {
				t.Fatalf("stderr does not name the source and cache path:\n%s", stderr)
			}
			assertNoSessionFiles(t, stateDir)
		})
	}
}

func TestBinaryLengthFinishStopsWithTokenLimit(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		writeJSON(w, 200, map[string]any{
			"id": "chatcmpl-length", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "partial output"},
				"finish_reason": "length",
			}},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 2},
		})
	})
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarness(t, bin, t.TempDir(), stateDir, f.url,
		"-task", "keep going", "-model", "deepseek-v4.1-flash")
	if run.exit != 2 {
		t.Fatalf("exit = %d, want 2 (token_limit)\nstderr:\n%s", run.exit, run.stderr)
	}
	if got := len(providerBodies(f)); got != 2 {
		t.Fatalf("provider calls = %d, want 2 consecutive length responses", got)
	}
	for _, want := range []string{`"kind":"consecutive_length"`, `"source":"continuation-hint"`, `"status":"token_limit"`} {
		if !strings.Contains(run.log, want) {
			t.Errorf("log missing %q:\n%s", want, run.log)
		}
	}
}

func TestBinaryContextOverflowCompactsAndRetries(t *testing.T) {
	bin := buildHarness(t)
	const toolTurns = 12
	cmdJSON := `{"command":"head -c 8000 /dev/zero | tr '\\0' x","timeout_ms":30000}`
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		switch {
		case call <= toolTurns:
			writeJSON(w, 200, map[string]any{
				"id": "chatcmpl-overflow", "object": "chat.completion",
				"choices": []any{map[string]any{
					"index": 0,
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []any{map[string]any{
							"id": "call_overflow", "type": "function",
							"function": map[string]any{"name": "run_command", "arguments": cmdJSON},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
			})
		case call == toolTurns+1:
			writeJSON(w, 400, map[string]any{"error": map[string]any{
				"message": "This model's maximum context length is exceeded",
			}})
		default:
			writeJSON(w, 200, chatFinalResponse("overflow recovered"))
		}
	})
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarness(t, bin, t.TempDir(), stateDir, f.url,
		"-task", "overflow", "-model", "deepseek-v4.1-flash",
		"-max-context-tokens", "12000", "-max-turns", "30")
	if run.exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout, "overflow recovered") {
		t.Fatalf("stdout = %q", run.stdout)
	}
	if !strings.Contains(run.log, `"policy":"provider-overflow"`) {
		t.Fatalf("log missing the provider-overflow compaction:\n%s", run.log)
	}
}

func ageCatalogCache(t *testing.T, path string) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(mustRead(t, path), &document); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	document["fetched_at"] = time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("age cache: %v", err)
	}
}

func TestBinaryStaleCacheFallbackOnImplicitRefresh(t *testing.T) {
	bin := buildHarness(t)
	catalog := startFakeModelsDev(t)
	provider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		writeJSON(w, 200, chatFinalResponse("stale catalog run"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}
	if _, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "", extra, nil, "-refresh-models"); exit != 0 {
		t.Fatalf("seed refresh exit = %d\nstderr:\n%s", exit, stderr)
	}
	cachePath := model.CatalogCachePath(stateDir, providerName)
	ageCatalogCache(t, cachePath)
	seeded := catalog.Calls()
	catalog.SetFailure(http.StatusBadGateway, "catalog down")

	stdout, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, provider.url, extra, nil,
		"-batch", "-task", "work", "-model", "deepseek-v4.1-flash")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0 using the stale cache\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stderr, "catalog refresh failed") || !strings.Contains(stderr, "using cached catalog") {
		t.Fatalf("stderr missing the stale-cache warning:\n%s", stderr)
	}
	headers := catalog.Headers()
	if len(headers) != seeded+1 {
		t.Fatalf("catalog requests = %d, want %d", len(headers), seeded+1)
	}
	if got := headers[len(headers)-1].Get("If-None-Match"); got != `"test-catalog-v1"` {
		t.Fatalf("stale load If-None-Match = %q, want the cached ETag", got)
	}
	if files := sessionFiles(t, stateDir); len(files) != 1 {
		t.Fatalf("session files = %v, want the fallback run", files)
	}
}

func TestBinaryRefreshCacheWriteFailureExitsTwo(t *testing.T) {
	bin := buildHarness(t)
	catalog := startFakeModelsDev(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file where the cache directory must be makes the atomic cache write
	// fail after a successful fetch.
	if err := os.WriteFile(filepath.Join(stateDir, "cache"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "",
		[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, nil, "-refresh-models")
	if exit != exitConfig {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", exit, exitConfig, stderr)
	}
	if !strings.Contains(stderr, "cache") {
		t.Fatalf("stderr does not name the cache failure:\n%s", stderr)
	}
	assertNoSessionFiles(t, stateDir)
}

func TestBinaryExplicitOutputRejectedByProvider(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		writeJSON(w, 400, map[string]any{"error": map[string]any{
			"message": "max_tokens 1234 is too large for this model",
		}})
	})
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarness(t, bin, t.TempDir(), stateDir, f.url,
		"-task", "finish", "-model", "deepseek-v4.1-flash", "-max-output-tokens", "1234")
	if run.exit != exitProvider {
		t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", run.exit, exitProvider, run.stdout, run.stderr)
	}
	if bodies := providerBodies(f); len(bodies) != 1 {
		t.Fatalf("provider calls = %d, want no retry with a different budget", len(bodies))
	} else {
		var budget int
		if !decodeJSONField(t, bodies[0], "max_tokens", &budget) || budget != 1234 {
			t.Fatalf("request budget = %d, want the requested 1234", budget)
		}
	}
	if !strings.Contains(run.stderr, "max_tokens 1234 is too large") {
		t.Fatalf("stderr does not report the provider rejection:\n%s", run.stderr)
	}
	for _, want := range []string{`"status":"provider_error"`, `"class":"http"`} {
		if !strings.Contains(run.log, want) {
			t.Errorf("log missing %q:\n%s", want, run.log)
		}
	}
	if strings.Contains(run.log, `"policy":"provider-overflow"`) {
		t.Fatal("output-budget rejection used the context-overflow retry")
	}
}

// seedLegacyBudgetSession writes a pre-discovery session log whose recorded
// output budget is the old 8,192 default and whose base URL is the recorded
// loopback provider.
func seedLegacyBudgetSession(t *testing.T, stateDir, workspace, baseURL string) string {
	t.Helper()
	id := "0190a0c4-0000-7000-8000-0000000000ee"
	payload, err := jsonv2.Marshal(map[string]any{
		"prompt_version":  agent.PromptVersion,
		"toolset_version": tools.Version,
		"workspace":       workspace,
		"state_dir":       stateDir,
		"provider":        providerName,
		"dialect":         "openai-chat",
		"model":           "deepseek-v4.1-flash",
		"base_url":        baseURL,
		"limits": map[string]any{
			"max_context_tokens": 200000,
			"max_output_tokens":  8192,
			"safety_margin":      10000,
			"max_input_tokens":   181808,
			"compaction_target":  181808,
		},
		"max_turns":          30,
		"max_total_tokens":   2000000,
		"session_timeout_ms": 3600000,
		"model_timeout_ms":   300000,
		"harness_version":    "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := session.Event{
		V:       session.Version,
		Seq:     1,
		Session: id,
		TS:      time.Now().UTC(),
		Type:    session.TypeSessionStart,
		Data:    payload,
	}
	line, err := jsonv2.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	path := sessionPathFor(stateDir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBinaryResumeLegacyRecordedOutputBudget(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if call == 1 {
			writeJSON(w, 200, chatFinalResponse("inherited done"))
			return
		}
		writeJSON(w, 200, chatFinalResponse("cleared done"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	id := seedLegacyBudgetSession(t, stateDir, workspace, f.url)

	stdout, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, "", nil,
		"-batch", "-resume", id, "-task", "continue")
	if exit != 0 {
		t.Fatalf("resume exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	bodies := providerBodies(f)
	if len(bodies) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(bodies))
	}
	var inherited int
	if !decodeJSONField(t, bodies[0], "max_tokens", &inherited) || inherited != 8192 {
		t.Fatalf("legacy inherited max_tokens = %d, want 8192", inherited)
	}
	start, starts := recordedSession(t, sessionPathFor(stateDir, id))
	if start.Limits.MaxOutputTokens != 8192 {
		t.Fatalf("session_start output = %d", start.Limits.MaxOutputTokens)
	}
	if len(starts) != 1 || starts[0].Limits.MaxOutputTokens != 8192 || starts[0].Limits.MaxContextTokens != 200000 {
		t.Fatalf("legacy run_start = %+v", starts)
	}

	stdout, stderr, exit = runHarnessRaw(t, bin, workspace, stateDir, "", nil,
		"-batch", "-resume", id, "-task", "clear", "-max-output-tokens=0")
	if exit != 0 {
		t.Fatalf("clearing resume exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	bodies = providerBodies(f)
	if len(bodies) != 2 || hasJSONField(t, bodies[1], "max_tokens") {
		t.Fatalf("explicit zero did not clear the legacy budget: %s", bodies[len(bodies)-1])
	}
	_, starts = recordedSession(t, sessionPathFor(stateDir, id))
	if len(starts) != 2 || starts[1].Limits.MaxOutputTokens != 0 {
		t.Fatalf("cleared run_start = %+v", starts)
	}
}
