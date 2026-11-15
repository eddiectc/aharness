package tui

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/tools"
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

// agentsOverlay is the read-only definitions browser. It shows the set loaded
// for this process; editing takes effect for the next process.
type agentsOverlay struct {
	rows     []AgentDefView
	expanded map[int]bool
	cursor   int
	scroll   int
	width    int
	height   int
}

func (m *App) openAgentsOverlay() tea.Cmd {
	provider, ok := m.host.(AgentProvider)
	if !ok {
		m.notice = "agent definitions are unavailable"
		return nil
	}
	rows := provider.Agents()
	if len(rows) == 0 {
		m.notice = "no agent definitions were discovered"
	}
	slices.SortStableFunc(rows, func(a, b AgentDefView) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Status, b.Status)
	})
	m.overlay.agents = &agentsOverlay{rows: rows, expanded: make(map[int]bool)}
	m.overlay.push(OverlayAgents)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

func (a *agentsOverlay) resize(width, height int) {
	a.width, a.height = width, height
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
	switch msg.Keystroke() {
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
	}
	a.scrollWindow(max(1, a.height-1))
	return nil
}

func (a *agentsOverlay) wheel(up bool) {
	if up {
		a.cursor = max(0, a.cursor-mouseWheelLines)
	} else {
		a.cursor = min(max(0, len(a.rows)-1), a.cursor+mouseWheelLines)
	}
	a.scrollWindow(max(1, a.height-1))
}

func (a *agentsOverlay) view() string {
	if len(a.rows) == 0 {
		return "no agent definitions were discovered."
	}
	lines := []string{ansi.Truncate(padCell("NAME", 14)+"  "+padCell("SOURCE", 20)+"  "+
		padCell("MODEL", 14)+"  "+padCell("EFFORT", 8)+"  "+
		padCell("TOOLS", 5)+"  "+padCell("MAX_TURNS", 9)+"  "+"STATUS", a.width, "")}
	height := max(1, a.height-1)
	end := min(len(a.rows), a.scroll+height)
	for i := a.scroll; i < end; i++ {
		row := a.rows[i]
		source := row.Source
		if row.Path != "" {
			source += " " + row.Path
		}
		maxTurns := "—"
		if row.MaxTurns > 0 {
			maxTurns = fmt.Sprintf("%d", row.MaxTurns)
		}
		modelName := row.Model
		if modelName == "" {
			modelName = "inherit"
		}
		effort := row.Effort
		if effort == "" {
			effort = "inherit"
		}
		marker := "  "
		if i == a.cursor {
			marker = "> "
		}
		lines = append(lines, ansi.Truncate(marker+padCell(row.Name, 12)+"  "+padCell(source, 20)+"  "+
			padCell(modelName, 14)+"  "+padCell(effort, 8)+"  "+
			fmt.Sprintf("%5d  %9s  %s", len(row.Tools), maxTurns, row.Status), a.width, ""))
		if a.expanded[i] {
			lines = append(lines, a.detailRows(row)...)
		}
	}
	return strings.Join(lines, "\n")
}

// detailRows renders the expanded body of one definitions row.
func (a *agentsOverlay) detailRows(row AgentDefView) []string {
	indent := "    "
	out := []string{indent + "source:  " + SanitizeLine(row.Source)}
	if row.Path != "" {
		out = append(out, indent+"path:    "+SanitizeLine(row.Path))
	}
	if row.Status == "invalid" {
		out = append(out, indent+"error:   "+SanitizeLine(row.Err))
		return out
	}
	if row.Description != "" {
		out = append(out, indent+"about:   "+SanitizeLine(row.Description))
	}
	out = append(out, indent+"tools:   "+SanitizeLine(strings.Join(row.Tools, ", ")))
	if row.PromptFirstLine != "" {
		out = append(out, indent+"prompt:  "+SanitizeLine(row.PromptFirstLine))
	}
	for i := range out {
		out[i] = ansi.Truncate(out[i], a.width, "")
	}
	return out
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

// liveDelegateRows renders the roster rows belonging to one delegate call.
func (m *App) liveDelegateRows(callID string) []*subagentRow {
	var rows []*subagentRow
	for _, row := range m.children {
		if row.callID == callID {
			rows = append(rows, row)
		}
	}
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
