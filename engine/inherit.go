package engine

import (
	"fmt"

	"github.com/eddiectc/aharness/agent"
)

// resolveInherited applies the resume/fork inheritance matrix. It validates
// prompt and tool overrides before any new log exists. It returns the resolved
// spec, the effective prompt, and the host text to record as provenance.
func resolveInherited(spec Spec, state agent.State, fork bool) (Spec, string, string, error) {
	resolved := spec
	resolved.PromptVersion = state.PromptVersion
	resolved.ToolsetVersion = state.ToolsetVersion
	if spec.Agent == "" {
		resolved.Agent = state.Agent
	}
	explicitModel := spec.Model != ""
	if !explicitModel {
		resolved.Model = state.Model
		if resolved.Model == "" {
			resolved.Model = state.InitialModel
		}
		if resolved.Model == "" {
			return Spec{}, "", "", configError("Model", "session does not record a model; set a model")
		}
		resolved.Dialect = state.Dialect
		if resolved.Dialect == "" {
			resolved.Dialect = state.InitialDialect
		}
		resolved.BaseURL = state.BaseURL
		resolved.Limits = state.Limits
		if limitsSet(spec.Limits) {
			clamped, err := clampLimits(spec.Limits, state.Limits)
			if err != nil {
				return Spec{}, "", "", err
			}
			resolved.Limits = clamped
		}
		if resolved.Limits.MaxContextTokens <= 0 {
			return Spec{}, "", "", configError("Limits", "session does not record a context limit; supply an explicit model")
		}
		resolved.Pricing = state.Pricing
	}
	if spec.NewCompleter == nil {
		return Spec{}, "", "", configError("Model.Completer", "is required")
	}
	if explicitModel {
		if resolved.Dialect == "" {
			resolved.Dialect = state.Dialect
		}
		if resolved.BaseURL == "" {
			resolved.BaseURL = state.BaseURL
		}
		clamped, err := clampLimits(resolved.Limits, agent.Limits{})
		if err != nil {
			return Spec{}, "", "", err
		}
		resolved.Limits = clamped
	}
	// Thinking effort: nil inherits (or clears on an explicit model switch),
	// "" clears, a non-empty value sets.
	switch {
	case spec.ThinkingEffort == nil && explicitModel:
		resolved.ThinkingEffort = new("")
	case spec.ThinkingEffort == nil:
		resolved.ThinkingEffort = new(state.ThinkingEffort)
	case *spec.ThinkingEffort == "":
		resolved.ThinkingEffort = new("")
	default:
		resolved.ThinkingEffort = new(*spec.ThinkingEffort)
	}
	// Prompt: resume inherits; fork applies explicit overrides.
	inheritedPrompt := state.SystemPrompt
	if inheritedPrompt == "" {
		rendered, err := agent.RenderSystemPrompt(state.PromptVersion, state.Workspace, spec.Identity, spec.Agents)
		if err != nil {
			return Spec{}, "", "", configError("SystemPrompt", "render recorded prompt: %v", err)
		}
		inheritedPrompt = rendered
	}
	prompt := inheritedPrompt
	if fork {
		switch {
		case spec.SystemPromptSet && spec.AppendPromptSet:
			return Spec{}, "", "", configError("SystemPrompt", "must not be combined with Instructions")
		case spec.SystemPromptSet:
			prompt = agent.CapSystemPrompt(spec.SystemPrompt)
		case spec.AppendPromptSet:
			base := state.SystemPrompt
			if base == "" {
				rendered, err := agent.RenderSystemPrompt(state.PromptVersion, state.Workspace, resolved.Identity, spec.Agents)
				if err != nil {
					return Spec{}, "", "", configError("SystemPrompt", "render prompt: %v", err)
				}
				base = rendered
			}
			prompt = agent.CapSystemPrompt(base + "\n\n" + spec.AppendPrompt)
		}
	}
	// Appended text is provenance, never a replay key. Resume carries the
	// recorded value. A fork with an explicit prompt records none: the explicit
	// prompt is the host's whole contract. A fork that keeps the inherited
	// prompt carries the recorded value, extended by its own Instructions.
	appended := state.AppendedPrompt
	if fork {
		switch {
		case spec.SystemPromptSet:
			appended = ""
		case spec.AppendPromptSet:
			if appended == "" {
				appended = spec.AppendPrompt
			} else {
				appended += "\n\n" + spec.AppendPrompt
			}
		}
	}
	// Prompt tag: recorded attribution, never a replay key. Resume keeps the
	// recorded tag and rejects a different explicit one. Fork prefers an
	// explicit tag and otherwise inherits only when the prompt is unchanged; a
	// changed prompt with an inherited tag would misattribute.
	switch {
	case !fork:
		if spec.PromptTag != "" && spec.PromptTag != state.PromptTag {
			if state.PromptTag == "" {
				return Spec{}, "", "", configError("PromptTag", "the session records no prompt tag; fork to set %q", spec.PromptTag)
			}
			return Spec{}, "", "", configError("PromptTag", "prompt tag %q is recorded; fork to change it", state.PromptTag)
		}
		resolved.PromptTag = state.PromptTag
	case spec.PromptTag != "":
		resolved.PromptTag = spec.PromptTag
	case prompt == inheritedPrompt:
		resolved.PromptTag = state.PromptTag
	default:
		resolved.PromptTag = ""
	}
	resolved.SystemPrompt = prompt
	resolved.SystemPromptSet = false
	resolved.AppendPrompt = ""
	resolved.AppendPromptSet = false
	// Tools: an explicit set is applied on fork and matched on resume. A nil
	// set inherits the recorded contract.
	recordedCustom := state.CustomTools
	if spec.CustomToolsSet {
		if err := agent.VerifyCustomTools(recordedCustom, spec.CustomTools); err != nil {
			if !fork {
				return Spec{}, "", "", err
			}
			if err := agent.ValidateCustomTools(spec.CustomTools); err != nil {
				return Spec{}, "", "", configError("Tools.Extra", "%v", err)
			}
		}
		resolved.CustomTools = cloneCustomTools(spec.CustomTools)
	} else {
		resolved.CustomTools = cloneCustomTools(recordedCustom)
	}
	if spec.GrantSet {
		if !fork && !sameGrantSet(spec.Grant, state.Tools, state.ToolsetVersion, recordedCustom) {
			return Spec{}, "", "", fmt.Errorf("%w: recorded grant %v, caller supplied %v", agent.ErrToolMismatch, state.Tools, spec.Grant)
		}
		resolved.Grant = cloneGrant(spec.Grant)
	} else {
		resolved.Grant = cloneGrant(state.Tools)
	}
	if !fork && !spec.CustomToolsSet && len(recordedCustom) > 0 {
		return Spec{}, "", "", fmt.Errorf("%w: session records %d custom tool(s) but none were supplied; supply matching Tools.Extra", agent.ErrToolMismatch, len(recordedCustom))
	}
	// Root grant: an explicit set replaces on fork and is matched on resume. A
	// nil set inherits the recorded root grant; nil in the log means the root
	// used the session grant.
	if spec.RootGrant != nil {
		if !fork {
			recorded := state.RootTools
			if recorded == nil {
				recorded = state.Tools
			}
			if !sameGrantSet(spec.RootGrant, recorded, state.ToolsetVersion, recordedCustom) {
				return Spec{}, "", "", fmt.Errorf("%w: recorded root grant %v, caller supplied %v", agent.ErrToolMismatch, state.RootTools, spec.RootGrant)
			}
		}
		resolved.RootGrant = cloneGrant(spec.RootGrant)
	} else {
		resolved.RootGrant = cloneGrant(state.RootTools)
	}
	if err := validateToolsForVersion(resolved.Grant, resolved.RootGrant, resolved.CustomTools, resolved.ToolsetVersion, len(spec.Definitions) > 0); err != nil {
		return Spec{}, "", "", err
	}
	return resolved, prompt, appended, nil
}

