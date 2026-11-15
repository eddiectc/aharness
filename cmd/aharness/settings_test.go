package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"aharness/internal/model"
	"aharness/internal/tools"
	"aharness/internal/tui"
)

// settingsHost builds a host pointed at a fresh config file with sensible
// effective defaults. The file itself is written by each test.
func settingsHost(t *testing.T, path, stateDir string) *tuiHost {
	t.Helper()
	return &tuiHost{cfg: config{
		ConfigPath:       path,
		StateDir:         stateDir,
		Provider:         providerName,
		Model:            "current-model",
		Workspace:        "/workspace",
		MaxTurns:         20,
		MaxTotalTokens:   2_000_000,
		MaxSubagents:     defaultMaxSubagents,
		MaxContextTokens: 200_000,
		HTTPTimeout:      model.DefaultHTTPTimeout,
		CatalogTimeout:   model.CatalogRequestTimeout,
		WebTimeout:       tools.WebTimeoutDefault,
		WebFetchProvider: tools.WebFetchProviderBuiltin,
		UIRich:           true,
		SettingSources:   map[string]string{},
	}}
}

func fieldByKey(t *testing.T, view tui.SettingsView, key string) tui.SettingField {
	t.Helper()
	for _, field := range view.Fields {
		if field.Key == key {
			return field
		}
	}
	t.Fatalf("setting %q missing from the view: %+v", key, view.Fields)
	return tui.SettingField{}
}

func TestConfigSettingCoverage(t *testing.T) {
	configKeys := make(map[string]bool)
	typ := reflect.TypeOf(fileConfig{})
	for i := range typ.NumField() {
		key, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		configKeys[key] = true
	}
	listed := make(map[string]bool)
	for _, def := range configSettingDefs() {
		if listed[def.key] {
			t.Errorf("duplicate /settings row for %q", def.key)
		}
		listed[def.key] = true
	}
	for key := range configKeys {
		if !listed[key] {
			t.Errorf("config file key %q has no /settings row", key)
		}
	}
	for key := range listed {
		if !configKeys[key] {
			t.Errorf("/settings row %q is not a config file key", key)
		}
	}
	if len(configKeys) != len(listed) {
		t.Fatalf("config keys = %d, settings rows = %d", len(configKeys), len(listed))
	}
}

func TestParseConfigSettingSources(t *testing.T) {
	base := cfgTestBaseArgs(t)
	cfg := cfgTestParseOK(t, base, nil)
	if got := cfg.SettingSources["model"]; got != "flag -model" {
		t.Errorf("model source = %q, want the flag", got)
	}
	if got := cfg.SettingSources["workspace"]; got != "flag -workspace" {
		t.Errorf("workspace source = %q, want the flag", got)
	}
	if got := cfg.SettingSources["ui_rich"]; got != "default" {
		t.Errorf("ui_rich source = %q, want default", got)
	}
	if got := cfg.SettingSources["max_turns"]; got != "default" {
		t.Errorf("max_turns source = %q, want default", got)
	}

	path := writeConfigFile(t, `{"max_turns": 4, "searxng_url": "http://127.0.0.1:8080", "ui_rich": false}`)
	cfg = cfgTestParseOK(t, append(append([]string{}, base...), "-config", path, "-max-turns", "9"),
		map[string]string{"AHARNESS_MAX_TOTAL_TOKENS": "7000"})
	cases := map[string]string{
		"max_turns":        "flag -max-turns",
		"max_total_tokens": "environment AHARNESS_MAX_TOTAL_TOKENS",
		"searxng_url":      "config file",
		"ui_rich":          "config file",
		"provider":         "default",
		"agent_overrides":  "default",
	}
	for key, want := range cases {
		if got := cfg.SettingSources[key]; got != want {
			t.Errorf("%s source = %q, want %q", key, got, want)
		}
	}
}

