# aharness limits and timeouts

- **Date:** 2026-11-10
- **Status:** reference. Values marked **v1** are planned and are not implemented
  until the v1 milestones land; all other values are current v0.
- **Related:** [architecture](architecture.md), [v0 build plan](v0-build.md),
  [v1 build plan](v1-build.md), [coding standard](coding-standard.md).

This is the single reference for caps, limits, and timeouts. Units are bytes,
tokens, or durations as noted. A value of **same** means v1 keeps the v0 value.
A value of **—** means the limit does not exist in that version. "No separate
cap" means the value is bounded only by an enclosing limit (the session
deadline, the session line cap, disk, or process memory), not by its own
constant.

## 1. Session budgets and timeouts

| Limit | v0 current | v1 planned | Behavior |
|---|---|---|---|
| Max turns | 30 | same | Checked before each model call; counts successful model responses. v1 resume reconstructs it from the log. |
| Max total tokens | 2,000,000 | same | Cumulative over the session; v1 resume reconstructs it from the log. A retry that succeeds counts once. |
| Max context tokens | 200,000 provisional | same | Per call. Acceptance runs set this explicitly to the confirmed model window. |
| Max output tokens | 8,192 | same | Per call. |
| Safety margin | `max(5% of context, 1,000)` | same | Reserved from the context window for estimator error. |
| Max input tokens | `context - output - margin` | same | Per call. |
| Compaction target | max input tokens | same | Older tool-result bodies are elided to this target. |
| Session wall clock | 60 minutes | same flag, per run | Checked before each turn; v1 resume starts a fresh clock. |
| Model call timeout | 300 seconds | same | Per call; fixed in v0, not a CLI flag. |
| HTTP client timeout | 5 minutes | same | Outer bound on a provider request, including body reads. |
| Transport attempts | 5 total (initial + 4 retries) | same | 429, 5xx, transport failures, and a per-call timeout while the session context is alive. |
| Retry backoff | 1s base, 30s max, 25% jitter | same | Context-aware sleep. |
| `Retry-After` cap | 60 seconds | same | Longer values are clamped. |
| Context-overflow retry | 1 | same | Rebuilds against 90% of the failed per-call input limit. |
| Consecutive output-length stops | 2 | same | Two `length` responses without a tool round stop with `token_limit`. |
| Repeated-failure hint | 3 | same | Same tool, canonical arguments, and result code. |
| Repeated-failure stop | 5 | same | Same identity as the hint. |
| Retained verbatim turns | 4 | same | Most recent model-response turns in the projection. |
| Token estimate | `ceil(bytes/4) + 8/message + 16/tool schema` | same | Conservative; provider usage is authoritative for totals. |

## 2. Provider transport and streaming

| Limit | v0 current | v1 planned | Behavior |
|---|---|---|---|
| Provider response body | 1 MiB | same | Non-streaming response body; larger is `protocol_error`. |
| Provider error body | 4 KiB | same | Redacted, UTF-8 sanitized, and capped before logging. |
| SSE event | — | 4 MiB | A terminal `response.completed` event wraps the full response. |
| Assembled canonical response | — | 1 MiB | Text plus tool calls; larger is `protocol_error` before any tool executes. |
| Total stream | — | 8 MiB | Bounds deltas, keepalives, and the terminal event. |
| Partial text on interrupt or failed stream | — | 64 KiB | Diagnostics only; never projected. |
| Model request debug event | 1 MiB | same | Only when `AHARNESS_LOG_REQUESTS=1`; over-cap is replaced by a truncation marker. |
| Error event message | 2 KiB | same | Fatal error class and message; no secrets. |
| Panic message | 1 KiB | same | Recovered tool panic message. |
| Panic stack | 4 KiB | same | Recovered tool panic stack. |
| Final event text | 64 KiB | same | Summary in the log; stdout may print the full response up to the response cap. |
| Task or task-file effective text | 64 KiB | same | The file is read without a size cap; the recorded/projected text is capped with a truncation marker. |
| Steering line | — | 64 KiB | One stdin line; longer lines are rejected on stderr. |
| Session line | 1 MiB + 64 KiB (1,114,112 bytes) | 4 MiB | Reader rejects longer lines; a torn final line is tolerated. |
| Tool result output/error | 64 KiB per field | same | Tool-specific caps below are usually smaller. |
| `model_response` payload | 64 KiB per text/ID/name field; tool arguments bounded by the line cap | 1 MiB total | v1 guarantees the executed response is fully recorded. |

v0 does not field-cap tool-call arguments; an argument that pushes the
`model_response` line over the session line cap can leave the log unreadable.
v1's 1 MiB total `model_response` cap closes that gap by rejecting the response
before any tool executes.

Other event payloads:

| Limit | v0 current | v1 planned | Behavior |
|---|---|---|---|
| `session_start`/`run_start` | no separate cap | same | Bounded by the session line cap. |
| `tool_result.details` | no field cap | same | Bounded by the session line cap; panic details are 4 KiB. |
| `interrupt`/`error` partial text | — | 64 KiB | Diagnostics only; never projected. |
| `session_repair` | no separate cap | same | Bounded by the session line cap. |
| `compaction`/`limit` | no separate cap | same | Bounded by the session line cap. |

## 3. Session log and filesystem

