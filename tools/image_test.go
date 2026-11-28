package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	stdimage "image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// solidPNG renders a w x h PNG with a coarse gradient so encoded size tracks
// pixel count without relying on a fixture file.
func solidPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
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

func solidJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestReadImagePassthrough(t *testing.T) {
	r, dir := newTestRegistry(t)
	src := solidPNG(t, 64, 48)
	mustWriteFile(t, dir, "small.png", src)

	res := executeArgs(t, r, "read_image", map[string]any{"intent": "look", "path": "small.png"})
	if !res.OK {
		t.Fatalf("read_image failed: code=%s error=%s", res.Code, res.Error)
	}
	if len(res.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(res.Images))
	}
	img := res.Images[0]
	if img.MediaType != "image/png" || img.Width != 64 || img.Height != 48 {
		t.Errorf("image = %s %dx%d, want image/png 64x48", img.MediaType, img.Width, img.Height)
	}
	if !bytes.Equal(img.Data, src) {
		t.Errorf("passthrough changed the bytes")
	}
	if !strings.Contains(res.Output, "small.png: image/png 64x48") ||
		!strings.Contains(res.Output, "passthrough") {
		t.Errorf("caption = %q, want the passthrough summary", res.Output)
	}
}

func TestReadImageDownscalesLargeSource(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "big.jpg", solidJPEG(t, 4032, 3024))

	res := executeArgs(t, r, "read_image", map[string]any{"path": "big.jpg"})
	if !res.OK {
		t.Fatalf("read_image failed: code=%s error=%s", res.Code, res.Error)
	}
	img := res.Images[0]
	if img.MediaType != "image/jpeg" {
		t.Errorf("media type = %s, want image/jpeg", img.MediaType)
	}
	if img.Width > maxImageEdge || img.Height > maxImageEdge {
		t.Errorf("delivered %dx%d exceeds the %d px edge cap", img.Width, img.Height, maxImageEdge)
	}
	if int64(img.Width)*int64(img.Height) > maxImagePixels {
		t.Errorf("delivered %dx%d exceeds the %d px cap", img.Width, img.Height, maxImagePixels)
	}
	if len(img.Data) > MaxImageBytes {
		t.Errorf("delivered %d bytes exceeds the %d byte cap", len(img.Data), MaxImageBytes)
	}
	if !strings.Contains(res.Output, "->") || !strings.Contains(res.Output, "4032x3024") {
		t.Errorf("caption = %q, want source and delivered dimensions", res.Output)
	}
	if !strings.Contains(string(res.Details), `"transform":"downscaled"`) {
		t.Errorf("details = %s, want transform=downscaled", res.Details)
	}
}

func TestReadImageIsDeterministic(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "big.jpg", solidJPEG(t, 3000, 2000))

	first := executeArgs(t, r, "read_image", map[string]any{"path": "big.jpg"})
	second := executeArgs(t, r, "read_image", map[string]any{"path": "big.jpg"})
	if !first.OK || !second.OK {
		t.Fatalf("read_image failed: %s / %s", first.Error, second.Error)
	}
	sumFirst := sha256.Sum256(first.Images[0].Data)
	sumSecond := sha256.Sum256(second.Images[0].Data)
	if sumFirst != sumSecond {
		t.Errorf("two reads produced different bytes: %s vs %s",
			hex.EncodeToString(sumFirst[:]), hex.EncodeToString(sumSecond[:]))
	}
	if !strings.Contains(string(first.Details), hex.EncodeToString(sumFirst[:])) {
		t.Errorf("details does not record the delivered digest: %s", first.Details)
	}
}

func TestReadImageRejectsUnsupported(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "notes.txt", []byte("hello\n"))
	mustWriteFile(t, dir, "shape.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	mustWriteFile(t, dir, "photo.webp", append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 32)...))

	for _, name := range []string{"notes.txt", "shape.svg", "photo.webp"} {
		res := executeArgs(t, r, "read_image", map[string]any{"path": name})
		if res.OK {
			t.Errorf("read_image(%s) OK = true, want rejection", name)
			continue
		}
		if res.Code != CodeInvalidImage {
			t.Errorf("read_image(%s) code = %s, want %s", name, res.Code, CodeInvalidImage)
		}
		if len(res.Images) != 0 {
			t.Errorf("read_image(%s) returned images with a failure", name)
		}
	}
	if res := executeArgs(t, r, "read_image", map[string]any{"path": "notes.txt"}); !strings.Contains(res.Error, "read_file") {
		t.Errorf("text rejection error = %q, want a read_file hint", res.Error)
	}
}

