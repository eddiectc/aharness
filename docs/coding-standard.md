# Go coding standard

- **Date:** 2026-11-20
- **Status:** normative for all Go code in this repository
- **Toolchain:** Go 1.27.4; stdlib-first, with the approved extractor exception
- **Related:** [system architecture](architecture.md), [bare-minimum harness research](research/agent-harness-minimum.md)
- **Agent entry point:** [AGENTS.md](../AGENTS.md)

This document defines how the Go code is written. It does not define
architecture, tool behavior, protocols, or SDK runtime contracts; those live in
[`architecture.md`](architecture.md). If the documents conflict on structure or
runtime contracts, the architecture document wins; on Go practice, this
document wins.

Normative terms: **MUST** = required for merge; **SHOULD** = default, deviate
only with a recorded reason; **MAY** = optional.

## 1. Toolchain and module

- Use Go **1.27.4**. Keep `go 1.27.4` in `go.mod`; do not lower it. Go 1.27's
  `go test` runs the `stdversion` vet check by default, so the `go` line must
  admit every standard-library symbol used.
- The module is `github.com/eddiectc/aharness`. The root package is the
  high-level facade, and `agent`, `engine`, `model`, `tools`, and `session` are
  public component packages for hosts. A breaking public API change gets a new
  major module path. Do not add `internal/`, `cmd/`, a CLI, or a TUI here; the
  first-party coding agent lives in the separate acode module.
- Runtime dependencies: **stdlib by default**. The standard library is the
  default answer; a new runtime dependency MUST be justified in review against
  a stdlib alternative. The approved extractor exception is in §3. The TUI
  stack is an acode dependency, not an SDK dependency. No dependency may be
  added outside the recorded decisions without a new review.
- Development tools are not runtime dependencies: `goimports`/`gofmt`, `go vet`,
  `golangci-lint`, `govulncheck`. Pin them in CI; do not commit a `tool`
  directive unless the team explicitly chooses that tradeoff.
- Commit `go.mod` and, when a dependency exists, `go.sum`. Run `go mod tidy`
  and `go mod verify`. No `replace` directives in committed code; no vendoring.
- Run `go fix ./...` before review for modernizers, and `go doc -ex` when
  writing examples.
- Build with `-trimpath`; do not strip symbols by default. Tests with `-race`
  require cgo.

## 2. Packages and layout

The root facade plus the five component packages, and their dependency
direction, are defined in [`architecture.md` §3.1](architecture.md); that
document is normative for this repository's package structure. The rules below
apply to every package.

- No `internal/`, `cmd/`, `pkg/`, `api/`, `configs/`, or similar cargo-cult
  directory. Every package is either the root facade or a documented component
  package.
- The root facade contains public DTOs and composition over the component
  packages; it contains no loop, tool, or provider implementation.
- Respect the documented dependency direction. A component package MUST NOT
  import the root facade or an application.
- Do not create a package for one small function. Split when a package has a
  second consumer, independent tests, or a clear responsibility boundary.
- Package names are lowercase, short, and descriptive: no `util`, `common`,
  `types`, or `helper`.
- No “one type, one file” convention. Files SHOULD be focused enough that a
  maintainer knows where to look; avoid both thousand-line files and dozens of
  tiny files.
- Exported declarations MUST have doc comments starting with the name. Package
  comments are required when the package purpose is not obvious.

## 3. Dependencies

- Do not add a framework or library to save a few dozen lines. `flag`,
  `net/http`, `log/slog`, `encoding/json/v2`, `os/exec`, and `testing` cover v0.
- Specifically do not add: cobra/viper/urfave-cli, zap/logrus, testify/gomock,
  a JSON Schema validator, a UUID library, a retry/backoff library,
  SQLite/ORM, a dependency-injection framework, or a generic worker-pool
  library.
- The TUI stack is not an SDK dependency. Bubble Tea v2, Bubbles v2, Lip Gloss
  v2, Glamour v2, ntcharts v2, `x/vt`, their direct support packages, and
  `golang.org/x/term` belong to the acode repository. The SDK must stay
  headless; a TUI import here is a dependency-gate failure.
- `github.com/markusmobius/go-trafilatura/v2` v2.2.6 is the approved
  main-content extractor for `web_fetch`. It is Apache-2.0, native Go, and
  CGO-free; v2.2.6 is the current release of a maintained port with published
  extraction benchmarks. The stdlib gap is content selection: `golang.org/x/net/html` parses
  a DOM but does not separate an article from page chrome, and a bespoke
  extractor was rejected. Measured 2026-11-13 on a stripped
  `-trimpath -ldflags=-s -w` build: 19,009,696 to 37,707,936 bytes (+18.7 MB,
  +98%); `go list -m all` grew from 53 to 106 modules. `govulncheck ./...`
  reports no vulnerabilities after the `golang.org/x/net` v0.63.0 upgrade
  (re-verified 2026-11-20 with `gomoji` v1.4.1, `go-dateparser` v1.4.8,
  `go-colorable` v0.1.16, `go-re2` v1.13.0, `x/sys` v0.50.0, `x/text` v0.44.0;
  `make dod` green). The
  approved fallback if this decision is reversed is
  `codeberg.org/readeck/go-readability/v2` v2.1.3 (MIT, about +5.2 MB); do not
  ship both.
