# aharness

aharness is a Go SDK for building a coding agent over a single workspace. It
runs a model/tool loop against the files and commands the caller grants, appends
every event to a durable JSONL session log, and ends a run with a final answer
or a typed stop reason. The loop is bounded by the model context window,
termination policies, and errors; turn and token budgets are opt-in and
unlimited by default.

The SDK is headless. It adds no CLI, TUI, environment, config-file, TTY, or
signal layer. [acode](https://github.com/eddiectc/acode) is the first-party
terminal coding agent built on these packages; embedders can use the root
facade directly.

## Two levels of API

- **Root facade:** `github.com/eddiectc/aharness` exposes `Config`, `Agent`,
  `New`/`Resume`/`Fork`, `Run`/`Start`, public events and results, model
  adapters, catalog discovery, tool configuration, delegation, and typed errors.
  This is the recommended embedding surface.
- **Component packages:** `agent`, `engine`, `model`, `tools`, and `session`
  expose the lower-level runner, composition layer, canonical model/adapter,
  tool registry, and durable store. They are public for first-party hosts and
  advanced integrations that need display events, history, session inspection,
  or direct adapter wiring.

## Use as a Go library

The root package `github.com/eddiectc/aharness` is the high-level, embeddable
form of the loop: durable sessions, builtin and caller tools, model adapters,
delegation, events, and typed results. It is headless: it adds no environment, config-file,
working-directory, TTY, or signal layer, and imports no CLI or TUI code. The
recorded contracts, including the public facade surface, are in
[architecture.md](docs/architecture.md#321-public-library-facade).

This module requires Go 1.27.4 or later. Add it to an existing module; it ships
no binary and needs no separate installation:

```sh
go get github.com/eddiectc/aharness@v0.4.0
```

Release history is in [CHANGELOG.md](CHANGELOG.md).

### Quick start

```go
package main

import (
    "context"
    "fmt"
    "os"
    "path/filepath"

    "github.com/eddiectc/aharness"
)

type myModel struct{}

func (myModel) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
    return aharness.Response{
        Text:         "done",
        FinishReason: aharness.FinishStop,
        Usage:        aharness.Usage{InputTokens: 10, OutputTokens: 2, Reported: true},
    }, nil
}

func main() {
    if err := run(); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}

func run() error {
    home, err := os.UserHomeDir()
    if err != nil {
        return err
    }
    ctx := context.Background()
    ag, err := aharness.New(ctx, aharness.Config{
        Workspace: "/repo",
        StateDir:  filepath.Join(home, ".aharness"),
        UserAgent: "my-product/1.0.0",
        Model: aharness.CustomModel(aharness.ModelSpec{
            ID:            "my-model",
            Provider:      "my-provider",
            Dialect:       aharness.DialectCustom,
            ContextWindow: 128_000,
        }, myModel{}),
    })
    if err != nil {
        return err
    }
    defer func() { _ = ag.Close() }()

    result, err := ag.Run(ctx, "Fix the failing test", aharness.RunOptions{})
    if err != nil {
        return err
    }
    fmt.Println(result.FinalText)
    return nil
}
```

`New` creates a session, `Resume` opens one for append, and `Fork` copies one
into a new self-contained log. Resume inherits the recorded prompt, tool
contract, model, limits, and thinking effort; changing the prompt or tools on
resume is a configuration error. Fork is the supported path for a contract
change. `InspectSession` reads a session without taking its exclusive lock.

`Run` is the blocking convenience and returns an authoritative `Result`.
`Status` is the typed stop reason (`completed`, `max_turns`, `token_limit`,
`time_limit`, `provider_error`, `protocol_error`, `stuck`, `interrupted`, or
`internal_error`); `Err` is its diagnostic cause and is also the error `Run`
returns, so `errors.Is` and `errors.As` work. `Status.Failure` and
`Status.Label` replace per-host status tables; `Result.Reason` and
`Result.Duration` match the terminal `run_end`. `RunOptions.OnEvent` observes
the same events without switching to `Start`, and `Result.Children` carries one
outcome per delegated task.

`Agent.Start` returns a `*Run` whose `Events()` iterator streams text and
reasoning deltas, tool start and completion, notices, recorded messages,
delegated-child progress, and the terminal `RunFinished`. `Interrupt` stops the
in-flight model or tool operation and lets the run continue; `Cancel` ends the
run; `Steer` and `SteerChild` enqueue input at turn boundaries and return a
run-unique ID acknowledged on `MessageRecorded` or `SubagentMessageRecorded`.
Events are display-only observations, and the session log is the durable
record. `ViewEvent` projects any event to a flat JSON shape with a `kind`
discriminator for an SSE endpoint or structured log. The display callback is
never invoked after the run returns, and `RunFinished` is terminal.

`Transcript(ctx, stateDir, sessionID)` reads the display transcript — run
headers, user messages, assistant markdown source, tool calls paired with
results, and notices — of any session: a root, a delegated child, or a fork.
`Agent.Transcript()` is the same read for the current agent's session. Both read
a byte snapshot of the log without taking the session lock, so a running session
stays readable and a torn final line is ignored; neither replaces the log as the
durable record.

`OpenAIModel` builds a Chat Completions or Responses adapter for an explicit
OpenAI-compatible endpoint. `DiscoverModel` and `ListModels` resolve models and
limits from the [models.dev](https://models.dev) catalog. `CustomModel` wraps
any implementation of the `Model` interface; implement the optional
`StreamingModel` interface to emit deltas. `Config.UserAgent`,
`OpenAIModelOptions.UserAgent`, and `CatalogConfig.UserAgent` are required when
the corresponding caller runs: the SDK sends the caller's identity on every
outbound request and never substitutes its own. A `CatalogConfig` is validated
only when `DiscoverModel` or `ListModels` is called. A direct `engine` host must
set `engine.Spec.UserAgent`; `engine.Open` rejects an empty identity, and a
`ResolveModel` closure must use the same identity because the engine cannot
inspect its adapter's headers.

`Config.MaxTurns` and `MaxTotalTokens` bound the agent's cumulative turn and
token usage, while `RunTimeout` and `ModelTimeout` bound one run and one model
attempt; a zero value is unlimited. `Config.Identity` is the product name used in
the parent and delegated-child identity lines: empty means `aharness`, it is
limited to 64 bytes with no control characters, and it is not recorded, so supply
it on every construction; a fork's new children use the identity supplied to
Fork. `Config.PromptTag` tags the host prompt beside the SDK `prompt-v3`
template version: optional, at most 64 printable ASCII bytes matching
`[A-Za-z0-9._:/-]+`, recorded as `session_start.prompt_tag` and exposed as
`State.PromptTag`; it never affects rendering, caps, or replay. A recorded
prompt over the 32 KiB prompt cap is flagged by `State.PromptCapped`, so a host
reads the truncation state instead of matching the SDK's marker. Resume keeps
the recorded tag, and a fork inherits it only when the prompt is unchanged.
Prompt composition has three tiers. Set only `Identity` and inherit
`agent.DefaultPromptBody` (recommended for a coding agent); compose a body from
`agent.LoopRules`, your own policy lines, and `agent.AgentCatalogText`; or replace
the prompt wholesale with `Config.SystemPrompt`. `Config.Instructions` composes
host text into the resolved prompt and records it once as
`session_start.appended_prompt`; every fresh child, including a child of a
resumed or forked session, composes it too, and a continued child keeps it
through its recorded prompt. The SDK does not discover workspace files: read
`AGENTS.md` or its equivalent yourself and pass the bytes there. A wholesale
replacement must keep the invariants that
`agent.LoopRules` returns; the primitives exist so a host never transcribes them.

### Tools and policy

Eight builtin tools are available behind one validated registry with `os.Root`
workspace containment: `read_file`, `read_image`, `edit_file`, `search`,
`run_command`, `web_search`, `web_fetch`, and `todo`. `delegate` is added when
delegation is configured. They are the same tools the first-party acode agent
uses.

`read_file` returns a contiguous numbered line window (`offset`/`limit`, 2,000
lines by default), `read_image` returns a workspace PNG, JPEG, or GIF to a
vision model as an image part (downscaled to 2048 px and 512 KiB, with a
one-line caption as its text), `edit_file` applies a unique exact-byte
replacement atomically unless `replace_all` is set, `search` runs a Go RE2
regex over workspace-relative paths with an optional glob, and `run_command`
runs `/bin/sh -c` under a timeout with captured output. Caps and edge behavior
are in [limits.md](docs/limits.md) and
[architecture.md](docs/architecture.md#5-tools).

`ToolsConfig.Disable` removes a builtin tool and `ToolsConfig.Extra` registers
caller tools. Caller tools are recorded in `session_start.custom_tools`; a
resume must supply the same schemas and `ConcurrencySafe` declarations.
`ToolsConfig.RootGrant` narrows what the root is offered and may call without
changing the session grant, so a narrow orchestrator can delegate to broader
analysts; children are still intersected with the session grant.
`WebFetchConfig.AllowedOrigins` lists exact `scheme://host:port` origins the
builtin fetcher may reach when they resolve to loopback, private, or shared
address space, so a host can permit a local development server without a global
"allow private" switch. The list is host configuration; the model and fetched
content can never widen it, metadata addresses stay refused, and the Crawl4AI
provider rejects it.
`Config.Authorize` runs before every tool execution, including delegated
children. Returning `&aharness.Deny{}` (or another non-cancellation error)
produces a model-visible `permission_denied` result.

Authorization is policy, not a sandbox. Caller tools run with the host
process's privileges, `run_command` is not confined, and OS isolation is the
host's responsibility.

Tool calls within one run execute sequentially in arrival order, which is also
the durable log, replay, authorization, and stuck-detection order. Independent
work belongs in one batch tool that fans out internally, or in delegated
children whose grants are read-only or declare `ConcurrencySafe`. Delegation
depth is one. `DelegationConfig.Agents` is required: the SDK ships no role
definitions, and `Example_delegation` in `example_test.go` is the starting
template.

`DelegationConfig.ObserveChild` opts into a live per-child view. It is called
once per child that starts a run with a `ChildTarget` (session, agent, intent,
model, effort, tools) and returns that child's event sink, which receives its
own text, thinking, tool calls and results, notices, and run start and finish,
stamped with the child session. Ordering is per child; merging children into one
view is the host's. Sinks for different children may run concurrently with each
other and with the run's own stream, so a sink must not block and must be safe
for concurrent use. Registering an observer streams delegated children, which
makes a post-delta transport failure terminal for that child; leave
`ObserveChild` nil to keep the counter-only `Subagent*` roster with the full
retry budget. A sink error or panic ends that child as `internal_error` and
leaves its siblings and the run alone.

`Tool.Run` and `Config.Authorize` receive the calling session in
`ToolCall.Session` and, for a delegated child, the definition name in
`ToolCall.Agent`. A root call has an empty `Agent`. Use `Session` as the unique
identity: the same definition can run more than once. Both fields are empty on
model request, response, and event DTOs.

`ToolRequest.Path` is the absolute, lexically cleaned target of a built-in file
tool: `read_file`, `edit_file`, or `search` (defaulting to the workspace root).
It is empty for other tools and when the arguments do not contain a usable
path. It is not symlink-resolved, and `run_command` has no `Path`; the tools
still perform their own containment checks, and authorization remains policy
rather than a sandbox.

`Tool.ConcurrencySafe` is a static, recorded declaration because the fan-out
decision is made from the child grant before any child tool call exists. An
argument-dependent predicate could not change that decision. Declare it only
when the tool is safe for every call in concurrently running children; use
per-session state or internal locking when calls share resources.

### Large child reports

The 16 KiB `delegate` task and report caps protect the parent transcript; keep
them. When a child must return more, hand off a file instead of enlarging the
report:

1. Register a custom `write_report` tool. Its `Run` derives the target from
   `call.Session` (and optionally `call.Agent`), writes under a host-owned
   directory, and returns the workspace-relative path. Do not take the path
   from model-supplied arguments; that would make routing forgeable.
2. Grant `write_report` to the child definitions that need it. A child can use
   only its grant.
3. Have the child return a short summary plus the path in its 16 KiB report.
4. Read the file in the parent with `read_file` (60 KiB per page, `offset`
   continuation), or read it host-side. Keep it under the workspace if the
   built-in `read_file` must reach it.

The report file is auxiliary; the child session log remains the durable record,
and the `delegate` result still carries only the bounded summary into the parent
context.

### State directory inside the workspace

The default state directory is outside the workspace. A host may place it
inside for co-location, for example `StateDir: filepath.Join(workspace,
".aharness")`. The built-in file tools are workspace-contained, so the agent
can then reach its own session logs. Add an `Authorize` write deny using
`ToolRequest.Path`:

```go
stateDir := filepath.Join(workspace, ".aharness")
Authorize: func(ctx context.Context, req aharness.ToolRequest) error {
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

Deny `read_file` and `search` too if session logs must not be readable. This is
policy, not a sandbox: `run_command` has no `Path` and is not confined, so deny
`ToolRunCommand` or isolate the process when writes must be impossible. The log
is for debugging and resume, not tamper-proof audit; a same-UID process can
edit it.

### Scope of the library

aharness is a coding-agent SDK, not a general agent development kit. It adds
no provider matrix, graph or workflow orchestration, memory or RAG, pluggable
session store, background or nested delegation, structured-output DTOs, prompt
templating, or public TUI package. A change that alters a recorded
contract—session schema, prompt, tool grant, projection, or concurrency
model—is a different product, not an extension. The shipped facade is
enumerated in [architecture.md
§3.2.1](docs/architecture.md#321-public-library-facade).

The tool surface follows the same rule. A builtin is added only when the SDK
must understand the tool to keep the loop's guarantees: result pairing,
projection, caps, model capability handling, and replay. A tool that needs an
environment, a browser, a service, approval, or domain knowledge belongs to the
host through `ToolsConfig.Extra`; a policy the SDK enforces with host-supplied
values is an SDK mechanism with a strict default. Rendering, screenshots, and
accessibility or geometry snapshots are host tools; a domain tool such as a
quote lookup is a host tool too. The test and its consequences are in
[architecture.md §5.1](docs/architecture.md#51-where-a-tool-belongs).

## Component packages

The component packages are the SDK's lower-level contracts. They are documented
in [architecture.md §3](docs/architecture.md#3-packages-and-contracts):

| Package | Use it for |
|---|---|
| `github.com/eddiectc/aharness/agent` | The runner, display events, history and latest-model-exchange readers, state inspection, prompt/agent definitions, and the subagent spawner. |
| `github.com/eddiectc/aharness/engine` | New/resume/fork composition, workspace and work-directory setup (`WorkDir`, `CleanupWork`), session metadata (`History`, `Todo`, `LastSeq`), head-read `ListSummaries`, and lock-free inspection/listing. |
| `github.com/eddiectc/aharness/model` | Canonical model types, Chat Completions and Responses adapters, streaming, and models.dev catalog discovery. |
| `github.com/eddiectc/aharness/tools` | The builtin registry, workspace containment, the seven workspace/web tools, and the `delegate` seam. |
| `github.com/eddiectc/aharness/session` | The v2 JSONL event log, locking, torn-line repair, replay, and fork copy. |

Prefer the root facade unless the host genuinely needs one of these contracts.
The root facade copies DTOs at the boundary so component types can evolve.

## Documentation

| Document | Defines |
|---|---|
| [architecture.md](docs/architecture.md) | SDK structure, component contracts, loop, tools, session log, and public facade |
| [coding-standard.md](docs/coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [limits.md](docs/limits.md) | SDK caps, limits, and timeouts |
| [testing-strategy.md](docs/testing-strategy.md) | Unit, integration, fuzz, and manual smoke layers |
| [definition-of-done.md](docs/definition-of-done.md) | Completion checklist for every change |
| [docs/README.md](docs/README.md) | Documentation map and placement rules |
| [acode](https://github.com/eddiectc/acode) | The first-party TUI/batch coding agent, CLI, and configuration docs |

## Contributing

Follow [AGENTS.md](AGENTS.md), the [coding standard](docs/coding-standard.md),
and the [definition of done](docs/definition-of-done.md). `make dod` runs
`docs-check`, formatting, `go vet`, `golangci-lint`, `go test -race`,
`go build ./...`, and `govulncheck`.

`main` is protected: work on a short-lived feature branch and open a pull
request targeting `main`; never push directly to `main`. See
[docs/ci-release.md](docs/ci-release.md) §1.5 for the branching workflow and
§2 for releases.

## License

MIT. See [LICENSE](LICENSE).
