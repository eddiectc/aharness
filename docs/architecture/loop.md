# 4. Agent loop

Part of the [SDK architecture](../architecture.md) reference; this page is
normative for §4.

## 4.1 Turn sequence

1. Validate configuration before creating new session state. A new session
   requires a model identity. A resume or fork resolves the session under
   `<state>/sessions`, validates the v2 log, inherits the recorded
   provider/model/dialect/base URL/context/output values unless explicitly
   overridden, ignores the recorded run budgets (`max_turns`,
   `max_total_tokens`, `session_timeout_ms`, and `model_timeout_ms`), and
   requires the workspace to match. Resume/fork without an explicit model uses
   the recorded values and skips the catalog; an explicit model is resolved
   through the catalog, and an explicit provider that differs from the record is
   a configuration error. Construction failures return typed errors and write
   no session events.
2. Prepare durable state. A new session creates the store and appends
   `session_start`. A resume takes an exclusive `flock`, validates the log,
   repairs only a torn final line (recording `session_repair`), and pairs every
   unpaired tool call with `tool_result` carrying `code=canceled` and an
   outcome-unknown message. A fork takes a shared `flock` on the parent,
   validates and inherits, creates a new session ID and store, appends a new
   `session_start` with `parent_session`/`parent_seq`, copies the parent events
   (skipping its `session_start`) with the new session ID and renumbered `seq`,
   and repairs unpaired calls in the copy. The parent is never modified.
3. Remove and recreate `<state>/work/<session-id>` before the run. A failure to
   clear a stale directory is a startup failure, not a silent reuse. Cleanup
   restores owner access before retrying once, so read-only directories left by
   a child cannot wedge later runs.
4. Append `run_start` with a UUIDv7 `run_id`, `resumed_from_seq`, effective
   model/dialect/base URL/context/output values, the recorded run budgets
   (`0` unless explicitly set), prompt/toolset/harness versions, and the
   host's `interactive`/`stream` flags; sync. Append the task `user_message`
   when this run has one, with its source, and sync.
5. Consume any already-pending steering lines as `user_message` events with
   `source=steer` (or `source=subagent-steer` inside a delegated child, where
   the line was routed from a child-focus view), then check the cumulative
   turn/token budgets and the per-run wall clock only when the corresponding
   budget is explicit and positive. A reached explicit limit appends `limit`
   and terminates; otherwise those budgets are unlimited.
6. Rebuild the model input from the event log through `agent.Project`.
7. Call the model with retries (Section 8). With `Start`, use the adapter's
   `CompleteStream` and emit display-only text and thinking deltas; otherwise
   use `Complete` and emit any thinking once before the assistant text. Each
   failed transport attempt that will be retried appends `model_retry` with its
   attempt number, error class, HTTP status, and delay. The successful attempt
   appends exactly one `model_response`, including text, tool calls, finish
   reason, usage, latency, attempt number, and `streamed`; thinking text is
   never included. Failed attempts do not append `model_response`. A response
   over 1 MiB is `protocol_error` before any tool executes.
8. If there are no tool calls, act on the normalized finish reason: `stop` (or
   absent) consumes any steering line already pending and continues if one was
   appended, otherwise terminates with `completed`; `length` appends a
   `user_message` with source `continuation-hint` and continues, but a second
   consecutive `length` response without tool calls terminates with
   `token_limit`; `content_filter` terminates with `provider_error`; any other
   value terminates with `protocol_error`. A host operation interruption of the
   model call appends `interrupt` and a `user_message` with source
   `interrupt-hint`, then continues without counting a turn.
9. Before executing, reject a response with missing or duplicate tool-call IDs
   (`protocol_error`, Section 8). For each tool call, in response order: resolve
   the tool, validate arguments, execute with a timeout, append exactly one
   `tool_result` carrying a stable `code`, and sync. A delegated task is an
   ordinary tool call: `delegate` runs its tasks each in its own child store and
   returns one consolidated result whose `details` record the per-task turns,
   usage, and cost plus the call's execution mode. A call whose every task holds
   a grant within `read_file`, `read_image`, `search`, `web_search`, and
   `web_fetch` runs its tasks concurrently under a cap of four; any other call keeps array order and
   runs sequentially. The runner folds those totals into the
   run's subagent counters before the next turn, so `checkLimits` evaluates
   parent plus child tokens. A failure is still a result
   with the original call ID. A recovered panic is paired before the run stops
   with `internal_error`. A host tool interruption pairs the interrupted
   call and every unstarted call in that response with `code=canceled`, appends
   `interrupt`, and continues; the model sees the results.
10. Increment the turn counter and repeat from step 5. Stop when an explicit
    limit is reached, a fatal error occurs, or the run is canceled.
