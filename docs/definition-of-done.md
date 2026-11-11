# aharness definition of done

- **Date:** 2026-11-11
- **Status:** normative for every task, fix, feature, refactor, and
  documentation change
- **Applies to:** every implementation and every build plan in
  [docs/plans](plans/)
- **Related:** [Go coding standard](coding-standard.md),
  [system architecture](architecture.md),
  [testing strategy](testing-strategy.md), [limits](limits.md), and the
  [documentation map](README.md)

A change is done only when every applicable item below holds. Do not silently
skip an item: state why it does not apply. A build plan adds feature-specific
criteria in its §1.4, but it never replaces this checklist.

Precedence: the [coding standard](coding-standard.md) wins on Go practice; the
current reference documents ([architecture](architecture.md),
[limits](limits.md), [tui](tui.md), [commands](commands.md),
[testing strategy](testing-strategy.md)) win on structure and runtime
contracts. A plan records intent and decisions; where a plan and the current
reference disagree, the reference wins and the plan records the deviation.

## 1. Behavior and scope

- [ ] **Requested behavior.** The task works end to end, and every acceptance
      criterion in the issue or build plan is satisfied.
- [ ] **No unfinished work.** No stubs, placeholders, fake fallbacks,
      `TODO: implement`, or misleading "scaffold" claims. If a real
      implementation needs unavailable information or credentials, name the
      missing prerequisite exactly.
- [ ] **Clean cutover.** Every caller is migrated; obsolete code, aliases,
      re-exports, and compatibility shims are removed.
- [ ] **Regression discipline.** A bug fix starts from a failing test or a
      clear user report; add a regression test only when no existing test
      covers the path.
- [ ] **Edge behavior.** Invalid input, empty results, cancellation, timeouts,
      partial failure, and concurrent access produce typed, documented
      outcomes.
- [ ] **Errors and pairing.** Errors wrap with `%w` and are classified with
      `errors.Is`/`errors.As`; every tool call has exactly one result.

## 2. Verification

- [ ] **Narrowest check.** The test, command, or actual UI/CLI surface that
      exercises the change was run and passed once. Do not claim a check that
      was not run.
- [ ] **Automated DoD checks.** `make dod` passes. It runs the
      [coding standard](coding-standard.md) §8 gates (`goimports`, `go vet`,
      `golangci-lint`, `go test -race -count=1 ./...`, `go build -trimpath`,
      and `govulncheck ./...`) plus `docs-check`.
- [ ] **Fuzzing.** Touched trust boundaries have fuzz coverage,
      or the existing fuzz targets still pass.
- [ ] **Provider behavior.** Wire-format, streaming, or model-control changes
      pass the fake-provider tests and a real-provider smoke. If credentials
      are unavailable, record the exact pending check and do not describe it as
      verified.

## 3. Contracts and documentation

- [ ] **Current reference updated in the same change.** Use the single source
      of truth for each topic:
      [architecture](architecture.md) for structure, protocols, and CLI;
      [limits](limits.md) for caps and numbers;
      [tui](tui.md) and [commands](commands.md) for the shipped surface;
      [testing strategy](testing-strategy.md) for test layers.
- [ ] **No stale authority.** A plan does not claim to be normative for
      behavior that ships; it records intent, milestones, deviations, and
      verification.
- [ ] **Plan status.** If the work came from a build plan, update its status,
      §1.4 checklist, and deviations section.
- [ ] **User-facing surface.** A user-visible change updates the README
      feature description, examples, or CLI contract as applicable.
- [ ] **New interfaces.** New flags, environment variables, exit codes, events,
      and protocol fields are documented in the current reference and the
      README where users see them.
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
- [ ] **Handoff.** The change description states what changed, what was
      verified, and any gate, smoke, or prerequisite still pending.

The historical v0 verification record is preserved in
[v0-build.md §6](plans/v0-build.md#6-v0-completion-record).
