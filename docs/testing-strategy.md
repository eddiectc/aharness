# aharness v1 testing strategy

- **Date:** 2026-11-11
- **Status:** normative testing strategy for v1, the shipped TUI, and model discovery
- **Related:** [system architecture](architecture.md), [Go coding standard](coding-standard.md), [v1 build plan](v1-build.md), [TUI build plan](tui-build.md), [shipped TUI](tui.md), [model discovery build plan](model-discovery-build.md)

v1 uses four layers: unit tests for component behavior, integration tests
against a fake provider and the built binary, fuzz targets at the session and
SSE trust boundaries, and a manual smoke run against a real provider. Unit and
integration tests use fakes, `httptest`, `t.TempDir`, and `testing/synctest`;
they do not use real network access or `time.Sleep`-based waiting, except short
real timeouts for subprocess and signal tests.

## 1. Unit tests

- Workspace and tools: containment (`..`, absolute-path conversion, symlink
  escapes, `.git`), the four tool contracts, result codes, output caps, UTF-8
  sanitization, process-group cancellation, and the environment allowlist.
- Provider adapters: Chat Completions and Responses canonical mapping, tool-call
  round trips, finish/usage/error normalization, strict JSON decoding, and
  Responses `store:false` replay.
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
- Streaming adapters: SSE text and tool-call assembly for both dialects;
  `[DONE]`; comments, CRLF, and multi-line data; malformed JSON, duplicate
  names, invalid UTF-8; 4 MiB event, 1 MiB assembled-response, and 8 MiB
  total-stream caps; missing usage; non-SSE JSON fallback; error-body
  redaction; and `PartialError` after the first display delta.
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
  fallback printed once, retry before the first delta, no retry after a delta,
  partial error fields, and oversized responses rejected before tools.
- TUI/display: display-event order for streamed, fallback, tool, retry, and
  error paths; `RunStarted` model/dialect/resume sequence; display failure maps
  to `internal_error` and pairs tool calls; nil display disables events.
- History: multiple runs, run headers, assistant text once, tool pairing,
  canceled/panic/repaired results, anomalies, hints/notices, partial text,
  fork metadata, malformed payloads, unknown events/fields, and caps.
- TUI app: composer editing/history, submit vs steering, steering ack and
  unacknowledged requeue, first/second `Ctrl-C`, `Ctrl-D`, `Esc`, overlay
  focus/restore, rich/raw toggle, table/file/model/image overlays, model picker
  next-run-only, workspace containment, sanitizer, and raw Option-`[`/Option-`]`
  input through a fake input pipe. Status line renders `turns N`/`tokens N`
  for unlimited budgets and `N/M` only for explicit positive budgets, never
  `0/0`; the details overlay renders `unlimited`/`none`; old `max_turns`/
  `time_limit` terminal states still render.
- Headless UI: `-ui-json` decode/encode, caps, `wait` conditions/timeouts,
  frame/style/cursor/image metadata, SVG determinism, forced glyph mode, and
  no ANSI/raw image bytes. `x/vt` cell/style fixtures cover wide/CJK, combining
  marks, emoji, and cursor state.
- Markdown/images: Glamour fixtures for headings/emphasis/code/lists/tables/
  links, raw HTML inert, over-deep/over-block fallback, workspace-contained
  PNG/JPEG/GIF decode caps, placeholders, glyph thumbnails, viewer, and
  oversize/unsupported/symlink/escape rejection.
- Interrupt/control: model interrupt hint; tool interrupt pairs the interrupted
  call and unstarted calls; TUI steering consumed at turn boundaries and before
  a completed response; batch non-interactive signal behavior.
- Projection: prompt/toolset selection by version; run/control events ignored;
  resumed and forked histories; repaired calls paired; retained turns.
- Config: new flags; mutual exclusion; task rules; inheritance; explicit
  overrides; provider validation and precedence; list/refresh terminal actions;
  model resolution and provider mismatch; base-URL credential/query/fragment
  rejection; exit-code mapping; unlimited `0` defaults for turns/tokens/session
  timeout/model timeout; explicit `0` and explicit positive values; negative
  rejection naming the flag; recorded-budget ignoring on resume/fork.

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
  `streamed=true`; no delta events are written.
- Resume: a limit-stopped run resumes under the same session ID with inherited
  model/workspace/base URL/context/output values; recorded run budgets are not
  inherited; a torn final line is repaired and recorded;
  the same session log gains a second `run_start` and `final`. Resume/fork with
  the catalog server down and no explicit model still starts from the recorded
  values; an explicit model change resolves through the catalog; an explicit
  provider mismatch exits 2.
- `-list-models`: assert the tab-separated header and rows, exit 0, no session
  file, no task/token requirement, and stale-cache fallback.
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
and the models.dev catalog decoder at the trust boundary. Seeds live under
`internal/model/testdata/fuzz` and `internal/session/testdata/fuzz`; the
TUI/history targets use built-in seeds. The unit test run executes the seeds; a
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

For run budgets: confirm a default run can exceed the old 30-turn and 2M-token
thresholds without a `limit` event and without a wall-clock or per-call
deadline; resume or fork a seeded log with recorded budgets and confirm it
continues unbounded and the new `run_start` records `0`; exercise explicit
`-max-turns`, `-max-total-tokens`, `-session-timeout`, and `-model-timeout`
values and confirm each still stops or retries as documented. Confirm the TUI
status line shows `turns N`/`tokens N` and the details overlay shows
`unlimited`/`none` for the defaults. Record the real terminal,
`TERM`, exit code, and `stty -a` before/after.

For the TUI manual matrix, use at least one local emulator and tmux at 80x24 and
200x60 (tmux is not available in every CI environment; this is manual). Exercise
resize during streaming and a tool call, scrollback, long lines, CJK/emoji/
combining input and output, bracketed paste, `Esc`, first/second `Ctrl-C`, raw
Option-`[`/Option-`]`, `SIGTERM`, `SIGHUP`, and `Ctrl-D`; confirm no child
process or work directory survives. For images, render a PNG chart, open
`Ctrl-G`, verify Kitty where available and glyph fallback elsewhere, and check
close/resize/scroll leave no stale image. Run `-ui-json` at 80x24 and 200x60 and
confirm deterministic frames/snapshots for a fixed fake provider. Inspect the raw log, confirm stdout/stderr
separation, confirm no delta events are logged, and confirm no
`<state>/work/<session-id>` directory remains. If a hosted model does not
stream, record the limitation and do not claim streaming support for it.

This is a manual check, not a built-in eval suite. External benchmarking and
eval infrastructure are out of scope.

The v0 smoke passed for `deepseek-v4.1-flash` (2026-11-09) and
`muse-spark-1.3-contributor` (2026-11-10). v1 manual smoke results are recorded
with the implementation change that produced them.
