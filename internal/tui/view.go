package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"aharness/internal/agent"
	"aharness/internal/model"
)

const (
	minWidth  = 40
	minHeight = 10
)

type uiTheme struct {
	plain bool

	title    lipgloss.Style
	label    lipgloss.Style
	user     lipgloss.Style
	assist   lipgloss.Style
	tool     lipgloss.Style
	dim      lipgloss.Style
	notice   lipgloss.Style
	errorSty lipgloss.Style
	status   lipgloss.Style
	header   lipgloss.Style
	box      lipgloss.Style
}

func newTheme(noColor bool) uiTheme {
	if noColor {
		return uiTheme{
			plain:  true,
			title:  lipgloss.NewStyle().Bold(true),
			label:  lipgloss.NewStyle().Bold(true),
			status: lipgloss.NewStyle(),
			header: lipgloss.NewStyle(),
			box:    lipgloss.NewStyle(),
		}
	}
	accent := lipgloss.Color("#89b4fa")
	green := lipgloss.Color("#a6e3a1")
	red := lipgloss.Color("#f38ba8")
	yellow := lipgloss.Color("#f9e2af")
	muted := lipgloss.Color("#6c7086")
	text := lipgloss.Color("#cdd6f4")
	return uiTheme{
		title:    lipgloss.NewStyle().Bold(true).Foreground(accent),
		label:    lipgloss.NewStyle().Bold(true).Foreground(accent),
		user:     lipgloss.NewStyle().Bold(true).Foreground(green),
		assist:   lipgloss.NewStyle().Foreground(text),
		tool:     lipgloss.NewStyle().Foreground(yellow),
		dim:      lipgloss.NewStyle().Foreground(muted),
		notice:   lipgloss.NewStyle().Foreground(yellow),
		errorSty: lipgloss.NewStyle().Foreground(red),
		status:   lipgloss.NewStyle().Foreground(muted),
		header:   lipgloss.NewStyle().Foreground(text),
		box:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1),
	}
}

// View implements tea.Model.
func (m *App) View() tea.View {
	view := tea.NewView("")
	view.AltScreen = true
	view.MouseMode = tea.MouseModeNone
	view.KeyboardEnhancements = tea.KeyboardEnhancements{}
	if !m.ready || m.width <= 0 || m.height <= 0 {
		return view
	}
	if m.width < minWidth || m.height < minHeight {
		msg := fmt.Sprintf("terminal too small: %dx%d (minimum %dx%d)", m.width, m.height, minWidth, minHeight)
		view.SetContent(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg))
		return view
	}
	m.ensureContent()
	base := m.baseFrame()
	if m.overlay.kind != OverlayNone {
		overlay := m.overlay.view(m)
		if overlay != "" {
			baseLayer := lipgloss.NewLayer(base)
			ow, oh := lipgloss.Width(overlay), lipgloss.Height(overlay)
			x := max(0, (m.width-ow)/2)
			y := max(0, (m.height-oh)/2)
			overlayLayer := lipgloss.NewLayer(overlay).X(x).Y(y).Z(1)
			base = lipgloss.NewCompositor(baseLayer, overlayLayer).Render()
		}
	}
	view.SetContent(fitLines(base, m.width, m.height))
	view.Cursor = m.cursor()
	return view
}

func (m *App) baseFrame() string {
	header := m.headerLine()
	transcript := m.viewport.View()
	composer := m.composerView()
	status := m.statusLine()
	return lipgloss.JoinVertical(lipgloss.Left, header, transcript, composer, status)
}

func (m *App) headerLine() string {
	t := newTheme(m.noColor)
	modelName := SanitizeLine(m.info.Model)
	if modelName == "" {
		modelName = SanitizeLine(m.nextModel)
	}
	session := SanitizeLine(m.info.SessionID)
	if len(session) > 8 {
		session = session[:8]
	}
	workspace := SanitizeLine(m.info.Workspace)
	if i := strings.LastIndexAny(workspace, "/\\"); i >= 0 {
		workspace = workspace[i+1:]
	}
	if workspace == "" {
		workspace = "."
	}
	sessionLabel := "session " + session
	if m.headless {
		sessionLabel = "session --"
	}
	line := t.title.Render("aharness") + "  " + t.header.Render(modelName) +
		"  " + t.dim.Render(sessionLabel) + "  " + t.dim.Render(workspace)
	return ansi.Truncate(line, m.width, "")
}

