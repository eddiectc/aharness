# aharness limits and timeouts

- **Date:** 2026-11-27
- **Status:** current for the aharness SDK root facade and the `agent`,
  `engine`, `model`, `tools`, and `session` packages.
- **Related:** [architecture](architecture.md),
  [coding standard](coding-standard.md), and
  [testing strategy](testing-strategy.md). The first-party coding agent, acode,
  documents its own host caps in its own repository.

This is the single reference for caps, limits, and timeouts. Units are bytes,
tokens, or durations as noted. A value of **—** means the limit does not exist.
"No separate cap" means the value is bounded only by an enclosing limit (an
explicit session deadline, the session line cap, disk, or process memory), not
by its own constant. A backticked identifier names the Go symbol or constant
that owns the value; the package source is authoritative when the two disagree.

## 1. Session budgets and timeouts

| Limit | Value | Behavior |
|---|---|---|
| Max turns | 0 (unlimited) | Opt-in `Config.MaxTurns`. When explicit and positive, checked before each model call; counts successful model responses. Resume reconstructs cumulative turns from the log but does not inherit the budget. |
| Max total tokens | 0 (unlimited) | Opt-in `Config.MaxTotalTokens`. When explicit and positive, cumulative over the session; a retry that succeeds counts once. Recorded budgets are historical and are not inherited. |
| Max context tokens | Catalog `limit.context` | Per call. `Limits.MaxContextTokens` defaults to 0 (unset); an explicit positive value is clamped down to the model ceiling. |
| Max output tokens | Unset (0); catalog `limit.output` is advertised only | Per call. No harness cap by default; the request omits `max_tokens` / `max_output_tokens`. An explicit positive value is sent unchanged; `0` clears an inherited budget; a negative value is a configuration error. Set `Limits.MaxOutputTokens`. The advertised ceiling is display and validation metadata only: never a request budget and never an eligibility gate. |
| Safety margin | `max(5% of context, 1,000)` (`SafetyMargin`) | Reserved from the context window for estimator error. |
| Max input tokens | `min(model limit.input, context - safety_margin)` | Per call. When the catalog has no positive input limit, `context - safety_margin`. No output budget is subtracted. |
| Compaction target | max input tokens | Older tool-result bodies are elided to this target. |
| Catalog URL | `https://models.dev/api.json` (`DefaultCatalogURL`) | `CatalogConfig.URL` overrides the default for tests and mirrors; HTTPS except loopback HTTP. |
| Catalog request timeout | 15 seconds total (`CatalogRequestTimeout`) | Includes body reads; must be positive; no automatic retry. |
| Catalog response body | 16 MiB (`MaxCatalogBytes`) | Larger is a protocol error, not a partial parse. |
| Catalog cache TTL | 24 hours (`CatalogTTL`) | A fresh cache is used without a network request; a stale cache triggers a conditional GET. |
| Catalog fallback and refresh | implicit reads and the host list action only | On failure, a valid stale cache is used with a warning. A host refresh action is an unconditional GET that reuses the catalog timeout, 16 MiB body cap, and 0600 cache policy; its failure is returned to the host with no stale fallback, and a forced refresh never falls back. |
| Catalog cache file | `<state>/cache/models.dev/<provider>.json` | File 0600, directory 0700; atomic replacement. |
| Catalog cache version | 3 (`catalogCacheVersion`) | A version mismatch or corrupt entry is ignored and treated as absent. Version 2 carries normalized `reasoning`/`reasoning_efforts`; version 3 adds the optional normalized `cost` snapshot. Older caches are refetched. |
| Model ID | at most 256 bytes (`maxCatalogModelIDBytes`) | Valid UTF-8 with no control characters; used only as a JSON value/map key, never a path. |
| Thinking effort value | at most 32 bytes (`MaxThinkingEffortBytes`) | Non-empty valid UTF-8 matching `[a-z][a-z0-9_-]*`; `default` is reserved for clearing and is never a catalog value. Applies to the catalog, cache, and host configuration. |
| Effort values per model | 16 (`MaxReasoningEfforts`) | Extra values are dropped after canonical ordering; invalid values are dropped. A model with none advertises no effort control. |
| Discovered base URL | HTTPS `opencode.ai` | Host must be `opencode.ai` with no userinfo, query, or fragment; an explicit `ModelSpec.BaseURL` wins. |
| Session wall clock | 0 (none) | Opt-in `Config.RunTimeout`; per run, checked before each turn. `0` or an omitted value imposes no deadline; a parent context deadline may still stop the run. |
| Model call timeout | 0 (none) | Opt-in `Config.ModelTimeout`; per model attempt. `0` or an omitted value imposes no harness deadline; the HTTP client timeout remains the outer network bound. |
| HTTP client timeout | 15 minutes (`DefaultHTTPTimeout`) | Outer bound on a provider request, including body reads. Must be positive; a smaller value fails a stalled request sooner, a larger one tolerates a long generation. |
| Transport attempts | 5 total (`maxModelAttempts`) | Initial call plus 4 retries for 429, 5xx, transport failures, and an explicit `Config.ModelTimeout` expiry while the run context is alive. |
| Retry backoff | 1s base, 30s max, 25% jitter (`retryBaseDelay`, `retryMaxDelay`) | Context-aware sleep. |
| `Retry-After` cap | 60 seconds (`maxRetryAfter`) | Longer values are clamped. |
| Streaming retry | only before the first display delta | A retry after output would duplicate stdout and could duplicate tool calls. |
| Context-overflow retry | 1 | Rebuilds against 90% of the failed per-call input limit. |
| Consecutive output-length stops | 2 | Two `length` responses without a tool round stop with `token_limit`. |
| Repeated-failure thresholds | hint at 3, stop at 5 | Same tool, canonical arguments (the display-only `intent` is ignored), and result code. |
| Retained verbatim turns | 4 (`retainedTurns`) | Most recent model-response turns in the projection. |
| Token estimate | `ceil(bytes/4) + 8/message + 16/tool schema` (`bytesPerTokenBits`, `tokensPerMessage`, `tokensPerTool`) | Conservative; provider usage is authoritative for totals. |

