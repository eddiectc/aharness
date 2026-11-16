package tui

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/model"
	"github.com/eddiectc/aharness/internal/tools"
)

// Roster display caps. They keep /agent-hub readable at 24 rows and match the
// documented limits.
const (
	hubVisibleRows = 8
	hubIntentBytes = 200
)

// subagentRow is one child in the live roster. Elapsed is derived from
// startedAt at render time, so a throttled or late progress event cannot skew
// the clock and the display keeps ticking between events.
type subagentRow struct {
	index         int
	callID        string
	agent         string
	intent        string
	session       string
	model         string
	effort        string
	tools         []string
	status        string
	turns         int
	contextTokens int
	usage         model.Usage
	costMicroUSD  int64
	costKnown     bool
	startedAt     time.Time
	duration      time.Duration
	finished      bool
}

// elapsed reports the row's live or terminal duration.
func (r *subagentRow) elapsed(now time.Time) time.Duration {
	if r.finished {
		return r.duration
	}
	if r.startedAt.IsZero() {
		return 0
	}
	return now.Sub(r.startedAt)
}

// findChild returns the roster row for one child session.
func (m *App) findChild(session string) *subagentRow {
	for _, row := range m.children {
		if row.session == session {
			return row
		}
	}
	return nil
}

// childByIndex keeps a stable row when a session ID is not yet available.
func (m *App) childByIndex(callID string, index int) *subagentRow {
	for _, row := range m.children {
		if row.callID == callID && row.index == index {
			return row
		}
	}
	return nil
}

func (m *App) startedChild(ev agent.SubagentStarted) {
	row := m.findChild(ev.Session)
	if row == nil {
		row = m.childByIndex(ev.CallID, ev.Index)
	}
	if row == nil {
		row = &subagentRow{}
		m.children = append(m.children, row)
	}
	row.index = ev.Index
	row.callID = ev.CallID
	row.agent = SanitizeLine(ev.Agent)
	row.intent = CapText(SanitizeLine(ev.Intent), hubIntentBytes)
	row.session = SanitizeLine(ev.Session)
	row.model = SanitizeLine(ev.Model)
	row.effort = SanitizeLine(ev.Effort)
	row.tools = append([]string(nil), ev.Tools...)
	row.status = "running"
	row.startedAt = time.Now()
	if f := m.childFocus; f != nil && f.session == row.session {
		// A continuation can start a new run for the same child session.
		f.status = "running"
		f.readOnly = false
	}
	slices.SortStableFunc(m.children, func(a, b *subagentRow) int { return a.index - b.index })
	m.invalidateDelegateBlock(ev.CallID)
}

func (m *App) progressedChild(ev agent.SubagentProgress) {
	row := m.findChild(ev.Session)
	if row == nil {
		return
	}
	row.turns = ev.ChildTurns
	row.contextTokens = ev.ContextTokens
	row.usage = ev.Usage
	row.costMicroUSD = ev.CostMicroUSD
	row.costKnown = ev.CostComplete
	m.invalidateDelegateBlock(ev.CallID)
}

func (m *App) finishedChild(ev agent.SubagentFinished) {
	row := m.findChild(ev.Session)
	if row == nil {
		return
	}
	row.status = SanitizeLine(ev.Status)
	row.turns = ev.ChildTurns
	row.contextTokens = ev.ContextTokens
	row.usage = ev.Usage
	row.costMicroUSD = ev.CostMicroUSD
	row.costKnown = ev.CostComplete
	row.duration = ev.Duration
	row.finished = true
	m.invalidateDelegateBlock(ev.CallID)
}

// invalidateDelegateBlock re-renders the delegate tool block that owns callID
// so a progress or finish event updates its nested task line without adding a
// transcript item.
func (m *App) invalidateDelegateBlock(callID string) {
	if callID == "" {
		return
	}
	if it := m.toolItem(callID); it != nil {
		it.invalidateLines()
	}
	m.dirty = true
}

