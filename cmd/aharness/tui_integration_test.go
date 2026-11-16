//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/eddiectc/aharness/internal/session"
)

func TestBinaryUIJSONScriptedSession(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"hello "}}]}`)
		writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"from the tui"}}]}`)
		writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		writeSSE(t, w, "", `{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4}}`)
		writeSSE(t, w, "", `[DONE]`)
	})
	workspace := t.TempDir()
	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"say hello"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"snapshot","format":"svg"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-ui-size", "80x24", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	var frames, snapshots int
	var events []string
	var finalText string
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var head struct {
			Type  string   `json:"type"`
			Event string   `json:"event"`
			Lines []string `json:"lines"`
			SVG   string   `json:"svg"`
		}
		if err := jsonv2.Unmarshal([]byte(line), &head); err != nil {
			t.Fatalf("decode protocol line: %v\n%s", err, line)
		}
		switch head.Type {
		case "frame":
			frames++
			finalText = strings.Join(head.Lines, "\n")
			if strings.ContainsRune(finalText, '\x1b') {
				t.Fatal("frame contains ESC")
			}
		case "snapshot":
			snapshots++
			if !strings.HasPrefix(head.SVG, "<svg") {
				t.Fatalf("snapshot is not svg: %q", head.SVG)
			}
		case "event":
			events = append(events, head.Event)
		}
	}
	if frames < 2 || snapshots != 1 {
		t.Fatalf("frames = %d, snapshots = %d\n%s", frames, snapshots, stdout)
	}
	if !strings.Contains(strings.Join(events, ","), "run_started") ||
		!strings.Contains(strings.Join(events, ","), "run_finished") {
		t.Fatalf("events = %v", events)
	}
	if !strings.Contains(finalText, "hello from the tui") {
		t.Fatalf("final frame lacks the response:\n%s", finalText)
	}
	if !strings.Contains(stderr, "session ") || !strings.Contains(stderr, "last run completed") {
		t.Fatalf("stderr summary missing:\n%s", stderr)
	}

	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session files = %v", files)
	}
	id := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
	if _, err := os.Stat(filepath.Join(stateDir, "work", id)); !os.IsNotExist(err) {
		t.Fatalf("work directory survived: %v", err)
	}
	eventsLog := readSessionEvents(t, files[0])
	for _, typ := range []session.EventType{
		session.TypeSessionStart, session.TypeRunStart, session.TypeUserMessage,
		session.TypeModelResponse, session.TypeFinal, session.TypeRunEnd,
	} {
		if got := countEventType(eventsLog, typ); got != 1 {
			t.Errorf("%s events = %d, want 1", typ, got)
		}
	}
	if countEventType(eventsLog, "delta") != 0 {
		t.Fatal("session log contains display delta events")
	}
	for _, ev := range eventsLog {
		if ev.Type == session.TypeUserMessage {
			var data struct {
				Source string `json:"source"`
			}
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Source != "tui" {
				t.Errorf("user_message source = %q, want tui", data.Source)
			}
		}
	}
}

// TestBinaryUIJSONNewAndResumeSession proves the composer can replace the
// active session with a fresh log and then reopen a previous session in the
// same workspace. The resumed session appends its second run; the new session
// remains an empty session_start.
func TestBinaryUIJSONNewAndResumeSession(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		switch call {
		case 1:
			sseFinal(t, w, "first session answer")
		case 2:
			sseFinal(t, w, "second session answer")
		default:
			t.Errorf("unexpected provider call %d", call)
			http.Error(w, "unexpected call", http.StatusInternalServerError)
		}
	})
	workspace := t.TempDir()
	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"first session task"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"text","text":"/new"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"text","text":"/resume"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"text","text":"second session task"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-ui-size", "100x30", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if f.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", f.calls)
	}
	type frame struct {
		Overlay string   `json:"overlay"`
		Lines   []string `json:"lines"`
	}
	var frames []frame
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := jsonv2.Unmarshal([]byte(line), &head); err != nil {
			t.Fatalf("decode protocol line: %v\n%s", err, line)
		}
		if head.Type != "frame" {
			continue
		}
		var got frame
		if err := jsonv2.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("decode frame: %v\n%s", err, line)
		}
		frames = append(frames, got)
	}
	pickerFrame := -1
	for i, got := range frames {
		if got.Overlay == "session" {
			pickerFrame = i
			break
		}
	}
	if pickerFrame < 0 {
		t.Fatalf("no session picker frame\nstdout:\n%s", stdout)
	}
	pickerText := strings.Join(frames[pickerFrame].Lines, "\n")
	if !strings.Contains(pickerText, "first session task") {
		t.Fatalf("session picker lacks the previous task:\n%s", pickerText)
	}
	lastText := strings.Join(frames[len(frames)-1].Lines, "\n")
	if !strings.Contains(lastText, "first session answer") ||
		!strings.Contains(lastText, "second session answer") {
		t.Fatalf("final frame lacks the resumed transcript:\n%s", lastText)
	}
	if !strings.Contains(lastText, "first session task") || !strings.Contains(lastText, "second session task") {
		t.Fatalf("final frame lacks a task from the resumed session:\n%s", lastText)
	}

	files := sessionFiles(t, stateDir)
	if len(files) != 2 {
		t.Fatalf("session files = %v, want 2", files)
	}
	var resumedEvents, freshEvents []session.Event
	for _, path := range files {
		events := readSessionEvents(t, path)
		users := 0
		for _, ev := range events {
			if ev.Type == session.TypeUserMessage {
				users++
			}
		}
		switch users {
		case 2:
			resumedEvents = events
		case 0:
			freshEvents = events
		default:
			t.Fatalf("%s has %d user messages, want 0 or 2", path, users)
		}
	}
	if resumedEvents == nil || freshEvents == nil {
		t.Fatalf("session logs do not split into resumed and fresh: %v", files)
	}
	if got := countEventType(freshEvents, session.TypeSessionStart); got != 1 || len(freshEvents) != 1 {
		t.Fatalf("fresh session events = %+v, want only session_start", freshEvents)
	}
	if got := countEventType(resumedEvents, session.TypeRunStart); got != 2 {
		t.Fatalf("resumed session run_start events = %d, want 2", got)
	}
}

