# aharness workspace-instructions build plan

- **Date:** 2026-11-19
- **Status:** implemented and released 2026-11-19 as `v0.4.0`. Phases 1–4
  landed; `make dod` passes. The fresh-acode session check in §7.4 was **not
  run** (acode is a separate repository). §8.1's double cap is untouched and
  remains unfiled.
- **Shape (owner decision, A):** delete `agent.Config.AppendSystemPrompt` instead
  of fixing it. `Config.Instructions` stays the facade's only append-shaped
  input; the `agent` layer gains `AppendedPrompt`, which records the host's text
  and never appends anything.
- **Source:** the workspace-instructions issue filed from acode on 2026-11-19.
  It lives in the filer's gitignored `.issues/` directory, so it is named here
  rather than linked.
- **Baseline:** `v0.3.0` (`55d33a2`), prompt-v3, session schema `2`
- **Method:** every claim below was checked against the source at `HEAD`; the
  rules in [AGENTS.md](../../AGENTS.md),
  [architecture.md](../architecture.md) §3.2.1,
  [coding-standard.md](../coding-standard.md), and
  [definition-of-done.md](../definition-of-done.md) were applied. The cited
  session log was read directly.
- **References:** [architecture.md](../architecture.md) §3.2.1, §4.3, §6.4;
  [limits.md](../limits.md); [testing-strategy.md](../testing-strategy.md);
  [prompt-surface-build.md](prompt-surface-build.md).
- **Index entry:** [docs/README.md](../README.md).

## 1. Verdict

The issue is right about the bug and right that the fix belongs in the SDK. Its
*mechanism* was one field too many, and the owner chose the smaller cut: there
is one way to append at the facade, none at the agent layer, and one new field
that only records what the host contributed.

Confirmed at `HEAD`:

1. **Fresh children lose the host text.** `childConfig`
   (`agent/spawn.go:751-784`) composes `childSystemPrompt` and carries nothing
   from the parent; `continuedConfig` (`agent/spawn.go:786-824`) reuses the
   child's recorded prompt, so only a continued child is correct — and every
   child a parent starts is fresh.
2. **The facade loses the bytes before the spawner exists.**
   `aharness.Config.Instructions` becomes `engine.Spec.AppendPrompt`
   (`aharness.go:216-219`); `resolvedNewPrompt` folds it into the prompt text
   (`engine/engine.go:655-669`); `Open` records that folded text as
   `agent.Config.SystemPrompt` (`engine/engine.go:306-328`); `runConfig` copies
   `SystemPrompt` and nothing else (`engine/runner.go:156-190`).
3. **Resume and fork keep no separate copy.** Resume rejects an append
   (`engine/engine.go:351-353`); fork appends to the recorded text
   (`:563-580`) and clears the inputs (`:601-604`). A new child of a resumed or
   forked parent therefore has no source for the bytes.
4. **The evidence is exact.** The cited log records a **2,636-byte**
   `prompt-v3` prompt in workspace `/home/tc/sr.ht/acode` with exactly one
   mention of `AGENTS.md` (the v3 rule line) and no workspace text; `v=2`. The
   cited 3,483-byte `AGENTS.md` cannot be re-checked: that workspace no longer
   exists.
5. **The line references hold.** All 17 references in the issue's §7 are within
   ±4 lines of the symbol they name, and `docs/plans/prompt-surface-build.md:737-743`
   (D7) says exactly what the issue's §2.3 quotes.

## 2. Findings that shape the fix

### 2.1 The dead field is engine-dead, not universally dead

`agent.Config.AppendSystemPrompt` (`agent/types.go:99-102`) has exactly one
reader, `systemPromptText` (`agent/prompt.go:223-225`), and **no engine or
spawner path sets it**: `engine.Open`, `engine.openFork`, and
`Spawner.childConfig` all leave it empty, and `aharness.Config.Instructions`
reaches the prompt by folding instead (`engine/engine.go:655-669`).

It is still reachable for a *component* host that builds `agent.Config` itself
and calls the public `agent.WriteSessionStart` (`docs/architecture.md:606-611`)
or `Runner.StartSession` (`agent/runner.go:109-114`). So "delete it" is a
breaking change for that host, not a no-op — see D2 for the migration, which is
byte-identical to today's output.

