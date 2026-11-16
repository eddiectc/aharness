package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/tools"
)

// Child view display caps. They bound one focused child's transcript rendering
// only; the child session log is unchanged.
const (
	childToolBodyRows  = 12
	childToolBodyBytes = 2 << 10
	childPendingSteers = 8
	childErrBytes      = 512
)

// childFocus is one delegated child's read-only transcript, opened by Enter on
// an /agent-hub row. While the child is running the composer steers it; a
// finished child leaves the view read-only until Esc returns to the parent.
type childFocus struct {
	session  string
	agent    string
	status   string
	readOnly bool
	items    []agent.HistoryItem
	// pending holds composer lines sent to the child that have not appeared in
	// its log yet. They render as queued and are dropped once the child records
	// them with source=subagent-steer.
	pending []string
	err     string
	view    transcriptView
	follow  bool
	width   int
}

// setItems installs a fresh child history snapshot and reconciles pending
// steering lines with what the child has recorded.
func (f *childFocus) setItems(items []agent.HistoryItem) {
	f.items = items
	f.dropAckedPending()
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind != agent.HistoryRun {
			continue
		}
		if items[i].Status != "" {
			f.status = string(items[i].Status)
			f.readOnly = true
			// A terminal run cannot consume a queued line; leaving it visible
			// would claim steering that will never happen.
			f.pending = nil
		}
		break
	}
}

// addPending records a steering line already accepted by the host.
func (f *childFocus) addPending(text string) {
	f.pending = append(f.pending, text)
	if len(f.pending) > childPendingSteers {
		f.pending = f.pending[len(f.pending)-childPendingSteers:]
	}
}

// dropAckedPending removes lines the child log now carries. The child records
// them at its next turn boundary, so a line stays queued while a model call is
// in flight.
func (f *childFocus) dropAckedPending() {
	if len(f.pending) == 0 {
		return
	}
	kept := f.pending[:0]
	for _, text := range f.pending {
		acked := false
		for _, item := range f.items {
			if item.Kind == agent.HistoryUser && item.Source == agent.SourceSubagentSteer && item.Text == text {
				acked = true
				break
			}
		}
		if !acked {
			kept = append(kept, text)
		}
	}
	f.pending = kept
}

// rebuild renders the child history into the focus viewport.
func (f *childFocus) rebuild(t uiTheme, width int, rich, noColor bool) {
	if width <= 0 {
		return
	}
	f.width = width
	var lines []string
	if f.err != "" {
		lines = append(lines, t.errorSty.Render("error: "+SanitizeLine(f.err)))
	}
	lines = append(lines, renderChildHistory(t, f.items, f.pending, width, rich, noColor)...)
	f.view.SetLines(wrapLines(lines, width))
	if f.follow {
		f.view.GotoBottom()
	}
}

// renderChildHistory renders one delegated child's display history. Assistant
// text is markdown source and follows the parent transcript's rich/raw mode;
// tool bodies are capped because the focused view is not the log.
func renderChildHistory(t uiTheme, items []agent.HistoryItem, pending []string, width int, rich, noColor bool) []string {
	var lines []string
	for _, item := range items {
		switch item.Kind {
		case agent.HistoryRun:
			lines = append(lines, renderChildRun(t, item, width))
		case agent.HistoryUser:
			label := "task"
			if item.Source == agent.SourceSubagentSteer {
				label = "delegation steer"
			}
			lines = append(lines, renderTextBlock(t, label, t.user, item.Text, width)...)
		case agent.HistoryAssistant:
			lines = append(lines, renderChildAssistant(t, item.Text, width, rich, noColor)...)
		case agent.HistoryTool:
			lines = append(lines, renderChildTool(t, item, width)...)
		case agent.HistoryNotice:
			lines = append(lines, renderTextBlock(t, "notice", t.notice, item.Text, width)...)
		}
	}
	for _, text := range pending {
		lines = append(lines, renderTextBlock(t, "delegation steer", t.user, text, width)...)
		lines = append(lines, t.dim.Render("  queued…"))
	}
	if len(lines) == 0 {
		lines = append(lines, t.dim.Render("the child has no transcript yet"))
	}
	return lines
}

func renderChildRun(t uiTheme, item agent.HistoryItem, width int) string {
	status := string(item.Status)
	if status == "" {
		status = "in progress"
	}
	parts := []string{fmt.Sprintf("── run %d", item.Run), status}
	if item.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", item.Turns))
	}
	if item.Usage.Total() > 0 {
		parts = append(parts, humanTokens(item.Usage.Total())+" tokens")
	}
	if item.Duration > 0 {
		parts = append(parts, formatElapsed(item.Duration))
	}
	if item.Model != "" {
		parts = append(parts, SanitizeLine(item.Model))
	}
	if item.Reason != "" && item.Status != agent.StopCompleted {
		parts = append(parts, SanitizeLine(item.Reason))
	}
	return t.dim.Render(ansi.Truncate(strings.Join(parts, " · ")+" ──", width, ""))
}

func renderChildAssistant(t uiTheme, text string, width int, rich, noColor bool) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	label := t.label.Render("assistant ")
	indent := strings.Repeat(" ", max(0, ansi.StringWidth(label)))
	bodyWidth := max(10, width-ansi.StringWidth(label))
	rendered, _, err := renderMarkdown(text, bodyWidth, rich, noColor)
	if err != nil {
		rendered = Sanitize(text) + "\n[markdown fallback: " + SanitizeLine(err.Error()) + "]"
	}
	body := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if len(body) == 0 {
		body = []string{""}
	}
	out := make([]string, 0, len(body))
	for i, line := range body {
		if i == 0 {
			out = append(out, label+t.assist.Render(line))
		} else {
			out = append(out, indent+line)
		}
	}
	return out
}

