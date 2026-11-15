package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"aharness/internal/model"
	"aharness/internal/tools"
	"aharness/internal/tui"
)

// SetSetting persists one config-file key. The file is re-read and validated
// as a whole before the atomic replacement, so an external edit or a malformed
// file is never overwritten. Only the two display preferences apply to this
// process; every other key is used at the next startup.
func (h *tuiHost) SetSetting(key, value string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	trimmed := strings.TrimSpace(value)
	var liveBool bool
	if key == tui.SettingRich || key == tui.SettingExpandAll {
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return fmt.Errorf("%s must be true or false", key)
		}
		liveBool = parsed
	}
	changed, err := h.updateConfigFile(func(file *fileConfig) (bool, error) {
		if err := h.applyConfigSetting(file, key, trimmed); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil || !changed {
		return err
	}
	switch key {
	case tui.SettingRich:
		h.cfg.UIRich = liveBool
		h.cfg.uiRichSet = true
	case tui.SettingExpandAll:
		h.cfg.UIExpandAll = liveBool
		h.cfg.uiExpandSet = true
	}
	return nil
}

// ResetSetting removes one saved key so the built-in default applies again. A
// key that is not in the file is a no-op and writes nothing.
func (h *tuiHost) ResetSetting(key string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed, err := h.updateConfigFile(func(file *fileConfig) (bool, error) {
		return clearConfigSetting(file, key)
	})
	if err != nil || !changed {
		return err
	}
	switch key {
	case tui.SettingRich:
		h.cfg.UIRich = true
		h.cfg.uiRichSet = false
	case tui.SettingExpandAll:
		h.cfg.UIExpandAll = false
		h.cfg.uiExpandSet = false
	}
	return nil
}

// updateConfigFile re-reads the active file, applies mutate, validates the
// result, and replaces the file atomically. It reports whether mutate changed
// anything; a no-op does not rewrite the file. The caller holds h.mu.
func (h *tuiHost) updateConfigFile(mutate func(*fileConfig) (bool, error)) (bool, error) {
	path := h.cfg.ConfigPath
	if path == "" {
		return false, errors.New(`the config file is disabled (-config ""); settings cannot be saved`)
	}
	if withinWorkspace(h.ws, path) {
		return false, errors.New("the config file is inside the workspace; settings are operator state and are never written there")
	}
	file, err := loadFileConfig(path, false)
	if err != nil {
		return false, err
	}
	changed, err := mutate(&file)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if err := file.validate(path); err != nil {
		return false, err
	}
	if err := writeFileConfig(path, file); err != nil {
		return false, err
	}
	return true, nil
}

// applyConfigSetting parses and validates one value, then stores it in file. An
// empty string removes the key for every string-like kind. The web fetch key is
// never included in an error message.
func (h *tuiHost) applyConfigSetting(file *fileConfig, key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "model":
		if value == "" {
			file.Model = nil
			return nil
		}
		if len(value) > maxConfigModelBytes {
			return fmt.Errorf("model: %d bytes exceeds the %d-byte limit", len(value), maxConfigModelBytes)
		}
		if strings.ContainsFunc(value, unicode.IsSpace) || strings.ContainsFunc(value, unicode.IsControl) {
			return errors.New("model must not contain whitespace or control characters")
		}
		if len(h.catalog) > 0 && value != h.cfg.Model {
			m, ok := h.catalog[value]
			if !ok {
				return fmt.Errorf("model %q is not in the loaded catalog", value)
			}
			if m.Unavailable != "" {
				return fmt.Errorf("model %q is not usable: %s", value, m.Unavailable)
			}
		}
		file.Model = &value
	case "provider":
		if value == "" {
			file.Provider = nil
			return nil
		}
		if err := validateProvider(value); err != nil {
			return err
		}
		file.Provider = &value
	case "workspace":
		if value == "" {
			file.Workspace = nil
			return nil
		}
		dir, err := resolveWorkspace(value)
		if err != nil {
			return err
		}
		file.Workspace = &dir
	case "provider_base_url":
		if value == "" {
			file.ProviderBaseURL = nil
			return nil
		}
		if err := validateBaseURL(value); err != nil {
			return err
		}
		file.ProviderBaseURL = &value
	case "models_dev_url":
		if value == "" {
			file.ModelsDevURL = nil
			return nil
		}
		if _, err := model.NewModelsDevSource(model.CatalogOptions{URL: value, StateDir: h.cfg.StateDir}); err != nil {
			return err
		}
		file.ModelsDevURL = &value
	case "searxng_url":
		if value == "" {
			file.SearXNGURL = nil
			return nil
		}
		normalized, err := tools.ParseSearXNGURL(value)
		if err != nil {
			return fmt.Errorf("searxng_url: %w", err)
		}
		file.SearXNGURL = &normalized
	case "web_timeout":
		if value == "" {
			file.WebTimeout = nil
			return nil
		}
		duration, err := parseSettingDuration(key, value)
		if err != nil {
			return err
		}
		if err := validateWebTimeout(duration); err != nil {
			return fmt.Errorf("web_timeout: %w", err)
		}
		converted := configDuration(duration)
		file.WebTimeout = &converted
	case "web_fetch_provider":
		if value == "" {
			file.WebFetchProvider = nil
			return nil
		}
		provider, err := tools.ParseWebFetchProvider(value)
		if err != nil {
			return err
		}
		file.WebFetchProvider = &provider
	case "web_fetch_url":
		if value == "" {
			file.WebFetchURL = nil
			return nil
		}
		normalized, err := tools.ParseWebFetchURL(value)
		if err != nil {
			return err
		}
		file.WebFetchURL = &normalized
	case "web_fetch_key":
		if value == "" {
			file.WebFetchKey = nil
			return nil
		}
		if err := tools.ValidateWebFetchKey(value); err != nil {
			return err
		}
		file.WebFetchKey = &value
	case "thinking_effort":
		if value == "" || strings.EqualFold(value, "default") {
			file.ThinkingEffort = nil
			return nil
		}
		if !model.ValidReasoningEffort(value) {
			return fmt.Errorf("invalid thinking effort %q: must match [a-z][a-z0-9_-]* and be at most %d bytes",
				value, model.MaxThinkingEffortBytes)
		}
		modelID := h.cfg.Model
		if file.Model != nil {
			modelID = *file.Model
		}
		if m, ok := h.catalog[modelID]; ok {
			if err := validateModelThinkingEffort(value, m); err != nil {
				return err
			}
		}
		file.ThinkingEffort = &value
	case "max_turns", "max_total_tokens", "max_subagents", "max_context_tokens", "max_output_tokens":
		if value == "" {
			clearConfigInt(file, key)
			return nil
		}
		number, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s must be an integer", key)
		}
		if key == "max_context_tokens" {
			if number <= 0 {
				return fmt.Errorf("%s must be positive, got %d", key, number)
			}
		} else if number < 0 {
			return fmt.Errorf("%s must not be negative, got %d", key, number)
		}
		setConfigInt(file, key, number)
	case "session_timeout", "model_timeout", "http_timeout", "catalog_timeout":
		if value == "" {
			clearConfigDuration(file, key)
			return nil
		}
		duration, err := parseSettingDuration(key, value)
		if err != nil {
			return err
		}
		if (key == "http_timeout" || key == "catalog_timeout") && duration <= 0 {
			return fmt.Errorf("%s must be positive, got %s", key, duration)
		}
		if duration < 0 {
			return fmt.Errorf("%s must not be negative, got %s", key, duration)
		}
		setConfigDuration(file, key, duration)
	case "require_usage", "log_requests", "ui_rich", "ui_expand_all":
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s must be true or false", key)
		}
		setConfigBool(file, key, enabled)
	case "agent_overrides":
		return errors.New("agent_overrides is edited in /agents")
	default:
		return fmt.Errorf("unknown config setting %q", key)
	}
	return nil
}

