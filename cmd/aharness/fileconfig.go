package main

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

// maxConfigFileBytes caps one config file. The file only holds a handful of
// scalar settings, so anything larger is a mistake or an attack.
const maxConfigFileBytes = 1 << 20

// configFileHint is the human name of the implicit config file. The state
// directory is resolved at run time, so help text names the layout instead of
// an absolute path.
const configFileHint = "<state-dir>/config.json"

// fileConfig is the optional JSON config file. Every key is optional; a
// present key participates in the flag > environment > config > default
// resolution. The provider token, state directory, task, session selection,
// and mode flags are deliberately absent: the token never belongs on disk, the
// state directory locates this file, and the rest are per-invocation inputs.
type fileConfig struct {
	Model            *string         `json:"model"`
	Provider         *string         `json:"provider"`
	Workspace        *string         `json:"workspace"`
	ProviderBaseURL  *string         `json:"provider_base_url"`
	ModelsDevURL     *string         `json:"models_dev_url"`
	ThinkingEffort   *string         `json:"thinking_effort"`
	MaxTurns         *int            `json:"max_turns"`
	MaxTotalTokens   *int            `json:"max_total_tokens"`
	MaxContextTokens *int            `json:"max_context_tokens"`
	MaxOutputTokens  *int            `json:"max_output_tokens"`
	SessionTimeout   *configDuration `json:"session_timeout"`
	ModelTimeout     *configDuration `json:"model_timeout"`
	HTTPTimeout      *configDuration `json:"http_timeout"`
	CatalogTimeout   *configDuration `json:"catalog_timeout"`
	RequireUsage     *bool           `json:"require_usage"`
	LogRequests      *bool           `json:"log_requests"`
}

// configDuration decodes a duration written as a string, so a config file uses
// the same "90s"/"2h" spelling as the flags instead of raw nanoseconds.
type configDuration time.Duration

// UnmarshalJSON implements json.Unmarshaler.
func (d *configDuration) UnmarshalJSON(data []byte) error {
	var raw string
	if err := jsonv2.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("want a duration string such as \"90s\": %w", err)
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return err
	}
	*d = configDuration(parsed)
	return nil
}

// loadFileConfig reads one optional JSON config file. An empty path selects no
// file. A missing implicit file is not an error; a missing explicit file is,
// because a mistyped -config/AHARNESS_CONFIG must not silently fall back to
// defaults. Unknown keys are rejected so a typo is reported instead of ignored.
func loadFileConfig(path string, explicit bool) (fileConfig, error) {
	if path == "" {
		return fileConfig{}, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) && !explicit {
		return fileConfig{}, nil
	}
	if err != nil {
		return fileConfig{}, fmt.Errorf("read config file %s: %w", path, err)
	}
	// The config file is read-only; a close error cannot change the result.
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxConfigFileBytes+1))
	if err != nil {
		return fileConfig{}, fmt.Errorf("read config file %s: %w", path, err)
	}
	if len(data) > maxConfigFileBytes {
		return fileConfig{}, fmt.Errorf("config file %s exceeds %d bytes", path, maxConfigFileBytes)
	}

	var cfg fileConfig
	if err := jsonv2.Unmarshal(data, &cfg, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fileConfig{}, fmt.Errorf("config file %s: %w", path, err)
	}
	if err := cfg.validate(path); err != nil {
		return fileConfig{}, err
	}
	return cfg, nil
}

// validate rejects config values that are invalid regardless of which layer
// ultimately wins, so a broken file is reported even when a flag overrides the
// offending key.
func (c fileConfig) validate(path string) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("config file %s: %s", path, fmt.Sprintf(format, args...))
	}
	for _, budget := range []struct {
		name  string
		value *int
	}{
		{"max_turns", c.MaxTurns},
		{"max_total_tokens", c.MaxTotalTokens},
		{"max_output_tokens", c.MaxOutputTokens},
	} {
		if budget.value != nil && *budget.value < 0 {
			return fail("%s must not be negative, got %d", budget.name, *budget.value)
		}
	}
	if c.MaxContextTokens != nil && *c.MaxContextTokens <= 0 {
		return fail("max_context_tokens must be positive, got %d", *c.MaxContextTokens)
	}
	// An empty effort means "unset" everywhere else; in a file it is a typo,
	// because omitting the key already expresses "unset" and the flag rejects
	// an explicit empty value too.
	if c.ThinkingEffort != nil && *c.ThinkingEffort == "" {
		return fail("thinking_effort must not be empty; omit the key or use \"default\"")
	}
	for _, timeout := range []struct {
		name        string
		value       *configDuration
		mustBeAbove bool
	}{
		{"session_timeout", c.SessionTimeout, false},
		{"model_timeout", c.ModelTimeout, false},
		{"http_timeout", c.HTTPTimeout, true},
		{"catalog_timeout", c.CatalogTimeout, true},
	} {
		if timeout.value == nil {
			continue
		}
		value := time.Duration(*timeout.value)
		if timeout.mustBeAbove && value <= 0 {
			return fail("%s must be positive, got %s", timeout.name, value)
		}
		if value < 0 {
			return fail("%s must not be negative, got %s", timeout.name, value)
		}
	}
	return nil
}

// layer holds the three configurable sources for one setting. Precedence,
// highest first: explicit flag, environment, config file, built-in default.
// Only explicit values participate, so an unset flag or an empty environment
// variable never masks the layer below it.
type layer[T any] struct {
	flag    T
	flagSet bool
	env     T
	envSet  bool
	file    *T
	def     T
}

// layered assembles one setting's layers. file is nil when the config file
// omits the key.
func layered[T any](flagValue T, flagSet bool, envValue T, envSet bool, file *T, def T) layer[T] {
	return layer[T]{flag: flagValue, flagSet: flagSet, env: envValue, envSet: envSet, file: file, def: def}
}

// value returns the effective value for the setting.
func (l layer[T]) value() T {
	switch {
	case l.flagSet:
		return l.flag
	case l.envSet:
		return l.env
	case l.file != nil:
		return *l.file
	default:
		return l.def
	}
}

// set reports whether a configurable layer supplied the value. A caller uses
// it where an explicit value changes behavior beyond the value itself, such as
// clamping a model context window or clearing a recorded budget.
func (l layer[T]) set() bool {
	return l.flagSet || l.envSet || l.file != nil
}

// durationLayer assembles one duration setting. The config file spells
// durations as strings, so its value is converted to a time.Duration here.
func durationLayer(flagValue time.Duration, flagSet bool, envValue time.Duration, envSet bool, file *configDuration, def time.Duration) layer[time.Duration] {
	if file == nil {
		return layered(flagValue, flagSet, envValue, envSet, (*time.Duration)(nil), def)
	}
	value := time.Duration(*file)
	return layered(flagValue, flagSet, envValue, envSet, &value, def)
}
