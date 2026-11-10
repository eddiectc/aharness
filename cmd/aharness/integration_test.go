//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// buildHarness builds the real binary once for a test. Integration tests run
// the binary, not runArgs, so exit codes, signals, and file modes are real.
func buildHarness(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	bin := filepath.Join(t.TempDir(), "aharness")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-o", bin, "./cmd/aharness")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build harness: %v\n%s", err, out)
	}
	return bin
}

type harnessRun struct {
	stdout string
	stderr string
	exit   int
	log    string
	id     string
}

func runHarness(t *testing.T, bin, workspace, stateDir, baseURL string, args ...string) harnessRun {
	t.Helper()
	return runHarnessEnv(t, bin, workspace, stateDir, baseURL, []string{"AHARNESS_PROVIDER_TOKEN=test-token"}, args...)
}

func runHarnessEnv(t *testing.T, bin, workspace, stateDir, baseURL string, extraEnv []string, args ...string) harnessRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workspace
	env := make([]string, 0, len(os.Environ())+3+len(extraEnv))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "AHARNESS_") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"AHARNESS_PROVIDER_BASE_URL="+baseURL,
		"AHARNESS_STATE_DIR="+stateDir,
	)
	env = append(env, extraEnv...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run harness: %v\nstderr:\n%s", err, stderr.String())
		}
		exit = ee.ExitCode()
	}
	files, err := filepath.Glob(filepath.Join(stateDir, "sessions", "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("session logs: %v, %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
	return harnessRun{stdout: stdout.String(), stderr: stderr.String(), exit: exit, log: string(data), id: id}
}

type fakeProvider struct {
	mu       sync.Mutex
	calls    int
	headers  []http.Header
	bodies   [][]byte
	url      string
	scenario func(w http.ResponseWriter, r *http.Request, call int, body []byte)
}

func newFakeProvider(t *testing.T, scenario func(w http.ResponseWriter, r *http.Request, call int, body []byte)) *fakeProvider {
	t.Helper()
	f := &fakeProvider{scenario: scenario}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		f.mu.Lock()
		f.calls++
		call := f.calls
		f.headers = append(f.headers, r.Header.Clone())
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		f.scenario(w, r, call, body)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func chatToolCallResponse() map[string]any {
	return map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "read_file", "arguments": `{"path":"hello.txt"}`},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
	}
}

func chatFinalResponse(text string) map[string]any {
	return map[string]any{
		"id": "chatcmpl-2", "object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 4},
	}
}

func responsesToolCallResponse() map[string]any {
	return map[string]any{
		"id": "resp-1", "object": "response", "status": "completed",
		"output": []any{map[string]any{
			"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_file",
			"arguments": `{"path":"hello.txt"}`,
		}},
		"usage": map[string]any{"input_tokens": 11, "output_tokens": 7},
	}
}

func responsesFinalResponse(text string) map[string]any {
	return map[string]any{
		"id": "resp-2", "object": "response", "status": "completed",
		"output": []any{map[string]any{
			"type": "message", "role": "assistant", "id": "msg_1",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		}},
		"usage": map[string]any{"input_tokens": 20, "output_tokens": 4},
	}
}

