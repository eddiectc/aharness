package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