- Test-only exception: `github.com/google/go-cmp` MAY be added if struct diffs
  become materially harder to read. It is the only sanctioned test dependency;
  do not add testify.
- A new dependency proposal MUST state: the stdlib gap, maintenance/security
  posture, license, binary-size impact, and the `govulncheck` result. Prefer
  small, well-maintained modules.

## 4. API, errors, and control flow

### API

- Default to concrete types. Return concrete types; accept interfaces only when
  the caller needs substitutability.
- Define interfaces at the consumer, not the producer. Keep interfaces to one
  or two methods. Do not create an interface for a single implementation unless
  a real test or substitution need exists. Do not export a test double from a
  production package.
- The concrete contracts (`model.OpenAIChat`, `model.OpenAIResponses`,
  `tools.Registry`, `session.Store`, `agent.Project`) are defined in
  [`architecture.md` §3.2](architecture.md).
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
- Sentinel errors live at package boundaries for caller decisions. Use typed
  errors when the caller needs data. Tool sentinel errors and result codes are
  defined in [`architecture.md` §3.2 and §5](architecture.md).
- Never branch on error message text. Use `errors.Is`/`errors.As`, typed
  errors, or a stable result code supplied by the boundary. Never compare raw
  argument strings.
- When an error crosses a process or protocol boundary, convert it to data:
  return a result carrying the stable code and a short message. Do not unwind
  the control loop for a boundary failure unless the failure makes the session
  state untrustworthy.

### Panics

- Do not use panic for control flow. Do not recover arbitrary panics.
- Recover only at a documented boundary such as a tool or request dispatcher.
  Convert the panic to a returned result or error, log a capped stack, and take
  the caller's documented failure path (which may stop the run). Do not
  continue executing a component that may be corrupted.

## 5. Context and concurrency

- `context.Context` is the first parameter of any function that does I/O or may
  block. Do not store a context in a struct.
- Use `signal.NotifyContext` for SIGINT/SIGTERM. A signal handler MUST only
  cancel the context; it MUST NOT perform I/O or mutate shared state from
  another goroutine. One owning goroutine performs state and log writes.
- No unmanaged goroutines or worker pools. Document the exit condition of every
  goroutine. Prefer the `os/exec` pipe goroutines and `signal.NotifyContext` to
  hand-rolled concurrency.
- Retry sleeps MUST be context-aware. Use `time.NewTimer` and stop it; do not
  leak `time.After` timers in loops. Cancellation is not a retryable failure.
- Distinguish `context.Canceled` from `context.DeadlineExceeded` when mapping a
  result. The parent context's cancellation is not a per-call timeout.
- A shutdown path that must write after cancellation uses a fresh bounded
  context; the main context is already canceled.

## 6. I/O, JSON, and logs

### Filesystem and subprocess

Workspace containment, tool limits, and command policy are in
[`architecture.md` §5](architecture.md). These are the general Go rules:

- Read with a limit, never an unbounded `io.ReadAll`. Reject binary or invalid
  UTF-8 at a boundary and sanitize before the bytes reach a transcript or log.
- Atomic replacement: create a temp file in the same directory with
  `O_CREATE|O_EXCL` and a random name; write; call `f.Chmod(existingMode)` on
  the open file (fchmod, not path-based `Chmod`); `f.Sync`; close; rename.
  Remove the temp file on error. New files default to mode 0644 unless the
  product specifies otherwise.
- Use `os.Root` and `filepath.IsLocal` for workspace containment. `os.Root` is
  an accident guard, not a sandbox.
- `exec.CommandContext` kills only the direct child. When a command owns a
  process group, use `Setpgid`, a `Cancel` that kills `-pid`, and a `WaitDelay`
  to unblock inherited pipes. In a Unix-only file (`//go:build unix`):

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

- Set `stdin` to nil for non-interactive commands so they cannot hang waiting
  for input.
- Pass an explicit environment allowlist; never inherit all of `os.Environ`
  for commands that may be untrusted.
- Cap captured output and sanitize it to valid UTF-8 before it reaches the
  transcript or JSON log. Mark truncation.
- Do not automatically retry side-effecting commands or tool executions.

### JSON

- Use `encoding/json/v2` for new code. It is GA in Go 1.27, rejects duplicate
  object names and invalid UTF-8, and has faster unmarshaling.
  `encoding/json` is backed by v2 but keeps v1 semantics; do not mix semantics
  accidentally.
- Use `jsontext.NewEncoder` plus `jsonv2.MarshalEncode` for JSONL. It writes
  exactly one value plus newline per event and flushes per top-level value;
  there is no `Close`.
- `omitempty` in v2 means “encodes as empty JSON”, not “Go zero value”. Use
  `omitzero` for zero bool/number/pointer scalars, or write fields explicitly
  in the schema.
