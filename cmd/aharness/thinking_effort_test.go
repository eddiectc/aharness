package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/session"
)

// thinkingEffortCatalogModels returns the shared fixture with per-model
// reasoning options. The effort arrays are deliberately unordered so tests
// also prove catalog normalization.
func thinkingEffortCatalogModels() map[string]fakeCatalogModel {
	models := defaultFakeCatalogModels()
	set := func(id string, efforts ...string) {
		m := models[id]
		m.Reasoning = true
		m.Efforts = efforts
		models[id] = m
	}
	set("deepseek-v4.1-flash", "max", "low", "high")
	set("gpt-6-luna", "medium", "none", "low", "high")
	set("crouton-alpha-free", "high", "low")
	set("muse-spark-1.3-contributor", "xhigh", "minimal", "high")
	set("kimi-k2.7-code")
	set("qwen3.7-plus")
	return models
}

func TestConfigThinkingEffort(t *testing.T) {
	tooLong := strings.Repeat("a", model.MaxThinkingEffortBytes+1)
	tests := []struct {
		name    string
		env     map[string]string
		extra   []string
		want    string
		wantSet bool
		wantErr string
	}{
		{name: "unset uses provider default"},
		{name: "environment value", env: map[string]string{"AHARNESS_THINKING_EFFORT": "low"}, want: "low", wantSet: true},
		{
			name: "flag overrides environment",
			env:  map[string]string{"AHARNESS_THINKING_EFFORT": "low"},
			extra: []string{
				"-thinking-effort", "high",
			},
			want: "high", wantSet: true,
		},
		{
			name: "explicit default clears environment",
			env:  map[string]string{"AHARNESS_THINKING_EFFORT": "high"},
			extra: []string{
				"-thinking-effort", "default",
			},
			wantSet: true,
		},
		{name: "explicit default", extra: []string{"-thinking-effort", "default"}, wantSet: true},
		{
			name:    "environment default",
			env:     map[string]string{"AHARNESS_THINKING_EFFORT": "default"},
			wantSet: true,
		},
		{name: "empty environment is unset", env: map[string]string{"AHARNESS_THINKING_EFFORT": ""}},
		{
			name:    "empty flag is rejected",
			extra:   []string{"-thinking-effort="},
			wantErr: "must not be empty",
		},
		{
			name:    "uppercase value is rejected",
			extra:   []string{"-thinking-effort", "High"},
			wantErr: "invalid thinking effort",
		},
		{
			name:    "leading digit is rejected",
			extra:   []string{"-thinking-effort", "1low"},
			wantErr: "invalid thinking effort",
		},
		{
			name:    "leading dash is rejected",
			extra:   []string{"-thinking-effort", "-low"},
			wantErr: "invalid thinking effort",
		},
		{
			name:    "space is rejected",
			extra:   []string{"-thinking-effort", "lo w"},
			wantErr: "invalid thinking effort",
		},
		{
			name:    "control character is rejected",
			extra:   []string{"-thinking-effort", "high\x01"},
			wantErr: "invalid thinking effort",
		},
		{
			name:    "invalid UTF-8 is rejected",
			extra:   []string{"-thinking-effort", string([]byte{0xff})},
			wantErr: "invalid thinking effort",
		},
		{
			name:    "malformed environment value is rejected",
			env:     map[string]string{"AHARNESS_THINKING_EFFORT": "High"},
			wantErr: "invalid thinking effort",
		},
		{
			name:    "oversized value is rejected",
			extra:   []string{"-thinking-effort", tooLong},
			wantErr: "invalid thinking effort",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := append(cfgTestBaseArgs(t), tc.extra...)
			cfg, err := parseConfig(args, cfgTestGetenv(tc.env), io.Discard)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseConfig = %+v, want error containing %q", cfg, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseConfig: %v", err)
			}
			if cfg.ThinkingEffort != tc.want {
				t.Errorf("ThinkingEffort = %q, want %q", cfg.ThinkingEffort, tc.want)
			}
			if cfg.thinkingEffortSet != tc.wantSet {
				t.Errorf("thinkingEffortSet = %t, want %t", cfg.thinkingEffortSet, tc.wantSet)
			}
		})
	}
}