func (m *App) statusLine() string {
	t := newTheme(m.noColor)
	turns := "turns " + formatTurnBudget(m.info.Turns, m.info.MaxTurns)
	tokens := "tokens " + formatTokenBudget(m.info.Usage.Total(), m.info.MaxTotalTokens)
	state := string(m.currentState())
	renderMode := "rich"
	if !m.rich {
		renderMode = "raw"
	}
	extra := ""
	if m.run.active && !m.headless {
		extra = " · " + formatElapsed(time.Since(m.run.started))
	}
	modelName := m.info.Model
	if modelName == "" {
		modelName = m.nextModel
	}
	runNo := m.run.number
	if runNo == 0 {
		runNo = m.nextRun
	}
	parts := []string{"model " + SanitizeLine(modelName), fmt.Sprintf("run %d", runNo), turns, tokens, state + extra, renderMode}
	if m.overlay.kind != OverlayNone {
		parts = append(parts, "overlay "+string(m.overlay.kind))
	}
	if m.imageProtocol != "" {
		parts = append(parts, "img:"+m.imageProtocol)
	}
	line := strings.Join(parts, " · ")
	if m.notice != "" {
		line = SanitizeLine(m.notice) + " · " + line
	}
	return t.status.Render(ansi.Truncate(line, m.width, ""))
}

func (m *App) composerView() string {
	h := m.composerHeight()
	view := m.composer.View()
	lines := strings.Split(view, "\n")
	if len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	return strings.Join(lines, "\n")
}

func (m *App) cursor() *tea.Cursor {
	if m.overlay.kind != OverlayNone {
		return nil
	}
	cur := m.composer.Cursor()
	if cur == nil {
		return nil
	}
	header, composerH, status := 1, m.composerHeight(), 1
	transcript := max(1, m.height-header-composerH-status)
	x := cur.X
	y := header + transcript + cur.Y
	if x < 0 || x >= m.width || y < 0 || y >= m.height {
		return nil
	}
	c := tea.NewCursor(x, y)
	c.Shape = cur.Shape
	c.Color = cur.Color
	c.Blink = cur.Blink
	return c
}

// ensureContent lazily rebuilds the transcript viewport when an item, width,
// or rendering mode changed.
func (m *App) ensureContent() {
	if m.width <= 0 {
		return
	}
	if !m.dirty && m.contentWidth == m.width {
		if m.follow && !m.viewport.AtBottom() {
			m.viewport.GotoBottom()
		}
		return
	}
	m.dirty = false
	m.contentWidth = m.width
	m.rebuildImages()
	m.imageRefs = nil
	lines := m.transcriptLines()
	m.viewport.SetContentLines(lines)
	if m.follow {
		m.viewport.GotoBottom()
	}
}

func (m *App) transcriptLines() []string {
	t := newTheme(m.noColor)
	var lines []string
	for _, it := range m.items {
		switch it.kind {
		case itemRun:
			lines = append(lines, m.renderRunLine(t, it))
		case itemUser:
			lines = append(lines, renderTextBlock(t, "user", t.user, it.text, m.width)...)
			if it.queued {
				lines = append(lines, t.dim.Render("  steering queued…"))
			}
		case itemAssistant:
			lines = append(lines, m.renderAssistant(t, it)...)
		case itemTool:
			lines = append(lines, m.renderTool(t, it)...)
		case itemNotice:
			style := t.notice
			if it.noticeKind == agent.NoticeError {
				style = t.errorSty
			}
			lines = append(lines, renderTextBlock(t, "notice", style, it.text, m.width)...)
		case itemImage:
			start := len(lines) + 1
			imgLines := m.renderImage(t, it)
			if len(imgLines) > 1 && len(m.imageRefs) > 0 {
				m.imageRefs[len(m.imageRefs)-1].line = start
			}
			lines = append(lines, imgLines...)
		}
		lines = append(lines, "")
	}
	if len(lines) == 0 {
		lines = append(lines, t.dim.Render("No messages yet. Type a task and press Enter."))
	}
	return lines
}

func (m *App) renderRunLine(t uiTheme, it *transcriptItem) string {
	status := string(it.status)
	if status == "" {
		status = "in progress"
	}
	parts := []string{fmt.Sprintf("run %d", it.run), status}
	if it.turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", it.turns))
	}
	if it.usage.Total() > 0 {
		parts = append(parts, humanTokens(it.usage.Total())+" tokens")
	}
	if it.duration > 0 && !m.headless {
		parts = append(parts, formatElapsed(it.duration))
	}
	if it.model != "" {
		parts = append(parts, it.model)
	}
	if it.reason != "" && it.status != agent.StopCompleted {
		parts = append(parts, it.reason)
	}
	text := "── " + strings.Join(parts, " · ") + " ──"
	style := t.dim
	if it.status == agent.StopInternalError || it.status == agent.StopProviderError || it.status == agent.StopProtocolError {
		style = t.errorSty
	}
	return style.Render(ansi.Truncate(text, m.width, ""))
}

