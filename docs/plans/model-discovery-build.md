# aharness model discovery build plan (opencode-go)

- **Date:** 2026-11-10; implemented 2026-11-11.
- **Status:** implemented as of 2026-11-11. The shipped contract is in
  [architecture.md](../architecture.md), [limits.md](../limits.md),
  [tui.md](../tui.md), and [testing-strategy.md](../testing-strategy.md); this
  document records the plan, decisions, and deviations (§11).
- **Source:** [v1 build plan](v1-build.md), the shipped provider/model registry,
  and the opencode-go catalog.
- **Target:** Go 1.27.3, module `aharness`.
- **Architecture:** [architecture.md](../architecture.md) describes the shipped
  provider/model behavior after discovery replaced the fixed registry.
- **Testing:** [testing-strategy.md](../testing-strategy.md) plus §6 here.
- **Limits:** [limits.md](../limits.md); §7 records the new caps and changed
  values.
- **Definition of done:** [project definition of done](../definition-of-done.md);
  feature criteria in §1.4.

## Decision summary

- Add `-provider` and `AHARNESS_PROVIDER`; the only supported value remains
  `opencode-go`. The provider is recorded in `session_start` and inherited on
  resume/fork unless explicitly changed.
- For `opencode-go`, load the models.dev catalog from
  `https://models.dev/api.json`, select the `opencode-go` provider, and cache a
  normalized copy under the state directory.
- The cache can be refreshed on demand: `-refresh-models` performs an
  unconditional fetch, replaces the cache, and exits without a session; the TUI
  model picker refreshes in place with `Ctrl-R`.
- Discover each model's ID, display name, context/input/output limits, tool-call
  support, status, base URL, and dialect. Remove `acceptedModels`,
  `acceptedModelIDs`, `defaultModelID`, and `dialectForModel`.
- Derive the dialect from models.dev's AI SDK package: `@ai-sdk/openai` is
  Responses; every other supported package uses Chat Completions. OpenCode
  documents that every text model also works through the other APIs, so
  Anthropic- and Google-native models are used through Chat translation.
- New sessions default to the model's context window and the model's input
  limit. The harness imposes no per-call output budget: `-max-output-tokens`
  defaults to unset, and the request omits `max_tokens` / `max_output_tokens` so
  the provider/model ceiling applies. An explicit positive value is the
  operator's own budget. The catalog's output limit is informational.
- Resume/fork without an explicit model does not require a catalog fetch for
  resolution; the values recorded in the session log are authoritative. This
  keeps resume offline and reproducible. The TUI may load the catalog
  best-effort for its picker and falls back to the recorded model.
- No session schema change. `session_start` and `run_start` already record the
  effective provider, model, dialect, base URL, and limits.

## 1. Scope

### 1.1 What this plan owns

| Area | Implementation |
|---|---|
| Provider selection | `-provider` / `AHARNESS_PROVIDER`, default `opencode-go`; validated and recorded. |
| Catalog discovery | models.dev `api.json`, normalized catalog, 24-hour disk cache, conditional refresh, stale-cache fallback, explicit `-refresh-models` / picker `Ctrl-R` refresh. |
| Model eligibility | Chat/text models with tool calls and usable context/input limits; the advertised output ceiling is informational; deprecated models are selectable but labeled. |
| Dialect | Models.dev AI SDK package mapped to `openai-chat` or `openai-responses`; unsupported native SDKs translate through Chat. |
| Limits | Context/input limits from the catalog; no harness per-call output cap; explicit overrides; existing safety margin and compaction target. |
| Base URL | Model/provider base URL from the catalog; explicit override wins; discovered URLs restricted to `opencode.ai`. |
| Registry removal | Remove the hardcoded model list, default model, and model-to-dialect switch. |
| Listing | `-list-models` prints the usable catalog and exits without creating a session. |
| TUI | Model picker lists discovered models and refreshes the catalog with `Ctrl-R`; switching updates the next run's dialect, base URL, and limits. |
| Documentation | Architecture, limits, testing strategy, TUI doc, README, and this plan. |

### 1.2 What stays the same

- One provider, `opencode-go`; no provider matrix, routing, fallback, or price
  ranking.
- The agent loop, prompt, four tools, workspace policy, projection, compaction,
  retry policy, and session v2 schema.
- The two OpenAI wire adapters (`model.OpenAIChat` and
  `model.OpenAIResponses`) and their request/response canonicalization.
- `-model` or `AHARNESS_MODEL` is required for a new session. The TUI model
  picker changes the model for the next run only.
- Provider auth stays `AHARNESS_PROVIDER_TOKEN`; provider-specific environment
  variables are not read implicitly.
- No new runtime dependency. Discovery uses `net/http` and `encoding/json/v2`.

### 1.3 Not in this plan

- Native Anthropic Messages or Google generate-content adapters. Those models
  run through OpenCode's documented Chat translation; native adapters remain
  deferred.
- Additional providers, automatic model selection, fallback chains, or
  capability/price-based routing.
- Per-call output-budget estimation. There is no default output budget; an
  explicit `-max-output-tokens` value applies unchanged to every call.
- Background, periodic, or during-run catalog refresh. Explicit refresh (the
  `-refresh-models` flag and the picker `Ctrl-R` action) is in scope.
- Changes to turn/token/wall-clock budgets, prompt, tools, session schema, or
  the `x-opencode-session` contract. Run-budget removal is planned separately
  in [limits-removal-build.md](limits-removal-build.md).
- Model-based summarization or any new compaction behavior.

### 1.4 Definition of done

In addition to the [project definition of done](../definition-of-done.md), the
plan is complete when every item below holds.

- [x] `-provider` and `AHARNESS_PROVIDER` work; an unknown provider fails with
      exit 2 and a message listing `opencode-go`.
- [x] A new session with a discovered `-model` uses the catalog's context and
      input limits, dialect, and base URL; `session_start` and `run_start`
      record the effective values. A new session sends no output budget unless
      `-max-output-tokens` is explicit; resume/fork inherits the recorded value
      unless the flag clears it.
- [x] Provider length and context-overflow outcomes are reported clearly: a
      `length` response uses the existing continuation path, a context-overflow
      error uses the existing compaction retry, and an unrecoverable failure
      stops with a typed status.
- [x] No hardcoded model IDs, default model, or model-to-dialect switch remain.
      A grep for `acceptedModels`, `acceptedModelIDs`, `defaultModelID`, and
      `dialectForModel` is empty.
