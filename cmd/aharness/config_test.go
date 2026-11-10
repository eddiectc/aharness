package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"aharness/internal/agent"
	"aharness/internal/model"
)

func cfgTestGetenv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func cfgTestParseOK(t *testing.T, args []string, env map[string]string) config {
	t.Helper()
	cfg, err := parseConfig(args, cfgTestGetenv(env), io.Discard)
	if err != nil {
		t.Fatalf("parseConfig(%v) returned error: %v", args, err)
	}
	return cfg
}

func cfgTestParseErr(t *testing.T, args []string, env map[string]string) error {
	t.Helper()
	cfg, err := parseConfig(args, cfgTestGetenv(env), io.Discard)
	if err == nil {
		t.Fatalf("parseConfig(%v) = %+v, want error", args, cfg)
	}
	return err
}

func cfgTestBaseArgs(t *testing.T) []string {
	t.Helper()
	return []string{
		"-task", "fix the bug",
		"-model", "deepseek-v4.1-flash",
		"-workspace", t.TempDir(),
		"-state-dir", t.TempDir(),
	}
}

func cfgTestArgs(t *testing.T, extra ...string) []string {
	t.Helper()
	return append(cfgTestBaseArgs(t), extra...)
}

func cfgTestClearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"AHARNESS_MODEL",
		"AHARNESS_WORKSPACE",
		"AHARNESS_STATE_DIR",
		"AHARNESS_PROVIDER_TOKEN",
		"AHARNESS_MAX_CONTEXT_TOKENS",
		"AHARNESS_LOG_REQUESTS",
		"AHARNESS_PROVIDER_BASE_URL",
		"XDG_STATE_HOME",
	} {
		t.Setenv(key, "")
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := cfgTestParseOK(t, cfgTestBaseArgs(t), nil)

	if cfg.Task != "fix the bug" {
		t.Errorf("Task = %q, want %q", cfg.Task, "fix the bug")
	}
	if cfg.TaskSource != "-task" {
		t.Errorf("TaskSource = %q, want %q", cfg.TaskSource, "-task")
	}
	if cfg.Provider != providerName {
		t.Errorf("Provider = %q, want %q", cfg.Provider, providerName)
	}
	if cfg.Model != "deepseek-v4.1-flash" {
		t.Errorf("Model = %q, want %q", cfg.Model, "deepseek-v4.1-flash")
	}
	if cfg.Dialect != model.DialectChat {
		t.Errorf("Dialect = %q, want %q", cfg.Dialect, model.DialectChat)
	}
	if cfg.MaxTurns != defaultMaxTurns {
		t.Errorf("MaxTurns = %d, want %d", cfg.MaxTurns, defaultMaxTurns)
	}
	if cfg.MaxTotalTokens != defaultMaxTotalTokens {
		t.Errorf("MaxTotalTokens = %d, want %d", cfg.MaxTotalTokens, defaultMaxTotalTokens)
	}
	if cfg.MaxContextTokens != defaultMaxContextTokens {
		t.Errorf("MaxContextTokens = %d, want %d", cfg.MaxContextTokens, defaultMaxContextTokens)
	}
	if cfg.MaxOutputTokens != defaultMaxOutputTokens {
		t.Errorf("MaxOutputTokens = %d, want %d", cfg.MaxOutputTokens, defaultMaxOutputTokens)
	}
	if cfg.SessionTimeout != defaultSessionTimeout {
		t.Errorf("SessionTimeout = %s, want %s", cfg.SessionTimeout, defaultSessionTimeout)
	}
	if cfg.ModelTimeout != defaultModelTimeout {
		t.Errorf("ModelTimeout = %s, want %s", cfg.ModelTimeout, defaultModelTimeout)
	}
	if cfg.BaseURL != model.DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, model.DefaultBaseURL)
	}
	if cfg.LogRequests {
		t.Error("LogRequests = true, want false")
	}
	if !cfg.ProvisionalContext {
		t.Error("ProvisionalContext = false, want true when max context is not configured")
	}
	wantMargin := agent.SafetyMargin(defaultMaxContextTokens)
	if wantMargin != 10_000 {
		t.Fatalf("agent.SafetyMargin(%d) = %d, want 10000", defaultMaxContextTokens, wantMargin)
	}
	if cfg.Limits.SafetyMargin != wantMargin {
		t.Errorf("Limits.SafetyMargin = %d, want %d", cfg.Limits.SafetyMargin, wantMargin)
	}
	wantInput := defaultMaxContextTokens - defaultMaxOutputTokens - wantMargin
	if cfg.Limits.MaxInputTokens != wantInput {
		t.Errorf("Limits.MaxInputTokens = %d, want %d", cfg.Limits.MaxInputTokens, wantInput)
	}
	if cfg.Limits.CompactionTarget != wantInput {
		t.Errorf("Limits.CompactionTarget = %d, want %d", cfg.Limits.CompactionTarget, wantInput)
	}
}

