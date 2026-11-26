package aharness_test

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/eddiectc/aharness"
)

// writePNG drops a small real PNG into the workspace.
func writePNG(t *testing.T, dir, name string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
}

// readImageModel scripts one read_image call followed by a stop.
func readImageModel(path string) *scriptedModel {
	return &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls:    []aharness.ToolCall{{ID: "call-1", Name: "read_image", Arguments: json.RawMessage(`{"path":"` + path + `"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
}

// imageCapabilityModel builds a run override whose spec carries the capability.
// It must script the same tool call, or the run would produce no new result and
// the assertion would read a stale one.
func imageCapabilityModel(capability *bool, path string) *aharness.ModelConfig {
	return aharness.CustomModel(aharness.ModelSpec{
		ID:            "override-model",
		Provider:      "test",
		Dialect:       aharness.DialectCustom,
		ContextWindow: 100_000,
		ImageInput:    capability,
	}, readImageModel(path))
}

// TestRunModelSwitchChangesImageGate is the reported bug: the gate was fixed at
// New, so switching models mid-session did not change it until a new session.
func TestRunModelSwitchChangesImageGate(t *testing.T) {
	no := false
	yes := true

	t.Run("text-only construction gates, image override enables", func(t *testing.T) {
		model := readImageModel("a.png")
		workspace, stateDir := t.TempDir(), t.TempDir()
		writePNG(t, workspace, "a.png")
		ag, err := aharness.New(context.Background(), aharness.Config{
			Workspace: workspace,
			StateDir:  stateDir,
			UserAgent: "aharness-test/1.0",
			Model: aharness.CustomModel(aharness.ModelSpec{
				ID: "test-model", Provider: "test", Dialect: aharness.DialectCustom,
				ContextWindow: 100_000, ImageInput: &no,
			}, model),
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = ag.Close() }()

		first, err := ag.Run(context.Background(), "read the image", aharness.RunOptions{})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := lastToolCode(t, ag); got != "unavailable" {
			t.Fatalf("text-only model tool code = %q, want unavailable", got)
		}
		_ = first

		// Switch to an image-capable model for the next run, same session.
		if _, err := ag.Run(context.Background(), "read the image again", aharness.RunOptions{
			Model: imageCapabilityModel(&yes, "a.png"),
		}); err != nil {
			t.Fatalf("Run with override: %v", err)
		}
		if got := lastToolCode(t, ag); got != "ok" {
			t.Errorf("after switching to an image model, tool code = %q, want ok", got)
		}
	})

	t.Run("image construction enables, text-only override gates", func(t *testing.T) {
		model := readImageModel("a.png")
		workspace, stateDir := t.TempDir(), t.TempDir()
		writePNG(t, workspace, "a.png")
		ag, err := aharness.New(context.Background(), aharness.Config{
			Workspace: workspace,
			StateDir:  stateDir,
			UserAgent: "aharness-test/1.0",
			Model: aharness.CustomModel(aharness.ModelSpec{
				ID: "test-model", Provider: "test", Dialect: aharness.DialectCustom,
				ContextWindow: 100_000, ImageInput: &yes,
			}, model),
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = ag.Close() }()

		if _, err := ag.Run(context.Background(), "read the image", aharness.RunOptions{}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := lastToolCode(t, ag); got != "ok" {
			t.Fatalf("image model tool code = %q, want ok", got)
		}

		if _, err := ag.Run(context.Background(), "read the image again", aharness.RunOptions{
			Model: imageCapabilityModel(&no, "a.png"),
		}); err != nil {
			t.Fatalf("Run with override: %v", err)
		}
		if got := lastToolCode(t, ag); got != "unavailable" {
			t.Errorf("after switching to a text-only model, tool code = %q, want unavailable", got)
		}
	})
}

// lastToolCode returns the code of the most recent read_image tool result in the
// live session transcript.
func lastToolCode(t *testing.T, ag *aharness.Agent) string {
	t.Helper()
	items, err := ag.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	code := ""
	for _, item := range items {
		if item.Kind == aharness.TranscriptTool && item.Name == "read_image" {
			code = item.Code
		}
	}
	return code
}
