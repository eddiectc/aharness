# aharness documentation map

This directory separates current reference from dated plans and research. The
root [README](../README.md) is the user-facing entry point; it describes shipped
features and does not index plans or research.

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
  limits, and shipped surface: [architecture.md](architecture.md),
  [limits.md](limits.md), [tui.md](tui.md), [commands.md](commands.md), and
  [testing-strategy.md](testing-strategy.md).
- A plan records intent and decisions; it never overrides the current
  reference. Where they disagree, the reference wins and the plan records the
  deviation.
- [definition-of-done.md](definition-of-done.md) applies to every task and
  every build plan. A plan's §1.4 adds feature-specific criteria; it does not
  replace the project checklist.

## Index

### Current reference

| Document | Defines |
|---|---|
| [architecture.md](architecture.md) | System structure, packages, loop and tool contracts, CLI, and configuration |
| [configuration.md](configuration.md) | Flag > environment > config file > default precedence, the JSON config file, environment variables, and configurable vs fixed limits |
| [coding-standard.md](coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [commands.md](commands.md) | Shipped slash commands, autocomplete, arguments, and `-ui-json` mapping |
| [definition-of-done.md](definition-of-done.md) | Normative completion checklist for every change |
| [limits.md](limits.md) | The single source for caps, limits, and timeouts |
| [testing-strategy.md](testing-strategy.md) | Unit, integration, fuzz, and manual smoke layers |
| [tui.md](tui.md) | Shipped TUI modes, keys, protocol, caps, and verification status |

### Build plans

| Document | Status |
|---|---|
| [plans/agent-tooling-build.md](plans/agent-tooling-build.md) | Implemented M0–M3; real-provider smoke pending; eval tooling removed 2026-11-14 |
| [plans/limits-removal-build.md](plans/limits-removal-build.md) | Implemented 2026-11-11 |
| [plans/model-discovery-build.md](plans/model-discovery-build.md) | Implemented 2026-11-11 |
| [plans/subagent-build.md](plans/subagent-build.md) | Proposed 2026-11-15; no code changed |
| [plans/thinking-effort-build.md](plans/thinking-effort-build.md) | Implemented 2026-11-11; real-provider smoke pending |
| [plans/transcript-folding-build.md](plans/transcript-folding-build.md) | Implemented 2026-11-14; real-provider fold smoke pending |
| [plans/tui-build.md](plans/tui-build.md) | Implemented 2026-11-10 |
| [plans/v0-build.md](plans/v0-build.md) | Historical; v0 complete |
| [plans/v1-build.md](plans/v1-build.md) | Implemented; real-provider streamed smoke pending |
| [plans/web-fetch-build.md](plans/web-fetch-build.md) | Implemented 2026-11-13; public-page and Crawl4AI smoke passed |
| [plans/web-search-build.md](plans/web-search-build.md) | Implemented 2026-11-13; manual SearXNG smoke passed |

### Research

| Document | Snapshot |
|---|---|
| [research/agent-harness-minimum.md](research/agent-harness-minimum.md) | 2026-11-09; minimum harness and primary sources |
| [research/agent-tooling-research.md](research/agent-tooling-research.md) | 2026-11-11; tool set, limits, usage, and evaluation methodology |
| [research/evaluation-research.md](research/evaluation-research.md) | 2026-11-15; private, harness-agnostic evaluation design, task sourcing, metrics, and experiment options |
| [research/subagent-research.md](research/subagent-research.md) | 2026-11-15; delegation and multi-agent features in Claude Code, Codex, OMP, and OpenCode, and which fit aharness |

## Link check

`make docs-check` verifies that relative links and image targets in repository
Markdown files resolve. Code fences are ignored so examples are not treated as
links. `make dod` runs this check as part of the full automated
definition-of-done set.
