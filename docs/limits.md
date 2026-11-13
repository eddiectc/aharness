# aharness limits and timeouts

- **Date:** 2026-11-13
- **Status:** current for v1, the shipped TUI, model discovery, thinking
  effort, web search, and the flag/env/config-file configuration layers.
- **Related:** [architecture](architecture.md), [configuration](configuration.md),
  [v1 build plan](plans/v1-build.md),
  [TUI build plan](plans/tui-build.md), [shipped TUI](tui.md),
  [slash commands](commands.md),
  [model discovery build plan](plans/model-discovery-build.md),
  [thinking effort build plan](plans/thinking-effort-build.md),
  [web search build plan](plans/web-search-build.md),
  [coding standard](coding-standard.md).

This is the single reference for caps, limits, and timeouts. Units are bytes,
tokens, or durations as noted. A value of **—** means the limit does not exist.
"No separate cap" means the value is bounded only by an enclosing limit (an
explicit session deadline, the session line cap, disk, or process memory), not
by its own constant.

## 1. Session budgets and timeouts

| Limit | v1 value | Behavior |
|---|---|---|
| Max turns | 0 (unlimited) | Opt-in `-max-turns` / `AHARNESS_MAX_TURNS` / `max_turns`. When explicit and positive, checked before each model call; counts successful model responses. Resume reconstructs cumulative turns from the log but does not inherit the budget. |
| Max total tokens | 0 (unlimited) | Opt-in `-max-total-tokens` / `AHARNESS_MAX_TOTAL_TOKENS` / `max_total_tokens`. When explicit and positive, cumulative over the session; a retry that succeeds counts once. Recorded budgets are historical and are not inherited. |
| Max context tokens | Catalog `limit.context` | Per call. `-max-context-tokens` / `AHARNESS_MAX_CONTEXT_TOKENS` / `max_context_tokens` defaults to 0 (unset); an explicit positive value is clamped down to the model ceiling with a warning. |
| Max output tokens | Unset (0) | Per call. No harness cap by default; the request omits `max_tokens` / `max_output_tokens`. An explicit positive value is sent unchanged; `0` clears an inherited budget; a negative value is invalid. Accepts `-max-output-tokens`, `AHARNESS_MAX_OUTPUT_TOKENS`, and `max_output_tokens`. |
| Advertised output ceiling | Catalog `limit.output` | Display/validation metadata only; never a request budget and never an eligibility gate. |
| Safety margin | `max(5% of context, 1,000)` | Reserved from the context window for estimator error. |
| Max input tokens | `min(model limit.input, context - safety_margin)` | Per call. When the catalog has no positive input limit, `context - safety_margin`. No output budget is subtracted. |
| Compaction target | max input tokens | Older tool-result bodies are elided to this target. |
| Catalog URL | `https://models.dev/api.json` | `AHARNESS_MODELS_DEV_URL` / `models_dev_url` override for tests and mirrors; HTTPS except loopback HTTP. |
| Catalog request timeout | 15 seconds total (`-catalog-timeout` / `AHARNESS_CATALOG_TIMEOUT` / `catalog_timeout`) | Includes body reads; must be positive; no automatic retry. |
| Catalog response body | 16 MiB | Larger is a protocol error, not a partial parse. |
| Catalog cache TTL | 24 hours | A fresh cache is used without a network request; a stale cache triggers a conditional GET. |
| Catalog stale fallback | implicit and `-list-models` only | On failure, a valid stale cache is used with a warning. Explicit refresh never falls back. |
| Explicit catalog refresh | `-refresh-models` or picker `Ctrl-R` | Unconditional GET, reusing the catalog timeout, 16 MiB body cap, and 0600 cache policy. Failure exits 3/4 (CLI) or notifies (TUI); no stale fallback. |
| Catalog cache file | `<state>/cache/models.dev/<provider>.json` | File 0600, directory 0700; atomic replacement. |
| Catalog cache version | 3 | A version mismatch or corrupt entry is ignored and treated as absent. Version 2 carries normalized `reasoning`/`reasoning_efforts`; version 3 adds the optional normalized `cost` snapshot. Older caches are refetched. |
| Model ID | at most 256 bytes | Valid UTF-8 with no control characters; used only as a JSON value/map key, never a path. |
| Thinking effort value | at most 32 bytes | Non-empty valid UTF-8 matching `[a-z][a-z0-9_-]*`; `default` is reserved for clearing and is never a catalog value. Applies to the catalog, cache, CLI, and environment. |
| Effort values per model | 16 | Extra values are dropped after canonical ordering; invalid values are dropped. A model with none advertises no effort control. |
| Discovered base URL | HTTPS `opencode.ai` | Host must be `opencode.ai` with no userinfo, query, or fragment; an explicit `-provider-base-url` wins. |
| Session wall clock | 0 (none) | Opt-in `-session-timeout` / `AHARNESS_SESSION_TIMEOUT` / `session_timeout`; per run, checked before each turn. `0` or an omitted value imposes no deadline; a parent context deadline may still stop the run. |
| Model call timeout | 0 (none) | Opt-in `-model-timeout` / `AHARNESS_MODEL_TIMEOUT` / `model_timeout`; per model attempt. `0` or an omitted value imposes no harness deadline; the HTTP client timeout remains the outer network bound. |
| HTTP client timeout | 15 minutes (`-http-timeout` / `AHARNESS_HTTP_TIMEOUT` / `http_timeout`) | Outer bound on a provider request, including body reads. Must be positive; a smaller value fails a stalled request sooner, a larger one tolerates a long generation. |
| Transport attempts | 5 total (initial + 4 retries) | 429, 5xx, transport failures, and an explicit `-model-timeout` expiry while the run context is alive. |
| Retry backoff | 1s base, 30s max, 25% jitter | Context-aware sleep. |
| `Retry-After` cap | 60 seconds | Longer values are clamped. |
| Streaming retry | only before the first display delta | A retry after output would duplicate stdout and could duplicate tool calls. |
| Context-overflow retry | 1 | Rebuilds against 90% of the failed per-call input limit. |
| Consecutive output-length stops | 2 | Two `length` responses without a tool round stop with `token_limit`. |
| Repeated-failure hint | 3 | Same tool, canonical arguments, and result code. |
| Repeated-failure stop | 5 | Same identity as the hint. |
| Retained verbatim turns | 4 | Most recent model-response turns in the projection. |
| Token estimate | `ceil(bytes/4) + 8/message + 16/tool schema` | Conservative; provider usage is authoritative for totals. |

