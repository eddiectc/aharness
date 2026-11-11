//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"aharness/internal/session"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func waitUntil(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func integrationEnv(stateDir, baseURL string) []string {
	return integrationEnvWith(stateDir, baseURL, nil)
}

func integrationEnvWith(stateDir, baseURL string, extra []string) []string {
	env := make([]string, 0, len(os.Environ())+4+len(extra))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "AHARNESS_") {
			continue
		}
		env = append(env, entry)
	}
	if !envHasKey(extra, "AHARNESS_STATE_DIR") {
		env = append(env, "AHARNESS_STATE_DIR="+stateDir)
	}
	if !envHasKey(extra, "AHARNESS_PROVIDER_TOKEN") {
		env = append(env, "AHARNESS_PROVIDER_TOKEN=test-token")
	}
	if baseURL != "" && !envHasKey(extra, "AHARNESS_PROVIDER_BASE_URL") {
		env = append(env, "AHARNESS_PROVIDER_BASE_URL="+baseURL)
	}
	if !envHasKey(extra, "AHARNESS_MODELS_DEV_URL") {
		env = append(env, testCatalogEnv())
	}
	env = append(env, extra...)
	return env
}

// runHarnessRaw runs the binary without assuming a single session log.
func runHarnessRaw(t *testing.T, bin, workspace, stateDir, baseURL string, stdin io.Reader, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	return runHarnessRawEnv(t, bin, workspace, stateDir, baseURL, nil, stdin, args...)
}

// runHarnessRawEnv is runHarnessRaw with additional environment entries.
func runHarnessRawEnv(t *testing.T, bin, workspace, stateDir, baseURL string, extra []string, stdin io.Reader, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workspace
	cmd.Env = integrationEnvWith(stateDir, baseURL, extra)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run harness: %v\nstderr:\n%s", err, errBuf.String())
		}
		exit = ee.ExitCode()
	}
	return out.String(), errBuf.String(), exit
}

func sessionPathFor(stateDir, id string) string {
	return filepath.Join(stateDir, "sessions", id+".jsonl")
}

func sessionFiles(t *testing.T, stateDir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(stateDir, "sessions", "*.jsonl"))
	if err != nil {
		t.Fatalf("glob session logs: %v", err)
	}
	sort.Strings(files)
	return files
}

func readSessionEvents(t *testing.T, path string) []session.Event {
	t.Helper()
	events, err := session.Read(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return events
}

func lastEventSeq(events []session.Event) uint64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq
}