func sseFinal(t *testing.T, w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"`+text+`"}}]}`)
	writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	writeSSE(t, w, "", `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	writeSSE(t, w, "", `[DONE]`)
}

// TestBinaryUIJSONShowsThinking proves the model's reasoning reaches the TUI
// transcript while staying out of the durable log.
func TestBinaryUIJSONShowsThinking(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"reasoning_content":"Let me check the failing test "}}]}`)
		writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"reasoning_content":"before editing."}}]}`)
		writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"all done"}}]}`)
		writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		writeSSE(t, w, "", `{"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4}}`)
		writeSSE(t, w, "", `[DONE]`)
	})
	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"show your thinking"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-ui-size", "90x30", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	var finalText string
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var head struct {
			Type  string   `json:"type"`
			Lines []string `json:"lines"`
		}
		if err := jsonv2.Unmarshal([]byte(line), &head); err != nil {
			t.Fatalf("decode protocol line: %v\n%s", err, line)
		}
		if head.Type == "frame" {
			finalText = strings.Join(head.Lines, "\n")
		}
	}
	if !strings.Contains(finalText, "thinking") {
		t.Fatalf("final frame has no thinking block:\n%s", finalText)
	}
	if !strings.Contains(finalText, "Let me check the failing test before editing.") {
		t.Fatalf("final frame lacks the streamed reasoning:\n%s", finalText)
	}
	if !strings.Contains(finalText, "all done") {
		t.Fatalf("final frame lacks the assistant answer:\n%s", finalText)
	}

	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session files = %v", files)
	}
	if data := string(mustRead(t, files[0])); strings.Contains(data, "Let me check the failing test") {
		t.Fatalf("thinking text leaked into the session log:\n%s", data)
	}
}

