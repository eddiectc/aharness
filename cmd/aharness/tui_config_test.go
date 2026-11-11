package main

import (
	"io"
	"strings"
	"testing"

	"aharness/internal/model"
)

func TestParseModeSelection(t *testing.T) {
	stateDir := t.TempDir()
	base := []string{"-state-dir", stateDir, "-model", "deepseek-v4.1-flash", "-workspace", t.TempDir()}
	cfg := cfgTestParseOK(t, base, nil)
	if cfg.Mode != ModeTUI || !cfg.Stream {
		t.Fatalf("default mode = %v stream=%v", cfg.Mode, cfg.Stream)
	}
	if cfg.TaskSet {
		t.Fatal("TUI new session should allow no task")
	}
	cfg = cfgTestParseOK(t, append(append([]string{}, base...), "-batch", "-task", "work"), nil)
	if cfg.Mode != ModeBatch {
		t.Fatalf("batch mode = %v", cfg.Mode)
	}
	cfg = cfgTestParseOK(t, append(append([]string{}, base...), "-ui-json", "-ui-size", "80x24"), nil)
	if cfg.Mode != ModeUIJSON || cfg.UISizeWidth != 80 || cfg.UISizeHeight != 24 {
		t.Fatalf("ui-json config = %+v", cfg)
	}
}

func TestParseRemovedInteractiveFlag(t *testing.T) {
	args := append(cfgTestBaseArgs(t), "-interactive")
	if err := cfgTestParseErr(t, args, nil); err == nil {
		t.Fatal("-interactive accepted")
	}
}

func TestParseModeExclusionsAndUISize(t *testing.T) {
	base := cfgTestBaseArgs(t)
	if err := cfgTestParseErr(t, append(append([]string{}, base...), "-batch", "-ui-json"), nil); err == nil {
		t.Fatal("-batch and -ui-json accepted together")
	}
	if err := cfgTestParseErr(t, append(append([]string{}, base...), "-ui-size", "80x24"), nil); err == nil {
		t.Fatal("-ui-size accepted without -ui-json")
	}
	for _, size := range []string{"19x10", "100x4", "501x30", "100x201", "bad", "100", "100x"} {
		if err := cfgTestParseErr(t, append(append([]string{}, base...), "-ui-json", "-ui-size", size), nil); err == nil {
			t.Errorf("-ui-size %q accepted", size)
		}
	}
	cfg := cfgTestParseOK(t, append(append([]string{}, base...), "-ui-json"), nil)
	if cfg.UISizeWidth != defaultUISizeWidth || cfg.UISizeHeight != defaultUISizeHeight {
		t.Fatalf("default ui size = %dx%d", cfg.UISizeWidth, cfg.UISizeHeight)
	}
}

func TestModeTaskAndStreamRules(t *testing.T) {
	// Batch requires a task for a new session; TUI does not.
	batch := config{Mode: ModeBatch}
	if err := validateModeRules(&batch); err == nil || !strings.Contains(err.Error(), "task") {
		t.Fatalf("batch rule error = %v", err)
	}
	tui := config{Mode: ModeTUI}
	if err := validateModeRules(&tui); err != nil || !tui.Stream {
		t.Fatalf("TUI rule = %v stream=%v", err, tui.Stream)
	}
	fork := config{Mode: ModeBatch, ForkID: "0190a0c4-0000-7000-8000-0000000000aa"}
	if err := validateModeRules(&fork); err == nil {
		t.Fatal("batch fork without a task accepted")
	}
	fork.Mode = ModeTUI
	if err := validateModeRules(&fork); err != nil {
		t.Fatalf("TUI fork without a task: %v", err)
	}
	noStream := config{Mode: ModeTUI, StreamSet: true}
	if err := validateModeRules(&noStream); err == nil {
		t.Fatal("-stream=false accepted in TUI mode")
	}
	pdf := config{Mode: ModeBatch, TaskSet: true}
	if err := validateModeRules(&pdf); err != nil {
		t.Fatalf("batch with task: %v", err)
	}
}

func TestVersionSkipsModeSelection(t *testing.T) {
	cfg, err := parseConfig([]string{"-version", "-ui-json", "-batch"}, cfgTestGetenv(nil), io.Discard)
	if err != nil || !cfg.ShowVersion {
		t.Fatalf("version config = %+v err=%v", cfg, err)
	}
}

