# aharness subagent research

- **Date:** 2026-11-15
- **Status:** research complete; no code changed. Input to the
  [subagent build plan](../plans/subagent-build.md).
- **Snapshot:** repository `ef77b92`; harness surface `toolset-v1`,
  `prompt-v0`, session schema v2. External harness documentation read
  2026-11-15.
- **Scope:** delegation and multi-agent features in production coding
  harnesses, which pieces are load-bearing, and which fit aharness's
  single-loop contract.
- **Related:** [architecture](../architecture.md), [limits](../limits.md),
  [testing strategy](../testing-strategy.md),
  [bare-minimum harness research](agent-harness-minimum.md), and
  [agent tooling research](agent-tooling-research.md).

## 1. Summary

Six conclusions drive the plan.

1. **Delegation has a permanent context benefit that compaction cannot
   substitute.** Projection elides only tool results older than
   `retainedTurns = 4` (`internal/agent/context.go:18,156-175`), and only when
   the estimate exceeds the target. The most recent four turns of exploratory
   reads are therefore always resident, by construction. A child context is the
   only mechanism that keeps that material out of the parent's projection
   entirely.
2. **The mechanism is the same in every harness reviewed.** A
   markdown-or-TOML agent definition carrying name, description, and system
   prompt; a fresh context; a per-agent model and reasoning effort; a tool
   allowlist; and one bounded report back. Everything else is a differentiator
   layered on that core.
3. **Per-role model, effort, and prompt are the high-value axis,** not
   parallelism. All four harnesses define an explicit precedence chain for
   model and effort, and three document a "selecting a model resets the effort"
   rule that aharness must copy or it will send an unsupported pairing.
4. **Background execution, agent messaging, and live rosters are a later
   layer.** They need a parent that keeps working while a child runs. aharness
   starts exactly one run per submission
   (`internal/tui/app.go:126,821,849`) and dispatches tools synchronously
   (`internal/agent/runner.go:764`), so a blocking delegation already suspends
   the orchestrator. Messaging during that window has no author.
5. **A child needs observation more than it needs a turn cap.** Budgets in this
   harness are unlimited unless set, and a child inherits the parent's token
   ceiling and stuck detection. A fixed constant cannot tell a slow task from a
   stuck one, so the safeguard that matters is forwarded progress plus `Esc` —
   and withholding delegation entirely where no human is watching (§6.6).
6. **A blocking call with parallel fan-out inside it buys most of the
   benefit.** Codex's model — spawn several agents, wait for all, return one
   consolidated result — is a single tool call and needs no asynchronous run
   state, no durable completion event, and no new run origin.

Recommended order: blocking delegation with one generic agent; role
definitions with per-agent model, effort, tools, and prompt; parallel read-only
fan-out; then resume-based steering. Background, messaging, worktrees, and a
reviewer-on-delta subsystem wait for evidence.

## 2. Method

- Source audit of `internal/agent`, `internal/tools`, `internal/session`,
  `internal/tui`, and `cmd/aharness`, at the snapshot above.
- Primary-source review of the current subagent documentation for Claude Code,
  Codex, OMP (oh-my-pi), and OpenCode. Sources are in §9.
- Judgment rule: a feature is **essential** when all four harnesses implement
  it and each states a failure it prevents; **recommended** when three of four
  do; **differentiator** when one or two do and the cost is large.
- Feasibility is judged against aharness's existing contracts rather than
  against a greenfield design: one writer per session, one run per submission,
  append-only v2 log, versioned prompt and toolset for replay, and no
  built-in sandbox.

## 3. What the harnesses do

### 3.1 Mechanism comparison