// limitsSet reports whether an explicit per-call limit was supplied. Only the
// honored fields count: SafetyMargin is always derived, so it cannot make a
// value explicit on its own.
func limitsSet(limits agent.Limits) bool {
	return limits.MaxContextTokens != 0 || limits.MaxOutputTokens != 0 ||
		limits.MaxInputTokens != 0 || limits.CompactionTarget != 0
}

func clampLimits(limits, recorded agent.Limits) (agent.Limits, error) {
	if limits.MaxOutputTokens < 0 {
		return agent.Limits{}, configError("Limits.MaxOutputTokens", "must not be negative")
	}
	context := limits.MaxContextTokens
	if context <= 0 {
		context = recorded.MaxContextTokens
	}
	if recorded.MaxContextTokens > 0 && context > recorded.MaxContextTokens {
		context = recorded.MaxContextTokens
	}
	if context <= 0 {
		return agent.Limits{}, configError("Limits.MaxContextTokens", "must be positive")
	}
	// The output budget is a host choice, not a model property, so an explicit
	// override replaces the recorded value instead of inheriting it: 0 clears
	// an inherited budget.
	output := limits.MaxOutputTokens
	inputLimit := limits.MaxInputTokens
	if inputLimit <= 0 {
		inputLimit = recorded.MaxInputTokens
	} else if recorded.MaxInputTokens > 0 && recorded.MaxInputTokens < inputLimit {
		inputLimit = recorded.MaxInputTokens
	}
	// A narrowed context makes the recorded or overridden input limit obsolete;
	// the remainder is the context minus the safety margin.
	if inputLimit > context {
		inputLimit = context
	}
	derived, err := agent.NewLimits(context, output, inputLimit)
	if err != nil {
		return agent.Limits{}, configError("Limits", "%v", err)
	}
	target := derived.CompactionTarget
	if limits.CompactionTarget > 0 && limits.CompactionTarget < target {
		target = limits.CompactionTarget
	}
	derived.CompactionTarget = target
	return derived, nil
}

func effortValue(effort *string, fallback string) string {
	if effort == nil {
		return fallback
	}
	return *effort
}
