package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Config-file keys whose values apply to this process immediately. Every other
// saved key is used at the next startup.
const (
	SettingRich      = "ui_rich"
	SettingExpandAll = "ui_expand_all"
)

const (
	settingsLabelWidth = 20
	settingsValueWidth = 22
	settingsSourceWide = 7
	// maxSettingInputBytes bounds one inline config value. It is larger than
	// the agent setting cap because a workspace path or URL can be long.
	maxSettingInputBytes = 512
)

// settingsOverlay is the /settings config-file browser. A saved value is
// written atomically to the active config file; the two display preferences
// apply to this process immediately and every other key is used at the next
// startup. Secrets are never prefilled, rendered, or echoed.
type settingsOverlay struct {
	view   SettingsView
	cursor int
	scroll int
	width  int
	height int

	provider SettingsProvider
	editor   SettingsEditor

	editing bool
	editKey string
	editBuf string

	notice  string
	errText string
}

// currentSettings is the fallback view for a host without SettingsProvider. It
// exposes only the in-memory display state.
func (m *App) currentSettings() SettingsView {
	return SettingsView{Fields: []SettingField{
		{
			Key: SettingRich, Label: "assistant rendering", Kind: SettingToggle,
			Value: strconv.FormatBool(m.rich), Display: richDisplay(m.rich),
			Source: "runtime", SourceDetail: "runtime", Live: true,
			Options: richOptions(), Detail: "applies now · runtime value",
		},
		{
			Key: SettingExpandAll, Label: "block folding", Kind: SettingToggle,
			Value: strconv.FormatBool(m.expandAll), Display: foldDisplay(m.expandAll),
			Source: "runtime", SourceDetail: "runtime", Live: true,
			Options: foldOptions(), Detail: "applies now · runtime value",
		},
	}}
}

func richOptions() []SettingOption {
	return []SettingOption{{Value: "true", Label: "rich"}, {Value: "false", Label: "raw"}}
}

func foldOptions() []SettingOption {
	return []SettingOption{{Value: "true", Label: "expanded"}, {Value: "false", Label: "folded"}}
}

func richDisplay(rich bool) string {
	if rich {
		return "rich"
	}
	return "raw"
}

func foldDisplay(expanded bool) string {
	if expanded {
		return "expanded"
	}
	return "folded"
}

// applySettings installs the live display preferences from a host view.
func (m *App) applySettings(view SettingsView) {
	m.applySettingFields(view.Fields)
}

// applySettingFields updates the rich/raw and fold modes from the matching
// rows. Other fields are process-scoped and are not applied here.
func (m *App) applySettingFields(fields []SettingField) {
	rich, expand := m.rich, m.expandAll
	for _, field := range fields {
		switch field.Key {
		case SettingRich:
			if value, err := strconv.ParseBool(field.Value); err == nil {
				rich = value
			}
		case SettingExpandAll:
			if value, err := strconv.ParseBool(field.Value); err == nil {
				expand = value
			}
		}
	}
	if rich != m.rich {
		m.rich = rich
		m.invalidateRenders()
	}
	if expand != m.expandAll {
		m.expandAll = expand
		m.invalidateFolds()
	}
	m.ensureContent()
}

// openSettingsOverlay opens the config-file browser.
func (m *App) openSettingsOverlay() tea.Cmd {
	provider, _ := m.host.(SettingsProvider)
	editor, _ := m.host.(SettingsEditor)
	view := m.currentSettings()
	if provider != nil {
		view = provider.Settings()
	}
	s := &settingsOverlay{view: view, provider: provider, editor: editor}
	if editor == nil {
		s.notice = "TUI settings are read-only in this host"
	}
	m.overlay.settings = s
	m.overlay.push(OverlaySettings)
	m.overlay.resize(m, m.width, m.height)
	return nil
}

func (s *settingsOverlay) resize(width, height int) {
	s.width, s.height = width, height
	s.scrollWindow()
}

// selected returns the row under the cursor, or nil for an empty view.
func (s *settingsOverlay) selected() *SettingField {
	if s.cursor < 0 || s.cursor >= len(s.view.Fields) {
		return nil
	}
	return &s.view.Fields[s.cursor]
}

// refresh re-reads the settings after a save and keeps the cursor in range.
func (s *settingsOverlay) refresh(m *App) {
	view := m.currentSettings()
	if s.provider != nil {
		view = s.provider.Settings()
	}
	s.view = view
	s.cursor = min(max(0, s.cursor), max(0, len(s.view.Fields)-1))
	s.scrollWindow()
}

// bodyHeight is the number of setting rows visible under the column header and
// above the footer.
func (s *settingsOverlay) bodyHeight() int {
	return max(1, s.height-1-len(s.footer()))
}

// scrollWindow keeps the cursor inside the visible window.
func (s *settingsOverlay) scrollWindow() {
	height := s.bodyHeight()
	limit := max(0, len(s.view.Fields)-height)
	s.cursor = min(max(0, s.cursor), max(0, len(s.view.Fields)-1))
	s.scroll = min(max(0, s.scroll), limit)
	if s.cursor < s.scroll {
		s.scroll = s.cursor
	}
	if s.cursor >= s.scroll+height {
		s.scroll = s.cursor - height + 1
	}
}

