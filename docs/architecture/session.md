# 6. Context budget

Part of the [SDK architecture](../architecture.md) reference; this page is
normative for §6 and §7.

The event log is append-only. The model input is a lossy projection rebuilt from
it on every turn. Compaction never mutates the log.

## 6.1 Projection invariants

1. The system prompt is always first.
2. The original task is always retained.
3. The last four turns are retained verbatim.
4. Every assistant tool call is followed by its matching tool result. Tool-call
   IDs are never dropped, and a result is never emitted without its call.
5. Older tool-result bodies may be replaced by a one-line stub; the call ID,
   tool name, and original byte count remain. An elided image result keeps a
   `; image <media> <w>x<h> elided` note in the stub and drops its bytes.
6. The projection fits the configured input budget; the input cap does not
   reserve or subtract an output budget.
7. If the projection cannot fit after compaction, the run stops with
   `token_limit` before the model call.
8. The tool schemas are selected from the session's recorded toolset version
   and narrowed to the recorded tool grant, carried in `Projection.Schemas`, and
   used both for the request and for the per-schema token estimate. A resumed
   v0/v1 session never silently receives v2 descriptions, and a child never
   receives a schema outside its grant.
9. The system prompt is the text recorded in `session_start`. A log that
   predates the recorded prompt renders the frozen template for its recorded
   version, so replay never depends on the current agent files.
10. Image parts are carried only by tool messages, are excluded from the
    byte-based estimate in favor of a per-image patch estimate, and are dropped
    from the retained window's predecessors before any text is elided.

## 6.2 Budget policy

- Before each call, estimate input tokens from the projected messages and tool
  schemas. Use a conservative `ceil(bytes/4)` estimate plus 8 tokens per message
  and 16 tokens per tool schema; an image part estimates as
  `ceil(width/28) * ceil(height/28)` and its bytes are not counted, because
  base64 would overstate the cost by an order of magnitude. Treat
  provider-reported usage as authoritative for totals after each response. These constants are part of the projection
  contract and are covered by tests.
- The per-call limit is `min(model.limit.input, context - safety_margin)` when
  the catalog supplies an input ceiling, otherwise `context - safety_margin`,
  where `safety_margin` defaults to 5% of the context window (minimum 1,000
  tokens) to absorb estimator error. The output budget is not subtracted.
- There is no harness-imposed per-call output budget by default. The request
  omits `max_tokens` / `max_output_tokens`, leaving the ceiling to the
  provider/model; an explicit `Limits.MaxOutputTokens` is sent unchanged. The
  catalog's advertised output limit is display metadata, not a request budget.
- A `length` finish reason uses the continuation hint; two consecutive
  `length` responses without a tool round stop with `token_limit`. A provider
  context-overflow error rebuilds against 90% of the failed per-call input limit
  and retries once; if that retry fails or nothing can be elided, the run stops
  with `token_limit`.
- Tool results are truncated at capture, then elided from old turns in the
  projection. The initial task, system prompt, and recent turns are never
  elided.
- Compaction is deterministic: elide oldest tool-result bodies first, in order,
  until the estimate fits. Log a `compaction` event with before/after estimates
  and the number of elided results.
- Model-based summarization is deferred. Dropping old tool output satisfies the
  source's Tier 1 requirement and is easier to reason about.
- Cumulative totals include every model response's input and output tokens,
  including retries, and are recorded regardless of whether a budget is set.
  Context occupancy is tracked separately: `State.ContextTokens` and
  `Result.ContextTokens` hold the latest response's input plus output tokens —
  the size the next call starts from — while `Usage` remains the
  session-cumulative sum. The two diverge as soon as a run makes more than one
  model call, so only occupancy is comparable to a context window; a host
  status line reports occupancy and the details overlay reports both.
  The session additionally records an observational dollar estimate from the
  catalog price snapshot; it is not a billing authority and never changes a
  budget decision. When `Config.MaxTotalTokens` is explicit, a 1,000,000-token
  context can consume that cumulative budget in roughly two full-context
  calls.
- `run_start` records the effective `pricing` snapshot (`available`,
  `currency=USD`, `unit=per_million_tokens`, the four integer micro-USD rates,
  source URL, and fetch time). `model_response` records `cost_micro_usd` only
  when the snapshot can price that response. `run_end` records
  `run_cost_micro_usd`, cumulative `cost_micro_usd`, and separate
  `run_cost_complete`/`cost_complete` flags; a response without a price makes
  the corresponding flag false. Cache-read falls back to the input rate when
  the catalog omits it; reasoning is already inside output tokens and is not
  charged twice. The runtime does not model provider time-of-day tiers; the
  estimate uses the flat catalog rates.

# 7. Session log


