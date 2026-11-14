# aharness v1 testing strategy

- **Date:** 2026-11-13
- **Status:** normative testing strategy for v1, the shipped TUI, model
  discovery, thinking effort, and the builtin tool set
- **Related:** [system architecture](architecture.md), [Go coding standard](coding-standard.md), [v1 build plan](plans/v1-build.md), [TUI build plan](plans/tui-build.md), [shipped TUI](tui.md), [model discovery build plan](plans/model-discovery-build.md), [thinking effort build plan](plans/thinking-effort-build.md), [web search build plan](plans/web-search-build.md)

v1 uses four layers: unit tests for component behavior, integration tests
against a fake provider and the built binary, fuzz targets at the session and
SSE trust boundaries, and a manual smoke run against a real provider. Unit and
integration tests use fakes, `httptest`, `t.TempDir`, and `testing/synctest`;
they do not use real network access or `time.Sleep`-based waiting, except short
real timeouts for subprocess and signal tests.

## 1. Unit tests

- Workspace and tools: containment (`..`, absolute-path conversion, symlink
  escapes, `.git`), the tool contracts, result codes, output caps, UTF-8
  sanitization, process-group cancellation, and the environment allowlist,
  including that `HOME` is inherited so git identity and configuration are
  visible while the token denylist still applies. Web search additionally tests
  base-URL validation (schemes, loopback HTTP, userinfo/query/fragment, trailing
  slash, 8 KiB cap), request parameters and base path, result URL filtering and
  dedupe, snippet sanitization, results/output caps, details, and the
  unavailable/403/http_error/transport/malformed/oversized/timeout/canceled
  mappings against fake `httptest` SearXNG responses. The todo tool tests
  set/read/clear lifecycle, per-registry isolation, normalization, the item and
  content caps, the status enum, the single-`in_progress` rule, strict argument
  decoding, cancellation, the structured `details` round trip, and seeding from
  a recorded list (including a rejected seed). `agent.RestoreTodo` tests the
  last-write-wins restore, ignored failed and foreign results, a clear, and
  malformed details.
- Provider adapters: Chat Completions and Responses canonical mapping, tool-call
  round trips, finish/usage/error normalization, strict JSON decoding, and
  Responses `store:false` replay.
- Reasoning continuation: both adapters retain and replay native material in a
  second tool-round request. Chat sends `reasoning_content`; Responses sends
  `include: ["reasoning.encrypted_content"]` and replays the raw reasoning item
  before the matching function call. Buffered and streamed paths; encrypted and
  plaintext reasoning; summary-only or ID-only items are not replayed; the
  replayed item is unchanged and ordered before the call.
- Catalog: models.dev fixture decoding and normalization; SDK-to-dialect mapping
  for supported, translated, and unknown packages; eligibility (tools, text
  output, missing/invalid context, missing output ceiling, deprecated/alpha/beta,
  unknown type); base-URL allowlist and rejection of userinfo/query/fragment;
  context clamp, input cap without output subtraction, unset output, explicit
  output pass-through without a catalog clamp, explicit `0` clearing an
  inherited budget, and negative-output rejection; cache fresh hit without HTTP,
  stale conditional `304`, replacement on `200`, stale fallback, corrupt cache,
  version mismatch, atomic write and modes; explicit refresh without
  `If-None-Match`, unexpected `304` rejection, and preservation of the previous
  cache on every failure.
- Catalog reasoning effort: `reasoning` and `reasoning_options` decoding;
  effort-only, multiple effort options, toggle-only, token-budget-only, empty
  array, and unknown option types; flattening, trimming, deduplication, and
  canonical ordering (`none` … `max`, then unknown lexicographic); invalid
  syntax, over-32-byte values, case rejection, control characters, the reserved
  `default` value, and the 16-value cap; a malformed `reasoning_options` value
  yields no effort control rather than a catalog failure; cache version 2 round
  trip, version 1 rejection and refetch, and rejection of a tampered cache
  effort list (count, duplicate, order, or syntax).
- Streaming adapters: SSE text and tool-call assembly for both dialects;
  `[DONE]`; comments, CRLF, and multi-line data; malformed JSON, duplicate
  names, invalid UTF-8; 4 MiB event, 1 MiB assembled-response, and 8 MiB
  total-stream caps; missing usage; non-SSE JSON fallback; error-body
  redaction; and `PartialError` after the first display delta.
