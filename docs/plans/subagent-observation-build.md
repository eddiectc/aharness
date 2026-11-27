# aharness subagent observation and transcript build plan

- **Date:** 2026-11-23
- **Status:** implemented 2026-11-23 and released 2026-11-25 as `v0.6.0`.
  Phases 1–4 landed as written except where §5 records a deviation; the
  automated gates pass and the real-provider observation smoke in §7 is
  pending.
- **Maintained during implementation:** this is a living work record, not a
  reference document. Update it as each phase lands — status, decisions,
  deviations, and §7 verification — and correct any claim the code outgrows.
  The current reference documents stay the authority for shipped behavior; this
  plan never becomes that authority, and it is never protected from editing.
- **Revision:** 2026-11-23, first draft; 2026-11-23, implemented
  (owner decisions O1 and O2 taken as recommended, three recorded deviations in
  §5).
- **Baseline:** `v0.5.0` (`26e3e2a`, 2026-11-22). Session schema `2`,
  `prompt-v3`, `toolset-v2`.
- **Source:** a host reported that a delegated child exposes a narrower
  observation contract than the root session, and asked for one merged live
  view of every child. The supporting audit is `aharness-host-surface-issues.md`
  issue 6 (root-only events), in the filer's gitignored `.issues/` directory, so
  it is named here rather than linked. This plan supersedes it for the child
  half.
- **Target:** Go 1.27.3, module `aharness`.
- **Architecture:** [architecture.md](../architecture.md) §3.2.1 (facade),
  §3.3 (host observation), §4 (loop), §7 (session log).
- **Owner decisions:** §8 O1 (sink error semantics) and O2 (sink
  concurrency). Both recommendations were adopted on 2026-11-23; the recorded
  contract follows §D3 and §D4.

## 1. Verdict

The gap is real and it is an API gap, not a rendering preference. A child
session exposes counters; the root exposes a transcript. The SDK does not
currently offer any way to receive a child's text, thinking, tool results, or
notices while that child runs.

This plan closes it with **one new opt-in hook and one reader**, and it changes
no event type, no session schema, no prompt, no tool grant, and no concurrency
model:

1. `DelegationConfig.ObserveChild` registers a **per-child display sink** that
   receives the same display events the root run's own stream would deliver for
   that session.
2. Children stream (`Config.Stream = true`) **only when a sink is registered**,
   so an unobserved child keeps today's retry behaviour and cost.
3. Facade `Transcript(ctx, stateDir, sessionID)` and `Agent.Transcript()` read
   the durable transcript of **any** session — root, child, or fork — without
   taking the session lock.

The host owns merging, layout, and rendering. The SDK owes per-child ordering
and correct session identity, and nothing more.

## 2. Findings that shape the fix

Each claim below was checked in the `v0.5.0` tree. File and symbol are named
because line numbers move.

### 2.1 The child adapter drops five event kinds

`agent/spawn.go`, `(*childDisplay).display` is a three-case switch:

| Event | Child adapter | Root `Display` |
|---|---|---|
| `ResponseCompleted` | accumulates, then `SubagentProgress` at most 1/s | delivered |
| `ToolStarted` | records `d.lastTool` only; emits nothing | delivered with the full call |
| `MessageRecorded` | re-emitted as `SubagentMessageRecorded` | delivered |
| `RunStarted` | dropped | delivered |
| `ModelStarted` | dropped | delivered |
| `TextDelta` | **dropped** | delivered |
| `ReasoningDelta` | **dropped** | delivered |
| `ToolCompleted` | **dropped** | delivered with output, exit code, `Details` |
| `Notice` | **dropped** | delivered with `NoticeKind` |
| `RunFinished` | dropped | delivered |

The throttle is a response-boundary gate, not a ticker:
`if now.Sub(d.lastEmit) < time.Second { return nil }`. There is no progress
event during a long model call or a long tool call.

### 2.2 The delta events carry no session identity

```go
// agent/display.go
type TextDelta struct { Text string }
type ReasoningDelta struct { Text string }
```

There is no `Session`, `CallID`, or `Agent` field. Every event that *is*
forwarded to the root display from a child (`SubagentStarted`,
`SubagentProgress`, `SubagentFinished`, `SubagentMessageRecorded`) carries
`CallID`/`Index`/`Agent`/`Session` precisely because identity had to be added
there.

