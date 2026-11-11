package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/session"
	"aharness/internal/tools"
)

const v1TestSession = "0190a0c4-0000-7000-8000-0000000000aa"

// v1OldBaseURL is the base URL recorded by sessions created before
// discovery. Resume must keep it, not rewrite it to the documented OpenAI
// base.
const v1OldBaseURL = "https://opencode.ai/inference/go/v1"

func TestParseResumeDeferredValidation(t *testing.T) {
	stateDir := t.TempDir()
	cfg := cfgTestParseOK(t, []string{"-resume", v1TestSession, "-state-dir", stateDir}, nil)
	if cfg.ResumeID != v1TestSession {
		t.Fatalf("ResumeID = %q", cfg.ResumeID)
	}
	if cfg.TaskSet || cfg.Model != "" || cfg.Workspace != "" {
		t.Fatalf("resume config should defer task/model/workspace: %+v", cfg)
	}
	if cfg.Dialect != "" || cfg.BaseURL != "" || cfg.MaxContextTokens != 0 || cfg.MaxOutputTokens != 0 {
		t.Fatalf("resume config should defer catalog values: %+v", cfg)
	}
	if !cfg.Stream {
		t.Fatal("TUI mode should stream")
	}
	if cfg.Mode != ModeTUI {
		t.Fatalf("Mode = %v, want TUI", cfg.Mode)
	}

	cfg = cfgTestParseOK(t, []string{"-resume", v1TestSession, "-state-dir", stateDir, "-stream"}, nil)
	if !cfg.Stream {
		t.Fatal("-stream should be set")
	}
	if _, err := parseConfig([]string{"-resume", v1TestSession, "-state-dir", stateDir, "-stream=false"}, cfgTestGetenv(nil), io.Discard); err == nil {
		t.Fatal("-stream=false accepted in TUI mode")
	}
	batch := cfgTestParseOK(t, []string{"-batch", "-resume", v1TestSession, "-state-dir", stateDir}, nil)
	if batch.Mode != ModeBatch || batch.Stream {
		t.Fatalf("batch config = %+v", batch)
	}
}

func TestParseResumeAndForkValidation(t *testing.T) {
	stateDir := t.TempDir()
	if err := cfgTestParseErr(t, []string{"-resume", v1TestSession, "-fork", v1TestSession, "-state-dir", stateDir}, nil); !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v", err)
	}
	if err := cfgTestParseErr(t, []string{"-resume", "../escape", "-state-dir", stateDir}, nil); err == nil {
		t.Fatal("invalid resume id accepted")
	}
	if err := cfgTestParseErr(t, []string{"-batch", "-fork", v1TestSession, "-state-dir", stateDir}, nil); err == nil {
		t.Fatal("batch fork without a task accepted")
	}
	cfg := cfgTestParseOK(t, []string{"-fork", v1TestSession, "-state-dir", stateDir}, nil)
	if cfg.ForkID != v1TestSession || cfg.TaskSet || cfg.Mode != ModeTUI {
		t.Fatalf("TUI fork config = %+v", cfg)
	}
	cfg = cfgTestParseOK(t, []string{"-fork", v1TestSession, "-state-dir", stateDir, "-task", "new work"}, nil)
	if cfg.ForkID != v1TestSession || !cfg.TaskSet || cfg.Task != "new work" {
		t.Fatalf("fork config = %+v", cfg)
	}
}

func TestParseStreamWithoutTaskRequiresResume(t *testing.T) {
	stateDir := t.TempDir()
	args := []string{"-batch", "-stream", "-state-dir", stateDir, "-model", "deepseek-v4.1-flash", "-workspace", t.TempDir()}
	if err := cfgTestParseErr(t, args, nil); err == nil {
		t.Fatal("batch stream without a task accepted for a new session")
	}
	args = append(args, "-task", "work")
	cfg := cfgTestParseOK(t, args, nil)
	if !cfg.Stream {
		t.Fatal("Stream = false, want true")
	}
	tuiCfg := cfgTestParseOK(t, []string{"-state-dir", stateDir, "-model", "deepseek-v4.1-flash", "-workspace", t.TempDir()}, nil)
	if !tuiCfg.Stream || tuiCfg.Mode != ModeTUI {
		t.Fatalf("TUI new session without a task = %+v", tuiCfg)
	}
}

func TestParseBaseURLRejectsUnsafeComponents(t *testing.T) {
	for _, raw := range []string{
		"http://user:pass@example.test/v1",
		"http://example.test/v1?token=x",
		"http://example.test/v1#frag",
	} {
		t.Run(raw, func(t *testing.T) {
			args := append(cfgTestBaseArgs(t), "-provider-base-url", raw)
			if err := cfgTestParseErr(t, args, nil); err == nil {
				t.Fatalf("base URL %q accepted", raw)
			}
		})
	}
}