// TestBinaryUIJSONShowsResponsesReasoning proves the Responses dialect's
// reasoning summary reaches the transcript through the terminal event.
func TestBinaryUIJSONShowsResponsesReasoning(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(t, w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"all done"}`)
		writeSSE(t, w, "response.completed", `{"type":"response.completed","response":{"status":"completed","output":[`+
			`{"type":"reasoning","summary":[{"type":"summary_text","text":"Checked the projection first."}]},`+
			`{"type":"message","content":[{"type":"output_text","text":"all done"}]}],`+
			`"usage":{"input_tokens":9,"output_tokens":4}}}`)
	})
	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"show your reasoning"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-ui-size", "90x30", "-model", "crouton-alpha-free",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	var finalText string
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var head struct {
			Type  string   `json:"type"`
			Lines []string `json:"lines"`
		}
		if err := jsonv2.Unmarshal([]byte(line), &head); err != nil {
			t.Fatalf("decode protocol line: %v\n%s", err, line)
		}
		if head.Type == "frame" {
			finalText = strings.Join(head.Lines, "\n")
		}
	}
	if !strings.Contains(finalText, "thinking") || !strings.Contains(finalText, "Checked the projection first.") {
		t.Fatalf("final frame lacks the Responses reasoning summary:\n%s", finalText)
	}
	files := sessionFiles(t, stateDir)
	if data := string(mustRead(t, files[0])); strings.Contains(data, "Checked the projection first.") {
		t.Fatalf("reasoning summary leaked into the session log:\n%s", data)
	}
}

func TestBinaryUIJSONSeededTask(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		sseFinal(t, w, "seeded hello")
	})
	stateDir := t.TempDir()
	script := `{"v":1,"type":"wait","until":"idle","timeout_ms":20000}` + "\n" +
		`{"v":1,"type":"quit"}` + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-task", "seeded task", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session files = %v", files)
	}
	events := readSessionEvents(t, files[0])
	if countEventType(events, session.TypeRunStart) != 1 || countEventType(events, session.TypeRunEnd) != 1 {
		t.Fatalf("run events = %d/%d", countEventType(events, session.TypeRunStart), countEventType(events, session.TypeRunEnd))
	}
	for _, ev := range events {
		if ev.Type == session.TypeUserMessage {
			var data struct {
				Source string `json:"source"`
			}
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Source != "-task" {
				t.Fatalf("seeded user_message source = %q", data.Source)
			}
		}
	}
}

func TestBinaryUIJSONResume(t *testing.T) {
	bin := buildHarness(t)
	firstProvider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		sseFinal(t, w, "first done")
	})
	workspace := t.TempDir()
	stateDir := t.TempDir()
	first := runHarness(t, bin, workspace, stateDir, firstProvider.url,
		"-task", "first task", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if first.exit != 0 {
		t.Fatalf("first exit = %d\n%s", first.exit, first.stderr)
	}
	secondProvider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		sseFinal(t, w, "second done")
	})
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"second task"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	_, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, secondProvider.url, strings.NewReader(script),
		"-ui-json", "-resume", first.id, "-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("resume exit = %d\nstderr:\n%s", exit, stderr)
	}
	events := readSessionEvents(t, sessionPathFor(stateDir, first.id))
	if countEventType(events, session.TypeSessionStart) != 1 ||
		countEventType(events, session.TypeRunStart) != 2 ||
		countEventType(events, session.TypeRunEnd) != 2 {
		t.Fatalf("session events: start=%d run_start=%d run_end=%d",
			countEventType(events, session.TypeSessionStart),
			countEventType(events, session.TypeRunStart),
			countEventType(events, session.TypeRunEnd))
	}
	var tuiSource bool
	for _, ev := range events {
		if ev.Type == session.TypeUserMessage {
			var data struct {
				Text   string `json:"text"`
				Source string `json:"source"`
			}
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Text == "second task" && data.Source == "tui" {
				tuiSource = true
			}
		}
	}
	if !tuiSource {
		t.Fatal("second task was not recorded with source tui")
	}
}

func TestBinaryUIJSONForkLeavesParentUnchanged(t *testing.T) {
	bin := buildHarness(t)
	firstProvider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		sseFinal(t, w, "parent done")
	})
	workspace := t.TempDir()
	stateDir := t.TempDir()
	first := runHarness(t, bin, workspace, stateDir, firstProvider.url,
		"-task", "parent task", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if first.exit != 0 {
		t.Fatalf("first exit = %d\n%s", first.exit, first.stderr)
	}
	parentPath := sessionPathFor(stateDir, first.id)
	parentBefore := string(mustRead(t, parentPath))

	forkProvider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		sseFinal(t, w, "fork done")
	})
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"fork task"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	_, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, forkProvider.url, strings.NewReader(script),
		"-ui-json", "-fork", first.id, "-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("fork exit = %d\nstderr:\n%s", exit, stderr)
	}
	if got := string(mustRead(t, parentPath)); got != parentBefore {
		t.Fatal("parent session changed across fork")
	}
	files := sessionFiles(t, stateDir)
	if len(files) != 2 {
		t.Fatalf("session files = %v", files)
	}
	var child string
	for _, file := range files {
		if file != parentPath {
			child = file
		}
	}
	if child == "" {
		t.Fatal("no child session")
	}
	events := readSessionEvents(t, child)
	if countEventType(events, session.TypeSessionStart) != 1 {
		t.Fatalf("child session_start = %d", countEventType(events, session.TypeSessionStart))
	}
	// The copied parent run plus the fork run.
	if countEventType(events, session.TypeRunStart) != 2 || countEventType(events, session.TypeRunEnd) != 2 {
		t.Fatalf("child runs = %d/%d", countEventType(events, session.TypeRunStart), countEventType(events, session.TypeRunEnd))
	}
}

func TestBinaryUIJSONSIGTERMCancelsAndExits130(t *testing.T) {
	bin := buildHarness(t)
	started := make(chan struct{})
	var once sync.Once
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
	})
	stateDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"-ui-json", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	cmd.Dir = t.TempDir()
	cmd.Env = integrationEnv(stateDir, f.url)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdin, `{"v":1,"type":"text","text":"hang"}`+"\n"+`{"v":1,"type":"key","key":"enter"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("provider never called\nstderr:\n%s", stderr.String())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("wait: %v", err)
		}
		exit = ee.ExitCode()
	}
	_ = stdin.Close()
	if exit != 130 {
		t.Fatalf("exit = %d, want 130\nstdout:\n%s\nstderr:\n%s", exit, stdout.String(), stderr.String())
	}
	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session files = %v", files)
	}
	events := readSessionEvents(t, files[0])
	if countEventType(events, session.TypeRunEnd) != 1 || countEventType(events, session.TypeFinal) != 1 {
		t.Fatalf("final/run_end = %d/%d", countEventType(events, session.TypeFinal), countEventType(events, session.TypeRunEnd))
	}
	for _, ev := range events {
		if ev.Type == session.TypeRunEnd {
			var data struct {
				Status string `json:"status"`
			}
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Status != "interrupted" {
				t.Fatalf("run_end status = %q", data.Status)
			}
		}
	}
}

func TestBinaryUIJSONSteeringReachesNextModelCall(t *testing.T) {
	bin := buildHarness(t)
	firstDelta := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		switch call {
		case 1:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"working"}}]}`)
			once.Do(func() { close(firstDelta) })
			<-release
			writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			writeSSE(t, w, "", `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
			writeSSE(t, w, "", `[DONE]`)
		default:
			if !bytes.Contains(body, []byte("steer please")) {
				t.Errorf("second request does not include steering: %s", body)
			}
			sseFinal(t, w, "steered done")
		}
	})
	stateDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"-ui-json", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	cmd.Dir = t.TempDir()
	cmd.Env = integrationEnv(stateDir, f.url)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 1024)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 64<<10), 2<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(stdin, s); err != nil {
			t.Fatalf("write command: %v", err)
		}
	}
	write(`{"v":1,"type":"text","text":"first task"}` + "\n")
	write(`{"v":1,"type":"key","key":"enter"}` + "\n")
	select {
	case <-firstDelta:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("provider never streamed\nstderr:\n%s", stderr.String())
	}
	write(`{"v":1,"type":"text","text":"steer please"}` + "\n")
	write(`{"v":1,"type":"key","key":"enter"}` + "\n")
	// A snapshot reply proves the steering commands were processed before
	// the provider is released; no frame is emitted for a snapshot itself.
	write(`{"v":1,"type":"snapshot","format":"svg"}` + "\n")
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stdout closed before the snapshot reply")
			}
			var head struct {
				Type string `json:"type"`
			}
			if jsonv2.Unmarshal([]byte(line), &head) == nil {
				if head.Type == "snapshot" {
					goto snapped
				}
			}
		case <-deadline:
			_ = cmd.Process.Kill()
			t.Fatalf("no snapshot reply after steering\nstderr:\n%s", stderr.String())
		}
	}