- Thinking effort adapters: a Chat request body has top-level
  `reasoning_effort` and no nested `reasoning`; a Responses body has
  `reasoning.effort` and no top-level field; an empty effort sends neither;
  buffered and streaming requests carry the same value through the shared
  payload builder; a tool-call round trip repeats the value on every call; the
  value is sent unchanged with no provider-name translation; unknown members
  remain tolerated.
- Session v2: envelope round-trip; v0/unknown version rejection; one version per
  file; unknown fields and event types; run/control payloads; `seq`/`session`
  validation; UUID validation; exclusive and shared `flock`; torn-final repair
  versus corrupt-final rejection; valid final line without a newline; fork copy
  with new session/seq and preserved payloads/timestamps; `session_repair`.
- Resume/fork state: cumulative turns/usage reconstruction; recorded run
  budgets (`max_turns`, `max_total_tokens`, `session_timeout_ms`,
  `model_timeout_ms`) ignored for enforcement; pending-call detection;
  pending-call repair before `run_start`; task matrix; inherited
  model/workspace/base URL/context/output values; explicit overrides; at-or-over
  checks only for explicit positive turn/token flags; workspace mismatch;
  unsupported prompt/toolset; parent immutability.
- Session thinking effort: `session_start` and `run_start` record the effective
  optional value; the latest `run_start` wins for reconstruction; a
  `session_start` value applies before the first run; a v2 log without the
  field reconstructs as empty and resumes with the provider default; a fork
  records the fork's effective value while copied events keep historical values;
  malformed known payloads still error and unknown fields remain ignored.
- Agent request: `ThinkingEffort` is copied into every constructed model request
  (including the context-overflow retry), `model_request` logging includes it in
  both the full and truncated payloads, retries preserve the same request, and
  usage/finish handling is unchanged.
- Loop: scripted model sequences for all terminal states; every call paired;
  panic paired then `internal_error`; duplicate call IDs stop with
  `protocol_error`; code-based stuck detection; one `final` and one `run_end`
  per run; run-local output and usage with cumulative totals. Unlimited
  turns/tokens produce no budget event; explicit turn/token limits and explicit
  session/model timeouts still enforce their documented stop/retry outcomes,
  and `limit` is emitted only for an explicit positive budget;
  `-session-timeout`/`-model-timeout` `0` means no deadline while an explicit
  value enforces one.
- Streaming loop: deltas emitted once, no reprint after deltas, non-streaming
  fallback printed once, retry before the first delta, no retry after a text or
  thinking delta, partial error fields, and oversized responses rejected before
  tools.
- Thinking: both adapters map buffered and streamed `reasoning_content`/
  `reasoning` and Responses reasoning summary/content into display-only
  `Response.Reasoning`; encrypted reasoning content is not rendered and
  Responses raw reasoning items are retained only in the adapter continuation
  cache; the 64 KiB cap clamps streamed and settled text at a rune boundary;
  thinking never counts toward `responseContentBytes`; the runner emits
  `ReasoningDelta` before `ResponseCompleted`, reprints buffered reasoning once
  (and only once), and never writes thinking into any session event; the TUI
  streams it into a `thinking` block, sanitizes control bytes, settles the
  header on assistant text, and caps the item. `-ui-json` end to end: frames
  show the thinking block for both dialects and the session log contains none
  of it.
- TUI/display: display-event order for streamed, fallback, tool, retry, and
  error paths; `RunStarted` model/dialect/resume sequence and effective
  thinking effort; display failure maps to `internal_error` and pairs tool
  calls; nil display disables events.
- History: multiple runs, run headers, assistant text once, tool pairing,
  canceled/panic/repaired results, anomalies, hints/notices, partial text,
  fork metadata, malformed payloads, unknown events/fields, and caps.
- TUI app: composer editing/history, submit vs steering, steering ack and
  unacknowledged requeue, first/second `Ctrl-C`, `Ctrl-D`, `Esc`, overlay
  focus/restore, rich/raw toggle, table/file/model/image overlays, model picker
  next-run-only, workspace containment, sanitizer, and raw Option-`[`/Option-`]`
  input through a fake input pipe. Status line renders `turns N` and
  `ctx N/L` for unlimited turn budgets and `N/M` only for explicit positive
  turn budgets, never `0/0`; `ctx` tracks a live run and reports context
  occupancy against the context window rather than the session-cumulative
  token sum; the details overlay renders `unlimited`/`none`; old `max_turns`/
  `time_limit` terminal states still render. The empty transcript's tips block
  lists `F1`, `Shift`+drag, `Ctrl+Shift+V`, and the wheel; it yields to the
  first item, fits the 40-column and minimum-height frames, and falls back to
  the one-line prompt when the transcript is too short; the help overlay
  documents the terminal's `Shift` mouse bypass.
