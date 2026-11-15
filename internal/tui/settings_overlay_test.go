package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// uiSettingsHost is the manual host plus the settings and agent capabilities.
type uiSettingsHost struct {
	*manualHost
	fields  []SettingField
	saves   []uiSettingSave
	resets  []string
	secret  string
	saveErr error
}

type uiSettingSave struct {
	key   string
	value string
}

func newUISettingsHost() *uiSettingsHost {
	return &uiSettingsHost{
		manualHost: newManualHost(),
		fields: []SettingField{
			{
				Key: "model", Label: "model", Kind: SettingText,
				Value: "deepseek-v4.1-flash", Display: "deepseek-v4.1-flash",
				Source: "file", SourceDetail: "config file", Saved: true,
				Detail: "next process · saved in the config file",
			},
			{
				Key: "max_turns", Label: "max turns", Kind: SettingInt,
				Value: "20", Display: "20", Source: "current", SourceDetail: "default",
				Detail: "next process · using the current value; saving sets a file default",
			},
			{
				Key: SettingRich, Label: "assistant rendering", Kind: SettingToggle,
				Value: "true", Display: "rich", Source: "file", SourceDetail: "config file", Saved: true,
				Live: true, Options: richOptions(), Detail: "applies now · saved in the config file",
			},
			{
				Key: SettingExpandAll, Label: "block folding", Kind: SettingToggle,
				Value: "false", Display: "folded", Source: "current", SourceDetail: "default",
				Live: true, Options: foldOptions(), Detail: "applies now · using the current value",
			},
			{
				Key: "web_fetch_key", Label: "web fetch key", Kind: SettingSecret,
				Display: "(unset)", Source: "current", SourceDetail: "default",
				Detail: "next process · never displayed",
			},
			{
				Key: "agent_overrides", Label: "agent overrides", Kind: SettingInfo,
				Value: "0", Display: "none", Source: "current", SourceDetail: "default",
				Action: "agents", Detail: "next process · edited in /agents",
			},
		},
	}
}

func (h *uiSettingsHost) Settings() SettingsView {
	return SettingsView{
		Fields:     append([]SettingField(nil), h.fields...),
		ConfigPath: "/state/config.json",
	}
}

// Agents makes the agent_overrides action open the /agents overlay.
func (h *uiSettingsHost) Agents() []AgentDefView { return nil }

func (h *uiSettingsHost) SetSetting(key, value string) error {
	if h.saveErr != nil {
		return h.saveErr
	}
	for i := range h.fields {
		field := &h.fields[i]
		if field.Key != key {
			continue
		}
		h.saves = append(h.saves, uiSettingSave{key: key, value: value})
		field.Saved = true
		field.Source = "file"
		field.SourceDetail = "config file"
		if field.Kind == SettingSecret {
			h.secret = value
			field.Value = ""
			field.Display = "set"
			return nil
		}
		field.Value = value
		field.Display = value
		for _, option := range field.Options {
			if option.Value == value {
				field.Display = option.Label
			}
		}
		return nil
	}
	return fmt.Errorf("unknown setting %q", key)
}

func (h *uiSettingsHost) ResetSetting(key string) error {
	if h.saveErr != nil {
		return h.saveErr
	}
	h.resets = append(h.resets, key)
	for i := range h.fields {
		field := &h.fields[i]
		if field.Key != key {
			continue
		}
		field.Saved = false
		field.Source = "current"
		field.SourceDetail = "default"
		switch key {
		case "model":
			field.Value, field.Display = "", "(none)"
		case SettingRich:
			field.Value, field.Display = "true", "rich"
		case SettingExpandAll:
			field.Value, field.Display = "false", "folded"
		case "web_fetch_key":
			h.secret = ""
			field.Value, field.Display = "", "(unset)"
		default:
			field.Value, field.Display = "", "(none)"
		}
	}
	return nil
}

