# aharness CI and release management

- **Date:** 2026-11-20
- **Status:** current for the aharness SDK: CI runs on every commit to `main`
  and releases are cut from deliberate `vX.Y.Z` tags.
- **Related:** [coding standard](coding-standard.md) §8 (review gates),
  [testing strategy](testing-strategy.md) (test layers),
  [definition of done](definition-of-done.md) (completion checklist), and
  [CHANGELOG](../CHANGELOG.md) (released versions). The first-party coding
  agent, acode, documents its own release process in its own repository.

Every commit to `main` runs the full automated definition-of-done gates. Tags
cut releases; CI never tags. The `Makefile` is the source of truth for what
runs: both workflows call `make dod` and add nothing beside it.

## 1. CI

Workflow: [ci.yml](../.github/workflows/ci.yml).

### 1.1 Triggers

- `push` to `main`.
- `pull_request` targeting `main`.
- `workflow_dispatch` for a manual re-run.

There is one Linux job and no OS matrix, per the
[coding standard](coding-standard.md) §8. A concurrent run on the same ref
cancels the older one.

### 1.2 What runs

Exactly `make dod`:

```text
docs-check + goimports/gofmt + go vet + golangci-lint +
go test -race -count=1 ./... + go build -trimpath ./... + govulncheck ./...
```

This covers the [testing strategy](testing-strategy.md) unit and integration
layers: deterministic tests over fakes, `t.TempDir`, and `testing/synctest`,
with no real network and no `time.Sleep`-based waiting. Fuzz targets are not
part of per-commit CI; see §3.

### 1.3 Tool pinning

Go comes from `go.mod` (`go-version-file`), so the workflow tracks the
toolchain directive instead of hard-coding a version. The remaining dev tools
are pinned in the workflow `env` block so CI matches the local gates:

| Tool | Pin | Notes |
|---|---|---|
| Go | `go.mod` (`go 1.27.3`) | `actions/setup-go@v7` reads `go-version-file` |
| goimports | `golang.org/x/tools` v0.51.0 | Falls back to `gofmt` only locally, never in CI |
| golangci-lint | v2.15.0 (2026-10-16) | Via `golangci/golangci-lint-action@v9`; config is [.golangci.yml](../.golangci.yml) |
| govulncheck | `golang.org/x/vuln` v1.9.0 | |
| gcc | `apt-get install gcc` | Required for `-race` (cgo) |
| actions/checkout | v7 | Node24 runtime; needs runner v2.327.1+ |
| actions/setup-go | v7 | ESM internals; no input/behavior change from v6 |

Dependabot (or a manual bump) keeps the `actions/*` versions current; a tool
bump updates the workflow `uses:` lines, the workflow `env`, and this table in
the same change.

### 1.4 Hardening

- `permissions: contents: read` (least privilege); no secrets are used because
  the tests are hermetic.
- `timeout-minutes: 20` on the job; cancelled automatically on a newer run.
- Third-party actions are version-pinned; prefer a full commit SHA when the
  team adopts SHA pinning.
- CI never writes tags, releases, or commits.

### 1.5 Branching and protection

`main` is protected: every change lands via a pull request. Do not commit
directly to `main` — humans and agents alike work on a short-lived feature
branch cut from `main`:

1. `git checkout main && git pull && git checkout -b <topic>`.
2. Make the change; keep the diff focused (see the
   [definition of done](definition-of-done.md) §5).
3. Run `make dod` locally and fix failures.
4. Push the branch and open a pull request targeting `main`.
5. Merge only when CI (`make dod`) is green and review is satisfied.

This is GitHub Flow (branch → PR → `main` → tag), not classic gitflow:
there is no `develop` branch. Releases are tags on `main` (§2), so `main`
stays releasable and a red `main` is fixed first.

### 1.6 Local reproduction

Run the same gates before pushing:

```sh
make dod
```

CI commands are the source of truth; do not add a second list of checks that
can drift from the `Makefile` (see the
[coding standard](coding-standard.md) §8).

### 1.7 What CI does not do

