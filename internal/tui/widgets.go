package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"aharness/internal/agent"
	"aharness/internal/model"
	"aharness/internal/tools"
)

// overlayState is the TUI overlay/focus stack. Only the top overlay is drawn;
// closing it restores the overlay below and then the transcript.
type overlayState struct {
	kind   OverlayKind
	stack  []OverlayKind
	focus  int
	help   []string
	scroll int

	table  *tableOverlay
	file   *fileOverlay
	model  *modelOverlay
	effort *effortOverlay
	image  *imageOverlay
}

func (o *overlayState) push(kind OverlayKind) {
	if o.kind != OverlayNone {
		o.stack = append(o.stack, o.kind)
		if len(o.stack) > 4 {
			o.stack = o.stack[len(o.stack)-4:]
		}
	}
	o.kind = kind
	o.focus = 0
	o.scroll = 0
}

func (o *overlayState) pop() {
	o.kind = OverlayNone
	if n := len(o.stack); n > 0 {
		o.kind = o.stack[n-1]
		o.stack = o.stack[:n-1]
	}
	o.focus = 0
}

func (m *App) closeOverlay() {
	if m.overlay.kind == OverlayModel {
		m.cancelModelRefresh()
	}
	m.overlay.pop()
	m.layout()
}

func (m *App) closeOverlayCmd() tea.Cmd {
	cmd := m.clearImageViewerCmd()
	m.closeOverlay()
	return cmd
}

// insert routes text to the focused overlay filter. It reports whether the
// overlay consumed the text.
func (o *overlayState) insert(m *App, text string) bool {
	switch o.kind {
	case OverlayFile:
		if o.file != nil {
			o.file.insertFilter(text)
			return true
		}
	case OverlayModel:
		if o.model != nil {
			o.model.insertFilter(text)
			return true
		}
	case OverlayEffort:
		if o.effort != nil {
			o.effort.insertFilter(text)
			return true
		}
	}
	return false
}

func (o *overlayState) resize(m *App, width, height int) {
	_ = o.resizeCmd(m, width, height)
}

func (o *overlayState) resizeCmd(m *App, width, height int) tea.Cmd {
	innerW, innerH := boxInner(width, height)
	if o.file != nil {
		o.file.resize(innerW, innerH)
	}
	if o.model != nil {
		o.model.resize(innerW, innerH)
	}
	if o.effort != nil {
		o.effort.resize(innerW, innerH)
	}
	if o.table != nil {
		o.table.resize(innerW, innerH)
	}
	if o.image != nil {
		return o.image.resize(m, innerW, innerH)
	}
	return nil
}

func (o *overlayState) update(m *App, msg tea.KeyPressMsg) tea.Cmd {
	key := msg.Keystroke()
	switch key {
	case "tab", "shift+tab":
		o.focus = 1 - o.focus
		return nil
	}
	switch o.kind {
	case OverlayHelp, OverlayDetails:
		switch key {
		case "up", "k":
			o.scroll = max(0, o.scroll-1)
		case "down", "j":
			o.scroll++
		case "pgup":
			o.scroll = max(0, o.scroll-8)
		case "pgdown":
			o.scroll += 8
		}
		return nil
	case OverlayTable:
		if o.table != nil {
			return o.table.update(msg)
		}
	case OverlayFile:
		if o.file != nil {
			return o.file.update(m, msg)
		}
	case OverlayModel:
		if o.model != nil {
			return o.model.update(m, msg)
		}
	case OverlayEffort:
		if o.effort != nil {
			return o.effort.update(m, msg)
		}
	case OverlayImage:
		if o.image != nil {
			return o.image.update(m, msg)
		}
	}
	return nil
}

