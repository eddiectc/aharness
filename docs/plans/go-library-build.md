# aharness Go library build plan

- **Date:** 2026-11-16
- **Revision:** 2026-11-16; the second draft adds the TUI as first customer,
  §1.5 boundary, `Tool.ConcurrencySafe`, `Fork` contract overrides, the
  sequential-tool-call contract and its gate, `ListModels`, `SteerChild`, and
  `WebFetchConfig.UserAgent`.
- **Status:** implemented 2026-11-16 (M0–M6 and the M8 documentation work);
  M7 TUI adoption is deferred, and the real-provider library smoke is pending
  `AHARNESS_PROVIDER_TOKEN`. Current reference documents win where they
  disagree; implementation updated them in the same change, and §13 records the
  deviations and verification.
- **Source:** [Go library research](../research/go-library-research.md).
- **Target:** Go 1.27.3; module `aharness` today, publishable module path after
  M0.
- **Architecture:** [architecture.md](../architecture.md) §3.1 (packages), §3.2
  (contracts), §3.3 (display/history), §4 (loop), §5 (tools), §6 (projection),
  §7 (session log), §9 (CLI/config).
- **Limits:** [limits.md](../limits.md).
- **Testing:** [testing-strategy.md](../testing-strategy.md) plus §7 here.
- **Definition of done:** [project definition of done](../definition-of-done.md);
  feature criteria in §1.4.

## Decision summary

The library is a headless, embeddable form of the existing loop. It is not a
second harness, not a subprocess wrapper, and not a replacement for the CLI.

- **One public package at the module root.** `package aharness` is a facade over
  `internal/agent`, `internal/model`, `internal/tools`, and `internal/session`.
  Internals stay internal. Public DTOs are copied and converted at the
  boundary; they are not aliases of internal types.
- **The module path becomes publishable first.** `module aharness` cannot be
  fetched as a remote module. M0 chooses the final path (for example
  `github.com/<org>/aharness`) and updates the 191 internal import references.
  No `/v1` suffix; a breaking public API change gets `/v2`.
- **Construction is explicit.** `New`, `Resume`, and `Fork` take a `Config`
  struct and return `*Agent`. `New` requires a workspace, state directory, and
  model. `Resume` and `Fork` require the caller's model implementation and
  validate it against the recorded session.
- **The unit is `Agent` = one durable session.** `Agent.Run` is the blocking
  one-call convenience; `Agent.Start` returns a `*Run` whose `Events()` is
  `iter.Seq2[Event, error]`. `Run` exposes `Interrupt`, `Cancel`, `Steer`,
  `SteerChild`, `Done`, `Result`, and `Close`.
- **The result is authoritative.** `Result.Status` is the typed stop reason and
  carries final text, turns, usage, context occupancy, cost, subagent totals,
  and the diagnostic `Err` cause. `Agent.Run` returns that same cause as its
  error, so conventional `if err != nil` handling works. The event iterator
  carries the cause in `RunFinished.Result.Err` and yields a separate final
  error only when no `Result` could be produced. There is one terminal state,
  not two.
- **Custom models and custom tools are first-class.** `Model` is a public
  interface with canonical request/response DTOs. Built-in OpenAI Chat and
  Responses constructors and a models.dev discovery helper cover the common
  case. `Tool` is a public struct registered into the existing tool registry.
  Built-in tools can be disabled; names cannot collide.
- **Tools are recorded.** `session_start` gains an additive `custom_tools`
  array. Projection appends those schemas before the recorded grant filter, so
  resume replays the exact tool set the model saw. This is a v2-compatible
  field, not a schema version bump.
- **One host policy hook.** `Config.Authorize` is called before every tool
  execution, including delegated children. A denial becomes a model-visible
  `permission_denied` result. It is explicitly not a sandbox; the host still
  owns OS isolation.
- **Delegation is opt-in and explicit.** The library takes definitions and a
  model resolver; it does not read `<state>/agents` implicitly. `DefaultAgents`
  provides the bundled set for callers that want it. Delegation depth is one.
- **The TUI is the first customer, and the boundary is explicit.** The TUI
  adopts the public run, event, result, and session surface; the library never
  grows a TUI package or imports `internal/tui` or `cmd`. A second, written
  acceptance case is a research agent over a data workspace with no shell: the
  same coding loop with a replaced prompt and deterministic custom tools. §1.5
  is the tripwire list. A change that alters a recorded contract — session
  schema, prompt, tool grant, projection, or concurrency model — is a different
  product, not an extension.
- **Tool calls inside one run stay sequential.** Arrival order is the durable
  log order, the replay order, and the authorization order. Independent remote
  work belongs in one batch tool or in delegated children; `Tool.ConcurrencySafe`
  only allows concurrent *children*, never concurrent calls in one run. The
  revisit gate is measured from logs (§4.3), not assumed.
- **`Fork` may change the contract; `Resume` may not.** `Resume` inherits the
  recorded prompt and tool set and rejects changes. `Fork` creates a new
  self-contained log, applies explicit prompt or tool overrides, and records a
  `contract_override` naming what changed. That is the only way to correct a
  prompt or tool schema without abandoning the conversation.
- **The CLI remains a separate consumer.** A new `internal/engine` package owns
  the new/resume/fork wiring currently duplicated in `cmd/aharness`. Batch mode
  dogfoods the engine; the TUI migrates onto the public surface in M7, before
  the facade is called stable. The library never imports `internal/tui` or
  `cmd`.

## 1. Scope

### 1.1 What this plan owns

| Area | Implementation |
|---|---|
| Module | Publishable module path; import rewrite; `go.mod` and release metadata. |
| Public package | `aharness` root facade: config, agent, run, events, results, state, models, tools, delegation, errors. |
| Engine | `internal/engine` extraction of new/resume/fork resolution, workspace/state/work-dir setup, adapter/registry/runner wiring, run lifecycle. |
| Model extension | Public canonical DTOs and `Model`/`StreamingModel`; OpenAI Chat/Responses wrappers; models.dev discovery and listing helpers; caller-supplied models. |
| Tool extension | Public `Tool`, `ToolResult`, `ToolName`, `ToolsConfig`, policy hook; registry `Register`; disable built-ins via a recorded grant; `ConcurrencySafe` recording; stable result codes. |
| Session extension | Additive `custom_tools` and `contract_override` in `session_start`; `Inspect` exposure; resume/fork validation; session listing/summary helper. |
| Delegation | Public `AgentDefinition`, `DelegationConfig`, default bundled agents, model resolver wiring, child policy inheritance, `ConcurrencySafe` fan-out, depth one. |
| Control | Blocking `Run`; streaming `Start` with `iter.Seq2`; interrupt, cancel, steer (including a live delegated child), close; backpressure and goroutine lifecycle. |
| UI contract | Complete public event, result, catalog, and session surface for the TUI and a headless host; no public TUI package. |
| Boundary | §1.5 tripwires; an external-module acceptance test for the data-workspace, no-shell research shape. |
| CLI | Batch mode migrated to the engine and (where practical) the library; exit-code mapping stays in `cmd`; TUI migrated onto the public surface in M7; no user-facing behavior change. |
| Docs | README usage section, architecture package/contract updates, limits where numbers change, testing strategy, and release notes. |

### 1.2 What stays the same

- The model/tool loop, turn sequence, projection/compaction policy, retry
  policy, stuck detection, and terminal statuses.
- The seven built-in tool names, arguments, result codes, workspace containment,
  `.git` and symlink policy, and process-group cancellation.
- The v2 JSONL session schema, `flock`, torn-line repair, fork copy, and the
  rule that the log is the only durable truth.
- One writer per session and one active run per `Agent`; multiple `Agent`s in
  one process are allowed when they use different session IDs.
- Multiple tool calls within one model response execute sequentially in arrival
  order. Correctness, replay order, and the authorization contract do not
  depend on provider-side `parallel_tool_calls`. Delegated children may fan out
  concurrently under the recorded grant rule (§3.8).
- Run budgets are per run and are not inherited from the log; model identity,
  prompt, tool grant, and limits are recorded. Model and limits can be
  overridden on resume/fork. Prompt and tool grant are inherited on resume and
  may be overridden only on fork, where the new `session_start` records the
  resulting contract and a `contract_override`.
- Zero new runtime dependencies. The public package uses the standard library
  and the existing internal packages only.
- The CLI's flags, environment, config file, TUI, `-ui-json` protocol, exit
  codes, and operator-facing behavior. `Authorize` is library-only in v1.

### 1.3 Not in this plan

- A sandbox, approval UI, or OS isolation. `Authorize` is policy, not a security
  boundary.
- In-memory or database session stores. v1 uses the existing JSONL store and a
  required `StateDir`. A store seam is a later change.
- MCP, plugins, hooks beyond `Authorize`, RAG/embeddings, or additional
  provider dialects.
- A provider matrix. One `Model` interface, OpenAI-compatible adapters, and
  models.dev discovery are the integration story; vendor SDKs stay out.
- A public TUI package, widget toolkit, or programmatic TUI host. The TUI stays
  a CLI consumer and migrates onto the public run/event/result/session surface;
  the library never imports `internal/tui` or `cmd`.
- Structured-output schemas, output parsers, report DTOs, or JSON-mode
  enforcement. The durable artifact is a file the agent writes, or text the
  caller decodes.
- Graph/workflow orchestration, an agent DSL, prompt templating, routers,
  handoffs, memory, or multi-tenancy. Those are ADK features; see §1.5.
- Background/asynchronous runs, agent-to-agent messaging, nested delegation,
  tree-wide budgets or policy, delegation-depth configuration, or worktrees.
  Delegation depth is one and a child cannot delegate.
- Parallel execution of tool calls within one model response. Sequential is the
  contract; batch tools and delegated children are the escape hatches; the
  log-derived gate is in §4.3.
- Changing provider credentials, OAuth/login, or a credential store. The caller
  supplies an API key in memory.
- Changing the session log version. The `custom_tools`, `contract_override`, and
  `concurrency_safe` fields are additive within v2.
- A public `History` transcript DTO in v1. `State` and `Sessions` cover resume
  and listing; live rendering uses events and durable replay stays internal.
  The second consumer is expected to render a report artifact, not a chat
  history; revisit only if a host needs message-level replay.
- Streaming model deltas for callers that use `Agent.Run`; only `Start`
  requests streaming.

### 1.4 Definition of done

The plan is complete when all of the following are true.

- `go.mod` has the publishable module path, and an external module with a
  `replace` directive can `import "…/aharness"` and build.
- `go test ./...`, `go vet ./...`, `go test -race`, `golangci-lint`,
  `govulncheck`, `make build`, and `make docs-check` pass.
- External-package tests (`package aharness_test`) cover new, resume, fork
  (including contract overrides), blocking run, streaming run, every event
  kind, custom model, custom tool (including a `ConcurrencySafe` fan-out),
  policy denial, interrupt, cancel, steer, steer-child, `ListModels`,
  close-during-run, and error paths.
- A fake model drives a complete tool-call round without network access, and a
  scripted fake proves the event order and the result accounting.
- A session created by the existing CLI can be resumed by the library with the
  same model, prompt, tool grant, and limits when the caller can reproduce the
  recorded grant (a nil CLI grant includes `delegate`, so it needs a
  `DelegationConfig`); a session created by the library can be resumed by the
  CLI when the recorded provider/dialect and grant are CLI-reproducible.
- A session with custom tools fails closed when the caller omits or changes a
  custom tool, and replays semantically equal schemas when the caller supplies
  them.
- An external-module acceptance test builds the research shape: a data
  workspace, `run_command` disabled, a replaced system prompt, four
  deterministic custom tools (at least one `ConcurrencySafe`), the four
  read-only built-ins, and three analyst definitions. A fake model completes a
  run with no network access and no shell.
- A public-surface transcript test builds text, tool blocks, the todo panel,
  the subagent roster, usage/cost, and terminal status from `Events()` and
  `Result` alone, without reading the log. This is the UI contract test.
- Fork contract overrides are covered: the new `session_start` records the
  applied prompt, grant, and custom schemas plus `contract_override`; `Resume`
  rejects the same overrides; copied history that references a tool absent from
  the new grant still replays.
- A custom tool declared `ConcurrencySafe` fans out across delegated children;
  the same call with an undeclared custom tool runs sequentially; the
  `Authorize` hook is never entered concurrently within one session.
- Tool calls within a run are proven sequential: a fake model returns three
  calls in one response, and the log records started/completed pairs in arrival
  order with no interleaving or reordering.
- Session v2 fixtures written before this change still resume; no migration is
  required.
- The batch CLI behavior and exit codes are unchanged; the TUI runs on the
  public surface and its tests still pass.
- The README, architecture, limits, testing strategy, and package docs describe
  the shipped surface, and the library has a runnable example in
  `example_test.go`.
- No new runtime dependency is added.

### 1.5 Boundary: coding SDK, not an ADK

The library is the embeddable form of the aharness coding agent, not a general
agent development kit. One test decides every proposed extension:

> Does the change alter a recorded contract — session schema, prompt, tool
> grant, projection, or concurrency model? If yes, it is a different product,
> not a library extension.

The following stay out of scope even when a plausible customer asks: a provider
matrix or vendor SDK types in public DTOs; graph/workflow orchestration,
routers, handoffs, or topology vocabulary; memory, RAG, embeddings, or vector
stores; a pluggable session store or store interface; background runs, nested
delegation, agent messaging, or worktrees; structured-output schemas or report
DTOs; prompt templating or an agent configuration language; a server, launcher,
deployment path, auth, or tenancy.

Two customers are accepted, and both are written down:

1. The coding TUI exercises run control, every event, child sessions,
   transcript rendering, model/effort selection, and session listing.
2. The data-workspace research agent with no shell exercises a replaced prompt,
   deterministic custom tools, read-only analysts, `ConcurrencySafe` fan-out,
   and report-as-file output.

An extension lands through `Model`, `Tool`, `Authorize`, `AgentDefinition`, or
`RunOptions`. A new recorded value must be an additive v2 field with a
fail-closed reader rule; new loop or log behavior is a separate product. A
third, out-of-workspace domain — chat-only, API-only, multi-tenant, or
background orchestration — is evidence for a sibling project, not an order to
widen this one. The accepted second shape is still a workspace agent: files in,
files out, one durable log. Shared plumbing, if any, is extracted into a
separate module; the root package stays coding-specific.

## 2. Research summary

