package aharness_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eddiectc/aharness"
)

// childImageModel scripts a child that calls read_image and then reports.
func childImageModel() *scriptedModel {
	return &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls:    []aharness.ToolCall{{ID: "img-1", Name: "read_image", Arguments: json.RawMessage(`{"path":"shot.png"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true},
		},
		{Text: "child done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true}},
	}}
}

// runDelegatedImageChild drives one delegate call whose child names a different
// model, and returns the child's recorded read_image result.
func runDelegatedImageChild(t *testing.T, parentImageInput *bool, childImageInput *bool) aharness.TranscriptItem {
	t.Helper()
	parent := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "delegate-1",
				Name:      "delegate",
				Arguments: delegateArgs(map[string]string{"agent": "vision", "task": "read shot.png"}),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true},
		},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 2, OutputTokens: 2, Reported: true}},
	}}
	childModel := childImageModel()

	workspace, stateDir := t.TempDir(), t.TempDir()
	writePNG(t, workspace, "shot.png")

	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model: aharness.CustomModel(aharness.ModelSpec{
			ID: "parent-model", Provider: "test", Dialect: aharness.DialectCustom,
			ContextWindow: 100_000, ImageInput: parentImageInput,
		}, parent),
		Tools: &aharness.ToolsConfig{
			Delegation: &aharness.DelegationConfig{
				Agents: []aharness.AgentDefinition{{
					Name:        "vision",
					Description: "Reads one image.",
					Prompt:      "Read the image and report.",
					Model:       "child-model",
					Tools:       []aharness.ToolName{aharness.ToolReadImage},
				}},
				ResolveModel: func(_ context.Context, modelID, _ string) (*aharness.ModelConfig, error) {
					if modelID != "child-model" {
						t.Fatalf("resolved unexpected model %q", modelID)
					}
					return aharness.CustomModel(aharness.ModelSpec{
						ID: "child-model", Provider: "test", Dialect: aharness.DialectCustom,
						ContextWindow: 100_000, ImageInput: childImageInput,
					}, childModel), nil
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()

	var childSession string
	run, err := ag.Start(context.Background(), "delegate", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for ev, err := range run.Events() {
		if err != nil {
			t.Fatalf("event error: %v", err)
		}
		if started, ok := ev.(aharness.SubagentStarted); ok {
			childSession = started.ChildSession
		}
	}
	if childSession == "" {
		t.Fatal("no child session started")
	}

	items, err := aharness.Transcript(context.Background(), stateDir, childSession)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	for _, item := range items {
		if item.Kind == aharness.TranscriptTool && item.Name == "read_image" {
			return item
		}
	}
	t.Fatal("child transcript has no read_image result")
	return aharness.TranscriptItem{}
}

// TestDelegatedChildImageGateUsesChildModel is the facade-level counterpart of
// the agent test: the child runs on its own model, so its gate must come from
// that model and not from the parent's capability.
func TestDelegatedChildImageGateUsesChildModel(t *testing.T) {
	no := false
	yes := true

	t.Run("parent image-capable, child text-only", func(t *testing.T) {
		item := runDelegatedImageChild(t, &yes, &no)
		if item.Code != "unavailable" {
			t.Errorf("child read_image code = %q, want unavailable", item.Code)
		}
	})

	t.Run("parent text-only, child image-capable", func(t *testing.T) {
		item := runDelegatedImageChild(t, &no, &yes)
		if item.Code != "ok" {
			t.Fatalf("child read_image code = %q, want ok (error %q)", item.Code, item.Error)
		}
	})
}