// v1State is the recorded session state of a pre-discovery session: the old
// base URL, the 200,000 context window, and the old 8,192 output budget.
func v1State() agent.State {
	limits := agent.Limits{
		MaxContextTokens: 200_000,
		MaxOutputTokens:  8_192,
		SafetyMargin:     10_000,
		MaxInputTokens:   181_808,
		CompactionTarget: 181_808,
	}
	return agent.State{
		Workspace:      "/tmp/recorded-workspace",
		StateDir:       "/tmp/recorded-state",
		Provider:       providerName,
		PromptVersion:  agent.PromptVersion,
		ToolsetVersion: tools.Version,
		InitialModel:   "deepseek-v4.1-flash",
		InitialDialect: string(model.DialectChat),
		Model:          "deepseek-v4.1-flash",
		Dialect:        string(model.DialectChat),
		BaseURL:        v1OldBaseURL,
		Limits:         limits,
		MaxTurns:       77,
		MaxTotalTokens: 999_999,
		SessionTimeout: 13 * time.Minute,
		ModelTimeout:   42 * time.Second,
		Turns:          3,
		Usage:          model.Usage{InputTokens: 10, OutputTokens: 5},
	}
}

func v1ResumeConfig(t *testing.T, args ...string) config {
	t.Helper()
	base := []string{"-resume", v1TestSession, "-state-dir", t.TempDir()}
	return cfgTestParseOK(t, append(base, args...), nil)
}

func TestApplySessionDefaultsInheritsOffline(t *testing.T) {
	cfg := v1ResumeConfig(t)
	state := v1State()
	// A nil source proves inheritance does not consult the catalog.
	var stderr bytes.Buffer
	if err := applySessionDefaults(context.Background(), &cfg, state, nil, &stderr); err != nil {
		t.Fatalf("applySessionDefaults: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no warnings", stderr.String())
	}
	if cfg.Model != state.Model || cfg.Dialect != model.DialectChat {
		t.Fatalf("model = %q dialect = %q", cfg.Model, cfg.Dialect)
	}
	if cfg.Workspace != state.Workspace {
		t.Fatalf("workspace = %q, want %q", cfg.Workspace, state.Workspace)
	}
	if cfg.BaseURL != v1OldBaseURL {
		t.Fatalf("base URL = %q, want the recorded old base %q", cfg.BaseURL, v1OldBaseURL)
	}
	if cfg.MaxTurns != state.MaxTurns || cfg.MaxTotalTokens != state.MaxTotalTokens {
		t.Fatalf("budgets = %d/%d", cfg.MaxTurns, cfg.MaxTotalTokens)
	}
	if cfg.MaxContextTokens != 200_000 {
		t.Fatalf("MaxContextTokens = %d, want 200000", cfg.MaxContextTokens)
	}
	if cfg.MaxOutputTokens != 8_192 || cfg.Limits.MaxOutputTokens != 8_192 {
		t.Fatalf("output budget = %d/%d, want recorded 8192", cfg.MaxOutputTokens, cfg.Limits.MaxOutputTokens)
	}
	// The recorded input cap already includes the old margin and output
	// subtraction; it is inherited as-is when it is below context-margin.
	if cfg.Limits.MaxInputTokens != 181_808 || cfg.Limits.CompactionTarget != 181_808 {
		t.Fatalf("input limits = %+v, want recorded 181808", cfg.Limits)
	}
	wantMargin := agent.SafetyMargin(200_000)
	if cfg.Limits.SafetyMargin != wantMargin {
		t.Fatalf("safety margin = %d, want %d", cfg.Limits.SafetyMargin, wantMargin)
	}
	if cfg.SessionTimeout != state.SessionTimeout || cfg.ModelTimeout != state.ModelTimeout {
		t.Fatalf("timeouts = %s/%s", cfg.SessionTimeout, cfg.ModelTimeout)
	}
	if cfg.PromptVersion != agent.PromptVersion || cfg.ToolsetVersion != tools.Version {
		t.Fatalf("versions = %q/%q", cfg.PromptVersion, cfg.ToolsetVersion)
	}
}