| Dimension | Claude Code | Codex | OMP | OpenCode |
|---|---|---|---|---|
| Definition format | Markdown + YAML frontmatter | TOML, one file per agent | Markdown + frontmatter | Markdown + frontmatter, or JSON config |
| Scopes | project, user, managed, plugin, CLI | project, user | project, user, extension, bundled | project, user, config |
| Discovery precedence | nearest project, then user | project over built-in | project, user, extensions, Claude plugins, bundled; first name wins | config order, later scalars replace |
| Selection | `description` plus auto-delegation | `description`, or a direct request | `task` call with `agent` name | subagent tool, `description` shown to the model |
| Model field | alias, full ID, or `inherit` | `model` | role alias (`@review`), prioritized list, `:level` suffix | `provider/model#variant` |
| Effort field | `effort`, per-invocation parameter | `model_reasoning_effort` | `thinking-level`, `:level`, `task.maxEffort` clamp | variant |
| Tool control | `tools` allowlist, `disallowedTools` denylist | feature toggles | `tools` allowlist, `spawns` | ordered allow/ask/deny rules |
| Nesting | up to 3 layers, env-overridable | not documented | `maxRecursionDepth` defaults to 2 | permission deny on `subagent` |
| Concurrency | 20 by default, env-overridable | `max_concurrent_threads_per_session` | not documented as a cap | not documented |
| Context | fresh, own system prompt | fresh | fresh, own tool session | fresh child session |
| Results | final report, partial on failure | consolidated response after all agents | final text or schema-validated object | report when a child goes idle |
| Persistence | per-agent JSONL under the session | agent threads | per-agent JSONL plus revive | child session |

### 3.2 Claude Code

The reference implementation, and the most explicit about purpose. Its stated
benefits are: keep exploration and implementation out of the main conversation,
limit which tools a subagent can use, reuse configurations across projects,
specialize with focused prompts, and route work to cheaper models.

Load-bearing details:

- **Built-ins are files.** `Explore` (read-only, denies `Write`/`Edit`),
  `Plan`, and `General-purpose`. `Explore` and `Plan` also start without
  `CLAUDE.md` and without the git-status snapshot "to keep research fast and
  inexpensive" — evidence that inherited project context is a real cost for
  read-only roles.
- **A description budget.** Claude Code warns when the combined subagent
  descriptions pass **15,000 tokens**, and still loads every one. Descriptions
  are prompt material billed on every request.
- **Model precedence**, highest first: per-invocation parameter, definition
  `model`, `CLAUDE_CODE_SUBAGENT_MODEL`, parent model. Nested subagents default
  to the *spawning* subagent's model.
- **Effort:** `effort` in the definition, overridable per invocation,
  overridden in turn by `CLAUDE_CODE_EFFORT_LEVEL`. A subagent with no effort
  runs at the session level only when it is on the parent's model; on another
  model that level does not apply.
- **Frontmatter surface:** `description`, `tools`, `disallowedTools`, `model`,
  `permissionMode`, `mcpServers`, `hooks`, `maxTurns`, `autoCompactWindow`,
  `skills`, `initialPrompt`, `memory`, `effort`, `background`,
  `omitClaudeMd`, `isolation`.
- **Failure behavior:** a foreground subagent cut off by a rate limit or server
  error returns its partial output with a note that it did not finish; a
  `maxTurns` stop is marked partial and can be resumed.
- **Resume.** Each invocation is a new instance; a resumed subagent keeps its
  full history and can keep reading the prompt cache its first run warmed.
- **Output scanning.** Each final report is scanned before the parent reads it.
  The scan "never removes or rewords anything": it inserts a backslash into text
  imitating harness output (`<system-reminder>`, a line starting `Human:` or
  `Assistant:`) and prepends
  `[harness: subagent output matched instruction-shaped pattern(s)]`.
- **Background is the default** in an interactive session, and a background
  subagent's results "reach Claude as a completion notification in a later
  turn".

### 3.3 Codex

The framing is the clearest statement of the problem, and the smallest surface.

- **The motivation is context rot.** The documentation names context pollution
  and context rot, and recommends moving noisy work — exploration notes, test
  logs, stack traces, command output — off the main thread.