// openAgentHubOverlay opens the live roster. It is display-only and works
// while a run is active.
func (m *App) openAgentHubOverlay() tea.Cmd {
	rows := 0
	if m.run.active {
		rows = len(m.children)
	}
	if rows == 0 {
		m.notice = "no child is running"
	}
	m.overlay.push(OverlayAgentHub)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

// hubColumn is one responsive roster column.
type hubColumn struct {
	title string
	width int
}

// hubLayout selects and sizes the roster columns for the available width.
// Columns drop in the documented order: intent and turns below 60 columns,
// context tokens below 70, model and effort below 80, cost below 90. The model
// and intent columns share the leftover space; when the selected set still
// does not fit, the lowest-priority optional column is dropped so agent,
// elapsed, and status always survive.
func hubLayout(width int) []hubColumn {
	cols := []hubColumn{{title: "AGENT", width: 10}}
	if width >= 60 {
		cols = append(cols, hubColumn{title: "INTENT", width: 8})
	}
	if width >= 80 {
		cols = append(cols, hubColumn{title: "MODEL", width: 14}, hubColumn{title: "EFFORT", width: 8})
	}
	cols = append(cols, hubColumn{title: "ELAPSED", width: 8})
	if width >= 70 {
		cols = append(cols, hubColumn{title: "CTX", width: 6})
	}
	if width >= 60 {
		cols = append(cols, hubColumn{title: "TURNS", width: 5})
	}
	if width >= 90 {
		cols = append(cols, hubColumn{title: "COST", width: 10})
	}
	cols = append(cols, hubColumn{title: "STATUS", width: 9})

	total := 0
	for i, col := range cols {
		if i > 0 {
			total += 2
		}
		total += col.width
	}
	index := func(title string) int {
		for i, col := range cols {
			if col.title == title {
				return i
			}
		}
		return -1
	}
	modelIndex := index("MODEL")
	intentIndex := index("INTENT")

	// Shrink the flexible columns before dropping any: the model may give up
	// six columns, then the intent two.
	if over := total - width; over > 0 && modelIndex >= 0 {
		shrink := min(over, cols[modelIndex].width-10)
		cols[modelIndex].width -= shrink
		total -= shrink
	}
	if over := total - width; over > 0 && intentIndex >= 0 {
		shrink := min(over, cols[intentIndex].width-6)
		cols[intentIndex].width -= shrink
		total -= shrink
	}

	// Give the flexible columns the leftover space: the model widens to its
	// full ID width first, then the intent takes the rest.
	leftover := max(0, width-total)
	if modelIndex >= 0 {
		extra := min(leftover, 10)
		if intentIndex >= 0 {
			extra = min(extra, max(0, leftover-8))
		}
		cols[modelIndex].width += extra
		leftover -= extra
	}
	if intentIndex >= 0 {
		cols[intentIndex].width += leftover
	}

	// If even the selected set is too wide, drop optional columns in priority
	// order.
	for len(cols) > 3 {
		total := 0
		for i, col := range cols {
			if i > 0 {
				total += 2
			}
			total += col.width
		}
		if total <= width {
			break
		}
		drop := -1
		for _, title := range []string{"INTENT", "COST", "TURNS", "CTX", "EFFORT", "MODEL"} {
			if i := index(title); i >= 0 {
				drop = i
				break
			}
		}
		if drop < 0 {
			break
		}
		cols = append(cols[:drop], cols[drop+1:]...)
	}
	return cols
}

// renderHubRow formats one roster row for the selected columns.
func renderHubRow(cols []hubColumn, cells map[string]string) string {
	parts := make([]string, 0, len(cols))
	for _, col := range cols {
		parts = append(parts, padCell(cells[col.title], col.width))
	}
	return strings.TrimRight(strings.Join(parts, "  "), " ")
}

// padCell truncates or right-pads one roster cell to width. A truncated cell
// ends with an ellipsis so a dropped tail is visible.
func padCell(value string, width int) string {
	value = ansi.Truncate(SanitizeLine(value), width, "…")
	if gap := width - ansi.StringWidth(value); gap > 0 {
		value += strings.Repeat(" ", gap)
	}
	return value
}

// agentHubView renders the roster overlay body.
func (m *App) agentHubView(width, height int) string {
	now := time.Now()
	if !m.run.active || len(m.children) == 0 {
		return "no child is running.\n\nChildren exist only inside a delegate call. Esc closes this panel."
	}
	cols := hubLayout(width)
	var lines []string
	header := make(map[string]string, len(cols))
	for _, col := range cols {
		header[col.title] = col.title
	}
	lines = append(lines, renderHubRow(cols, header))

	rows := m.children
	start := min(max(0, m.overlay.scroll), max(0, len(rows)-1))
	visible := min(hubVisibleRows, max(0, len(rows)-start))
	if height > 0 {
		visible = min(visible, max(0, height-2))
	}
	for i := 0; i < visible; i++ {
		lines = append(lines, renderHubRow(cols, hubCells(rows[start+i], now)))
	}
	if omitted := len(rows) - start - visible; omitted > 0 {
		lines = append(lines, fmt.Sprintf("+%d more", omitted))
	}
	return strings.Join(lines, "\n")
}

// hubCells maps one roster row onto the column titles.
func hubCells(row *subagentRow, now time.Time) map[string]string {
	status := row.status
	if status == "" {
		status = "running"
	}
	effort := row.effort
	if effort == "" {
		effort = "default"
	}
	cost := "—"
	if row.costKnown {
		cost = formatCostMicroUSD(row.costMicroUSD, true)
	}
	intent := row.intent
	if collapsed := strings.Join(strings.Fields(intent), " "); collapsed != "" {
		intent = collapsed
	}
	return map[string]string{
		"AGENT":   row.agent,
		"INTENT":  intent,
		"MODEL":   row.model,
		"EFFORT":  effort,
		"ELAPSED": formatElapsed(row.elapsed(now)),
		"CTX":     humanTokens(row.contextTokens),
		"TURNS":   fmt.Sprintf("%d", row.turns),
		"COST":    cost,
		"STATUS":  status,
	}
}

// maxAgentSettingInputBytes bounds one inline setting edit. The model cap is
// the largest accepted value; a longer buffer is refused before it is written.
const maxAgentSettingInputBytes = 200

// agentsOverlay is the definitions browser. It shows the set loaded for this
// process with the current operator overrides applied for display; a saved
// setting writes the active config file and takes effect on the next process.
// Only model, effort, max_turns, and enabled are editable: the tool grant and
// prompt body stay in the definition file so this surface cannot widen
// authority.
type agentsOverlay struct {
	rows     []AgentDefView
	expanded map[int]bool
	cursor   int
	scroll   int
	width    int
	height   int

	provider AgentProvider
	settings AgentSettings

	editing bool
	editKey string
	editBuf string
	editErr string
	notice  string
}

func (m *App) openAgentsOverlay() tea.Cmd {
	provider, ok := m.host.(AgentProvider)
	if !ok {
		m.notice = "agent definitions are unavailable"
		return nil
	}
	settings, _ := m.host.(AgentSettings)
	rows := provider.Agents()
	if len(rows) == 0 {
		m.notice = "no agent definitions were discovered"
	}
	sortAgentRows(rows)
	notice := ""
	if settings == nil {
		notice = "agent settings are read-only in this host"
	}
	m.overlay.agents = &agentsOverlay{
		rows:     rows,
		expanded: make(map[int]bool),
		provider: provider,
		settings: settings,
		notice:   notice,
	}
	m.overlay.push(OverlayAgents)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

// sortAgentRows orders the browser by name, with the invalid row for a name
// after its active definition.
func sortAgentRows(rows []AgentDefView) {
	slices.SortStableFunc(rows, func(a, b AgentDefView) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Status, b.Status)
	})
}