func countEventType(events []session.Event, typ session.EventType) int {
	n := 0
	for _, ev := range events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func appendToSession(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open session for append: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		t.Fatalf("append session: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
}

func writeSSE(t *testing.T, w http.ResponseWriter, event, data string) {
	t.Helper()
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}

func TestBinaryStreamsBothDialects(t *testing.T) {
	bin := buildHarness(t)
	tests := []struct {
		name   string
		model  string
		path   string
		first  func(t *testing.T, w http.ResponseWriter)
		second func(t *testing.T, w http.ResponseWriter)
	}{
		{
			name:  "chat",
			model: "deepseek-v4.1-flash",
			path:  "/chat/completions",
			first: func(t *testing.T, w http.ResponseWriter) {
				writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"hel"}}]}`)
			},
			second: func(t *testing.T, w http.ResponseWriter) {
				writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"lo"}}]}`)
				writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
				writeSSE(t, w, "", `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
				writeSSE(t, w, "", `[DONE]`)
			},
		},
		{
			name:  "responses",
			model: "muse-spark-1.3-contributor",
			path:  "/responses",
			first: func(t *testing.T, w http.ResponseWriter) {
				writeSSE(t, w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"hel"}`)
			},
			second: func(t *testing.T, w http.ResponseWriter) {
				writeSSE(t, w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"lo"}`)
				writeSSE(t, w, "response.completed", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":2}}}`)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
				}
				if call != 1 {
					t.Errorf("provider calls = %d, want 1", call)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				tc.first(t, w)
				once.Do(func() { close(started) })
				<-release
				tc.second(t, w)
			})
			workspace := t.TempDir()
			stateDir := filepath.Join(t.TempDir(), "state")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin,
				"-stream",
				"-task", "say hello",
				"-model", tc.model,
				"-max-turns", "2",
				"-max-output-tokens", "1000",
				"-max-context-tokens", "200000",
			)
			cmd.Dir = workspace
			cmd.Env = integrationEnv(stateDir, f.url)
			stdoutPipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			var stdout syncBuffer
			copied := make(chan struct{})
			go func() {
				_, _ = io.Copy(&stdout, stdoutPipe)
				close(copied)
			}()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("provider never sent its first delta\nstderr:\n%s", stderr.String())
			}
			waitUntil(t, "the first streamed delta on stdout", func() bool {
				return strings.Contains(stdout.String(), "hel")
			})
			close(release)
			err = cmd.Wait()
			<-copied
			exit := 0
			if err != nil {
				var ee *exec.ExitError
				if !errors.As(err, &ee) {
					t.Fatalf("wait: %v", err)
				}
				exit = ee.ExitCode()
			}
			if exit != 0 {
				t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", exit, stdout.String(), stderr.String())
			}
			if got := strings.Count(stdout.String(), "hello"); got != 1 {
				t.Fatalf("stdout contains the response %d times, want once: %q", got, stdout.String())
			}
			files := sessionFiles(t, stateDir)
			if len(files) != 1 {
				t.Fatalf("session files = %v, want 1", files)
			}
			events := readSessionEvents(t, files[0])
			if got := countEventType(events, session.TypeModelResponse); got != 1 {
				t.Fatalf("model_response events = %d, want 1", got)
			}
			var streamed bool
			for _, ev := range events {
				if ev.Type == session.TypeModelResponse {
					var data struct {
						Streamed bool `json:"streamed"`
					}
					if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
						t.Fatalf("decode model_response: %v", err)
					}
					streamed = data.Streamed
				}
			}
			if !streamed {
				t.Fatal("model_response streamed = false, want true")
			}
			if strings.Contains(string(mustRead(t, files[0])), "delta") {
				t.Fatal("log contains delta events, want only canonical responses")
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBinaryResumeAfterLimit(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatToolCallResponse())
			return
		}
		writeJSON(w, 200, chatFinalResponse("resumed done"))
	})
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello from file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	first := runHarness(t, bin, workspace, stateDir, f.url,
		"-task", "hit the limit",
		"-model", "deepseek-v4.1-flash",
		"-max-turns", "1",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	if first.exit != 2 {
		t.Fatalf("first exit = %d, want 2\nstderr:\n%s", first.exit, first.stderr)
	}
	firstEvents := readSessionEvents(t, sessionPathFor(stateDir, first.id))
	firstSeq := lastEventSeq(firstEvents)

	second := runHarness(t, bin, workspace, stateDir, "",
		"-resume", first.id,
		"-max-turns", "5",
	)
	if second.exit != 0 {
		t.Fatalf("resume exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", second.exit, second.stdout, second.stderr)
	}
	if !strings.Contains(second.stdout, "resumed done") {
		t.Fatalf("stdout = %q, want resumed answer", second.stdout)
	}
	events := readSessionEvents(t, sessionPathFor(stateDir, first.id))
	if got := countEventType(events, session.TypeRunStart); got != 2 {
		t.Fatalf("run_start events = %d, want 2", got)
	}
	var resumedFrom uint64
	runStarts := 0
	for _, ev := range events {
		if ev.Type != session.TypeRunStart {
			continue
		}
		runStarts++
		if runStarts == 2 {
			var data struct {
				Resumed uint64 `json:"resumed_from_seq"`
			}
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			resumedFrom = data.Resumed
		}
	}
	if resumedFrom < firstSeq {
		t.Fatalf("resumed_from_seq = %d, want >= %d", resumedFrom, firstSeq)
	}
	finals := 0
	for _, ev := range events {
		if ev.Type == session.TypeFinal {
			finals++
		}
	}
	if finals != 2 {
		t.Fatalf("final events = %d, want 2 (one per run)", finals)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "work", first.id)); !os.IsNotExist(err) {
		t.Fatalf("work dir remains after resume: %v", err)
	}
	f.mu.Lock()
	headers := append([]http.Header(nil), f.headers...)
	f.mu.Unlock()
	if len(headers) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(headers))
	}
	if got := headers[1].Get("x-opencode-session"); got != first.id {
		t.Fatalf("resumed x-opencode-session = %q, want %q", got, first.id)
	}
}