- **Guidance on safety:** "use parallel agents for read-heavy tasks such as
  exploration, tests, triage, and summarization. Be more careful with parallel
  write-heavy workflows, because agents editing code at once can create
  conflicts."
- **Orchestration is internal:** Codex spawns, routes follow-ups, waits, and
  closes threads; "when many agents are running, Codex waits until all
  requested results are available, then returns a consolidated response."
- **Built-ins:** `default` (general purpose), `worker` (implementation),
  `explorer` (read-heavy exploration). Custom agents are TOML files under
  `~/.codex/agents/` or `.codex/agents/`, requiring `name`, `description`, and
  `developer_instructions`.
- **Model and effort precedence:** explicit spawn value, then the `[agents]`
  default, then the parent. With the important exception: if a model is
  selected without an effort, the subagent uses *that model's* default effort.
- **Custom agents cannot widen authority.** They inherit permissions, provider,
  and MCP servers; a custom agent file "can't override those settings". It can
  only reduce capability, by disabling feature keys or skills.
- **Global caps:** `agents.enabled`,
  `agents.max_concurrent_threads_per_session`,
  `agents.default_subagent_model`,
  `agents.default_subagent_reasoning_effort`,
  `agents.interrupt_message`.
- **A named caveat:** custom instructions can *ask* an agent to avoid edits, but
  they "don't create a separate read-only sandbox."

### 3.4 OMP (oh-my-pi)

The most elaborate, and the most useful for distinguishing concepts the other
harnesses conflate.

The named pieces in the documentation read are not exactly "five predefined
subagents"; they are three different mechanisms:

| Mechanism | What it is | Examples |
|---|---|---|
| Bundled task agents | embedded markdown definitions | `scout`, `reviewer`, `security-reviewer`, generic `task`, `sonic` |
| Advisor | a separate reviewer subsystem, not a peer | `advisor` role, `WATCHDOG.md`, `WATCHDOG.yml` roster |
| Prompt mode | a keyword that injects an orchestration contract | `orchestrate`, `workflowz` |

Load-bearing details:

- **Definition shape:** required `name`, `description`, `systemPrompt`;
  optional `tools`, `spawns`, prioritized `model` list, `thinkingLevel`,
  `output`, `blocking`, `structured`, `autoloadSkills`, `readSummarize`,
  `prewalk`, `advisor`.
- **Model routing uses roles**, so routing changes without editing agent files:
  a frontmatter `model: "@review"` resolves through `modelRoles.review`. The
  default set has nine roles (`default`, `smol`, `slow`, `plan`, `commit`,
  `vision`, `task`, `advisor`, `tiny`).
- **Effort is clamped:** `task.maxEffort` bounds every spawn, and the ceiling is
  carried across retry-fallback model switches.
- **Precedence:** `task.agentModelOverrides[agent]`, then frontmatter `model`,
  then the parent's models; a per-call `model` is gated by
  `task.perCallModel` (`off`, `eval`, `on`).
- **Project configuration can only narrow.** Settings are reloaded before every
  spawn, "so a role written there mid-session would otherwise approve its own
  model". Project layers may lower `task.perCallModel`, disable agents, and
  narrow an allowlist; they never grant.
- **Recursion and spawn policy:** `maxRecursionDepth` defaults to 2; at the cap
  the child's `task` tool is removed and its spawn policy emptied. `spawns`
  accepts `*`, a CSV, or an array; a `PI_BLOCKED_AGENT` guard rejects
  self-recursion before discovery.
- **Plan mode narrows children** to `read`, `grep`, `glob`, `web_search`, clears
  their spawns, and prepends a plan-mode prompt.
- **Structured output** via a `yield` tool plus an `output` schema, read back by
  path with `agent://<id>/findings.0.path`.
- **Agent Hub** (`Alt+A`) is a live roster: status, model role, resolved model,
  task, cost, elapsed, requests, tool calls, tokens; open a transcript, type to
  steer, change that agent's model or effort session-only, `x` to kill, `r` to
  revive. The main agent is not listed.
