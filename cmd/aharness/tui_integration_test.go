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

	"aharness/internal/session"
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

func sseFinal(t *testing.T, w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{"content":"`+text+`"}}]}`)
	writeSSE(t, w, "", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	writeSSE(t, w, "", `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	writeSSE(t, w, "", `[DONE]`)
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