func (a *agentsOverlay) resize(width, height int) {
	a.width, a.height = width, height
}

// currentRow returns the row under the cursor, or nil when the list is empty.
func (a *agentsOverlay) currentRow() *AgentDefView {
	if a.cursor < 0 || a.cursor >= len(a.rows) {
		return nil
	}
	return &a.rows[a.cursor]
}

// currentName is the row name for the footer prompt.
func (a *agentsOverlay) currentName() string {
	if row := a.currentRow(); row != nil {
		return row.Name
	}
	return ""
}

// bodyHeight is the number of transcript rows available after the header,
// legend, and footer.
func (a *agentsOverlay) bodyHeight() int {
	return max(1, a.height-2-len(a.footer()))
}

// refresh re-reads the browser rows from the host after a successful save. The
// host applies the saved override for display while the process keeps the
// catalog it loaded for running children.
func (a *agentsOverlay) refresh() {
	if a.provider == nil {
		return
	}
	rows := a.provider.Agents()
	sortAgentRows(rows)
	a.rows = rows
	a.cursor = min(max(0, a.cursor), max(0, len(a.rows)-1))
	a.scrollWindow(a.bodyHeight())
}

// resetScroll keeps the cursor inside the visible window.
func (a *agentsOverlay) scrollWindow(height int) {
	limit := max(0, len(a.rows)-max(1, height))
	a.cursor = min(max(0, a.cursor), max(0, len(a.rows)-1))
	a.scroll = min(max(0, a.scroll), limit)
	if a.cursor < a.scroll {
		a.scroll = a.cursor
	}
	if a.cursor >= a.scroll+max(1, height) {
		a.scroll = a.cursor - max(1, height) + 1
	}
}