func TestApplySessionDefaultsExplicitContextClamp(t *testing.T) {
	cfg := v1ResumeConfig(t, "-max-context-tokens", "150000")
	state := v1State()
	if err := applySessionDefaults(context.Background(), &cfg, state, nil, io.Discard); err != nil {
		t.Fatalf("applySessionDefaults: %v", err)
	}
	if cfg.MaxContextTokens != 150_000 {
		t.Fatalf("MaxContextTokens = %d, want explicit 150000", cfg.MaxContextTokens)
	}
	// The recorded input cap is 181808; the smaller margin-adjusted window is
	// 142500, so the effective input cap follows the explicit window.
	if cfg.Limits.SafetyMargin != 7_500 || cfg.Limits.MaxInputTokens != 142_500 {
		t.Fatalf("limits = %+v, want margin 7500 input 142500", cfg.Limits)
	}
	if cfg.MaxOutputTokens != 8_192 {
		t.Fatalf("output budget = %d, want inherited 8192", cfg.MaxOutputTokens)
	}
}

func TestApplySessionDefaultsContextCannotExceedRecord(t *testing.T) {
	cfg := v1ResumeConfig(t, "-max-context-tokens", "400000")
	state := v1State()
	if err := applySessionDefaults(context.Background(), &cfg, state, nil, io.Discard); err != nil {
		t.Fatalf("applySessionDefaults: %v", err)
	}
	if cfg.MaxContextTokens != 200_000 {
		t.Fatalf("MaxContextTokens = %d, want the recorded 200000 replay window", cfg.MaxContextTokens)
	}
	if cfg.Limits.MaxInputTokens != 181_808 {
		t.Fatalf("MaxInputTokens = %d, want recorded 181808", cfg.Limits.MaxInputTokens)
	}
}

func TestApplySessionDefaultsOutputOverrides(t *testing.T) {
	t.Run("explicit zero clears", func(t *testing.T) {
		cfg := v1ResumeConfig(t, "-max-output-tokens=0")
		state := v1State()
		if err := applySessionDefaults(context.Background(), &cfg, state, nil, io.Discard); err != nil {
			t.Fatalf("applySessionDefaults: %v", err)
		}
		if cfg.MaxOutputTokens != 0 || cfg.Limits.MaxOutputTokens != 0 {
			t.Fatalf("output budget = %d/%d, want cleared 0", cfg.MaxOutputTokens, cfg.Limits.MaxOutputTokens)
		}
	})
	t.Run("explicit positive replaces", func(t *testing.T) {
		cfg := v1ResumeConfig(t, "-max-output-tokens=4096")
		state := v1State()
		if err := applySessionDefaults(context.Background(), &cfg, state, nil, io.Discard); err != nil {
			t.Fatalf("applySessionDefaults: %v", err)
		}
		if cfg.MaxOutputTokens != 4096 || cfg.Limits.MaxOutputTokens != 4096 {
			t.Fatalf("output budget = %d/%d, want 4096", cfg.MaxOutputTokens, cfg.Limits.MaxOutputTokens)
		}
	})
}