func TestBinaryRepairsTornFinalLine(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatFinalResponse("first done"))
			return
		}
		writeJSON(w, 200, chatFinalResponse("after repair"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	first := runHarness(t, bin, workspace, stateDir, f.url,
		"-task", "first",
		"-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	if first.exit != 0 {
		t.Fatalf("first exit = %d", first.exit)
	}
	path := sessionPathFor(stateDir, first.id)
	firstBytes := mustRead(t, path)
	before := readSessionEvents(t, path)
	appendToSession(t, path, []byte(`{"v":2,"seq":999,"session":"`+first.id+`","type":"model_response","data":`))

	second := runHarness(t, bin, workspace, stateDir, "",
		"-resume", first.id,
		"-task", "continue",
	)
	if second.exit != 0 {
		t.Fatalf("resume exit = %d\nstdout:\n%s\nstderr:\n%s", second.exit, second.stdout, second.stderr)
	}
	events := readSessionEvents(t, path)
	repairs := 0
	for _, ev := range events {
		if ev.Type != session.TypeSessionRepair {
			continue
		}
		repairs++
		var data session.RepairData
		if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.DiscardedBytes <= 0 || data.LastGoodSeq != lastEventSeq(before) {
			t.Fatalf("repair = %+v", data)
		}
	}
	if repairs != 1 {
		t.Fatalf("session_repair events = %d, want 1", repairs)
	}
	if !bytes.HasPrefix(mustRead(t, path), firstBytes) {
		t.Fatal("repair rewrote the durable prefix")
	}
	if !strings.Contains(string(mustRead(t, path)), "after repair") {
		t.Fatal("resumed run did not complete")
	}
}

func TestBinaryForkIsSelfContained(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatFinalResponse("parent done"))
			return
		}
		writeJSON(w, 200, chatFinalResponse("fork done"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	parent := runHarness(t, bin, workspace, stateDir, f.url,
		"-task", "parent task",
		"-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	if parent.exit != 0 {
		t.Fatalf("parent exit = %d", parent.exit)
	}
	parentPath := sessionPathFor(stateDir, parent.id)
	parentHash := sha256.Sum256(mustRead(t, parentPath))
	parentEvents := readSessionEvents(t, parentPath)

	stdout, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, "", nil,
		"-fork", parent.id,
		"-task", "fork task",
	)
	if exit != 0 {
		t.Fatalf("fork exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "fork done") {
		t.Fatalf("stdout = %q", stdout)
	}
	if got := sha256.Sum256(mustRead(t, parentPath)); got != parentHash {
		t.Fatal("fork changed the parent log")
	}
	files := sessionFiles(t, stateDir)
	var childPath string
	for _, file := range files {
		if file != parentPath {
			childPath = file
		}
	}
	if childPath == "" {
		t.Fatalf("fork did not create a new session log: %v", files)
	}
	childID := strings.TrimSuffix(filepath.Base(childPath), ".jsonl")
	childEvents := readSessionEvents(t, childPath)
	if len(childEvents) == 0 || childEvents[0].Type != session.TypeSessionStart {
		t.Fatalf("child first event = %+v", childEvents)
	}
	var start struct {
		ParentSession string `json:"parent_session"`
		ParentSeq     uint64 `json:"parent_seq"`
	}
	if err := jsonv2.Unmarshal(childEvents[0].Data, &start); err != nil {
		t.Fatal(err)
	}
	if start.ParentSession != parent.id || start.ParentSeq != lastEventSeq(parentEvents) {
		t.Fatalf("parent metadata = %+v", start)
	}
	if got := countEventType(childEvents, session.TypeSessionStart); got != 1 {
		t.Fatalf("child session_start events = %d, want 1", got)
	}
	if childEvents[1].Type != parentEvents[1].Type {
		t.Fatalf("child event 1 = %q, want copied parent %q", childEvents[1].Type, parentEvents[1].Type)
	}
	if childEvents[1].Session != childID {
		t.Fatalf("copied event session = %q, want %q", childEvents[1].Session, childID)
	}
	if !childEvents[1].TS.Equal(parentEvents[1].TS) {
		t.Fatalf("copied event ts = %v, want %v", childEvents[1].TS, parentEvents[1].TS)
	}
	foundTask := false
	for _, ev := range childEvents {
		if ev.Type == session.TypeUserMessage {
			var data struct {
				Text string `json:"text"`
			}
			if err := jsonv2.Unmarshal(ev.Data, &data); err == nil && data.Text == "fork task" {
				foundTask = true
			}
		}
	}
	if !foundTask {
		t.Fatal("fork transcript does not contain the new task")
	}
}

func seedCrashSession(t *testing.T, stateDir, workspace, baseURL, id string) []session.Event {
	t.Helper()
	limits := `{"max_context_tokens":200000,"max_output_tokens":1000,"safety_margin":10000,"max_input_tokens":189000,"compaction_target":189000}`
	payloads := []struct {
		typ  session.EventType
		role string
		data string
	}{
		{
			typ: session.TypeSessionStart,
			data: `{"prompt_version":"prompt-v0","toolset_version":"toolset-v0","workspace":` + quoteJSON(t, workspace) +
				`,"state_dir":` + quoteJSON(t, stateDir) + `,"provider":"opencode-go","dialect":"openai-chat","model":"deepseek-v4.1-flash","limits":` + limits +
				`,"max_turns":5,"max_total_tokens":2000000,"session_timeout_ms":3600000,"model_timeout_ms":300000,"harness_version":"test"}`,
		},
		{
			typ: session.TypeRunStart,
			data: `{"run_id":"0190a0c4-0000-7000-8000-0000000000cc","resumed_from_seq":0,"model":"deepseek-v4.1-flash","dialect":"openai-chat","base_url":` +
				quoteJSON(t, baseURL) + `,"limits":` + limits +
				`,"max_turns":5,"max_total_tokens":2000000,"session_timeout_ms":3600000,"model_timeout_ms":300000,"prompt_version":"prompt-v0","toolset_version":"toolset-v0","harness_version":"test","interactive":false,"stream":false}`,
		},
		{
			typ:  session.TypeUserMessage,
			role: "user",
			data: `{"text":"crash task","source":"-task"}`,
		},
		{
			typ:  session.TypeModelResponse,
			role: "assistant",
			data: `{"tool_calls":[{"id":"call_crash","name":"read_file","arguments":{"path":"hello.txt"}}],"finish_reason":"tool_calls","usage":{"input_tokens":1,"output_tokens":1},"latency_ms":0,"attempt":1,"streamed":false}`,
		},
	}
	var log bytes.Buffer
	for i, payload := range payloads {
		ev := session.Event{
			Turn: i,
			Type: payload.typ,
			Role: payload.role,
			Data: json.RawMessage(payload.data),
		}
		ev.V = session.Version
		ev.Seq = uint64(i + 1)
		ev.Session = id
		ev.TS = time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		line, err := jsonv2.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		log.Write(line)
		log.WriteByte('\n')
	}
	path := sessionPathFor(stateDir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, log.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return readSessionEvents(t, path)
}

func TestBinaryForkRepairsPendingCallsInCopy(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call != 1 {
			t.Errorf("provider calls = %d, want 1", call)
		}
		writeJSON(w, 200, chatFinalResponse("fork recovered"))
	})
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	parentID := "0190a0c4-0000-7000-8000-0000000000bb"
	parentEvents := seedCrashSession(t, stateDir, workspace, f.url, parentID)
	parentPath := sessionPathFor(stateDir, parentID)
	parentHash := sha256.Sum256(mustRead(t, parentPath))

	stdout, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, "", nil,
		"-fork", parentID,
		"-task", "fork after crash",
	)
	if exit != 0 {
		t.Fatalf("fork exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "fork recovered") {
		t.Fatalf("stdout = %q", stdout)
	}
	if got := sha256.Sum256(mustRead(t, parentPath)); got != parentHash {
		t.Fatal("fork changed the parent log")
	}
	files := sessionFiles(t, stateDir)
	var childPath string
	for _, file := range files {
		if file != parentPath {
			childPath = file
		}
	}
	if childPath == "" {
		t.Fatalf("fork did not create a child log: %v", files)
	}
	childID := strings.TrimSuffix(filepath.Base(childPath), ".jsonl")
	if _, err := os.Stat(filepath.Join(stateDir, "work", childID)); !os.IsNotExist(err) {
		t.Fatalf("fork left a work directory: %v", err)
	}
	if info, err := os.Stat(childPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("fork session mode = %o, want 600", info.Mode().Perm())
	}
	f.mu.Lock()
	headers := append([]http.Header(nil), f.headers...)
	f.mu.Unlock()
	if len(headers) != 1 || headers[0].Get("x-opencode-session") != childID {
		t.Fatalf("provider session header = %v, want child %q", headers, childID)
	}
	childEvents := readSessionEvents(t, childPath)
	repairs := 0
	for _, ev := range childEvents {
		if ev.Type != session.TypeToolResult {
			continue
		}
		var data struct {
			CallID string `json:"call_id"`
			Code   string `json:"code"`
			Error  string `json:"error"`
		}
		if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.CallID == "call_crash" {
			repairs++
			if data.Code != "canceled" || !strings.Contains(data.Error, "may or may not") {
				t.Fatalf("repair = %+v", data)
			}
		}
	}
	if repairs != 1 {
		t.Fatalf("repaired results = %d, want 1", repairs)
	}
	if got := countEventType(childEvents, session.TypeRunStart); got != 2 {
		t.Fatalf("child run_start events = %d, want 2 (copied + new)", got)
	}
	if lastEventSeq(parentEvents) == 0 {
		t.Fatal("parent has no events")
	}
}