No test and no current reference document mentions the field; the only
appearance outside the two code sites is the dated plan
`docs/plans/go-library-build.md:1976`, which is frozen and does not override the
reference (`docs/README.md` precedence).

### 2.2 Deleting it removes the fork double-append hazard entirely

The earlier split design needed two agent fields because a fork hands
`WriteSessionStart` a config whose `SystemPrompt` is the *inherited* prompt
(`engine/engine.go:448-471`), which already contains the parent's text; any
append applied at write time would duplicate it.

With no append at the agent layer there is nothing to apply twice. The fork
records the text as provenance; the prompt bytes are whatever the fork resolved.
Ask 1 item 4 is satisfied by construction rather than by a rule.

### 2.3 A child records nothing

If a child recorded `appended_prompt`, its log would claim an append already
baked into its `system_prompt`; a later continuation reuses that prompt
(`agent/spawn.go:808`) and would double the text. Delegation depth is one
(`doc.go:100`), so nothing consumes a child's value.

Rule: a child composes the bytes into `system_prompt` and records no
`appended_prompt` — the same rule a delegated child already follows for
`prompt_tag` (`docs/architecture.md:1076-1078`).

### 2.4 "A host cannot compensate" is over-stated

The issue says a host cannot fix this itself because the child prompt is built
inside the SDK and `AgentDefinition.Prompt` is per role. But the host builds the
catalog, so it could append workspace text to every definition's `Prompt`
before constructing (`aharness.DelegationConfig.Agents`). Undesirable and
duplicative, but possible. The honest claim is "a host should not have to
reimplement propagation per role". This weakens only the severity argument, not
the ask.

### 2.5 A pre-existing double cap, unchanged by this plan

The engine caps the prompt twice: `resolvedNewPrompt`
(`engine/engine.go:658`, `:667`) and again in `systemPromptText` when
`session_start` is written (`agent/prompt.go:226`). `capText` keeps the head and
appends `[truncated N bytes]` (`agent/events.go:296-309`), so the second pass
cuts into the first marker.

Verified with a 40,000-byte input at the real 32 KiB cap: first pass 32,791
bytes ending `[truncated 7232 bytes]`; second pass 32,789 bytes ending
`[truncated 23 bytes]` — the true loss is 7,232 and the recorded count is the
marker's own length.

Independent of this issue and untouched by the deletion, but the acceptance row
"Append over the prompt cap → one truncation marker" hits it if the test drives
`engine.Open`. Phase 1 pins that case at the `agent` level, where there is
exactly one cap; §8 files the engine defect separately.

## 3. Decisions

### D0 — Scope

Record the host's appended text once, propagate it to every fresh child, carry
it across resume and fork, and inherit it in children of resumed and forked
parents. No session-schema bump (`session.Version` stays `2`,
`session/event.go:16`), no new prompt version, no change to role bodies, the
catalog, the 200-byte description cap (`agent/agents.go:23`), or the tool
surface.

### D1 — One new field: the record

Add `session_start.appended_prompt`, `omitempty`, beside `system_prompt`
(`agent/events.go:38`), mirroring `prompt_tag`'s additive shape. Exposed as
`agent.State.AppendedPrompt` (`agent/state.go:23`) and `aharness.State.AppendedPrompt`
(`result.go:115`, filled in `stateFromSnapshot` at `:216-217`: the snapshot is
built by `agent.Inspect`, `engine/sessions.go:215-220`).

It is provenance, never a replay key:

- empty for every log written before the field, and for every delegated child;
- the projection ignores it — `projectPrompt` keeps using `system_prompt`
  (`agent/context.go:385-394`);
- old readers ignore it: event decoding does not set
  `RejectUnknownMembers` (`agent/context.go:410-415`; only tool arguments do,
  `tools/registry.go:245`), so a rollback neither fails nor corrupts.

### D2 — Delete `agent.Config.AppendSystemPrompt`

Remove the field (`agent/types.go:99-102`) and its only use
(`agent/prompt.go:223-225`). `systemPromptText` keeps its current contract:
explicit `SystemPrompt` verbatim, otherwise render the configured version, then
one `capText`.