This is the finding that determines the design. Forwarding a child
`TextDelta` onto the root stream would produce an event indistinguishable from
root text. The cheap fix is therefore **not** a new field on every delta; it is
**one sink per child**, where identity is structural and cannot be lost.

### 2.3 Children never stream, so there is nothing to forward

`agent/spawn.go`, `childConfig` and `continuedConfig` both set `Stream: false`.
`agent.Config.Stream` is what enables `CompleteStream` *and* what makes
`processCompletion` emit a buffered `TextDelta` once for a non-streaming
adapter.

Consequence: today a child would emit no deltas even if the adapter forwarded
them. Any fix must also gate `Stream` on observation, or it delivers empty
sinks.

### 2.4 Streaming costs a retry, which is why it must be opt-in

`docs/architecture.md` §3.3: *"Transport retries happen only before the first
assistant-text delta; after partial text, a failure is terminal."* The adapter
wraps post-delta failures in `*model.PartialError`, and `retryableError`
returns false for it.

A child's partial text has no value to the parent — only the final report is
returned — so forcing children to stream would trade a real retry budget for
text nobody asked for. Gating on `ObserveChild` preserves that reasoning and
makes the trade the host's explicit choice.

### 2.5 The live child log is already readable, but only at response boundaries

`Store.appendAt` writes with an unbuffered `file.Write`, so appended bytes are
visible to another reader immediately; `Sync` is about crash durability, not
visibility. `session.ReadSnapshot` captures the byte length before reading and
treats a partially written last line as `errTornLine` + final, so a poll cannot
mistake an in-flight append for corruption.

`(*Spawner).ChildHistory(sessionID)` already builds on that and is documented to
work *"while the child run still holds it"*. This gives a live transcript one
model call behind, which is a useful fallback but not equivalent to the root's
delta stream, and it is component-level only.

### 2.6 The facade has no transcript reader for any session

`engine.Host.History()` reads the live root. There is no facade counterpart:
`Agent` exposes `ID`, `SessionPath`, `Workspace`, `State`, and `Close`, but no
transcript. A facade-only host must import `session` and `agent` to render
anything.

So the child is not uniquely disadvantaged at the facade — every session is.
The plan fixes both, which is also what makes a merged view possible without
importing component packages.

### 2.7 Display errors are already terminal for a run

`(*Runner).emit` wraps a callback error in `errDisplay`; `failDisplay` records
an `internal_error` and terminates. `agent.Config.Display` is documented as
"must not block, call the runner, or write the session log."

A child already forwards `SubagentProgress` through the *parent* display, so a
host renderer error can already fail a child today. Any new sink must decide
its own rule instead of inheriting the root's by accident; see §D3.

### 2.8 The existing serialization guarantee does not extend to per-child sinks

`agent.Config.Display` is documented as never entered concurrently, and the
spawner holds `displayMu` while forwarding child counters. That guarantee exists
because every child shares one callback.

Per-child sinks are a different shape: read-only fan-out runs up to four
children on separate goroutines, so distinct sinks can be entered
concurrently. Serializing them behind one mutex would reintroduce the coupling
this plan is meant to remove. §D4 and §8 O2 settle it.

## 3. Decisions

### D0 — Scope: observation and reading only

In scope: live per-child events, the `Stream` gate, the facade transcript
reader, and the documentation.

Out of scope: durable thinking (§9), any new event type, any ordering guarantee
across children, and any host-side rendering helper.

### D1 — Identity is structural: one sink per child

`ObserveChild` is called once per child and returns that child's callback. No
delta event gains a field. Reasons:

- No event-schema change, so `ViewEvent`, `EventView`, and every recorded
  contract stay exactly as they are.
- Ordering is per child by construction, so the "merged view" problem is
  reduced to the host merging N already-ordered streams.
- A host that only wants one child subscribes to one child.
- The alternative — a `ChildSession` field on `TextDelta`, `ReasoningDelta`,
  `ToolCompleted`, and `Notice` — touches five event types, every `ViewEvent`
  case, and the `-ui-json`-style consumers that switch on `Kind`. It is a
  strictly larger change for the same capability.

