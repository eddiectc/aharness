# aharness limits and timeouts

- **Date:** 2026-11-18
- **Status:** current for the aharness SDK: the root facade and the `agent`,
  `engine`, `model`, `tools`, and `session` packages, including custom tools,
  model discovery, thinking effort, web search, and delegated subagents.
- **Related:** [architecture](architecture.md),
  [coding standard](coding-standard.md), and
  [testing strategy](testing-strategy.md). The first-party coding agent, acode,
  documents its host/host caps in its own repository.

This is the single reference for caps, limits, and timeouts. Units are bytes,
tokens, or durations as noted. A value of **—** means the limit does not exist.
"No separate cap" means the value is bounded only by an enclosing limit (an
explicit session deadline, the session line cap, disk, or process memory), not
by its own constant.

## 1. Session budgets and timeouts

| Limit | v1 value | Behavior |
|---|---|---|
| Max turns | 0 (unlimited) | Opt-in `Config.MaxTurns`. When explicit and positive, checked before each model call; counts successful model responses. Resume reconstructs cumulative turns from the log but does not inherit the budget. |
| Max total tokens | 0 (unlimited) | Opt-in `Config.MaxTotalTokens`. When explicit and positive, cumulative over the session; a retry that succeeds counts once. Recorded budgets are historical and are not inherited. |
| Max context tokens | Catalog `limit.context` | Per call. `Limits.MaxContextTokens` defaults to 0 (unset); an explicit positive value is clamped down to the model ceiling with a warning. |
| Max output tokens | Unset (0) | Per call. No harness cap by default; the request omits `max_tokens` / `max_output_tokens`. An explicit positive value is sent unchanged; `0` clears an inherited budget; a negative value is invalid. Set `Limits.MaxOutputTokens`. |
| Advertised output ceiling | Catalog `limit.output` | Display/validation metadata only; never a request budget and never an eligibility gate. |
| Safety margin | `max(5% of context, 1,000)` | Reserved from the context window for estimator error. |
| Max input tokens | `min(model limit.input, context - safety_margin)` | Per call. When the catalog has no positive input limit, `context - safety_margin`. No output budget is subtracted. |
| Compaction target | max input tokens | Older tool-result bodies are elided to this target. |
| Catalog URL | `https://models.dev/api.json` | `CatalogConfig.URL` overrides the default for tests and mirrors; HTTPS except loopback HTTP. |
| Catalog request timeout | 15 seconds total (`CatalogConfig.Timeout`) | Includes body reads; must be positive; no automatic retry. |
| Catalog response body | 16 MiB | Larger is a protocol error, not a partial parse. |
| Catalog cache TTL | 24 hours | A fresh cache is used without a network request; a stale cache triggers a conditional GET. |
| Catalog stale fallback | implicit and `-list-models` only | On failure, a valid stale cache is used with a warning. A forced refresh never falls back. |
| Explicit catalog refresh | a host refresh action | Unconditional GET, reusing the catalog timeout, 16 MiB body cap, and 0600 cache policy. Failure is returned to the host; no stale fallback. |
| Catalog cache file | `<state>/cache/models.dev/<provider>.json` | File 0600, directory 0700; atomic replacement. |
| Catalog cache version | 3 | A version mismatch or corrupt entry is ignored and treated as absent. Version 2 carries normalized `reasoning`/`reasoning_efforts`; version 3 adds the optional normalized `cost` snapshot. Older caches are refetched. |
| Model ID | at most 256 bytes | Valid UTF-8 with no control characters; used only as a JSON value/map key, never a path. |
| Thinking effort value | at most 32 bytes | Non-empty valid UTF-8 matching `[a-z][a-z0-9_-]*`; `default` is reserved for clearing and is never a catalog value. Applies to the catalog, cache, and host configuration. |
| Effort values per model | 16 | Extra values are dropped after canonical ordering; invalid values are dropped. A model with none advertises no effort control. |
| Discovered base URL | HTTPS `opencode.ai` | Host must be `opencode.ai` with no userinfo, query, or fragment; an explicit `ModelSpec.BaseURL` wins. |
| Session wall clock | 0 (none) | Opt-in `Config.RunTimeout`; per run, checked before each turn. `0` or an omitted value imposes no deadline; a parent context deadline may still stop the run. |
| Model call timeout | 0 (none) | Opt-in `Config.ModelTimeout`; per model attempt. `0` or an omitted value imposes no harness deadline; the HTTP client timeout remains the outer network bound. |
| HTTP client timeout | 15 minutes (adapter HTTP timeout) | Outer bound on a provider request, including body reads. Must be positive; a smaller value fails a stalled request sooner, a larger one tolerates a long generation. |
| Transport attempts | 5 total (initial + 4 retries) | 429, 5xx, transport failures, and an explicit `Config.ModelTimeout` expiry while the run context is alive. |
| Retry backoff | 1s base, 30s max, 25% jitter | Context-aware sleep. |
| `Retry-After` cap | 60 seconds | Longer values are clamped. |
| Streaming retry | only before the first display delta | A retry after output would duplicate stdout and could duplicate tool calls. |
| Context-overflow retry | 1 | Rebuilds against 90% of the failed per-call input limit. |
| Consecutive output-length stops | 2 | Two `length` responses without a tool round stop with `token_limit`. |
| Repeated-failure hint | 3 | Same tool, canonical arguments (the display-only `intent` is ignored), and result code. |
| Repeated-failure stop | 5 | Same identity as the hint. |
| Retained verbatim turns | 4 | Most recent model-response turns in the projection. |
| Token estimate | `ceil(bytes/4) + 8/message + 16/tool schema` | Conservative; provider usage is authoritative for totals. |

