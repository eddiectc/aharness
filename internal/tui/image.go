package tui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/NimbleMarkets/ntcharts/v2/picture"
	"github.com/charmbracelet/x/ansi"

	"aharness/internal/tools"
)

const (
	maxImageBytes     = 8 << 20
	maxImageDimension = 4096
	maxImagePixels    = 16 << 20
	maxImages         = 32
	maxThumbCols      = 60
	maxThumbRows      = 20
)

var imageLinkRE = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)

// ImageProtocolFromEnv validates AHARNESS_IMAGE_PROTOCOL. The auto value is an
// environment-based best-effort Kitty detection; headless mode always uses
// glyph cells.
func ImageProtocolFromEnv(getenv func(string) string, headless bool) (string, error) {
	if headless {
		return "glyph", nil
	}
	raw := ""
	if getenv != nil {
		raw = strings.TrimSpace(getenv("AHARNESS_IMAGE_PROTOCOL"))
	}
	switch raw {
	case "", "auto":
		if kittyEnvironment(getenv) {
			return "kitty", nil
		}
		return "glyph", nil
	case "kitty":
		return "kitty", nil
	case "glyph":
		return "glyph", nil
	case "none":
		return "none", nil
	default:
		return "", fmt.Errorf("invalid AHARNESS_IMAGE_PROTOCOL %q: want auto, kitty, glyph, or none", raw)
	}
}

func kittyEnvironment(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	term := getenv("TERM")
	if strings.Contains(term, "kitty") || strings.Contains(term, "ghostty") || strings.Contains(term, "wezterm") {
		return true
	}
	switch getenv("TERM_PROGRAM") {
	case "WezTerm", "ghostty":
		return true
	}
	return getenv("KITTY_WINDOW_ID") != "" || getenv("WEZTERM_PANE") != ""
}

// imageBlock is one display-only markdown image. Source bytes are never kept:
// thumbnails are rendered once per width, and the viewer re-reads the file.
type imageBlock struct {
	path   string
	alt    string
	width  int
	height int

	errText   string
	protocol  string
	ws        *tools.Workspace
	thumbFor  int
	thumb     []string
	thumbCols int
	thumbRows int
}

// imageRef is one rendered thumbnail rectangle for protocol metadata.
type imageRef struct {
	path, alt string
	line      int
	col       int
	width     int
	height    int
	protocol  string
}

func (m *App) rebuildImages() {
	out := make([]*transcriptItem, 0, len(m.items))
	m.images = nil
	seen := make(map[string]*imageBlock)
	for _, it := range m.items {
		if it.kind == itemImage {
			continue
		}
		out = append(out, it)
		if it.kind != itemAssistant || len(m.images) >= maxImages {
			continue
		}
		for _, match := range imageLinkRE.FindAllStringSubmatch(it.text, -1) {
			if len(m.images) >= maxImages {
				break
			}
			alt := SanitizeLine(match[1])
			path := strings.TrimSpace(match[2])
			key := m.imageProtocol + "\x00" + path + "\x00" + alt
			block, ok := seen[key]
			if !ok {
				block, ok = m.imageCache[key]
			}
			if !ok {
				block = m.newImageBlock(alt, path)
				if block.errText == "" {
					if len(m.imageCache) >= 64 {
						for k := range m.imageCache {
							delete(m.imageCache, k)
							break
						}
					}
					m.imageCache[key] = block
				}
			}
			seen[key] = block
			m.images = append(m.images, block)
			out = append(out, &transcriptItem{kind: itemImage, run: it.run, turn: it.turn, image: block})
		}
	}
	m.items = out
}

func (m *App) newImageBlock(alt, path string) *imageBlock {
	ws := m.isWorkspace()
	block := &imageBlock{path: path, alt: alt, protocol: m.imageProtocol, ws: ws}
	if m.imageProtocol == "none" {
		block.errText = "image rendering disabled"
		return block
	}
	if ws == nil {
		block.errText = "no workspace"
		return block
	}
	if !filepathLocal(path) {
		block.errText = "image path is not a workspace-relative file"
		return block
	}
	data, err := ws.ReadDisplayFile(path, maxImageBytes)
	if err != nil {
		block.errText = SanitizeLine(err.Error())
		return block
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		block.errText = "unsupported or invalid image: " + SanitizeLine(err.Error())
		return block
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageDimension || cfg.Height > maxImageDimension ||
		int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		block.errText = fmt.Sprintf("image dimensions %dx%d exceed the display cap", cfg.Width, cfg.Height)
		return block
	}
	block.width, block.height = cfg.Width, cfg.Height
	return block
}

