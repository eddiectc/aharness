package agent

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// imageToolTurn appends one assistant read_image call and its recorded result.
func imageToolTurn(t *testing.T, events []session.Event, turn int, id string, images []toolImageData) []session.Event {
	t.Helper()
	events = append(events, newTestEvent(t, session.TypeModelResponse, turn, string(model.RoleAssistant), modelResponseData{
		ToolCalls:    []loggedToolCall{{ID: id, Name: "read_image", Arguments: []byte(`{"path":"a.png"}`)}},
		FinishReason: model.FinishToolCalls,
		Usage:        model.Usage{InputTokens: 1, OutputTokens: 1},
	}))
	events = append(events, newTestEvent(t, session.TypeToolResult, turn, string(model.RoleTool), toolResultData{
		CallID: id,
		Name:   "read_image",
		OK:     true,
		Code:   tools.CodeOK,
		Output: "a.png: image/png 4x2 (1 KiB, passthrough)",
		Images: images,
	}))
	return events
}

func recordedImage(t *testing.T, media string, data []byte, w, h int) toolImageData {
	t.Helper()
	return toolImageData{
		MediaType: media,
		Data:      base64.StdEncoding.EncodeToString(data),
		Width:     w,
		Height:    h,
		Bytes:     len(data),
	}
}

func TestProjectToolImages(t *testing.T) {
	raw := []byte("\x89PNG\r\n\x1a\npayload")
	events := []session.Event{
		startEvent(t, "/workspace/project"),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
	}
	events = imageToolTurn(t, events, 1, "call_img", []toolImageData{recordedImage(t, model.MediaTypePNG, raw, 4, 2)})

	proj, err := Project(events, Limits{MaxInputTokens: 1_000_000, CompactionTarget: 1_000_000})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	var toolMsg *model.Message
	for i := range proj.Messages {
		if proj.Messages[i].Role == model.RoleTool {
			toolMsg = &proj.Messages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("projection has no tool message")
	}
	if len(toolMsg.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(toolMsg.Images))
	}
	img := toolMsg.Images[0]
	if img.MediaType != model.MediaTypePNG || img.Width != 4 || img.Height != 2 {
		t.Errorf("image = %s %dx%d, want image/png 4x2", img.MediaType, img.Width, img.Height)
	}
	if string(img.Data) != string(raw) {
		t.Errorf("image bytes did not round-trip")
	}
	if !strings.Contains(toolMsg.Content, "passthrough") {
		t.Errorf("tool content = %q, want the caption preserved", toolMsg.Content)
	}
}

func TestProjectRejectsCorruptImagePayload(t *testing.T) {
	tests := []struct {
		name  string
		image toolImageData
	}{
		{"bad base64", toolImageData{MediaType: model.MediaTypePNG, Data: "not base64!!", Width: 1, Height: 1}},
		{"empty data", toolImageData{MediaType: model.MediaTypePNG, Data: "", Width: 1, Height: 1}},
		{"bad media type", toolImageData{MediaType: "image/webp", Data: base64.StdEncoding.EncodeToString([]byte("x")), Width: 1, Height: 1}},
		{"zero width", toolImageData{MediaType: model.MediaTypePNG, Data: base64.StdEncoding.EncodeToString([]byte("x")), Width: 0, Height: 1}},
		{"byte mismatch", toolImageData{MediaType: model.MediaTypePNG, Data: base64.StdEncoding.EncodeToString([]byte("xy")), Width: 1, Height: 1, Bytes: 99}},
		{"absurd dimensions", toolImageData{MediaType: model.MediaTypePNG, Data: base64.StdEncoding.EncodeToString([]byte("x")), Width: 1 << 20, Height: 1 << 20}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events := []session.Event{
				startEvent(t, "/workspace/project"),
				newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
			}
			events = imageToolTurn(t, events, 1, "call_img", []toolImageData{tc.image})
			if _, err := Project(events, Limits{MaxInputTokens: 1_000_000, CompactionTarget: 1_000_000}); err == nil {
				t.Fatal("Project accepted a corrupt image payload")
			}
		})
	}
}

func TestProjectElidesOldImages(t *testing.T) {
	raw := []byte("\x89PNG\r\n\x1a\npayload")
	events := []session.Event{
		startEvent(t, "/workspace/project"),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
	}
	// A delivered-size image costs 2590 estimated tokens, so the target binds.
	events = imageToolTurn(t, events, 1, "call_img", []toolImageData{recordedImage(t, model.MediaTypePNG, raw, 2048, 976)})
	// Five more turns push the image outside the retained window.
	for turn := 2; turn <= 6; turn++ {
		events = append(events, newTestEvent(t, session.TypeModelResponse, turn, string(model.RoleAssistant), modelResponseData{
			Text: "done", FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 1, OutputTokens: 1},
		}))
	}
	// The image alone costs 2590 estimated tokens, so a 1000-token target forces
	// it out while the remaining text still fits.
	proj, err := Project(events, Limits{MaxInputTokens: 1000, CompactionTarget: 1000})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if proj.ElidedResults == 0 {
		t.Fatal("nothing was elided")
	}
	for _, msg := range proj.Messages {
		if len(msg.Images) != 0 {
			t.Fatalf("an elided tool result kept %d image(s)", len(msg.Images))
		}
		if strings.Contains(msg.Content, "image") && !strings.Contains(msg.Content, "elided") {
			t.Errorf("tool content = %q, want an elided-image marker", msg.Content)
		}
	}
}

func TestEstimateTokensCountsImages(t *testing.T) {
	text := []model.Message{{Role: model.RoleTool, Content: "ok"}}
	withImage := []model.Message{{Role: model.RoleTool, Content: "ok", Images: []model.ImagePart{
		{MediaType: model.MediaTypePNG, Data: make([]byte, 1<<20), Width: 2048, Height: 976},
	}}}
	textOnly := estimateTokens(text, 0)
	imageAware := estimateTokens(withImage, 0)
	// ceil(2048/28) * ceil(976/28) = 74 * 35 = 2590.
	if got := imageAware - textOnly; got != 2590 {
		t.Errorf("image token delta = %d, want 2590", got)
	}
}

func TestLoggableMessagesDropImages(t *testing.T) {
	msgs := []model.Message{{
		Role:    model.RoleTool,
		Content: "caption",
		Images:  []model.ImagePart{{MediaType: model.MediaTypePNG, Data: []byte("secret-pixels"), Width: 1, Height: 1}},
	}}
	out := loggableMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("loggable messages = %d, want 1", len(out))
	}
	if len(out[0].Images) != 0 {
		t.Errorf("logged message kept %d image(s)", len(out[0].Images))
	}
}

func TestReadImageIsReadOnly(t *testing.T) {
	if !isReadOnlyTool("read_image") {
		t.Error("read_image is not treated as read-only, so delegation would serialize")
	}
	if !isReadOnlyGrant([]string{"read_file", "read_image"}) {
		t.Error("a read_file,read_image grant is not read-only")
	}
}