- **The advisor is not an agent.** It reviews the primary's *transcript delta*,
  injects `<advisory severity="nit|concern|blocker">` notes, has an
  `immuneTurns` cooldown, an emission guard that suppresses content-free phrases
  and dedupes by severity, and is explicitly not messageable, revivable, or
  killable.
- **`prewalk`** starts a subagent on its resolved model and hands off to a cheap
  role at the first edit or write.

### 3.5 OpenCode

A compact, permission-centric variant that confirms the same core.

- Markdown agents with `description`, `mode` (`primary`, `subagent`, `all`),
  `model` with an optional variant, `system`, `permissions`, `steps`, `hidden`,
  `disabled`.
- "Subagents run with fresh context in their own child sessions. They always
  run in the background: the parent receives a report when a child goes idle."
- A subagent "uses its configured model, or inherits the parent session's model
  when none is configured."
- Permission rules use an ordered `action`/`resource`/`effect` list, with
  `subagent` as an action — so which agents may launch which agents is a
  permission rule, not a separate mechanism.

### 3.6 What actually converged

Across all four:

1. A declarative definition file with a name, a description the model reads,
   and a system prompt.
2. A fresh context that excludes the parent transcript.
3. Per-agent model, with inheritance as the default.
4. Per-agent reasoning effort, with an explicit resolution chain.
5. A tool allowlist, and a read-only tier that every reviewer uses.
6. One bounded result returned to the parent.
7. Built-in definitions that a user definition of the same name overrides.

Less consistently, but in three of four: partial output on failure, persisted
child transcripts, a nesting cap, and a concurrency cap.

Only one of the four treats the model-facing *description* budget as a finite
resource with a warning threshold, and only one separates a passive reviewer
from a delegatable agent. Both distinctions are worth keeping.

## 4. What aharness already has

### 4.1 Loop and dispatch

- `Runner.Run` is one run per `session.Store`, with `run_start`/`run_end`
  boundaries, one `final` per run, and typed `StopReason` values
  (`internal/agent/runner.go:131,939-989`;
  `internal/agent/types.go:113-146`).
- Tool calls are dispatched one at a time, in response order, through one
  registry (`internal/agent/runner.go:764-830`;
  `internal/tools/registry.go:56-67`). Each call is wrapped in
  `beginOperation(ctx)` (`runner.go:790`), which is the interactive interrupt
  seam.
- The registry owns the workspace, the command environment, the web clients,
  and per-run todo state (`internal/tools/registry.go:23-45`). A second child
  registry is therefore naturally isolated; nothing is shared but the
  `*tools.Workspace`.

### 4.2 Sessions, resume, and fork

- `session.Create`/`Open`/`OpenShared` take an exclusive or shared `flock`;
  one writer per log (`docs/architecture.md` §3.2).
- `Inspect(events)` reconstructs cumulative turns, usage, cost, the effective
  configuration, the latest thinking effort, and every unpaired call
  (`internal/agent/state.go:23-69`).
- Fork already supports a parent reference: `ParentRef{Session, Seq}` is
  recorded in the forked `session_start`
  (`internal/agent/types.go:94-99`; `runner.go:52-85`).
- Unpaired calls are repaired on resume with `code=canceled`
  (`internal/agent/runner.go:272-286`). This covers a tool call that never
  produced a result — it does not cover a *completed* tool call whose side
  effect (a child run) is still in flight, because a blocking call cannot leave
  one behind.

### 4.3 Steering and cancellation

- `Config.Steering` is a `chan string` consumed only at turn boundaries,
  capped at 64 KiB, recorded as `user_message` with `source=steer`
  (`internal/agent/types.go:77-79`; `runner.go:364-391`;
  `internal/agent/events.go:169,213`).
- The TUI creates one channel per run and fills it from the composer
  (`internal/tui/app.go:839,866`; `internal/tui/model.go:541-565`).