func (m *App) renderAssistant(t uiTheme, it *transcriptItem) []string {
	width := max(10, m.width-2)
	if !it.renderedSet || it.renderedWidth != width || it.renderedRich != m.rich {
		rendered, _, err := renderMarkdown(it.text, width, m.rich, m.noColor)
		if err != nil {
			rendered = Sanitize(it.text) + "\n[markdown fallback: " + SanitizeLine(err.Error()) + "]"
		}
		it.rendered = rendered
		it.renderedWidth = width
		it.renderedRich = m.rich
		it.renderedSet = true
	}
	body := strings.Split(strings.TrimRight(it.rendered, "\n"), "\n")
	if len(body) == 0 {
		body = []string{""}
	}
	label := t.label.Render("assistant ")
	indent := strings.Repeat(" ", lipgloss.Width(label))
	lines := make([]string, 0, len(body)+1)
	for i, line := range body {
		if i == 0 {
			lines = append(lines, label+t.assist.Render(line))
		} else {
			lines = append(lines, indent+line)
		}
	}
	return lines
}

func (m *App) renderTool(t uiTheme, it *transcriptItem) []string {
	status := "pending"
	switch {
	case it.running:
		status = "running"
	case !it.pending && it.ok:
		status = "ok"
	case !it.pending && !it.ok:
		status = "error"
	}
	if it.code != "" && it.code != "ok" {
		status += " " + it.code
	}
	if it.truncated {
		status += " truncated"
	}
	if it.timedOut {
		status += " timeout"
	}
	head := fmt.Sprintf("tool %s", it.name)
	if it.args != "" {
		head += " " + it.args
	}
	head += " · " + status
	if it.toolTime > 0 && !m.headless {
		head += " · " + formatElapsed(it.toolTime)
	}
	tstyle := t.tool
	if !it.ok && !it.pending {
		tstyle = t.errorSty
	}
	lines := []string{tstyle.Render(ansi.Truncate(head, m.width, ""))}
	body := it.output
	if body == "" {
		body = it.errText
	}
	if body != "" {
		bodyLines := strings.Split(strings.TrimRight(body, "\n"), "\n")
		const foldedLines = 40
		const foldedBytes = 4 << 10
		truncated := false
		if len(bodyLines) > foldedLines {
			bodyLines = bodyLines[:foldedLines]
			truncated = true
		}
		if size := 0; true {
			for i, line := range bodyLines {
				size += len(line)
				if size > foldedBytes {
					bodyLines = bodyLines[:i]
					truncated = true
					break
				}
			}
		}
		for _, line := range bodyLines {
			lines = append(lines, "  "+t.dim.Render(ansi.Truncate(line, max(1, m.width-2), "")))
		}
		if truncated {
			lines = append(lines, "  "+t.dim.Render("[output folded; the full result is in the session log]"))
		}
	}
	return lines
}

func renderTextBlock(t uiTheme, label string, style lipgloss.Style, text string, width int) []string {
	lines := strings.Split(Sanitize(text), "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	prefix := t.label.Render(label + " ")
	indent := strings.Repeat(" ", lipgloss.Width(prefix))
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			out = append(out, prefix+style.Render(ansi.Truncate(line, max(1, width-lipgloss.Width(prefix)), "")))
		} else {
			out = append(out, indent+style.Render(ansi.Truncate(line, max(1, width-lipgloss.Width(indent)), "")))
		}
	}
	return out
}

func fitLines(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}

func humanTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

// formatTurnBudget renders cumulative turns with an explicit positive budget.
// A zero budget means unlimited and omits the ratio.
func formatTurnBudget(turns, maxTurns int) string {
	if maxTurns > 0 {
		return fmt.Sprintf("%d/%d", turns, maxTurns)
	}
	return fmt.Sprintf("%d", turns)
}

// formatTokenBudget renders cumulative tokens with an explicit positive budget.
// A zero budget means unlimited and omits the ratio.
func formatTokenBudget(used, maxTokens int) string {
	if maxTokens > 0 {
		return fmt.Sprintf("%s/%s", humanTokens(used), humanTokens(maxTokens))
	}
	return humanTokens(used)
}

// formatMaxTurns labels a configured cumulative turn budget.
func formatMaxTurns(configured int) string {
	if configured <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", configured)
}

// formatMaxTokens labels a configured cumulative token budget.
func formatMaxTokens(configured int) string {
	if configured <= 0 {
		return "unlimited"
	}
	return humanTokens(configured)
}

// formatTimeout labels a configured per-run or per-call deadline.
func formatTimeout(d time.Duration) string {
	if d <= 0 {
		return "none"
	}
	return d.String()
}

func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

var _ = model.Usage{}