func TestBinaryRunsBothDialects(t *testing.T) {
	bin := buildHarness(t)
	for _, tc := range []struct {
		model string
		path  string
		first map[string]any
		final map[string]any
	}{
		{model: "deepseek-v4.1-flash", path: "/chat/completions", first: chatToolCallResponse(), final: chatFinalResponse("chat done")},
		{model: "crouton-alpha-free", path: "/responses", first: responsesToolCallResponse(), final: responsesFinalResponse("responses done")},
		{model: "muse-spark-1.3-contributor", path: "/responses", first: responsesToolCallResponse(), final: responsesFinalResponse("muse done")},
	} {
		t.Run(tc.model, func(t *testing.T) {
			f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
				}
				if call == 1 {
					writeJSON(w, 200, tc.first)
					return
				}
				if !bytes.Contains(body, []byte("hello from file")) {
					t.Errorf("second request missing tool result: %s", body)
				}
				writeJSON(w, 200, tc.final)
			})
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello from file\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			stateDir := filepath.Join(t.TempDir(), "state")
			run := runHarness(t, bin, workspace, stateDir, f.url,
				"-task", "read hello.txt and finish",
				"-model", tc.model,
				"-max-turns", "5",
				"-max-output-tokens", "1000",
				"-max-context-tokens", "200000",
			)
			if run.exit != 0 {
				t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
			}
			if !strings.Contains(run.stdout, "done") {
				t.Errorf("stdout = %q, want final assistant text", run.stdout)
			}
			for _, want := range []string{"session_start", "model_response", "tool_result", "final", "session_end", "call_1", "hello from file"} {
				if !strings.Contains(run.log, want) {
					t.Errorf("log missing %q:\n%s", want, run.log)
				}
			}
			if _, err := os.Stat(filepath.Join(stateDir, "work", run.id)); !os.IsNotExist(err) {
				t.Errorf("work dir not removed: %v", err)
			}
			for i, h := range f.headers {
				if got := h.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("request %d Authorization = %q", i+1, got)
				}
				if got := h.Get("x-opencode-session"); got != run.id {
					t.Errorf("request %d x-opencode-session = %q, want %q", i+1, got, run.id)
				}
				if got := h.Get("User-Agent"); !strings.HasPrefix(got, "aharness/") {
					t.Errorf("request %d User-Agent = %q", i+1, got)
				}
			}
			info, err := os.Stat(filepath.Join(stateDir, "sessions", run.id+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Errorf("session file mode = %o, want 600", got)
			}
			stateInfo, err := os.Stat(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if got := stateInfo.Mode().Perm(); got != 0o700 {
				t.Errorf("state dir mode = %o, want 700", got)
			}
		})
	}
}

func rawResponse(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func TestBinaryProviderFailures(t *testing.T) {
	bin := buildHarness(t)
	tests := []struct {
		name      string
		scenario  func(w http.ResponseWriter, r *http.Request, call int, body []byte)
		wantExit  int
		wantCalls int
		wantLog   []string
	}{
		{
			name: "401 fast fail",
			scenario: func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				writeJSON(w, 401, map[string]any{"error": map[string]any{"message": "missing api key"}})
			},
			wantExit: 3, wantCalls: 1,
			wantLog: []string{"error", "provider_error"},
		},
		{
			name: "429 then success",
			scenario: func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				if call == 1 {
					w.Header().Set("Retry-After", "0")
					writeJSON(w, 429, map[string]any{"error": map[string]any{"message": "slow down"}})
					return
				}
				writeJSON(w, 200, chatFinalResponse("recovered"))
			},
			wantExit: 0, wantCalls: 2,
			wantLog: []string{"model_retry", "model_response", "completed"},
		},
		{
			name: "duplicate provider JSON",
			scenario: func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				rawResponse(w, 200, `{"choices":[],"choices":[]}`)
			},
			wantExit: 4, wantCalls: 1,
			wantLog: []string{"error", "protocol_error"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeProvider(t, tc.scenario)
			workspace := t.TempDir()
			stateDir := filepath.Join(t.TempDir(), "state")
			run := runHarness(t, bin, workspace, stateDir, f.url,
				"-task", "finish",
				"-model", "deepseek-v4.1-flash",
				"-max-turns", "2",
				"-max-output-tokens", "1000",
				"-max-context-tokens", "200000",
			)
			if run.exit != tc.wantExit {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", run.exit, tc.wantExit, run.stdout, run.stderr)
			}
			f.mu.Lock()
			calls := f.calls
			f.mu.Unlock()
			if calls != tc.wantCalls {
				t.Errorf("provider calls = %d, want %d", calls, tc.wantCalls)
			}
			for _, want := range tc.wantLog {
				if !strings.Contains(run.log, want) {
					t.Errorf("log missing %q:\n%s", want, run.log)
				}
			}
			if _, err := os.Stat(filepath.Join(stateDir, "work", run.id)); !os.IsNotExist(err) {
				t.Errorf("work dir not removed on error path: %v", err)
			}
			if strings.Contains(run.log, "test-token") {
				t.Errorf("provider token leaked into session log")
			}
		})
	}
}

