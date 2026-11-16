package agent

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Prompt versions. prompt-v0 and prompt-v1 are frozen for replay; prompt-v2
// adds usage guidance for the todo and delegate tools and is the current
// version. A new session records the resolved prompt text, so the template is
// only consulted when a log predates that field.
const (
	PromptVersionV0 = "prompt-v0"
	PromptVersionV1 = "prompt-v1"
	PromptVersion   = "prompt-v2"
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

// promptV1Delegation is the paragraph prompt-v1 adds to the shared rules. It
// is frozen with v1; prompt-v2 carries its own guidance instead.
const promptV1Delegation = `
- ` + "`delegate`" + ` starts an agent with no conversation history, so a delegation prompt must be self-contained: state the goal, the constraints, and what to report.`

// promptV2Todo is the todo guidance prompt-v2 adds to a parent prompt and to a
// child prompt whose grant includes the tool.
const promptV2Todo = `
- ` + "`todo`" + ` tracks multi-step work. Write the checklist before the first step, keep exactly one item ` + "`in_progress`" + `, and mark items ` + "`completed`" + ` as you finish them. Skip it for a single-step task.`

// promptV2Delegation is the parent-only delegation guidance that replaces the
// v1 delegation paragraph.
const promptV2Delegation = `
- ` + "`delegate`" + ` runs independent work in a child agent. Delegate a self-contained investigation, review, or verification when it would otherwise flood this conversation with tool output, or when independent read-only tasks can run in parallel. State the goal, the constraints, and what to report: the child starts with no conversation history. Do not delegate work that depends on context the child will not have.`

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
	var text string
	switch version {
	case PromptVersionV0:
		return fmt.Sprintf(promptV0Format, workspace), nil
	case PromptVersionV1:
		text = fmt.Sprintf(promptV0Format, workspace) + promptV1Delegation
	case PromptVersion:
		text = fmt.Sprintf(promptV0Format, workspace) + promptV2Todo + promptV2Delegation
	default:
		return "", fmt.Errorf("project: unsupported prompt version %q", version)
	}
	if len(agents) > 0 {
		catalog, omitted := renderAgentCatalog(agents)
		text += promptV1CatalogHeading + catalog
		if omitted > 0 {
			text += fmt.Sprintf("[%d agents omitted]\n", omitted)
		}
	}
	return text, nil
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
// and the shared rules, then the agent's role body. A child gets no catalog and
// no delegation guidance; it cannot delegate. A prompt-v2 child whose grant
// includes todo also gets the todo guidance, because most child grants do not.
func childSystemPrompt(def AgentDefinition, workspace, version string) string {
	rules := harnessRules
	if version == PromptVersion && slices.Contains(def.Tools, "todo") {
		rules += promptV2Todo
	}
	return fmt.Sprintf("You are the aharness %q agent, working in the workspace %q.\n\n%s\n\n## Your role\n\n%s",
		def.Name, workspace, rules, strings.TrimSpace(def.Prompt))
}

// systemPromptText returns the prompt a runner records and projects. A Config
// that carries an explicit SystemPrompt (a child session) uses it verbatim;
// otherwise the prompt is rendered from the configured version and catalog.
func systemPromptText(cfg Config) string {
	text := cfg.SystemPrompt
	if text == "" {
		rendered, err := renderPrompt(promptVersion(cfg), cfg.Workspace, cfg.Agents)
		if err != nil {
			// A Config carries a validated version; a render failure here would be
			// a programming error. Fall back to the frozen v0 prompt so a run still
			// records a usable system message.
			rendered = fmt.Sprintf(promptV0Format, cfg.Workspace)
		}
		text = rendered
	}
	if cfg.AppendSystemPrompt != "" {
		text += "\n\n" + cfg.AppendSystemPrompt
	}
	return capText(text, maxSystemPromptBytes)
}

// RenderSystemPrompt renders the harness prompt for a version and workspace and
// applies the recorded prompt cap. A fork uses it to materialize a new
// contract before the new log is created.
func RenderSystemPrompt(version, workspace string, agents []AgentInfo) (string, error) {
	text, err := renderPrompt(version, workspace, agents)
	if err != nil {
		return "", err
	}
	return capText(text, maxSystemPromptBytes), nil
}

// CapSystemPrompt applies the same cap the session log uses.
func CapSystemPrompt(text string) string {
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
