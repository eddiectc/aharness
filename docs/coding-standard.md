# Go coding standard

- **Date:** 2026-11-09
- **Status:** normative for all Go code in this repository
- **Toolchain:** Go 1.27.3; zero runtime dependencies
- **Related:** [v0 build plan](v0-build.md), [bare-minimum harness research](agent-harness-minimum.md)
- **Agent entry point:** [AGENTS.md](../AGENTS.md)

`docs/v0-build.md` defines what v0 ships. This document defines how the Go code
is written. If they conflict on product scope, the build plan wins; on Go
practice, this document wins.

Normative terms: **MUST** = required for merge; **SHOULD** = default, deviate
only with a recorded reason; **MAY** = optional.

## 1. Toolchain and module

- Use Go **1.27.3**. Keep `go 1.27.3` in `go.mod`; do not lower it. Go 1.27's
  `go test` runs the `stdversion` vet check by default, so the `go` line must
  admit every standard-library symbol used.
- The module is `aharness` for a local binary. Before publishing or installing
  from a URL, change the module to the repository import path (for example
  `git.sr.ht/~user/aharness`).
- Runtime dependencies: **zero**. The standard library is the default answer.
  A new runtime dependency MUST be justified in review against a stdlib
  alternative.
- Development tools are not runtime dependencies: `goimports`/`gofmt`, `go vet`,
  `golangci-lint`, `govulncheck`. Pin them in CI; do not commit a `tool`
  directive unless the team explicitly chooses that tradeoff.
- Commit `go.mod` and, when a dependency exists, `go.sum`. Run `go mod tidy`
  and `go mod verify`. No `replace` directives in committed code; no vendoring
  for this small binary.
- Run `go fix ./...` before review for modernizers, and `go doc -ex` when
  writing examples.
- Build release binaries with `-trimpath`; do not strip symbols by default.
  Tests with `-race` require cgo; the release build MAY use `CGO_ENABLED=0`.

## 2. Repository layout

v0 starts with these five packages:

```text
go.mod
cmd/aharness/
  main.go        # flags/env, wiring, signals, exit codes
  config.go      # parsing and validation
  config_test.go
internal/agent/
  runner.go      # loop, stop policy, repeated-failure detection
  context.go     # projection, token estimate, compaction
  retry.go
  stop.go
internal/model/
  types.go       # provider-neutral request/response/error types
  opencode.go    # the one adapter
  errors.go
internal/tools/
  registry.go
  root.go        # os.Root wrapper and path policy
  read.go
  edit.go
  search.go
  command.go
  output.go
internal/session/
  event.go       # event envelope only
  store.go       # append-only JSONL store and reader
```

This matches the package list in `v0-build.md` §3.1. `config` lives in `cmd`,
projection lives in `agent`, and workspace policy lives in `tools`.

Rules:

- `internal/` for all non-exported packages. `cmd/` only for `package main`
  commands. There is no `pkg/`, `api/`, `configs/`, or similar cargo-cult
  directory.
- `cmd/aharness` contains wiring and I/O only. No loop, tool, or provider logic
  in `main`.
- Dependency direction: `cmd -> agent -> {model, tools, session}`. The leaves
  MUST NOT import `agent` or `cmd`. `session` MUST remain a leaf and MUST NOT
  import `model` or `tools`.
- `internal/session` stores an envelope with `Data json.RawMessage`; producers
  own their payload structs and decode them on replay. This avoids cycles and
  keeps the store dumb.
- Do not create a package for one small function. Context projection lives in
  `agent`; config parsing lives in `cmd`; workspace containment lives in
  `tools`. Split when a package has a second consumer, independent tests, or a
  clear responsibility boundary.
- Package names are lowercase, short, and descriptive: no `util`, `common`,
  `types`, or `helper`.
- No “one type, one file” convention. Files SHOULD be focused enough that a
  maintainer knows where to look; avoid both thousand-line files and dozens of
  tiny files.
- Exported declarations MUST have doc comments starting with the name. Package
  comments are required when the package purpose is not obvious.
- `internal/session` event schema: `v`, `seq`, `ts`, `session`, `turn`, `type`,
  `role` (message events), `data`. Keep the envelope stable and versioned.

## 3. Dependencies

