package aharness_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/eddiectc/aharness"
)

// outputBudgetSpec advertises a 100-token ceiling so a test can prove the
// ceiling never becomes a request budget.
func outputBudgetSpec(id string, contextWindow int) aharness.ModelSpec {
	return aharness.ModelSpec{
		ID:              id,
		Provider:        "test",
		Dialect:         aharness.DialectCustom,
		ContextWindow:   contextWindow,
		MaxOutputTokens: 100,
	}
}

func TestMaxOutputTokensIsHostOptedIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  int
		wantErr bool
		want    int
	}{
		{name: "negative is a config error", output: -1, wantErr: true},
		{name: "zero sends no budget", output: 0, want: 0},
		{name: "positive is sent unchanged", output: 500, want: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &scriptedModel{}
			ag, err := aharness.New(context.Background(), aharness.Config{
				Workspace: t.TempDir(),
				StateDir:  t.TempDir(),
				UserAgent: "aharness-test/1.0",
				Limits:    aharness.Limits{MaxOutputTokens: tc.output},
				Model:     aharness.CustomModel(outputBudgetSpec("limit-model", 10_000), model),
			})
			if tc.wantErr {
				var configErr *aharness.ConfigError
				if !errors.As(err, &configErr) || configErr.Field != "Limits.MaxOutputTokens" {
					t.Fatalf("New error = %v, want ConfigError for Limits.MaxOutputTokens", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = ag.Close() }()
			state, err := ag.State()
			if err != nil {
				t.Fatalf("State: %v", err)
			}
			if got := state.Limits.MaxOutputTokens; got != tc.want {
				t.Errorf("State.Limits.MaxOutputTokens = %d, want %d", got, tc.want)
			}
			if _, err := ag.Run(context.Background(), "hello", aharness.RunOptions{}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(model.requests) == 0 {
				t.Fatal("model received no request")
			}
			if got := model.requests[0].MaxOutputTokens; got != tc.want {
				t.Errorf("request MaxOutputTokens = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestContextOverrideNarrowsInputLimit covers the documented input limit:
// min(model limit.input, context - safety margin), so lowering the context
// narrows the input budget instead of failing.
func TestContextOverrideNarrowsInputLimit(t *testing.T) {
	model := &scriptedModel{}
	spec := outputBudgetSpec("limit-model", 10_000)
	spec.InputLimit = 9_000
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
		Limits:    aharness.Limits{MaxContextTokens: 8_000},
		Model:     aharness.CustomModel(spec, model),
	})
	if err != nil {
		t.Fatalf("New with a context override below the input limit: %v", err)
	}
	defer func() { _ = ag.Close() }()
	state, err := ag.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.Limits.MaxContextTokens != 8_000 {
		t.Errorf("MaxContextTokens = %d, want 8000", state.Limits.MaxContextTokens)
	}
	if state.Limits.MaxInputTokens != 7_000 {
		t.Errorf("MaxInputTokens = %d, want 7000 (8000 minus the 1000 safety margin)", state.Limits.MaxInputTokens)
	}
}

func TestModelSwitchKeepsSessionOutputBudget(t *testing.T) {
	model := &scriptedModel{}
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
		Limits:    aharness.Limits{MaxOutputTokens: 500},
		Model:     aharness.CustomModel(outputBudgetSpec("limit-model", 10_000), model),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	if _, err := ag.Run(context.Background(), "hello", aharness.RunOptions{
		Model: aharness.CustomModel(outputBudgetSpec("switch-model", 64_000), model),
	}); err != nil {
		t.Fatalf("Run with model override: %v", err)
	}
	if len(model.requests) == 0 {
		t.Fatal("model received no request")
	}
	if got := model.requests[0].MaxOutputTokens; got != 500 {
		t.Errorf("switched request MaxOutputTokens = %d, want the session budget 500", got)
	}
	// run_start records the switched run's limits: the context window follows
	// the new spec (64,000 - a 3,200 margin) and the output budget does not.
	state, err := ag.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.Limits.MaxContextTokens != 64_000 || state.Limits.MaxInputTokens != 60_800 {
		t.Errorf("switched limits = %+v, want context 64000 and input 60800", state.Limits)
	}
	if state.Limits.MaxOutputTokens != 500 {
		t.Errorf("switched MaxOutputTokens = %d, want 500", state.Limits.MaxOutputTokens)
	}
}

func TestResumeLimitsOutputBudget(t *testing.T) {
	newSession := func(t *testing.T, model aharness.Model) (string, string) {
		t.Helper()
		workspace, stateDir := t.TempDir(), t.TempDir()
		ag, err := aharness.New(context.Background(), aharness.Config{
			Workspace: workspace,
			StateDir:  stateDir,
			UserAgent: "aharness-test/1.0",
			Limits:    aharness.Limits{MaxOutputTokens: 500},
			Model:     aharness.CustomModel(outputBudgetSpec("limit-model", 10_000), model),
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		id := ag.ID()
		if err := ag.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		return id, stateDir
	}

	for _, tc := range []struct {
		name    string
		limits  aharness.Limits
		wantErr bool
		want    int
	}{
		{name: "no override inherits the recorded budget", want: 500},
		{name: "an explicit override clears the recorded budget with zero", limits: aharness.Limits{MaxContextTokens: 8_000}},
		{name: "negative override is a config error", limits: aharness.Limits{MaxOutputTokens: -1}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &scriptedModel{}
			sessionID, stateDir := newSession(t, model)
			resumed, err := aharness.Resume(context.Background(), aharness.Config{
				StateDir:  stateDir,
				UserAgent: "aharness-test/1.0",
				Model:     aharness.CustomModel(aharness.ModelSpec{}, model),
				Limits:    tc.limits,
			}, sessionID)
			if tc.wantErr {
				if resumed != nil {
					_ = resumed.Close()
				}
				var configErr *aharness.ConfigError
				if !errors.As(err, &configErr) || configErr.Field != "Limits.MaxOutputTokens" {
					t.Fatalf("Resume error = %v, want ConfigError for Limits.MaxOutputTokens", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			defer func() { _ = resumed.Close() }()
			if _, err := resumed.Run(context.Background(), "continue", aharness.RunOptions{}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			// run_start records the effective per-call limits, so the state is only
			// authoritative after the resumed session has run once.
			state, err := resumed.State()
			if err != nil {
				t.Fatalf("State: %v", err)
			}
			if got := state.Limits.MaxOutputTokens; got != tc.want {
				t.Errorf("State.Limits.MaxOutputTokens = %d, want %d", got, tc.want)
			}
			if len(model.requests) == 0 {
				t.Fatal("model received no request")
			}
			if got := model.requests[0].MaxOutputTokens; got != tc.want {
				t.Errorf("resumed request MaxOutputTokens = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDelegatedChildOnAnotherModelKeepsSessionOutputBudget covers the
// resolver path: the child's context window and input limit follow its own
// model, but the session's explicit output budget still applies.
func TestDelegatedChildOnAnotherModelKeepsSessionOutputBudget(t *testing.T) {
	parentModel := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "delegate-1",
				Name:      "delegate",
				Arguments: delegateArgs(map[string]string{"agent": "analyst", "task": "read the data"}),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true},
		},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true}},
	}}
	childModel := &scriptedModel{responses: []aharness.Response{
		{Text: "child done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true}},
	}}
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
		Limits:    aharness.Limits{MaxOutputTokens: 500},
		Model:     aharness.CustomModel(outputBudgetSpec("limit-model", 100_000), parentModel),
		Tools: &aharness.ToolsConfig{Delegation: &aharness.DelegationConfig{
			Agents: []aharness.AgentDefinition{{
				Name:        "analyst",
				Description: "Read-only data analyst.",
				Prompt:      "Read the assigned data and report.",
				Tools:       []aharness.ToolName{"read_file"},
				Model:       "child-model",
			}},
			ResolveModel: func(_ context.Context, modelID, _ string) (*aharness.ModelConfig, error) {
				if modelID != "child-model" {
					return nil, fmt.Errorf("unexpected model %q", modelID)
				}
				return aharness.CustomModel(outputBudgetSpec("child-model", 64_000), childModel), nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	if result, err := ag.Run(context.Background(), "delegate", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v (result %+v)", err, result)
	}
	if len(childModel.requests) == 0 {
		t.Fatal("child model received no request")
	}
	if got := childModel.requests[0].MaxOutputTokens; got != 500 {
		t.Errorf("child request MaxOutputTokens = %d, want the session budget 500", got)
	}
}

// TestDelegatedChildKeepsSessionOutputBudget covers a child that keeps the
// parent model: the whole session limit set applies.
func TestDelegatedChildKeepsSessionOutputBudget(t *testing.T) {
	model := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "delegate-1",
				Name:      "delegate",
				Arguments: delegateArgs(map[string]string{"agent": "analyst", "task": "read the data"}),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true},
		},
		{Text: "child done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true}},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true}},
	}}
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
		Limits:    aharness.Limits{MaxOutputTokens: 500},
		Model:     aharness.CustomModel(outputBudgetSpec("limit-model", 100_000), model),
		Tools: &aharness.ToolsConfig{Delegation: &aharness.DelegationConfig{Agents: []aharness.AgentDefinition{{
			Name:        "analyst",
			Description: "Read-only data analyst.",
			Prompt:      "Read the assigned data and report.",
			Tools:       []aharness.ToolName{"read_file"},
		}}}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	if result, err := ag.Run(context.Background(), "delegate", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v (result %+v)", err, result)
	}
	if len(model.requests) < 2 {
		t.Fatalf("model received %d requests, want a parent and a child call", len(model.requests))
	}
	for i, req := range model.requests {
		if req.MaxOutputTokens != 500 {
			t.Errorf("request %d MaxOutputTokens = %d, want the session budget 500", i, req.MaxOutputTokens)
		}
	}
}
