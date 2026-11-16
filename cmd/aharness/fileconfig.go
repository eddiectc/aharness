package main

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/tools"
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
	Model            *string                      `json:"model,omitzero"`
	Provider         *string                      `json:"provider,omitzero"`
	Workspace        *string                      `json:"workspace,omitzero"`
	ProviderBaseURL  *string                      `json:"provider_base_url,omitzero"`
	ModelsDevURL     *string                      `json:"models_dev_url,omitzero"`
	SearXNGURL       *string                      `json:"searxng_url,omitzero"`
	WebTimeout       *configDuration              `json:"web_timeout,omitzero"`
	WebFetchProvider *string                      `json:"web_fetch_provider,omitzero"`
	WebFetchURL      *string                      `json:"web_fetch_url,omitzero"`
	WebFetchKey      *string                      `json:"web_fetch_key,omitzero"`
	ThinkingEffort   *string                      `json:"thinking_effort,omitzero"`
	MaxTurns         *int                         `json:"max_turns,omitzero"`
	MaxTotalTokens   *int                         `json:"max_total_tokens,omitzero"`
	MaxSubagents     *int                         `json:"max_subagents,omitzero"`
	MaxContextTokens *int                         `json:"max_context_tokens,omitzero"`
	MaxOutputTokens  *int                         `json:"max_output_tokens,omitzero"`
	SessionTimeout   *configDuration              `json:"session_timeout,omitzero"`
	ModelTimeout     *configDuration              `json:"model_timeout,omitzero"`
	HTTPTimeout      *configDuration              `json:"http_timeout,omitzero"`
	CatalogTimeout   *configDuration              `json:"catalog_timeout,omitzero"`
	RequireUsage     *bool                        `json:"require_usage,omitzero"`
	LogRequests      *bool                        `json:"log_requests,omitzero"`
	UIRich           *bool                        `json:"ui_rich,omitzero"`
	UIExpandAll      *bool                        `json:"ui_expand_all,omitzero"`
	AgentOverrides   map[string]fileAgentOverride `json:"agent_overrides,omitzero"`
}

// fileAgentOverride is the JSON shape of one operator override. Only routing
// settings are overridable; a tool grant or prompt body here is an unknown key
// and a config error.
type fileAgentOverride struct {
	Model          *string `json:"model,omitzero"`
	ThinkingEffort *string `json:"thinking_effort,omitzero"`
	MaxTurns       *int    `json:"max_turns,omitzero"`
	Enabled        *bool   `json:"enabled,omitzero"`
}

// toAgent converts one file override into the catalog type.
func (o fileAgentOverride) toAgent() agent.AgentOverride {
	return agent.AgentOverride{
		Model:          o.Model,
		ThinkingEffort: o.ThinkingEffort,
		MaxTurns:       o.MaxTurns,
		Enabled:        o.Enabled,
	}
}

// fromAgent converts one catalog override into the file shape.
func fromAgent(o agent.AgentOverride) fileAgentOverride {
	return fileAgentOverride{
		Model:          o.Model,
		ThinkingEffort: o.ThinkingEffort,
		MaxTurns:       o.MaxTurns,
		Enabled:        o.Enabled,
	}
}

// agentOverrides converts every file override into the catalog type. A nil or
// empty map yields nil, which means "no overrides".
func (c fileConfig) agentOverrides() map[string]agent.AgentOverride {
	if len(c.AgentOverrides) == 0 {
		return nil
	}
	out := make(map[string]agent.AgentOverride, len(c.AgentOverrides))
	for name, override := range c.AgentOverrides {
		out[name] = override.toAgent()
	}
	return out
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

// MarshalJSON implements json.Marshaler so the settings writer preserves the
// same "90s"/"2h" spelling the reader accepts.
func (d configDuration) MarshalJSON() ([]byte, error) {
	return jsonv2.Marshal(time.Duration(d).String())
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
		{"max_subagents", c.MaxSubagents},
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
	// Overrides are validated without the discovered catalog: the name shape,
	// the two free-form routing values, and the turn cap are fixed rules. The
	// catalog exists only in the TUI path, which additionally checks that each
	// name resolves to an active definition.
	if err := agent.ValidateAgentOverrides(c.agentOverrides()); err != nil {
		return fail("%v", err)
	}
	if c.SearXNGURL != nil && *c.SearXNGURL != "" {
		if _, err := tools.ParseSearXNGURL(*c.SearXNGURL); err != nil {
			return fail("searxng_url: %v", err)
		}
	}
	if c.WebFetchProvider != nil {
		if _, err := tools.ParseWebFetchProvider(*c.WebFetchProvider); err != nil {
			return fail("web_fetch_provider: %v", err)
		}
	}
	if c.WebFetchURL != nil && *c.WebFetchURL != "" {
		if _, err := tools.ParseWebFetchURL(*c.WebFetchURL); err != nil {
			return fail("web_fetch_url: %v", err)
		}
	}
	if c.WebFetchKey != nil {
		if err := tools.ValidateWebFetchKey(*c.WebFetchKey); err != nil {
			return fail("web_fetch_key: %v", err)
		}
	}
	if c.WebTimeout != nil {
		value := time.Duration(*c.WebTimeout)
		if value <= 0 || value > tools.WebTimeoutMax {
			return fail("web_timeout must be positive and at most %s, got %s", tools.WebTimeoutMax, value)
		}
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

// writeFileConfig atomically replaces path with cfg. It writes a same-directory
// temporary file in 0600, syncs it, and renames it over the target, so a
// failure leaves the previous file intact and no partial file is ever visible
// under the config path. The temporary file is removed on every error path.
func writeFileConfig(path string, cfg fileConfig) error {
	if path == "" {
		return errors.New("no config file is configured")
	}
	data, err := marshalFileConfig(cfg)
	if err != nil {
		return fmt.Errorf("encode config file %s: %w", path, err)
	}
	if len(data) > maxConfigFileBytes {
		return fmt.Errorf("config file %s would be %d bytes; the limit is %d", path, len(data), maxConfigFileBytes)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("write config file %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config file %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config file %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config file %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config file %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config file %s: %w", path, err)
	}
	// The rename is already atomic; syncing the directory is a best-effort
	// durability improvement and cannot undo the replacement.
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

// marshalFileConfig renders the config file with a stable two-space layout and
// a trailing newline, matching what an operator would write by hand.
func marshalFileConfig(cfg fileConfig) ([]byte, error) {
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf, jsontext.WithIndent("  "))
	if err := jsonv2.MarshalEncode(enc, cfg); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
