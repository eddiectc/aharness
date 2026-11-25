# aharness definition of done

- **Date:** 2026-11-18
- **Status:** normative for every task, fix, feature, refactor, and
  documentation change
- **Applies to:** every change in this repository, and every dated record in
  [docs/plans](plans/) and [docs/research](research/)
- **Related:** [Go coding standard](coding-standard.md),
  [SDK architecture](architecture.md),
  [testing strategy](testing-strategy.md), [limits](limits.md), and the
  [documentation map](README.md)

A change is done only when every applicable item below holds. Do not silently
skip an item: state why it does not apply.

Precedence: the [coding standard](coding-standard.md) wins on Go practice; the
current reference documents ([architecture](architecture.md),
[limits](limits.md), [testing strategy](testing-strategy.md)) win on structure
and runtime contracts. A dated plan or research document records intent and
evidence; where one disagrees with the current reference, the reference wins.
Precedence is not immutability: a plan under implementation is edited as the
work lands, and a dated record is corrected when it turns out to be wrong.

## 1. Behavior and scope

- [ ] **Requested behavior.** The task works end to end, and every acceptance
      criterion in the issue or change request is satisfied.
- [ ] **No unfinished work.** No stubs, placeholders, fake fallbacks,
      `TODO: implement`, or misleading "scaffold" claims. If a real
      implementation needs unavailable information or credentials, name the
      missing prerequisite exactly.
- [ ] **Clean cutover.** Every caller is migrated; obsolete code, aliases,
      re-exports, and compatibility shims are removed.
- [ ] **SDK boundary.** The change stays inside the root facade and the five
      component packages. It adds no CLI, TUI, environment, config file, signal
      handler, terminal I/O, or `os.Exit`; those belong to acode.
- [ ] **Recorded contracts.** A public-library change passes the compatibility
      rules in
      [architecture §3.2.1](architecture.md#321-public-library-facade). A
      change that alters the session schema, prompt, tool grant, projection, or
      concurrency model is a different product, not an extension. No new
      provider matrix, graph/workflow orchestration, memory/RAG, pluggable
      session store, background or nested delegation, structured-output DTOs,
      prompt templating, or public TUI package.
- [ ] **Public API compatibility.** Adding a field or event is backward
      compatible; adding a method to `Model` or `Tool` is not. Before `v1.0.0`,
      a minor release may rename or remove an exported identifier or change an
      exported method signature; the change is listed under **Changed** or
      **Removed** in `CHANGELOG.md` with a migration step. From `v1.0.0` such a
      change requires a major module version. The session schema version stays
      independent of the module version.
- [ ] **Two API levels.** The root facade copies DTOs at the boundary and does
      not expose component types as aliases. A component package does not import
      the root facade.
- [ ] **Errors and pairing.** Errors wrap with `%w` and are classified with
      `errors.Is`/`errors.As`; every tool call has exactly one result.

## 2. Verification

- [ ] **Narrowest check.** The test, command, or public API surface that
      exercises the change was run and passed once. Do not claim a check that
      was not run.
- [ ] **Automated DoD checks.** `make dod` passes. It runs the
      [coding standard](coding-standard.md) §8 gates (`goimports`, `go vet`,
      `golangci-lint`, `go test -race -count=1 ./...`, `go build -trimpath
      ./...`, and `govulncheck ./...`) plus `docs-check`.
- [ ] **Fuzzing.** Touched trust boundaries have fuzz coverage, or the existing
      fuzz targets still pass.
- [ ] **Provider behavior.** Wire-format, streaming, or model-control changes
      pass the fake-provider tests and a real-provider smoke. If credentials
      are unavailable, record the exact pending check and do not describe it as
      verified.

## 3. Contracts and documentation

- [ ] **Current reference updated in the same change.** Use the single source
      of truth for each topic: [architecture](architecture.md) for structure,
      protocols, and the public facade; [limits](limits.md) for caps and
      numbers; [testing strategy](testing-strategy.md) for test layers.
- [ ] **No stale authority.** A plan does not claim to be normative for
      behavior that ships; it records intent, milestones, deviations, and
      verification.
- [ ] **Dated records stay current.** A plan under implementation is updated as
      the work lands — status, decisions, deviations, and verification — and a
      claim that the code has outgrown is corrected or marked superseded. The
      current reference still carries the shipped truth; the plan never becomes
      the authority.
- [ ] **User-facing surface.** A public API change updates the README feature
      description and examples.
- [ ] **New interfaces.** New exported types, fields, methods, errors, and
      events are documented in the current reference and the README where
      embedders see them.
- [ ] **Honest docs.** Documented behavior matches exercised behavior; pending
      checks and environment limits are named, not implied.

## 4. Safety, limits, and cleanup

- [ ] **Workspace containment.** Paths go through `os.Root` and
      `filepath.IsLocal`; `.git` components and symlink escapes are rejected.
- [ ] **Secrets.** No token, secret, full environment, header, or uncapped body
      reaches logs, the transcript, or child environments.
- [ ] **Encoding and caps.** Invalid UTF-8 is sanitized before it reaches the
      transcript or log; payload and output caps are enforced.
- [ ] **Process and runtime cleanup.** Timeout and cancellation kill the whole
      process group; no child process survives; the per-run work directory
      cleanup is attempted on normal, error, and signal paths.
- [ ] **Limit semantics.** The [limits](limits.md) table reflects any changed
      value; defaults stay opt-in where documented; recorded session limits are
      not silently reinterpreted.
- [ ] **Dependencies.** A new runtime dependency satisfies
      [coding standard §3](coding-standard.md#3-dependencies); otherwise use
      the standard library.

## 5. Repository hygiene

- [ ] **Focused diff.** The change touches only intended files; unrelated
      formatting, generated noise, and drive-by refactors are absent.
- [ ] **No scaffolding.** Throwaway scripts, debug output, temporary files, and
      local-fetch helpers are removed.
- [ ] **Module files.** `go.mod` and `go.sum` change only for an approved
      dependency change.
- [ ] **Placement.** New documents follow the placement rules in the
      [documentation map](README.md).
