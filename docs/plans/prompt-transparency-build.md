# aharness prompt-transparency build plan

- **Date:** 2026-11-20
- **Status:** implemented 2026-11-22 on branch `prompt-transparency`. Written to
  be posted on [aharness#5](https://github.com/eddiectc/aharness/issues/5); the
  verification record is in §6, and the index row records the implementation.
- **Source:** the issue "agent: expose LatestModelExchange and
  State.PromptCapped for prompt/request transparency", filed 2026-11-20 from
  acode. Its supporting acode citations could not be reproduced; see §2.5,
  which replaces them with evidence that does hold.
- **Baseline:** `v0.4.1` (tag object `930cedc`, commit `2556236`). `main` =
  `42bd096` is docs-only on top: `git diff v0.4.1..HEAD` touches
  `docs/ci-release.md` and nothing else.
- **Method:** every claim below was checked against the source at `HEAD`, and
  every cited line number was re-derived rather than copied. The rules in
  [AGENTS.md](../../AGENTS.md), [architecture.md](../architecture.md) §3.2.1,
  [coding-standard.md](../coding-standard.md), [limits.md](../limits.md), and
  [definition-of-done.md](../definition-of-done.md) were applied.
- **References:** [architecture.md](../architecture.md) §3.2.1, §6.1, §7;
  [limits.md](../limits.md) §2, §3; [testing-strategy.md](../testing-strategy.md)
  §1; [ci-release.md](../ci-release.md) §2; [CHANGELOG.md](../../CHANGELOG.md).
- **Index entry:** [docs/README.md](../README.md).

## 1. Verdict

The gap is real, both additions are additive, and the shapes the issue proposes
are right. Every code fact it cites checks out (§2.1). The plan accepts both
proposals with four corrections (§10):

1. `PromptCapped` must cross the facade, not stop at `agent.State` (§2.2).
2. The truncation flag must be derived by re-running the cap rule, not by
   matching the marker suffix loosely (§2.4).
3. Fork semantics need to be stated, because a copied parent exchange is what a
   fresh fork reports (§2.6).
4. The acode citations are not verifiable; the argument is rebuilt from acode's
   `AGENTS.md` and its `go.mod` pin (§2.5).

Nothing here changes the session schema, the event set, event order, the
projection, the prompt, tool grants, or the concurrency model. `session.Version`
stays `2` (`session/event.go:16`), and old readers keep working because neither
addition writes anything new to the log.

## 2. Findings that shape the fix

### 2.1 The code claims are exact

| Claim | Verified at `HEAD` |
|---|---|
| `Inspect` keeps no last request or response | `agent/state.go:113-236`: it aggregates counters, grants, and pending calls; no payload is retained |
| `History` drops `model_request` | `agent/history.go:265-268`, with the comment "requests are debug payloads and are not part of the display transcript" |
| Requests are opt-in and capped at 1 MiB | `cfg.LogRequests` gate at `agent/retry.go:258`; `maxModelRequestBytes` at `agent/events.go:290` |
| The recorded prompt is capped at 32 KiB | `maxSystemPromptBytes` (`agent/prompt.go:27`); `systemPromptText` applies one `capText` (`agent/prompt.go:231`) |
| `CapSystemPrompt` reports nothing | `agent/prompt.go:246-248` |
| `session.ReadSnapshot` is the lock-free read | `session/reader.go:61-79` |
| `session.ErrCorrupt` is the corrupt-log wrapper | `session/errors.go:21-22` |
| The three participating event types are exported | `session/event.go:23-26` |

Two traps were checked and cleared, because either would have broken the
proposal:

- **An over-cap request is still a JSON object.** `recordModelRequest` replaces
  an oversized payload with a `modelRequestData` carrying `Truncated: true`
  (`agent/retry.go:274-283`), not with a marker string. The "returned payload
  must be a JSON object" rule therefore rejects nothing the SDK writes.
- **A retry can only sit between the pair.** The request is recorded once,
  before the attempt loop (`agent/retry.go:51-54`), and retries reuse it; the
  response is recorded after `completeOnce` returns. Ignoring `model_retry`
  during both scans is necessary and sufficient.

### 2.2 Two API levels: `PromptCapped` must cross the facade

The issue specifies `agent.State.PromptCapped` only. That leaves facade hosts
blind:

- `agent.State` reaches the facade through `engine.Snapshot.State`
  (`engine/sessions.go:130-136`), built by `engine.Inspect` (`:216`), which
  calls `agent.Inspect`.
- `aharness.State` is a distinct DTO (`result.go:115-153`) filled field by field
  in `stateFromSnapshot` (`result.go:205-285`). A field added to `agent.State`
  does not appear there by itself.

Facade `Agent.State()` (`aharness.go:413`) and `InspectSession`
(`result.go:169-177`) both end in `stateFromSnapshot`, so the facade mirror is a
one-line change plus a doc comment. It
is not optional: [architecture.md §3.2.1](../architecture.md#321-public-library-facade)
calls the root package "the stable API most callers should use", and
[definition-of-done.md](../definition-of-done.md) §3 requires new exported
fields to be documented where embedders see them.

### 2.3 `LatestModelExchange` stays component-level

The facade has no log-reader surface to hang it on:

- Every reader in `docs/architecture.md:601-620` is `agent`-level (`Inspect`,
  `Summarize`, `WriteSessionStart`, `Project`); `agent.History` is likewise
  component-level (`docs/architecture.md:911`).
- The facade's session surface is `InspectSession`/`Sessions`, which return
  metadata and `State`, and `EventView`/`ViewEvent`, which project a live
  in-run `Event` (`docs/architecture.md:683-685`, `:697`, `:908-910`).

A facade host that needs the payload bytes can already reach them: `session` and
`agent` are public packages, and `session.ReadSnapshot` plus
`agent.LatestModelExchange` is exactly the path the issue describes. Adding a
facade DTO would either alias raw bytes across the boundary — which §3.2.1
forbids for component types — or need a decode/normalize layer that the issue
lists as a non-goal. Decision D6 records this; the doc comment must say it
plainly so a facade host does not file the same issue again.

### 2.4 The truncation marker is internal, and the exact test is narrow

`capText` (`agent/events.go:298-312`) cuts at `limit`, then backs the cut down
only until `s[:cut]` is valid UTF-8, then appends
`"\n[truncated %d bytes]"` with the true dropped byte count. Therefore, for any
text it produced:

- the prefix before the marker is exactly `limit`, `limit-1`, `limit-2`, or
  `limit-3` bytes long (a rune is at most 4 bytes);
- the marker's count is `> 0`;
- the prefix is valid UTF-8.

Testing those conditions narrows the issue's accepted false positive from
"any prompt that ends with the marker" to "a hand-written prompt of exactly
32,765–32,768 bytes that ends with a well-formed marker". That is a much weaker
assumption for the same one-line cost, and it needs no schema change.

The pre-existing engine double cap does not break the rule: the engine caps at
`engine/engine.go:578`/`:588` and `systemPromptText` caps again, so the second
pass strips the first marker and rewrites the count while the prefix stays at
32,768 bytes. The flag is still `true`, and the flag is all this plan needs. The
double cap itself is filed separately in
[workspace-instructions-build.md](workspace-instructions-build.md) §8.1 and is
out of scope here.

### 2.5 The acode evidence does not hold; the argument does

Checked against the public acode repository on 2026-11-20:

| Issue cites | Actual |
|---|---|
| `docs/plans/prompt-transparency-build.md`, Phase 0 (D7) | **404.** `docs/plans/` contains only `transcript-folding-build.md` and `tui-build.md` |
| acode is "blocked at v0.4.1" | acode `go.mod` pins `github.com/eddiectc/aharness v0.0.1` |
| acode `AGENTS.md` "must not encode event ordering, request/response pairing, or the prompt-cap rule" | That sentence is a paraphrase. The file says "Do not copy SDK logic into this repository; change the aharness module and update the dependency" and "Do not implement a loop, projection, tool, provider adapter, or session schema here" |

The request is still justified, and by three sentences that do exist in acode's
`AGENTS.md`: SDK logic must not be copied, a projection and session schema must
not be implemented app-side, and the dependency must be pinned to a tagged
release. Pairing rules and a truncation test are SDK logic; a hand-rolled reader
in acode would violate the first two, and the pinned-tag rule is why a release
is part of this plan. The issue should make that argument instead of citing a
file that does not exist, and should drop the `v0.4.1` blocking claim, which
nothing in acode corroborates.

Severity note: nothing in acode's rules stops it from reading the log today
with `session.ReadSnapshot` and matching the marker. That is not a copy of SDK
logic so much as a second copy of a contract, which is exactly the duplication
the prohibition is meant to prevent. The honest claim is "the contract should
have one owner", not "acode cannot proceed". The ask survives; only the
hardness of the dependency softens.

### 2.6 A fork inherits its parent's model events

`Store.CopyHistory` copies every event except `session_start`
(`session/store.go:227-237`), and a fork writes its own `session_start` first
and copies history after (`engine/engine.go:478-486`). Renumbering only changes
`v`, `seq`, `session`, and `ts` (`session/store.go:241-259`); `type`, `turn`,
and `data` are preserved. So on a freshly forked log, before it has run
anything:

- `LatestModelExchange` returns the **parent's** last exchange;
- `Run` counts the parent's `run_start` events;
- the copied events keep the parent's `turn`, and `seq` and `turn` continue
  across runs (`docs/architecture.md:1513`), so the fork's first new turn
  follows them.

That is consistent with the definition ("the newest recorded attempt in this
log"), and it is what a `/prompt` overlay should show for a fork. It must be
documented on the function, because a host rendering "run 3" on a fork that has
never run is otherwise surprising. No code special-cases it: filtering copied
events would need a `resumed_from_seq` watermark and a store read, which the
issue explicitly avoids.

### 2.7 Two specification defects to correct while implementing

1. **`Data == ""` is unreachable through the reader.** `session/reader.go:233`
   rejects a line whose `data` is not valid JSON, so `""` never reaches a
   caller of `Read`/`ReadSnapshot`. The case stays in the test table as
   constructed-events coverage, labeled as such, so nobody assumes the reader
   permits it.
2. **`Run` needs a stated reason for zero.** Zero means "no `run_start` at or
   before the selected event", which happens for a malformed log or an
   extremely old one. It is never synthesized from ordering, and hosts render
   it as unknown.

### 2.8 The raw-payload type is `json.RawMessage`, deliberately

`encoding/json/v2` has no `RawMessage` (verified with `go doc
encoding/json/v2.RawMessage`), and this package already carries raw recorded
JSON as `json.RawMessage`: `HistoryItem.Arguments`/`Details`
(`agent/history.go:45`, `:57`) and `customToolData.Parameters`
(`agent/events.go:61`).

The [coding standard](../coding-standard.md) §6 rule is about encoding,
decoding, and JSONL: use v2 and `jsontext` for those. `ModelExchange` never
encodes or decodes. It validates with `jsontext.Value(...).IsValid()` (v2
rules, the same call the reader uses) and clones bytes. Carrying the result as
`json.RawMessage` is inert and keeps the public DTO consistent with its
neighbors; it does not mix v1 semantics into a decode path.

## 3. Decisions

### D0 — Scope

Two additions, one derived field, and their documentation:

- `agent/exchange.go` with `ModelExchange` and `LatestModelExchange`;
- `agent.State.PromptCapped`, its derivation helper, and the
  `aharness.State.PromptCapped` mirror.

No session-schema bump, no new event type, no new event ordering, no change to
`History`, `log_requests` defaults, caps, `prompt-v3`, tool grants, toolset
version, or the concurrency model.

### D1 — `ModelExchange` shape

```go
// ModelExchange is the latest projected model attempt in one session log: the
// newest recorded model_request or model_response, whichever is later, plus
// its pair when one exists in the same run and turn.
//
// Raw payloads are the recorded event JSON (the SDK's projection), not wire
// bytes: HTTP headers, auth, and exact request/response frames are never
// recorded.
type ModelExchange struct {
	Request  json.RawMessage
	Response json.RawMessage
	// RequestSeq and ResponseSeq are the recorded sequence numbers of the
	// events the payloads came from. Zero means that side is absent.
	RequestSeq  uint64
	ResponseSeq uint64
	// Run is the count of run_start events up to and including the selected
	// event. Zero means the log has no run_start at or before it (malformed or
	// very old); hosts render it as unknown. It is never synthesized from
	// ordering.
	Run int
	// Turn is the envelope Turn of the selected event. A returned pair always
	// shares one Turn.
	Turn int
	// HasRequest and HasResponse report which sides were found. Both are
	// false only for the zero value returned by an empty scan.
	HasRequest  bool
	HasResponse bool
}
```

`Request`/`Response` are nil, not empty, when the side is absent. Sequence
numbers are `uint64` to match `session.Event.Seq`.

### D2 — Pairing is a session-schema contract

Stated once, here, in the form the implementation must follow. Only
`TypeModelRequest`, `TypeModelResponse`, `TypeRunStart`, and `TypeSessionStart`
participate in any decision; every other type (`model_retry`, `user_message`,
`tool_result`, `compaction`, `limit`, `interrupt`, `error`, `session_repair`,
`final`, `run_end`) is skipped by the forward scan and is transparent to the
backward scan.

1. **Select.** Walk the events once. Remember the index of the last event whose
   type is `model_request` or `model_response`, and count `run_start` events at
   or before that index. With no such event, return the zero value and a nil
   error.
2. **Fix the envelope.** `Turn` = the selected event's `Turn`; `Run` = the
   count from step 1.
3. **Selected event is a request.** `Request = clone(Data)`,
   `RequestSeq = Seq`, `HasRequest = true`, response side zero. This is the
   in-flight or terminally failed case. Never fall back to an older completed
   exchange and never synthesize a response.
4. **Selected event is a response.** `Response = clone(Data)`,
   `ResponseSeq = Seq`, `HasResponse = true`. Then walk backward from the
   selected index:
   - the first `model_request` ends the scan: pair it only when its `Turn`
     equals the selected event's `Turn`, then stop either way;
   - an earlier `model_response`, a `run_start`, or a `session_start` ends the
     scan without pairing;
   - anything else is skipped.

   Never scan past the first `model_request`, and never pair across a
   `run_start` or `session_start`.
5. **Copy, never alias.** Use `bytes.Clone`. The caller must be able to mutate
   the result without touching `Event.Data`.
6. **Do not decode the payload structs.** `modelRequestData` and
   `modelResponseData` stay private to the writer. Raw JSON keeps the reader
   robust to field additions and lets a host pretty-print the recorded spelling
   and field order unchanged.

Consequence worth a test: two consecutive requests (a hand-written log) yield
the **later** request paired with nothing, because the backward scan stops at
the first request even when its `Turn` differs.

### D3 — Validation and error contract

Validation runs only on a payload that is returned:

1. non-empty `Data`;
2. `jsontext.Value(Data).IsValid()`;
3. the first non-space byte is `{`.

A failure returns `fmt.Errorf("%w: %s seq %d: payload is not a JSON object",
session.ErrCorrupt, ev.Type, ev.Seq)`. Unselected model events are never
validated; envelope integrity (`v`, `seq`, `session`) remains
`session.reader`'s job. No panic on malformed data, and no error for an empty or
model-event-free log.

### D4 — The reader is a pure function, not a state field

`LatestModelExchange(events []session.Event) (ModelExchange, error)` lives in
`agent/exchange.go`. It is not a `State` field and not a `Runner` method:
`Inspect` runs on every resume and fork and must not grow large payload copies,
while the exchange is an on-demand debug read that a host performs from a
snapshot, possibly while another process appends. A pure function composes
directly with `session.ReadSnapshot` (`session/reader.go:61`).

### D5 — `PromptCapped` is derived by re-running the cap rule

Add an unexported helper beside `capText` in `agent/events.go`, so the marker's
format and its inverse live in one file:

```go
// truncatedBy reports whether text is exactly what capText(text, limit)
// produces when it truncates: a marker suffix naming a positive dropped-byte
// count, over a valid UTF-8 prefix whose length is within one rune of limit.
// The marker format is internal; hosts must never match it themselves.
func truncatedBy(text string, limit int) bool
```

Rules:

- Empty or short text is `false`.
- Parse the suffix `\n[truncated N bytes]`; require `N > 0`.
- Require the prefix length to be in `[limit-3, limit]` and the prefix to be
  valid UTF-8.
- `Inspect`'s `session_start` case calls it with `maxSystemPromptBytes`
  (`agent/state.go:143`, beside the existing `state.SystemPrompt` assignment)
  and sets `State.PromptCapped`.
- A legacy log with an empty `SystemPrompt` is `false`; the host labels that log
  as reconstructed by other means.
- `WriteSessionStart` is unchanged: it keeps recording the capped text, so the
  flag is correct for logs written before it existed.

The helper stays unexported and the format string is not exported. If the
format ever changes, `truncatedBy` changes with `capText` in the same file, and
the pinned test in Phase 2 fails loudly.

### D6 — Facade mirror

Add `aharness.State.PromptCapped bool` beside the prompt fields
(`result.go:115-153`) and set it in `stateFromSnapshot` next to
`SystemPrompt`/`AppendedPrompt` (`result.go:221-222`). Document it as derived,
never a replay key, and false for a legacy log with an empty prompt.

Do **not** add a facade DTO for `ModelExchange` (§2.3). The doc comment on
`LatestModelExchange` and the architecture readers paragraph both say it is a
component-level reader for first-party hosts, and why.

### D7 — Fork behaviour is documented, not special-cased

`LatestModelExchange`'s doc comment gains the §2.6 consequence: on a fork the
newest attempt may be an event copied from the parent, `Run` counts the parent's
runs, and hosts should show the run number as recorded rather than assume it
belongs to the current process. `docs/architecture.md` gets one sentence beside
the fork-copy description.

### D8 — Docs and release

The change is not documentation-light, and `make docs-check` will not catch a
miss: it is a link checker (`Makefile:49-66`), not a content checker. Required:

- `docs/architecture.md` §3.2.1 readers paragraph (`:601-620`): one entry for
  `agent.LatestModelExchange`, and one sentence on `PromptCapped` beside the
  `Inspect` description.
- `docs/architecture.md` §3.2.1 "Session state" row (`:697`): add
  `PromptCapped` to the `State` field list.
- `docs/architecture.md` event table (`:1500`): note that `model_request` and
  `model_response` pairing is readable through `LatestModelExchange`, so hosts
  do not reimplement it.
- `docs/limits.md` (`:176`): extend the Prompt version row — truncation state is
  reported as `State.PromptCapped` and hosts must not match the marker.
- `README.md` (`:156-163`): one sentence for `PromptCapped` beside the
  `State.PromptTag` text.
- `CHANGELOG.md`: **Added** entry for the two types, the function, and both
  `PromptCapped` fields; schema stays `2`, no migration.
- `docs/README.md`: the index row for this plan.
- Cut and publish `v0.5.0` after merge. CI never tags
  ([ci-release.md](../ci-release.md) §2); a pre-1.0 minor may add exports
  ([CHANGELOG.md](../../CHANGELOG.md) preamble). Nothing in this set is breaking.

### D9 — Tests pin contracts, not incidental formatting

The suffix-pinning test in Phase 2 is the one deliberate exception: it must fail
if `capText`'s marker format changes, because `truncatedBy` depends on it. Every
other test asserts behavior (pairing, copies, error identity, the flag), not
byte-for-byte JSON spelling.

## 4. Contract deltas

| Kind | Delta | Compatibility |
|---|---|---|
| Added | `agent.ModelExchange`, `agent.LatestModelExchange` | Additive component API; no log write; usable with `session.ReadSnapshot` |
| Added | `agent.State.PromptCapped`, `aharness.State.PromptCapped` | Derived field; old logs get a correct value; no schema change |
| Added (internal) | `truncatedBy` in `agent/events.go` | Unexported; the marker format stays private |
| Unchanged | `session.Version` `2`, event set and order, `model_request`/`model_response` payloads, `model_retry`, `History`, `log_requests` default, `maxSystemPromptBytes`, `maxModelRequestBytes`, `prompt-v3`, tool grants, toolset version, projection, concurrency model | — |

No existing identifier changes meaning. `State` gains a field, which is
backward compatible for readers and forward compatible for old logs.

## 5. Build plan

### Phase 1 — `agent/exchange.go` and its tests

New file `agent/exchange.go`:

- `ModelExchange` with the D1 doc comment and field docs.
- `LatestModelExchange` with the D1–D4 semantics: one forward pass counting
  `run_start`, the backward scan with exactly three stop conditions, `bytes.Clone`
  on both payloads, `jsontext` validation of returned payloads only, and
  `session.ErrCorrupt` wrapping with `%w`.

Imports: `bytes`, `fmt`, `encoding/json`, `encoding/json/jsontext`,
`session`. No `jsonv2` need arises; nothing is marshaled.

New file `agent/exchange_test.go`, table-driven over constructed
`[]session.Event` (a helper that builds events with explicit `Type`, `Turn`,
`Seq`, and `Data`):

| Case | Expectation |
|---|---|
| Newest response with a matching request | `HasRequest && HasResponse`, `RequestSeq < ResponseSeq`, same `Run` and `Turn`, both payloads equal to the event `Data` |
| Newest response, logging off in that turn | `HasResponse`, `!HasRequest`; an older turn's request is not paired |
| Newest event is a request (in flight) | `HasRequest`, `!HasResponse`; an older completed pair is not returned |
| Newest request whose call failed (request, then `error`, then `run_end`) | `HasRequest`, `!HasResponse`; no synthetic response |
| Retry between request and response | `model_retry` is transparent; the pair is returned |
| Compaction between turns | Selection and run boundary unaffected |
| Resumed run (`run_start` before the new pair) | Pairing does not cross the new `run_start`; `Run` counts it |
| Earlier run recorded a request, later run has logging off | Earlier request is not paired with the later response |
| Request and response with different `Turn` envelopes | Response returned with `HasRequest=false` |
| Two consecutive requests | Later request returned unpaired (`D2` consequence) |
| No model events (empty log; `session_start` only) | Zero value, nil error |
| Malformed selected payload (`null`, a bare string, an array) | Error wrapping `session.ErrCorrupt`; no panic |
| Empty `Data` (constructed event only; the reader rejects it) | Error wrapping `session.ErrCorrupt` |
| Unselected malformed model event | No error; the selected event decides |
| Copy semantics | Mutating the returned `Request`/`Response` leaves `events[i].Data` untouched |
| `Run` counting | 0 without `run_start`; 1/2/3 with 1/2/3 preceding `run_start` events |

`agent/exchange_test.go` uses `errors.Is(err, session.ErrCorrupt)` for the
error rows, never string matching.

### Phase 2 — `State.PromptCapped` and its tests

`agent/events.go`

- Add `truncatedBy` beside `capText` with the D5 doc. It parses the suffix
  backwards, so it allocates nothing beyond the marker parse, and reuses the
  existing marker literal in one place (extract the format into an unexported
  helper if that keeps `capText` and `truncatedBy` honest).

`agent/state.go`

- Add `PromptCapped bool` to `State` after `SystemPrompt` (`:41`) with the doc:
  derived from the recorded prompt against the SDK's cap, false for a legacy
  empty prompt and for a prompt that fits, never a replay key.
- Set it in the `session_start` case (`:143`) from `data.SystemPrompt` via
  `truncatedBy(data.SystemPrompt, maxSystemPromptBytes)`.

`agent/state_test.go`

| Case | Expectation |
|---|---|
| Prompt over the cap written through the real `agent.WriteSessionStart` path | `PromptCapped == true`; the test pins the actual recorded suffix (32,768-byte prefix + `\n[truncated 7232 bytes]` for a 40,000-byte input) so a format change fails loudly |
| Prompt under the cap | `PromptCapped == false` |
| Prompt exactly at the cap | `PromptCapped == false` |
| Legacy log with an empty `SystemPrompt` | `PromptCapped == false` |
| Hand-written prompt ending with a well-formed marker but the wrong prefix length | `PromptCapped == false` (the §2.4 residual-risk boundary) |
| `State.PromptCapped` survives an unrelated later event | true after a following `run_start`/`model_response` |

The over-cap case must use `WriteSessionStart`, not a hand-built payload, so the
test exercises the writer that the field describes.

### Phase 3 — facade mirror

`result.go`

- Add `PromptCapped bool` beside `SystemPrompt`/`AppendedPrompt` (`:115-153`)
  with the facade-level doc: derived, false on logs written before the field or
  with an empty prompt, never a replay key.
- Set it in `stateFromSnapshot` beside `SystemPrompt` (`:221`).

Tests: extend the facade state test (the file that already covers
`State.PromptTag` and `State.AppendedPrompt`) with two cases — a session whose
recorded prompt was truncated through `aharness.New` with an oversized
`SystemPrompt`, and a normal session — asserting `Agent.State().PromptCapped`
and `InspectSession` agree. No new facade method is added.

### Phase 4 — reference docs and release

Apply the D8 list, then:

- `make dod`.
- Move this file's status to `implemented` with the verification record in §6,
  and change the [docs/README.md](../README.md) index row.
- Maintainer cuts `v0.5.0`; reply on the issue with the tag name so acode can
  pin it, per the release request. The reply should also correct the record
  (§2.5): the plan file acode cited does not exist at `main`, and acode pins
  `v0.0.1`, so "blocked at v0.4.1" should be restated.

## 6. Acceptance mapping

Every row of the issue's test table maps to a named case above, plus three rows
the issue does not have:

| Issue row | Plan location |
|---|---|
| Newest response with a matching request | Phase 1 table, row 1 |
| Newest response, logging off | Phase 1 table, row 2 |
| Newest event is a request (in flight) | Phase 1 table, row 3 |
| Failed newest request | Phase 1 table, row 4 |
| Retry between request and response | Phase 1 table, row 5 |
| Compaction between turns | Phase 1 table, row 6 |
| Resumed run | Phase 1 table, row 7 |
| Earlier run request, later run logging off | Phase 1 table, row 8 |
| Different `Turn` envelopes | Phase 1 table, row 9 |
| No model events | Phase 1 table, row 11 |
| Malformed selected payload | Phase 1 table, rows 12–13 |
| Copy semantics | Phase 1 table, row 15 |
| `Run` counting | Phase 1 table, row 16 |
| Prompt over the cap through `WriteSessionStart` | Phase 2 table, row 1 |
| Prompt under the cap | Phase 2 table, row 2 |
| Legacy log with empty `SystemPrompt` | Phase 2 table, row 4 |
| **Added:** facade mirror | Phase 3 |
| **Added:** exact-length false-positive boundary | Phase 2 table, row 5 |
| **Added:** unselected malformed event is ignored | Phase 1 table, row 14 |

**Verification record (2026-11-22).** Implemented on branch
`prompt-transparency`; the working tree was uncommitted when this record was
written, and `main` is protected, so the change lands by pull request.

| Check | Result |
|---|---|
| `make dod` | passed: goimports clean, `go vet ./...`, golangci-lint (0 issues), `go test -race -count=1 ./...`, `go build ./...`, govulncheck (no vulnerabilities), `docs-check` |
| Targeted tests (§7.2) | `go test ./agent -run 'Exchange|PromptCapped|Inspect|TruncatedBy' -count=1` and `go test . -run PromptCapped -count=1` passed |
| Marker single source (§7.3) | `agent/events.go` owns the format as `truncationMarkerOpen`, `truncationMarkerClose`, and `truncationMarker`, the extraction §5 Phase 2 allowed. `grep -rn "truncated .* bytes" --include=*.go` finds only the tests and the unrelated `tools` output markers; there is no host-facing copy of the prompt marker. `TestInspectPromptCapped` pins the recorded 32,768-byte prefix plus `\n[truncated 7232 bytes]`, and `TestTruncatedBy` pins the `[limit-3, limit]` window and the positive-count rule |
| Module files (§7.4) | `go.mod` and `go.sum` unchanged; no dependency added |
| Fork check (§7.5) | `TestLatestModelExchangeOnFork` builds a two-run parent log, forks it the way `engine.openFork` does (`agent.WriteSessionStart` then `session.Store.CopyHistory`), and asserts the fork reports the parent's payloads at its own seqs 7/8 with `Run == 2` |
| Docs spot-check (§7.6) | the architecture readers paragraph and `State` row, the event-table note, the fork bullet's inherited-exchange sentence, the limits prompt row, the README sentence and component table, the CHANGELOG **Added** entries, and the `docs/README.md` index row are present and agree with the `go doc` comments |
| Real-session smoke (§7.7) | not run: it needs an existing acode session log and remains a maintainer check |

No deviation from §3–§10 was needed. The `v0.5.0` tag is not cut here: CI never
tags ([ci-release.md](../ci-release.md) §2), and the release is a maintainer
step after merge.

## 7. Verification

1. `make dod` — docs-check, goimports/gofmt, `go vet`, golangci-lint,
   `go test -race -count=1 ./...`, `go build -trimpath ./...`, govulncheck.
2. Targeted while building: `go test ./agent -run
   'Exchange|PromptCapped|Inspect' -count=1` and `go test . -run 'PromptCapped'`.
3. Format single-source check: the marker literal `[truncated %d bytes]`
   appears once in `agent/events.go`, and `grep -rn "truncated .* bytes"
   --include=*.go` finds no host-facing copy.
4. `go.mod` and `go.sum` are unchanged; no dependency was added.
5. Fork check: build a parent log with one completed exchange, then build a
   forked log the way `engine.openFork` does — `agent.WriteSessionStart`, then
   `session.Store.CopyHistory` (`engine/engine.go:478-486`) — and assert the
   fork's `LatestModelExchange` reports the parent's payloads and the inherited
   `Run` count (§2.6). Constructed events, not a runner test.
6. Docs spot-check, because docs-check is link-only: the architecture readers
   paragraph, the `State` field row, the event-table note, the limits row, the
   README sentence, and the CHANGELOG entry are all present and agree with the
   go doc comments.
7. Real-session smoke (optional, maintainer): read an existing acode session
   with `session.ReadSnapshot` plus `LatestModelExchange`, and confirm the
   pretty-printed payload matches the recorded line for that `seq`.

## 8. Open decisions

1. **Residual false positive.** A hand-written prompt of exactly 32,765–32,768
   bytes ending with a well-formed marker is still flagged. Accepted: the
   alternative is recording a boolean in `session_start`, which is a schema
   change the issue says acode does not need, and which would be wrong for every
   log written before it existed. Impact is a header note in a diagnostic
   overlay.
2. **`Run` on a fork.** Accepted as documented (§2.6), not filtered. A filter
   needs `run_start.resumed_from_seq` compared against a watermark and a store
   read; it also makes "newest attempt" depend on which process is asking, which
   the pure-function design deliberately avoids.
3. **Whether `LatestModelExchange` should ever gain a facade surface.** Not now
   (§2.3). If a second first-party consumer needs it, the smaller move is a
   facade helper that takes a session ID and does the snapshot read internally,
   returning a copied DTO — not exporting raw bytes as an alias.
4. **Retry timeline.** Out of scope; `model_retry` stays readable from events if
   a host wants the history later.

## 9. Non-goals

- No retry timeline: v1 shows only the latest attempt.
- No payload decoding, normalization, redaction, re-marshaling, or display caps:
  hosts format the recorded bytes.
- No exact HTTP wire capture; that would be a separate opt-in adapter tap.
- No change to `History` (requests stay out of the transcript) and no change to
  `log_requests` defaults.
- No session-schema version change, new event type, or new `session_start`
  field.
- No facade event/log reader, and no facade DTO for the exchange.
- No new dependency, no change to caps, prompt version, tool grants, or
  concurrency.
- No fix for the engine double cap (§2.4); it is filed separately.

## 10. Deviations from the issue

1. **`PromptCapped` crosses the facade** (§2.2, D6). The issue stops at
   `agent.State`; the facade copies `State` field by field and would silently
   omit it.
2. **The truncation test is exact, not a suffix match** (§2.4, D5). The flag is
   derived by re-running the cap rule, so a hand-written prompt that merely ends
   with marker-like text is not flagged unless it also has the exact length
   window and a positive count.
3. **Fork semantics are documented** (§2.6, D7) rather than left to the reader.
   No code special-case is added.
4. **The acode evidence is corrected** (§2.5). The cited plan does not exist at
   `main`, acode pins `v0.0.1`, and the `AGENTS.md` quote is a paraphrase; the
   justification is rebuilt from the sentences that do exist, and the release
   request stands on the pinned-tag rule.
5. **Documentation obligations are larger than doc comments** (D8). The DoD
   requires the current reference and README, not only `go doc`; docs-check will
   not catch a miss.
6. **`Data == ""` is labeled constructed-only** (§2.7) because the reader
   rejects invalid JSON before a caller sees it.
7. **`json.RawMessage` is kept and justified** (§2.8) against the v2 rule, which
   governs encode/decode and JSONL, not an inert raw carrier.
8. **Two extra tests** are added beyond the issue's table: the two-consecutive-
   requests case (D2 consequence) and the wrong-length marker boundary (§2.4).

## 11. Evidence index

- Issue: [aharness#5](https://github.com/eddiectc/aharness/issues/5).
- Baseline: `git describe` = `v0.4.1-3-g42bd096`; `git diff --stat
  v0.4.1..HEAD` = `docs/ci-release.md` only.
- Code: `agent/state.go:23`, `:41`, `:113-236`, `:143`; `agent/history.go:45`,
  `:57`, `:265-268`; `agent/retry.go:51-54`, `:257-285`, `:274-283`;
  `agent/events.go:61`, `:143`, `:290`, `:298-312`; `agent/prompt.go:27`,
  `:219-231`, `:246-248`; `session/event.go:16`, `:23-26`;
  `session/errors.go:21-22`; `session/reader.go:61-79`, `:233`;
  `session/store.go:227-237`, `:241-259`; `engine/sessions.go:130-136`, `:140`,
  `:216`; `engine/engine.go:478-486`, `:578`, `:588`, `:971`; `result.go:115-153`,
  `:205-285`, `:221-222`; `aharness.go:90-96`, `:413`.
- Docs: `docs/architecture.md:601-620`, `:683-685`, `:697`, `:908-910`, `:911`,
  `:1490-1501`, `:1513`; `docs/limits.md:80`, `:176`; `Makefile:49-66`;
  [workspace-instructions-build.md](workspace-instructions-build.md) §8.1.
- External: `github.com/eddiectc/acode` at `main` on 2026-11-20 — `AGENTS.md`
  (SDK-logic prohibition, app-side projection/schema prohibition,
  pinned-tag rule), `go.mod` (`aharness v0.0.1`), `docs/plans/` (two files;
  `prompt-transparency-build.md` absent).
- Toolchain: `go1.27.4`; `go doc encoding/json/v2.RawMessage` reports no such
  symbol; `jsontext.Value` is the v2 raw type.