func TestConfigThinkingEffortTerminalActionsIgnoreSetting(t *testing.T) {
	for _, action := range []string{"-list-models", "-refresh-models"} {
		for _, value := range []string{"", "High", "high\x01", strings.Repeat("z", model.MaxThinkingEffortBytes+1)} {
			t.Run(action+"_"+value, func(t *testing.T) {
				args := []string{action, "-state-dir", t.TempDir(), "-thinking-effort", value}
				env := map[string]string{"AHARNESS_THINKING_EFFORT": value}
				cfg, err := parseConfig(args, cfgTestGetenv(env), io.Discard)
				if err != nil {
					t.Fatalf("parseConfig(%v): %v", args, err)
				}
				if cfg.ThinkingEffort != "" || cfg.thinkingEffortSet {
					t.Fatalf("terminal action resolved effort = %q set=%t, want ignored", cfg.ThinkingEffort, cfg.thinkingEffortSet)
				}
				if action == "-list-models" && !cfg.ListModels {
					t.Fatalf("ListModels = false")
				}
				if action == "-refresh-models" && !cfg.RefreshModels {
					t.Fatalf("RefreshModels = false")
				}
			})
		}
	}
}

func TestResolveNewModelThinkingEffort(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		effort  string
		plain   bool
		want    string
		wantErr string
	}{
		{name: "supported value", model: "deepseek-v4.1-flash", effort: "high", want: "high"},
		{name: "omitted sends provider default", model: "deepseek-v4.1-flash"},
		{name: "explicit default clears", model: "deepseek-v4.1-flash", effort: "default"},
		{
			name: "unsupported value lists advertised levels", model: "deepseek-v4.1-flash", effort: "medium",
			wantErr: "supported: low, high, max",
		},
		{
			name: "model with no effort control", model: "kimi-k2.7-code", effort: "high",
			wantErr: "advertises no effort levels",
		},
		{
			name: "non-reasoning model", model: "plain-text", effort: "high", plain: true,
			wantErr: "does not support reasoning",
		},
		{name: "default on a non-reasoning model", model: "plain-text", effort: "default", plain: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			models := thinkingEffortCatalogModels()
			if tc.plain {
				models["plain-text"] = testCatalogModel("Plain Text", "", 100_000, 0, 8_192)
			}
			fake := startFakeModelsDev(t)
			fake.SetModels(models)
			args := []string{
				"-task", "work",
				"-model", tc.model,
				"-workspace", t.TempDir(),
				"-state-dir", t.TempDir(),
			}
			if tc.effort != "" {
				args = append(args, "-thinking-effort", tc.effort)
			}
			cfg := cfgTestParseOK(t, args, nil)
			if tc.wantErr != "" {
				err := cfgTestResolveErr(t, cfg, fake)
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			cfg, _ = cfgTestResolve(t, cfg, fake)
			if cfg.ThinkingEffort != tc.want {
				t.Fatalf("ThinkingEffort = %q, want %q", cfg.ThinkingEffort, tc.want)
			}
		})
	}
}