- TUI slash commands: parser case/aliases/arguments, `/help` lists the
  registry, known commands execute without starting a run, unknown `/text` and
  seeded tasks fall through as messages, `/model`/`/effort` arguments and
  active-run notices, and `/interrupt`/`/cancel`/`/quit` run control. The
  completion menu covers prefix matching, alias de-duplication, Up/Down
  selection and windowing, Tab completion, Enter execution, Esc precedence,
  history-recall suppression, and the minimum-size layout cap.
- TUI thinking effort: `F2` opens and closes the next-run picker; it is
  disabled with a notice during a run; entries are `provider default` then the
  model's values in canonical order; a model with no effort values shows a
  notice and no overlay; the selection changes only the next run; the details
  overlay row; model-picker effort metadata; a model switch keeps a supported
  value and clears an unsupported one with a notice; deterministic frames for
  each state.
- Headless UI: `-ui-json` decode/encode, caps, `wait` conditions/timeouts,
  frame/style/cursor/image metadata, SVG determinism, forced glyph mode, and
  no ANSI/raw image bytes. `x/vt` cell/style fixtures cover wide/CJK, combining
  marks, emoji, and cursor state. Thinking effort: `f2` is accepted; the
  selection updates the next run's request and `run_start`; `model_changed` and
  `run_started` expose the optional `thinking_effort` field; the `F2` selection
  emits `notice` with `kind: "thinking_effort"`; a client that ignores the
  field still parses the version 1 frames.
- Markdown/images: Glamour fixtures for headings/emphasis/code/lists/tables/
  links, raw HTML inert, over-deep/over-block fallback, workspace-contained
  PNG/JPEG/GIF decode caps, placeholders, glyph thumbnails, viewer, and
  oversize/unsupported/symlink/escape rejection.
- Interrupt/control: model interrupt hint; tool interrupt pairs the interrupted
  call and unstarted calls; TUI steering consumed at turn boundaries and before
  a completed response; batch non-interactive signal behavior.
- Usage mapping: Chat and Responses, buffered and streamed; absent, empty, and
  populated detail objects; DeepSeek `prompt_cache_hit_tokens` fallback;
  cache-write and reasoning subsets; a present all-zero object reported versus
  an absent object not reported; invalid subset bounds becoming
  `protocol_error`; old usage JSON decoding with `reported=false`; `Total()`
  excluding the subsets.
- Cost and strict usage: catalog cost decoding and micro-USD rounding; a
  missing cost, negative rate, or non-finite rate leaving pricing unavailable;
  cache-read fallback to the input rate; reasoning not charged twice; per-call
  and run aggregation; `run_cost_complete`/`cost_complete` false for an
  unpriced response; a priced zero-cost response still priced; `-require-usage`
  accepted for a reported response and rejected before tool execution for an
  absent one, while the default still accepts missing usage; old v2 logs
  decode with no cost and `reported=false`.
- Read windowing: exact line boundaries; default 2,000 and max 4,000 clamps;
  the 60 KiB byte cap; a 1,175-line/42 KB file in one default call; a
  500-line/80-byte page with no middle loss; actual `lines A-B of Z` headers;
  `continue with offset=N` only when lines remain; `truncated` only for byte
  loss; a single over-budget line prefix with its marker and no continuation;
  empty files, trailing newlines, CRLF, multibyte UTF-8, offset past EOF, and
  no index panics.
- Eval driver: manifest/meta validation, setup-failure exclusion, verifier
  failure, arm interleaving and recorded order, metrics from a hand-built
  session fixture, price-table and peak-window cost, catalog-pricing fallback,
  missing usage not averaged as zero, bootstrap determinism, and summary
  ordering. The shipped suite test asserts 30 tasks, at least three clusters,
  executable setup/verify scripts, the price snapshot, and the honest
  `baseline.json` status.