### D2 — Children stream only when observed

`childConfig`/`continuedConfig` set `Stream: s.opts.Observe != nil`. Effects,
all documented:

- Observed child: `CompleteStream` when the adapter supports it, deltas reach
  the sink as they arrive.
- Observed child with a non-streaming adapter: `processCompletion` emits one
  `TextDelta`, so the sink still receives the text.
- Unobserved child: unchanged. `Stream: false`, retries intact, no deltas.

A streamed child's `model_response.streamed` becomes `true`, which is the
correct recorded value.

### D3 — A sink failure terminates that child, and only that child

A returned error, or a recovered panic, from `ObserveChild` or from a sink:

- terminates **that** child with `Status: internal_error`;
- appears in the child's `SubagentReport` and therefore in
  `SubagentFinished.Error` and `DelegateTaskDetails.Message` (§5 records which
  field carries it; the report has no `Error` field);
- does not fail the parent run and does not affect sibling children.

Rationale: it matches the existing rule that a display failure is an internal
error (§2.7), keeps a deterministic host bug visible instead of silently
dropping child output, and keeps blast radius local. The spawner must `recover`
around both the registration call and each sink call; a panic is not allowed to
take down a child goroutine uncleaned.

**Owner decision (O1), 2026-11-23: adopted.** An error or panic from the hook
or a sink terminates that child as `internal_error`; the alternative (log and
drop, keep the child running) was rejected because it hides a deterministic
host bug.

### D4 — Per-child serialization, not global

One child's sink is never entered concurrently with itself. Sinks for different
children, and a child sink versus the parent display callback, may run
concurrently.

Implementation: `(*childDisplay).display` invokes its sink directly, on the
child's own goroutine, **before** the existing `displayMu`-protected parent
forwarding in `emit`. The child has no other display caller, so per-child order
follows from the runner's single goroutine and needs no lock.

**Owner decision (O2), 2026-11-23: adopted.** Sinks are serialized per child
and concurrent across children. The spawner-wide-mutex alternative was rejected
because it couples children to each other's render latency. The
`TestSpawnerChildObservationOrderPerChild` barrier fails (deadlock under
`synctest`) if a global mutex is introduced.

### D5 — Registration happens at spawn, never for a pre-start failure

`Observe` is invoked from exactly two call sites — `spawnOne` and
`continueChild` — after `withIntent` and before `started()`. Order is: build
adapter, set intent, register sink, emit `SubagentStarted`, run.

`finishedWithoutStart` does **not** register a sink. There is no child run and
no child transcript; the parent-level `SubagentStarted`/`SubagentFinished`
contract already covers that case. A host must not receive a registration with
no session behind it.

### D6 — The facade copies the transcript DTO