func (o *overlayState) view(m *App) string {
	t := newTheme(m.noColor)
	innerW, innerH := boxInner(m.width, m.height)
	var title string
	var content string
	switch o.kind {
	case OverlayHelp:
		title = "help"
		content = renderScroll(o.help, innerW, innerH, o.scroll)
	case OverlayDetails:
		title = "details"
		content = renderScroll(o.help, innerW, innerH, o.scroll)
	case OverlayTable:
		title = "table · Ctrl-T"
		if o.table != nil {
			content = o.table.view(innerW, innerH)
			if o.focus == 1 {
				title += " · actions"
			}
		}
	case OverlayFile:
		title = "file picker · Ctrl-O"
		if o.file != nil {
			content = o.file.view()
		}
	case OverlayModel:
		title = "model picker · Ctrl-P"
		if o.model != nil {
			content = o.model.view()
		}
	case OverlayEffort:
		title = "thinking effort · F2"
		if o.effort != nil {
			content = o.effort.view()
		}
	case OverlayImage:
		title = "image · Ctrl-G"
		if o.image != nil {
			content = o.image.view()
		}
	}
	body := t.label.Render(title) + "\n" + content
	return t.box.Render(fitLines(body, innerW, innerH))
}

func boxInner(width, height int) (int, int) {
	boxW := min(width-2, max(38, width*9/10))
	boxH := min(height-2, max(8, height*4/5))
	return max(10, boxW-4), max(3, boxH-2)
}

func renderScroll(lines []string, width, height, offset int) string {
	if len(lines) == 0 {
		return ""
	}
	offset = min(offset, max(0, len(lines)-1))
	end := min(len(lines), offset+height)
	return fitLines(strings.Join(lines[offset:end], "\n"), width, height)
}