func (a *agentsOverlay) update(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.Keystroke()
	if a.editing {
		switch key {
		case "enter":
			a.commitEdit()
		case "backspace":
			a.backspace()
		case "ctrl+u":
			a.editBuf = ""
			a.editErr = ""
		default:
			// A modal overlay swallows the composer's printable-input path, so
			// the editor consumes the key's text itself. Paste arrives through
			// insertInput and takes the same buffer.
			if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModMeta) {
				a.insert(Sanitize(msg.Text))
			}
		}
		a.scrollWindow(a.bodyHeight())
		return nil
	}
	switch key {
	case "up", "k":
		a.cursor = max(0, a.cursor-1)
	case "down", "j":
		a.cursor = min(max(0, len(a.rows)-1), a.cursor+1)
	case "pgup":
		a.cursor = max(0, a.cursor-8)
	case "pgdown":
		a.cursor = min(max(0, len(a.rows)-1), a.cursor+8)
	case "home":
		a.cursor = 0
	case "end":
		a.cursor = max(0, len(a.rows)-1)
	case "enter":
		if a.expanded == nil {
			a.expanded = make(map[int]bool)
		}
		a.expanded[a.cursor] = !a.expanded[a.cursor]
	case "m":
		a.startEdit("model")
	case "e":
		a.startEdit("effort")
	case "t":
		a.startEdit("max_turns")
	case "d":
		a.toggleEnabled()
	case "r":
		a.resetOverrides()
	}
	a.scrollWindow(a.bodyHeight())
	return nil
}

func (a *agentsOverlay) wheel(up bool) {
	if a.editing {
		return
	}
	if up {
		a.cursor = max(0, a.cursor-mouseWheelLines)
	} else {
		a.cursor = min(max(0, len(a.rows)-1), a.cursor+mouseWheelLines)
	}
	a.scrollWindow(a.bodyHeight())
}

// insert appends printable text to the active inline edit. It reports whether
// the overlay consumed the text; when no edit is active the composer keeps it,
// matching the browser's previous behavior.
func (a *agentsOverlay) insert(text string) bool {
	if !a.editing {
		return false
	}
	if len(a.editBuf)+len(text) > maxAgentSettingInputBytes {
		a.editErr = fmt.Sprintf("input is capped at %d bytes", maxAgentSettingInputBytes)
		return true
	}
	a.editBuf += text
	a.editErr = ""
	return true
}

// command consumes the single-character editing keys when a headless client
// sends them as text instead of key presses. The interactive path handles the
// same keys in update.
func (a *agentsOverlay) command(text string) bool {
	if a.editing || len(text) != 1 {
		return false
	}
	switch text {
	case "m":
		a.startEdit("model")
	case "e":
		a.startEdit("effort")
	case "t":
		a.startEdit("max_turns")
	case "d":
		a.toggleEnabled()
	case "r":
		a.resetOverrides()
	default:
		return false
	}
	a.scrollWindow(a.bodyHeight())
	return true
}