Existing sessions keep their recorded context, output, and base URL values;
new sessions use the catalog. Recorded `max_turns`, `max_total_tokens`,
`session_timeout_ms`, and `model_timeout_ms` are historical metadata and are
not inherited on resume/fork; only an explicit positive flag enforces a run
budget. When `-max-total-tokens` is explicit, a 1,000,000-token context can
consume that cumulative budget quickly.

Thinking effort introduces no run budget. It is bounded only by the model's
catalog list, the 32-byte per-value cap, and the 16-value normalization cap.
Reasoning tokens are output tokens: they count against `-max-output-tokens`
when that flag is set and against total usage, and they add no separate cap.

## 2. Provider transport and streaming

| Limit | v1 value | Behavior |
|---|---|---|
| Non-streaming response body | 1 MiB | Larger is `protocol_error`. |
| Provider error body | 4 KiB | Redacted, UTF-8 sanitized, and capped before logging. |
| SSE event | 4 MiB | A terminal `response.completed` event wraps the full response. |
| Assembled canonical response | 1 MiB | Text plus tool calls; larger is `protocol_error` before any tool executes. |
| Total stream | 8 MiB | Bounds deltas, keepalives, and the terminal event. |
| Thinking text | 64 KiB per response | Display-only reasoning retained for the TUI; larger text is dropped from the display and never logged. |
| Partial text on interrupt or failed stream | 64 KiB | Diagnostics only; never projected. |
| Model request debug event | 1 MiB | Only when `AHARNESS_LOG_REQUESTS=1`; over-cap is replaced by a truncation marker. |
| Error event message | 2 KiB | Fatal error class and message; no secrets. |
| Panic message | 1 KiB | Recovered tool panic message. |
| Panic stack | 4 KiB | Recovered tool panic stack. |
| Final event text | 64 KiB | Summary in the log; stdout may print the full response up to the response cap. |
| Task or task-file effective text | 64 KiB | The file is read without a size cap; the recorded/projected text is capped with a truncation marker. |
| Steering message | 64 KiB | One TUI composer submission; longer is rejected with a notice. Batch has no steering. |
| Session line | 4 MiB | Includes the JSON envelope and escaping. |
| `model_response` payload | 1 MiB total canonical content | The executed response is fully recorded so resume can replay it. |
| Tool result output/error | 64 KiB per field | Tool-specific caps below are usually smaller. |
| Model response usage | totals plus subsets | Input/output totals; cached, cache-write, and reasoning subsets; `reported` distinguishes an absent usage object from a present one. Subsets never change `Total()`. |
| `-require-usage` | off | When set, a completed buffered response without provider usage is a `protocol_error` before any tool executes. Streaming already requires usage. Not inherited on resume/fork. |
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
| `web_fetch` | Request timeout | 30 seconds, max 120 | `-web-timeout` / `AHARNESS_WEB_TIMEOUT` / `web_timeout`; covers connect, redirects, and body reads. |
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
| Toolset version | `toolset-v1` for new sessions | `toolset-v0` declarations are frozen for replay; `toolset-v1` replays the current extended set, including `web_search`. An unknown recorded version is a replay error. `PromptVersion` is unchanged. |
| Tool calls | Automatic retry | never | Side effects are not idempotent; the model decides. |

