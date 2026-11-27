# Changelog

All notable changes to this module are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html). Before `v1.0.0`, a
minor version may contain a breaking change to the public surface; those
changes are called out under **Changed** and **Removed**.

## [Unreleased]

### Fixed

- A non-2xx `web_fetch` provider response now carries the provider's own error
  detail instead of a bare status. The previous message,
  `provider 127.0.0.1:51235 returned HTTP 400 Bad Request`, discarded the body,
  so a provider-side refusal (for example Crawl4AI's SSRF guard naming the
  blocked target) was indistinguishable from a broken provider and cost several
  tool calls to diagnose. The body is now read once under a 4 KiB cap, sanitized
  and capped at 400 characters, and appended to the existing message; a JSON
  `detail`, `error`, `error_message`, or `message` member is preferred, plain
  text is used otherwise, and an absent, unknown-member, or truncated JSON body
  keeps the status-only message. The result code stays `fetch_failed`; the SDK
  does not classify provider prose into a typed cause.

## [v0.7.2] - 2026-11-26

### Fixed

- The `read_image` capability gate now follows a model switch. The gate was read
  once at `New`, so a host that switched models mid-session (a picker changing
  `RunOptions.Model`) kept the old answer until a new session. Because the
  registry is built per run, the capability now travels with the run:
  `RunOptions.Model` re-resolves it into `engine.RunSpec.ImageInput`, which the
  engine applies to that run's registry. Switching to a model whose capability
  is unknown clears the gate instead of keeping a stale answer.
- A delegated child on a different model now follows that model's capability
  through `ModelResolution.ImageInput` instead of inheriting the parent's
  construction-time value.

## [v0.7.1] - 2026-11-26

### Fixed

- `tools.Names()` returned a trailing empty name and omitted `read_image`. It
  allocated the current set's length but ranged over the frozen v2 declarations,
  so the two lists disagreed after the v0.7.0 toolset bump. No SDK path calls
  `Names()` and no test covered it, which is why the release gate stayed green;
  `NamesFor` and `NamesWithout` were always correct, so the first-party acode
  agent was unaffected. `Names()` now derives from `Schemas()`, so the two
  cannot drift on a future version bump, and `TestNamesMatchesSchemas` pins
  them together. `v0.7.0` is superseded.

## [v0.7.0] - 2026-11-26

This is a minor release that adds one builtin tool, `read_image`, so a vision
model can read an image from the workspace. It is additive at the session
schema level: the envelope stays `v=2` and the new payload member is omitted
when empty. It does change recorded contracts, so read the notes.

### Added

- Builtin `read_image` tool (`intent`, `path`). It reads one workspace PNG,
  JPEG, or GIF, normalizes it, and returns it to the model as an image part with
  a one-line caption as the text result. Sources are capped at 2 MiB and
  16,000,000 pixels (checked from the header before a full decode); delivered
  images are capped at 2048 px on the longest edge, 2,000,000 pixels, and
  512 KiB, with a box-filter downscale and a JPEG quality ladder. A source that
  already fits passes through byte for byte. Output is deterministic and the
  delivered SHA-256 is recorded in `details`.
- `model.ImagePart` and `model.Message.Images` carry image input in the
  canonical model. Both dialect adapters map a tool result's image into a
  following `user` message, the shape every OpenAI-compatible server accepts.
- `read_image` and `invalid_image` result codes: the tool reports a non-image as
  `invalid_image` (with a `read_file` hint for text) and an over-budget source as
  `too_large`.
- `CatalogModel.ImageInput` and `ModelSpec.ImageInput *bool` report whether the
  model accepts image input, decoded from models.dev `attachment` and
  `modalities.input`. Nil means unknown. It never affects eligibility; an
  explicit false makes `read_image` return `unavailable` without reading the
  file.
- `tools.Options.ImageInput *bool` carries that capability to the registry.
- `read_image` is treated as read-only for delegated fan-out, so a child granted
  only read tools still runs concurrently.

### Changed

- The toolset version is now `toolset-v3`; `toolset-v2` is frozen. A session
  that recorded v0, v1, or v2 keeps its declarations and cannot be granted
  `read_image`. Resuming such a session with a `read_image` grant is a
  configuration error rather than a silent widening.
- The projection estimates an image at `ceil(width/28) * ceil(height/28)` tokens
  instead of counting its bytes, and elides an old image together with its text
  stub, leaving an `[image <media> <w>x<h> elided]` note.
