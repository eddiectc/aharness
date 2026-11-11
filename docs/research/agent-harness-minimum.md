# Bare-Minimum Agent Harness

Research notes on the smallest useful runtime harness for an LLM agent, with primary sources.

- Date: 2026-11-09
- Scope: runtime harness, not evaluation/environment scaffolding
- Bottom line: four features make it an agent; four more make it safe to use on real work

## Summary

The irreducible harness is:

1. A model call with tool schemas.
2. A bounded agent loop.
3. A tool contract and dispatcher.
4. A transcript and context builder.

The minimum for real use adds:

5. A context budget.
6. Failure handling.
7. A durable event log.
8. A permission boundary: a sandbox, with approval only for what crosses it.

Everything else—multi-agent orchestration, RAG, MCP, planners, graph DSLs—is optional until the single loop is reliable.

## What "harness" means

The term is overloaded. In the runtime sense—the one Claude Code's docs use—the harness is "the layer around the model that provides the tools and manages the context the model sees." Anthropic's Managed Agents post decomposes that layer into:

- **Session**: an append-only log of everything that happened.
- **Harness**: the loop that calls the model and routes tool calls.
- **Sandbox**: the execution environment where tools run.

OpenAI's "harness engineering" post uses the other sense: the repository, CI, documentation, and observability scaffolding around a coding agent. That is a separate and much larger project.

Anthropic recommends starting with the LLM API directly rather than a framework, because the loop is small and frameworks obscure the prompts and tool results you need to debug. OpenAI describes the run as a loop with an exit condition. Simon Willison's working definition is "an LLM runs tools in a loop to achieve a goal."

## Tier 0 — the irreducible loop

| Feature | Minimum implementation | Why it is load-bearing |
|---|---|---|
| **Model call** | One function: system prompt + message list + tool schemas + `max_tokens` → assistant text and tool calls. Hide provider quirks behind it. | OpenAI names model, tools, and instructions as the three core components of an agent. Claude Code calls the surrounding layer the harness. |
| **Bounded loop** | `while turn < max_turns`: call; append the assistant message; if there are no tool calls, return; otherwise execute each call, append results, repeat. Also stop on token/cost/time budget. | This is the definition of an agent. OpenAI's SDK is exactly this loop, with `max_turns` raising when exceeded. Anthropic calls for stopping conditions such as a maximum number of iterations. |
| **Tool contract and dispatch** | A small registry: name, description, JSON Schema, handler. Validate arguments. Unknown tool or invalid args → a model-visible error, not a crash. Return a result for every call ID. | The model never executes anything; it emits a structured request and your code decides whether and how to run it. Every `tool_use` needs a matching `tool_result` before the next model call. |
| **Transcript and context builder** | An append-only event list (user, assistant, tool call, tool result). Rebuild the exact model input from it each turn. | The transcript is working memory. Keeping the durable log separate from the context window is what makes compaction and resume possible later. |

## Tier 1 — the minimum for real use

| Feature | Minimum implementation | Why it is load-bearing |
|---|---|---|
| **Context budget** | Cap input tokens; truncate each tool result; drop or summarize old tool results; compact near the limit. Prefer search/read on demand over preloading. | Context is finite and degrades as it grows; how much history the model sees changes task success. |
| **Failure handling** | Retry 429/5xx with backoff; per-tool timeout; tool exceptions become error results; malformed calls get a corrective message; cost ceiling; escalate after repeated failure. | Real tools fail and models make malformed calls. OpenAI lists errors and max turns as exit conditions; 12-factor says compact errors into context rather than dropping them. |
| **Durable event log** | Append-only JSONL or SQLite: event ID, timestamp, role, content, tool args/results, token usage, latency, error. The log is truth; the context window is a projection. | You cannot debug, replay, or resume what you did not record. Anthropic keeps the session outside the harness so a crashed harness can resume from the last event. Claude Code writes JSONL sessions and supports resume/fork. |
| **Permission boundary and sandbox** | One OS-enforced sandbox for all shell and file mutation (bwrap, container, or VM), built by trusted code and not configurable by the model: host read-only, workspace writable, no credentials, no egress. Tool allowlist and least privilege. Approval is the fallback for actions that leave the sandbox—network, pushes, host mounts—not for ordinary commands. | The model can be wrong or prompt-injected. OWASP's LLM06 names excessive functionality, permissions, and autonomy as the root causes; its mitigations are least privilege, complete mediation, and logging. Approval is one mediation mechanism; an enforced sandbox is the stronger one. |

## Evidence: interface and context are performance levers

