package agent

import (
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/eddiectc/aharness/model"
)

// Agent override caps. They bound the two free-form routing values so a config
// file cannot smuggle a huge string into every child request.
const (
	maxAgentOverrideModelBytes = 200
	maxAgentOverrides          = maxAgents
)

// AgentOverride is the operator override for one agent definition. Every field
// is optional: nil means "use the definition's value" (and the default enabled
// state for Enabled). Only routing settings are overridable; the tool grant and
// the prompt body stay file-only so a settings surface cannot widen authority
// without editing a definition.
type AgentOverride struct {
	Model          *string
	ThinkingEffort *string
	MaxTurns       *int
	Enabled        *bool
}

// Clone returns an independent copy of the override. Every pointer field is
// copied, so a host can edit one field without mutating the original.
func (o AgentOverride) Clone() AgentOverride {
	out := o
	if o.Model != nil {
		value := *o.Model
		out.Model = &value
	}
	if o.ThinkingEffort != nil {
		value := *o.ThinkingEffort
		out.ThinkingEffort = &value
	}
	if o.MaxTurns != nil {
		value := *o.MaxTurns
		out.MaxTurns = &value
	}
	if o.Enabled != nil {
		value := *o.Enabled
		out.Enabled = &value
	}
	return out
}

// Empty reports whether the override changes nothing.
func (o AgentOverride) Empty() bool {
	return o.Model == nil && o.ThinkingEffort == nil && o.MaxTurns == nil && o.Enabled == nil
}

// ValidateAgentOverride checks one override independently of the discovered
// catalog. The config file validates it before the catalog exists; the catalog
// validates it again when it applies the override.
func ValidateAgentOverride(name string, o AgentOverride) error {
	if err := ValidateAgentName(name); err != nil {
		return err
	}
	if o.Model != nil {
		value := *o.Model
		if value == "" {
			return errors.New("model must not be empty; omit the key to inherit")
		}
		if len(value) > maxAgentOverrideModelBytes {
			return fmt.Errorf("model is %d bytes; the limit is %d", len(value), maxAgentOverrideModelBytes)
		}
		if !utf8.ValidString(value) {
			return errors.New("model is not valid UTF-8")
		}
	}
	if o.ThinkingEffort != nil {
		value := *o.ThinkingEffort
		if value == "" {
			return errors.New("thinking_effort must not be empty; omit the key to inherit")
		}
		if !model.ValidReasoningEffort(value) {
			return fmt.Errorf("thinking_effort %q must match [a-z][a-z0-9_-]* and be at most %d bytes",
				value, model.MaxThinkingEffortBytes)
		}
	}
	if o.MaxTurns != nil && *o.MaxTurns <= 0 {
		return fmt.Errorf("max_turns must be positive, got %d; omit the key to inherit", *o.MaxTurns)
	}
	return nil
}

// ValidateAgentOverrides checks a whole override map independently of the
// discovered catalog. The config file uses it before the catalog exists; the
// catalog uses it again before applying the set.
func ValidateAgentOverrides(overrides map[string]AgentOverride) error {
	if len(overrides) > maxAgentOverrides {
		return fmt.Errorf("agent_overrides has %d entries; the limit is %d", len(overrides), maxAgentOverrides)
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := ValidateAgentOverride(name, overrides[name]); err != nil {
			return fmt.Errorf("agent_overrides[%q]: %w", name, err)
		}
	}
	return nil
}

// ApplyAgentOverrides returns a catalog with the operator overrides applied to
// a copy of base. The definition's tool grant and prompt body are never
// touched. An unknown agent name, an invalid override value, or an override
// for a file that failed to parse is an error; the error names the agent and
// the field.
func ApplyAgentOverrides(base *AgentCatalog, overrides map[string]AgentOverride) (*AgentCatalog, error) {
	if err := ValidateAgentOverrides(overrides); err != nil {
		return nil, err
	}
	out := &AgentCatalog{}
	if base != nil {
		out.Agents = slices.Clone(base.Agents)
		out.Invalid = slices.Clone(base.Invalid)
		out.Warnings = slices.Clone(base.Warnings)
	}
	if len(overrides) == 0 {
		return out, nil
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	slices.Sort(names)

	index := make(map[string]int, len(out.Agents))
	for i, def := range out.Agents {
		index[def.Name] = i
	}
	invalid := make(map[string]AgentDefinition, len(out.Invalid))
	for _, def := range out.Invalid {
		invalid[def.Name] = def
	}

	// Validate every name before applying any, so a rejected override cannot
	// leave the catalog half-updated.
	for _, name := range names {
		if _, ok := index[name]; ok {
			continue
		}
		if bad, ok := invalid[name]; ok {
			return nil, fmt.Errorf("agent_overrides[%q]: the definition file %s is invalid: %s",
				name, bad.Path, bad.Err)
		}
		return nil, fmt.Errorf("agent_overrides[%q]: no agent definition with that name", name)
	}

	for _, name := range names {
		override := overrides[name]
		if override.Empty() {
			continue
		}
		def := out.Agents[index[name]]
		def.DeclaredModel = def.Model
		def.DeclaredThinkingEffort = def.ThinkingEffort
		def.DeclaredMaxTurns = def.MaxTurns
		copied := override
		def.Override = &copied
		if override.Model != nil {
			def.Model = *override.Model
			// A model change without an explicit effort resets to the model
			// default, mirroring the spawner's resolution rule. Keeping the
			// definition's effort could pair a new model with a level it does
			// not support.
			if *override.Model != def.DeclaredModel && override.ThinkingEffort == nil {
				def.ThinkingEffort = ""
			}
		}
		if override.ThinkingEffort != nil {
			def.ThinkingEffort = *override.ThinkingEffort
		}
		if override.MaxTurns != nil {
			def.MaxTurns = *override.MaxTurns
		}
		if override.Enabled != nil {
			def.Disabled = !*override.Enabled
		}
		out.Agents[index[name]] = def
	}
	return out, nil
}
