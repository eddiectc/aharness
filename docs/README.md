# aharness documentation map

This directory separates current reference from dated plans and research. The
root [README](../README.md) is the SDK entry point; it describes the shipped
surface and does not index plans or research. The first-party coding agent,
[acode](https://github.com/eddiectc/acode), keeps its CLI, TUI, commands, and
configuration docs in its own repository.

## Where a document belongs

1. **Current reference** — describes behavior that must be true on `main`
   today. Lives in `docs/`. Update it in the same change as the code.
2. **Build plan** — a dated record of one initiative: scope, contract deltas,
   milestones, decisions, deviations, and verification. Lives in `docs/plans/`.
3. **Research** — dated evidence, source synthesis, and recommendations that
   inform a plan. Not a contract. Lives in `docs/research/`.

Naming: plans use `<topic>-build.md`; research uses `<topic>-research.md`. A
plan is `proposed`, `implemented`, `superseded`, or a historical record. Once
implemented, a plan is frozen except for status, deviations, and verification
notes. Research is a frozen snapshot; add a `Superseded by:` line instead of
rewriting its conclusions.

## Precedence

- [coding-standard.md](coding-standard.md) wins on Go practice.
- The current reference documents win on structure, runtime contracts, numeric
  limits, and shipped SDK surface: [architecture.md](architecture.md),
  [limits.md](limits.md), and [testing-strategy.md](testing-strategy.md).
- A dated plan or research document records intent and evidence; it never
  overrides the current reference. Where they disagree, the reference wins.
  Plans and research are frozen: later behavior changes update the reference,
  never the dated record.
- [definition-of-done.md](definition-of-done.md) applies to every task,
  documentation change, and dated record.

## Index

### Current reference

| Document | Defines |
|---|---|
| [architecture.md](architecture.md) | SDK structure, component packages, loop and tool contracts, session log, and the public facade |
| [coding-standard.md](coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [definition-of-done.md](definition-of-done.md) | Normative completion checklist for every change |
| [limits.md](limits.md) | The single source for SDK caps, limits, and timeouts |
| [testing-strategy.md](testing-strategy.md) | Unit, integration, fuzz, and manual smoke layers |
| [ci-release.md](ci-release.md) | CI triggers, DoD gates, and the tag/release process |
| [../CHANGELOG.md](../CHANGELOG.md) | Released versions and the public-surface changes in each |

### Build plans

| Document | Status |
|---|---|
| [plans/agent-tooling-build.md](plans/agent-tooling-build.md) | Implemented M0–M3; real-provider smoke pending; eval tooling removed 2026-11-14 |
| [plans/go-library-build.md](plans/go-library-build.md) | Implemented M0–M6 and M8 docs; real-provider library smoke pending |
| [plans/limits-removal-build.md](plans/limits-removal-build.md) | Implemented 2026-11-11 |
| [plans/model-discovery-build.md](plans/model-discovery-build.md) | Implemented 2026-11-11 |
| [plans/prompt-surface-build.md](plans/prompt-surface-build.md) | Implemented 2026-11-19 as v0.3.0 work; real-model probes pending |
| [plans/prompt-transparency-build.md](plans/prompt-transparency-build.md) | Implemented and released 2026-11-22 as v0.5.0 (v0.4.2 was an accidental tag of the same source); real-session smoke pending |
| [plans/subagent-build.md](plans/subagent-build.md) | Implemented 2026-11-15 (milestones 1–4); real-provider smoke pending |
| [plans/subagent-observation-build.md](plans/subagent-observation-build.md) | Implemented 2026-11-23; real-provider observation smoke pending |
| [plans/thinking-effort-build.md](plans/thinking-effort-build.md) | Implemented 2026-11-11; real-provider smoke pending |
| [plans/v0-build.md](plans/v0-build.md) | Historical; v0 complete |
| [plans/v1-build.md](plans/v1-build.md) | Implemented; real-provider streamed smoke pending |
| [plans/web-fetch-build.md](plans/web-fetch-build.md) | Implemented 2026-11-13; public-page and Crawl4AI smoke passed |
| [plans/web-search-build.md](plans/web-search-build.md) | Implemented 2026-11-13; manual SearXNG smoke passed |
| [plans/workspace-instructions-build.md](plans/workspace-instructions-build.md) | Implemented and released 2026-11-19 as v0.4.0; fresh-acode check pending |

### Research

| Document | Snapshot |
|---|---|
| [research/agent-harness-minimum.md](research/agent-harness-minimum.md) | 2026-11-09; minimum harness and primary sources |
| [research/agent-tooling-research.md](research/agent-tooling-research.md) | 2026-11-11; tool set, limits, usage, and evaluation methodology |
| [research/evaluation-research.md](research/evaluation-research.md) | 2026-11-15; private, harness-agnostic evaluation design, task sourcing, metrics, and experiment options |
| [research/go-library-research.md](research/go-library-research.md) | 2026-11-16; comparable Go agent libraries and coding-harness SDK embedding APIs |
| [research/subagent-research.md](research/subagent-research.md) | 2026-11-15; delegation and multi-agent features in Claude Code, Codex, OMP, and OpenCode, and which fit aharness |

## Link check

`make docs-check` verifies that relative links and image targets in repository
Markdown files resolve. Code fences are ignored so examples are not treated as
links. `make dod` runs this check as part of the full automated
definition-of-done set.