- Toolset selection: frozen v0 schemas, current v1 schemas, unknown version
  error, `Projection.Schemas` and the token estimate using the recorded set,
  and a seeded v0 session resuming with v0 descriptions.
- Search: `**` zero-or-more segments versus `*` not crossing `/`; `.git`
  exclusion; skipped-file counts by reason; count-cap versus byte-truncation
  flags.
- Command spill: threshold, 0600 file under the per-run tool-output directory,
  absolute path in the result and `details`, inline result within 64 KiB,
  cleanup with the work directory, O_EXCL/no-symlink behavior, and spill
  failure leaving the inline result usable.
- Projection: prompt/toolset selection by version; run/control events ignored;
  resumed and forked histories; repaired calls paired; retained turns; the
  selected schema set drives the request and token estimate.
- Config: new flags; mutual exclusion; task rules; inheritance; explicit
  overrides; provider validation and precedence; list/refresh terminal actions;
  model resolution and provider mismatch; base-URL credential/query/fragment
  rejection; exit-code mapping; unlimited `0` defaults for turns/tokens/session
  timeout/model timeout; explicit `0` and explicit positive values; negative
  rejection naming the flag; recorded-budget ignoring on resume/fork.
- Configuration layers: flag > environment > config file > default per setting,
  including a mixed case where each layer contributes; file-supplied values
  count as explicit for clamping and clearing; the implicit
  `<state-dir>/config.json` and `AHARNESS_CONFIG`/`-config` selection with
  flag-over-environment; `-config ""` disabling the file; an explicit or
  environment-selected missing file rejected while an absent implicit file is
  allowed; unknown keys, duplicate keys, wrong types, bare-number durations, and
  an oversize file rejected; every key validated even when a higher layer
  overrides it; `-version` bypassing a broken file; and the HTTP/catalog timeout
  reaching the adapter and catalog source. Integration covers one real run whose
  model, base URL, workspace, and turn budget come only from the file, a flag
  beating a file value, the resolved path in the startup log, and a broken file
  exiting 2 before any session state.
- Thinking-effort config: flag-over-environment precedence; explicit
  `default`; an explicitly empty flag rejected while an empty environment value
  is unset; malformed syntax, over-32-byte values, and leading non-lowercase
  characters rejected before catalog resolution; an unsupported value for the
  effective model; a model that does not reason versus one that advertises no
  effort levels; an explicit effort+model conflict; an inherited supported
  value kept on model change; an inherited unsupported value cleared with a
  warning and an empty recorded value; explicit non-default effort on
  resume/fork requiring a catalog fetch while an inherited value does not; and
  `-list-models`/`-refresh-models` ignoring the setting.

Tests follow the coding standard: table-driven where cases share logic; no
assert or mock frameworks; fakes at consumer interfaces; `httptest` for the
provider adapters; got-before-want failure messages with `t.Helper`; test error
identity with `errors.Is`/`errors.As`, not strings; `t.Context`,
`t.TempDir`, and the `t.Chdir`/`t.Setenv` parallel restrictions; no real network
or `time.Sleep`-based waiting. Inside a `synctest` bubble, do not call `t.Run`,
`t.Parallel`, or `t.Deadline`. Golden session logs are decoded and compared
semantically; do not test wiring, forwarding, or trivial getters; coverage is a
diagnostic, not a gate.

## 2. Integration tests

- Run a fake models.dev catalog server and a fake `opencode-go` server with a
  temp workspace and temp state directory. Point the binary at the fake catalog
  with `AHARNESS_MODELS_DEV_URL` (loopback HTTP is allowed) and at the fake
  provider with `-provider-base-url` or `AHARNESS_PROVIDER_BASE_URL`. Use a
  fixture catalog with a Chat model, a Responses model, a translated
  Anthropic/Google-native model, a deprecated model, and ineligible entries.
- Assert the persisted log, stdout final/streamed text, stderr progress, exit
  code, file modes, and that the run removed `<state>/work/<session-id>`.
- Exercise a discovered Chat model and a discovered Responses model through
  their catalog dialect paths and verify the adapter sends the selected model,
  `x-opencode-session`, and a self-identifying `User-Agent`; include a tool-call
  round trip for each dialect and for one translated model through Chat. Assert
  `session_start` and `run_start` record the effective provider/model/dialect/
  base URL/limits.