After removal the agent layer has exactly one prompt input (`SystemPrompt`) and
one provenance value (`AppendedPrompt`). No name in the package differs from
another by a tense.

**Breaking for component hosts. Migration** (byte-identical output): fold the
text into the prompt before the call.

```go
cfg.SystemPrompt += "\n\n" + hostText
cfg.SystemPrompt = agent.CapSystemPrompt(cfg.SystemPrompt) // optional; the write caps once
```

Today's path is `text + append` then one cap; the migrated path is
`text + append` then one cap. Same bytes.

### D3 — One way to compose at the facade: `Instructions`

`aharness.Config.Instructions` keeps its name, its `ConfigError` mutual
exclusion with `SystemPrompt` (`aharness.go:166-168`), its resume rejection
(`:169-171`), and its fold (`engine/engine.go:655-669`). `engine.Spec.AppendPrompt`
does not change. The facade/component naming difference is already recorded as
deliberate (`docs/architecture.md:506-519`); the plan adds the missing crosswalk
row rather than renaming anything.

### D4 — Where the engine carries the bytes

`engine.Host` gains an unexported `appendedPrompt string`, returned by
`resolveInherited` beside the prompt and set in each opener:

| Path | Value |
|---|---|
| `Open` (new) | `spec.AppendPrompt` when `AppendPromptSet` |
| `openResume` | `state.AppendedPrompt` |
| `openFork`, inheriting the prompt | `state.AppendedPrompt` + `"\n\n" + spec.AppendPrompt` when the fork supplies one |
| `openFork`, explicit `SystemPrompt` | `""` |

`Open` and `openFork` also set `cfg.AppendedPrompt` on the `agent.Config` they
hand `agent.WriteSessionStart` (`engine/engine.go:307-327`, `:448-471`).

No new exported `Spec` field and **no new `SpawnerOptions` field**: `runConfig`
sets `AppendedPrompt: h.appendedPrompt` on the run config
(`engine/runner.go:156-190`), and `childConfig` already reads `s.opts.Config`.
`Spec.Agent` (`engine/engine.go:88`) is not a second composition path —
`resolvedNewPrompt` has no agent branch — so `childConfig` stays the only place
a role body is built.

### D5 — Config field semantics (what the one new field means)

`agent.Config.AppendedPrompt` is documented as one thing with two uses:

> Bytes the host contributed to the prompt in this config, recorded as
> `session_start.appended_prompt` when this config starts a session. It is
> record-only: the caller has already composed the text into `SystemPrompt`; the
> package never appends it. A running session's config carries it so delegated
> children inherit the text, and a child's own config leaves it empty.

`WriteSessionStart` records `capText(cfg.AppendedPrompt, maxSystemPromptBytes)`.

### D6 — Fork rule

A fork that inherits the recorded prompt keeps that text unchanged when it
supplies no `Instructions`, and appends to it when it does — the existing
behaviour at `engine/engine.go:563-580`, untouched. Either way it records the
union (D4), which is the prompt's tail when nothing was truncated. A fork with an
explicit `SystemPrompt` records `""` — an explicit prompt is the host's whole
contract, matching the existing exclusion (issue open decision 2, recommendation
adopted). A fork whose parent log predates the field records `""`; the bytes are
not recoverable from a prompt, and behaviour equals today.

### D7 — Cap and precedence

Composition order is unchanged: body or inherited prompt, append last, one
`capText` at 32 KiB (`agent/prompt.go:211-227`). A child's body is identity,
shared rules, role body, then the carried text, and the child's write applies the
single cap — so an over-cap append is what the marker cuts.

The recorded field is capped independently at the same 32 KiB and is **not**
guaranteed to be a byte suffix of `system_prompt` once truncation applies; docs
must call it provenance, not a diff. A host that needs a rule to survive an
over-cap prompt puts it at the front of its own text.

### D8 — Documentation and ownership

State in `doc.go`, `README.md`, `docs/architecture.md` §4.3, and
`docs/limits.md` that:

- `Config.Instructions` reaches the parent's recorded prompt, every fresh
  child's prompt, and children of resumed and forked sessions; a continued child
  keeps it through its recorded prompt;