- The `delegate` concurrent-fan-out set now includes `read_image`.

**Migration:** no exported identifier was renamed or removed, so existing code
compiles unchanged. Two recorded values moved:

- New sessions record `toolset-v3` and are offered `read_image` by default. A
  host that expects a fixed tool list, or that enumerates tools for its own UI,
  must treat `read_image` as a new builtin and may remove it with
  `ToolsConfig.Disable` (`ToolReadImage`).
- A session recorded under `toolset-v0`–`v2` replays with its frozen
  declarations and cannot be granted `read_image`; resuming one with that grant
  is a configuration error. Continue those sessions as before, or fork them to
  adopt v3.
- `ModelSpec.ImageInput` is a new `*bool`. Leaving it nil keeps the previous
  behavior: `read_image` runs and the provider decides. Set it to `false` only
  to fail fast on a model known to be text-only.

## [v0.6.0] - 2026-11-25

This is a minor, additive release: a live per-child observation stream and a
transcript reader for any session. The session schema stays `2`, existing logs
need no migration, `prompt-v3` and the toolset version are unchanged, no event
type changed, and the concurrency model is unchanged — delegation is still one
blocking tool call at depth one.

The real-provider observation smoke is pending: no provider token was available
when this landed. It covers two definitions with `ObserveChild` set, asserting
interleaved child text arrives before `SubagentFinished`, and a streamed child
that fails mid-stream, asserting `provider_error` with partial text instead of a
retry. The fake-provider tests cover both paths.

### Added

- `DelegationConfig.ObserveChild func(ChildTarget) func(Event) error` registers
  a live display sink for one delegated child. It is called once per child that
  starts a run, before that child's first event, with a `ChildTarget` naming the
  child session, agent, intent, model, effort, and grant. The returned callback
  receives that child's own `RunStarted`, text and thinking deltas,
  `ResponseCompleted`, `ToolStarted`, `ToolCompleted`, `MessageRecorded`,
  `Notice`, and `RunFinished`, each with `EventMeta.Session` set to the child
  session. The facade forwards only events that have a public form, so internal
  transport events such as `ModelStarted` are not delivered; retries stay
  visible through `Notice`. Ordering is per child; merging children into one view
  is the host's. Distinct child sinks, and a child sink
  versus the run's own stream, may run concurrently. A returned error or a
  panic terminates that child as `internal_error` with the cause in its report
  and leaves its siblings and the run untouched. A task that fails before its
  run starts registers no sink.
- `Transcript(ctx, stateDir, sessionID) ([]TranscriptItem, error)` reads the
  display transcript of any session — root, delegated child, or fork — from a
  byte snapshot of the log, without the session lock, so a running session stays
  readable and a torn final line is ignored. A missing log is
  `ErrSessionNotFound` and an unusable ID is `ErrInvalidSessionID`.
- `Agent.Transcript()` returns the same transcript for the current agent's
  session and `ErrClosed` after `Close`.
- `TranscriptItem` and `TranscriptKind` mirror `agent.HistoryItem` at the
  facade boundary, so a facade-only host renders a merged view without importing
  a component package.
- At the component level: `agent.SpawnerOptions.Observe`, `agent.ChildSink`,
  `agent.ChildObservation`, `engine.Spec.ObserveChild`, and
  `engine.RunSpec.ObserveChild`.

### Changed

- A delegated child streams (`Config.Stream`) only when `ObserveChild` is
  registered. An observed child uses `CompleteStream` when its adapter supports
  it and emits one buffered `TextDelta` otherwise, and its `model_response`
  records `streamed: true` when it streamed. This is opt-in, and it is the one
  behavioral trade in the release: for an observed child, a transport failure
  after the first assistant-text delta is terminal instead of retried, exactly
  as for the root. An unobserved child keeps `Stream: false`, the full retry
  budget, and the counter-only `Subagent*` roster events, which are unchanged.

## [v0.5.0] - 2026-11-22

This release adds prompt and request transparency with no schema change. The
session schema stays `2`, existing logs need no migration, `prompt-v3` is
unchanged, and tool grants, tool schemas, and the toolset version are unchanged.

It supersedes `v0.4.2`, which published the same source without this record; see
that entry below. A consumer already on `v0.4.2` sees no code change, and one on
`v0.4.1` gains the readers. The optional real-session smoke in
[the plan](docs/plans/prompt-transparency-build.md) §7.7 has not run: it needs an
existing acode session log and remains a maintainer check.

### Added

