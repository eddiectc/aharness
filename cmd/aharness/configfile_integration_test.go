//go:build unix

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBinaryReadsConfigFile proves the file layer reaches a real run: with no
// -model/workspace/max-turns flags, a config file under the state directory
// supplies them, and the environment and flags still win over the file.
func TestBinaryReadsConfigFile(t *testing.T) {
	bin := buildHarness(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello from file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFakeProvider(t, func(w http.ResponseWriter, _ *http.Request, call int, _ []byte) {
		if call == 1 {
			writeJSON(w, 200, chatToolCallResponse())
			return
		}
		writeJSON(w, 200, chatFinalResponse("config done"))
	})
	configPath := filepath.Join(stateDir, "config.json")
	body := `{
		"model": "deepseek-v4.1-flash",
		"provider_base_url": "` + f.url + `",
		"workspace": "` + workspace + `",
		"max_turns": 5
	}`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	// No -model, -workspace, -provider-base-url, or -max-turns: every
	// effective value comes from the file.
	stdout, stderr, exit := runHarnessRaw(t, bin, workspace, stateDir, "", nil,
		"-task", "read hello.txt and finish")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "config done") {
		t.Errorf("stdout = %q, want the final assistant text", stdout)
	}
	files := sessionFiles(t, stateDir)
	if len(files) != 1 {
		t.Fatalf("session logs = %v, want 1", files)
	}
	start, starts := recordedSession(t, files[0])
	if start.Model != "deepseek-v4.1-flash" || start.BaseURL != f.url {
		t.Errorf("session_start model/base URL = %q/%q, want the config values", start.Model, start.BaseURL)
	}
	if len(starts) != 1 || starts[0].MaxTurns != 5 {
		t.Errorf("run_start max_turns = %+v, want 5 from the config file", starts)
	}
	if !strings.Contains(stderr, configPath) {
		t.Errorf("stderr = %q, want the resolved config path in the startup log", stderr)
	}
	if data, err := os.ReadFile(files[0]); err != nil || !strings.Contains(string(data), "hello from file") {
		t.Errorf("session log missing the tool result: %v", err)
	}

	// A flag still wins over the file for the same value.
	stdout, stderr, exit = runHarnessRaw(t, bin, workspace, stateDir, "", nil,
		"-task", "read hello.txt and finish",
		"-max-turns", "9")
	if exit != 0 {
		t.Fatalf("flag run exit = %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	files = sessionFiles(t, stateDir)
	if len(files) != 2 {
		t.Fatalf("session logs = %v, want 2", files)
	}
	_, starts = recordedSession(t, files[1])
	if len(starts) != 1 || starts[0].MaxTurns != 9 {
		t.Errorf("run_start max_turns = %+v, want the flag value 9", starts)
	}
}

// TestBinaryHTTPTimeoutDoesNotBreakFastCall proves -http-timeout reaches the
// adapter without disturbing a normal request.
func TestBinaryHTTPTimeoutDoesNotBreakFastCall(t *testing.T) {
	bin := buildHarness(t)
	f := newFakeProvider(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ []byte) {
		writeJSON(w, 200, chatFinalResponse("fast"))
	})
	stateDir := filepath.Join(t.TempDir(), "state")
	run := runHarness(t, bin, t.TempDir(), stateDir, f.url,
		"-task", "say fast",
		"-model", "deepseek-v4.1-flash",
		"-http-timeout", "30s",
	)
	if run.exit != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", run.exit, run.stderr)
	}
	if !strings.Contains(run.stdout, "fast") {
		t.Errorf("stdout = %q, want the final text", run.stdout)
	}
	if !strings.Contains(run.stderr, "http_timeout_ms=30000") {
		t.Errorf("stderr = %q, want the effective HTTP timeout logged", run.stderr)
	}
}

// TestBinaryConfigFileErrorsExitTwo covers the operator-visible failure modes:
// a broken explicit file is a config error before any session state exists.
func TestBinaryConfigFileErrorsExitTwo(t *testing.T) {
	bin := buildHarness(t)
	stateDir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte(`{"max_turns": -2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, exit := runHarnessRaw(t, bin, t.TempDir(), stateDir, "", nil,
		"-task", "x", "-model", "deepseek-v4.1-flash", "-config", path)
	if exit != exitConfig {
		t.Fatalf("exit = %d, want %d; stderr = %q", exit, exitConfig, stderr)
	}
	if !strings.Contains(stderr, "max_turns") {
		t.Errorf("stderr = %q, want the offending key named", stderr)
	}
	assertNoSessionFiles(t, stateDir)
}