`agent.HistoryItem` is a component type, so the facade defines
`aharness.TranscriptItem` and `aharness.TranscriptKind` and copies at the
boundary, per [architecture §3.2.1](../architecture/facade.md#321-public-library-facade).

This is not a "structured-output/report DTO" in the `AGENTS.md` sense: that
boundary forbids DTOs describing *model* output. `State`, `SessionInfo`, and
`ToolResult` are the same kind of projection already.

### D7 — One reader for every session, lock-free

`Transcript(ctx, stateDir, sessionID)` uses `session.ReadSnapshot` +
`agent.History`, never `session.Open`, so a live writer is tolerated and no lock
is taken. `fs.ErrNotExist` maps to the existing `ErrSessionNotFound`; an
invalid ID maps to `ErrInvalidSessionID`.

`Agent.Transcript()` is the live-root convenience. Both return
`[]TranscriptItem`.

**Deviation, 2026-11-23.** `Agent.Transcript` reads the same lock-free log
snapshot as `Transcript`, not `engine.Host.History()`. `Host.History` projects
the store's in-memory event slice, and the store has no internal lock: its rule
is that the single runner goroutine is the only writer. A host calling
`Agent.Transcript` during a run would therefore race the runner's append. The
snapshot read is the only race-free live read, and it returns the same
projection when the session is idle, because `Append` writes the line before it
records the event in memory. `TestAgentTranscriptMatchesPackageReader` pins that
the two entry points agree and that the read reflects the run that just
finished; it cannot compare `engine.Host.History` directly, because the facade
test package cannot reopen the session while the agent holds its lock.

No `(*Run).ChildTranscript` method: the package-level reader already covers a
live child (the host has the ID from `ChildTarget.Session`), and parentage
validation already exists as `agent.Spawner.ChildHistory` for hosts at the
component level.

### D8 — Keep `SubagentProgress` exactly as it is

The throttled counters remain the parent-level roster signal. They are not
replaced, not throttled differently, and not re-based. An observed child
produces both the counters and the sink events; a host may ignore either.

### D9 — Documentation and release

A minor, additive release. `doc.go`, architecture §3.2.1 and §3.3, limits,
testing strategy, README, CHANGELOG, and this directory's plan index move in the
same change.

## 4. Contract deltas

| Area | Before | After | Compatibility |
|---|---|---|---|
| Child live events | counters plus `SubagentMessageRecorded` | full per-child stream when `ObserveChild` is set | additive, opt-in |
| Child `Stream` | always false | true when `ObserveChild` is set | behavior change, opt-in |
| Child retry after partial text | not applicable | terminal, as for the root | documented trade |
| Sink concurrency | n/a | per child serialized; across children concurrent | new contract, §D4/O2 |
| Sink error | n/a | terminates that child as `internal_error` | new contract, §D3/O1 |
| Facade transcript | none | `Transcript`, `Agent.Transcript`, `TranscriptItem`, `TranscriptKind` | additive |
| Event types, `ViewEvent` | — | unchanged | none |
| Session schema, prompt, grant, projection | — | unchanged | none |
| Concurrency model | parent suspended for the whole delegate call | unchanged | none |

No recorded contract in `AGENTS.md` moves. Delegation is still one blocking tool
call; nothing becomes background, nested, or asynchronous, and no merge order is
promised by the SDK.

## 5. Build plan

### Phase 1 — `agent`: per-child observation

Files: `agent/spawn.go`, `agent/display.go`, `agent/spawn_test.go`, and
`agent/display_test.go`.

Add to `agent/display.go` (or `spawn.go` beside `SpawnerOptions`):

```go
// ChildObservation identifies one delegated child to an observation sink.
type ChildObservation struct {
    CallID  string
    Index   int
    Agent   string
    Session string
    Intent  string
    Model   string
    Effort  string
    Tools   []string
}

// ChildSink receives one child's display events in that child's order. It is
// never entered concurrently with itself; sinks for different children may run
// concurrently.
type ChildSink func(DisplayEvent) error
```

`SpawnerOptions` gains:

```go
// Observe, when non-nil, registers one live display sink per delegated child.
// It is called once per child, after the definition is resolved and before the
// child's first event. The returned sink may be nil, which observes nothing.
// Observe is called from child goroutines during read-only fan-out, so it must
// be safe for concurrent use. A returned error or recovered panic terminates
// that child as internal_error; siblings and the parent are unaffected.
Observe func(ChildObservation) ChildSink
```

In `childDisplay` add `sink ChildSink`, and an `observe()` method that calls
`SpawnerOptions.Observe` under a `recover`, stores the result, and returns the
error. `display` gains forwarding **before** the existing switch logic:

```go
func (d *childDisplay) display(ev DisplayEvent) error {
    if err := d.forward(ev); err != nil {   // sink first, no displayMu
        return err
    }
    switch ev := ev.(type) { /* unchanged */ }
}
```

`forward` calls `d.sink(ev)` with a `recover`, and returns a wrapped
`errDisplay` on error or panic. It must not take `displayMu`.

Call `observe()` at the two real spawn sites, and make `Stream` follow the
registration:

- `spawnOne`: `adapter := s.newChildDisplay(...).withIntent(spec.Task)`; then
  `if err := adapter.observe(); err != nil { ... }`; then `adapter.started()`.
- `continueChild`: same order after the adapter is built.
- `childConfig` and `continuedConfig`: replace `Stream: false` with
  `Stream: s.opts.Observe != nil`.

`finishedWithoutStart` is unchanged (§D5).

Tests:

| Test | Asserts |
|---|---|
| `TestSpawnerChildObservationDeliversChildEvents` | a streamed child's sink receives `RunStarted`, `ModelStarted`, `TextDelta`, `ReasoningDelta`, `ResponseCompleted`, `ToolStarted`, `ToolCompleted`, `MessageRecorded`, `RunFinished` |
| `TestSpawnerChildObservationOrderPerChild` | concurrent read-only fan-out: each child's sink sequence is monotonic; two sinks interleave |
| `TestSpawnerChildObservationNilKeepsCounterOnly` | `Observe` nil: no sink, `model_response.streamed == false`, existing `Subagent*` events unchanged |
| `TestSpawnerChildObservationErrorEndsThatChildOnly` | sink returns an error: that child is `internal_error`, its sibling completes, the parent run completes |
| `TestSpawnerChildObservationPanicIsRecovered` | a panicking sink does not crash the process or the parent run |
| `TestSpawnerChildObservationEnablesStreaming` | `streamed` is true for a streamed adapter with a sink, false without |
| `TestSpawnerChildObservationBufferedFallback` | non-streaming completer plus sink: exactly one `TextDelta` |
| `TestSpawnerChildObservationSanitizes` | ANSI/control characters are stripped before the sink |
| `TestSpawnerChildObservationSkipsPreStartFailure` | `Observe` is never called for a task that fails before its run starts |

**Landed 2026-11-23.** All nine tests exist, plus
`TestSpawnerChildObservationHookPanicIsRecovered` for the registration call,
which §D3 requires to recover separately from the sink call. Two notes:

- `ChildObservation`, `ChildSink`, and the `Observe` field live in `spawn.go`
  beside `SpawnerOptions`; `display.go` is unchanged.
- The sink receives the same sanitized event the parent display receives,
  because the runner sanitizes before calling `Config.Display`; the spawner
  needs no second sanitizer.
- §D3 asks for the sink failure in the child's report. `tools.SubagentReport`
  has no `Error` field, so the failure text is the report's `Text`: a child that
  produced no assistant text reports the display error itself, which then flows
  into `SubagentFinished.Error` and `DelegateTaskDetails.Message`. Only the
  first sink failure is used: a terminating run repeats the same cause on its
  later display attempts. A child that produced text keeps it, because `Text` is
  output, and the durable child log already holds the `error` event naming the
  cause.

### Phase 2 — `engine`: plumb the hook

Files: `engine/engine.go`, `engine/runner.go`, `engine/runner_test.go`.

- `Spec` and `RunSpec` gain `ObserveChild func(agent.ChildObservation) agent.ChildSink`.
  `RunSpec` wins when set, mirroring `ResolveModel` and `NewChildCompleter`.
- `BuildRunnerWithSpawner` passes it into `agent.SpawnerOptions.Observe`.

Test: `TestBuildRunnerWiresChildObservation` — a run through the engine reaches
the supplied observer exactly once per child.

**Landed 2026-11-23.** The test also covers the precedence rule: a
`RunSpec.ObserveChild` replaces `Spec.ObserveChild` for that run.

### Phase 3 — facade

Files: `delegate.go`, `events.go`, a new `transcript.go`, `aharness.go`, plus
`delegation_test.go` and a new `transcript_test.go`.

`delegate.go`:

```go
// ChildTarget identifies one delegated child before it starts.
type ChildTarget struct {
    CallID  string
    Index   int
    Agent   string
    Session string
    Intent  string
    Model   string
    Effort  string
    Tools   []ToolName
}

type DelegationConfig struct {
    // ...
    // ObserveChild, when non-nil, registers a live observation sink for one
    // delegated child. It is called once per child before that child's first
    // event, and never for a task that fails before its run starts. The
    // returned callback receives that child's own display events in that
    // child's order, with EventMeta.Session set to the child session; a nil
    // return observes nothing. Parent-level Subagent* events are not delivered
    // to it and keep arriving on the run's own stream.
    //
    // Registering any observer enables streaming for delegated children, which
    // makes a post-delta transport failure terminal instead of retried. Sinks
    // for different children may run concurrently, and concurrently with the
    // run's own event stream; a sink must not block.
    //
    // A returned error, or a panic, terminates that child with internal_error.
    // Siblings and the run are unaffected.
    ObserveChild func(ChildTarget) func(Event) error
}
```

`events.go`: a small adapter that converts one internal event to a public
`Event` with the **child's** session ID, reusing `eventFromInternal(ev,
childSession, time.Now())`, and skips `(nil, false)`.

`delegate.go`: `delegationSetup` carries the public hook and exposes
`internalObserve() func(agent.ChildObservation) agent.ChildSink`, converting
`agent.ChildObservation` → `ChildTarget` and `agent.DisplayEvent` → `Event`,
and copying `Tools` into `[]ToolName`. `aharness.go`'s `buildDelegation` and
`start` pass it into `RunSpec.ObserveChild`.

`transcript.go`:

```go
// TranscriptKind classifies one transcript item.
type TranscriptKind string