func TestTUIHostSettingsView(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{
		"max_turns": 4,
		"ui_rich": false,
		"web_fetch_key": "hidden-value",
		"web_fetch_provider": "builtin"
	}`), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	host := settingsHost(t, path, dir)
	host.cfg.WebFetchKey = "hidden-value"
	host.cfg.SettingSources = map[string]string{
		"model":         "flag -model",
		"max_turns":     "config file",
		"ui_rich":       "config file",
		"web_fetch_key": "config file",
	}
	view := host.Settings()
	if view.ConfigPath != path {
		t.Fatalf("config path = %q, want %q", view.ConfigPath, path)
	}
	if field := fieldByKey(t, view, "max_turns"); field.Value != "4" || field.Source != "file" {
		t.Fatalf("max_turns field = %+v, want the saved value", field)
	}
	if field := fieldByKey(t, view, "ui_rich"); field.Value != "false" || field.Display != "raw" || field.Source != "file" {
		t.Fatalf("ui_rich field = %+v", field)
	}
	if field := fieldByKey(t, view, "model"); field.Value != "current-model" || field.Source != "flag" {
		t.Fatalf("model field = %+v, want the effective flag value", field)
	}
	if field := fieldByKey(t, view, "workspace"); field.Value != "/workspace" || field.Source != "current" {
		t.Fatalf("workspace field = %+v, want the effective value", field)
	}
	secret := fieldByKey(t, view, "web_fetch_key")
	if secret.Value != "" || secret.Display != "set" || secret.Kind != tui.SettingSecret {
		t.Fatalf("web_fetch_key field = %+v, want a masked saved value", secret)
	}
	if rendered := fmt.Sprintf("%+v", view); strings.Contains(rendered, "hidden-value") {
		t.Fatalf("settings view exposed the saved secret: %s", rendered)
	}
	if view.Warning != "" {
		t.Fatalf("warning = %q, want none for a builtin pair", view.Warning)
	}
}

func TestTUIHostSetsEveryConfigSetting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	workspace := t.TempDir()
	host := settingsHost(t, path, dir)
	samples := map[string]string{
		"model":              "sample-model",
		"provider":           providerName,
		"workspace":          workspace,
		"provider_base_url":  "https://example.test/v1",
		"models_dev_url":     "https://models.example.test/api.json",
		"searxng_url":        "http://127.0.0.1:8080",
		"web_timeout":        "20s",
		"web_fetch_provider": "crawl4ai",
		"web_fetch_url":      "http://127.0.0.1:11235",
		"web_fetch_key":      "test-secret-token",
		"thinking_effort":    "high",
		"max_turns":          "7",
		"max_total_tokens":   "7000",
		"max_subagents":      "3",
		"max_context_tokens": "64000",
		"max_output_tokens":  "0",
		"session_timeout":    "5m",
		"model_timeout":      "90s",
		"http_timeout":       "30s",
		"catalog_timeout":    "9s",
		"require_usage":      "true",
		"log_requests":       "true",
		"ui_rich":            "false",
		"ui_expand_all":      "true",
	}
	for _, def := range configSettingDefs() {
		if def.kind == tui.SettingInfo {
			continue
		}
		value, ok := samples[def.key]
		if !ok {
			t.Fatalf("no sample value for %q", def.key)
		}
		if err := host.SetSetting(def.key, value); err != nil {
			t.Fatalf("SetSetting(%q, %q): %v", def.key, value, err)
		}
	}
	loaded, err := loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"model", strDeref(loaded.Model), "sample-model"},
		{"provider", strDeref(loaded.Provider), providerName},
		{"workspace", strDeref(loaded.Workspace), filepath.Clean(workspace)},
		{"provider_base_url", strDeref(loaded.ProviderBaseURL), "https://example.test/v1"},
		{"models_dev_url", strDeref(loaded.ModelsDevURL), "https://models.example.test/api.json"},
		{"searxng_url", strDeref(loaded.SearXNGURL), "http://127.0.0.1:8080"},
		{"web_timeout", durationDeref(loaded.WebTimeout), 20 * time.Second},
		{"web_fetch_provider", strDeref(loaded.WebFetchProvider), "crawl4ai"},
		{"web_fetch_url", strDeref(loaded.WebFetchURL), "http://127.0.0.1:11235"},
		{"web_fetch_key", strDeref(loaded.WebFetchKey), "test-secret-token"},
		{"thinking_effort", strDeref(loaded.ThinkingEffort), "high"},
		{"max_turns", intDeref(loaded.MaxTurns), 7},
		{"max_total_tokens", intDeref(loaded.MaxTotalTokens), 7000},
		{"max_subagents", intDeref(loaded.MaxSubagents), 3},
		{"max_context_tokens", intDeref(loaded.MaxContextTokens), 64000},
		{"max_output_tokens", intDeref(loaded.MaxOutputTokens), 0},
		{"session_timeout", durationDeref(loaded.SessionTimeout), 5 * time.Minute},
		{"model_timeout", durationDeref(loaded.ModelTimeout), 90 * time.Second},
		{"http_timeout", durationDeref(loaded.HTTPTimeout), 30 * time.Second},
		{"catalog_timeout", durationDeref(loaded.CatalogTimeout), 9 * time.Second},
		{"require_usage", boolDeref(loaded.RequireUsage), true},
		{"log_requests", boolDeref(loaded.LogRequests), true},
		{"ui_rich", boolDeref(loaded.UIRich), false},
		{"ui_expand_all", boolDeref(loaded.UIExpandAll), true},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
	if host.cfg.UIRich || !host.cfg.uiRichSet || !host.cfg.UIExpandAll || !host.cfg.uiExpandSet {
		t.Fatalf("live display settings = %+v", host.cfg)
	}
	view := host.Settings()
	if rendered := fmt.Sprintf("%+v", view); strings.Contains(rendered, "test-secret-token") {
		t.Fatalf("settings view exposed the new secret: %s", rendered)
	}
	if view.Warning != "" {
		t.Fatalf("warning = %q, want a complete web fetch pair", view.Warning)
	}
	for _, def := range configSettingDefs() {
		if def.kind == tui.SettingInfo {
			continue
		}
		field := fieldByKey(t, view, def.key)
		if field.Source != "file" {
			t.Errorf("%s source = %q, want file after a save", def.key, field.Source)
		}
		if def.kind == tui.SettingSecret {
			if field.Value != "" || field.Display != "set" {
				t.Errorf("%s view = %+v, want a masked saved value", def.key, field)
			}
			continue
		}
		if field.Value == "" {
			t.Errorf("%s view value is empty after a save", def.key)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, err = %v, want 0600", info.Mode().Perm(), err)
	}

	// Every key can also be removed, leaving an empty config object.
	for _, def := range configSettingDefs() {
		if def.kind == tui.SettingInfo {
			continue
		}
		if err := host.ResetSetting(def.key); err != nil {
			t.Fatalf("ResetSetting(%q): %v", def.key, err)
		}
	}
	loaded, err = loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload after resets: %v", err)
	}
	if !reflect.DeepEqual(loaded, fileConfig{}) {
		t.Fatalf("config after resetting every key = %+v, want an empty object", loaded)
	}
	if !host.cfg.UIRich || host.cfg.uiRichSet || host.cfg.UIExpandAll || host.cfg.uiExpandSet {
		t.Fatalf("live display settings after reset = %+v", host.cfg)
	}
}

func strDeref(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func intDeref(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func boolDeref(value *bool) bool {
	return value != nil && *value
}

func durationDeref(value *configDuration) time.Duration {
	if value == nil {
		return -1
	}
	return time.Duration(*value)
}

func TestTUIHostSettingValidation(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"model whitespace", "model", "two words", "whitespace"},
		{"model too long", "model", strings.Repeat("m", maxConfigModelBytes+1), "limit"},
		{"unknown provider", "provider", "other", "unsupported provider"},
		{"missing workspace", "workspace", filepath.Join(t.TempDir(), "missing"), "workspace"},
		{"bad base URL", "provider_base_url", "ftp://example.test/v1", "base URL"},
		{"plain-http catalog", "models_dev_url", "http://models.example.test/api.json", "loopback"},
		{"bad search URL", "searxng_url", "ftp://example.test", "searxng_url"},
		{"zero web timeout", "web_timeout", "0s", "web_timeout"},
		{"bad web URL", "web_fetch_url", "not-a-url", "web fetch URL"},
		{"bad web key", "web_fetch_key", "a\x00b", "web fetch key"},
		{"bad effort shape", "thinking_effort", "High", "thinking effort"},
		{"negative turns", "max_turns", "-1", "max_turns"},
		{"zero context", "max_context_tokens", "0", "max_context_tokens"},
		{"non-integer tokens", "max_total_tokens", "many", "integer"},
		{"zero HTTP timeout", "http_timeout", "0s", "http_timeout"},
		{"negative session timeout", "session_timeout", "-1s", "session_timeout"},
		{"bad bool", "require_usage", "maybe", "true or false"},
		{"agent overrides", "agent_overrides", "x", "/agents"},
		{"unknown key", "unknown_key", "x", "unknown config setting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(`{"max_turns": 4}`), 0o600); err != nil {
				t.Fatalf("seed config: %v", err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			host := settingsHost(t, path, dir)
			err = host.SetSetting(tc.key, tc.value)
			if err == nil {
				t.Fatalf("SetSetting(%q, %q) accepted", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read config after failure: %v", readErr)
			}
			if string(before) != string(after) {
				t.Fatalf("a rejected setting changed the file:\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func TestTUIHostSettingCatalogValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	host := settingsHost(t, path, dir)
	host.catalog = map[string]model.CatalogModel{
		"supported": {ID: "supported", Dialect: model.DialectChat, Reasoning: true, ReasoningEfforts: []string{"low", "high"}},
		"unusable":  {ID: "unusable", Unavailable: "missing base URL"},
	}
	if err := host.SetSetting("model", "absent"); err == nil || !strings.Contains(err.Error(), "not in the loaded catalog") {
		t.Fatalf("absent model = %v", err)
	}
	if err := host.SetSetting("model", "unusable"); err == nil || !strings.Contains(err.Error(), "not usable") {
		t.Fatalf("unusable model = %v", err)
	}
	if err := host.SetSetting("model", "supported"); err != nil {
		t.Fatalf("supported model: %v", err)
	}
	if err := host.SetSetting("thinking_effort", "max"); err == nil || !strings.Contains(err.Error(), "supported") {
		t.Fatalf("unsupported effort = %v", err)
	}
	if err := host.SetSetting("thinking_effort", "low"); err != nil {
		t.Fatalf("supported effort: %v", err)
	}
	loaded, err := loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if strDeref(loaded.Model) != "supported" || strDeref(loaded.ThinkingEffort) != "low" {
		t.Fatalf("saved model/effort = %q/%q", strDeref(loaded.Model), strDeref(loaded.ThinkingEffort))
	}
}

func TestTUIHostResetsConfigSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{
		"model": "keep-model",
		"max_turns": 4,
		"ui_rich": false,
		"ui_expand_all": true
	}`), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	host := settingsHost(t, path, dir)
	host.cfg.UIRich, host.cfg.uiRichSet = false, true
	host.cfg.UIExpandAll, host.cfg.uiExpandSet = true, true
	host.cfg.SettingSources = map[string]string{
		"max_turns":     "config file",
		"ui_rich":       "config file",
		"ui_expand_all": "config file",
	}
	if err := host.ResetSetting("max_turns"); err != nil {
		t.Fatalf("reset max_turns: %v", err)
	}
	loaded, err := loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.MaxTurns != nil {
		t.Fatalf("max_turns survived a reset: %v", *loaded.MaxTurns)
	}
	if field := fieldByKey(t, host.Settings(), "max_turns"); field.Value != "0" ||
		field.Display != "unlimited" || field.Source != "current" {
		t.Fatalf("max_turns after reset = %+v, want the built-in default", field)
	}
	if strDeref(loaded.Model) != "keep-model" || loaded.UIRich == nil || loaded.UIExpandAll == nil {
		t.Fatalf("reset dropped other keys: %+v", loaded)
	}
	if err := host.ResetSetting(tui.SettingRich); err != nil {
		t.Fatalf("reset ui_rich: %v", err)
	}
	if !host.cfg.UIRich || host.cfg.uiRichSet {
		t.Fatalf("live rich after reset = %t/%t", host.cfg.UIRich, host.cfg.uiRichSet)
	}
	if field := fieldByKey(t, host.Settings(), tui.SettingRich); field.Value != "true" ||
		field.Display != "rich" || field.Source != "current" {
		t.Fatalf("ui_rich after reset = %+v, want the built-in default", field)
	}
	loaded, err = loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload after ui_rich reset: %v", err)
	}
	if loaded.UIRich != nil {
		t.Fatalf("ui_rich survived a reset: %v", *loaded.UIRich)
	}
	// Resetting an absent key writes nothing.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := host.ResetSetting("max_turns"); err != nil {
		t.Fatalf("second reset: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second reset: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("a no-op reset rewrote the file")
	}
	if err := host.ResetSetting("agent_overrides"); err == nil {
		t.Fatal("resetting agent_overrides was accepted")
	}
}