func quoteJSON(t *testing.T, s string) string {
	t.Helper()
	data, err := jsonv2.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBinaryResumeRepairsPendingCalls(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call != 1 {
			t.Errorf("provider calls = %d, want 1", call)
		}
		if !bytes.Contains(body, []byte("may or may not")) {
			t.Errorf("resumed request is missing the repair result: %s", body)
		}
		writeJSON(w, 200, chatFinalResponse("resumed after crash"))
	})
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	id := "0190a0c4-0000-7000-8000-0000000000dd"
	seeded := seedCrashSession(t, stateDir, workspace, f.url, id)
	seededLast := lastEventSeq(seeded)

	run := runHarness(t, bin, workspace, stateDir, "",
		"-resume", id,
		"-task", "resume after crash",
	)
	if run.exit != 0 {
		t.Fatalf("resume exit = %d\nstdout:\n%s\nstderr:\n%s", run.exit, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout, "resumed after crash") {
		t.Fatalf("stdout = %q", run.stdout)
	}
	events := readSessionEvents(t, sessionPathFor(stateDir, id))
	repairs := 0
	for _, ev := range events {
		if ev.Type != session.TypeToolResult {
			continue
		}
		var data struct {
			CallID string `json:"call_id"`
			Code   string `json:"code"`
			Error  string `json:"error"`
		}
		if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.CallID == "call_crash" {
			repairs++
			if data.Code != "canceled" || !strings.Contains(data.Error, "may or may not") {
				t.Fatalf("repair = %+v", data)
			}
		}
	}
	if repairs != 1 {
		t.Fatalf("repaired results = %d, want 1", repairs)
	}
	if got := countEventType(events, session.TypeRunStart); got != 2 {
		t.Fatalf("run_start events = %d, want 2", got)
	}
	if got := countEventType(events, session.TypeFinal); got != 1 {
		t.Fatalf("final events = %d, want 1 for the new run", got)
	}
	if got := countEventType(events, session.TypeRunEnd); got != 1 {
		t.Fatalf("run_end events = %d, want 1 for the new run", got)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "work", id)); !os.IsNotExist(err) {
		t.Fatalf("resume left a work directory: %v", err)
	}
	for _, ev := range events {
		if ev.Type != session.TypeRunStart {
			continue
		}
		if ev.Seq <= seededLast {
			continue
		}
		var data struct {
			ResumedFrom uint64 `json:"resumed_from_seq"`
		}
		if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.ResumedFrom != seededLast {
			t.Fatalf("resumed_from_seq = %d, want %d", data.ResumedFrom, seededLast)
		}
	}
}

