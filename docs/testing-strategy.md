# aharness testing strategy

- **Date:** 2026-11-18
- **Status:** normative for the aharness SDK
- **Related:** [SDK architecture](architecture.md),
  [Go coding standard](coding-standard.md), [limits](limits.md)
- **Agent entry point:** [AGENTS.md](../AGENTS.md)

The SDK is tested at four layers: unit, integration, fuzz, and manual smoke.
Every automated layer is deterministic: no real network, no `time.Sleep`-based
waiting, no dependence on the host terminal, and no shared state outside
`t.TempDir` and `testing/synctest`. The first-party coding agent, acode, tests
its CLI/TUI surfaces separately against this SDK.

## 1. Unit tests

Unit tests live beside the package they exercise and use fakes or `t.TempDir`.
The package boundaries are the test seams; do not add production interfaces
only for tests.

### `agent`

- Runner lifecycle: new/resume/fork, run start and finish, turn counting,
  cumulative usage, stop reasons, interrupt/cancel, steering at turn
  boundaries, and exactly one paired result per tool call.
- Projection: canonical message order, system prompt, tool schemas, grant
  narrowing, custom-tool verification, context estimate, deterministic
  compaction, and retention of the initial task and recent turns.
- Display events: order for streamed, buffered, tool, retry, subagent, and
  interrupt paths; display failures and child serialization; no thinking text
  in durable events.
- History and state: replay of every v2 event, unpaired calls, unknown
  events/fields, torn/older logs, stop-status reconstruction, and cost folding.
- Prompt and agent definitions: versioned prompt inventory, environment block,
  definition parsing and precedence, overrides, and replayability of old
  prompt/toolset versions.
- Retry policy: 429/5xx/transport retries, `Retry-After`, streaming retries
  only before the first delta, context overflow, and repeated-failure hints and
  stops.
- Delegation: child grants, depth one, read-only concurrent fan-out,
  `ConcurrencySafe`, child budgets/accounting, display ordering, and
  parent/child session metadata.

### `engine`

- `Open` in new/resume/fork modes: workspace canonicalization, inheritance,
  prompt/tool-contract validation, fork overrides, `session_start` recording,
  history copy, and lock behavior.
- `Host`: per-run registry/spawner/runner construction, execution grants,
  todo seeding, work-directory reset, `State`, `Close`, and lock-free
  inspection/listing.

### `model`

- Canonical mapping and normalization for both Chat Completions and Responses:
  messages, tool calls, finish reasons, usage subsets, reasoning text, and
  typed errors.
- Buffered and SSE paths share one payload function; test both with fake HTTP
  servers. Cover malformed JSON, duplicate names, invalid UTF-8, oversized
  bodies/events, missing usage, truncated streams, and provider errors.
- Continuation: Chat reasoning replay, Responses raw reasoning items, bounded
  caches, and adapter isolation per session.
- Catalog: models.dev decode/normalize, eligibility, dialect mapping, cache
  versioning, TTL/conditional GET, stale fallback, refresh, URL policy, and
  warnings.
- User-Agent values are required, sent verbatim, and never defaulted; a missing
  value is a construction error or an `unavailable` tool result.

### `tools`

- Registry validation, grant enforcement, argument decoding, result shape, and
  panic pairing.
- Workspace policy: `os.Root`, `filepath.IsLocal`, `.git` rejection, symlink
  rejection for edits, lexical `ToolRequest.Path`, and outside-workspace
  errors.
- `read_file`, `edit_file`, `search`, and `run_command`: offsets/limits,
  binary/large files, exact-byte replacement, atomic writes, RE2/glob/case
  handling, timeout/process-group kill, output caps and spill files.
- `web_search`: URL validation, SearXNG JSON, dedupe, snippet sanitization,
  timeouts, and absent-configuration results.
- `web_fetch`: URL/address guard tables, redirects, content types, charset
  handling, extraction, paging, caps, and Crawl4AI provider mapping.
- `todo` and `delegate`: argument rules, restoration, report caps, hostile
  text marking, and child definitions.

### `session`

- Create/open/resume/fork, mode and version validation, `flock` behavior,
  torn-line repair, sequence validation, append/sync/close, and event
  canonicalization.
- Corrupt, truncated, v0, and unknown-schema logs fail closed.

### Root facade

- Construction validation, DTO copying, run/start behavior, event iteration
  and backpressure, result/error identity, custom tools, authorization, state,
  session inspection/listing, and model/catalog helpers.
- `testdata/external` builds a separate module against the public facade.

## 2. Integration tests

Integration tests use real stores, real registries, and `httptest` fake
providers. No test contacts models.dev or a real provider.

- New/resume/fork round trips: session log contents, prompt/tool inheritance,
  contract override, work-directory cleanup, and resume after an unpaired call.
- Fake Chat and Responses servers with streaming and buffered responses, tool
  calls, retries, provider errors, and usage accounting.
- Custom tools and authorization: denial results, session/agent identity,
  `ToolRequest.Path`, and resume schema matching.
- Delegation: parent and child logs, grants, concurrent read-only fan-out,
  child steering, budgets, and folded usage/cost.
- Web tools: fake SearXNG and Crawl4AI endpoints; search and fetch result
  pairing; no network beyond `httptest`.
- Catalog: fake models.dev server with ETag/TTL/refresh/stale behavior.
- Cancellation: context deadlines and `Interrupt`/`Cancel` kill the whole
  process group and leave a valid terminal log.

## 3. Fuzzing

Fuzz targets cover the trust boundaries. Run each target for a bounded time in
CI or before a risky change; add seeds for every regression.

- `model`: SSE decoder, Chat stream, Responses stream, catalog decoder.
- `session`: v2 JSONL reader and torn-line repair.
- `agent`: history reconstruction and agent-definition parsing.
- `tools`: HTML extraction and URL/address policy.

Fuzz targets must not panic, must keep output valid UTF-8, and must respect the
documented caps.

## 4. Manual smoke

Automated tests use fakes. Before a release that changes the loop, adapters,
tools, or session schema, run the relevant smoke against the hosted
`opencode-go` endpoint and a small temporary repository:

- New session, multi-turn tool use, interrupt/continue, resume, fork.
- Both dialects and a reasoning-capable model.
- `web_search` against a local SearXNG and `web_fetch` against a public page and
  a configured Crawl4AI endpoint.
- Delegation with a read-only child and a sequential child.
- Catalog list/refresh and cache behavior with and without network.

Record the exact pending checks when credentials or services are unavailable.
Do not describe an unrun smoke as verified. TUI, CLI, `-ui-json`, terminal
safety, and image smokes belong to acode's testing strategy.