- **Location:** `Config.StateDir`; a host chooses the default, for example
  `~/.aharness`. The log is `<state>/sessions/<session-id>.jsonl`; session IDs
  are UUIDv7 and are validated before a path is built. `run_command` gets a
  harness-owned `TMPDIR` at `<state>/work/<session-id>/tmp`, created with mode
  0700 at the start of every run; `HOME` is inherited. The whole
  `<state>/work/<session-id>` subtree is reset at the next `BuildRunner` and
  removed by `Host.Close` (or `Host.CleanupWork`); a delegated child removes its
  own subtree when the child run ends. `sessions/` is the only durable record.
- **Creation:** create the state directory with mode 0700 and the session file
  with `O_CREATE|O_RDWR|O_APPEND|O_EXCL`, mode 0600, then take an exclusive
  non-blocking `flock`. If the state directory is not writable, start-up fails
  with a clear error. The default is outside the workspace; avoid
  pointing it into the workspace, where the log becomes part of the task diff.
- **Resume:** open the log read-write, take `LOCK_EX|LOCK_NB`, verify mode 0600,
  validate every line, truncate only a torn/undecodable final line (append
  `session_repair` with the discarded byte count and last good `seq`), and
  terminate a valid final line that lacks a newline by appending one. A
  decodable final line that fails sequence or session validation is
  corruption and refuses resume. A version mismatch is rejected as an
  unsupported schema. Then pair every unpaired tool call with
  `tool_result` carrying `code=canceled` and the outcome-unknown message, append
  a new `run_start`, and continue. The session ID is unchanged.
- **Fork:** open the parent read-only with `LOCK_SH|LOCK_NB`, verify mode 0600,
  validate it, and leave it unchanged. Create a new session ID and log, append a
  new `session_start` with `parent_session`/`parent_seq`, copy the parent events
  (skipping its `session_start`) with the new session ID and renumbered `seq`
  while preserving payloads and timestamps, repair unpaired calls in the copy,
  and append the new run. A fork is self-contained; it never reads the parent
  again. Because the copy keeps the parent's `model_request`/`model_response`
  events, `agent.LatestModelExchange` on a fresh fork reports the parent's last
  exchange, and its `Run` counts the parent's runs.
- **Format:** one JSON object per line, encoded with `encoding/json/v2` through
  `jsontext.NewEncoder` (`jsonv2.MarshalEncode`). Common fields:
  `v` (schema version, 2 for v1), `seq` (starts at 1 and increments by 1),
  `ts` (RFC3339Nano), `session`, `turn`, `type`, `role` (message events only),
  and `data`. Keep the envelope stable and versioned. Use `omitzero` for zero
  scalars; v2 `omitempty` means “encodes as empty JSON”. Every event in a file
  has the same `v`; a v0 (`v=1`) or unknown version is rejected with a clear
  error, and there is no migration.
- **Durability:** `jsontext.Encoder` flushes each top-level value;
  `Store.Append` assigns `v`, `seq`, `ts`, and `session`, and `Store.Sync`
  flushes the file. Sync points are after `session_start`, `run_start`, each
  user message, each `model_response` before any tool executes, each
  `tool_result`, and `final`/`run_end`. `Close` syncs, unlocks, and closes. The
  single runner goroutine is the only writer; a signal handler only cancels
  contexts and never appends from another goroutine. The reader accepts a valid
  final line without a trailing newline, treats only a torn/undecodable final
  line as a crash artifact, validates that `seq` starts at 1 and increments by 1
  on replay, and never rewrites the log except for the documented torn-line
  repair.
- **Session ID:** `uuid.NewV7().String()` from the Go 1.27 standard library. No
  reuse. The provider's `x-opencode-session` header uses this ID; resume keeps
  it, fork uses the new ID.
- **Lock:** `resume` uses an exclusive non-blocking `flock`; `fork` uses a
  shared non-blocking `flock`. The lock is the session log file itself, not a
  separate lock file. Locking is advisory and local-filesystem only. A busy
  session is a config error naming the trace path.
- **Observation is not durable:** a display or per-child observation sink sees
  events only; it appends nothing, changes no payload, and leaves the schema at
  `2`. A streaming child records `stream` on `run_start` and `streamed` on
  `model_response`, which are ordinary recorded values.
- **Truth:** the log is the durable record; the context window is a projection.
  The log contains enough information to rebuild every model input except the
  exact provider wire format. With `Config.LogModelRequests`, an additional
  `model_request` event stores the exact projected messages, capped at 1 MiB.
  It is off by default. Secrets are redacted; the log never contains full
  environments, HTTP headers, or credentials. Tool arguments and results appear
  only as capped event payloads, never in host output.