func TestBinaryLockConflictExitsTwo(t *testing.T) {
	bin := buildHarness(t)
	started := make(chan struct{})
	release := make(chan struct{})
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			close(started)
			<-release
			writeJSON(w, 200, chatFinalResponse("first done"))
			return
		}
		writeJSON(w, 200, chatFinalResponse("second done"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"-task", "hold the lock",
		"-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	cmd.Dir = workspace
	cmd.Env = integrationEnv(stateDir, f.url)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("first run never reached the provider\nstderr:\n%s", stderr.String())
	}
	waitUntil(t, "the session log", func() bool { return len(sessionFiles(t, stateDir)) == 1 })
	id := strings.TrimSuffix(filepath.Base(sessionFiles(t, stateDir)[0]), ".jsonl")

	_, lockStderr, exit := runHarnessRaw(t, bin, workspace, stateDir, f.url, nil,
		"-resume", id,
		"-task", "second writer",
	)
	if exit != 2 {
		t.Fatalf("locked resume exit = %d, want 2\nstderr:\n%s", exit, lockStderr)
	}
	if !strings.Contains(lockStderr, id+".jsonl") {
		t.Fatalf("lock error does not name the trace path:\n%s", lockStderr)
	}
	close(release)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("first run failed: %v\nstderr:\n%s", err, stderr.String())
	}
}

