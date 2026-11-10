# aharness v1 testing strategy

- **Date:** 2026-11-10
- **Status:** normative testing strategy for v1
- **Related:** [system architecture](architecture.md), [Go coding standard](coding-standard.md), [v1 build plan](v1-build.md)

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
- Resume/fork state: cumulative turns/usage reconstruction; pending-call
  detection; pending-call repair before `run_start`; task matrix; inherited
  model/workspace/base URL/limits; explicit overrides; cumulative-limit checks;
  workspace mismatch; unsupported prompt/toolset; parent immutability.
- Loop: scripted model sequences for all terminal states; every call paired;
  panic paired then `internal_error`; duplicate call IDs stop with
  `protocol_error`; code-based stuck detection; one `final` and one `run_end`
  per run; run-local output and usage with cumulative totals.
- Streaming loop: deltas emitted once, no reprint after deltas, non-streaming
  fallback printed once, retry before the first delta, no retry after a delta,
  partial error fields, and oversized responses rejected before tools.
- Interactive: model interrupt hint; tool interrupt pairs the interrupted call
  and unstarted calls; steering consumed at turn boundaries and before a
  completed response; EOF and line caps; non-interactive signal behavior.
- Projection: prompt/toolset selection by version; run/control events ignored;
  resumed and forked histories; repaired calls paired; retained turns.
- Config: new flags; mutual exclusion; task rules; inheritance; explicit
  overrides; base-URL credential/query/fragment rejection; exit-code mapping.

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

- Build the binary and run it against a fake `opencode-go` server in a temp
  workspace and temp state directory. Point the binary at the fake server with
  `-provider-base-url` or `AHARNESS_PROVIDER_BASE_URL`; the default remains the
  hosted URL.
- Assert the persisted log, stdout final/streamed text, stderr progress, exit
  code, file modes, and that the run removed `<state>/work/<session-id>`.
- Exercise every accepted model ID through its assigned dialect path and verify
  the adapter sends the selected model, `x-opencode-session`, and a
  self-identifying `User-Agent`; include a tool-call round trip for each
  dialect.
- Streaming: for both dialects, a delta is visible on stdout while the fake
  provider is still blocked; the log has one `model_response` with
  `streamed=true`; no delta events are written.
- Resume: a limit-stopped run resumes under the same session ID with inherited
  model/workspace/base URL/limits; a torn final line is repaired and recorded;
  the same session log gains a second `run_start` and `final`.
- Fork: a completed session forks into a new self-contained log with parent
  metadata; the parent hash is unchanged; the child contains the parent prefix
  with a new session ID and renumbered `seq` plus the new task.
- Locks: a second `-resume` against a run paused in the provider exits 2 with
  the trace path.
- Interactive: the first `SIGINT` during a model call records `interrupt` and
  the interrupt hint, stdin steering reaches the next call, and the run
  completes; a second `SIGINT` exits 130; a tool interrupt pairs remaining calls
  with `code=canceled` and kills the process group; non-interactive signals
  remain terminal.
- Crash/schema: a v0 log is rejected with a clear error and is unchanged;
  unknown v2 events and fields are tolerated.
- Caps: an oversized response stops with `protocol_error` and executes no tool;
  a 1 MiB streamed response round-trips through the log.
- Run the suite with `-race -count=1` and `govulncheck ./...`; no leaked
  goroutines or child processes. Use `synctest` for retry/deadline tests; use
  real short timeouts for subprocess tests.
- CI runs the standard's §8 gates in one Linux job, with `gcc` installed for
  `-race`; fuzzing MAY run on a schedule.
- CI also runs `go mod tidy`/`go mod verify`; a dirty `go.mod` or `go.sum`
  fails.

## 3. Fuzzing

Fuzz targets cover the SSE decoder and the v2 JSONL reader/repair path, with
seed corpora under `internal/model/testdata/fuzz` and
`internal/session/testdata/fuzz`. The unit test run executes the seeds; a
scheduled job MAY run longer fuzzing. The fuzz targets must not make real
network calls or require writable state.

## 4. Manual smoke

Run the built binary against the real `opencode-go` provider with each
implemented dialect on a small temporary repository; set
`AHARNESS_PROVIDER_TOKEN` if the endpoint requires auth, and set
`-max-context-tokens` to the confirmed window. OpenCode currently restricts
`crouton-alpha-free` to OpenCode clients; use `muse-spark-1.3-contributor` for
the Responses smoke. It requires a Go subscription and the workspace privacy
setting that allows paid endpoints to train on request data.

For each dialect: run a non-streamed task, a streamed task, interrupt and steer,
resume the session, and fork it. Inspect the raw log, confirm stdout/stderr
separation, confirm no delta events are logged, and confirm no
`<state>/work/<session-id>` directory remains. If a hosted model does not
stream, record the limitation and do not claim streaming support for it.

This is a manual check, not a built-in eval suite. External benchmarking and
eval infrastructure are out of scope.

The v0 smoke passed for `deepseek-v4.1-flash` (2026-11-09) and
`muse-spark-1.3-contributor` (2026-11-10). v1 manual smoke results are recorded
with the implementation change that produced them.
