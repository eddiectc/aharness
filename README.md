# aharness

aharness is a small coding-agent harness written in Go. It takes one task, runs
a bounded model/tool loop against a workspace, appends every event to a JSONL
session log, and exits with a final answer or a typed stop reason.

- **Status:** v1 implementation complete (M0–M3), and the post-v1 TUI
  (M0–M5 in the [TUI build plan](docs/tui-build.md)) is implemented. The TUI is
  the default in a terminal; `-batch` keeps the one-shot CLI and `-ui-json` is
  the headless automation surface. Mock-provider, integration, fuzz, race, and
  §8 gate checks pass. The real-provider TUI smoke remains pending provider
  credentials; it is the one unverified release gate.
- **Toolchain:** Go 1.27.3, module `aharness`.
- **Dependencies:** the Go standard library by default. The approved TUI
  exception is Bubble Tea v2, Bubbles v2, Lip Gloss v2, Glamour v2, ntcharts v2,
  and `x/vt`; the justification and measured binary impact are recorded in the
  [coding standard](docs/coding-standard.md#3-dependencies) and the
  [TUI build plan](docs/tui-build.md#28-package-layout-and-wiring).

## What v1 includes

- One provider (`opencode-go`) behind a canonical model, with OpenAI Chat
  Completions and Responses adapters, and the `crouton-alpha-free`,
  `deepseek-v4.1-flash`, and `muse-spark-1.3-contributor` models mapped to
  their dialects.
- Four tools — `read_file`, `edit_file`, `search`, `run_command` — behind one
  validated registry with `os.Root` workspace containment.
- An append-only v2 JSONL session log with `run_start`/`run_end` boundaries,
  exclusive/shared `flock`, torn-final-line repair, and a per-run `final`.
- `-resume <id>` continues a session in place; `-fork <id>` creates a new
  self-contained session with parent metadata. v0 (`v=1`) logs are rejected;
  there is no migration.
- Opt-in `-stream` assistant-text streaming for both dialects in batch mode.
  The TUI and `-ui-json` always stream. The canonical response is the only
  transcript record; deltas are display-only.
- The default interactive TUI: a full-screen transcript, streaming text, tool
  activity, steering, interrupt/cancel, markdown, tables, file/model pickers,
  and workspace-contained images. One process owns one session and may execute
  one run per submitted message.
- `-batch` forces the one-shot stdout contract. Non-TTY invocations batch
  automatically. `-ui-json` runs the same Bubble Tea program headlessly over a
  versioned JSONL protocol (`frames`, semantic `events`, and deterministic SVG
  `snapshots`), with no PTY required.
- `Esc` interrupts only the in-flight operation, the first `Ctrl-C` does the
  same, the second cancels the run, and `Ctrl-D` quits. `SIGTERM`/`SIGHUP`
  cancel, write terminal events, restore the terminal, and exit 130.
- Explicit cumulative turn/token budgets, a per-run wall clock, context
  projection with deterministic compaction, and typed stop reasons.
- One binary configured by flags and environment; no daemon, config file, or
  provider matrix.

Not in v1: hooks, model-based summarization, subagents, RAG/embeddings,
MCP/plugins, additional provider dialects, built-in evals, price tracking,
workspace snapshots, built-in isolation/approval, session rotation, or v0 log
migration. See [v1 scope](docs/v1-build.md#1-scope). The TUI adds no durable
event type, schema version, tool, prompt input, or security boundary. Its
contract, milestones, and follow-ons are in [tui-build.md](docs/tui-build.md).

## Documentation

| Document | What it defines |
|---|---|
| [architecture.md](docs/architecture.md) | System structure, packages, loop and tool contracts, CLI and configuration |
| [limits.md](docs/limits.md) | Caps, limits, and timeouts |
| [coding-standard.md](docs/coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [v1-build.md](docs/v1-build.md) | v1 scope, contract deltas, milestones, decisions, and costly-to-reverse assumptions |
| [tui-build.md](docs/tui-build.md) | Post-v1 default TUI, `-batch`/`-ui-json` modes, display/terminal contracts, milestones, decisions, and risks |
| [tui.md](docs/tui.md) | Shipped TUI modes, keys, `-ui-json` protocol, caps, and verification status |
| [v0-build.md](docs/v0-build.md) | v0 scope, milestones, acceptance criteria, decisions, and risks |
| [testing-strategy.md](docs/testing-strategy.md) | Unit, integration, fuzz, and manual smoke checks |
| [definition-of-done.md](docs/definition-of-done.md) | The v0 completion checklist |
| [agent-harness-minimum.md](docs/agent-harness-minimum.md) | Research rationale and primary sources behind the design |
| [AGENTS.md](AGENTS.md) | Instructions for coding agents working in this repository |

## Build and run

The CLI is specified in [architecture.md §9](docs/architecture.md#9-cli-and-configuration).

```sh
go build -trimpath -o bin/aharness ./cmd/aharness

export AHARNESS_PROVIDER_TOKEN=...   # optional for local no-auth endpoints

# Default in a terminal: interactive TUI, waiting for the first message.
./bin/aharness -model deepseek-v4.1-flash -workspace /path/to/repo

# Seed the first TUI message.
./bin/aharness -model deepseek-v4.1-flash -task "Fix the failing test"

# One-shot batch on a terminal or in a pipe.
./bin/aharness -batch -task "Fix the failing test" -model deepseek-v4.1-flash

# Resume/fork a v2 session (TUI default; add -batch for one-shot output).
./bin/aharness -resume <session-id>
./bin/aharness -batch -resume <session-id> -task "Now add the regression test"
./bin/aharness -fork <session-id>

# Headless TUI for a coding agent; commands and frames are JSONL.
./bin/aharness -ui-json -ui-size 100x30 -model deepseek-v4.1-flash < commands.jsonl
```

`make build` and `make test` wrap the same build and race-test commands.

In TUI and `-ui-json` modes a task is optional: a new session is created
eagerly, the header shows its ID, and the first submitted message supplies the
task. `-batch` keeps the v1 matrix: a new session needs exactly one of `-task`
or `-task-file`, `-fork` requires a task, and `-resume` requires a task only
after a completed run. `-model` (or `AHARNESS_MODEL`) is required for a new
session. `-interactive` was removed in the TUI cutover; passing it is an
unknown-flag error. Session logs default to `$XDG_STATE_HOME/aharness` (or
`~/.local/state/aharness`), outside the workspace; `-state-dir` overrides it.
Session IDs resolve only under `<state>/sessions/<id>.jsonl`.
`-provider-base-url` (or `AHARNESS_PROVIDER_BASE_URL`) points the adapters at a
fake or local OpenAI-compatible server; normal runs leave it at the hosted
default. On resume/fork the recorded base URL is inherited unless explicitly
overridden.

## Isolation

aharness does not sandbox itself and has no approval workflow. Tool calls write
to the real workspace and can use the host network. Run it inside bwrap,
Docker, or a VM whenever the repository or the task is untrusted. Interactive, resumed, and TUI runs extend the exposure of the same process and
tool contract; the TUI adds no approval step or sandbox.

## Contributing

Follow [AGENTS.md](AGENTS.md) and the [coding standard](docs/coding-standard.md);
every change must pass the §8 gates before review.
