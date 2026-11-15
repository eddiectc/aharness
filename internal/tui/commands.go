package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// slashCommand describes one composer command. The first name is canonical;
// the remaining names are aliases. No command takes more than one argument.
type slashCommand struct {
	names   []string
	usage   string
	summary string
	hasArg  bool
}

const maxSlashSuggestions = 8

// slashCommands is the command registry and the source of the help listing.
var slashCommands = []slashCommand{
	{names: []string{"help"}, usage: "/help", summary: "show keys and commands"},
	{names: []string{"model"}, usage: "/model [id]", summary: "pick a model for the next run", hasArg: true},
	{names: []string{"provider"}, usage: "/provider", summary: "active provider and configuration"},
	{names: []string{"effort"}, usage: "/effort [value]", summary: "pick a thinking effort for the next run", hasArg: true},
	{names: []string{"details"}, usage: "/details", summary: "session, terminal, and config details"},
	{names: []string{"new"}, usage: "/new", summary: "start a new session (idle)"},
	{names: []string{"resume"}, usage: "/resume", summary: "resume a previous session (idle)"},
	{names: []string{"files", "file"}, usage: "/files", summary: "workspace file picker (idle)"},
	{names: []string{"table"}, usage: "/table", summary: "latest markdown table"},
	{names: []string{"agent-hub"}, usage: "/agent-hub", summary: "live delegated-child roster"},
	{names: []string{"agents"}, usage: "/agents", summary: "agent definitions and settings"},
	{names: []string{"settings"}, usage: "/settings", summary: "all config-file settings; saved to the config file"},
	{names: []string{"image"}, usage: "/image", summary: "image viewer"},
	{names: []string{"rich"}, usage: "/rich", summary: "rich markdown rendering"},
	{names: []string{"raw"}, usage: "/raw", summary: "raw markdown rendering"},
	{names: []string{"repaint"}, usage: "/repaint", summary: "full terminal repaint"},
	{names: []string{"refresh"}, usage: "/refresh", summary: "refresh the model catalog (idle)"},
	{names: []string{"interrupt"}, usage: "/interrupt", summary: "interrupt the in-flight operation"},
	{names: []string{"cancel"}, usage: "/cancel", summary: "cancel the active run"},
	{names: []string{"quit", "exit"}, usage: "/quit", summary: "quit; cancels an active run"},
}

// lookupSlashCommand finds a canonical name or alias.
func lookupSlashCommand(name string) (slashCommand, bool) {
	for _, command := range slashCommands {
		for _, candidate := range command.names {
			if candidate == name {
				return command, true
			}
		}
	}
	return slashCommand{}, false
}

// parseSlashCommand splits text into a registered command and its trimmed
// argument. Command names are case-insensitive; an unrecognized name is
// reported as not a command so the text can be submitted as a message.
func parseSlashCommand(text string) (slashCommand, string, bool) {
	body, ok := strings.CutPrefix(text, "/")
	if !ok {
		return slashCommand{}, "", false
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return slashCommand{}, "", false
	}
	name, args := body, ""
	if i := strings.IndexFunc(body, unicode.IsSpace); i >= 0 {
		name, args = body[:i], strings.TrimSpace(body[i:])
	}
	command, ok := lookupSlashCommand(strings.ToLower(name))
	return command, args, ok
}

// slashCommandHelpLines formats the registry for the help overlay.
func slashCommandHelpLines() []string {
	lines := make([]string, len(slashCommands))
	for i, command := range slashCommands {
		lines[i] = fmt.Sprintf("%-16s %s", command.usage, command.summary)
	}
	return lines
}

// slashPrefix returns the command-name prefix being typed. Only a single-line
// value that begins with "/" and has no whitespace after it has a prefix.
func slashPrefix(value string) (string, bool) {
	body, ok := strings.CutPrefix(value, "/")
	if !ok || strings.IndexFunc(body, unicode.IsSpace) >= 0 {
		return "", false
	}
	return body, true
}

// matchSlashCommands returns registry entries whose canonical name or alias
// starts with prefix, in registry order.
func matchSlashCommands(prefix string) []slashCommand {
	prefix = strings.ToLower(prefix)
	matches := make([]slashCommand, 0, len(slashCommands))
	for _, command := range slashCommands {
		for _, name := range command.names {
			if strings.HasPrefix(name, prefix) {
				matches = append(matches, command)
				break
			}
		}
	}
	return matches
}

