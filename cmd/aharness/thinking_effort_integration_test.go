//go:build unix

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func startEffortCatalog(t *testing.T) *fakeModelsDev {
	t.Helper()
	catalog := startFakeModelsDev(t)
	catalog.SetModels(thinkingEffortCatalogModels())
	return catalog
}

func assertRequestEffort(t *testing.T, body []byte, dialect, want string) {
	t.Helper()
	switch dialect {
	case "openai-chat":
		if want == "" {
			if hasJSONField(t, body, "reasoning_effort") {
				t.Fatalf("chat request unexpectedly carries reasoning_effort: %s", body)
			}
			return
		}
		var got string
		if !decodeJSONField(t, body, "reasoning_effort", &got) {
			t.Fatalf("chat request omits reasoning_effort: %s", body)
		}
		if got != want {
			t.Errorf("reasoning_effort = %q, want %q", got, want)
		}
		if hasJSONField(t, body, "reasoning") {
			t.Errorf("chat request carries the Responses reasoning object: %s", body)
		}
	case "openai-responses":
		if want == "" {
			if hasJSONField(t, body, "reasoning") || hasJSONField(t, body, "reasoning_effort") {
				t.Fatalf("responses request unexpectedly carries reasoning: %s", body)
			}
			return
		}
		var got struct {
			Effort string `json:"effort"`
		}
		if !decodeJSONField(t, body, "reasoning", &got) {
			t.Fatalf("responses request omits reasoning: %s", body)
		}
		if got.Effort != want {
			t.Errorf("reasoning.effort = %q, want %q", got.Effort, want)
		}
		if hasJSONField(t, body, "reasoning_effort") {
			t.Errorf("responses request carries the top-level reasoning_effort: %s", body)
		}
	default:
		t.Fatalf("unknown dialect %q", dialect)
	}
}

func TestBinaryNewSessionThinkingEffort(t *testing.T) {
	bin := buildHarness(t)
	catalog := startEffortCatalog(t)
	tests := []struct {
		name     string
		model    string
		dialect  string
		path     string
		effort   string
		response map[string]any
	}{
		{
			name: "chat supported", model: "deepseek-v4.1-flash", dialect: "openai-chat",
			path: "/chat/completions", effort: "high", response: chatFinalResponse("chat done"),
		},
		{
			name: "responses supported", model: "gpt-6-luna", dialect: "openai-responses",
			path: "/responses", effort: "high", response: responsesFinalResponse("responses done"),
		},
		{
			name: "chat omitted", model: "deepseek-v4.1-flash", dialect: "openai-chat",
			path: "/chat/completions", response: chatFinalResponse("chat default"),
		},
		{
			name: "responses omitted", model: "gpt-6-luna", dialect: "openai-responses",
			path: "/responses", response: responsesFinalResponse("responses default"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response := tc.response
			f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
				}
				writeJSON(w, 200, response)
			})
			workspace := t.TempDir()
			stateDir := filepath.Join(t.TempDir(), "state")
			args := []string{
				"-batch", "-task", "work", "-model", tc.model,
			}
			if tc.effort != "" {
				args = append(args, "-thinking-effort", tc.effort)
			}
			run := runHarnessEnv(t, bin, workspace, stateDir, f.url,
				[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, args...)
			if run.exit != 0 {
				t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
			}
			bodies := providerBodies(f)
			if len(bodies) != 1 {
				t.Fatalf("provider calls = %d, want 1", len(bodies))
			}
			assertRequestEffort(t, bodies[0], tc.dialect, tc.effort)
			start, starts := recordedSession(t, sessionPathFor(stateDir, run.id))
			if start.ThinkingEffort != tc.effort {
				t.Errorf("session_start thinking_effort = %q, want %q", start.ThinkingEffort, tc.effort)
			}
			if len(starts) != 1 {
				t.Fatalf("run_start events = %d, want 1", len(starts))
			}
			if starts[0].ThinkingEffort != tc.effort {
				t.Errorf("run_start thinking_effort = %q, want %q", starts[0].ThinkingEffort, tc.effort)
			}
		})
	}
}