func (a *agentsOverlay) backspace() {
	if a.editBuf == "" {
		return
	}
	_, size := utf8.DecodeLastRuneInString(a.editBuf)
	a.editBuf = a.editBuf[:len(a.editBuf)-size]
	a.editErr = ""
}

// cancelEdit drops the inline editor without saving. The App calls it for Esc
// so an edit cancels before the overlay closes.
func (a *agentsOverlay) cancelEdit() {
	a.editing = false
	a.editBuf = ""
	a.editErr = ""
}

// startEdit opens the inline editor for one routing setting. The buffer is
// empty when no override is set, so an empty submission clears the override
// instead of copying a definition value into the config file.
func (a *agentsOverlay) startEdit(key string) {
	row := a.currentRow()
	if row == nil || row.Status == "invalid" {
		return
	}
	if a.settings == nil {
		a.notice = "agent settings are read-only in this host"
		return
	}
	a.editing = true
	a.editKey = key
	a.editBuf = ""
	a.editErr = ""
	a.notice = ""
	if row.Override == nil {
		return
	}
	switch key {
	case "model":
		if row.Override.Model != nil {
			a.editBuf = *row.Override.Model
		}
	case "effort":
		if row.Override.ThinkingEffort != nil {
			a.editBuf = *row.Override.ThinkingEffort
		}
	case "max_turns":
		if row.Override.MaxTurns != nil {
			a.editBuf = fmt.Sprintf("%d", *row.Override.MaxTurns)
		}
	}
}

// commitEdit saves the buffer as one field of the row's complete override. A
// failed validation or write keeps the editor open with the error shown.
func (a *agentsOverlay) commitEdit() {
	row := a.currentRow()
	if row == nil || a.settings == nil {
		a.cancelEdit()
		return
	}
	value := strings.TrimSpace(a.editBuf)
	next := cloneAgentOverride(row.Override)
	switch a.editKey {
	case "model":
		if value == "" {
			next.Model = nil
		} else {
			next.Model = &value
		}
	case "effort":
		if value == "" {
			next.ThinkingEffort = nil
		} else {
			next.ThinkingEffort = &value
		}
	case "max_turns":
		if value == "" {
			next.MaxTurns = nil
		} else {
			turns, err := strconv.Atoi(value)
			if err != nil || turns <= 0 {
				a.editErr = "max_turns must be a positive integer"
				return
			}
			next.MaxTurns = &turns
		}
	default:
		a.cancelEdit()
		return
	}
	if err := a.settings.SetAgentOverride(row.Name, next); err != nil {
		a.editErr = SanitizeLine(err.Error())
		return
	}
	label := agentSettingLabel(a.editKey)
	a.editing = false
	a.editBuf = ""
	a.editErr = ""
	a.notice = "saved " + label + "; applies to the next process"
	a.refresh()
}

// toggleEnabled flips the row's operator enable state. Enabling writes an
// explicit true; r removes the override entirely.
func (a *agentsOverlay) toggleEnabled() {
	row := a.currentRow()
	if row == nil || row.Status == "invalid" {
		return
	}
	if a.settings == nil {
		a.notice = "agent settings are read-only in this host"
		return
	}
	next := cloneAgentOverride(row.Override)
	enabled := !row.Enabled
	next.Enabled = &enabled
	if err := a.settings.SetAgentOverride(row.Name, next); err != nil {
		a.notice = "save failed: " + SanitizeLine(err.Error())
		return
	}
	a.notice = fmt.Sprintf("saved enabled=%t; applies to the next process", enabled)
	a.refresh()
}

// resetOverrides removes every operator override for the row.
func (a *agentsOverlay) resetOverrides() {
	row := a.currentRow()
	if row == nil || row.Status == "invalid" {
		return
	}
	if a.settings == nil {
		a.notice = "agent settings are read-only in this host"
		return
	}
	if row.Override == nil {
		a.notice = "no operator override to clear"
		return
	}
	if err := a.settings.ClearAgentOverrides(row.Name); err != nil {
		a.notice = "save failed: " + SanitizeLine(err.Error())
		return
	}
	a.notice = "overrides cleared; applies to the next process"
	a.refresh()
}