const (
    TranscriptRun       TranscriptKind = "run"
    TranscriptUser      TranscriptKind = "user"
    TranscriptAssistant TranscriptKind = "assistant"
    TranscriptTool      TranscriptKind = "tool"
    TranscriptNotice    TranscriptKind = "notice"
)

// TranscriptItem is one display-only transcript entry.
type TranscriptItem struct { /* mirrors agent.HistoryItem, incl. json tags */ }

// Transcript reads the display transcript of any session under stateDir —
// root, delegated child, or fork — without taking its session lock, so a
// running session stays readable. A torn final line is ignored.
func Transcript(ctx context.Context, stateDir, sessionID string) ([]TranscriptItem, error)
```

`aharness.go`: `func (a *Agent) Transcript() ([]TranscriptItem, error)` over
`a.host.History()`, returning `ErrClosed` when closed, consistent with `State`.

Tests:

| Test | Asserts |
|---|---|
| `TestObserveChildReceivesFacadeEventsWithChildSession` | every delivered `Event` has `Session == childSession`; text, tool, notice, and finished kinds all arrive |
| `TestObserveChildNotCalledForPreStartFailure` | unknown agent, limit exhausted, bad continuation: no registration |
| `TestObserveChildEnablesChildStreaming` | facade run with a streaming fake model: child `TextDelta` arrives incrementally |
| `TestObserveChildErrorIsolatesChild` | facade: one child fails, `Result.Children` shows it, siblings complete |
| `TestTranscriptRootAndChild` | same projection for a root and for a child read by ID |
| `TestTranscriptLiveWriter` | read while a run is active; no lock error; no corruption report |
| `TestTranscriptTornFinalLine` | truncated last line is ignored |
| `TestTranscriptNotFound` | `errors.Is(err, aharness.ErrSessionNotFound)`; invalid ID gives `ErrInvalidSessionID` |
| `TestAgentTranscriptMatchesHostHistory` | `Agent.Transcript` equals `Transcript(ctx, stateDir, a.ID())` |

Phase 3 tables must run under `-race`; the merged-view case is the reason.

**Landed 2026-11-23.** Two deviations, both recorded rather than silent:

- `TranscriptItem` carries no JSON tags. `agent.HistoryItem` has none, and the
  facade's other DTOs (`State`, `Result`, `SessionInfo`) carry none either; the
  only tagged projection is `EventView`. Mirroring therefore means the fields,
  their order, and their types. §10's guard is
  `TestTranscriptItemMirrorsHistoryItem`, a reflection walk over both structs
  that allows only the four documented type conversions (`Kind`, `NoticeKind`,
  `Status`, `Usage`).
- `TestAgentTranscriptMatchesHostHistory` became
  `TestAgentTranscriptMatchesPackageReader`; see the §D7 deviation for why the
  reader is the log snapshot rather than `engine.Host.History`.
- The public adapter (`childEventSink`) lives in `events.go` next to
  `eventFromInternal`, and the facade bridge (`internalObserve`, plus the
  `[]string` → `[]ToolName` copy) in `delegate.go`.
- The facade sink receives only events that have a public form. The component
  sink also gets `agent.ModelStarted`, which has no public counterpart, so
  `childEventSink` skips it; a retry stays visible through `Notice`. The
  CHANGELOG, `delegate.go`, and architecture §3.3 state the subset instead of
  promising the component list.
- `forward` and `observe` return their errors without the `errDisplay` marker:
  the runner's `emit` adds it once, and `observe` never passes through the
  runner, so marking either place produced
  `display output failed: display output failed: ...` in the child's report.

### Phase 4 — reference docs and release

| File | Change |
|---|---|
| `doc.go` | "# Events": a child is observable through `ObserveChild`; ordering and concurrency are per child; unobserved children stay counter-only; a child transcript is read with `Transcript` |
| `docs/architecture.md` §3.2.1 | add `ObserveChild`, `ChildTarget`, `Transcript`, `Agent.Transcript`, `TranscriptItem` to the shipped surface lists |
| `docs/architecture.md` §3.3 | replace "child text, arguments, and tool output stay in the child's own transcript" with the observed and unobserved cases; state the `Stream` gate, the per-child ordering and concurrency rule, the sink-error rule, and the partial-text retry consequence |
| `docs/architecture.md` §7 | one sentence: the sink changes nothing durable; the schema stays `2` |
| `docs/limits.md` §5 | a row for the observation sink: synchronous, unbuffered, per child, so a blocking sink throttles that child's model stream |
| `docs/testing-strategy.md` | child-observation ordering, isolation, and merged-view tests |
| `README.md` | one paragraph under delegation and one under session reading |
| `CHANGELOG.md` | **Added** entry; note the opt-in `Stream` change explicitly |
| `docs/README.md` | plan index row |

Then `make dod`.

**Landed 2026-11-23.** All listed files changed, plus `docs/ci-release.md`'s
convention: the entry is under `## [Unreleased]` in `CHANGELOG.md` until a
release moves it.