Existing sessions keep their recorded context, output, and base URL values; a
new session takes its context window and input limit from the model spec. On
Resume or Fork, an all-zero `Limits` is no override and inherits the recorded
per-call limits; any non-zero context, output, input, or compaction target makes
it an explicit override that replaces the whole recorded set. A zero
`MaxOutputTokens` in an override therefore clears the inherited output budget,
while a zero context or input limit still inherits the recorded value, clamped
to the new context. A context override below the model's input limit narrows the
input budget to `context` minus the safety margin instead of failing. Recorded
`max_turns`, `max_total_tokens`, `session_timeout_ms`, and `model_timeout_ms`
are historical metadata and are not inherited on resume/fork; only an explicit
positive flag enforces a run budget. When `Config.MaxTotalTokens` is explicit, a
1,000,000-token context can consume that cumulative budget quickly.

Thinking effort introduces no run budget. It is bounded only by the model's
catalog list, the 32-byte per-value cap, and the 16-value normalization cap.
Reasoning tokens are output tokens: they count against `Limits.MaxOutputTokens`
when that flag is set and against total usage, and they add no separate cap.

## 2. Provider transport and streaming

| Limit | Value | Behavior |
|---|---|---|
| Non-streaming response body | 1 MiB (`maxResponseBody`) | Larger is `protocol_error`. |
| Provider error body | 4 KiB (`maxErrorBody`) | Redacted, UTF-8 sanitized, and capped before logging. |
| SSE event | 4 MiB (`maxSSEEvent`) | A terminal `response.completed` event wraps the full response. |
| Assembled canonical response | 1 MiB (`maxModelResponseBytes`) | Text plus tool calls; larger is `protocol_error` before any tool executes. |
| Total stream | 8 MiB | Bounds deltas, keepalives, and the terminal event. |
| Thinking text | 64 KiB per response | Display-only reasoning retained for a host; larger text is dropped from the display and never logged. |
| Reasoning continuation | 4 MiB per response (`maxContinuationBytes`); 16 MiB per adapter (`maxContinuationTotal`) | Adapter-owned, in-memory, never logged. Chat replays thinking as `reasoning_content`; Responses replays raw reasoning output items (encrypted content or plaintext `reasoning_text`). Dropped when the adapter (one run) is discarded. |
| Partial text on interrupt or failed stream | 64 KiB (`maxPartialTextBytes`) | Diagnostics only; never projected. |
| Model request debug event | 1 MiB (`maxModelRequestBytes`) | Only when `Config.LogModelRequests` is set; over-cap is replaced by a truncation marker. |
| Error event message | 2 KiB (`maxErrorEventBytes`) | Fatal error class and message; no secrets. |
| Panic message and stack | 1 KiB (`maxPanicMessageBytes`), 4 KiB (`maxPanicStackBytes`) | Recovered tool panic text. |
| Final event text | 64 KiB (`maxEventTextBytes`) | Summary in the log; stdout may print the full response up to the response cap. |
| Task or task-file effective text | 64 KiB | The file is read without a size cap; the recorded/projected text is capped with a truncation marker. |
| Steering message | 64 KiB (`maxSteeringBytes`) | One host steering submission; longer is rejected with a notice. Steering is optional for a headless host. |
| Session line | 4 MiB (`maxEventLine`) | Includes the JSON envelope and escaping. |
| `model_response` payload | 1 MiB total canonical content | The executed response is fully recorded so resume can replay it. |
| Tool result output/error | 64 KiB per field | Tool-specific caps in §4 are usually smaller. |
| Tool result image | 512 KiB decoded (~683 KiB base64) per entry, one entry per call (`tools.MaxImageBytes`) | Produced by `read_image` and by a caller tool's `ToolResult.Image`, which is normalized to the same caps; the recorder drops an entry above the cap, and the session line cap bounds the envelope. |
| Model response usage | totals plus subsets | Input/output totals; cached, cache-write, and reasoning subsets; `reported` distinguishes an absent usage object from a present one. Subsets never change `Total()`. |
| `Config.RequireUsage` | off | When set, a completed buffered response without provider usage is a `protocol_error` before any tool executes. Streaming already requires usage. Not inherited on resume/fork. |
| Cost estimate | catalog micro-USD | Observational only. `$0.15` per 1M tokens is stored as `150000` micro-USD; per-response and run totals use integer arithmetic. Unpriced responses make the completeness flags false. |