func TestSettingsOverlayListsAllFields(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	if cmd := submitComposer(t, app, "/settings"); cmd != nil {
		t.Fatalf("cmd = %v, want nil", cmd)
	}
	if app.overlay.kind != OverlaySettings {
		t.Fatalf("overlay = %q, want settings", app.overlay.kind)
	}
	body := ansi.Strip(app.overlay.settings.viewText(app))
	for _, want := range []string{
		"SETTING", "VALUE", "SOURCE", "model", "deepseek-v4.1-flash", "file",
		"max turns", "20", "current", "assistant rendering", "rich",
		"block folding", "folded", "web fetch key", "(unset)", "agent overrides", "none",
		"config /state/config.json", "Enter edit",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings overlay missing %q:\n%s", want, body)
		}
	}

	// Navigation alone never writes.
	press(t, app, "down")
	press(t, app, "up")
	press(t, app, "end")
	press(t, app, "home")
	if len(host.saves) != 0 || len(host.resets) != 0 {
		t.Fatalf("navigation wrote settings: saves=%v resets=%v", host.saves, host.resets)
	}
}

func TestSettingsOverlayEditsTextValue(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")

	// model is prefilled with the saved value; Ctrl-U clears it for a new one.
	press(t, app, "enter")
	settings := app.overlay.settings
	if !settings.editing || settings.editKey != "model" {
		t.Fatalf("editor = %+v, want the model editor", settings)
	}
	if settings.editBuf != "deepseek-v4.1-flash" {
		t.Fatalf("model prefill = %q", settings.editBuf)
	}
	press(t, app, "ctrl+u")
	typeText(t, app, "next-model")
	press(t, app, "enter")
	if len(host.saves) != 1 || host.saves[0].key != "model" || host.saves[0].value != "next-model" {
		t.Fatalf("saves = %v, want model=next-model", host.saves)
	}
	if settings.editing {
		t.Fatal("successful save left the editor open")
	}
	body := ansi.Strip(settings.viewText(app))
	if !strings.Contains(body, "next-model") || !strings.Contains(body, "saved model") {
		t.Fatalf("saved model not reflected:\n%s", body)
	}

	// Backspace edits the buffer; Esc cancels without a second save.
	press(t, app, "enter")
	press(t, app, "backspace")
	press(t, app, "esc")
	if app.overlay.kind != OverlaySettings {
		t.Fatalf("Esc closed the overlay instead of cancelling: %q", app.overlay.kind)
	}
	if settings.editing || len(host.saves) != 1 {
		t.Fatalf("cancel state: editing=%t saves=%v", settings.editing, host.saves)
	}
	press(t, app, "esc")
	if app.overlay.kind != OverlayNone {
		t.Fatalf("second Esc overlay = %q, want none", app.overlay.kind)
	}
}

func TestSettingsOverlayToggleAppliesLive(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")
	press(t, app, "down") // max turns
	press(t, app, "down") // assistant rendering
	press(t, app, "enter")
	if len(host.saves) != 1 || host.saves[0].key != SettingRich || host.saves[0].value != "false" {
		t.Fatalf("saves = %v, want ui_rich=false", host.saves)
	}
	if app.rich {
		t.Fatal("rich rendering stayed on after saving raw")
	}
	if body := ansi.Strip(app.overlay.settings.viewText(app)); !strings.Contains(body, "raw") {
		t.Fatalf("saved toggle not reflected:\n%s", body)
	}
	// Left cycles back to rich.
	press(t, app, "left")
	if len(host.saves) != 2 || host.saves[1].value != "true" {
		t.Fatalf("saves = %v, want ui_rich=true", host.saves)
	}
	if !app.rich {
		t.Fatal("rich rendering did not return")
	}
	// Space activates the next row (block folding).
	press(t, app, "down")
	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if len(host.saves) != 3 || host.saves[2].key != SettingExpandAll || host.saves[2].value != "true" {
		t.Fatalf("saves = %v, want ui_expand_all=true", host.saves)
	}
	if !app.expandAll {
		t.Fatal("expanded blocks did not apply live")
	}
}