The [research document](../research/go-library-research.md) surveys Eino, Google
ADK Go, LangChainGo, the OpenAI Agents Go port, Claude Agent SDK Go ports,
docker-agent, gptscript, mcp-go, and the embedding surfaces of Claude, OpenAI
Agents, Codex, opencode, goose, crush, Cursor, and Aider. The decisions that
follow from it:

| Finding | Decision |
|---|---|
| Config struct + constructor dominates medium/large Go APIs. | `Config` struct + `New`/`Resume`/`Fork`; per-run `RunOptions` struct. |
| Streaming is not converged; `iter.Seq2` is the 2026 idiom. | `Run.Events() iter.Seq2[Event, error]`; blocking `Agent.Run` for the simple case. |
| Typed tool registration plus a raw schema escape hatch is table stakes. | Public `Tool` with explicit JSON Schema and a typed handler; built-ins remain. |
| Provider-neutral DTOs; no SDK types leak. | Copy canonical DTOs; adapters convert to `internal/model`. |
| Sessions are verbs, not options. | `New`, `Resume`, `Fork`; session ID and path on `Agent`; `State`/`Sessions` inspect helpers. |
| Approval is policy, not a boolean; allowlists must not silently pre-empt it. | One `Authorize` hook plus a recorded, explicit tool grant; availability, permission, and sandbox are documented as three layers. |
| Custom tools otherwise force sequential delegation. | `Tool.ConcurrencySafe` extends the read-only fan-out set across children only; calls within one run remain sequential. |
| Prompt and tool set are frozen at `session_start`. | `Resume` inherits and rejects changes; `Fork` applies overrides and records `contract_override`. |
| Scope creep is how Go agent libraries become indistinguishable ADKs. | §1.5 boundary plus the recorded-contract test; two written customers only. |
| Wrapping a CLI imports its release cadence. | In-process library over the existing loop; no subprocess bridge. |
| Result objects carry status, usage, cost, session ID. | `Result` carries the full existing accounting plus `Status`. |
| Do not double-signal failure. | One `Result`; `Status` is authoritative and `Err` is its diagnostic cause; a separate iterator error means no `Result` exists. |

## 3. Public API

The following is the v1 contract. Field names are normative for the plan; the
implementation may add fields before release, but not remove them.

### 3.1 Module and package layout

```text
<module path>/
  aharness.go        # package doc, Config, Agent, New/Resume/Fork
  run.go             # Run, RunOptions, events iterator, control
  events.go          # public Event interface and concrete events
  result.go          # Result, StopReason, Usage, Limits, State, SessionInfo
  model.go           # public model DTOs, Model/StreamingModel, OpenAI helpers
  catalog.go         # models.dev discovery helper
  tool.go            # Tool, ToolResult, ToolName, ToolsConfig, policy
  delegate.go        # AgentDefinition, DelegationConfig, defaults
  errors.go          # sentinels and typed errors
  example_test.go    # external-package runnable examples
  internal/...       # unchanged implementation packages + internal/engine
  cmd/aharness/...   # CLI and TUI (separate consumer)
```

`package aharness` is the only public Go package in v1. It imports internal
packages; internal packages never import it. No `pkg/`, `util`, `common`, or
one-package-per-type. If a second public package becomes necessary (for example
an instrumentation adapter), it is a separate, justified change.

The module path is a hard prerequisite. `module aharness` has no dot in its
first path element and cannot be fetched by `go get`. M0 chooses the final path
and rewrites imports mechanically. The CLI's import path changes with it; no
user-facing flag or output changes.

### 3.2 Construction and lifecycle

```go
// Agent is one durable session and the loop that advances it.
type Agent struct { /* ... */ }

// New creates a new session. Workspace, StateDir, and Model are required.
func New(ctx context.Context, cfg Config) (*Agent, error)

// Resume opens an existing session for append. cfg.Model is required; its
// Completer is the caller's implementation and a zero Spec inherits the
// recorded model identity, limits, and pricing.
func Resume(ctx context.Context, cfg Config, sessionID string) (*Agent, error)

// Fork copies an existing session into a new self-contained session and opens
// it for append. Explicit Instructions, SystemPrompt, and Tools apply to the
// new session and are recorded with a contract_override. The source session is
// not modified.
func Fork(ctx context.Context, cfg Config, sessionID string) (*Agent, error)

func (a *Agent) ID() string           // canonical session UUID
func (a *Agent) SessionPath() string  // <StateDir>/sessions/<id>.jsonl
func (a *Agent) Workspace() string    // canonical absolute workspace
func (a *Agent) State() (State, error) // recorded state; ErrBusy during a run
func (a *Agent) Close() error         // cancel/drain, sync/unlock, close workspace
```

`New` performs all setup before returning: open the workspace, prepare the state
directory, resolve or validate the model, create the log, and write and sync
`session_start`. A returned error leaves no store or workspace open.

`Resume` opens and validates the log, inherits the recorded values described in
§4.7, validates the caller's model and tool set, then returns. It does not fetch
the catalog. `Fork` additionally creates the new log, applies explicit prompt
and tool overrides, records `contract_override`, and copies the source history.
Fork override validation happens before the new log is created, so a rejected
override leaves nothing behind. Neither call rewrites the source log.

`Close` is idempotent. It cancels an active run, drains the run's event channel
until the runner exits, then closes the store (syncing and releasing the lock),
closes the workspace, and removes the per-session work directory. A run never
closes the store. `Close` must not race a goroutine that is ranging `Events`;
cancel first and let the iterator finish, or close without ranging.

`State` is valid only while the agent is idle and no run is active; it returns
`ErrBusy` otherwise. It is a read of the durable log through `agent.Inspect`,
not a cache.

`InspectSession` is the discovery call for a host that wants to show a resume
screen or construct the required `ModelConfig`/`ToolsConfig`: it returns the
recorded model spec, prompt, grant, custom schemas, limits, parent, and
timestamps without opening the store or taking its lock. `Resume` then validates
the caller's model implementation and tool set against those recorded values.

### 3.3 Configuration

```go
type Config struct {
    // Workspace is the repository root the agent may read and write.
    // Required for New. For Resume/Fork, empty inherits the recorded
    // workspace; a non-empty value must canonicalize to the same directory.
    Workspace string

    // StateDir is the durable state root. Required. Sessions live under
    // <StateDir>/sessions; per-run work directories under <StateDir>/work.
    StateDir string

    // Model is the model implementation and identity. Required for New. On
    // Resume/Fork it is required, but a zero Spec inherits the recorded one.
    Model *ModelConfig

    // Tools selects built-in tools, registers custom ones, and configures the
    // web and delegation tools. Nil selects the default built-in set. On
    // Resume, a non-nil value must match the recorded availability and custom
    // schemas. On Fork, a non-nil value replaces the copied contract and is
    // recorded with the new session. See §4.8 for concurrency declarations.
    Tools *ToolsConfig

    // Limits overrides the per-call context budget. Zero fields are derived
    // from Model.Spec; see Limits.
    Limits Limits

    // MaxTurns is the cumulative model-turn budget across this agent's runs.
    // 0 means unlimited. It is a run budget and is never inherited from the log.
    MaxTurns int
    // MaxTotalTokens is the cumulative input+output token budget. 0 means
    // unlimited. It is a run budget and is never inherited.
    MaxTotalTokens int
    // RunTimeout bounds one Run. 0 means no harness deadline.
    RunTimeout time.Duration
    // ModelTimeout bounds one model attempt. 0 means no harness deadline.
    ModelTimeout time.Duration

    // ThinkingEffort is the reasoning effort. On New, nil means the provider
    // default. On Resume/Fork, nil inherits the latest recorded effort; a
    // pointer to "" clears it and a non-empty value sets it.
    ThinkingEffort *string

    // Instructions is appended to the harness system prompt. It is recorded in
    // session_start. It is valid for New and Fork; Resume requires it to be
    // empty and inherits the recorded prompt. A Fork that sets it records a
    // contract_override. It must be empty when SystemPrompt is set; both set is
    // a ConfigError. Capped by the existing 32 KiB prompt cap.
    Instructions string
    // SystemPrompt replaces the harness system prompt entirely. Advanced; the
    // caller owns the tool and termination instructions. It is recorded and is
    // valid for New and Fork; Resume requires it to be empty. The package doc
    // lists the prompt invariants the loop depends on.
    SystemPrompt string

    // Authorize is called before every tool execution. Nil allows every tool.
    // A returned error denies the call with a permission_denied result unless
    // it is a context cancellation, which interrupts the run. The library
    // serializes calls within one session, including delegated children, so
    // the hook is never entered concurrently by one Agent's runs. If the same
    // function is shared across Agents, it must be safe for concurrent use.
    // It runs on a run goroutine and may block for a human decision; ctx is
    // canceled when the run is interrupted or canceled.
    Authorize func(ctx context.Context, req ToolRequest) error

    // RequireUsage makes a completed response without provider usage a protocol
    // error before any tool executes.
    RequireUsage bool
    // LogModelRequests records full model requests in the session log. It is
    // off by default because requests can contain sensitive prompt text.
    LogModelRequests bool
    // Logger receives operational logs. Nil discards them. The library never
    // writes to stdout, stderr, or the terminal.
    Logger *slog.Logger
}
```

Config validation errors are `*ConfigError` with `Field`, `Value`, and `Detail`.
No environment variable, config file, working directory, TTY, or signal is read
by the library.

### 3.4 Models

```go
// Model is the provider-facing contract.
type Model interface {
    Complete(ctx context.Context, req Request) (Response, error)
}

// StreamingModel is the optional streaming contract. The returned Response is
// authoritative; deltas are display-only.
type StreamingModel interface {
    Model
    CompleteStream(ctx context.Context, req Request, stream StreamHandler) (Response, error)
}

type StreamHandler struct {
    Text      func(string) error
    Reasoning func(string) error
}

// ModelConfig pairs a model implementation with its recorded identity.
type ModelConfig struct {
    // Spec is the model identity and capabilities. Required for New. On
    // Resume/Fork an empty ID inherits the recorded spec; a non-empty ID is an
    // explicit override and the whole spec is used.
    Spec ModelSpec
    // Completer is required. It is called on the run goroutine. On
    // Resume/Fork with an empty Spec, the caller must supply a completer for
    // the recorded model; the library can inherit metadata but not an
    // implementation.
    Completer Model
    // NewCompleter, when non-nil, returns an independent implementation for a
    // delegated child session. When nil, children share Completer, which must
    // then be safe for concurrent use.
    NewCompleter func(sessionID string) Model
}

// Dialect is the recorded wire dialect. It is metadata for the loop; only the
// caller's Model implementation acts on it.
type Dialect string
const (
    DialectChat      Dialect = "openai-chat"
    DialectResponses Dialect = "openai-responses"
    DialectCustom    Dialect = "custom"
)

type ModelSpec struct {
    ID              string   // required
    Provider        string   // recorded name; default "custom"
    Dialect         Dialect  // recorded dialect; default DialectCustom
    BaseURL         string   // recorded URL; informational for custom models
    ContextWindow   int      // required, positive
    MaxOutputTokens int      // 0 omits the wire field
    InputLimit      int      // 0 derives from ContextWindow
    ThinkingEfforts []string // empty accepts any effort
    Pricing         Pricing  // optional observational snapshot
}

// CustomModel pairs a caller implementation with a spec.
func CustomModel(spec ModelSpec, m Model) *ModelConfig

// OpenAIModel builds a Chat Completions or Responses adapter for an explicit
// OpenAI-compatible endpoint. Spec.Dialect must be DialectChat or
// DialectResponses; Spec.BaseURL is used as the endpoint. DialectCustom is for
// caller-supplied models.
type OpenAIModelOptions struct {
    Spec       ModelSpec
    APIKey     string
    HTTPClient *http.Client // nil selects a bounded default
    Timeout    time.Duration
}
func OpenAIModel(opts OpenAIModelOptions) (*ModelConfig, error)

// DiscoverModel resolves a model through models.dev and returns a ready
// ModelConfig. The API key goes only to the adapter, never to the catalog.
type DiscoverModelOptions struct {
    Catalog    CatalogConfig
    Provider   string // required
    Model      string // required
    APIKey     string
    BaseURL    string // optional override of the discovered URL
    HTTPClient *http.Client
    Timeout    time.Duration
}
func DiscoverModel(ctx context.Context, opts DiscoverModelOptions) (*ModelConfig, error)

type CatalogConfig struct {
    URL        string
    StateDir   string // required; normally the same as Config.StateDir
    HTTPClient *http.Client
    TTL        time.Duration
    Timeout    time.Duration
    UserAgent  string
}

// CatalogModel is one normalized catalog entry for a model picker or a host's
// discovery screen. It is read-only metadata; it does not create a model or a
// session. Unavailable is non-empty when the entry cannot be used by the
// harness and explains why.
type CatalogModel struct {
    ID              string
    Name            string
    Provider        string
    Dialect         Dialect
    BaseURL         string
    ContextWindow   int
    InputLimit      int
    MaxOutputTokens int
    ToolCall        bool
    Reasoning       bool
    ThinkingEfforts []string
    Deprecated      bool
    Status          string
    Unavailable     string
    Pricing         Pricing
}

type CatalogListing struct {
    Provider  string
    FetchedAt time.Time
    Models    []CatalogModel // sorted by ID; includes unavailable entries
    Warnings  []string       // non-fatal cache or refresh fallbacks
}

// ListModels returns the catalog for a provider using the same cache,
// conditional-refresh, and stale-fallback policy as DiscoverModel. The common
// consumer is a host model picker. It never selects or creates a session.
func ListModels(ctx context.Context, cfg CatalogConfig, provider string) (CatalogListing, error)
```

A new session whose recorded provider/dialect is `custom` is library-only: the
CLI rejects providers and dialects it does not know. A session intended to be
resumable by both consumers must use a CLI-supported provider (for example
`opencode-go`) and an OpenAI dialect. This is a documentation constraint, not a
runtime restriction in the library.

Canonical DTOs are public copies of `internal/model`:

```go
type Role string
const (RoleSystem Role = "system"; RoleUser = "user"; RoleAssistant = "assistant"; RoleTool = "tool")

type FinishReason string
const (FinishStop FinishReason = "stop"; FinishLength = "length"; FinishToolCalls = "tool_calls"; FinishContentFilter = "content_filter"; FinishOther = "other")

type ToolCall struct {
    ID        string
    Name      string
    Arguments json.RawMessage
    // Intent is display metadata parsed from the arguments' "intent" member on
    // events. It is empty on model requests and is never acted on by the loop.
    Intent string
}

type Message struct {
    Role       Role
    Content    string
    ToolCalls  []ToolCall
    ToolCallID string
    Name       string
}

type ToolSchema struct {
    Name        string
    Description string
    Parameters  json.RawMessage
}

type Request struct {
    Model           string
    Messages        []Message
    Tools           []ToolSchema
    MaxOutputTokens int
    ThinkingEffort  string
}

type Usage struct {
    InputTokens           int
    OutputTokens          int
    CachedInputTokens     int
    CacheWriteInputTokens int
    ReasoningOutputTokens int
    Reported              bool
}
func (u Usage) Total() int
func (u Usage) UncachedInputTokens() int

type Response struct {
    Text         string
    ToolCalls    []ToolCall
    FinishReason FinishReason
    Usage        Usage
    Reasoning    string // display-only
    Streamed     bool
}

type Pricing struct {
    Available          bool
    Currency           string
    Unit               string
    InputMicroUSD      int64
    OutputMicroUSD     int64
    CacheReadMicroUSD  int64
    CacheWriteMicroUSD int64
    Source             string
    FetchedAt          time.Time
}
```

`Model` implementations must be safe for concurrent use when `NewCompleter` is
nil and delegation is enabled. The library never calls a model concurrently for
the same session: a run has one model call in flight. Read-only delegated
children may call their models concurrently up to the existing fan-out limit.

### 3.5 Runs, events, and control

```go
type RunOptions struct {
    // Model overrides the agent's model for this run. The spec must be
    // complete (non-empty ID, positive context window); zero fields are not
    // inherited. It is recorded in run_start and does not rewrite
    // session_start.
    Model *ModelConfig
    // ThinkingEffort overrides the effort for this run. nil inherits the
    // agent's current value; "" means the provider default.
    ThinkingEffort *string
    // MaxTurns and MaxTotalTokens override the agent's budgets for this run.
    // Zero means the agent's value (and therefore the agent's zero means
    // unlimited).
    MaxTurns       int
    MaxTotalTokens int
    // Timeout overrides Config.RunTimeout for this run.
    Timeout time.Duration
}

// Run executes one run to a terminal state and returns the result. It is the
// blocking convenience over Start.
func (a *Agent) Run(ctx context.Context, task string, opts RunOptions) (Result, error)

// Start starts one run and returns a handle. The run begins immediately; its
// event stream applies backpressure, so the caller must consume Events or call
// Close.
func (a *Agent) Start(ctx context.Context, task string, opts RunOptions) (*Run, error)

type Run struct { /* ... */ }

func (r *Run) Events() iter.Seq2[Event, error] // single-use
func (r *Run) Interrupt()                     // cancel the in-flight operation only
func (r *Run) Cancel()                        // terminate the run
func (r *Run) Steer(text string) error         // enqueue a user message at a turn boundary

// SteerChild enqueues steering for a live delegated child of this run. It
// returns ErrNoSuchChild when sessionID is not a running child of this run and
// ErrSteeringFull when that child's queue is full. The line is consumed at the
// child's next turn boundary and recorded in the child log with
// source=subagent-steer. It never blocks.
func (r *Run) SteerChild(sessionID, text string) error
func (r *Run) Done() <-chan struct{}
func (r *Run) Result() (Result, bool)          // ok is false until Done closes
func (r *Run) Close() error                    // cancel, drain, wait; idempotent
```

`Agent.Run` and `Agent.Start` are not safe to call concurrently with each other
on the same `Agent`; the second call returns `ErrBusy`. `Interrupt`, `Cancel`,
`Steer`, `Done`, `Result`, and `Run.Close` are safe to call from other
goroutines; `Agent.ID`, `Agent.SessionPath`, and `Agent.Workspace` are immutable
and safe. `Close` on the agent waits for any active run.

`Start` requires a non-empty task. `Run`/`Start` always record a user message
for the task. A continuation after an interrupted run is a new task (for
example "continue"); there is no implicit empty-task run in v1.

Events are a sealed interface with concrete structs. Every event embeds
`EventMeta`; `Time` is the library's observation time, not a durable timestamp.
`Session` is the session ID.

```go
type Event interface{ event() }

type EventMeta struct {
    Time    time.Time
    Session string
}

type RunStarted struct {
    EventMeta
    Turn           int
    ResumedFromSeq uint64
    Model          string
    Dialect        string
    ThinkingEffort string
}
type TextDelta struct{ EventMeta; Text string }
type ReasoningDelta struct{ EventMeta; Text string }
type ResponseCompleted struct {
    EventMeta
    Turn         int
    Text         string
    Reasoning    string
    ToolCalls    []ToolCall
    FinishReason FinishReason
    Usage        Usage
    Latency      time.Duration
    Streamed     bool
}
type ToolStarted struct{ EventMeta; Turn int; Call ToolCall }
type ToolCompleted struct {
    EventMeta
    Turn      int
    CallID    string
    Name      string
    OK        bool
    Code      string
    Output    string
    Error     string
    ExitCode  int
    Duration  time.Duration
    TimedOut  bool
    Truncated bool
    Details   json.RawMessage
}
type MessageRecorded struct{ EventMeta; Turn int; Source, Text string }

type NoticeKind string
const (NoticeRetry NoticeKind = "retry"; NoticeCompaction = "compaction"; NoticeLimit = "limit"; NoticeInterrupt = "interrupt"; NoticeRepair = "repair"; NoticeWarning = "warning"; NoticeError = "error")

type Notice struct{ EventMeta; Turn int; Kind NoticeKind; Text string }

type SubagentStarted struct {
    EventMeta
    Turn         int
    CallID       string
    Index        int
    Agent        string
    Intent       string
    ChildSession string
    Model        string
    Effort       string
    Tools        []string
}
type SubagentProgress struct {
    EventMeta
    Turn          int
    CallID        string
    Index         int
    Agent         string
    ChildSession  string
    ChildTurns    int
    ContextTokens int
    Usage         Usage
    CostMicroUSD  int64
    CostComplete  bool
    LastTool      string
}
type SubagentFinished struct {
    EventMeta
    Turn          int
    CallID        string
    Index         int
    Agent         string
    ChildSession  string
    Status        string
    ChildTurns    int
    ContextTokens int
    Usage         Usage
    CostMicroUSD  int64
    CostComplete  bool
    Duration      time.Duration
}
type RunFinished struct {
    EventMeta
    Result   Result
    // Reason is the internal terminal reason when available. A synthesized
    // event (the internal display event was not emitted) may have an empty
    // reason; Result.Status and Result.Err remain authoritative.
    Reason   string
    Duration time.Duration
}
```

