# aharness v1 build plan

- **Date:** 2026-11-10
- **Status:** implemented (M0–M3); real-provider streamed smoke pending
  credentials in this environment. Mock-provider, integration, fuzz, and gate
  checks pass. **Deviation:** the v1 "no config file" decision was later
  reversed. Aharness now supports an optional JSON config file below flags and
  environment; see [configuration.md](https://github.com/eddiectc/acode/blob/main/docs/configuration.md) and the deviation
  noted in §1.2. **Deviation:** the assumption that Responses needs no
  reasoning-item continuation no longer holds. Both adapters now retain
  provider-native continuation material in memory for tool rounds; see
  [architecture §2.2 and §2.4](../architecture/provider.md#22-canonical-model-and-dialect-adapters).
- **Source:** [v0 build plan](v0-build.md); v0 is complete, including both
  dialect smoke runs.
- **Research:** [Bare-Minimum Agent Harness](../research/agent-harness-minimum.md). v1
  follows its Tier 2 ordering for streaming/interrupt and the replay/fork part
  of session continuity. The Tier 1 sandbox/approval boundary remains external;
  workspace snapshots, hooks, metrics/evals, and Tier 3 remain out.
- **Target:** Go 1.27.3, module `aharness`.
- **Architecture:** the shipped v1 contracts are in the
  [repository architecture](../architecture.md) and
  [testing strategy](../testing-strategy.md); this document records the v1
  scope, milestones, decisions, and release criteria. On Go practice, the
  [coding standard](../coding-standard.md) wins.
- **Testing:** the shipped layers and cases are in the
  [testing strategy](../testing-strategy.md); §4 here records the v1 test plan.
- **Limits:** [caps, limits, and timeouts](../limits.md) are the current values.
- **Definition of done:** [project definition of done](../definition-of-done.md);
  feature criteria in §1.4.

v1 turns the one-shot v0 loop into an interactive, resumable single-agent
harness. It keeps the same provider, tools, workspace policy, and deterministic
context projection. It adds streaming, interrupt/steering, and session
continuity. It does not add a second agent, a provider matrix, a sandbox, or an
evaluation platform.

## 1. Scope

### 1.1 What v1 owns

| Area | v1 implementation |
|---|---|
| Streaming | Opt-in assistant-text streaming for both OpenAI dialects. The final canonical response remains the only transcript record; deltas are display-only. |
| Interactive control | Opt-in `-interactive`: first `SIGINT` interrupts the in-flight model call or tool, second `SIGINT`/`SIGTERM` terminates; stdin lines are steering messages. |
| Continuity | `-resume <session-id>` appends a new run to an existing v2 session. `-fork <session-id>` creates a new self-contained v2 session from an existing v2 session. |
| Session schema | A v2 schema that separates a durable *session* from a process *run*: `run_start`/`run_end`, per-run `final`, `interrupt`, and `session_repair` events. |
| Durability | Exclusive `flock` on the session log, torn-final-line repair on resume, and sync points after user input, model responses, each tool result, and run end. |
| Versioning | v1 writes and reads only its own schema (`v=2`). v0 logs are rejected, not migrated; the CLI and log layout may change without a v0 compatibility guarantee. |

### 1.2 What stays the same

- One provider (`opencode-go`), the two OpenAI dialects, and the three accepted
  models; no routing or fallback.
- The four tools, their schemas, descriptions, result codes, and ACI behavior.
- `os.Root` containment, `.git` protection, symlink policy, command environment
  allowlist, and output caps.
- The canonical model, deterministic compaction, and the projection constants.
- One process, one loop, one writer; no daemon, UI, or plugin system. (A later
  deviation added an optional JSON config file: see
  [configuration.md](https://github.com/eddiectc/acode/blob/main/docs/configuration.md).)
- No built-in isolation or approval. External isolation remains the operator's
  responsibility.

### 1.3 Not in v1

No hooks, model-based summarization, subagents, RAG/embeddings, MCP/plugins,
additional provider dialects, built-in evals or an eval runner, price tracking,
workspace snapshots/rollback, built-in isolation or approval, session rotation
or retention policy, arbitrary-path resume, a control protocol beyond plain
stdin lines, v0 log migration or backward compatibility, or an interactive TUI.

### 1.4 Definition of done

v1 is complete when the [project definition of
done](../definition-of-done.md) holds and every item below holds.

- [x] **Streaming (local).** The built binary streams assistant text from a
      fake SSE server for both dialects. The adapters fall back to non-streamed
      completion when the endpoint returns JSON.
- [x] **Streaming (real provider).** A real streamed smoke passes for at least
      one model per dialect. This requires provider credentials and was not run
      in this environment; if a hosted model does not stream, record the
      limitation and keep non-streamed completion.
- [x] **Interrupt/steering.** First `SIGINT` during a model call and during a
      command interrupts the operation and continues the run; second `SIGINT`
      exits 130. A steering line becomes a `user_message` and is visible to the
      next model call. Non-interactive `SIGINT` remains terminal. No child
      process or process group survives.
- [x] **Resume.** A killed, interrupted, or limit-stopped run resumes under the
      same session ID, repairs any unpaired tool calls, reconstructs turns and
      usage, and honors the cumulative limits. A torn final line is repaired
      and recorded. A concurrent writer fails cleanly.
- [x] **Fork.** A v2 session forks into a new self-contained v2 log with
      parent metadata. The parent log is unchanged. Unpaired calls are repaired
      in the copy, and the fork's transcript diverges from the new task.
- [x] **Schema handling.** A v2 session round-trips; a v0 or unknown schema
      version is rejected with a clear error; unknown fields and event types
      within the v2 schema are ignored.
- [x] **Documentation.** `README.md` links this plan. The repository
      architecture and testing-strategy documents reflect the v1 contracts;
      `limits.md` reflects the current values.
- [x] **Gates.** The coding standard §8 checks pass: `goimports`, `go vet`,
      `golangci-lint`, `go test -race -count=1`, `go build -trimpath`, and
      `govulncheck`.

## 2. Contract deltas

These deltas are the v1 product contract. They are deliberately narrow: they
change the session log, the CLI control surface, and the provider interface,
and nothing else.

### 2.1 Session and run model

A **session** is the durable conversation. A **run** is one process invocation
that advances it. v0 conflated the two; v1 does not.

The session log is one append-only JSONL file per session, with the envelope
(`v`, `seq`, `ts`, `session`, `turn`, `type`, `role`, `data`). v1 writes `v=2`
and reads only `v=2`. Every event in a file has the same `v`; a `v=1` or
unknown version is rejected with a clear error.

| Event | Scope | Payload |
|---|---|---|
| `session_start` | once per session | workspace, state dir, provider, dialect, model, limits, prompt/toolset/harness versions, plus optional `parent_session` and `parent_seq` for a fork. |
| `run_start` | once per run | `run_id` (UUIDv7), `resumed_from_seq`, effective model/dialect/base URL/limits, prompt/toolset/harness versions, `interactive`, `stream`. |
| `user_message` | as needed | task/hint text and source; sources add `steer` and `interrupt-hint`. |
| `model_response` | per successful call | text, tool calls, finish reason, usage, latency, attempt, `streamed`; capped at 1 MiB total. |
| `interrupt` | as needed | operation (`model`/`tool`), call ID/name for a tool, capped `partial_text`, reason. |
| `session_repair` | on resume | discarded bytes, last good seq, reason. |
| `final` | once per run | terminal status and last assistant text; the last `final` is the current run's answer. |
| `run_end` | once per run | status, reason, run turns/usage, cumulative turns/usage, duration. |

`model_request`, `model_retry`, `tool_result`, `compaction`, `limit`, and
`error` keep their existing payloads; `error` gains optional capped
`partial_text` and `partial_bytes` for a stream that failed after output began.
`seq` and `turn` continue across runs. v1 uses `run_end` and does not emit
`session_end`. The projection ignores every run/control event. A resumed or
forked run's `final` event and stdout carry only that run's assistant text;
prior final text is in the log but is not reprinted.

A run without a `run_end` is a crash artifact. Resume does not fabricate a
terminal event for it; the new `run_start` records `resumed_from_seq`.

Before a new run starts, every unpaired tool call in the durable log is paired
with a `tool_result` carrying `code=canceled` and a message stating that the
harness restarted and the side effect may or may not have occurred. This
restores the pairing invariant without claiming the tool did not run.

### 2.2 Resume and fork

Session IDs are UUIDv7 and are validated before any path is built. They resolve
only under `<state>/sessions/<id>.jsonl`; path-based resume/fork is deferred.

- **`-resume <id>`** opens the existing v2 session, takes an exclusive
  `flock`, validates the log, repairs only a torn final line, appends a new
  `run_start`, and continues. The session ID is unchanged.
- **`-fork <id>`** requires a task. It takes a shared `flock` on the parent,
  reads and validates the parent, then creates a new session ID, writes a new
  `session_start` with `parent_session`/`parent_seq`, copies the parent's
  events (skipping its `session_start`) into a new v2 log with the new session
  ID and renumbered `seq`, repairs unpaired calls in the copy, and appends the
  new run. The parent is never modified.
- Resume and fork accept only v2 sessions. A v0 log is rejected with a clear
  error; there is no migration.

Fork preserves source event timestamps but not monotonic `ts`; consumers order
by `seq`. A fork is self-contained: it does not reference the parent for reads.

| Situation | `-resume` | `-fork` |
|---|---|---|
| New session | n/a | n/a |
| Last run ended `completed` | task required | task required |
| Last run ended interrupted, crashed, `provider_error`, `protocol_error`, `stuck`, `time_limit`, or `token_limit` | task optional; a reached turn/token limit must be raised | task required; a reached turn/token limit must be raised |
| v0 log (schema v1) | rejected | rejected |

Configuration inheritance:

- The recorded workspace must match; if `-workspace` is omitted, the session's
  workspace is used. A different workspace is a config error.
- If `-model` is omitted, the last run's model is used. An explicit model may
  change between runs; `run_start` records the effective value.
- Effective limits are inherited from the last run unless explicitly
  overridden. A resume or fork that is already at an inherited turn/token
  limit fails at config validation unless that limit is raised.
- Prompt and toolset versions are selected from `session_start` and must be
  supported by the binary. v1 ships only `prompt-v0` and `toolset-v0`.
- The provider base URL is inherited from the last run unless explicitly set,
  so resuming a local or test session does not silently switch to the hosted
  endpoint. `run_start` records it. Config validation rejects URL userinfo and
  query/fragment so the log cannot capture URL credentials.
- The provider token and state directory are read from the current invocation;
  they are not inherited from the log.

Locks use the session log file itself, not a separate lock file. Resume takes
`LOCK_EX|LOCK_NB`; fork takes `LOCK_SH|LOCK_NB`. A busy session is a config
error with the trace path. Locking is advisory and local-filesystem only.

The provider's `x-opencode-session` header uses the session ID: resume keeps
it, fork uses the new ID. Both adapters still replay the full projection and
keep `store: false`; the header does not make the transcript server-owned.

### 2.3 Streaming

Each dialect adapter optionally implements:

```go
type StreamCompleter interface {
	CompleteStream(ctx context.Context, req Request, emit func(string) error) (Response, error)
}
```

The agent keeps `Completer` and falls back to `Complete` when streaming is
requested but the adapter does not implement it. The final `Response` is
authoritative for the transcript; the callback is display-only. Tool calls are
assembled inside the adapter and are not exposed as deltas.

- Chat Completions sends `stream: true` and
  `stream_options: {"include_usage": true}`, and assembles text, tool-call
  fragments, finish reason, and usage from the chunks.
- Responses consumes `response.output_text.delta`,
  `response.function_call_arguments.delta`, and the terminal
  `response.completed`/`response.failed` events. The completed response is
  authoritative.
- A 2xx response that is not `text/event-stream` is decoded as a normal JSON
  response, so local OpenAI-compatible servers keep working.
- Final usage is required. A completed stream without usage is a
  `protocol_error`; the harness does not silently undercount tokens.
- Retries happen only before the first delta. After the first delta, a
  transport failure is terminal (`provider_error` for transport, or
  `protocol_error` for malformed events) and is not retried, because a retry
  would duplicate stdout and could duplicate tool calls.
- Caps: 4 MiB per SSE event, 1 MiB for the assembled canonical response,
  8 MiB for the total stream, and 64 KiB for partial text on an interrupt or
  failed stream. The event cap is larger than the response cap because a
  terminal `response.completed` event wraps the full response in JSON.
  A response over 1 MiB is a `protocol_error` before any tool executes. Invalid
  JSON, duplicate names, and invalid UTF-8 are `protocol_error`. Error bodies
  are capped, redacted, and never logged whole. The 1 MiB response cap matches
  the non-streaming body cap and is well above the default 8,192-token output;
  supporting the full 131k/384k output windows would require raising the
  transport, log, and line caps together.
- The durable transcript keeps the executed response: the `model_response`
  payload is capped at 1 MiB total, and the session line cap is raised to 4 MiB
  so JSON escaping and envelope fields fit. Tool results remain at 64 KiB. This
  avoids a mismatch where a large response is executed but its logged fields
  are truncated, which would break resume.
- A failed stream after deltas appends one `error` event with the partial byte
  count and capped partial text; no `model_response` is written and the partial
  text is not projected. An interrupt appends `interrupt` instead.
- No per-delta events are written. The log size and projection behavior stay
  proportional to complete responses.

`-stream` is opt-in and defaults off, preserving the buffered stdout contract.
With `-stream`, stdout carries assistant text from every turn as it arrives; the
final text for a response is not reprinted if any delta for it was emitted. If
the adapter fell back to non-streaming, the final text is printed once.
`-interactive` implies streaming unless `-stream=false` is explicit.

### 2.4 Interactive control and signals

`-interactive` is opt-in. It reads steering from stdin (a pipe, terminal, or
`/dev/null`) and allows a task to be omitted only when `-resume` supplies a
session whose last run did not end `completed`.

- Stdin lines are steering text, not a command language. Each non-empty line is
  sanitized, capped at 64 KiB, and appended as a `user_message` with
  `source=steer`. A line too large is rejected on stderr without stopping the
  run. EOF stops steering; the run continues.
- Steering is consumed at turn boundaries: after a model response and its tool
  round, before the next model call, and before finalizing a `completed`
  response if a line is already pending. It never interrupts a tool round;
  `SIGINT` does that.
- First `SIGINT` cancels the in-flight model call or tool operation only. If
  no operation is in flight, it is ignored. For a model interruption, append
  `interrupt` and a `user_message` with `source=interrupt-hint` so the next
  call knows the previous attempt was interrupted. For a tool interruption,
  pair the interrupted call and every unstarted call in that response with
  `code=canceled`, then continue; the model sees the results.
- Second `SIGINT` or `SIGTERM` cancels the run. The terminal path writes
  `final` and `run_end`, syncs, removes the work directory, and exits 130.
- Non-interactive `SIGINT` remains terminal: cancel the run, write the terminal
  events, exit 130.
- The signal goroutine only cancels contexts or sends on a bounded channel;
  the raw handler performs no I/O and mutates no state. The runner's owning
  goroutine performs every state and log write. An operation cancellation is
  distinguished from the run deadline by checking the run context first.
- A tool cancellation kills the whole process group and returns
  `code=canceled`; a provider cancellation is not retried.

The steering reader is one goroutine with a documented exit: EOF, stdin error,
or run cancellation. It performs no logging and mutates no run state.

### 2.5 CLI and configuration

New flags:

| Flag | Meaning |
|---|---|
| `-resume <id>` | Continue an existing v2 session under the current state directory. |
| `-fork <id>` | Create a new v2 session from an existing v2 session; requires a task. |
| `-interactive` | Read steering from stdin and enable first-`SIGINT` interruption. |
| `-stream` | Stream assistant text to stdout. |

The new flags have no environment equivalents in v1: session selection and
interactive behavior are per-invocation, and an ambient `AHARNESS_RESUME` is a
foot-gun. Existing environment variables remain, but v1 does not guarantee v0
CLI compatibility.

On resume/fork, `-provider-base-url` or `AHARNESS_PROVIDER_BASE_URL` overrides
the recorded base URL; otherwise the last run's URL is used. Config validation
rejects userinfo and query/fragment in the URL so the recorded value is safe.

The session ID and trace path are logged on stderr at startup for a new,
resumed, or forked session. For a fork, the new session ID is the one to use
afterward. Stdout remains the final or streamed assistant text.

Validation matrix:

- New session: exactly one of `-task`/`-task-file`; `-model` required.
- `-resume`: at most one task source; a task is required if the last run ended
  `completed`; model/workspace/limits are inherited unless explicitly set.
- `-fork`: exactly one task source; model/workspace/limits are inherited
  unless explicitly set.
- `-resume` and `-fork` are mutually exclusive.
- `-interactive` without `-resume` requires a task.
- `-stream` without a task is allowed only with `-resume`.

Exit codes: `completed` 0, limits 2, provider 3, protocol/stuck 4,
interrupted 130, internal 1. A missing/invalid/locked session, a workspace
mismatch, an unsupported prompt/toolset version, or a task-rule violation is a
config error (2). A corrupt or unreadable existing log is a state failure (1)
and writes no new session events. A v0 or unknown schema log is a config error
(2) with a clear message; v1 does not migrate it.

### 2.6 Budgets, durability, and cleanup

- `max_turns` and `max_total_tokens` remain session-cumulative. Resume
  reconstructs them from `model_response` events; the limit event reports
  cumulative values. Raising an already-reached limit is required to continue.
- `max_context_tokens` and `max_output_tokens` remain per call.
- `session-timeout` remains the wall-clock bound of one run. Resume starts a
  fresh clock; `run_end` records the run duration.
- Sync after `session_start`/`run_start`, each user message, each
  `model_response` before any tool executes, each `tool_result`, and
  `final`/`run_end`. This tightens the prior turn-boundary durability because
  resume makes the crash window user-visible.
- A valid final line without a trailing newline is accepted and terminated
  with one before appending; it is not rewritten. Only a torn or undecodable
  final line is truncated, and that repair is recorded. A decodable final line
  that fails sequence or session validation is corruption and refuses resume.
- The session line cap is 4 MiB and the `model_response` payload cap is 1 MiB
  total; tool results remain 64 KiB. A response over 1 MiB is rejected before
  tool execution, so every executed tool call is fully recorded.
- `<state>/work/<session-id>` is removed and recreated at the start of every
  run, and removed best-effort at the end. A failure to clear a stale work
  directory is a startup failure (exit 1), not a silent reuse. Cleanup restores
  owner access before retrying once, so a child that leaves read-only
  directories cannot wedge later runs. Child `TMPDIR` is per-run and is not
  durable; `HOME` is inherited so git and other tools see the user's identity
  and configuration.
- The session log remains `<state>/sessions/<id>.jsonl`, mode 0600, with the
  state directory at 0700. Resume and fork verify the source log's mode; a
  broader mode is a startup error. No rotation or retention policy in v1.

### 2.7 Schema and versioning

- `session.Version` is 2. The reader accepts only 2, requires one version per
  file, and ignores unknown event types and fields within v2. A v0 (`v=1`) or
  unknown version is rejected with a clear error; there is no migration.
- v1 is not backward compatible with v0 logs or the v0 CLI/log contract. It
  keeps v0 flag names where they remain useful, but does not promise that old
  scripts, exit-code expectations, or session files continue to work.
- `prompt-v0` and `toolset-v0` remain the compiled v1 prompt/tool material.
  v1 does not change the system prompt or tool schemas; steering instructions
  are user messages and recovery notes are tool results. Future v1.x changes
  add a new version and keep the old one for v1.x replay, or explicitly refuse
  to resume/fork the old version. There is no requirement to replay v0
  sessions.
- A model or dialect may change between runs; the canonical transcript is
  model-neutral. The workspace may not.
- Provider-specific reasoning/continuation material remains adapter-owned. The
  Responses continuation caveat still applies to any new Responses model.

## 3. Milestones

| Milestone | Deliverables | Exit criteria |
|---|---|---|
| **M0 — Session v2 foundation** | v2 envelope and event payloads; reader accepts only v2 with one version per file; store create/open/lock/repair/copy; UUID validation; state reconstruction (cumulative turns/usage and pending calls; the run's output starts empty); prompt/toolset version lookup; session setup split from run start; repository architecture/testing updated for the v2 schema, run model, and store. | v2 round-trips; v0/unknown versions rejected; mixed versions rejected; torn final line repaired and recorded; concurrent writer fails; fork copy has new session/seq and preserved payloads/timestamps; a normal run writes v2; repo docs updated for M0; §8 gates pass. |
| **M1 — Resume and fork** | `-resume`/`-fork`; task matrix; inherited config; pending-call repair; self-contained fork; work-directory recreate; cumulative-limit checks; repository architecture/testing updated for resume/fork and the CLI. | A killed/interrupted/limit-stopped run resumes and completes; a v1 (`v=2`) session forks; parent bytes are unchanged; lock conflict exits 2; v0 logs are rejected; no leftover work directories; repo docs updated for M1; §8 gates pass. |
| **M2 — Streaming** | `StreamCompleter`; Chat and Responses SSE; `-stream`; retry-before-delta rule; usage requirement; 4 MiB event/1 MiB response/8 MiB stream caps; 1 MiB `model_response` log cap and 4 MiB session line; error mapping; `streamed`/partial fields; non-SSE fallback; repository architecture/testing updated for streaming. | Mock SSE for both dialects; integration proves a delta reaches stdout before the provider completes; 429 before a delta retries; reset after a delta is terminal; missing usage is `protocol_error`; an over-1-MiB response is rejected before tool execution and a 1 MiB response round-trips through the log; real streamed smoke per dialect or a recorded provider limitation; repo docs updated for M2; §8 gates pass. |
| **M3 — Interactive and hardening** | Signal routing; `-interactive`; steering reader and turn-boundary consumption; interrupt hints; tool-round pairing; non-interactive preservation; crash/resume matrix; repository architecture/testing updated for interactive control and signals; docs and manual smoke. | First `SIGINT` during a model call and a command continues the run; second exits 130; steering changes the next call; no orphaned child or process group; resume after interrupt works; non-interactive signal behavior unchanged; repo docs updated for M3; §8 gates and both-dialect smoke pass. |

Repository doc updates are part of the milestone that changes the contract, not
a final documentation pass: M0 updates the architecture's session/store
contracts and the testing strategy's session tests; M1 updates resume/fork and
the CLI; M2 updates the provider/streaming contract; M3 updates interactive
control, signals, and the full test matrix.

Every milestone is gated by the coding standard's §8 checks; do not defer lint,
vet, race, or vulnerability checks. Run `go fix ./...` before review and use
`go doc -ex` for examples; do not run `go fix` as a CI check. If a milestone
cannot pass, fix the interface instead of pulling a later feature forward.

## 4. Testing strategy

Unit and integration tests use fakes, `httptest`, `t.TempDir`, and
`testing/synctest`; no real network and no `time.Sleep`-based waiting, except
short real timeouts for subprocess/signal tests.

### 4.1 Unit

- Session: v1 (`v=2`) fixture decode; v0/unknown version rejection; one version
  per file; unknown fields/events; run/control payloads; `seq`/`session`/version
  validation; UUID validation; lock acquisition; torn-final repair versus
  corrupt-final rejection; fork copy; session repair event.
- Resume/fork: state reconstruction; pending-call pairing; task matrix;
  inherited model/workspace/limits; cumulative limits; workspace mismatch;
  unsupported prompt/toolset; parent immutability; fork timestamps.
- Streaming: SSE text and tool-call assembly for both dialects; `[DONE]`;
  comments/CRLF; malformed JSON/duplicate names/invalid UTF-8; event, response,
  and total-stream caps; oversized response rejected before tool execution;
  1 MiB `model_response` log round-trip; missing usage; non-SSE JSON fallback;
  error-body redaction; retry-before-first-delta versus no retry after.
- Interactive: steering at turn boundaries and before `completed`; model
  interrupt hint; tool interrupt pairs remaining calls; second interrupt
  terminal; non-interactive remains terminal; steering cap/sanitization; EOF.
- Projection: prompt/toolset selection by version; v2 run/control events
  ignored; resumed/forked histories; repaired calls paired; retained turns.
- Config: new flags; mutual exclusion; task rules; inheritance; explicit
  overrides; exit-code mapping.

### 4.2 Integration

Build the binary and run it against a fake provider in a temp workspace and
state directory.

- SSE for both dialects: a delta is visible on stdout before the fake completes;
  the log has one `model_response` with `streamed=true`; no delta events.
- Oversized response: a 1 MiB response round-trips through the log and
  projection; an over-cap response stops with `protocol_error` and executes no
  tool call.
- Interrupt: block the fake provider, send `SIGINT`, observe `interrupt` and
  the interrupt hint, then send a steering line and a final response; send a
  second `SIGINT` and assert exit 130.
- Tool interrupt: run a command that writes its PID and sleeps; send `SIGINT`;
  assert `code=canceled`, the process group is gone, and the run can continue.
- Crash/resume: kill -9 after a side-effecting command but before its result is
  synced; resume; assert the repaired result says the outcome is unknown and
  the new run completes. Repeat after a model call.
- Fork: complete a session, fork with a new task, assert the parent hash is
  unchanged and the new log contains the parent prefix plus the new run.
- Lock: pause a run in the provider; a second `-resume` exits 2 with the trace
  path.
- Work/state: modes 0700/0600; work directory removed on normal, error, and
  signal paths; stale work directory recreated on resume.
- v0 log: rejected with a clear error; no migration. Unknown v2 event/field
  tolerance.

### 4.3 Manual smoke

With the real `opencode-go` endpoint, for each implemented dialect: run a
non-streamed task, a streamed task, interrupt and steer, resume, and fork.
Inspect the raw log, confirm stdout/stderr separation, and confirm no work
directory remains. If a hosted model does not stream, record the limitation;
do not claim streaming support for it.

### 4.4 Fuzzing and gates

Fuzz the SSE decoder and the v2 JSONL reader/repair path with seed corpora in
`testdata`. Run the coding standard §8 gates in one Linux CI job; fuzzing may
run on a schedule. No real network in CI.

## 5. Decisions, risks, and assumptions

### 5.1 Decisions

| # | Decision | Rationale | Reversal risk |
|---|---|---|---|
| D1 | Split session from run and write a v2 schema. | Resume/fork need run boundaries, per-run status, and crash detection; the new envelope is unambiguous and self-contained. | High; persisted contract and external consumers. |
| D2 | Resume appends to the same log; fork copies into a new self-contained log. | Resume preserves identity; a self-contained fork keeps the raw trace usable by external tools. | Medium; CLI semantics and disk growth. |
| D3 | v1 does not read v0 logs; there is no migration. | Removes mixed-version and migration complexity; v1 is a new contract. | Low; old logs remain on disk but v1 cannot open them. |
| D4 | Turns/tokens are session-cumulative; wall clock is per-run. | Keeps the session budget meaningful and makes long sessions bounded; wall clock across processes is not meaningful. | Medium; users may expect per-run budgets. |
| D5 | Streaming deltas are display-only and never transcript state. | Keeps the log small and the projection deterministic; a failed stream is a failed call, not a partial message. | Medium; partial-output behavior. |
| D6 | Retry only before the first streamed delta. | A retry after display would duplicate stdout and can duplicate tool calls. | Medium; changes provider retry guarantees. |
| D7 | `-stream` is opt-in; `-interactive` implies it unless disabled. | Preserves the buffered stdout contract for scripts. | Low. |
| D8 | First `SIGINT` interrupts a turn; second terminates; non-interactive remains terminal. | Gives interactive users control without changing scripted signal behavior. | Medium; signal/exit contract. |
| D9 | Steering is plain stdin text; no command language. | The model already understands user text; a command parser is a second protocol. | Low. |
| D10 | Prompt/toolset versions stay compiled and selectable. | Resume/fork must replay the material a session recorded; version changes must not break v1.x logs. | High; every future prompt/tool change adds permanent replay code. |
| D11 | v1 does not change `prompt-v0` or `toolset-v0`. | Avoids a new prompt version before resume is proven; hints are user messages. | Low now, high if a prompt change is smuggled in later. |
| D12 | Workspace must match; model/dialect may change; limits may be overridden. | The transcript is model-neutral, but relative tool paths only make sense in the same tree. | Medium. |
| D13 | Lock the log file with `flock`; no separate lock file. | One writer per session; a stale lock disappears with the process. | Low. |
| D14 | Sync after each tool result and repair only a torn final line. | Resume makes side-effect durability visible; a torn line is not durable data. | Medium; write latency and recovery rules. |
| D15 | Session IDs resolve under the state directory; no arbitrary paths or rotation. | Prevents path traversal and keeps identity/lock/work-dir rules simple. | Low; path-based resume can be added. |
| D16 | Record and inherit the provider base URL; reject URL userinfo and query/fragment. | A local/test resume must not silently target the hosted endpoint, and the log must not capture URL credentials. | Low. |
| D17 | No built-in isolation or approval in v1. | Keeps the external-sandbox posture; the harness owns the loop, not the boundary. | High if built-in isolation becomes required. |
| D18 | 4 MiB per SSE event, 1 MiB assembled response, 8 MiB total stream; 1 MiB `model_response` log and 4 MiB session line. | A terminal Responses event wraps the full response in JSON; a 1 MiB response must be fully logged for replay. | Medium; log size and memory. |
| D19 | `run_command` inherits the host `HOME`; only `TMPDIR` is harness-owned. The work directory is still removed and recreated per run. | A harness-owned `HOME` broke `git commit` (no `user.email`), `safe.directory`, credential helpers, and `includeIf` rules, and defeated every cache, while buying no containment: the child runs with the harness's privileges and reads any path by absolute name. Isolation is external (bwrap, container, VM), where a container-scoped home already provides the same seam. `TMPDIR` stays harness-owned because the host `/tmp` may be `noexec`, size-limited, or cleaned mid-session. | Low; a future `-child-home` flag can restore a redirected home for untrusted runs. |

### 5.2 Risks

Existing risks remain unless this section changes them; the rows below are
v1-specific or newly amplified.

| Risk | Mitigation |
|---|---|
| Real provider streaming shapes or usage differ from mocks. | Per-dialect SSE tests, terminal-event validation, required usage, and a real smoke. If a model does not stream, record the limitation and keep non-streaming. |
| A crash after a side effect but before the result is durable. | Sync after each tool result; repair with `canceled` and an "outcome unknown" message; the model must verify; document that resume is not transactional. |
| `flock` is unreliable on network filesystems. | Require a local state directory; the lock is advisory and same-UID processes can ignore it. |
| Fork copies a large log. | Self-contained logs are deliberate; document O(history) fork cost. Rotation is deferred. |
| A stream failure leaves partial stdout. | Streaming is opt-in; the log records the failure and never a partial response; do not retry after output. |
| Signal races around completion. | Interrupt only while an operation is active; best-effort steering at completion; `-resume` is the recovery path. |
| Long sessions amplify per-turn full-event projection. | Measure; cache or incrementalize the projection if it becomes material. Not a v1 contract. |
| Prompt/toolset version debt. | Keep versions immutable, add rather than edit, and refuse unsupported resume/fork rather than guessing. |
| The workspace changed between runs. | Record the path, not a snapshot; require version control or a disposable tree; document the limitation. |
| Logs grow without rotation. | Caps at capture; no streaming deltas in the log; rotation is a follow-on. |
| Large model responses increase log size. | Cap `model_response` at 1 MiB total; keep tool results at 64 KiB; do not log deltas. |
| Secrets in tool output or files are copied by fork. | The harness redacts only the provider token. Use external isolation and a clean workspace. |
| The session log is trusted input on resume/fork; a same-UID process can edit it. | Accepted limitation, now more consequential because the log drives the next run. Use a separate UID/VM when audit integrity matters. |
| The hosted model behind a model ID can change between runs. | The log records the ID, not a model snapshot; use fixed model IDs and compare traces when behavior drifts. |
| Interactive and resumed runs extend the exposure of an untrusted repository or task to the host. | Still no built-in isolation; launch inside bwrap, Docker, or a VM and keep credentials out of the child environment. |

### 5.3 Assumptions expensive to reverse

These are the assumptions a late correction would pay for across the persisted
log, the CLI, the provider boundary, or the tool/prompt contract. The v0 build
plan's §4.4 assumptions about the loop, tools, workspace, and isolation still
apply unless this table changes them. The v0 build plan's log/CLI compatibility
assumptions do not; v1 is a new contract. Confirm each before the milestone
that freezes it.

| Assumption | If it is false | Confirm by |
|---|---|---|
| v1 is a new contract; v0 logs and CLI behavior need not be preserved. | v1 needs a v0 reader, migration, and compatibility shims; the schema and CLI surface change. | M0. |
| A session is a durable conversation and a run is one process invocation; v2 adds run boundaries, per-run finals, and `run_end`. | The reader, external consumers, and every log written by v1 need a migration or a new version; resume/fork semantics change. | M0, before v2 is written. |
| The session log remains one append-only JSONL file per session; no SQLite, index, or separate transcript store. | The store, reader, fork copy, locking, and external tooling change; a migration path is needed. | M0. |
| A 1 MiB canonical model response is the maximum replayable output; larger responses are rejected before tool execution. | Raise the transport, log, and line caps together; projection, memory, and log size change. | M2. |
| Resume appends to the same session ID; fork copies into a new self-contained session. | Session identity, CLI behavior, provenance, and log growth change; a parent-link or snapshot design would be a different feature. | M1, before the flags ship. |
| Turns and tokens are session-cumulative; only wall clock is per-run. | Limit semantics, exit behavior, and resume expectations change; users may need a new budget flag. | M1. |
| Prompt and toolset versions remain compiled, immutable, and selectable; v1 keeps `prompt-v0`/`toolset-v0`. | Every prompt/tool change either breaks resume/fork or requires embedding old prompts/schemas in every log; replay code becomes permanent. | M0 for the mechanism; M2 before any prompt/tool change. |
| The workspace is identified by its path; v1 does not snapshot files or create worktrees. | Resume needs snapshots, content hashes, or rollback; the session log alone is insufficient for faithful continuation. | M1, before claiming continuity on real work. |
| Streaming is display-only; no partial assistant message is transcript state, and retries stop at the first delta. | The log gains delta events or partial responses; retry, projection, and output contracts change. | M2. |
| Interactive first `SIGINT` interrupts, second terminates; non-interactive `SIGINT` remains terminal; steering is plain stdin text. | Scripts, exit codes, terminal behavior, and any future control protocol change. | M3, before release. |
| Isolation and approval remain external; v1 adds neither. | The process, tool, environment, and credential contracts change; this is the highest-cost product reversal. | M1/M3, before recommending real-work use. |
| Unix-like hosts with `flock`, `/bin/sh`, process groups, and `SIGINT`/`SIGTERM` are sufficient; the state directory is local. | Per-OS locking, command handling, and signal paths are required; Windows remains unclaimed. | M0/M1. |
| Provider streaming uses SSE with a terminal completed event and final usage; reasoning continuation is adapter-owned, bounded, and in-memory in both dialects. | The adapter needs a different continuation mechanism, a durable reasoning design, or a different accounting path. | M2 with a real provider. |
| Session IDs resolve under `<state>/sessions`; there is no arbitrary-path resume or log rotation in v1. | Identity, path containment, work-directory, and storage-retention rules change. | M1. |
| Durability syncs after each tool result; a torn final line is the only rewrite of a log. | The store's append-only/repair contract and crash-recovery rules change. | M0/M1. |
| The session log is trusted input; v1 does not authenticate or sign it. | Resume/fork need a trust boundary, signatures, or a separate UID; the log format and threat model change. | M1, before recommending resume on shared hosts. |

### 5.4 Prerequisites and open confirmations

1. **Scope confirmation:** v1 is streaming + interrupt/steering + resume/fork.
   Hooks, model-based compaction, and additional dialects remain follow-ons
   even though the source lists them near the same tier.
2. **Resume identity:** confirm same-session append rather than a new session
   per resume.
3. **Fork representation:** confirm self-contained copy rather than a parent
   chain.
4. **Budget scope:** confirm cumulative turns/tokens and per-run wall clock.
5. **Prompt/toolset policy:** confirm the v1.x versioning policy and that v1
   commits to retaining future versions. No v0 replay is required.
6. **Workspace continuity:** confirm path identity is enough for v1, with no
   snapshot/rollback.
7. **Interactive contract:** confirm first-`SIGINT` interrupt and plain-stdin
   steering.
8. **Provider streaming:** confirm real SSE support and usage reporting for
   both dialects in M2; if not, record the limitation.
9. **Isolation:** confirm v1 keeps the external-sandbox posture; built-in
   isolation remains a separate, high-cost decision.
10. **Streaming caps:** confirm 4 MiB per SSE event, 1 MiB assembled response,
    8 MiB total stream, 1 MiB `model_response` log, and 4 MiB session line.

## 6. Follow-on order

After v1 is reliable: lifecycle hooks; model-based compaction; additional
provider dialects as adapters; workspace snapshots/rollback or disposable
worktrees; session rotation/retention; path-based resume. Subagents, RAG, and
MCP/plugins wait for evidence that the single loop is the bottleneck. External
evaluation and benchmarking remain separate from the harness. Built-in
isolation or approval is a product decision, not an incremental feature.
