package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"testing"
)

func executableFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "arm.sh")
	writeFile(t, path, "#!/bin/sh\nexit 0\n", 0o755)
	return path
}

func TestParseConfigValid(t *testing.T) {
	t.Parallel()
	first := executableFile(t)
	second := executableFile(t)
	cfg, err := parseConfig([]string{
		"-tasks", "eval/tasks",
		"-arm", "before=" + first,
		"-arm", "after=" + second,
		"-model", "model-x",
		"-thinking-effort", "high",
		"-repeats", "2",
		"-seed", "9",
		"-prices", "",
		"-out", "results",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(cfg.Arms) != 2 || cfg.Arms[0].Name != "before" || cfg.Arms[1].Name != "after" {
		t.Fatalf("arms = %+v", cfg.Arms)
	}
	if cfg.Arms[0].Path != first || cfg.Arms[1].Path != second {
		t.Errorf("arm paths = %s, %s; want %s, %s", cfg.Arms[0].Path, cfg.Arms[1].Path, first, second)
	}
	if cfg.Repeats != 2 || cfg.Seed != 9 || cfg.Model != "model-x" || cfg.ThinkingEffort != "high" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestParseConfigDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := parseConfig([]string{"-arm", "only=" + executableFile(t), "-model", "m", "-prices", ""}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.TasksDir != "eval/tasks" {
		t.Errorf("tasks = %q, want eval/tasks", cfg.TasksDir)
	}
	if cfg.Repeats != defaultRepeats || cfg.Seed != defaultSeed {
		t.Errorf("repeats/seed = %d/%d, want %d/%d", cfg.Repeats, cfg.Seed, defaultRepeats, defaultSeed)
	}
	if cfg.OutDir != "eval/results/latest" {
		t.Errorf("out = %q, want eval/results/latest", cfg.OutDir)
	}
	if cfg.ThinkingEffort != "default" {
		t.Errorf("thinking effort = %q, want default", cfg.ThinkingEffort)
	}
}

func TestParseConfigInvalid(t *testing.T) {
	t.Parallel()
	executable := executableFile(t)
	directory := t.TempDir()
	notExecutable := filepath.Join(t.TempDir(), "plain.sh")
	writeFile(t, notExecutable, "#!/bin/sh\n", 0o644)
	tests := []struct {
		name string
		args []string
	}{
		{"missing arm", []string{"-model", "m"}},
		{"missing model", []string{"-arm", "a=" + executable}},
		{"bad arm syntax", []string{"-arm", executable, "-model", "m"}},
		{"empty arm name", []string{"-arm", "=" + executable, "-model", "m"}},
		{"duplicate arm", []string{"-arm", "a=" + executable, "-arm", "a=" + executable, "-model", "m"}},
		{"missing arm path", []string{"-arm", "a=" + filepath.Join(t.TempDir(), "nope"), "-model", "m"}},
		{"directory arm", []string{"-arm", "a=" + directory, "-model", "m"}},
		{"not executable", []string{"-arm", "a=" + notExecutable, "-model", "m"}},
		{"zero repeats", []string{"-arm", "a=" + executable, "-model", "m", "-repeats", "0"}},
		{"negative repeats", []string{"-arm", "a=" + executable, "-model", "m", "-repeats", "-1"}},
		{"empty effort", []string{"-arm", "a=" + executable, "-model", "m", "-thinking-effort", ""}},
		{"extra args", []string{"-arm", "a=" + executable, "-model", "m", "extra"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseConfig(test.args, io.Discard); err == nil {
				t.Fatalf("parseConfig(%s) succeeded, want error", test.name)
			}
		})
	}
}

func TestParseConfigHelp(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	_, err := parseConfig([]string{"-h"}, &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseConfig(-h) error = %v, want flag.ErrHelp", err)
	}
	if stderr.Len() == 0 {
		t.Error("help output is empty")
	}
}

func TestRunHelpExitsZero(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"-h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(-h) = %d, want 0", code)
	}
}
