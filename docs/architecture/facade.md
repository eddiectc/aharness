# 3.2.1 Public library facade

Part of the [SDK architecture](../architecture.md) reference; this page is
normative for §3.2.1.

The module root is `package aharness`, the recommended embedding surface. It
copies DTOs at the boundary instead of aliasing component types, so component
fields remain free to change. The five component packages are public for hosts
that need the lower-level contracts; the root facade is the stable API most
callers should use. The shipped surface, by area:

- Construction: `Config`, `New`, `Resume`, `Fork`, `Agent.ID/SessionPath/
  Workspace/State/Close`. `New` requires workspace, state directory, and model;
  `Resume`/`Fork` take the caller's model implementation and inherit the
  recorded identity when `ModelConfig.Spec.ID` is empty. `Resume` rejects prompt
  and tool-contract changes; `Fork` applies explicit prompt or tool overrides
  and records `contract_override`.
- Runs: blocking `Agent.Run(ctx, task, RunOptions) (Result, error)` and
  streaming `Agent.Start(...) *Run` with `Events() iter.Seq2[Event, error]`,
  `Interrupt`, `Cancel`, `Steer`, `SteerChild`, `Done`, `Result`, and `Close`.
  `Result.Status` is authoritative and `Result.Err` is the same cause returned
  by `Agent.Run`. Breaking the iterator cancels and drains the run.
- Models: public canonical `Request`/`Response` DTOs, `Model`,
  `StreamingModel`, `ModelSpec`, `ModelConfig`, `CustomModel`, `OpenAIModel`,
  `DiscoverModel`, `ListModels`, and `CatalogListing`. The built-in adapters are
  wrappers over `model`; a caller implementation is wrapped for the
  loop. API keys are held in memory and never recorded.
- Tools: `Tool`, `ToolResult`, `ToolName`, `ToolsConfig`, `WebSearchConfig`,
  `WebFetchConfig`, `ToolRequest`, and `Deny`. The facade normalizes an explicit
  grant (enabled built-ins plus custom names, minus disabled, including
  `delegate` only with delegation), registers custom tools in a per-run
  registry, and records their schemas and `ConcurrencySafe` declarations.
- Delegation: `AgentDefinition`, `DelegationConfig`. The definition set is
  always the host's (`DelegationConfig.Agents` is required, and the SDK ships no
  role definitions); the facade takes definitions and a resolver, and it never
  reads `<state>/agents`. Discovery is the host-facing
  `agent.LoadAgents(base, stateDir)` helper: it validates the host's base
  entries (`SourceDefault`, empty `Path`) and merges `<state>/agents/*.md`
  (`SourceUser`, file `Path`) in file-name order, with a file replacing a base
  entry of the same name. A definition grant may name a builtin tool of the
  current toolset; `agent.LoadAgentsWithTools` and
  `agent.ParseAgentDefinitionWithTools` also accept the host's registered custom
  tool names, so a discovered role can grant a host tool. A rejected custom name
  is dropped with a warning, and the definition that names it fails validation
  with an actionable error rather than an empty grant.
  Child grants are intersected with the parent's authority, a child can never
  delegate, and `ConcurrencySafe` extends concurrent child fan-out only.
  `DelegationConfig.ObserveChild` opts into a per-child display stream: it
  receives one `ChildTarget` per child that starts a run and returns that
  child's sink. The sink is optional and additive; the `Subagent*` roster
  events keep arriving on the run's own stream.
- State and discovery: `State`, `SessionInfo`, `InspectSession`, `Sessions`,
  `SchemaVersion`, `Version`, sentinel errors, `ConfigError`, and
  `ProviderError`. `InspectSession` canonicalizes the ID and reads a snapshot
  without the exclusive lock; `Sessions` lists newest first and tolerates a live
  writer. `Transcript` reads the display transcript of any session - root,
  delegated child, or fork - from the same lock-free snapshot, and
  `Agent.Transcript` is that read for the live root session.

**Field-level contract.** The exported surface is listed here; the `go doc`
comment on a field narrows these rules but never widens them.

