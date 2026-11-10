package tui

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image/color"
	"strconv"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"aharness/internal/model"
)

// CursorFrame is the protocol view of the terminal cursor.
type CursorFrame struct {
	Row     int    `json:"row"`
	Col     int    `json:"col"`
	Visible bool   `json:"visible"`
	Shape   string `json:"shape"`
}

// StyleSpan is one coalesced run of identically styled cells.
type StyleSpan struct {
	Row   int      `json:"row"`
	Col   int      `json:"col"`
	Width int      `json:"width"`
	Text  string   `json:"text"`
	Fg    string   `json:"fg"`
	Bg    string   `json:"bg"`
	Attrs []string `json:"attrs"`
}

// ImageMeta is one rendered image rectangle. Raw image bytes are never part of
// a frame.
type ImageMeta struct {
	ID       int    `json:"id"`
	Path     string `json:"path"`
	Alt      string `json:"alt"`
	Row      int    `json:"row"`
	Col      int    `json:"col"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Protocol string `json:"protocol"`
}

// Frame is the deterministic rendered cell view.
type Frame struct {
	V               int         `json:"v"`
	Type            string      `json:"type"`
	Seq             int         `json:"seq"`
	Width           int         `json:"width"`
	Height          int         `json:"height"`
	State           UIState     `json:"state"`
	Overlay         OverlayKind `json:"overlay"`
	Cursor          CursorFrame `json:"cursor"`
	Lines           []string    `json:"lines"`
	Styles          []StyleSpan `json:"styles"`
	Images          []ImageMeta `json:"images"`
	ColorProfile    string      `json:"color_profile"`
	NoColor         bool        `json:"no_color"`
	ImageProtocol   string      `json:"image_protocol"`
	StylesTruncated bool        `json:"styles_truncated"`
}

const (
	maxFrameStyles     = 16_000
	maxFrameStyleBytes = 256 << 10
)

// captureFrame projects the real renderer output from an x/vt screen.
func captureFrame(emu *vt.SafeEmulator, seq int, state UIState, overlay OverlayKind, images []imageRef, profile string, noColor bool, imageProtocol string) Frame {
	width, height := emu.Width(), emu.Height()
	frame := Frame{
		V:             1,
		Type:          "frame",
		Seq:           seq,
		Width:         width,
		Height:        height,
		State:         state,
		Overlay:       overlay,
		Lines:         make([]string, height),
		Styles:        make([]StyleSpan, 0, 128),
		Images:        make([]ImageMeta, 0, len(images)),
		ColorProfile:  profile,
		NoColor:       noColor,
		ImageProtocol: imageProtocol,
	}
	if frame.State == "" {
		frame.State = StateIdle
	}
	if frame.Overlay == "" {
		frame.Overlay = OverlayNone
	}
	for y := range height {
		var line strings.Builder
		for x := range width {
			cell := emu.CellAt(x, y)
			if cell == nil || cell.Width == 0 {
				continue
			}
			if cell.Content == "" {
				line.WriteByte(' ')
				continue
			}
			line.WriteString(cell.Content)
		}
		frame.Lines[y] = strings.TrimRight(line.String(), " ")
	}
	frame.Styles, frame.StylesTruncated = captureStyles(emu, width, height)
	pos := emu.CursorPosition()
	style, blink := emu.CursorStyle()
	_ = blink
	frame.Cursor = CursorFrame{
		Row:     clampInt(pos.Y, 0, max(0, height-1)),
		Col:     clampInt(pos.X, 0, max(0, width-1)),
		Visible: !emu.CursorHidden(),
		Shape:   cursorShapeName(style),
	}
	for i, ref := range images {
		meta := ImageMeta{
			ID:       i + 1,
			Path:     ref.path,
			Alt:      ref.alt,
			Row:      ref.line,
			Col:      ref.col,
			Width:    ref.width,
			Height:   ref.height,
			Protocol: ref.protocol,
		}
		if meta.Row < 0 {
			meta.Height += meta.Row
			meta.Row = 0
		}
		if meta.Col < 0 {
			meta.Width += meta.Col
			meta.Col = 0
		}
		if meta.Row+meta.Height > height {
			meta.Height = height - meta.Row
		}
		if meta.Col+meta.Width > width {
			meta.Width = width - meta.Col
		}
		if meta.Width <= 0 || meta.Height <= 0 {
			continue
		}
		frame.Images = append(frame.Images, meta)
	}
	return frame
}

func captureStyles(emu *vt.SafeEmulator, width, height int) ([]StyleSpan, bool) {
	spans := make([]StyleSpan, 0, 128)
	styleBytes := 0
	for y := range height {
		for x := range width {
			cell := emu.CellAt(x, y)
			if cell == nil || cell.Width == 0 {
				continue
			}
			if len(spans) >= maxFrameStyles || styleBytes >= maxFrameStyleBytes {
				return spans, true
			}
			span := cellSpan(x, y, cell)
			if span == nil {
				continue
			}
			if n := len(spans); n > 0 {
				prev := &spans[n-1]
				if prev.Row == span.Row && prev.Col+prev.Width == span.Col && sameStyle(*prev, *span) {
					prev.Width += span.Width
					prev.Text += span.Text
					styleBytes += len(span.Text)
					continue
				}
			}
			spans = append(spans, *span)
			styleBytes += len(span.Text) + 64
		}
	}
	return spans, false
}

func cellSpan(x, y int, cell *uv.Cell) *StyleSpan {
	fg := colorName(cell.Style.Fg)
	bg := colorName(cell.Style.Bg)
	attrs := styleAttrs(&cell.Style)
	if fg == "default" && bg == "default" && len(attrs) == 0 {
		return nil
	}
	text := cell.Content
	if text == "" {
		text = " "
	}
	return &StyleSpan{Row: y, Col: x, Width: max(1, cell.Width), Text: text, Fg: fg, Bg: bg, Attrs: attrs}
}

func sameStyle(a, b StyleSpan) bool {
	if a.Fg != b.Fg || a.Bg != b.Bg || len(a.Attrs) != len(b.Attrs) {
		return false
	}
	for i := range a.Attrs {
		if a.Attrs[i] != b.Attrs[i] {
			return false
		}
	}
	return true
}

func styleAttrs(style *uv.Style) []string {
	attrs := make([]string, 0, 7)
	if style.Attrs&uv.AttrBold != 0 {
		attrs = append(attrs, "bold")
	}
	if style.Attrs&uv.AttrItalic != 0 {
		attrs = append(attrs, "italic")
	}
	if style.Underline != uv.UnderlineNone {
		attrs = append(attrs, "underline")
	}
	if style.Attrs&uv.AttrReverse != 0 {
		attrs = append(attrs, "reverse")
	}
	if style.Attrs&uv.AttrFaint != 0 {
		attrs = append(attrs, "dim")
	}
	if style.Attrs&uv.AttrBlink != 0 {
		attrs = append(attrs, "blink")
	}
	if style.Attrs&uv.AttrStrikethrough != 0 {
		attrs = append(attrs, "strike")
	}
	return attrs
}

func cursorShapeName(style vt.CursorStyle) string {
	switch style {
	case vt.CursorUnderline:
		return "underline"
	case vt.CursorBar:
		return "bar"
	default:
		return "block"
	}
}

func colorName(c color.Color) string {
	if c == nil {
		return "default"
	}
	switch c := c.(type) {
	case ansi.BasicColor:
		return "ansi:" + strconv.Itoa(int(c))
	case ansi.IndexedColor:
		return "ansi:" + strconv.Itoa(int(c))
	case ansi.RGBColor:
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	case color.RGBA:
		if c.A == 0 {
			return "default"
		}
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	case color.NRGBA:
		if c.A == 0 {
			return "default"
		}
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	r, g, b, a := c.RGBA()
	if a == 0 {
		return "default"
	}
	return fmt.Sprintf("#%02x%02x%02x", uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

func clampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

// snapshotSVG renders the same screen cells to a deterministic SVG. It is a
// structural/visual aid, not a pixel-perfect terminal emulator.
func snapshotSVG(emu *vt.SafeEmulator, frame Frame) string {
	width, height := emu.Width(), emu.Height()
	const cellW, cellH, fontSize = 8, 16, 13
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`,
		width*cellW, height*cellH, width*cellW, height*cellH)
	b.WriteString(`<rect width="100%" height="100%" fill="#101014"/>`)
	fmt.Fprintf(&b, `<g font-family="monospace" font-size="%d" xml:space="preserve">`, fontSize)
	for y := range height {
		x := 0
		for x < width {
			cell := emu.CellAt(x, y)
			if cell == nil {
				x++
				continue
			}
			if cell.Width == 0 {
				x++
				continue
			}
			style := cell.Style
			start := x
			var text strings.Builder
			for x < width {
				next := emu.CellAt(x, y)
				if next == nil || next.Width == 0 {
					break
				}
				if !sameCellStyle(&style, &next.Style) {
					break
				}
				content := next.Content
				if content == "" {
					content = " "
				}
				text.WriteString(content)
				x += max(1, next.Width)
			}
			drawSVGRun(&b, start*cellW, y*cellH, text.String(), &style)
		}
	}
	if frame.Cursor.Visible {
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" fill="#f5e0dc" fill-opacity="0.65"/>`,
			frame.Cursor.Col*cellW, frame.Cursor.Row*cellH, cellW, cellH)
	}
	b.WriteString(`</g></svg>`)
	return b.String()
}

