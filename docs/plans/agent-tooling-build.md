# aharness agent tooling and measurement build plan

- **Date:** 2026-11-11
- **Status:** implemented for M0–M3. The mock-provider, fixture, fuzz, and
  automated gate checks pass. Two measurement items remain environment-blocked:
  the real-provider thinking-tool/cache smoke and the 30-task baseline/A-B run
  both require `AHARNESS_PROVIDER_TOKEN`, which is unset here. The plan records
  those as pending rather than fabricating results; see §12.
- **Source:** [agent tooling, limits, and measurement research](../research/agent-tooling-research.md).
- **Target:** Go 1.27.3, module `aharness`.
- **Architecture:** [architecture.md](../architecture.md) §2 (canonical model and
  usage), §4 (agent loop), §5 (tools), §9 (CLI/config), §10 (defaults).
- **Limits:** [limits.md](../limits.md) §1, §4, and §5.
- **Testing:** [testing-strategy.md](../testing-strategy.md) plus §6 here.
- **Definition of done:** [project definition of done](../definition-of-done.md);
  feature criteria in §1.4.

## Decision summary

The plan follows the research recommendation: measure first, then repair the
read interface, then build the A/B instrument, then polish search/command.

- **M0 — measurement foundation.** Add cached-input and reasoning-output
  detail to `model.Usage`, a `reported` flag, catalog price decoding and
  per-response cost, a `-require-usage` mode for experiments, and a
  multi-round thinking-tool smoke for `deepseek-v4.1-flash`.
- **M1 — read contract.** Make `read_file` return contiguous lines with the
  actual delivered range and a `continue with offset=N` footer. Raise the
  default to 2,000 lines, the maximum to 4,000, and the file-result cap to
  60 KiB. Keep head+tail truncation for command output only. Bump the toolset
  version and retain frozen v0 schemas so existing sessions still replay.
- **M2 — evaluation.** Add a dev-only `cmd/aharness-eval` command, a 30-task
  internal suite, tier-aware cost from a price file, and paired
  repeated A/B runs. Expand to ~150 tasks only for a solve-rate decision.
- **M3 — search and command.** Add skipped-file counts and real `**` glob
  matching to `search`; spill oversized command output to a per-run file and
  return the path with inline head/tail.
- **No new model-facing tools.** The four-tool surface stays. `todo`,
  subagents, LSP, and web access remain out of scope.
- **No schema version bump.** Usage, cost, and pricing fields are additive in
  the v2 event payload; old logs decode with `reported=false` and no cost.
- **Cost is observational.** The catalog supplies a price snapshot; the eval
  driver owns peak/off-peak and price-table overrides. Runtime cost is an
  estimate, never the authority.

## 1. Scope

### 1.1 What this plan owns

| Area | Implementation |
|---|---|
| Usage | `cached_input_tokens`, `reasoning_output_tokens`, and `reported` in `model.Usage`; adapter mapping for Chat and Responses, buffered and streamed. |
| Missing usage | `-require-usage`: a completed response with no provider usage is a `protocol_error` before any tool executes. Default off for local OpenAI-compatible endpoints. |
| Cost | Decode models.dev `cost` into the catalog; record a pricing snapshot in `run_start`; record per-response `cost_micro_usd`; aggregate in `run_end`; show in TUI details. |
| Read | Contiguous line window; actual delivered range; continuation footer; head-only truncation; 2,000-line default; 4,000-line maximum; 60 KiB result cap. |
| Toolset versioning | `toolset-v1` for new sessions; frozen `toolset-v0` schemas retained for replay; `Projection` carries the schemas selected by the recorded version. |
| Evaluation | `cmd/aharness-eval`, task manifests, setup/verify hooks, session-JSONL metrics, price file, paired bootstrap, interleaved arms. |
| Search | 60 KiB result cap; skipped-file counts; `**` glob semantics; count-cap and byte-truncation reported separately. |
| Command | 64 KiB inline cap retained; full output spilled to a per-run file with the path in the result. |
| Documentation | README, architecture, limits, testing strategy, TUI doc, and this plan. |

### 1.2 What stays the same

- The four tools, their names, argument shapes, result codes, workspace
  containment, `.git` protection, symlink policy, command environment
  allowlist, and process-group cancellation.
- The canonical message/request/response shape, finish reasons, streaming
  assembly, projection/compaction, retry policy, stuck detection, and stop
  statuses.
- One provider (`opencode-go`), the two OpenAI dialects, model discovery, and
  thinking effort.
- The 2 MiB file-edit and file-read limits; the 64 KiB command cap; the 1 MiB
  provider response cap; the 4 MiB session-line cap.
- The v2 session schema and `toolset-v0` logs.
- Zero runtime dependencies. The eval command uses only the standard library
  and `internal/session`.

### 1.3 Not in this plan