| Event type | Scope | Payload |
|---|---|---|
| `session_start` | once per session | Prompt version, optional `prompt_tag` (host attribution beside the template version; never a replay key), toolset version, workspace, state dir, provider, dialect, model, base URL, context/output limits, max turns/total tokens, timeouts (all four run budgets recorded as `0` for unlimited), harness version, optional `thinking_effort`, the resolved `system_prompt` (32 KiB cap), optional `appended_prompt` (the host text composed into the prompt; provenance only, empty on old logs and on children, never a replay key), optional `agent` (the root's configured name or a child's definition name), the recorded `tools` grant (omitted means every tool of the version; `[]` means none), optional `root_tools` (root-only offer; omitted means `tools`), optional `custom_tools` (ordered `{name, description, parameters, concurrency_safe}` declarations, now additive in v2), optional `contract_override` (`parent_session`, `prompt`, `tools`) on a fork that changed the inherited contract, and optional `parent_session`/`parent_seq` plus `parent_call_id` for a delegated child (a fork has no call ID). |
| `run_start` | once per run | `run_id` (UUIDv7), `resumed_from_seq`, effective provider, model, dialect, base URL, context/output limits, optional `thinking_effort`, recorded run budgets, prompt/toolset/harness versions, `interactive`, `stream`, `require_usage`, and the effective `pricing` snapshot. |
| `user_message` | as needed | Task/hint/steering text and source (`-task`, `-task-file`, `continuation-hint`, `stuck-hint`, `steer`, `subagent-steer`, `interrupt-hint`). |
| `model_request` | debug only | Exact projected messages, capped at 1 MiB; one per distinct projected request; includes `thinking_effort` when one was sent. |
| `model_response` | per successful call | Text, tool calls (ID, name, arguments), finish reason, usage with cached/cache-write/reasoning subsets and `reported`, optional `cost_micro_usd`, latency, attempt, `streamed`; 1 MiB total canonical content. |
| `model_retry` | per retried attempt | Attempt number, error class, HTTP status, delay before the next attempt. |
| `tool_result` | per call | Call ID, tool name, `ok`, stable code, output, error message, exit code, duration, truncated flag, and optional `images` entries (`media_type`, base64 `data`, `width`, `height`, `bytes`, `sha256`). `read_image` is the only producer. The member is additive: a reader that predates it ignores it and projects the caption instead. |
| `compaction` | as needed | Before/after token estimate, elided result count, policy. |
| `limit` | as needed | Limit kind, configured value, observed value. |
| `interrupt` | as needed | Operation (`model`/`tool`), call ID/name for a tool, capped partial text, reason. |
| `error` | as needed | Fatal error class and message; optional capped partial text and partial byte count for a failed stream. |
| `session_repair` | on resume | Discarded bytes, last good `seq`, reason. |
| `final` | once per run | Terminal status and last assistant text for this run, if any. |
| `run_end` | once per run | Status, reason, run turns/usage, cumulative turns/usage, duration, estimated run/cumulative cost, cost-completeness flags, and the folded `subagent_turns`/`subagent_usage`/`subagent_cost_micro_usd`/`subagent_cost_complete` totals. Child turns are excluded from the parent's turn budget; child tokens are included in the token budget. |

`model_request` and `model_response` pairing is part of the session-schema
contract. A host that needs the latest recorded attempt reads it with
`agent.LatestModelExchange` (see §3.2) instead of reimplementing the rule; it
returns the recorded payloads as copies, with the request included only when it
shares the response's turn and no run or session boundary intervenes.

v1 uses `run_end` and does not emit `session_end`. The projection ignores every
run/control event. `seq` and `turn` continue across runs. A run without a
`run_end` is a crash artifact; resume does not fabricate a terminal event for it,
and the new `run_start` records `resumed_from_seq`.

`thinking_effort` is additive and omitted when empty. It records the effective
value after resolution; empty means the provider/model default and no wire
field. `agent.Inspect` uses the latest `run_start` value as the resume/fork
default, falling back to `session_start` before any run. A fork writes a new
`session_start` with the fork's effective value and keeps the copied parent
events' historical values unchanged. Old v2 logs without the field reconstruct
as empty and resume with the provider default; the schema version stays 2 and
old readers ignore the added field.

`tool_result.images` follows the same additive rule: it is omitted when a result
carries no image, the schema version stays 2, and a reader that predates it
ignores the member and projects the text caption. A session that recorded
`toolset-v3` still cannot be resumed by a binary that predates it, because the
recorded toolset version is rejected first; the graceful path is `Inspect` and
`Transcript`, which never decode images.

The log is for debugging and resume, not a tamper-proof audit trail. A shell
running as the same user can reach the state directory. Separating the log from
the shell would require a separate UID or process boundary and is out of scope.