## 6. Acceptance mapping

| Requirement | Where |
|---|---|
| A child's text reaches the host live | §D2, Phase 1 `DeliversChildEvents`, Phase 3 facade test |
| A child's thinking reaches the host live | §D2 (ReasoningDelta), Phase 1 test |
| A child's tool call and result reach the host live | Phase 1 `ToolStarted`/`ToolCompleted` assertions |
| A host can merge all children into one ordered view | §D1, §D4, Phase 1 ordering test, Phase 3 `-race` |
| One bad sink cannot break unrelated work | §D3, Phase 1 and Phase 3 isolation tests |
| Nothing changes for a host that does not opt in | §D2, Phase 1 `NilKeepsCounterOnly` |
| A child transcript is readable at the same API level as a root transcript | §D7, Phase 3 `TranscriptRootAndChild`, `AgentTranscriptMatchesHostHistory` |
| A live session is readable without a lock | §D7, Phase 3 `LiveWriter`; `Agent.Transcript` also reads the snapshot (see the §D7 deviation) |
| No event type, schema, prompt, grant, or concurrency change | §4; `make dod`; no `ViewEvent` diff |

## 7. Verification

Automated: `make dod` (coding-standard §8 gates plus `docs-check`), including
`go test -race -count=1 ./...`.

Targeted:

```sh
go test ./agent -run 'SpawnerChildObservation' -race -count=1
go test ./engine -run 'ChildObservation' -race -count=1
go test . -run 'ObserveChild|Transcript' -race -count=1
```

**Result, 2026-11-23.** All four commands pass, and `make dod` passes
(`goimports`, `go vet`, `golangci-lint`, `go test -race -count=1 ./...`,
`go build -trimpath ./...`, `govulncheck ./...`, `docs-check`). The merged-view
concurrency case is covered by `TestSpawnerChildObservationOrderPerChild` and
`TestObserveChildErrorIsolatesChild` under `-race`.

Manual, and required before claiming parity:

1. A real-provider run with two definitions and `ObserveChild` set, asserting
   interleaved child text arrives before `SubagentFinished`. Needs
   `AHARNESS_PROVIDER_TOKEN`.
2. A real-provider streamed child that fails mid-stream, confirming the child
   reports `provider_error` with partial text rather than retrying.

**Pending, 2026-11-23.** `AHARNESS_PROVIDER_TOKEN` is unset in the implementation
environment, so neither smoke ran. Both remain open maintainer checks; nothing
below or above claims them. The fake-provider tests cover the same code paths
for streaming (incremental deltas and `streamed: true`) and for a post-delta
failure being terminal, but they are not a provider smoke.