// parseSettingDuration parses an optional duration; an empty value is zero.
func parseSettingDuration(key, value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return duration, nil
}

func clearConfigInt(file *fileConfig, key string) {
	switch key {
	case "max_turns":
		file.MaxTurns = nil
	case "max_total_tokens":
		file.MaxTotalTokens = nil
	case "max_subagents":
		file.MaxSubagents = nil
	case "max_context_tokens":
		file.MaxContextTokens = nil
	case "max_output_tokens":
		file.MaxOutputTokens = nil
	}
}

func setConfigInt(file *fileConfig, key string, value int) {
	switch key {
	case "max_turns":
		file.MaxTurns = &value
	case "max_total_tokens":
		file.MaxTotalTokens = &value
	case "max_subagents":
		file.MaxSubagents = &value
	case "max_context_tokens":
		file.MaxContextTokens = &value
	case "max_output_tokens":
		file.MaxOutputTokens = &value
	}
}

func clearConfigDuration(file *fileConfig, key string) {
	switch key {
	case "session_timeout":
		file.SessionTimeout = nil
	case "model_timeout":
		file.ModelTimeout = nil
	case "http_timeout":
		file.HTTPTimeout = nil
	case "catalog_timeout":
		file.CatalogTimeout = nil
	}
}

func setConfigDuration(file *fileConfig, key string, value time.Duration) {
	converted := configDuration(value)
	switch key {
	case "session_timeout":
		file.SessionTimeout = &converted
	case "model_timeout":
		file.ModelTimeout = &converted
	case "http_timeout":
		file.HTTPTimeout = &converted
	case "catalog_timeout":
		file.CatalogTimeout = &converted
	}
}