- Output budget: default request bodies contain no `max_tokens` /
  `max_output_tokens`; an explicit `-max-output-tokens` value appears unchanged;
  resuming a log with a recorded budget and `-max-output-tokens 0` omits the
  field; a provider `length` or context-overflow response follows the existing
  continuation and compaction-retry paths.
- Run budgets: a default run exceeds the old 30-turn and 2M-token thresholds
  and completes without a `limit` event; explicit turn/token/session/model
  timeout flags still stop with the existing statuses and exit codes; resuming
  or forking a log with recorded budgets ignores those values and the new
  `run_start` records `0`.
- Streaming: for both dialects, a delta is visible on stdout while the fake
  provider is still blocked; the log has one `model_response` with
  `streamed=true`; no delta events are written. A buffered and a streamed
  response with the same logical usage produce the same canonical usage.
- Usage/cost end to end: a fake provider returns cached, cache-write, and
  reasoning subsets; `run_start` records the catalog pricing snapshot,
  `model_response` records the expected `cost_micro_usd`, and `run_end`
  reconciles run and cumulative cost; a resumed old log with no cost keeps
  `cost_complete=false`; `-require-usage` stops a missing-usage response before
  the tool runs; the `-ui-json` run-finished usage carries the additive detail
  fields without a protocol bump.
- Resume: a limit-stopped run resumes under the same session ID with inherited
  model/workspace/base URL/context/output values; recorded run budgets are not
  inherited; a torn final line is repaired and recorded;
  the same session log gains a second `run_start` and `final`. Resume/fork with
  the catalog server down and no explicit model still starts from the recorded
  values; an explicit model change resolves through the catalog; an explicit
  provider mismatch exits 2.
- `-list-models`: assert the tab-separated header and rows, exit 0, no session
  file, no task/token requirement, and stale-cache fallback. The header ends
  with `EFFORT`; rows print the normalized comma-separated values and `-` for a
  model with no effort control.
- Thinking effort end to end: a fake catalog with differing per-model effort
  lists and a fake provider asserting exact request bodies for a new session,
  an omitted setting (no field), an explicit value, resume inheritance, fork,
  a model change that keeps a supported value, a model change that clears an
  unsupported value with a stderr warning, and an explicit `default` clear.
  Unsupported values and an explicit incompatible model+effort combination exit
  2 before any provider request; `session_start`/`run_start` record the effective
  value, and `-ui-json` `f2` reaches the next request.
- Web search end to end: a fake provider returns a `web_search` tool call, the
  registry points at a fake SearXNG `httptest` server, and the binary records
  exactly one paired `tool_result` with the formatted title/URL/snippet. Repeat
  without `AHARNESS_SEARXNG_URL` (`unavailable`) and with a 500 (`http_error`):
  the result reaches the next provider request as data and the run continues by
  the existing policy. The startup log records `search_configured` and
  `search_timeout_ms` without the URL.
- Web fetch unit (`internal/tools/web_fetch_test.go`, `web_extract_test.go`,
  `web_fetch_crawl4ai_test.go`): URL-shape and address tables (loopback,
  private, link-local, shared, multicast, documentation, benchmarking,
  metadata, IPv4-mapped, NAT64, mixed-address hosts); content-type dispatch
  for HTML/XHTML/plain/Markdown/JSON/XML/CSV/PDF/image/octet-stream and
  missing-type sniffing; charset decoding with `Content-Type`, meta charset,
  BOM, and invalid bytes; gzip, unsupported encoding, declared and streamed
  body caps; 204/403/404/429/500; same-host, cross-host, downgrade, loop, and
  missing-`Location` redirects; offset boundaries and the continuation footer;
  sanitization; extraction fixtures for article boilerplate, code/tables,
  hidden nodes, app shells, challenges, and empty bodies; and the Crawl4AI
  request shape, streamed field skipping, status/redirect mapping, caps, and
  error mapping. `httptest` page and provider servers are the only network.
- Web fetch end to end: a fake model provider returns a `web_fetch` tool call.
  With `web_fetch_provider=crawl4ai` and a fake `/crawl` endpoint the binary
  records exactly one paired `tool_result`, extracts the rendered fixture, and
  the next provider request carries the text; a 500 from the provider is
  `fetch_failed` with no builtin request. With `builtin`, a loopback target is
  `blocked_address` before any connection. The startup log records
  `web_timeout_ms`, `web_fetch_provider`, and the endpoint host, never the key.
  A `toolset-v1` session resumes with `web_fetch` in the projected set.
