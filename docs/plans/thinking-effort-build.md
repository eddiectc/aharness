# aharness thinking effort build plan

- **Date:** 2026-11-11
- **Status:** implemented 2026-11-11. This document is the decision and
  verification record; the shipped behavior is documented in
  [architecture.md](../architecture.md), [tui.md](../tui.md),
  [testing-strategy.md](../testing-strategy.md), [limits.md](../limits.md), and
  the README. The real-provider smoke in §1.4 and §4.3 was **not** run because
  provider credentials are unavailable; it remains an explicit release
  prerequisite (§9).
- **Source:** [model discovery build plan](model-discovery-build.md), the live
  models.dev `opencode-go` catalog, and the OpenAI Chat Completions and
  Responses reasoning parameters.
- **Provider references:** [OpenAI reasoning guide](https://developers.openai.com/api/docs/guides/reasoning),
  [OpenCode models and variants](https://opencode.ai/docs/models/), and the
  [models.dev catalog](https://models.dev).
- **Target:** Go 1.27.3, module `aharness`.
- **Architecture:** [architecture.md](../architecture.md) §2.1–2.3 (provider,
  canonical model, streaming), §9 (CLI and configuration), and §10 (defaults).
- **Testing:** [testing-strategy.md](../testing-strategy.md) plus §5 here.
- **Limits:** [limits.md](../limits.md). This plan adds catalog-bounded values and
  no run budget.
- **Definition of done:** [project definition of done](../definition-of-done.md);
  feature criteria in §1.4.

## Decision summary

- Add `-thinking-effort` and `AHARNESS_THINKING_EFFORT`. The public name is
  **thinking effort**; the provider and catalog call the same control
  **reasoning effort**.
- The accepted values are model-specific and discovered from models.dev's
  `reasoning_options` entry of type `effort`. There is no harness-wide enum
  beyond the values the selected model advertises.
- Omitting the setting sends no field and therefore uses the provider/model
  default. The explicit value `default` clears an inherited setting.
- Add one canonical request field, `Request.ThinkingEffort`. The Chat adapter
  sends top-level `reasoning_effort`; the Responses adapter sends
  `reasoning: {"effort": "..."}`. Both buffered and streaming paths use the
  same payload builder, so they cannot diverge.
- Record the effective value in `session_start` and every `run_start`. Resume
  and fork inherit the latest recorded value unless the flag/environment
  explicitly overrides it. No v2 schema version bump.
- Discover and record `reasoning` and the allowed `effort` values per catalog
  model. Bump the catalog cache version so an old cache cannot silently hide
  the new capability until its 24-hour TTL expires.
- Add a TUI effort picker on `F2` for the next run, show the value in the
  details overlay, and show supported efforts in the model picker rows.
  `-ui-json` accepts `f2` and exposes the effective value in existing events;
  it adds no new event type and no protocol version bump.
- Add an `EFFORT` column to `-list-models`.
- Scope is deliberately effort only. Toggle-only and token-budget-only
  reasoning controls, reasoning-text display, reasoning summaries, and
  adaptive thinking are out of scope.
- No new runtime dependency and no change to the loop, prompt, tools,
  projection, compaction, retry policy, or token accounting.

## 1. Scope

### 1.1 What this plan owns

| Area | Implementation |
|---|---|
| CLI | `-thinking-effort` flag and `AHARNESS_THINKING_EFFORT` with flag > env > recorded precedence. |
| Validation | Per-model membership in the catalog's `effort` values; `default` clears; invalid values are config errors with exit 2. |
| Canonical request | `model.Request.ThinkingEffort`, copied through the agent's request construction. |
| Chat Completions | Top-level `reasoning_effort` in buffered and streaming payloads. |
| Responses | Nested `reasoning.effort` in buffered and streaming payloads. |
| Catalog | Parse `reasoning` and `reasoning_options`; normalize, validate, cache, and expose per-model effort values. |
| Session log | Add `thinking_effort` to `session_start` and `run_start`; reconstruct it for resume/fork; log it in `model_request` when request logging is on. |
| Model changes | Preserve a supported inherited effort; clear an unsupported inherited effort with a warning; reject an explicit conflict. |
| TUI | `F2` picker for the next run, details-overlay display, model-picker effort metadata, and model-switch behavior. |
| `-ui-json` | Accept `f2`; add optional `thinking_effort` fields to existing output events. |
| Listing | `-list-models` prints an `EFFORT` column. |
| Documentation | Architecture, TUI doc, testing strategy, limits/caps note, README, and this plan. |

### 1.2 What stays the same

- The canonical response type, finish reasons, usage, streaming assembly, and
  transcript projection. Reasoning text, reasoning summaries, and encrypted
  reasoning items remain adapter-dropped unless a future loop feature consumes
  them.
- The loop, system prompt, four tools, workspace containment, projection,
  compaction, retry policy, stuck detection, and stop statuses.
- One provider (`opencode-go`), the two OpenAI dialect adapters, and the
  catalog/base-URL rules.
- The v2 session envelope and event types. The new fields are additive and old
  logs decode with an empty value.
- The absence of a default model, default output cap, and default run budgets.
- `-max-output-tokens` semantics. Reasoning tokens are output tokens and need
  no separate budget.
- Provider auth, the `x-opencode-session` header, request caps, and error
  classification.
- No new runtime dependency.

### 1.3 Not in this plan

- Toggle-only reasoning controls (`reasoning_options: [{"type":"toggle"}]`).
  Turning reasoning on or off is not an effort level.
- Token-budget thinking controls (`reasoning_options` with `budget_tokens`).
  The harness does not invent a token budget from an effort name.
- A harness-wide `off`/`on` switch for models that expose no effort values.
- Reasoning/thinking text display, reasoning summaries, or replay of reasoning
  items across tool rounds. *(Superseded: the shipped TUI shows a display-only
  thinking block, and both adapters now retain and replay provider-native
  continuation material within a tool round. Durable reasoning is still out of
  scope; see [architecture §2.4](../architecture.md#24-continuation-for-a-new-dialect).)*
- Automatic effort selection, model-based selection, cost-based selection, or
  a default effort different from the provider default.
- Native Anthropic Messages or Google generate-content adapters. Those models
  continue through the documented Chat translation.
- Per-turn or per-tool effort changes. One effective value applies to every
  model call in a run.
- A v2 schema version bump, a new durable event type, or a new `-ui-json`
  protocol version.
- Price/cost accounting for additional reasoning tokens.

### 1.4 Definition of done

In addition to the [project definition of done](../definition-of-done.md), the
feature is complete when every item below holds.

- [x] The catalog parses `reasoning` and `reasoning_options`, retains only
      `effort` values, deduplicates and orders them deterministically, and
      rejects malformed or oversized values. The normalized cache is version 2;
      a version 1 cache is treated as absent and refetched.
- [x] `-thinking-effort` and `AHARNESS_THINKING_EFFORT` work with the documented
      precedence. `default` clears; an empty flag value is invalid; a malformed
      or oversized value is invalid.
- [x] A new session validates the explicit value against the selected model. A
      value the model does not advertise, or any non-default value on a model
      with no effort control, exits 2 with a message listing the model's
      advertised values or saying it advertises none.
- [x] With no setting, neither adapter sends a reasoning field. With a setting,
      Chat sends `reasoning_effort` and Responses sends `reasoning.effort` in
      both buffered and streamed requests.
- [x] `session_start` and `run_start` record `thinking_effort`; resume inherits
      the latest `run_start` value; fork records the fork's effective value; old
      v2 logs without the field decode and resume with the provider default.
- [x] Resume/fork without an explicit effort change keeps working without a
      catalog fetch. An explicit non-default effort requires a catalog
      resolution for the effective model and fails clearly if unavailable.
- [x] A model change keeps a supported inherited effort. An unsupported
      inherited effort is cleared with a warning and recorded as empty for the
      new run. An explicit effort plus an explicit incompatible model is a
      config error.
- [x] `Ctrl-X` shows `thinking effort`; `F2` opens the next-run picker while
      idle and is disabled with a notice during a run; model rows show
      supported effort values; a model switch that clears effort shows a
      notice.
- [x] `-ui-json` accepts `f2`, and `model_changed`/`run_started` expose the
      effective value as an optional field. Existing protocol version 1
      clients continue to parse the frames.
- [x] `-list-models` prints an `EFFORT` column; models with no effort control
      print `-`.
- [x] Unit, adapter, integration, TUI, fuzz, race, build, lint, and
      vulnerability gates pass. Documentation is updated.
      **Verified 2026-11-11:** `goimports -l .`, `go vet ./...`,
      `golangci-lint run ./...` (0 issues), `go test -race -count=1 ./...`,
      `go build ./...`, `govulncheck ./...` (0 called vulnerabilities), and
      short fuzz runs for all five fuzz targets passed. The documentation
      updates in §8 are complete.
- [ ] A real-provider smoke proves at least one effort value through each
      native SDK path used by the selected models (`@ai-sdk/openai-compatible`,
      `@ai-sdk/openai`, `@ai-sdk/anthropic`, `@ai-sdk/google`). If a translated
      path rejects the OpenAI-compatible field, the plan's mapping is corrected
      or the affected models do not advertise effort in this harness. This
      requires provider credentials and is not claimed by mock tests.
      **Not run:** `AHARNESS_PROVIDER_TOKEN` is unset in the implementation
      environment. This is an explicit release prerequisite (§9).

## 2. Current state

**Historical:** this section records the state before implementation and is
kept for rationale. §9 records the shipped state and deviations.

The model discovery implementation parses a deliberately small models.dev
subset. The relevant gaps:

- `modelsDevModel` reads `id`, `name`, `type`, `tool_call`, `status`,
  `provider`, `limit`, and `modalities.output`. It does not read `reasoning` or
  `reasoning_options`.
- `CatalogModel` has no reasoning field. The normalized cache therefore cannot
  expose effort values to the CLI or TUI.
- `model.Request` carries only `Model`, `Messages`, `Tools`, and
  `MaxOutputTokens`. Neither adapter has a reasoning field in its wire request.
- `sessionStartData` and `runStartData` do not record an effort, and
  `agent.State` does not reconstruct one.
- `-list-models` prints `ID`, `NAME`, `DIALECT`, `CONTEXT`, `OUTPUT`, and
  `STATUS`; the TUI model picker shows dialect, context, input, output, status,
  and translated markers, but no effort.
- The details overlay has no effort row, and there is no TUI control for it.

The live models.dev `opencode-go` catalog on 2026-11-11 lists 40 models. Every
entry reports `reasoning: true`, but the controls differ:

| Observed `reasoning_options` | Example models | This plan |
|---|---|---|
| `effort` with `low`, `high`, `max` | `deepseek-v4.1-flash`, `glm-5.3` | Supported. |
| `effort` with `minimal` through `xhigh` | `muse-spark-1.3-contributor` | Supported. |
| `effort` including `none` | `gpt-6-luna`, `hy3` | Supported; `none` is an advertised value, not synthesized. |
| `toggle` only | `longcat-2.0`, `minimax-m3` | Not supported by this plan. |
| `budget_tokens` only or with a toggle | `qwen3.7-plus` | Not supported by this plan. |
| empty array | `kimi-k2.7-code`, `mimo-v2.5` | No effort control; reasoning is model-fixed. |

The provider API is OpenAI-compatible. The Chat Completions endpoint accepts
top-level `reasoning_effort`; the Responses endpoint uses the standard nested
`reasoning.effort` object. The harness must not send either field when the
operator did not request one, because non-reasoning models and models without
an effort control may reject unknown parameters.

## 3. Contract

### 3.1 CLI and precedence

| Flag | Environment | Default | Behavior |
|---|---|---|---|
| `-thinking-effort` | `AHARNESS_THINKING_EFFORT` | unset | A model-advertised effort value, or `default` to clear. |

Resolution, highest first:

1. Explicit flag.
2. Environment variable.
3. For resume/fork only: the latest recorded `run_start.thinking_effort`, then
   `session_start.thinking_effort`.
4. Unset: provider/model default; omit the wire field.

Rules:

- `default` is a reserved clear value, never a catalog effort level. It is
  accepted on new sessions, resume, fork, and TUI selection.
- An explicitly empty flag value (`-thinking-effort=`) is a config error;
  use `default` to clear. An empty environment value is treated as unset.
- A non-default value must be non-empty, at most 32 bytes, valid UTF-8, and
  match `[a-z][a-z0-9_-]*`. The syntax check runs before catalog resolution so
  a typo or control character never reaches the request or the log.
- Catalog membership is checked against the effective model after model
  resolution. The message lists the model's values, for example:
  `thinking effort "medium" is not supported by model "deepseek-v4.1-flash"; supported: high, max`.
  A model with no effort control reports that it advertises no effort levels;
  toggle and budget controls are not aliases for effort.
- `-list-models` and `-refresh-models` ignore this setting, matching the other
  session-only flags they ignore.
- Unknown values exit 2 (config). A provider that rejects a value that the
  catalog advertised is a terminal `provider_error`; the harness does not retry
  without the field or silently fall back.

```sh
# Use the provider's maximum advertised effort for this model.
aharness -model deepseek-v4.1-flash -thinking-effort max

# Clear an inherited effort on resume and use the provider default.
aharness -batch -resume <session-id> -thinking-effort default
```

### 3.2 Resolution and inheritance

New session:

1. Resolve the model through the catalog.
2. If the setting is non-default, validate it against that model's effort
   values.
3. Record the effective value (possibly empty) in `session_start`.

Resume/fork:

| Situation | Behavior |
|---|---|
| No explicit effort, no model change | Inherit the latest recorded value. Do not fetch the catalog; the recorded value is authoritative. |
| No explicit effort, explicit model change, inherited value supported | Keep the value. |
| No explicit effort, explicit model change, inherited value unsupported | Clear to provider default and warn on stderr (batch) or as a TUI notice. |
| Explicit `default` | Clear; no catalog fetch is required. |
| Explicit non-default effort, same or changed model | Load the catalog, resolve the effective model, and validate. Failure is exit 2. |
| Explicit effort and explicit model, incompatible | Exit 2; the user asked for both and one cannot be honored. |

The explicit-effort-on-resume rule is the only new catalog dependency. A fresh
cache satisfies it offline. This is consistent with the existing rule that an
explicit model change requires catalog metadata; an explicit capability value
does the same.

Fork copies the parent's events, then writes a new `session_start` with the
fork's effective value. The copied parent events keep their historical values.

### 3.3 Canonical request and wire mapping

Add one field:

```go
type Request struct {
	Model           string
	Messages        []Message
	Tools           []ToolSchema
	MaxOutputTokens int
	ThinkingEffort  string
}
```

Mapping:

| Canonical | Chat Completions | Responses | Empty |
|---|---|---|---|
| `ThinkingEffort` | `reasoning_effort` at the top level | `reasoning: {"effort": "<value>"}` | Field omitted in both dialects. |

- Both adapters use their existing `payload` builder for buffered and streaming
  requests, so one mapping covers both paths.
- The value is sent unchanged; the adapter does not translate names between
  providers.
- The response shape is unchanged. Reasoning text, reasoning summaries, and
  reasoning-token details remain ignored; output usage already counts reasoning
  tokens as output tokens.
- Tool-call rounds reuse the same run configuration, so every model call in a
  run carries the same effort.
- Request logging (`AHARNESS_LOG_REQUESTS`) adds `thinking_effort` to the
  `model_request` payload, including the truncated-payload variant.
- A 400/422 caused by the effort value is a normal typed provider error. The
  retry policy does not special-case it.

### 3.4 Session log

Add `thinking_effort,omitempty` to `sessionStartData` and `runStartData`.

- The value is the effective string after resolution; empty means provider
  default.
- `agent.State` gains `ThinkingEffort`. `session_start` sets it; each
  `run_start` replaces it. The latest `run_start` is therefore the resume
  default, matching model/dialect inheritance.
- Old v2 logs have no field and reconstruct as empty. New fields are ignored by
  old readers, as the v2 envelope already requires.
- No schema version bump, no new event type, and no change to prompt/toolset
  versions. The prompt and toolset are unchanged by this feature.
- `RunStarted` gains the effective value for display/protocol use. It is
  display metadata, not transcript content.

### 3.5 Model switching

The current effort is model-specific. Model changes follow one rule set:

1. If the current effort is empty, the new model uses its provider default.
2. If the current effort is advertised by the new model, keep it.
3. If the current effort is not advertised:
   - An inherited/current value is cleared with a warning and the new run
     records empty. This applies to batch model changes and TUI picker
     selections.
   - An effort and model explicitly resolved together at process start are a
     config error, because the operator requested a combination the model
     cannot honor. A later TUI picker change is interactive rather than a
     resolution-time conflict, so it clears with a notice instead.
4. `default` always means empty; it never conflicts with a model change.

Clearing is visible: batch writes one `aharness: warning:` line to stderr, the
TUI shows a notice, and the next `run_start` records the cleared value. The
model picker's rows show supported efforts so the outcome is predictable before
selection.

### 3.6 TUI and `-ui-json`

- `F2` opens a "thinking effort for the next run" picker while idle. It is
  currently unused (`F1` is help), so it does not collide with the composer or
  overlay keys. It lists `provider default` first, then the effective model's
  advertised values in canonical order.
- If the effective model advertises no effort values, `F2` shows a notice and
  opens no overlay. During a run, it shows the same disabled notice as the
  model picker rather than opening.
- Selecting a value updates the next run only; the active run and existing log
  are untouched. The selection is recorded in the next `run_start`.
- The details overlay (`Ctrl-X`) adds `thinking effort: <value>` or
  `thinking effort: provider default`.
- Model picker rows add `effort low/high/max` (or `effort -`). The offline
  recorded-model fallback shows the model's recorded value in details and
  cannot switch models.
- `-ui-json` accepts `f2` in the `key` command. `model_changed` gains an
  optional `thinking_effort` field reflecting the post-selection value, and
  `run_started` gains the same optional field; an omitted field means the
  provider default. An `F2` selection emits a `notice` with
  `kind: "thinking_effort"`. No new event type is introduced, and the protocol
  version stays 1; existing clients ignore the added fields.
- The help overlay and key table document `F2`.

### 3.7 Catalog normalization

`CatalogModel` gains:

```go
Reasoning        bool     `json:"reasoning"`
ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
```

Parsing and normalization:

- Read `reasoning` and `reasoning_options` (an array of objects with `type`,
  `values`, and `max`).
- Keep `reasoning` so an explicit effort on a non-reasoning model can say the
  model does not support reasoning rather than that it advertises no effort
  levels.
- Keep only options where `type == "effort"`. `toggle`, `budget_tokens`, and
  unknown types are retained in the raw decode but not normalized into the
  harness contract.
- Flatten, trim, deduplicate, and validate values with the same rules as the
  CLI: non-empty, valid UTF-8, no control characters, at most 32 bytes each,
  and matching `[a-z][a-z0-9_-]*`; at most 16 values per model. Drop the
  reserved word `default` so the clear sentinel can never collide with a
  catalog level. Invalid values are dropped. A catalog with no valid effort
  values yields an empty list; it does not make the model unusable.
- Order values by the known canonical rank `none`, `minimal`, `low`, `medium`,
  `high`, `xhigh`, `max`, then unknown values lexicographically. This makes the
  CLI, picker, and tests deterministic.
- Bump `catalogCacheVersion` from 1 to 2. A version 1 cache is rejected and
  refetched; no migration is needed because the cache is not durable truth.
- Extend `validUsableCachedModel` so a usable cached entry's effort values must
  satisfy the same length/count/syntax rules. A tampered or malformed cache is
  discarded rather than trusted.
- The raw catalog remains untrusted: values are sanitized for terminal display
  and never concatenated into a request without membership validation.

### 3.8 `-list-models`

Add an `EFFORT` column at the end:

```text
ID	NAME	DIALECT	CONTEXT	OUTPUT	STATUS	EFFORT
```

- Print comma-separated normalized values, or `-` when the model advertises no
  effort values.
- Values are sanitized and capped for one tab-separated column.
- This is a documented output change. Consumers that read the first six columns
  by position keep working; consumers that require exactly six columns must be
  updated.

## 4. Implementation

### 4.1 File map

| Area | Files | Change |
|---|---|---|
| Catalog types | `internal/model/catalog.go` | Add `Reasoning`/`ReasoningEfforts`; bump cache version. |
| Catalog parser | `internal/model/catalog_source.go` | Decode `reasoning_options`; normalize, validate, cap, sort, cache-validate. |
| Canonical request | `internal/model/types.go` | Add `ThinkingEffort`. |
| Chat adapter | `internal/model/openai_chat.go` | Add `reasoning_effort` to `chatRequest`; copy in `payload`. |
| Responses adapter | `internal/model/openai_responses.go` | Add `reasoning` object to `responsesRequest`; copy in `payload`. |
| Agent config | `internal/agent/types.go` | Add `Config.ThinkingEffort`. |
| Agent events/state | `internal/agent/events.go`, `state.go`, `runner.go` | Add session/run fields, state reconstruction, request construction, `RunStarted`. |
| Request log | `internal/agent/retry.go` | Add `thinking_effort` to both normal and truncated payloads. |
| CLI config | `cmd/aharness/config.go` | Flag, env, validation, precedence, usage text. |
| Model resolution | `cmd/aharness/models.go` | Validate against the catalog; apply inheritance/clear rules; list column. |
| Runner wiring | `cmd/aharness/run.go` | Copy `ThinkingEffort` into `agent.Config`. |
| TUI host | `cmd/aharness/tui.go` | Mutate the next run's effort; details; picker metadata; warnings. |
| TUI model | `internal/tui/app.go`, `model.go`, `view.go`, `widgets.go` | `F2`, effort overlay, details row, model-picker metadata, model-switch refresh. |
| `-ui-json` | `internal/tui/protocol.go`, `headless.go` | Accept `f2`; emit optional fields and the notice. |
| Docs/tests | `docs/*`, `README.md`, `*_test.go` | Contract, tests, examples. |

### 4.2 Milestones

| Milestone | Work | Acceptance |
|---|---|---|
| **M0 — Catalog and request** | Add catalog fields and parsing, cache version 2, canonical request field, both adapter mappings. | Unit and adapter tests prove normalization, cache rejection/refetch, exact Chat/Responses bodies, and omission when empty. No behavior changes outside the adapters yet. |
| **M1 — CLI, session, batch** | Flag/env parsing, model validation, inheritance, session/run recording, state reconstruction, request logging, `-list-models`. | A batch fake-provider run sends the requested value; omitted sends nothing; unsupported values exit 2; resume/fork inherit and override correctly; old logs decode. |
| **M2 — TUI and `-ui-json`** | `F2` picker, details, model-picker metadata, switching rules, protocol key/fields, notices. | Headless tests drive `f2`, assert the next run's request and `run_start`, and prove model switches preserve/clear with the documented notice. |
| **M3 — Hardening and docs** | Fuzz seeds, caps, manual smoke, README/architecture/testing/TUI docs, §8 gates. | Every §1.4 item except the real-provider smoke is checked; the smoke is recorded with credentials or remains an explicit release prerequisite. |

### 4.3 Real-provider verification

The catalog's effort values are provider metadata, but the harness talks to the
OpenAI-compatible endpoint directly. Before claiming full support, send a small
tool-call round trip with one advertised effort value through each native SDK
path:

| Path | Example model | Wire dialect |
|---|---|---|
| `@ai-sdk/openai-compatible` | `deepseek-v4.1-flash` | Chat Completions |
| `@ai-sdk/openai` | `muse-spark-1.3-contributor` | Responses |
| `@ai-sdk/anthropic` (translated) | `claude-haiku-5-5` | Chat Completions |
| `@ai-sdk/google` (translated) | `gemini-4-argon` | Chat Completions |

If a translated path rejects `reasoning_effort`, the fix is adapter-side
mapping for that dialect path, not a loop branch or a raw provider field in the
canonical model. If no correct OpenAI-compatible mapping exists, the affected
models must not offer effort in this harness even if the catalog lists it; that
requires an explicit, documented exclusion rather than a silent drop.

**Status:** this smoke has not been run. `AHARNESS_PROVIDER_TOKEN` is unset in
the implementation environment, so no hosted request was made. It is an
explicit release prerequisite; see §9.

## 5. Testing

| Layer | Cases |
|---|---|
| Catalog unit | Effort-only, multiple effort options, toggle, budget, empty, unknown type; duplicate and invalid values; case/length/control-character rejection; canonical ordering; 16-value cap; cache round trip; v1 cache rejection; tampered cache rejection. |
| Adapter unit | Chat body has top-level `reasoning_effort` and no nested `reasoning`; Responses body has `reasoning.effort` and no top-level field; empty sends neither; buffered and streaming; tool-call round trip uses the same value on each call; existing unknown-member tolerance unchanged. |
| Config unit | Flag/env precedence; explicit `default`; empty flag rejected; invalid syntax; unsupported model value; model with no effort control; explicit conflict; inherited supported/unsupported on model change; terminal actions ignore the setting. |
| Session unit | `session_start`/`run_start` fields; latest run wins; old log without the field; fork records the fork's value; malformed known payload still errors; unknown field still ignored. |
| Agent unit | Request construction copies `ThinkingEffort`; request logging includes it; no change to usage/finish handling; retry preserves the same request. |
| Integration | Fake catalog with per-model efforts and fake provider asserting request bodies for new session, resume, fork, model change, and clear. Exit 2 for unsupported combinations. `-list-models` column. |
| TUI/headless | `F2` opens/closes; disabled during a run; default + values ordering; no-effort notice; details row; model-picker description; model switch keeps/clears with notice; `-ui-json` `f2`, `model_changed`, `run_started`, and `notice` fields; deterministic frames/snapshots. |
| Fuzz | Catalog payloads with arbitrary `reasoning_options` shapes and values; no panic, no unbounded allocation, deterministic normalization; existing fuzz targets extended. |
| Manual | Real-provider smoke from §4.3. Record exact model, effort, dialect, and whether the provider honored or rejected it. |

Tests use the existing fakes, `t.TempDir`, and `testing/synctest`; no real
network in the suite.

## 6. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Catalog effort values drift or add unknown option types. | Parse defensively, ignore unknown types, validate values, and never send a value the selected model does not advertise. A future value flows through without a harness release as long as it is syntactically valid. |
| A translated Anthropic/Google model rejects the OpenAI-compatible field. | §4.3 smoke per SDK path; fix the mapping or exclude the affected models explicitly. Do not silently drop the requested effort. |
| The provider ignores the field without an error. | The harness cannot detect silent provider behavior. Request-body tests prove what was sent; the plan does not claim the provider honored it beyond the documented parameter. |
| Sending the field to a model without an effort control breaks the request. | Fail closed at config resolution; omitted means no field; never synthesize values from toggle/budget controls. |
| Resume with an explicit effort requires the catalog. | Document it; a cached catalog satisfies it offline; `default` and no-flag resume remain offline. |
| A model switch silently changes effort. | Keep only advertised values; clear with a visible warning/notice and record the cleared value. Explicit conflicts are errors. |
| Cache version bump forces one refetch. | Expected, bounded, and preferable to a stale capability set; the cache is not durable truth. |
| `-list-models` consumers parse exact columns. | Add the column at the end and document the change; the first six columns are unchanged. |
| Session compatibility. | Additive `omitempty` fields only; no v2 bump; old logs decode empty; old readers ignore unknown fields. |
| TUI key/protocol compatibility. | `F2` is unused; `-ui-json` adds an accepted key and optional event fields, not a new event type or version. |
| Effort increases output tokens and cost. | No default output cap is introduced. The operator controls effort and the existing output budget; cost tracking remains out of scope. |

## 7. Decisions and alternatives

| Decision | Rationale | Cost if reversed |
|---|---|---|
| Public name `-thinking-effort` / `AHARNESS_THINKING_EFFORT`; wire name `reasoning_effort`. | Matches the operator-facing concept while using the provider's standard parameter. One name in the CLI and UI avoids a synonym flag. | Renaming a public flag, env var, session field, and TUI label. |
| Catalog-driven per-model values, not a fixed enum. | The live catalog already varies from `high,max` to `none..max`; a global enum would reject valid values or send invalid ones. It also follows model discovery. | Session validation, listing, and picker semantics change. |
| Unset means omit; `default` explicitly clears. | Preserves the provider/model default and gives resume a way to override a recorded value without inventing an effort. | The empty value would need different precedence semantics, and scripts would need another sentinel. |
| Only `effort` controls are supported. | Toggle and budget controls do not map to one OpenAI-compatible `reasoning_effort` field. Mapping them would be provider-specific guessing. | A later feature can add typed controls without changing this field. |
| No reasoning-text display or replay. | The canonical model stays lossless only where the loop consumes data; current loop behavior does not need reasoning text. | A future feature needs a typed canonical field and possibly continuation persistence. |
| No v2 schema bump; cache version bump only. | The session fields are additive and optional; the cache is disposable derived data. | A schema migration would be needed only if old readers could not ignore the fields, which the v2 contract already requires. |
| TUI `F2` picker, model-picker metadata, details row. | Gives the default front end parity with the CLI without overloading the model picker or composer keys. | A different key or a picker-in-picker interaction. |
| Inherited unsupported effort clears with a warning; explicit conflict errors. | Model switches remain usable interactively, while scripted combinations fail closed instead of silently ignoring an explicit request. | The policy becomes stricter (reject model switches) or looser (silently clear explicit values). |
| No retry/fallback without the field on a provider rejection. | Silently changing a requested reasoning level is worse than a typed error. | A fallback would make behavior non-deterministic and hide capability drift. |

## 8. Documentation

When implemented, update:

- `docs/architecture.md`: canonical request field, adapter mapping, CLI/config
  table, session payloads, and the rule that reasoning text stays
  adapter-owned.
- `docs/tui.md`: `F2`, details row, model-picker effort metadata, model-switch
  clearing behavior, and `-ui-json` key/field additions.
- `docs/testing-strategy.md`: catalog, adapter, integration, and headless
  cases.
- `docs/limits.md` only if the catalog value caps are recorded there; no run
  budget changes.
- `README.md`: feature bullet, example, and documentation-table row for this
  plan.
- This plan: mark implemented and record deviations, following the
  model-discovery and limits-removal plans.

## 9. Implementation status and verification

**Status:** implemented 2026-11-11. The shipped behavior is documented in
[architecture.md](../architecture.md) §2.2 and §7–§9, [tui.md](../tui.md),
[testing-strategy.md](../testing-strategy.md), [limits.md](../limits.md), and the
[README](../../README.md).

Deviations and clarifications against the contract above:

| Area | Shipped behavior |
|---|---|
| Catalog decoding | Only `reasoning_options` entries with `type: "effort"` contribute values; `toggle`, token-budget, and unknown types are ignored. A malformed `reasoning_options` payload yields an empty effort list rather than failing the catalog. The raw `reasoning` flag is retained. |
| Cache validation | Version 2 validation additionally requires unique values in strictly canonical order, on top of the 32-byte, 16-value, and syntax rules. A tampered or misordered usable entry invalidates the cache, which is refetched. |
| Inheritance without a fetch | An inherited effort is validated against the catalog only when the effective model is resolved (an explicit model change). A resume/fork that changes no model trusts the recorded value without a fetch, as §3.2 specifies. |
| Clear sentinel | `default` is converted to the empty value immediately after the syntax check, so it never reaches catalog validation, an adapter request, or the session log. |
| TUI and `-ui-json` | `F2`, the details row, model-picker effort metadata, the model-switch clearing notice, the `f2` key, and the optional `thinking_effort` fields follow §3.6. The header line additionally shows `effort <value>` (or `effort default`) for the next run, so the effective value stays visible without opening the details overlay. No new event type; protocol version stays 1. |
| Live catalog | Values remain catalog-driven. On 2026-11-11 the catalog advertised different lists per model (for example `low,high,max` for `deepseek-v4.1-flash` and other sets including `none`), so the shipped docs do not hardcode a list. Catalog drift changes `-list-models` and picker output without a harness release; the mechanism does not depend on any observed entry. |
| Thinking display | Added after this plan shipped, and it supersedes the §1.3 exclusion of reasoning text display. The adapters now map provider thinking into the display-only `model.Response.Reasoning` field and the TUI renders a separate `thinking` block. Thinking is still never logged or projected; both adapters now keep a bounded in-memory continuation and replay the provider's native material within a tool round. Durable reasoning remains out of scope. See [tui.md § Thinking](../tui.md#thinking) and [architecture.md §2.2/§2.4](../architecture.md#22-canonical-model-and-dialect-adapters). |

Verification:

- Automated coverage: catalog normalization/cache, adapter request bodies,
  config resolution, session reconstruction, and agent request construction
  cases are present in the working tree (see
  [testing-strategy.md](../testing-strategy.md) §1–§2). The TUI/headless `f2`
  cases are listed there; `internal/tui` tests cover the picker ordering,
  disabled/no-effort notices, details row, model-switch clearing, and the
  `thinking_effort` notice kind.
- Gates: `goimports -l .`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race -count=1 ./...`, `go build ./...`, and `govulncheck ./...`
  pass. Short `-fuzztime=2-3s` runs of `FuzzCatalogDecoder`, `FuzzSSEDecoder`,
  `FuzzChatStream`, `FuzzResponsesStream`, and `FuzzHistory` also pass; these
  are smoke runs, not long-duration fuzzing.
- Real-provider smoke: **not run**. `AHARNESS_PROVIDER_TOKEN` is unset in the
  implementation environment, so no hosted request was made and no provider
  acceptance is claimed. Mock/fake-provider tests prove what the adapters send,
  not that the hosted endpoint honored the value. Before release, run the §4.3
  matrix, record the exact model, effort, dialect, and result, and either
  correct a translated mapping or explicitly exclude the affected models from
  advertising effort.
- No new runtime dependency, and no change to the loop, prompt, tools,
  projection, compaction, retry policy, token accounting, or session schema
  version.