- `agent.LatestModelExchange(events) (agent.ModelExchange, error)` returns the
  newest recorded model attempt: the latest `model_request` or `model_response`
  plus its pair when one exists in the same turn without an intervening run or
  session start. Payloads are copies of the recorded event JSON, never wire
  bytes, and are not decoded. A newest `model_request` is returned alone, so an
  in-flight or failed call never borrows an older exchange. On a fork the newest
  attempt may be an event copied from the parent, and `Run` counts the parent's
  runs. It is a component-level reader: the facade has no exchange DTO, and a
  facade host reads with `session.ReadSnapshot` and calls this directly.
- `agent.ModelExchange` carries the payloads, their sequence numbers, the run
  count, the turn, and which sides were found. It is exported with the reader;
  no new runtime dependency or encode/decode path is involved.
- `agent.State.PromptCapped` and `aharness.State.PromptCapped` report whether
  the recorded `system_prompt` was truncated by the 32 KiB prompt cap. The flag
  is derived by re-running the cap rule over the recorded text, so it is correct
  for logs written before the field, and hosts never match the marker, whose
  format stays internal. It is never a replay key.

## [v0.4.2] - 2026-11-22

Tagged in error on the prompt-transparency merge commit (`72c271e`) without a
CHANGELOG entry. The `release` workflow re-ran `make dod` successfully but failed
its metadata check on the missing `## [v0.4.2]` heading, so no GitHub Release was
created. The tag stays where it is: the module proxy and the checksum database
already serve it, and a published tag is never moved or deleted
([ci-release.md](docs/ci-release.md) §2.4). The source is the tree `v0.5.0`
documents above; use `v0.5.0`.

## [v0.4.1] - 2026-11-20

This is a patch release: it restores Responses thinking text with no public API
change. The session schema stays `2`, existing logs need no migration,
`prompt-v3` is unchanged, and tool grants, tool schemas, and the toolset
version are unchanged.

### Fixed

- Responses thinking text is restored: the adapter now sends
  `reasoning: {"effort": "<value>", "summary": "auto"}` whenever an explicit
  thinking effort is set. Without `"summary": "auto"` the endpoint returns
  encrypted reasoning only (empty `summary` while reasoning tokens are still
  billed), so no thinking text reached `Response.Reasoning`, `ReasoningDelta`,
  or `ResponseCompleted.Reasoning` in buffered or streamed calls. The
  encrypted copy still drives tool-round replay, and an empty effort still
  omits the field.

## [v0.4.0] - 2026-11-19

This is the workspace-instructions release. `Config.Instructions` now reaches
every fresh delegated child, survives `Resume` and `Fork`, and is recorded once
as `session_start.appended_prompt`. It is a minor release because the module is
pre-1.0 and the change set is both additive and breaking: component hosts lose
`agent.Config.AppendSystemPrompt`, which was engine-dead. Every item under
**Changed** or **Removed** carries a **Migration** note. The session schema
stays `2`, existing logs need no migration, `prompt-v3` is unchanged, and tool
grants, tool schemas, and the toolset version are unchanged.

The build plan's fresh-acode session check had not run when this tag was cut:
acode is a separate repository. The exact pending check is recorded in
[the plan](docs/plans/workspace-instructions-build.md) §7.4; the release does
not claim it was verified.

### Added

- `session_start.appended_prompt` records the host text composed into the
  prompt. It is optional, exposed as `agent.State.AppendedPrompt` and
  `aharness.State.AppendedPrompt`, and empty on every log written before the
  field and on every delegated child. The session schema stays `2`, and an old
  reader ignores the field.
- `agent.Config.AppendedPrompt` carries that text to the record and to
  delegated children. It is record-only: the caller composes the bytes into
  `SystemPrompt` first, and the package never appends them.

### Changed

- `aharness.Config.Instructions` now reaches every fresh child instead of only
  the parent, and it survives `Resume` and `Fork`: a resumed session carries the
  recorded text, a fork inherits or extends it, and a continued child keeps it
  through its recorded prompt. The SDK still does not discover workspace files;
  a host reads `AGENTS.md` or its equivalent and passes the bytes.
  **Migration:** a host that already passes `Instructions` needs none. Text that
  must stay root-only belongs in a composed `SystemPrompt` body
  (`agent.LoopRules` plus host lines), not in `Instructions`.
- A fork with `Instructions` records the union as `appended_prompt`; its
  recorded `system_prompt` bytes are unchanged.

### Removed