- Todo end to end: a fake provider returns a `todo` tool call with two
  statuses; the binary records exactly one paired `tool_result` with the
  rendered checklist, the next provider request carries that list, and the
  workspace stays empty. A resume in a new process issues a `todo` read and
  receives the list recorded by the earlier run.
- Web extraction fuzz: arbitrary HTML and content types through the extraction
  pipeline (no panic, valid UTF-8, no control characters, bounded output), and
  arbitrary strings through the fetch-target and provider-endpoint validators
  (only absolute http(s), no userinfo, no fragment, no control characters).
  The fuzz targets run without network access.
- `-refresh-models`: the fake catalog server observes an unconditional request
  with no `If-None-Match` and no provider token; the summary is printed and no
  session file is created. Server-down and invalid-catalog cases exit 3/4 and
  preserve a seeded cache. `-refresh-models -list-models` refreshes before
  listing and writes the summary to stderr.
- Cache/offline: a fresh cache makes no catalog HTTP request; a stale cache
  sends a conditional `304`; a failed implicit refresh uses stale data with a
  warning; a corrupt or version-mismatched cache is refetched; the cache file
  mode is 0600 and the directory mode 0700.
- Fork: a completed session forks into a new self-contained log with parent
  metadata; the parent hash is unchanged; the child contains the parent prefix
  with a new session ID and renumbered `seq` plus the new task.
- Locks: a second `-resume` against a run paused in the provider exits 2 with
  the trace path.
- TUI/`-ui-json` end to end: default startup/seed, live streaming before the
  provider completes, tool activity, a second message as a second run in the
  same log, steering queued/acknowledged and reaching the next request,
  resume/fork, first/second interrupt, `Ctrl-D` active 130, `SIGTERM` 130 with
  `final`/`run_end`, clean EOF/quit 0, protocol errors 2, and no ANSI in
  frames. Include catalog-backed picker contents, model-switch effects on the
  next run, `Ctrl-R` refresh success/selection preservation/failure notice and
  in-flight cancellation on close, and the offline-resume one-entry fallback
  that `Ctrl-R` expands.
- Batch: preserve the v1 stdout/stderr, streaming, resume/fork, and exit codes;
  non-TTY invocations batch automatically; `-interactive` is an unknown flag.
- TUI terminal safety: resize mid-stream, model/tool text containing OSC 52,
  CSI cursor moves, bidi, raw HTML, and image links with `..`/`.git`/symlink/
  absolute paths; frames contain no ESC and unsafe images become placeholders.
- Crash/schema: a v0 log is rejected with a clear error and is unchanged;
  unknown v2 events and fields are tolerated.
- Caps: an oversized response stops with `protocol_error` and executes no tool;
  a 1 MiB streamed response round-trips through the log.
- Run the suite with `-race -count=1` and `govulncheck ./...`; no leaked
  goroutines or child processes. Use `synctest` for retry/deadline tests; use
  real short timeouts for subprocess tests.
- CI runs the standard's §8 gates in one Linux job, with `gcc` installed for
  `-race`; TUI and `-ui-json` tests use the Bubble Tea fake input/output and
  `x/vt`, so no PTY is required. Fuzzing MAY run on a schedule.
- CI also runs `go mod tidy`/`go mod verify`; a dirty `go.mod` or `go.sum`
  fails.

## 3. Fuzzing

Fuzz targets cover the SSE decoder, the v2 JSONL reader/repair path, the TUI
terminal sanitizer, markdown input, file-path/image containment, the
`-ui-json` command decoder and frame/event/snapshot encoders, `agent.History`,
and the models.dev catalog decoder at the trust boundary. Catalog payloads
include arbitrary `reasoning_options` shapes and values, and stream seeds
include usage detail objects and malformed subset bounds: no panic, no unbounded
allocation, deterministic normalization, and invalid subsets rejected as
`protocol_error`. Seeds live under
`internal/model/testdata/fuzz` and `internal/session/testdata/fuzz`; the
TUI/history targets use built-in seeds. `FuzzSearchJSON` decodes arbitrary
SearXNG JSON through the same filter/sanitize path: no panic, at most ten
results, http(s)-only URLs with no fragment, snippets within 1,200 runes, and a
200-rune unresponsive warning. The unit test run executes the seeds; a
scheduled job MAY run longer fuzzing. The fuzz targets must not make real
network calls or require writable state.

