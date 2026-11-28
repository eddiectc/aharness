# aharness documentation map

This directory holds the current reference for the SDK. Plans and research are
work records in GitHub issues, not committed documents; a decision that binds
only one change lives in the commit body. The root [README](../README.md) is
the SDK entry point and describes the shipped surface. The first-party coding
agent, [acode](https://github.com/eddiectc/acode), keeps its CLI, TUI, commands,
and configuration docs in its own repository.

## Where a document belongs

1. **Current reference** — describes behavior that must be true on `main`
   today. Lives in `docs/`. Update it in the same change as the code.
2. **Work record** — a plan with milestones, its deviations, and its
   verification, or the evidence behind one decision. Lives in a GitHub issue
   ([writing standard §8](writing-standard.md#8-plans-as-issues)) and is never
   committed. `.issues/`, `docs/plans/`, and `docs/research/` are retired and
   MUST NOT be recreated.
3. **Research** — dated sources and measured results belong in the issue that
   needed them, or inline where the reference uses the finding.

A work record carries intent; the current reference carries the shipped truth.
An issue stays editable for its whole life: tick milestones and record
deviations as the work lands, and correct a claim the code has outgrown instead
of leaving it to mislead the next reader.

## Precedence

- [coding-standard.md](coding-standard.md) wins on Go practice.
- The current reference documents win on structure, runtime contracts, numeric
  limits, and shipped SDK surface: [architecture.md](architecture.md) and its
  area pages under [architecture/](architecture/), [limits.md](limits.md), and
  [testing-strategy.md](testing-strategy.md).
- A work record in an issue records intent and evidence; it never overrides the
  current reference. Where they disagree, the reference wins. Losing precedence
  is not immutability: an issue or pull request that a later change contradicts
  is corrected or closed, so no reader is told to leave a record standing when
  the code has outgrown it.
- [definition-of-done.md](definition-of-done.md) applies to every task, fix,
  feature, refactor, and documentation change.

## Index

### Current reference

| Document | Defines |
|---|---|
| [architecture.md](architecture.md) | SDK architecture index: system overview, precedence, recorded boundary, and one page per area under [architecture/](architecture/) |
| [coding-standard.md](coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [definition-of-done.md](definition-of-done.md) | Normative completion checklist for every change |
| [writing-standard.md](writing-standard.md) | Normative prose, comment, commit, issue, and pull-request rules |
| [limits.md](limits.md) | The single source for SDK caps, limits, and timeouts |
| [testing-strategy.md](testing-strategy.md) | Unit, integration, fuzz, and manual smoke layers |
| [ci-release.md](ci-release.md) | CI triggers, DoD gates, and the tag/release process |
| [../SECURITY.md](../SECURITY.md) | Security scope, non-goals, supported versions, and how to report |
| [../CHANGELOG.md](../CHANGELOG.md) | Released versions and the public-surface changes in each |

## Deleted records

`docs/plans/` (16 files) and `docs/research/` (5 files) were removed on
2026-11-27 by #25 and its sub-issues #26–#29. Every open item moved to #26,
and the deletion commit is `f0e54f3`:

```sh
git ls-tree -r --name-only f0e54f3^ docs/plans docs/research
git show f0e54f3^:docs/plans/subagent-build.md
```

## Link check

`make docs-check` verifies that relative links and image targets in `README.md`,
`AGENTS.md`, `SECURITY.md`, and `docs/**/*.md` resolve, and that a `#fragment`
matches a heading on the target page using GitHub's slug rules (lowercase,
punctuation dropped, underscores kept, spaces as hyphens). Only inline links are
read, and code fences are ignored so examples are not links. `make dod` runs this
check as part of the full automated definition-of-done set.
