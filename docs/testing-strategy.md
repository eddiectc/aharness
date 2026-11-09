# aharness v0 testing strategy

- **Date:** 2026-11-09
- **Status:** normative testing strategy for v0
- **Related:** [system architecture](architecture.md), [Go coding standard](coding-standard.md), [v0 definition of done](definition-of-done.md)

v0 uses three layers: unit tests for component behavior, integration tests
against a fake provider, and a manual smoke run against a real provider. Unit
and integration tests use fakes, temp directories, and `testing/synctest`; they
do not use real network access or `time.Sleep`-based waiting.

## 1. Unit tests

- Workspace containment: `..`, absolute-path conversion, absolute path outside,
  symlink-to-outside, absolute symlink, in-root symlink read, symlink component
  in an edit, `.git` component (including `sub/.git`), new file in a symlinked
  parent, symlinked search root.
- `edit_file`: unique match, zero matches, multiple matches, `replace_all`,
  create-only-when-absent, atomic write, mode preservation, `fchmod`, old==new,
  CRLF preservation, symlink rejection.
- `read_file`: paging, line counting (empty/trailing newline), binary rejection,
  size cap, truncation marker, byte preservation.
- `search`: regex errors, glob errors, skip rules, result cap/capped flag,
  long-line clip, `**` unsupported, real-directory root.
- `run_command`: non-zero exit as data, timeout process-group kill, `WaitDelay`
  on inherited pipes, output cap, UTF-8 sanitization, cwd escape rejection,
  environment allowlist (`HOME`/`TMPDIR`/token), nil stdin.
- Context: budget invariants, pairing, compaction determinism, initial-task and
  recent-turn retention through `agent.Project`.
- Session: JSONL round-trip, store-assigned `v`/`seq`/`ts`/`session`, valid final
  line without newline, torn/invalid line tolerance, append-only behavior,
  `Sync` durability, 0700/0600 modes, UUIDv7 IDs, secret redaction, `json/v2`
  strictness.
- Loop: scripted model sequences for all terminal states; every call paired;
  panic paired then `internal_error`; duplicate call IDs stop with
  `protocol_error`; code-based stuck detection with canonical-argument
  identity.
- Fuzz: JSONL reader (torn/truncated/invalid UTF-8/duplicate keys) and tool
  argument decoding.

Tests follow the coding standard: table-driven where cases share logic; no
assert or mock frameworks; fakes at consumer interfaces; `httptest` for the
provider adapter; got-before-want failure messages with `t.Helper`; test error
identity with `errors.Is`/`errors.As`, not strings; `t.Context`,
`t.TempDir`, and the `t.Chdir`/`t.Setenv` parallel restrictions; no real network
or `time.Sleep`-based waiting. Inside a `synctest` bubble, do not call `t.Run`,
`t.Parallel`, or `t.Deadline`. Golden session logs are decoded and compared
semantically; do not test wiring, forwarding, or trivial getters; coverage is a
diagnostic, not a gate.

## 2. Integration tests

- Build the binary and run it against a fake `opencode-go` server in a temp
  workspace and temp state directory.
- Assert the persisted log, stdout final text, stderr progress, and exit code.
- Exercise both model IDs through the Chat Completions path and verify the
  adapter sends the selected model, `x-opencode-session`, and a self-identifying
  `User-Agent`; include a tool-call round trip for each model so translation
  quirks surface.
- Cover 429→success, 401 fast-fail, malformed/duplicate-key/invalid-UTF-8 JSON,
  and context overflow.
- Run the suite with `-race -count=1` and `govulncheck ./...`; no leaked
  goroutines or child processes. Use `synctest` for retry/deadline tests; use
  real short timeouts for subprocess tests.
- CI runs the standard's §8 gates in one Linux job, with `gcc` installed for
  `-race`; fuzzing MAY run on a schedule.
- CI also runs `go mod tidy`/`go mod verify`; a dirty `go.mod` or `go.sum`
  fails.

## 3. Manual smoke

Run the built binary against the real `opencode-go` provider with each model on a
small temporary repository; set `AHARNESS_PROVIDER_TOKEN` if the endpoint
requires auth, and set `-max-context-tokens` to the confirmed window. Verify one
read, one edit, one search, and one test command, then inspect the raw session
log. Optionally repeat the run inside an external sandbox to confirm the harness
behaves the same with or without isolation.

This is a manual check, not a built-in eval suite. External benchmarking and
eval infrastructure are out of scope for v0.