func filepathLocal(path string) bool {
	if path == "" || strings.Contains(path, "://") || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return false
	}
	for part := range strings.SplitSeq(strings.ReplaceAll(path, "\\", "/"), "/") {
		if part == ".." || part == ".git" {
			return false
		}
	}
	return true
}

// renderImage renders one image item and records its thumbnail rectangle.
func (m *App) renderImage(t uiTheme, it *transcriptItem) []string {
	block := it.image
	if block == nil {
		return nil
	}
	label := fmt.Sprintf("image %s", block.alt)
	if block.alt == "" {
		label = "image " + block.path
	}
	if block.width > 0 {
		label += fmt.Sprintf(" %dx%d", block.width, block.height)
	}
	if block.errText != "" {
		errLabel := label + " (" + block.path + ")"
		return []string{t.dim.Render(ansi.Truncate(fmt.Sprintf("[%s · %s]", errLabel, block.errText), m.width, ""))}
	}
	lines, cols, rows := block.thumbnail(max(1, m.width-2))
	if len(lines) == 0 {
		return []string{t.dim.Render(ansi.Truncate("["+label+" · Ctrl-G view]", m.width, ""))}
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, t.dim.Render(ansi.Truncate("["+label+" · Ctrl-G view]", m.width, "")))
	for _, line := range lines {
		out = append(out, ansi.Truncate("  "+line, m.width, ""))
	}
	m.imageRefs = append(m.imageRefs, imageRef{
		path:     block.path,
		alt:      block.alt,
		col:      2,
		width:    cols,
		height:   rows,
		protocol: block.protocol,
	})
	return out
}

func (b *imageBlock) thumbnail(width int) ([]string, int, int) {
	if b.thumbFor == width {
		return b.thumb, b.thumbCols, b.thumbRows
	}
	b.thumbFor = width
	b.thumb = nil
	if b.ws == nil {
		return nil, 0, 0
	}
	img, err := loadDisplayImage(b.ws, b.path)
	if err != nil {
		b.errText = SanitizeLine(err.Error())
		return nil, 0, 0
	}
	cols := min(maxThumbCols, max(1, width-4))
	if b.width > 0 {
		cols = min(cols, max(1, b.width))
	}
	rows := 1
	if b.width > 0 && b.height > 0 {
		rows = max(1, (cols*b.height+b.width-1)/(b.width*2))
		rows = min(rows, maxThumbRows)
	}
	pic := picture.NewWithConfig(picture.Config{
		Fit:        picture.FitContain,
		Background: color.Transparent,
	})
	pic.SetSize(cols, rows)
	pic.SetImage(img)
	content := strings.TrimRight(pic.View().Content, "\n")
	if content == "" {
		b.errText = "image could not be rendered"
		return nil, 0, 0
	}
	lines := strings.Split(content, "\n")
	b.thumbCols, b.thumbRows = cols, rows
	b.thumb = lines
	return lines, cols, rows
}

func loadDisplayImage(ws *tools.Workspace, path string) (image.Image, error) {
	data, err := ws.ReadDisplayFile(path, maxImageBytes)
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image config: %w", err)
	}
	if cfg.Width > maxImageDimension || cfg.Height > maxImageDimension ||
		int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, errors.New("image exceeds the decoded pixel cap")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return img, nil
}

// imageOverlay is the fit-to-viewport image viewer.
type imageOverlay struct {
	block  *imageBlock
	img    image.Image
	pic    picture.Model
	fit    bool
	width  int
	height int
	err    string
	kitty  bool
}