Other event payloads have no separate cap and are bounded by the session line
cap: `session_start`, `run_start`, `run_end`, `tool_result.details` (panic
details 4 KiB), `session_repair`, `compaction`, and `limit`. `interrupt` and
`error` partial text is 64 KiB and is never projected.

## 3. Session log and filesystem

| Limit | Value | Behavior |
|---|---|---|
| Schema version | 2 (`session.Version`) | The reader accepts only `v=2`; v0 (`v=1`) and unknown versions are rejected. |
| Session file mode | 0600 | Created with `O_EXCL`; resume/fork verify it. |
| State directory mode | 0700 | Created if needed. |
| Work directory mode | 0700 | Per session; contains child `TMPDIR` and command-output spill; removed and recreated at every run start. |
| Log and state directory size | no cap | The log grows with the session and has no rotation or retention policy; the state directory is bounded by logs and transient work directories. |
| Task/task-file read size | no cap | Effective transcript text is capped at 64 KiB. |
| Session lock | exclusive `flock` on resume, shared on fork | The lock is the session log file itself; a busy session is a config error. |
| Session ID | UUIDv7 | Validated before path construction; resolves only under `<state>/sessions`. |
| Final line | repaired or terminated | A torn final line is truncated and recorded as `session_repair`; a decodable invalid final line refuses resume. A valid final line without a newline is terminated with one and is not rewritten. |
| Retention | none | Old session logs remain until removed by the operator. |

v0 session limits are gone: the `session_end` event, the 1 MiB plus 64 KiB
session line, and the 64 KiB-per-field `model_response` cap. v0 logs are
rejected rather than migrated.

## 4. Tools

