# Agent instructions

The Go coding standard is [docs/coding-standard.md](docs/coding-standard.md).
Read it before editing Go code. It is normative: **MUST** / **SHOULD** / **MAY**
are defined there, and it wins over other docs on Go practice. Product scope
and milestones live in [docs/v1-build.md](docs/v1-build.md) (v0 history is in
[docs/v0-build.md](docs/v0-build.md)); system structure,
tool behavior, and CLI/config contracts live in
[docs/architecture.md](docs/architecture.md); the testing strategy lives in
[docs/testing-strategy.md](docs/testing-strategy.md); current completion
criteria live in [docs/v1-build.md §1.4](docs/v1-build.md#14-definition-of-done)
(the historical v0 checklist is in [docs/definition-of-done.md](docs/definition-of-done.md)).

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
- Before finishing, run the gates in the standard's §8 and fix failures:
  `goimports`, `go vet`, `golangci-lint`, `go test -race -count=1`, `go build`,
  `govulncheck`.

When in doubt, prefer the smaller stdlib solution. If other docs conflict, the
coding standard wins on Go practice; note the conflict in the change.
