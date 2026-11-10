# aharness

aharness is a small coding-agent harness written in Go. It takes one task, runs
a bounded model/tool loop against a workspace, appends every event to a JSONL
session log, and exits with a final answer or a typed stop reason.

- **Status:** v1 implementation complete (M0–M3). Mock-provider, integration,
  fuzz, and §8 gate checks pass; a real-provider streamed smoke is pending
  provider credentials. The one-shot v0 loop now supports opt-in
  assistant-text streaming, interactive `SIGINT` interruption and stdin
  steering, and resume/fork over the v2 session log. v0 history is in the
  [v0 build plan](docs/v0-build.md); v1 contracts and milestones are in the
  [v1 build plan](docs/v1-build.md).
- **Toolchain:** Go 1.27.3, module `aharness`.
- **Dependencies:** Go standard library only at runtime; see the
  [coding standard](docs/coding-standard.md) for the narrow exception process.

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
- Opt-in `-stream` assistant-text streaming for both dialects. The canonical
  response is the only transcript record; deltas are display-only.
- Opt-in `-interactive`: the first `SIGINT` interrupts the in-flight model call
  or tool and the run continues; the second `SIGINT` or `SIGTERM` terminates
  with exit 130. Stdin lines are steering messages.
- Explicit cumulative turn/token budgets, a per-run wall clock, context
  projection with deterministic compaction, and typed stop reasons.
- One binary configured by flags and environment; no daemon, UI, config file,
  or provider matrix.

Not in v1: hooks, model-based summarization, subagents, RAG/embeddings,
MCP/plugins, additional provider dialects, built-in evals, price tracking,
workspace snapshots, built-in isolation/approval, session rotation, v0 log
migration, or an interactive TUI. See [v1 scope](docs/v1-build.md#1-scope).

## Documentation

| Document | What it defines |
|---|---|
| [architecture.md](docs/architecture.md) | System structure, packages, loop and tool contracts, CLI and configuration |
| [limits.md](docs/limits.md) | Caps, limits, and timeouts |
| [coding-standard.md](docs/coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [v1-build.md](docs/v1-build.md) | v1 scope, contract deltas, milestones, decisions, and costly-to-reverse assumptions |
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
./bin/aharness -task "Fix the failing test" \
  -model deepseek-v4.1-flash \
  -workspace /path/to/repo

# Continue an existing v2 session; a task is optional unless the last run
# completed. Inherited model/workspace/limits can be overridden.
./bin/aharness -resume <session-id> -task "Now add the regression test"

# Create a new self-contained session from an existing one.
./bin/aharness -fork <session-id> -task "Try a different approach"

# Stream assistant text and accept stdin steering; first SIGINT interrupts.
./bin/aharness -interactive -stream -task "Investigate the failure"
```

`make build` and `make test` wrap the same build and race-test commands.

Exactly one of `-task` or `-task-file` is required for a new session or a
fork, and `-model` (or `AHARNESS_MODEL`) is required for a new session. Session
logs default to `$XDG_STATE_HOME/aharness` (or `~/.local/state/aharness`),
outside the workspace; `-state-dir` overrides it. Session IDs resolve only
under `<state>/sessions/<id>.jsonl`. `-provider-base-url` (or
`AHARNESS_PROVIDER_BASE_URL`) points the adapters at a fake or local
OpenAI-compatible server; normal runs leave it at the hosted default. On
resume/fork the recorded base URL is inherited unless explicitly overridden.

## Isolation

aharness does not sandbox itself and has no approval workflow. Tool calls write
to the real workspace and can use the host network. Run it inside bwrap,
Docker, or a VM whenever the repository or the task is untrusted. Interactive
and resumed runs extend the exposure of the same process and tool contract.

## Contributing

Follow [AGENTS.md](AGENTS.md) and the [coding standard](docs/coding-standard.md);
every change must pass the §8 gates before review.