snapped:
	close(release)
	write(`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}` + "\n")
	write(`{"v":1,"type":"quit"}` + "\n")
	err = cmd.Wait()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("wait: %v", err)
		}
		exit = ee.ExitCode()
	}
	_ = stdin.Close()
	if exit != 0 {
		t.Fatalf("exit = %d\nstderr:\n%s", exit, stderr.String())
	}
	events := readSessionEvents(t, sessionFiles(t, stateDir)[0])
	if countEventType(events, session.TypeRunStart) != 1 || countEventType(events, session.TypeRunEnd) != 1 {
		var dump []string
		for _, ev := range events {
			dump = append(dump, string(ev.Type)+":"+string(ev.Data))
		}
		t.Fatalf("runs = %d/%d\n%s", countEventType(events, session.TypeRunStart), countEventType(events, session.TypeRunEnd), strings.Join(dump, "\n"))
	}
	if countEventType(events, session.TypeModelResponse) != 2 {
		var dump []string
		for _, ev := range events {
			dump = append(dump, string(ev.Type)+":"+string(ev.Data))
		}
		t.Fatalf("model_response = %d, want 2\n%s", countEventType(events, session.TypeModelResponse), strings.Join(dump, "\n"))
	}
	var steerSource bool
	for _, ev := range events {
		if ev.Type == session.TypeUserMessage {
			var data struct {
				Text   string `json:"text"`
				Source string `json:"source"`
			}
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Text == "steer please" && data.Source == "steer" {
				steerSource = true
			}
		}
	}
	if !steerSource {
		t.Fatal("steering was not recorded with source steer")
	}
}

func TestBinaryUIJSONCtrlDActiveExits130(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		<-r.Context().Done()
	})
	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"hang"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"run_started","timeout_ms":10000}`,
		`{"v":1,"type":"key","key":"ctrl-d"}`,
	}, "\n") + "\n"
	_, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 130 {
		t.Fatalf("exit = %d, want 130\nstderr:\n%s", exit, stderr)
	}
	events := readSessionEvents(t, sessionFiles(t, stateDir)[0])
	if countEventType(events, session.TypeFinal) != 1 || countEventType(events, session.TypeRunEnd) != 1 {
		t.Fatalf("final/run_end = %d/%d", countEventType(events, session.TypeFinal), countEventType(events, session.TypeRunEnd))
	}
}

func TestBinaryUIJSONStatusLineShowsContextOccupancy(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			// A tool round with reported usage, so the session-cumulative token
			// sum and the context occupancy diverge.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"checking"}}]}`)
			writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			writeSSE(t, w, "", `{"choices":[],"usage":{"prompt_tokens":9000,"completion_tokens":300}}`)
			writeSSE(t, w, "", `[DONE]`)
			return
		}
		sseFinal(t, w, "second answer")
	})
	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"first"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"text","text":"second"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-model", "deepseek-v4.1-flash", "-max-output-tokens", "1000",
		"-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", exit, stderr)
	}
	status := lastFrameText(t, stdout)
	status = status[strings.LastIndex(status, "\n")+1:]
	// Two responses accumulated into the session total, but the status line
	// reports the latest response's occupancy against the context window.
	if !strings.Contains(status, "ctx ") || !strings.Contains(status, "/200.0k") {
		t.Fatalf("status line = %q, want the context occupancy", status)
	}
	if strings.Contains(status, "tokens ") {
		t.Fatalf("status line = %q, still shows a cumulative token sum", status)
	}
	if !strings.Contains(status, "turns 2") {
		t.Fatalf("status line = %q, want the cumulative turn count", status)
	}
}

func TestBinaryUIJSONTwoRunsInOneSession(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if call == 1 {
			sseFinal(t, w, "first answer")
			return
		}
		sseFinal(t, w, "second answer")
	})
	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"text","text":"first"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"text","text":"second"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-model", "deepseek-v4.1-flash", "-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d\n%s", exit, stderr)
	}
	events := readSessionEvents(t, sessionFiles(t, stateDir)[0])
	if countEventType(events, session.TypeSessionStart) != 1 ||
		countEventType(events, session.TypeRunStart) != 2 ||
		countEventType(events, session.TypeRunEnd) != 2 {
		t.Fatalf("session/run events = %d/%d/%d",
			countEventType(events, session.TypeSessionStart),
			countEventType(events, session.TypeRunStart),
			countEventType(events, session.TypeRunEnd))
	}
	var lastRun struct {
		Turns int `json:"turns"`
	}
	for _, ev := range events {
		if ev.Type == session.TypeRunEnd {
			if err := jsonv2.Unmarshal(ev.Data, &lastRun); err != nil {
				t.Fatal(err)
			}
		}
	}
	if lastRun.Turns != 2 {
		t.Fatalf("cumulative turns = %d, want 2", lastRun.Turns)
	}
	if !strings.Contains(stdout, "second answer") {
		t.Fatalf("final frame lacks second answer:\n%s", stdout)
	}
}

