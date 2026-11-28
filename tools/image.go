package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	stdimage "image"
	_ "image/gif" // registers the GIF decoder for DecodeConfig and Decode
	"image/jpeg"
	_ "image/png" // registers the PNG decoder for DecodeConfig and Decode
	"math"
	"net/http"
	"strings"

	"github.com/eddiectc/aharness/model"
)

// Image limits. The source caps match read_file; the delivered caps are well
// inside every provider ceiling measured against the opencode-go gateway, where
// image token cost saturates near 2 MP and a side above 8192 px is rejected.
const (
	maxImageSourcePixels = 16_000_000
	maxImageEdge         = 2048
	maxImagePixels       = 2_000_000
	// MaxImageBytes is the delivered image cap. Every producer normalizes to it
	// and the session recorder drops an entry above it.
	MaxImageBytes = 512 << 10
	minImageEdge  = 256
)

// Transform names recorded in details.
const (
	transformPassthrough  = "passthrough"
	transformRecompressed = "recompressed"
	transformDownscaled   = "downscaled"
)

// readImageArgs is the typed argument shape for read_image. It must agree with
// the read_image schema in schemas.go.
type readImageArgs struct {
	Intent string `json:"intent,omitempty"`
	Path   string `json:"path"`
}

// imageDetails is the structured summary recorded beside the image.
type imageDetails struct {
	SourceMediaType string `json:"source_media_type"`
	SourceWidth     int    `json:"source_width"`
	SourceHeight    int    `json:"source_height"`
	SourceBytes     int    `json:"source_bytes"`
	MediaType       string `json:"media_type"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	Bytes           int    `json:"bytes"`
	SHA256          string `json:"sha256"`
	Transform       string `json:"transform"`
}

func (r *Registry) executeReadImage(ctx context.Context, args json.RawMessage) Result {
	if err := ctx.Err(); err != nil {
		return errorResult(err)
	}
	var a readImageArgs
	if err := decodeArgs("read_image", args, &a); err != nil {
		return Failure(CodeInvalidArgs, err.Error())
	}
	if a.Path == "" {
		return invalidArgs("read_image", "path is required")
	}
	if r.opts.ImageInput != nil && !*r.opts.ImageInput {
		return errorResult(ErrNoImageSupport)
	}

	src, err := r.readCappedFile(a.Path, maxReadFileBytes)
	if err != nil {
		return errorResult(err)
	}
	img, err := normalizeImageBytes(a.Path, src, "; use read_file for text")
	if err != nil {
		return errorResult(err)
	}
	return Result{
		OK:     true,
		Code:   CodeOK,
		Output: imageCaption(a.Path, img),
		Images: []Image{{
			MediaType: img.MediaType,
			Data:      img.Data,
			Width:     img.Width,
			Height:    img.Height,
			SHA256:    img.sha256,
		}},
		Details: img.details,
	}
}

// NormalizeImage prepares one caller-supplied image for the model. It applies
// the same sniffing, decode, downscale, and delivered caps as read_image, so a
// caller never hands the model a mislabeled or oversized payload. label names
// the source in the returned error and is never resolved as a path. The error
// wraps ErrInvalidImage, ErrTooLarge, or ErrNoImageSupport, and a returned image
// is always recordable.
func (r *Registry) NormalizeImage(label string, src []byte) (Image, error) {
	if r.opts.ImageInput != nil && !*r.opts.ImageInput {
		return Image{}, ErrNoImageSupport
	}
	if len(src) == 0 {
		return Image{}, fmt.Errorf("%w: %s is empty", ErrInvalidImage, label)
	}
	img, err := normalizeImageBytes(label, src, "")
	if err != nil {
		return Image{}, err
	}
	return Image{
		MediaType: img.MediaType,
		Data:      img.Data,
		Width:     img.Width,
		Height:    img.Height,
		SHA256:    img.sha256,
	}, nil
}

// normalizedImage is one image ready for the model.
type normalizedImage struct {
	MediaType string
	Data      []byte
	Width     int
	Height    int
	sha256    string
	details   json.RawMessage
}

// normalizeImageBytes sniffs, decodes, and bounds one image. The extension is
// never trusted: the media type comes from the bytes. label names the source in
// the error, and textHint is appended when the bytes look like text, which is
// how read_image points at read_file.
func normalizeImageBytes(label string, src []byte, textHint string) (normalizedImage, error) {
	sniffLen := min(len(src), 512)
	sniff := http.DetectContentType(src[:sniffLen])
	var mediaType string
	switch sniff {
	case model.MediaTypePNG, model.MediaTypeJPEG, "image/gif":
		mediaType = sniff
	default:
		hint := ""
		if strings.HasPrefix(sniff, "text/") {
			hint = textHint
		}
		return normalizedImage{}, fmt.Errorf("%w: %s is %s; supported formats are PNG, JPEG, and GIF%s",
			ErrInvalidImage, label, displayMediaType(sniff), hint)
	}

	cfg, _, err := stdimage.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return normalizedImage{}, fmt.Errorf("%w: %s: %w", ErrInvalidImage, label, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return normalizedImage{}, fmt.Errorf("%w: %s has no dimensions", ErrInvalidImage, label)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImageSourcePixels {
		return normalizedImage{}, fmt.Errorf("%w: %s is %dx%d (%d pixels, limit %d)",
			ErrTooLarge, label, cfg.Width, cfg.Height, int64(cfg.Width)*int64(cfg.Height), maxImageSourcePixels)
	}

	// A source that already fits every delivered limit is forwarded byte for
	// byte, so a screenshot is not recompressed for nothing.
	if (mediaType == model.MediaTypePNG || mediaType == model.MediaTypeJPEG) &&
		cfg.Width <= maxImageEdge && cfg.Height <= maxImageEdge &&
		int64(cfg.Width)*int64(cfg.Height) <= maxImagePixels && len(src) <= MaxImageBytes {
		return newNormalizedImage(label, mediaType, cfg.Width, cfg.Height, len(src), src, mediaType, cfg.Width, cfg.Height, transformPassthrough)
	}

	decoded, _, err := stdimage.Decode(bytes.NewReader(src))
	if err != nil {
		return normalizedImage{}, fmt.Errorf("%w: %s: %w", ErrInvalidImage, label, err)
	}

	w, h := fitImage(decoded.Bounds().Dx(), decoded.Bounds().Dy())
	transform := transformRecompressed
	if w != decoded.Bounds().Dx() || h != decoded.Bounds().Dy() {
		transform = transformDownscaled
	}
	for {
		scaled := decoded
		if w != decoded.Bounds().Dx() || h != decoded.Bounds().Dy() {
			scaled = boxResize(decoded, w, h)
		}
		for _, quality := range []int{85, 75, 65, 55} {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: quality}); err != nil {
				return normalizedImage{}, fmt.Errorf("encode %s: %w", label, err)
			}
			if buf.Len() <= MaxImageBytes {
				return newNormalizedImage(label, mediaType, cfg.Width, cfg.Height, len(src),
					buf.Bytes(), model.MediaTypeJPEG, w, h, transform)
			}
		}
		nextW, nextH := shrinkStep(w, h)
		if nextW == w && nextH == h {
			return normalizedImage{}, fmt.Errorf("%w: %s cannot be reduced below %d bytes without going under %d px",
				ErrTooLarge, label, MaxImageBytes, minImageEdge)
		}
		w, h = nextW, nextH
		transform = transformDownscaled
	}
}

func newNormalizedImage(label, srcMedia string, srcW, srcH, srcBytes int, data []byte, media string, w, h int, transform string) (normalizedImage, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	details, err := json.Marshal(imageDetails{
		SourceMediaType: srcMedia,
		SourceWidth:     srcW,
		SourceHeight:    srcH,
		SourceBytes:     srcBytes,
		MediaType:       media,
		Width:           w,
		Height:          h,
		Bytes:           len(data),
		SHA256:          digest,
		Transform:       transform,
	})
	if err != nil {
		return normalizedImage{}, fmt.Errorf("marshal image details: %w", err)
	}
	return normalizedImage{MediaType: media, Data: data, Width: w, Height: h, sha256: digest, details: details}, nil
}

// displayMediaType keeps an untrusted sniffed type readable in one message.
func displayMediaType(sniff string) string {
	if sniff == "" {
		return "an unknown type"
	}
	return sniff
}

// fitImage scales w x h down to both delivered limits, never up.
func fitImage(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return 1, 1
	}
	scale := 1.0
	if longest := max(w, h); longest > maxImageEdge {
		scale = float64(maxImageEdge) / float64(longest)
	}
	if int64(w)*int64(h) > maxImagePixels {
		if s := math.Sqrt(float64(maxImagePixels) / float64(int64(w)*int64(h))); s < scale {
			scale = s
		}
	}
	if scale >= 1 {
		return w, h
	}
	return max(1, int(math.Floor(float64(w)*scale))), max(1, int(math.Floor(float64(h)*scale)))
}

// shrinkStep reduces both edges by a quarter, stopping at the floor.
func shrinkStep(w, h int) (int, int) {
	nw := max(minImageEdge, int(float64(w)*0.75))
	nh := max(minImageEdge, int(float64(h)*0.75))
	if nw == w && nh == h {
		return nw, nh
	}
	if w <= minImageEdge && h <= minImageEdge {
		return w, h
	}
	nw = min(nw, w)
	nh = min(nh, h)
	return nw, nh
}

// boxResize averages every source pixel that falls inside each destination
// pixel. It is deterministic, allocation-light, and adequate for screenshots
// and photographs; a bilinear or Lanczos filter would need a dependency.
// Transparent source pixels are composited over white so JPEG has a sane color.
func boxResize(src stdimage.Image, dw, dh int) *stdimage.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := stdimage.NewRGBA(stdimage.Rect(0, 0, dw, dh))
	x0s, x1s := sourceSpans(sw, dw)
	y0s, y1s := sourceSpans(sh, dh)
	for dy := 0; dy < dh; dy++ {
		y0, y1 := y0s[dy], y1s[dy]
		for dx := 0; dx < dw; dx++ {
			x0, x1 := x0s[dx], x1s[dx]
			var r, g, bl, a uint64
			var n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					cr, cg, cb, ca := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
					r += uint64(cr >> 8)
					g += uint64(cg >> 8)
					bl += uint64(cb >> 8)
					a += uint64(ca >> 8)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			alpha := a / n
			// Composite over white: out = c*alpha + 255*(1-alpha).
			r = (r/n*alpha + 255*(255-alpha)) / 255
			g = (g/n*alpha + 255*(255-alpha)) / 255
			bl = (bl/n*alpha + 255*(255-alpha)) / 255
			i := dst.PixOffset(dx, dy)
			dst.Pix[i] = uint8(r)
			dst.Pix[i+1] = uint8(g)
			dst.Pix[i+2] = uint8(bl)
			dst.Pix[i+3] = 0xFF
		}
	}
	return dst
}

// sourceSpans returns the half-open source range [x0,x1) for each destination
// index, never empty so every destination pixel samples at least one source.
func sourceSpans(src, dst int) ([]int, []int) {
	x0s := make([]int, dst)
	x1s := make([]int, dst)
	for i := 0; i < dst; i++ {
		x0 := i * src / dst
		x1 := (i + 1) * src / dst
		if x1 <= x0 {
			x1 = x0 + 1
		}
		if x1 > src {
			x1 = src
		}
		x0s[i] = x0
		x1s[i] = x1
	}
	return x0s, x1s
}

// imageCaption renders the one-line summary the model and a host see beside the
// image bytes.
func imageCaption(path string, img normalizedImage) string {
	var d imageDetails
	if err := json.Unmarshal(img.details, &d); err != nil {
		return fmt.Sprintf("%s: %s %dx%d", path, img.MediaType, img.Width, img.Height)
	}
	kib := float64(len(img.Data)) / 1024
	if d.Transform == transformPassthrough {
		return fmt.Sprintf("%s: %s %dx%d (%.0f KiB, passthrough)", path, d.MediaType, img.Width, img.Height, kib)
	}
	return fmt.Sprintf("%s: %s %dx%d -> %s %dx%d (%.0f KiB, %s)",
		path, d.SourceMediaType, d.SourceWidth, d.SourceHeight, d.MediaType, img.Width, img.Height, kib, d.Transform)
}