- Adding `write`, `glob`, `todo`, subagent, LSP, or web tools.
- Changing the system prompt or `PromptVersion`.
- Raising the 2 MiB file cap, streaming file reads, or model-relative read
  caps. These are follow-ups after the M2 measurements.
- Exact tokenizer accounting or per-tool token attribution. Provider usage is
  per model call; the eval driver attributes call output to requested tools.
- A general benchmarking platform, dashboard, database, or network service.
- Provider time-of-day pricing in the runtime. The eval driver applies peak
  multipliers from a versioned price file.
- Reasoning-text display, reasoning summaries, or a canonical reasoning
  message shape. The thinking-tool smoke decides whether a bounded
  adapter-owned continuation is required.
- Changing the 64 KiB tool-result event field cap or the 4 MiB session line.
- Changing the output budget, run budgets, compaction constants, or safety
  margin.

### 1.4 Definition of done

In addition to the [project definition of done](../definition-of-done.md), all
items below must hold. Checked items are exercised by fixture, integration, or
gate tests; see §12 for the two environment-blocked measurement items.

- [x] `model.Usage` carries cached-input, cache-write-input, and
      reasoning-output subsets plus a `reported` flag; Chat and Responses map
      them on buffered and streaming paths; `Total()` remains input+output.
- [x] `-require-usage` turns absent provider usage into a `protocol_error`
      before any tool executes; default behavior still accepts missing usage
      for local endpoints and records `reported=false`.
- [x] `run_start` records the catalog pricing snapshot; `model_response`
      records a per-call cost when prices are available; `run_end` aggregates
      run and cumulative cost; old logs decode without cost.
- [ ] A manual multi-round tool smoke in thinking mode (`-thinking-effort
      high`) against `deepseek-v4.1-flash` passes, or the failure is recorded
      and the reasoning-continuation contingency is opened. **Not run:**
      `AHARNESS_PROVIDER_TOKEN` is unset in this environment. The adapter-owned
      Chat and Responses continuations are implemented and fixture-tested
      (§12); live gateway acceptance remains unverified.
- [x] `read_file` never returns head+tail file content; the header describes
      the delivered range; a non-final page ends with
      `continue with offset=<next>`; a truncated line is marked.
- [x] `read_file` defaults to 2,000 lines, caps at 4,000, and caps the result
      at 60 KiB; a 1,175-line/42 KB Go file is one call at default.
- [x] `toolset-v1` is recorded for new sessions; a seeded `toolset-v0` session
      resumes using the frozen v0 schemas; new sessions use v1.
- [x] `cmd/aharness-eval` runs a task manifest against two harness binaries,
      interleaves arms, repeats k times, and writes per-run JSONL plus a
      summary with paired bootstrap confidence intervals.
- [ ] The 30-task suite has executable setup/verify scripts, at least three
      clusters, and a recorded baseline; the M1 A/B reports read calls per
      file, tokens per solved task, turns, and solve rate. **Suite and driver
      complete:** 30 tasks across `go-fix`, `text-transform`, and
      `shell-pipeline`; every setup succeeds, no verifier is trivially
      satisfied, and `eval/baseline.json` records `status=unmeasured` because
      provider credentials are unavailable. The A/B itself is pending.
- [x] `search` reports skipped files, supports `**`, and distinguishes a
      result-count cap from byte truncation.
- [x] `run_command` spills output over 64 KiB to a per-run file, returns the
      path, and removes it with the per-run work directory.
- [x] README, architecture, limits, testing strategy, TUI doc, and this plan
      are updated; the coding-standard §8 gates pass (`make dod`: goimports,
      go vet, golangci-lint, `go test -race -count=1`, go build, govulncheck,
      docs-check).

## 2. Current state

This section records the pre-implementation baseline that motivated the plan;
§12 records the shipped behavior and deviations.

### 2.1 Usage and cost

- `model.Usage` has only `InputTokens` and `OutputTokens`
  (`internal/model/types.go:68-75`); `Total()` is their sum.
- Chat buffered maps `prompt_tokens`/`completion_tokens`; Chat streaming
  requires usage. Responses uses an optional pointer and silently leaves zero
  when absent.
- `model_response` records usage, latency, attempt, and streamed
  (`internal/agent/events.go:75-86`); `run_end` records run and cumulative
  usage (`:142-150`).
- The projection estimate is `ceil(bytes/4) + 8/message + 16/tool schema`
  (`internal/agent/context.go:22-24,190-205`). It is used for compaction and
  the input cap, not for reported totals.
- The catalog has `cost` data in the live models.dev payload (for example
  `input: 0.15`, `output: 0.6`, `cache_read: 0.003` for the selected model),
  but `CatalogModel` does not decode it.

### 2.2 Read and search

- `defaultReadLimit = 100`, `maxReadLimit = 500`, `maxReadFileBytes = 2 MiB`
  (`internal/tools/read.go:15-17`).