- **Breaking:** `agent.Config.AppendSystemPrompt` is removed. It was
  engine-dead: no engine, facade, or spawner path set it, and the facade already
  folded `Instructions` into the prompt. **Migration:** compose the text into
  `SystemPrompt` before the call. A config that already carried an explicit
  prompt appends the text; a config that relied on the rendered prompt must
  render it first, because a non-empty `SystemPrompt` suppresses rendering.
  Either way the recorded output is byte-identical to the old field.

  ```go
  if cfg.SystemPrompt == "" {
      rendered, err := agent.RenderSystemPrompt(agent.PromptVersion, cfg.Workspace, cfg.Identity, cfg.Agents)
      if err != nil {
          return err
      }
      cfg.SystemPrompt = rendered
  }
  cfg.SystemPrompt += "\n\n" + hostText
  cfg.SystemPrompt = agent.CapSystemPrompt(cfg.SystemPrompt) // optional; the write caps once
  ```

  A config with an explicit `PromptVersion` passes that version instead of
  `agent.PromptVersion`; a config that already carried an explicit
  `SystemPrompt` needs only the last two lines.

## [v0.3.0] - 2026-11-19

This is the prompt-surface release planned after `v0.2.0`. The SDK owns the
prompt contract and the loop invariants; identity, prompt content, and role
definitions now belong to the host. The module is pre-1.0, so this minor
release contains breaking changes for component hosts and for facade hosts that
used the bundled role set; every item under **Changed** or **Removed** carries a
**Migration** note. `prompt-v3` is the current prompt, the session schema stays
`2`, existing logs need no migration, and tool grants, tool schemas, and the
toolset version are unchanged.

The real-model component probes required by the build plan had not run when
this tag was cut. The exact pending checks are recorded in
[the plan](docs/plans/prompt-surface-build.md) §12; do not read this release as
probe-verified.

### Added

- Prompt composition primitives: `agent.LoopRules()` returns the
  version-independent loop invariants without coding policy,
  `agent.AgentCatalogText(agents)` renders the one-line catalog under the 4 KiB
  cap and omission marker, and `agent.DefaultPromptBody(version, agents)` returns
  the SDK default body without the identity line or recorded-prompt cap. A host
  can compose a replacement prompt without transcribing contract text.
- `aharness.Config.Identity`, `engine.Spec.Identity`, and `agent.Config.Identity`
  name the host product in the parent and delegated-child identity lines. Empty
  means `aharness`; the engine trims it, caps it at 64 bytes, and rejects invalid
  UTF-8 and control characters. It is not recorded; supply it on every
  construction, including `Resume` and `Fork`.
- `aharness.Config.PromptTag`, `engine.Spec.PromptTag`, and
  `agent.Config.PromptTag` attribute the host's prompt template beside the SDK
  template version. It is recorded as optional `session_start.prompt_tag` and
  exposed as `State.PromptTag` (`aharness.State` and `agent.State`). Empty means
  unset; otherwise it is at most 64 bytes of printable ASCII matching
  `[A-Za-z0-9._:/-]+`. It is never a replay key and never affects rendering,
  caps, or replay. Resume keeps the recorded tag and rejects a different
  explicit tag; Fork prefers an explicit tag and inherits only when the prompt
  is unchanged. A delegated child never records its parent's tag. The session
  schema stays `2`; the field is optional.

- `agent.PromptVersionV2` names the now-frozen `prompt-v2` template;
  `agent.PromptVersion` is `prompt-v3`. `agent.SupportsPrompt` accepts v0, v1,
  v2, and v3.

### Changed

- Prompt content: `prompt-v2` is no longer current; new sessions record
  `prompt-v3`. The v3 default body keeps the available-agents catalog and the
  unchanged todo guidance, and replaces the shared rules with nine lines: it
  adds project instructions, verified-versus-unverified reporting, and the
  refuse-or-flag rule, states that every tool call receives exactly one result,
  and conditions delegation on the independence of the conclusion as well as on
  output volume or parallelism. A prompt-v2 or prompt-v3 child whose grant
  includes `todo` gets the todo guidance; v0 and v1 children keep their frozen
  rules. Existing logs replay their recorded prompt or frozen template and need
  no migration.
- **Breaking:** `agent.RenderSystemPrompt` now takes the host identity:
  `RenderSystemPrompt(version, workspace, identity string, agents []AgentInfo)`.
  **Migration:** pass your product name, or `""` for the `aharness` default, as
  the third argument. `Identity == "aharness"` renders the exact frozen v0/v1/v2
  bytes.