- Do not add a framework or library to save a few dozen lines. `flag`,
  `net/http`, `log/slog`, `encoding/json/v2`, `os/exec`, and `testing` cover v0.
- Specifically do not add: cobra/viper/urfave-cli, zap/logrus, testify/gomock,
  a JSON Schema validator, a UUID library, a retry/backoff library,
  SQLite/ORM, a dependency-injection framework, or a generic worker-pool
  library.
- Test-only exception: `github.com/google/go-cmp` MAY be added if struct diffs
  become materially harder to read. It is the only sanctioned test dependency;
  do not add testify.
- A new dependency proposal MUST state: the stdlib gap, maintenance/security
  posture, license, binary-size impact, and the `govulncheck` result. Prefer
  small, well-maintained modules.
- Tool descriptions and JSON schemas are hand-written `json.RawMessage`
  constants, versioned with the toolset. Validate arguments with typed structs
  and `RejectUnknownMembers(true)`; test that schema and struct agree.

## 4. API, errors, and control flow

### API

- Default to concrete types. Return concrete types; accept interfaces only when
  the caller needs substitutability.
- Define interfaces at the consumer, not the producer. `agent` MAY define
  unexported `completer`/`eventStore` interfaces for tests; `model` exports a
  concrete adapter; `tools` exports `*Registry`; `session` exports `*Store`.
- Keep interfaces to one or two methods. Do not create an interface for a
  single implementation unless a real test or substitution need exists. Do not
  export a test double from a production package.
- Avoid package-level mutable state. Wire dependencies in `main`; pass a
  logger, config, and store explicitly.
- No `init()` for wiring or configuration. Initialization errors are returned
  to `main`, which prints an actionable message and exits.
- `main` calls `os.Exit(run())`; `run() int` owns defers and cleanup. No
  `os.Exit` in library code.

### Errors

- Handle every returned error. Do not discard with `_` unless the operation is
  explicitly best-effort and the reason is commented.
- Wrap with `%w`; inspect with `errors.Is`/`errors.As`. Error strings are
  lowercase, have no trailing punctuation, and include the operation and path:
  `fmt.Errorf("read %s: %w", path, err)`.
- Sentinel errors live at package boundaries for caller decisions
  (`ErrOutsideWorkspace`, `ErrGitProtected`, `ErrEditAmbiguous`,
  `ErrEditNotFound`, `ErrBinaryFile`, `ErrTooLarge`). Use typed errors when the
  caller needs data.
- Never branch on error message text. Tool results carry a stable `Code`; the
  loop uses the code plus canonical arguments for repeated-failure detection.
- `StopReason` is a typed string: `completed`, `max_turns`, `token_limit`,
  `time_limit`, `provider_error`, `protocol_error`, `stuck`, `interrupted`,
  `internal_error`. Map it to an exit code in one function.
- Tool errors are data. Invalid args, missing files, ambiguity, timeout, spawn
  failure, and panic become `tool_result` events; they do not unwind the loop.
  Every assistant tool call MUST receive exactly one result with the same call
  ID before the next model call.
- A non-zero command exit is a normal result: `ok=true` with `exit_code`.
  `ok=false` is reserved for dispatch/validation/timeout/cancel/panic/spawn
  failure.

### Panics

- Do not use panic for control flow. Do not recover arbitrary panics.
- Recover only at the tool dispatcher boundary. Append a result for the call,
  log a capped stack, and then stop the run with `internal_error`. “Never unwind
  the loop” means pair the result and exit cleanly; it does not mean continuing
  after a potentially corrupted tool.

## 5. Context, concurrency, and signals

- `context.Context` is the first parameter of any function that does I/O or may
  block. Do not store a context in a struct.
- Use `signal.NotifyContext` for SIGINT/SIGTERM. The signal cancels the context;
  it MUST NOT write to the session log from another goroutine. The single runner
  goroutine owns all log writes.
- On shutdown, use a fresh
  `context.WithTimeout(context.Background(), 5*time.Second)` for final
  `final`/`session_end` writes and log sync; the main context is already
  canceled.
- One loop, one writer, one agent. No worker pools, no unmanaged goroutines.
  Document the exit condition of every goroutine. Prefer the `os/exec` pipe
  goroutines and `signal.NotifyContext` to hand-rolled concurrency.