- Sanitize all strings that cross a process or provider boundary to valid UTF-8
  before marshaling. Cap every payload at capture time and at log-field time;
  mark truncation.
- Event-log and provider-decoding specifics are in
  [`architecture.md` §7 and §2.2](architecture.md).

### Logging and secrets

- Use `log/slog` to stderr for human progress. The durable JSONL log is the
  record for replay; it is not a human log.
- Never log full environments, HTTP headers, credentials, or file contents in
  human/progress logs. Keep secrets in memory; redact them from error bodies
  and diagnostic strings, and cap every body before logging.
- Provider token handling, request logging, HTTP client limits, and retry
  policy are defined in [`architecture.md` §2, §7, and §8](architecture.md).

## 7. Testing

- Table-driven tests for shared logic; separate test functions when the
  assertion logic differs. Subtest names are human-readable; include inputs in
  failure messages.
- No assert or mock frameworks. Write fakes at the consumer interface; use
  `httptest` for HTTP adapters. Compare full structs; print got-before-want;
  use `t.Errorf` to keep going, `t.Fatal` for setup/subtest failure. Mark
  helpers with `t.Helper`.
- Test error identity with `errors.Is`/`errors.As` or typed errors, not message
  strings. Test message contents only for properties such as “includes the
  path”.
- Use `t.Context()`, `t.TempDir()`, and `t.Cleanup`. `t.Chdir` is process-wide
  and MUST NOT be used in parallel tests. `t.Setenv` also MUST NOT be used with
  `t.Parallel`.
- Use `testing/synctest` for retry/backoff, deadlines, and other fake-time
  behavior; use `httptest.NewTestServer` and its `Client()` inside the bubble.
  Do not call `t.Run`, `t.Parallel`, or `t.Deadline` inside a bubble. Do not
  use synctest for real subprocess time; use a short real timeout there.
- Fuzz decoders at trust boundaries with a seed corpus in `testdata`. Golden
  outputs are decoded and compared semantically; normalize timestamps and
  generated IDs, and do not compare raw serialized bytes.
- Run `go test -race -count=1 ./...`. Check for leaked goroutines and child
  processes. Coverage is a diagnostic, not a merge gate.
- Do not write tests for wiring, forwarding, trivial getters, or incidental
  defaults. Test behavior, boundaries, invariants, errors, and transitions.

## 8. Tooling and review gates

Every change MUST pass:

```sh
goimports -l .          # or gofmt -l . if goimports is unavailable
go vet ./...
golangci-lint run
go test -race -count=1 ./...
go build -trimpath ./...
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
- [ ] Paths use the documented containment policy (`os.Root`,
      `filepath.IsLocal`).
- [ ] Errors wrap with `%w`; no string matching; error strings lowercase.
- [ ] Context is first-parameter, deadlines/cancellation are respected,
      goroutines have documented exits.
- [ ] No invalid UTF-8 or unbounded output reaches JSON, the transcript, or the
      log.
- [ ] No token, secret, full env, header, or uncapped body is logged.
- [ ] Tests use fakes/temp dirs/synctest; no real network or `time.Sleep`-based
      waiting.
- [ ] `goimports`, `go vet`, `golangci-lint`, `go test -race`, `go build`,
      `govulncheck` pass.
- [ ] Exported names have doc comments; new Go 1.27 APIs match the `go`
      directive.
- [ ] The repository non-negotiables in [AGENTS.md](../AGENTS.md) and the
      product invariants in [`architecture.md`](architecture.md) also hold.

## 9. Appendix: verified Go 1.27 facts

Reference only; the rules are above. These were checked against the installed
Go 1.27.4 toolchain, not recalled:

| Fact | Consequence |
|---|---|
| `encoding/json/v2` and `jsontext` are available without `GOEXPERIMENT`; v2 rejects duplicate object names and invalid UTF-8. `jsontext.Encoder` writes one newline-terminated value per top-level value and has no `Close`. | Use v2 + `jsontext` for JSONL; sanitize strings; no hand-rolled JSON. |
| `uuid.NewV7()` is in the standard library. | Do not add `github.com/google/uuid`; use the standard package. |
| `os.Root` rejects `..`, absolute names, and symlink escapes. It rejects absolute symlinks even inside the root. It does not block bind mounts, `/proc`, or device files. Unix `Chmod`/`Chown`/`Chtimes` have a TOCTOU race. | Normalize absolute inputs; treat Root as an accident guard, not a sandbox. |
| `exec.CommandContext` kills only the direct child; `Cmd.Cancel` and `Cmd.WaitDelay` control the group and inherited pipes. | Use the `Setpgid` + group-kill + `WaitDelay` pattern for child process groups. |
| `testing/synctest` provides fake time; `httptest.NewTestServer` uses an in-memory network; `synctest.Sleep` exists. `t.Run`, `t.Parallel`, and `t.Deadline` are forbidden inside a bubble. | Use synctest for retry/deadline tests; use real short timeouts for subprocesses. |
| `go test` invokes `stdversion` vet by default; `go mod init` writes `go 1.27.4`. | Keep the `go` line honest; new stdlib symbols require Go 1.27. |
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