- [x] All eligible opencode-go models are selectable; ineligible models are
      omitted with a diagnostic. Deprecated models are labeled and selectable.
- [x] `-list-models` prints the usable catalog and exits without creating a
      session.
- [x] The TUI picker is catalog-backed, and switching models updates the next
      run's dialect, base URL, and limits.
- [x] The catalog cache works: fresh-cache runs make no network request; stale
      entries use conditional GET; a failed implicit refresh falls back to
      stale data with a warning; no cache plus a failed fetch is a clear error.
- [x] `-refresh-models` performs an unconditional fetch, atomically replaces
      the cache, prints a summary, and exits without creating a session or
      requiring a task/token. A failed explicit refresh exits 3 or 4 and leaves
      the existing cache unchanged.
- [x] The TUI model picker refreshes with `Ctrl-R`, replaces its entries on
      success, preserves the current selection when it still exists, and shows
      a notice on failure without discarding the loaded catalog.
- [x] Resume/fork without an explicit model works with the catalog server
      unreachable; the recorded session values are used.
- [x] Unit, integration, fuzz, race, build, lint, and vulnerability gates pass.
      Verified 2026-11-11: `goimports -l .` clean, `go vet ./...`,
      `golangci-lint run` 0 issues, `go test -race -count=1 ./...`,
      `go build -trimpath -o bin/aharness ./cmd/aharness`, and
      `govulncheck ./...` (no called vulnerabilities). The catalog fuzz target
      also passed a short local run.
- [ ] A real-provider smoke passes for one Chat model and one Responses model;
      one translated Anthropic- or Google-native model is tested if credentials
      and workspace policy allow it. Not run on 2026-11-11 because
      `AHARNESS_PROVIDER_TOKEN` was unset. A local smoke against the real
      models.dev catalog and a fake provider passed for a Chat model
      (`deepseek-v4.1-flash`), a Responses model (`crouton-alpha-free`), and a
      translated Anthropic model (`claude-haiku-5-5`, Chat path), including
      discovered limits, recorded values, and omitted output fields.
- [x] README, architecture, limits, testing-strategy, and TUI docs are updated.

## 2. Pre-implementation state and catalog findings

Before discovery, the shipped code had three model options and two dialect
mappings in `cmd/aharness/config.go`:

- `acceptedModels`, `acceptedModelIDs`, and `defaultModelID`.
- `dialectForModel` mapped `deepseek-v4.1-flash` to Chat and
  `crouton-alpha-free` / `muse-spark-1.3-contributor` to Responses.
- Context defaulted to 200,000 provisional; output defaulted to 8,192; neither
  was read from the provider. The `-max-context-tokens` flag defaulted to
  200,000, so the new resolution path had to distinguish "unset" (use the
  catalog) from an explicit value.
- `model.DefaultBaseURL` was `https://opencode.ai/inference/go/v1`; the
  documented OpenAI endpoints are under
  `https://opencode.ai/inference/go/openai/v1`.
- The TUI built its picker from `acceptedModelIDs` and ignored per-model
  limits and base URLs.

models.dev's `api.json` for `opencode-go` is a provider object with a `models`
map. The fields this plan uses are:

| Field | Use |
|---|---|
| `provider.npm` | Default AI SDK package; `@ai-sdk/openai-compatible` today. |
| `model.provider.npm` | Per-model SDK override; omitted means inherit the provider. |
| `model.limit.context` | Default per-call context window. |
| `model.limit.input` | Optional input ceiling. |
| `model.limit.output` | Advertised output ceiling; informational only, never a default or sent request budget. |
| `model.tool_call` | Eligibility: the harness needs tool calls. |
| `model.modalities.output` | Eligibility: must contain `text`. |
| `model.status` | `deprecated`, `alpha`, or `beta`; preserve for display. |
| `model.provider.api` | Per-model base URL, when present. |
| `model.name` | Human-readable picker label. |

A snapshot on 2026-11-10 had 40 models: 26 inherited
`@ai-sdk/openai-compatible` (Chat), 6 used `@ai-sdk/openai` (Responses), 6 used
`@ai-sdk/anthropic`, and 2 used `@ai-sdk/google`. Eight were deprecated. All 40
advertised tool calls and text output. Counts are data, not a contract.

The GitHub repository is the upstream source of the data. The runtime uses the
published `https://models.dev/api.json` endpoint, so it does not need GitHub API
credentials or subject itself to GitHub API rate limits.

models.dev's v1 `api.json` does **not** publish the `[api.<protocol>]` tables
from its v2 TOML files. It publishes the provider/model base URL and AI SDK
package only. The dialect therefore has to be derived from the SDK package.
The OpenCode Go provider also has a documented `/inference/go/v1/models`
endpoint that includes an `api` map with protocol names (`chat-completions`,
`responses`, `messages`, `generate-content`); it is the verification source for
the real smoke if the SDK mapping ever looks wrong. It is not the runtime source
in this plan because the request is for models.dev.

OpenCode documents that every text model also works through the other APIs,
translated by Console. That is what makes the Anthropic/Google-native models
usable with the existing Chat adapter.

## 3. Contract

### 3.1 Provider and model resolution

Precedence, highest first:

| Value | Sources |
|---|---|
| Provider | `-provider`, `AHARNESS_PROVIDER`, session record, default `opencode-go` |
| Model | `-model`, `AHARNESS_MODEL`, session record; required for a new session |
| Base URL | `-provider-base-url`, `AHARNESS_PROVIDER_BASE_URL`, model catalog, session record |
| Context | `-max-context-tokens`, `AHARNESS_MAX_CONTEXT_TOKENS`, model catalog |
| Output | `-max-output-tokens`, session record; unset means no harness cap |

The session record applies only on resume/fork when the corresponding value was
not explicit. It outranks the catalog because the recorded values are the replay
contract; the catalog is consulted only for an explicit model change. An unset
output budget is recorded as `0`; `0` means no harness cap and omits the request
field. The catalog's advertised output ceiling never supplies a default and
does not clamp an explicit value.

CLI and environment surface:

| Flag / variable | Default | Behavior |
|---|---|---|
| `-provider` / `AHARNESS_PROVIDER` | `opencode-go` | Selects the provider and catalog key; unknown values are config errors. |
| `-list-models` | off | Prints usable models and exits; no session, task, or token. |
| `-refresh-models` | off | Terminal action: unconditional catalog fetch, atomic cache replace, one-line summary, then exits. With `-list-models`, refreshes before printing the list. |
| `-model` / `AHARNESS_MODEL` | none | Required for a new session; inherited on resume/fork unless explicit. |
| `-provider-base-url` / `AHARNESS_PROVIDER_BASE_URL` | catalog | Overrides the discovered base URL; accepts loopback test servers. |
| `-max-context-tokens` / `AHARNESS_MAX_CONTEXT_TOKENS` | 0 (unset) | Explicit positive value; clamped to the model context. |
| `-max-output-tokens` | 0 (unset) | `0` omits the request field and clears an inherited budget; a positive value is sent unchanged; a negative value is a config error. |
| `AHARNESS_MODELS_DEV_URL` | `https://models.dev/api.json` | Test/mirror override for the catalog; HTTPS except loopback HTTP. |

Resolution algorithm:

1. Parse provider/model/base-URL/limit flags and environment, remembering which
   values were explicit.
2. Validate the provider. `opencode-go` is the only supported value for now.
3. New session: require a model. Load the catalog, look up the model exactly,
   and reject missing or ineligible models.
4. Resolve the effective base URL, dialect, context window, optional explicit
   output budget, safety margin, and input cap (§3.3–§3.4). Apply context
   clamps and emit warnings.
5. Resume/fork with an explicit model: resolve it through the catalog. Without
   an explicit model, inherit the recorded provider/model/dialect/base
   URL/limits and skip the catalog; an explicit provider must match the
   recorded provider.
6. Validate the effective limits and start the session/run.

A single `resolveModel` helper is used by the batch path and the TUI host. It is
called after flags/env parsing and before `session_start` for a new session, and
before each run when the TUI selects a different model.

`-version`, `-refresh-models`, and `-list-models` are terminal actions; they run
before mode selection and session creation and require no task, session, or
provider token. Precedence is `-version`, then `-refresh-models`, then
`-list-models`. `-refresh-models -list-models` refreshes first and then prints
the list. Task, session-selection, and mode flags are ignored by a terminal
action; `-provider`, `-state-dir`, and the catalog URL still apply.
`-refresh-models` is a flag rather than a subcommand to keep the single
flag-based CLI contract (§9).

### 3.2 Discovery source and cache

- Default source: `https://models.dev/api.json`.
- The catalog URL is overridable with `AHARNESS_MODELS_DEV_URL` for tests and
  local mirrors. The default must be HTTPS. An HTTP override is allowed only
  for loopback hosts (`127.0.0.1`, `::1`, `localhost`).
- The request sends `Accept: application/json` and
  `User-Agent: aharness/<version>`. It never sends the provider token.
- Request timeout: 15 seconds total. Response body cap: 16 MiB. No automatic
  retry; the cache is the retry story.
- Parse with `encoding/json/v2` into a narrow struct; ignore unknown members so
  models.dev can add fields. Duplicate object names are rejected by the v2
  decoder.
- Cache path: `<state>/cache/models.dev/<provider>.json` (`opencode-go.json`
  today), file mode 0600, directory mode 0700. Cache writes are atomic
  (temporary file plus rename). For implicit discovery a cache write failure is
  a warning and discovery proceeds from the network response; an explicit
  refresh fails instead (see below).
- Cache TTL: 24 hours. A fresh cache is used without a network request. A stale
  cache triggers a conditional GET with `If-None-Match`; `304` refreshes
  `fetched_at`, `200` replaces the cache.
- If an implicit refresh (TTL expiry) fails (transport, 5xx, timeout) or
  returns an unusable catalog, and a stale but valid cache exists, continue
  with the cache and emit a warning. If no valid cache exists, fail. A corrupt
  or version-mismatched cache is ignored and treated as absent.
- Explicit refresh paths:
  - `-refresh-models` and the TUI model picker's `Ctrl-R` bypass the TTL and
    the stored ETag. They send an unconditional GET with no `If-None-Match`;
    a `304` response to an unconditional request is a protocol error.
  - A `200` response is normalized, validated, and atomically replaces the
    cache, including its `etag` and `fetched_at`, for later conditional
    refreshes.
  - An explicit refresh has no stale fallback. Transport, timeout, or 5xx
    failure is exit 3; invalid catalog JSON/schema or an unexpected `304` is
    exit 4. The existing cache is not modified.
  - A CLI refresh that cannot write the cache fails with a state-directory
    error (exit 2), because the refresh did not persist. The TUI uses the
    refreshed in-memory catalog and shows a notice.
  - `-list-models` without `-refresh-models` remains the lighter revalidation
    path: it bypasses the TTL, sends `If-None-Match`, and keeps stale-cache
    fallback.
  - Explicit refresh uses the same source URL, non-conditional headers,
    timeout, body cap, and token rules as discovery.
- Cache envelope:

```json
{
  "cache_version": 1,
  "source": "https://models.dev/api.json",
  "provider": "opencode-go",
  "fetched_at": "2026-11-10T23:00:00Z",
  "etag": "\"8e22...\"",
  "models": []
}
```

The normalized `models` array contains only the fields in §3.3–§3.4. Bump
`cache_version` when the normalized shape changes.

Validation of the network catalog:

- The top-level object must contain the selected provider key (`opencode-go`
  today); its `models` must be non-empty.
- Each model key must be a non-empty UTF-8 ID of at most 256 bytes with no
  control characters. The model's `id`, when present, must match the key.
- `limit.context` must be positive. `limit.input` is used when present and
  greater than zero; zero is treated as absent. A positive `limit.input` must
  not exceed `limit.context`. `limit.output` is optional and informational; a
  non-positive value is treated as absent and never makes a model ineligible.
- `tool_call` must be true; `modalities.output` must contain `text`; `type`
  must be absent or `chat`.
- The SDK must be one of the mapped values below. Unknown SDKs are marked
  unavailable with a diagnostic and fail if explicitly selected.
- A model missing `limit.context` is marked unavailable with a diagnostic; it
  is not repaired with a hardcoded constant. A missing output ceiling is
  displayed as unknown and does not exclude the model.

### 3.3 Dialect and base URL

The native SDK comes from `model.provider.npm`, falling back to
`provider.npm`. The effective adapter dialect and base URL are:

| SDK package | Native protocol | Effective dialect | Base URL |
|---|---|---|---|
| `@ai-sdk/openai-compatible` | `chat-completions` | `openai-chat` | model `provider.api`, else provider `api` |
| `@ai-sdk/openai` | `responses` | `openai-responses` | model `provider.api`, else provider `api` |
| `@ai-sdk/anthropic` | `messages` | `openai-chat` (translated) | provider `api` (OpenAI base) |
| `@ai-sdk/google` | `generate-content` | `openai-chat` (translated) | provider `api` (OpenAI base) |
| anything else | unknown | unavailable | — |

Rules:

- Store the native protocol/SDK on the catalog record for display and
  diagnostics; `Dialect` is always one of the two values the harness
  implements.
- Translation is deliberate: OpenCode documents that every text model works
  through the other APIs. Do not point a translated model at its Anthropic or
  Google base URL.
- Discovered base URLs must be HTTPS, have a host, and have no userinfo, query,
  or fragment. For provider `opencode-go`, the host must be `opencode.ai`.
  This prevents a compromised catalog from redirecting the provider token to a
  different host.
- If neither the model nor the provider supplies a base URL, mark the model
  unavailable; do not guess one.
- Explicit `-provider-base-url` / `AHARNESS_PROVIDER_BASE_URL` may point at any
  valid HTTP(S) host, including loopback test servers, and wins over discovery.
- Existing sessions keep their recorded base URL. New sessions use the
  discovered documented OpenAI base
  (`https://opencode.ai/inference/go/openai/v1`), not the old compiled-in
  `https://opencode.ai/inference/go/v1`.

### 3.4 Limits

Let `context` be the effective context window and
`margin = max(5% of context, 1000)` (existing `agent.SafetyMargin`).

- Context default: `model.limit.context`.
- `-max-context-tokens` changes its default from 200,000 to 0, meaning "unset,
  use the catalog." An explicit positive value uses the smaller of the explicit
  value and `model.limit.context`; warn when clamped. A smaller explicit value
  is honored. An explicit zero or negative value remains a config error.
- `AHARNESS_MAX_CONTEXT_TOKENS`, when set, is explicit and follows the same
  clamp rule.
- Output: no harness-imposed per-call budget by default. `-max-output-tokens`
  defaults to 0 (unset); the request omits `max_tokens` / `max_output_tokens`,
  so the provider/model decides the output ceiling.
- An explicit positive `-max-output-tokens` is sent unchanged on every call;
  the catalog does not clamp it. An explicit 0 means "no cap" and clears an
  inherited budget on resume/fork; a negative value is a config error.
- Input cap: `min(model.limit.input, context - margin)` when
  `model.limit.input` is present and greater than zero; otherwise
  `context - margin`. The output budget is not subtracted.
- Reject the model/configuration when `input <= 0`.
- `CompactionTarget` remains the input cap.
- Run-budget defaults and replay semantics are owned by
  [limits-removal-build.md](limits-removal-build.md): turns, total tokens, run
  wall clock, and the model-call timeout are opt-in and unlimited by default.

The output budget is intentionally unset by default. A fixed default (8,192 in
v1) is an artificial cap: reaching it produces a `length` response, and the
operator can only raise it by restarting with a flag. Omitting the parameter
delegates the ceiling to the provider/model; an explicit flag is the operator's
own choice. Failures are reported rather than preempted:

- A `length` finish reason uses the existing continuation hint; two consecutive
  `length` responses without a tool round stop with `token_limit`.
- A provider context-overflow error uses the existing one-shot compaction retry
  against 90% of the failed per-call input limit. If the retry fails, the run
  stops with a typed error; it is not retried indefinitely.
- A provider rejection of an explicit output budget is a provider error naming
  the requested value; the harness does not silently clamp or retry with a
  different budget.

The catalog's `limit.output` is the provider's advertised maximum for one
request. It is recorded and displayed, but it is not a request budget and does
not gate eligibility. Resume/fork inherits the recorded output value unless
`-max-output-tokens` is explicit; `0` in the record means no harness cap.

Examples with the snapshot data:

| Model | Context | Input limit | Advertised output ceiling | Effective default |
|---|---:|---:|---:|---|
| `deepseek-v4.1-flash` | 1,000,000 | — | 384,000 | ctx 1,000,000; input 950,000; output unset |
| `gpt-6-luna` | 1,050,000 | 922,000 | 128,000 | ctx 1,050,000; input 922,000; output unset |
| `kimi-k2.7-code` | 262,144 | — | 262,144 | ctx 262,144; input 249,037; output unset |

### 3.5 Model selection and TUI

- A new session still requires `-model` or `AHARNESS_MODEL`; there is no
  hardcoded default. The TUI starts with that model selected.
- `-list-models` is a terminal action like `-version`: it uses `-provider` and
  the catalog, prints the usable models, and exits without creating a session.
  Task and session-selection flags are ignored. It bypasses the freshness TTL
  and performs a conditional GET (falling back to cache on failure), so listing
  also revalidates the cache without forcing a full download. Output is a
  tab-separated header plus one row per model: `ID`, `NAME`, `DIALECT`,
  `CONTEXT`, `OUTPUT`, `STATUS`, `EFFORT`. `OUTPUT` is the advertised ceiling,
  not an effective request budget; a missing ceiling prints as `-`. `EFFORT`
  is the normalized comma-separated effort list added by
  [thinking effort](thinking-effort-build.md); a model with no effort control
  prints `-`.
- `-refresh-models` is the explicit cache-refresh action. It validates
  `-provider`, performs the unconditional fetch described in §3.2, atomically
  replaces the cache, and exits 0 without creating a session or reading the
  provider token. Alone it prints one stdout line:
  `refreshed <n> usable models for <provider> from <source> at <RFC3339>`; with
  `-list-models` the same line goes to stderr so stdout stays the tab-separated
  list. A refresh failure exits 3/4 and leaves the existing cache unchanged.
  When combined with `-list-models`, the list is printed from the catalog
  returned by the refresh; no second catalog request is made.
- The model picker lists eligible models from the catalog, sorted by ID. Each
  row shows the display name, ID, dialect, context/input limits, advertised
  output ceiling, normalized effort values, and a `deprecated` / `alpha` /
  `beta` / `translated` marker when applicable.