func TestSettingsOverlayReset(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")

	// model is saved, so r removes it and reports the default.
	pressRune(t, app, 'r')
	if len(host.resets) != 1 || host.resets[0] != "model" {
		t.Fatalf("resets = %v, want model", host.resets)
	}
	body := ansi.Strip(app.overlay.settings.viewText(app))
	if !strings.Contains(body, "reset model to the default") {
		t.Fatalf("reset notice missing:\n%s", body)
	}

	// A row using the current value is not rewritten.
	press(t, app, "down")
	pressRune(t, app, 'r')
	if len(host.resets) != 1 {
		t.Fatalf("current-value reset wrote: %v", host.resets)
	}
	if !strings.Contains(app.overlay.settings.notice, "no saved max turns") {
		t.Fatalf("notice = %q, want the no-saved-value message", app.overlay.settings.notice)
	}

	// A saved display row toggled at runtime is still resettable.
	press(t, app, "ctrl+r")
	press(t, app, "down") // assistant rendering
	app.overlay.settings.viewText(app)
	pressRune(t, app, 'r')
	if len(host.resets) != 2 || host.resets[1] != SettingRich {
		t.Fatalf("resets = %v, want a ui_rich reset after a runtime toggle", host.resets)
	}
}

func TestSettingsOverlayMasksSecret(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")
	for range 4 {
		press(t, app, "down")
	}
	if field := app.overlay.settings.selected(); field == nil || field.Key != "web_fetch_key" {
		t.Fatalf("selected = %+v, want web_fetch_key", field)
	}
	press(t, app, "enter")
	settings := app.overlay.settings
	if !settings.editing || settings.editBuf != "" {
		t.Fatalf("secret editor = editing %t buffer %q, want empty", settings.editing, settings.editBuf)
	}
	typeText(t, app, "super-secret-token")
	body := ansi.Strip(settings.viewText(app))
	if strings.Contains(body, "super-secret-token") {
		t.Fatalf("secret leaked into the view:\n%s", body)
	}
	if !strings.Contains(body, "••••••") {
		t.Fatalf("secret edit is not masked:\n%s", body)
	}
	press(t, app, "enter")
	if host.secret != "super-secret-token" {
		t.Fatalf("saved secret = %q", host.secret)
	}
	body = ansi.Strip(settings.viewText(app))
	if strings.Contains(body, "super-secret-token") || !strings.Contains(body, "set") {
		t.Fatalf("saved secret display is wrong:\n%s", body)
	}
}

func TestSettingsOverlayValidationKeepsEditor(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")
	host.saveErr = errors.New("max_turns must not be negative")
	press(t, app, "down")
	press(t, app, "enter")
	press(t, app, "ctrl+u")
	typeText(t, app, "-1")
	press(t, app, "enter")
	settings := app.overlay.settings
	if !settings.editing {
		t.Fatal("failed save closed the editor")
	}
	if !strings.Contains(settings.errText, "negative") {
		t.Fatalf("error = %q, want the host error", settings.errText)
	}
	if body := ansi.Strip(settings.viewText(app)); !strings.Contains(body, "error: max_turns must not be negative") {
		t.Fatalf("error not rendered:\n%s", body)
	}
}

func TestSettingsOverlayScrollsToCursor(t *testing.T) {
	host := newUISettingsHost()
	for i := range 30 {
		host.fields = append(host.fields, SettingField{
			Key: fmt.Sprintf("filler-%d", i), Label: fmt.Sprintf("filler %d", i),
			Kind: SettingText, Value: "x", Display: "x", Source: "current",
		})
	}
	app := newTestApp(t, host)
	app.setSize(80, 24)
	submitComposer(t, app, "/settings")
	press(t, app, "end")
	settings := app.overlay.settings
	if settings.cursor != len(host.fields)-1 {
		t.Fatalf("cursor = %d, want the last row", settings.cursor)
	}
	body := ansi.Strip(settings.viewText(app))
	if !strings.Contains(body, fmt.Sprintf("filler %d", 29)) {
		t.Fatalf("last row is not in the window:\n%s", body)
	}
	if settings.scroll == 0 {
		t.Fatal("scroll did not follow the cursor")
	}
}