func TestApplySessionDefaultsExplicitModelChange(t *testing.T) {
	fake := startFakeModelsDev(t)

	t.Run("catalog values win", func(t *testing.T) {
		cfg := v1ResumeConfig(t, "-model", "gpt-6-luna")
		cfg.ModelsDevURL = fake.URL()
		source, err := newCatalogSource(cfg)
		if err != nil {
			t.Fatal(err)
		}
		state := v1State()
		if err := applySessionDefaults(context.Background(), &cfg, state, source, io.Discard); err != nil {
			t.Fatalf("applySessionDefaults: %v", err)
		}
		if cfg.Model != "gpt-6-luna" || cfg.Dialect != model.DialectResponses {
			t.Fatalf("model/dialect = %q/%q", cfg.Model, cfg.Dialect)
		}
		if cfg.BaseURL != testCatalogBaseURL {
			t.Fatalf("BaseURL = %q, want discovered %q", cfg.BaseURL, testCatalogBaseURL)
		}
		if cfg.MaxContextTokens != 1_050_000 || cfg.InputLimit != 922_000 {
			t.Fatalf("context/input limit = %d/%d", cfg.MaxContextTokens, cfg.InputLimit)
		}
		if cfg.Limits.MaxInputTokens != 922_000 {
			t.Fatalf("MaxInputTokens = %d, want 922000", cfg.Limits.MaxInputTokens)
		}
		// Output is still inherited: only the model changed.
		if cfg.MaxOutputTokens != 8_192 {
			t.Fatalf("output budget = %d, want inherited 8192", cfg.MaxOutputTokens)
		}
	})

	t.Run("translated model uses chat", func(t *testing.T) {
		cfg := v1ResumeConfig(t, "-model", "qwen3.7-plus")
		cfg.ModelsDevURL = fake.URL()
		source, err := newCatalogSource(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := applySessionDefaults(context.Background(), &cfg, v1State(), source, io.Discard); err != nil {
			t.Fatalf("applySessionDefaults: %v", err)
		}
		if cfg.Dialect != model.DialectChat || cfg.MaxContextTokens != 131_072 {
			t.Fatalf("dialect/context = %q/%d, want translated chat 131072", cfg.Dialect, cfg.MaxContextTokens)
		}
	})

	t.Run("explicit base URL wins", func(t *testing.T) {
		cfg := v1ResumeConfig(t, "-model", "gpt-6-luna", "-provider-base-url", "http://127.0.0.1:4321/v1")
		cfg.ModelsDevURL = fake.URL()
		source, err := newCatalogSource(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := applySessionDefaults(context.Background(), &cfg, v1State(), source, io.Discard); err != nil {
			t.Fatalf("applySessionDefaults: %v", err)
		}
		if cfg.BaseURL != "http://127.0.0.1:4321/v1" {
			t.Fatalf("BaseURL = %q, want the explicit override", cfg.BaseURL)
		}
	})

	t.Run("model change requires a catalog", func(t *testing.T) {
		cfg := v1ResumeConfig(t, "-model", "gpt-6-luna")
		err := applySessionDefaults(context.Background(), &cfg, v1State(), nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "catalog is required") {
			t.Fatalf("error = %v, want a missing-catalog error", err)
		}
	})

	t.Run("unknown model is a config error", func(t *testing.T) {
		cfg := v1ResumeConfig(t, "-model", "unknown-model")
		cfg.ModelsDevURL = fake.URL()
		source, err := newCatalogSource(cfg)
		if err != nil {
			t.Fatal(err)
		}
		err = applySessionDefaults(context.Background(), &cfg, v1State(), source, io.Discard)
		if err == nil || !errors.Is(err, model.ErrModelNotFound) || !strings.Contains(err.Error(), "unknown-model") {
			t.Fatalf("error = %v, want model-not-found", err)
		}
		if got := catalogExitCode(err); got != exitConfig {
			t.Fatalf("catalogExitCode = %d, want %d", got, exitConfig)
		}
	})
}

func TestApplySessionDefaultsOverrides(t *testing.T) {
	fake := startFakeModelsDev(t)
	args := []string{
		"-resume", v1TestSession,
		"-state-dir", t.TempDir(),
		"-model", "crouton-alpha-free",
		"-workspace", t.TempDir(),
		"-provider-base-url", "http://override.example/v1",
	}
	args = append(args, "-max-turns", "200", "-max-total-tokens", "5000000", "-max-context-tokens", "120000", "-max-output-tokens", "4096", "-session-timeout", "2m")
	cfg := cfgTestParseOK(t, args, nil)
	cfg.ModelsDevURL = fake.URL()
	source, err := newCatalogSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := v1State()
	if err := applySessionDefaults(context.Background(), &cfg, state, source, io.Discard); err != nil {
		t.Fatalf("applySessionDefaults: %v", err)
	}
	if cfg.Model != "crouton-alpha-free" || cfg.Dialect != model.DialectResponses {
		t.Fatalf("model/dialect = %q/%q", cfg.Model, cfg.Dialect)
	}
	if cfg.BaseURL != "http://override.example/v1" {
		t.Fatalf("base URL = %q", cfg.BaseURL)
	}
	if cfg.MaxTurns != 200 || cfg.MaxTotalTokens != 5_000_000 {
		t.Fatalf("budgets = %d/%d", cfg.MaxTurns, cfg.MaxTotalTokens)
	}
	if cfg.MaxContextTokens != 120_000 || cfg.MaxOutputTokens != 4096 || cfg.SessionTimeout != 2*time.Minute {
		t.Fatalf("context/output/timeout = %d/%d/%s", cfg.MaxContextTokens, cfg.MaxOutputTokens, cfg.SessionTimeout)
	}
	if cfg.Limits.MaxInputTokens != 114_000 {
		t.Fatalf("MaxInputTokens = %d, want 114000", cfg.Limits.MaxInputTokens)
	}
	if !cfg.workspaceSet || cfg.Workspace == state.Workspace {
		t.Fatal("explicit workspace was not retained")
	}
}

func TestApplySessionDefaultsProviderMismatch(t *testing.T) {
	state := v1State()

	t.Run("explicit provider differs", func(t *testing.T) {
		cfg := config{Provider: "other", providerSet: true}
		err := applySessionDefaults(context.Background(), &cfg, state, nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "does not match the session provider") {
			t.Fatalf("error = %v, want a provider mismatch", err)
		}
	})

	t.Run("explicit provider matches", func(t *testing.T) {
		cfg := config{Provider: providerName, providerSet: true}
		if err := applySessionDefaults(context.Background(), &cfg, state, nil, io.Discard); err != nil {
			t.Fatalf("applySessionDefaults: %v", err)
		}
	})

	t.Run("recorded provider unsupported", func(t *testing.T) {
		state := v1State()
		state.Provider = "other"
		cfg := config{}
		err := applySessionDefaults(context.Background(), &cfg, state, nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "is not supported") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestApplySessionDefaultsRejectsUnsupportedState(t *testing.T) {
	base := v1ResumeConfig(t)
	tests := []struct {
		name   string
		mutate func(*agent.State)
		want   string
	}{
		{name: "provider", mutate: func(s *agent.State) { s.Provider = "other" }, want: "provider"},
		{name: "prompt", mutate: func(s *agent.State) { s.PromptVersion = "prompt-v9" }, want: "prompt version"},
		{name: "toolset", mutate: func(s *agent.State) { s.ToolsetVersion = "toolset-v9" }, want: "toolset version"},
		{name: "model", mutate: func(s *agent.State) { s.Model = ""; s.InitialModel = "" }, want: "model"},
		{name: "dialect", mutate: func(s *agent.State) { s.Dialect = "native"; s.InitialDialect = "" }, want: "dialect"},
		{name: "context", mutate: func(s *agent.State) { s.Limits.MaxContextTokens = 0 }, want: "context limit"},
		{name: "workspace", mutate: func(s *agent.State) { s.Workspace = "" }, want: "workspace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			state := v1State()
			tc.mutate(&state)
			err := applySessionDefaults(context.Background(), &cfg, state, nil, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestValidateSessionRules(t *testing.T) {
	makeCfg := func(t *testing.T, args ...string) config {
		t.Helper()
		base := []string{"-batch", "-resume", v1TestSession, "-state-dir", t.TempDir()}
		return cfgTestParseOK(t, append(base, args...), nil)
	}

	state := v1State()
	state.LastRunEnded = true
	state.LastRunStatus = agent.StopCompleted
	cfg := makeCfg(t)
	if err := validateSessionRules(&cfg, state); err == nil {
		t.Fatal("completed resume without a task accepted")
	}
	cfg = makeCfg(t, "-task", "continue")
	if err := validateSessionRules(&cfg, state); err != nil {
		t.Fatalf("completed resume with a task: %v", err)
	}

	state.LastRunStatus = agent.StopInterrupted
	cfg = makeCfg(t)
	if err := validateSessionRules(&cfg, state); err != nil {
		t.Fatalf("interrupted resume without a task: %v", err)
	}

	state = v1State()
	state.LastRunEnded = true
	state.LastRunStatus = agent.StopCompleted
	state.Turns = 77
	cfg = makeCfg(t, "-task", "continue")
	if err := validateSessionRules(&cfg, state); err == nil || !strings.Contains(err.Error(), "turn limit") {
		t.Fatalf("turn limit error = %v", err)
	}
	cfg = makeCfg(t, "-task", "continue", "-max-turns", "100")
	if err := validateSessionRules(&cfg, state); err != nil {
		t.Fatalf("raised turn limit: %v", err)
	}

	state = v1State()
	state.Usage = model.Usage{InputTokens: 2_000_000, OutputTokens: 0}
	cfg = makeCfg(t)
	if err := validateSessionRules(&cfg, state); err == nil || !strings.Contains(err.Error(), "token limit") {
		t.Fatalf("token limit error = %v", err)
	}
	cfg = makeCfg(t, "-max-total-tokens", "3000000")
	if err := validateSessionRules(&cfg, state); err != nil {
		t.Fatalf("raised token limit: %v", err)
	}
}

func TestSessionFailureExitCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "missing", err: os.ErrNotExist, want: exitConfig},
		{name: "locked", err: session.ErrLocked, want: exitConfig},
		{name: "unsupported version", err: session.ErrUnsupportedVersion, want: exitConfig},
		{name: "mode", err: session.ErrMode, want: exitConfig},
		{name: "invalid id", err: session.ErrInvalidID, want: exitConfig},
		{name: "corrupt", err: session.ErrCorrupt, want: exitSetup},
		{name: "other", err: errors.New("disk failure"), want: exitSetup},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if got := sessionFailure(tc.err, &stderr); got != tc.want {
				t.Fatalf("sessionFailure(%v) = %d, want %d", tc.err, got, tc.want)
			}
			if stderr.Len() == 0 {
				t.Fatal("sessionFailure wrote no diagnostic")
			}
		})
	}
}
