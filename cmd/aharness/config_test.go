package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
		"AHARNESS_PROVIDER",
		"AHARNESS_WORKSPACE",
		"AHARNESS_STATE_DIR",
		"AHARNESS_PROVIDER_TOKEN",
		"AHARNESS_MAX_CONTEXT_TOKENS",
		"AHARNESS_LOG_REQUESTS",
		"AHARNESS_PROVIDER_BASE_URL",
		"AHARNESS_MODELS_DEV_URL",
		"XDG_STATE_HOME",
	} {
		t.Setenv(key, "")
	}
}

// cfgTestResolve runs the new-session catalog resolution for a parsed config.
func cfgTestResolve(t *testing.T, cfg config, fake *fakeModelsDev) (config, string) {
	t.Helper()
	if cfg.ModelsDevURL == "" {
		cfg.ModelsDevURL = fake.URL()
	}
	source, err := newCatalogSource(cfg)
	if err != nil {
		t.Fatalf("newCatalogSource: %v", err)
	}
	var stderr bytes.Buffer
	if err := resolveNewModel(context.Background(), &cfg, source, &stderr); err != nil {
		t.Fatalf("resolveNewModel: %v", err)
	}
	return cfg, stderr.String()
}

func cfgTestResolveErr(t *testing.T, cfg config, fake *fakeModelsDev) error {
	t.Helper()
	if cfg.ModelsDevURL == "" {
		cfg.ModelsDevURL = fake.URL()
	}
	source, err := newCatalogSource(cfg)
	if err != nil {
		t.Fatalf("newCatalogSource: %v", err)
	}
	err = resolveNewModel(context.Background(), &cfg, source, io.Discard)
	if err == nil {
		t.Fatalf("resolveNewModel(%q) succeeded, want an error", cfg.Model)
	}
	return err
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
	// parseConfig only parses flags and environment. Dialect, base URL, and
	// per-call limits are resolved from the catalog before session_start.
	if cfg.Dialect != "" {
		t.Errorf("Dialect = %q, want empty before resolution", cfg.Dialect)
	}
	if cfg.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty before resolution", cfg.BaseURL)
	}
	if cfg.MaxContextTokens != 0 {
		t.Errorf("MaxContextTokens = %d, want 0 (unset) before resolution", cfg.MaxContextTokens)
	}
	if cfg.MaxOutputTokens != 0 {
		t.Errorf("MaxOutputTokens = %d, want 0 (no harness cap)", cfg.MaxOutputTokens)
	}
	if cfg.InputLimit != 0 {
		t.Errorf("InputLimit = %d, want 0 before resolution", cfg.InputLimit)
	}
	if cfg.Limits != (agent.Limits{}) {
		t.Errorf("Limits = %+v, want zero before resolution", cfg.Limits)
	}
	if cfg.maxContextSet || cfg.maxOutputSet || cfg.baseURLSet {
		t.Errorf("explicit flags = context %t output %t base %t, want all false", cfg.maxContextSet, cfg.maxOutputSet, cfg.baseURLSet)
	}
	if !cfg.modelSet {
		t.Error("modelSet = false, want true for -model")
	}
	if cfg.MaxTurns != 0 {
		t.Errorf("MaxTurns = %d, want 0 (unlimited)", cfg.MaxTurns)
	}
	if cfg.MaxTotalTokens != 0 {
		t.Errorf("MaxTotalTokens = %d, want 0 (unlimited)", cfg.MaxTotalTokens)
	}
	if cfg.SessionTimeout != 0 {
		t.Errorf("SessionTimeout = %s, want 0 (no deadline)", cfg.SessionTimeout)
	}
	if cfg.ModelTimeout != 0 {
		t.Errorf("ModelTimeout = %s, want 0 (no deadline)", cfg.ModelTimeout)
	}
	if cfg.maxTurnsSet || cfg.maxTotalTokensSet || cfg.sessionTimeoutSet || cfg.modelTimeoutSet {
		t.Errorf("explicit budgets = turns %t tokens %t session %t model %t, want all false",
			cfg.maxTurnsSet, cfg.maxTotalTokensSet, cfg.sessionTimeoutSet, cfg.modelTimeoutSet)
	}
	if cfg.LogRequests {
		t.Error("LogRequests = true, want false")
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
	if cfg.Workspace != flagWorkspace {
		t.Errorf("Workspace = %q, want flag value %q", cfg.Workspace, flagWorkspace)
	}
	if cfg.StateDir != flagState {
		t.Errorf("StateDir = %q, want flag value %q", cfg.StateDir, flagState)
	}
	if cfg.MaxContextTokens != 150_000 {
		t.Errorf("MaxContextTokens = %d, want flag value 150000", cfg.MaxContextTokens)
	}
	if !cfg.maxContextSet {
		t.Error("maxContextSet = false, want true for -max-context-tokens")
	}
	if cfg.BaseURL != "http://flag.example/v1" {
		t.Errorf("BaseURL = %q, want flag value %q", cfg.BaseURL, "http://flag.example/v1")
	}
	if !cfg.baseURLSet {
		t.Error("baseURLSet = false, want true for -provider-base-url")
	}
	if cfg.Token != "env-token" {
		t.Errorf("Token = %q, want environment value %q", cfg.Token, "env-token")
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
	if cfg.Dialect != "" || cfg.MaxOutputTokens != 0 {
		t.Errorf("Dialect = %q output = %d, want unresolved/zero", cfg.Dialect, cfg.MaxOutputTokens)
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

func TestConfigNewSessionResolvedValues(t *testing.T) {
	fake := startFakeModelsDev(t)
	env := map[string]string{"AHARNESS_MODELS_DEV_URL": fake.URL()}
	cases := []struct {
		model       string
		dialect     model.Dialect
		context     int
		inputLimit  int
		inputCap    int
		outputLimit int
	}{
		{model: "deepseek-v4.1-flash", dialect: model.DialectChat, context: 1_000_000, inputCap: 950_000, outputLimit: 384_000},
		{model: "gpt-6-luna", dialect: model.DialectResponses, context: 1_050_000, inputLimit: 922_000, inputCap: 922_000, outputLimit: 128_000},
		{model: "kimi-k2.7-code", dialect: model.DialectChat, context: 262_144, inputCap: 249_037, outputLimit: 262_144},
		{model: "qwen3.7-plus", dialect: model.DialectChat, context: 131_072, inputCap: 124_519, outputLimit: 8_192},
		{model: "crouton-alpha-free", dialect: model.DialectResponses, context: 200_000, inputCap: 190_000, outputLimit: 8_192},
		{model: "old-deprecated", dialect: model.DialectChat, context: 100_000, inputCap: 95_000, outputLimit: 8_192},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			args := cfgTestArgs(t)
			args = append(args, "-model", tc.model)
			cfg := cfgTestParseOK(t, args, env)
			cfg, stderr := cfgTestResolve(t, cfg, fake)

			if cfg.Model != tc.model {
				t.Errorf("Model = %q, want %q", cfg.Model, tc.model)
			}
			if cfg.Dialect != tc.dialect {
				t.Errorf("Dialect = %q, want %q", cfg.Dialect, tc.dialect)
			}
			if cfg.BaseURL != testCatalogBaseURL {
				t.Errorf("BaseURL = %q, want discovered %q", cfg.BaseURL, testCatalogBaseURL)
			}
			if cfg.MaxContextTokens != tc.context {
				t.Errorf("MaxContextTokens = %d, want %d", cfg.MaxContextTokens, tc.context)
			}
			if cfg.InputLimit != tc.inputLimit {
				t.Errorf("InputLimit = %d, want %d", cfg.InputLimit, tc.inputLimit)
			}
			// The advertised output ceiling is informational only.
			if cfg.MaxOutputTokens != 0 || cfg.Limits.MaxOutputTokens != 0 {
				t.Errorf("output budget = %d/%d, want 0 (no harness cap)", cfg.MaxOutputTokens, cfg.Limits.MaxOutputTokens)
			}
			wantMargin := agent.SafetyMargin(tc.context)
			if cfg.Limits.SafetyMargin != wantMargin {
				t.Errorf("SafetyMargin = %d, want %d", cfg.Limits.SafetyMargin, wantMargin)
			}
			if cfg.Limits.MaxInputTokens != tc.inputCap {
				t.Errorf("MaxInputTokens = %d, want %d", cfg.Limits.MaxInputTokens, tc.inputCap)
			}
			if cfg.Limits.CompactionTarget != tc.inputCap {
				t.Errorf("CompactionTarget = %d, want %d", cfg.Limits.CompactionTarget, tc.inputCap)
			}
			if tc.model == "old-deprecated" {
				if !strings.Contains(stderr, "deprecated") {
					t.Errorf("stderr = %q, want a deprecated model warning", stderr)
				}
			} else if strings.Contains(stderr, "deprecated") {
				t.Errorf("unexpected deprecated warning: %q", stderr)
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

func TestConfigUnknownModelResolvesAsConfigError(t *testing.T) {
	fake := startFakeModelsDev(t)
	cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-model", "unknown-model"), nil)
	err := cfgTestResolveErr(t, cfg, fake)
	if !errors.Is(err, model.ErrModelNotFound) || !strings.Contains(err.Error(), "unknown-model") {
		t.Errorf("error = %q, want a catalog model-not-found error naming the model", err)
	}
	if got := catalogExitCode(err); got != exitConfig {
		t.Errorf("catalogExitCode = %d, want %d", got, exitConfig)
	}
}

func TestConfigIneligibleModelsResolveAsConfigErrors(t *testing.T) {
	fake := startFakeModelsDev(t)
	cases := []struct {
		model string
		want  string
	}{
		{model: "no-tools", want: "tool calls are not supported"},
		{model: "no-context", want: "missing context limit"},
		{model: "exotic-sdk", want: "unsupported AI SDK package"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-model", tc.model), nil)
			err := cfgTestResolveErr(t, cfg, fake)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err, tc.want)
			}
			if got := catalogExitCode(err); got != exitConfig {
				t.Errorf("catalogExitCode = %d, want %d", got, exitConfig)
			}
		})
	}
}

func TestConfigExplicitContext(t *testing.T) {
	fake := startFakeModelsDev(t)

	t.Run("flag below window", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-model", "kimi-k2.7-code", "-max-context-tokens", "100000"), nil)
		cfg, stderr := cfgTestResolve(t, cfg, fake)
		if cfg.MaxContextTokens != 100_000 {
			t.Fatalf("MaxContextTokens = %d, want explicit 100000", cfg.MaxContextTokens)
		}
		if cfg.Limits.SafetyMargin != 5_000 || cfg.Limits.MaxInputTokens != 95_000 {
			t.Fatalf("limits = %+v, want margin 5000 input 95000", cfg.Limits)
		}
		if strings.Contains(stderr, "clamped") {
			t.Errorf("unexpected clamp warning: %q", stderr)
		}
	})

	t.Run("environment below window", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-model", "kimi-k2.7-code"), map[string]string{
			"AHARNESS_MODELS_DEV_URL":     fake.URL(),
			"AHARNESS_MAX_CONTEXT_TOKENS": "100000",
		})
		cfg, _ = cfgTestResolve(t, cfg, fake)
		if cfg.MaxContextTokens != 100_000 || cfg.Limits.MaxInputTokens != 95_000 {
			t.Fatalf("context/input = %d/%d, want 100000/95000", cfg.MaxContextTokens, cfg.Limits.MaxInputTokens)
		}
	})

	t.Run("above window clamps and warns", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-model", "kimi-k2.7-code", "-max-context-tokens", "999999"), nil)
		cfg, stderr := cfgTestResolve(t, cfg, fake)
		if cfg.MaxContextTokens != 262_144 {
			t.Fatalf("MaxContextTokens = %d, want the model window 262144", cfg.MaxContextTokens)
		}
		if !strings.Contains(stderr, "clamped") || !strings.Contains(stderr, "262144") {
			t.Fatalf("stderr = %q, want a clamp warning naming the window", stderr)
		}
		if cfg.Limits.SafetyMargin != 13_107 || cfg.Limits.MaxInputTokens != 249_037 {
			t.Fatalf("limits = %+v, want margin 13107 input 249037", cfg.Limits)
		}
	})

	t.Run("equal to window does not warn", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-model", "kimi-k2.7-code", "-max-context-tokens", "262144"), nil)
		_, stderr := cfgTestResolve(t, cfg, fake)
		if strings.Contains(stderr, "clamped") {
			t.Fatalf("unexpected clamp warning: %q", stderr)
		}
	})
}