The event set mirrors the existing display contract. `EventMeta.Session` is the
session that produced the event (the run's session); subagent events additionally
name the child in `ChildSession`. It is display-only and sanitized for terminal
control characters; the session log remains canonical and raw. `TextDelta` and
`ReasoningDelta` are emitted only by `Start` when the model implements
`StreamingModel`; `ResponseCompleted` is always emitted and carries the
authoritative response.

Events are totally ordered within one run. Tool calls in one model response
execute sequentially, so `ToolStarted`/`ToolCompleted` pairs never interleave
and arrive in the order the model returned the calls. A concurrent delegated
child call produces events for several children; those may interleave across
children, but each child's own sequence is ordered and identified by
`ChildSession`. Consumers must not assume that one child's events are contiguous.

### 3.6 Results, state, and listing

```go
type StopReason string
const (
    StopCompleted     StopReason = "completed"
    StopMaxTurns      StopReason = "max_turns"
    StopTokenLimit    StopReason = "token_limit"
    StopTimeLimit     StopReason = "time_limit"
    StopProviderError StopReason = "provider_error"
    StopProtocolError StopReason = "protocol_error"
    StopStuck         StopReason = "stuck"
    StopInterrupted   StopReason = "interrupted"
    StopInternalError StopReason = "internal_error"
)

type Result struct {
    Status          StopReason
    // Session is the canonical session ID. It is also on Agent and on every
    // event's EventMeta, but keeping it here makes a Result self-describing.
    Session         string
    // Err is the diagnostic cause of Status for provider, protocol, stuck, and
    // internal failures. It is nil for completed, limit, and interrupted
    // statuses, except that a display failure after the final event was
    // recorded can set it without changing Status: the durable terminal state
    // is still valid. Agent.Run returns this same value as its error.
    Err             error
    FinalText       string
    RunTurns        int
    RunUsage        Usage
    CumulativeTurns int
    CumulativeUsage Usage
    ContextTokens   int // last response input+output; not cumulative
    RunCostMicroUSD int64
    CostMicroUSD    int64
    RunCostComplete bool
    CostComplete    bool
    SubagentTurns        int
    SubagentUsage        Usage
    SubagentCostMicroUSD int64
    SubagentCostComplete bool
}

type Limits struct {
    MaxContextTokens int
    MaxOutputTokens  int
    SafetyMargin     int
    MaxInputTokens   int
    CompactionTarget int
}

type PendingCall struct {
    CallID string
    Name   string
    Turn   int
}

type ParentRef struct {
    Session string
    Seq     uint64
    CallID  string // set only for a delegated child
}

// ContractOverride records that a Fork replaced part of the inherited
// contract. It describes the new session relative to its parent; the parent
// log is unchanged.
type ContractOverride struct {
    ParentSession string
    Prompt        bool // SystemPrompt or Instructions changed
    Tools         bool // the grant, custom schemas, or concurrency declarations changed
}

type State struct {
    ID            string
    Workspace     string
    StateDir      string
    // Model is a complete spec suitable for OpenAIModel/CustomModel: it
    // combines the recorded identity and the recorded limits (ContextWindow
    // and MaxOutputTokens). ThinkingEfforts is not recorded and is empty.
    Model         ModelSpec
    SystemPrompt  string
    Tools         []ToolName // recorded grant; nil means every tool of the version
    CustomTools   []ToolSchema
    // ConcurrencySafe lists recorded custom tools that declared concurrent
    // child use. Built-in read-only tools are not repeated here.
    ConcurrencySafe []ToolName
    // ContractOverride is non-nil when this session was forked with a prompt
    // or tool-set change, and names what changed relative to the parent.
    ContractOverride *ContractOverride
    Limits        Limits // recorded per-call budget
    Turns         int
    Usage         Usage
    ContextTokens int
    CostMicroUSD  int64
    CostComplete  bool
    ThinkingEffort string
    LastRunStatus StopReason
    Pending       []PendingCall
    Parent        *ParentRef
    CreatedAt     time.Time
    UpdatedAt     time.Time
}

type SessionInfo struct {
    ID           string
    Workspace    string
    Model        string
    FirstMessage string
    CreatedAt    time.Time
    UpdatedAt    time.Time
    Turns        int
    Status       StopReason
    Parent       *ParentRef // non-nil for a forked or delegated session
}

// InspectSession reads a session without taking its exclusive lock. It is
// safe while another process appends; a torn final line is ignored. sessionID
// is canonicalized with session.CanonicalID before any path is built, so a
// traversal attempt is rejected rather than resolved.
func InspectSession(ctx context.Context, stateDir, sessionID string) (State, error)

// Sessions lists session metadata under stateDir, newest first. It ignores
// non-canonical filenames, replays each log in full to fill
// turns/status/timestamps, and never takes an exclusive lock; it tolerates a
// live writer. Child (delegated) sessions are included; use Parent to
// distinguish them.
func Sessions(ctx context.Context, stateDir string) ([]SessionInfo, error)
```

`Result.Status` is authoritative for why the run stopped. `Result.Err` is the
diagnostic cause that accompanies provider, protocol, stuck, and internal
statuses; it is nil for completed, turn/token/time limit, and interrupted
statuses, except for a display failure that happens after the durable `final`.
That exception never changes `Status`: the run is complete and only the
observation path failed. `Agent.Run` returns `(result, result.Err)`, so
`errors.Is`/`errors.As` work on provider failures without a second status
channel.

The facade always emits exactly one public `RunFinished` from the runner's
returned `Result`, even when the internal display event was not emitted (for
example a `run_end` write failure after `final`). The iterator's separate final
`(nil, err)` pair is reserved for the case where the library produced no
`Result` at all, or its own event pump failed; it is not a second terminal
status. A caller must never need to "catch" an error before resuming; the run
is already durable when `RunFinished` is observed.

### 3.7 Tools and policy

```go
type ToolName string
const (
    ToolReadFile   ToolName = "read_file"
    ToolEditFile   ToolName = "edit_file"
    ToolSearch     ToolName = "search"
    ToolRunCommand ToolName = "run_command"
    ToolWebSearch  ToolName = "web_search"
    ToolWebFetch   ToolName = "web_fetch"
    ToolTodo       ToolName = "todo"
    ToolDelegate   ToolName = "delegate"
)

// Tool is one caller tool. Name, Description, InputSchema, and Run are
// required. InputSchema is a JSON Schema object (use {} for no arguments); it
// is recorded in the session log and sent to the model.
type Tool struct {
    Name        string
    Description string
    InputSchema json.RawMessage
    Run         func(ctx context.Context, call ToolCall) (ToolResult, error)
    // ConcurrencySafe declares that concurrent delegated children may call
    // this tool at the same time. It is ignored for built-ins, which keep
    // their explicit read-only set. Default false. The library cannot verify
    // the claim: a false declaration can corrupt the caller's own state. Run
    // must be safe for concurrent use when true; Authorize remains serialized
    // within one session. It never permits concurrent calls within one run.
    ConcurrencySafe bool
}

type ToolResult struct {
    OK        bool
    Code      string
    Output    string
    Error     string
    ExitCode  *int
    TimedOut  bool
    Truncated bool
    Details   json.RawMessage
}

func TextResult(text string) ToolResult
func ErrorResult(code, message string) ToolResult

type ToolsConfig struct {
    // Disable omits built-in tools by name. Unknown, duplicate, or delegate
    // names are a ConfigError; delegate is controlled only by Delegation.
    Disable []ToolName
    // Extra registers caller tools. Names must be unique and must not collide
    // with any built-in name, enabled or disabled; at most 32 tools and 64 KiB
    // of total recorded schema.
    Extra []Tool
    // WebSearch configures the SearXNG endpoint. Nil keeps the tool
    // registered and returns an actionable unavailable result.
    WebSearch *WebSearchConfig
    // WebFetch configures the web fetch provider. Nil selects the builtin
    // provider.
    WebFetch *WebFetchConfig
    // Delegation enables the delegate tool. Nil disables it.
    Delegation *DelegationConfig
}

type WebSearchConfig struct {
    BaseURL string
    Timeout time.Duration
}

type WebFetchProvider string
const (WebFetchBuiltin WebFetchProvider = "builtin"; WebFetchCrawl4AI WebFetchProvider = "crawl4ai")

type WebFetchConfig struct {
    // Provider defaults to WebFetchBuiltin when empty.
    Provider WebFetchProvider
    Endpoint string
    Key      string
    Timeout  time.Duration
    // UserAgent is sent on every fetch. Some hosts (for example SEC EDGAR)
    // reject requests without a descriptive agent and contact address. Empty
    // selects the builtin default.
    UserAgent string
}

// ToolRequest is the policy input. It is available for built-in and custom
// tools, parent and child.
type ToolRequest struct {
    Tool    ToolName
    Call    ToolCall
    Intent  string
    Turn    int
    Session string
    Agent   string // "" for the session agent; a definition name for a child
}

// Deny is the recommended error for Authorize.
type Deny struct{ Reason string }
func (d *Deny) Error() string
```

`ToolRequest.Call.Arguments` is the raw model-supplied JSON; a host that
renders it to a terminal must sanitize it first. `Intent` is the parsed
display-only hint, not a trusted instruction. The hook runs before the tool
dispatcher validates the arguments, so a policy that needs the typed arguments
must validate them itself or accept the dispatcher's later `invalid_args`
result.

Tool names are validated at construction: lowercase ASCII letters, digits,
`_`, and `-`, at most 64 bytes. A custom tool's `Run` is recovered like a
built-in: a panic becomes a `panic` result, and the run continues. A returned
error becomes a `tool_error` result unless it is a `*Deny` (which becomes
`permission_denied`) or a context error (which cancels the operation). A
returned `ToolResult` with an empty `Code` is normalized to `ok` when `OK` and
`tool_error` otherwise. `Details` is capped at 64 KiB so a custom tool cannot
inflate the session log beyond the existing result cap.

Built-in tools remain exactly the current seven. `run_command` still inherits
the host environment through the existing allowlist and writes to the
per-session work directory; the library adds no sandbox. Custom tools run with
the host's privileges.

Tool calls within one model response execute sequentially, in the order the
model returned them. This is a contract, not an implementation detail: it fixes
log order, replay order, `Authorize` order, canonical-argument stuck detection,
and the guarantee that two `edit_file` calls in one response cannot race. A
caller that needs parallel remote work expresses it inside one tool — a batch
tool that fans out internally and returns one ordered result — or as delegated
children. `Tool.ConcurrencySafe` never changes in-run order; it only allows
concurrent child sessions (§3.8, §4.3).

### 3.8 Delegation

```go
type AgentDefinition struct {
    Name           string
    Description    string
    Prompt         string
    // Tools is the child's grant. An empty list means no tools; use explicit
    // names for the built-ins the child may call. delegate is never valid.
    Tools          []ToolName
    Model          string // optional; resolved by DelegationConfig.ResolveModel
    ThinkingEffort string
    MaxTurns       int
}

type DelegationConfig struct {
    // Agents is the runnable definition set. Nil or empty is a ConfigError;
    // use DefaultAgents to opt into the bundled set. Omitting Delegation
    // disables the delegate tool entirely.
    Agents []AgentDefinition
    // MaxSubagents bounds fresh children per run. 0 selects the CLI default of
    // 8; -1 means unlimited. Other negative values are a ConfigError.
    MaxSubagents int
    // ResolveModel resolves a definition's non-empty Model/ThinkingEffort.
    // Nil uses the parent model and rejects definitions that name another
    // model.
    ResolveModel func(ctx context.Context, modelID, effort string) (*ModelConfig, error)
}

// DefaultAgents returns copies of the bundled definitions.
func DefaultAgents() []AgentDefinition
```

File discovery and the strict markdown definition parser stay in the CLI. The
library takes explicit `AgentDefinition` values; a host that wants files can
parse them itself and construct the struct. This avoids teaching the public
library a file format whose validation rules are CLI-specific and cannot see
custom tools.

An empty `AgentDefinition.Tools` is converted to a non-nil empty grant, so
"no tools" is distinct from a nil grant ("all tools of the recorded version").
`DefaultAgents` always returns explicit grants. Name, Description, and Prompt
are required and names must be unique; a definition may name a custom tool only
when that tool is registered in `ToolsConfig.Extra`; construction rejects an
unknown name, and the spawner intersects the grant with the parent's enabled
set. If `ResolveModel` is nil, a definition that names a model other
than the parent's is rejected per task; an effort override for the parent model
is still allowed. If `ResolveModel` is non-nil, it is the only authority for a
child's model spec and limits; the returned `ModelConfig.Spec.ID` must equal the
requested model ID, and a mismatch is a task error so a resolver cannot silently
route to a different model.

Delegation uses the existing spawner. Each child is a separate session under
the same state directory with `parent_session` and `parent_call_id`. The
library supplies the current `Config`, workspace, definitions, display pump,
parent sequence, model resolver, and a per-child registry factory that registers
the same custom tools with the child's own options. The spawner serializes the
parent `Authorize` hook across concurrent children, so the hook is never entered
concurrently within one session.

A call runs its tasks concurrently when every task's effective grant consists
only of the built-in read-only set (`read_file`, `search`, `web_search`,
`web_fetch`) or custom tools declared `ConcurrencySafe`. Any other call keeps
array order and runs sequentially, and `details` records `execution` and the
reason. A `ConcurrencySafe` declaration is recorded per custom tool in
`session_start.custom_tools`; on resume the caller must reproduce it exactly.
The declaration is trusted host code, not verified by the library, and a false
declaration can corrupt the caller's own state. This rule parallelizes *across
children only*: tool calls within one run remain sequential. Custom tools are
available to a child only when the definition's grant names them. The hook
applies to child calls with `ToolRequest.Agent` set to the definition name.
`MaxSubagents` is normalized at construction: 0 selects the CLI default of 8
and -1 selects unlimited.

**Delegation depth is one.** A child cannot delegate; `delegate` is absent from
its schemas and grant, and a test asserts it. There is no tree-wide budget,
tree-aware policy, or depth parameter in v1. The documented pattern for staged
research is parent-driven: deterministic prep, then a read-only fan-out (read
data, summarize sources), then an analysis fan-out (each child reads the
prepared data and prior summaries), then the parent writes the artifact, then an
optional read-only reviewer. When a child needs more data, it returns a
`DATA_REQUEST:` line and the parent issues another `delegate` call. Context
shielding inside one child is a tool concern: a custom tool may call a model,
chunk-map-reduce, and return a bounded result. It does not create a session.

**A child grant never widens the parent's authority.** The spawner intersects
each definition's grant with the parent's enabled built-in/custom set (a nil
parent grant means every tool of the recorded version). If the definition names
a tool outside the parent's set, the task fails with `spawn_failed` and a
message naming the denied tool instead of running with a broader grant. The
same intersection applies to a resumed child: its recorded grant is narrowed by
the current parent. This closes the gap where a user-provided definition file
could grant a writer to a read-only parent session.

### 3.9 Errors, versions, and compatibility

```go
// SchemaVersion is the session log schema version this package reads and
// writes. It is independent of the module version.
const SchemaVersion = 2

// Version returns the library's build version, or "devel" when the build does
// not carry one. It is recorded as harness_version in new sessions.
func Version() string

var (
    ErrClosed           = errors.New("aharness: agent is closed")
    ErrBusy             = errors.New("aharness: a run is already active")
    ErrEmptyTask        = errors.New("aharness: task is empty")
    ErrIteratorConsumed = errors.New("aharness: events already consumed")
    ErrSessionNotFound  = errors.New("aharness: session not found")
    ErrSessionLocked    = errors.New("aharness: session is locked")
    ErrInvalidSessionID = errors.New("aharness: invalid session id")
    ErrModelRequired    = errors.New("aharness: model is required")
    ErrToolConflict     = errors.New("aharness: tool name conflict")
    ErrToolMismatch     = errors.New("aharness: tool set does not match the session")
    ErrSteeringFull     = errors.New("aharness: steering queue is full")
    ErrNoSuchChild      = errors.New("aharness: no such live child session")
)

type ConfigError struct {
    Field  string
    Value  string
    Detail string
}
func (e *ConfigError) Error() string

// ProviderError is available when errors.As can classify a provider failure.
type ProviderError struct {
    StatusCode int
    Retryable  bool
    Err        error
}
func (e *ProviderError) Error() string
func (e *ProviderError) Unwrap() error
```

Public API compatibility rules:

- Adding a field to `Config`, `RunOptions`, `ModelSpec`, `Tool`, `ToolResult`,
  `State`, `Result`, or an event struct is backward compatible.
- Adding a concrete event type is backward compatible; consumers should have a
  default branch.
- Adding a method to `Model` or `Tool` is not allowed; new capabilities are new
  optional interfaces or fields.
- Renaming or removing an exported identifier requires a major version.
- Session schema v2 remains stable across library minor versions. The additive
  `session_start.custom_tools` (including `concurrency_safe`) and
  `session_start.contract_override` fields are part of v2's tolerated
  unknown-field policy. They are forward-only: a reader that predates them must
  fail closed rather than replay a different contract.

### 3.10 Call-site examples

Minimal blocking use:

```go
model, err := aharness.DiscoverModel(ctx, aharness.DiscoverModelOptions{
    Catalog:  aharness.CatalogConfig{StateDir: stateDir},
    Provider: "opencode-go",
    Model:    "deepseek-v4.1-flash",
    APIKey:   os.Getenv("AHARNESS_PROVIDER_TOKEN"),
})
if err != nil {
    return err
}
ag, err := aharness.New(ctx, aharness.Config{
    Workspace: "/repo",
    StateDir:  stateDir,
    Model:     model,
})
if err != nil {
    return err
}
defer ag.Close()

res, err := ag.Run(ctx, "Fix the failing test", aharness.RunOptions{})
if err != nil {
    log.Printf("run status %s: %v", res.Status, err) // status is still authoritative
}
fmt.Println(res.FinalText)
```

Streaming and steering:

```go
run, err := ag.Start(ctx, "Review the diff", aharness.RunOptions{})
if err != nil {
    return err
}
defer run.Close()

for ev, err := range run.Events() {
    if err != nil {
        return err
    }
    switch ev := ev.(type) {
    case aharness.TextDelta:
        fmt.Print(ev.Text)
    case aharness.ToolStarted:
        fmt.Fprintf(os.Stderr, "\n[tool %s]\n", ev.Call.Name)
    case aharness.RunFinished:
        fmt.Fprintf(os.Stderr, "\nstatus: %s\n", ev.Result.Status)
    }
}
```

A custom tool and a policy denial:

```go
tools := &aharness.ToolsConfig{
    Extra: []aharness.Tool{{
        Name:        "lookup_ticket",
        Description: "Look up an issue by ID.",
        InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
        Run: func(ctx context.Context, call aharness.ToolCall) (aharness.ToolResult, error) {
            return aharness.TextResult("ticket body"), nil
        },
    }},
}
cfg := aharness.Config{
    Workspace: "/repo",
    StateDir:  stateDir,
    Model:     model,
    Tools:     tools,
    Authorize: func(ctx context.Context, req aharness.ToolRequest) error {
        if req.Tool == aharness.ToolRunCommand {
            return &aharness.Deny{Reason: "this host does not allow shell commands"}
        }
        return nil
    },
}
```

Discovering and resuming:

```go
st, err := aharness.InspectSession(ctx, stateDir, sessionID)
if err != nil {
    return err
}
model, err := aharness.OpenAIModel(aharness.OpenAIModelOptions{
    Spec:   st.Model, // recorded dialect, base URL, limits, pricing
    APIKey: os.Getenv("AHARNESS_PROVIDER_TOKEN"),
})
if err != nil {
    return err
}
ag, err := aharness.Resume(ctx, aharness.Config{
    StateDir: stateDir,
    Model:    model,
}, sessionID)
if err != nil {
    return err
}
```

Overriding the contract on fork:

```go
ag, err := aharness.Fork(ctx, aharness.Config{
    StateDir:     stateDir,
    Model:        model,
    SystemPrompt: analystPrompt, // replaces the recorded prompt in the new log
    Tools:        noShellTools,  // new grant, schemas, and concurrency declarations
}, sessionID)
if err != nil {
    return err
}
defer ag.Close()

st, err := ag.State()
if err != nil {
    return err
}
fmt.Println(st.ContractOverride.Prompt, st.ContractOverride.Tools) // true true
```

The examples deliberately pass every dependency explicitly. The library has no
implicit environment, config-file, or working-directory layer; that stays in the
CLI.

## 4. Semantics

### 4.1 Lifecycle and ownership

```text
New/Resume/Fork
      │
      ▼
   idle ── Start/Run ──► running ── terminal ──► idle
      │                    │
      │                    ├─ Interrupt: cancel current operation, run continues
      │                    └─ Cancel: run stops with interrupted
      ▼
   Close (idle or running)
```

- One `Agent` owns one workspace `*tools.Workspace`, one `*session.Store`, and
  one model configuration.
- One active run per agent. `Start`/`Run` return `ErrBusy` otherwise.
- The store is opened once and reused across runs. Each run appends
  `run_start`/`run_end`; `session_start` is written exactly once.
- `Close` cancels and waits, then closes the store. It is safe to call twice.
- A failed store append poisons the store (`session.Store` already enforces
  this); the run returns `StopInternalError` and subsequent runs fail with the
  same error until `Close`. This is documented, not papered over.
- `Agent` is not copyable after use. `New` returns a pointer.

### 4.2 Runs

- A run records the task as a `user_message`, then drives the existing loop.
- `Run` uses the same pipeline as `Start` with a discard consumer, so both have
  identical semantics. `Run` does not request streaming deltas.
- `Start` sets `Stream=true` internally; if the model does not implement
  `StreamingModel`, the loop falls back to `Complete` and emits only
  `ResponseCompleted`.
- Per-run `Model`/`ThinkingEffort` overrides are recorded in `run_start` and
  apply only to that run. They do not rewrite `session_start`.
- `MaxTurns` and `MaxTotalTokens` are cumulative session budgets enforced by the
  existing runner. They are configuration for this agent, not inherited.
- `Timeout` maps to the internal per-run wall clock.

### 4.3 Concurrency and backpressure

- The run executes on one library-owned goroutine. The event stream is an
  unbuffered channel; a slow consumer slows the loop. This is deliberate
  backpressure and is documented in `Run.Events`.
- The display pump's send selects on the run context, so cancellation unblocks
  a send that is already waiting. A context-caused stop returns nil from the
  display callback; the facade never turns consumer shutdown into an
  `errDisplay`, so the runner records `interrupted` rather than a false
  `internal_error`. A display error is returned only for a library-internal pump
  failure. The runner goroutine owns the send side and closes the channel after
  setting the result; `Events` and `Close` own the receive side and drain.
  `Close` starts its drain before cancelling when it already owns the receive
  side. `Close` must not be called concurrently with `Events`; use `Cancel` and
  let the iterator finish, or `Close` without ranging.
- `Events` is single-use. A second call, or a call after `Close`, yields
  `ErrIteratorConsumed`.
- `Steer` returns `ErrClosed` after the run is done; it does not panic.
- Breaking out of the `for range` calls `Run.Close` semantics: cancel the run
  and drain remaining events until the runner exits, so no goroutine is left
  blocked on a send.
- `Run.Close` may be called without consuming `Events`; it cancels and drains.
- `Result` is set before `Done` closes. It is safe to call after `Done`.
- `Steer` never blocks. The queue is bounded (16 lines); a full queue returns
  `ErrSteeringFull`. A line is consumed at the next turn boundary, matching the
  existing runner.
- `SteerChild` addresses a live child session started by the active run. It
  does not create a second run or a second store writer. The line is consumed
  at the child's next turn boundary, recorded in the child log with
  `source=subagent-steer`; the parent transcript gains nothing, matching the
  existing TUI child focus. After the child finishes, it returns
  `ErrNoSuchChild`.
- `Interrupt` is a no-op when no operation is in flight. It cancels the
  operation context; the runner records an `interrupt`, appends an interrupt
  hint, and continues the run, matching the TUI's first `Esc`.
- `Cancel` cancels the run context; the runner records `interrupted` and
  terminates.
- `Close` on the agent during a run cancels, drains, and waits before closing
  the store.
- Tool calls within one model response are sequential: the loop has at most one
  tool execution in flight. This is what makes the log order, replay order,
  `Authorize` serialization, canonical-argument stuck detection, and
  two-`edit_file` safety true. Interrupt and cancel target that one call; the
  existing process-group kill is unchanged.
- Concurrent tool execution is out of scope in v1. The gate is measured, not
  argued: compute the sum of recorded `tool_result` durations over multi-call
  turns divided by run wall clock from the session logs. If it exceeds 25–30%
  across the sample, reopen the plan with an opt-in `RunOptions` flag recorded
  in `run_start`, per-call process groups, and stuck detection keyed per tool
  signature. Otherwise concurrency optimizes the smaller term; model latency
  dominates.
- A batch custom tool may fan out internally. It returns one `ToolResult` in
  deterministic order and owns its own rate limit and cache; the loop still
  sees one call and one result. This is the preferred escape hatch.
- Delegated children may run concurrently under §3.8. Events from different
  children may interleave, but each child's sequence is ordered by
  `ChildSession`.

### 4.4 Durable vs display

- The session log is the only durable truth. Events are observation only; they
  are not replayed and their deltas are not logged.
- The event text is sanitized for terminal control characters by the existing
  display boundary. A host that needs the raw text reads the log.
- `ResponseCompleted` carries the authoritative response text, reasoning
  (display-only), tool calls, finish reason, usage, latency, and whether it was
  streamed.
- `ToolCompleted` carries the structured result details (for example the todo
  checklist) and the capped output, matching the current display contract.
- The internal `RunFinished` is emitted after `final` and `run_end` are durable.
  The facade synthesizes exactly one public `RunFinished` from the returned
  `Result` if the internal event was not emitted; a synthesized event can mean
  the log is incomplete (check `Result.Status`). It carries the final `Result`,
  the reason string when available, and the wall-clock duration.
- Unknown or future events are ignored by consumers; the interface is sealed so
  only the library can add them.

### 4.5 Event order

The order is the existing display contract:

```text
RunStarted
  per turn:
    ModelStarted (not exposed; internal retries become Notice)
    TextDelta*/ReasoningDelta*
    ResponseCompleted
    per tool call:
      ToolStarted*
      [Authorize]
      ToolCompleted*
    MessageRecorded* (task/steering/hint)
    Notice* (retry/compaction/limit/interrupt/repair/warning/error)
    SubagentStarted/Progress/Finished* (delegate calls)
RunFinished
```

`ModelStarted` is intentionally not public: it is transport-level and the
existing display does not require it. Retries surface as `NoticeRetry`. A policy
denial still emits `ToolStarted` and `ToolCompleted` with
`Code="permission_denied"`. Repair of an unpaired call emits a `ToolCompleted`
with `Code="canceled"` and a `NoticeRepair`, matching today.

Tool calls in one turn are executed and emitted in the order the model returned
them; there is no interleaving within a turn. A concurrent `delegate` call may
interleave `SubagentStarted`/`SubagentProgress`/`SubagentFinished` events from
different children, but each child's sequence is ordered and identified by
`ChildSession`. Consumers must not assume a child's events are contiguous.

### 4.6 Stop and error mapping

| Internal status | Public `StopReason` | `Result.Err` | `Agent.Run` error | Iterator final error |
|---|---|---|---|---|
| completed | `StopCompleted` | nil, or a post-terminal display failure | same as `Result.Err` | nil; cause is in `RunFinished.Result.Err` |
| max_turns | `StopMaxTurns` | nil | nil | nil |
| token_limit | `StopTokenLimit` | nil | nil | nil |
| time_limit | `StopTimeLimit` | nil | nil | nil |
| provider_error | `StopProviderError` | non-nil | same as `Result.Err` | nil; cause is in `RunFinished.Result.Err` |
| protocol_error | `StopProtocolError` | non-nil | same as `Result.Err` | nil; cause is in `RunFinished.Result.Err` |
| stuck | `StopStuck` | non-nil | same as `Result.Err` | nil; cause is in `RunFinished.Result.Err` |
| interrupted | `StopInterrupted` | nil | nil | nil |
| internal_error, terminal recorded | `StopInternalError` | non-nil | same as `Result.Err` | nil; cause is in `RunFinished.Result.Err` |
| internal_error, no terminal record | `StopInternalError` | non-nil | same as `Result.Err` | nil; synthesized `RunFinished`, log is incomplete |
| no `Result` produced | n/a | n/a | non-nil | non-nil |

`Result.Err` and the `Agent.Run` error are the same value. The iterator never
duplicates a cause that was already delivered in `RunFinished`; its final error
means "the library produced no Result, do not assume the log is complete."
Any status can carry a post-terminal display error without changing `Status`
(the common case is completed or internal after a failed `RunFinished` emit);
check the log before treating it as a failed run. Provider HTTP status and
retryability are available through `ProviderError` when `errors.As` applies.

### 4.7 Resume, fork, and inheritance

| Value | New | Resume | Fork |
|---|---|---|---|
| Workspace | required | inherited; explicit must match | inherited; explicit must match |
| StateDir | required | required | required |
| Model implementation | required | required | required |
| Model spec | required | inherited when `Spec.ID == ""`; explicit override otherwise | same |
| Provider/dialect/base URL | from spec | inherited unless model override | inherited unless model override |
| Context limits | derived from spec | recorded limits; explicit max values clamped, derived fields recomputed, model override re-derives | same |
| System prompt/instructions | recorded | inherited; `Instructions`/`SystemPrompt` must be empty | inherited when empty; explicit values replace the copied prompt and record `contract_override.prompt` |
| Tool grant and custom schemas | recorded | must match recorded availability, schemas, and concurrency declarations; caller supplies implementations | inherited when `Tools` is nil; an explicit value replaces the copied contract and records `contract_override.tools` |
| Contract override record | — | contract changes rejected | `contract_override` names prompt/tool changes; the parent log is unchanged |
| Thinking effort | explicit or provider default | inherited latest unless explicit | same |
| Run budgets | config | config (never inherited) | config |
| Parent metadata | none | none added | parent session/seq |

`ModelSpec.ThinkingEfforts` is not recorded in the log; the recorded effective
effort and model are. A zero-spec resume therefore trusts the recorded effort
rather than revalidating it against a catalog. An explicit model override
validates the chosen effort against the supplied spec's advertised list (when
that list is non-empty).