- the SDK does not discover workspace files — the host reads the file and
  supplies the bytes (issue §2.3, `prompt-surface-build.md` D7, and
  `AGENTS.md:29-35` agree);
- a delegated child records neither `appended_prompt` nor `prompt_tag`;
- the `agent` layer does not append; `AppendSystemPrompt` is gone.

## 4. Contract deltas

| Kind | Delta | Compatibility |
|---|---|---|
| Added | `session_start.appended_prompt` (optional), `agent.Config.AppendedPrompt`, `agent.State.AppendedPrompt`, `aharness.State.AppendedPrompt` | Additive; schema stays `2`; ignored by old readers; empty on old logs |
| Removed (breaking) | `agent.Config.AppendSystemPrompt` | Component-only removal; nothing in the engine, facade, spawner, or test suite sets it; CHANGELOG **Removed** + byte-identical migration (D2) |
| Changed | `Config.Instructions` now reaches every fresh child and survives resume and fork | Behaviour change on an existing knob; CHANGELOG **Changed** + migration: root-only text moves to a composed body (`agent.LoopRules` + host lines) |
| Changed | A fork with `Instructions` records the union as `appended_prompt` | Fork `system_prompt` bytes unchanged |
| Unchanged | Prompt template, `prompt-v3`, role bodies, catalog, caps, tool grants, toolset version, `session.Version`, replay, `prompt_tag`, `SpawnerOptions` | — |

## 5. Build plan

### Phase 1 — `agent` layer

`agent/types.go`

- Delete `AppendSystemPrompt` and its doc. Add `AppendedPrompt` with the D5 doc
  after `SystemPrompt`.

`agent/prompt.go`

- Delete the append block at `:223-225`; leave the rest of `systemPromptText`
  and its doc intact.
- `childSystemPrompt` (`:184-206`) gains an `append string` parameter and emits
  `"\n\n" + append` after the role body when non-empty.

`agent/events.go`

- Add `AppendedPrompt string \`json:"appended_prompt,omitempty"\`` to
  `sessionStartData` beside `SystemPrompt`.

`agent/runner.go`

- `WriteSessionStart` records `capText(cfg.AppendedPrompt, maxSystemPromptBytes)`.

`agent/state.go`

- Add `State.AppendedPrompt` (doc: recorded field, empty on old logs and on
  children, never a replay key) and copy it in the `session_start` case
  (`:130-140`) beside `PromptTag` and `SystemPrompt`.

`agent/spawn.go`

- `childConfig`: `SystemPrompt: childSystemPrompt(def, parent.Workspace,
  parent.Identity, promptVersion(parent), parent.AppendedPrompt)`. It sets no
  append field on the child config.
- `continuedConfig`: one comment stating why nothing is appended (the recorded
  prompt already holds the bytes).