- `Ctrl-R` in the model picker triggers the same explicit refresh. The picker
  footer shows `refreshing models…`; a second `Ctrl-R` while the request is in
  flight is ignored, and closing the picker cancels it without changing the
  loaded entries. On success the entries are replaced in place, preserving the
  highlighted model and filter when they still exist; otherwise the first
  usable model is highlighted. On failure the loaded entries remain and a
  notice reports the error. Refresh does not change the current model; only an
  explicit picker selection does. Outside the picker, `Ctrl-R` keeps its
  existing rich/raw meaning. The current model picker is idle-only (a run
  disables it), so refresh never races a run. `-ui-json` already accepts
  `ctrl-r`; when the picker is open it means refresh, and the protocol version
  does not change.
- Deprecated models remain selectable and explicitly labeled; they are not
  chosen automatically.
- Selecting a model changes the next run's dialect, base URL, and limits.
  Explicit context flags are clamped to the new model; an explicit output
  budget is sent unchanged.
- The details overlay shows the effective values for the current/next run,
  including `max output: unset` when no explicit budget applies.
- On resume without an explicit model, the TUI may skip the network. If the
  catalog is unavailable, the picker contains only the recorded current model
  with a notice that model switching is disabled; resume still works.
  `Ctrl-R` retries the catalog fetch; on success the picker expands to the
  usable catalog and switching is enabled.
- If the recorded model is absent from the catalog, it is still shown as the
  current picker entry and remains usable from the session record. Switching to
  a different model requires catalog metadata; switching back to the recorded
  model after leaving it is allowed only if the catalog still has it.
- Warnings (clamped context limit, deprecated model, stale cache, refresh
  failure) are stderr lines in batch mode and notices in the TUI. `-ui-json`
  uses the existing notice/error surface; do not write to stderr after the UI
  starts.

### 3.6 Session and replay

- No event or schema change. `session_start` records the startup provider,
  model, dialect, base URL, and limits; `run_start` records the effective
  values for each run. `max_output_tokens` is `0` when no harness cap applies;
  old logs with `8,192` keep that recorded budget.
- A TUI model switch before the first run appears in `run_start`; the session's
  initial model remains in `session_start`.
- A catalog change between runs does not rewrite existing logs. The next
  explicit model selection records new effective values in `run_start`.
- Resume/fork without an explicit model uses the recorded values and does not
  require models.dev for resolution. The TUI may load the catalog best-effort
  for the picker, as described in §3.5.
- An explicit provider different from the recorded provider is a config error.
  An explicit model change is allowed and resolved through the catalog.
- Existing v2 logs need no migration. They keep their recorded base URL, model,
  dialect, and limits, including the old `.../go/v1` base and the 200,000/8,192
  defaults. New sessions use discovery.

### 3.7 Errors and exit codes

| Condition | Exit | Behavior |
|---|---:|---|
| Unknown provider | 2 | Config error listing `opencode-go`. |
| Model not in catalog | 2 | Config error naming the provider and model. |
| Model lacks tools/text/context | 2 | Config error naming the missing requirement. |
| Unsupported SDK explicitly selected | 2 | Config error naming the SDK. |
| Invalid explicit limit | 2 | Existing validation messages; a negative output value is invalid, while `0` means unset. |
| Explicit output budget rejected by provider | 3 | Provider error naming the model and requested value; no silent clamp or retry with another budget. |
| Catalog fetch fails, no valid cache | 3 | Provider error; mention the source URL and cache path. |
| Catalog JSON/schema is invalid, no valid cache | 4 | Protocol error; no payload or secrets logged. |
| Implicit or `-list-models` refresh fails, stale cache used | 0 | Warning; continue with the cache. |
| Explicit refresh transport/timeout/5xx | 3 | Error names the source and cache path; cache unchanged; no stale fallback. |
| Explicit refresh invalid JSON/schema or unexpected `304` | 4 | Protocol error; cache unchanged; no payload or secrets logged. |
| Explicit refresh cache write fails | 2 | State-directory error; the refreshed catalog was not persisted. |
| Explicit context limit above model ceiling | 0 | Clamp and warn; record the effective value. |
| Deprecated model selected | 0 | Warn; continue. |

`-list-models` exits 0 on success and uses the same config/catalog errors on
failure. It creates no session and requires no task or token. With
`-refresh-models`, the refresh runs first and its error wins; no list is
printed. `-refresh-models` alone likewise creates no session and requires no
task or token.

### 3.8 Security

- The catalog request is unauthenticated and must never include the provider
  token, session ID, workspace path, or task text.
- Treat catalog strings as untrusted: validate IDs/base URLs before use and
  sanitize names before terminal display.
- Restrict discovered base URLs to `opencode.ai` for `opencode-go`; only an
  explicit operator flag may use another host.
- The cache contains public catalog data. Use 0600/0700 anyway to match the
  state directory policy.
- Validate provider IDs as `[a-z0-9-]+` before using one in a cache path or
  filename.
- Do not use a model ID as a file path. It is only a JSON value and a map key.
- Keep the existing provider token behavior: memory only, never logged, never
  passed to tools.

## 4. Package changes and wiring

Discovery lives in `internal/model` to avoid a sixth package: the catalog
returns `model.Dialect` values and base URLs, and `cmd` already imports `model`.
No `model` → `agent` import is introduced; limit computation stays in `cmd`.

| File | Change |
|---|---|
| `internal/model/catalog.go` (new) | `CatalogModel`, `Catalog`, `CatalogSource`, `CatalogOptions`, normalization, eligibility, dialect map, cache path, normalized cache envelope. |
| `internal/model/catalog_source.go` (new) | models.dev HTTP source: load, TTL revalidation, explicit refresh, conditional GET, cache read/write, stale fallback, `Catalog.Warnings`. |
| `internal/model/catalog_test.go` (new) | Unit, cache, and fuzz tests; fixture under `internal/model/testdata/`. |
| `cmd/aharness/config.go` | `-provider`/`AHARNESS_PROVIDER`; remove hardcoded registry; flag/env/limit defaults; `-list-models`; `-refresh-models`; terminal-action parsing. |
| `cmd/aharness/models.go` (new) | `resolveModel`, session inheritance, provider/base-URL/limit resolution, catalog source wiring, list/refresh actions and exit-code mapping. |
| `cmd/aharness/run.go` | Resolve the catalog model before session creation; run the terminal refresh/list actions; update the no-token warning to test the resolved base URL host. |
| `cmd/aharness/tui.go` | Catalog-backed picker; `Ctrl-R` refresh with in-flight/cancel handling; per-run model resolution; offline resume fallback. |
| `internal/tui/app.go` | `ModelChoice` metadata; picker refresh message/result; remove the hardcoded `Default` field. |
| `internal/tui/widgets.go` | Picker labels/filter for limits, status, translation; `refreshing models…` footer. |
| `cmd/aharness/*_test.go` | Config precedence, discovery, cache, list, refresh, provider mismatch, TUI picker. |
| `docs/*` | README, architecture, limits, testing strategy, TUI doc. |