// updateSuggestions recomputes the command menu from the composer value. It
// keeps the highlighted row while the prefix is unchanged and resets it when
// the prefix changes. It reports whether a menu is shown.
func (m *App) updateSuggestions() bool {
	prefix, ok := slashPrefix(m.composer.Value())
	if !ok {
		m.suggestions = nil
		m.suggestionIndex = 0
		m.suggestionPrefix = ""
		m.suggestionDismissed = false
		return false
	}
	prefixChanged := prefix != m.suggestionPrefix
	if prefixChanged {
		m.suggestionPrefix = prefix
		m.suggestionIndex = 0
		m.suggestionDismissed = false
	}
	if m.suggestionDismissed {
		m.suggestions = nil
		return false
	}
	m.suggestions = matchSlashCommands(prefix)
	if len(m.suggestions) == 0 {
		m.suggestionIndex = 0
		return false
	}
	if prefixChanged {
		if command, ok := lookupSlashCommand(strings.ToLower(prefix)); ok {
			// An exact name or alias starts on its own row instead of the
			// first prefix match (for example, "/quit", not "/help").
			for i, match := range m.suggestions {
				if match.names[0] == command.names[0] {
					m.suggestionIndex = i
					break
				}
			}
		}
	}
	if m.suggestionIndex >= len(m.suggestions) {
		m.suggestionIndex = len(m.suggestions) - 1
	}
	return true
}

func (m *App) suggestionsVisible() bool { return len(m.suggestions) > 0 }

// dismissSuggestions hides the menu until the composer text changes again.
func (m *App) dismissSuggestions() {
	m.suggestionDismissed = true
	m.suggestions = nil
	if prefix, ok := slashPrefix(m.composer.Value()); ok {
		m.suggestionPrefix = prefix
	}
}

func (m *App) moveSuggestion(delta int) {
	if len(m.suggestions) == 0 {
		return
	}
	m.suggestionIndex = min(max(m.suggestionIndex+delta, 0), len(m.suggestions)-1)
}

// completeSuggestion replaces the prefix with the highlighted canonical
// command. Commands with an optional argument leave a trailing space.
func (m *App) completeSuggestion() tea.Cmd {
	command := m.suggestions[m.suggestionIndex]
	text := "/" + command.names[0]
	if command.hasArg {
		text += " "
	}
	m.composer.SetValue(text)
	m.composer.CursorEnd()
	m.dismissSuggestions()
	m.layout()
	return nil
}

// runSuggestion executes the highlighted command exactly as if it had been
// typed and submitted.
func (m *App) runSuggestion() tea.Cmd {
	command := m.suggestions[m.suggestionIndex]
	text := "/" + command.names[0]
	m.clearComposer()
	m.layout()
	return m.submit(text, "tui")
}

// handleSlashCommand executes a composer command. It reports false for
// ordinary text, including unknown slash words, so the caller can submit it.
func (m *App) handleSlashCommand(text string) (tea.Cmd, bool) {
	command, args, ok := parseSlashCommand(text)
	if !ok {
		return nil, false
	}
	switch command.names[0] {
	case "help":
		return m.openHelpOverlay(), true
	case "model":
		return m.commandModel(args), true
	case "provider":
		return m.commandProvider(), true
	case "effort":
		return m.commandEffort(args), true
	case "details":
		return m.openDetailsOverlay(), true
	case "new":
		return m.commandNewSession(), true
	case "resume":
		return m.openResumeOverlay(), true
	case "files":
		if m.run.active {
			m.notice = "the file picker is disabled while a run is active"
			return nil, true
		}
		return m.openFileOverlay(), true
	case "table":
		return m.openTableOverlay(), true
	case "agent-hub":
		return m.openAgentHubOverlay(), true
	case "agents":
		return m.openAgentsOverlay(), true
	case "settings":
		return m.openSettingsOverlay(), true
	case "image":
		return m.toggleImageViewer(), true
	case "rich":
		m.setRich(true)
		return nil, true
	case "raw":
		m.setRich(false)
		return nil, true
	case "repaint":
		return func() tea.Msg { return tea.ClearScreen() }, true
	case "refresh":
		return m.commandRefresh(), true
	case "interrupt":
		m.commandInterrupt()
		return nil, true
	case "cancel":
		m.commandCancel()
		return nil, true
	case "quit":
		if m.run.active {
			return m.requestQuit(130, true), true
		}
		return m.requestQuit(0, false), true
	}
	return nil, false
}

