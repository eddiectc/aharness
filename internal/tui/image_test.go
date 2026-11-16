package tui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/model"
	"github.com/eddiectc/aharness/internal/tools"
)

func writeTestPNG(t *testing.T, path string, width, height int, c color.Color) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func imageTestApp(t *testing.T, md string) (*App, *manualHost, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := tools.OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	host := newManualHost()
	host.ws = ws
	host.info.Workspace = dir
	host.history = []agent.HistoryItem{{Kind: agent.HistoryAssistant, Text: md}}
	app := newTestApp(t, host)
	app.imageProtocol = "glyph"
	app.ensureContent()
	return app, host, dir
}

func TestImageBlockRendersGlyphThumbnailAndMetadata(t *testing.T) {
	app, _, dir := imageTestApp(t, "![chart](chart.png)")
	writeTestPNG(t, filepath.Join(dir, "chart.png"), 64, 32, color.RGBA{R: 255, A: 255})
	app.invalidateRenders()
	app.ensureContent()
	if len(app.images) != 1 {
		t.Fatalf("images = %d", len(app.images))
	}
	refs := app.Images()
	if len(refs) != 1 || refs[0].width <= 0 || refs[0].height <= 0 || refs[0].protocol != "glyph" {
		t.Fatalf("image refs = %+v", refs)
	}
	lines := strings.Join(app.transcriptLines(), "\n")
	if !strings.Contains(lines, "chart") || !strings.Contains(lines, "▄") {
		t.Fatalf("thumbnail missing from transcript:\n%s", lines)
	}
	if !strings.Contains(app.images[0].alt, "chart") {
		t.Fatalf("alt text = %q", app.images[0].alt)
	}
}

func TestImageRejectsUnsafeAndMissingPaths(t *testing.T) {
	for _, path := range []string{"missing.png", "../outside.png", "/etc/passwd.png", ".git/x.png", "https://example.com/x.png"} {
		app, _, _ := imageTestApp(t, "![x]("+path+")")
		if len(app.images) != 1 {
			t.Fatalf("%s: images = %d", path, len(app.images))
		}
		if app.images[0].errText == "" {
			t.Fatalf("%s: expected a placeholder error, got %+v", path, app.images[0])
		}
		lines := strings.Join(app.transcriptLines(), "\n")
		if !strings.Contains(lines, "image ") {
			t.Fatalf("%s: placeholder missing:\n%s", path, lines)
		}
	}
}

func TestImageRejectsSymlinkAndUnsupportedAndOversized(t *testing.T) {
	app, _, dir := imageTestApp(t, "![x](link.png)")
	outside := filepath.Join(t.TempDir(), "outside.png")
	writeTestPNG(t, outside, 2, 2, color.White)
	if err := os.Symlink(outside, filepath.Join(dir, "link.png")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	app.invalidateRenders()
	app.ensureContent()
	if len(app.images) != 1 || app.images[0].errText == "" {
		t.Fatalf("symlink accepted: %+v", app.images)
	}

	app2, _, dir2 := imageTestApp(t, "![x](bad.svg)")
	if err := os.WriteFile(filepath.Join(dir2, "bad.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	app2.invalidateRenders()
	app2.ensureContent()
	if len(app2.images) != 1 || !strings.Contains(app2.images[0].errText, "unsupported") {
		t.Fatalf("SVG accepted: %+v", app2.images)
	}

	app3, _, dir3 := imageTestApp(t, "![x](huge.png)")
	writeTestPNG(t, filepath.Join(dir3, "huge.png"), maxImageDimension+1, 1, color.Black)
	app3.invalidateRenders()
	app3.ensureContent()
	if len(app3.images) != 1 || !strings.Contains(app3.images[0].errText, "cap") {
		t.Fatalf("oversized image accepted: %+v", app3.images)
	}
}

func TestImageViewerOpensAndCloses(t *testing.T) {
	app, _, dir := imageTestApp(t, "![chart](chart.png)")
	writeTestPNG(t, filepath.Join(dir, "chart.png"), 16, 16, color.RGBA{G: 255, A: 255})
	app.invalidateRenders()
	app.ensureContent()
	press(t, app, "ctrl+g")
	if app.overlay.kind != OverlayImage {
		t.Fatalf("overlay = %q", app.overlay.kind)
	}
	if view := app.overlay.image.view(); view == "" {
		t.Fatal("image viewer is empty")
	}
	// Enter toggles fit/actual and must not close the viewer.
	press(t, app, "enter")
	if app.overlay.kind != OverlayImage || app.overlay.image.fit {
		t.Fatalf("viewer toggle failed: %q fit=%v", app.overlay.kind, app.overlay.image.fit)
	}
	press(t, app, "ctrl+g")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("Ctrl-G toggle did not close the viewer: %q", app.overlay.kind)
	}
	press(t, app, "ctrl+g")
	if app.overlay.kind != OverlayImage {
		t.Fatalf("Ctrl-G did not reopen the viewer: %q", app.overlay.kind)
	}
	press(t, app, "esc")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("viewer did not close: %q", app.overlay.kind)
	}
}

func TestImageProtocolNoneUsesPlaceholder(t *testing.T) {
	app, _, _ := imageTestApp(t, "![x](chart.png)")
	app.imageProtocol = "none"
	app.invalidateRenders()
	app.ensureContent()
	if len(app.images) != 1 || !strings.Contains(app.images[0].errText, "disabled") {
		t.Fatalf("protocol none: %+v", app.images)
	}
}

func TestFrameProtocolContainsNoRawImageBytes(t *testing.T) {
	app, _, dir := imageTestApp(t, "![chart](chart.png)")
	writeTestPNG(t, filepath.Join(dir, "chart.png"), 8, 8, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	app.invalidateRenders()
	app.ensureContent()
	data, err := os.ReadFile(filepath.Join(dir, "chart.png"))
	if err != nil {
		t.Fatal(err)
	}
	signature := string(data[:8])
	lines := strings.Join(app.transcriptLines(), "\n")
	if strings.Contains(lines, signature) {
		t.Fatal("transcript contains raw PNG bytes")
	}
	_ = model.Usage{}
}