| Group | Types and fields |
|---|---|
| Construction | `Config`: `Workspace`, `StateDir`, `Model`, `AgentName`, `UserAgent`, `Identity`, `PromptTag`, `Tools`, `Limits`, `MaxTurns`, `MaxTotalTokens`, `RunTimeout`, `ModelTimeout`, `ThinkingEffort`, `Instructions`, `SystemPrompt`, `Authorize`, `RequireUsage`, `LogModelRequests`, `Logger` |
| Lifecycle | `Agent`: `ID`, `SessionPath`, `Workspace`, `State`, `Transcript`, `Run`, `Start`, `Close`; `RunOptions`: `Model`, `ThinkingEffort`, `MaxTurns`, `MaxTotalTokens`, `Timeout`, `OnEvent`; `Run`: `Events`, `Interrupt`, `Cancel`, `Steer`, `SteerChild`, `Done`, `Result`, `Close` |
| Outcome | `Result`: `Status`, `Session`, `Err`, `FinalText`, `RunTurns`, `RunUsage`, `CumulativeTurns`, `CumulativeUsage`, `ContextTokens`, `RunCostMicroUSD`, `CostMicroUSD`, `RunCostComplete`, `CostComplete`, `SubagentTurns`, `SubagentUsage`, `SubagentCostMicroUSD`, `SubagentCostComplete`, `Children`, `Reason`, `Duration`; `ChildOutcome`: `Agent`, `Session`, `CallID`, `Intent`, `Status`, `Turns`, `ContextTokens`, `Usage`, `CostMicroUSD`, `CostComplete`, `Error`; `StopReason` helpers `Failure`, `Label` |
| Session state | `State`: `ID`, `Workspace`, `StateDir`, `Model`, `AgentName`, `SystemPrompt`, `AppendedPrompt`, `PromptCapped`, `PromptTag`, `Tools`, `RootTools`, `CustomTools`, `ConcurrencySafe`, `ContractOverride`, `Limits`, `Turns`, `Usage`, `ContextTokens`, `SubagentTurns`, `SubagentUsage`, `SubagentCostMicroUSD`, `SubagentCostComplete`, `CostMicroUSD`, `CostComplete`, `ThinkingEffort`, `LastRunStatus`, `Pending`, `Parent`, `CreatedAt`, `UpdatedAt`, `TotalUsage`; `SessionInfo`: `ID`, `Workspace`, `Model`, `FirstMessage`, `CreatedAt`, `UpdatedAt`, `Turns`, `Status`, `Parent`; `Limits`: `MaxContextTokens`, `MaxOutputTokens`, `SafetyMargin`, `MaxInputTokens`, `CompactionTarget`; `PendingCall`: `CallID`, `Name`, `Turn`; `ParentRef`: `Session`, `Seq`, `CallID`; `ContractOverride`: `ParentSession`, `Prompt`, `Tools`; `TranscriptItem`: the same fields as `agent.HistoryItem` with `TranscriptKind`, `NoticeKind`, `StopReason`, and the public `Usage`; `Transcript(ctx, stateDir, sessionID) ([]TranscriptItem, error)` |
| Events | `EventMeta`: `Time`, `Session`, embedded in each event; `RunStarted`: `RunID`, `Turn`, `ResumedFromSeq`, `Model`, `Dialect`, `ThinkingEffort`; `TextDelta` and `ReasoningDelta`: `Text`; `ResponseCompleted`: `Turn`, `Text`, `Reasoning`, `ToolCalls`, `FinishReason`, `Usage`, `Latency`, `Streamed`, `CumulativeTurns`, `CumulativeUsage`, `ContextTokens`, `CostMicroUSD`, `CostComplete`; `ToolStarted`: `Turn`, `Call`; `ToolCompleted`: `Turn`, `CallID`, `Name`, `OK`, `Code`, `Output`, `Error`, `ExitCode`, `Duration`, `TimedOut`, `Truncated`, `Details`; `MessageRecorded`: `Turn`, `ID`, `Source`, `Text`; `SubagentMessageRecorded`: `Turn`, `CallID`, `Index`, `Agent`, `ChildSession`, `ID`, `Source`, `Text`; `Notice`: `Turn`, `Kind`, `Text`; `SubagentStarted`: `Turn`, `CallID`, `Index`, `Agent`, `Intent`, `ChildSession`, `Model`, `Effort`, `Tools`; `SubagentProgress`: `Turn`, `CallID`, `Index`, `Agent`, `ChildSession`, `ChildTurns`, `ContextTokens`, `Usage`, `CostMicroUSD`, `CostComplete`, `LastTool`; `SubagentFinished`: `Turn`, `CallID`, `Index`, `Agent`, `ChildSession`, `Status`, `ChildTurns`, `ContextTokens`, `Usage`, `CostMicroUSD`, `CostComplete`, `Duration`, `Error`; `RunFinished`: `RunID`, `Result`, `Reason`, `Duration`; `EventView`: a flat JSON projection with `Kind`; `ViewEvent(Event) EventView`; `EventKind*` constants |
| Models | `Model`: `Complete`; `StreamingModel`: `Complete`, `CompleteStream`; `StreamHandler`: `Text`, `Reasoning`; `Request`: `Model`, `Messages`, `Tools`, `MaxOutputTokens`, `ThinkingEffort`; `Response`: `Text`, `ToolCalls`, `FinishReason`, `Usage`, `Reasoning`, `Streamed`; `Message`: `Role`, `Content`, `ToolCalls`, `ToolCallID`, `Name`, `Images`; `ImagePart`: `MediaType`, `Data`, `Width`, `Height`; `ToolCall`: `ID`, `Name`, `Arguments`, `Intent`, `Session`, `Agent` |
| Model identity | `ModelSpec`: `ID`, `Provider`, `Dialect`, `BaseURL`, `ContextWindow`, `MaxOutputTokens`, `InputLimit`, `ThinkingEfforts`, `Pricing`, `ImageInput`; `ModelConfig`: `Spec`, `Completer`, `NewCompleter`; `Pricing`: `Available`, `Currency`, `Unit`, `InputMicroUSD`, `OutputMicroUSD`, `CacheReadMicroUSD`, `CacheWriteMicroUSD`, `Source`, `FetchedAt`; `Usage`: `InputTokens`, `OutputTokens`, `CachedInputTokens`, `CacheWriteInputTokens`, `ReasoningOutputTokens`, `Reported` |
| Discovery | `OpenAIModelOptions`: `Spec`, `APIKey`, `UserAgent`, `HTTPClient`, `Timeout`; `DiscoverModelOptions`: `Catalog`, `Provider`, `Model`, `APIKey`, `BaseURL`, `HTTPClient`, `Timeout`; `CatalogConfig`: `URL`, `StateDir`, `HTTPClient`, `TTL`, `Timeout`, `UserAgent`, `Validate`; `CatalogModel`: `ID`, `Name`, `Provider`, `Dialect`, `BaseURL`, `ContextWindow`, `InputLimit`, `MaxOutputTokens`, `ToolCall`, `Reasoning`, `ThinkingEfforts`, `ImageInput`, `Deprecated`, `Status`, `Unavailable`, `Pricing`, `Validate`; `Catalog`: `Lookup`, `Price`, `Usable`, `SortedModels`; `CatalogListing`: `Provider`, `FetchedAt`, `Models`, `Warnings`; helpers `ValidateCatalogURL`, `ValidateBaseURL`, `CatalogOptions.Validate` |
| Tools | `Tool`: `Name`, `Description`, `InputSchema`, `Run`, `ConcurrencySafe`; `ToolResult`: `OK`, `Code`, `Output`, `Error`, `ExitCode`, `TimedOut`, `Truncated`, `Details`, `Image`; `ToolSchema`: `Name`, `Description`, `Parameters`; `ToolsConfig`: `Disable`, `RootGrant`, `Extra`, `WebSearch`, `WebFetch`, `Delegation`; `ToolRequest`: `Tool`, `Call`, `Intent`, `Turn`, `Session`, `Agent`, `Path`; `Deny`: `Reason`; `WebSearchConfig`: `BaseURL`, `Timeout`; `WebFetchConfig`: `Provider`, `Endpoint`, `Key`, `Timeout`, `UserAgent`, `AllowedOrigins`; helpers `TextResult`, `ErrorResult`, `Name*`, `NamesFor`, `ParseDelegateArgs`, `ParseTodoDetails`, `IntentLine` |
| Delegation | `AgentDefinition`: `Name`, `Description`, `Prompt`, `Tools`, `Model`, `ThinkingEffort`, `MaxTurns`; `DelegationConfig`: `Agents`, `MaxSubagents`, `ResolveModel`, `ObserveChild`; `ChildTarget`: `CallID`, `Index`, `Agent`, `Session`, `Intent`, `Model`, `Effort`, `Tools` |
| Enumerations | `Role`: `RoleSystem`, `RoleUser`, `RoleAssistant`, `RoleTool`; `FinishReason`: `FinishStop`, `FinishLength`, `FinishToolCalls`, `FinishContentFilter`, `FinishOther`; `StopReason`: `StopCompleted`, `StopMaxTurns`, `StopTokenLimit`, `StopTimeLimit`, `StopProviderError`, `StopProtocolError`, `StopStuck`, `StopInterrupted`, `StopInternalError`; `Dialect`: `DialectChat`, `DialectResponses`, `DialectCustom`; `ToolName`: the eight built-in names plus `ToolDelegate`; `NoticeKind`: `NoticeRetry`, `NoticeCompaction`, `NoticeLimit`, `NoticeInterrupt`, `NoticeRepair`, `NoticeWarning`, `NoticeError`, `Failure`; `Subagent*` status constants; `WebFetchProvider`: `WebFetchBuiltin`, `WebFetchCrawl4AI`; `EventKind*`; `TranscriptKind`: `TranscriptRun`, `TranscriptUser`, `TranscriptAssistant`, `TranscriptTool`, `TranscriptNotice` |
| Errors and versions | Sentinels `ErrClosed`, `ErrBusy`, `ErrEmptyTask`, `ErrIteratorConsumed`, `ErrSessionNotFound`, `ErrSessionLocked`, `ErrInvalidSessionID`, `ErrModelRequired`, `ErrToolConflict`, `ErrToolMismatch`, `ErrSteeringFull`, `ErrNoSuchChild`, `ErrAuth`, `ErrModelUnavailable`; `ConfigError`: `Field`, `Value`, `Detail`; `ProviderError`: `StatusCode`, `Retryable`, `Err`; `SchemaVersion`, `Version()` |

