package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/eddiectc/aharness/tools"
)

// Prompt versions. prompt-v0, prompt-v1, and prompt-v2 are frozen for replay.
// prompt-v3 is the current version: it adds project instructions, a
// verified-vs-unverified reporting rule, a refuse/flag rule, and the
// independence criterion for delegation. A new session records the resolved
// prompt text, so the template is only consulted when a log predates that
// field.
const (
	PromptVersionV0 = "prompt-v0"
	PromptVersionV1 = "prompt-v1"
	PromptVersionV2 = "prompt-v2"
	PromptVersion   = "prompt-v3"
)

// Prompt caps. They bound the recorded system prompt and the catalog rendered
// into it.
const (
	maxSystemPromptBytes = 32 << 10
	maxAgentCatalogBytes = 4 << 10
)

// harnessRulesFrozen is the shared instruction body for prompt-v0 through
// prompt-v2. It is frozen prompt material: changing it would change a
// replayable version.
const harnessRulesFrozen = `- Tools are your only way to act on the workspace. Do not claim an action you did not take through a tool call.
- Search and read before editing. Keep changes minimal and focused on the task.
- Run the relevant tests after changing code.
- Commands run with the harness's privileges. The harness adds no sandbox and no approval step.
- Tool errors are data: read them, adapt, and try a different approach instead of escalating.
- Stop by replying without a tool call when the task is complete or when you are blocked.`

// defaultIdentity is the product name used when a host supplies none.
const defaultIdentity = "aharness"

// The v3 rules are assembled from per-line constants so LoopRules and the
// default body cannot drift: the invariant lines appear in both, and the
// coding-policy lines appear only in the default body.
const (
	ruleToolOnly   = `- Tools are your only way to act on the workspace. Report an action as done only when a tool call performed it.`
	ruleOneResult  = `- Every tool call receives exactly one result.`
	rulePrivileges = `- Commands run with the harness's privileges. The harness adds no sandbox and no approval step.`
	ruleToolErrors = `- Tool errors are data: read them, adapt, and try a different approach instead of escalating.`
	ruleStop       = `- Stop by replying without a tool call when the task is complete or when you are blocked; when blocked, say what blocks you and what remains.`
)

// loopRules is the version-independent instruction set the loop depends on.
// Coding policy and coordination guidance deliberately do not live here so a
// host that replaces the default body can keep only the invariants.
const loopRules = ruleToolOnly + "\n" + ruleOneResult + "\n" + rulePrivileges + "\n" + ruleToolErrors + "\n" + ruleStop

// codingPolicyV3 is the replaceable policy part of the v3 default body. It is
// not part of LoopRules.
const codingPolicyV3 = `- Search and read before editing. Keep changes minimal and focused on the task.
- Run the relevant tests before reporting the change done, and say plainly what you could not verify.
- If the workspace carries project instructions (AGENTS.md or the file it names), read them before acting on the code.
- If a task is infeasible as stated, or a check is wrong, say so rather than working around it.`

// harnessRulesV3 is the current shared instruction body: the invariant lines
// interleaved with the coding-policy lines in their recorded order.
const harnessRulesV3 = ruleToolOnly + "\n" + ruleOneResult + "\n" + codingPolicyV3 + "\n" + rulePrivileges + "\n" + ruleToolErrors + "\n" + ruleStop

// LoopRules returns the instruction lines the loop depends on: a tool call is
// the only way to act, every call receives exactly one result, commands run
// with the harness's privileges, tool errors are data, and a reply without a
// call ends the run. A replacement system prompt must keep them.
func LoopRules() string { return loopRules }

// promptV1Delegation is the paragraph prompt-v1 adds to the shared rules. It
// is frozen with v1; prompt-v2 carries its own guidance instead.
const promptV1Delegation = `
- ` + "`delegate`" + ` starts an agent with no conversation history, so a delegation prompt must be self-contained: state the goal, the constraints, and what to report.`

// promptV2Todo is the todo guidance prompt-v2 adds to a parent prompt and to a
// child prompt whose grant includes the tool. prompt-v3 reuses the text
// unchanged.
const promptV2Todo = `
- ` + "`todo`" + ` tracks multi-step work. Write the checklist before the first step, keep exactly one item ` + "`in_progress`" + `, and mark items ` + "`completed`" + ` as you finish them. Skip it for a single-step task.`

// promptV2Delegation is the parent-only delegation guidance for prompt-v2,
// frozen with that version.
const promptV2Delegation = `
- ` + "`delegate`" + ` runs independent work in a child agent. Delegate a self-contained investigation, review, or verification when it would otherwise flood this conversation with tool output, or when independent read-only tasks can run in parallel. State the goal, the constraints, and what to report: the child starts with no conversation history. Do not delegate work that depends on context the child will not have.`

// promptV3Delegation is the parent-only delegation guidance for prompt-v3. It
// adds the independence criterion: fresh context is valuable when the
// conclusion must be reached without the context that produced the work, not
// only when output volume or parallelism justifies a child.
const promptV3Delegation = `
- ` + "`delegate`" + ` runs independent work in a child agent. Delegate a self-contained investigation, review, or verification when the conclusion must be reached without the context that produced the work, or when it would otherwise flood this conversation with tool output, or when independent read-only tasks can run in parallel. State the goal, the constraints, and what to report: the child starts with no conversation history. Do not delegate work that depends on context the child will not have.`

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
func renderPrompt(version, workspace, identity string, agents []AgentInfo) (string, error) {
	body, err := DefaultPromptBody(version, agents)
	if err != nil {
		return "", err
	}
	return identityLine(identity, workspace) + "\n\n" + body, nil
}