Existing sessions keep their recorded context, output, and base URL values;
new sessions use the catalog. Recorded `max_turns`, `max_total_tokens`,
`session_timeout_ms`, and `model_timeout_ms` are historical metadata and are
not inherited on resume/fork; only an explicit positive flag enforces a run
budget. When `Config.MaxTotalTokens` is explicit, a 1,000,000-token context can
consume that cumulative budget quickly.

Thinking effort introduces no run budget. It is bounded only by the model's
catalog list, the 32-byte per-value cap, and the 16-value normalization cap.
Reasoning tokens are output tokens: they count against `Limits.MaxOutputTokens`
when that flag is set and against total usage, and they add no separate cap.

## 2. Provider transport and streaming

| Limit | v1 value | Behavior |
|---|---|---|
| Non-streaming response body | 1 MiB | Larger is `protocol_error`. |
| Provider error body | 4 KiB | Redacted, UTF-8 sanitized, and capped before logging. |
| SSE event | 4 MiB | A terminal `response.completed` event wraps the full response. |
| Assembled canonical response | 1 MiB | Text plus tool calls; larger is `protocol_error` before any tool executes. |
| Total stream | 8 MiB | Bounds deltas, keepalives, and the terminal event. |
| Thinking text | 64 KiB per response | Display-only reasoning retained for a host; larger text is dropped from the display and never logged. |
| Reasoning continuation | 4 MiB per response; 16 MiB per adapter | Adapter-owned, in-memory, never logged. Chat replays thinking as `reasoning_content`; Responses replays raw reasoning output items (encrypted content or plaintext `reasoning_text`). Dropped when the adapter (one run) is discarded. |
| Partial text on interrupt or failed stream | 64 KiB | Diagnostics only; never projected. |
| Model request debug event | 1 MiB | Only when `Config.LogModelRequests` is set; over-cap is replaced by a truncation marker. |
| Error event message | 2 KiB | Fatal error class and message; no secrets. |
| Panic message | 1 KiB | Recovered tool panic message. |
| Panic stack | 4 KiB | Recovered tool panic stack. |
| Final event text | 64 KiB | Summary in the log; stdout may print the full response up to the response cap. |
| Task or task-file effective text | 64 KiB | The file is read without a size cap; the recorded/projected text is capped with a truncation marker. |
| Steering message | 64 KiB | One host steering submission; longer is rejected with a notice. Steering is optional for a headless host. |
| Session line | 4 MiB | Includes the JSON envelope and escaping. |
| `model_response` payload | 1 MiB total canonical content | The executed response is fully recorded so resume can replay it. |
| Tool result output/error | 64 KiB per field | Tool-specific caps below are usually smaller. |
| Model response usage | totals plus subsets | Input/output totals; cached, cache-write, and reasoning subsets; `reported` distinguishes an absent usage object from a present one. Subsets never change `Total()`. |
| `Config.RequireUsage` | off | When set, a completed buffered response without provider usage is a `protocol_error` before any tool executes. Streaming already requires usage. Not inherited on resume/fork. |
| Cost estimate | catalog micro-USD | Observational only. `$0.15` per 1M tokens is stored as `150000` micro-USD; per-response and run totals use integer arithmetic. Unpriced responses make the completeness flags false. |