func TestBinaryMissingTokenHint(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		writeJSON(w, 401, map[string]any{"error": map[string]any{"message": "Go requires a credential"}})
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarnessEnv(t, bin, workspace, stateDir, f.url, nil,
		"-task", "finish",
		"-model", "deepseek-v4.1-flash",
		"-max-turns", "2",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	if run.exit != 3 {
		t.Fatalf("exit = %d, want 3\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stderr, "AHARNESS_PROVIDER_TOKEN") {
		t.Errorf("stderr missing token hint:\n%s", run.stderr)
	}
	f.mu.Lock()
	headers := append([]http.Header(nil), f.headers...)
	f.mu.Unlock()
	if len(headers) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(headers))
	}
	if got := headers[0].Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want omitted without a token", got)
	}
}

func TestSignalKillsChildAndCleansWorkDir(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, map[string]any{
				"id": "chatcmpl-sig", "object": "chat.completion",
				"choices": []any{map[string]any{
					"index": 0,
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []any{map[string]any{
							"id": "call_sig", "type": "function",
							"function": map[string]any{
								"name":      "run_command",
								"arguments": `{"command":"echo $$ > child.pid; sleep 60","timeout_ms":60000}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
			})
			return
		}
		writeJSON(w, 200, chatFinalResponse("should not finish"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"-task", "run a long command",
		"-model", "deepseek-v4.1-flash",
		"-max-turns", "3",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(),
		"AHARNESS_PROVIDER_BASE_URL="+f.url,
		"AHARNESS_PROVIDER_TOKEN=test-token",
		"AHARNESS_STATE_DIR="+stateDir,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(workspace, "child.pid")
	deadline := time.Now().Add(10 * time.Second)
	var pidBytes []byte
	for time.Now().Before(deadline) {
		pidBytes, _ = os.ReadFile(pidPath)
		if len(bytes.TrimSpace(pidBytes)) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(bytes.TrimSpace(pidBytes)) == 0 {
		_ = cmd.Process.Kill()
		t.Fatalf("child pid file not written\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &pid); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("parse child pid %q: %v", pidBytes, err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	err := cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 130 {
		t.Fatalf("wait = %v, want exit 130\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	// The process group must be gone, not merely detached.
	killDeadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(killDeadline) {
			t.Fatalf("child pid %d still alive: %v", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	files, err := filepath.Glob(filepath.Join(stateDir, "sessions", "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("session logs: %v, %v", files, err)
	}
	logData, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	log := string(logData)
	for _, want := range []string{"interrupted", "final", "session_end", "tool_result", "canceled"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	id := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
	if _, err := os.Stat(filepath.Join(stateDir, "work", id)); !os.IsNotExist(err) {
		t.Errorf("work dir not removed after signal: %v", err)
	}
}

func TestBinaryCompactsLargeToolHistory(t *testing.T) {
	bin := buildHarness(t)
	const toolTurns = 20
	cmdJSON := `{"command":"head -c 2000 /dev/zero | tr '\\0' x","timeout_ms":30000}`
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call <= toolTurns {
			writeJSON(w, 200, map[string]any{
				"id": fmt.Sprintf("chatcmpl-%d", call), "object": "chat.completion",
				"choices": []any{map[string]any{
					"index": 0,
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []any{map[string]any{
							"id": fmt.Sprintf("call_%d", call), "type": "function",
							"function": map[string]any{"name": "run_command", "arguments": cmdJSON},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
			})
			return
		}
		writeJSON(w, 200, chatFinalResponse("compacted and done"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarness(t, bin, workspace, stateDir, f.url,
		"-task", "generate output",
		"-model", "deepseek-v4.1-flash",
		"-max-turns", "25",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "12000",
	)
	if run.exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
	}
	compacted := false
	for line := range strings.SplitSeq(strings.TrimSpace(run.log), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Data struct {
				ElidedResults int `json:"elided_results"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode log line: %v\n%s", err, line)
		}
		if ev.Type == "compaction" && ev.Data.ElidedResults > 0 {
			compacted = true
		}
	}
	if !compacted {
		t.Errorf("log missing compaction event with elided results:\n%s", run.log)
	}
	if got := strings.Count(run.log, `"type":"tool_result"`); got != toolTurns {
		t.Errorf("tool results = %d, want %d", got, toolTurns)
	}
}
