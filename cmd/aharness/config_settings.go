package main

import (
	"strconv"
	"strings"
	"time"

	"aharness/internal/model"
	"aharness/internal/tools"
	"aharness/internal/tui"
)

// maxConfigModelBytes bounds one model ID entered from the settings browser.
// It matches the agent-override model cap.
const maxConfigModelBytes = 200

// configSettingDef is one config-file key exposed by /settings. The order here
// is the display order. agent_overrides is the only object key: it is an info
// row that points at /agents, which owns the per-agent editor.
type configSettingDef struct {
	key     string
	label   string
	kind    tui.SettingKind
	options []tui.SettingOption
	live    bool
	hint    string
}

// configSettingDefs lists every fileConfig key. TestConfigSettingCoverage
// asserts that this set and the fileConfig JSON tags stay identical.
func configSettingDefs() []configSettingDef {
	boolOptions := []tui.SettingOption{{Value: "true", Label: "on"}, {Value: "false", Label: "off"}}
	return []configSettingDef{
		{key: "model", label: "model", kind: tui.SettingText,
			hint: "new sessions only; resume and fork keep the recorded model"},
		{key: "provider", label: "provider", kind: tui.SettingText,
			hint: "only opencode-go is supported"},
		{key: "workspace", label: "workspace", kind: tui.SettingText,
			hint: "the current session keeps the workspace it opened"},
		{key: "provider_base_url", label: "provider base URL", kind: tui.SettingText,
			hint: "empty uses the catalog URL"},
		{key: "models_dev_url", label: "catalog URL", kind: tui.SettingText,
			hint: "empty uses models.dev"},
		{key: "thinking_effort", label: "thinking effort", kind: tui.SettingText,
			hint: "validated against the model; default clears"},
		{key: "max_turns", label: "max turns", kind: tui.SettingInt,
			hint: "0 is unlimited"},
		{key: "max_total_tokens", label: "max total tokens", kind: tui.SettingInt,
			hint: "0 is unlimited; child tokens count"},
		{key: "max_subagents", label: "max subagents", kind: tui.SettingInt,
			hint: "0 is unlimited"},
		{key: "max_context_tokens", label: "max context tokens", kind: tui.SettingInt,
			hint: "positive; clamped to the model window at startup"},
		{key: "max_output_tokens", label: "max output tokens", kind: tui.SettingInt,
			hint: "0 omits the request field"},
		{key: "session_timeout", label: "session timeout", kind: tui.SettingDuration,
			hint: "0 means no deadline"},
		{key: "model_timeout", label: "model timeout", kind: tui.SettingDuration,
			hint: "0 means no harness deadline"},
		{key: "http_timeout", label: "HTTP timeout", kind: tui.SettingDuration,
			hint: "must be positive"},
		{key: "catalog_timeout", label: "catalog timeout", kind: tui.SettingDuration,
			hint: "must be positive"},
		{key: "require_usage", label: "require usage", kind: tui.SettingToggle, options: boolOptions},
		{key: "log_requests", label: "log requests", kind: tui.SettingToggle, options: boolOptions},
		{key: "searxng_url", label: "SearXNG URL", kind: tui.SettingText,
			hint: "empty disables web_search"},
		{key: "web_timeout", label: "web timeout", kind: tui.SettingDuration,
			hint: "positive, at most 120s"},
		{key: "web_fetch_provider", label: "web fetch provider", kind: tui.SettingEnum,
			options: []tui.SettingOption{{Value: "builtin", Label: "builtin"}, {Value: "crawl4ai", Label: "crawl4ai"}}},
		{key: "web_fetch_url", label: "web fetch URL", kind: tui.SettingText,
			hint: "required for crawl4ai and empty for builtin"},
		{key: "web_fetch_key", label: "web fetch key", kind: tui.SettingSecret,
			hint: "never displayed; prefer AHARNESS_WEB_FETCH_KEY"},
		{key: "ui_rich", label: "assistant rendering", kind: tui.SettingToggle, live: true,
			options: []tui.SettingOption{{Value: "true", Label: "rich"}, {Value: "false", Label: "raw"}}},
		{key: "ui_expand_all", label: "block folding", kind: tui.SettingToggle, live: true,
			options: []tui.SettingOption{{Value: "true", Label: "expanded"}, {Value: "false", Label: "folded"}}},
		{key: "agent_overrides", label: "agent overrides", kind: tui.SettingInfo,
			hint: "edited in /agents"},
	}
}