`ThinkingEffort` is tri-state on both `Config` and `RunOptions`: nil inherits the
agent's or session's value, a pointer to `""` clears it to the provider default,
and a non-empty pointer sets it. The engine carries the same `*string` so the
distinction survives the facade boundary. An explicit model override with no
explicit effort clears the inherited effort to the provider default, matching
the CLI's model-switch rule; set an explicit effort if the new model needs one.

Explicit limits on resume are clamped, not applied blindly: `MaxContextTokens`
is capped at the model window, `SafetyMargin` and `MaxInputTokens` are
recomputed, and `CompactionTarget` follows the input limit, matching the CLI's
`validateEffective`. A recorded `CompactionTarget` tightened by a provider
overflow remains the session's effective target through the existing runner
state, not through a new recorded-limit override.

"Must match" means the library compares names, custom schemas, and custom-tool
concurrency declarations and returns `ErrToolMismatch` with a diff summary in
the message. Tool implementations are not comparable; only their recorded
schemas and declarations are. A CLI-created TUI session records a nil grant,
which means every tool of its recorded toolset, including `delegate`; resuming
it through the library requires a `DelegationConfig`, or a `Fork` with an
explicit `Tools` value to adopt a different contract. A library-created session
records an explicit grant, so this ambiguity does not arise for new sessions.

`Fork` copies history into a new self-contained log; it does not fork the
workspace or uncommitted files. This is documented in the package doc.

The system prompt is frozen per session at `session_start` because projection
uses it verbatim. `Resume` rejects `Instructions` or `SystemPrompt` with a
`ConfigError` rather than silently ignoring them. `Fork` is the supported
contract-change path: it renders the new prompt, validates the new tool set,
custom schemas, and concurrency declarations, writes them into the new
`session_start`, and records `contract_override` with the parent session and
which parts changed. The parent log is never rewritten.

Copied history is message history, not a schema replay. It is replayed under the
new prompt, and a recorded tool call or result from the old contract remains a
message. A tool name in copied history that is absent from the new grant is
allowed; it is not made available to the model and is never re-executed. This is
what makes prompt and tool correction possible without discarding a useful
conversation, and why a fork is self-describing rather than a hash mismatch.

Provider behavior is the limit. A dialect that rejects a historical tool call
whose name is not in the current declaration will fail the first request with a
provider error; the library does not rewrite history to hide it. In that case
fork with the old grant, or start a new session.

### 4.8 Custom tools, grants, and projection

The existing `Project` function selects built-in schemas from the recorded
`toolset_version` and filters them by the recorded grant. Custom tools require
three changes:

1. `session_start` records `custom_tools` as an ordered array of
   `{name, description, parameters, concurrency_safe}`. It is capped at 32 tools
   and 64 KiB total; a tool's description is capped at 2 KiB, and its parameters
   at 32 KiB. The array is part of the v2 payload; readers that do not know it
   ignore it, and a reader that does not know `concurrency_safe` treats it as
   false rather than replaying a wider fan-out.
2. `ProjectWithTools(events, limits, availableCustomTools)` takes the run's
   available custom schemas and declarations. It appends them only after
   verifying they match the recorded set semantically (names, descriptions,
   canonical JSON parameters, `concurrency_safe`). A recorded custom tool the
   caller did not supply is an error before the first model call, and a custom
   name that collides with any built-in (enabled or disabled) is a construction
   error and never reaches a log. The existing `Project(events, limits)` remains
   as the built-in-only entry point and returns an error if the session records
   custom tools. The runner uses `ProjectWithTools` with `Config.CustomTools`;
   the CLI/TUI uses `Project`, so a custom-tool session opened by a reader that
   cannot provide the implementations fails closed instead of sending schemas it
   cannot execute. On resume, the caller's custom tools must reproduce the
   recorded array; no partial match is allowed. The custom tool set is fixed per
   session; `Fork` with an explicit `Tools` value is the supported way to change
   it.
3. Grant presence becomes explicit. `nil` means every tool of the recorded
   version; a non-nil empty slice means no tools. The current code collapses an
   empty grant to nil in several places, which would make a public
   `ToolsConfig.Disable` of every tool silently enable all of them:
   - `tools.Registry.SetGrant` uses `append([]string(nil), names...)`, which
     yields nil for an empty non-nil input;
   - `agent.WriteSessionStart` copies `cfg.Tools` the same way, so an explicit
     empty grant records as absent;
   - `sessionStartData.Tools` uses `omitempty`, so an explicit empty grant
     records as absent;
   - `agent.Inspect` reconstructs `State.Tools` with the same append;
   - the spawner's `childConfig` and `continuedConfig` clone `def.Tools` /
     `state.Tools` the same way, and `continuedConfig` substitutes every
     built-in when the recorded grant is nil (or collapsed to nil);
   - `cmd`'s `applySessionDefaults` collapses an explicit empty grant, and
     `batchTools` then enables the full batch set.

   The plan fixes every writer: preserve a non-nil empty slice in `SetGrant`,
   `WriteSessionStart`, `Inspect`, child configs, and CLI resume defaults; drop
   `omitempty` from the recorded `tools` field (`null` still means all; `[]`
   means none); and make `batchTools`/`continuedConfig` branch on nil, not
   `len == 0`. `Project`, `Filter`, `Registry.granted`, and the public
   normalization are then consistent. This is a prerequisite for the public
   tool controls, not a library-only nicety.

The recorded grant is always explicit for library-created sessions:
`Config.Tools` is normalized to the enabled built-in names plus custom names,
minus disabled, including `delegate` only when delegation is configured. An
empty result is a non-nil empty grant. This avoids the current "nil grant means
every tool of the version" trap when delegation is not wired. The CLI keeps its
existing grant rules.

`Registry.Schemas()` is not the projection source; `Project` is. `Schemas()`
includes the registered custom schemas so construction-time grant validation and
inspection see the same set the model will see. The runner sends only
`proj.Schemas`.

A custom tool's `Run` may be called concurrently only across delegated children
and only when it declared `ConcurrencySafe`. The per-child registry factory
registers the same tools for each child; the declaration is recorded and must be
reproduced on resume. A tool that mutates shared host state must leave it false.
A batch tool that fans out internally is the supported way to parallelize
within one session or one tool call; it must own its ordering, rate limit, and
cache, and return one `ToolResult`.

