package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	harnessOnce sync.Once
	harnessPath string
	harnessDir  string
	harnessErr  error
)

// buildHarness returns the real binary built from the current source tree.
// Integration tests run the binary, not runArgs, so exit codes, signals, and
// file modes are real.
//
// The binary is built once per test process and shared by every caller. Go
// caches compiled packages but not a linked executable written to a fresh -o
// path, so each caller that passed its own t.TempDir() paid for a full 26 MB
// relink. Sharing one build keeps the suite fast; tests only execute the
// binary and never modify it.
func buildHarness(t *testing.T) string {
	t.Helper()
	harnessOnce.Do(func() {
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			harnessErr = errors.New("runtime.Caller failed")
			return
		}
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
		dir, err := os.MkdirTemp("", "aharness-test-bin")
		if err != nil {
			harnessErr = fmt.Errorf("temp dir for harness: %w", err)
			return
		}
		bin := filepath.Join(dir, "aharness")
		cmd := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-o", bin, "./cmd/aharness")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(dir)
			harnessErr = fmt.Errorf("build harness: %w\n%s", err, out)
			return
		}
		harnessPath, harnessDir = bin, dir
	})
	if harnessErr != nil {
		t.Fatal(harnessErr)
	}
	return harnessPath
}

// cleanupHarness removes the shared test binary. TestMain calls it.
func cleanupHarness() {
	if harnessDir != "" {
		_ = os.RemoveAll(harnessDir)
	}
}

// runCLIEnv runs the CLI in-process against an explicit environment. It mirrors
// runWithIO, so stdout, stderr, and the exit code match a real process without
// paying process startup. Use it for setup runs whose process boundary is not
// under test; signals, TTY handling, file modes, and the run under test still
// execute the real binary.
func runCLIEnv(t *testing.T, getenv func(string) string, stdin io.Reader, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	var out, errBuf bytes.Buffer
	exit = runWithIOEnv(args, getenv, stdin, &out, &errBuf)
	return out.String(), errBuf.String(), exit
}

// envLookup turns environment entries into the getenv function parseConfig
// expects. A missing key reads as unset, matching a child process.
func envLookup(entries []string) func(string) string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	return func(key string) string { return values[key] }
}