func TestConfigOutputBudget(t *testing.T) {
	fake := startFakeModelsDev(t)

	t.Run("unset omits the budget", func(t *testing.T) {
		cfg := cfgTestParseOK(t, cfgTestArgs(t), nil)
		cfg, _ = cfgTestResolve(t, cfg, fake)
		if cfg.MaxOutputTokens != 0 || cfg.Limits.MaxOutputTokens != 0 {
			t.Fatalf("output budget = %d/%d, want 0", cfg.MaxOutputTokens, cfg.Limits.MaxOutputTokens)
		}
		if cfg.Limits.MaxInputTokens != 950_000 {
			t.Fatalf("MaxInputTokens = %d, want 950000 without output subtraction", cfg.Limits.MaxInputTokens)
		}
	})

	t.Run("explicit positive passes through unchanged", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-max-output-tokens", "1234"), nil)
		cfg, _ = cfgTestResolve(t, cfg, fake)
		if cfg.MaxOutputTokens != 1234 || cfg.Limits.MaxOutputTokens != 1234 {
			t.Fatalf("output budget = %d/%d, want 1234", cfg.MaxOutputTokens, cfg.Limits.MaxOutputTokens)
		}
		// The catalog output ceiling (384000) never clamps an explicit value,
		// and the output budget is not subtracted from the input cap.
		if cfg.Limits.MaxInputTokens != 950_000 {
			t.Fatalf("MaxInputTokens = %d, want 950000", cfg.Limits.MaxInputTokens)
		}
	})

	t.Run("explicit zero is allowed", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-max-output-tokens", "0"), nil)
		cfg, _ = cfgTestResolve(t, cfg, fake)
		if cfg.MaxOutputTokens != 0 || !cfg.maxOutputSet {
			t.Fatalf("output = %d explicit = %t, want 0 and true", cfg.MaxOutputTokens, cfg.maxOutputSet)
		}
		if cfg.Limits.MaxInputTokens != 950_000 {
			t.Fatalf("MaxInputTokens = %d, want 950000", cfg.Limits.MaxInputTokens)
		}
	})

	t.Run("negative rejected at parse", func(t *testing.T) {
		err := cfgTestParseErr(t, append(cfgTestArgs(t), "-max-output-tokens", "-1"), nil)
		if !strings.Contains(err.Error(), "must not be negative") {
			t.Fatalf("error = %q, want negative-output rejection", err)
		}
	})
}