func TestConfigEnvFlagPrecedence(t *testing.T) {
	envWorkspace := t.TempDir()
	envState := t.TempDir()
	flagWorkspace := t.TempDir()
	flagState := t.TempDir()

	env := map[string]string{
		"AHARNESS_MODEL":              "deepseek-v4.1-flash",
		"AHARNESS_WORKSPACE":          envWorkspace,
		"AHARNESS_STATE_DIR":          envState,
		"AHARNESS_MAX_CONTEXT_TOKENS": "100000",
		"AHARNESS_PROVIDER_BASE_URL":  "http://env.example/v1",
		"AHARNESS_PROVIDER_TOKEN":     "env-token",
	}
	args := []string{
		"-task", "flag task",
		"-model", "crouton-alpha-free",
		"-workspace", flagWorkspace,
		"-state-dir", flagState,
		"-max-context-tokens", "150000",
		"-provider-base-url", "http://flag.example/v1",
	}
	cfg := cfgTestParseOK(t, args, env)

	if cfg.Model != "crouton-alpha-free" {
		t.Errorf("Model = %q, want flag value %q", cfg.Model, "crouton-alpha-free")
	}
	if cfg.Dialect != model.DialectResponses {
		t.Errorf("Dialect = %q, want %q", cfg.Dialect, model.DialectResponses)
	}
	if cfg.Workspace != flagWorkspace {
		t.Errorf("Workspace = %q, want flag value %q", cfg.Workspace, flagWorkspace)
	}
	if cfg.StateDir != flagState {
		t.Errorf("StateDir = %q, want flag value %q", cfg.StateDir, flagState)
	}
	if cfg.MaxContextTokens != 150_000 {
		t.Errorf("MaxContextTokens = %d, want flag value 150000", cfg.MaxContextTokens)
	}
	if cfg.BaseURL != "http://flag.example/v1" {
		t.Errorf("BaseURL = %q, want flag value %q", cfg.BaseURL, "http://flag.example/v1")
	}
	if cfg.Token != "env-token" {
		t.Errorf("Token = %q, want environment value %q", cfg.Token, "env-token")
	}
	wantMargin := agent.SafetyMargin(150_000)
	if cfg.Limits.SafetyMargin != wantMargin {
		t.Errorf("Limits.SafetyMargin = %d, want %d", cfg.Limits.SafetyMargin, wantMargin)
	}
	if cfg.ProvisionalContext {
		t.Error("ProvisionalContext = true, want false when max context is configured")
	}
}

func TestConfigEnvOverridesDefaults(t *testing.T) {
	envWorkspace := t.TempDir()
	envState := t.TempDir()
	env := map[string]string{
		"AHARNESS_MODEL":              "deepseek-v4.1-flash",
		"AHARNESS_WORKSPACE":          envWorkspace,
		"AHARNESS_STATE_DIR":          envState,
		"AHARNESS_MAX_CONTEXT_TOKENS": "100000",
		"AHARNESS_PROVIDER_BASE_URL":  "http://env.example/v1",
		"AHARNESS_PROVIDER_TOKEN":     "env-token",
		"AHARNESS_LOG_REQUESTS":       "1",
	}
	cfg := cfgTestParseOK(t, []string{"-task", "env task"}, env)

	if cfg.Task != "env task" {
		t.Errorf("Task = %q, want %q", cfg.Task, "env task")
	}
	if cfg.Model != "deepseek-v4.1-flash" {
		t.Errorf("Model = %q, want environment value", cfg.Model)
	}
	if cfg.Workspace != envWorkspace {
		t.Errorf("Workspace = %q, want environment value %q", cfg.Workspace, envWorkspace)
	}
	if cfg.StateDir != envState {
		t.Errorf("StateDir = %q, want environment value %q", cfg.StateDir, envState)
	}
	if cfg.MaxContextTokens != 100_000 {
		t.Errorf("MaxContextTokens = %d, want environment value 100000", cfg.MaxContextTokens)
	}
	if cfg.BaseURL != "http://env.example/v1" {
		t.Errorf("BaseURL = %q, want environment value", cfg.BaseURL)
	}
	if cfg.Token != "env-token" {
		t.Errorf("Token = %q, want environment value", cfg.Token)
	}
	if !cfg.LogRequests {
		t.Error("LogRequests = false, want true from AHARNESS_LOG_REQUESTS=1")
	}
	if cfg.ProvisionalContext {
		t.Error("ProvisionalContext = true, want false when AHARNESS_MAX_CONTEXT_TOKENS is set")
	}
}