## 4. Manual smoke

Run the built binary against the real models.dev catalog and `opencode-go`
provider on a small temporary repository; set `AHARNESS_PROVIDER_TOKEN` if the
endpoint requires auth. Use a current Chat model (for example
`deepseek-v4.1-flash`) and a current Responses model from `-list-models` (for
example `muse-spark-1.3-contributor`). If credentials and workspace policy
allow, run one Anthropic- or Google-native model through Chat translation (for
example `qwen3.7-plus`) and record the result. The Responses smoke requires a Go
subscription and the workspace privacy setting that allows paid endpoints to
train on request data.

Verify discovery itself before the per-dialect runs: `-list-models` prints the
usable catalog without creating a session; `-refresh-models` performs an
unconditional fetch, replaces the cache, and exits without a session; a second
run reuses the cache without a catalog request; a forced implicit refresh with
the catalog server unreachable falls back to the stale cache with a warning; and
resume without an explicit model works with the catalog server down. Confirm
`session_start`/`run_start` record the catalog's provider/model/dialect/base
URL/limits and that new sessions use the documented
`https://opencode.ai/inference/go/openai/v1` base.

For each dialect: run a non-streamed batch task and a streamed batch task,
then run the TUI through new, stream, tool round, steer, `Esc`/first `Ctrl-C`,
second `Ctrl-C`, a second message, quit, resume, and fork. In the TUI, open the
model picker, confirm the name/ID/dialect/context/input/advertised-output/
status/translated metadata, switch models and confirm the next run's values,
and use `Ctrl-R` to refresh in place. Drive `-ui-json` as
a coding agent would: pipe a command script, parse frames/events/styles/images,
request `snapshot`, inspect the SVG, and confirm the session log is the durable
record. Use `AHARNESS_LOG_REQUESTS=1` to confirm a default request body has no
`max_tokens` / `max_output_tokens` field; exercise one explicit output budget
and one provider `length`/context-overflow outcome.

For usage and cost: run a task that needs at least two tool rounds with
`-thinking-effort high` and inspect the session for `reported=true`, cached
input, reasoning output, the `pricing` snapshot in `run_start`, per-response
`cost_micro_usd`, and `run_end` cost/completeness. Run a two-turn conversation
and confirm `prompt_cache_hit_tokens` or `cached_input_tokens` is positive on
the second call when the prefix is stable. Re-run with `-require-usage`; if the
endpoint omits usage, the run must stop with `protocol_error` before a tool
executes. A `reported=false` response with the default flag must still complete.

For web search: point `-searxng-url` or `AHARNESS_SEARXNG_URL` at a local
SearXNG instance that includes `json` in `search.formats`, run a task that
calls `web_search`, and confirm the title/URL/snippet list, entity/tag
sanitization, and the documented 403 and malformed-JSON outcomes. Record the
instance version, query, and observed results in the
[web search plan](plans/web-search-build.md#10-implementation-status-and-deviations).
A 2026-11-13 smoke against a local instance at `http://localhost:28080` passed.

For the read/search/command contract: read a 1,175-line, roughly 42 KB Go file
in one default call and confirm the header range; page a file with
`limit` and follow the `continue with offset=N` footer; read a file with one
line longer than 60 KiB and confirm the truncation marker and absence of a
misleading continuation. Run a search with `**/*.go` and confirm nested files
match while `*.go` does not cross directories; search a tree containing a file
over 1 MiB, a binary file, and a symlink and confirm the skipped summary. Run a
command that emits about 1 MiB, then page the `full output:` path with a second
`run_command`; confirm the file is under the session work directory and is gone
after the run. Finally, confirm a seeded `toolset-v0` session resumes with the
v0 descriptions while a new session records `toolset-v1`.

For thinking effort: pick a model whose `-list-models` `EFFORT` column is
non-empty, choose one advertised value, and run a small tool-call round trip
with `AHARNESS_LOG_REQUESTS=1`; confirm the request body carries the value in
the dialect's field (top-level `reasoning_effort` for Chat, nested
`reasoning.effort` for Responses) and that the provider accepts the request.
Repeat for a model on each native SDK path the catalog uses
(`@ai-sdk/openai-compatible`, `@ai-sdk/openai`, `@ai-sdk/anthropic`,
`@ai-sdk/google`); if a translated path rejects the field, record the exact
model, value, and error, and do not claim support for that path. Confirm an
omitted setting sends no reasoning field, `-thinking-effort default` clears on
resume, and `session_start`/`run_start` record the effective value. In the TUI,
select an effort with `F2` and confirm the next run's request and `run_start`,
then switch to a model that does not advertise it and confirm the notice and
cleared value.