Tool schemas and behavior are in [architecture §5](architecture/tools.md#5-tools);
this section owns the numbers and the semantics of a cap. `fileOutputCap` bounds
`read_file` and `search` results; `commandOutputCap` bounds `run_command`.
Schema-only parameter descriptions belong to the tool schema, not here.

| Tool or mechanism | Caps, timeouts, and defaults | Cap semantics |
|---|---|---|
| All tools | `intent` 200 bytes (`MaxIntentBytes`) | Shared string: one short sentence naming why the call is needed. Required by every current schema, accepted but never acted on by the tools, trimmed and cut on a rune boundary for a host title. A missing intent never fails the call. |
| `todo` | 50 items (`maxTodoItems`); 500 bytes per item (`maxTodoContentBytes`); 1 `in_progress` item | A larger `items` array is `invalid_args` and items are never silently dropped; length is measured after trimming, control replacement, and whitespace collapse, and a longer item is `invalid_args`; a second in-progress item is `invalid_args`. State is in-memory during a run, seeded at run start from the last successful `todo` result's `details` in the session log, so resume, fork, and follow-up runs keep the list. Not a workspace file and not a durable event of its own; the full list is returned after every call. |
| `read_file` | 2,000 lines default (`defaultReadLimit`); 4,000 max (`maxReadLimit`); 2 MiB file (`maxReadFileBytes`); 60 KiB result (`fileOutputCap`) | Returns one contiguous numbered line window, never head+tail; `offset` is 1-based and a larger `limit` is clamped. A larger file is `too_large`; binary or non-UTF-8 content is `binary_file`. The header names the delivered range and a non-final page ends with `continue with offset=N`. A byte-capped page keeps whole lines; a single over-budget line gets a prefix marker with no continuation. |
| `read_image` | 2 MiB source (`maxReadFileBytes`); 16,000,000 source pixels (`maxImageSourcePixels`); 2,048 px longest edge (`maxImageEdge`); 2,000,000 delivered pixels (`maxImagePixels`); 512 KiB delivered (`maxImageBytes`); 256 px floor (`minImageEdge`) | Source caps are checked from the header before a full decode, so a decompression bomb is `too_large`. Format is detected from the bytes, never the extension: PNG, JPEG, and GIF (first frame) are accepted, GIF is re-encoded as JPEG, and SVG, WebP, AVIF, HEIC, BMP, TIFF, and PDF are `invalid_image`. Downscale only; both delivered limits apply and the smaller scale wins. JPEG quality ladder 85/75/65/55, then 0.75 size steps to the 256 px floor, then `too_large`; a PNG or JPEG already inside every limit passes through byte for byte. The same source produces the same bytes and SHA-256, recorded in `details`. One image per call, with no array argument. The image travels in `tool_result.images`, never in `output`; `output` is one line naming the source and delivered dimensions. A delivered 2048x976 image estimates to `ceil(w/28) * ceil(h/28)` = 2,590 tokens, and the provider bills about 950. An explicit `ModelSpec.ImageInput` false returns `unavailable` without reading the file; nil lets the provider decide, a mid-session `RunOptions.Model` switch re-resolves it for that run, and a delegated child follows its own resolved model. |
| `edit_file` | 2 MiB resulting file (`maxEditFileBytes`); new file mode 0644 | The cap applies to existing and new files. New files use 0644; an existing mode is preserved, including setuid/setgid/sticky bits. A unique exact match is required unless `replace_all`; zero matches is `not_found` and multiple is `ambiguous`. A symlink anywhere in the path is rejected, as is any `.git` component. |
| `search` | 100 results default (`defaultSearchResults`); 200 max (`maxSearchResults`); 1 MiB file (`maxSearchFileBytes`); 512-byte reported line (`maxSearchLineBytes`); 60 KiB result (`fileOutputCap`) | `max_results` is clamped to 200 and capped results are marked. Files over 1 MiB are skipped, and longer reported lines are clipped with `...`. The result is head-only combined text; dropped bytes set `truncated`, while a result-count cap gets its own `[results capped at N]` marker and does not. Skips `.git`, symlinks, binaries, and large files, reporting counts by reason in one summary line; a real directory root is required. A glob matches Go `path.Match` semantics per segment, with `**` spanning zero or more whole segments and `*`/`?` not crossing `/`. |
| `web_search` | 5 results default (`defaultSearchResultCount`); 10 max (`maxSearchResultCount`); 2 MiB response (`maxSearchResponseBytes`); 60 KiB result (`maxSearchOutputBytes`); 15 s timeout (`SearchTimeout`); 1,200-char snippet (`maxSearchSnippetRunes`); 200-char unresponsive warning (`maxUnresponsiveRunes`) | No partial parse. One GET per call, with no automatic retry, pagination loop, or provider fallback; the fixed timeout covers connect, headers, and body, and the tool context cancels the request. A larger `max_results` is clamped after URL filtering and deduplication. Output is head-only; dropped results or bytes set `truncated` and append `[output truncated at 61440 bytes]`, and the header count is the number delivered. Snippets mirror SearXNG's normalized `content` cap and are cut on a rune boundary. Only absolute http(s) URLs without userinfo are kept; fragments are stripped for deduplication. An unresponsive-engine warning names engines in one line and does not fail the search. |
| `web_fetch` | 2 MiB body (`maxWebBodyBytes`); 64 KiB header (`maxWebHeaderBytes`); 60 KiB text per call (`maxWebOutputBytes`); 5 redirects (`maxWebRedirects`); 30 s default and 120 s max (`WebTimeoutDefault`, `WebTimeoutMax`), covering connect, redirects, and body reads; 8 KiB URL (`maxWebURLBytes`); 16 MiB provider response (`maxWebProviderBytes`); 4 KiB provider error body (`maxWebProviderErrorBytes`); 400-char detail; 16 origins at 256 bytes (`maxWebAllowedOrigins`, `maxWebAllowedOriginSize`) | The body cap is after decompression; a larger declared `Content-Length` is rejected before reading and a streamed body is rejected at the cap (`too_large`). The header cap is `Transport.MaxResponseHeaderBytes` and overage is a transport error (`fetch_failed`). Scheme, host, userinfo, control-character, and fragment rules apply to every hop. Text output is sanitized UTF-8 with `offset` continuation; a byte-capped page sets `truncated` and appends `[truncated at 61440 bytes; continue with offset=N]`. Only the builtin follows redirects: same-host hops are revalidated and followed; HTTPS-to-HTTP and cross-host hops are returned as successful data. A stopped redirect returns success with `redirect: true`, `redirect_url`, and the 3xx status. Every resolved address is checked at dial time and on every hop; loopback, private, and shared addresses (including IPv4-mapped and NAT64-wrapped forms) need an exact allowed origin, while metadata, link-local, unspecified, multicast, documentation, benchmarking, reserved, and protocol-assignment addresses are always refused. Each allowed origin is `scheme://host[:port]` with the default port filled and duplicates collapsed; a path, query, fragment, userinfo, control character, or non-http(s) scheme is a configuration error, and the Crawl4AI provider rejects a non-empty list. Compression is Go's transparent gzip; an unsupported content encoding is `unsupported_content_type`. The provider error body is read once for a non-2xx response; the sanitized detail is capped at 400 characters and names a JSON `detail`/`error`/`error_message`/`message` member when present. One request per call; no retry against the builtin path after a provider failure. |
| `run_command` | 120 s default (`defaultCommandTimeoutMS`); 600 s max (`maxCommandTimeoutMS`); 64 KiB combined output (`commandOutputCap`); 5 s `WaitDelay` (`commandWaitDelay`) | Larger timeouts are clamped. Output keeps head and tail with a truncation marker; oversized combined output is spilled to `<state>/work/<session-id>/tool-output/*.log` (0600 in a 0700 directory, `O_EXCL`, no symlink following) and the absolute path is appended inline. The whole process group is killed on timeout, cancellation, or interactive interrupt. `stdin` is nil. The environment is an allowlist: `PATH`, `LANG`, `LC_ALL`, `TERM`, inherited `HOME`, harness `TMPDIR`; names ending in `_TOKEN`, `_SECRET`, `_KEY`, or `_PASSWORD` are denied. |
| File tools and `search` walk | no separate timeout | Bounded by any explicit run deadline and the file/result caps. |
| Toolset version | `toolset-v3` for new sessions | `toolset-v0`, `toolset-v1`, and `toolset-v2` declarations are frozen for replay; `toolset-v3` adds `read_image` to v2. Per-role grants are recorded session data, not new versions. An unknown recorded version is a replay error, an older session cannot delegate, and a `toolset-v2` session cannot be granted `read_image`. |
| Prompt | `prompt-v3` for new sessions; 32 KiB prompt (`maxSystemPromptBytes`); 64-byte identity; 64-byte tag; 32 KiB append | `prompt-v0`, `prompt-v1`, and `prompt-v2` stay frozen for replay. The resolved `system_prompt` is recorded and capped at 32 KiB; truncation is reported as `agent.State.PromptCapped` and `aharness.State.PromptCapped`, and hosts must not match the marker, whose format is internal. Identity defaults to `aharness`, is 64 bytes of valid UTF-8, trimmed, with no control characters, and is not recorded, so it is re-supplied like `UserAgent`. The prompt tag is at most 64 bytes of printable ASCII matching `[A-Za-z0-9._:/-]+`; empty means unset, and it is recorded as optional `session_start.prompt_tag` beside `prompt_version` for attribution only, never a replay key, and it never changes rendering or caps. The appended prompt is host text recorded as optional `session_start.appended_prompt` under the same 32 KiB cap; it is empty on logs written before the field and on delegated children, is provenance only, is carried into every fresh child's prompt, and after truncation is not necessarily a suffix of `system_prompt`. |
| `delegate` tasks and reports | 4 tasks (`maxDelegateTasks`); 16 KiB task (`maxSubagentTaskBytes`); 16 KiB report (`maxSubagentReportBytes`); 4 KiB details (`maxDelegateDetails`) | The schema sets `maxItems: 4`; a larger array is `invalid_args`, and results always appear in array order. A longer task is `invalid_args`: it is a delegation prompt, not a document. The child report is head-capped with `[truncated N bytes]`, marked once when instruction-shaped, never rewritten, and an empty report becomes `(no report)`. Exceeding `details` is `details_too_large`. |
| `delegate` concurrency and sessions | 4 concurrent children (`maxConcurrentChildren`); 8 subagents per run default, -1 unlimited | A call runs concurrently only when every task's effective grant is read-only (`read_file`, `read_image`, `search`, `web_search`, `web_fetch`) or a custom tool declared `ConcurrencySafe`; any other grant forces the whole call sequential, and the cap matches `maxItems`. `details` records `concurrent` or `sequential`, with `execution_reason` when sequential. A call that cannot reserve every fresh child under `DelegationConfig.MaxSubagents` runs sequentially so the rejected spawn is deterministic. `MaxSubagents` 0 selects the default 8 and -1 is unlimited; other negative values are a construction error; it is a fan-out guard, not a spend budget. A `session` task must be a child whose recorded `parent_session` matches the caller; a sibling, parent, unknown, foreign, or duplicate canonical UUID is `invalid_args`. Progress events are 1 per child response and at most 1 per second per child, carrying counters and names only, never child text, arguments, or tool output. |
| `delegate` child budgets | Turns inherited; tokens from the parent's remainder; stuck at 5 identical failures | Turns are the definition's `max_turns` when present, else the parent's explicit `Config.MaxTurns`, else unlimited; a child stopped by a declared cap reports `status=max_turns`, `code=subagent_limit`, and its partial text. Tokens are always the parent's `Config.MaxTotalTokens` remainder, unlimited when the parent is, and count against the parent's cumulative total. Stuck detection is inherited unchanged. `Interrupt` cancels the child loop and any child `run_command` process group; the child log ends `interrupted` with one `final`. |
| Agent definition | 64 KiB file (`maxAgentDefinitionBytes`); 64-byte name (`maxAgentNameBytes`); 200-byte description (`maxAgentDescriptionSize`); 16 KiB prompt (`maxAgentPromptBytes`); 32 discovered (`maxAgents`) | Larger files are skipped with a warning. The name is a stable `[a-z][a-z0-9-]*` identifier in logs and prompts; the description is billed on every parent call through the catalog. Tools must be non-empty, in the current toolset, and cannot include `delegate`, because a child with no tools cannot act and depth is one. The host-provided base and later user files are capped together, and later entries are ignored with a warning. |
| Agent override | 32 entries (`maxAgentOverrides`); 200-byte model (`maxAgentOverrideModelBytes`); 32-byte effort (`MaxThinkingEffortBytes`); positive `max_turns` | One per discovered agent at most; a larger `agent_overrides` object is a config error. The model is non-empty and resolved through the catalog at spawn time. The effort must be a concrete catalog value; `default` and an empty value are rejected. There is no zero or negative turn cap; omit the key to inherit. A disabled agent remains discoverable but is not runnable. |
| Agent catalog | 4 KiB (`maxAgentCatalogBytes`) | One line per agent plus an `[N agents omitted]` marker. |
| Roster display | 8 visible rows plus header and `+N more`; 200-byte intent, one line | A host display cap, not an SDK constant: it keeps a roster readable at 24 rows, and more is scrollable. The full prompt stays in the tool-call arguments. Elapsed time is computed by the host from the start event and is not carried in events, so throttled progress cannot skew it. |
| Child view display | 12 lines / 2 KiB tool body; 8 queued steering markers | A host display cap, not an SDK constant. A focused transcript is a view; the full result stays in the child session log. Display only: the child's steering channel still holds up to 64 lines. |
| Tool calls | automatic retry never | Side effects are not idempotent; the model decides. |
| Custom tools | 32 per session (`MaxCustomTools`); 64 KiB recorded schema total (`MaxCustomToolBytes`); 64-byte name (`MaxToolNameBytes`); 2 KiB description (`MaxCustomToolDescription`); 32 KiB parameters (`MaxCustomToolParams`) | Public `ToolsConfig.Extra`; construction rejects a larger set or schema total. The name is lowercase ASCII matching `[a-z0-9_-]+`, unique, and cannot collide with any built-in (enabled or disabled) or `delegate`. The description is required and recorded in `session_start.custom_tools`; parameters are a required JSON Schema object, `{}` for no arguments. A resume must reproduce the declarations exactly. Result `Details` is capped at 64 KiB by the root facade, and larger details are replaced by a bounded truncation marker before recording. |
| Custom tool `ConcurrencySafe` | false default | Trusted host declaration recorded as `concurrency_safe`; permits concurrent delegated children only, never in-run parallel calls. A resume must reproduce it exactly. |
| Tool policy | `Authorize` one at a time per session | The engine's shared policy mutex covers the parent and delegated children; the hook is never entered concurrently within one session. |
| Steering queues | 16 lines root (`Run.Steer`), 64 lines child | `Run.Steer` never blocks; a full root queue returns `ErrSteeringFull` and is consumed at the next turn boundary. `Run.SteerChild` routes to one live child; full returns `ErrSteeringFull`, finished or unknown returns `ErrNoSuchChild`. |

## 5. No cap or no separate cap

Child turns have no separate fixed cap: the definition's `max_turns`, then the
parent's explicit `Config.MaxTurns`, then unlimited. A hard-coded child cap was
rejected because a number chosen in advance either truncates legitimate long
work or fails to stop a child that keeps making different calls. The real bound
is observation plus `Interrupt`: `SubagentProgress` is forwarded through the
parent display, a host roster can show a moving clock, and a headless host need
not grant `delegate`. Tokens are different: a child always spends inside the
parent's call, so its tokens count against `Config.MaxTotalTokens` and cannot
be used to escape the budget.

These are intentionally bounded only by an enclosing limit, not by a dedicated
constant:

- Total session log size and total state-directory size.
- Task text read size; the transcript truncates at 64 KiB.
- Search walk time and file-tool execution time; any explicit run deadline
  applies.
- Command length, `old_string`/`new_string` length, and search pattern/glob
  length; these are indirectly bounded by the model-response cap and tool
  result caps.
- Number of tool calls per response, bounded by the response cap; number of
  events per session, bounded by the turn, token, and time budgets only when
  those explicit budgets are set.
- The child observation sink; it is synchronous, unbuffered, and per child, so a
  blocking sink throttles that child's model stream and no other child. A host
  that wants a bounded queue owns it.
- Thinking effort values; bounded by the model's normalized catalog list (at
  most 16 values, 32 bytes each). No run budget is added.
- Number of concurrent sessions; one process holds one active session and one
  lock at a time. A host may replace the active session while idle; there is no
  global cap.
- Child process memory and CPU; external isolation is the boundary.
- Workspace size; file tools enforce their own file-size caps.
- Provider token length; held in memory only.
- Session retention; old logs remain until the operator removes them.