- Retry sleeps MUST be context-aware. Use `time.NewTimer` and stop it; do not
  leak `time.After` timers in loops.
- Tool timeouts are per-call; the session deadline is a parent context.
  Distinguish `context.DeadlineExceeded` for a tool timeout from a canceled
  session when filling `timed_out`/`canceled`.
- SIGINT/SIGTERM stops with `interrupted` and exit 130. Kill the running child’s
  process group before writing the final events.

## 6. Filesystem and subprocess

### Workspace policy

- Canonicalize the workspace to an absolute path once at startup. Open
  `os.OpenRoot(workspace)` once and reuse it.
- Accept relative tool paths and absolute paths that resolve inside the
  workspace. `os.Root` rejects absolute names, so convert with `filepath.Rel`
  first, then require `filepath.IsLocal`.
- `os.Root` rejects `..`, absolute names, and symlink escapes. It rejects
  absolute symlinks even if they point inside. It does not prohibit bind mounts,
  `/proc`, or device files; it is an accident guard, not a sandbox.
- `.git` writes: reject any path with a `.git` component, including
  `sub/.git/config`. This is a component check, not a root-prefix check.
- Symlink policy: `read_file` MAY follow in-root symlinks; `search` does not
  descend into symlinked directories; `edit_file` MUST reject a symlink anywhere
  in the path. The last rule prevents a symlinked directory from bypassing the
  `.git` guard and prevents atomic rename from replacing a symlink instead of
  the target.
- `search` MUST require a real directory root; do not silently search nothing
  when the path is a symlink.
- Do not rely on path deny-lists for secrets. The real boundary is running
  without credentials and with external isolation.

```go
// rel normalizes a tool path. Root performs the real containment check.
func (w *Workspace) rel(p string) (string, error) {
	if filepath.IsAbs(p) {
		var err error
		if p, err = filepath.Rel(w.dir, p); err != nil {
			return "", err
		}
	}
	p = filepath.Clean(p)
	if !filepath.IsLocal(p) {
		return "", ErrOutsideWorkspace
	}
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if part == ".git" {
			return "", ErrGitProtected
		}
	}
	return p, nil
}
```

### Reads, writes, search

- Read cap 2 MiB; reject binary or invalid UTF-8. Read with a limit, not an
  unbounded `io.ReadAll`.
- `read_file`: 1-based offset, default limit 100, max 500. Define line counting:
  split on `\n`, a trailing newline does not create an extra line, an empty file
  has zero lines. Return `lines X–Y of Z` and a truncation marker at 32 KiB.
- `edit_file`: unique exact byte match unless `replace_all`. Zero matches and
  ambiguous matches are actionable errors. `old_string` empty creates only when
  the file is absent; the parent directory must exist. `old_string ==
  new_string` is an error. Preserve bytes, including CRLF; do not normalize line
  endings.
- Atomic edit: temp file in the same directory, `O_CREATE|O_EXCL`, random name;
  write; `f.Chmod(existingMode)` via the open file (fchmod, not path-based
  Chmod); `f.Sync`; close; `Root.Rename`. Remove the temp on error. New files
  use mode 0644; never write `.git`.
- `search`: Go RE2 regex, optional glob over workspace-relative slash paths,
  case-insensitive by default, max 200 results. Skip `.git`, binary files, and
  files over 1 MiB. Tell the model when results were capped. `path.Match` does
  not support `**`; document that.
- Long lines are clipped; output is valid UTF-8. Do not shell out to `rg` in
  v0; measure before adding it.

### Commands

`run_command` runs `/bin/sh -c` with the harness’s privileges. It is not a
sandbox.

- `cwd` defaults to the workspace root; resolve and contain it with the same
  path policy. Reject a cwd outside the workspace or not a directory.
- Timeout default 120 s, max 600 s. Reject out-of-range values with an
  actionable result.
- Own process group; kill the whole group on timeout or cancellation.
  `CommandContext` alone kills only the direct shell. Use `Setpgid`, a `Cancel`
  that kills `-pid`, and a `WaitDelay` to unblock inherited pipes.