Both wire request structs tag the output budget `omitzero` (`max_tokens` and
`max_output_tokens`). `encoding/json/v2`'s `omitempty` did not omit a zero
integer (the first smoke sent `max_tokens: 0`), so the tags use `omitzero`; an
unset budget of `0` now omits the field. The adapters need no request-shape
change. Limit computation stays in `cmd`, and the input cap no longer subtracts
an output budget.

Sketch:

```go
type CatalogModel struct {
    ID            string
    Name          string
    Provider      string
    NativeSDK     string
    NativeDialect string
    Dialect       Dialect
    BaseURL       string
    ContextWindow int
    InputLimit    int
    OutputLimit   int // advertised ceiling; informational, not a request budget
    ToolCall      bool
    Status        string // "", "alpha", "beta", or "deprecated"
    Deprecated    bool   // convenience for Status == "deprecated"
    Translated    bool   // effective dialect differs from the native protocol
    Unavailable   string // non-empty means the model is not usable
}

type Catalog struct {
    Provider  string
    Source    string
    FetchedAt time.Time
    ETag      string
    Models    map[string]CatalogModel
    Warnings  []string // non-fatal stale-cache/refresh fallbacks
}

type CatalogSource interface {
    // Load returns a catalog, honoring the cache TTL and conditional GET.
    Load(ctx context.Context, provider string) (Catalog, error)
    // Revalidate bypasses the TTL and keeps conditional GET and stale fallback;
    // -list-models uses it to revalidate without a forced full download.
    Revalidate(ctx context.Context, provider string) (Catalog, error)
    // Refresh bypasses the TTL and ETag and replaces the cache on success.
    // It has no stale fallback.
    Refresh(ctx context.Context, provider string) (Catalog, error)
}

func (c Catalog) Lookup(id string) (CatalogModel, error)
func (c Catalog) Usable() []CatalogModel
func (c Catalog) SortedModels() []CatalogModel // includes unavailable entries
```

`Catalog.Models` keeps normalized entries even when `Unavailable` is set, so an
explicit selection can report why a model is not usable. `Usable` filters those
entries, sorts by ID, and deprecation only affects the label, not availability.
`cmd` constructs the models.dev source with the HTTP client, state directory,
clock, and cache TTL; tests inject a fake `CatalogSource` or an `httptest`
server.

## 5. Milestones

### M0 — Catalog source and normalization

- Implement `internal/model/catalog.go` and `internal/model/catalog_source.go`
  with the fixture-based tests.
- Fetch, normalize, validate, cache, conditionally refresh, and force-refresh.
- Implement the SDK→dialect mapping and eligible-model filtering.
- No CLI behavior changes yet.

Acceptance: a test fixture with Chat, Responses, Anthropic, Google, deprecated,
missing-context, missing-output, no-tool, and unknown-SDK entries yields the
expected usable set; cache tests cover fresh, 304, 200, stale fallback, corrupt,
and no-cache paths; the explicit refresh path sends no conditional header and
preserves the old cache on every failure.

### M1 — Provider flag and batch resolution

- Add `-provider`/`AHARNESS_PROVIDER`, `-list-models`, `-refresh-models`, and
  `resolveModel`.
- Remove the hardcoded registry and dialect switch.
- New sessions use discovered context/input limits, dialect, and base URL; no
  output budget is sent by default.
- Resume/fork inheritance and offline behavior.
- Integration tests with a fake models.dev server and fake provider.

Acceptance: `aharness -list-models` prints the usable catalog;
`aharness -refresh-models` performs an unconditional fetch, prints its summary,
and exits without a session, while a failed refresh exits 3/4 and leaves the
seeded cache intact; a batch run against the fake provider sends the
catalog-selected path and records the effective values, and its request omits
`max_tokens` / `max_output_tokens`; an explicit output budget is sent
unchanged; a second run with the catalog server down resumes from the session
log; an explicit model change uses discovery.

### M2 — TUI picker and per-run resolution

- Build `[]tui.ModelChoice` from the catalog.
- Show names, context/input limits, advertised output ceilings, status, and
  translation markers; remove the default label.
- Recompute dialect/base URL/limits when the next run's model changes.
- Implement the offline-resume one-entry picker fallback and `Ctrl-R` refresh.
- Update the details overlay, refresh footer, and notices.

Acceptance: TUI tests select a Chat model and a Responses model and observe the
corresponding adapter/path and `run_start` values; a deprecated model is listed
with its marker; `Ctrl-R` refreshes and preserves the selection, and a failed
refresh keeps the loaded list with a notice; resume with the catalog unavailable
still starts with the recorded model, and `Ctrl-R` enables switching when the
catalog returns.

### M3 — Documentation and gates

- Update README, architecture, limits, testing strategy, and TUI doc.
- Run the coding-standard §8 gates.
- Run the real-provider smoke.

Acceptance: no hardcoded model/dialect symbols remain; all gates pass; the
smoke records one Chat and one Responses model with discovered context/input
limits and base URLs, and confirms the request has no default output budget.

## 6. Testing

### Unit

- Catalog decoding and normalization from a fixture.
- Dialect mapping for every supported and unsupported SDK.
- Eligibility: no tools, non-text output, missing context, missing output
  ceiling (still eligible), invalid input, deprecated/alpha/beta, unknown type.
- Limit resolution: context default/clamp, output unset, explicit output
  pass-through without a catalog clamp, explicit `0` clearing an inherited
  budget, negative output rejection, input ceiling without output subtraction,
  margin, invalid combinations.
- Base URL validation: discovered host allowlist, explicit loopback override,
  userinfo/query/fragment rejection.
- Cache: fresh hit without HTTP, stale conditional `304`, replacement on `200`,
  stale fallback on failure, corrupt cache, version mismatch, atomic write and
  modes.
- Explicit refresh: no `If-None-Match`, unconditional `200` replaces the cache,
  `304` is rejected, every transport/protocol failure leaves the previous cache
  bytes intact, and the cache write failure is fatal for the CLI path.
- Fuzz the catalog decoder at the trust boundary with a seed corpus under
  `internal/model/testdata/fuzz/`.

### Integration