func sameCellStyle(a, b *uv.Style) bool {
	return a.Equal(b)
}

func drawSVGRun(b *bytes.Buffer, x, y int, text string, style *uv.Style) {
	if text == "" {
		return
	}
	fgName := colorName(style.Fg)
	bgName := colorName(style.Bg)
	if style.Attrs&uv.AttrReverse != 0 {
		fgName, bgName = bgName, fgName
	}
	if bgName != "default" {
		fmt.Fprintf(b, `<rect x="%d" y="%d" width="%d" height="%d" fill="%s"/>`,
			x, y, ansi.StringWidth(text)*8, 16, svgColor(bgName, "#101014"))
	}
	fg := svgColor(fgName, "#e6e6e6")
	var attrs strings.Builder
	if style.Attrs&uv.AttrBold != 0 {
		attrs.WriteString(` font-weight="bold"`)
	}
	if style.Attrs&uv.AttrItalic != 0 {
		attrs.WriteString(` font-style="italic"`)
	}
	if style.Underline != uv.UnderlineNone {
		attrs.WriteString(` text-decoration="underline"`)
	}
	if style.Attrs&uv.AttrStrikethrough != 0 {
		attrs.WriteString(` text-decoration="line-through"`)
	}
	if style.Attrs&uv.AttrFaint != 0 {
		attrs.WriteString(` fill-opacity="0.6"`)
	}
	fmt.Fprintf(b, `<text x="%d" y="%d" fill="%s"%s>`, x, y+12, fg, attrs.String())
	_ = xml.EscapeText(b, []byte(text))
	b.WriteString(`</text>`)
}

func svgColor(name, def string) string {
	if name == "" || name == "default" {
		return def
	}
	if strings.HasPrefix(name, "#") {
		return name
	}
	if after, ok := strings.CutPrefix(name, "ansi:"); ok {
		n, err := strconv.Atoi(after)
		if err == nil {
			return ansiPalette(n)
		}
	}
	return def
}

func ansiPalette(n int) string {
	base := []string{
		"#000000", "#800000", "#008000", "#808000", "#000080", "#800080", "#008080", "#c0c0c0",
		"#808080", "#ff0000", "#00ff00", "#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff",
	}
	if n >= 0 && n < len(base) {
		return base[n]
	}
	if n >= 16 && n <= 231 {
		n -= 16
		levels := []int{0, 95, 135, 175, 215, 255}
		r := levels[n/36]
		g := levels[(n/6)%6]
		b := levels[n%6]
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	}
	if n >= 232 && n <= 255 {
		v := 8 + (n-232)*10
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
	return "#e6e6e6"
}

var _ = model.Usage{}
