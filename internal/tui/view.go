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

	// mouseWheelLines is one wheel notch. It matches the viewport's own
	// default wheel delta so the mouse and PgUp/PgDn feel consistent.
	mouseWheelLines = 3

	// minEmptyTipRows is the fewest tip rows worth showing. A transcript too
	// short for a tips header plus this many rows keeps only the prompt.
	minEmptyTipRows = 2
)

type uiTheme struct {
	plain bool

	title    lipgloss.Style
	label    lipgloss.Style
	user     lipgloss.Style
	assist   lipgloss.Style
	think    lipgloss.Style
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
			think:  lipgloss.NewStyle().Italic(true),
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
		think:    lipgloss.NewStyle().Foreground(muted).Italic(true),
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
	// Cell-motion mode is click/release/wheel only; the app ignores clicks
	// and uses the wheel to scroll. All-motion (hover) reporting stays off.
	view.MouseMode = tea.MouseModeCellMotion
	view.KeyboardEnhancements = tea.KeyboardEnhancements{}
	if !m.ready || m.width <= 0 || m.height <= 0 {
		return view
	}
	if m.width < minWidth || m.height < minHeight {
		msg := fmt.Sprintf("terminal too small: %dx%d (minimum %dx%d)", m.width, m.height, minWidth, minHeight)
		view.SetContent(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, msg))
		return view
	}
	// A streaming delta marks the transcript dirty and schedules a coalesced
	// rebuild. Bubble Tea calls View after every message, including every
	// token, so rebuilding here would defeat that throttle and re-render the
	// growing block once per token. Keep painting the last frame until the
	// scheduled rebuild runs; all other mutations rebuild immediately.
	if !m.coalesceScheduled {
		m.ensureContent()
	}
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
	parts := []string{m.headerLine(), m.viewport.View()}
	if suggestions := m.suggestionView(); suggestions != "" {
		parts = append(parts, suggestions)
	}
	parts = append(parts, m.composerView(), m.statusLine())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m *App) headerLine() string {
	t := newTheme(m.noColor)
	modelName := SanitizeLine(m.currentModelID())
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
	effort := "effort default"
	if m.nextEffort != "" {
		effort = "effort " + SanitizeLine(m.nextEffort)
	}
	line := t.title.Render("aharness") + "  " + t.header.Render(modelName) +
		"  " + t.dim.Render(effort) + "  " + t.dim.Render(sessionLabel) +
		"  " + t.dim.Render(workspace)
	return ansi.Truncate(line, m.width, "")
}