func TestConfigTaskSources(t *testing.T) {
	common := []string{
		"-batch",
		"-model", "deepseek-v4.1-flash",
		"-workspace", t.TempDir(),
		"-state-dir", t.TempDir(),
	}

	t.Run("task flag", func(t *testing.T) {
		args := append(append([]string{}, common...), "-task", "inline task")
		cfg := cfgTestParseOK(t, args, nil)
		if cfg.Task != "inline task" {
			t.Errorf("Task = %q, want %q", cfg.Task, "inline task")
		}
		if cfg.TaskSource != "-task" {
			t.Errorf("TaskSource = %q, want %q", cfg.TaskSource, "-task")
		}
	})

	t.Run("task file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "task.md")
		if err := os.WriteFile(path, []byte("file task\n"), 0o600); err != nil {
			t.Fatalf("write task file: %v", err)
		}
		args := append(append([]string{}, common...), "-task-file", path)
		cfg := cfgTestParseOK(t, args, nil)
		if cfg.Task != "file task\n" {
			t.Errorf("Task = %q, want %q", cfg.Task, "file task\n")
		}
		if cfg.TaskSource != "-task-file" {
			t.Errorf("TaskSource = %q, want %q", cfg.TaskSource, "-task-file")
		}
	})

	t.Run("task file sanitizes invalid utf8", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "task.md")
		if err := os.WriteFile(path, []byte{'h', 'i', 0xff}, 0o600); err != nil {
			t.Fatalf("write task file: %v", err)
		}
		args := append(append([]string{}, common...), "-task-file", path)
		cfg := cfgTestParseOK(t, args, nil)
		if !utf8.ValidString(cfg.Task) {
			t.Errorf("Task = %q, want valid UTF-8", cfg.Task)
		}
		if !strings.Contains(cfg.Task, "\uFFFD") {
			t.Errorf("Task = %q, want replacement character for invalid input", cfg.Task)
		}
		if !strings.Contains(cfg.Task, "hi") {
			t.Errorf("Task = %q, want preserved valid bytes", cfg.Task)
		}
	})
}

func TestConfigTaskRequiredExactlyOne(t *testing.T) {
	common := []string{
		"-batch",
		"-model", "deepseek-v4.1-flash",
		"-workspace", t.TempDir(),
		"-state-dir", t.TempDir(),
	}

	t.Run("missing", func(t *testing.T) {
		err := cfgTestParseErr(t, common, nil)
		if !strings.Contains(err.Error(), "one of -task or -task-file is required") {
			t.Errorf("error = %q, want task requirement", err)
		}
	})

	t.Run("both", func(t *testing.T) {
		args := append(append([]string{}, common...), "-task", "inline", "-task-file", "task.md")
		err := cfgTestParseErr(t, args, nil)
		if !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("error = %q, want exclusivity error", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.md")
		args := append(append([]string{}, common...), "-task-file", path)
		err := cfgTestParseErr(t, args, nil)
		if !strings.Contains(err.Error(), "read task file") {
			t.Errorf("error = %q, want task file read error", err)
		}
	})
}

func TestConfigModelRegistry(t *testing.T) {
	common := []string{
		"-task", "task",
		"-workspace", t.TempDir(),
		"-state-dir", t.TempDir(),
	}
	cases := []struct {
		model   string
		dialect model.Dialect
	}{
		{"crouton-alpha-free", model.DialectResponses},
		{"deepseek-v4.1-flash", model.DialectChat},
		{"muse-spark-1.3-contributor", model.DialectResponses},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			args := append(append([]string{}, common...), "-model", tc.model)
			cfg := cfgTestParseOK(t, args, nil)
			if cfg.Model != tc.model {
				t.Errorf("Model = %q, want %q", cfg.Model, tc.model)
			}
			if cfg.Dialect != tc.dialect {
				t.Errorf("Dialect = %q, want %q", cfg.Dialect, tc.dialect)
			}
			if cfg.Provider != providerName {
				t.Errorf("Provider = %q, want %q", cfg.Provider, providerName)
			}
		})
	}
}