Tests: `agent/prompt_test.go` (child append position after the role body;
over-cap append yields exactly one marker with the role body intact; empty
append byte-identical to today), `agent/spawn_test.go` (fresh child's
`system_prompt` contains the text once and the child records no
`appended_prompt`; a continued child's prompt equals its recorded prompt),
`agent/runner_test.go` (recorded JSON gains the field only when set; a config
with an explicit `SystemPrompt` records no append), `agent/state_test.go`
(`Inspect` exposes it; a helper-built `session_start` without it reads empty).

### Phase 2 — `engine` layer

`engine/engine.go`

- `Host.appendedPrompt string`; `resolveInherited` returns the carried value
  per D4/D6; `Open`, `openResume`, and `openFork` store it; `Open` and
  `openFork` set `cfg.AppendedPrompt`. No change to caps, `promptChanged`
  (`:436`), or `contract_override` logic.

`engine/runner.go`

- `runConfig` sets `AppendedPrompt: h.appendedPrompt`.

Tests: `engine/engine_test.go` — new parent with `Instructions` (recorded field
equals the text); resume (carried, `appended_prompt` not rewritten, nothing
re-recorded); fork inheriting the prompt (prompt byte-identical, field equals
the tail); fork with `Instructions` (field equals the union); fork with an
explicit `SystemPrompt` (field empty); a hand-built log without the field
(resume and fork behave as today).

### Phase 3 — facade

`aharness.go`

- Extend the `Config.Instructions` doc (`:79-81`): appended for the parent and
  for every fresh child, recorded once as `session_start.appended_prompt`,
  inherited by children of a resumed or forked session, and not discovered by
  the SDK — a host that wants workspace instructions in children reads the file
  and passes it here.

`result.go`

- Add `State.AppendedPrompt` and copy it in `stateFromSnapshot` (`:216-217`),
  matching `SystemPrompt`/`PromptTag`.

`doc.go`

- The paragraph at `:76-77` gains the child, resume, and fork rule plus the
  ownership sentence, and drops any implication that the `agent` layer appends.

Tests: extend `TestIdentityParentAndChildPrompts`
(`prompt_composition_test.go:12`), which already collects the child session ID,
to assert with `aharness.InspectSession` that the text appears once after the
role body; a companion case without `Instructions` asserts the child prompt is
byte-identical to today's golden.

### Phase 4 — reference docs and release

- `docs/architecture.md` §4.3 (`:1036-1135`): the appended-text paragraph after
  the `Config.Instructions` sentence at `:1125`, the child rule beside the
  child-prompt text at `:1046-1052`, and the "the SDK does not discover
  workspace files" sentence.
- `docs/architecture.md` component crosswalk (`:510-519`): one row —
  `Config.Instructions` ↔ `engine.Spec.AppendPrompt` ↔
  `agent.Config.AppendedPrompt` ↔ recorded `session_start.appended_prompt`;
  never a replay key; carried to fresh children; the `agent` layer never appends.
- `docs/architecture.md` `session_start` payload row (`:1470`): the optional
  field, like `prompt_tag`.
- `docs/limits.md` (`:176-178`): a Prompt append row — recorded under the 32 KiB
  prompt cap, optional, carried to fresh children, never a replay key, not
  necessarily a suffix of `system_prompt` after truncation.
- `CHANGELOG.md`: **Added** (`appended_prompt`, `State.AppendedPrompt`),
  **Removed** (`agent.Config.AppendSystemPrompt` + migration), **Changed**
  (`Instructions` now reaches children and survives resume/fork + migration).
- `README.md:161-163`: one sentence for the child and resume/fork rule.
- If adopted: move this file to `docs/plans/workspace-instructions-build.md`
  and add the `docs/README.md` index row.

## 6. Acceptance mapping

| Issue §4 row | Test |
|---|---|
| Unit: parent with an append, fresh child | `agent/spawn_test.go`: child `system_prompt` contains the text once after the role body |
| Unit: continued child | `agent/spawn_test.go`: equals its recorded prompt, no duplicate |
| Unit: resumed parent, fresh child | `engine/engine_test.go`: host carries `State.AppendedPrompt` with no `Instructions` supplied |
| Unit: forked parent, fresh child | `engine/engine_test.go`: inherited prompt unchanged, child gets the bytes |
| Unit: append over the cap | `agent/prompt_test.go`: one marker, role body intact (single `capText`) |
| Facade: `Instructions` with a `delegate` definition | `prompt_composition_test.go` extension, child inspected by session ID |
| Replay: log before the field | `agent/state_test.go` + `engine/engine_test.go`: decodes, resumes, forks, no append, `session.Version == 2` |
| Docs | `doc.go`, `docs/architecture.md`, `docs/limits.md`, `README.md`, `CHANGELOG.md`; `make docs-check` |

## 7. Verification

1. `make dod` (docs-check, goimports, vet, golangci-lint, `go test -race
   -count=1`, build, govulncheck).
2. Targeted while building: `go test ./agent ./engine -run
   'Prompt|Spawn|State|Fork|Resume' -count=1`.
3. `grep -rn AppendSystemPrompt` returns only this plan and the issue.
4. Fresh acode session with `Instructions` set: the parent records the same
   prompt bytes as today plus `appended_prompt`; one delegate call records the
   child prompt with the text once, after the role body, and no
   `appended_prompt` on the child.

Verification note, 2026-11-19: `make dod` passed. Item 3 also returns the frozen
`docs/plans/go-library-build.md:1976` (recorded in §2.1) and the CHANGELOG
migration note; no live code path references the removed field. Item 4 was not
run because acode is a separate repository.

## 8. Open decisions

1. **Double cap (§2.5):** out of scope here; file it separately. The minimal fix
   is a single fold-and-cap point (let `systemPromptText` own the cap and drop
   the one in `resolvedNewPrompt`), which then requires `promptChanged`
   (`engine/engine.go:436`) to compare capped values. Touching `capText` instead
   would mean reserving marker space across 39 call sites.
2. **Backfill on resume:** should a host be able to supply bytes for a log
   written before the field? Recommended no — it needs a new `Spec` input and a
   rule for a prompt that already contains the text. Deferred.

## 9. Non-goals

- No SDK-side `AGENTS.md` discovery, nested hierarchy, `CLAUDE.md`
  compatibility, memory/RAG, or summarization.
- No `prompt-v4`; the v3 read rule remains the fallback for hosts that do not
  inject.
- No re-reading the file mid-session: the bytes are fixed at `session_start`,
  exactly like `prompt_tag`.
- No change to role bodies, the catalog, the description cap, tool grants, or
  delegation depth.
- No new dependency; no schema bump; no new `SpawnerOptions` field.

## 10. Deviations from the issue

1. **Delete the append mechanism rather than split it** (D2). The issue assumed
   `AppendSystemPrompt` ships as a live input; it is engine-dead, and keeping it
   beside `AppendedPrompt` would leave a permanent live/dead pair.
2. **Children record nothing** (§2.3, Phase 1); the issue's "copy it in
   `childConfig`" would record an append on every child and double the text on a
   later continuation.
3. **"A host cannot compensate" is corrected** (§2.4); the ask stands, the
   severity argument is weaker.
4. **A pre-existing double cap is surfaced** (§2.5) and filed separately.
5. **No new spawner option:** the bytes ride `agent.Config.AppendedPrompt`
   through `runConfig` (D4), keeping `SpawnerOptions` unchanged.
6. **D2's migration snippet gained a rendered-prompt branch.** Appending to an
   empty `SystemPrompt` makes it non-empty and therefore suppresses rendering,
   so `cfg.SystemPrompt += "\n\n" + hostText` alone is byte-identical only when
   the config already carried an explicit prompt. The CHANGELOG directs a
   component host that relied on the rendered path to call
   `agent.RenderSystemPrompt` first.

## 11. Evidence index

- Issue: `ISSUE-aharness-workspace-instructions.md`.
- Session log: `/home/tc/.acode/sessions/01a1f489-4824-74c3-bfc5-600a81b1ccad.jsonl`
  — `session_start` prompt 2,636 bytes, `prompt-v3`, `v=2`, workspace
  `/home/tc/sr.ht/acode`, one `AGENTS.md` mention.
- Code: `agent/prompt.go:27`, `:64`, `:184-206`, `:211-227` (delete `:223-225`);
  `agent/types.go:95-102`; `agent/events.go:38`, `:296-309`;
  `agent/state.go:23`, `:130-140`; `agent/spawn.go:61-84`, `:677`, `:751-784`,
  `:786-824`, `:808`; `agent/runner.go:52-93`, `:109-114`;
  `agent/context.go:385-394`, `:410-415`; `agent/agents.go:23`;
  `engine/engine.go:88`, `:164-172`, `:306-328`, `:351-353`, `:436`,
  `:448-471`, `:563-580`, `:601-604`, `:655-669`, `:673-675`;
  `engine/runner.go:156-190`; `engine/sessions.go:215-220`;
  `aharness.go:79-81`, `:166-171`, `:216-219`; `result.go:115`, `:200-233`;
  `doc.go:76-77`, `:100`; `session/event.go:16`; `tools/registry.go:245`.
- Docs: [architecture.md](../architecture.md) `:506-519`, `:606-611`,
  `:1046-1052`, `:1061-1062`, `:1076-1078`, `:1125-1129`, `:1470`;
  [limits.md](../limits.md) `:176-178`;
  [prompt-surface-build.md](prompt-surface-build.md) `:737-743`;
  [go-library-build.md](go-library-build.md) `:1976`.
- Plan format and precedence: [docs/README.md](../README.md),
  [definition-of-done.md](../definition-of-done.md).