func TestTUIHostSecretSetting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"web_fetch_key": "first-secret"}`), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	host := settingsHost(t, path, dir)
	host.cfg.WebFetchKey = "first-secret"
	view := host.Settings()
	field := fieldByKey(t, view, "web_fetch_key")
	if field.Value != "" || field.Display != "set" {
		t.Fatalf("secret field = %+v", field)
	}
	if rendered := fmt.Sprintf("%+v", view); strings.Contains(rendered, "first-secret") {
		t.Fatalf("view exposed the saved secret: %s", rendered)
	}
	if err := host.SetSetting("web_fetch_key", "second-secret"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	loaded, err := loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if strDeref(loaded.WebFetchKey) != "second-secret" {
		t.Fatalf("saved secret = %q", strDeref(loaded.WebFetchKey))
	}
	view = host.Settings()
	if rendered := fmt.Sprintf("%+v", view); strings.Contains(rendered, "second-secret") {
		t.Fatalf("view exposed the new secret: %s", rendered)
	}
	if err := host.ResetSetting("web_fetch_key"); err != nil {
		t.Fatalf("reset secret: %v", err)
	}
	loaded, err = loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload after reset: %v", err)
	}
	if loaded.WebFetchKey != nil {
		t.Fatalf("web_fetch_key survived a reset: %q", *loaded.WebFetchKey)
	}
}

func TestTUIHostWarnsAboutWebFetchPair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"web_fetch_provider": "crawl4ai"}`), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	host := settingsHost(t, path, dir)
	host.cfg.WebFetchProvider = tools.WebFetchProviderCrawl4AI
	view := host.Settings()
	if !strings.Contains(view.Warning, "web_fetch_url is required") {
		t.Fatalf("warning = %q, want the missing endpoint", view.Warning)
	}
	if err := host.SetSetting("web_fetch_url", "http://127.0.0.1:11235"); err != nil {
		t.Fatalf("set web fetch URL: %v", err)
	}
	if view = host.Settings(); view.Warning != "" {
		t.Fatalf("warning after the URL = %q, want none", view.Warning)
	}
}