func TestConfigModelRequired(t *testing.T) {
	args := []string{
		"-task", "task",
		"-workspace", t.TempDir(),
		"-state-dir", t.TempDir(),
	}
	err := cfgTestParseErr(t, args, nil)
	if !strings.Contains(err.Error(), "model is required") {
		t.Errorf("error = %q, want model requirement", err)
	}
}

func TestConfigModelUnsupported(t *testing.T) {
	args := []string{
		"-task", "task",
		"-model", "unknown-model",
		"-workspace", t.TempDir(),
		"-state-dir", t.TempDir(),
	}
	err := cfgTestParseErr(t, args, nil)
	if !strings.Contains(err.Error(), "unsupported model") {
		t.Errorf("error = %q, want unsupported model error", err)
	}
}

func TestConfigStateDirResolution(t *testing.T) {
	common := []string{
		"-task", "task",
		"-model", "deepseek-v4.1-flash",
		"-workspace", t.TempDir(),
	}

	t.Run("flag", func(t *testing.T) {
		dir := t.TempDir()
		cfg := cfgTestParseOK(t, append(append([]string{}, common...), "-state-dir", dir), nil)
		if cfg.StateDir != dir {
			t.Errorf("StateDir = %q, want %q", cfg.StateDir, dir)
		}
	})

	t.Run("environment", func(t *testing.T) {
		dir := t.TempDir()
		cfg := cfgTestParseOK(t, common, map[string]string{"AHARNESS_STATE_DIR": dir})
		if cfg.StateDir != dir {
			t.Errorf("StateDir = %q, want environment value %q", cfg.StateDir, dir)
		}
	})

	t.Run("xdg absolute", func(t *testing.T) {
		dir := t.TempDir()
		cfg := cfgTestParseOK(t, common, map[string]string{"XDG_STATE_HOME": dir})
		want := filepath.Join(dir, "aharness")
		if cfg.StateDir != want {
			t.Errorf("StateDir = %q, want %q", cfg.StateDir, want)
		}
	})

	t.Run("xdg relative rejected", func(t *testing.T) {
		err := cfgTestParseErr(t, common, map[string]string{"XDG_STATE_HOME": "relative/state"})
		if !strings.Contains(err.Error(), "XDG_STATE_HOME") {
			t.Errorf("error = %q, want XDG_STATE_HOME error", err)
		}
	})

	t.Run("home fallback", func(t *testing.T) {
		home := t.TempDir()
		cfg := cfgTestParseOK(t, common, map[string]string{"HOME": home})
		want := filepath.Join(home, ".local", "state", "aharness")
		if cfg.StateDir != want {
			t.Errorf("StateDir = %q, want %q", cfg.StateDir, want)
		}
	})

	t.Run("explicit overrides relative xdg", func(t *testing.T) {
		dir := t.TempDir()
		env := map[string]string{"XDG_STATE_HOME": "relative/state"}
		cfg := cfgTestParseOK(t, append(append([]string{}, common...), "-state-dir", dir), env)
		if cfg.StateDir != dir {
			t.Errorf("StateDir = %q, want explicit %q", cfg.StateDir, dir)
		}
	})
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		flag string
		want string
	}{
		{"max turns zero", "-max-turns=0", "max turns"},
		{"max turns negative", "-max-turns=-1", "max turns"},
		{"max total tokens zero", "-max-total-tokens=0", "max total tokens"},
		{"max total tokens negative", "-max-total-tokens=-1", "max total tokens"},
		{"max context tokens zero", "-max-context-tokens=0", "max context tokens"},
		{"max context tokens negative", "-max-context-tokens=-1", "max context tokens"},
		{"max output tokens zero", "-max-output-tokens=0", "max output tokens"},
		{"max output tokens negative", "-max-output-tokens=-1", "max output tokens"},
		{"session timeout zero", "-session-timeout=0s", "session timeout"},
		{"session timeout negative", "-session-timeout=-1s", "session timeout"},
		{"output plus margin equals context", "-max-context-tokens=9192", "safety margin"},
		{"output plus margin exceeds context", "-max-context-tokens=9000", "safety margin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cfgTestParseErr(t, cfgTestArgs(t, tc.flag), nil)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestConfigLimitBoundary(t *testing.T) {
	cfg := cfgTestParseOK(t, cfgTestArgs(t, "-max-context-tokens=9193"), nil)
	if cfg.Limits.SafetyMargin != 1000 {
		t.Errorf("Limits.SafetyMargin = %d, want 1000", cfg.Limits.SafetyMargin)
	}
	if cfg.Limits.MaxInputTokens != 1 {
		t.Errorf("Limits.MaxInputTokens = %d, want 1", cfg.Limits.MaxInputTokens)
	}
	if cfg.Limits.CompactionTarget != 1 {
		t.Errorf("Limits.CompactionTarget = %d, want 1", cfg.Limits.CompactionTarget)
	}
}

func TestConfigSafetyMargin(t *testing.T) {
	for _, contextTokens := range []int{10_000, 20_000, 40_000, 100_000, 200_000, 250_000} {
		t.Run(strconv.Itoa(contextTokens), func(t *testing.T) {
			cfg := cfgTestParseOK(t, cfgTestArgs(t, "-max-context-tokens="+strconv.Itoa(contextTokens)), nil)
			wantMargin := agent.SafetyMargin(contextTokens)
			if cfg.Limits.SafetyMargin != wantMargin {
				t.Errorf("Limits.SafetyMargin = %d, want %d", cfg.Limits.SafetyMargin, wantMargin)
			}
			wantInput := contextTokens - cfg.MaxOutputTokens - wantMargin
			if cfg.Limits.MaxInputTokens != wantInput {
				t.Errorf("Limits.MaxInputTokens = %d, want %d", cfg.Limits.MaxInputTokens, wantInput)
			}
			if cfg.Limits.CompactionTarget != wantInput {
				t.Errorf("Limits.CompactionTarget = %d, want %d", cfg.Limits.CompactionTarget, wantInput)
			}
		})
	}

	cfg := cfgTestParseOK(t, cfgTestArgs(t, "-max-context-tokens=10000"), nil)
	if cfg.Limits.SafetyMargin != 1000 {
		t.Errorf("small-context safety margin = %d, want 1000 floor", cfg.Limits.SafetyMargin)
	}
}

func TestConfigBaseURL(t *testing.T) {
	common := []string{
		"-task", "task",
		"-model", "deepseek-v4.1-flash",
		"-workspace", t.TempDir(),
		"-state-dir", t.TempDir(),
	}

	t.Run("default", func(t *testing.T) {
		cfg := cfgTestParseOK(t, common, nil)
		if cfg.BaseURL != model.DefaultBaseURL {
			t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, model.DefaultBaseURL)
		}
	})

	t.Run("environment", func(t *testing.T) {
		cfg := cfgTestParseOK(t, common, map[string]string{
			"AHARNESS_PROVIDER_BASE_URL": "http://env.example/v1",
		})
		if cfg.BaseURL != "http://env.example/v1" {
			t.Errorf("BaseURL = %q, want environment override", cfg.BaseURL)
		}
	})

	t.Run("flag overrides environment", func(t *testing.T) {
		args := append(append([]string{}, common...), "-provider-base-url", "http://flag.example/v1")
		cfg := cfgTestParseOK(t, args, map[string]string{
			"AHARNESS_PROVIDER_BASE_URL": "http://env.example/v1",
		})
		if cfg.BaseURL != "http://flag.example/v1" {
			t.Errorf("BaseURL = %q, want flag override", cfg.BaseURL)
		}
	})

	t.Run("invalid scheme", func(t *testing.T) {
		err := cfgTestParseErr(t, append(append([]string{}, common...), "-provider-base-url=ftp://example.com/v1"), nil)
		if !strings.Contains(err.Error(), "provider base URL") {
			t.Errorf("error = %q, want base URL error", err)
		}
	})

	t.Run("missing host", func(t *testing.T) {
		err := cfgTestParseErr(t, append(append([]string{}, common...), "-provider-base-url=http:///v1"), nil)
		if !strings.Contains(err.Error(), "provider base URL") {
			t.Errorf("error = %q, want base URL error", err)
		}
	})
}

