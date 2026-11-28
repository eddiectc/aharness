# 3. Packages and contracts

Part of the [SDK architecture](../architecture.md) reference; this page is
normative for §3.1, §3.2, and §3.3 except §3.2.1.

## 3.1 Package layout

| Path | Responsibility |
|---|---|
| `aharness` (module root) | Public high-level facade: `Config`, `Agent`, `New`/`Resume`/`Fork`, runs, events, results, state, model and tool DTOs, OpenAI/catalog helpers, delegation, and errors. Copies DTOs at the boundary; imports the component packages only. |
| `agent/` | Runner, run lifecycle, turn execution, stop policy, retry policy, display events, history reconstruction, operation interrupts, state reconstruction, repeated-failure detection, projection/compaction, agent definitions and prompt templates, the subagent spawner, tool policy, and custom-tool projection. |
| `engine/` | Host-agnostic composition layer: new/resume/fork resolution, workspace/state/work-dir setup, `session_start` recording, fork contract overrides, per-run registry/spawner/runner wiring, and lock-free session inspection/listing. |
| `model/` | Canonical types; Chat Completions and Responses adapters; buffered and SSE transport; shared HTTP plumbing; typed transport errors; models.dev catalog discovery, cache, and SDK-to-dialect mapping. |
| `tools/` | Registry, argument validation, result shape, workspace containment policy, the seven workspace/web tools, and the `delegate` spawn interface. |
| `session/` | Event envelope, v2 JSONL store, locking, torn-line repair, fork copy, replay-ready reader. |

The root facade and the five component packages are the SDK. The root is the
recommended embedding API and the stable public facade; the component packages
are the lower-level API used by the first-party coding agent (acode) and by
advanced hosts that need the engine, display events, history, catalog, or
session store directly. The SDK contains no CLI, TUI, flags, environment
parsing, config file, terminal I/O, signals, or `os.Exit`; acode owns those.

Dependency direction:

- `session` is a leaf.
- `model` and `tools` import `session` only where needed and never `agent`,
  `engine`, the root package, or an application.
- `agent` imports `model`, `tools`, and `session`.
- `engine` composes `agent`, `model`, `tools`, and `session`.
- The root facade imports the component packages; no component imports the
  root.
- No package imports an application package. A host depends on the SDK, never
  the reverse.