- `stdin` is nil (`/dev/null`) so commands cannot hang waiting for input.
- Environment is an explicit allowlist, never `os.Environ`: `PATH`, `LANG`,
  `LC_ALL`, `TMPDIR`, `TERM`, plus validated cache variables. Set `HOME` and
  `TMPDIR` to harness-owned directories under the state dir, not the user’s real
  home. Never pass `AHARNESS_PROVIDER_TOKEN` or any credential. This is hygiene,
  not isolation.
- Capture stdout and stderr separately, cap combined output at 64 KiB head+tail
  with an explicit `[truncated N bytes]` marker, and sanitize invalid UTF-8
  before it reaches the transcript or JSON log.
- Non-zero exit is `ok=true` with `exit_code`. Timeout is `ok=false`,
  `timed_out=true`; spawn failure is `ok=false`; cancellation by signal is a
  canceled result. Do not automatically retry commands.
- On normal exit, a deliberately backgrounded grandchild MAY survive; document
  that and use external isolation when it matters.

In a Unix-only file (`//go:build unix`):

```go
cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
cmd.Dir, cmd.Env, cmd.Stdin = dir, env, nil
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
cmd.Cancel = func() error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
cmd.WaitDelay = 5 * time.Second
```

## 7. JSON, logs, and secrets

### JSON

- Use `encoding/json/v2` for new code. It is GA in Go 1.27, rejects duplicate
  object names and invalid UTF-8, and has faster unmarshaling. `encoding/json`
  is backed by v2 but keeps v1 semantics; do not mix semantics accidentally.
- Use `jsontext.NewEncoder` plus `jsonv2.MarshalEncode` for JSONL. It writes
  exactly one value plus newline per event and flushes per top-level value;
  there is no `Close`.
- Open the session file with `O_CREATE|O_WRONLY|O_APPEND|O_EXCL`, mode `0600`;
  create the state directory with `0700`. `f.Sync()` at turn boundaries and on
  exit. Treat a write failure as fatal: the run cannot be made durable.
- The reader accepts a valid final line without a trailing newline and ignores
  only a torn/invalid final line. Never rewrite the log. Validate `seq`
  monotonicity when replaying.
- `omitempty` in v2 means “encodes as empty JSON”, not “Go zero value”. Use
  `omitzero` for zero bool/number/pointer scalars, or write fields explicitly
  in the event schema.
- Sanitize all strings that cross a process or provider boundary to valid UTF-8
  before marshaling. Cap every payload at capture time and at log-field time;
  mark truncation.

```go
enc := jsontext.NewEncoder(f)
if err := jsonv2.MarshalEncode(enc, ev); err != nil {
	return fmt.Errorf("append %s event: %w", ev.Type, err)
}
// One value + '\n' per call. Call f.Sync() at turn boundaries.
```

### Logging and secrets

- `log/slog` to stderr for human progress. One line per model call and tool
  call. The JSONL session log is the durable record; it is not a human log.
- Never log full environments, HTTP headers, the provider token, or file
  contents in human/progress logs. The durable session log records only the
  capped event payloads it needs for replay; tool arguments and results stay in
  those capped payloads and never in stderr.
- Read `AHARNESS_PROVIDER_TOKEN` once at startup. Keep it in memory; redact it
  from provider error bodies and any diagnostic string. Test with a sentinel
  token: it MUST be absent from the log and from child environments.
- Provider error bodies are capped and redacted before logging.
  `AHARNESS_LOG_REQUESTS=1` logs the projected request, capped at 1 MiB with
  `truncated:true`; it never logs headers.
- Provider HTTP: one `http.Client` with a timeout, `http.NewRequestWithContext`,
  always close and cap the response body. Never use `http.DefaultClient`.
- Retry only model calls: 429, 5xx, connection reset before response. Honor
  `Retry-After` (seconds or HTTP-date) up to 60 s, add jitter with
  `math/rand/v2`, and make the sleep context-aware. Other 4xx fail fast. Never
  retry tool executions.
- Keep the adapter to one wire call with typed errors (`HTTPError`,
  `TransportError`, `ProtocolError`, context overflow). The agent owns
  retry/compaction policy. Do not add opaque provider options or capability
  flags until a second adapter exists.

## 8. CLI and configuration