// cloneAgentOverride deep-copies the persisted override so an edit cannot
// mutate the view or a previously saved pointer.
func cloneAgentOverride(o *agent.AgentOverride) agent.AgentOverride {
	if o == nil {
		return agent.AgentOverride{}
	}
	out := *o
	if o.Model != nil {
		value := *o.Model
		out.Model = &value
	}
	if o.ThinkingEffort != nil {
		value := *o.ThinkingEffort
		out.ThinkingEffort = &value
	}
	if o.MaxTurns != nil {
		value := *o.MaxTurns
		out.MaxTurns = &value
	}
	if o.Enabled != nil {
		value := *o.Enabled
		out.Enabled = &value
	}
	return out
}

// agentSettingLabel is the human name used in prompts and notices.
func agentSettingLabel(key string) string {
	switch key {
	case "model":
		return "model"
	case "effort":
		return "thinking effort"
	case "max_turns":
		return "max turns"
	default:
		return key
	}
}

// footer renders the inline editor, a save error, or the last save notice.
func (a *agentsOverlay) footer() []string {
	var lines []string
	switch {
	case a.editing:
		lines = append(lines, fmt.Sprintf("%s override for %s (empty clears): %s▏",
			agentSettingLabel(a.editKey), a.currentName(), a.editBuf))
		if a.editErr != "" {
			lines = append(lines, "error: "+a.editErr)
		}
		lines = append(lines, "Enter saves · Esc cancels")
	case a.editErr != "":
		lines = append(lines, "error: "+a.editErr)
	case a.notice != "":
		lines = append(lines, a.notice)
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(SanitizeLine(line), a.width, "…")
	}
	return lines
}

func (a *agentsOverlay) view() string {
	if len(a.rows) == 0 {
		return "no agent definitions were discovered."
	}
	header := ansi.Truncate(padCell("NAME", 14)+"  "+padCell("SOURCE", 20)+"  "+
		padCell("MODEL", 14)+"  "+padCell("EFFORT", 8)+"  "+
		padCell("TOOLS", 5)+"  "+padCell("MAX_TURNS", 9)+"  "+"STATUS", a.width, "")
	legend := "* = operator override · m model · e effort · t max_turns · d enabled · r reset · Enter details"
	lines := []string{header, ansi.Truncate(legend, a.width, "…")}

	bodyHeight := a.bodyHeight()
	body := make([]string, 0, bodyHeight)
	last := a.scroll - 1
	for i := a.scroll; i < len(a.rows) && len(body) < bodyHeight; i++ {
		row := a.rows[i]
		last = i
		source := row.Source
		if row.Path != "" {
			source += " " + row.Path
		}
		modelName := row.Model
		if modelName == "" {
			modelName = "inherit"
		}
		if row.ModelSource == "override" {
			modelName += "*"
		}
		effort := row.Effort
		if effort == "" {
			effort = "inherit"
			if row.EffortSource == "model default" {
				effort = "default"
			}
		}
		if row.EffortSource == "override" {
			effort += "*"
		}
		maxTurns := "—"
		if row.MaxTurns > 0 {
			maxTurns = fmt.Sprintf("%d", row.MaxTurns)
		}
		if row.MaxTurnsSource == "override" {
			maxTurns += "*"
		}
		marker := "  "
		if i == a.cursor {
			marker = "> "
		}
		body = append(body, ansi.Truncate(marker+padCell(row.Name, 12)+"  "+padCell(source, 20)+"  "+
			padCell(modelName, 14)+"  "+padCell(effort, 8)+"  "+
			fmt.Sprintf("%5d  %9s  %s", len(row.Tools), maxTurns, SanitizeLine(row.Status)), a.width, ""))
		if a.expanded[i] {
			for _, detail := range a.detailRows(row) {
				if len(body) >= bodyHeight {
					break
				}
				body = append(body, detail)
			}
		}
	}
	lines = append(lines, body...)
	if omitted := len(a.rows) - (last + 1); omitted > 0 {
		lines = append(lines, fmt.Sprintf("+%d more", omitted))
	}
	lines = append(lines, a.footer()...)
	return strings.Join(lines, "\n")
}