func TestBinaryRejectsV0Log(t *testing.T) {
	bin := buildHarness(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	dir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := v1TestSession
	path := sessionPathFor(stateDir, id)
	content := `{"v":1,"seq":1,"ts":"1970-01-01T00:00:00Z","session":"` + id + `","type":"session_start","data":{}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, "http://127.0.0.1:1", nil,
		"-resume", id,
		"-task", "work",
	)
	if exit != 2 {
		t.Fatalf("exit = %d, want 2\nstderr:\n%s", exit, stderr)
	}
	if !strings.Contains(stderr, "schema version") {
		t.Fatalf("stderr does not explain the version rejection:\n%s", stderr)
	}
	if got := string(mustRead(t, path)); got != content {
		t.Fatal("v0 log was modified")
	}
}

func TestBinaryStaleWorkDirRecreated(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatFinalResponse("first"))
			return
		}
		writeJSON(w, 200, chatFinalResponse("second"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	first := runHarness(t, bin, workspace, stateDir, f.url,
		"-task", "first",
		"-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	if first.exit != 0 {
		t.Fatalf("first exit = %d", first.exit)
	}
	workDir := filepath.Join(stateDir, "work", first.id)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(workDir, "junk")
	if err := os.WriteFile(junk, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := runHarness(t, bin, workspace, stateDir, "",
		"-resume", first.id,
		"-task", "second",
	)
	if second.exit != 0 {
		t.Fatalf("resume exit = %d\nstderr:\n%s", second.exit, second.stderr)
	}
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("work dir remains after resume: %v", err)
	}
}

func TestBinaryUnknownV2EventTolerated(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			writeJSON(w, 200, chatFinalResponse("first"))
			return
		}
		writeJSON(w, 200, chatFinalResponse("second"))
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	first := runHarness(t, bin, workspace, stateDir, f.url,
		"-task", "first",
		"-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	if first.exit != 0 {
		t.Fatalf("first exit = %d", first.exit)
	}
	path := sessionPathFor(stateDir, first.id)
	events := readSessionEvents(t, path)
	ev, err := session.NewEvent("future_event", 0, "", map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	ev.V = session.Version
	ev.Seq = lastEventSeq(events) + 1
	ev.Session = first.id
	ev.TS = time.Now().UTC()
	line, err := jsonv2.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	appendToSession(t, path, append(line, '\n'))

	second := runHarness(t, bin, workspace, stateDir, "",
		"-resume", first.id,
		"-task", "second",
	)
	if second.exit != 0 {
		t.Fatalf("resume exit = %d\nstderr:\n%s", second.exit, second.stderr)
	}
	if got := countEventType(readSessionEvents(t, path), "future_event"); got != 1 {
		t.Fatalf("future_event events = %d, want 1", got)
	}
}

func TestBinaryOversizedResponseRejectedBeforeTools(t *testing.T) {
	bin := buildHarness(t)
	huge := strings.Repeat("x", 1<<20)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		writeJSON(w, 200, map[string]any{
			"id": "chatcmpl-over", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": huge,
					"tool_calls": []any{map[string]any{
						"id": "call_over", "type": "function",
						"function": map[string]any{"name": "read_file", "arguments": `{"path":"hello.txt"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
	})
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarness(t, bin, workspace, stateDir, f.url,
		"-task", "over",
		"-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	if run.exit != 4 {
		t.Fatalf("exit = %d, want 4\nstderr:\n%s", run.exit, run.stderr)
	}
	if strings.Contains(run.log, `"type":"tool_result"`) {
		t.Fatal("oversized response executed a tool")
	}
	if !strings.Contains(run.log, `"class":"protocol"`) {
		t.Fatalf("log missing protocol error:\n%s", run.log)
	}
}

func TestBinaryOneMiBStreamedResponseRoundTrips(t *testing.T) {
	bin := buildHarness(t)
	huge := strings.Repeat("x", 1<<20)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"`+huge+`"}}]}`)
		writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		writeSSE(t, w, "", `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		writeSSE(t, w, "", `[DONE]`)
	})
	workspace := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarness(t, bin, workspace, stateDir, f.url,
		"-stream",
		"-task", "large",
		"-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000",
		"-max-context-tokens", "200000",
	)
	if run.exit != 0 {
		t.Fatalf("exit = %d\nstderr:\n%s", run.exit, run.stderr)
	}
	events := readSessionEvents(t, sessionPathFor(stateDir, run.id))
	for _, ev := range events {
		if ev.Type != session.TypeModelResponse {
			continue
		}
		var data struct {
			Text     string `json:"text"`
			Streamed bool   `json:"streamed"`
		}
		if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
			t.Fatal(err)
		}
		if !data.Streamed || len(data.Text) != 1<<20 {
			t.Fatalf("model_response = streamed %v text %d, want true and %d", data.Streamed, len(data.Text), 1<<20)
		}
		return
	}
	t.Fatal("no model_response event")
}