- Use `flag`. No CLI framework in v0. No config file; flags and environment are
  sufficient.
- Precedence: defaults, then environment, then flags. Parse environment errors
  as config errors even if a flag would override them; validate the effective
  config once before starting the loop.
- Required: exactly one of `-task` or `-task-file`; `-model` or
  `AHARNESS_MODEL` from the allowlist; a workspace that exists and is a
  directory; a writable state directory.
- Limits: `-max-turns`, `-max-total-tokens`, `-max-context-tokens`,
  `-max-output-tokens`, `-session-timeout`. Validate positive values and
  `max-output-tokens < max-context-tokens` with the safety margin. Confirm model
  context windows; do not guess silently.
- State dir: `-state-dir`, then `AHARNESS_STATE_DIR`, then
  `$XDG_STATE_HOME/aharness`, then `~/.local/state/aharness`. Reject a relative
  `XDG_STATE_HOME`. Verify writability at startup.
- `-version` prints build info from `runtime/debug.ReadBuildInfo` (module
  version, Go version, VCS revision/modified) and exits 0.
- Output contract: final assistant text on stdout only; progress, trace path,
  and errors on stderr. Usage errors exit 2.
- Exit codes: `completed` 0; `max_turns`/`token_limit`/`time_limit` 2;
  `provider_error` 3; `protocol_error`/`stuck` 4; `interrupted` 130;
  `internal_error` 1.

## 9. Testing

- Table-driven tests for shared logic; separate test functions when the
  assertion logic differs. Subtest names are human-readable; include inputs in
  failure messages.
- No assert or mock frameworks. Write fakes at the consumer interface; use
  `httptest` for the provider adapter. Compare full structs; print got-before-
  want; use `t.Errorf` to keep going, `t.Fatal` for setup/subtest failure. Mark
  helpers with `t.Helper`.
- Test error identity with `errors.Is`/`errors.As` or typed errors, not message
  strings. Test message contents only for properties such as “includes the
  path”.
- Use `t.Context()`, `t.TempDir()`, and `t.Cleanup`. `t.Chdir` is process-wide
  and MUST NOT be used in parallel tests. `t.Setenv` also MUST NOT be used with
  `t.Parallel`.
- Use `testing/synctest` for retry/backoff, session deadlines, and other
  fake-time behavior; use `httptest.NewTestServer` and its `Client()` inside the
  bubble. Do not call `t.Run`, `t.Parallel`, or `t.Deadline` inside a bubble. Do
  not use synctest for real subprocess time; use a short real timeout there.
- Fuzz the JSONL reader (torn/truncated lines, invalid UTF-8, duplicate keys)
  and tool argument decoding. Keep a seed corpus in `testdata`.
- Golden session logs: decode and compare semantic fields; normalize timestamps
  and session IDs. Do not compare raw JSON bytes; serialization is not a stable
  contract.
- Integration: build the binary, run it against a fake provider in a temp
  workspace/state dir, and assert exit code, stdout, stderr, and log. Verify
  every tool call is paired, no child process survives a timeout/signal, and no
  sentinel token appears.
- Run `go test -race -count=1 ./...`. Check for leaked goroutines/children.
  Coverage is a diagnostic, not a merge gate.
- Do not write tests for wiring, forwarding, trivial getters, or incidental
  defaults. Test behavior, boundaries, invariants, errors, and transitions.

## 10. Tooling and review gates

Every change MUST pass:

```sh
goimports -l .          # or gofmt -l . if goimports is unavailable
go vet ./...
golangci-lint run
go test -race -count=1 ./...
mkdir -p bin
go build -trimpath -o bin/aharness ./cmd/aharness
govulncheck ./...
```

Lint configuration: keep the golangci-lint defaults and add only targeted
linters. The verified v2 config in `.golangci.yml`:

```yaml
version: "2"
linters:
  enable:
    - bodyclose
    - errorlint
    - noctx
```

- Do not install standalone `staticcheck`; it is bundled in golangci-lint.
- Do not require `gofumpt`; it is not part of the standard toolchain.
- Do not add a 50-linter config or a coverage threshold. Add a linter only when
  it catches a real class of bug in this codebase.
- CI: one Linux job, no OS matrix, `-count=1`, `govulncheck`; fuzzing MAY run on
  a schedule. Install `gcc` for `-race`.
