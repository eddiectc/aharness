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
}

// slashCommands is the command registry and the source of the help listing.
var slashCommands = []slashCommand{
	{[]string{"help"}, "/help", "show keys and commands"},
	{[]string{"model"}, "/model [id]", "pick a model for the next run"},
	{[]string{"provider"}, "/provider", "active provider and configuration"},
	{[]string{"effort"}, "/effort [value]", "pick a thinking effort for the next run"},
	{[]string{"details"}, "/details", "session, terminal, and config details"},
	{[]string{"files", "file"}, "/files", "workspace file picker (idle)"},
	{[]string{"table"}, "/table", "latest markdown table"},
	{[]string{"image"}, "/image", "image viewer"},
	{[]string{"rich"}, "/rich", "rich markdown rendering"},
	{[]string{"raw"}, "/raw", "raw markdown rendering"},
	{[]string{"repaint"}, "/repaint", "full terminal repaint"},
	{[]string{"refresh"}, "/refresh", "refresh the model catalog (idle)"},
	{[]string{"interrupt"}, "/interrupt", "interrupt the in-flight operation"},
	{[]string{"cancel"}, "/cancel", "cancel the active run"},
	{[]string{"quit", "exit"}, "/quit", "quit; cancels an active run"},
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
	case "files":
		if m.run.active {
			m.notice = "the file picker is disabled while a run is active"
			return nil, true
		}
		return m.openFileOverlay(), true
	case "table":
		return m.openTableOverlay(), true
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

func (m *App) commandCancel() {
	if !m.run.active || m.run.handle == nil {
		m.notice = "no run is active"
		return
	}
	m.run.ctrlArmed = false
	m.notice = "canceling the run"
	m.run.handle.Terminate()
}