func TestSelectModeMatrix(t *testing.T) {
	orig := terminalCheck
	t.Cleanup(func() { terminalCheck = orig })
	t.Setenv("TERM", "xterm-256color")
	base := cfgTestBaseArgs(t)

	terminalCheck = func(any) bool { return true }
	cfg := cfgTestParseOK(t, base, nil)
	if err := selectMode(&cfg, strings.NewReader(""), io.Discard); err != nil || cfg.Mode != ModeTUI {
		t.Fatalf("TTY mode = %v err=%v", cfg.Mode, err)
	}

	terminalCheck = func(any) bool { return false }
	cfg = cfgTestParseOK(t, base, nil)
	if err := selectMode(&cfg, strings.NewReader(""), io.Discard); err != nil || cfg.Mode != ModeBatch {
		t.Fatalf("non-TTY mode = %v err=%v", cfg.Mode, err)
	}

	terminalCheck = func(any) bool { return true }
	cfg = cfgTestParseOK(t, base, nil)
	t.Setenv("TERM", "dumb")
	if err := selectMode(&cfg, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("TERM=dumb accepted for TUI")
	}
	t.Setenv("TERM", "")
	if err := selectMode(&cfg, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("empty TERM accepted for TUI")
	}

	// -ui-json ignores the TTY and TERM checks.
	cfg = cfgTestParseOK(t, append(append([]string{}, base...), "-ui-json"), nil)
	if err := selectMode(&cfg, strings.NewReader(""), io.Discard); err != nil || cfg.Mode != ModeUIJSON {
		t.Fatalf("ui-json mode = %v err=%v", cfg.Mode, err)
	}

	// Non-TTY downgrade applies the batch task matrix.
	terminalCheck = func(any) bool { return false }
	missingTask := []string{"-model", "deepseek-v4.1-flash", "-workspace", t.TempDir(), "-state-dir", t.TempDir()}
	cfg = cfgTestParseOK(t, missingTask, nil)
	if err := selectMode(&cfg, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("non-TTY new session without a task accepted")
	}
}

func TestParseTUIDefersCatalogResolution(t *testing.T) {
	// The TUI host resolves the catalog after parseConfig; an unknown model is
	// not rejected until the host loads the catalog.
	cfg := cfgTestParseOK(t, []string{
		"-ui-json",
		"-model", "model-not-in-catalog",
		"-state-dir", t.TempDir(),
		"-workspace", t.TempDir(),
	}, nil)
	if cfg.Mode != ModeUIJSON || !cfg.Stream {
		t.Fatalf("mode/stream = %v/%t", cfg.Mode, cfg.Stream)
	}
	if cfg.Dialect != "" || cfg.BaseURL != "" || cfg.MaxContextTokens != 0 || cfg.MaxOutputTokens != 0 {
		t.Fatalf("TUI parse resolved catalog values early: %+v", cfg)
	}
	if !cfg.modelSet {
		t.Fatal("modelSet = false, want true")
	}
}

func TestParseCatalogTerminalActionsIgnoreTTYRules(t *testing.T) {
	for _, action := range []string{"-list-models", "-refresh-models"} {
		t.Run(action, func(t *testing.T) {
			orig := terminalCheck
			t.Cleanup(func() { terminalCheck = orig })
			terminalCheck = func(any) bool { return false }
			t.Setenv("TERM", "")
			cfg := cfgTestParseOK(t, []string{
				action,
				"-ui-json",
				"-task", "ignored",
				"-state-dir", t.TempDir(),
			}, nil)
			if !cfg.ListModels && !cfg.RefreshModels {
				t.Fatalf("terminal action not recorded: %+v", cfg)
			}
			if cfg.TaskSet || cfg.Task != "" {
				t.Fatalf("terminal action retained the task: %+v", cfg)
			}
			if cfg.Mode != 0 || cfg.UIJSON {
				t.Fatalf("terminal action retained mode flags: %+v", cfg)
			}
		})
	}
}

func TestTUISelectModelReportsContextClamp(t *testing.T) {
	host := &tuiHost{
		cfg: config{
			Provider:         providerName,
			Model:            "large",
			Dialect:          model.DialectChat,
			BaseURL:          testCatalogBaseURL,
			MaxContextTokens: 1_000_000,
			MaxTurns:         defaultMaxTurns,
			MaxTotalTokens:   defaultMaxTotalTokens,
			SessionTimeout:   defaultSessionTimeout,
			ModelTimeout:     defaultModelTimeout,
			maxContextSet:    true,
			maxContextValue:  500_000,
		},
		catalog: map[string]model.CatalogModel{
			"small": {
				ID:            "small",
				Dialect:       model.DialectChat,
				BaseURL:       testCatalogBaseURL,
				ContextWindow: 262_144,
				ToolCall:      true,
			},
		},
	}
	if _, err := host.SelectModel("small"); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	if host.cfg.MaxContextTokens != 262_144 {
		t.Fatalf("MaxContextTokens = %d, want the model clamp", host.cfg.MaxContextTokens)
	}
	warning := host.TakeModelWarning()
	if !strings.Contains(warning, "clamped") {
		t.Fatalf("warning = %q, want the context clamp", warning)
	}
	if got := host.TakeModelWarning(); got != "" {
		t.Fatalf("warning was not consumed: %q", got)
	}
}
