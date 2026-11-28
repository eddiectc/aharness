package aharness_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/eddiectc/aharness"
	"github.com/eddiectc/aharness/session"
)

// capturePNG renders a w x h PNG with a coarse gradient so encoded size tracks
// pixel count without a fixture file.
func capturePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(((x / 17) * 31) ^ ((y / 19) * 13))
			img.Set(x, y, color.RGBA{R: v, G: v / 2, B: 255 - v, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// overPixelCapPNG rewrites a small PNG's IHDR to declare 5000x4000 (20 MP). The
// source cap is checked from the header, so no 80 MB encode is needed; the CRC
// is recomputed because the decoder verifies it.
func overPixelCapPNG(t *testing.T) []byte {
	t.Helper()
	out := capturePNG(t, 8, 8)
	binary.BigEndian.PutUint32(out[16:20], 5000)
	binary.BigEndian.PutUint32(out[20:24], 4000)
	binary.BigEndian.PutUint32(out[29:33], crc32.ChecksumIEEE(out[12:29]))
	return out
}

// screenshotTools registers one caller tool that returns src as its result.
func screenshotTools(t *testing.T, src []byte, calls *int) *aharness.ToolsConfig {
	t.Helper()
	return &aharness.ToolsConfig{Extra: []aharness.Tool{{
		Name:        "screenshot",
		Description: "Capture the page as an image.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Run: func(_ context.Context, _ aharness.ToolCall) (aharness.ToolResult, error) {
			*calls++
			result := aharness.TextResult("captured the page")
			result.Image = src
			return result, nil
		},
	}}}
}

// oneShotModel scripts one screenshot call followed by a final answer.
func oneShotModel() *scriptedModel {
	return &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls:    []aharness.ToolCall{{ID: "shot-1", Name: "screenshot"}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
}

// recordedImage is the images member of one recorded tool_result.
type recordedImage struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
}

// recordedToolResult is the part of a tool_result payload these tests assert.
type recordedToolResult struct {
	Name   string          `json:"name"`
	OK     bool            `json:"ok"`
	Code   string          `json:"code"`
	Output string          `json:"output"`
	Error  string          `json:"error"`
	Images []recordedImage `json:"images"`
}

func recordedToolResults(t *testing.T, stateDir, sessionID string) []recordedToolResult {
	t.Helper()
	events, err := session.Read(session.SessionPath(stateDir, sessionID))
	if err != nil {
		t.Fatalf("read session %s: %v", sessionID, err)
	}
	var out []recordedToolResult
	for _, ev := range events {
		if ev.Type != session.TypeToolResult {
			continue
		}
		var payload recordedToolResult
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("decode tool_result: %v", err)
		}
		out = append(out, payload)
	}
	return out
}

func TestCustomToolImageIsRecordedForTheModel(t *testing.T) {
	src := capturePNG(t, 64, 48)
	called := 0
	model := oneShotModel()
	ag, _, stateDir := newTestAgent(t, screenshotTools(t, src, &called), model)

	result, err := ag.Run(context.Background(), "capture the page", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != aharness.StopCompleted {
		t.Fatalf("status = %q (err %v)", result.Status, result.Err)
	}
	if called != 1 {
		t.Fatalf("tool ran %d times, want 1", called)
	}

	results := recordedToolResults(t, stateDir, ag.ID())
	if len(results) != 1 {
		t.Fatalf("recorded tool results = %d, want 1", len(results))
	}
	got := results[0]
	if !got.OK || got.Code != "ok" {
		t.Fatalf("recorded result = %+v, want ok", got)
	}
	if len(got.Images) != 1 {
		t.Fatalf("recorded images = %d, want 1", len(got.Images))
	}
	img := got.Images[0]
	if img.MediaType != "image/png" || img.Width != 64 || img.Height != 48 {
		t.Errorf("image = %s %dx%d, want image/png 64x48", img.MediaType, img.Width, img.Height)
	}
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		t.Fatalf("decode recorded image: %v", err)
	}
	if !bytes.Equal(data, src) {
		t.Errorf("an image inside every delivered cap was recompressed")
	}
	if img.Bytes != len(src) {
		t.Errorf("bytes = %d, want %d", img.Bytes, len(src))
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(src)); img.SHA256 != want {
		t.Errorf("sha256 = %q, want %q", img.SHA256, want)
	}

	// The next request carries the recorded image as an image part, so the model
	// sees the pixels in the same turn the tool result is projected.
	if len(model.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.requests))
	}
	var toolMsg *aharness.Message
	for i := range model.requests[1].Messages {
		if model.requests[1].Messages[i].Role == aharness.RoleTool {
			toolMsg = &model.requests[1].Messages[i]
		}
	}
	if toolMsg == nil {
		t.Fatalf("second request has no tool message: %+v", model.requests[1].Messages)
	}
	if len(toolMsg.Images) != 1 {
		t.Fatalf("tool message images = %d, want 1", len(toolMsg.Images))
	}
	part := toolMsg.Images[0]
	if part.MediaType != "image/png" || part.Width != 64 || part.Height != 48 {
		t.Errorf("image part = %s %dx%d, want image/png 64x48", part.MediaType, part.Width, part.Height)
	}
	if !bytes.Equal(part.Data, src) {
		t.Errorf("image part bytes do not match the delivered image")
	}
}