- Fake models.dev server plus fake provider, temp workspace/state.
- Assert `session_start` and `run_start` provider/model/dialect/base URL/limits.
- Assert `/chat/completions` for Chat models and `/responses` for Responses
  models, including `x-opencode-session` and `User-Agent`.
- The default request bodies contain no `max_tokens` / `max_output_tokens`;
  an explicit `-max-output-tokens` value appears unchanged; resuming an old
  session with a recorded budget and `-max-output-tokens 0` omits the field; a
  provider `length`/context-overflow response follows the existing continuation
  and compaction-retry paths.
- `-list-models` output and exit code; no session file created.
- `-refresh-models`: the fake catalog server observes an unconditional request
  with no `If-None-Match` and no token; the summary is printed; no session file
  is created. Server-down and invalid-catalog cases exit 3/4 and preserve a
  seeded cache. `-refresh-models -list-models` refreshes before listing.
- Cache across two processes; seed a stale `fetched_at`, then run with the
  catalog server down to exercise stale fallback.
- Resume with the catalog server down and no explicit model.
- Explicit model change on resume; provider mismatch error.
- Deprecated warning and explicit selection.
- TUI picker contents and model-switch effects; `Ctrl-R` refresh success,
  selection preservation, failure notice, in-flight cancellation on close, and
  the offline-resume one-entry retry; `-ui-json` `ctrl-r` with the picker open.

### Manual smoke

Run against the real models.dev catalog and `opencode-go` with a token. Use a
current Chat model (for example `deepseek-v4.1-flash`) and a current Responses
model (for example `muse-spark-1.3-contributor`). If credentials and workspace
policy allow, also run one Anthropic-native model through Chat translation (for
example `qwen3.7-plus`) and record the result. Verify `-list-models`,
`-refresh-models` (unconditional fetch and cache replacement), the picker
`Ctrl-R` refresh, `session_start`/`run_start`, cache reuse, and offline resume.
Use `AHARNESS_LOG_REQUESTS=1` to confirm the default request has no
`max_tokens` / `max_output_tokens` field, and exercise one explicit output
budget and one provider length/context-overflow outcome.

## 7. Limits updates

Add or change these rows in [limits.md](../limits.md):

| Limit | New value/behavior |
|---|---|
| Max context tokens | Model `limit.context`; `-max-context-tokens` default becomes 0 (unset); explicit positive values are clamped down to the model ceiling. |
| Max input tokens | `min(model limit.input, context - safety margin)`. |
| Max output tokens | No harness cap by default; `-max-output-tokens` defaults to 0 (unset) and omits the request field. An explicit positive value is sent unchanged; the provider/model ceiling applies. |
| Advertised output ceiling | `model.limit.output`; display/validation metadata only, never a request budget or eligibility gate. |
| Safety margin | Unchanged: `max(5% of context, 1,000)`. |
| Compaction target | Unchanged: effective max input tokens. |
| Catalog URL | `https://models.dev/api.json`; `AHARNESS_MODELS_DEV_URL` overrides for tests/mirrors; HTTPS except loopback HTTP. |
| Catalog request timeout | 15 seconds total. |
| Catalog response body | 16 MiB; larger is a protocol error. |
| Catalog cache TTL | 24 hours. |
| Catalog stale fallback | Allowed only for implicit or `-list-models` refresh failure, with a warning. Explicit refresh never falls back. |
| Explicit catalog refresh | `-refresh-models` or picker `Ctrl-R`; unconditional GET; reuses the 15-second timeout, 16 MiB body cap, and 0600 cache policy; failure exits 3/4 (CLI) or notifies (TUI); no new budget. |
| Catalog cache file | `<state>/cache/models.dev/<provider>.json`; file 0600, directory 0700. |
| Catalog cache version | 3; mismatches are ignored and refetched. Version 2 adds normalized `reasoning` / `reasoning_efforts` from [thinking effort](thinking-effort-build.md); version 3 adds the optional normalized `cost` snapshot from [agent tooling](agent-tooling-build.md). |
| Model ID | At most 256 bytes; valid UTF-8; no control characters. |
| Discovered base URL | HTTPS, host `opencode.ai`, no userinfo/query/fragment. |
| `-provider` | `opencode-go` only; unknown values are config errors. |
| `-list-models` | No separate output cap; bounded by the catalog size. |
| `-refresh-models` | Terminal action; no session, task, or token; bounded by the catalog timeout/body caps. |
| TUI catalog refresh | `Ctrl-R` in the model picker; idle only; no `-ui-json` protocol version change. |

Remove the provisional 200,000 context default and its warning for new
sessions. Existing sessions keep their recorded context/output values and base
URL; new sessions use the catalog. Run-budget defaults and replay semantics are
owned by [limits-removal-build.md](limits-removal-build.md): the turn, token,
and wall-clock budgets are opt-in and unlimited by default.

## 8. Risks and assumptions

- **models.dev has no explicit dialect.** The npm→dialect mapping is the main
  correctness risk. It matches the current opencode-go data, but a future model
  using `@ai-sdk/openai` for Chat or a new SDK will be misclassified. Mitigate
  with the real smoke, the first-party `/models` cross-check, and fail-closed
  unknown handling.
- **Translation is a provider promise.** Anthropic- and Google-native models
  are used through Chat translation. Verify tool calls and streaming with at
  least one translated model; if translation is unreliable, restrict the
  catalog to native Chat/Responses models.
- **The catalog can lag.** models.dev is community-maintained; a new
  opencode-go model may not appear immediately. The first-party endpoint may be
  fresher. The cache can serve stale data for up to the TTL or indefinitely
  after an implicit refresh failure; `-refresh-models` and the picker `Ctrl-R`
  are the immediate re-fetch paths.
- **The base URL changes.** The documented new-session base is
  `.../inference/go/openai/v1`, not the compiled-in `.../inference/go/v1`.
  Existing sessions keep their recorded URL; new sessions use discovery. Update
  the docs and smoke expectations.
- **No default output cap.** Omitting the request field delegates the output
  ceiling to the provider; a provider default may be lower than the model's
  advertised maximum, and a long response consumes the remaining context. The
  harness reports `length` and context-overflow outcomes through the existing
  continuation and compaction-retry paths. Operators who want a fixed budget
  pass `-max-output-tokens`; the harness does not synthesize one.
- **Uncapped output can consume an explicit run budget quickly.** Long
  responses count against the cumulative token budget and wall clock only when
  those flags are set; by default the run is unbounded, so the operator owns
  cost and latency.