func TestTUIHostRefusesConfigWrites(t *testing.T) {
	if err := (&tuiHost{}).SetSetting("max_turns", "4"); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled config error = %v, want a disabled-file message", err)
	}
	if err := (&tuiHost{}).ResetSetting("max_turns"); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled config reset = %v, want a disabled-file message", err)
	}

	workspace := t.TempDir()
	ws, err := tools.OpenWorkspace(workspace)
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	path := filepath.Join(workspace, "config.json")
	host := &tuiHost{cfg: config{ConfigPath: path}, ws: ws}
	if err := host.SetSetting("max_turns", "4"); err == nil ||
		!strings.Contains(err.Error(), "workspace") {
		t.Fatalf("workspace config save = %v, want a refusal", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("workspace config file was created: %v", statErr)
	}

	broken := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(broken, []byte(`{"unknown_key": true}`), 0o600); err != nil {
		t.Fatalf("seed broken config: %v", err)
	}
	host = &tuiHost{cfg: config{ConfigPath: broken}}
	if err := host.SetSetting("max_turns", "4"); err == nil ||
		!strings.Contains(err.Error(), "unknown_key") {
		t.Fatalf("broken config save = %v, want the parse error", err)
	}
	raw, readErr := os.ReadFile(broken)
	if readErr != nil {
		t.Fatalf("read broken config: %v", readErr)
	}
	if string(raw) != `{"unknown_key": true}` {
		t.Fatalf("broken config was rewritten: %s", raw)
	}
}
