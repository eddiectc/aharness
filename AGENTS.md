# Agent instructions

The Go coding standard is [docs/coding-standard.md](docs/coding-standard.md).
Read it before editing Go code. It is normative: **MUST** / **SHOULD** / **MAY**
are defined there, and it wins over other docs on Go practice.

Non-negotiables:

- Runtime dependencies stay at zero. Use the standard library; do not add a
  framework, UUID library, JSON Schema validator, or retry library.
- Start with the five-package layout in the standard. Do not create `pkg/`,
  `util`, `common`, or one-package-per-type.
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
- Before finishing, run the gates in the standard's §10 and fix failures:
  `goimports`, `go vet`, `golangci-lint`, `go test -race -count=1`, `go build`,
  `govulncheck`.

When in doubt, prefer the smaller stdlib solution. If other docs conflict, the
coding standard wins on Go practice; note the conflict in the change.