func (m *App) toggleImageViewer() tea.Cmd {
	if m.overlay.kind == OverlayImage {
		return m.closeOverlayCmd()
	}
	if len(m.images) == 0 {
		m.notice = "no image block in the transcript"
		return nil
	}
	block := m.images[len(m.images)-1]
	ws := block.ws
	if ws == nil {
		m.notice = "image viewer requires a workspace"
		return nil
	}
	io := &imageOverlay{block: block, fit: true, kitty: m.imageProtocol == "kitty"}
	if m.imageProtocol == "none" {
		io.err = "image rendering disabled"
	} else if block.errText != "" {
		io.err = block.errText
	} else {
		img, err := loadDisplayImage(ws, block.path)
		if err != nil {
			io.err = SanitizeLine(err.Error())
		} else {
			io.img = img
			io.pic = picture.NewWithConfig(picture.Config{
				Fit:        picture.FitContain,
				Background: color.Transparent,
			})
			if io.kitty {
				picture.ForceKittyCapability(picture.KittyCapabilitySupported)
			}
		}
	}
	m.overlay.image = io
	m.overlay.push(OverlayImage)
	m.overlay.resize(m, m.width, m.height)
	return io.applyCmd()
}

func (io *imageOverlay) setSize() tea.Cmd {
	if io.img == nil || io.width <= 0 || io.height <= 0 {
		return nil
	}
	var cols, rows int
	if io.fit {
		cols = io.width
		rows = io.height
		b := io.img.Bounds()
		if b.Dx() > 0 {
			rows = max(1, (cols*b.Dy()+b.Dx()-1)/(b.Dx()*2))
			rows = min(rows, io.height)
		}
	} else {
		b := io.img.Bounds()
		cols = min(io.width, max(1, b.Dx()))
		rows = min(io.height, max(1, (b.Dy()+1)/2))
	}
	io.pic.SetSize(cols, rows)
	return io.applyCmd()
}

func (io *imageOverlay) applyCmd() tea.Cmd {
	if io.img == nil {
		return nil
	}
	renderCmd := io.pic.SetImage(io.img)
	if renderCmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := renderCmd()
		return imageRenderMsg{msg: msg}
	}
}

type imageRenderMsg struct{ msg tea.Msg }

func (io *imageOverlay) resize(m *App, width, height int) tea.Cmd {
	io.width, io.height = max(1, width), max(1, height-2)
	if io.fit {
		return io.setSize()
	}
	return nil
}

func (io *imageOverlay) update(m *App, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Keystroke() {
	case "enter", " ":
		io.fit = !io.fit
		return io.setSize()
	}
	if msg.Keystroke() == "pgup" || msg.Keystroke() == "pgdown" {
		io.fit = !io.fit
		return io.setSize()
	}
	return nil
}

func (io *imageOverlay) view() string {
	if io.err != "" {
		return "[" + SanitizeLine(io.err) + "]"
	}
	if io.img == nil {
		return "[image unavailable]"
	}
	if io.kitty {
		content := strings.TrimRight(io.pic.View().Content, "\n")
		if content != "" {
			return content
		}
	}
	// Glyph fallback: render at the current size with half-blocks.
	return strings.TrimRight(io.pic.View().Content, "\n")
}

// ClearImageViewerCmd clears any Kitty placement before the overlay closes.
func (m *App) clearImageViewerCmd() tea.Cmd {
	if m.overlay.image == nil || m.overlay.image.pic.Mode() != picture.PictureKitty {
		return nil
	}
	if cmd := m.overlay.image.pic.SetImage(nil); cmd != nil {
		return cmd
	}
	return nil
}

// forwardPictureMsg lets the image viewer consume its own Kitty messages.
func (m *App) forwardPictureMsg(msg tea.Msg) (bool, tea.Cmd) {
	if m.overlay.image == nil || !picture.IsPictureMsg(msg) {
		return false, nil
	}
	return true, m.overlay.image.pic.Update(msg)
}

func (m *App) handleImageRender(msg imageRenderMsg) tea.Cmd {
	if m.overlay.image == nil {
		return nil
	}
	return m.overlay.image.pic.Update(msg.msg)
}

// Images returns the current image metadata with frame-relative rectangles.
func (m *App) Images() []imageRef {
	if m.overlay.kind != OverlayNone {
		return nil
	}
	header := 1
	top := header - m.viewport.YOffset()
	out := make([]imageRef, 0, len(m.imageRefs))
	for _, ref := range m.imageRefs {
		row := top + ref.line
		if row+ref.height <= 0 || row >= m.height {
			continue
		}
		ref.line = row
		out = append(out, ref)
	}
	return out
}