Not run by this plan: any acode-side merged view. acode is a separate
repository and its adoption is its own decision.

## 8. Open decisions

Both are one-line choices, but they are recorded contracts, so they are the
owner's.

**O1 — sink error semantics.** §D3 recommends terminating that child with
`internal_error`. Alternative: log and drop, keep the child running.

**O2 — sink concurrency.** §D4 recommends per-child serialization with
cross-child concurrency and a documented requirement that the sink be safe for
concurrent use. Alternative: one spawner-wide mutex that preserves the current
"never entered concurrently" guarantee at the cost of coupling children.

If both recommendations are accepted, Phase 1 can start as written. If either is
reversed, only `forward`/`observe` in `agent/spawn.go` and the §4 rows change.

## 9. Non-goals

- **Durable thinking.** Still never logged, for the root or a child. Persisting
  it needs a session-schema field and a decision about display text versus
  provider-native continuation material; it remains the separate
  durable-reasoning decision named in
  [architecture §2.4](../architecture/provider.md#24-continuation-for-a-new-dialect).
  `ObserveChild` closes the *live* asymmetry only.
- **A global merge order.** The SDK promises per-child order. Any interleaving
  across children is the host's.
- **Background, nested, or asynchronous delegation.** Unchanged; still one
  blocking tool call, depth one.
- **Buffering, coalescing, or sampling child deltas.** The sink is synchronous,
  matching `Config.Display`. A host that wants a bounded queue owns it.
- **A rendering, layout, or TUI helper.** Out of the SDK by `AGENTS.md`.
- **Replacing `SubagentProgress` or the roster events.**
- **A config file, environment, or CLI switch for any of this.** Opt-in is a
  field.

## 10. Risks

- **A blocking sink throttles a child.** The sink is called synchronously from
  the child's model loop, so a host renderer that blocks slows that child's
  stream. Documented in `limits.md`; the mitigation is host-side.
- **Concurrent sinks are a race hazard for a careless host.** The root contract
  currently promises "never concurrent"; this plan narrows the promise to "never
  concurrent per child". That is the single most likely source of a downstream
  bug, which is why §D4 and §O2 exist. `-race` tests must cover the merged case.
- **A panicking host sink kills a child.** Accepted in §D3 because the
  alternative hides the bug. Reversible via O1.
- **Streaming a child changes its failure mode.** Mid-stream failures become
  terminal. Only for opted-in hosts, and it is documented at the field.
- **Facade DTO drift.** `TranscriptItem` mirrors `agent.HistoryItem` by hand.
  A field added to one and not the other is a silent omission.
  `TestTranscriptItemMirrorsHistoryItem` walks both structs by reflection and
  fails on a missing, reordered, or retyped field, allowing only the four
  documented public conversions.

## 11. Evidence index

| Claim | Source |
|---|---|
| Child adapter forwards three kinds and drops the rest | `agent/spawn.go`, `(*childDisplay).display` |
| Deltas carry no session identity | `agent/display.go`, `TextDelta`, `ReasoningDelta` |
| Children never stream | `agent/spawn.go`, `childConfig`, `continuedConfig` |
| Post-delta failure is terminal | `docs/architecture.md` §3.3; `model.PartialError`; `retryableError` |
| Live child log readable, one model call behind | `session/store.go` `appendAt`; `session/reader.go` `ReadSnapshot`; `agent/spawn.go` `ChildHistory` |
| Facade has no transcript reader | `aharness.go`, `Agent` methods; `engine/engine.go`, `Host.History` |
| Display errors are terminal today | `agent/runner.go`, `emit`, `failDisplay` |
| Per-child sinks can run concurrently | `docs/architecture.md` §3.3 (read-only fan-out); `agent/spawn.go` `displayMu` |
| Root-only child events were already an audit finding | `aharness-host-surface-issues.md` issue 6 (gitignored `.issues/` directory; named, not linked) |
| Recorded child run shape | [subagent-build.md](subagent-build.md) §3.6 table, §3.8 |
| Facade copies DTOs | [architecture.md](../architecture/facade.md#321-public-library-facade) |
| DoD gates | [definition-of-done.md](../definition-of-done.md) |
