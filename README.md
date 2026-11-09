# aharness

aharness is a small coding-agent harness written in Go. It takes one task, runs
a bounded model/tool loop against a workspace, appends every event to a JSONL
session log, and exits with a final answer or a typed stop reason.

- **Status:** pre-v0. The documents in [`docs/`](docs) are the contract; there
  is no runnable binary yet. Progress is tracked by the
  [v0 build plan](docs/v0-build.md).
- **Toolchain:** Go 1.27.3, module `aharness`.
- **Dependencies:** Go standard library only at runtime; see the
  [coding standard](docs/coding-standard.md) for the narrow exception process.

## What v0 includes

- One provider (`opencode-go`) behind provider-neutral types, speaking the
  OpenAI Chat Completions dialect, with the `crouton-alpha-free` and
  `deepseek-v4.1-flash` models.
- Four tools — `read_file`, `edit_file`, `search`, `run_command` — behind one
  validated registry with `os.Root` workspace containment.
- An append-only JSONL session log; the model input is a projection rebuilt
  from events on every turn, with deterministic compaction.
- Explicit turn, token, context, and wall-clock budgets, plus typed stop
  reasons and per-tool failure handling.
- One binary configured by flags and environment; no daemon, UI, config file,
  or provider matrix.

Not in v0: streaming, resume/fork, MCP/plugins, subagents, RAG, model-based
summarization, and built-in evals. See [v0 scope](docs/v0-build.md#1-scope).

## Documentation

| Document | What it defines |
|---|---|
| [architecture.md](docs/architecture.md) | System structure, packages, loop and tool contracts, CLI and configuration |
| [coding-standard.md](docs/coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [v0-build.md](docs/v0-build.md) | Scope, milestones, acceptance criteria, decisions, and risks |
| [testing-strategy.md](docs/testing-strategy.md) | Unit, integration, and manual smoke checks |
| [definition-of-done.md](docs/definition-of-done.md) | The v0 completion checklist |
| [agent-harness-minimum.md](docs/agent-harness-minimum.md) | Research rationale and primary sources behind the design |
| [AGENTS.md](AGENTS.md) | Instructions for coding agents working in this repository |

## Build and run (planned for v0)

The CLI is specified in [architecture.md §9](docs/architecture.md#9-cli-and-configuration).
Once the implementation lands:

```sh
go build -trimpath -o bin/aharness ./cmd/aharness

export AHARNESS_PROVIDER_TOKEN=...   # optional for local no-auth endpoints
./bin/aharness -task "Fix the failing test" \
  -model deepseek-v4.1-flash \
  -workspace /path/to/repo
```

Exactly one of `-task` or `-task-file` is required, and `-model` (or
`AHARNESS_MODEL`) is required. Session logs default to
`$XDG_STATE_HOME/aharness` (or `~/.local/state/aharness`), outside the
workspace; `-state-dir` overrides it.

## Isolation

aharness does not sandbox itself and has no approval workflow. Tool calls write
to the real workspace and can use the host network. Run it inside bwrap,
Docker, or a VM whenever the repository or the task is untrusted.

## Contributing

Follow [AGENTS.md](AGENTS.md) and the [coding standard](docs/coding-standard.md);
every change must pass the §8 gates before review.