Other event payloads:

| Limit | v1 value | Behavior |
|---|---|---|
| `session_start`/`run_start`/`run_end` | no separate cap | Bounded by the session line cap. |
| `tool_result.details` | no field cap | Bounded by the session line cap; panic details are 4 KiB. |
| `interrupt`/`error` partial text | 64 KiB | Diagnostics only; never projected. |
| `session_repair` | no separate cap | Bounded by the session line cap. |
| `compaction`/`limit` | no separate cap | Bounded by the session line cap. |

## 3. Session log and filesystem

| Limit | v1 value | Behavior |
|---|---|---|
| Schema version | 2 | The reader accepts only `v=2`; v0 (`v=1`) and unknown versions are rejected. |
| Session file mode | 0600 | Created with `O_EXCL`; resume/fork verify it. |
| State directory mode | 0700 | Created if needed. |
| Work directory mode | 0700 | Per session; contains child `TMPDIR` and command-output spill; removed and recreated at every run start. |
| Session log total size | no cap | Grows with the session; no rotation or retention policy. |
| State directory total size | no cap | Bounded by logs and transient work directories. |
| Task/task-file read size | no cap | Effective transcript text is capped at 64 KiB. |
| Session lock | exclusive `flock` on resume, shared on fork | The lock is the session log file itself; a busy session is a config error. |
| Session ID | UUIDv7 | Validated before path construction; resolves only under `<state>/sessions`. |
| Torn final line | repaired on resume | Truncated and recorded as `session_repair`; a decodable invalid final line refuses resume. |
| Valid final line without newline | terminated with one | The line is not rewritten. |
| Retention | none | Old session logs remain until removed by the operator. |

## 4. Tools