- **Breaking:** `agent.LoadAgents` now takes the host's base definitions:
  `LoadAgents(base []AgentDefinition, stateDir string)`.
  **Migration:** replace `LoadAgents(dir)` with `LoadAgents(hostDefaults(), dir)`
  when you have host defaults, or `LoadAgents(nil, dir)` when you do not. `nil`
  means no host defaults: the former bundled set is no longer implicit. If you
  relied on that set, supply your own base (see **Removed** below); the
  `Example_delegation` example in `example_test.go` is the starting template.
  `AgentDefinition.Source` values are exported as `agent.SourceDefault`
  (`"default"`, formerly `"bundled"`) and `agent.SourceUser` (`"user"`).

### Removed

- **Breaking:** `agent.DefaultAgents` and `aharness.DefaultAgents` are removed.
  The SDK ships no role definitions, so no name, description, prompt body, or
  tool grant is implicit. `DelegationConfig.Agents` stays required, and a nil or
  empty set is a `ConfigError`.
  **Migration:** supply the host's definition set. A facade host passes it in
  `DelegationConfig.Agents`; a component host that wants `<state>/agents/*.md`
  discovery seeds `agent.LoadAgents(hostDefinitions, stateDir)` and converts the
  catalog for the facade. Use `Example_delegation` in `example_test.go` as the
  starting template; it defines two roles, configures `Tools.Delegation`, and
  runs one delegated task.

### Fixed

- Fork with `Instructions` now appends to the effective recorded prompt instead
  of re-rendering the SDK prompt from scratch. A forked session whose parent
  recorded a replacement `SystemPrompt` keeps that text and gains the appended
  instructions; only a parent log that predates the recorded prompt renders the
  version's prompt as the append base.

## [v0.2.0] - 2026-11-18

This is the host-surface expansion planned after `v0.1.0`. It adds required
engine identity, root-only tool grants, host read APIs, per-child outcomes,
steering identities, catalog validation and pricing helpers, and a
serialisable event view. The module is still pre-1.0, so this minor release
contains breaking changes for direct component-package hosts and for
`Run.Steer`/`Run.SteerChild`; every item under **Changed** or **Removed** with a
**Migration** note states what to change. The session schema remains `2`,
existing logs need no migration, and there are no new dependencies.

### Added

- `aharness.EventView` and `ViewEvent` project every event to a flat,
  JSON-tagged struct with a stable `kind` discriminator, so a host can forward
  events over SSE or a structured log without a type switch. Tests cover every
  concrete event and the stable JSON shape.
- `aharness.ValidEffort` delegates to `model.ValidReasoningEffort`, so a
  facade-only settings path can validate an effort without importing the model
  package.
- `Pricing.WorstCase` returns a conservative ceiling at the highest positive
  rate, rounded up, and reports overflow/unavailability. Pricing remains
  observational, not a billing authority.
- `tools.Name*` constants and `tools.NamesFor(version, exclude...)` expose the
  stable built-in names and version-aware name lists; `NamesWithout` stays.
- `State.TotalUsage` (agent and facade) returns parent plus folded delegated
  child usage. The facade `State` also exposes the folded subagent totals.
- `session.CreateNew` creates a log with a generated canonical UUIDv7 ID;
  `Create` still takes a caller ID for forks and tests.
- `agent.AgentOverride.Clone` returns an independent copy; a reflection test
  covers every pointer field so a future field cannot be silently dropped.
- `agent.NewLimits` derives the safety margin, input budget, and compaction
  target, and `Limits.MaxOutputSet` distinguishes an omitted output budget from
  an explicit one. Facade and engine derivations now share the constructor.
- The event contract now documents root-only versus child events, the
  one-per-second `SubagentProgress` throttle, cumulative-so-far fields, the
  delegate per-task header, the display-after-return invariant, and the retry
  rule that partial text is terminal. Tests assert `RunFinished` is last and no
  display call happens after the run returns.
- `RunStarted.RunID` and `RunFinished.RunID` carry the UUIDv7 recorded in
  `run_start`, so a host can discard stale events without numbering runs.
- `Result.Reason` and `Result.Duration` (component and facade) carry the
  terminal explanation and wall-clock time, matching `run_end` and
  `RunFinished`.
- `StopReason.Failure` and `StopReason.Label` (component and facade) classify
  and label terminal statuses; `NoticeKind.Failure` classifies failure-styled
  notices. Every returned `Result.Status` is a defined, non-empty value.