func (s *settingsOverlay) update(m *App, msg tea.KeyPressMsg) tea.Cmd {
	key := msg.Keystroke()
	if s.editing {
		switch key {
		case "enter":
			s.commit(m)
		case "backspace":
			s.backspace()
		case "ctrl+u":
			s.editBuf = ""
			s.errText = ""
		default:
			if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModMeta) {
				s.insert(Sanitize(msg.Text))
			}
		}
		s.scrollWindow()
		return nil
	}
	switch key {
	case "up", "k":
		s.cursor = max(0, s.cursor-1)
	case "down", "j":
		s.cursor = min(max(0, len(s.view.Fields)-1), s.cursor+1)
	case "pgup":
		s.cursor = max(0, s.cursor-8)
	case "pgdown":
		s.cursor = min(max(0, len(s.view.Fields)-1), s.cursor+8)
	case "home":
		s.cursor = 0
	case "end":
		s.cursor = max(0, len(s.view.Fields)-1)
	case "enter", " ":
		return s.activate(m)
	case "space":
		return s.activate(m)
	case "right", "l":
		s.cycle(m, 1)
	case "left", "h":
		s.cycle(m, -1)
	case "r":
		s.reset(m)
	}
	s.scrollWindow()
	return nil
}

func (s *settingsOverlay) wheel(up bool) {
	if s.editing {
		return
	}
	if up {
		s.cursor = max(0, s.cursor-1)
	} else {
		s.cursor = min(max(0, len(s.view.Fields)-1), s.cursor+1)
	}
	s.scrollWindow()
}

// insert appends printable text to the active editor. It reports whether the
// overlay consumed the text.
func (s *settingsOverlay) insert(text string) bool {
	if !s.editing {
		return false
	}
	if len(s.editBuf)+len(text) > maxSettingInputBytes {
		s.errText = fmt.Sprintf("input is capped at %d bytes", maxSettingInputBytes)
		return true
	}
	s.editBuf += text
	s.errText = ""
	return true
}

// command consumes the single-character reset key when a headless client sends
// it as text instead of a key press.
func (s *settingsOverlay) command(m *App, text string) bool {
	if s.editing || text != "r" {
		return false
	}
	s.reset(m)
	return true
}

func (s *settingsOverlay) backspace() {
	if s.editBuf == "" {
		return
	}
	_, size := utf8.DecodeLastRuneInString(s.editBuf)
	s.editBuf = s.editBuf[:len(s.editBuf)-size]
	s.errText = ""
}

// cancelEdit drops the inline editor without saving. Esc is consumed by the
// overlay stack before it closes the browser.
func (s *settingsOverlay) cancelEdit() {
	s.editing = false
	s.editKey = ""
	s.editBuf = ""
	s.errText = ""
}

// activate dispatches Enter on the selected row: info rows open their action,
// toggles and enums cycle, and everything else opens the inline editor.
func (s *settingsOverlay) activate(m *App) tea.Cmd {
	field := s.selected()
	if field == nil {
		return nil
	}
	if field.Kind == SettingInfo {
		if field.Action == "agents" {
			return m.openAgentsOverlay()
		}
		return nil
	}
	if field.Kind == SettingToggle || field.Kind == SettingEnum {
		s.cycle(m, 1)
		return nil
	}
	s.startEdit()
	return nil
}

// startEdit opens the inline editor. A secret starts empty so the existing
// value is never copied into the buffer; every other kind is prefilled with its
// current raw value.
func (s *settingsOverlay) startEdit() {
	field := s.selected()
	if field == nil {
		return
	}
	if s.editor == nil {
		s.notice = "TUI settings are read-only in this host"
		return
	}
	if field.Kind != SettingSecret && len(field.Value) > maxSettingInputBytes {
		s.notice = fmt.Sprintf("saved value is %d bytes; the editor is capped at %d bytes",
			len(field.Value), maxSettingInputBytes)
		return
	}
	s.editing = true
	s.editKey = field.Key
	s.editBuf = ""
	s.errText = ""
	s.notice = ""
	if field.Kind != SettingSecret {
		s.editBuf = field.Value
	}
	s.scrollWindow()
}

// commit saves the buffer. An empty value removes the key for string-like
// kinds; a validation or write error keeps the editor open.
func (s *settingsOverlay) commit(m *App) {
	field := s.selected()
	if field == nil || s.editor == nil {
		s.cancelEdit()
		return
	}
	value := strings.TrimSpace(s.editBuf)
	if err := s.editor.SetSetting(field.Key, value); err != nil {
		s.errText = SanitizeLine(err.Error())
		return
	}
	label := field.Label
	s.editing = false
	s.editKey = ""
	s.editBuf = ""
	s.errText = ""
	s.refresh(m)
	m.applySettingFields(s.view.Fields)
	s.notice = "saved " + label + " to the config file"
}