| Tool | Limit | Value | Behavior |
|---|---|---|---|
| All tools | Intent argument | 200 bytes displayed | Shared `intent` string: one short sentence naming why the call is needed. Required by every current schema, accepted but never acted on by the tools, trimmed and cut on a rune boundary for a host title. A missing intent never fails the call. |
| `todo` | Items per list | 50 | A larger `items` array is `invalid_args`; items are never silently dropped. |
| `todo` | Item content | 500 bytes | Measured after trimming, control replacement, and whitespace collapse. A longer item is `invalid_args`. |
| `todo` | In-progress items | 1 | A second `in_progress` item is `invalid_args`. |
| `todo` | State lifetime | session | In-memory during a run, seeded at run start from the last successful `todo` result's `details` in the session log, so resume, fork, and follow-up runs keep the list. Not a workspace file and not a durable event of its own. The full list is returned after every call. |
| `read_file` | Default lines | 2,000 | `offset` is 1-based. |
| `read_file` | Max lines per call | 4,000 | Larger `limit` values are clamped. |
| `read_file` | Max file size | 2 MiB | Larger files are `too_large`. |
| `read_file` | Result cap | 60 KiB | One contiguous line window; never head+tail. The header names the delivered range and a non-final page ends with `continue with offset=N`. A byte-capped page keeps whole lines; a single over-budget line gets a prefix marker with no continuation. |
| `read_file` | Binary/non-UTF-8 | rejected | `binary_file`. |
| `edit_file` | Max resulting file size | 2 MiB | Existing and new files; larger is `too_large`. |
| `edit_file` | New file mode | 0644 | Existing mode is preserved, including setuid/setgid/sticky bits. |
| `edit_file` | Match rule | unique unless `replace_all` | Zero matches is `not_found`; multiple is `ambiguous`. |
| `edit_file` | Symlinks and `.git` | rejected | A symlink anywhere in the path is rejected. |
| `search` | Default results | 100 | `max_results` is clamped to 200. |
| `search` | Max results | 200 | Capped results are marked. |
| `search` | Max file size | 1 MiB | Larger files are skipped. |
| `search` | Reported line length | 512 bytes | Longer lines are clipped with `...`. |
| `search` | Result cap | 60 KiB | Head-only combined result text. Dropped bytes set `truncated`; a result-count cap is its own `[results capped at N]` marker and does not. |
| `search` | Skipped entries | `.git`, symlinks, binaries, files >1 MiB | Search requires a real directory root; skipped counts are reported by reason in one summary line. |
| `search` | Glob | Go `path.Match` per segment | `**` matches zero or more whole path segments; `*` and `?` do not cross `/`. |
| `web_search` | Default results | 5 | `max_results` defaults to 5. |
| `web_search` | Max results | 10 | Larger values are clamped after URL filtering and deduplication. |
| `web_search` | SearXNG response body | 2 MiB | Larger is `too_large`; no partial parse. |
| `web_search` | Result cap | 60 KiB | Head-only output. Dropped results or bytes set `truncated` and append `[output truncated at 61440 bytes]`; the header count is the number actually delivered. |
| `web_search` | Timeout | 15 seconds | Fixed; covers connect, headers, and body. The tool context cancels the request. |
| `web_search` | Snippet length | 1,200 characters | Mirrors SearXNG's normalized `content` cap; longer snippets are cut on a rune boundary. |
| `web_search` | Result URL | http/https only | Non-http(s) URLs and URLs with userinfo are skipped; fragments are stripped for deduplication. |
| `web_search` | Unresponsive warning | 200 characters | One line naming engines; it does not fail the search. |
| `web_search` | Requests per call | 1 | No automatic retry, pagination loop, or provider fallback. |
| `web_fetch` | Response body | 2 MiB | After decompression. A larger declared `Content-Length` is rejected before reading; a streamed body is rejected at the cap. `too_large`. |
| `web_fetch` | Response header | 64 KiB | `Transport.MaxResponseHeaderBytes`; larger is a transport error (`fetch_failed`). |
| `web_fetch` | Text output per call | 60 KiB | Byte slice of sanitized UTF-8 text; `offset` continues. A byte-capped page sets `truncated` and appends `[truncated at 61440 bytes; continue with offset=N]`. |
| `web_fetch` | Redirects | 5 followed | Builtin only. Same-host hops are revalidated and followed; HTTPS-to-HTTP and cross-host hops are returned as successful data, not followed. |
| `web_fetch` | Request timeout | 30 seconds, max 120 | `WebFetchConfig.Timeout`; covers connect, redirects, and body reads. |
| `web_fetch` | URL length | 8 KiB | Larger is `invalid_args`. Scheme, host, userinfo, control, and fragment rules apply to every hop. |
| `web_fetch` | Provider response | 16 MiB | Crawl4AI envelope; stream-decoded while skipping unused fields. `results[0].html` is capped at 2 MiB. |
| `web_fetch` | Compression | gzip | Go's transparent gzip handling. An unsupported content encoding is `unsupported_content_type`. |
| `web_fetch` | Address guard | builtin | Every resolved address must be globally routable unicast; any blocked address refuses the URL. The check repeats at dial time and on every hop. |
| `web_fetch` | Redirect result | data | A stopped redirect returns success with `redirect: true`, `redirect_url`, and the 3xx status; the model decides whether to fetch it. |
| `web_fetch` | Requests per call | 1 | No automatic retry against the builtin path after a provider failure. |
| `run_command` | Default timeout | 120 seconds | Per call. |
| `run_command` | Max timeout | 600 seconds | Larger values are clamped. |
| `run_command` | Output cap | 64 KiB combined | Head and tail of stdout and stderr, with a truncation marker. Larger combined output is spilled to a per-run `<state>/work/<session-id>/tool-output/*.log` file (0600, directory 0700) and the absolute path is appended inline. |
| `run_command` | `WaitDelay` | 5 seconds | Unblocks inherited pipes after cancellation. |
| `run_command` | Process group | whole group killed | On timeout, cancellation, or interactive interrupt. |
| `run_command` | stdin | nil | Commands cannot wait for interactive input. |
| `run_command` | Environment | allowlist | `PATH`, `LANG`, `LC_ALL`, `TERM`, inherited `HOME`, harness `TMPDIR`; names ending in `_TOKEN`, `_SECRET`, `_KEY`, or `_PASSWORD` are denied. |
| File tools | Per-call timeout | no separate cap | Bounded by any explicit run deadline and the file/result caps. |
| `search` | Walk time | no separate cap | Bounded by any explicit run deadline and the result cap. |
| Toolset version | `toolset-v2` for new sessions | `toolset-v0` and `toolset-v1` declarations are frozen for replay; `toolset-v2` adds `delegate` to v1. Per-role grants are recorded session data, not new versions. An unknown recorded version is a replay error, and an older session cannot delegate. |
| Prompt version | `prompt-v3` for new sessions | `prompt-v0`, `prompt-v1`, and `prompt-v2` stay frozen for replay. The resolved `system_prompt` is recorded and capped at 32 KiB. Truncation is reported as `agent.State.PromptCapped` and `aharness.State.PromptCapped`; hosts must not match the marker, whose format is internal. |
| Prompt identity | `aharness` default | 64 bytes of valid UTF-8, trimmed, with no control characters. Used in the parent and child identity lines; not recorded, so it is re-supplied like `UserAgent`. |
| Prompt tag | unset | At most 64 bytes of printable ASCII matching `[A-Za-z0-9._:/-]+`; empty means unset. Recorded as optional `session_start.prompt_tag` beside `prompt_version`; attribution only, never a replay key, and it never changes rendering or caps. |
| Prompt append | unset | Host text composed into the prompt and recorded as optional `session_start.appended_prompt` under the same 32 KiB prompt cap. Empty on logs written before the field and on delegated children. Provenance only, never a replay key, carried into every fresh child's prompt; after truncation it is not necessarily a suffix of `system_prompt`. |
| `delegate` | Tasks per call | 4 | The schema sets `maxItems: 4`; a larger array is `invalid_args`. Results always appear in array order. |
| `delegate` | Concurrent children per call | 4 | A call runs concurrently only when every task's effective grant is a subset of `read_file`, `search`, `web_search`, and `web_fetch`; any write-capable task forces the whole call sequential. The cap matches `maxItems`. |
| `delegate` | Recorded execution mode | `concurrent` or `sequential` in `details` | `execution_reason` explains a sequential choice. A call that cannot reserve every fresh child under `DelegationConfig.MaxSubagents` runs sequentially so the rejected spawn is deterministic. |
| `delegate` | `task` field | 16 KiB | A longer self-contained prompt is `invalid_args`; it is a delegation prompt, not a document. |
| `delegate` | Child report | 16 KiB | Head-capped with `[truncated N bytes]`, marked once when instruction-shaped, and never rewritten. An empty report becomes `(no report)`. |
| `delegate` | `details` | 4 KiB | Exceeding it is `details_too_large`: the spawner controls the size. |
| `delegate` | Child turns | Inherited, not fixed | The definition's `max_turns` when present, else the parent's explicit `Config.MaxTurns`, else unlimited. A child stopped by a declared cap reports `status=max_turns`, `code=subagent_limit`, and its partial text. |
| `delegate` | Child tokens | Parent's remaining budget | Always the parent's `Config.MaxTotalTokens` remainder; unlimited when the parent is. Child tokens count against the parent's cumulative total. |
| `delegate` | Child stuck detection | 5 identical failures | Inherited unchanged from the normal loop. |
| `delegate` | Child cancellation | parent operation context | `Interrupt` cancels the child loop and any child `run_command` process group; the child log ends `interrupted` with one `final`. |
| `delegate` | Session continuation | canonical UUID | A `session` task must be a child whose recorded `parent_session` matches the caller; a sibling, parent, unknown, foreign, or duplicate ID is `invalid_args`. |
| `delegate` | Progress events | 1 per child response, 1 per second per child | Counters and names only; never child text, arguments, or tool output. |
| `delegate` | Subagents per run | 8 default; -1 unlimited | `DelegationConfig.MaxSubagents`; 0 selects the default of 8. A fan-out guard, not a spend budget. |
| Agent definition | File size | 64 KiB | Larger files are skipped with a warning. |
| Agent definition | Name | 64 bytes, `[a-z][a-z0-9-]*` | Stable identifier in logs and prompts. |
| Agent definition | Description | 200 bytes | Billed on every parent call through the catalog. |
| Agent definition | Prompt body | 16 KiB | Recorded per session. |
| Agent definition | Tools | non-empty, current toolset, no `delegate` | A child with no tools cannot act, and depth is one. |
| Agent definitions | Discovered count | 32 | The catalog stays readable; the host-provided base and later user files are capped together, and later entries are ignored with a warning. |
| Agent override | Entries | 32 | One per discovered agent at most; a larger `agent_overrides` object is a config error. |
| Agent override | Model | 200 bytes | Non-empty; resolved through the catalog at spawn time. |
| Agent override | Thinking effort | 32 bytes, `[a-z][a-z0-9_-]*` | A concrete catalog effort; `default` and an empty value are rejected. |
| Agent override | `max_turns` | positive integer | No zero or negative cap; omit the key to inherit. A disabled agent remains discoverable but is not runnable. |
| Agent catalog | Prompt material | 4 KiB | One line per agent plus an `[N agents omitted]` marker. |
| Roster | Visible rows | 8 plus header and `+N more` | Keeps a host roster readable at 24 rows; more is scrollable. |
| Roster | Intent text | 200 bytes, one line | The full prompt stays in the tool-call arguments. |
| Roster | Elapsed | Host clock | Computed from the start event; not carried in events, so throttled progress cannot skew it. |
| Child view | Tool body | 12 lines / 2 KiB | A focused transcript is a view; the full result stays in the child session log. |
| Child view | Queued steering markers | 8 | Display only; the child's steering channel still holds up to 64 lines. |
| Tool calls | Automatic retry | never | Side effects are not idempotent; the model decides. |
| Custom tools | Count per session | 32 | Public `ToolsConfig.Extra`; construction rejects a larger set. |
| Custom tools | Recorded schema total | 64 KiB | Sum of name, description, and parameters across the set; larger is a construction error. |
| Custom tool | Name | 64 bytes, `[a-z0-9_-]+` | Lowercase ASCII; unique; cannot collide with any built-in (enabled or disabled) or `delegate`. |
| Custom tool | Description | 2 KiB | Required; recorded in `session_start.custom_tools`. |
| Custom tool | Parameters | 32 KiB | Required JSON Schema object; use `{}` for no arguments. |
| Custom tool | Result details | 64 KiB | Larger details are replaced by a bounded truncation marker before recording. |
| Custom tool | `ConcurrencySafe` | false default | Trusted host declaration recorded as `concurrency_safe`; permits concurrent delegated children only, never in-run parallel calls. A resume must reproduce it exactly. |
| Tool policy | `Authorize` serialization | one at a time per session | The engine's shared policy mutex covers the parent and delegated children; the hook is never entered concurrently within one session. |
| Public run | Steering queue | 16 lines | `Run.Steer` never blocks; a full queue returns `ErrSteeringFull`. Consumed at the next turn boundary. |
| Public run | Child steering queue | 64 lines | `Run.SteerChild` routes to one live child; full returns `ErrSteeringFull`, finished/unknown returns `ErrNoSuchChild`. |
| Public delegation | `MaxSubagents` | `0` selects 8; `-1` unlimited | Other negative values are a construction error; the spawner uses 8/0. |

