# Embeddable Go agent library research

- **Date:** 2026-11-16
- **Status:** research complete; no code changed. Input to the
  [Go library build plan](../plans/go-library-build.md).
- **Superseded in part:** the plan's 2026-11-16 revision refines three
  recommendations here — `Fork` may change the recorded prompt and tool set and
  records `contract_override`; custom tools may declare `ConcurrencySafe` for
  child fan-out; and tool calls within one run are explicitly sequential, with a
  log-derived gate for revisiting. The comparisons and sources below are
  unchanged.
- **Snapshot:** repository `cecceeb`; module `aharness`; session schema v2;
  `toolset-v2`; `prompt-v2`; Go 1.27.3.
- **Scope:** how comparable Go libraries and coding-harness SDKs shape their
  embedding API, and which patterns aharness should copy or reject when it
  becomes an importable Go library.
- **Related:** [architecture](../architecture.md), [coding standard](../coding-standard.md),
  [bare-minimum harness research](agent-harness-minimum.md),
  [subagent research](subagent-research.md), and the
  [Go library build plan](../plans/go-library-build.md).

## 1. Summary

Seven conclusions drive the design.

1. **In-process embedding is a different product from a subprocess CLI.** The
   coding-harness SDKs that expose the most control (OpenAI Agents, Cursor
   local, aharness's own loop) run in the caller's process and can accept
   in-process tools, hooks, and model implementations. The ones built on a
   shipped CLI (Claude Agent SDK, Codex `exec`, opencode server, goose ACP)
   import the CLI's release cadence: community Go ports of Claude's SDK spend
   their changelog catching up with settings and message subtypes. aharness
   already has an in-process loop; the library should expose that, not wrap the
   `aharness` binary.
2. **A config struct plus explicit constructors is the dominant Go shape.**
   Eino, Google ADK Go, docker-agent, and the OpenAI Agents Go port all build
   with a config or builder and keep per-run knobs separate. Functional options
   remain useful for small per-run overrides, not for the whole configuration.
3. **Streaming has not converged, but `iter.Seq2` is the 2026 idiom.** ADK Go's
   `Runner.Run` returns `iter.Seq2[*session.Event, error]`, Eino uses an
   `AsyncIterator` with `Next() (T, bool)`, LangChainGo and gptscript use
   callbacks, and the OpenAI Agents Go port ships callbacks, channels, *and*
   iterators in one release. For a new Go harness, `iter.Seq2` composes with
   `for range`, carries the terminal error in-band, and makes "stop consuming"
   the cancellation signal.
4. **Tools want a typed-function fast path and a raw-schema escape hatch.**
   Eino (`InferTool[T,D]`), ADK Go (`functiontool.New`), and the OpenAI Agents
   Go port (`NewFunctionTool[T,R]`) all infer JSON Schema from Go types, and all
   keep a manual schema path for tools that cannot be expressed that way. A
   coding harness also needs the reverse: built-in tools with fixed schemas that
   the host can disable.
5. **Sessions are a first-class verb set, not a stringly-typed option.**
   Claude exposes continue, resume, and fork; Cursor separates a durable
   `Agent` from a single `Run`; Codex has `thread/resume` and `thread/fork` and
   refuses to resume across a provider change. aharness's v2 JSONL log already
   supports resume and fork, so the library should expose `New`, `Resume`, and
   `Fork` and put the session ID on the result.
6. **Approval is a policy layer, not a boolean.** The mature SDKs separate
   *availability* (which tools exist) from *permission* (which calls are
   allowed), and several make approval a resumable event rather than a callback.
   The most-criticized bug is silent pre-emption of a permission callback by an
   allowlist. Aharness has no approval workflow and no sandbox; the library
   should expose one explicit host policy hook, document that it is not a
   security boundary, and leave sandboxing to the host.
7. **The public surface must stay small and independent of internals.** The Go
   libraries that age well expose a narrow core and a raw escape hatch; those
   that expose every internal type inherit their internal churn. Aharness has
   roughly 66k lines under `internal/` with 200+ exported identifiers. The
   library should be one root package that owns public DTOs and converts at the
   boundary, not a rename of `internal/`.

## 2. Method

- Source review of `internal/agent`, `internal/model`, `internal/tools`,
  `internal/session`, `internal/tui`, and `cmd/aharness` at the snapshot above.
- Primary-source review of Go agent libraries on pkg.go.dev and GitHub, and of
  coding-harness embedding documentation, on 2026-11-16. Package pages were
  fetched directly for Eino ADK, Google ADK Go v2, and the OpenAI Agents Go
  port; other projects are cited from their repositories and documentation.
- Judgment rule: prefer APIs demonstrated by at least two independent projects,
  unless aharness's existing contracts make the choice materially different.
- Uncertainty: vendor SDK docs and CLIs change frequently. Names in the
  subprocess SDKs are reported as observed on the access date. The official
  Anthropic Go SDK and an official OpenAI Go Agents SDK were not found; the Go
  ports reviewed are community projects. "Crush" and "Cursor SDK" have no
  stable published embedding contract that could be verified; they are treated
  as evidence of a product shape, not as API references.

## 3. Go agent libraries

### 3.1 Comparison

| Project | Import / package | Construction | Tool definition | Model injection | Streaming / events | Sessions |
|---|---|---|---|---|---|---|
| CloudWeGo Eino | `github.com/cloudwego/eino`; `adk`, `compose`, `components/*`, `schema` | `adk.NewChatModelAgent(ctx, *Config)`; `adk.NewRunner(Config)` | `utils.InferTool[T,D]`, `utils.NewTool(*schema.ToolInfo, fn)` | `model.BaseModel[M]` in config; retry/failover config | `*adk.AsyncIterator[*AgentEvent]` (`Next`); callbacks; newer push `TurnLoop` | session values; checkpoint store; `Resume`/`ResumeWithParams`; interrupt/resume |
| Google ADK Go | `google.golang.org/adk/v2`; `agent/llmagent`, `runner`, `session`, `tool/*` | `llmagent.New(llmagent.Config{...})`; `runner.New(Config)` | `functiontool.New(Config{...}, fn)` with signature inference; MCP toolset | `model.LLM`; `GenerateContentConfig` | `Runner.Run(...) iter.Seq2[*session.Event, error]`; `RunLive` | `session.Service`; state deltas; compaction; confirmation |
| LangChainGo | `github.com/tmc/langchaingo/agents` | `agents.NewExecutor(agent, opts...)`; agent constructors | implement `tools.Tool` (`Description`, `Call`) | any `llms.Model` | 16-method `callbacks.Handler` | `schema.Memory` |
| OpenAI Agents Go | `github.com/nlpodyssey/openai-agents-go/agents` | `agents.New(name).WithInstructions(...).WithTools(...)` | `NewFunctionTool[T,R]`; `FunctionTool` raw schema | `Agent.Model` / `ModelProvider` | `RunStreamed` callback, `RunStreamedChan`, `RunStreamedSeq`; `RunResult` | `RunConfig.Session`; `previous_response_id`; `RunState` |
| Claude Agent SDK Go ports | community: `github.com/M1n9X/claude-agent-sdk-go`, `roasbeef`, `schlunsen`, `tylergannon` | `claude.Query(ctx, prompt, options)`; `Client` | in-process "SDK MCP server"; `SimpleTool`/`QuickTool` | bundled `claude` CLI options | `<-chan types.Message`; hooks | `Resume`, `Continue`, `ForkSession`; JSONL transcripts |
| docker-agent | `github.com/docker/docker-agent` | `runtime.New(ctx, team.New(...))`; `agent.New(...)` options | YAML toolsets and MCP; Go hooks/permissions packages | provider clients in model config | runtime events; event merging | `session` package; SQLite store |
| gptscript | `github.com/gptscript-ai/gptscript` | `gptscript.New(ctx, opts...)`; `Run(ctx, prg, env, input, opts)` | DSL/MCP, not a Go tool registry | `llm.Registry`; provider options | `runner.Monitor` callback | `Chat` with prior state |
| mcp-go | `github.com/mark3labs/mcp-go` | `server.NewMCPServer(name, version, opts...)` | `mcp.NewTool` builder + `AddTool` | n/a (protocol library) | hooks/middleware | task-augmented tools |

### 3.2 What each contributes

**Eino.** The most complete Go agent framework reviewed. Its construction is a
config struct (`ChatModelAgentConfig`) plus a runner, with per-run
`AgentRunOption`s. Two features stand out for aharness: a checkpoint store with
explicit `Resume`/`ResumeWithParams`, and safe-point cancellation
(`CancelAfterChatModel`, `CancelAfterToolCalls`, `CancelImmediate`) with a
`CancelHandle.Wait` and typed cancel errors. That is the right *shape* for a
long-running loop, but it is also over-built for a harness whose durable log is
already the checkpoint. Eino's own docs mark large parts of the multi-agent
surface "NOT RECOMMENDED" and deprecate an earlier middleware interface; the
lesson is to avoid shipping speculative layers in v1.

**Google ADK Go.** The cleanest idiomatic-Go surface: config structs,
`iter.Seq2` for a run, a session service, and tools inferred from Go function
signatures. Its weak point, acknowledged by TODOs in the source, is that
Gemini/genai types leak into the public API (`*genai.Content`, `genai.Schema`).
The design lesson is to keep the public DTOs provider-neutral and free of
internal types, even at the cost of a conversion layer.

**LangChainGo.** The lowest-friction on-ramp and the closest to a Python
transliteration. Inputs are `map[string]string`, memory is an interface, and
events are a 16-method callback handler. It demonstrates both the value and the
cost of callbacks: easy to start, awkward to backpressure, cancel, or adapt to
a typed event stream. The in-repo deprecation of `agents.Initialize` is the
only hard churn signal available; claims that the project is unmaintained were
not verified from a primary source.

**OpenAI Agents Go (nlpodyssey).** The clearest evidence that streaming API
shape is unsettled: one release provides a callback stream, a channel stream,
and a single-use iterator (`RunStreamedSeq` returning `EventSeqResult{Seq,
Err}`). It also bundles obvious good ideas: typed `NewFunctionTool`, a
`ModelProvider` seam, `MaxTurnsExceededError`, `RunState` for approval pauses,
and `Agent.AsTool` as a distinct concept from a handoff. Its builder style
(`New(...).With...`) is pleasant for small agents but scales poorly to
aharness's larger configuration surface.

**Claude Agent SDK and its Go ports.** The most complete approval and hook
model: permission modes, `canUseTool`, ten hook events, and resume/continue/
fork. It is also the strongest warning against a subprocess transport: the SDK
is a wrapper over a CLI and its settings, so every upstream release can add a
message subtype or setting that the port must chase. The in-process tool path
is implemented as an "SDK MCP server", which is a clever bridge but still a
protocol inside a subprocess. Aharness should keep its tools in-process.

**docker-agent.** Shows the value of separating the runtime from the team/agent
definition and of publishing a YAML-first configuration for humans while
keeping Go construction possible for embedders. Its `pkg/hooks` and
`pkg/permissions` packages are the right decomposition: hooks observe, policy
decides, the runtime executes. The Go embedding example is a useful sanity
check that a durable runtime can be constructed without a CLI.

**gptscript and mcp-go.** Neither is an agent loop in aharness's sense, but both
are useful evidence. gptscript's `Run` returns a string and uses a callback
`Monitor` for events; that is the minimum viable embedding API and it is too
small for a coding harness that needs tool results, usage, and stop reasons.
mcp-go shows how far functional options can go before a config struct is
clearer; it also confirms that tool protocols should be a separate layer from
the loop.

**Not embeddable libraries.** `kagent-dev/kagent` is a Kubernetes controller
that reconciles agent CRDs; its Go surface is not a library API. No credible Go
port of LangGraph was found; Eino's compose layer fills that niche. The
`openai/` org has no Go Agents SDK; `anthropics/claude-agent-sdk-go` did not
resolve as an official repository.

## 4. Coding-harness embedding surfaces

### 4.1 What the products expose

| Product | Embedding API | Transport | Approval / policy | Sessions |
|---|---|---|---|---|
| Claude Agent SDK | `query(prompt, options)`; `ClaudeSDKClient` for bidirectional use; custom tools as SDK MCP servers | Subprocess: bundled CLI, NDJSON over stdio | Permission modes, `canUseTool`, hooks; ordering is hooks → deny → ask → mode → allow → callback | `continue`, `resume(id)`, `fork_session`; JSONL transcripts |
| OpenAI Agents SDK | `Agent` + `Runner.run`/`run_streamed`; sessions, guardrails, handoffs | In-process library over Responses API | Guardrails and tool approval items; MCP approval requests | `session`, `conversation_id`, `previous_response_id`, `RunState` |
| Codex | `codex exec --json`; `@openai/codex-sdk` (`startThread`, `resumeThread`, `thread.run`); app-server JSON-RPC | CLI or app-server (stdio/ws/unix) | `item/permissions/requestApproval`, sandbox policy per turn | `thread/resume`, `thread/fork`, provider-mismatch guard |
| opencode | `opencode serve` OpenAPI; JS/Go SDKs | HTTP + SSE | `POST /session/{id}/permissions/{permissionID}` | server-side sessions |
| goose | `goose run --output-format stream-json`; `goose acp`; `goose serve` | CLI or ACP (stdio/HTTP/WS) | ACP permission requests | ACP sessions |
| crush | `crush serve` + workspaces; SSE (no published stable SDK) | HTTP/SSE | `crushrc` allow/deny, `--yolo` | workspaces |
| Cursor | `@cursor/sdk` local/cloud `Agent`; `run.stream()`/`run.wait()`; CLI `agent -p` | In-process local or HTTP cloud | Headless local runs tools without approval | `Agent.resume` |
| Aider | Python `Coder.create(...).run()`; `--message` | In-process import or subprocess | `--yes`/`--dry-run` | file-based chat history |

### 4.2 Lessons

1. **Expose the run, stream, cancel, and resume verbs; keep rendering and
   pickers out.** The strongest SDKs expose a small set of operations and keep
   TUI-driving endpoints (opencode's prompt-injection endpoints are the
   anti-pattern) out of the embedding contract.
2. **A result object must carry more than text.** Claude's `ResultMessage` has
   a subtype, terminal reason, usage, cost, session ID, and structured output;
   Cursor's `RunResult` has status, result, usage, and git state. aharness's
   `agent.Result` already has status, final text, turns, usage, context
   occupancy, cost, and subagent totals. The library should expose all of it.
3. **Do not double-signal failure.** Claude yields an error result *and* raises
   `ResultError`, forcing callers to catch the error before resuming. Aharness
   should make the terminal status authoritative, put the diagnostic cause on
   the result (and return it from the blocking call), and reserve a separate
   stream error for "no result exists".
4. **Two event layers, one stable envelope.** Products commonly ship coarse
   messages plus optional raw deltas, and warn that delta fields are less stable
   than the envelope. Aharness's `DisplayEvent` is already display-only and its
   session log is the durable truth; the library should expose one typed event
   stream, document that the log is authoritative, and version the event
   envelope.
5. **Do not make the caller's allowlist silently override policy.** Claude's
   shadowed-callback warning is the documented regret. Availability (which
   tools are registered), permission (which calls may run), and sandboxing (what
   the process can reach) are three layers. Aharness has availability and path
   containment; the library adds permission; sandboxing stays with the host.
6. **Session resume is local and imperfect; say so.** Claude's sessions are
   filesystem-local, forking branches history but not files, and Codex refuses
   to resume across a provider change. Aharness's v2 log is similarly local and
   provider-agnostic; the library must document that `Fork` does not fork the
   workspace and that changing the model/provider is recorded per run.
7. **Wrapping a CLI imports its release cadence.** This is the single most
   repeated regret in the Go ports. Aharness's loop, tools, and log are already
   in-process; the library should not take a subprocess dependency for
   convenience.
8. **Keep a raw escape hatch.** The best SDKs pair typed options with a raw
   settings/args path. For aharness, the equivalent is a custom `Model` and
   custom `Tool`, not a grab bag of provider-specific config keys.

## 5. Patterns that recur

### 5.1 Construction

- Config struct + `New` is dominant for medium and large surfaces (Eino, ADK Go,
  docker-agent). Functional options are dominant for small surfaces and per-run
  knobs (mcp-go, gptscript, ADK Go's `RunOption`).
- Constructors validate early and return errors; they do not defer
  configuration failures to the first run.
- The object that owns a session is separate from the single-run call. Cursor's
  `Agent`/`Run` split and ADK Go's `Runner.Run` are the clearest examples.

### 5.2 Tools

- Typed-function registration plus schema inference is table stakes. The raw
  JSON-Schema path must remain for dynamic tools.
- Tool handlers return a result to the model; an error from the handler is not
  automatically fatal to the run. The OpenAI Agents Go port is explicit about
  this (`FailureErrorFunction`).
- Consumers need to disable built-ins and add custom ones without forking the
  registry. aharness's registry is currently a closed map of seven tools, which
  is the largest library gap.

### 5.3 Models

- Every mature library has a model interface with a canonical request/response,
  and at least one built-in HTTP adapter. Provider-specific SDK types are
  isolated behind the adapter (with ADK Go's genai leak as the cautionary
  example).
- A model interface that only supports buffered completion is insufficient for
  a coding harness; streaming is a separate optional interface (aharness's
  `StreamCompleter` is already this shape).
- Factories matter for subagents: a shared completer is not necessarily safe
  for concurrent children, so the library needs a per-session model factory,
  not just a model instance.

### 5.4 Events and errors

- `iter.Seq2` is the current Go idiom for a pull-based stream, but it needs a
  lifecycle story: who runs the loop, what happens when the consumer breaks,
  and how the terminal result is delivered. ADK Go and the OpenAI Agents Go
  iterator both park the terminal error in-band or on a result object.
- Stop reasons should be a typed taxonomy (`completed`, `max_turns`,
  `token_limit`, `time_limit`, `provider_error`, `protocol_error`, `stuck`,
  `interrupted`, `internal_error`), not a boolean. aharness already has this.
- Usage and cost should be on the result, not inferred from events.

### 5.5 Sessions

- The durable log is the source of truth; the context window is a projection.
  Aharness already states this and implements it.
- Resume inherits recorded model, provider, limits, and prompt; explicit
  overrides win. Budgets are per-run and are not inherited.
- Fork copies history into a new self-contained log. It does not copy the
  workspace.
- Session listing and first-message summaries are a UI convenience that belongs
  in the library if it wants hosts to build resume pickers without reading
  JSONL by hand.

### 5.6 Approvals and sandboxing

- Three implementations recur: in-process callback, protocol request/response,
  and declarative modes decided before the run.
- The ordering of deny rules, ask rules, allow rules, and callbacks is
  load-bearing; an allowlist that silently pre-empts a callback is a design
  bug.
- A policy callback is not a sandbox. Claude's own docs say path rules are not
  OS enforcement; Codex ships a separate sandbox policy. Aharness's README
  already says it does not sandbox itself; the library must repeat that.

## 6. What aharness already has

The existing code is a strong foundation for a library:

| Capability | Where it lives | Library readiness |
|---|---|---|
| Bounded model/tool loop | `internal/agent/runner.go` | `agent.New` takes a completer, registry, and store; ready to wrap |
| Canonical model DTOs and two OpenAI adapters | `internal/model` | Clean interfaces; needs public copies and constructors |
| Typed stop reasons and result accounting | `internal/agent/types.go` | Ready to expose; `ExitCode` stays CLI-only |
| Append-only v2 JSONL with repair/locking/fork | `internal/session` | Ready; add custom-tool metadata |
| Projection and compaction | `internal/agent/context.go` | Pure function; needs custom schemas appended |
| Display events | `internal/agent/display.go` | Sealed and complete; needs public mapping |
| Workspace containment and seven tools | `internal/tools` | Strong; registry must accept extra tools |
| Catalog discovery | `internal/model/catalog*.go` | Clean `CatalogSource`; needs public config |
| Subagents | `internal/agent/spawn.go` | Powerful but CLI-wired; library needs explicit definitions/resolver |
| TUI | `internal/tui`, `cmd` | Out of scope for the library |

## 7. What aharness lacks for embedding

1. **No public package.** All implementation is under `internal/`; external
   modules cannot import it.
2. **No publishable module path.** `module aharness` has no dot in its first
   path element and cannot be fetched as a remote module.
3. **No custom tools.** The registry is a closed map; `toolFunc` is unexported.
4. **No custom model injection.** `model.Completer` is clean, but its request
   and response types are internal; there is no public constructor for a
   caller-owned model.
5. **No credential injection.** The provider token comes only from
   `AHARNESS_PROVIDER_TOKEN`.
6. **CLI-local configuration and resolution.** The new/resume/fork inheritance
   rules live in `cmd/aharness`, not in a reusable package.
7. **No host policy hook.** Tool execution is unconditional after validation.
8. **No library-facing session API.** `session.Store` and `agent.Inspect` are
   exported inside the module but not usable outside it.
9. **No public event/result DTOs.** `agent.DisplayEvent` and `agent.Result` are
   internal types.
10. **No documented concurrency contract for embedding.** The display callback
    must not block; the store is single-goroutine; one run at a time. A library
    needs explicit methods and documentation for all three.

## 8. Recommendations

These feed the [build plan](../plans/go-library-build.md) directly.

1. **One public root package, facade over internals.** Keep `internal/agent`,
   `internal/model`, `internal/tools`, and `internal/session` as implementation
   details. The facade owns public DTOs and conversion. Do not move 66k lines
   of internals into the public API.
2. **Module path is a prerequisite.** Choose the final repository path (for
   example `github.com/<org>/aharness`) before publishing; update the 191
   internal import references mechanically. No `/v1` suffix; add `/v2` only for
   a breaking public API.
3. **`Config` struct + `New`/`Resume`/`Fork`.** Required construction data is
   explicit; optional inherited data is nil. Per-run overrides are a
   `RunOptions` struct.
4. **Run blocking and streaming.** `Agent.Run` returns `(Result, error)`.
   `Agent.Start` returns a `*Run` whose `Events()` is an `iter.Seq2[Event,
   error]`; `Interrupt`, `Cancel`, and `Steer` are explicit methods. Breaking
   the iterator cancels and waits.
5. **Copy public DTOs; do not alias internals.** The conversion layer is
   mechanical and keeps the semver surface independent.
6. **Custom tools and models are first-class.** `Model` is a public interface;
   custom tools are registered into the existing registry and recorded in the
   session log. Built-in tools remain available and can be disabled.
7. **One policy hook, clearly not a sandbox.** `Config.Authorize` is called
   before every tool call; a denial becomes a model-visible result. Sandboxing
   remains the host's job.
8. **Record what the model saw.** Custom tool schemas must be part of
   `session_start`, and projection must use them; otherwise resume replays a
   different tool set. Add this as an additive v2 field, not a new session
   version.
9. **Delegation is opt-in.** The library takes explicit agent definitions and a
   model resolver; it does not read `<state>/agents` or invent a model for a
   child.
10. **Keep the CLI and TUI as separate consumers.** Extract the reusable
    new/resume/fork wiring into an internal engine, dogfood it from batch mode,
    and leave TUI policy in `cmd`.

## 9. Sources

Repository (snapshot `cecceeb`):

- `internal/agent/runner.go`, `types.go`, `display.go`, `context.go`,
  `state.go`, `spawn.go`, `agents.go`, `events.go`
- `internal/model/types.go`, `catalog.go`, `catalog_source.go`,
  `openai_chat.go`, `openai_responses.go`, `http.go`
- `internal/tools/registry.go`, `result.go`, `schemas.go`, `root.go`,
  `delegate.go`
- `internal/session/event.go`, `store.go`, `reader.go`
- `cmd/aharness/run.go`, `config.go`, `models.go`, `sessions.go`, `tui.go`
- `docs/architecture.md` §3, §9; `docs/limits.md`; `docs/configuration.md`

Go libraries (accessed 2026-11-16):

- CloudWeGo Eino, [`adk` package documentation](https://pkg.go.dev/github.com/cloudwego/eino/adk)
  and [`eino` module](https://pkg.go.dev/github.com/cloudwego/eino):
  `NewChatModelAgent`, `ChatModelAgentConfig`, `AsyncIterator`, checkpoint
  resume, cancel modes, middleware deprecation.
- Google ADK Go, [`runner` package documentation](https://pkg.go.dev/google.golang.org/adk/v2/runner)
  and [`adk` module](https://pkg.go.dev/google.golang.org/adk/v2): `Runner.Run`
  returning `iter.Seq2[*session.Event, error]`, `session.Service`, compaction.
- LangChainGo, [`agents` package](https://pkg.go.dev/github.com/tmc/langchaingo/agents)
  and [`callbacks`](https://pkg.go.dev/github.com/tmc/langchaingo/callbacks):
  `Executor`, `tools.Tool`, callback handler.
- OpenAI Agents Go, [`agents` package](https://pkg.go.dev/github.com/nlpodyssey/openai-agents-go/agents):
  `Agent`, `NewFunctionTool`, `Model`/`ModelProvider`, `RunStreamed*`,
  `RunStreamedSeq`, `MaxTurnsExceededError`.
- Claude Agent SDK Go ports:
  [`M1n9X/claude-agent-sdk-go`](https://github.com/M1n9X/claude-agent-sdk-go)
  and the ports linked from it. No official
  `github.com/anthropics/claude-agent-sdk-go` repository was found.
- Docker agent (formerly cagent): [`docker/docker-agent`](https://github.com/docker/docker-agent),
  including its Go library example and `pkg/hooks`, `pkg/permissions`,
  `pkg/session` packages.
- GPTScript: [`gptscript-ai/gptscript`](https://github.com/gptscript-ai/gptscript),
  `gptscript.New`, `Run`, and `runner.Monitor`.
- MCP Go: [`mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go),
  `server.NewMCPServer`, `mcp.NewTool`, `AddTool`.
- `kagent-dev/kagent` (Kubernetes controller, not an embeddable library) and
  `Ingenimax/agent-sdk-go` (Go framework, not reviewed in depth).

Coding-harness SDKs and docs (accessed 2026-11-16):

- Anthropic, [Agent SDK overview](https://docs.claude.com/en/api/agent-sdk/overview)
  and [Subagents in the SDK](https://code.claude.com/docs/en/agent-sdk/subagents):
  `query`, `ClaudeSDKClient`, permission modes, `canUseTool`, hooks, resume,
  fork, and the warning that path rules are not OS enforcement.
- OpenAI, [Agents SDK](https://openai.github.io/openai-agents-python/): agents,
  runners, sessions, guardrails, handoffs, `RunState`.
- OpenAI Codex: [`openai/codex`](https://github.com/openai/codex) (`codex exec`,
  app-server JSON-RPC, approvals, `thread/resume`, `thread/fork`) and the
  published Codex SDK.
- OpenCode: [`sst/opencode`](https://github.com/sst/opencode) and
  `opencode-sdk-go`: `opencode serve`, OpenAPI, SSE events, permission
  endpoints.
- Goose: [`block/goose`](https://github.com/block/goose): `goose run`,
  `goose acp`, `goose serve`.
- Crush: [`charmbracelet/crush`](https://github.com/charmbracelet/crush):
  `crush serve` and workspace endpoints; no stable published SDK was found.
- Cursor: Cursor Agent SDK documentation (`Agent.create`, `run.stream`,
  `run.wait`, local/cloud runtimes); headless local runs do not prompt for
  approval. The exact SDK surface is product-dated and was not independently
  verified beyond vendor docs.
- Aider: [Scripting](https://aider.chat/docs/scripting.html) and
  [Command line](https://aider.chat/docs/config/options.html): Python `Coder`
  is explicitly not a stable public API; `--message` is the supported path.

Go language references:

- [Range over functions](https://go.dev/blog/range-functions): `iter.Seq` and
  `iter.Seq2`.
- [Go modules reference](https://go.dev/ref/mod) and
  [Module version numbering](https://go.dev/doc/modules/version-numbers):
  module paths, v1, and `/vN` major-version suffixes.
- [Go 1.4 internal packages](https://go.dev/doc/go1.4#internalpackages): the
  import restriction that makes a facade necessary.
- [Go compatibility promise](https://go.dev/blog/compat): what "stable public
  API" means for Go packages.