- Interrupt inside a run appends `interrupt` plus an `interrupt-hint` user
  message and continues without counting a turn
  (`internal/agent/runner.go:479`).
- Cancellation propagates through the operation context, so a child run derived
  from it is stopped and its process group killed with no new plumbing
  (`runner.go:790`; [limits](../limits.md) §4).

### 4.4 Projection and compaction

- `Project` is a pure function of events plus limits, and selects the prompt
  and tool schemas from the versions recorded in `session_start`
  (`internal/agent/context.go:43-57`).
- Elision applies only to tool results from turns older than `retainedTurns`
  (`context.go:18,156-175`); recent tool output is always resident.
- Nothing in the projection can shrink a *recent* result, so a 60 KiB read from
  the current turn is carried on every subsequent call until it ages out.

### 4.5 Display and TUI

- `Config.Display` is the single runner-to-UI path with a fixed event order
  (`internal/agent/display.go:1-130`; `docs/architecture.md` §3.3).
- `ToolCompleted` carries `Details json.RawMessage` — the same channel the todo
  widget uses to maintain state without parsing rendered output
  (`internal/agent/display.go:77-96`).
- Tool transcript items key on `callID` (`internal/tui/model.go:88,502-508`);
  thinking and tool blocks already fold (`docs/tui.md`).
- `-ui-json` maps display events to `tool_started`, `tool_finished`, and
  `notice` (`internal/tui/protocol.go:288-308`); additive payload fields have
  precedent without a protocol bump.

### 4.6 Versioning and replay

- `PromptVersion = "prompt-v0"`; `Project` rejects any other value
  (`internal/agent/types.go:20`; `context.go:51-53`).
- `tools.Version = "toolset-v1"`, with `v0Schemas` frozen and `SchemasFor`
  selecting by recorded version (`internal/tools/result.go:15`;
  `internal/tools/schemas.go:206-227`).
- Adding, removing, or editing a tool description is a toolset change and
  requires a frozen predecessor to keep old sessions resumable.
- The recorded version is copied through resume *and* fork
  (`cmd/aharness/models.go:319,423-424`), so a session created before a tool
  exists keeps its old schema set and can never call that tool. New capabilities
  reach new sessions only, unless an explicit upgrade path is added.

### 4.7 Constraints a subagent feature must respect

| Constraint | Consequence for this feature |
|---|---|
| One writer per session log | A child needs its own log; child events must not enter the parent's log |
| `Project` consumes one flat event stream | In-log nesting would leak into the parent's context unless every reader filtered |
| One run per submission, one `final`/`run_end` per run | A nested run needs its own boundaries; a child that outlives a run has no host |
| `tools` MUST NOT import `agent`; `agent` is the only orchestrator (`docs/architecture.md` §3.1) | A delegation tool cannot call a child loop from inside `internal/tools` |
| Prompt and toolset are versioned and replayable | Role prompts must be recorded as data, or each one becomes a version to maintain |
| No sandbox, no snapshots | Concurrent writers on one workspace are unsafe |
| `os.Root` containment, `.git` protection | A child inherits the same workspace policy; a worktree would need a second root |

## 5. Essential, recommended, and differentiator

### 5.1 Essential

| Feature | Why it is not optional |
|---|---|
| A definition file with name, description, system prompt | All four. `description` is the only input to model-side selection; requiring code changes to add a role rules out user-defined agents |
| A generic agent whose prompt the orchestrator writes | All four. It carries most of the real work; named specialists are the minority case |
| Fresh context | All four. It is the entire point |
| Per-agent model with inheritance | All four. Cost and capability routing |
| Per-agent reasoning effort with an explicit chain | All four. A model switch must reset effort to that model's default, or the harness sends an unsupported pairing |
| Tool allowlist with a read-only tier | All four. Every reviewer in every harness is read-only |
| One bounded result to the parent | All four. Unbounded return defeats the purpose |
| Per-agent usage accounting | Three of four surface it; OpenAI warns subagents cost more tokens. An unaccounted child is a budget bypass |