## 5. CLI, input, and configuration

Every configurable limit resolves per setting as **flag > environment > config
file > default**; see [configuration.md](configuration.md). The "Layer" column
below names which sources exist for that row: a flag name, an environment
variable, and a config-file key.

| Limit | v1 value | Layer | Behavior |
|---|---|---|---|
| Config file | `<state-dir>/config.json` | `-config`, `AHARNESS_CONFIG` | Optional JSON; an explicit path must exist, the implicit default may be absent; `-config ""` disables it; 1 MiB cap; unknown keys and invalid values are config errors (exit 2). |
| `-provider` / `AHARNESS_PROVIDER` | `opencode-go` | flag, env, `provider` | Only supported provider; an unknown value is a config error (exit 2) listing `opencode-go`. |
| `-list-models` | off | flag | Terminal action: prints `ID`, `NAME`, `DIALECT`, `CONTEXT`, `OUTPUT`, `STATUS`, `EFFORT` and exits; no session, task, or token. `EFFORT` is comma-separated normalized values or `-`. No separate output cap; bounded by the catalog size. |
| `-refresh-models` | off | flag | Terminal action: unconditional catalog fetch, atomic cache replace, one-line summary, then exits; no session, task, or token. Bounded by the catalog timeout/body caps; no new budget. |
| `-max-turns` | 0 (unlimited) | flag, `AHARNESS_MAX_TURNS`, `max_turns` | Cumulative when explicit and positive; `0`/omitted unlimited; negative is a config error. |
| `-max-total-tokens` | 0 (unlimited) | flag, `AHARNESS_MAX_TOTAL_TOKENS`, `max_total_tokens` | Cumulative when explicit and positive; `0`/omitted unlimited; negative is a config error. |
| `-max-context-tokens` | 0 (unset) | flag, `AHARNESS_MAX_CONTEXT_TOKENS`, `max_context_tokens` | Use the catalog context window. When explicitly set it must be positive and is clamped to the model ceiling with a warning. |
| `-max-output-tokens` | 0 (unset) | flag, `AHARNESS_MAX_OUTPUT_TOKENS`, `max_output_tokens` | Omit the request field. A positive value is sent unchanged; `0` clears an inherited budget; a negative value is a config error. |
| `-require-usage` | off | flag, `AHARNESS_REQUIRE_USAGE`, `require_usage` | Opt-in fail-closed accounting: a completed buffered response without provider usage is a `protocol_error` before tools run. Per-run, not inherited. No new budget. |
| `-thinking-effort` / `AHARNESS_THINKING_EFFORT` | unset | flag, env, `thinking_effort` | Provider/model default. An explicit non-default value must be advertised by the effective model and match the 32-byte syntax rule; `default` clears. Flag wins over environment and file; resume/fork inherit the latest recorded value. Invalid or unsupported values exit 2. No new run budget. |
| `-session-timeout` | 0 (none) | flag, `AHARNESS_SESSION_TIMEOUT`, `session_timeout` | Per run when explicit and positive; `0`/omitted no deadline; negative is a config error. |
| `-model-timeout` | 0 (none) | flag, `AHARNESS_MODEL_TIMEOUT`, `model_timeout` | Per model attempt when explicit and positive; `0`/omitted no harness deadline; negative is a config error. |
| `-http-timeout` | 15 minutes | flag, `AHARNESS_HTTP_TIMEOUT`, `http_timeout` | Outer bound on a provider request, including body reads. Must be positive in every layer; a negative or zero value is a config error. |
| `-catalog-timeout` | 15 seconds | flag, `AHARNESS_CATALOG_TIMEOUT`, `catalog_timeout` | Bound on one catalog request, including body reads. Must be positive; valid for the terminal catalog actions too. |
| `-log-requests` / `AHARNESS_LOG_REQUESTS` | off | flag, env, `log_requests` | Records each projected model request as a debug event, capped at 1 MiB. |
| `-task` / `-task-file` | no read cap | flag | Per-invocation only; effective transcript text is 64 KiB. |
| Steering message | 64 KiB | TUI | Composer submission while a run is active. |
| Session selection | UUIDv7 ID | flag | `-resume`/`-fork` resolve under the state directory; no arbitrary paths. |
| Provider token | no length cap | `AHARNESS_PROVIDER_TOKEN` only | In memory only; never logged or passed to tools; deliberately not a flag or config key. |
| Provider base URL | no length cap | flag, env, `provider_base_url` | Explicit override must be http(s) with a host; userinfo and query/fragment are rejected. Discovered URLs are HTTPS `opencode.ai`. |
| SearXNG base URL | 8 KiB | flag, `AHARNESS_SEARXNG_URL`, `searxng_url` | `web_search` backend. Absolute http(s); HTTP only for loopback hosts; userinfo, query, and fragment are rejected; a path prefix is allowed and trailing slashes are trimmed. Empty disables search while the tool remains registered. |
| Web request timeout | 30 seconds, max 120 | flag, `AHARNESS_WEB_TIMEOUT`, `web_timeout` | `web_fetch` request bound. Must be positive in every layer; zero, negative, or above 120 seconds is a config error. Independent of `http_timeout`. |
| `-web-fetch-provider` | `builtin` | flag, `AHARNESS_WEB_FETCH_PROVIDER`, `web_fetch_provider` | One of `builtin` or `crawl4ai`; selected once at startup with no fallback. |
| Web provider endpoint | 8 KiB | flag, `AHARNESS_WEB_FETCH_URL`, `web_fetch_url` | Empty for builtin; required for crawl4ai. Absolute http(s), no userinfo or fragment; loopback and private hosts are allowed because it is the operator's service. |
| Web provider key | 4 KiB | flag, `AHARNESS_WEB_FETCH_KEY`, `web_fetch_key` | Optional bearer token, sent only to the provider and never logged. Control characters are rejected. |
| Catalog URL | no length cap | env, `models_dev_url` | Default `https://models.dev/api.json`; must be HTTPS except loopback HTTP. |
| State directory path | OS path limit | flag, `AHARNESS_STATE_DIR` | No explicit application cap. Deliberately not a config key: it locates the default config file. |
| Session log retention | none | — | No rotation or automatic deletion. |

