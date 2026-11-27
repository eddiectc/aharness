package engine

import (
	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/tools"
)

// validateContract validates a new session's prompt and tool contract.
func validateContract(spec Spec, toolsetVersion, promptVersion string) error {
	if spec.SystemPromptSet && spec.AppendPromptSet {
		return configError("SystemPrompt", "must not be combined with Instructions")
	}
	if spec.AppendPromptSet && len(spec.AppendPrompt) > 0 && promptVersion == "" {
		return configError("Instructions", "cannot be appended to an unknown prompt version")
	}
	if err := agent.ValidateCustomTools(spec.CustomTools); err != nil {
		return configError("Tools.Extra", "%v", err)
	}
	if err := validateToolsForVersion(spec.Grant, spec.RootGrant, spec.CustomTools, toolsetVersion, len(spec.Definitions) > 0); err != nil {
		return err
	}
	return nil
}

// validateToolsForVersion verifies that every granted name exists in the
// recorded built-in toolset or the custom set, and that delegate follows the
// delegation configuration.
func validateToolsForVersion(grant, root []string, custom []agent.CustomTool, toolsetVersion string, delegation bool) error {
	if err := agent.ValidateCustomTools(custom); err != nil {
		return configError("Tools.Extra", "%v", err)
	}
	if grant == nil && root == nil {
		return nil
	}
	schemas, err := tools.SchemasFor(toolsetVersion)
	if err != nil {
		return configError("ToolsetVersion", "unsupported toolset version %q", toolsetVersion)
	}
	known := make(map[string]bool, len(schemas)+len(custom))
	for _, schema := range schemas {
		known[schema.Name] = true
	}
	for _, tool := range custom {
		known[tool.Schema.Name] = true
	}
	for _, name := range grant {
		if !known[name] {
			return configError("Tools", "tool %q is not defined by toolset %s", name, toolsetVersion)
		}
		if name == "delegate" && !delegation {
			return configError("Tools", "delegate requires a DelegationConfig")
		}
	}
	if root == nil {
		return nil
	}
	session := known
	if grant != nil {
		session = make(map[string]bool, len(grant))
		for _, name := range grant {
			session[name] = true
		}
	}
	seen := make(map[string]bool, len(root))
	for _, name := range root {
		if seen[name] {
			return configError("RootGrant", "tool %q is duplicated", name)
		}
		seen[name] = true
		if !session[name] {
			return configError("RootGrant", "tool %q is not in the session grant", name)
		}
		if name == "delegate" && !delegation {
			return configError("RootGrant", "delegate requires a DelegationConfig")
		}
	}
	return nil
}

func sameGrant(one, two []string) bool {
	if one == nil || two == nil {
		return one == nil && two == nil
	}
	if len(one) != len(two) {
		return false
	}
	for i := range one {
		if one[i] != two[i] {
			return false
		}
	}
	return true
}

// sameGrantSet compares an explicit caller grant with the recorded semantic
// grant. A recorded nil grant means every tool of the recorded version.
func sameGrantSet(grant, recorded []string, toolsetVersion string, custom []agent.CustomTool) bool {
	if recorded == nil {
		schemas, err := tools.SchemasFor(toolsetVersion)
		if err != nil {
			return false
		}
		want := make(map[string]bool, len(schemas)+len(custom))
		for _, schema := range schemas {
			want[schema.Name] = true
		}
		for _, tool := range custom {
			want[tool.Schema.Name] = true
		}
		if len(grant) != len(want) {
			return false
		}
		for _, name := range grant {
			if !want[name] {
				return false
			}
		}
		return true
	}
	return sameGrant(grant, recorded)
}

func sameCustomTools(one, two []agent.CustomTool) bool {
	return agent.VerifyCustomTools(two, one) == nil
}

func cloneGrant(names []string) []string {
	if names == nil {
		return nil
	}
	return append([]string{}, names...)
}

func cloneCustomTools(custom []agent.CustomTool) []agent.CustomTool {
	if len(custom) == 0 {
		return nil
	}
	out := make([]agent.CustomTool, len(custom))
	for i, tool := range custom {
		out[i] = agent.CustomTool{
			Schema: model.ToolSchema{
				Name:        tool.Schema.Name,
				Description: tool.Schema.Description,
				Parameters:  append([]byte(nil), tool.Schema.Parameters...),
			},
			ConcurrencySafe: tool.ConcurrencySafe,
		}
	}
	return out
}
