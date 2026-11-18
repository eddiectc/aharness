package aharness_test

import (
	"context"
	"encoding/json"
	"errors"
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

func TestConfigValidation(t *testing.T) {
	model := aharness.CustomModel(validSpec(), &scriptedModel{})
	cases := []struct {
		name string
		cfg  aharness.Config
		want error
	}{
		{name: "missing workspace", cfg: aharness.Config{StateDir: t.TempDir(), UserAgent: "test", Model: model}},
		{name: "missing state dir", cfg: aharness.Config{Workspace: t.TempDir(), UserAgent: "test", Model: model}},
		{name: "missing model", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test"}, want: aharness.ErrModelRequired},
		{name: "missing user agent", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), Model: model}},
		{name: "zero context", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: aharness.CustomModel(aharness.ModelSpec{ID: "m", Dialect: aharness.DialectCustom}, &scriptedModel{})}},
		{name: "prompt conflict", cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model, SystemPrompt: "a", Instructions: "b"}},
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
		},
		{
			name: "delegation delegate",
			cfg: aharness.Config{Workspace: t.TempDir(), StateDir: t.TempDir(), UserAgent: "test", Model: model,
				Tools: &aharness.ToolsConfig{Delegation: &aharness.DelegationConfig{Agents: []aharness.AgentDefinition{{
					Name: "a", Description: "d", Prompt: "p", Tools: []aharness.ToolName{"delegate"},
				}}}}},
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
			var configErr *aharness.ConfigError
			if !errors.As(err, &configErr) && !errors.Is(err, aharness.ErrModelRequired) && !errors.Is(err, aharness.ErrToolConflict) {
				t.Fatalf("error = %v, want ConfigError or a documented sentinel", err)
			}
		})
	}
}