func TestConfigProviderSelection(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		cfg := cfgTestParseOK(t, cfgTestBaseArgs(t), nil)
		if cfg.Provider != providerName || cfg.providerSet {
			t.Fatalf("provider = %q explicit = %t", cfg.Provider, cfg.providerSet)
		}
	})
	t.Run("flag", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(cfgTestBaseArgs(t), "-provider", providerName), nil)
		if cfg.Provider != providerName || !cfg.providerSet {
			t.Fatalf("provider = %q explicit = %t", cfg.Provider, cfg.providerSet)
		}
	})
	t.Run("environment", func(t *testing.T) {
		cfg := cfgTestParseOK(t, cfgTestBaseArgs(t), map[string]string{"AHARNESS_PROVIDER": providerName})
		if cfg.Provider != providerName || !cfg.providerSet {
			t.Fatalf("provider = %q explicit = %t", cfg.Provider, cfg.providerSet)
		}
	})
	t.Run("unknown flag", func(t *testing.T) {
		err := cfgTestParseErr(t, append(cfgTestBaseArgs(t), "-provider", "other"), nil)
		if !strings.Contains(err.Error(), "unsupported provider") || !strings.Contains(err.Error(), providerName) {
			t.Fatalf("error = %q, want supported provider list", err)
		}
	})
	t.Run("unknown environment", func(t *testing.T) {
		err := cfgTestParseErr(t, cfgTestBaseArgs(t), map[string]string{"AHARNESS_PROVIDER": "other"})
		if !strings.Contains(err.Error(), "unsupported provider") {
			t.Fatalf("error = %q, want unsupported provider", err)
		}
	})
	t.Run("flag overrides unknown environment", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(cfgTestBaseArgs(t), "-provider", providerName), map[string]string{"AHARNESS_PROVIDER": "other"})
		if cfg.Provider != providerName {
			t.Fatalf("provider = %q, want %q", cfg.Provider, providerName)
		}
	})
}