Behavioral rules that the field lists do not show:

- `Config.Workspace` is required for `New` and inherited when empty on
  `Resume`/`Fork`; `StateDir` and `Model` are always required; a zero
  `Model.Spec` inherits the recorded identity. `Config.UserAgent` is required
  for every construction; `Config.AgentName` is recorded and inherited. `MaxTurns` and `MaxTotalTokens`
  are cumulative across the agent's runs and are not inherited from the log;
  `RunTimeout` bounds one run and `ModelTimeout` one model attempt.
  `ThinkingEffort` uses nil to inherit, `""` to clear, and a value to set.
  `Config.Identity` defaults to `aharness` when empty or whitespace, is limited
  to 64 bytes of valid UTF-8 with no control characters, and is not recorded, so
  it never appears in `State`; supply it on every construction, including
  `Resume` and `Fork`.
  `Config.PromptTag` is optional attribution for the host prompt template: empty
  means unset, otherwise it is at most 64 bytes of printable ASCII matching
  `[A-Za-z0-9._:/-]+`. It is recorded in `session_start` beside the SDK
  `prompt_version`, is exposed as `State.PromptTag`, and never affects
  rendering, caps, or replay. A new session records the explicit tag. `Resume`
  keeps the recorded tag and rejects a different explicit tag with a
  `ConfigError`; an equal tag is accepted. `Fork` prefers an explicit tag and,
  with none, inherits the parent tag only when the resulting prompt is
  unchanged; a changed prompt records no tag. A delegated child's prompt is not
  the host template, so a child never records its parent's tag.
  `Instructions` and `SystemPrompt` are mutually exclusive and invalid on
  `Resume`. `Instructions` composes host text after the SDK body and is
  recorded once as `session_start.appended_prompt`; every fresh child composes
  it into its prompt too, including children of a resumed or forked session,
  and a continued child keeps it through its recorded prompt. The SDK does not
  discover workspace files; the host reads the file and supplies the bytes. A
  nil `Logger` discards.
