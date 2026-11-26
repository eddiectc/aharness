package agent

import (
	"bytes"
	stdimage "image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// writeWorkspacePNG drops a real PNG into the fixture workspace so read_image
// decodes rather than rejecting a fake header.
func writeWorkspacePNG(t *testing.T, dir, name string) {
	t.Helper()
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, 12, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 12; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 20), G: 40, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

// imageReaderDefinitions is the minimum needed to exercise the child gate: a
// read_image child and a plain reader that keeps the parent's model.
func imageReaderDefinitions() []AgentDefinition {
	return []AgentDefinition{
		{
			Name:        "vision",
			Description: "Test-only image reader on a different model.",
			Model:       "other-model",
			Tools:       []string{"read_image"},
			Prompt:      "Read the assigned image.\n",
		},
		{
			Name:        "inheritor",
			Description: "Test-only image reader on the parent model.",
			Tools:       []string{"read_image"},
			Prompt:      "Read the assigned image.\n",
		},
	}
}

// imageChildFixture builds a spawner whose parent grant includes read_image and
// whose resolver reports the given capability for a switched child model.
//
// For the parent's own model it mirrors the facade's resolver, which returns the
// parent's effective capability; imageInheritedForParent overrides that to nil to
// exercise the fallback where the child takes the capability from the inherited
// tool options instead.
func imageChildFixture(t *testing.T, parentCapability *bool, resolvedCapability *bool, parentModelCapability *bool) *spawnFixture {
	t.Helper()
	cfg := testConfig()
	cfg.Tools = tools.Names()
	f := newSpawnFixture(t, cfg)
	f.catalog = LoadAgents(imageReaderDefinitions(), t.TempDir())
	f.spawner.opts.Definitions = f.catalog
	f.spawner.opts.Config.Tools = tools.Names()
	f.spawner.opts.NewToolOptions = func(string, string, string) tools.Options {
		return tools.Options{ImageInput: parentCapability}
	}
	f.spawner.opts.Resolve = func(modelID, effort string) (ModelResolution, error) {
		f.resolvedMu.Lock()
		f.resolved = append(f.resolved, struct{ model, effort string }{modelID, effort})
		f.resolvedMu.Unlock()
		capability := resolvedCapability
		if modelID == "test-model" {
			capability = parentModelCapability
		}
		return ModelResolution{
			Dialect:    string(model.DialectChat),
			BaseURL:    "http://127.0.0.1:1",
			Limits:     testConfig().Limits,
			ImageInput: capability,
		}, nil
	}
	return f
}

// imageCallSteps scripts a child that calls read_image and then reports.
func imageCallSteps() []scriptStep {
	return []scriptStep{
		{resp: model.Response{
			ToolCalls:    []model.ToolCall{{ID: "img-1", Name: "read_image", Arguments: []byte(`{"path":"shot.png"}`)}},
			FinishReason: model.FinishToolCalls,
			Usage:        model.Usage{InputTokens: 5, OutputTokens: 2, Reported: true},
		}},
		{resp: model.Response{
			Text:         "the child saw it",
			FinishReason: model.FinishStop,
			Usage:        model.Usage{InputTokens: 6, OutputTokens: 3, Reported: true},
		}},
	}
}

// childToolResult returns the child's recorded read_image result.
func childToolResult(t *testing.T, f *spawnFixture, sessionID string) toolResultData {
	t.Helper()
	var found *toolResultData
	for _, ev := range f.readLog(t, sessionID) {
		if ev.Type != session.TypeToolResult {
			continue
		}
		data := decodePayload[toolResultData](t, ev)
		if data.Name == "read_image" {
			copied := data
			found = &copied
		}
	}
	if found == nil {
		t.Fatalf("child log has no read_image result")
	}
	return *found
}

// TestChildImageGateFollowsItsOwnModel is the end-to-end check the resolution
// unit test could not give: a spawned child that actually calls read_image,
// with the gate decided by the model the child runs on.
func TestChildImageGateFollowsItsOwnModel(t *testing.T) {
	no := false
	yes := true

	t.Run("child on a text-only model is gated", func(t *testing.T) {
		// The parent is image-capable, so only the child's own model can gate.
		f := imageChildFixture(t, &yes, &no, &yes)
		writeWorkspacePNG(t, f.workspace.Dir(), "shot.png")
		f.completer.steps = imageCallSteps()

		report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"},
			tools.SubagentSpec{Agent: "vision", Task: "read shot.png"}))
		if report.Status != tools.SubagentCompleted {
			t.Fatalf("report = %+v", report)
		}
		if got := childToolResult(t, f, report.Session); got.Code != tools.CodeUnavailable {
			t.Errorf("child read_image code = %q, want %q", got.Code, tools.CodeUnavailable)
		}
	})

	t.Run("child on an image model delivers the image", func(t *testing.T) {
		// The parent is text-only; a child that switches to a vision model must
		// still see the image, which is the case a frozen gate breaks.
		f := imageChildFixture(t, &no, &yes, &no)
		writeWorkspacePNG(t, f.workspace.Dir(), "shot.png")
		f.completer.steps = imageCallSteps()

		report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"},
			tools.SubagentSpec{Agent: "vision", Task: "read shot.png"}))
		if report.Status != tools.SubagentCompleted {
			t.Fatalf("report = %+v", report)
		}
		got := childToolResult(t, f, report.Session)
		if got.Code != tools.CodeOK {
			t.Fatalf("child read_image code = %q, want ok (error %q)", got.Code, got.Error)
		}
		if len(got.Images) != 1 {
			t.Fatalf("child recorded %d image(s), want 1", len(got.Images))
		}
		if got.Images[0].MediaType != model.MediaTypePNG {
			t.Errorf("media type = %q, want %q", got.Images[0].MediaType, model.MediaTypePNG)
		}
	})

	t.Run("inheriting child falls back to the inherited gate when the resolver is silent", func(t *testing.T) {
		f := imageChildFixture(t, &no, &yes, nil)
		writeWorkspacePNG(t, f.workspace.Dir(), "shot.png")
		f.completer.steps = imageCallSteps()

		// No definition model, so resolveAgent keeps the parent model and never
		// consults the resolver; the inherited option must decide.
		report := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"},
			tools.SubagentSpec{Agent: "inheritor", Task: "read shot.png"}))
		if report.Status != tools.SubagentCompleted {
			t.Fatalf("report = %+v", report)
		}
		if got := childToolResult(t, f, report.Session); got.Code != tools.CodeUnavailable {
			t.Errorf("inherited child read_image code = %q, want %q", got.Code, tools.CodeUnavailable)
		}
		if slices.ContainsFunc(f.resolved, func(r struct{ model, effort string }) bool {
			return r.model != "test-model"
		}) {
			t.Errorf("an inheriting child resolved a different model: %+v", f.resolved)
		}
	})
}