func renderChildTool(t uiTheme, item agent.HistoryItem, width int) []string {
	status := "ok"
	switch {
	case item.Pending:
		status = "pending"
	case !item.OK:
		status = item.Code
		if status == "" {
			status = "failed"
		}
	}
	parts := []string{"tool " + SanitizeLine(item.Name), SanitizeLine(status)}
	if intent := SanitizeLine(tools.Intent(item.Arguments)); intent != "" {
		parts = append(parts, intent)
	}
	lines := []string{t.tool.Render(ansi.Truncate(strings.Join(parts, " · "), width, ""))}
	body := item.Output
	if body == "" {
		body = item.Error
	}
	body = strings.TrimRight(Sanitize(body), "\n")
	if body == "" {
		return lines
	}
	bodyLines := strings.Split(body, "\n")
	folded := false
	if len(bodyLines) > childToolBodyRows {
		bodyLines = bodyLines[:childToolBodyRows]
		folded = true
	}
	size := 0
	for i, line := range bodyLines {
		size += len(line)
		if size > childToolBodyBytes {
			bodyLines = bodyLines[:i]
			folded = true
			break
		}
	}
	for _, line := range bodyLines {
		lines = append(lines, indentRows("  ", t.dim, line, width)...)
	}
	if folded {
		lines = append(lines, "  "+t.dim.Render("[output folded; the full result is in the child session log]"))
	}
	return lines
}

// focusAgentHubRow opens the selected roster row as the focused child view.
// Enter on a running child makes the composer its steering input; a finished
// child opens read-only. Esc later returns to the parent transcript without
// interrupting anything.
func (m *App) focusAgentHubRow() tea.Cmd {
	if !m.run.active || len(m.children) == 0 {
		m.notice = "no child is running"
		return nil
	}
	index := min(max(0, m.overlay.scroll), len(m.children)-1)
	row := m.children[index]
	if row.session == "" {
		m.notice = "the selected child has no session yet"
		return nil
	}
	m.childFocus = &childFocus{
		session:  row.session,
		agent:    row.agent,
		status:   row.status,
		readOnly: row.finished,
		follow:   true,
	}
	// Enter replaces the roster with the child view; one Esc then returns to the
	// parent transcript, matching the documented focus behavior.
	m.overlay = overlayState{kind: OverlayNone}
	m.layout()
	m.refreshChildFocus(row.session)
	if row.finished {
		m.notice = "child " + SanitizeLine(row.agent) + " finished; read-only transcript; Esc returns to the parent"
	} else {
		m.notice = "focused child " + SanitizeLine(row.agent) + "; the composer steers it; Esc returns to the parent"
	}
	return nil
}

// refreshChildFocus reloads one child's display history. It is called when the
// view opens and at child progress or finish boundaries, never on a timer: the
// child log is read without its lock, so the refresh also works while the child
// still writes.
func (m *App) refreshChildFocus(session string) {
	f := m.childFocus
	if f == nil || f.session != session {
		return
	}
	provider, ok := m.host.(SubagentFocus)
	if !ok {
		f.err = "child transcripts are unavailable"
		m.rebuildChildFocus()
		return
	}
	items, err := provider.SubagentHistory(session)
	if err != nil {
		f.err = CapText(SanitizeLine(err.Error()), childErrBytes)
		m.rebuildChildFocus()
		return
	}
	f.err = ""
	f.setItems(items)
	m.rebuildChildFocus()
}

// rebuildChildFocus re-renders the focused transcript at the current width.
func (m *App) rebuildChildFocus() {
	f := m.childFocus
	if f == nil {
		return
	}
	f.rebuild(newTheme(m.noColor), m.width, m.rich, m.noColor)
}

// clearChildFocus returns the main transcript to the parent.
func (m *App) clearChildFocus() {
	if m.childFocus == nil {
		return
	}
	m.childFocus = nil
	m.layout()
}

// submitChildSteering sends a composer line to the focused child. The parent
// transcript is deliberately untouched: the line belongs to the child's log
// and its own transcript view.
func (m *App) submitChildSteering(text string) tea.Cmd {
	f := m.childFocus
	if f == nil {
		return nil
	}
	if f.readOnly {
		m.notice = "child " + SanitizeLine(f.agent) + " finished; Esc returns to the parent"
		m.restoreComposer(text)
		return nil
	}
	provider, ok := m.host.(SubagentFocus)
	if !ok {
		m.notice = "steering a child is unavailable"
		m.restoreComposer(text)
		return nil
	}
	if err := provider.SteerSubagent(f.session, text); err != nil {
		m.notice = "child steering: " + CapText(SanitizeLine(err.Error()), childErrBytes)
		m.restoreComposer(text)
		return nil
	}
	f.addPending(text)
	m.rebuildChildFocus()
	m.notice = "steering queued for " + SanitizeLine(f.agent)
	return nil
}

// restoreComposer puts a rejected submission back when the composer is empty,
// so a read-only or unavailable child does not silently swallow the draft.
func (m *App) restoreComposer(text string) {
	if m.composer.Value() == "" {
		m.composer.SetValue(text)
		m.composer.CursorEnd()
	}
	m.dismissSuggestions()
	m.layout()
}

// scrollChildFocus scrolls the focused transcript. Scrolling up detaches the
// live tail; returning to the bottom reattaches it.
func (m *App) scrollChildFocus(up bool, lines int) {
	f := m.childFocus
	if f == nil {
		return
	}
	offset := f.view.YOffset()
	if up {
		f.view.ScrollUp(lines)
		if f.view.YOffset() != offset {
			f.follow = false
		}
		return
	}
	f.view.ScrollDown(lines)
	f.follow = f.view.AtBottom()
}