// cycle saves the next or previous option for a toggle or enum row.
func (s *settingsOverlay) cycle(m *App, delta int) {
	field := s.selected()
	if field == nil || len(field.Options) == 0 {
		return
	}
	if s.editor == nil {
		s.notice = "TUI settings are read-only in this host"
		return
	}
	index := 0
	for i, option := range field.Options {
		if option.Value == field.Value {
			index = i
			break
		}
	}
	next := ((index+delta)%len(field.Options) + len(field.Options)) % len(field.Options)
	label := field.Label
	if err := s.editor.SetSetting(field.Key, field.Options[next].Value); err != nil {
		s.errText = SanitizeLine(err.Error())
		return
	}
	s.errText = ""
	s.refresh(m)
	m.applySettingFields(s.view.Fields)
	s.notice = "saved " + label + " to the config file"
}

// reset removes the saved key so the built-in default applies again. A row that
// is already using a flag, environment, or current value is not rewritten.
func (s *settingsOverlay) reset(m *App) {
	field := s.selected()
	if field == nil || field.Kind == SettingInfo {
		return
	}
	if s.editor == nil {
		s.notice = "TUI settings are read-only in this host"
		return
	}
	if !field.Saved {
		s.notice = "no saved " + field.Label + "; using " + field.SourceDetail
		return
	}
	label := field.Label
	if err := s.editor.ResetSetting(field.Key); err != nil {
		s.errText = SanitizeLine(err.Error())
		return
	}
	s.errText = ""
	s.refresh(m)
	m.applySettingFields(s.view.Fields)
	s.notice = "reset " + label + " to the default"
}

// footer renders the selected row's detail, the editor or the last result, and
// the key hint.
func (s *settingsOverlay) footer() []string {
	var lines []string
	if field := s.selected(); field != nil {
		lines = append(lines, field.Label+" · "+field.Detail)
	}
	if s.view.Warning != "" {
		lines = append(lines, "warning: "+s.view.Warning)
	}
	switch {
	case s.editing:
		field := s.selected()
		value := s.editBuf
		if field != nil && field.Kind == SettingSecret {
			value = "••••••"
			if s.editBuf == "" {
				value = "(empty)"
			}
		}
		prompt := s.editKey
		if field != nil {
			prompt = field.Label
		}
		lines = append(lines, fmt.Sprintf("%s: %s▏", prompt, value))
		if s.errText != "" {
			lines = append(lines, "error: "+s.errText)
		}
		lines = append(lines, "Enter saves · Esc cancels · empty clears the key")
	case s.view.Err != "":
		lines = append(lines, "config file: "+s.view.Err)
		lines = append(lines, s.helpLine())
	case s.errText != "":
		lines = append(lines, "error: "+s.errText)
		lines = append(lines, s.helpLine())
	default:
		if s.notice != "" {
			lines = append(lines, s.notice)
		}
		lines = append(lines, s.helpLine())
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(SanitizeLine(line), s.width, "…")
	}
	return lines
}

// helpLine names the config file and the non-editing keys.
func (s *settingsOverlay) helpLine() string {
	if s.view.ConfigPath == "" {
		return `config disabled (-config "") · edits cannot be saved · Esc closes`
	}
	if s.editor == nil {
		return "config " + s.view.ConfigPath + " · read-only in this host · Esc closes"
	}
	return "config " + s.view.ConfigPath + " · Enter edit · ←/→ cycle · r reset · Esc closes"
}

// syncRuntime labels a display value changed with Ctrl-R or F3 since the last
// read as runtime, so the browser never shows a stale preference. A saved file
// value that matches the runtime state needs no label change.
func (s *settingsOverlay) syncRuntime(m *App) {
	for i := range s.view.Fields {
		field := &s.view.Fields[i]
		switch field.Key {
		case SettingRich:
			if value, err := strconv.ParseBool(field.Value); err == nil && value != m.rich {
				field.Value = strconv.FormatBool(m.rich)
				field.Display = richDisplay(m.rich)
				field.Source = "runtime"
				field.SourceDetail = "runtime"
			}
		case SettingExpandAll:
			if value, err := strconv.ParseBool(field.Value); err == nil && value != m.expandAll {
				field.Value = strconv.FormatBool(m.expandAll)
				field.Display = foldDisplay(m.expandAll)
				field.Source = "runtime"
				field.SourceDetail = "runtime"
			}
		}
	}
}

func (s *settingsOverlay) viewText(m *App) string {
	s.syncRuntime(m)
	header := "  " + padCell("SETTING", settingsLabelWidth) + "  " +
		padCell("VALUE", settingsValueWidth) + "  " + padCell("SOURCE", settingsSourceWide)
	lines := []string{header}
	body := s.bodyHeight()
	for i := s.scroll; i < len(s.view.Fields) && len(lines) <= body; i++ {
		field := s.view.Fields[i]
		marker := "  "
		if i == s.cursor {
			marker = "> "
		}
		lines = append(lines, ansi.Truncate(
			marker+padCell(field.Label, settingsLabelWidth)+"  "+
				padCell(field.Display, settingsValueWidth)+"  "+padCell(field.Source, settingsSourceWide),
			s.width, "…"))
	}
	lines = append(lines, s.footer()...)
	return strings.Join(lines, "\n")
}