SWE-agent's ablations on SWE-bench Lite with GPT-4 Turbo:

| Change | Resolve rate |
|---|---|
| Shell-only agent → custom agent-computer interface | 11.0% → 18.0% |
| Full history → last five observations at full detail | 15.0% → 18.0% |
| Full file → 100-line file window | 12.7% → 18.0% |
| No linting → linting on edit | 15.0% → 18.0% |

Anthropic caps Claude Code tool responses at 25,000 tokens by default and recommends pagination, filtering, and truncation. The absolute numbers are dated; the direction is the point.

For safety, a coding harness is the hard case: a shell is the point, so do not remove it—isolate it. Anthropic's Managed Agents keeps credentials out of the sandbox entirely and wires Git tokens into the remote rather than the agent's environment.

## Approval vs. isolation

Approval and isolation are substitutes for the same action, not layers: both mediate the same threat—the model acting on a bad plan or a prompt injection—and stacking them buys prompts, not safety.

- **Approval covers only what escapes the sandbox.** Codex's `on-request` policy runs anything the sandbox allows without asking, and prompts only to "edit files outside the workspace or to run commands that require network access." Claude Code's auto-allow mode does the same for sandboxed Bash because the OS enforces the limit. If every `run_command` and write takes one fixed sandboxed path, neither needs per-action approval.
- **Isolation covers what approval cannot.** An approval prompt is a human decision made from a one-line summary the model wrote, and it degrades to a rubber stamp under load. Adversarial repository content can steer an agent into requesting a plausible but broad allow-rule, and the approved rule then blinds the harness: researchers elicited such rules in 58% of experimental instances, with saved rules covering 56.9% of category-specific capability tests, and 47 of 58 eligible instances (81%) reaching verified, silent goal completion once the rule was approved. Prompts are a review surface, not a boundary.
- **bwrap is only as strong as the mount table.** Trusted code, not the model, builds the invocation: `--ro-bind` the host, tmpfs or overlay for scratch, `--bind` only the workspace, `--unshare-all` (network included), `--die-with-parent`, `--new-session`, and capabilities left at their none-by-default. bwrap is an unprivileged user-namespace tool, not a VM; it contains reach, not kernel bugs.
- **The workspace is still the user's.** A writable bind mount means `rm -rf` inside the sandbox still deletes real files—bwrap isolates the system, not the repo. The fix is recoverability, not consent: a git worktree or disposable checkout, `.git` mounted read-only (Codex protects it by default), and a diff to review.
- **Gate the escapes, not the commands.** Egress, credentials, and effects outside the sandbox—`git push`, publish, deploy, send, spend—are the actions that still need approval or their own tool with its own policy.

## Tier 2 — add when the loop is boring

- **Streaming and interrupt.** Users need to stop a bad tool call and steer. Claude Code treats interrupt and queued steering as part of the loop; OpenAI's SDK exposes `run_streamed`. This is UX, not correctness.
- **Session continuity.** Replay from the event log, fork sessions, and snapshot files before edits. Claude Code does this with JSONL plus checkpoints. Anthropic's long-running harness adds a progress file and git history so a fresh context can pick up. This is how you survive context resets.
- **Metrics and evals.** Turns, tokens/cost, tool error rate, wall-clock, and a small set of realistic tasks with verifiable outcomes. Both Anthropic and OpenAI make evals the basis for improving prompts and tools; without them you are guessing.
- **Hooks and lifecycle.** Pre/post model, pre/post tool, session start/end. Useful for policy and logging; the Claude Agent SDK exposes them. Not v0.

## Tier 3 — do not build yet

- **Multi-agent and subagents.** OpenAI says maximize a single agent first; Anthropic says add complexity only when it demonstrably improves outcomes. Subagents are a context-isolation technique for long tasks, not a v0 feature.
- **RAG and vector databases.** Claude Code uses just-in-time retrieval (`glob`/`grep`/read) plus a small `CLAUDE.md`, not embeddings. Add retrieval only if your domain has no navigable structure.
- **MCP and plugin systems.** A tool transport. Your dispatcher is the requirement; MCP is an adapter.
- **Graph DSLs and declarative workflows.** The loop is the control flow. OpenAI notes declarative graphs get cumbersome as workflows grow dynamic.
- **Custom UI and fine-tuning.** Not harness features; solve the loop first.

One more reason to stay small: Anthropic's harness-design post notes that every harness component encodes an assumption about what the model cannot do, and those assumptions go stale as models improve. Context resets helped Sonnet 4.5 but became dead weight on Opus 4.5. Build the smallest loop, measure, then add scaffolding.