11. On termination of an initialized run, emit exactly one `final` event with
    the terminal status and the last assistant text emitted in this run, if any,
    then append `run_end` with the run and cumulative turns/usage, the folded
    subagent turns/usage/cost, and the run duration, and sync the log. The
    engine owns the parent work directory, not the runner: it is reset before
    the next `BuildRunner` and removed by `Host.Close` or
    `Host.CleanupWork`. A delegated child removes its own work directory when
    the child run ends. If the log write itself fails, return `internal_error`
    without claiming durability; the next `BuildRunner` still resets the work
    directory.

The loop never batches multiple assistant turns, never skips a tool call, and
never sends a tool result without a preceding call. Multiple tool calls execute
sequentially in the order returned; correctness does not depend on provider-side
`parallel_tool_calls` support. `seq` and `turn` continue across runs. A host may
use steering for interactive submissions; it is consumed only at turn boundaries
and never interrupts a tool round. A child-focus view may route steering to that
child's channel instead, and the child records it with `source=subagent-steer`;
the parent transcript gains nothing. A headless host need not use steering. The
host maps signals and keys to `Interrupt` and `Cancel`; the SDK owns neither.

## 4.2 Terminal states

| Status | Meaning |
|---|---|
| `completed` | Model returned a final message with no tool calls and a `stop`/absent finish reason. |
| `max_turns` | Explicit positive `MaxTurns` reached (`turns >= max_turns`). |
| `token_limit` | Explicit positive `MaxTotalTokens` reached, repeated consecutive output-length truncation, or an unrecoverable context overflow. |
| `time_limit` | Explicit positive `RunTimeout` reached. A parent context deadline without an explicit budget also stops with `time_limit` but emits no `limit` event. |
| `provider_error` | Non-retryable provider failure, content filter, or retries exhausted. |
| `protocol_error` | Model response cannot be paired or parsed safely, including missing or duplicate tool-call IDs, malformed streams, missing usage, or an over-cap response. |
| `stuck` | Repeated identical tool failures (same tool, canonical arguments, stable result code) crossed the threshold. |
| `interrupted` | A host canceled the run through `Cancel` after an in-flight operation; the child process group is killed. |
| `internal_error` | Post-validation disk or harness invariant failure, or a recovered tool panic. Construction failures occur before a session exists. |

Every terminal state of an initialized run emits exactly one `final` event with
the status and the last assistant text emitted in that run, if any, followed by
one `run_end` event with a human-readable reason, run and cumulative totals, and
the run duration. Terminal statuses recorded by older logs (`max_turns`,
`time_limit`) remain decodable and renderable even when the current invocation
sets no budget. v1 does not emit `session_end`. If the log write itself fails,
the run returns `internal_error`; the harness does not claim durability.
Construction failures produce no session events. A resumed or forked run's
`final` event and `Result.FinalText` carry only that run's assistant text; prior
final text stays in the log and is not reprinted. The host maps `StopReason` to
its own exit code or UI state in one place; the SDK does not choose an exit
code.

## 4.3 System prompt

The system prompt is versioned and recorded in `session_start`. `prompt-v0`,
`prompt-v1`, and `prompt-v2` are frozen for replay. `prompt-v3` is current: it
keeps the catalog and the todo guidance, and replaces the shared rules with the
nine lines below. Compared with v2 it adds the project-instructions rule, the
verified-versus-unverified reporting rule, the refuse-or-flag rule, and the
delegation independence criterion; it also states that the tool call is the only
way to act and that every call receives exactly one result. As in v1 and v2, a
parent session carries the available-agents catalog (one `- name: description`
line per agent, capped at 4 KiB with an `[N agents omitted]` marker). A child's
prompt is its agent body plus the environment block and shared rules. A fresh
child also composes the host's `Config.Instructions` text after its role body;
a continued child reuses its recorded prompt, which already holds it. A child
gets no catalog and no delegation guidance because it cannot delegate; a
prompt-v2 or prompt-v3 child whose grant includes `todo` also gets the todo
guidance, while v0 and v1 children keep their frozen rules. The recorded text is
authoritative for projection, so editing an agent file never rewrites an
existing session's prompt. New sessions record `prompt-v3` and `toolset-v2`.

The parent identity line is parameterized. `Identity` names the host product:
the parent line reads `You are acode, a coding harness working in the workspace
...` and a delegated child's line reads
`You are the acode "reviewer" agent, ...`. `Identity` defaults to `aharness`; the
engine trims it, caps it at 64 bytes, rejects invalid UTF-8 and control
characters, and does not record it. A host re-supplies it on every construction,
including resume and fork, like `UserAgent`; a fork's new children use the
identity supplied to `Fork`. Child prompts are recorded verbatim, so existing
children keep their text. A log written before the recorded prompt predates the
identity parameter, so its replay uses the `aharness` default. Recording
identity would add an optional field, not a schema bump; this change deliberately
does not.