- `Config.Limits` caps one model call. The context window, safety margin, input
  limit, and compaction target derive from `Model.Spec`; the output budget is a
  host choice, so a model's advertised output ceiling is never sent and a zero
  `MaxOutputTokens` omits `max_tokens`/`max_output_tokens` from the request
  entirely. A negative `MaxOutputTokens` is a `ConfigError`. On `Resume`/`Fork`
  a non-zero `Limits` is an explicit override that replaces the recorded
  per-call limits, so `MaxOutputTokens` 0 clears an inherited budget, while
  all-zero `Limits` with an empty `Model.Spec.ID` inherit the recorded limits;
  an explicit `Model.Spec.ID` supplies the context window and input limit
  instead.
- `RunOptions` applies to one run and never rewrites the session; its `Timeout`
  replaces `Config.RunTimeout`. `RunOptions.OnEvent` observes the events the
  blocking `Run` consumes, in order, and never changes the result.
  `Run.Events` applies backpressure, can be consumed once, and breaking the
  iterator cancels and drains the run. `Interrupt` keeps the run alive;
  `Cancel` ends it; `Steer` and `SteerChild` enqueue input at turn boundaries
  and return run-unique steering IDs echoed on `MessageRecorded.ID` and
  `SubagentMessageRecorded.ID`.
