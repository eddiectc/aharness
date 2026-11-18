# Agent instructions

This repository is the **aharness SDK**, not the coding agent application. The
first-party coding agent is [acode](https://github.com/eddiectc/acode); it
depends on this module and owns the CLI, TUI, configuration, and presentation
layers. Do not add those layers here.

The Go coding standard is [docs/coding-standard.md](docs/coding-standard.md).
Read it before editing Go code. It is normative: **MUST** / **SHOULD** / **MAY**
are defined there, and it wins over other docs on Go practice. System
structure, tool behavior, the public two-level API, and the SDK runtime
contracts live in [docs/architecture.md](docs/architecture.md); the testing
strategy lives in [docs/testing-strategy.md](docs/testing-strategy.md); caps and
numbers live in [docs/limits.md](docs/limits.md). Every change verifies against
the project-wide [docs/definition-of-done.md](docs/definition-of-done.md). The
[documentation map](docs/README.md) explains where current reference, dated
plans, and research live.

Non-negotiables:

- Runtime dependencies stay at zero by default. Use the standard library;
  exceptions MUST satisfy the justification requirements in the coding standard
  §3. Do not add a framework, UUID library, JSON Schema validator, or retry
  library without that justification.
- The SDK is the root facade plus the `agent`, `engine`, `model`, `tools`, and
  `session` component packages. Do not create `pkg/`, `util`, `common`, or
  one-package-per-type. Do not add a CLI, TUI, config file, environment parser,
  signal handler, `os.Exit`, or terminal code; those belong to acode.
- Keep the recorded boundary in
  [architecture.md §3.2.1](docs/architecture.md#321-public-library-facade):
  no provider matrix, graph/workflow orchestration, memory/RAG, session-store
  seam, background or nested delegation, in-run parallel tool execution,
  structured-output/report DTOs, prompt templating, or public TUI package. A
  change that alters a recorded contract — session schema, prompt, tool grant,
  projection, or concurrency model — is a different product, not an extension.
- Keep the two API levels honest: the root facade copies DTOs at the boundary;
  component packages are public for first-party hosts and advanced integrations.
  Do not leak root-package types into component packages or make a component
  import the root.
- Route workspace paths through `os.Root` and `filepath.IsLocal`; reject `.git`
  components and symlink components for edits.
- Wrap errors with `%w`; branch with `errors.Is`/`errors.As`; never match error
  strings. Every tool call must be paired with exactly one result.
- Kill the whole process group on command timeout or cancellation; never leave
  child processes behind.
- Use `encoding/json/v2` + `jsontext` for JSONL; sanitize invalid UTF-8 before
  it reaches the transcript or log.
- Tests use fakes, `t.TempDir`, and `testing/synctest`; no real network or
  `time.Sleep`-based waiting.
- Before finishing, run `make dod` and fix failures. It runs the standard's
  §8 gates (`goimports`, `go vet`, `golangci-lint`, `go test -race -count=1`,
  `go build`, and `govulncheck`) plus `docs-check`.

When in doubt, prefer the smaller stdlib solution. If other docs conflict, the
coding standard wins on Go practice; note the conflict in the change.