func (m *App) statusLine() string {
	t := newTheme(m.noColor)
	turns, contextTokens := m.info.Turns, m.info.ContextTokens
	if m.run.active {
		// A live run shows its own progress instead of the counters that are
		// only written when a run finishes.
		turns, contextTokens = m.run.turns, m.run.contextTokens
	}
	turnsLabel := "turns " + formatTurnBudget(turns, m.info.MaxTurns)
	// Tokens are context occupancy against the per-call context ceiling: the
	// cumulative sum over every response is not comparable to a context window
	// and grows with each tool round.
	tokensLabel := "ctx " + formatTokenBudget(contextTokens, m.contextLimit())
	state := string(m.currentState())
	renderMode := "rich"
	if !m.rich {
		renderMode = "raw"
	}
	extra := ""
	if m.run.active && !m.headless {
		extra = " · " + formatElapsed(time.Since(m.run.started))
	}
	modelName := m.currentModelID()
	if m.run.active && m.run.model != "" {
		modelName = m.run.model
	}
	runNo := m.run.number
	if runNo == 0 {
		runNo = m.nextRun
	}
	parts := []string{"model " + SanitizeLine(modelName), fmt.Sprintf("run %d", runNo), turnsLabel, tokensLabel, state + extra, renderMode}
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
	h := m.editorHeight()
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

// suggestionView renders the slash-command completion menu. The highlighted
// row is reversed; the window scrolls to keep it visible.
func (m *App) suggestionView() string {
	count := m.visibleSuggestionCount()
	if count == 0 {
		return ""
	}
	start := m.suggestionWindowStart(count)
	t := newTheme(m.noColor)
	selected := lipgloss.NewStyle().Reverse(true)
	lines := make([]string, count)
	for i := range count {
		command := m.suggestions[start+i]
		line := ansi.Truncate(fmt.Sprintf("%-16s %s", command.usage, command.summary), m.width, "")
		if start+i == m.suggestionIndex {
			line = selected.Render(line)
		} else {
			line = t.dim.Render(line)
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func (m *App) suggestionWindowStart(count int) int {
	if m.suggestionIndex < count {
		return 0
	}
	start := m.suggestionIndex - count + 1
	if start+count > len(m.suggestions) {
		start = len(m.suggestions) - count
	}
	return max(0, start)
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
	y := header + transcript + m.visibleSuggestionCount() + cur.Y
	if x < 0 || x >= m.width || y < 0 || y >= m.height {
		return nil
	}
	c := tea.NewCursor(x, y)
	c.Shape = cur.Shape
	c.Color = cur.Color
	c.Blink = cur.Blink
	return c
}

// ensureContent lazily rebuilds the transcript window when an item, width, or
// rendering mode changed. Unchanged items keep their rendered lines, so a
// streaming delta only re-renders the item it touched.
func (m *App) ensureContent() {
	if m.width <= 0 {
		return
	}
	if m.contentWidth != m.width {
		m.contentWidth = m.width
		m.invalidateRenders()
	}
	if !m.dirty {
		if m.follow {
			m.viewport.GotoBottom()
		}
		return
	}
	m.dirty = false
	if m.imagesDirty {
		m.imagesDirty = false
		m.rebuildImages()
	}
	m.imageRefs = nil
	m.lineBuf = m.transcriptLinesInto(m.lineBuf[:0])
	m.viewport.SetLines(m.lineBuf)
	if m.follow {
		m.viewport.GotoBottom()
	}
}

// transcriptLines renders the transcript into a fresh slice. The app's hot
// path uses transcriptLinesInto to reuse its buffer.
func (m *App) transcriptLines() []string {
	return m.transcriptLinesInto(nil)
}

func (m *App) transcriptLinesInto(buf []string) []string {
	t := newTheme(m.noColor)
	for _, it := range m.items {
		if it.kind == itemImage {
			start := len(buf) + 1
			imgLines := m.renderImage(t, it)
			if len(imgLines) > 1 && len(m.imageRefs) > 0 {
				m.imageRefs[len(m.imageRefs)-1].line = start
			}
			buf = append(buf, imgLines...)
			buf = append(buf, "")
			continue
		}
		if !it.linesSet {
			it.lines = wrapLines(m.renderItem(t, it), m.width)
			it.linesSet = true
		}
		buf = append(buf, it.lines...)
		buf = append(buf, "")
	}
	if len(buf) == 0 {
		return m.emptyTranscriptLines(t)
	}
	return buf
}

// renderItem renders one non-image transcript entry. The result is cached on
// the item and must not depend on anything but the item and the global render
// mode; width changes invalidate the cache.
func (m *App) renderItem(t uiTheme, it *transcriptItem) []string {
	switch it.kind {
	case itemRun:
		return []string{m.renderRunLine(t, it)}
	case itemUser:
		lines := renderTextBlock(t, "user", t.user, it.text, m.width)
		if it.queued {
			lines = append(lines, t.dim.Render("  steering queued…"))
		}
		return lines
	case itemAssistant:
		return m.renderAssistant(t, it)
	case itemThinking:
		return m.renderThinking(t, it)
	case itemTool:
		return m.renderTool(t, it)
	case itemNotice:
		style := t.notice
		if it.noticeKind == agent.NoticeError {
			style = t.errorSty
		}
		return renderTextBlock(t, "notice", style, it.text, m.width)
	default:
		return nil
	}
}

// emptyTranscriptLines renders the tips block that fills an empty transcript.
// It disappears as soon as the first item exists, so a live or resumed session
// never mixes onboarding text into its history. Rows are planned against the
// transcript viewport: the full block is used when it fits, and the prompt
// keeps the transcript to itself when there is no room for a tips header plus
// minEmptyTipRows rows. Every row fits the 40-column minimum terminal, so the
// minimum window shows the block intact instead of clipping mid-word.
func (m *App) emptyTranscriptLines(theme uiTheme) []string {
	prompt := []string{theme.dim.Render("No messages yet. Type a task.")}
	tips := []string{"", theme.label.Render("tips")}
	headerRows := len(tips)
	for _, tip := range emptyTranscriptTips {
		tips = append(tips, theme.dim.Render(fmt.Sprintf("  %-13s %s", tip.key, tip.what)))
	}
	if height := m.viewport.Height(); height > 0 {
		room := height - len(prompt)
		if room < headerRows+minEmptyTipRows {
			return truncateLines(prompt, m.width)
		}
		tips = tips[:min(len(tips), room)]
	}
	return truncateLines(append(prompt, tips...), m.width)
}

// emptyTranscriptTips are the onboarding rows shown before the first message,
// ordered so that a short transcript keeps the most useful ones: the full key
// list, then the terminal selection/paste bypass the mouse capture requires.
var emptyTranscriptTips = []struct{ key, what string }{
	{"F1", "keys and slash commands"},
	{"Shift+drag", "select or copy text"},
	{"Ctrl+Shift+V", "paste; Shift+middle too"},
	{"wheel", "scroll; PgUp/PgDn also"},
	{"Ctrl+P", "models · Ctrl+X · Ctrl+O"},
}

// truncateLines clips rows to the frame width. A zero width means the terminal
// size is not known yet, so the rows are left intact for the first resize.
func truncateLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
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

// renderThinking renders one thinking block as preformatted text. The header
// shows whether the model is still thinking; the body is never parsed as
// markdown because it is provider prose, not assistant output. Width wrapping
// happens once in wrapLines when the item's lines are cached.
func (m *App) renderThinking(t uiTheme, it *transcriptItem) []string {
	header := "thinking"
	if it.thinking {
		header = "thinking…"
	}
	label := t.label.Render(header + " ")
	labelWidth := lipgloss.Width(label)
	// Sanitize again at the render boundary: terminal safety must not depend on
	// every future producer of a thinking item remembering to sanitize.
	body := strings.Split(strings.TrimRight(Sanitize(it.text), "\n"), "\n")
	if len(body) == 1 && body[0] == "" {
		body = []string{"…"}
	}
	indent := strings.Repeat(" ", labelWidth)
	lines := make([]string, 0, len(body))
	for i, line := range body {
		if i == 0 {
			lines = append(lines, label+t.think.Render(line))
		} else {
			lines = append(lines, indent+t.think.Render(line))
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

// contextLimit reports the effective per-call context ceiling: the host's
// resolved context window when available, otherwise the selected model's
// catalog window. Zero means unknown and omits the ratio.
func (m *App) contextLimit() int {
	if m.info.ContextLimit > 0 {
		return m.info.ContextLimit
	}
	for _, choice := range m.models {
		if choice.ID == m.currentModelID() && choice.ContextWindow > 0 {
			return choice.ContextWindow
		}
	}
	return 0
}

// formatTurnBudget renders cumulative turns with an explicit positive budget.
// A zero budget means unlimited and omits the ratio.
func formatTurnBudget(turns, maxTurns int) string {
	if maxTurns > 0 {
		return fmt.Sprintf("%d/%d", turns, maxTurns)
	}
	return fmt.Sprintf("%d", turns)
}

// formatCostMicroUSD renders an observational cost estimate. Micro-USD is an
// integer so accumulated estimates never drift.
func formatCostMicroUSD(micro int64, complete bool) string {
	value := fmt.Sprintf("$%d.%06d", micro/1_000_000, micro%1_000_000)
	if !complete {
		value += " (incomplete)"
	}
	return value
}

// formatTokenBudget renders token occupancy against an explicit positive
// ceiling. A zero ceiling omits the ratio.
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