For thinking display: with a reasoning model and a non-minimal effort, confirm
the TUI shows a `thinking…` block that grows as the model thinks, settles to
`thinking` when the answer starts, and precedes the assistant text. Confirm the
session log has no thinking text (grep the JSONL for a phrase visible on
screen), that resume shows no prior thinking, and that `Enter` on a long
thinking block does not corrupt the transcript. Confirm a model or effort with
no thinking produces no block, and that `Ctrl-R` toggles only assistant
markdown.

For reasoning continuation: run a task that needs at least two tool rounds with
thinking enabled, once on a Chat model and once on a Responses model. Inspect
the session's request logging or a local proxy and confirm the second request
carries the native material — `reasoning_content` for Chat, and the raw
`reasoning` item before the matching `function_call` for Responses, together
with `include: ["reasoning.encrypted_content"]`. Confirm the model continues
its prior reasoning instead of restating it, and that no reasoning material
appears in the session log. If a provider rejects the echo, record the exact
model, request, and error; do not silently retry without it. When a native
Anthropic or Gemini adapter replaces the Chat translation, repeat this with
signed thinking blocks or `thoughtSignature` parts and confirm they are
replayed unmodified.

This real-provider continuation smoke has **not** been run: provider
credentials (`AHARNESS_PROVIDER_TOKEN`) are unavailable in the implementation
environment. It remains an explicit release prerequisite.

This real-provider thinking-effort and thinking-display smoke has **not** been
run: provider credentials (`AHARNESS_PROVIDER_TOKEN`) are unavailable in the
implementation environment. It remains an explicit release prerequisite, and
mock-provider or request-body tests do not claim that the hosted provider
accepted the value or returned thinking text.

For run budgets: confirm a default run can exceed the old 30-turn and 2M-token
thresholds without a `limit` event and without a wall-clock or per-call
deadline; resume or fork a seeded log with recorded budgets and confirm it
continues unbounded and the new `run_start` records `0`; exercise explicit
`-max-turns`, `-max-total-tokens`, `-session-timeout`, and `-model-timeout`
values and confirm each still stops or retries as documented. Confirm the TUI
status line shows `turns N` and `ctx N/L` and the details overlay shows
`unlimited`/`none` for the defaults. Record the real terminal,
`TERM`, exit code, and `stty -a` before/after.

For the TUI manual matrix, use at least one local emulator and tmux at 80x24 and
200x60 (tmux is not available in every CI environment; this is manual). Exercise
resize during streaming and a tool call, scrollback, long lines, CJK/emoji/
combining input and output, bracketed paste, `Esc`, first/second `Ctrl-C`, raw
Option-`[`/Option-`]`, `SIGTERM`, `SIGHUP`, and `Ctrl-D`; confirm no child
process or work directory survives. Mouse: confirm the wheel scrolls the
transcript and the open overlay, that `Shift`+drag selects text with the
terminal and `Shift`+middle-click pastes (Option in iTerm2), and that plain
drag and middle-click stay inert. Confirm the empty transcript's tips block and
the `F1` overlay both describe that bypass. For images, render a PNG chart, open
`Ctrl-G`, verify Kitty where available and glyph fallback elsewhere, and check
close/resize/scroll leave no stale image. Run `-ui-json` at 80x24 and 200x60 and
confirm deterministic frames/snapshots for a fixed fake provider. Inspect the raw log, confirm stdout/stderr
separation, confirm no delta events are logged, and confirm no
`<state>/work/<session-id>` directory remains. If a hosted model does not
stream, record the limitation and do not claim streaming support for it.

These are manual checks, not substitute measurements.

The v0 smoke passed for `deepseek-v4.1-flash` (2026-11-09) and
`muse-spark-1.3-contributor` (2026-11-10). v1 manual smoke results are recorded
with the implementation change that produced them. The thinking-effort
real-provider smoke is pending provider credentials and is a release
prerequisite, not a completed check.