- `fileOutputCap = 32 KiB`, `commandOutputCap = 64 KiB`
  (`internal/tools/output.go:11-13`).
- `read_file` renders the requested window, then `capOutput` keeps head and
  tail. The header still describes the requested window.
- `search` defaults to 100 results, caps at 200, caps output at 32 KiB, skips
  files over 1 MiB, and uses `path.Match` (no `**`).
- `Project` rejects a session whose recorded toolset version differs from
  `tools.Version` (`internal/agent/context.go:51-56`).

### 2.3 Evaluation

There is no eval runner. `docs/testing-strategy.md` states that external
benchmarking and eval infrastructure are out of scope. The session JSONL
already records enough for most harness metrics; the missing pieces are usage
detail, cost, and an aggregation/verification driver.

## 3. Contract

### 3.1 Usage

Additive canonical fields:

```go
type Usage struct {
	InputTokens           int  `json:"input_tokens"`
	OutputTokens          int  `json:"output_tokens"`
	CachedInputTokens     int  `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens int  `json:"cache_write_input_tokens,omitempty"`
	ReasoningOutputTokens int  `json:"reasoning_output_tokens,omitempty"`
	Reported              bool `json:"reported"`
}
```

- `InputTokens` remains the total prompt/input count. `CachedInputTokens` and
  `CacheWriteInputTokens` are disjoint subsets of it.
- `OutputTokens` remains the total completion count, including reasoning.
  `ReasoningOutputTokens` is a subset.
- `Total()` remains `InputTokens + OutputTokens`. Do not add the subsets.
- `Reported` is true when the provider supplied a usage object. A present
  all-zero object is reported; an absent object is not.
- The adapters validate
  `0 <= CachedInputTokens + CacheWriteInputTokens <= InputTokens` and
  `0 <= ReasoningOutputTokens <= OutputTokens`; a violation is a
  `protocol_error`.
- Old v2 events decode with `Reported=false` and zero subsets. Resume keeps
  summing `InputTokens + OutputTokens`.

Mapping:

| Wire field | Canonical |
|---|---|
| Chat `prompt_tokens` | `InputTokens` |
| Chat `prompt_tokens_details.cached_tokens` | `CachedInputTokens` (preferred) |
| Chat `prompt_tokens_details.cache_write_tokens` | `CacheWriteInputTokens` |
| Chat `prompt_cache_hit_tokens` | fallback for `CachedInputTokens` |
| Chat `completion_tokens` | `OutputTokens` |
| Chat `completion_tokens_details.reasoning_tokens` | `ReasoningOutputTokens` |
| Responses `input_tokens` | `InputTokens` |
| Responses `input_tokens_details.cached_tokens` | `CachedInputTokens` |
| Responses `input_tokens_details.cache_write_tokens` | `CacheWriteInputTokens` |
| Responses `output_tokens` | `OutputTokens` |
| Responses `output_tokens_details.reasoning_tokens` | `ReasoningOutputTokens` |

Chat buffered usage becomes a pointer so absence is distinguishable from a
present zero object. `consumeChatStream` already uses a pointer. Responses
already uses a pointer. Non-SSE fallback uses the buffered mapping.

### 3.2 Missing usage and `-require-usage`

- New flag `-require-usage` (default `false`). No environment variable.
- `false`: preserve today's behavior. A completed buffered response with no
  usage records `Reported=false` and zeros; local OpenAI-compatible endpoints
  keep working.
- `true`: after recording the `model_response` with `Reported=false`, stop
  with `protocol_error` before executing any tool call. The model response is
  durable, so the transcript still pairs correctly.
- Streaming already fails on missing usage; it records `Reported=true` on
  success. An empty `{}` is reported and accepted as zeros.
- Record `require_usage` in `run_start`. It is a per-run flag and is not
  inherited on resume/fork; an omitted flag means false.

### 3.3 Cost

Runtime cost is an estimate from catalog prices and is observational. It is
not a billing authority.

- Decode models.dev `cost` into `CatalogModel`:
  `input`, `output`, `cache_read`, `cache_write` in USD per 1M tokens. Store
  integer micro-USD per 1M tokens (`$0.15` → `150000`) to avoid floating
  arithmetic in the loop. Reject negative or non-finite values.
- Bump the normalized catalog cache version. An older cache is treated as
  absent and refetched.
- `run_start` records a `pricing` object for the effective model: currency
  (`USD`), unit (`per_million_tokens`), the four rates, source URL, and
  `fetched_at`. If the catalog has no cost, `pricing.available=false` and cost
  fields are omitted.
- `model_response.cost_micro_usd` is computed when pricing is available:

```text
uncached = max(0, InputTokens - CachedInputTokens - CacheWriteInputTokens)
cost_micro_usd = round(
    uncached * input_rate_micro
  + CachedInputTokens * cache_read_rate_micro
  + CacheWriteInputTokens * cache_write_rate_micro
  + OutputTokens * output_rate_micro
) / 1_000_000
```

  `cache_read` falls back to `input` when absent. Reasoning is already inside
  `OutputTokens`; do not charge it twice.
- `run_end` records `run_cost_micro_usd`, `cost_micro_usd`,
  `run_cost_complete` (true only when every model response in the run had a
  price), and `cost_complete` (true only when every response in the cumulative
  session had a price).
- The recorded catalog price is assumed to be the provider's base/off-peak
  rate. The runtime does not model provider time-of-day tiers. The eval driver
  applies peak multipliers and emits its own cost; tier-aware claims use the
  driver, not `run_end.cost`.
- TUI details may show cost when present. `-ui-json` may add optional
  `cost_micro_usd`, `cached_input_tokens`, `reasoning_output_tokens`, and
  `reported` to existing usage objects; no new event type and no protocol
  version bump.

### 3.4 Read contract

`read_file` returns one contiguous window.

- Header: `lines A-B of Z`, where A and B are the lines actually delivered.
- If `B < Z`, append `continue with offset=B+1`.
- If the byte cap stops the window before the requested `limit`, deliver only
  whole lines that fit, set `Truncated=true`, and append
  `continue with offset=<next line>`.
- If a single line cannot fit in the remaining budget, deliver a prefix of
  that line, append `[line N truncated at X bytes]` and
  `use search or run_command to inspect the rest of line N`, set
  `Truncated=true`, and do not emit a next-line offset for the partial line.
- Never use head+tail for file content. `capOutput` remains available for
  command output.
- Normal paging (`B < Z` because of `limit`, not bytes) does not set
  `Truncated`; the header and footer carry the continuation.
- `search` uses a head-only result window. It sets `Truncated` only when bytes
  were dropped; a result-count cap remains an explicit
  `[results capped at N]` marker and does not set `Truncated`.

Limits:

| Limit | New value | Notes |
|---|---:|---|
| `read_file` default lines | 2,000 | One call for most source files |
| `read_file` max lines | 4,000 | Larger values clamp |
| `read_file` result cap | 60 KiB | Keeps the rendered result plus marker under the 64 KiB tool-result event cap |
| `read_file` max file | 2 MiB | Unchanged |
| `search` result cap | 60 KiB | Same event-cap reason |
| `search` result count | 100 default / 200 max | Unchanged |
| `run_command` inline cap | 64 KiB | Unchanged; spill in M3 |

Tool descriptions in `internal/tools/schemas.go` must state the new defaults,
caps, and the continuation footer.

### 3.5 Toolset versioning

Changing schemas/descriptions is a toolset change. Do not make old sessions
unresumable.

- Keep the current schema set frozen as `toolset-v0`.
- Add `toolset-v1` with the new read/search descriptions and limits.
- `tools.Version = "toolset-v1"` for new sessions.
- Add `tools.SchemasFor(version string) ([]model.ToolSchema, error)`.
  `toolset-v0` returns the frozen set; `toolset-v1` returns the current set;
  unknown versions return an error.
- `agent.Projection` gains `Schemas []model.ToolSchema`. `Project` selects
  schemas from `session_start.toolset_version` and uses the same set for the
  token estimate.
- The runner sends `proj.Schemas`, not `r.tools.Schemas()`, so a resumed v0
  session replays with v0 descriptions and a new session sends v1.
- A v0 session continues to record v0 in its `session_start`; the new run's
  `run_start` keeps the recorded version. No migration.
- `PromptVersion` is unchanged; the system prompt is unchanged.

### 3.6 Search and command (M3)

Search:

- Add `**` support: `**` matches zero or more path segments; `*` does not
  cross `/`. `path.Match` alone is insufficient.
- Count skipped files by reason (`too_large`, `binary`, `symlink`) and append
  one summary line, for example
  `[skipped 12 files: 9 over 1 MiB, 2 binary, 1 symlink]`.
- Keep the 60 KiB result cap and the count markers. The two limits are
  reported separately.

Command:

- Keep 64 KiB inline.
- When combined stdout+stderr exceeds the cap, write the full combined output
  to `<state>/work/<session-id>/tool-output/<call-id>.log` (file 0600,
  directory 0700), and append
  `full output: <absolute path>` to the inline head/tail.
- The model can page the file with `run_command` (`sed`, `grep`, `tail`);
  `read_file` cannot reach it because it is outside the workspace.
- The spill file is per-run. The per-run work directory cleanup removes it.
  Record `spilled=true` and the path in `tool_result` for diagnostics; on
  resume after the work directory is recreated, the path is historical and
  the content is gone. Document this limitation.
- Reject a spill path that escapes the state work directory, and never follow
  symlinks when creating the file.

### 3.7 Eval command

`cmd/aharness-eval` is dev tooling, not shipped runtime. It may contain
multiple files in its own package and imports only the standard library plus
`internal/session`.

Invocation:

```sh
aharness-eval \
  -tasks eval/tasks \
  -arm before=./bin/aharness-before \
  -arm after=./bin/aharness-after \
  -model deepseek-v4.1-flash \
  -thinking-effort high \
  -repeats 3 \
  -prices eval/prices.json \
  -seed 1 \
  -out eval/results/read-limits
