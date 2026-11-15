package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Prompt versions. prompt-v0 is frozen for replay; prompt-v1 adds the
// delegation paragraph and the available-agents catalog and is the current
// version. A new session records the resolved prompt text, so the template is
// only consulted when a log predates that field.
const (
	PromptVersionV0 = "prompt-v0"
	PromptVersion   = "prompt-v1"
)

// Prompt caps. They bound the recorded system prompt and the catalog rendered
// into it.
const (
	maxSystemPromptBytes = 32 << 10
	maxAgentCatalogBytes = 4 << 10
)

// harnessRules is the shared instruction body. It is prompt material: changing
// it requires a new prompt version.
const harnessRules = `- Tools are your only way to act on the workspace. Do not claim an action you did not take through a tool call.
- Search and read before editing. Keep changes minimal and focused on the task.
- Run the relevant tests after changing code.
- Commands run with the harness's privileges. The harness adds no sandbox and no approval step.
- Tool errors are data: read them, adapt, and try a different approach instead of escalating.
- Stop by replying without a tool call when the task is complete or when you are blocked.`

// promptV0Format is the frozen v0 system prompt.
const promptV0Format = `You are aharness, a coding harness working in the workspace %q.

` + harnessRules

// promptV1Delegation is the paragraph prompt-v1 adds to the shared rules.
const promptV1Delegation = `
- ` + "`delegate`" + ` starts an agent with no conversation history, so a delegation prompt must be self-contained: state the goal, the constraints, and what to report.`

// promptV1CatalogHeading introduces the available-agents catalog.
const promptV1CatalogHeading = `

Available agents:
`

// AgentInfo is one agent name and description rendered into the parent prompt.
type AgentInfo struct {
	Name        string
	Description string
}

// renderPrompt returns the system prompt for one recorded version. The
// catalog is appended to the parent's prompt only; a child's prompt is the
// agent body plus the environment block, recorded verbatim in SystemPrompt.
func renderPrompt(version, workspace string, agents []AgentInfo) (string, error) {
	switch version {
	case PromptVersionV0:
		return fmt.Sprintf(promptV0Format, workspace), nil
	case PromptVersion:
		text := fmt.Sprintf(promptV0Format, workspace) + promptV1Delegation
		if len(agents) > 0 {
			catalog, omitted := renderAgentCatalog(agents)
			text += promptV1CatalogHeading + catalog
			if omitted > 0 {
				text += fmt.Sprintf("[%d agents omitted]\n", omitted)
			}
		}
		return text, nil
	default:
		return "", fmt.Errorf("project: unsupported prompt version %q", version)
	}
}

// renderAgentCatalog renders one line per agent, capped at
// maxAgentCatalogBytes. It returns the rendered lines and the number of agents
// that did not fit.
func renderAgentCatalog(agents []AgentInfo) (string, int) {
	var b strings.Builder
	for i, agent := range agents {
		line := fmt.Sprintf("- %s: %s\n", cleanText(agent.Name), cleanText(agent.Description))
		if b.Len()+len(line) > maxAgentCatalogBytes {
			return b.String(), len(agents) - i
		}
		b.WriteString(line)
	}
	return b.String(), 0
}

// childSystemPrompt composes a child's recorded prompt: the environment block
// and the shared rules, then the agent's role body. A child gets no catalog
// and no delegation guidance because it cannot delegate.
func childSystemPrompt(def AgentDefinition, workspace string) string {
	return fmt.Sprintf("You are the aharness %q agent, working in the workspace %q.\n\n%s\n\n## Your role\n\n%s",
		def.Name, workspace, harnessRules, strings.TrimSpace(def.Prompt))
}

// systemPromptText returns the prompt a runner records and projects. A Config
// that carries an explicit SystemPrompt (a child session) uses it verbatim;
// otherwise the prompt is rendered from the configured version and catalog.
func systemPromptText(cfg Config) string {
	if cfg.SystemPrompt != "" {
		return capText(cfg.SystemPrompt, maxSystemPromptBytes)
	}
	text, err := renderPrompt(promptVersion(cfg), cfg.Workspace, cfg.Agents)
	if err != nil {
		// A Config carries a validated version; a render failure here would be
		// a programming error. Fall back to the frozen v0 prompt so a run still
		// records a usable system message.
		text = fmt.Sprintf(promptV0Format, cfg.Workspace)
	}
	return capText(text, maxSystemPromptBytes)
}

// subagentIntent derives the one-line roster intent from a task prompt: the
// first non-empty line, whitespace-collapsed, sanitized, and capped with an
// ellipsis.
func subagentIntent(task string) string {
	for line := range strings.SplitSeq(task, "\n") {
		collapsed := strings.Join(strings.Fields(displaySafe(line)), " ")
		if collapsed == "" {
			continue
		}
		if len(collapsed) <= maxReportIntentBytes {
			return collapsed
		}
		cut := maxReportIntentBytes
		for cut > 0 && !utf8.ValidString(collapsed[:cut]) {
			cut--
		}
		return collapsed[:cut] + "…"
	}
	return ""
}