func TestBinaryThinkingEffortValidationExitsTwo(t *testing.T) {
	bin := buildHarness(t)
	models := thinkingEffortCatalogModels()
	models["plain-text"] = testCatalogModel("Plain Text", "", 100_000, 0, 8_192)
	catalog := startFakeModelsDev(t)
	catalog.SetModels(models)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "unsupported value lists supported efforts",
			args: []string{"-model", "deepseek-v4.1-flash", "-thinking-effort", "medium"},
			want: "supported: low, high, max",
		},
		{
			name: "model advertises no levels",
			args: []string{"-model", "kimi-k2.7-code", "-thinking-effort", "high"},
			want: "advertises no effort levels",
		},
		{
			name: "model does not support reasoning",
			args: []string{"-model", "plain-text", "-thinking-effort", "high"},
			want: "does not support reasoning",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				t.Errorf("provider called for an invalid effort: %s", body)
			})
			stateDir := filepath.Join(t.TempDir(), "state")
			args := append([]string{"-batch", "-task", "work", "-workspace", t.TempDir()}, tc.args...)
			stdout, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, f.url,
				[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, nil, args...)
			if exit != exitConfig {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", exit, exitConfig, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr = %q, want substring %q", stderr, tc.want)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if calls := len(providerBodies(f)); calls != 0 {
				t.Errorf("provider calls = %d, want 0", calls)
			}
			assertNoSessionFiles(t, stateDir)
		})
	}
}