- A Makefile is optional. CI commands are the source of truth; do not add a
  build system that hides them.
- `go fix ./...` before review is encouraged; do not run it in CI as a check.
  `go doc -ex` is the preferred way to inspect examples.

### Review checklist

- [ ] No new top-level package without a second consumer; no `pkg/`, `util`,
      `common`, or `types`.
- [ ] No new runtime dependency without a written justification; stdlib used
      where possible.
- [ ] Paths use the workspace policy (`os.Root`, `filepath.IsLocal`, `.git`
      component check, symlink rules).
- [ ] Every tool call is paired with exactly one result; timeouts kill the
      process group.
- [ ] No invalid UTF-8 or unbounded output reaches JSON, the transcript, or the
      log.
- [ ] No token, secret, full env, header, or uncapped provider body is logged.
- [ ] Errors wrap with `%w`; no string matching; error strings lowercase.
- [ ] Context is first-parameter, deadlines/cancellation are respected,
      goroutines have documented exits.
- [ ] Tests use fakes/temp dirs/synctest; no real network or `time.Sleep`-based
      waiting.
- [ ] `goimports`, `go vet`, `golangci-lint`, `go test -race`, `go build`,
      `govulncheck` pass.
- [ ] Exported names have doc comments; new Go 1.27 APIs match the `go`
      directive.

## 11. Verified Go 1.27 notes

These were checked against the installed Go 1.27.3 toolchain, not recalled:

| Fact | Consequence |
|---|---|
| `encoding/json/v2` and `jsontext` are available without `GOEXPERIMENT`; v2 rejects duplicate object names and invalid UTF-8. `jsontext.Encoder` writes one newline-terminated value per top-level value and has no `Close`. | Use v2 + `jsontext` for JSONL; sanitize strings; no hand-rolled JSON. |
| `uuid.NewV7()` is in the standard library. | Do not add `github.com/google/uuid`. Use UUIDv7 for session IDs; keep the RFC3339Nano `ts`. |
| `os.Root` rejects `..`, absolute names, and symlink escapes. It rejects absolute symlinks even inside the root. It does not block bind mounts, `/proc`, or device files. Unix `Chmod`/`Chown`/`Chtimes` have a TOCTOU race. | Normalize absolute inputs; define the symlink policy; treat Root as an accident guard, not a sandbox. |
| `exec.CommandContext` kills only the direct child; `Cmd.Cancel` and `Cmd.WaitDelay` control the group and inherited pipes. | Use the `Setpgid` + group-kill + `WaitDelay` pattern for `run_command`. |
| `testing/synctest` provides fake time; `httptest.NewTestServer` uses an in-memory network; `synctest.Sleep` exists. `t.Run`, `t.Parallel`, and `t.Deadline` are forbidden inside a bubble. | Use synctest for retry/deadline tests; use real short timeouts for subprocesses. |
| `go test` invokes `stdversion` vet by default; `go mod init` writes `go 1.27.3`. | Keep the `go` line honest; new stdlib symbols require Go 1.27. |
| `log.Exit`/`log.Exitf` are not in the Go 1.27 standard library. | Use `slog` and a returned exit code; do not copy the Google style guide literally here. |
| `go fix` has `atomictypes`, `embedlit`, `slicesbackward`, and `unsafefuncs` modernizers; `go doc -ex` prints runnable examples. | Run `go fix ./...` before review; use `go doc -ex` while coding. |

## Sources

- [Organizing a Go module](https://go.dev/doc/modules/layout)
- [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- [Go Test Comments](https://go.dev/wiki/TestComments)
- [Google Go style: best practices](https://google.github.io/styleguide/go/best-practices)
- [Security best practices for Go](https://go.dev/doc/security/best-practices)
- [Go 1.27 release notes](https://go.dev/doc/go1.27)
- [os.Root](https://pkg.go.dev/os#Root), [os/exec.Cmd](https://pkg.go.dev/os/exec#Cmd),
  [encoding/json/v2](https://pkg.go.dev/encoding/json/v2),
  [uuid](https://pkg.go.dev/uuid),
  [testing/synctest](https://pkg.go.dev/testing/synctest)
