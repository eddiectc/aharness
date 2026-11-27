package aharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/eddiectc/aharness"
)

func validSpec() aharness.ModelSpec {
	return aharness.ModelSpec{ID: "m", Dialect: aharness.DialectCustom, ContextWindow: 10_000}
}

func validTool(name string) aharness.Tool {
	return aharness.Tool{
		Name:        name,
		Description: "test tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Run: func(context.Context, aharness.ToolCall) (aharness.ToolResult, error) {
			return aharness.TextResult("ok"), nil
		},
	}
}

func TestValidEffort(t *testing.T) {
	for _, value := range []string{"low", "medium", "high", "x-high", "a_b"} {
		if !aharness.ValidEffort(value) {
			t.Errorf("ValidEffort(%q) = false", value)
		}
	}
	for _, value := range []string{"", "default", "High", "high ", "-high", "high!", strings.Repeat("a", 33)} {
		if aharness.ValidEffort(value) {
			t.Errorf("ValidEffort(%q) = true", value)
		}
	}
}

func TestPromptTagValidation(t *testing.T) {
	model := aharness.CustomModel(validSpec(), &scriptedModel{})
	for _, tag := range []string{"acode/prompt-v1", "a.b:c-d_e"} {
		t.Run("valid "+tag, func(t *testing.T) {
			ag, err := aharness.New(context.Background(), aharness.Config{
				Workspace: t.TempDir(),
				StateDir:  t.TempDir(),
				UserAgent: "test",
				Model:     model,
				PromptTag: tag,
			})
			if err != nil {
				t.Fatalf("New with PromptTag %q: %v", tag, err)
			}
			if err := ag.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		tag  string
	}{
		{name: "whitespace only", tag: "   "},
		{name: "space", tag: "acode prompt"},
		{name: "newline", tag: "acode\nprompt"},
		{name: "at sign", tag: "acode@prompt"},
		{name: "non ascii", tag: "acodé"},
		{name: "sixty five bytes", tag: strings.Repeat("a", 65)},
	} {
		t.Run("invalid "+tc.name, func(t *testing.T) {
			_, err := aharness.New(context.Background(), aharness.Config{
				Workspace: t.TempDir(),
				StateDir:  t.TempDir(),
				UserAgent: "test",
				Model:     model,
				PromptTag: tc.tag,
			})
			var configErr *aharness.ConfigError
			if !errors.As(err, &configErr) || configErr.Field != "PromptTag" {
				t.Fatalf("New with PromptTag %q: err = %v, want ConfigError for PromptTag", tc.tag, err)
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	model := aharness.CustomModel(validSpec(), &scriptedModel{})
	cases := []struct {
		name       string
		cfg        aharness.Config
		want       error
		wantDetail string
	}{
		{name: "missing workspace", cfg: aharness.Config{StateDir: t.TempDir(), UserAgent: "test", Model: model}},
		{name: "missing state dir", cfg: aharness.Config{Workspace: t.TempDir(), UserAgent: "test", Model: model}},
		{name: "missing model", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test"}, want: aharness.ErrModelRequired},
		{name: "missing user agent", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), Model: model}},
		{name: "zero context", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: aharness.CustomModel(aharness.ModelSpec{ID: "m", Dialect: aharness.DialectCustom}, &scriptedModel{})}},
		{name: "prompt conflict", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model, SystemPrompt: "a", Instructions: "b"}},
		{name: "identity newline", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model, Identity: "aco\nde"}},
		{name: "identity control", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model, Identity: "aco\x01de"}},
		{name: "identity invalid utf8", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model, Identity: string([]byte{0xff})}},
		{name: "identity too long", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model, Identity: strings.Repeat("a", 65)}},
		{name: "negative turns", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model, MaxTurns: -1}},
		{
			name: "unknown disable",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{Disable: []aharness.ToolName{"nope"}}},
		},
		{
			name: "disable delegate",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{Disable: []aharness.ToolName{aharness.ToolDelegate}}},
		},
		{
			name: "duplicate custom tool",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{Extra: []aharness.Tool{validTool("lookup"), validTool("lookup")}}},
			want: aharness.ErrToolConflict,
		},
		{
			name: "builtin collision",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{Extra: []aharness.Tool{validTool("read_file")}}},
			want: aharness.ErrToolConflict,
		},
		{
			name: "bad custom schema",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{Extra: []aharness.Tool{{
					Name: "lookup", Description: "d", InputSchema: json.RawMessage(`[]`),
					Run: validTool("lookup").Run,
				}}}},
		},
		{
			name: "empty delegation",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{Delegation: &aharness.DelegationConfig{}}},
			wantDetail: "must not be empty; supply the host's definition set",
		},
		{
			name: "delegation delegate",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{Delegation: &aharness.DelegationConfig{Agents: []aharness.AgentDefinition{{
					Name: "a", Description: "d", Prompt: "p", Tools: []aharness.ToolName{"delegate"},
				}}}}},
		},
		{
			name: "allowed origin with crawl4ai",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{WebFetch: &aharness.WebFetchConfig{
					Provider:       aharness.WebFetchCrawl4AI,
					Endpoint:       "http://127.0.0.1:11235",
					AllowedOrigins: []string{"http://127.0.0.1:8888"},
				}}},
			wantDetail: "cannot enforce a per-resource origin list",
		},
		{
			name: "invalid allowed origin",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{WebFetch: &aharness.WebFetchConfig{
					AllowedOrigins: []string{"https://example.com/path"},
				}}},
			wantDetail: "only scheme, host, and port are allowed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := aharness.New(context.Background(), tc.cfg)
			if err == nil {
				t.Fatal("New accepted invalid config")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.wantDetail != "" && !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantDetail)
			}
			var configErr *aharness.ConfigError
			if !errors.As(err, &configErr) && !errors.Is(err, aharness.ErrModelRequired) && !errors.Is(err, aharness.ErrToolConflict) {
				t.Fatalf("error = %v, want ConfigError or a documented sentinel", err)
			}
		})
	}
}