func TestConfigProvisionalContext(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want bool
	}{
		{
			name: "neither set",
			want: true,
		},
		{
			name: "environment set",
			env:  map[string]string{"AHARNESS_MAX_CONTEXT_TOKENS": "100000"},
			want: false,
		},
		{
			name: "flag set to default value",
			args: []string{"-max-context-tokens=200000"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgTestParseOK(t, cfgTestArgs(t, tc.args...), tc.env)
			if cfg.ProvisionalContext != tc.want {
				t.Errorf("ProvisionalContext = %t, want %t", cfg.ProvisionalContext, tc.want)
			}
		})
	}
}

func TestConfigEnvErrors(t *testing.T) {
	common := []string{
		"-task", "task",
		"-model", "deepseek-v4.1-flash",
		"-workspace", t.TempDir(),
		"-state-dir", t.TempDir(),
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "max context not an integer",
			env:  map[string]string{"AHARNESS_MAX_CONTEXT_TOKENS": "many"},
			want: "AHARNESS_MAX_CONTEXT_TOKENS",
		},
		{
			name: "log requests not a bool",
			env:  map[string]string{"AHARNESS_LOG_REQUESTS": "sometimes"},
			want: "AHARNESS_LOG_REQUESTS",
		},
		{
			name: "max context not positive",
			env:  map[string]string{"AHARNESS_MAX_CONTEXT_TOKENS": "0"},
			want: "max context tokens must be positive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cfgTestParseErr(t, common, tc.env)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestVersionRequested(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"long form", []string{"-version"}, true},
		{"double dash", []string{"--version"}, true},
		{"documented true", []string{"-version=true"}, true},
		{"explicit false", []string{"-version=false"}, false},
		{"after flags", []string{"-task", "x", "-version"}, true},
		{"task value named version", []string{"-task", "-version"}, false},
		{"after positional", []string{"task", "-version"}, false},
		{"after terminator", []string{"--", "-version"}, false},
		{"help first", []string{"-h", "-version"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionRequested(tc.args); got != tc.want {
				t.Errorf("versionRequested(%v) = %t, want %t", tc.args, got, tc.want)
			}
		})
	}
}