Dependencies stay deliberately small: the Go standard library by default, plus
`github.com/markusmobius/go-trafilatura/v2` for `web_fetch` content extraction
and its `golang.org/x/net` parser dependency. The TUI stack is not an SDK
dependency; it belongs to acode. The dependency justification rules and the
measured binaries are in [coding-standard.md §3](../coding-standard.md#3-dependencies).

```text
go.mod
aharness.go     # Config, Agent, New/Resume/Fork, lifecycle
run.go          # Run, RunOptions, event iterator, interrupt/cancel/steer
events.go       # public Event interface and concrete events
result.go       # Result, StopReason, Usage, Limits, State, SessionInfo
model.go        # public model DTOs, Model/StreamingModel, OpenAI and catalog helpers
tool.go         # Tool, ToolResult, ToolName, ToolsConfig, policy
delegate.go     # AgentDefinition, DelegationConfig
errors.go       # sentinels and typed errors
doc.go          # package documentation and boundary
version.go      # Version()
agent/
  runner.go     # run lifecycle, limits, steering, turn dispatch, stop policy
  turn.go       # one model call, completion handling, overflow retry
  tool_exec.go  # sequential tool execution, policy, panic guard, result events
  session_log.go # event recording and the terminal result
  session_start.go # once-per-session start event
  display.go    # display-only event contract
  history.go    # capped transcript reconstruction
  state.go      # durable state reconstruction and pending calls
  events.go     # event payloads and caps
  context.go    # projection, token estimate, compaction
  agents.go     # strict definition parser, host-base and user-file discovery, precedence
  prompt.go     # frozen prompt versions, environment block, catalog
  spawn.go      # subagent spawner: options, steering, child history
  spawn_grants.go # grant narrowing, read-only fan-out, child budgets
  spawn_child.go # child session lifecycle, configs, runner wiring
  spawn_display.go # child display forwarding and observation sinks
  retry.go      # retry policy and streaming-aware model calls
  types.go
engine/
  engine.go     # Spec, Host, mode, config errors
  open.go       # new-session creation and input validation
  resume.go     # resume/fork resolution
  inherit.go    # inheritance matrix, limit clamp, effort resolution
  contract.go   # tool contract validation and grant equality
  runner.go     # per-run registry, spawner, and runner construction
  sessions.go   # lock-free Inspect/List snapshots
  workdir.go    # per-session work directory lifecycle
model/
  types.go            # canonical types, StreamCompleter
  catalog.go          # catalog types, lookup/usable filtering, cache path, source contract
  catalog_source.go   # models.dev decode/normalize/eligibility, HTTP load/revalidate/refresh, cache
  openai_chat.go      # Chat Completions buffered + SSE adapter
  openai_responses.go # Responses buffered + SSE adapter
  http.go             # shared request, body cap, SSE decoder, errors
  errors.go
tools/
  registry.go
  root.go        # os.Root wrapper and path policy
  read.go
  image.go       # read_image: sanitizing decode, downscale, JPEG quality ladder
  edit.go
  search.go
  web_search.go  # SearXNG JSON client, base-URL validation, dedupe, sanitization
  web_fetch.go   # tool construction, dispatch, target validation, fetch
  web_fetch_transport.go # HTTP request, redirects, response reading, limits
  web_fetch_addr.go # address guard and allowed-origin policy
  web_origin.go  # allowed-origin parsing and validation
  web_fetch_content.go # content classification and charset decoding
  web_fetch_result.go # results, details, paging, error mapping
  web_fetch_provider.go  # fetcher interface, provider selection, endpoint validation
  web_fetch_crawl4ai.go  # Crawl4AI /crawl adapter and streaming decode
  web_extract.go # trafilatura extraction, DOM fallback, challenge detection, sanitization
  command.go
  command_spill.go # per-run oversized-output capture
  command_unix.go
  todo.go       # per-run checklist state and validation
  delegate.go   # delegate arguments, spawn interface, report caps and marking
  schemas.go    # frozen v0/v1 sets and the current v2 set
  output.go
session/
  event.go       # v2 envelope and event types
  reader.go      # replay, validation, torn-line offsets
  store.go       # create/open/lock/repair/copy/append
  lock_unix.go   # flock helpers
  errors.go
```

The root facade plus the five component packages is the normative layout.
`config` belongs to the host, projection and durable-state interpretation live in
`agent`, and workspace policy lives in `tools`. `session` remains a leaf; event
payloads are `json.RawMessage`, decoded by their producers. `agent` is the only
orchestrator. Add a package only when a second consumer or a clear boundary
justifies it; do not add `pkg/`, `util`, `common`, or `types`.

**Component field crosswalk.** The facade and the component packages name the
same inputs differently by design; a component host should use its package's
name, not assume the facade's shape:

| Concept | Facade | Component |
|---|---|---|
| Harness/build version | `Config` uses `Version()` internally | `engine.Spec.Version` ↔ `agent.Config.HarnessVersion` |
| Per-run wall clock | `Config.RunTimeout` / `RunOptions.Timeout` | `engine.Spec.RunTimeout` / `RunSpec.RunTimeout` ↔ `agent.Config.SessionTimeout` |
| Thinking effort | `Config.ThinkingEffort` (`*string`, tri-state) | `engine.Spec.ThinkingEffort` (`*string`) ↔ `agent.Config.ThinkingEffort` (`string`, empty means default) |
| Image capability | `ModelSpec.ImageInput` (`*bool`, nil means unknown); `RunOptions.Model` re-resolves it per run | `engine.RunSpec.ImageInput` (`*bool`, applied when `Model` is set) ↔ `tools.Options.ImageInput` / `tools.Registry.SetImageInput` ↔ `agent.ModelResolution.ImageInput` for a child |
| Host identity | `Config.UserAgent` | `engine.Spec.UserAgent` (required); `tools.Options.UserAgent` is filled from it |
| Prompt identity | `Config.Identity` | `engine.Spec.Identity` ↔ `agent.Config.Identity`; defaults to `aharness`, 64 bytes with no control characters, not recorded |
| Prompt tag | `Config.PromptTag` | `engine.Spec.PromptTag` ↔ `agent.Config.PromptTag` ↔ recorded `session_start.prompt_tag`; optional, at most 64 printable ASCII bytes matching `[A-Za-z0-9._:/-]+`, never a replay key |
| Prompt append | `Config.Instructions` | `engine.Spec.AppendPrompt` ↔ `agent.Config.AppendedPrompt` ↔ recorded `session_start.appended_prompt`; provenance only, never a replay key, carried to fresh children; the `agent` layer never appends the text |
| Root tool offer | `ToolsConfig.RootGrant` | `engine.Spec.RootGrant` ↔ recorded `session_start.root_tools` |
| Agent name | `Config.AgentName` | `engine.Spec.Agent` ↔ `agent.Config.Agent` |

Effort type unification and field renames are deliberately deferred; this table
is the contract for hosts that mix levels today.

## 3.2 Core contracts

- `model.CatalogSource` — the discovery seam. `Load(ctx, provider)` honors the
  24-hour cache TTL and performs a conditional GET when stale;
  `Revalidate(ctx, provider)` bypasses the TTL but still falls back to a stale
  valid cache; `Refresh(ctx, provider)` performs an unconditional GET with no
  stale fallback. `Load` backs implicit resolution, `Revalidate` backs an
  explicit listing action, and `Refresh` backs a forced host refresh. A returned `model.Catalog` carries normalized entries, the source
  URL, `FetchedAt`, the ETag, and non-fatal `Warnings`; `Lookup` returns
  `ErrModelNotFound`, and `Usable` filters unavailable entries and sorts by ID.
  The host constructs the models.dev implementation with the HTTP client, state
  directory, clock, and TTL; tests inject a fake `CatalogSource` or an
  `httptest` server.
- `model.OpenAIChat` and `model.OpenAIResponses` — concrete dialect adapters
  with `Complete(ctx, model.Request) (model.Response, error)`. `model.Request`
  carries the projected messages with the versioned system prompt first, tool
  schemas, an optional `max_output_tokens` (zero omits the wire field), an
  optional `thinking_effort` (empty omits the wire field and means the
  provider/model default), and model name. `model.Response` carries assistant
  text, display-only thinking text, zero or more tool calls (canonical call ID,
  name, raw JSON arguments), a normalized finish reason (`stop`, `length`,
  `tool_calls`, `content_filter`, or `other`), usage, and display-only
  `Reasoning`/`Streamed` fields. Adapters guarantee a
  non-empty call ID; for a dialect that supplies IDs, a missing or duplicate ID
  is `protocol_error` and is not repaired. Typed errors (`HTTPError`,
  `TransportError`, `ProtocolError`, `PartialError`, and context overflow) keep
  retry policy in `agent`. The host resolves the catalog entry to its dialect,
  base URL, and limits and wires the corresponding adapter. `WithSessionID` and
  `WithUserAgent` return copied adapters; a host constructs the adapter with its
  product identity or supplies one through `WithUserAgent`. The SDK never
  substitutes its own identity, and a request without one fails with
  `ErrUserAgentRequired`.
- `model.StreamCompleter` — optional `CompleteStream(ctx, req, StreamHandler)`
  contract. The handler receives display-only text and thinking deltas; the
  returned `Response` is authoritative. The agent falls back to `Complete` when
  an adapter does not implement it.
- `tools.Registry` — concrete registry with `Schemas() []model.ToolSchema`,
  `Execute(ctx, name, args) tools.Result`, and
  `ExecuteCall(ctx, tools.Call, name, args) tools.Result`. The dispatcher
  validates arguments and converts errors into `tools.Result`; a panic is
  paired with a result and then stops the run with `internal_error`. `SetGrant`
  narrows the registry to a recorded tool-name allowlist; `Schemas` returns only
  granted declarations and `Execute` rejects an ungranted name with
  `unknown_tool` without running it, so a grant is enforced twice. `Call`
  carries the parent tool-call ID (recorded as a child's `parent_call_id`), the
  parent turn, and the parent's cumulative usage. `NormalizeImage` applies the
  `read_image` sniffing, source caps, downscale, and delivered cap to caller
  bytes, and `MaxImageBytes` is that delivered cap. The package exports sentinel
  errors for caller decisions: `ErrOutsideWorkspace`, `ErrGitProtected`,
  `ErrEditAmbiguous`, `ErrEditNotFound`, `ErrBinaryFile`, `ErrInvalidImage`,
  `ErrNoImageSupport`, and `ErrTooLarge`.
- `tools.Spawning` — the `delegate` seam:
  `Spawn(ctx, Call, []SubagentSpec) SpawnResult` plus `Agents() []AgentInfo`.
  `SpawnResult` carries one report per task in array order plus the call's
  `Execution` mode and, when sequential, a reason. `agent.Spawner` implements it
  and owns the child stores. A nil implementation keeps `delegate` registered
  and returns `unavailable`; `-batch` never wires one.
- `session.Create(stateDir, id)` — creates a new v2 log with mode 0600 and an
  exclusive non-blocking `flock`. `session.Open(stateDir, id)` opens an existing
  log for resume, takes an exclusive `flock`, validates it, truncates only a
  torn final line (recording `session_repair`), and terminates a valid final
  line that lacks a newline. `session.OpenShared(stateDir, id)` takes a shared
  `flock` for a fork and never modifies the parent. `Store` exposes
  `Append(Event) error`, `CopyHistory([]Event) error`, `Events() []Event`,
  `Sync() error`, `Close() error`, `ID()`, and `Path()`. One writer.
  `Append` assigns `v=2`, `seq`, `ts`, and `session`; forked copies preserve the
  source timestamps and payloads while renumbering `seq` and the session ID.
  `seq` starts at 1 and increments by 1. `Sync` flushes the file; `Close` syncs,
  unlocks, and closes. `session.Read(path)` replays a whole log;
  `session.ReadHead(path, maxEvents)` validates and returns only its prefix for
  metadata listing without reading whole logs; `session.ReadSnapshot(path)`
  replays the log as of a byte length captured before the read, so a focused
  child view can read a log another process still appends to without treating a
  just-completed line as corruption.
- `agent.Inspect(events) (State, error)` — pure function of the durable log. It
  reconstructs session metadata, cumulative turns and usage, the last run's
  effective configuration (including the latest recorded thinking effort) and
  terminal status, and every unpaired tool call.
  It also folds every recorded `delegate` result's `details` into
  `SubagentTurns`, `SubagentUsage`, and `SubagentCostMicroUSD`, so a resumed
  parent keeps the spend those children already incurred. It derives
  `State.PromptCapped` by re-running the 32 KiB prompt-cap rule over the
  recorded `system_prompt`, so the flag is correct for logs written before it
  existed and a host never matches the truncation marker itself. Unknown event
  types and fields within v2 are ignored.
- `agent.Summarize(events) (SessionSummary, error)` — decodes only the
  `session_start` metadata and first user message from a log head for the
  `/resume` picker. It is not a substitute for `Inspect`.
- `agent.LatestModelExchange(events) (ModelExchange, error)` — component-level
  reader for the newest recorded model attempt: the newest `model_request` or
  `model_response` plus its pair when one exists in the same turn without an
  intervening `run_start` or `session_start`. Payloads are copies of the
  recorded event JSON, never wire bytes, and are never decoded. An empty log, or
  one with no model events, returns the zero value; a returned payload that is
  not a JSON object is a corrupt-log error. There is no facade DTO: a facade
  host reads with `session.ReadSnapshot` and calls this, so raw payload bytes
  never cross the facade boundary. On a fork the newest attempt may be an event
  copied from the parent, and `Run` counts the parent's runs.
- `agent.WriteSessionStart(store, cfg, parent)` — writes and syncs the
  once-per-session `session_start` event for a new, forked, or delegated
  session. It records the resolved `system_prompt`, the optional `agent` name,
  the recorded tool grant (`tools`, nil meaning every tool of the version), and
  `parent_session`/`parent_seq` plus `parent_call_id` for a delegated child.
- `agent.Project(events []session.Event, limits Limits) (Projection, error)` —
  pure function of events plus limits; no hidden state. `Limits` carries the
  per-call input budget and the compaction target; the
  prompt is the `system_prompt` recorded in `session_start` used verbatim, or
  the frozen template for the recorded prompt version when the log predates that
  field; tool schemas are selected from the recorded toolset version and
  narrowed by the recorded tool grant. Run/control events
  (`run_start`, `run_end`, `interrupt`, `session_repair`, `final`, `limit`,
  `error`, and unknown types) are ignored. The returned `Projection` carries the
  canonical messages, the input-token estimate, and the elided-result count; the
  runner logs `compaction` when the count changes.

- `agent.ProjectWithTools(events, limits, availableCustomTools)` — projection
  for a session that records caller tools. It verifies the supplied declarations
  against `session_start.custom_tools` (names, descriptions, canonical JSON
  parameters, and `concurrency_safe`), appends the recorded schemas, and then
  applies the recorded grant. `agent.Project` remains the built-in-only entry
  point and returns `agent.ErrToolMismatch` when the session records custom
  tools.
- `agent.CustomTool{Schema, ConcurrencySafe}` — the single internal carrier for
  a caller tool. Registration, projection, `session_start` recording, and the
  spawner read this value so they cannot disagree.
- `agent.Config.ToolPolicy` — called by the runner after `ToolStarted` and
  before dispatch. A non-context error becomes a `permission_denied` result and
  continues; a context cancellation follows the operation-interrupt path. The
  engine serializes one policy closure across the parent and delegated children,
  so `Authorize` is never entered concurrently within one session.

§3.2.1, the public library facade, is its own page:
[facade.md §3.2.1](facade.md#321-public-library-facade).

## 3.3 Host observation contracts

`agent.Config.Display func(DisplayEvent) error` is the single runner-to-host
observation path. Events are `RunStarted`, `ModelStarted`, `ReasoningDelta`,
`TextDelta`, `ResponseCompleted`, `ToolStarted`, `SubagentStarted`,
`SubagentProgress`, `SubagentFinished`, `ToolCompleted`, `MessageRecorded`,
`SubagentMessageRecorded`, `Notice`, and `RunFinished`, emitted in that contract
order around the matching durable events. The four subagent events carry
counters and names only, and host-originated steering text is echoed on
`SubagentMessageRecorded` so the host can acknowledge it.
`SubagentProgress` is emitted at child model-response boundaries and throttled
to one event per second per child, so its usage and cost are cumulative so far,
not final. Read-only fan-out runs children on separate goroutines, so the
spawner serializes the parent display callback before forwarding them; that
callback is never entered concurrently, and each child's `SubagentStarted`
precedes its own progress and finish events. A child failure before its run
starts still emits one `SubagentFinished` with an empty session, zero counters,
and `Error`, and it registers no observation sink.

A child's own event stream is available through
`agent.SpawnerOptions.Observe` (`DelegationConfig.ObserveChild` at the facade).
It registers one sink per child that starts a run, after the intent is resolved
and before `SubagentStarted`. The sink receives that child's `RunStarted`,
`ModelStarted`, text and thinking deltas, `ResponseCompleted`, `ToolStarted`,
`ToolCompleted`, `MessageRecorded`, `Notice`, and `RunFinished`, sanitized as for
the parent display. Identity is structural: the registration names the child
session, and the facade stamps it on every sink event, because the delta events
carry no session field. The facade forwards only events with a public form; an
internal transport event such as `ModelStarted` has no public counterpart and is
not delivered, and a retry stays visible through `Notice`. One sink is never entered concurrently with itself;
distinct child sinks, and a child sink versus the parent display, may run
concurrently. The sink is synchronous and unbuffered, so a blocking sink
throttles only that child. A returned error, or a recovered panic, from the
registration or the sink terminates that child as `internal_error` with the
cause in its report and leaves its siblings and the run untouched.

Registering an observer sets `Config.Stream` for every delegated child, so an
observed child streams when its adapter supports streaming and emits one
buffered `TextDelta` otherwise; `model_response.streamed` records which happened.
The trade is the same retry rule as the root: once partial text has been
displayed, a transport failure is terminal instead of retried. An unobserved
child keeps `Stream: false`, the full retry budget, and the counter-only
stream.

Every display event is observation only and carries no provider token or ANSI.
`ToolCompleted` also carries the structured tool result `details`, which a host
uses to maintain derived UI such as a todo widget without parsing rendered
output. A root display failure before `RunFinished` is an internal error for
that run; during a tool round it pairs the current and unstarted calls with
`canceled`. `ReasoningDelta` and `ResponseCompleted.Reasoning` carry thinking
text that is never logged.

`ResponseCompleted.Turn` and the `Cumulative*` fields include the completed
response and earlier turns; a later turn may change them, so they are progress,
not a promise of the final totals. `RunStarted.RunID` and `RunFinished.RunID`
carry the UUIDv7 recorded in `run_start`. `RunFinished` is terminal: the display
callback is never invoked after `Run` returns, and if the final append or sync
fails the event iterator closes with the result instead. `RunOptions.OnEvent`
sees the same blocking-run events in order. Transport retries happen only
before the first assistant-text delta; after partial text, a failure is
terminal and the displayed partial text is retained rather than reset.
`ViewEvent` projects any event to a flat JSON shape with a stable `kind`
discriminator for an SSE or structured-log consumer.

`agent.History(events)` reconstructs a capped, display-only transcript from the
log: run headers with effective model/dialect and terminal status, user
messages, assistant markdown source, tool calls/results paired by call ID
(anomalies retained), hints, notices with their `NoticeKind`, and partial text.
`final` does not duplicate the assistant text. Unknown events/fields are
ignored; malformed known payloads are errors. A host may use the same function
for a child log read without taking the child's session lock so a running child
stays viewable; a torn final line is ignored. The facade exposes the same read
as `Transcript(ctx, stateDir, sessionID)` for a root, child, or fork session,
and as `Agent.Transcript` for the live root, so a facade-only host renders a
merged view without importing a component package.

The SDK defines no `Host` interface, key bindings, terminal protocol, or
rendering safety layer. Those are host responsibilities; acode owns the shipped