## Implementation gotchas

1. **Pair every call with a result.** If a tool crashes or times out, return an error result with the same ID. A missing result makes the next model request invalid.
2. **Handle parallel calls.** A single assistant turn can contain several tool calls; execute all and return all results, or disable parallel tool use in v1.
3. **Do not append blindly to the context.** The log is append-only; the model input is a curated projection. Rebuild it each turn and apply the budget.
4. **Tool errors are data.** Return a short, actionable error string to the model; do not throw out of the loop. SWE-agent's linting feedback is a good model.
5. **Sandbox the hands, then stop asking.** If the harness can run shell commands, assume prompt injection. Put every command on one fixed OS-enforced path (bwrap, container, or VM) with the host read-only, no credentials, and no egress, and gate only what leaves it. Per-command approval inside a sandbox is theatre, and it trains the user to approve the next request.

## A reasonable v0

For a coding harness:

- One loop.
- Four tools: `read_file`, `edit_file`, `search`, `run_command`.
- A JSONL event log.
- Max turns plus a token/cost ceiling.
- `run_command` and all writes on one fixed bwrap/container path: host read-only, workspace writable, no credentials, no network.
- Approval only for escapes—egress, `git push`, anything outside the sandbox.
- A disposable worktree and a read-only `.git` (or commit outside the sandbox), so in-sandbox mistakes are recoverable without a prompt.

That is enough to do real work. Add `ask_user` or `finish` only if you need structured clarification or a structured stop. Then write 10–20 realistic eval tasks and read the raw traces; the traces will tell you which tool descriptions and context policies to fix. Do not add a second agent until the first one is reliable.

The concrete build plan is in [`v0-build.md`](../plans/v0-build.md).

## Sources

- [Claude Code — How Claude Code works](https://code.claude.com/docs/en/how-claude-code-works) — harness definition, agentic loop, tools, permissions, sessions
- [Claude Code — Sandboxing](https://code.claude.com/docs/en/sandboxing) — OS-enforced Bash sandbox, bubblewrap on Linux, auto-allow without prompts
- [Anthropic — Scaling Managed Agents](https://www.anthropic.com/research/managed-agents) — session/harness/sandbox, crash recovery, security boundary
- [Anthropic — Building effective agents](https://www.anthropic.com/research/building-effective-agents) — loop, stopping conditions, start-simple guidance, ACI
- [Anthropic — Effective context engineering for AI agents](https://www.anthropic.com/research/effective-context-engineering-for-ai-agents) — context budget, compaction, just-in-time retrieval
- [Anthropic — Writing effective tools for agents](https://www.anthropic.com/research/writing-tools-for-agents) — tool design, truncation, actionable errors, evals
- [Anthropic — Effective harnesses for long-running agents](https://www.anthropic.com/research/effective-harnesses-for-long-running-agents) — progress files, multi-session continuity
- [Anthropic — Harness design for long-running apps](https://www.anthropic.com/research/harness-design-long-running-apps) — harness assumptions go stale, evaluator patterns
- [OpenAI — A practical guide to building agents](https://cdn.openai.com/business-guides-and-resources/a-practical-guide-to-building-agents.pdf) — model/tools/instructions, run loop, guardrails, human intervention
- [OpenAI Agents SDK — Running agents](https://openai.github.io/openai-agents-python/running_agents/) — exact loop, `max_turns`, streaming, tracing
- [OpenAI — Codex agent approvals & security](https://learn.chatgpt.com/docs/agent-approvals-security) — sandbox modes, `on-request` approvals, workspace-write, network off, `.git` protection
- [bubblewrap — `bwrap(1)`](https://man.archlinux.org/man/bwrap.1.en) — namespaces, bind mounts, capability dropping, `--die-with-parent`
- [SWE-agent paper](https://arxiv.org/abs/2405.15793) — agent-computer interface evidence, history processing, linting
- [From Approval to Abuse](https://arxiv.org/abs/2610.22987) — approval fatigue and allow-rule privilege escalation in Claude Code and Codex CLI
- [OWASP LLM06: Excessive Agency](https://genai.owasp.org/llmrisk/llm062025-excessive-agency/) — least privilege, approval, complete mediation, logging
- [12-Factor Agents](https://github.com/humanlayer/12-factor-agents) — own your prompts and context, unify execution state, compact errors
- [Simon Willison — "Agent" definition](https://simonwillison.net/2025/Sep/18/agents/) — tools in a bounded loop