func TestCustomToolImageRejectionIsModelVisible(t *testing.T) {
	tests := []struct {
		name     string
		image    []byte
		wantCode string
	}{
		{"unsupported format", []byte("not an image\n"), "invalid_image"},
		{"corrupt png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 8)...), "invalid_image"},
		{"over the source pixel cap", overPixelCapPNG(t), "too_large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := 0
			ag, _, stateDir := newTestAgent(t, screenshotTools(t, tc.image, &called), oneShotModel())

			result, err := ag.Run(context.Background(), "capture the page", aharness.RunOptions{})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Status != aharness.StopCompleted {
				t.Fatalf("status = %q (err %v)", result.Status, result.Err)
			}

			results := recordedToolResults(t, stateDir, ag.ID())
			if len(results) != 1 {
				t.Fatalf("recorded tool results = %d, want 1", len(results))
			}
			got := results[0]
			if got.OK {
				t.Errorf("rejected image recorded as a success")
			}
			if got.Code != tc.wantCode {
				t.Errorf("code = %q, want %q (error %q)", got.Code, tc.wantCode, got.Error)
			}
			if len(got.Images) != 0 {
				t.Errorf("recorded %d images with a rejection", len(got.Images))
			}
			if got.Output != "captured the page" {
				t.Errorf("output = %q, want the caller's caption preserved", got.Output)
			}
		})
	}
}

// TestCallerModelReceivesReadImageResult is the regression for the bridge: a
// facade host's model used to get no image part at all, so the loop could answer
// about a picture it had never seen.
func TestCallerModelReceivesReadImageResult(t *testing.T) {
	workspace, stateDir := t.TempDir(), t.TempDir()
	writePNG(t, workspace, "a.png")
	model := readImageModel("a.png")
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(model),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()

	if _, err := ag.Run(context.Background(), "read the image", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.requests))
	}
	found := false
	for _, msg := range model.requests[1].Messages {
		if msg.Role != aharness.RoleTool || msg.Name != "read_image" {
			continue
		}
		found = true
		if len(msg.Images) != 1 {
			t.Fatalf("read_image message images = %d, want 1", len(msg.Images))
		}
		if part := msg.Images[0]; part.MediaType != "image/png" || part.Width != 16 || part.Height != 16 {
			t.Errorf("image part = %s %dx%d, want image/png 16x16", part.MediaType, part.Width, part.Height)
		}
	}
	if !found {
		t.Fatalf("second request has no read_image tool message: %+v", model.requests[1].Messages)
	}
}

func TestCustomToolImageRejectedWhenTheModelTakesNoImages(t *testing.T) {
	no := false
	modelCfg := testModel(oneShotModel())
	modelCfg.Spec.ImageInput = &no
	called := 0
	workspace, stateDir := t.TempDir(), t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     modelCfg,
		Tools:     screenshotTools(t, capturePNG(t, 8, 8), &called),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()

	if _, err := ag.Run(context.Background(), "capture the page", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	results := recordedToolResults(t, stateDir, ag.ID())
	if len(results) != 1 || results[0].Code != "unavailable" || len(results[0].Images) != 0 {
		t.Fatalf("recorded result = %+v, want unavailable with no image", results)
	}
}

func TestCustomToolWithoutImageRecordsNoImageMember(t *testing.T) {
	called := 0
	model := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls:    []aharness.ToolCall{{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"id":"1"}`)}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	ag, _, stateDir := newTestAgent(t, customTools(t, &called), model)

	if _, err := ag.Run(context.Background(), "look it up", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, err := session.Read(session.SessionPath(stateDir, ag.ID()))
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	results := 0
	for _, ev := range events {
		if ev.Type != session.TypeToolResult {
			continue
		}
		results++
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("decode tool_result: %v", err)
		}
		if _, ok := payload["images"]; ok {
			t.Errorf("tool_result carries an images member without an image: %s", ev.Data)
		}
	}
	if results != 1 {
		t.Fatalf("recorded tool results = %d, want 1", results)
	}
}

func TestCustomToolImageSurvivesResume(t *testing.T) {
	src := capturePNG(t, 32, 16)
	called := 0
	workspace, stateDir := t.TempDir(), t.TempDir()
	config := func(m aharness.Model) aharness.Config {
		return aharness.Config{
			Workspace: workspace,
			StateDir:  stateDir,
			UserAgent: "aharness-test/1.0",
			Model:     testModel(m),
			Tools:     screenshotTools(t, src, &called),
		}
	}

	ag, err := aharness.New(context.Background(), config(oneShotModel()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sessionID := ag.ID()
	if _, err := ag.Run(context.Background(), "capture the page", aharness.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	recorded := recordedToolResults(t, stateDir, sessionID)
	if len(recorded) != 1 || len(recorded[0].Images) != 1 {
		t.Fatalf("recorded tool results before resume = %+v, want one image", recorded)
	}

	// The resumed run rebuilds the projection from the log before its first
	// model call, so a malformed recorded image would fail the run here.
	resumed, err := aharness.Resume(context.Background(), config(&scriptedModel{responses: []aharness.Response{
		{Text: "still here", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}), sessionID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = resumed.Close() }()

	result, err := resumed.Run(context.Background(), "continue", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	if result.Status != aharness.StopCompleted {
		t.Fatalf("resumed status = %q (err %v)", result.Status, result.Err)
	}
	recorded = recordedToolResults(t, stateDir, sessionID)
	if len(recorded) != 1 || len(recorded[0].Images) != 1 {
		t.Errorf("recorded tool results after resume = %+v, want one image", recorded)
	}
}