func (m *App) openHelpOverlay() tea.Cmd {
	t := newTheme(m.noColor)
	lines := []string{t.label.Render("commands")}
	lines = append(lines, slashCommandHelpLines()...)
	lines = append(lines,
		"",
		t.label.Render("keys"),
		"",
		"Enter            submit the composer (steering while a run is active)",
		"Esc              close overlay, clear composer, or interrupt the operation",
		"Ctrl-C           first interrupt, second cancel the run",
		"Ctrl-D           quit (cancels an active run)",
		"Ctrl-L           full repaint",
		"Ctrl-U           clear the composer",
		"Ctrl-A / Ctrl-E  composer start / end",
		"Ctrl-W / Ctrl-K  delete previous word / to end",
		"Up / Down        input history",
		"PgUp / PgDn      scroll the transcript",
		"Ctrl-R           toggle rich/raw rendering",
		"Ctrl-T           table overlay",
		"Ctrl-G           image viewer",
		"Ctrl-P           model picker (idle)",
		"Ctrl-O           file picker (idle)",
		"F1               help; Ctrl-X details",
		"F2               thinking effort picker (idle)",
		"",
		t.dim.Render("Type a command in the composer; unknown /text is sent as a message."),
		t.dim.Render("Mouse reporting is disabled. Terminal output is sanitized."),
	)
	m.overlay.help = lines
	m.overlay.push(OverlayHelp)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

func detailsSession(m *App) string {
	if m.headless {
		return "(hidden in -ui-json; the session log is the durable record)"
	}
	return SanitizeLine(m.info.SessionID)
}

func detailsTrace(m *App) string {
	if m.headless {
		return "(hidden in -ui-json)"
	}
	return SanitizeLine(m.info.TracePath)
}

// effortLabel renders an effective thinking effort, with the empty value
// spelled out as the provider default.
func effortLabel(value string) string {
	if value == "" {
		return "provider default"
	}
	return SanitizeLine(value)
}

func (m *App) openDetailsOverlay() tea.Cmd {
	t := newTheme(m.noColor)
	lines := []string{
		t.label.Render("session"),
		"session:    " + detailsSession(m),
		"trace:      " + detailsTrace(m),
		"workspace:  " + SanitizeLine(m.info.Workspace),
		"model:      " + SanitizeLine(m.info.Model) + " (" + SanitizeLine(m.info.Dialect) + ")",
		"thinking effort: " + effortLabel(m.nextEffort),
		"turns:      " + formatTurnBudget(m.info.Turns, m.info.MaxTurns),
		"tokens:     " + formatTokenBudget(m.info.Usage.Total(), m.info.MaxTotalTokens),
		"run:        " + fmt.Sprintf("%d", m.run.number),
		"",
		t.label.Render("terminal"),
		"name:       " + SanitizeLine(m.opts.Terminal.Name),
		"version:    " + SanitizeLine(m.opts.Terminal.Version),
		"color:      " + SanitizeLine(m.colorProfile),
		"no-color:   " + fmt.Sprintf("%v", m.noColor),
		"images:     " + SanitizeLine(m.imageProtocol),
	}
	if provider, ok := m.host.(DetailsProvider); ok {
		d := provider.Details()
		maxOutput := fmt.Sprintf("%d", d.MaxOutputTokens)
		if d.MaxOutputUnset {
			maxOutput = "unset"
		}
		lines = append(lines,
			"",
			t.label.Render("configuration"),
			"provider:   "+SanitizeLine(d.Provider),
			"base URL:   "+SanitizeLine(d.BaseURL),
			"prompt:     "+SanitizeLine(d.PromptVersion),
			"toolset:    "+SanitizeLine(d.ToolsetVersion),
			"harness:    "+SanitizeLine(d.HarnessVersion),
			"state dir:  "+SanitizeLine(d.StateDir),
			"max ctx:    "+fmt.Sprintf("%d", d.MaxContextTokens),
			"max output: "+maxOutput,
			"max turns:  "+formatMaxTurns(m.info.MaxTurns),
			"total tokens: "+formatMaxTokens(m.info.MaxTotalTokens),
			"run timeout:  "+formatTimeout(d.SessionTimeout),
			"model timeout: "+formatTimeout(d.ModelTimeout),
		)
	}
	m.overlay.help = lines
	m.overlay.push(OverlayDetails)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

// tableOverlay renders the most recent markdown table.
type tableOverlay struct {
	table  table.Model
	data   *tableData
	xoff   int
	width  int
	height int
}

func (m *App) openTableOverlay() tea.Cmd {
	data := m.latestTable()
	if data == nil {
		m.notice = "no markdown table in the transcript"
		return nil
	}
	t := newTheme(m.noColor)
	cols := make([]table.Column, len(data.headers))
	for i, header := range data.headers {
		w := ansi.StringWidth(header)
		for _, row := range data.rows {
			if i < len(row) {
				w = max(w, ansi.StringWidth(row[i]))
			}
		}
		cols[i] = table.Column{Title: header, Width: min(max(4, w+2), maxTableColumnLen)}
	}
	rows := make([]table.Row, len(data.rows))
	for i, row := range data.rows {
		cells := make([]string, len(cols))
		copy(cells, row)
		rows[i] = table.Row(cells)
	}
	tbl := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithStyles(table.Styles{
			Header:   t.label,
			Cell:     lipgloss.NewStyle().Padding(0, 1),
			Selected: lipgloss.NewStyle().Reverse(true),
		}),
	)
	m.overlay.table = &tableOverlay{table: tbl, data: data}
	m.overlay.push(OverlayTable)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

func (to *tableOverlay) resize(width, height int) {
	to.width = width
	to.height = height
	to.table.SetWidth(width)
	to.table.SetHeight(max(3, height-1))
}

func (to *tableOverlay) update(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Keystroke() {
	case "left", "h":
		to.xoff = max(0, to.xoff-4)
		return nil
	case "right", "l":
		to.xoff += 4
		return nil
	case "home":
		to.xoff = 0
		return nil
	}
	updated, cmd := to.table.Update(msg)
	to.table = updated
	return cmd
}

func (to *tableOverlay) view(width, height int) string {
	lines := strings.Split(to.table.View(), "\n")
	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	for i := range lines {
		lines[i] = ansi.Cut(lines[i], to.xoff, to.xoff+width)
	}
	hint := fmt.Sprintf("←/→ scroll · position %d · %d rows", to.xoff, len(to.data.rows))
	if len(lines) == 0 {
		return hint
	}
	return fitLines(strings.Join(lines, "\n")+"\n"+hint, width, height)
}

func (m *App) latestTable() *tableData {
	for _, v := range slices.Backward(m.items) {
		if v.kind == itemAssistant {
			if table := parseLastTable(v.text); table != nil {
				return table
			}
		}
	}
	return nil
}

// fileOverlay is a Bubbles list constrained to the workspace root. Bubbles'
// filepicker reads directories directly and cannot enforce os.Root/symlink
// containment, so the picker keeps Bubbles' list/filter UX but reads entries
// only through tools.Workspace.ListDisplayDir.
type fileOverlay struct {
	root   *tools.Workspace
	dir    string
	list   list.Model
	err    string
	width  int
	height int
}

type fileItem struct {
	name string
	rel  string
	dir  bool
}

func (f fileItem) Title() string {
	if f.dir {
		return f.name + "/"
	}
	return f.name
}

func (f fileItem) Description() string { return f.rel }

func (f fileItem) FilterValue() string { return f.name }

func (m *App) openFileOverlay() tea.Cmd {
	root := m.isWorkspace()
	if root == nil {
		m.notice = "file picker requires a workspace"
		return nil
	}
	fo := &fileOverlay{root: root, dir: "."}
	fo.reload("")
	if fo.err != "" {
		m.notice = fo.err
		return nil
	}
	m.overlay.file = fo
	m.overlay.push(OverlayFile)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

func (f *fileOverlay) reload(dir string) {
	entries, err := f.root.ListDisplayDir(dir, 2000)
	if err != nil {
		f.err = SanitizeLine(err.Error())
		return
	}
	f.err = ""
	f.dir = dir
	if f.dir == "" {
		f.dir = "."
	}
	items := make([]list.Item, 0, len(entries)+1)
	if f.dir != "." {
		parent := filepath.Dir(f.dir)
		items = append(items, fileItem{name: "..", rel: parent})
	}
	for _, entry := range entries {
		if hasControlRune(entry.Name) {
			f.err = "some file names were rejected because they contain control characters"
			continue
		}
		items = append(items, fileItem{name: SanitizeLine(entry.Name), rel: entry.Rel, dir: entry.Dir})
	}
	if f.list.Width() == 0 {
		f.list = list.New(items, list.NewDefaultDelegate(), f.width, f.height)
		f.list.SetFilteringEnabled(true)
		f.list.SetShowFilter(true)
		f.list.SetShowStatusBar(true)
		f.list.SetShowHelp(false)
		f.list.SetShowPagination(false)
		f.list.SetStatusBarItemName("file", "files")
	} else {
		f.list.SetFilterText("")
		f.list.SetItems(items)
	}
	f.list.Title = "files: ./" + strings.TrimPrefix(SanitizeLine(f.dir), "./")
	f.list.SetFilterText("")
}

func hasControlRune(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

func (f *fileOverlay) insertFilter(text string) {
	f.list.SetFilterText(f.list.FilterInput.Value() + text)
}

func (f *fileOverlay) resize(width, height int) {
	f.width, f.height = width, height
	f.list.SetSize(width, max(3, height-2))
}

func (f *fileOverlay) view() string {
	if f.err != "" {
		return SanitizeLine(f.err)
	}
	return f.list.View()
}

func (f *fileOverlay) update(m *App, msg tea.KeyPressMsg) tea.Cmd {
	key := msg.Keystroke()
	if key == "enter" || key == "right" || key == "l" {
		item, ok := f.list.SelectedItem().(fileItem)
		if !ok {
			return nil
		}
		if item.dir {
			f.reload(item.rel)
			return nil
		}
		if item.rel == "" {
			return nil
		}
		if err := f.confirm(item.rel); err != nil {
			m.notice = SanitizeLine(err.Error())
			return nil
		}
		m.closeOverlay()
		m.composer.InsertString(item.rel)
		m.layout()
		return nil
	}
	if (key == "left" || key == "backspace" || key == "h") && f.list.FilterInput.Value() == "" {
		if f.dir != "." {
			f.reload(filepath.Dir(f.dir))
		}
		return nil
	}
	updated, cmd := f.list.Update(msg)
	f.list = updated
	return cmd
}

// confirm rechecks containment and regular-file status without reading the file.
func (f *fileOverlay) confirm(rel string) error {
	dir := filepath.Dir(rel)
	entries, err := f.root.ListDisplayDir(dir, 2000)
	if err != nil {
		return err
	}
	name := filepath.Base(rel)
	for _, entry := range entries {
		if entry.Name == name && !entry.Dir {
			return nil
		}
	}
	return fmt.Errorf("%s is not a selectable workspace file", rel)
}

// modelOverlay is the catalog-backed model picker.
type modelOverlay struct {
	list       list.Model
	refreshing bool
	width      int
	height     int
}

const offlineSwitchNotice = "model switching is unavailable until the catalog loads; Ctrl-R retries"

type modelItem struct {
	choice  ModelChoice
	current bool
}

func (mi modelItem) Title() string {
	title := mi.name()
	if mi.current {
		title += " (current)"
	}
	return title
}

func (mi modelItem) name() string {
	if name := SanitizeLine(mi.choice.Name); name != "" {
		return name
	}
	return SanitizeLine(mi.choice.ID)
}

func (mi modelItem) Description() string {
	parts := []string{
		SanitizeLine(mi.choice.ID),
		SanitizeLine(string(mi.choice.Dialect)),
		fmt.Sprintf("ctx %d", max(0, mi.choice.ContextWindow)),
		fmt.Sprintf("in %d", max(0, mi.choice.InputLimit)),
		"out " + modelOutputLimit(mi.choice.OutputLimit),
		effortDescription(mi.choice.ReasoningEfforts),
	}
	if status := SanitizeLine(mi.choice.Status); status != "" {
		parts = append(parts, status)
	}
	if mi.choice.Translated {
		parts = append(parts, "translated")
	}
	return strings.Join(parts, " · ")
}

// effortDescription renders the model's advertised effort values for a picker
// row, or a dash when the model has no effort control.
func effortDescription(values []string) string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = SanitizeLine(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) == 0 {
		return "effort -"
	}
	return "effort " + strings.Join(cleaned, "/")
}

func (mi modelItem) FilterValue() string {
	return SanitizeLine(mi.choice.ID + " " + mi.choice.Name)
}

func modelOutputLimit(limit int) string {
	if limit <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", limit)
}

func modelItems(choices []ModelChoice, currentID string) []list.Item {
	items := make([]list.Item, len(choices))
	for i, choice := range choices {
		items[i] = modelItem{choice: choice, current: choice.ID == currentID}
	}
	return items
}

func (m *App) currentModelID() string {
	if m.nextModel != "" {
		return m.nextModel
	}
	return m.info.Model
}

func (m *App) openModelOverlay() tea.Cmd {
	if m.run.active {
		m.notice = "the model picker is disabled while a run is active"
		return nil
	}
	if len(m.models) == 0 {
		m.notice = "no models available"
		return nil
	}
	switchable := false
	for _, choice := range m.models {
		if choice.Switchable {
			switchable = true
			break
		}
	}
	if !switchable {
		m.notice = offlineSwitchNotice
	}
	items := modelItems(m.models, m.currentModelID())
	ml := list.New(items, list.NewDefaultDelegate(), 40, 8)
	current := 0
	for i, choice := range m.models {
		if choice.ID == m.currentModelID() {
			current = i
			break
		}
	}
	ml.Select(current)
	ml.SetShowFilter(true)
	ml.SetShowStatusBar(false)
	ml.SetShowHelp(false)
	ml.SetShowPagination(false)
	ml.Title = "model for the next run"
	mo := &modelOverlay{list: ml}
	m.overlay.model = mo
	m.overlay.push(OverlayModel)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

func (mo *modelOverlay) insertFilter(text string) {
	mo.list.SetFilteringEnabled(true)
	mo.list.SetFilterText(mo.list.FilterInput.Value() + text)
}

func (mo *modelOverlay) resize(width, height int) {
	mo.width, mo.height = width, height
	// Reserve the last line for the refresh/selection footer.
	mo.list.SetSize(width, max(1, height-1))
}

func (mo *modelOverlay) view() string {
	footer := "Ctrl-R refresh · Enter select · Esc close"
	if mo.refreshing {
		footer = "refreshing models…"
	}
	return strings.Join([]string{mo.list.View(), footer}, "\n")
}

func (mo *modelOverlay) update(m *App, msg tea.KeyPressMsg) tea.Cmd {
	if msg.Keystroke() == "enter" {
		item, ok := mo.list.SelectedItem().(modelItem)
		if !ok {
			return nil
		}
		return m.chooseModel(item.choice)
	}
	updated, cmd := mo.list.Update(msg)
	mo.list = updated
	return cmd
}

// effortOverlay is the next-run thinking effort picker. The first row always
// means provider default; the remaining rows are the effective model's
// advertised values in catalog order.
type effortOverlay struct {
	list   list.Model
	width  int
	height int
}

type effortItem struct {
	value string // empty selects the provider default
}

func (ei effortItem) Title() string { return effortLabel(ei.value) }

func (ei effortItem) Description() string {
	if ei.value == "" {
		return "send no reasoning effort field"
	}
	return "reasoning effort " + SanitizeLine(ei.value)
}

func (ei effortItem) FilterValue() string { return effortLabel(ei.value) }

// currentModelChoice returns the picker metadata for the effective model.
func (m *App) currentModelChoice() (ModelChoice, bool) {
	id := m.currentModelID()
	if id == "" {
		return ModelChoice{}, false
	}
	for _, choice := range m.models {
		if choice.ID == id {
			return choice, true
		}
	}
	return ModelChoice{}, false
}

func (m *App) openEffortOverlay() tea.Cmd {
	if m.run.active {
		m.notice = "the thinking effort picker is disabled while a run is active"
		return nil
	}
	choice, ok := m.currentModelChoice()
	if !ok || len(choice.ReasoningEfforts) == 0 {
		if id := SanitizeLine(m.currentModelID()); id != "" {
			m.notice = "model " + id + " advertises no effort levels"
		} else {
			m.notice = "the current model advertises no effort levels"
		}
		return nil
	}
	items := make([]list.Item, 0, len(choice.ReasoningEfforts)+1)
	items = append(items, effortItem{})
	selected := 0
	for _, value := range choice.ReasoningEfforts {
		items = append(items, effortItem{value: value})
		if value == m.nextEffort {
			selected = len(items) - 1
		}
	}
	el := list.New(items, list.NewDefaultDelegate(), 40, 8)
	el.Select(selected)
	el.SetShowFilter(true)
	el.SetShowStatusBar(false)
	el.SetShowHelp(false)
	el.SetShowPagination(false)
	el.Title = "thinking effort for the next run"
	m.overlay.effort = &effortOverlay{list: el}
	m.overlay.push(OverlayEffort)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

func (eo *effortOverlay) insertFilter(text string) {
	eo.list.SetFilteringEnabled(true)
	eo.list.SetFilterText(eo.list.FilterInput.Value() + text)
}

func (eo *effortOverlay) resize(width, height int) {
	eo.width, eo.height = width, height
	eo.list.SetSize(width, max(1, height-1))
}

func (eo *effortOverlay) view() string {
	return strings.Join([]string{eo.list.View(), "Enter select · Esc close"}, "\n")
}

func (eo *effortOverlay) update(m *App, msg tea.KeyPressMsg) tea.Cmd {
	if msg.Keystroke() == "enter" {
		item, ok := eo.list.SelectedItem().(effortItem)
		if !ok {
			return nil
		}
		return m.chooseEffort(item.value)
	}
	updated, cmd := eo.list.Update(msg)
	eo.list = updated
	return cmd
}

// chooseEffort applies an F2 selection to the next run only.
func (m *App) chooseEffort(value string) tea.Cmd {
	m.nextEffort = value
	text := "thinking effort for the next run: " + effortLabel(value)
	m.notice = text
	m.emit(EventNotice{Kind: "thinking_effort", Text: text})
	m.closeOverlay()
	return nil
}

// chooseModel applies a picker selection for the next run. A choice that the
// host marked unswitchable is the offline recorded-model fallback: it stays
// visible but can never replace the current model.
func (m *App) chooseModel(choice ModelChoice) tea.Cmd {
	if !choice.Switchable {
		m.notice = offlineSwitchNotice
		return nil
	}
	selected := choice
	warning := ""
	if selector, ok := m.host.(ModelSelector); ok {
		resolved, err := selector.SelectModel(choice.ID)
		if err != nil {
			m.notice = "select model: " + CapText(SanitizeLine(err.Error()), 512)
			return nil
		}
		selected = resolved
		if provider, ok := m.host.(ModelWarningProvider); ok {
			warning = provider.TakeModelWarning()
		}
	}
	m.nextModel = selected.ID
	m.nextDialect = selected.Dialect
	cleared := ""
	if m.nextEffort != "" && !slices.Contains(selected.ReasoningEfforts, m.nextEffort) {
		cleared = m.nextEffort
		m.nextEffort = ""
	}
	m.notice = "model for the next run: " + SanitizeLine(selected.ID)
	if selected.Status == "deprecated" {
		m.notice += " (deprecated)"
	}
	if warning != "" {
		m.notice += "; " + CapText(SanitizeLine(warning), 512)
	}
	if cleared != "" {
		text := "thinking effort " + `"` + effortLabel(cleared) + `"` + " cleared: model " +
			SanitizeLine(selected.ID) + " does not advertise it; using the provider default"
		m.notice += "; " + CapText(text, 512)
		m.emit(EventNotice{Kind: string(agent.NoticeWarning), Text: text})
	}
	m.emit(EventModelChanged{
		Model:          selected.ID,
		Dialect:        string(selected.Dialect),
		ThinkingEffort: m.nextEffort,
	})
	m.closeOverlay()
	return nil
}

// refreshModelCatalog starts one explicit catalog refresh through the optional
// host capability. The request is canceled when the picker closes, and a
// result whose sequence no longer matches is discarded.
func (m *App) refreshModelCatalog() tea.Cmd {
	mo := m.overlay.model
	if mo == nil || mo.refreshing {
		return nil
	}
	refresher, ok := m.host.(ModelRefresher)
	if !ok {
		m.notice = "model refresh is unavailable"
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.refreshSeq++
	seq := m.refreshSeq
	m.refreshCancel = cancel
	mo.refreshing = true
	return func() tea.Msg {
		choices, err := refresher.RefreshModels(ctx)
		return modelRefreshMsg{seq: seq, choices: choices, err: err}
	}
}

// cancelModelRefresh invalidates any in-flight refresh and releases its
// context. Results that arrive later fail the sequence check in
// handleModelRefresh.
func (m *App) cancelModelRefresh() {
	m.refreshSeq++
	if m.refreshCancel != nil {
		m.refreshCancel()
		m.refreshCancel = nil
	}
	if m.overlay.model != nil {
		m.overlay.model.refreshing = false
	}
}

func (m *App) handleModelRefresh(msg modelRefreshMsg) {
	mo := m.overlay.model
	if mo == nil || msg.seq != m.refreshSeq {
		return
	}
	m.refreshSeq++
	if m.refreshCancel != nil {
		m.refreshCancel()
		m.refreshCancel = nil
	}
	mo.refreshing = false
	if msg.err != nil {
		if len(msg.choices) > 0 && errors.Is(msg.err, model.ErrCatalogCache) {
			// The fetch succeeded but persisting the cache failed. Use the
			// refreshed in-memory catalog and report the failure.
			m.models = msg.choices
			mo.replaceItems(m, msg.choices)
		}
		m.notice = "refresh models: " + CapText(SanitizeLine(msg.err.Error()), 512)
		return
	}
	m.models = msg.choices
	mo.replaceItems(m, msg.choices)
	m.notice = fmt.Sprintf("refreshed %d models", len(msg.choices))
}

// replaceItems swaps the picker entries in place, preserving the filter text
// and the highlighted model ID when the refreshed catalog still has it.
func (mo *modelOverlay) replaceItems(m *App, choices []ModelChoice) {
	filter := mo.list.FilterInput.Value()
	state := mo.list.FilterState()
	selectedID := ""
	if item, ok := mo.list.SelectedItem().(modelItem); ok {
		selectedID = item.choice.ID
	}
	// SetItems only returns an async filter command; an active filter is
	// recomputed synchronously by SetFilterText below, and an unfiltered list
	// needs no filtered slice.
	_ = mo.list.SetItems(modelItems(choices, m.currentModelID()))
	if state != list.Unfiltered {
		mo.list.SetFilterText(filter)
	}
	mo.selectModel(selectedID)
}

// selectModel highlights the retained model ID, or the first switchable entry
// in the current filter when it disappeared. It falls back to the first
// visible entry when no visible entry is switchable.
func (mo *modelOverlay) selectModel(id string) {
	visible := mo.list.VisibleItems()
	firstUsable := -1
	for i, item := range visible {
		entry, ok := item.(modelItem)
		if !ok {
			continue
		}
		if entry.choice.ID == id {
			mo.list.Select(i)
			return
		}
		if firstUsable < 0 && entry.choice.Switchable {
			firstUsable = i
		}
	}
	if firstUsable < 0 && len(visible) > 0 {
		firstUsable = 0
	}
	if firstUsable >= 0 {
		mo.list.Select(firstUsable)
	}
}