func TestConfigTerminalActions(t *testing.T) {
	stateDir := t.TempDir()
	env := map[string]string{
		"AHARNESS_STATE_DIR":      stateDir,
		"AHARNESS_MODELS_DEV_URL": "http://127.0.0.1:9/api.json",
	}

	t.Run("list models ignores session inputs", func(t *testing.T) {
		args := []string{
			"-list-models",
			"-batch",
			"-task-file", filepath.Join(t.TempDir(), "missing.md"),
			"-resume", "not-a-session",
			"-model", "unknown-model",
			"-max-context-tokens", "0",
			"-provider", providerName,
			"-state-dir", stateDir,
		}
		cfg := cfgTestParseOK(t, args, env)
		if !cfg.ListModels || cfg.RefreshModels {
			t.Fatalf("terminal flags = refresh %t list %t", cfg.RefreshModels, cfg.ListModels)
		}
		if cfg.TaskSet || cfg.Model != "" {
			t.Fatalf("terminal action should ignore task/model flags: %+v", cfg)
		}
		if cfg.StateDir != stateDir || cfg.Provider != providerName {
			t.Fatalf("terminal action state/provider = %q/%q", cfg.StateDir, cfg.Provider)
		}
		if cfg.ModelsDevURL != "http://127.0.0.1:9/api.json" {
			t.Fatalf("ModelsDevURL = %q", cfg.ModelsDevURL)
		}
	})

	t.Run("refresh models ignores session inputs", func(t *testing.T) {
		args := []string{
			"-refresh-models",
			"-task", "ignored",
			"-model", "unknown-model",
			"-ui-json",
			"-ui-size", "not-a-size",
			"-state-dir", stateDir,
		}
		cfg := cfgTestParseOK(t, args, env)
		if !cfg.RefreshModels || cfg.ListModels {
			t.Fatalf("terminal flags = refresh %t list %t", cfg.RefreshModels, cfg.ListModels)
		}
		if cfg.TaskSet || cfg.Model != "" || cfg.UIJSON || cfg.Batch {
			t.Fatalf("terminal action retained session or mode flags: %+v", cfg)
		}
		if cfg.StateDir != stateDir {
			t.Fatalf("StateDir = %q", cfg.StateDir)
		}
	})

	t.Run("refresh then list", func(t *testing.T) {
		cfg := cfgTestParseOK(t, []string{"-refresh-models", "-list-models", "-state-dir", stateDir}, nil)
		if !cfg.RefreshModels || !cfg.ListModels {
			t.Fatalf("terminal flags = refresh %t list %t", cfg.RefreshModels, cfg.ListModels)
		}
	})

	t.Run("failed terminal action errors do not block parse", func(t *testing.T) {
		// -provider is still validated even for terminal actions.
		err := cfgTestParseErr(t, []string{"-refresh-models", "-provider", "other"}, nil)
		if !strings.Contains(err.Error(), "unsupported provider") {
			t.Fatalf("error = %q", err)
		}
	})
}

