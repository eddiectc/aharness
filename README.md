# aharness

aharness is a coding agent for a single workspace, written in Go. It runs a
model/tool loop against the files and commands it is allowed to use, appends
every event to a durable JSONL session log, and ends a run with a final answer
or a typed stop reason. The loop is bounded by the model context window,
termination policies, and errors; turn and token budgets are opt-in and
unlimited by default.

The same loop is available two ways:

- **[Use as a Go library](#use-as-a-go-library):** embed the agent in another
  Go program. Headless, in-process, and independent of the CLI and TUI.
- **[TUI coding agent](#tui-coding-agent):** run the `aharness` binary in a
  terminal for an interactive workbench over the same sessions.

## Use as a Go library

The root package `github.com/eddiectc/aharness` is the embeddable form of the
loop: durable sessions, builtin and caller tools, model adapters, delegation,
events, and typed results. It is headless: it adds no environment, config-file,
working-directory, TTY, or signal layer, and imports no CLI or TUI code. The
recorded contracts, including the public facade surface, are in
[architecture.md](docs/architecture.md#321-public-library-facade).

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
returns, so `errors.Is` and `errors.As` work.

`Agent.Start` returns a `*Run` whose `Events()` iterator streams text and
reasoning deltas, tool start and completion, notices, recorded messages,
delegated-child progress, and the terminal `RunFinished`. `Interrupt` stops the
in-flight model or tool operation and lets the run continue; `Cancel` ends the
run; `Steer` and `SteerChild` enqueue input at turn boundaries. Events are
display-only observations, and the session log is the durable record.

`OpenAIModel` builds a Chat Completions or Responses adapter for an explicit
OpenAI-compatible endpoint. `DiscoverModel` and `ListModels` resolve models and
limits from the [models.dev](https://models.dev) catalog. `CustomModel` wraps
any implementation of the `Model` interface; implement the optional
`StreamingModel` interface to emit deltas.

`Config.MaxTurns` and `MaxTotalTokens` bound the agent's cumulative turn and
token usage, while `RunTimeout` and `ModelTimeout` bound one run and one model
attempt; a zero value is unlimited. `Config.Instructions` appends to the
harness system prompt, while `Config.SystemPrompt` replaces it. A replacement
must preserve the loop's invariants: a tool call is the only way to act on the
workspace, every tool call receives exactly one result, and a reply without a
tool call ends the run.

### Tools and policy

Seven builtin tools are available behind one validated registry with `os.Root`
workspace containment: `read_file`, `edit_file`, `search`, `run_command`,
`web_search`, `web_fetch`, and `todo`. `delegate` is added when delegation is
configured. They are the same tools the TUI uses.

`read_file` returns a contiguous numbered line window (`offset`/`limit`, 2,000
lines by default), `edit_file` applies a unique exact-byte replacement
atomically unless `replace_all` is set, `search` runs a Go RE2 regex over
workspace-relative paths with an optional glob, and `run_command` runs
`/bin/sh -c` under a timeout with captured output. Caps and edge behavior are
in [limits.md](docs/limits.md) and
[architecture.md](docs/architecture.md#5-tools).

`ToolsConfig.Disable` removes a builtin tool and `ToolsConfig.Extra` registers
caller tools. Caller tools are recorded in `session_start.custom_tools`; a
resume must supply the same schemas and `ConcurrencySafe` declarations.
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
depth is one.

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

The library is a coding-agent SDK, not a general agent development kit. It adds
no provider matrix, graph or workflow orchestration, memory or RAG, pluggable
session store, background or nested delegation, structured-output DTOs, prompt
templating, or public TUI package. A change that alters a recorded
contract—session schema, prompt, tool grant, projection, or concurrency
model—is a different product, not an extension. The shipped facade is
enumerated in [architecture.md
§3.2.1](docs/architecture.md#321-public-library-facade).

## TUI coding agent

Run `aharness` in a terminal and it opens a full-screen TUI. It is the default
when stdin and stdout are both terminals; otherwise the invocation batches. One
process owns one active session and executes one submitted message at a time,
with streaming output, tool activity, steering during a run, and
interrupt/cancel controls.

### Build and run

```sh
make build        # go build -trimpath -o bin/aharness ./cmd/aharness
make install      # install to ~/.local/bin; override with INSTALL_DIR

export AHARNESS_PROVIDER_TOKEN=...   # omit for a local no-auth endpoint

# Print the usable catalog, or force a refresh of the 24-hour cache.
./bin/aharness -list-models
./bin/aharness -refresh-models

# Open the TUI in a workspace, optionally with a first message.
./bin/aharness -model <model-id> -workspace /path/to/repo
./bin/aharness -model <model-id> -task "Fix the failing test"

# Resume a session in the TUI, or fork it into a new one.
./bin/aharness -resume <session-id>
./bin/aharness -fork <session-id>
```

Building requires Go 1.27.3 or newer. In the TUI and `-ui-json`, the task is
optional: a new session is created eagerly, the header shows its ID, and the
first submitted message supplies the task. `-model` (or `AHARNESS_MODEL`) is
required for a new session and must name a usable model in the catalog.

### Models and thinking effort

The CLI's only provider is `opencode-go`, selected by `-provider` or
`AHARNESS_PROVIDER`. A new session resolves its model's dialect, base URL,
context window, and input cap from [models.dev](https://models.dev), cached for
24 hours under `<state-dir>/cache/models.dev/<provider>.json`. A fresh cache
avoids the network; a stale cache refreshes conditionally, and a failed
implicit refresh falls back to stale data with a warning. `-refresh-models` and
`Ctrl-R` in the model picker force an unconditional refresh.

`-list-models` prints the usable catalog without creating a session, including
each model's advertised thinking efforts. `-thinking-effort` (or
`AHARNESS_THINKING_EFFORT`) sends the selected `reasoning_effort` on Chat
Completions or `reasoning.effort` on Responses; unset uses the provider/model
default, and `default` clears an inherited value. The effective effort is
recorded and inherited on resume or fork unless changed. In the TUI, `Ctrl-P`
opens the model picker and `F2` opens the effort picker for the next run.

Resume and fork resolve the recorded provider, model, dialect, base URL,
limits, and effort without a catalog fetch. An explicit model change
re-resolves through the catalog, and an explicitly unsupported effort is a
startup error.

### The terminal workbench

The transcript renders assistant text as rich Markdown with table overlays and
displays workspace-contained images; `Ctrl-R` toggles rich and raw rendering.
`Ctrl-T` opens the most recent table, `Ctrl-G` the most recent image, and
`Ctrl-O` a workspace file picker. Model reasoning appears as a display-only
`thinking` block; it is never logged, projected, replayed, or printed in batch
mode. `F3` folds or expands thinking and tool blocks.

The `todo` tool maintains a checklist: send items to replace it, omit items to
read it, or send an empty list to clear it. At most one item is `in_progress`.
The TUI shows the checklist in a persistent panel above the composer. Tool
calls show their `intent` as the block title, and delegated work gets a
foldable block per call.

The composer is multi-line: `Enter` submits, `Shift-Enter` or `Alt-Enter`
inserts a newline, and `F4` edits the draft in `$VISUAL` or `$EDITOR`. While a
run is active, `Enter` submits steering for the running agent, `Esc` interrupts
the in-flight operation, and a second `Ctrl-C` cancels the run. `Ctrl-D` quits.
The mouse wheel scrolls the transcript; because the TUI captures the mouse,
use `Shift`+drag to select text and `Shift`+middle-click or `Ctrl+Shift+V` to
paste.

| Key | Action |
|---|---|
| `Enter` / `Shift-Enter` | submit or steer / insert a newline |
| `Esc` | close an overlay; otherwise interrupt the operation or clear the composer |
| `Ctrl-C` | during a run: interrupt, then cancel; idle: clear the composer |
| `Ctrl-D` | quit |
| `Ctrl-P` / `F2` | model picker / thinking-effort picker |
| `Ctrl-O` / `Ctrl-G` / `Ctrl-T` | file picker / image viewer / table overlay |
| `Ctrl-R` / `F3` | rich or raw rendering / fold or expand blocks |
| `F1` / `Ctrl-X` | help / session and terminal details |
| `F4` | edit the composer in `$VISUAL` or `$EDITOR` |

Typing `/` opens a filtered command menu; `Tab` completes the highlighted
command and `Enter` runs it. The commands are `/help`, `/model`, `/effort`,
`/provider`, `/details`, `/settings`, `/agents`, `/agent-hub`, `/new`,
`/resume`, `/files`, `/table`, `/image`, `/rich`, `/raw`, `/repaint`,
`/refresh`, `/interrupt`, `/cancel`, and `/quit`. Unrecognized `/text` is sent
as a normal message. The full reference is in [commands.md](docs/commands.md).

### Sessions, resume, and fork

Sessions are append-only v2 JSONL logs under
`<state-dir>/sessions/<id>.jsonl`; the state directory defaults to
`~/.aharness` and can be moved with `-state-dir` (or `AHARNESS_STATE_DIR`).
Logs live outside the workspace. A process holds one active session and an
exclusive `flock` on its log; `/new` replaces the active session and `/resume`
reopens a previous session in the same workspace while idle.

`-resume` continues a session in place. `-fork` creates a new self-contained
session with parent metadata and copies the history under the forked contract.
Both open the TUI by default; add `-batch` for one-shot output. Each run
records turns, provider usage totals and subsets, and an observational cost
estimate from the catalog price snapshot. The estimate is not a billing
authority.

### Delegated subagents

The `delegate` tool runs up to four child agents in one call. Each child is a
separate session under `<state-dir>/sessions`, linked by `parent_session` and
`parent_call_id`, with its own resolved prompt, tool grant, model, effort,
provider, usage, and pricing. A child cannot delegate further.

Five agents are built in: `explore`, `researcher`, `reviewer`,
`implementer`, and `task`. Files in `<state-dir>/agents/*.md` override them by
exact name; an override uses a strict `key: value` header and a Markdown prompt
body. A call runs concurrently (up to four) when every task's grant is limited
to `read_file`, `search`, `web_search`, and `web_fetch`; any write-capable task
forces the whole call to run sequentially.

The TUI renders one foldable block per delegated call. `/agent-hub` shows a
live roster of running children with agent, intent, model, effort, elapsed,
context tokens, turns, cost, and status. `Enter` on a roster row focuses that
child: its read-only transcript replaces the parent view, and composer input
becomes steering for the child; `Esc` returns to the parent without
interrupting it. `/agents` browses the definitions and edits an agent's
`model`, `thinking_effort`, `max_turns`, and `enabled` through the
`agent_overrides` config object. Tool grants and prompts stay file-only, so the
settings surface cannot widen authority.

Child input and output tokens count against `-max-total-tokens`. Child turns do
not count against `-max-turns`, but seed the child's default cap.
`-max-subagents` (default 8, `0` unlimited) bounds fresh children per run. A
session's tool grant is fixed for its lifetime, so delegation requires a
session whose grant includes the `delegate` tool. In `-batch`, the grant omits
`delegate`, because an unattended run has no way to observe or steer children.

### Batch and headless modes

`-batch` forces the one-shot CLI: final assistant text goes to stdout,
diagnostics go to stderr, and the exit code reports the result. A new session
needs exactly one of `-task` or `-task-file`; `-fork` requires a task;
`-resume` requires a task only after a completed run. `-stream` opts into
assistant-text streaming; the TUI and `-ui-json` always stream, and deltas are
display-only.

`-ui-json` runs the same Bubble Tea program headlessly over a versioned JSONL
protocol of frames, semantic events, and deterministic SVG snapshots, with no
PTY required. `-ui-size` sets the initial frame size. Without `-ui-json`,
non-TTY invocations batch automatically.

### Configuration

Configuration comes from flags, environment variables, an optional JSON config
file, and built-in defaults, in that order. The file defaults to
`<state-dir>/config.json` and can be selected with `-config` or
`AHARNESS_CONFIG`; `-config ""` disables it, and unknown keys are rejected.

The common operational keys cover the model, workspace, provider base URL,
thinking effort, run turn and token budgets, context and output caps, timeouts,
`require_usage`, and `log_requests`. `searxng_url` configures `web_search`;
`web_timeout`, `web_fetch_provider`, `web_fetch_url`, and `web_fetch_key`
configure `web_fetch`. `agent_overrides` routes an agent's model, effort, turn
cap, and enable state. `ui_rich` and `ui_expand_all` set the TUI's initial
rendering and fold modes. Run budgets default to unlimited.

`/settings` lists every config-file key with its saved or effective value and
the layer that supplies it, then writes edits atomically. It masks
`web_fetch_key` and applies display preferences immediately. `/agents` writes
`agent_overrides` through the same writer. See
[configuration.md](docs/configuration.md) and [limits.md](docs/limits.md).

### Web tools

`web_search` queries an operator-configured SearXNG instance with
`format=json` and returns up to ten titles, URLs, and snippets. Set
`-searxng-url`, `AHARNESS_SEARXNG_URL`, or `searxng_url`; HTTP is allowed only
for loopback instances. With no endpoint, the tool stays registered and
returns an actionable `unavailable` result.

`web_fetch` retrieves one absolute http(s) URL and returns plain text. HTML is
stripped of scripts, styles, navigation, and boilerplate by a native Go
extractor; text, Markdown, JSON, XML, and CSV pass through. Results are capped
at 60 KiB with `offset` continuation, and empty or app-shell pages report
`content_unavailable` rather than empty success. The builtin client refuses
private, loopback, link-local, and metadata addresses at dial time, follows at
most five same-host redirects, and sends no credentials or cookies. For
JavaScript-heavy pages, `-web-fetch-provider crawl4ai` uses an operator-run
Crawl4AI endpoint (`-web-fetch-url`, optional `-web-fetch-key`);
`web_timeout` defaults to 30 seconds and is capped at 120.

## Isolation

aharness does not sandbox itself and has no approval workflow. Tool calls write
to the real workspace and can use the host network. Run it inside bwrap,
Docker, or a VM whenever the repository or the task is untrusted. The TUI adds
no approval step or sandbox.

### Sandboxed runs with bubblewrap

[`scripts/aharness-bwrap`](scripts/aharness-bwrap) runs the binary under
bubblewrap with a minimal read-only root. The project and `~/.aharness` stay
writable, the rest of `$HOME` is hidden, and the script refuses a project that
is `/`, `$HOME`, or an ancestor of it.

```sh
cd /path/to/repo
scripts/aharness-bwrap -model <model-id> -task "Fix the failing test"

# Select the project instead of using the current directory.
scripts/aharness-bwrap --project /path/to/repo -model <model-id>

# Open a shell in the same sandbox to check what the agent can see.
scripts/aharness-bwrap --project /path/to/repo --shell
```

The sandbox shares the host network, so it bounds filesystem and process
access, not egress: the agent can still reach the provider and anything else
the host can reach. Environment variables are inherited, which is how
`AHARNESS_PROVIDER_TOKEN` gets in.

The script is a sample. It preconfigures Go (`~/go/pkg` writable, `~/go/bin` and
`~/.local/bin` on `PATH`) and mounts `~/.gitconfig` and `~/.agents` read-only.
It does not mount credentials: `~/.ssh` and cloud or registry tokens stay
hidden. Another ecosystem needs its per-user toolchain mounted, which is
everything not under `/usr`:

| Ecosystem | Add to `AHARNESS_BWRAP_BIND` |
|---|---|
| Rust via rustup | `~/.cargo ~/.rustup` |
| Node via nvm | `~/.nvm` |
| Python via pyenv, or uv-managed interpreters | `~/.pyenv ~/.local/share/uv` |
| Toolchain installed with `go install`-style user prefixes | `~/go/bin`, `~/.local/bin` (already mounted read-only) |

Adding the ecosystem's cache (`~/.cache/go-build` for Go, `~/.cache/uv`,
`~/.npm`) avoids cold rebuilds and re-downloads. It is optional: without it the
tools still work, just slower.

```sh
AHARNESS_BWRAP_BIND="$HOME/.cargo $HOME/.rustup" \
  scripts/aharness-bwrap -model <model-id> -task "Fix the failing test"
```

Paths must be absolute and are bound read-write, so the agent can modify them. A
project-local `.venv`, `node_modules`, or `target/` needs no entry: it lives
inside the project, which is already mounted.

## Documentation

| Document | What it defines |
|---|---|
| [architecture.md](docs/architecture.md) | System structure, packages, loop and tool contracts, the public library facade, CLI, and configuration |
| [configuration.md](docs/configuration.md) | Precedence, the JSON config file, environment variables, and which limits are worth configuring |
| [limits.md](docs/limits.md) | Caps, limits, and timeouts |
| [coding-standard.md](docs/coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [tui.md](docs/tui.md) | TUI modes, keys, `-ui-json` protocol, and caps |
| [commands.md](docs/commands.md) | Slash-command reference: syntax, autocomplete, arguments, key equivalents, idle/run behavior, `-ui-json` mapping, and caps |
| [testing-strategy.md](docs/testing-strategy.md) | Unit, integration, fuzz, and manual smoke checks |
| [definition-of-done.md](docs/definition-of-done.md) | Completion checklist for every change |
| [docs/README.md](docs/README.md) | Documentation map and placement rules |
| [AGENTS.md](AGENTS.md) | Instructions for coding agents working in this repository |

## Contributing

Follow [AGENTS.md](AGENTS.md), the [coding standard](docs/coding-standard.md),
and the [definition of done](docs/definition-of-done.md). `make dod` runs the
automated checks; review also covers behavior, edge cases, and the real CLI and
TUI surfaces.