// Settings reports every config-file setting with its saved or effective value
// and the layer that supplied it. It never returns the web fetch key.
func (h *tuiHost) Settings() tui.SettingsView {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	return settingsView(cfg)
}

// settingsView builds the settings browser rows from a locked config snapshot.
func settingsView(cfg config) tui.SettingsView {
	view := tui.SettingsView{ConfigPath: cfg.ConfigPath}
	var file fileConfig
	if cfg.ConfigPath != "" {
		loaded, err := loadFileConfig(cfg.ConfigPath, false)
		if err != nil {
			view.Err = err.Error()
		} else {
			file = loaded
		}
	}
	for _, def := range configSettingDefs() {
		field := tui.SettingField{
			Key:     def.key,
			Label:   def.label,
			Kind:    def.kind,
			Options: def.options,
			Live:    def.live,
		}
		if def.key == "agent_overrides" {
			field.Action = "agents"
		}
		fileRaw, fileSet := fileSettingRaw(file, def.key)
		field.Saved = fileSet
		sourceDetail := cfg.SettingSources[def.key]
		if sourceDetail == "" {
			sourceDetail = "default"
		}
		// The file had the key at startup but no longer does (a reset or an
		// external edit). With no higher layer, the next startup uses the
		// built-in default rather than the startup snapshot.
		removedFileValue := !fileSet && sourceDetail == "config file"
		if removedFileValue {
			sourceDetail = "default"
		}
		field.SourceDetail = sourceDetail
		if fileSet {
			field.Source = "file"
		} else {
			field.Source = shortSettingSource(sourceDetail)
		}
		raw := fileRaw
		if !fileSet {
			if removedFileValue {
				raw = defaultSettingRaw(def.key)
			} else {
				raw = effectiveSettingRaw(cfg, def.key)
			}
		}
		if def.kind == tui.SettingSecret {
			// The raw sentinel only means "set"; never hand a secret to the
			// display layer.
			field.Value = ""
		} else {
			field.Value = raw
		}
		field.Display = settingDisplay(def, raw, fileSet)
		effectiveRaw := raw
		if settingSourceIsHigher(sourceDetail) {
			effectiveRaw = effectiveSettingRaw(cfg, def.key)
		}
		effectiveSet := true
		if def.kind == tui.SettingSecret {
			effectiveSet = effectiveRaw != ""
		}
		field.Detail = settingDetail(def, fileSet, sourceDetail, settingDisplay(def, effectiveRaw, effectiveSet))
		view.Fields = append(view.Fields, field)
	}
	if view.Err == "" {
		if err := prospectiveWebFetchError(cfg, file); err != nil {
			view.Warning = err.Error()
		}
	}
	return view
}

// fileSettingRaw returns one saved value. The web fetch key is never returned:
// it is represented by the literal "set", or an empty string when explicitly
// empty, and only its presence leaves the host.
func fileSettingRaw(file fileConfig, key string) (raw string, set bool) {
	switch key {
	case "model":
		return derefString(file.Model)
	case "provider":
		return derefString(file.Provider)
	case "workspace":
		return derefString(file.Workspace)
	case "provider_base_url":
		return derefString(file.ProviderBaseURL)
	case "models_dev_url":
		return derefString(file.ModelsDevURL)
	case "searxng_url":
		return derefString(file.SearXNGURL)
	case "web_timeout":
		return configDurationRaw(file.WebTimeout)
	case "web_fetch_provider":
		return derefString(file.WebFetchProvider)
	case "web_fetch_url":
		return derefString(file.WebFetchURL)
	case "web_fetch_key":
		if file.WebFetchKey == nil {
			return "", false
		}
		if *file.WebFetchKey == "" {
			return "", true
		}
		return "set", true
	case "thinking_effort":
		return derefString(file.ThinkingEffort)
	case "max_turns":
		return configIntRaw(file.MaxTurns)
	case "max_total_tokens":
		return configIntRaw(file.MaxTotalTokens)
	case "max_subagents":
		return configIntRaw(file.MaxSubagents)
	case "max_context_tokens":
		return configIntRaw(file.MaxContextTokens)
	case "max_output_tokens":
		return configIntRaw(file.MaxOutputTokens)
	case "session_timeout":
		return configDurationRaw(file.SessionTimeout)
	case "model_timeout":
		return configDurationRaw(file.ModelTimeout)
	case "http_timeout":
		return configDurationRaw(file.HTTPTimeout)
	case "catalog_timeout":
		return configDurationRaw(file.CatalogTimeout)
	case "require_usage":
		return configBoolRaw(file.RequireUsage)
	case "log_requests":
		return configBoolRaw(file.LogRequests)
	case "ui_rich":
		return configBoolRaw(file.UIRich)
	case "ui_expand_all":
		return configBoolRaw(file.UIExpandAll)
	case "agent_overrides":
		if len(file.AgentOverrides) == 0 {
			return "0", false
		}
		return strconv.Itoa(len(file.AgentOverrides)), true
	default:
		return "", false
	}
}