- `ResponseCompleted` carries cumulative turns, usage, context occupancy,
  estimated cost, and cost completeness including the response itself, so a
  host progress view needs no separate accounting. `Turn` keeps its existing
  cumulative meaning.
- `agent.HistoryItem.NoticeKind` preserves retry, compaction, limit,
  interrupt, repair, warning, and error affinity when a transcript is replayed.
- `model.ErrAuth` (aliased as `aharness.ErrAuth`) and
  `HTTPError.Unauthorized` classify 401/403 responses, including through a
  `ProviderError`; 429 and 5xx retry behavior is unchanged.
- `model.CatalogOptions.Validate`, `model.ValidateCatalogURL`,
  `model.ValidateBaseURL`, and `CatalogConfig.Validate` validate settings
  without allocating a catalog source or an adapter. Constructors use the same
  rules.
- `model.CatalogModel.Validate`, `Catalog.Price`, and `ErrModelUnavailable`
  give a host one validation call and price provenance from the catalog value
  it already holds. `Usable` remains.
- `tools.Workspace.Contains` and `Workspace.LocalDisplayPath` apply the
  workspace containment, symlink, and `.git` policy without a second path
  implementation in the host.
- `engine.ListSummaries` (with `ListOptions`/`Summary`) lists sessions newest
  first with workspace, limit, head-read, and child filters. `agent.Summarize`
  now reports `CreatedAt`; `UpdatedAt` comes from file mtime and is advisory.
  `engine.List` remains the full-state listing.
- `agent.SteeringLine` carries a host-assigned steering ID, returned by
  `Run.Steer`/`Run.SteerChild` and echoed on `MessageRecorded.ID` and the new
  `SubagentMessageRecorded` event. Duplicate text now acknowledges distinct
  lines; child steering has an explicit ack.
- `agent.RunOptions.ResumedFromSeqSet` lets the runner derive the resume
  boundary: zero before the first recorded run, otherwise the last durable
  sequence; an explicit value still wins. The facade no longer computes it.
- `RunOptions.OnEvent` receives every display event consumed by the blocking
  `Run`, in order, without changing the result. `Start` ignores it; a blocking
  consumer should use `Start`.
- `tools.CodeDetailsTooLarge` reports a structured details payload over its
  documented cap; delegate details no longer collapse into `internal`.
- `aharness.Subagent*` constants name every `SubagentFinished.Status` and
  `ChildOutcome.Status` value. The fields remain `string` for compatibility
  with recorded data.
- `tools.ParseDelegateArgs` (with `DelegateArgs`/`DelegateTask`),
  `tools.ParseTodoDetails`, and `tools.IntentLine` expose the SDK's strict
  delegate/todo wire shapes and the spawner's one-line intent derivation so a
  host policy or pre-start roster does not mirror private structs. The
  `delegate` tool now uses the same parser.
- `Result.Children` carries one `ChildOutcome` per delegated task whose
  delegate details were folded before termination. A task canceled before its
  delegate result returned may be absent; that boundary is part of the
  contract. `SubagentFinished` gains `Error`, and a child that fails before
  its run starts now emits exactly one finished event with an empty session and
  zero counters. Delegate task details gain an optional `message` for failures.
- `engine.Host.History`, `Host.Todo`, and `Host.LastSeq` expose the durable
  transcript, restored checklist, and resume boundary without hosts touching
  `Store()`. `Store` and `WorkspaceHandle` are documented as engine wiring.
- `engine.Host.WorkDir` and `engine.Host.CleanupWork` expose and remove the
  per-session work directory. Cleanup is idle-only; the next `BuildRunner`
  recreates it. Architecture now states the implemented contract: the engine
  resets the parent work tree before a run and removes it on `Close`, while
  delegated children remove their own on child end.
- `ToolsConfig.RootGrant` narrows the tools offered and dispatched to the
  root session without changing `session_start.tools` or a child definition's
  grant. The effective root grant is recorded as the optional
  `session_start.root_tools` field (`nil` means the session grant), exposed as
  `State.RootTools`, inherited on resume/fork when omitted, and matched when
  supplied on resume. Children still derive authority from the session grant.
- `Config.AgentName` names the root agent. It is recorded in
  `session_start.agent`, passed to tool policy and tool calls, inherited by
  `Resume` and `Fork` when omitted, and exposed as `State.AgentName`.
