# aharness

aharness is a small coding-agent harness written in Go. It takes one task, runs
a bounded model/tool loop against a workspace, appends every event to a JSONL
session log, and exits with a final answer or a typed stop reason.

- **Status:** v0 complete. Both implemented dialects passed the real-provider
  smoke: `deepseek-v4.1-flash` (Chat Completions) and
  `muse-spark-1.3-contributor` (Responses). v1 is drafted in the
  [v1 build plan](docs/v1-build.md); v0 history is in the
  [v0 build plan](docs/v0-build.md).
- **Toolchain:** Go 1.27.3, module `aharness`.
- **Dependencies:** Go standard library only at runtime; see the
  [coding standard](docs/coding-standard.md) for the narrow exception process.

## What v0 includes

- One provider (`opencode-go`) behind a canonical model, with OpenAI Chat
  Completions and Responses adapters, and the `crouton-alpha-free`,
  `deepseek-v4.1-flash`, and `muse-spark-1.3-contributor` models mapped to
  their dialects.
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
The v1 draft adds streaming, interrupt/steering, and resume/fork; see
[v1 scope](docs/v1-build.md#1-scope).

## Documentation

| Document | What it defines |
|---|---|
| [architecture.md](docs/architecture.md) | System structure, packages, loop and tool contracts, CLI and configuration |
| [limits.md](docs/limits.md) | Caps, limits, and timeouts (current and v1 planned) |
| [coding-standard.md](docs/coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [v1-build.md](docs/v1-build.md) | Draft v1 scope, contract deltas, milestones, decisions, and costly-to-reverse assumptions |
| [v0-build.md](docs/v0-build.md) | v0 scope, milestones, acceptance criteria, decisions, and risks |
| [testing-strategy.md](docs/testing-strategy.md) | Unit, integration, and manual smoke checks |
| [definition-of-done.md](docs/definition-of-done.md) | The v0 completion checklist |
| [agent-harness-minimum.md](docs/agent-harness-minimum.md) | Research rationale and primary sources behind the design |
| [AGENTS.md](AGENTS.md) | Instructions for coding agents working in this repository |

## Build and run

The CLI is specified in [architecture.md §9](docs/architecture.md#9-cli-and-configuration).

```sh
go build -trimpath -o bin/aharness ./cmd/aharness

export AHARNESS_PROVIDER_TOKEN=...   # optional for local no-auth endpoints
./bin/aharness -task "Fix the failing test" \
  -model deepseek-v4.1-flash \
  -workspace /path/to/repo
```

`make build` and `make test` wrap the same build and race-test commands.

Exactly one of `-task` or `-task-file` is required, and `-model` (or
`AHARNESS_MODEL`) is required. Session logs default to
`$XDG_STATE_HOME/aharness` (or `~/.local/state/aharness`), outside the
workspace; `-state-dir` overrides it. `-provider-base-url` (or
`AHARNESS_PROVIDER_BASE_URL`) points the adapters at a fake or local
OpenAI-compatible server; normal runs leave it at the hosted default.

## Isolation

aharness does not sandbox itself and has no approval workflow. Tool calls write
to the real workspace and can use the host network. Run it inside bwrap,
Docker, or a VM whenever the repository or the task is untrusted.

## Contributing

Follow [AGENTS.md](AGENTS.md) and the [coding standard](docs/coding-standard.md);
every change must pass the §8 gates before review.
