package main

import (
	"bytes"
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

func TestParseResumeDeferredValidation(t *testing.T) {
	stateDir := t.TempDir()
	cfg := cfgTestParseOK(t, []string{"-resume", v1TestSession, "-state-dir", stateDir}, nil)
	if cfg.ResumeID != v1TestSession {
		t.Fatalf("ResumeID = %q", cfg.ResumeID)
	}
	if cfg.TaskSet || cfg.Model != "" || cfg.Workspace != "" {
		t.Fatalf("resume config should defer task/model/workspace: %+v", cfg)
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

func v1State() agent.State {
	limits := agent.Limits{
		MaxContextTokens: 150_000,
		MaxOutputTokens:  2_000,
		SafetyMargin:     7_500,
		MaxInputTokens:   140_500,
		CompactionTarget: 140_500,
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
		BaseURL:        "http://recorded.example/v1",
		Limits:         limits,
		MaxTurns:       77,
		MaxTotalTokens: 999_999,
		SessionTimeout: 13 * time.Minute,
		ModelTimeout:   42 * time.Second,
		Turns:          3,
		Usage:          model.Usage{InputTokens: 10, OutputTokens: 5},
	}
}

func TestApplySessionDefaultsInherits(t *testing.T) {
	cfg, err := parseConfig([]string{"-resume", v1TestSession, "-state-dir", t.TempDir()}, cfgTestGetenv(nil), nil)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	state := v1State()
	if err := applySessionDefaults(&cfg, state); err != nil {
		t.Fatalf("applySessionDefaults: %v", err)
	}
	if cfg.Model != state.Model || cfg.Dialect != model.DialectChat {
		t.Fatalf("model = %q dialect = %q", cfg.Model, cfg.Dialect)
	}
	if cfg.Workspace != state.Workspace {
		t.Fatalf("workspace = %q, want %q", cfg.Workspace, state.Workspace)
	}
	if cfg.BaseURL != state.BaseURL {
		t.Fatalf("base URL = %q, want %q", cfg.BaseURL, state.BaseURL)
	}
	if cfg.MaxTurns != state.MaxTurns || cfg.MaxTotalTokens != state.MaxTotalTokens {
		t.Fatalf("budgets = %d/%d", cfg.MaxTurns, cfg.MaxTotalTokens)
	}
	if cfg.MaxContextTokens != state.Limits.MaxContextTokens || cfg.MaxOutputTokens != state.Limits.MaxOutputTokens {
		t.Fatalf("context/output = %d/%d", cfg.MaxContextTokens, cfg.MaxOutputTokens)
	}
	if cfg.SessionTimeout != state.SessionTimeout || cfg.ModelTimeout != state.ModelTimeout {
		t.Fatalf("timeouts = %s/%s", cfg.SessionTimeout, cfg.ModelTimeout)
	}
	if cfg.PromptVersion != agent.PromptVersion || cfg.ToolsetVersion != tools.Version {
		t.Fatalf("versions = %q/%q", cfg.PromptVersion, cfg.ToolsetVersion)
	}
	if cfg.ProvisionalContext {
		t.Fatal("ProvisionalContext = true for an inherited session")
	}
	wantMargin := agent.SafetyMargin(state.Limits.MaxContextTokens)
	if cfg.Limits.SafetyMargin != wantMargin {
		t.Fatalf("safety margin = %d, want %d", cfg.Limits.SafetyMargin, wantMargin)
	}
}

func TestApplySessionDefaultsOverrides(t *testing.T) {
	args := []string{
		"-resume", v1TestSession,
		"-state-dir", t.TempDir(),
		"-model", "crouton-alpha-free",
		"-workspace", t.TempDir(),
		"-provider-base-url", "http://override.example/v1",
	}
	args = append(args, "-max-turns", "200", "-max-total-tokens", "5000000", "-max-context-tokens", "120000", "-max-output-tokens", "4096", "-session-timeout", "2m")
	cfg, err := parseConfig(args, cfgTestGetenv(nil), nil)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	state := v1State()
	if err := applySessionDefaults(&cfg, state); err != nil {
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
	if !cfg.workspaceSet || cfg.Workspace == state.Workspace {
		t.Fatal("explicit workspace was not retained")
	}
}

func TestApplySessionDefaultsRejectsUnsupportedState(t *testing.T) {
	base, err := parseConfig([]string{"-resume", v1TestSession, "-state-dir", t.TempDir()}, cfgTestGetenv(nil), nil)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*agent.State)
		want   string
	}{
		{name: "provider", mutate: func(s *agent.State) { s.Provider = "other" }, want: "provider"},
		{name: "prompt", mutate: func(s *agent.State) { s.PromptVersion = "prompt-v9" }, want: "prompt version"},
		{name: "toolset", mutate: func(s *agent.State) { s.ToolsetVersion = "toolset-v9" }, want: "toolset version"},
		{name: "model", mutate: func(s *agent.State) { s.Model = ""; s.InitialModel = "" }, want: "model"},
		{name: "workspace", mutate: func(s *agent.State) { s.Workspace = "" }, want: "workspace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			state := v1State()
			tc.mutate(&state)
			err := applySessionDefaults(&cfg, state)
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