// commandModel opens the picker, or selects an exact model ID for the next
// run. Like Ctrl-P, it never changes the active run.
func (m *App) commandModel(id string) tea.Cmd {
	if m.run.active {
		m.notice = "the model picker is disabled while a run is active"
		return nil
	}
	if id == "" {
		return m.openModelOverlay()
	}
	for _, choice := range m.models {
		if choice.ID == id {
			return m.chooseModel(choice)
		}
	}
	m.notice = fmt.Sprintf("unknown model %q", SanitizeLine(id))
	return nil
}

// commandProvider reports the single active provider and opens the details
// overlay, which carries the base URL and the rest of the configuration.
func (m *App) commandProvider() tea.Cmd {
	provider, ok := m.host.(DetailsProvider)
	if !ok {
		m.notice = "provider details are unavailable"
		return nil
	}
	m.notice = "provider: " + SanitizeLine(provider.Details().Provider)
	return m.openDetailsOverlay()
}

// commandEffort opens the picker, or applies one advertised value (or the
// reserved "default") to the next run. Like F2, it never changes an active run.
func (m *App) commandEffort(value string) tea.Cmd {
	if m.run.active {
		m.notice = "the thinking effort picker is disabled while a run is active"
		return nil
	}
	if value == "" {
		return m.openEffortOverlay()
	}
	if strings.EqualFold(value, "default") {
		return m.chooseEffort("")
	}
	choice, ok := m.currentModelChoice()
	if !ok {
		m.notice = "the current model has no catalog metadata"
		return nil
	}
	if !slices.Contains(choice.ReasoningEfforts, value) {
		if len(choice.ReasoningEfforts) == 0 {
			m.notice = "model " + SanitizeLine(choice.ID) + " advertises no effort levels"
		} else {
			m.notice = fmt.Sprintf("model %s does not advertise effort %q (values: %s)",
				SanitizeLine(choice.ID), SanitizeLine(value), strings.Join(choice.ReasoningEfforts, ", "))
		}
		return nil
	}
	return m.chooseEffort(value)
}

// commandRefresh performs the picker's explicit refresh after opening the
// picker, so the result is visible and the next selection has fresh metadata.
func (m *App) commandRefresh() tea.Cmd {
	if m.run.active {
		m.notice = "the model picker is disabled while a run is active"
		return nil
	}
	if len(m.models) == 0 {
		m.notice = "no models available"
		return nil
	}
	m.openModelOverlay()
	return m.refreshModelCatalog()
}

func (m *App) commandInterrupt() {
	if !m.run.active || m.run.handle == nil {
		m.notice = "no operation to interrupt"
		return
	}
	m.interruptOperation()
}

// commandNewSession replaces the active session with a fresh one. It is
// idle-only: a run owns the store until it finishes.
func (m *App) commandNewSession() tea.Cmd {
	if m.run.active {
		m.notice = "session switching is disabled while a run is active"
		return nil
	}
	manager, ok := m.host.(SessionManager)
	if !ok {
		m.notice = "session switching is unavailable"
		return nil
	}
	snapshot, err := manager.NewSession(RunOptions{
		Model:             m.nextModel,
		ThinkingEffort:    m.nextEffort,
		ThinkingEffortSet: true,
	})
	if err != nil {
		m.notice = "new session: " + CapText(SanitizeLine(err.Error()), 512)
		return nil
	}
	m.applySessionSnapshot(snapshot)
	return m.sessionSwitched("new session ", snapshot.Info.SessionID)
}

// resumeSession opens one previous session selected from the /resume picker.
func (m *App) resumeSession(id string) tea.Cmd {
	manager, ok := m.host.(SessionManager)
	if !ok {
		m.notice = "session switching is unavailable"
		return nil
	}
	snapshot, err := manager.ResumeSession(id)
	if err != nil {
		m.notice = "resume session: " + CapText(SanitizeLine(err.Error()), 512)
		return nil
	}
	m.applySessionSnapshot(snapshot)
	return m.sessionSwitched("resumed session ", snapshot.Info.SessionID)
}

// sessionSwitched reports a successful switch in the status line and protocol.
func (m *App) sessionSwitched(action, id string) tea.Cmd {
	text := action + shortSessionID(id)
	m.notice = text
	m.emit(EventNotice{Kind: "session", Text: text})
	return nil
}

// shortSessionID is the stable display prefix for a session UUID.
func shortSessionID(id string) string {
	id = SanitizeLine(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func (m *App) commandCancel() {
	if !m.run.active || m.run.handle == nil {
		m.notice = "no run is active"
		return
	}
	m.run.ctrlArmed = false
	m.notice = "canceling the run"
	m.run.handle.Terminate()
}
