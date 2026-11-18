# Changelog

All notable changes to this module are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html). Before `v1.0.0`, a
minor version may contain a breaking change to the public surface; those
changes are called out under **Changed** and **Removed**.

## [Unreleased]

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

[v0.2.0]: https://github.com/eddiectc/aharness/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/eddiectc/aharness/compare/v0.0.1...v0.1.0
[v0.0.1]: https://github.com/eddiectc/aharness/releases/tag/v0.0.1