- `Result.Status` is authoritative and `Result.Err` is the same cause
  `Agent.Run` returns. `EventMeta.Time` is observation time, not a durable
  timestamp; events are display-only and the session log is the only durable
  truth.
- `Pricing` is an observational snapshot and never a billing authority.
  `Pricing.WorstCase` is a conservative projection, not a billing guarantee;
  `Usage.Reported` distinguishes provider-reported totals from estimates.
- `ToolsConfig.RootGrant` narrows only the root's schemas and dispatch, so a
  narrow orchestrator can delegate to broader analysts without widening the
  session. The session grant (`session_start.tools`) remains the authority for
  child intersection; `session_start.root_tools` records the root grant, and nil
  means the session grant.
- `Transcript` and `Agent.Transcript` read a byte snapshot of the log without
  the session lock or the in-memory event slice, so both tolerate a live writer
  and a torn final line. `Transcript` accepts a root, child, or fork session ID;
  an unusable ID is `ErrInvalidSessionID` and a missing log `ErrSessionNotFound`.
  `Agent.Transcript` returns `ErrClosed` after `Close`.
- The engine host reads a session without touching `Store`:
  `History` reconstructs the transcript, `Todo` restores the checklist,
  `LastSeq` returns the resume boundary, `WorkDir` names the per-session work
  directory, and `CleanupWork` removes it while idle. `ListSummaries` lists
  sessions from log heads with workspace, limit, head-size, and child filters;
  `List` remains the full-state listing.
- Direct engine hosts must set `Spec.UserAgent`; the engine fills
  `ToolOptions.UserAgent` and rejects a mismatch. The model completer closure
  still captures the same identity because the engine cannot inspect its
  headers.
- `Authorize` errors deny with a `permission_denied` result unless they are
  context cancellations. `Deny.Reason` is shown to the model.
- `Tool.Run` and `Authorize` receive the calling session in `ToolCall.Session`
  and the delegated definition name in `ToolCall.Agent` (empty at the session
  root). `Session` is the unique identity because one definition may run more
  than once. Both fields are empty on model request, response, and event DTOs.
- `ToolRequest.Path` is the absolute, lexically cleaned target of `read_file`,
  `read_image`, `edit_file`, or `search`; a relative argument is joined to the
  workspace and `search` defaults to the workspace root. It is empty for every
  other tool and when the arguments carry no usable path. It is not
  symlink-resolved, and `run_command` has no `Path` because its writes are not
  constrained by `cwd`. The built-in tools still perform containment and symlink
  checks; `Path` is policy input, not validation.
- `Tool.ConcurrencySafe` remains a static, recorded grant-level declaration.
  The spawner chooses concurrent or sequential execution from the child grant
  before any child tool call exists, so an argument-dependent predicate could
  not affect the choice. A tool that shares resources across concurrently
  running children must serialize internally or declare false.