- No auto-tagging: every `main` commit is verified, but only a deliberate tag
  is a release. A tag-per-commit would force meaningless version bumps and
  pollute the module proxy.
- No `go fix` as a check, no coverage gate, no `staticcheck` outside
  golangci-lint, no `gofumpt`: see the
  [coding standard](coding-standard.md) §8 for what is deliberately excluded.

## 2. Releases

Workflow: [release.yml](../.github/workflows/release.yml). It runs on
`v*.*.*` tag pushes only.

### 2.1 Versioning rules

- Tags are `vX.Y.Z`; the leading `v` is required for Go modules. Use annotated
  tags: `git tag -a v0.5.0 -m "Release v0.5.0"`.
- Before `v1.0.0`, a minor release may carry a breaking public-surface change;
  each one is listed under **Changed** or **Removed** in
  [CHANGELOG](../CHANGELOG.md) with a **Migration** note, per the
  [definition of done](definition-of-done.md) §1. From `v1.0.0`, a breaking
  change requires a new major module path.
- The session schema version stays independent of the module version.
- `Version()` reports the version from the build info embedded at compile time
  (see `version.go`), so a binary built from an untagged commit reports
  `devel`. Consumers get the release version by building the tagged module.

### 2.2 CHANGELOG rules

The [CHANGELOG](../CHANGELOG.md) follows Keep a Changelog. Work accumulates
under `## [Unreleased]`; a release moves it to `## [vX.Y.Z] - YYYY-MM-DD`
with `Added` / `Changed` / `Removed` entries. Breaking entries carry
`Migration:` notes. CI-only or doc-only changes with no public-surface effect
are not changelog entries.

### 2.3 Cutting a release

1. Move `## [Unreleased]` to `## [vX.Y.Z] - YYYY-MM-DD` in
   [CHANGELOG](../CHANGELOG.md), with migration notes for every breaking
   item.
2. Run `make dod` locally and fix failures.
3. Commit the release: `git commit -m "Release vX.Y.Z"`.
4. Tag the commit: `git tag -a vX.Y.Z -m "Release vX.Y.Z"`.
5. Push both: `git push origin main vX.Y.Z`.
6. The `release` workflow re-runs `make dod` on the tag and verifies:
   the tag is `vX.Y.Z`, the tag is on `main`, the tag has a
   `## [vX.Y.Z]` CHANGELOG entry, and `go.mod`/`go.sum` are tidy.
   It then creates the GitHub Release (CHANGELOG stays authoritative;
   generated notes are supplementary).
7. Confirm the version resolves on the Go module proxy
   (`go list -m github.com/eddiectc/aharness@vX.Y.Z` from a clean checkout).

Record any pending manual smoke (hosted-endpoint loop, adapter, tool, or
session-schema changes) in the release notes or the plan; never describe an
unrun smoke as verified, per the [definition of done](definition-of-done.md)
§2 and the [testing strategy](testing-strategy.md) §4.

### 2.4 Rollback

Never move or delete a published tag. A bad release is superseded by a new
`vX.Y.Z` (patch for a compatible fix, minor for a pre-1.0 breaking fix) with
its own CHANGELOG entry.

## 3. Fuzzing

Fuzz targets cover the trust boundaries listed in the
[testing strategy](testing-strategy.md) §3 (`model` decoders, `session` JSONL
reader and torn-line repair, `agent` history and definition parsing, `tools`
extraction and URL policy). Run each target for a bounded time before a risky
change or on a schedule (for example a weekly workflow running
`go test -fuzz=<target> -fuzztime=60s` per package); add a seed for every
regression. Fuzzing stays out of per-commit CI.

## 4. Maintenance

- Bumping Go means changing the `go` directive in `go.mod` (never lowering
  it), then verifying `make dod` locally; CI follows automatically via
  `go-version-file`.
- Bumping a dev tool means updating the workflow `env` pins, this document's
  §1.3 table, and the local installs in the same change.
- Workflow changes are verified by opening a PR (CI runs on it) and, for
  `release.yml`, by dry-running the verify steps locally against the intended
  tag before pushing it.