func derefString(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	return *value, true
}

func configIntRaw(value *int) (string, bool) {
	if value == nil {
		return "", false
	}
	return strconv.Itoa(*value), true
}

func configBoolRaw(value *bool) (string, bool) {
	if value == nil {
		return "", false
	}
	return strconv.FormatBool(*value), true
}

func configDurationRaw(value *configDuration) (string, bool) {
	if value == nil {
		return "", false
	}
	return time.Duration(*value).String(), true
}

// defaultSettingRaw is the built-in value for one key after its config-file
// entry is removed. A flag or environment layer above the file is handled by
// the caller; these are the values the next startup uses with neither.
func defaultSettingRaw(key string) string {
	switch key {
	case "provider":
		return providerName
	case "web_timeout":
		return tools.WebTimeoutDefault.String()
	case "web_fetch_provider":
		return tools.WebFetchProviderBuiltin
	case "max_subagents":
		return strconv.Itoa(defaultMaxSubagents)
	case "http_timeout":
		return model.DefaultHTTPTimeout.String()
	case "catalog_timeout":
		return model.CatalogRequestTimeout.String()
	case "ui_rich":
		return "true"
	case "ui_expand_all", "require_usage", "log_requests":
		return "false"
	case "session_timeout", "model_timeout":
		return "0s"
	case "max_turns", "max_total_tokens", "max_context_tokens", "max_output_tokens", "agent_overrides":
		return "0"
	default:
		return ""
	}
}

// effectiveSettingRaw returns the current process value for one key. The web
// fetch key is reduced to its presence, like fileSettingRaw.
func effectiveSettingRaw(cfg config, key string) string {
	switch key {
	case "model":
		return cfg.Model
	case "provider":
		return cfg.Provider
	case "workspace":
		return cfg.Workspace
	case "provider_base_url":
		return cfg.BaseURL
	case "models_dev_url":
		return cfg.ModelsDevURL
	case "searxng_url":
		return cfg.SearXNGURL
	case "web_timeout":
		return cfg.WebTimeout.String()
	case "web_fetch_provider":
		return cfg.WebFetchProvider
	case "web_fetch_url":
		return cfg.WebFetchURL
	case "web_fetch_key":
		if cfg.WebFetchKey == "" {
			return ""
		}
		return "set"
	case "thinking_effort":
		return cfg.ThinkingEffort
	case "max_turns":
		return strconv.Itoa(cfg.MaxTurns)
	case "max_total_tokens":
		return strconv.Itoa(cfg.MaxTotalTokens)
	case "max_subagents":
		return strconv.Itoa(cfg.MaxSubagents)
	case "max_context_tokens":
		return strconv.Itoa(cfg.MaxContextTokens)
	case "max_output_tokens":
		return strconv.Itoa(cfg.MaxOutputTokens)
	case "session_timeout":
		return cfg.SessionTimeout.String()
	case "model_timeout":
		return cfg.ModelTimeout.String()
	case "http_timeout":
		return cfg.HTTPTimeout.String()
	case "catalog_timeout":
		return cfg.CatalogTimeout.String()
	case "require_usage":
		return strconv.FormatBool(cfg.RequireUsage)
	case "log_requests":
		return strconv.FormatBool(cfg.LogRequests)
	case "ui_rich":
		return strconv.FormatBool(cfg.UIRich)
	case "ui_expand_all":
		return strconv.FormatBool(cfg.UIExpandAll)
	case "agent_overrides":
		return strconv.Itoa(len(cfg.AgentOverrides))
	default:
		return ""
	}
}