// identityLine renders the parent identity line. Empty identity selects the
// default product name. Validation belongs to the engine; this function only
// formats the value it is given.
func identityLine(identity, workspace string) string {
	return fmt.Sprintf("You are %s, a coding harness working in the workspace %q.", identityOrDefault(identity), workspace)
}

func identityOrDefault(identity string) string {
	if identity == "" {
		return defaultIdentity
	}
	return identity
}

// DefaultPromptBody returns the SDK's default body for one prompt version: the
// shared rules, the version's coordination guidance, and the catalog when
// agents are supplied. It is the default parent prompt without the identity
// line and without the recorded-prompt cap. prompt-v0 is frozen: it ignores
// agents and never carried a catalog.
func DefaultPromptBody(version string, agents []AgentInfo) (string, error) {
	var text string
	switch version {
	case PromptVersionV0:
		return harnessRulesFrozen, nil
	case PromptVersionV1:
		text = harnessRulesFrozen + promptV1Delegation
	case PromptVersionV2:
		text = harnessRulesFrozen + promptV2Todo + promptV2Delegation
	case PromptVersion:
		text = harnessRulesV3 + promptV2Todo + promptV3Delegation
	default:
		return "", fmt.Errorf("project: unsupported prompt version %q", version)
	}
	if len(agents) > 0 {
		text += promptV1CatalogHeading + AgentCatalogText(agents)
	}
	return text, nil
}

// AgentCatalogText renders one "- name: description" line per agent under the
// catalog cap, including the "[N agents omitted]" marker when the set does not
// fit. It returns the text only; the caller supplies any heading. It returns
// "" for no agents.
func AgentCatalogText(agents []AgentInfo) string {
	var b strings.Builder
	for i, agent := range agents {
		line := fmt.Sprintf("- %s: %s\n", cleanText(agent.Name), cleanText(agent.Description))
		if b.Len()+len(line) > maxAgentCatalogBytes {
			fmt.Fprintf(&b, "[%d agents omitted]\n", len(agents)-i)
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// childSystemPrompt composes a child's recorded prompt: the environment block
// and the shared rules, then the agent's role body. A child gets no catalog and
// no delegation guidance; it cannot delegate. A prompt-v2 or prompt-v3 child
// whose grant includes todo also gets the todo guidance, because most child
// grants do not; v0 and v1 children keep their frozen rules.
func childSystemPrompt(def AgentDefinition, workspace, identity, version string) string {
	var rules string
	switch version {
	case PromptVersionV0, PromptVersionV1:
		rules = harnessRulesFrozen
	case PromptVersionV2:
		rules = harnessRulesFrozen
		if slices.Contains(def.Tools, "todo") {
			rules += promptV2Todo
		}
	case PromptVersion:
		rules = harnessRulesV3
		if slices.Contains(def.Tools, "todo") {
			rules += promptV2Todo
		}
	default:
		// A Config carries a validated version; keep the frozen contract for
		// an unknown one rather than silently dropping the rules.
		rules = harnessRulesFrozen
	}
	return fmt.Sprintf("You are the %s %q agent, working in the workspace %q.\n\n%s\n\n## Your role\n\n%s",
		identityOrDefault(identity), def.Name, workspace, rules, strings.TrimSpace(def.Prompt))
}

// systemPromptText returns the prompt a runner records and projects. A Config
// that carries an explicit SystemPrompt (a child session) uses it verbatim;
// otherwise the prompt is rendered from the configured version and catalog.
func systemPromptText(cfg Config) string {
	text := cfg.SystemPrompt
	if text == "" {
		rendered, err := renderPrompt(promptVersion(cfg), cfg.Workspace, cfg.Identity, cfg.Agents)
		if err != nil {
			// A Config carries a validated version; a render failure here would be
			// a programming error. Fall back to the frozen v0 rules so a run still
			// records a usable system message.
			rendered = identityLine(cfg.Identity, cfg.Workspace) + "\n\n" + harnessRulesFrozen
		}
		text = rendered
	}
	if cfg.AppendSystemPrompt != "" {
		text += "\n\n" + cfg.AppendSystemPrompt
	}
	return capText(text, maxSystemPromptBytes)
}

// RenderSystemPrompt renders the harness prompt for a version, workspace, and
// host identity and applies the recorded prompt cap. A fork uses it to
// materialize a new contract before the new log is created. Empty identity
// means aharness.
func RenderSystemPrompt(version, workspace, identity string, agents []AgentInfo) (string, error) {
	text, err := renderPrompt(version, workspace, identity, agents)
	if err != nil {
		return "", err
	}
	return capText(text, maxSystemPromptBytes), nil
}

// CapSystemPrompt applies the same cap the session log uses.
func CapSystemPrompt(text string) string {
	return capText(text, maxSystemPromptBytes)
}

// subagentIntent derives the one-line roster intent from a task prompt. It is
// the same derivation the tools package exports as IntentLine.
func subagentIntent(task string) string {
	return tools.IntentLine(task)
}