- `engine.Spec.UserAgent` is now required host identity. When
  `Spec.ToolOptions.UserAgent` is empty the engine fills it from the spec; a
  differing value is a `ConfigError` naming `ToolOptions.UserAgent`.

### Changed

These changes need action from hosts that use the component packages directly
or that call `Run.Steer`/`Run.SteerChild`. Every item marked **Migration:** is
a breaking change in this pre-1.0 minor; the note states the replacement.

- `engine.Spec.UserAgent` is now required. `engine.Open` rejects an empty or
  whitespace-only identity with a `ConfigError`, fills
  `Spec.ToolOptions.UserAgent` when it is empty, and rejects a differing
  value.
  **Migration:** set `Spec.UserAgent` to the same product identity your model
  completer closure sends, for example `"acode/1.2.0"`. Either leave
  `Spec.ToolOptions.UserAgent` empty and let the engine fill it, or set it to
  the identical value. Facade hosts already pass the required
  `Config.UserAgent` and need no change.
- `agent.Config.Steering` is `<-chan agent.SteeringLine`, and
  `engine.BuildRunner`/`BuildRunnerWithSpawner` take that channel.
  `agent.Spawner.SteerChild` takes a `SteeringLine`, and `Run.Steer`/
  `Run.SteerChild` return `(uint64, error)`.
  **Migration:** send `agent.SteeringLine` values on the steering channel
  instead of strings, pass a `SteeringLine` to `Spawner.SteerChild`, and
  replace `err := run.Steer(text)` or `err := run.SteerChild(id, text)` with
  `_, err := ...`. Keep the returned ID when you want to match the
  `MessageRecorded.ID` or `SubagentMessageRecorded.ID` acknowledgement. A bare
  call statement still compiles. Facade callers get the ID for free.
- `agent.RunOptions.ResumedFromSeq` is derived from the log unless
  `ResumedFromSeqSet` is true.
  **Migration:** component hosts that pass an explicit `ResumedFromSeq` must
  also set `ResumedFromSeqSet: true`; otherwise the supplied value is ignored
  and the runner records zero before the first run or the previous last
  sequence after it. `Run`/`Start` facade callers need no change.
- `ModelsDevSource.Refresh` no longer returns `ErrCatalogCache` when only the
  cache write failed. It returns the fetched catalog with a `Warnings` entry
  and a nil error; fetch and decode failures remain errors.
  **Migration:** treat a nil error as "the catalog is usable" and surface
  `catalog.Warnings` to the operator instead of treating the call as failed.
  `ErrCatalogCache` is no longer produced by `Refresh`; it remains for other
  `CatalogSource` implementations that classify persistence failures, so keep
  an `errors.Is` branch only if you also use one.
- `Result`, `Usage`, `ToolCall`, and `ChildOutcome` now carry stable
  snake_case JSON tags, and `Result.Err` is `json:"-"`.
  **Migration:** if you marshal these structs yourself, update the JSON keys
  (`Status` -> `status`, `InputTokens` -> `input_tokens`, and so on) and stop
  expecting an `Err` member; use `Result.Err` in process. Prefer
  `aharness.ViewEvent` for any wire format, since its shape is the supported
  one and it carries the error text in `Error`. The Go field names are
  unchanged.
- The facade `Run` now returns `Status: StopInternalError` with the cause when
  a run cannot start, instead of an empty status.
  **Migration:** delete any host-side normalization of an empty
  `Result.Status`; every returned result now has a defined status.
- Documentation now carries the component field crosswalk for `Spec.Version` ↔
  `HarnessVersion`, `RunTimeout` ↔ `SessionTimeout`, and tri-state versus
  string effort, plus the root/child event split, the engine host read surface,
  and the new recorded `root_tools` field.

### Removed

- `engine.Spec.ExecutionGrant` and `ExecutionGrantSet` were removed in favor of
  the recorded `Spec.RootGrant`.
  **Migration:** replace `ExecutionGrant`/`ExecutionGrantSet` with
  `Spec.RootGrant`. There is no Set flag: `nil` keeps the session grant, and a
  non-nil value (including an empty slice) narrows the root and is recorded as
  `session_start.root_tools`. Facade hosts use `ToolsConfig.RootGrant`. The
  restriction is now recorded and inherited, unlike the process-only execution
  grant: resume inherits the recorded grant, and to change it use `Fork`
  rather than an explicit mismatching resume.

### Fixed

- `Pricing.Cost` now rejects the final rounded add that could wrap near
  `MaxInt64` and still report `ok=true`. A cost near the overflow boundary is
  reported as unavailable instead of a negative or wrapped estimate.