`Config.PromptTag` is the host's own attribution beside the SDK template
version: `session_start.prompt_tag` is optional, recorded next to
`prompt_version`, and exposed as `State.PromptTag`. It is at most 64 bytes of
printable ASCII matching `[A-Za-z0-9._:/-]+` (for example
`acode/prompt-v1`); empty means unset. It is never a replay key and never
affects rendering, caps, or replay. A new session records the explicit tag.
Resume keeps the recorded tag, accepts an equal explicit tag, and rejects a
different one with a `ConfigError` that says to fork. Fork prefers an explicit
tag; with none it inherits the parent tag only when the resulting prompt is
unchanged, and records none when the prompt changes. A delegated child records
no tag, because its recorded prompt is its agent body, not the host template
the tag describes. A log written before the field reads as unset.

The default prompt states, briefly:

- Tools are the only way to act on the workspace: report an action as done only
  when a tool call performed it, and every tool call receives exactly one
  result.
- Search and read before editing; keep changes minimal; run the relevant tests
  before reporting the change done and say plainly what could not be verified.
- Read the workspace's project instructions (`AGENTS.md` or the file it names)
  before acting on the code.
- If a task is infeasible as stated, or a check is wrong, say so rather than
  working around it.
- Commands run with the harness's privileges; the harness adds no sandbox or
  approval step.
- Tool errors are data; adapt rather than escalate.
- Stop by replying without a tool call when the task is complete or blocked;
  when blocked, say what blocks you and what remains.
- Use `todo` for multi-step work: write the checklist first, keep one item in
  progress, and mark items completed as they finish.
- Use `delegate` for independent, self-contained work when the conclusion must
  be reached without the context that produced it, when it would otherwise
  flood the conversation with tool output, or when independent read-only tasks
  can run in parallel; state the goal, constraints, and required report because
  the child has no history.

Prompt assembly is three exported primitives.
`agent.DefaultPromptBody(version, agents)` returns the default body without the
identity line and without the recorded-prompt cap: the shared rules, the
version's guidance, and the catalog. `agent.LoopRules()` returns the four
invariant lines plus the privileges fact and no coding or coordination policy; it
is version-independent. `agent.AgentCatalogText(agents)` renders the one-line
catalog under the 4 KiB cap with the `[N agents omitted]` marker. Unexported
`renderPrompt` is the identity line, a blank line, and `DefaultPromptBody`; it
never strips text, so the composed and default paths cannot drift. v0 is frozen
and ignores the catalog.

Hosts choose one of three tiers:

1. Set `Config.Identity` and inherit `DefaultPromptBody`. Recommended for a
   coding agent: the host owns its name and its catalog and inherits the rest.
2. Compose `LoopRules()` + host policy + `AgentCatalogText(agents)`. For a
   non-coding product or a different coordination policy; no invariant is
   transcribed and the catalog cap is not re-implemented.
3. Replace the prompt with `Config.SystemPrompt`. `LoopRules()` is how the host
   obtains the invariants without retyping them.

`Config.Instructions` appends to the resolved prompt. `DefaultPromptBody` is
uncapped; `RenderSystemPrompt` and `CapSystemPrompt` apply the 32 KiB recorded
prompt cap. `RenderSystemPrompt(version, workspace, identity, agents)` takes the
identity as a deliberate breaking component change; an empty value renders
`aharness`.

The appended text is provenance, not a replay key. A new session records it once
as optional `session_start.appended_prompt` and exposes it as
`State.AppendedPrompt`; the projection keeps using `system_prompt`, so the field
never changes a replay. Every fresh child composes the parent's text after its
role body, so a delegated child's `system_prompt` contains it and the child
records no `appended_prompt` of its own; a continued child reuses its recorded
prompt, which already holds the bytes. Resume carries the recorded value without
rewriting `session_start`. A fork inherits it with the prompt, records the union
when it supplies its own `Instructions`, and records none when it replaces the
prompt with an explicit `SystemPrompt`. A fork whose parent log predates the
field records none: the bytes are not recoverable from a prompt. The recorded
value is capped at 32 KiB independently and is not necessarily a suffix of
`system_prompt` once truncation applies; a host that needs a rule to survive an
over-cap prompt puts it at the front of its own text. The SDK does not discover
workspace files: a host that wants `AGENTS.md`, or an equivalent, in the parent
and in every child reads the file and passes the bytes here. The component
`agent` layer never appends; `agent.Config.AppendedPrompt` only records text the
caller already composed into `SystemPrompt`.

Tool mechanics belong in tool descriptions; the system prompt states only when a
coordination tool is worth a turn. Steering instructions are user messages and
recovery notes are tool results, not prompt changes. Any prompt change bumps the
version and must keep the old version replayable or explicitly refuse
resume/fork for it.