// detailRows renders the expanded body of one definitions row, including the
// effective value and provenance of each overridable setting.
func (a *agentsOverlay) detailRows(row AgentDefView) []string {
	indent := "    "
	out := []string{indent + "source:    " + SanitizeLine(row.Source)}
	if row.Path != "" {
		out = append(out, indent+"path:      "+SanitizeLine(row.Path))
	}
	if row.Status == "invalid" {
		out = append(out, indent+"error:     "+SanitizeLine(row.Err))
		return truncateLines(out, a.width)
	}
	if row.Description != "" {
		out = append(out, indent+"about:     "+SanitizeLine(row.Description))
	}
	out = append(out,
		indent+"model:     "+settingDetail(row.Model, "inherit", row.ModelSource, row.DeclaredModel),
		indent+"effort:    "+settingDetail(row.Effort, effortFallback(row), row.EffortSource, row.DeclaredEffort),
		indent+"max_turns: "+settingDetail(maxTurnsText(row.MaxTurns), "—", row.MaxTurnsSource, declaredMaxTurnsText(row)),
		indent+"enabled:   "+enabledDetail(row),
		indent+"tools:     "+SanitizeLine(strings.Join(row.Tools, ", ")),
	)
	if row.PromptFirstLine != "" {
		out = append(out, indent+"prompt:    "+SanitizeLine(row.PromptFirstLine))
	}
	return truncateLines(out, a.width)
}

// settingDetail renders one effective setting and its source. When an override
// is in effect, the definition's declared value follows so the operator can
// see what the override replaced.
func settingDetail(value, fallback, source, declared string) string {
	shown := value
	if shown == "" {
		shown = fallback
	}
	detail := source
	if source == "override" && declared != shown {
		if declared == "" {
			declared = "—"
		}
		detail += "; file: " + declared
	}
	return shown + "  (" + detail + ")"
}

// effortFallback labels an empty effective effort: a model switch resets it to
// the model default, while no value at all inherits from the parent run.
func effortFallback(row AgentDefView) string {
	if row.EffortSource == "model default" {
		return "model default"
	}
	return "inherit"
}

// maxTurnsText renders a positive turn cap, or an empty string for unlimited.
func maxTurnsText(turns int) string {
	if turns > 0 {
		return fmt.Sprintf("%d", turns)
	}
	return ""
}

// declaredMaxTurnsText renders the definition's cap for provenance; an empty
// value means the file declares no cap.
func declaredMaxTurnsText(row AgentDefView) string {
	if row.DeclaredMaxTurns > 0 {
		return fmt.Sprintf("%d", row.DeclaredMaxTurns)
	}
	return ""
}

// enabledDetail shows the effective enable state and whether an override set
// it.
func enabledDetail(row AgentDefView) string {
	state := "yes"
	if !row.Enabled {
		state = "no"
	}
	source := "default"
	if row.Override != nil && row.Override.Enabled != nil {
		source = "override"
	}
	return state + "  (" + source + ")"
}

// renderDelegateTaskRow renders one nested per-task line for a delegate block.
// Live rows come from the roster; recorded rows come from the tool details.
func renderDelegateTaskRow(index int, agentName, status, modelName, effort string, turns, contextTokens int, usage model.Usage, costMicroUSD int64, costKnown bool, elapsed time.Duration, showElapsed bool, width int) string {
	status = SanitizeLine(status)
	if status == "" {
		status = "running"
	}
	effort = effortLabel(effort)
	modelName = SanitizeLine(modelName)
	if modelName == "" {
		modelName = "model inherit"
	}
	cost := "cost —"
	if costKnown {
		cost = "cost " + formatCostMicroUSD(costMicroUSD, true)
	}
	parts := []string{
		fmt.Sprintf("[%d] %s", index+1, SanitizeLine(agentName)),
		status,
	}
	if showElapsed {
		parts = append(parts, formatElapsed(elapsed))
	}
	parts = append(parts, modelName, "effort "+effort,
		fmt.Sprintf("%d turns", turns),
		"ctx "+humanTokens(contextTokens),
		humanTokens(usage.Total())+" tok",
		cost,
	)
	return ansi.Truncate("  "+strings.Join(parts, " · "), width, "")
}