func TestSettingsOverlayInfoOpensAgents(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")
	press(t, app, "end")
	press(t, app, "enter")
	if app.overlay.kind != OverlayAgents {
		t.Fatalf("overlay = %q, want agents from the info action", app.overlay.kind)
	}
}

func TestSettingsOverlayAcceptsTextReset(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")
	typeText(t, app, "r")
	if len(host.resets) != 1 || host.resets[0] != "model" {
		t.Fatalf("resets = %v, want model from a text-encoded r", host.resets)
	}
}

func TestSettingsOverlayTracksGlobalToggle(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")
	press(t, app, "ctrl+r")
	body := ansi.Strip(app.overlay.settings.viewText(app))
	if app.rich || !strings.Contains(body, "raw") || !strings.Contains(body, "runtime") {
		t.Fatalf("runtime raw toggle not reflected:\n%s", body)
	}
	press(t, app, "ctrl+r")
	body = ansi.Strip(app.overlay.settings.viewText(app))
	if !app.rich || !strings.Contains(body, "rich") {
		t.Fatalf("runtime rich toggle not reflected:\n%s", body)
	}
}

func TestSettingsOverlayReadOnly(t *testing.T) {
	app := newTestApp(t, newManualHost())
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")
	press(t, app, "enter")
	body := ansi.Strip(app.overlay.settings.viewText(app))
	if !strings.Contains(body, "read-only") {
		t.Fatalf("read-only footer missing:\n%s", body)
	}
	if !app.rich {
		t.Fatal("read-only host changed the rendering mode")
	}
}

func TestSettingsApplyAtStartup(t *testing.T) {
	host := newUISettingsHost()
	for i := range host.fields {
		switch host.fields[i].Key {
		case SettingRich:
			host.fields[i].Value = "false"
			host.fields[i].Display = "raw"
		case SettingExpandAll:
			host.fields[i].Value = "true"
			host.fields[i].Display = "expanded"
		}
	}
	app := newTestApp(t, host)
	if app.rich {
		t.Fatal("startup ignored ui_rich=false")
	}
	if !app.expandAll {
		t.Fatal("startup ignored ui_expand_all=true")
	}
}

func TestSettingsCommandDiscoverable(t *testing.T) {
	lines := strings.Join(slashCommandHelpLines(), "\n")
	if !strings.Contains(lines, "/settings") {
		t.Fatalf("help lines missing /settings:\n%s", lines)
	}
	matches := matchSlashCommands("set")
	if len(matches) != 1 || matches[0].names[0] != "settings" {
		t.Fatalf("autocomplete matches = %+v, want settings", matches)
	}
	command, args, ok := parseSlashCommand("/settings extra words")
	if !ok || command.names[0] != "settings" || args != "extra words" {
		t.Fatalf("parseSlashCommand = %+v, %q, %v", command, args, ok)
	}
}

func TestSettingsOverlayRefusesLongSavedValue(t *testing.T) {
	host := newUISettingsHost()
	host.fields[0].Value = strings.Repeat("a", maxSettingInputBytes+1)
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")
	press(t, app, "enter")
	settings := app.overlay.settings
	if settings.editing {
		t.Fatal("an over-cap saved value opened the editor")
	}
	if !strings.Contains(settings.notice, "capped") {
		t.Fatalf("notice = %q, want the cap message", settings.notice)
	}
}

func TestSettingsInputCap(t *testing.T) {
	host := newUISettingsHost()
	app := newTestApp(t, host)
	app.setSize(100, 30)
	submitComposer(t, app, "/settings")
	press(t, app, "enter")
	press(t, app, "ctrl+u")
	typeText(t, app, strings.Repeat("a", maxSettingInputBytes+1))
	settings := app.overlay.settings
	if len(settings.editBuf) != 0 {
		t.Fatalf("over-cap buffer = %d bytes, want 0", len(settings.editBuf))
	}
	if !strings.Contains(settings.errText, "capped") {
		t.Fatalf("error = %q, want the cap message", settings.errText)
	}
}
