# Changelog

All notable changes to this module are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html). Before `v1.0.0`, a
minor version may contain a breaking change to the public surface; those
changes are called out under **Changed** and **Removed**.

## [Unreleased]

### Added

- `aharness.EventView` and `ViewEvent` project every event to a flat,
  JSON-tagged struct with a stable `kind` discriminator, so a host can forward
  events over SSE or a structured log without a type switch. Tests cover every
  concrete event and the stable JSON shape.
- `aharness.ValidEffort` delegates to `model.ValidReasoningEffort`, so a
  facade-only settings path can validate an effort without importing the model
  package.
- `Pricing.WorstCase` returns a conservative ceiling at the highest positive
  rate, rounded up, and reports overflow/unavailability. `Pricing.Cost` now
  rejects the final-add overflow that previously wrapped near `MaxInt64`.
  Pricing remains observational, not a billing authority.
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

- `agent.Spawner.SteerChild` takes an `agent.SteeringLine` instead of a string,
  and `engine.Spec.ExecutionGrant`/`ExecutionGrantSet` were replaced by the
  recorded `Spec.RootGrant`. Direct component hosts must update.
- `EventView`'s nested `Result`, `Usage`, `ToolCall`, and `ChildOutcome` values
  use stable snake_case JSON tags; `Result.Err` is omitted and surfaced as the
  view's top-level `error` text. These DTOs are otherwise unchanged.
- Documentation now carries the component field crosswalk for `Spec.Version` ↔
  `HarnessVersion`, `RunTimeout` ↔ `SessionTimeout`, and tri-state versus
  string effort, plus the root/child event split, the engine host read surface,
  and the new recorded `root_tools` field.
- `ModelsDevSource.Refresh` now returns a usable catalog with a warning, and a
  nil error, when only cache persistence failed. Fetch and decode failures
  remain errors; this matches `Load`'s existing cache-write warning.
- `agent.Config.Steering` is now `<-chan agent.SteeringLine`, and
  `engine.BuildRunner*` takes the same channel. `Run.Steer` and
  `Run.SteerChild` return `(uint64, error)`. A bare call statement still
  compiles; callers that assigned the single error must update.
- Direct `engine` hosts must now set `Spec.UserAgent`; `engine.Open` rejects an
  empty identity with a `ConfigError`. The model completer closure still
  captures the same identity independently because the engine cannot inspect
  its outbound headers.

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

[v0.1.0]: https://github.com/eddiectc/aharness/compare/v0.0.1...v0.1.0
[v0.0.1]: https://github.com/eddiectc/aharness/releases/tag/v0.0.1