| Limit | v0 current | v1 planned | Behavior |
|---|---|---|---|
| Session line | 1 MiB + 64 KiB | 4 MiB | Includes the JSON envelope; see §2 for event payload caps. |
| Session file mode | 0600 | same | Created with `O_EXCL`; state directory is 0700. |
| State directory mode | 0700 | same | Created if needed. |
| Work directory mode | 0700 | same | Per session; contains child `HOME` and `TMPDIR`. |
| Session log total size | no cap | no cap | Grows with the session; no rotation or retention policy. |
| State directory total size | no cap | no cap | Bounded by logs and transient work directories. |
| Task/task-file read size | no cap | no cap | Effective transcript text is capped at 64 KiB. |
| Session lock | — | exclusive `flock` on resume, shared on fork | A busy session is a config error. |
| Session ID | UUIDv7 | same | Validated before path construction. |
| Work directory cleanup | on termination | at run start and end | Best-effort; a stale directory is cleared before reuse. |
| Retention | none | none | Old session logs remain until removed by the operator. |

## 4. Tools

| Tool | Limit | Value | Behavior |
|---|---|---|---|
| `read_file` | Default lines | 100 | `offset` is 1-based. |
| `read_file` | Max lines per call | 500 | Larger `limit` values are clamped. |
| `read_file` | Max file size | 2 MiB | Larger files are `too_large`. |
| `read_file` | Result cap | 32 KiB | Head and tail are kept with a truncation marker. |
| `read_file` | Binary/non-UTF-8 | rejected | `binary_file`. |
| `edit_file` | Max resulting file size | 2 MiB | Existing and new files; larger is `too_large`. |
| `edit_file` | New file mode | 0644 | Existing mode is preserved, including setuid/setgid/sticky bits. |
| `edit_file` | Match rule | unique unless `replace_all` | Zero matches is `not_found`; multiple is `ambiguous`. |
| `edit_file` | Symlinks and `.git` | rejected | A symlink anywhere in the path is rejected. |
| `search` | Default results | 100 | `max_results` is clamped to 200. |
| `search` | Max results | 200 | Capped results are marked. |
| `search` | Max file size | 1 MiB | Larger files are skipped. |
| `search` | Reported line length | 512 bytes | Longer lines are clipped with `...`. |
| `search` | Result cap | 32 KiB | Combined result text; truncated flag is set. |
| `search` | Skipped entries | `.git`, symlinks, binaries, files >1 MiB | Search requires a real directory root. |
| `run_command` | Default timeout | 120 seconds | Per call. |
| `run_command` | Max timeout | 600 seconds | Larger values are clamped. |
| `run_command` | Output cap | 64 KiB combined | Head and tail of stdout and stderr, with a truncation marker. |
| `run_command` | `WaitDelay` | 5 seconds | Unblocks inherited pipes after cancellation. |
| `run_command` | Process group | whole group killed | On timeout or cancellation. |
| `run_command` | stdin | nil | Commands cannot wait for interactive input. |
| `run_command` | Environment | allowlist | `PATH`, `LANG`, `LC_ALL`, `TERM`, harness `HOME`, harness `TMPDIR`; names ending in `_TOKEN`, `_SECRET`, `_KEY`, or `_PASSWORD` are denied. |
| File tools | Per-call timeout | no separate cap | Bounded by the session deadline and file/result caps. |
| `search` | Walk time | no separate cap | Bounded by the session deadline and result cap. |
| Tool calls | Automatic retry | never | Side effects are not idempotent; the model decides. |

## 5. CLI, input, and configuration

| Limit | v0 current | v1 planned | Behavior |
|---|---|---|---|
| `-max-turns` | 30 | same | Must be positive. |
| `-max-total-tokens` | 2,000,000 | same | Must be positive; cumulative. |
| `-max-context-tokens` | 200,000 provisional | same | Must be positive; explicit value required for acceptance. |
| `-max-output-tokens` | 8,192 | same | Must be positive; output plus safety margin must be less than context. |
| `-session-timeout` | 60 minutes | same | Must be positive; v1 applies it per run. |
| Model call timeout | 300 seconds | same | Fixed; no CLI flag. |
| `-task` / `-task-file` | no read cap | same | Effective transcript text is 64 KiB. |
| Steering line | — | 64 KiB | `-interactive` stdin line. |
| Session selection | — | UUIDv7 ID | `-resume`/`-fork` resolve under the state directory; no arbitrary paths. |
| Provider token | no length cap | same | In memory only; never logged or passed to tools. |
| Provider base URL | no length cap | same | Must be http(s) with a host; v1 rejects userinfo and query/fragment. |
| State directory path | OS path limit | same | No explicit application cap. |
| Session log retention | none | none | No rotation or automatic deletion. |

## 6. No cap or no separate cap

These are intentionally bounded only by an enclosing limit, not by a dedicated
constant:

- Total session log size and total state-directory size.
- `-task`/`-task-file` read size; the transcript truncates at 64 KiB.
- Search walk time and file-tool execution time; the session deadline applies.
- Command length, `old_string`/`new_string` length, and search pattern/glob
  length; these are indirectly bounded by the model-response cap and tool
  result caps.
- Number of tool calls per response; bounded by the response cap.
- Number of events per session; bounded by turn, token, and time budgets.
- Number of concurrent sessions; one process per session, no global cap.
- Child process memory and CPU; external isolation is the boundary.
- Workspace size; file tools enforce their own file-size caps.
- Provider token length; held in memory only.
- Session retention; old logs remain until the operator removes them.

## 7. v1 planned changes

The v1 plan changes these values; see [v1-build.md](v1-build.md) §2.3 and §2.6.

| Limit | v1 value |
|---|---|
| SSE event | 4 MiB |
| Assembled canonical response | 1 MiB; over-cap is `protocol_error` before tool execution |
| Total stream | 8 MiB |
| `model_response` log payload | 1 MiB total |
| Session line | 4 MiB |
| Steering line | 64 KiB |
| Partial text on interrupt/failed stream | 64 KiB |
| Session wall clock | per run |
| Turns and tokens | still session-cumulative |
| Session lock | exclusive on resume, shared on fork |
| v0 session logs | rejected; no migration |