func TestBinaryResumeThinkingEffortInheritanceOffline(t *testing.T) {
	bin := buildHarness(t)
	catalog := startEffortCatalog(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		switch call {
		case 1:
			writeJSON(w, 200, chatFinalResponse("first done"))
		case 2:
			writeJSON(w, 200, chatFinalResponse("inherited done"))
		default:
			writeJSON(w, 200, chatFinalResponse("cleared done"))
		}
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	first := runHarnessEnv(t, bin, workspace, stateDir, f.url,
		[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()},
		"-batch", "-task", "first", "-model", "deepseek-v4.1-flash", "-thinking-effort", "high")
	if first.exit != 0 {
		t.Fatalf("first exit = %d\nstderr:\n%s", first.exit, first.stderr)
	}
	start, starts := recordedSession(t, sessionPathFor(stateDir, first.id))
	if start.ThinkingEffort != "high" || len(starts) != 1 || starts[0].ThinkingEffort != "high" {
		t.Fatalf("first run recorded effort start=%q starts=%+v", start.ThinkingEffort, starts)
	}
	assertRequestEffort(t, providerBodies(f)[0], "openai-chat", "high")

	// Remove the cache and break the catalog: inheritance must stay offline.
	if err := os.RemoveAll(filepath.Join(stateDir, "cache")); err != nil {
		t.Fatal(err)
	}
	catalog.SetFailure(http.StatusServiceUnavailable, "catalog offline")
	before := catalog.Calls()
	stdout, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, "",
		[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, nil,
		"-batch", "-resume", first.id, "-task", "continue")
	if exit != 0 {
		t.Fatalf("inherited resume exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "inherited done") {
		t.Fatalf("stdout = %q", stdout)
	}
	if got := catalog.Calls(); got != before {
		t.Fatalf("inherited resume made %d catalog requests, want none", got-before)
	}
	bodies := providerBodies(f)
	if len(bodies) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(bodies))
	}
	assertRequestEffort(t, bodies[1], "openai-chat", "high")
	_, starts = recordedSession(t, sessionPathFor(stateDir, first.id))
	if len(starts) != 2 || starts[1].ThinkingEffort != "high" {
		t.Fatalf("inherited run_start = %+v", starts)
	}

	before = catalog.Calls()
	stdout, stderr, exit = runHarnessRawEnv(t, bin, workspace, stateDir, "",
		[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, nil,
		"-batch", "-resume", first.id, "-task", "clear", "-thinking-effort", "default")
	if exit != 0 {
		t.Fatalf("clearing resume exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "cleared done") {
		t.Fatalf("stdout = %q", stdout)
	}
	if got := catalog.Calls(); got != before {
		t.Fatalf("explicit default resume made %d catalog requests, want none", got-before)
	}
	bodies = providerBodies(f)
	if len(bodies) != 3 {
		t.Fatalf("provider calls = %d, want 3", len(bodies))
	}
	assertRequestEffort(t, bodies[2], "openai-chat", "")
	_, starts = recordedSession(t, sessionPathFor(stateDir, first.id))
	if len(starts) != 3 || starts[2].ThinkingEffort != "" {
		t.Fatalf("cleared run_start = %+v", starts)
	}
}

func TestBinaryResumeThinkingEffortExplicitRequiresCatalog(t *testing.T) {
	bin := buildHarness(t)
	catalog := startEffortCatalog(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatFinalResponse("first done"))
			return
		}
		writeJSON(w, 200, chatFinalResponse("explicit done"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	first := runHarnessEnv(t, bin, workspace, stateDir, f.url,
		[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()},
		"-batch", "-task", "first", "-model", "deepseek-v4.1-flash")
	if first.exit != 0 {
		t.Fatalf("first exit = %d\nstderr:\n%s", first.exit, first.stderr)
	}
	assertRequestEffort(t, providerBodies(f)[0], "openai-chat", "")

	// An explicit value revalidates through the catalog even when the model is
	// unchanged; a cold cache therefore needs the source.
	if err := os.RemoveAll(filepath.Join(stateDir, "cache")); err != nil {
		t.Fatal(err)
	}
	before := catalog.Calls()
	stdout, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, "",
		[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, nil,
		"-batch", "-resume", first.id, "-task", "continue", "-thinking-effort", "max")
	if exit != 0 {
		t.Fatalf("explicit resume exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "explicit done") {
		t.Fatalf("stdout = %q", stdout)
	}
	if got := catalog.Calls(); got == before {
		t.Fatal("explicit effort did not load the catalog")
	}
	bodies := providerBodies(f)
	if len(bodies) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(bodies))
	}
	assertRequestEffort(t, bodies[1], "openai-chat", "max")
	_, starts := recordedSession(t, sessionPathFor(stateDir, first.id))
	if len(starts) != 2 || starts[1].ThinkingEffort != "max" {
		t.Fatalf("explicit run_start = %+v", starts)
	}

	// Without a cache or a reachable catalog the explicit value cannot be
	// validated, so the run fails before any provider call.
	if err := os.RemoveAll(filepath.Join(stateDir, "cache")); err != nil {
		t.Fatal(err)
	}
	catalog.SetFailure(http.StatusServiceUnavailable, "catalog offline")
	before = catalog.Calls()
	_, stderr, exit = runHarnessRawEnv(t, bin, workspace, stateDir, "",
		[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, nil,
		"-batch", "-resume", first.id, "-task", "continue", "-thinking-effort", "max")
	if exit != exitProvider {
		t.Fatalf("offline explicit resume exit = %d, want %d\nstderr:\n%s", exit, exitProvider, stderr)
	}
	if got := catalog.Calls(); got != before+1 {
		t.Fatalf("catalog requests = %d, want one failed load", got-before)
	}
	if got := len(providerBodies(f)); got != 2 {
		t.Fatalf("provider calls = %d, want no call after catalog failure", got)
	}
	_, starts = recordedSession(t, sessionPathFor(stateDir, first.id))
	if len(starts) != 2 {
		t.Fatalf("failed resume appended a run_start: %+v", starts)
	}
}

func TestBinaryResumeThinkingEffortModelChange(t *testing.T) {
	bin := buildHarness(t)
	tests := []struct {
		name       string
		first      string
		args       []string
		wantEffort string
		wantExit   int
		wantStderr string
		wantWarn   string
	}{
		{
			name: "supported inherited value survives", first: "high",
			args:       []string{"-model", "gpt-6-luna"},
			wantEffort: "high", wantExit: 0,
		},
		{
			name: "unsupported inherited value is cleared", first: "max",
			args:       []string{"-model", "gpt-6-luna"},
			wantEffort: "", wantExit: 0,
			wantWarn: `thinking effort "max" cleared`,
		},
		{
			name: "explicit value conflicts with the changed model", first: "high",
			args:       []string{"-model", "gpt-6-luna", "-thinking-effort", "max"},
			wantExit:   exitConfig,
			wantStderr: "supported: none, low, medium, high",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			catalog := startEffortCatalog(t)
			f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				if r.URL.Path == "/responses" {
					writeJSON(w, 200, responsesFinalResponse("changed done"))
					return
				}
				writeJSON(w, 200, chatFinalResponse("first done"))
			})
			workspace := t.TempDir()
			stateDir := filepath.Join(t.TempDir(), "state")
			first := runHarnessEnv(t, bin, workspace, stateDir, f.url,
				[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()},
				"-batch", "-task", "first", "-model", "deepseek-v4.1-flash", "-thinking-effort", tc.first)
			if first.exit != 0 {
				t.Fatalf("first exit = %d\nstderr:\n%s", first.exit, first.stderr)
			}
			args := append([]string{"-batch", "-resume", first.id, "-task", "continue"}, tc.args...)
			stdout, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, f.url,
				[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, nil, args...)
			if exit != tc.wantExit {
				t.Fatalf("resume exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", exit, tc.wantExit, stdout, stderr)
			}
			bodies := providerBodies(f)
			if tc.wantExit != 0 {
				if !strings.Contains(stderr, tc.wantStderr) {
					t.Fatalf("stderr = %q, want substring %q", stderr, tc.wantStderr)
				}
				if len(bodies) != 1 {
					t.Fatalf("provider calls = %d, want no call for the conflict", len(bodies))
				}
				_, starts := recordedSession(t, sessionPathFor(stateDir, first.id))
				if len(starts) != 1 {
					t.Fatalf("conflict appended a run_start: %+v", starts)
				}
				return
			}
			if !strings.Contains(stdout, "changed done") {
				t.Fatalf("stdout = %q", stdout)
			}
			if tc.wantWarn != "" {
				if !strings.Contains(stderr, tc.wantWarn) {
					t.Fatalf("stderr = %q, want warning %q", stderr, tc.wantWarn)
				}
			} else if strings.Contains(stderr, "cleared") {
				t.Fatalf("unexpected clearing warning:\n%s", stderr)
			}
			if len(bodies) != 2 {
				t.Fatalf("provider calls = %d, want 2", len(bodies))
			}
			assertRequestEffort(t, bodies[1], "openai-responses", tc.wantEffort)
			_, starts := recordedSession(t, sessionPathFor(stateDir, first.id))
			if len(starts) != 2 || starts[1].ThinkingEffort != tc.wantEffort {
				t.Fatalf("changed run_start = %+v, want effort %q", starts, tc.wantEffort)
			}
		})
	}
}