// settingDisplay renders one raw value for the value column.
func settingDisplay(def configSettingDef, raw string, set bool) string {
	switch def.kind {
	case tui.SettingToggle, tui.SettingEnum:
		for _, option := range def.options {
			if option.Value == raw {
				return option.Label
			}
		}
		return raw
	case tui.SettingSecret:
		if !set {
			return "(unset)"
		}
		if raw == "" {
			return "(empty)"
		}
		return "set"
	case tui.SettingInfo:
		if raw == "" || raw == "0" {
			return "none"
		}
		if raw == "1" {
			return "1 agent"
		}
		return raw + " agents"
	}
	switch def.key {
	case "model":
		if raw == "" {
			return "(none)"
		}
	case "workspace":
		if raw == "" {
			return "(current directory)"
		}
	case "provider_base_url":
		if raw == "" {
			return "(catalog URL)"
		}
	case "models_dev_url":
		if raw == "" {
			return "(models.dev)"
		}
	case "thinking_effort":
		if raw == "" {
			return "provider default"
		}
	case "max_turns", "max_total_tokens", "max_subagents":
		if raw == "0" {
			return "unlimited"
		}
	case "max_context_tokens":
		if raw == "0" || raw == "" {
			return "(unset)"
		}
	case "max_output_tokens":
		if raw == "0" {
			return "unset"
		}
	case "session_timeout", "model_timeout":
		if raw == "0s" {
			return "none"
		}
	case "searxng_url":
		if raw == "" {
			return "(disabled)"
		}
	case "web_fetch_url":
		if raw == "" {
			return "(unset)"
		}
	}
	if raw == "" {
		return "(empty)"
	}
	return raw
}

// settingDetail explains the row in the footer: scope, file/higher-layer state,
// and the setting-specific hint.
func settingDetail(def configSettingDef, fileSet bool, sourceDetail, effective string) string {
	parts := make([]string, 0, 3)
	if def.live {
		parts = append(parts, "applies now")
	} else {
		parts = append(parts, "next process")
	}
	if fileSet {
		parts = append(parts, "saved in the config file")
		if settingSourceIsHigher(sourceDetail) {
			parts = append(parts, sourceDetail+" wins in this process (effective: "+effective+")")
		}
	} else if settingSourceIsHigher(sourceDetail) {
		parts = append(parts, "using "+sourceDetail+" in this process")
	} else {
		parts = append(parts, "not saved; the effective value applies")
	}
	if def.hint != "" {
		parts = append(parts, def.hint)
	}
	return strings.Join(parts, " · ")
}

// settingSourceIsHigher reports whether a flag or environment variable sits
// above the config file for one key.
func settingSourceIsHigher(source string) bool {
	return strings.HasPrefix(source, "flag ") || strings.HasPrefix(source, "environment ")
}

// shortSettingSource turns the stored provenance into one column label.
func shortSettingSource(source string) string {
	switch {
	case strings.HasPrefix(source, "flag "):
		return "flag"
	case strings.HasPrefix(source, "environment "):
		return "env"
	case source == "config file":
		return "file"
	default:
		return "current"
	}
}

// prospectiveWebFetchError applies the cross-field web_fetch rules to the
// values the next startup would resolve: a new file value if the file sets the
// key, an explicit flag/environment value when that layer wins, and otherwise
// the built-in default. It is a warning in the browser, not a write gate: an
// interactive editor must be able to enter the crawl4ai URL before the provider
// is switched, and the pair is validated again at startup.
func prospectiveWebFetchError(cfg config, file fileConfig) error {
	provider := tools.WebFetchProviderBuiltin
	switch {
	case settingSourceIsHigher(cfg.SettingSources["web_fetch_provider"]):
		provider = cfg.WebFetchProvider
	case file.WebFetchProvider != nil:
		provider = *file.WebFetchProvider
	}
	url := ""
	switch {
	case settingSourceIsHigher(cfg.SettingSources["web_fetch_url"]):
		url = cfg.WebFetchURL
	case file.WebFetchURL != nil:
		url = *file.WebFetchURL
	}
	key := ""
	switch {
	case settingSourceIsHigher(cfg.SettingSources["web_fetch_key"]):
		key = cfg.WebFetchKey
	case file.WebFetchKey != nil:
		key = *file.WebFetchKey
	}
	return tools.ValidateWebFetchConfig(provider, url, key)
}
