# Agent instructions

The Go coding standard is [docs/coding-standard.md](docs/coding-standard.md).
Read it before editing Go code. It is normative: **MUST** / **SHOULD** / **MAY**
are defined there, and it wins over other docs on Go practice. Product scope
and milestones live in [docs/plans/v1-build.md](docs/plans/v1-build.md) (v0
history is in [docs/plans/v0-build.md](docs/plans/v0-build.md)); system
structure, tool behavior, and CLI/config contracts live in
[docs/architecture.md](docs/architecture.md); the flag/env/config-file layers
are in [docs/configuration.md](docs/configuration.md); the testing strategy
lives in
[docs/testing-strategy.md](docs/testing-strategy.md). Every implementation and
build plan verifies against the project-wide
[docs/definition-of-done.md](docs/definition-of-done.md); feature-specific
criteria are in the plan's §1.4. The
[documentation map](docs/README.md) explains where current reference, plans,
and research live.

Non-negotiables:

- Runtime dependencies stay at zero by default. Use the standard library;
  exceptions MUST satisfy the justification requirements in the coding standard
  §3. Do not add a framework, UUID library, JSON Schema validator, or retry
  library without that justification.
- Start with the five-package layout in `docs/architecture.md` §3.1. Do not
  create `pkg/`, `util`, `common`, or one-package-per-type.
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
  `go build`, `govulncheck`) plus `docs-check`.

When in doubt, prefer the smaller stdlib solution. If other docs conflict, the
coding standard wins on Go practice; note the conflict in the change.