// liveDelegateRows renders the roster rows belonging to one delegate call in
// task order. Concurrent children may start in a different order, so the block
// sorts by index rather than relying on event arrival.
func (m *App) liveDelegateRows(callID string) []*subagentRow {
	var rows []*subagentRow
	for _, row := range m.children {
		if row.callID == callID {
			rows = append(rows, row)
		}
	}
	slices.SortStableFunc(rows, func(a, b *subagentRow) int { return a.index - b.index })
	return rows
}

// delegateDetailsRows renders the recorded per-task table of one delegate
// result after a resume or after the call completed.
func delegateDetailsRows(details *tools.DelegateDetails, width int) []string {
	if details == nil {
		return nil
	}
	rows := make([]string, 0, len(details.Tasks))
	for i, task := range details.Tasks {
		rows = append(rows, renderDelegateTaskRow(i, task.Agent, task.Status, task.Model, task.Effort,
			task.Turns, task.ContextTokens, task.Usage, task.CostMicroUSD, task.CostComplete, 0, false, width))
	}
	return rows
}

// SubagentTaskLine is the optional per-task summary carried on the -ui-json
// tool_started and tool_finished payloads. It adds no event kind and no
// protocol version bump.
type SubagentTaskLine struct {
	Agent         string `json:"agent"`
	Intent        string `json:"intent,omitempty"`
	Session       string `json:"session,omitempty"`
	Model         string `json:"model,omitempty"`
	Effort        string `json:"effort,omitempty"`
	Status        string `json:"status,omitempty"`
	Turns         int    `json:"turns,omitempty"`
	ContextTokens int    `json:"context_tokens,omitempty"`
	InputTokens   int    `json:"input_tokens,omitempty"`
	OutputTokens  int    `json:"output_tokens,omitempty"`
	CostMicroUSD  *int64 `json:"cost_micro_usd,omitempty"`
}

// subagentTasksFromArgs extracts the requested tasks from one delegate call so
// tool_started can describe what is about to run.
func subagentTasksFromArgs(raw json.RawMessage) []SubagentTaskLine {
	var args struct {
		Tasks []struct {
			Agent   string `json:"agent"`
			Task    string `json:"task"`
			Session string `json:"session"`
		} `json:"tasks"`
	}
	if err := jsonv2.Unmarshal(raw, &args); err != nil {
		return nil
	}
	out := make([]SubagentTaskLine, 0, len(args.Tasks))
	for _, task := range args.Tasks {
		out = append(out, SubagentTaskLine{
			Agent:   SanitizeLine(task.Agent),
			Intent:  CapText(SanitizeLine(intentLine(task.Task)), hubIntentBytes),
			Session: SanitizeLine(task.Session),
		})
	}
	return out
}

// subagentTasksFromDetails extracts the recorded per-task table of a finished
// delegate call.
func subagentTasksFromDetails(details *tools.DelegateDetails) []SubagentTaskLine {
	if details == nil {
		return nil
	}
	out := make([]SubagentTaskLine, 0, len(details.Tasks))
	for _, task := range details.Tasks {
		line := SubagentTaskLine{
			Agent:         SanitizeLine(task.Agent),
			Intent:        CapText(SanitizeLine(task.Intent), hubIntentBytes),
			Session:       SanitizeLine(task.Session),
			Model:         SanitizeLine(task.Model),
			Effort:        SanitizeLine(task.Effort),
			Status:        SanitizeLine(task.Status),
			Turns:         task.Turns,
			ContextTokens: task.ContextTokens,
			InputTokens:   task.Usage.InputTokens,
			OutputTokens:  task.Usage.OutputTokens,
		}
		if task.CostComplete {
			cost := task.CostMicroUSD
			line.CostMicroUSD = &cost
		}
		out = append(out, line)
	}
	return out
}

// intentLine is the first non-empty line of a task prompt, whitespace
// collapsed. It mirrors the roster intent the spawner derives.
func intentLine(task string) string {
	for line := range strings.SplitSeq(task, "\n") {
		if collapsed := strings.Join(strings.Fields(line), " "); collapsed != "" {
			return collapsed
		}
	}
	return ""
}