Context shielding is a tool concern, not an agent concern. A custom tool may
call a model, map-reduce over fetched sources, and return a bounded result. It
does not create a child session or a nested delegation, and its model usage is
the caller's to record in `ToolResult.Details`; it is not folded into
`Result.Usage`. This is the supported way for a child to inspect more material
than its own projection should retain.

### 4.9 Security

- The library adds no sandbox and no approval UI. `Authorize` is a policy hook.
  A custom tool can do anything the host process can do; the hook is not a
  security boundary against malicious host code.
- Built-in file tools remain confined by `os.Root` and the `.git`/symlink
  policy. `run_command` is not confined; it inherits the host environment
  through the existing allowlist and can use the host network. The host must use
  a container, VM, or equivalent for untrusted tasks.
- `web_fetch` keeps its address guard and redirect policy. `web_search` contacts
  only the configured SearXNG base URL.
- API keys are held in memory by the caller's `ModelConfig`; they are never
  recorded, logged, or exposed in `State`. The model request log option can
  record prompt content, not credentials.
- Session logs and state directories contain prompts, tool output, and possibly
  secrets. The library uses the existing 0600/0700 permissions and documents
  that the state directory must be protected.
- Event text is sanitized for terminal control characters; the raw log is not.
  A host that renders events to a terminal must preserve this boundary.
- Caller code is host code. Tool panics are recovered by the existing registry
  and become a `panic` result. The facade also recovers a panic in a caller
  `Model` and reports `StopInternalError` rather than crashing the host. A
  panic in `Authorize` is recovered and treated as a denial (the tool does not
  run); the panic text is sanitized like any other policy error. Do not rely on
  these recoveries for control flow: they exist so one bad host component
  cannot take down a long-lived process.
- `ConcurrencySafe` is a declaration, not a sandbox. The library cannot verify
  that a tool is safe to call concurrently, and a false declaration can corrupt
  host state. A host that does not need shell access should disable
  `run_command` (and any writer it does not want) and grant only deterministic
  tools; the remaining file, search, web, todo, and delegate surface is a
  read-oriented data workspace. `web_fetch` still enforces its address guard;
  set `WebFetchConfig.UserAgent` for hosts such as SEC EDGAR that require it.

## 5. Persistence and compatibility

### 5.1 Session log

The v2 schema is unchanged except for additive `session_start` fields and the
removal of `omitempty` from the recorded `tools` field. No new event types are
added. The additive fields are:

- `custom_tools`: an ordered array of
  `{name, description, parameters, concurrency_safe}`. `concurrency_safe` is
  false when absent.
- `contract_override`: present only on a fork that changed the prompt or tool
  set, as `{parent_session, prompt, tools}`. The parent ID is already in the
  parent metadata; this record names which contract parts changed.

The library does not write its own metadata outside the existing fields;
`harness_version` records the library build version. There is no per-run
concurrency flag because tool calls within a run are always sequential; only
child fan-out uses the recorded declarations.

`custom_tools` is forward-only. A reader that predates it ignores the field and
then rejects the recorded custom names during grant filtering with "tool X is
not defined by the recorded toolset". That is an explicit error, not a silent
replay with a different tool set; a session that used custom tools is resumable
only by a build that understands them. The library reports this as
`ErrToolMismatch` with the missing names. The same rule applies to a custom
model provider or dialect: the CLI rejects providers it does not know, so such a
session is library-only even though its log is valid v2.

`concurrency_safe` fails in the safe direction: a reader that predates it treats
the missing declaration as false and runs children sequentially, never the
reverse. `contract_override` is observational; ignoring it does not change
replay because the new prompt and tool set are already the recorded contract.

Removing `omitempty` makes an explicit empty grant record as `[]` while a
legacy/absent grant still decodes as nil ("all tools of the recorded version").
That is the presence contract the public tool controls rely on.

### 5.2 Module and release

- The public package is versioned with the module. Start at `v0.x` while the
  facade is new, then `v1.0.0` after the API stabilizes. `/v2` is required for
  a breaking public API.
- The session schema has its own `Version = 2`; it is not tied to the library
  major version.
- `SchemaVersion` is exported as a constant for hosts that inspect logs.
- Release notes call out changes to the public package separately from CLI
  changes.

### 5.3 CLI compatibility

- Batch mode uses the engine and, where it maps cleanly, the public facade.
  Its flags, environment, config file, stdout contract, and exit codes stay
  unchanged.
- TUI mode adopts the public run, event, result, catalog, and session surface
  in M7, before the facade is called stable. It lives in `cmd` as a rendering
  consumer; there is no public TUI package. It keeps internal log readers for
  historical transcripts and child-focus replay until a public `History` DTO is
  justified, which is why that DTO is still deferred. Model and effort pickers
  use `ListModels` and `RunOptions`; session switch uses `Sessions`,
  `InspectSession`, `Resume`, and `Fork`; child-focus steering uses
  `Run.SteerChild`; todo and child progress come from `ToolCompleted.Details`
  and the `Subagent*` events.
- Sessions created by the CLI are readable by the library; library sessions are
  readable by the CLI when their recorded provider and dialect are ones the CLI
  supports (for example `opencode-go` with an OpenAI dialect) and their grant
  contains only tools the CLI can provide. Custom-tool sessions and
  custom-provider sessions are library-only.
- A library session that records `delegate` needs the CLI's TUI path (batch
  withholds the tool); the CLI may still reject or withhold it depending on
  mode. This is existing behavior, not a new compatibility promise.

## 6. Implementation

### 6.1 Package layout

```text
<module>/
  aharness.go, run.go, events.go, result.go,
  model.go, catalog.go, tool.go, delegate.go, errors.go
  internal/engine/        # new: non-CLI composition layer
  internal/agent/         # loop, projection, prompts, spawner
  internal/model/         # canonical types and adapters
  internal/tools/         # registry, built-ins, workspace policy
  internal/session/       # v2 store and reader
  internal/tui/           # unchanged
  cmd/aharness/           # flags, TUI, signals, exit codes
```

`internal/engine` exists because two consumers now need the same new/resume/fork
wiring: `cmd/aharness` and the public facade. It must not import `internal/tui`
or `cmd`, and it must not own flags, environment, terminal I/O, signals, or
`os.Exit`. Its contract:

```go
package engine

type Mode int
const (ModeNew Mode = iota; ModeResume; ModeFork)

type Spec struct {
    Workspace string
    StateDir  string
    Mode      Mode
    SessionID string // resume/fork source

    Provider string
    Dialect  string
    BaseURL  string
    Model    string
    Limits   agent.Limits
    Pricing  model.Pricing
    ThinkingEffort *string // nil inherits/default; pointer to "" clears
    Agents         []agent.AgentInfo // parent prompt catalog
    Definitions    []agent.AgentDefinition
    ResolveModel   func(modelID, effort string) (agent.ModelResolution, error)

    NewCompleter func(dialect model.Dialect, baseURL, sessionID string) model.Completer
    NewChildCompleter func(def agent.AgentDefinition, resolution agent.ModelResolution, sessionID string) model.Completer
    NewRegistry  func(sessionID, workDir, tmpDir string) (*tools.Registry, error)
    CustomTools  []agent.CustomTool
    Grant        []string // non-nil; empty means no tools
    ToolOptions  tools.Options
    ToolPolicy   func(context.Context, agent.ToolPolicyRequest) error
    MaxSubagents int // public value: 0 selects 8, -1 means unlimited; maps to spawner 8/0

    SystemPrompt      string // New and Fork; Fork records a contract override
    AppendPrompt      string // New and Fork; Fork records a contract override
    MaxTurns          int
    MaxTotalTokens    int
    RunTimeout        time.Duration
    ModelTimeout      time.Duration
    RequireUsage      bool
    LogRequests       bool
    Version           string
    Logger            *slog.Logger
}

// RunSpec carries the per-run overrides that do not rewrite the session.
type RunSpec struct {
    Model          string
    Dialect        string
    BaseURL        string
    Limits         agent.Limits
    Pricing        model.Pricing
    ThinkingEffort *string
    MaxTurns       int
    MaxTotalTokens int
    RunTimeout     time.Duration
    ModelTimeout   time.Duration
    NewCompleter   func(dialect model.Dialect, baseURL, sessionID string) model.Completer
}

type Host struct { /* workspace, store, paths, spec */ }

func Open(ctx context.Context, spec Spec) (*Host, error)
func (h *Host) ID() string
func (h *Host) Path() string
func (h *Host) Workspace() string
func (h *Host) State() (agent.State, error)
func (h *Host) BuildRunner(run RunSpec, display func(agent.DisplayEvent) error, steering <-chan string, controller agent.Controller) (*agent.Runner, error)
func (h *Host) Close() error
```