func TestResolveSessionThinkingEffort(t *testing.T) {
	state := v1State()
	state.ThinkingEffort = "high"

	t.Run("inherits without a catalog", func(t *testing.T) {
		cfg := v1ResumeConfig(t)
		if err := resolveSessionThinkingEffort(context.Background(), &cfg, state, nil, nil, nil); err != nil {
			t.Fatalf("resolveSessionThinkingEffort: %v", err)
		}
		if cfg.ThinkingEffort != "high" {
			t.Fatalf("ThinkingEffort = %q, want inherited high", cfg.ThinkingEffort)
		}
	})

	t.Run("explicit default clears without a catalog", func(t *testing.T) {
		cfg := v1ResumeConfig(t, "-thinking-effort", "default")
		if err := resolveSessionThinkingEffort(context.Background(), &cfg, state, nil, nil, nil); err != nil {
			t.Fatalf("resolveSessionThinkingEffort: %v", err)
		}
		if cfg.ThinkingEffort != "" || !cfg.thinkingEffortSet {
			t.Fatalf("ThinkingEffort = %q set=%t, want explicit clear", cfg.ThinkingEffort, cfg.thinkingEffortSet)
		}
	})

	t.Run("explicit value requires a catalog", func(t *testing.T) {
		cfg := v1ResumeConfig(t, "-thinking-effort", "max")
		err := resolveSessionThinkingEffort(context.Background(), &cfg, state, nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "catalog is required") {
			t.Fatalf("error = %v, want a catalog requirement", err)
		}
	})

	t.Run("inherited value is cleared when the model drops it", func(t *testing.T) {
		cfg := v1ResumeConfig(t)
		resolved := model.CatalogModel{
			ID:               "gpt-6-luna",
			Reasoning:        true,
			ReasoningEfforts: []string{"low", "medium"},
		}
		var warning string
		if err := resolveSessionThinkingEffort(context.Background(), &cfg, state, nil, &resolved, func(w string) {
			warning = w
		}); err != nil {
			t.Fatalf("resolveSessionThinkingEffort: %v", err)
		}
		if cfg.ThinkingEffort != "" {
			t.Fatalf("ThinkingEffort = %q, want cleared", cfg.ThinkingEffort)
		}
		if !strings.Contains(warning, `thinking effort "high" cleared`) {
			t.Fatalf("warning = %q", warning)
		}
	})
}

func TestWriteCatalogListEffortColumn(t *testing.T) {
	models := []model.CatalogModel{
		{
			ID:               "alpha",
			Name:             "Alpha",
			Dialect:          model.DialectChat,
			ContextWindow:    1000,
			OutputLimit:      100,
			Reasoning:        true,
			ReasoningEfforts: []string{"low", "high", "max"},
		},
		{
			ID:               "beta",
			Name:             "Beta",
			Dialect:          model.DialectResponses,
			ContextWindow:    2000,
			OutputLimit:      0,
			Reasoning:        true,
			ReasoningEfforts: nil,
		},
	}
	var out bytes.Buffer
	writeCatalogList(&out, models)
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want header plus two rows:\n%s", len(lines), out.String())
	}
	if want := "ID\tNAME\tDIALECT\tCONTEXT\tOUTPUT\tSTATUS\tEFFORT"; lines[0] != want {
		t.Fatalf("header = %q, want %q", lines[0], want)
	}
	if want := "alpha\tAlpha\topenai-chat\t1000\t100\t-\tlow,high,max"; lines[1] != want {
		t.Errorf("row = %q, want %q", lines[1], want)
	}
	if want := "beta\tBeta\topenai-responses\t2000\t-\t-\t-"; lines[2] != want {
		t.Errorf("row = %q, want %q", lines[2], want)
	}
}

func TestInspectLegacySessionWithoutThinkingEffort(t *testing.T) {
	id := "0190a0c4-0000-7000-8000-0000000000dd"
	events := []session.Event{
		{
			V:       session.Version,
			Seq:     1,
			Session: id,
			TS:      time.Now().UTC(),
			Type:    session.TypeSessionStart,
			Data:    json.RawMessage(`{"prompt_version":"prompt-v0","toolset_version":"toolset-v0","provider":"opencode-go","dialect":"openai-chat","model":"deepseek-v4.1-flash","workspace":"/tmp/w"}`),
		},
		{
			V:       session.Version,
			Seq:     2,
			Session: id,
			TS:      time.Now().UTC(),
			Type:    session.TypeRunStart,
			Data:    json.RawMessage(`{"prompt_version":"prompt-v0","toolset_version":"toolset-v0","model":"deepseek-v4.1-flash","dialect":"openai-chat"}`),
		},
	}
	state, err := agent.Inspect(events)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.ThinkingEffort != "" {
		t.Fatalf("ThinkingEffort = %q, want empty for a legacy log", state.ThinkingEffort)
	}
	if state.Model != "deepseek-v4.1-flash" {
		t.Fatalf("Model = %q", state.Model)
	}
}