func TestBinaryUIJSONModelPickerChangesNextRunOnly(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(t, w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"picked"}`)
		writeSSE(t, w, "response.completed", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"picked"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	})
	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"key","key":"ctrl-p"}`,
		`{"v":1,"type":"key","key":"up"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"text","text":"use the other model"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-model", "deepseek-v4.1-flash", "-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d\n%s", exit, stderr)
	}
	if !strings.Contains(stdout, `"event":"model_changed"`) {
		t.Fatalf("missing model_changed event:\n%s", stdout)
	}
	events := readSessionEvents(t, sessionFiles(t, stateDir)[0])
	var model, dialect string
	for _, ev := range events {
		if ev.Type == session.TypeRunStart {
			var data struct {
				Model   string `json:"model"`
				Dialect string `json:"dialect"`
			}
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			model, dialect = data.Model, data.Dialect
		}
	}
	if model != "crouton-alpha-free" || dialect != "openai-responses" {
		t.Fatalf("run_start = %q/%q", model, dialect)
	}
}

// interactiveHarness drives -ui-json over pipes so a test can switch the fake
// catalog between commands and wait for asynchronous refresh results.
type interactiveHarness struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout syncBuffer
	stderr syncBuffer

	once   sync.Once
	copied chan struct{}
	done   chan struct{}
	err    error
}

func startInteractiveHarness(t *testing.T, bin, workspace, stateDir, baseURL string, extra []string, args ...string) *interactiveHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	h := &interactiveHarness{
		t:      t,
		copied: make(chan struct{}),
		done:   make(chan struct{}),
	}
	h.cmd = exec.CommandContext(ctx, bin, args...)
	h.cmd.Dir = workspace
	h.cmd.Env = integrationEnvWith(stateDir, baseURL, extra)
	stdin, err := h.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	h.stdin = stdin
	stdout, err := h.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	h.cmd.Stderr = &h.stderr
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.Copy(&h.stdout, stdout)
		close(h.copied)
	}()
	go func() {
		h.err = h.cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(h.shutdown)
	return h
}

func (h *interactiveHarness) shutdown() {
	h.once.Do(func() {
		_ = h.stdin.Close()
		select {
		case <-h.done:
		case <-time.After(15 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.done
		}
		<-h.copied
	})
}

func (h *interactiveHarness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.stdin, line+"\n"); err != nil {
		h.t.Fatalf("write command %q: %v", line, err)
	}
}

func (h *interactiveHarness) output() string { return h.stdout.String() }

func (h *interactiveHarness) waitFor(substr string) {
	h.t.Helper()
	waitUntil(h.t, "stdout to contain "+substr, func() bool {
		return strings.Contains(h.output(), substr)
	})
}

// framesUntil sends frame commands until a frame containing marker appears.
// Refresh completion is asynchronous, so a command reply is needed to render
// the resulting notice.
func (h *interactiveHarness) framesUntil(marker string) string {
	h.t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		before := strings.Count(h.output(), `"type":"frame"`)
		h.send(`{"v":1,"type":"frame"}`)
		waitUntil(h.t, "another protocol frame", func() bool {
			return strings.Count(h.output(), `"type":"frame"`) > before
		})
		if strings.Contains(h.output(), marker) {
			return h.output()
		}
	}
	h.t.Fatalf("no frame contained %q\nstdout:\n%s\nstderr:\n%s", marker, h.output(), h.stderr.String())
	return ""
}

func (h *interactiveHarness) quit() {
	h.t.Helper()
	h.send(`{"v":1,"type":"quit"}`)
	select {
	case <-h.done:
	case <-time.After(15 * time.Second):
		h.t.Fatalf("harness did not exit after quit\nstdout:\n%s\nstderr:\n%s", h.output(), h.stderr.String())
	}
	<-h.copied
	if h.err != nil {
		h.t.Fatalf("interactive harness exit: %v\nstdout:\n%s\nstderr:\n%s", h.err, h.output(), h.stderr.String())
	}
}

// frameText extracts the visible frame lines from -ui-json stdout.
func frameText(t *testing.T, raw string) string {
	t.Helper()
	var b strings.Builder
	for _, lines := range protocolFrames(t, raw) {
		for _, text := range lines {
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// lastFrameText returns the most recent rendered frame.
func lastFrameText(t *testing.T, raw string) string {
	t.Helper()
	frames := protocolFrames(t, raw)
	if len(frames) == 0 {
		t.Fatalf("no frames in protocol output:\n%s", raw)
	}
	return strings.Join(frames[len(frames)-1], "\n")
}

func protocolFrames(t *testing.T, raw string) [][]string {
	t.Helper()
	var frames [][]string
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var frame struct {
			Type  string   `json:"type"`
			Lines []string `json:"lines"`
		}
		if err := jsonv2.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("decode protocol line: %v\n%s", err, line)
		}
		if frame.Type == "frame" {
			frames = append(frames, frame.Lines)
		}
	}
	return frames
}

func TestBinaryUIJSONModelPickerListsCatalog(t *testing.T) {
	bin := buildHarness(t)
	script := strings.Join([]string{
		`{"v":1,"type":"key","key":"ctrl-p"}`,
		`{"v":1,"type":"frame"}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), t.TempDir(), "", strings.NewReader(script),
		"-ui-json", "-ui-size", "120x40", "-model", "deepseek-v4.1-flash")
	if exit != 0 {
		t.Fatalf("exit = %d\nstderr:\n%s", exit, stderr)
	}
	text := frameText(t, stdout)
	for _, want := range []string{
		"Crouton Alpha Free", "DeepSeek V4.1 Flash", "GPT-6 Luna", "Kimi K2.7 Code",
		"Muse Spark 1.3 Contributor", "Old Deprecated", "Qwen3.7 Plus",
		"ctx 1000000", "out 384000", "ctx 1050000", "in 922000",
		"openai-responses", "openai-chat", "deprecated", "translated",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("picker frame missing %q:\n%s", want, text)
		}
	}
	for _, omitted := range []string{"No Tools", "No Context", "Exotic SDK"} {
		if strings.Contains(text, omitted) {
			t.Errorf("ineligible model %q appears in the picker:\n%s", omitted, text)
		}
	}
	order := []string{"Crouton Alpha Free", "DeepSeek V4.1 Flash", "GPT-6 Luna", "Kimi K2.7 Code", "Muse Spark 1.3 Contributor", "Old Deprecated", "Qwen3.7 Plus"}
	last := -1
	for _, name := range order {
		index := strings.Index(text, name)
		if index < 0 {
			t.Fatalf("picker frame missing %q:\n%s", name, text)
		}
		if index <= last {
			t.Fatalf("picker rows are not sorted by ID at %q:\n%s", name, text)
		}
		last = index
	}
}