func TestBinaryForkThinkingEffortSessionStart(t *testing.T) {
	bin := buildHarness(t)
	catalog := startEffortCatalog(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		writeJSON(w, 200, chatFinalResponse("done"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	parent := runHarnessEnv(t, bin, workspace, stateDir, f.url,
		[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()},
		"-batch", "-task", "parent", "-model", "deepseek-v4.1-flash", "-thinking-effort", "high")
	if parent.exit != 0 {
		t.Fatalf("parent exit = %d\nstderr:\n%s", parent.exit, parent.stderr)
	}
	parentPath := sessionPathFor(stateDir, parent.id)
	known := map[string]bool{parentPath: true}

	// Both forks inherit or clear without consulting the catalog.
	if err := os.RemoveAll(filepath.Join(stateDir, "cache")); err != nil {
		t.Fatal(err)
	}
	catalog.SetFailure(http.StatusServiceUnavailable, "catalog offline")
	before := catalog.Calls()

	fork := func(args ...string) string {
		t.Helper()
		stdout, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, "",
			[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}, nil, args...)
		if exit != 0 {
			t.Fatalf("fork exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
		}
		var child string
		for _, path := range sessionFiles(t, stateDir) {
			if !known[path] {
				child = path
				known[path] = true
			}
		}
		if child == "" {
			t.Fatalf("fork created no session: %v", sessionFiles(t, stateDir))
		}
		return child
	}

	inherited := fork("-batch", "-fork", parent.id, "-task", "inherit")
	start, starts := recordedSession(t, inherited)
	if start.ThinkingEffort != "high" {
		t.Fatalf("inherited fork session_start effort = %q", start.ThinkingEffort)
	}
	if len(starts) == 0 || starts[len(starts)-1].ThinkingEffort != "high" {
		t.Fatalf("inherited fork run_starts = %+v", starts)
	}

	cleared := fork("-batch", "-fork", parent.id, "-task", "clear", "-thinking-effort", "default")
	start, starts = recordedSession(t, cleared)
	if start.ThinkingEffort != "" {
		t.Fatalf("cleared fork session_start effort = %q", start.ThinkingEffort)
	}
	if len(starts) == 0 || starts[len(starts)-1].ThinkingEffort != "" {
		t.Fatalf("cleared fork run_starts = %+v", starts)
	}

	if got := catalog.Calls(); got != before {
		t.Fatalf("forks made %d catalog requests, want none", got-before)
	}
	bodies := providerBodies(f)
	if len(bodies) != 3 {
		t.Fatalf("provider calls = %d, want parent plus two forks", len(bodies))
	}
	assertRequestEffort(t, bodies[1], "openai-chat", "high")
	assertRequestEffort(t, bodies[2], "openai-chat", "")
}

func TestBinaryListModelsEffortColumn(t *testing.T) {
	bin := buildHarness(t)
	catalog := startEffortCatalog(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	stdout, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, "",
		[]string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL(), "AHARNESS_PROVIDER_TOKEN="}, nil,
		"-list-models")
	if exit != 0 {
		t.Fatalf("exit = %d\nstderr:\n%s", exit, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 8 {
		t.Fatalf("list rows = %d, want header plus 7 usable models:\n%s", len(lines)-1, stdout)
	}
	if want := "ID\tNAME\tDIALECT\tCONTEXT\tOUTPUT\tSTATUS\tEFFORT"; lines[0] != want {
		t.Fatalf("header = %q, want %q", lines[0], want)
	}
	want := map[string]string{
		"deepseek-v4.1-flash":        "low,high,max",
		"gpt-6-luna":                 "none,low,medium,high",
		"crouton-alpha-free":         "low,high",
		"muse-spark-1.3-contributor": "minimal,high,xhigh",
		"kimi-k2.7-code":             "-",
		"qwen3.7-plus":               "-",
	}
	seen := 0
	for _, line := range lines[1:] {
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			t.Fatalf("row has %d columns, want 7: %q", len(fields), line)
		}
		id := fields[0]
		if effort, ok := want[id]; ok {
			if fields[6] != effort {
				t.Errorf("%s effort = %q, want %q", id, fields[6], effort)
			}
			seen++
		}
	}
	if seen != len(want) {
		t.Fatalf("matched %d models, want %d:\n%s", seen, len(want), stdout)
	}
	assertNoSessionFiles(t, stateDir)
}