### 5.1 What is worth configuring

`docs/configuration.md` §4 explains the reasoning. In short, a limit belongs in
the flag/env/config surface when two operators could reasonably disagree for the
same binary:

- **Worth configuring:** the HTTP client timeout, the per-call model timeout,
  the session wall clock, the turn/total-token budgets, the context/output
  budgets, thinking effort, the catalog timeout and URL, the SearXNG base URL,
  and the model / provider / base-URL / workspace selectors.
- **Intentionally fixed:** body and stream caps, tool result caps, the search
  response/result/output/snippet caps, the 15-second search timeout, the
  session line and schema versions, file modes, retention, the retry policy,
  UI display caps, and the `-ui-json` protocol caps. Each defines the wire
  format, the replay contract, or a display-only safety boundary. Nothing in
  §2, §3, §4, or §6 is configurable.

## 6. TUI and `-ui-json`

These caps are display-only; none changes the session log or the model
contract. See [tui.md](tui.md) for the shipped behavior and
[commands.md](commands.md) for slash commands.

| Cap | Value | Behavior |
|---|---|---|
| Task/steering message | 64 KiB | Longer is rejected with a notice; the run continues. |
| Transcript items | 20,000 | Oldest UI items are dropped with an "older history omitted" marker. |
| Transcript text | 16 MiB | Oldest UI items are dropped; the log is unchanged. |
| Assistant text per item | 64 KiB displayed | Truncation marker; the full response stays in the log. |
| Thinking text per item | 64 KiB displayed | Display-only; truncation marker. Never logged, so a resumed session shows no prior thinking. |
| Markdown source per item | 64 KiB | Longer is rendered as plain text with a marker. |
| Markdown blocks per item | 4,000 | Excess input falls back to plain text with a notice. |
| Markdown nesting | 16 levels | Deeper input falls back to plain text with a notice. |
| Table columns / rows | 12 / 500 displayed | Column width cap 40 cells; excess scrolls/folds with a marker. |
| Widget list / file picker entries | 1,000 / 2,000 per directory | File picker does not recurse, read contents, follow symlinks, or list `.git`; filter query is 64 bytes. |
| Overlay stack | 4 | Deeper requests close the oldest non-modal overlay with a notice. |
| Image file / dimensions | 8 MiB / 4096×4096 | Larger files are rejected with a placeholder. `image.DecodeConfig` runs first. |
| Decoded image pixels | 16 Mi (4096×4096 max) | Larger images are rejected before full decode. |
| Images per transcript | 32 | Oldest displayed thumbnails are dropped; the transcript item remains. |
| Image thumbnail | 60×20 cells or available width | Half-block glyph preview; Kitty only in the viewer. |
| Tool output | 4 KiB or 40 lines folded | Expanded output never exceeds the logged tool-result cap. |
| Tool arguments preview | 2 KiB or 8 lines | Full arguments remain in `model_response`/the log. |
| Input history | 100 entries | In-memory only; not persisted. |
| Minimum terminal size | 40×10 | Below shows a resize message; quit/resize keys still work. |
| Frame coalescing | 16 ms | Text deltas coalesce; state changes flush immediately. |
| Escape timeout | 50 ms | A lone Esc is delivered; modified sequences never interrupt. |
| Mouse wheel | 3 lines per notch | Scrolls the transcript, or the top overlay when one is open. Clicks, releases, motion, and horizontal wheel events are ignored; focus reporting stays off. Terminal text selection and middle-click paste need the terminal's `Shift` bypass; see [tui.md](tui.md). |
| Display queue | 256 events | Backpressure; text deltas are not durable. |
| Slash-command menu | 8 rows | Filtered by command name/alias; scrolls to the selection; shrinks to keep the header, one transcript row, composer, and status visible. |
| TUI catalog refresh | `Ctrl-R` in the model picker | Idle only; reuses the catalog timeout/body/cache caps; no `-ui-json` protocol version change. |
| Thinking effort picker | `F2`; model's normalized list | Idle only; lists `provider default` plus the model's values; no overlay when the model advertises none; next run only; no protocol version change. |
| `-ui-json` command line | 1 MiB | Longer lines are a protocol error (exit 2). |
| `-ui-json` text/paste | 64 KiB per command | Longer text is rejected with a protocol error. |
| `-ui-json` frame size | 20×5–500×200 | `-ui-size`/`resize` outside the range is a config/protocol error. |
| `-ui-json` wait timeout | 30,000 ms default; 300,000 ms max | A timeout emits `error{code:"timeout"}` and does not exit. |
| `-ui-json` frame line | 1 MiB | Over-cap emits `frame_too_large` and continues. |
| `-ui-json` style spans | 16,000 or 256 KiB encoded | Excess styles are dropped; `styles_truncated` is true and `lines` stay complete. |
| `-ui-json` snapshot | 512 KiB SVG payload | Over-cap emits `snapshot_too_large` and continues. |

## 7. No cap or no separate cap

These are intentionally bounded only by an enclosing limit, not by a dedicated
constant:

- Total session log size and total state-directory size.
- `-task`/`-task-file` read size; the transcript truncates at 64 KiB.
- Search walk time and file-tool execution time; any explicit run deadline
  applies.
- Command length, `old_string`/`new_string` length, and search pattern/glob
  length; these are indirectly bounded by the model-response cap and tool
  result caps.
- Number of tool calls per response; bounded by the response cap.
- Number of events per session; bounded by the turn, token, and time budgets
  only when those explicit flags are set.
- Thinking effort values; bounded by the model's normalized catalog list (at
  most 16 values, 32 bytes each). No run budget is added.
- Number of concurrent sessions; one process per session, no global cap.
- Child process memory and CPU; external isolation is the boundary.
- Workspace size; file tools enforce their own file-size caps.
- Provider token length; held in memory only.
- Session retention; old logs remain until the operator removes them.

## 8. Removed v0 limits

v1 replaces the v0 session contract outright. The v0 `session_end` event, the
1 MiB + 64 KiB session line, and the 64 KiB-per-field `model_response` text cap
no longer exist. v0 logs are rejected rather than migrated.