- `Version` reports the version resolved for this module in the build that
  contains it: the dependency version when the SDK is embedded, and the main
  module version when the SDK is the main module. It returns `"devel"` when the
  build carries no SDK version, including a filesystem `replace` with no
  version. The value comes from build information already embedded in the
  binary, so it costs no I/O or network access; it is recorded as
  `harness_version` in new sessions. It is not used as a `User-Agent`; the host
  supplies that identity through `Config`, `OpenAIModelOptions`, and
  `CatalogConfig`.

**Compatibility rules.** These constrain every future change to the facade:

- Adding a field to `Config`, `RunOptions`, `ModelSpec`, `Tool`, `ToolResult`,
  `ToolCall`, `ToolRequest`, `State`, `Result`, or an event struct is backward
  compatible. Adding a concrete event type is also compatible; consumers keep a
  default branch.
- Adding a method to `Model` or `StreamingModel` is a breaking change; both are
  caller-implemented interfaces. A new capability is a new optional interface or
  a `Config` field. `StreamHandler` is a struct, so an added callback field is
  additive.
- Renaming or removing an exported identifier, or changing an exported method
  signature, is breaking. Before `v1.0.0`, a minor release may contain such a
  change, including on the root facade; it is listed under **Changed** or
  **Removed** in `CHANGELOG.md` with a migration step, and a patch release
  never breaks the surface. From `v1.0.0`, such a change requires a major
  module version. `SchemaVersion` is independent of the module version and
  stays 2 across library minor versions.
- A session that records `custom_tools` cannot be resumed without matching
  `Tools.Extra`; it fails with `ErrToolMismatch` rather than replaying a
  different contract. A recorded toolset version the binary does not support is
  rejected, not approximated.

**Large child reports.** The 16 KiB task and child-report caps are part of the
parent-context budget and stay fixed. To return more, the child writes a
host-routed report file and returns its path in the bounded report:

1. Register a custom `write_report` tool whose `Run` derives the target from
   `call.Session` (and optionally `call.Agent`) under a host-owned directory,
   writes it, and returns a short result naming the workspace-relative path.
   The path must not come from model-supplied arguments.
2. Grant the tool only to the child definitions that need it.
3. Keep the delegated task and the child's final report at or below 16 KiB; the
   report names the file and carries the summary.
4. Read the file with `read_file` (60 KiB per page with `offset` continuation)
   or host-side. A file under `Workspace` is reachable by the built-in tool; a
   file under `StateDir` is not when the state directory is outside the
   workspace.

The report file is auxiliary. The child session log is the durable record, and
the `delegate` result still carries only the bounded report into the parent
context. A per-session report path lets `write_report` declare
`ConcurrencySafe` for concurrent children; a shared ledger needs internal
locking or a false declaration.

**State directory inside the workspace.** The default state directory is
outside `Workspace`. A host may colocate it, for example as
`<workspace>/.aharness`, so the logs are visible to another workspace tool. The
built-in file tools are workspace-contained, so the agent can then reach its own
logs. Deny writes with the normalized policy path:

```go
stateDir := filepath.Join(workspace, ".aharness")
authorize := func(ctx context.Context, req aharness.ToolRequest) error {
    if req.Tool == aharness.ToolEditFile && within(stateDir, req.Path) {
        return &aharness.Deny{Reason: "the state directory is read-only to the agent"}
    }
    return nil
}

func within(dir, path string) bool {
    rel, err := filepath.Rel(dir, path)
    return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

Deny `read_file` and `search` as well when session logs must not be readable.
`Path` is lexical, not symlink-resolved; `edit_file` independently rejects
symlink components. This is policy, not a sandbox: `run_command` has no `Path`
and is not confined, so deny `run_command` or isolate the process when writes
must be impossible. The log supports debugging and resume; it is not a
tamper-proof audit trail against a same-UID process.

The agent MAY define unexported consumer interfaces (`completer`, `toolset`,
`eventStore`) so loop tests use fakes. Keep each interface to one or two
methods; split append/sync from read/close rather than defining one wide
`eventStore`. Production wires the selected dialect adapter
(`*model.OpenAIChat` or `*model.OpenAIResponses`), `*tools.Registry`, and
`*session.Store`. Keep consumer interfaces unexported; do not export them solely for tests.