`Open` writes and syncs `session_start` for `ModeNew` and `ModeFork` after
creating the log; for fork it then copies the parent history (excluding the
parent's `session_start`), matching `cmd` and the store's "session_start must be
event 1" rule. `BuildRunner` never writes `session_start`; calling it twice must
work for multiple runs. For `ModeFork`, an explicit `SystemPrompt` or
`AppendPrompt` replaces the inherited prompt and an explicit non-nil grant or
`CustomTools` replaces the inherited tool contract; when any of them differs
from the parent's recorded values, `Open` records `contract_override` in the new
`session_start`. Validation happens before the new log is created: a prompt
conflict, unknown tool, duplicate name, or over-cap schema leaves the state
directory unchanged. When no override is supplied, the new `session_start`
reuses the parent's recorded prompt, grant, custom schemas, and concurrency
declarations; it does not re-render a prompt from the current definition
catalog. The model spec and limits are inherited unless the caller supplied an
explicit override.

`BuildRunner` constructs the per-run registry through `NewRegistry` when
non-nil, otherwise through `tools.NewRegistry`, then applies the grant. The
factory owns custom-tool registration and web/command options; the engine owns
the per-run work directories, the grant, and the spawner. The spawner receives
the custom-tool declarations, applies the cross-child concurrency rule, and
refuses to construct a child that would itself delegate. `Host.State` is
idle-only and uses `agent.Inspect`.

When `Definitions` is non-empty, the engine constructs the per-run
`agent.Spawner` with those definitions, `ResolveModel`, `NewChildCompleter`,
`NewRegistry`, the serialized `ToolPolicy`, and `MaxSubagents` mapped to the
spawner's 8/0 convention. `NewChildCompleter` receives the resolved definition
and model resolution (model, effort, dialect, base URL, limits, pricing) so it
can build the right public `ModelConfig`; it is not restricted to the parent's
adapter. The engine never reads `<state>/agents`; file discovery stays in the
CLI.

`BuildRunner` is per run rather than per host because the registry, todo state,
spawner, and model adapter are per run. The host owns the store, workspace,
resolved spec, work directories, and cleanup. The facade owns the event pump,
public DTO conversion, and the run handle.

The engine must reproduce the current CLI rules exactly:

- new session: workspace required, state dir prepared, work dirs reset, model
  resolved/validated, session created, `WriteSessionStart`, then run;
- resume: open and inspect, inherit model/dialect/base URL/limits/effort/grant,
  validate workspace, prompt-version support, toolset support, and the caller's
  tool set, reset work dirs, then run;
- fork: open shared, inspect/inherit, create new, write `session_start` with
  parent metadata, copy history, close the shared source, then run;
- close: store close, work-dir removal, workspace close.

### 6.2 Internal changes

| File | Change |
|---|---|
| `go.mod`, all Go imports | Publishable module path; mechanical rewrite. |
| `internal/engine/*.go` | New composition layer extracted from `cmd/aharness/run.go`, `models.go`, and session helpers; write `session_start` in `Open` for new/fork before copying fork history (never in `BuildRunner`); apply fork prompt/tool overrides and record `contract_override`; compare custom tool schemas as canonical JSON and declarations exactly; own `Sessions` full-replay behavior; canonicalize `InspectSession` IDs; map public 0/-1 `MaxSubagents` to internal 8/0. |
| `cmd/aharness/run.go`, `models.go`, `sessions.go` | Replace duplicated wiring with engine calls. Behavior-preserving. |
| `internal/model/catalog.go` | Expose the normalized catalog listing (usable plus unavailable, warnings, fetch time) for the public `ListModels` and the TUI picker. |
| `internal/agent/types.go` | Add `SessionID string` (runtime-only), `CustomTool{Schema, ConcurrencySafe}`, `ContractOverride`, `AppendSystemPrompt string`, `ToolPolicyRequest`, and the `ToolPolicy` hook. |
| `internal/agent/runner.go` | Invoke the tool policy before `registry.ExecuteCall`; translate a denial to a `permission_denied` result and continue; exclude `permission_denied` from the repeated-failure/stuck key; preserve a non-nil empty grant in `WriteSessionStart`; include custom tools and their declarations in the recorded payload. Tool execution remains strictly sequential; do not add concurrency here. |
| `internal/agent/events.go` | Add `CustomTools` and `ContractOverride` to `sessionStartData`; record `concurrency_safe` per custom tool; drop `omitempty` from `tools` so an explicit empty grant is distinguishable from no grant; record the append prompt in the resolved `system_prompt`. |
| `internal/agent/context.go` | Add `ProjectWithTools(events, limits, availableCustomTools)`; keep `Project` as the built-in-only path and make it error on recorded custom tools; verify and append custom schemas before grant filtering. Projection verifies names, descriptions, and canonical parameters; concurrency is a spawner concern, not a schema concern. |
| `internal/agent/agents.go` | Export a copy of the bundled definition set for `DefaultAgents`; leave file discovery and the strict parser in the CLI. |
| `internal/agent/state.go` | Expose custom tools, concurrency declarations, `contract_override`, and recorded limits in `State`; preserve a non-nil empty recorded grant when reconstructing; keep the existing recorded prompt and parent fields. |
| `internal/agent/prompt.go` | Expose prompt rendering and validation for `Fork`; do not change `prompt-v2` text. |
| `internal/agent/spawn.go` | Inherit custom tools, declarations, and policy for child configs; add a `NewRegistry` hook so custom tools can be registered in a child; add/extend the child completer factory so it receives the resolved definition and model resolution; serialize `ToolPolicy` with the display mutex; intersect each child grant with the parent grant; preserve non-nil empty grants in `childConfig`/`continuedConfig`; select concurrent execution from the built-in read-only set plus `ConcurrencySafe` customs; reject any child configuration that would include `delegate` (depth one). |
| `internal/tools/registry.go` | Add `Register(schema, fn, concurrencySafe)` with name/schema validation and duplicate rejection; include registered schemas in `Schemas()`; preserve a non-nil empty grant in `SetGrant`; make `granted` treat nil as all and empty as none. |
| `internal/tools/result.go` | Add `CodePermissionDenied`, `CodeToolError`; document the public mapping. |
| `internal/model/openai_chat.go`, `openai_responses.go`, `http.go` | Add constructor variants that accept a caller `*http.Client`, so the public adapter can honor `OpenAIModelOptions.HTTPClient` without changing the existing constructors. |
| `internal/tools/web_fetch.go` | Thread an operator `UserAgent` through the builtin and Crawl4AI providers; default to the current value when empty. |
| `cmd/aharness/sessions.go`, `run.go` | Preserve an explicit empty grant in resume defaults; branch on nil rather than length in `batchTools`; keep the CLI grant rules otherwise unchanged. |
| `internal/tools/delegate.go` | No signature change. |
| `internal/tui/*` | No rendering change in v1. M7 replaces its runner wiring with the public `Agent`/`Run`/event/session surface; historical and child-focus transcript replay stay on internal readers until a public `History` DTO is justified. No public TUI package. |
| `docs/*` | Update architecture §3.1/§3.2/§7, limits for the custom-tool caps, testing strategy, README usage and the coding-SDK boundary. |

The policy hook is a deliberately small internal change:

```go
// In agent.Config:
SessionID string // runtime-only; not recorded
Agent     string // definition name for a child; empty for the session agent
ToolPolicy func(ctx context.Context, req ToolPolicyRequest) error

type ToolPolicyRequest struct {
    Call    tools.Call
    Name    string
    Args    json.RawMessage
    Turn    int
    Session string
    Agent   string
}
```

The runner builds the request from its own session ID, `Config.Agent`, the turn,
and the canonical call, so a delegated child reports its own session and
definition name. The spawner sets the child's `SessionID` when it opens the
store; no per-child closure is required.

`agent.CustomTool` is the single internal carrier so registration, projection,
recording, and the spawner cannot disagree:

```go
type CustomTool struct {
    Schema          model.ToolSchema
    ConcurrencySafe bool
}
```

`ProjectWithTools` verifies `Schema`; the spawner reads `ConcurrencySafe`;
`WriteSessionStart` records both. A public `Tool` maps to exactly one
`agent.CustomTool`; no other code path may set the declaration.

The runner invokes it after `ToolStarted`, before `registry.ExecuteCall`. A
non-nil error produces `tools.Failure(tools.CodePermissionDenied, err.Error())`
and the normal `ToolCompleted` path. `context.Canceled`/`DeadlineExceeded` are
returned to the existing cancellation path instead. The dispatcher still
enforces the recorded grant first, so availability and permission remain
separate. A permission denial is a policy outcome, not a tool malfunction: the
runner excludes `permission_denied` from the repeated-failure key that leads to
`StopStuck`, so a host cannot accidentally turn a deliberate deny into a
terminated run. The spawner serializes the policy with the display mutex, so a
single `Authorize` callback is never entered concurrently by parent and
read-only child runs.

### 6.3 Facade mapping

| Public | Internal |
|---|---|
| `Config` | `engine.Spec` plus `agent.Config` runtime fields |
| `ModelConfig.Completer` | `model.Completer`, optionally wrapped to satisfy `model.StreamCompleter` |
| `ModelConfig.NewCompleter` | adapted to `engine.Spec.NewChildCompleter(def, resolution, sessionID)`; child resolution builds a `ModelConfig` per definition |
| `ToolsConfig.Extra` | `engine.Spec.NewRegistry` factory plus `tools.Registry.Register`; also `engine.Spec.CustomTools` for recording, projection, and spawner declarations. The same factory is used for delegated children. |
| `ModelSpec` | `engine.Spec` plus `run_start` fields |
| `ToolsConfig` | `tools.Options`, registry `Register`, normalized grant |
| `Tool` | `tools.Registry.Register` entry with its `ConcurrencySafe` declaration |
| `ToolsConfig.WebFetch.UserAgent` | `tools.Options` web-fetch user agent |
| `ListModels` | `internal/model` catalog load plus `Usable`/entry mapping; read-only |
| `Fork` overrides | `engine.Open` `ModeFork`; prompt rendering, tool/schema/declaration validation, and `contract_override` recording |
| `Run.SteerChild` | the spawner's live child steering channel, keyed by child session ID |
| `Authorize` | `agent.Config.ToolPolicy` |
| `Agent` | `engine.Host` plus a mutex/active-run handle |
| `Run` | a goroutine over `agent.Runner.Run`, an event channel, a controller, and a steering channel |
| `Event` | `agent.DisplayEvent` mapped to public structs |
| `RunFinished` | mapped from the internal event, or synthesized exactly once from the runner's returned `Result` if the internal event was not emitted |
| `Result` | `agent.Result` mapped; `Err` becomes the returned error |
| `State` | `agent.Inspect` mapped, plus session timestamps, recorded limits, custom tools, concurrency declarations, contract override, and parent identity |
| `State.Model` | recorded `session_start`/`run_start` fields |
| `State.CustomTools` | `session_start.custom_tools`; `State.ConcurrencySafe` is the subset with `concurrency_safe=true` |
| `State.ContractOverride` | `session_start.contract_override` |
| `InspectSession` | `session.CanonicalID` + `session.ReadSnapshot` + `agent.Inspect`; no exclusive lock |
| `Sessions` | `session.ReadSnapshot` plus `agent.Inspect`/`agent.Summarize`; full replay for turns/status/timestamps |
| `DelegationConfig` | `agent.SpawnerOptions` (definitions, `Resolve`/`NewChildCompleter`, `MaxSubagents`), `engine.Spec.Agents`/`Definitions`, `engine.Spec.NewRegistry`, and `tools.Options.Spawning` |

The facade conversion is mechanical. It should live in one file per direction
(public-to-internal in construction, internal-to-public in events/results) and
have table tests for every field that matters.

### 6.4 CLI migration

M5 migrates batch mode first:

- `executeNew`/`executeResume`/`executeFork` call `engine.Open`.
- `executeRun` calls `host.BuildRunner` with the existing text display and
  signal controller, then `runner.Run`.
- `cmd` keeps flag parsing, catalog actions, TUI mode, exit-code mapping, and
  `os.Exit`.
- The existing integration tests are the regression suite; they must pass
  without changing expectations.

TUI mode migrates in M7, after the facade covers its needs. The public surface
already provides per-run model switching (`RunOptions.Model`), model listing
(`ListModels`), the session shell (`Sessions`, `InspectSession`, `Resume`,
`Fork`), child steering (`Run.SteerChild`), and live child progress
(`Subagent*`). Two things deliberately stay internal: historical transcript
replay and the focused child's read-only transcript, both read from the JSONL
log through `internal/session`. A public `History` DTO is deferred until an
external host needs message-level replay. `-ui-json` snapshots remain an
internal TUI rendering surface and are not part of the library API.

Migration order: batch first (M5), then the TUI's run and session construction
(M7), then delete the duplicated `cmd` wiring. The TUI keeps its own Bubble Tea
types; it imports public `aharness` DTOs, not `internal/agent` or
`internal/model`.

## 7. Testing

### 7.1 Unit

- Public DTO conversions: model request/response, usage, pricing, limits,
  events, result, state. Table tests with representative zero/nil/full values.
- Config validation: missing workspace/state/model, negative budgets, bad tool
  names, duplicate tools, built-in collisions, invalid schema, unknown disabled
  tool, prompt conflict, invalid effort, and delegated definitions with missing
  fields, duplicate names, `delegate` in a grant, or an unknown tool.
- Resume/fork validation: workspace mismatch, tool mismatch, missing model
  completer, zero-spec inheritance, explicit model override, and rejection of
  `Instructions`/`SystemPrompt` on resume.
- Custom-tool schema caps and canonical-JSON validation.
- Grant presence: nil means all recorded tools, a non-nil empty grant means
  none, and `[]` survives `SetGrant`, session encode/decode, projection, child
  config, and dispatch.
- Policy: a denial produces a model-visible result, is excluded from the
  repeated-failure key, and is serialized across concurrent child fan-out.
- Concurrency declarations: recorded, reproduced on resume, and used by the
  spawner; a mixed grant (a read built-in plus an undeclared custom tool) is
  sequential; an all-safe custom grant is concurrent; `Authorize` remains
  serialized; a changed declaration is an `ErrToolMismatch`.
- Fork overrides: prompt, grant, schema, and declaration changes are validated
  before the new log exists; `contract_override` is recorded; `Resume` rejects
  the same inputs; copied history with a tool absent from the new grant still
  replays.
- `ListModels`: usable and unavailable entries, warnings passthrough, and no
  session or model creation.
- `SteerChild`: unknown, finished, and non-child session IDs return
  `ErrNoSuchChild`; a full child queue returns `ErrSteeringFull`.
- Sequential execution: three calls in one response produce three ordered pairs
  with no time overlap, and canonical-argument stuck detection sees the same
  order it always did.
- Child grants: a definition naming a tool outside the parent grant fails
  closed; a resumed child is narrowed by the current parent; a child definition
  that would include `delegate` is rejected.
- `Sessions` and `InspectSession` against a live writer (snapshot read, no torn
  line error) and a log large enough to exercise the full-replay path.

### 7.2 External API tests

All public tests live in `package aharness_test` and use only the public API.
They prove:

- an external module can import the package (a small `testdata` module with a
  `replace` directive is built in CI);
- `example_test.go` compiles and its output is stable;
- a fake `Model` drives text, tool calls, and a final answer;
- every event type is observed at least once;
- the blocking and streaming paths produce the same result/text;
- a custom tool is called, recorded, and replayed on resume;
- `Authorize` denial is model-visible, repeated denials do not become
  `StopStuck`, and the run continues;
- interrupt, cancel, steer, and close-during-run terminate cleanly, including
  `Close` with an unconsumed event stream (status `interrupted`, not a false
  `internal_error`) and a completed run whose post-terminal display failed
  (Status stays completed; Err is diagnostic);
- a second concurrent run returns `ErrBusy`;
- an iterator break cancels and leaves no goroutine blocked (use
  `testing/synctest` or a bounded wait with a fail-fast timer);
- the research acceptance shape builds and runs from an external module: a data
  workspace, `run_command` disabled, a replaced prompt, four deterministic
  custom tools with explicit schemas, three analyst definitions, and no network
  or shell;
- a public-surface transcript renderer builds text, tool blocks, the todo panel,
  the subagent roster, usage/cost, and terminal status from `Events()` and
  `Result` alone, without reading the log;
- fork with prompt and tool overrides works from an external package, and
  `State.ContractOverride` reports the change.

### 7.3 Integration

- `t.TempDir()` workspace and state directory; fake model; fake SearXNG/fetch
  endpoints via `httptest`.
- Assert the JSONL log: `session_start` fields, `run_start`/`run_end`, user
  message, model response, tool result pairing, final, and `custom_tools`.
- Resume the log with a new `Agent` and assert projection/limits/effort/tools.
- Fork and assert copied history, new session ID, parent metadata, and that the
  source is unchanged. Fork again with an explicit prompt and tool set and
  assert the new `session_start` records both plus `contract_override`, while
  `Resume` on the source rejects the same inputs.
- Delegated concurrency: an all-read-only grant and an all-`ConcurrencySafe`
  custom grant run concurrently; a mixed or undeclared grant runs sequentially;
  `details.execution` and its reason match; child events carry distinct
  `ChildSession` values and each child's sequence is ordered.
- After M7, the TUI's run/session construction goes through the public facade and
  `-ui-json` frames and snapshots are unchanged for a fixed script; the TUI has
  no `internal/agent`/`internal/model` imports for run construction.
- CLI compatibility: run the existing batch integration tests; resume a
  library-created session through the CLI and vice versa when the recorded
  provider/dialect and grant are CLI-reproducible. Assert a clear error, not
  silent replay, when the CLI (which has no custom tools) opens a custom-tool
  session.
- Catalog: `httptest` catalog source, no real network; cache and stale-fallback
  behavior comes from the existing tests.

### 7.4 Compatibility and fuzz

- Keep the existing session fixtures and fuzzers. Add a fixture with
  `custom_tools` (including `concurrency_safe`) and one without; both must
  replay, and the absent declaration defaults to false.
- Add a `contract_override` fixture and a forked fixture whose copied history
  references a tool absent from the new grant; both must replay without
  re-executing the old tool.
- Add a fuzz target for custom-tool schema decode/caps if the decoder is new.
- Run `go test -race -count=1 ./...` and the existing `testing/synctest` tests.

### 7.5 Manual checks

- Build the binary and run batch mode against the fake/local endpoint.
- Use a small external program (checked into `testdata` or the docs example) to
  verify the import path and API ergonomics.
- Run one real-provider session when `AHARNESS_PROVIDER_TOKEN` is available;
  record it as a release prerequisite, not a CI dependency.
- Verify terminal sanitization by feeding control characters through a fake
  model and confirming events are display-safe while the log is raw.

## 8. Milestones

### M0 — Publishable module

- Choose the module path and repository URL.
- Rewrite `go.mod` and the internal imports; update docs that name the module.
- Add an external-module build check to CI.
- **Exit:** `go build ./...`, `go test ./...`, and the external import check pass
  with the new path; no behavior change.

### M1 — Internal engine and grant presence

- Add `internal/engine` with `Open`, `BuildRunner`, `State`, and `Close`.
- Move the new/resume/fork resolution and work-dir logic from `cmd`.
- Fix grant presence first: preserve a non-nil empty grant in `SetGrant`,
  state reconstruction, child configs, and `session_start`; add a regression
  test that nil means all and `[]` means none. This is behavior-preserving for
  existing sessions (they have no explicit empty grant) and a prerequisite for
  the public tool controls.
- Keep batch CLI behavior identical; keep the TUI on its current path.
- **Exit:** existing CLI tests pass; new engine unit tests cover the inheritance
  matrix and grant presence; no public API yet.

### M2 — Public core

- Add the root package: `Config`, `Agent`, `New`/`Resume`/`Fork`, `Close`,
  `State`, blocking `Run`, `Result`, `StopReason`, `Usage`, `Limits`.
- Add the public model DTOs, `Model`, `OpenAIModel`, `DiscoverModel`, and
  `ListModels`.
- Add built-in tools with the default grant; no custom tools yet.
- Add external-package tests and the first runnable example.
- **Exit:** a fake model completes a task; a session log is written and
  resumed; the public API compiles from an external module.

### M3 — Events and control

- Add the `Event` interface, all concrete event mappings, and `Run`.
- Add `Start`, `Events`, `Interrupt`, `Cancel`, `Steer`, `Done`, `Result`,
  `Close`, `ErrBusy`, `ErrIteratorConsumed`.
- Add backpressure, break-to-cancel, and goroutine-leak tests.
- **Exit:** every event is observed; blocking and streaming results agree; race
  tests pass.

### M4 — Tools and policy

- Add `Tool`, `ToolResult`, `ToolName`, `ToolsConfig`, `Authorize`, and
  `Deny`.
- Add `Registry.Register`; normalize grants; add `permission_denied` and
  `tool_error`.
- Add `custom_tools` recording, projection, resume validation, caps, and the
  `ConcurrencySafe` declaration.
- Add `WebFetchConfig.UserAgent`.
- Add `Fork` contract overrides for prompt, grant, schemas, and declarations,
  plus `contract_override` recording and strict `Resume` rejection.
- Add custom-tool and policy integration tests, including repeated denials not
  producing `StopStuck`, an unconsumed-event `Close` that terminates, safe
  custom fan-out, and sequential fallback for undeclared tools.
- **Exit:** a custom tool is model-visible, executes, is denied, and survives
  resume; a session with custom tools fails closed without them; a policy hook
  is never entered concurrently within one session; fork overrides are recorded
  and resume overrides are rejected.

### M5 — CLI dogfooding

- Migrate batch mode to `engine` and the public facade where the mapping is
  clean.
- Add the external example to the README and the package doc.
- Keep every existing CLI test and exit code.
- **Exit:** batch and TUI behavior unchanged; the CLI is a facade consumer for
  the non-TUI path.

### M6 — Delegation and session helpers

- Add `AgentDefinition`, `DelegationConfig`, and `DefaultAgents`.
- Wire the spawner through the engine; add the per-child registry factory;
  inherit policy and custom tools; intersect child grants with the parent grant;
  validate definitions; select concurrent execution from read-only built-ins
  plus `ConcurrencySafe` customs; reject a child configuration that includes
  `delegate`.
- Add `InspectSession`, `Sessions`, and `SessionInfo`.
- **Exit:** a delegated child runs, records parent metadata, and is visible in
  `State`; a custom tool works in a child; a safe custom grant fans out and a
  mixed grant stays sequential; a child cannot widen the parent grant or
  delegate; a caller can list and resume sessions through the library.

### M7 — TUI adoption

- Replace the TUI's run and session construction with public `Agent`, `Run`,
  events, results, `ListModels`, `SteerChild`, `Sessions`, and
  `InspectSession`.
- Keep Bubble Tea rendering and the internal JSONL reader for historical and
  child-focus transcripts; add no public TUI package and no `History` DTO.
- Add the public-surface transcript renderer test and the fixed-script
  `-ui-json` frame/snapshot goldens.
- Delete the duplicated runner wiring that the facade replaced; the TUI may
  import `internal/session` for replay only.
- **Exit:** the TUI uses the public facade for run construction; all TUI and
  `-ui-json` tests pass; the library does not import `internal/tui` or `cmd`.

### M8 — Release hardening

- Finish docs (architecture, limits, testing strategy, README, package docs).
- Run the full definition-of-done set and the manual smokes.
- Tag `v0.1.0` (or the chosen pre-1.0 version), publish the module, and verify
  `go get` from a clean module cache.
- **Exit:** all §1.4 criteria pass; no new dependencies; release notes list the
  public surface and the session-field additions.

Rough effort: M0 1 day; M1 3–5; M2 5–7; M3 3–4; M4 5–7; M5 2–3; M6 5–7; M7
3–5; M8 3–5. Calendar time depends on review and real-provider access.

## 9. Alternatives considered

1. **Move `internal/*` to public packages.** Rejected. It would expose 200+
   identifiers, tie users to internal churn, violate the five-package layout,
   and make the semver surface unmanageable. The facade costs a conversion layer
   and keeps the internals free.
2. **Subprocess JSON-RPC over the existing `aharness` binary.** Rejected. It
   loses in-process tools, model injection, and policy hooks; adds process
   lifecycle and protocol compatibility; and repeats the Claude SDK's release
   cadence problem. The loop is already in-process.
3. **A new public `pkg/` tree.** Rejected by AGENTS.md §"Non-negotiables" and
   the coding standard. A single root package is the smallest surface that
   works.
4. **Alias internal DTOs instead of copying.** Rejected for interfaces and
   events, which users implement or type-switch. Aliases would make internal
   field changes breaking public changes and leak `internal/...` into Go doc
   signatures. The conversion cost is bounded and testable.
5. **Callback-only API (`Config.OnEvent`).** Rejected as the only surface. It
   has no backpressure story, makes cancellation and "stop consuming" awkward,
   and repeats the callback regrets in LangChainGo and the Claude ports. It
   remains a *possible* future convenience over the iterator.
6. **Full framework scope in v1 (MCP, hooks, RAG, multi-provider matrix).**
   Rejected. aharness's value is a small, reliable loop with a durable log. A
   larger surface would repeat Eino's deprecation churn and obscure the prompt
   and tool contract.
7. **In-memory session store.** Deferred. It would help tests and servers, but
   it requires a store seam through `session.Store`, `Run`, `Inspect`, and fork.
   The on-disk store is the shipped contract and is easy to point at a temp
   directory.
8. **Public transcript/History DTO in v1.** Deferred. The TUI renders live from
   events and reads its own log for replay; the second customer's durable
   artifact is a report file, not a chat transcript. Adding a public message
   model now would freeze a format without an external replay consumer.
9. **Generic ADK scope (a second non-coding domain).** Rejected. Serving a
   non-coding domain from this contract would force a provider matrix,
   structured-output schemas, a store seam, background runs, and eventually
   graph orchestration — the features that make an ADK and that Google and Eino
   already do better. A second domain is a sibling project (§1.5).
10. **Parallel tool calls within one model response.** Rejected in v1. It would
    break total log order, deterministic replay, sequential `Authorize`,
    canonical-argument stuck detection, and the two-`edit_file` safety
    guarantee, for a latency gain bounded by tool time over wall clock. Batch
    tools and delegated children cover the real cases; the reopen gate is
    log-derived (§4.3).
11. **Nested delegation.** Rejected in v1. It requires a child prompt with
    delegation guidance, depth in the session log, a tree-wide budget, tree-aware
    policy, depth-aware child events, and tree child control — every one a
    recorded-contract change. Context shielding inside a child is a tool
    concern; staged parent-driven fan-out covers the workflow.
12. **Git worktrees for concurrent children.** Rejected. Worktrees are a coding
    harness's answer to concurrent writers; aharness deliberately keeps
    delegation read-only or tool-declared, returns reports, and gives the parent
    the only writer role. Worktrees would introduce merge semantics, disk
    lifecycle, and workspace-fork rules that the log cannot explain.

## 10. Open questions

These need decisions before or during the named milestone; none blocks the
design.

1. **Module path and ownership.** Final repository URL and whether a vanity
   domain is desired. Recommendation: use the repository path; no vanity.
2. **Pre-1.0 version.** Start at `v0.1.0` or go directly to `v1.0.0` after the
   facade stabilizes. Recommendation: `v0.x` until M6 is shipped.
3. **`Authorize` shape.** A single `func(ctx, ToolRequest) error` is the
   proposal. A richer ask/allow/deny decision or an asynchronous approval event
   is a later change; do not add it speculatively.
4. **`ConcurrencySafe` trust model.** v1 trusts the host declaration and cannot
   verify it. A future effect annotation or a read-only proof is deferred until a
   real misdeclaration appears; safety is never inferred from a tool name.
5. **`run_command` environment injection.** Hosts may need build secrets or a
   restricted env. v1 inherits the existing allowlist; a `ToolsConfig.Env` or
   command-policy surface is a separate security review.
6. **Public `History`/transcript helper.** Needed only if an external host must
   replay message-level history. The TUI reads its own log and the report
   artifact is a file, so neither forces it in v1.
7. **In-memory store.** Wait for a second store implementation or a concrete
   server requirement.
8. **Event timestamps and IDs.** Events carry observation time and session ID;
   they do not carry a durable sequence. If hosts need correlation with the
   log, add a `Seq`/`RunID` field, which is backward compatible.
9. **Provider error classification.** `ProviderError` covers the common HTTP
   case; whether to expose protocol/transport typed errors is deferred until a
   host needs to branch on them.
10. **TUI adoption gaps.** M7 may surface a need for child transcript access or
    more session-shell detail. Each gap is evaluated against §1.5; none should
    become a public TUI abstraction or a public `History` DTO by accident.
11. **Batching hint.** A prompt line encouraging independent reads in one
    response would reduce model round trips without changing tool execution.
    It requires a new prompt version and a replay decision, so defer it until
    logs show multi-call turns are common; the recorded tool-result duration
    already measures the alternative.

## 11. Documentation deliverables

- `README.md`: a "Use as a Go library" section after M5, with the import path,
  a minimal example, custom tool/model examples, the sandbox warning, and the
  coding-agent boundary. The root README indexes shipped behavior only, so this
  lands with the implementation, not with this plan.
- `docs/architecture.md`: add the root package and `internal/engine` to §3.1;
  define the public model/tool/event contracts and the custom-tool projection in
  §3.2; document sequential tool execution, the child fan-out rule, and
  `contract_override` in §7; add the library construction path to §9.
- `docs/limits.md`: add the custom-tool count/size caps and the public default
  `MaxSubagents` if it is normalized.
- `docs/testing-strategy.md`: add the external-module build check, the public
  API tests, the research acceptance test, the transcript renderer test, and the
  custom-tool/session compatibility fixtures.
- `docs/definition-of-done.md`: add the boundary review question and the
  recorded-contract test to the project-wide checklist.
- `docs/README.md`: index this plan and the research document.
- Package doc (`doc.go`) with a short overview, the lifecycle, the sandbox
  warning, a link to the architecture, the prompt invariants a caller must keep
  when replacing `SystemPrompt` (tools are the only way to act; one result per
  call; stop by replying without a tool call; errors are data), and the two
  supported parallel patterns (batch tool, delegated children).

## 12. Implementation record and deviations

**Implemented:** publishable module path `github.com/eddiectc/aharness`
(the repository path named in the coding standard's local-module guidance),
`internal/engine`, public facade, custom models/tools, policy hook,
custom-tool recording/projection, fork contract overrides, grant presence,
delegation bridge, session inspection and listing, batch CLI migration to the
engine, external-module build fixture, runnable example, and the reference
documentation updates.

**Deviations from the design:**

- **M7 TUI adoption is deferred.** The TUI still constructs its runs through
  `internal/agent` and uses its internal JSONL reader for historical and
  child-focus transcripts. Migrating the Bubble Tea host, session shell, model
  picker, child-focus replay, and `-ui-json` goldens is a separate change; no
  public TUI package or `History` DTO was added. The facade already covers the
  run/event/result/session surface M7 needs.
- **The session `tools` field uses `omitzero`, not “no tag”.** `encoding/json/v2`
  encodes a nil slice as `[]`, so a plain `json:"tools"` could not preserve the
  nil-versus-empty distinction. `omitzero` omits a nil grant (all tools) and
  writes `[]` for an explicit empty grant (none); `null` still decodes as nil.
- **The engine registry factory receives the workspace.** The design sketch's
  factory signature had no workspace handle; the implementation passes
  `*tools.Workspace` so a per-run factory can build the same registry without
  reopening the root. This is internal-only.
- **The engine adds an execution-only grant.** Batch mode withholds `delegate`
  without rewriting a resumed session's recorded contract; the facade never
  sets it.
- **`RunSpec` carries per-run delegation resolvers.** The TUI/facade may switch
  the parent model for one run, so the spawner's resolver and child completer
  are per-run rather than fixed at `Open`.
- **The README's library section and `doc.go` are the user-facing entry point;**
  the plan's package-level doc sketch is folded there. The planned separate
  `catalog.go` is folded into `model.go` and `version.go`; the public surface is
  unchanged.

**Verification performed:**

- `go test -race -count=1 ./...` (public, engine, internal, CLI, TUI).
- External-package lifecycle tests and `testdata/external` module build.
- `go vet ./...`, `gofmt`, `go build ./...`, `make docs-check`.
- Fake-provider tests only; no real-provider library smoke because
  `AHARNESS_PROVIDER_TOKEN` is unset.

**Pending before a stable release:** M7 TUI adoption, a real-provider library
smoke, and publishing/tagging the module.


## 13. Review checklist

Before marking the plan implemented:

- [x] The public package imports only internal packages and the standard
      library; `go list -deps` shows no TUI or `cmd` dependency.
- [x] The CLI and TUI packages do not import the public package in a cycle.
- [x] Every exported identifier has a doc comment and a stable semantic.
- [x] `Config` has no field that is silently accepted but ignored.
- [x] Every proposed extension passes §1.5: no in-run parallel tools, nested
      delegation, structured output, store seam, provider matrix, graph
      orchestration, or public TUI package.
- [x] Resume rejects prompt/tool changes; Fork validates overrides before
      creating the new log, records `contract_override`, and copied history
      replays under the new contract.
- [x] Tool calls in one run execute and emit in arrival order; delegated fan-out
      uses only the built-in read-only set plus `ConcurrencySafe` custom tools;
      `Authorize` is never entered concurrently within one session.
- [x] `concurrency_safe` is recorded, reproduced on resume, and absent means
      false; a changed declaration is `ErrToolMismatch`.
- [x] Public events and `Result` alone can render the live transcript (text,
      tool blocks, todo, subagent roster, usage/cost, terminal status); the
      renderer test passes without log access.
- [ ] **Deferred (M7):** the TUI still uses its internal run construction and
      log reader. The library itself never imports `internal/tui` or `cmd`; see
      §12.
- [x] A denied tool produces a model-visible result and a durable
      `tool_result`; it does not corrupt the call/result pairing.
- [x] A custom tool schema recorded in `session_start` is semantically equal
      (canonical JSON) on resume and its concurrency declaration matches;
      projection and the model request carry it.
- [x] Breaking the event iterator cancels and waits; `go test -race` reports no
      leaked goroutines.
- [x] `Result.Status`, `Result.Err`, and the durable `final` agree; the
      iterator's separate error appears only when no `Result` exists.
- [x] Session v2 fixtures and CLI-created sessions still resume.
- [x] README and architecture describe the shipped behavior, not the plan.
- [x] `make dod` passes. The manual real-provider smoke is **pending**
      `AHARNESS_PROVIDER_TOKEN`; see §12.