```

Task layout:

```text
eval/tasks/<id>/
  task.md       # prompt passed to the harness
  setup.sh      # creates/initializes $WORKSPACE; exit nonzero = invalid task
  verify.sh     # exits 0 when the task is solved
  meta.json     # cluster, language, timeout, notes
```

Driver behavior:

- Create a fresh temp workspace per run; run `setup.sh` with `WORKSPACE`,
  `TASK_DIR`, and `TASK_ID`.
- Run the selected arm with
  `-batch -workspace <tmp> -state-dir <tmp-state> -task-file task.md -model
  <model> -thinking-effort <effort> -require-usage`.
- Read the single session JSONL from the temp state directory; run
  `verify.sh`; record the exit status, stdout/stderr tail, and metrics.
- Interleave arms per task and repeat; record the seed and arm order.
- Emit `results.jsonl` (one record per run) and `summary.json`/`summary.md`.
- Metrics: pass/fail, turns, tool calls, read calls per path, repeated reads,
  truncation/continuation events, input/cached/output/reasoning tokens, cost,
  retries, tool errors, wall time. Compare `model_request.estimated_input_tokens`
  with the next response's `InputTokens` to quantify estimator error.
- Statistics: paired bootstrap by task with a fixed seed (10,000 resamples,
  95% percentile CI); cluster by `meta.json` cluster for the standard error.
  Report n, k, mean difference, CI, and cost delta.
- Price file: currency, unit, per-model input/output/cache-read/cache-write
  rates, and optional peak windows/rates. The driver chooses the tier by the
  request timestamp in UTC. The catalog snapshot remains the fallback.

Task suite:

- Start with 30 tasks across at least three clusters; each needs setup and an
  executable verifier. Target 30–70% baseline solve on at least 20 tasks.
- Expand to ~150 tasks for a solve-rate decision; a 30-task suite is a smoke
  and call-count/token instrument, not a 5 pp detector.
- Keep a held-out set and refresh tasks when contamination or overfitting is
  suspected.

## 4. Package changes and wiring

| File / area | Change |
|---|---|
| `internal/model/types.go` | Usage fields; helper for uncached input. |
| `internal/model/openai_chat.go` | Usage detail structs; buffered pointer; mapping and validation. |
| `internal/model/openai_responses.go` | Usage detail structs; mapping and validation. |
| `internal/model/catalog.go`, `catalog_source.go` | Cost decode, integer micro-USD conversion, cache version bump. |
| `internal/agent/events.go` | Usage/pricing/cost fields in `model_response`, `run_start`, `run_end`; `require_usage`. |
| `internal/agent/runner.go` | `-require-usage` check; cost computation; use `proj.Schemas`; accumulate before emitting display events. |
| `internal/agent/context.go` | `Projection.Schemas`; schema selection by recorded toolset version. |
| `internal/agent/state.go` | Decode new fields; old-log defaults. |
| `internal/tools/read.go` | Contiguous, line-aware window; actual range; continuation footer. |
| `internal/tools/output.go` | Head-only helper for read/search; keep head+tail for command. |
| `internal/tools/search.go` | 60 KiB cap; skipped counts; `**`; split truncation semantics. |
| `internal/tools/command.go` | Spill oversized output; path in result; cleanup. |
| `internal/tools/schemas.go`, `result.go` | v1 descriptions; frozen v0 set; `SchemasFor`; `toolset-v1`. |
| `cmd/aharness/config.go` | `-require-usage`; pricing resolution for the effective model. |
| `cmd/aharness/run.go`, `tui.go` | Pass config/pricing; record run fields. |
| `internal/tui/protocol.go`, `view.go`, `widgets.go` | Optional usage detail/cost display; no protocol bump. |
| `cmd/aharness-eval/` | New dev command; manifest, process runner, JSONL metrics, bootstrap, price table. |
| `eval/` | Task fixtures, price file, result output directory. |

No new `internal/` package. The eval command is a separate `cmd` binary and a
dev-only top-level `eval/` tree; it is not part of the runtime package graph.

## 5. Milestones

### M0 — Measurement foundation

- Add usage subsets and `reported`.
- Add `-require-usage`.
- Decode catalog cost, record the pricing snapshot, compute per-response and
  run cost.
- Update TUI details/`-ui-json` usage fields.
- Add a manual multi-round thinking-tool smoke and record the result.

Acceptance:

- Fixture tests cover Chat/Responses, buffered/streamed, present/absent/zero
  detail objects, cached/cache-write/reasoning subsets, and invalid subset
  values.
- `-require-usage` fails before tool execution on a missing-usage response and
  leaves the default behavior unchanged.
- Cost is correct for known usage/prices, uses cached rates for cached tokens,
  excludes reasoning from double-charging, and reports
  `run_cost_complete=false` / `cost_complete=false` when any response is
  unpriced.
- Old v2 logs decode and resume; `reported=false` is visible.
- The thinking-tool smoke either passes or produces a recorded failure with
  the exact request/response and a follow-up decision.

### M1 — Read contract and limits

- Implement contiguous line-aware output with actual range and footer.
- Change the read defaults/caps; update schemas and tests.
- Add `toolset-v0`/`toolset-v1` schema selection and use the recorded version
  in projection and requests.
- Keep search at its current cap until M3, except for tests that share the
  output helper.

Acceptance:

- A 1,175-line/42 KB file reads in one default call.
- A 500-line/80 B-per-line page never drops middle lines; the header matches
  the delivered lines and the footer gives the next offset.
- A single over-budget line is marked and does not emit a misleading
  continuation.
- `Truncated` is true only for byte loss, not normal paging.
- New sessions record `toolset-v1`; a seeded `toolset-v0` session resumes and
  sends the frozen v0 schema descriptions; an unknown version errors.
- `go test -race` covers the new boundaries; no index panic on empty files,
  trailing newlines, CRLF, or multibyte lines.

### M2 — Eval command and A/B

- Add `cmd/aharness-eval`, the task format, the 30-task suite, the price file,
  metrics, and bootstrap.
- Run the M1 change as the first A/B: before/after binaries, 3 repeats,
  interleaved.

Acceptance:

- The driver runs with a fake harness script in tests and with the real binary
  in a local smoke.
- A seeded task with a passing verifier reports solved; a failing verifier
  reports unsolved; an invalid setup is excluded, not counted as unsolved.
- Metrics match a hand-computed fixture session.
- The A/B summary reports read calls per file, tokens per solved task, turns,
  solve rate, cost, and paired 95% CIs.
- The 30-task suite baseline is recorded; tasks are held out from prompt/tool
  tuning.

### M3 — Search and command

- Search: 60 KiB cap, skipped counts, `**`, separate cap/byte reporting.
- Command: spill oversized output, path in the result, cleanup.
- Re-run the 30-task smoke to confirm no regression.

Acceptance:

- A search that skips a 2 MiB file reports the skip.
- `**/*.go` matches nested files; `*` does not cross directories.
- A count-capped search does not set `Truncated`; a byte-truncated search
  does.
- A command producing 1 MiB returns 64 KiB inline plus a readable path; the
  path is under the session work directory; the file is removed at run end.
- A path escape attempt is rejected.

## 6. Testing

### Unit

- Usage mapping for both dialects and both transport paths; absent, empty, and
  populated details; DeepSeek cache fields; reasoning tokens; invalid subset
  bounds.
- Cost arithmetic, cache-rate fallback, micro-USD rounding, unpriced models,
  `cost_complete`.
- `-require-usage` acceptance/rejection and old-log decoding.
- Read windowing: exact boundaries, limit/max clamp, byte cap, long line,
  CRLF, trailing newline, empty file, multibyte UTF-8, actual header, footer.
- Toolset selection: v0 frozen schemas, v1 current schemas, unknown version,
  projection estimate with the selected set.
- Search: skipped counts, `**`, cap/byte distinction.
- Command: spill threshold, path containment, cleanup, absolute path output.
- Eval driver: manifest parsing, setup failure, verifier failure, metrics
  extraction, interleaving, bootstrap determinism.

### Integration

- Fake provider with scripted usage variants and tool rounds; confirm
  `model_response` fields and `run_end` reconciliation.
- Streaming and buffered paths produce the same canonical usage for the same
  logical response.
- Resume a v0 session with v1 binary; confirm frozen schemas and no toolset
  error.
- Eval driver with a fake harness and a temp task tree.
- Context projection with 60 KiB reads: compaction and input-limit behavior
  remain deterministic.

### Fuzz

Update the existing stream fuzz seeds with usage detail objects and malformed
subsets. No new network or sleeps.

### Manual smoke

- Real-provider run with `deepseek-v4.1-flash`, `-thinking-effort high`, a task
  that requires at least two tool rounds; inspect the session for usage,
  cache fields, cost, and completion.
- A two-turn run to confirm `prompt_cache_hit_tokens > 0` on the second call
  when the prefix is stable.
- A 1,175-line file read in one call; a long-line file with a continuation
  marker.
- A command that exceeds 64 KiB; page the spilled file with `run_command`.
- A search over a tree containing a large skipped file; confirm the skip line.

### Gates

Run the coding-standard §8 gates: `goimports`, `go vet`,
`golangci-lint`, `go test -race -count=1`, `go build`, `govulncheck`.

## 7. Limits updates

Update [limits.md](../limits.md):

| Limit | New behavior |
|---|---|
| `read_file` default lines | 2,000 |
| `read_file` max lines | 4,000 |
| `read_file` result cap | 60 KiB, contiguous; continuation footer |
| `read_file` max file | 2 MiB (unchanged) |
| `search` result cap | 60 KiB; head-only |
| `search` skipped files | reported by reason |
| `run_command` output | 64 KiB inline plus per-run spill path |
| Toolset version | New sessions `toolset-v1`; `toolset-v0` replay supported |
| Usage fields | Input/output totals; cached/cache-write/reasoning subsets; `reported` |
| Cost | Catalog-priced estimate; eval tier-aware |
| `-require-usage` | Opt-in; default off |

No other cap changes: the 2 MiB file/edit cap, 1 MiB provider response, 64 KiB
tool-result field, 4 MiB session line, and 16 MiB catalog body stay.

## 8. Risks and assumptions

- **Larger reads cost more.** A 60 KiB result is ~15k tokens replayed on later
  turns. The A/B must show that fewer calls and turns compensate. If not,
  fall back to 32 KiB or use a model-relative cap.
- **Cache invalidation.** Changing tool schemas changes the prompt prefix and
  can reduce cache hits for existing sessions. Cost comparisons must report
  cache-hit rate and uncached-equivalent cost.
- **Toolset migration.** Frozen v0 schemas add a small maintenance surface.
  Without them, bumping the version makes every existing session unresumable.
  This plan chooses replay compatibility.
- **Cost accuracy.** Catalog prices may be stale, tier-unaware, or absent for
  some models. Runtime cost is observational; the eval price file is the
  authority for experiments. Record the price source and timestamp.
- **Missing usage.** Some local OpenAI-compatible endpoints omit usage. The
  default remains permissive; eval runs use `-require-usage`. Never average a
  silent zero into an experiment.
- **Reasoning continuation.** DeepSeek-compatible thinking endpoints require
  the `reasoning_content` of earlier assistant messages to be echoed when the
  request carries tools, and Responses reasoning models require their reasoning
  items to be replayed in a stateless tool round. Both adapters now retain a
  bounded in-memory copy per response and replay it with the matching message;
  the copy is never logged and dies with the adapter. The live `opencode-go`
  smoke still needs to confirm each gateway accepts the echo; the design does
  not leak raw provider shapes into the loop.
- **Eval overfitting.** A small suite can be tuned accidentally. Hold out
  tasks, vary seeds, and refresh. Terminal-Bench is an anchor, not a substitute
  for the internal suite.
- **Eval maintenance.** The dev command and task fixtures are code. Keep them
  small, deterministic, and out of the runtime dependency graph.
- **Command spill.** Per-run files disappear on resume; the model may see a
  stale path in the transcript. Document it. Path containment and cleanup are
  security-sensitive.
- **Search glob semantics.** Adding `**` changes matching behavior; tests must
  pin `*` versus `**`, hidden directories, and `.git` exclusion.
- **Event cap interaction.** A 60 KiB read result plus header/marker must stay
  within the 64 KiB tool-result field cap; do not raise both independently.

## 9. Decisions and alternatives

| Decision | Choice | Why |
|---|---|---|
| Read default | 2,000 lines, max 4,000, 60 KiB | One call for most files; 60 KiB ≈ 15k tokens; stays under the event cap |
| Head+tail for files | Removed; head-only for read/search | File content must be contiguous; head+tail hides middle lines |
| Continuation | Explicit `offset=N` footer | Actionable truncation; avoids blind retries |
| Read cap sizing | Static first, model-relative later | Simpler; matches Claude Code’s fixed 25k-token precedent; measure before adding model-dependent output |
| Toolset change | Versioned schemas | Preserves resume/fork; avoids a breaking upgrade |
| Missing usage | `reported` flag plus opt-in strict mode | Keeps local endpoints working; eval fails closed |
| Cost ownership | Runtime estimate; eval tier-aware | Avoids provider pricing logic in the loop; keeps experiments reproducible |
| Cost representation | Integer micro-USD | Avoids floating drift; exact enough for per-call sums |
| Cache detail | Cached input subset + rate | Cache hits dominate cost; needed for both tokens and dollars |
| Reasoning detail | Reasoning subset, no separate charge | Already included in output tokens |
| Eval location | `cmd/aharness-eval` + top-level `eval/` | Dev-only; no new runtime package; no dependency |
| Benchmarks | Internal suite primary; Terminal-Bench anchor | Verified, low-contamination, harness-sensitive |
| New tools | None | Evidence supports the four-tool surface |

## 10. Documentation updates

When implemented, update:

- `README.md`: describe the user-facing behavior; the plan itself is indexed
  in `docs/README.md`, not the README documentation table.
- `docs/architecture.md`: canonical usage fields, cost/pricing, read
  continuation, toolset schema selection, `-require-usage`, eval boundary.
- `docs/limits.md`: the table in §7; note the event-cap interaction.
- `docs/testing-strategy.md`: usage fixtures, read boundaries, toolset replay,
  eval command, manual smoke.
- `docs/tui.md`: usage detail/cost display if added; no protocol bump.
- `docs/plans/model-discovery-build.md`: catalog cache version bump for cost.
- `docs/plans/agent-tooling-build.md`: status and implementation deviations.
- `docs/research/agent-tooling-research.md`: update only if evidence changes; it is a
  dated research record.

## 11. Open questions and prerequisites

- Provider credentials are required for the real thinking-tool smoke and the
  cache-hit check. Without them, M0 cannot claim portability for
  `deepseek-v4.1-flash`.
- Confirm that models.dev exposes `cost` for every usable model; if not, the
  runtime omits cost and the eval price file must cover the experiment models.
- Confirm the `opencode-go` gateway’s reasoning-continuation behavior before
  designing a canonical or adapter-owned continuation. The adapter-owned Chat
  continuation is implemented from the DeepSeek contract; the live gateway
  check remains pending.
- Decide whether the internal task fixtures can use this repository’s history
  or need small vendored repos. Licensing and size must be settled before the
  30-task suite is authored.
- Decide the eval result retention policy; results contain prompts, outputs,
  and cost data.
- Revisit the read cap after the first A/B. If 60 KiB still causes extra
  calls, test 100 KiB; if token cost rises, test 48 KiB or a model-relative
  cap.

## 12. Implementation notes and deviations

The implementation follows §1–§10 with these recorded choices:

- **Eval tooling removed (2026-11-14).** `cmd/aharness-eval` and the top-level
  `eval/` tree were deleted. They were dev-only, sat outside the runtime
  package graph, and never produced a measured baseline (`eval/baseline.json`
  was `unmeasured`), so no shipped behavior, metric, or claim depended on them.
  This supersedes the eval items in §1.4, §2.3, §3.7, §5 M2, §11, and the eval
  baseline note below. Evaluation is deferred; when it returns it should start
  from a fresh design rather than this driver.
- **Pricing on resume/fork.** When no catalog load is needed (model unchanged),
  the run inherits the latest recorded `run_start.pricing` snapshot instead of
  forcing a network fetch. An explicit model change refreshes the snapshot from
  the catalog; a catalog without cost leaves `pricing.available=false`.
- **Invalid catalog cost.** Negative, non-finite, or incomplete catalog rates
  make that model's pricing unavailable (`Cost=nil`) instead of making the model
  unusable. Cost is observational; eligibility is unchanged.
- **Read byte cap.** A page first keeps the largest run of whole lines that fits
  with the continuation footer. A partial line prefix is emitted when the first
  requested line cannot fit at all. In both cases `Truncated` means bytes were
  dropped and normal `limit` paging does not set it.
- **Command spill names.** Spill files use random `cmd-<hex>.log` names under
  `<state>/work/<session-id>/tool-output/` rather than the tool call ID. The
  observable contract is the absolute path in `details` and the inline
  `full output:` line; the directory is per-run and removed with the work tree.
- **`-require-usage` timing.** The response is recorded and displayed, and the
  turn/usage counters advance, before the runner stops with `protocol_error`.
  No tool executes. The default remains permissive.
- **Reasoning continuation.** The Chat adapter retains up to 4 MiB of thinking
  per response and 16 MiB per adapter, keyed by the canonical assistant message
  (content plus tool-call IDs), and sends it as `reasoning_content` when the
  runner replays that message in a tool round. The Responses adapter uses the
  same key and bounds to retain the raw `reasoning` output items, sets
  `include: ["reasoning.encrypted_content"]` for stateless replay, and inserts
  them before the matching assistant message/function calls. Summary-only or
  ID-only Responses items are not replayable and stay display-only. The display
  copy remains capped at 64 KiB and the continuation copy is never logged.
  Future native adapters follow the same pattern; see
  [architecture §2.4](../architecture.md#24-continuation-for-a-new-dialect).
- **Eval baseline.** Superseded by the removal note above. The 30-task suite
  and driver were complete and tested with fake arms; `eval/baseline.json`
  honestly recorded `status=unmeasured` with the credential reason. No solve
  rate, token delta, or cost delta was ever claimed.
- **Manual smokes.** The real-provider thinking-tool and cache-hit runs remain
  release prerequisites; see §11.