- **An explicit cumulative token budget may bind earlier.** A 1M-context model
  can consume a 2M explicit budget in roughly two full-context calls. The
  opt-in default and replay semantics belong to
  [limits-removal-build.md](limits-removal-build.md); acceptance runs should set
  the budget explicitly.
- **Host allowlist maintenance.** If OpenCode changes the inference host, the
  discovered base URL validation must be updated with the docs.
- **Catalog size.** `api.json` was 5.8 MiB on 2026-11-10; the 16 MiB cap leaves
  headroom. A much larger catalog is a protocol error, not a partial parse.
- **Deprecated models disappear.** Explicit selection may start failing when a
  model is removed; the error names the model and provider.

## 9. Decisions and alternatives

| Decision | Choice | Why |
|---|---|---|
| Catalog source | models.dev `api.json` | Requested source; one public request; includes IDs, limits, tools, and status. |
| Dialect source | npm mapping | models.dev v1 has no protocol table; matches the current data. |
| Native dialect vs translation | Responses only for `@ai-sdk/openai`; Chat for everything else | OpenCode documents all text models through the other APIs; the harness has two OpenAI adapters. |
| Unknown SDK | Mark unavailable; error if explicitly selected | Fail closed instead of guessing. |
| Context default | Model `limit.context` | Removes the provisional 200k guess. |
| Output default | None; omit the request field; explicit flag only | A fixed default causes a `length` error and a restart with a flag; delegate to the provider/model and report failures clearly. |
| Input cap | `context - margin`; output not subtracted | No artificial output reservation; a provider overflow drives the existing compaction retry. |
| Base URL | Discovered; explicit override; `opencode.ai` allowlist | Correct documented endpoint and token-exfiltration protection. |
| Cache | 24h + conditional GET + stale fallback | Avoid a 5.8 MiB fetch per run and keep offline use working. |
| Explicit refresh | Unconditional GET with no stale fallback; TUI keeps its loaded entries on failure | An operator asking for freshness must learn when it failed; a silent stale fallback would look like success. |
| Refresh surface | `-refresh-models` flag plus picker `Ctrl-R`, not a subcommand | The CLI is one flag-based command (`-version`, `-list-models`); a subcommand would add a second parsing style and contract for one maintenance action. Revisit only if model operations grow into a command family. |
| Resume behavior | Inherit recorded values; fetch only on explicit model change | Reproducible and offline-safe. |
| Hardcoded registry | Remove | Requested; the catalog is the source of truth. |
| Output-budget dynamics | Only an explicit static value; no per-call calculation | The harness has no default budget to resolve; context/compaction remains the only preemptive limit. |

A later source adapter can use the OpenCode `/inference/go/v1/models` endpoint
without changing `cmd` or the TUI, because the `CatalogSource` interface is the
only seam. That endpoint is the fallback if npm inference proves unreliable.

## 10. Documentation updates

- `README.md`: describe the user-facing discovery behavior; the plan itself is
  indexed in `docs/README.md`, not the README documentation table.
- `docs/architecture.md`: §2 provider/model call behavior (the output field is
  omitted by default; `length` and context-overflow handling), §2.1 wire
  format/base URL, §3.1 package tree and `config.go` comment, §3.2 `cmd`
  mapping, §9 CLI/config (`-max-output-tokens` defaults to 0, `0` clears an
  inherited budget, negative is invalid, and `-refresh-models` terminal-action
  precedence).
- `docs/limits.md`: the single source for the numeric contract — §1 max output
  row (no harness cap; unset omits the field), max input formula without output
  subtraction, advertised output ceiling row, `-max-output-tokens` default 0,
  and the unchanged compaction target; remove the provisional warning.
- `docs/testing-strategy.md`: fake catalog setup, remove "every accepted model
  ID", add cache/offline/list/refresh tests, assert the omitted output field and
  explicit-budget pass-through, update the manual smoke models.
- `docs/tui.md`: picker metadata, advertised-vs-effective output display,
  `Ctrl-R` refresh, model switching, and offline-resume fallback.
- `docs/plans/model-discovery-build.md`: status changes to implemented with the
  milestone/date and any deviations.

`limits.md` owns cap values and formulas; `architecture.md` §9 owns the CLI
flag, default, omission, and replay semantics. The implementation and the
shipped docs now describe discovery; the deferred verification items in §1.4
are recorded at handoff.

## 11. Implementation deviations

- **`CatalogSource.Revalidate`.** The sketch had only `Load` and `Refresh`.
  The shipped interface adds `Revalidate(ctx, provider)` for `-list-models`: it
  bypasses the TTL, sends a conditional GET with `If-None-Match`, and keeps the
  stale-cache fallback. `Load` remains the TTL-honoring path used for implicit
  resolution; `Refresh` remains the unconditional, no-fallback path used by
  `-refresh-models` and the picker `Ctrl-R`.
- **Source split.** Discovery is implemented across
  `internal/model/catalog.go` (types, catalog lookup/usable filtering, cache
  path and the `CatalogSource` contract) and
  `internal/model/catalog_source.go` (models.dev decoding, normalization,
  eligibility, SDK/dialect mapping, HTTP source, conditional GET, cache
  read/write, refresh, stale fallback). The contract and package boundary are
  unchanged.
- **`omitzero` output tags.** The plan assumed the existing `omitempty` tags
  omitted a zero output budget. With `encoding/json/v2` they did not; the first
  smoke sent `"max_tokens": 0`. Both request structs now use `omitzero`, and the
  default request omits the field.
- **Additive event fields.** `session_start` now records `base_url`, and
  `run_start` now records `provider`, so both events carry the effective
  provider/model/dialect/base URL/limits as §1.4 requires. This is an additive
  payload change within v2; the schema version is unchanged and unknown fields
  remain ignored. Old logs without the fields keep their recorded run values or
  the legacy base-URL fallback.
- **`Catalog.Warnings`.** A returned catalog carries non-fatal warnings for
  stale-cache fallback, cache-write failures during implicit discovery, and
  similar recoverable conditions. Batch mode prints them to stderr and the TUI
  surfaces them as notices. The catalog payload and token are never included.
- **Documentation status.** README, architecture, limits, testing strategy, and
  the TUI doc describe the implemented behavior. The §1.4 gate and real-provider
  smoke items remain unchecked here because their results are recorded at
  handoff, not in this plan.