func TestRunArgsConfigErrorExitsTwo(t *testing.T) {
	cfgTestClearEnv(t)
	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runArgs([]string{
		"-model", "deepseek-v4.1-flash",
		"-workspace", t.TempDir(),
		"-state-dir", stateDir,
	}, &stdout, &stderr)
	if code != exitConfig {
		t.Fatalf("runArgs exit code = %d, want %d", code, exitConfig)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("stderr is empty, want a config error message")
	}
	for _, sub := range []string{"sessions", "work"} {
		path := filepath.Join(stateDir, sub)
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat %s: err = %v, want os.ErrNotExist (no session state before validation)", path, err)
		}
	}
}

func TestRunArgsVersion(t *testing.T) {
	cfgTestClearEnv(t)
	var stdout, stderr bytes.Buffer
	code := runArgs([]string{"-version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runArgs -version exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"aharness", "go:", "vcs revision:", "vcs modified:"} {
		if !strings.Contains(out, want) {
			t.Errorf("version output = %q, want substring %q", out, want)
		}
	}
}

func TestRunArgsHelp(t *testing.T) {
	cfgTestClearEnv(t)
	var stdout, stderr bytes.Buffer
	if code := runArgs([]string{"-h"}, &stdout, &stderr); code != 0 {
		t.Errorf("runArgs -h exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "usage: aharness") {
		t.Errorf("stderr = %q, want usage text", stderr.String())
	}
}