func setConfigBool(file *fileConfig, key string, value bool) {
	switch key {
	case "require_usage":
		file.RequireUsage = &value
	case "log_requests":
		file.LogRequests = &value
	case "ui_rich":
		file.UIRich = &value
	case "ui_expand_all":
		file.UIExpandAll = &value
	}
}

// clearConfigSetting removes one key and reports whether it was present.
func clearConfigSetting(file *fileConfig, key string) (bool, error) {
	switch key {
	case "model":
		return clearString(&file.Model)
	case "provider":
		return clearString(&file.Provider)
	case "workspace":
		return clearString(&file.Workspace)
	case "provider_base_url":
		return clearString(&file.ProviderBaseURL)
	case "models_dev_url":
		return clearString(&file.ModelsDevURL)
	case "searxng_url":
		return clearString(&file.SearXNGURL)
	case "web_timeout":
		return clearDuration(&file.WebTimeout)
	case "web_fetch_provider":
		return clearString(&file.WebFetchProvider)
	case "web_fetch_url":
		return clearString(&file.WebFetchURL)
	case "web_fetch_key":
		return clearString(&file.WebFetchKey)
	case "thinking_effort":
		return clearString(&file.ThinkingEffort)
	case "max_turns":
		return clearInt(&file.MaxTurns)
	case "max_total_tokens":
		return clearInt(&file.MaxTotalTokens)
	case "max_subagents":
		return clearInt(&file.MaxSubagents)
	case "max_context_tokens":
		return clearInt(&file.MaxContextTokens)
	case "max_output_tokens":
		return clearInt(&file.MaxOutputTokens)
	case "session_timeout":
		return clearDuration(&file.SessionTimeout)
	case "model_timeout":
		return clearDuration(&file.ModelTimeout)
	case "http_timeout":
		return clearDuration(&file.HTTPTimeout)
	case "catalog_timeout":
		return clearDuration(&file.CatalogTimeout)
	case "require_usage":
		return clearBool(&file.RequireUsage)
	case "log_requests":
		return clearBool(&file.LogRequests)
	case "ui_rich":
		return clearBool(&file.UIRich)
	case "ui_expand_all":
		return clearBool(&file.UIExpandAll)
	case "agent_overrides":
		return false, errors.New("agent_overrides is edited in /agents")
	default:
		return false, fmt.Errorf("unknown config setting %q", key)
	}
}

func clearString(value **string) (bool, error) {
	if *value == nil {
		return false, nil
	}
	*value = nil
	return true, nil
}

func clearInt(value **int) (bool, error) {
	if *value == nil {
		return false, nil
	}
	*value = nil
	return true, nil
}

func clearBool(value **bool) (bool, error) {
	if *value == nil {
		return false, nil
	}
	*value = nil
	return true, nil
}

func clearDuration(value **configDuration) (bool, error) {
	if *value == nil {
		return false, nil
	}
	*value = nil
	return true, nil
}