## 5. No cap or no separate cap

Child turns have no separate fixed cap: the definition's `max_turns`, then the
parent's explicit `Config.MaxTurns`, then unlimited. The plan for subagents rejected
a hard-coded child cap because a number chosen in advance either truncates
legitimate long work or fails to stop a child that keeps making different
calls. The real bound is observation plus `Interrupt`: `SubagentProgress` is
forwarded through the parent display, a host roster can show a moving clock,
and a headless host need not grant `delegate`. Tokens are different: a child always spends
inside the parent's call, so its tokens count against `Config.MaxTotalTokens` and
cannot be used to escape the budget.


These are intentionally bounded only by an enclosing limit, not by a dedicated
constant:

- Total session log size and total state-directory size.
- Task text read size; the transcript truncates at 64 KiB.
- Search walk time and file-tool execution time; any explicit run deadline
  applies.
- Command length, `old_string`/`new_string` length, and search pattern/glob
  length; these are indirectly bounded by the model-response cap and tool
  result caps.
- Number of tool calls per response; bounded by the response cap.
- Number of events per session; bounded by the turn, token, and time budgets
  only when those explicit budgets are set.
- Thinking effort values; bounded by the model's normalized catalog list (at
  most 16 values, 32 bytes each). No run budget is added.
- Number of concurrent sessions; one process holds one active session and one
  lock at a time. A host may replace the active session while idle;
  there is no global cap.
- Child process memory and CPU; external isolation is the boundary.
- Workspace size; file tools enforce their own file-size caps.
- Provider token length; held in memory only.
- Session retention; old logs remain until the operator removes them.

## 6. Removed v0 limits

v1 replaces the v0 session contract outright. The v0 `session_end` event, the
1 MiB + 64 KiB session line, and the 64 KiB-per-field `model_response` text cap
no longer exist. v0 logs are rejected rather than migrated.