### 5.2 Recommended

| Feature | Evidence |
|---|---|
| Built-ins overridable by name | Codex states the rule; Claude Code, OMP, and OpenCode all allow a same-name override |
| Partial output on failure | Claude Code returns partial text and marks `maxTurns` stops partial; Codex returns each child's last output |
| Persisted child transcripts | Claude Code writes per-agent JSONL that survives parent compaction; OMP revives parked agents from artifacts |
| Depth cap | Claude Code 3, OMP 2, OpenCode a permission |
| Concurrency cap | Claude Code 20, Codex `max_concurrent_threads_per_session` |
| Read-only roles skip inherited project context | Claude Code's `omitClaudeMd` exists solely to make exploration cheap |

### 5.3 Differentiators, in descending value

| Feature | Cost |
|---|---|
| Structured results against a caller schema | New tool, schema validation, result addressing |
| Parallel fan-out inside one call | Goroutines and a concurrency cap; no loop change |
| Resume-based steering | Reuses session resume; no new channel |
| Live roster and per-agent steering in the TUI | New overlay, per-child display scope, channel routing |
| Background children | Async run origin, durable completion event, orphan repair, accounting races |
| Agent-to-agent messaging | Needs background children to have an addressee |
| Reviewer on the parent's delta | A second concurrent model per turn, an injection channel, dedupe and cooldown |
| Worktree isolation | A second workspace root and cleanup semantics; the prerequisite for parallel writers |
| Automatic model hand-off at first edit (`prewalk`) | Cheap-looking, but needs a live model switch mid-run |

### 5.4 Claims not to copy

- **"Approval or instructions make a child read-only."** Codex says plainly
  that instructions do not create a read-only sandbox; Claude Code enforces it
  with a tool deny list. aharness should enforce with the allowlist and say so.
- **Unbounded returned text.** A child that read forty files can return a
  forty-file summary; the cap, not the prompt, is the guarantee.
- **More agents as a default.** Claude Code's own guidance is to use the main
  conversation when phases share context, when the change is small, or when
  latency matters; a fresh context must re-gather what the parent already had.

## 6. Design tensions specific to this repository

### 6.1 Blocking versus background

A blocking call suspends the orchestrator inside `executeTools`. Three
consequences:

- The parent cannot produce steering text while the child runs; only the human
  can, and only if the TUI routes composer input to the child.
- Parallelism is still available *inside* the call: N children, wait for all,
  one result. Codex does exactly this and calls it orchestration.
- Nothing is orphaned on resume, because a completed tool call cannot leave a
  live child behind.

Background would require: a run origin that is not a submission
(`internal/tui/app.go:821`, `run_start` records a task), a durable completion
event because a report must survive resume, a repair rule for
spawned-but-unreported children (the current repair only pairs unpaired calls),
and accounting that tolerates usage arriving while the parent is idle.

### 6.2 Prompts as data versus a version matrix

`Project` rejects an unknown `prompt_version`, and `SchemasFor` rejects an
unknown `toolset_version`. Adding four role prompts as four versions creates a
matrix that must stay replayable forever, and it makes user-edited prompts
unrepresentable.

Recording the resolved system prompt in the child's `session_start` solves both:
replay is exact without a new constant per role, and editing an agent file does
not retroactively change an existing session. The cost is that the prompt is
session data and needs a cap and sanitization like any other recorded text.

The same argument applies to the per-agent tool allowlist: record names, filter
the versioned schema set, and validate as a subset at construction and at
dispatch.

### 6.3 Where the delegation tool can live

`internal/tools` cannot import `internal/agent`, and the plan of record says
`agent` is the only orchestrator. Two viable shapes:

| Shape | Consequence |
|---|---|
| The runner intercepts the tool name and runs the child itself | Orchestration stays in `agent`, but the registry is no longer the only dispatcher, and tool schemas have two sources |
| `tools` declares a one-method spawn interface; `cmd` wires an `agent` implementation | Registry stays the only dispatcher; `agent` remains the only orchestrator; the direction of dependency is unchanged |

The second is consistent with the existing consumer-interface pattern
(`completer`, `toolset`, `eventStore` in `internal/agent/types.go:187-201`).

### 6.4 Trust boundaries

Three new or enlarged surfaces:

1. **Agent definitions are prompts.** A repository-controlled agent file is
   repository-controlled prompt material. The reviewed harnesses disagree on
   this: OMP allows project files but forbids them from granting anything, and
   Codex forbids custom agents from overriding sandbox or approval settings.
   aharness has no project-level configuration surface at all today.
2. **Child output re-enters the parent's context.** A child may read files, web
   pages, and command output that the parent never saw. Claude Code's
   non-destructive marking pass is the model to follow; rewriting content
   would corrupt exact-match edits if the parent copies it.
3. **A child is a second consumer of the same workspace.** Read-only concurrency
   is safe; write concurrency is not, without worktrees.

### 6.5 Accounting

`max_turns` bounds a loop's own iterations; `max_total_tokens` bounds spend.
Child turns should not inflate the parent's turn counter, or the parent stops at
an unpredictable point. Child tokens should count against the token budget, or
delegation is a spend escape. Both need to be recorded so resume is exact, and
the tool result's `Details` is the existing durable channel for it.

### 6.6 How to bound a child

Prior art bounds the child with a *per-agent* turn budget, not a harness-wide
constant. Claude Code exposes `maxTurns` in the subagent definition and marks
the returned output as partial when it trips; OMP bounds recursion depth and
oversight request budgets per agent (`task.agentSoftRequestBudget`). Codex bounds
concurrency globally but does not publish a per-child turn cap.

The reason to prefer that shape: a fixed harness constant cannot separate a slow
legitimate task from a stuck one, and it is invisible to the operator. A budget
the operator or the role declares is both tunable and explicable.

Two bounds are not optional:

- **Tokens stay the parent's.** A child executes inside the parent's tool call,
  so a child allowed more than the parent's remaining spend makes
  `-max-total-tokens` meaningless.
- **The call must be interruptible.** Cancellation through the parent's
  operation context is what makes an unbounded child safe, and it only works if
  the human can see progress and decide. That makes progress forwarding, not a
  turn cap, the load-bearing safeguard for the runaway case.

`stuck` detection (five identical failures with the same arguments and result
code) is inherited by any child and is cheaper than a cap, but it only catches
repetition with no variation.

One asymmetry deserves an explicit rule: a non-interactive run has no observer
and no `Esc`, so delegation should be withheld there entirely rather than
bounded.

Observation is therefore part of the contract, not a nicety. Both harnesses that
show live children converge on the same fields: OMP's Agent Hub rows carry
status, identity, parent, model role and resolved model, age since last
activity, assigned task or current activity, cost, active time, request count,
tool-call count, and tokens, and its inspector adds the current tool and
arguments, the last intent, and context-window use. Claude Code shows the model
and effort per subagent row in `/tasks`. aharness already has the machinery —
`ToolCompleted.Details`, the persistent todo panel, and the overlay stack — so
the equivalent is one display command over live display events.

OMP also separates two surfaces that are easy to conflate: **Agent Hub** is the
live roster (`Alt+A`, legacy `Ctrl+S`), and **`/agents`** is a definition and
settings hub — "not the live roster" — managing discovered agents, enable/disable
state, model overrides, prewalk, and advisor selection. Keeping them apart is
right for aharness too: the roster is about children that exist only during a
run, while definitions outlive every run. A definitions surface in aharness can
start read-only, because the settings half needs a config writer, and the
repository has none today (`cmd/aharness/fileconfig.go` reads
`<state-dir>/config.json` and never writes it).

## 7. Recommendations