## [v0.1.0] - 2026-11-18

### Changed

- Outbound `User-Agent` values are now required host input and are sent
  verbatim. `Config.UserAgent`, `OpenAIModelOptions.UserAgent`, and
  `CatalogConfig.UserAgent` are required; the model adapters and catalog source
  reject a missing identity. The SDK no longer defaults any request to
  `aharness/<version>`; web tools return `unavailable` when the host supplies no
  identity.
- `model.NewOpenAIChat`, `model.NewOpenAIChatWithClient`,
  `model.NewOpenAIResponses`, and `model.NewOpenAIResponsesWithClient` now take
  the full `User-Agent` identity instead of a version string. A request without
  one fails with `model.ErrUserAgentRequired`.

## [v0.0.1] - 2026-11-18

The first tagged release of the aharness SDK. The module is a library: it ships
no binary, CLI, or TUI, and `go.mod` is unchanged by the release.

### Added

- Root facade `github.com/eddiectc/aharness`: `Config`, `Agent`,
  `New`/`Resume`/`Fork`, blocking `Run` and streaming `Start`, public events and
  results, model DTOs and adapters, catalog discovery, tool and delegation
  configuration, state inspection, and typed errors. The facade copies DTOs at
  the component boundary.
- Component packages `agent`, `engine`, `model`, `tools`, and `session` for
  hosts that need the runner, composition layer, canonical model and dialect
  adapters, tool registry, or durable store directly.
- Built-in tools behind one validated registry with `os.Root` workspace
  containment: `read_file`, `edit_file`, `search`, `run_command`, `web_search`,
  `web_fetch`, and `todo`, plus `delegate` when delegation is configured.
  Caller tools via `ToolsConfig.Extra` and policy via `Config.Authorize`.
- Delegation with depth one: `AgentDefinition`, `DelegationConfig`, and
  `DefaultAgents`, with child grants intersected with the parent's authority and
  `ConcurrencySafe` read-only fan-out.
- Durable v2 JSONL session log with locking, torn-line repair, replay, fork
  copy, and lock-free inspection and listing. `SchemaVersion` is `2` and is
  independent of the module version. Every event carries `harness_version`,
  recorded from `Version`.
- OpenAI-compatible Chat Completions and Responses adapters with buffered and
  SSE streaming transport, `CustomModel` for caller implementations, and
  models.dev catalog discovery with an ETag-validated cache.
- MIT license.

### Notes

- Requires Go 1.27.3 or later, matching `go.mod`.
- `Version` reports the version resolved for this module in the consuming
  build, so an embedded SDK reports its own release rather than the host
  program's version. It returns `"devel"` for an untagged or filesystem-replaced
  build, and reads build information only; it performs no I/O or network access.
- The first-party coding agent, [acode](https://github.com/eddiectc/acode), is a
  separate module and is not part of this release.
- The recorded decision was `v0.1.0` for the first tag. This release uses
  `v0.0.1` to signal a preview of the facade; the public surface may still
  change before a stable release.
- Real-provider smokes remain a release prerequisite for a stable release; the
  automated suite runs against fake providers.

[Unreleased]: https://github.com/eddiectc/aharness/compare/v0.7.2...HEAD
[v0.7.2]: https://github.com/eddiectc/aharness/compare/v0.7.1...v0.7.2
[v0.7.1]: https://github.com/eddiectc/aharness/compare/v0.7.0...v0.7.1
[v0.7.0]: https://github.com/eddiectc/aharness/compare/v0.6.0...v0.7.0
[v0.6.0]: https://github.com/eddiectc/aharness/compare/v0.5.0...v0.6.0
[v0.5.0]: https://github.com/eddiectc/aharness/compare/v0.4.2...v0.5.0
[v0.4.2]: https://github.com/eddiectc/aharness/compare/v0.4.1...v0.4.2
[v0.4.1]: https://github.com/eddiectc/aharness/compare/v0.4.0...v0.4.1
[v0.4.0]: https://github.com/eddiectc/aharness/compare/v0.3.0...v0.4.0
[v0.3.0]: https://github.com/eddiectc/aharness/compare/v0.2.0...v0.3.0
[v0.2.0]: https://github.com/eddiectc/aharness/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/eddiectc/aharness/compare/v0.0.1...v0.1.0
[v0.0.1]: https://github.com/eddiectc/aharness/releases/tag/v0.0.1