func TestReadImageRejectsOversizedSource(t *testing.T) {
	r, dir := newTestRegistry(t)
	// 5000x4000 is 20 MP, above the 16 MP source cap.
	mustWriteFile(t, dir, "huge.png", solidPNG(t, 5000, 4000))
	res := executeArgs(t, r, "read_image", map[string]any{"path": "huge.png"})
	if res.OK {
		t.Fatalf("read_image accepted a 20 MP source")
	}
	if res.Code != CodeTooLarge {
		t.Errorf("code = %s, want %s", res.Code, CodeTooLarge)
	}
	if len(res.Images) != 0 {
		t.Errorf("oversized source returned an image")
	}
}

func TestReadImageRejectsCorruptHeader(t *testing.T) {
	r, dir := newTestRegistry(t)
	// A valid PNG signature with a truncated IHDR.
	mustWriteFile(t, dir, "broken.png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 8)...))
	res := executeArgs(t, r, "read_image", map[string]any{"path": "broken.png"})
	if res.OK {
		t.Fatalf("read_image accepted a corrupt header")
	}
	if res.Code != CodeInvalidImage {
		t.Errorf("code = %s, want %s", res.Code, CodeInvalidImage)
	}
}

func TestReadImageMissingAndContainment(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "a.png", solidPNG(t, 8, 8))

	if res := executeArgs(t, r, "read_image", map[string]any{"path": "absent.png"}); res.Code != CodeNotFound {
		t.Errorf("missing file code = %s, want %s", res.Code, CodeNotFound)
	}
	if res := executeArgs(t, r, "read_image", map[string]any{"path": "../escape.png"}); res.Code != CodeOutsideWorkspace {
		t.Errorf("escape code = %s, want %s", res.Code, CodeOutsideWorkspace)
	}
	if res := executeArgs(t, r, "read_image", map[string]any{"path": "."}); res.Code != CodeInvalidArgs {
		t.Errorf("directory code = %s, want %s", res.Code, CodeInvalidArgs)
	}
	if res := executeArgs(t, r, "read_image", map[string]any{"path": ""}); res.Code != CodeInvalidArgs {
		t.Errorf("empty path code = %s, want %s", res.Code, CodeInvalidArgs)
	}
}

func TestReadImageUnavailableWithoutModelSupport(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "a.png", solidPNG(t, 8, 8))
	no := false
	r.opts.ImageInput = &no
	res := executeArgs(t, r, "read_image", map[string]any{"path": "a.png"})
	if res.OK {
		t.Fatalf("read_image ran without model support")
	}
	if res.Code != CodeUnavailable {
		t.Errorf("code = %s, want %s", res.Code, CodeUnavailable)
	}
}

func TestSetImageInputOverridesForTheRun(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "a.png", solidPNG(t, 8, 8))

	// Construction-time gate rejects; a per-run override to unknown must let the
	// tool run, which is what a mid-session switch to an unknown model means.
	no := false
	r.SetImageInput(&no)
	if res := executeArgs(t, r, "read_image", map[string]any{"path": "a.png"}); res.Code != CodeUnavailable {
		t.Fatalf("code = %s, want %s after SetImageInput(false)", res.Code, CodeUnavailable)
	}
	r.SetImageInput(nil)
	if res := executeArgs(t, r, "read_image", map[string]any{"path": "a.png"}); !res.OK {
		t.Fatalf("read_image failed after clearing the gate: code=%s error=%s", res.Code, res.Error)
	}

	// An explicit true keeps it enabled, and the override is copied so a caller
	// mutating its own variable cannot change the registry underneath it.
	yes := true
	r.SetImageInput(&yes)
	yes = false
	if res := executeArgs(t, r, "read_image", map[string]any{"path": "a.png"}); !res.OK {
		t.Errorf("registry observed a caller mutation of the capability pointer")
	}
}

