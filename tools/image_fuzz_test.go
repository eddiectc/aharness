package tools

import (
	"bytes"
	stdimage "image"
	"image/jpeg"
	"image/png"
	"testing"
)

// FuzzNormalizeImage covers the image trust boundary: arbitrary bytes are
// sniffed, header-decoded, and possibly fully decoded. The contract is that it
// never panics and either returns an error or a bounded image.
func FuzzNormalizeImage(f *testing.F) {
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte("\xff\xd8\xff\xe0"))
	f.Add([]byte("GIF89a"))
	f.Add([]byte("RIFF\x00\x00\x00\x00WEBP"))
	f.Add(solidPNGBytes())
	f.Add(solidJPEGBytes())

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip("input exceeds the fuzz budget")
		}
		img, err := normalizeImage("fuzz", data)
		if err != nil {
			if len(img.Data) != 0 {
				t.Fatalf("normalizeImage returned data with an error")
			}
			return
		}
		if len(img.Data) > maxImageBytes {
			t.Fatalf("delivered %d bytes, cap %d", len(img.Data), maxImageBytes)
		}
		if img.Width <= 0 || img.Height <= 0 {
			t.Fatalf("delivered %dx%d", img.Width, img.Height)
		}
		if img.MediaType != "image/png" && img.MediaType != "image/jpeg" {
			t.Fatalf("delivered unsupported media type %q", img.MediaType)
		}
	})
}

// solidPNGBytes and solidJPEGBytes provide deterministic seeds without a
// testing.T, so the fuzz target can use them.
func solidPNGBytes() []byte {
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, 16, 16))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func solidJPEGBytes() []byte {
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, 16, 16))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
