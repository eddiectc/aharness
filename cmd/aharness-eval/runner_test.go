package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCappedTail(t *testing.T) {
	t.Parallel()
	var tail cappedTail
	tail.limit = 5
	if _, err := tail.Write([]byte("abcdef")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tail.Write([]byte("gh")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := tail.String(); got != "defgh" {
		t.Errorf("tail = %q, want defgh", got)
	}

	var invalid cappedTail
	invalid.limit = 10
	if _, err := invalid.Write([]byte{0xff, 0xfe, 'o', 'k'}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := invalid.String(); !strings.Contains(got, "ok") || strings.ContainsRune(got, 0xff) {
		t.Errorf("sanitized tail = %q, want replacement runes and ok", got)
	}
}

func TestWithEnv(t *testing.T) {
	t.Parallel()
	got := withEnv([]string{"A=1", "B=2", "C=3"}, "B", "new", "D", "4")
	want := map[string]string{"A": "1", "B": "new", "C": "3", "D": "4"}
	if len(got) != len(want) {
		t.Fatalf("env = %v, want %d entries", got, len(want))
	}
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || want[key] != value {
			t.Errorf("entry %q does not match %v", entry, want)
		}
	}
}

func TestRunProcessTimeout(t *testing.T) {
	t.Parallel()
	start := time.Now()
	result := runProcess(context.Background(), 150*time.Millisecond, "/bin/sh", []string{"-c", "sleep 5"}, t.TempDir(), nil)
	if !result.TimedOut {
		t.Fatalf("TimedOut = false, want true (result %+v)", result)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout took %s, want under 3s", elapsed)
	}
	if result.StartErr != nil {
		t.Errorf("StartErr = %v, want nil", result.StartErr)
	}
}

func TestRunProcessExitCode(t *testing.T) {
	t.Parallel()
	result := runProcess(context.Background(), time.Second, "/bin/sh", []string{"-c", "exit 7"}, t.TempDir(), nil)
	if result.ExitCode != 7 || result.TimedOut || result.StartErr != nil {
		t.Fatalf("result = %+v, want exit 7", result)
	}
}

func TestRunProcessStartError(t *testing.T) {
	t.Parallel()
	result := runProcess(context.Background(), time.Second, "/nonexistent/aharness-eval-test", nil, t.TempDir(), nil)
	if result.StartErr == nil {
		t.Fatalf("StartErr = nil, want error (result %+v)", result)
	}
}

func TestFindSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := findSession(dir); err == nil {
		t.Fatal("findSession(empty) succeeded, want error")
	}
	sessions := filepath.Join(dir, "sessions")
	first := filepath.Join(sessions, "0199d0e0-0000-7000-8000-000000000001.jsonl")
	writeFile(t, first, "", 0o600)
	found, err := findSession(dir)
	if err != nil {
		t.Fatalf("findSession: %v", err)
	}
	if found != first {
		t.Errorf("findSession = %q, want %q", found, first)
	}
	writeFile(t, filepath.Join(sessions, "0199d0e0-0000-7000-8000-000000000002.jsonl"), "", 0o600)
	if _, err := findSession(dir); err == nil {
		t.Fatal("findSession(two logs) succeeded, want error")
	}
}