| Order | Work | Why in this position |
|---:|---|---|
| 1 | Blocking `delegate`, bundled generic agent, child session, per-agent model/effort/tools/prompt, usage rollup, folded TUI block, cancel | Delivers the context benefit and the role specialization in one increment |
| 2 | Resume by child session ID; human steering routed to a focused child; child transcript viewer | Reactive steering without messaging or concurrency |
| 3 | Parallel fan-out for read-only agents with a concurrency cap | Latency win inside the same blocking call |
| 4 | Optional, evidence-gated: worktrees for parallel writers, then background plus messaging | Both are prerequisites for each other and neither is a prerequisite for 1–3 |

Every item above is implementable against the current contracts. Items that are
not: a reviewer on the parent's delta needs a concurrent model per turn; a live
roster needs per-child display scope; automatic model hand-off needs a
mid-run model switch, which the runner does not support.

## 8. Open questions

- Does a per-role model actually pay for itself in this harness, or does the
  catalog's usable set make role assignment noise? Choose one strong and one
  cheap model per role set and measure.
- How much of the parent's task context must be restated in the delegation
  prompt before a child is productive? The child starts with no transcript;
  prompt quality is the dominant variable and it needs an observed baseline.
- Is a 16 KiB report cap correct, or does a reviewer's finding list routinely
  exceed it? Measure on real reviews before fixing the number.
- Do child sessions belong in the `/resume` picker? Leaning no: they are
  addressable by ID from the delegate result, and listing them pollutes the
  human view.
- Should the codebase-exploration role skip an inherited project instruction
  file? aharness has no such file today, so the question is deferred with it.
- Does read-only fan-out need a shared `todo` list or per-child lists? Per-child
  is simpler and already isolated by the registry.

## 9. Sources

Repository:

- `internal/agent/runner.go`, `context.go`, `display.go`, `state.go`,
  `events.go`, `types.go`
- `internal/tools/registry.go`, `result.go`, `schemas.go`
- `internal/session/event.go`, `store.go`
- `internal/tui/app.go`, `model.go`, `protocol.go`
- `docs/architecture.md` §3–§5, `docs/limits.md` §4, `docs/tui.md`

External (accessed 2026-11-15):

- Claude Code, [Create custom subagents](https://code.claude.com/docs/en/sub-agents):
  built-in agents, frontmatter reference, model and effort precedence, tool and
  permission control, nesting and concurrency limits, resume, output scanning,
  background behavior, and the 15,000-token description warning.
- Claude Code, [Subagents in the SDK](https://code.claude.com/docs/en/agent-sdk/subagents):
  `parent_tool_use_id` on subagent messages.
- OpenAI, [Subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents):
  context rot rationale, read-heavy versus write-heavy guidance, custom agent
  schema, `[agents]` settings, and the "no separate read-only sandbox"
  statement.
- OMP, [Task agent discovery and selection](https://github.com/can1357/oh-my-pi/blob/main/docs/task-agent-discovery.md):
  definition shape, role aliases, precedence, per-call model gating, spawn and
  recursion policy, plan-mode narrowing, structured output.
- OMP, [Agent Hub](https://github.com/can1357/oh-my-pi/blob/main/docs/agent-hub.md):
  live roster, per-agent usage, steering, revive and kill, persisted agents.
- OMP, [Advisor, WATCHDOG.md, and WATCHDOG.yml](https://github.com/can1357/oh-my-pi/blob/main/docs/advisor-watchdog.md):
  the reviewer subsystem, severity levels, emission guard, and the statement
  that an advisor is not a peer.
- OMP, [Magic keywords](https://github.com/can1357/oh-my-pi/blob/main/docs/magic-keywords.md):
  `orchestrate` and `workflowz` as per-turn prompt contracts.
- OpenCode, [Agents](https://opencode.ai/docs/agents/): agent modes, model
  variants, permission rules including `subagent`, and always-background
  subagents.