func TestConfigVersionPrecedence(t *testing.T) {
	cfg, err := parseConfig(
		[]string{"-version", "-list-models", "-refresh-models", "-provider", "other"},
		cfgTestGetenv(map[string]string{"AHARNESS_MAX_CONTEXT_TOKENS": "many"}),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !cfg.ShowVersion || cfg.ListModels || cfg.RefreshModels {
		t.Fatalf("version config = %+v", cfg)
	}

	cfg, err = parseConfig([]string{"-version=false", "-list-models", "-state-dir", t.TempDir()}, cfgTestGetenv(nil), io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ShowVersion || !cfg.ListModels {
		t.Fatalf("-version=false config = %+v", cfg)
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
		{"max turns negative", "-max-turns=-1", "max turns must not be negative"},
		{"max total tokens negative", "-max-total-tokens=-1", "max total tokens must not be negative"},
		{"max context tokens zero", "-max-context-tokens=0", "max context tokens"},
		{"max context tokens negative", "-max-context-tokens=-1", "max context tokens"},
		{"max output tokens negative", "-max-output-tokens=-1", "max output tokens"},
		{"session timeout negative", "-session-timeout=-1s", "session timeout must not be negative"},
		{"model timeout negative", "-model-timeout=-1s", "model timeout must not be negative"},
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

func TestConfigRunBudgets(t *testing.T) {
	t.Run("explicit zero is unlimited", func(t *testing.T) {
		cfg := cfgTestParseOK(t, cfgTestArgs(t,
			"-max-turns=0", "-max-total-tokens=0", "-session-timeout=0s", "-model-timeout=0s"), nil)
		if cfg.MaxTurns != 0 || cfg.MaxTotalTokens != 0 || cfg.SessionTimeout != 0 || cfg.ModelTimeout != 0 {
			t.Fatalf("budgets = %d/%d/%s/%s, want all zero",
				cfg.MaxTurns, cfg.MaxTotalTokens, cfg.SessionTimeout, cfg.ModelTimeout)
		}
		if !cfg.maxTurnsSet || !cfg.maxTotalTokensSet || !cfg.sessionTimeoutSet || !cfg.modelTimeoutSet {
			t.Fatalf("explicit budgets = turns %t tokens %t session %t model %t, want all true",
				cfg.maxTurnsSet, cfg.maxTotalTokensSet, cfg.sessionTimeoutSet, cfg.modelTimeoutSet)
		}
	})

	t.Run("explicit positive values", func(t *testing.T) {
		cfg := cfgTestParseOK(t, cfgTestArgs(t,
			"-max-turns=7", "-max-total-tokens=123456", "-session-timeout=2m", "-model-timeout=1500ms"), nil)
		if cfg.MaxTurns != 7 || cfg.MaxTotalTokens != 123456 {
			t.Fatalf("budgets = %d/%d, want 7/123456", cfg.MaxTurns, cfg.MaxTotalTokens)
		}
		if cfg.SessionTimeout != 2*time.Minute || cfg.ModelTimeout != 1500*time.Millisecond {
			t.Fatalf("timeouts = %s/%s, want 2m0s/1.5s", cfg.SessionTimeout, cfg.ModelTimeout)
		}
	})
}

func TestConfigLimitBoundary(t *testing.T) {
	fake := startFakeModelsDev(t)

	t.Run("one input token remains", func(t *testing.T) {
		cfg := cfgTestParseOK(t, cfgTestArgs(t, "-max-context-tokens=1001"), nil)
		cfg, _ = cfgTestResolve(t, cfg, fake)
		if cfg.Limits.SafetyMargin != 1000 {
			t.Fatalf("SafetyMargin = %d, want 1000", cfg.Limits.SafetyMargin)
		}
		if cfg.Limits.MaxInputTokens != 1 || cfg.Limits.CompactionTarget != 1 {
			t.Fatalf("input/compaction = %d/%d, want 1/1", cfg.Limits.MaxInputTokens, cfg.Limits.CompactionTarget)
		}
	})

	t.Run("margin consumes the window", func(t *testing.T) {
		cfg := cfgTestParseOK(t, cfgTestArgs(t, "-max-context-tokens=1000"), nil)
		err := cfgTestResolveErr(t, cfg, fake)
		if !strings.Contains(err.Error(), "max input tokens must be positive") {
			t.Fatalf("error = %q", err)
		}
	})

	t.Run("output budget is not subtracted", func(t *testing.T) {
		cfg := cfgTestParseOK(t, cfgTestArgs(t, "-max-context-tokens=9192", "-max-output-tokens=5000"), nil)
		cfg, _ = cfgTestResolve(t, cfg, fake)
		if cfg.Limits.SafetyMargin != 1000 || cfg.Limits.MaxInputTokens != 8192 {
			t.Fatalf("limits = %+v, want margin 1000 input 8192", cfg.Limits)
		}
	})
}

func TestConfigSafetyMargin(t *testing.T) {
	fake := startFakeModelsDev(t)
	for _, contextTokens := range []int{10_000, 20_000, 40_000, 100_000, 200_000, 250_000} {
		t.Run(strconv.Itoa(contextTokens), func(t *testing.T) {
			args := cfgTestArgs(t, "-max-context-tokens="+strconv.Itoa(contextTokens), "-max-output-tokens=500")
			cfg := cfgTestParseOK(t, args, nil)
			cfg, _ = cfgTestResolve(t, cfg, fake)
			wantMargin := agent.SafetyMargin(contextTokens)
			if cfg.Limits.SafetyMargin != wantMargin {
				t.Errorf("Limits.SafetyMargin = %d, want %d", cfg.Limits.SafetyMargin, wantMargin)
			}
			wantInput := contextTokens - wantMargin
			if cfg.Limits.MaxInputTokens != wantInput {
				t.Errorf("Limits.MaxInputTokens = %d, want %d", cfg.Limits.MaxInputTokens, wantInput)
			}
			if cfg.Limits.CompactionTarget != wantInput {
				t.Errorf("Limits.CompactionTarget = %d, want %d", cfg.Limits.CompactionTarget, wantInput)
			}
		})
	}

	cfg := cfgTestParseOK(t, cfgTestArgs(t, "-max-context-tokens=10000"), nil)
	cfg, _ = cfgTestResolve(t, cfg, fake)
	if cfg.Limits.SafetyMargin != 1000 {
		t.Errorf("small-context safety margin = %d, want 1000 floor", cfg.Limits.SafetyMargin)
	}
}

func TestConfigBaseURL(t *testing.T) {
	fake := startFakeModelsDev(t)
	common := []string{
		"-task", "task",
		"-model", "deepseek-v4.1-flash",
		"-workspace", t.TempDir(),
		"-state-dir", t.TempDir(),
	}

	t.Run("parse leaves it unset", func(t *testing.T) {
		cfg := cfgTestParseOK(t, common, nil)
		if cfg.BaseURL != "" {
			t.Errorf("BaseURL = %q, want empty before discovery", cfg.BaseURL)
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

	t.Run("loopback accepted", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(append([]string{}, common...), "-provider-base-url", "http://127.0.0.1:1234/v1"), nil)
		if cfg.BaseURL != "http://127.0.0.1:1234/v1" {
			t.Errorf("BaseURL = %q", cfg.BaseURL)
		}
	})

	t.Run("explicit wins over discovery", func(t *testing.T) {
		args := append(append([]string{}, common...), "-provider-base-url", "http://127.0.0.1:1234/v1")
		cfg := cfgTestParseOK(t, args, nil)
		cfg, _ = cfgTestResolve(t, cfg, fake)
		if cfg.BaseURL != "http://127.0.0.1:1234/v1" {
			t.Errorf("BaseURL = %q, want the explicit loopback override", cfg.BaseURL)
		}
	})

	for _, raw := range []string{
		"ftp://example.com/v1",
		"http:///v1",
		"http://user:pass@example.com/v1",
		"http://example.com/v1?token=x",
		"http://example.com/v1#frag",
	} {
		t.Run("invalid "+raw, func(t *testing.T) {
			err := cfgTestParseErr(t, append(append([]string{}, common...), "-provider-base-url", raw), nil)
			if !strings.Contains(err.Error(), "provider base URL") {
				t.Errorf("error = %q, want base URL error", err)
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

func TestCatalogExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{model.ErrCatalogUnavailable, exitProvider},
		{model.ErrCatalogInvalid, exitProtocol},
		{model.ErrCatalogCache, exitConfig},
		{model.ErrModelNotFound, exitConfig},
		{errors.New("other"), exitConfig},
	}
	for _, tc := range cases {
		if got := catalogExitCode(tc.err); got != tc.want {
			t.Errorf("catalogExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
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

func TestConfigRequireUsage(t *testing.T) {
	cfg := cfgTestParseOK(t, cfgTestArgs(t), nil)
	if cfg.RequireUsage {
		t.Error("RequireUsage default = true, want false")
	}
	cfg = cfgTestParseOK(t, cfgTestArgs(t, "-require-usage"), nil)
	if !cfg.RequireUsage {
		t.Error("RequireUsage with -require-usage = false, want true")
	}
}