func TestImageInputOptionIsCopied(t *testing.T) {
	no := false
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "a.png", solidPNG(t, 8, 8))
	r.SetImageInput(&no)
	no = true
	if res := executeArgs(t, r, "read_image", map[string]any{"path": "a.png"}); res.OK {
		t.Error("registry followed a mutation of the caller's capability value")
	}
}

func solidGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	img := stdimage.NewPaletted(stdimage.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

func TestReadImageGIFFirstFrame(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "anim.gif", solidGIF(t, 32, 24))

	res := executeArgs(t, r, "read_image", map[string]any{"path": "anim.gif"})
	if !res.OK {
		t.Fatalf("read_image(gif) failed: code=%s error=%s", res.Code, res.Error)
	}
	// A GIF is re-encoded as JPEG because the model adapters forward only PNG
	// and JPEG.
	if got := res.Images[0].MediaType; got != "image/jpeg" {
		t.Errorf("gif delivered as %s, want image/jpeg", got)
	}
}

func TestNormalizeImageAppliesReadImageCaps(t *testing.T) {
	tests := []struct {
		name      string
		src       []byte
		wantErr   error
		wantMedia string
	}{
		{"passthrough png", solidPNG(t, 64, 48), nil, "image/png"},
		{"downscaled jpeg", solidJPEG(t, 3000, 2000), nil, "image/jpeg"},
		{"gif re-encoded", solidGIF(t, 32, 24), nil, "image/jpeg"},
		{"text bytes", []byte("hello\n"), ErrInvalidImage, ""},
		{"corrupt png header", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 8)...), ErrInvalidImage, ""},
		{"empty", nil, ErrInvalidImage, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestRegistry(t)
			img, err := r.NormalizeImage("screenshot", tc.src)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("NormalizeImage error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if len(img.Data) != 0 {
					t.Errorf("returned %d bytes with an error", len(img.Data))
				}
				return
			}
			if img.MediaType != tc.wantMedia {
				t.Errorf("media type = %s, want %s", img.MediaType, tc.wantMedia)
			}
			if img.Width <= 0 || img.Height <= 0 || img.Width > maxImageEdge || img.Height > maxImageEdge {
				t.Errorf("dimensions = %dx%d, want within the %d px edge", img.Width, img.Height, maxImageEdge)
			}
			if int64(img.Width)*int64(img.Height) > maxImagePixels {
				t.Errorf("%dx%d exceeds the %d px cap", img.Width, img.Height, maxImagePixels)
			}
			if len(img.Data) == 0 || len(img.Data) > MaxImageBytes {
				t.Errorf("delivered %d bytes, want 1..%d", len(img.Data), MaxImageBytes)
			}
			if want := fmt.Sprintf("%x", sha256.Sum256(img.Data)); img.SHA256 != want {
				t.Errorf("SHA256 = %q, want the digest of the delivered bytes", img.SHA256)
			}
		})
	}
}

func TestNormalizeImageRejectsWhenTheModelTakesNoImages(t *testing.T) {
	r, _ := newTestRegistry(t)
	no := false
	r.SetImageInput(&no)
	if _, err := r.NormalizeImage("screenshot", solidPNG(t, 8, 8)); !errors.Is(err, ErrNoImageSupport) {
		t.Fatalf("error = %v, want ErrNoImageSupport", err)
	}
}

func TestFitImage(t *testing.T) {
	tests := []struct {
		w, h, wantW, wantH int
	}{
		{800, 600, 800, 600},
		// 4096x2048 is 8.4 MP, so the 2 MP cap binds before the 2048 px edge.
		{4096, 2048, 2000, 1000},
		{2048, 4096, 1000, 2000},
		{4000, 4000, 1414, 1414},
	}
	for _, tc := range tests {
		gotW, gotH := fitImage(tc.w, tc.h)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Errorf("fitImage(%d,%d) = %dx%d, want %dx%d", tc.w, tc.h, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
}