func TestBinaryUIJSONModelPickerSelectsAdapter(t *testing.T) {
	bin := buildHarness(t)
	cases := []struct {
		name    string
		model   string
		dialect string
		context int
		path    string
		answer  string
	}{
		{name: "chat", model: "kimi-k2.7-code", dialect: "openai-chat", context: 262_144, path: "/chat/completions", answer: "picked chat"},
		{name: "responses", model: "gpt-6-luna", dialect: "openai-responses", context: 1_050_000, path: "/responses", answer: "picked responses"},
		{name: "translated chat", model: "qwen3.7-plus", dialect: "openai-chat", context: 131_072, path: "/chat/completions", answer: "picked chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
				}
				if tc.path == "/responses" {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					writeSSE(t, w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"`+tc.answer+`"}`)
					writeSSE(t, w, "response.completed", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"`+tc.answer+`"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`)
					return
				}
				sseFinal(t, w, tc.answer)
			})
			stateDir := t.TempDir()
			script := strings.Join([]string{
				`{"v":1,"type":"key","key":"ctrl-p"}`,
				`{"v":1,"type":"text","text":"` + tc.model + `"}`,
				`{"v":1,"type":"key","key":"enter"}`,
				`{"v":1,"type":"text","text":"use the picked model"}`,
				`{"v":1,"type":"key","key":"enter"}`,
				`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
				`{"v":1,"type":"quit"}`,
			}, "\n") + "\n"
			stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, f.url, strings.NewReader(script),
				"-ui-json", "-ui-size", "120x40", "-model", "deepseek-v4.1-flash")
			if exit != 0 {
				t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
			}
			if !strings.Contains(stdout, `"event":"model_changed"`) || !strings.Contains(stdout, `"model":"`+tc.model+`"`) {
				t.Fatalf("stdout missing the model_changed event for %s:\n%s", tc.model, stdout)
			}
			files := sessionFiles(t, stateDir)
			if len(files) != 1 {
				t.Fatalf("session files = %v", files)
			}
			start, starts := recordedSession(t, files[0])
			if start.Model != "deepseek-v4.1-flash" {
				t.Fatalf("session_start model = %q, want the initial model", start.Model)
			}
			var last recordedRunStart
			for _, rs := range starts {
				if rs.Model == tc.model {
					last = rs
				}
			}
			if last.Model != tc.model || last.Dialect != tc.dialect || last.BaseURL != f.url {
				t.Fatalf("run_start = %+v", last)
			}
			if last.Limits.MaxContextTokens != tc.context {
				t.Fatalf("run_start context = %d, want %d", last.Limits.MaxContextTokens, tc.context)
			}
		})
	}
}

func TestBinaryUIJSONModelPickerRefresh(t *testing.T) {
	bin := buildHarness(t)
	catalog := startFakeModelsDev(t)
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}
	h := startInteractiveHarness(t, bin, t.TempDir(), t.TempDir(), "", extra,
		"-ui-json", "-ui-size", "120x40", "-model", "deepseek-v4.1-flash")
	h.waitFor(`"type":"frame"`)
	h.send(`{"v":1,"type":"key","key":"ctrl-p"}`)
	h.waitFor("Crouton Alpha Free")

	models := defaultFakeCatalogModels()
	models["zeta-extra"] = testCatalogModel("Zeta Extra", "@ai-sdk/openai-compatible", 99_999, 0, 4_096)
	catalog.SetModels(models)
	released := catalog.BlockNext()
	h.send(`{"v":1,"type":"key","key":"ctrl-r"}`)
	h.waitFor("refreshing models…")
	close(released)
	h.framesUntil("refreshed 8 models")
	text := lastFrameText(t, h.output())
	if !strings.Contains(text, "Zeta Extra") {
		t.Fatalf("refreshed picker is missing the new model:\n%s", text)
	}
	if !strings.Contains(text, "DeepSeek V4.1 Flash (current)") {
		t.Fatalf("refreshed picker lost the current selection:\n%s", text)
	}
	if !strings.Contains(text, "refreshed 8 models") {
		t.Fatalf("refreshed frame missing the notice:\n%s", text)
	}
	h.quit()
}

func TestBinaryUIJSONModelPickerRefreshFailure(t *testing.T) {
	bin := buildHarness(t)
	catalog := startFakeModelsDev(t)
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}
	h := startInteractiveHarness(t, bin, t.TempDir(), t.TempDir(), "", extra,
		"-ui-json", "-ui-size", "120x40", "-model", "deepseek-v4.1-flash")
	h.waitFor(`"type":"frame"`)
	h.send(`{"v":1,"type":"key","key":"ctrl-p"}`)
	h.waitFor("Crouton Alpha Free")

	catalog.SetFailure(http.StatusInternalServerError, "catalog exploded")
	h.send(`{"v":1,"type":"key","key":"ctrl-r"}`)
	h.framesUntil("refresh models:")
	text := lastFrameText(t, h.output())
	if !strings.Contains(text, "model catalog unavailable") {
		t.Fatalf("failure notice does not name the error:\n%s", text)
	}
	for _, want := range []string{"Crouton Alpha Free", "DeepSeek V4.1 Flash", "Qwen3.7 Plus"} {
		if !strings.Contains(text, want) {
			t.Errorf("failure discarded the loaded picker entries; missing %q:\n%s", want, text)
		}
	}
	h.quit()
}

func TestBinaryUIJSONOfflineResumeRefreshEnablesSwitching(t *testing.T) {
	bin := buildHarness(t)
	catalog := startFakeModelsDev(t)
	provider := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		writeJSON(w, 200, chatFinalResponse("first done"))
	})
	workspace := t.TempDir()
	stateDir := t.TempDir()
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}
	_, stderr, exit := runHarnessRawEnv(t, bin, workspace, stateDir, provider.url, extra, nil,
		"-batch", "-task", "first", "-model", "deepseek-v4.1-flash")
	if exit != 0 {
		t.Fatalf("first exit = %d\nstderr:\n%s", exit, stderr)
	}
	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session files = %v", files)
	}
	id := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
	// Force the best-effort catalog load at resume to fail: no cache and a
	// catalog server that is down.
	if err := os.RemoveAll(filepath.Join(stateDir, "cache")); err != nil {
		t.Fatal(err)
	}
	catalog.SetFailure(http.StatusServiceUnavailable, "catalog offline")

	h := startInteractiveHarness(t, bin, workspace, stateDir, "", extra,
		"-ui-json", "-ui-size", "120x40", "-resume", id)
	h.waitFor(`"type":"frame"`)
	h.send(`{"v":1,"type":"key","key":"ctrl-p"}`)
	h.waitFor("model for the next run")
	text := frameText(t, h.output())
	if !strings.Contains(text, "deepseek-v4.1-flash") {
		t.Fatalf("offline picker does not list the recorded model:\n%s", text)
	}
	if strings.Contains(text, "Crouton Alpha Free") || strings.Contains(text, "Qwen3.7 Plus") {
		t.Fatalf("offline picker should contain only the recorded model:\n%s", text)
	}
	h.send(`{"v":1,"type":"key","key":"enter"}`)
	h.waitFor("model switching is unavailable until the catalog loads")

	catalog.SetFailure(0, "")
	h.send(`{"v":1,"type":"key","key":"ctrl-r"}`)
	h.framesUntil("refreshed 7 models")
	text = frameText(t, h.output())
	if !strings.Contains(text, "Crouton Alpha Free") {
		t.Fatalf("refresh did not expand the picker:\n%s", text)
	}
	// The recorded model is retained and highlighted; move to the previous
	// sorted entry and select it.
	h.send(`{"v":1,"type":"key","key":"up"}`)
	h.send(`{"v":1,"type":"key","key":"enter"}`)
	h.waitFor(`"event":"model_changed"`)
	if !strings.Contains(h.output(), `"model":"crouton-alpha-free"`) {
		t.Fatalf("switching after refresh did not emit the picked model:\n%s", h.output())
	}
	h.quit()
}

func TestBinaryUIJSONDeprecatedModelSelectable(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		sseFinal(t, w, "old model answered")
	})
	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"key","key":"ctrl-p"}`,
		`{"v":1,"type":"text","text":"old"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"text","text":"use the old model"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, f.url, strings.NewReader(script),
		"-ui-json", "-ui-size", "120x40", "-model", "deepseek-v4.1-flash")
	if exit != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, `"model":"old-deprecated"`) {
		t.Fatalf("deprecated model was not selected:\n%s", stdout)
	}
	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session files = %v", files)
	}
	_, starts := recordedSession(t, files[0])
	found := false
	for _, rs := range starts {
		if rs.Model == "old-deprecated" {
			found = true
			if rs.Dialect != "openai-chat" || rs.Limits.MaxContextTokens != 100_000 {
				t.Fatalf("deprecated run_start = %+v", rs)
			}
		}
	}
	if !found {
		t.Fatalf("no run_start used the deprecated model: %+v", starts)
	}
}

func TestBinaryUIJSONThinkingEffortPickerAndClearing(t *testing.T) {
	bin := buildHarness(t)
	catalog := startFakeModelsDev(t)
	models := defaultFakeCatalogModels()
	deepseek := models["deepseek-v4.1-flash"]
	deepseek.Reasoning = true
	deepseek.Efforts = []string{"max", "low", "high"}
	models["deepseek-v4.1-flash"] = deepseek
	catalog.SetModels(models)
	extra := []string{"AHARNESS_MODELS_DEV_URL=" + catalog.URL()}

	f := newFakeProvider(t, func(w http.ResponseWriter, r *http.Request, call int, body []byte) {
		if r.URL.Path == "/responses" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeSSE(t, w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"picked"}`)
			writeSSE(t, w, "response.completed", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"picked"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`)
			return
		}
		sseFinal(t, w, "chat answer")
	})

	stateDir := t.TempDir()
	script := strings.Join([]string{
		`{"v":1,"type":"key","key":"ctrl-p"}`,
		`{"v":1,"type":"key","key":"esc"}`,
		`{"v":1,"type":"key","key":"f2"}`,
		`{"v":1,"type":"key","key":"down"}`,
		`{"v":1,"type":"key","key":"down"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"text","text":"first run"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		// Provider default for the second run.
		`{"v":1,"type":"key","key":"f2"}`,
		`{"v":1,"type":"key","key":"up"}`,
		`{"v":1,"type":"key","key":"up"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"text","text":"second run"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		// Select high again, then switch to a model without effort controls.
		`{"v":1,"type":"key","key":"f2"}`,
		`{"v":1,"type":"key","key":"down"}`,
		`{"v":1,"type":"key","key":"down"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"key","key":"ctrl-p"}`,
		`{"v":1,"type":"text","text":"crouton"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"text","text":"third run"}`,
		`{"v":1,"type":"key","key":"enter"}`,
		`{"v":1,"type":"wait","until":"idle","timeout_ms":20000}`,
		`{"v":1,"type":"quit"}`,
	}, "\n") + "\n"
	stdout, stderr, exit := runHarnessRawEnv(t, bin, t.TempDir(), stateDir, f.url, extra,
		strings.NewReader(script),
		"-ui-json", "-ui-size", "120x40", "-model", "deepseek-v4.1-flash",
		"-max-output-tokens", "1000", "-max-context-tokens", "200000")
	if exit != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}

	frame := frameText(t, stdout)
	for _, want := range []string{
		"effort low/high/max",
		"provider default",
		"thinking effort for the next run: high",
		"thinking effort for the next run: provider default",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frames missing %q:\n%s", want, frame)
		}
	}
	if !strings.Contains(stdout, `"kind":"warning"`) || !strings.Contains(stdout, "cleared") {
		t.Errorf("stdout missing the effort-clearing warning:\n%s", stdout)
	}

	var runStarted []map[string]any
	var changed map[string]any
	var thinkingNotice bool
	for line := range strings.SplitSeq(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Event string `json:"event"`
			Kind  string `json:"kind"`
		}
		if err := jsonv2.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode protocol line: %v\n%s", err, line)
		}
		if ev.Type != "event" {
			continue
		}
		var fields map[string]any
		if err := jsonv2.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatal(err)
		}
		switch ev.Event {
		case "run_started":
			runStarted = append(runStarted, fields)
		case "model_changed":
			changed = fields
		case "notice":
			if ev.Kind == "thinking_effort" {
				thinkingNotice = true
			}
		}
	}
	if !thinkingNotice {
		t.Fatalf("missing thinking_effort notice:\n%s", stdout)
	}
	if len(runStarted) != 3 {
		t.Fatalf("run_started events = %d, want 3\n%s", len(runStarted), stdout)
	}
	if runStarted[0]["thinking_effort"] != "high" {
		t.Errorf("first run_started = %v, want high", runStarted[0])
	}
	for i := 1; i < len(runStarted); i++ {
		if _, ok := runStarted[i]["thinking_effort"]; ok {
			t.Errorf("run_started %d exposed the provider default: %v", i, runStarted[i])
		}
	}
	if changed == nil || changed["model"] != "crouton-alpha-free" {
		t.Fatalf("model_changed = %v", changed)
	}
	if _, ok := changed["thinking_effort"]; ok {
		t.Fatalf("cleared model_changed exposed effort: %v", changed)
	}

	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session files = %v", files)
	}
	_, starts := recordedSession(t, files[0])
	if len(starts) != 3 {
		t.Fatalf("recorded run_start events = %d, want 3", len(starts))
	}
	if starts[0].ThinkingEffort != "high" {
		t.Errorf("first run_start effort = %q, want high", starts[0].ThinkingEffort)
	}
	if starts[1].ThinkingEffort != "" || starts[2].ThinkingEffort != "" {
		t.Errorf("provider-default run_start efforts = %q/%q", starts[1].ThinkingEffort, starts[2].ThinkingEffort)
	}
	if starts[2].Model != "crouton-alpha-free" {
		t.Errorf("third run_start model = %q, want the switched model", starts[2].Model)
	}

	gotBodies := providerBodies(f)
	if len(gotBodies) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(gotBodies))
	}
	var effort string
	if !decodeJSONField(t, gotBodies[0], "reasoning_effort", &effort) || effort != "high" {
		t.Errorf("first request reasoning_effort = %q present=%v\n%s", effort, hasJSONField(t, gotBodies[0], "reasoning_effort"), gotBodies[0])
	}
	if hasJSONField(t, gotBodies[1], "reasoning_effort") {
		t.Errorf("provider-default chat request sent reasoning_effort:\n%s", gotBodies[1])
	}
	if hasJSONField(t, gotBodies[2], "reasoning") {
		t.Errorf("provider-default responses request sent reasoning:\n%s", gotBodies[2])
	}
}

func TestBinaryUIJSONDetailsShowsOutputBudget(t *testing.T) {
	bin := buildHarness(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "unset", want: "max output: unset"},
		{name: "explicit", args: []string{"-max-output-tokens", "1234"}, want: "max output: 1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"-ui-json", "-ui-size", "120x40", "-model", "deepseek-v4.1-flash"}
			args = append(args, tc.args...)
			script := strings.Join([]string{
				`{"v":1,"type":"key","key":"ctrl-x"}`,
				`{"v":1,"type":"frame"}`,
				`{"v":1,"type":"quit"}`,
			}, "\n") + "\n"
			stdout, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), t.TempDir(), "", strings.NewReader(script), args...)
			if exit != 0 {
				t.Fatalf("exit = %d\nstderr:\n%s", exit, stderr)
			}
			text := frameText(t, stdout)
			if !strings.Contains(text, tc.want) {
				t.Fatalf("details frame missing %q:\n%s", tc.want, text)
			}
			if !strings.Contains(text, "provider:   opencode-go") {
				t.Fatalf("details frame missing the provider:\n%s", text)
			}
		})
	}
}
