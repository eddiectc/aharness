# aharness

aharness is a small coding-agent harness written in Go. It takes one task, runs
a model/tool loop against a workspace, appends every event to a JSONL session
log, and exits with a final answer or a typed stop reason. The loop is bounded
by the model context window, termination policies, and errors; run budgets are
opt-in and unlimited by default.

- **Status:** v1 implementation complete. The agent-tooling M0–M3 work is also
  implemented, including usage/cost accounting, the contiguous 2,000-line read
  contract with `toolset-v1` replay, search/command improvements, and builtin
  SearXNG-backed web search. The
  default TUI, provider/model discovery, per-model thinking effort,
  display-only thinking text, and folded thinking/tool output are implemented.
  The TUI is
  the default in a terminal;
  `-batch` keeps the one-shot CLI and `-ui-json` is the headless automation
  surface. Mock-provider, integration, fuzz, race, and §8 gate checks pass. The
  real-provider TUI, thinking-effort, thinking-display, and transcript-folding
  runs remain pending
  provider credentials (`AHARNESS_PROVIDER_TOKEN` is unset in the implementation
  environment); they are explicit release prerequisites.
- **Toolchain:** Go 1.27.3, module `aharness`.
- **Dependencies:** the Go standard library by default. The approved TUI
  exception is Bubble Tea v2, Bubbles v2, Lip Gloss v2, Glamour v2, ntcharts v2,
  and `x/vt`; the justification and measured binary impact are recorded in the
  [coding standard](docs/coding-standard.md#3-dependencies).

## What aharness includes

- One provider (`opencode-go`, selected by `-provider` or
  `AHARNESS_PROVIDER`) behind a canonical model, with OpenAI Chat Completions
  and Responses adapters. The usable model set, display names, context/input
  limits, dialect, and base URL are discovered from the
  [models.dev](https://models.dev) catalog and cached for 24 hours under the
  state directory.
- `-list-models` prints the usable catalog without creating a session;
  `-refresh-models`, and `Ctrl-R` in the TUI model picker, force an
  unconditional catalog refresh. There is no hardcoded model list or default.
  The catalog is cached for 24 hours: a fresh cache avoids network access, a
  stale cache is refreshed conditionally, and a failed implicit refresh falls
  back to stale data with a warning; an explicit refresh never falls back.
  Resume and fork resolve from the recorded session values and work offline.
- Per-model thinking effort: `-thinking-effort` / `AHARNESS_THINKING_EFFORT`
  sends top-level `reasoning_effort` on Chat Completions or
  `reasoning.effort` on Responses, with values discovered per model from the
  catalog. Unset omits the field and uses the provider/model default; `default`
  clears an inherited value. The effective value is recorded and inherited on
  resume/fork unless explicitly changed. `-list-models` ends with an `EFFORT`
  column, and the TUI offers a next-run picker on `F2`.
- Display-only thinking text: the TUI shows provider reasoning
  (`reasoning_content`/`reasoning` on Chat, reasoning summaries on Responses) as
  a `thinking` transcript block. It is never logged, projected, replayed, or
  printed in batch mode, so the session log and the one-shot stdout contract are
  unchanged.
- Folded transcript blocks: thinking and tool output are collapsed by default to
  their header, an elision row with the hidden rendered-row count, and the last
  seven rendered rows. The tail keeps the conclusion, failure, or test verdict
  visible. `F3` expands every block, and `F3` again collapses them; the mode is
  sticky across turns and runs, inert in overlays, and not persisted. Expanded
  tool output stays bounded to 40 lines / 4 KiB, and the 64 KiB safety caps and
  truncation markers are unchanged.
- Tool intents: every tool schema declares a shared `intent` string, one short
  sentence explaining why the call is needed. The TUI uses it as the tool block
  title and falls back to the raw argument preview when a replayed or
  non-compliant call omits it. It is display metadata: tools accept it for
  strict argument agreement but never act on it, and it is recorded only as
  part of the model response's tool-call arguments.
- Provider usage detail and an observational cost estimate. `model.Usage`
  keeps input/output totals plus cached-input, cache-write, and
  reasoning-output subsets and a `reported` flag; `run_start` records the
  catalog price snapshot, `model_response` an optional per-call
  `cost_micro_usd`, and `run_end` run/cumulative cost with completeness flags.
  The estimate is never a billing authority. `-require-usage` makes a completed
  buffered response without provider usage a `protocol_error` before any tool
  executes; the default stays compatible with local OpenAI-compatible
  endpoints.
- Six tools — `read_file`, `edit_file`, `search`, `run_command`, `web_search`,
  and `web_fetch` — behind one validated registry with `os.Root` workspace
  containment. `web_search` queries an operator-configured SearXNG instance
  with `format=json` and returns up to ten titles, URLs, and snippets; it does
  not fetch result pages or call a third-party search API. Set
  `-searxng-url`, `AHARNESS_SEARXNG_URL`, or `searxng_url` (HTTP is allowed
  only for loopback instances); unset keeps the tool registered with an
  actionable `unavailable` result. `web_fetch` retrieves one absolute http(s)
  URL and returns plain text only: HTML is stripped of scripts, styles,
  navigation, and boilerplate and extracted with a native Go extractor, while
  text, Markdown, JSON, XML, and CSV pass through. Results are capped at
  60 KiB with `offset` continuation; app shells, bot challenges, and empty
  extractions report `content_unavailable` instead of empty success. The
  builtin client refuses private, loopback, link-local, and metadata addresses
  at dial time, follows at most five same-host redirects, and sends no
  credentials or cookies. JavaScript-heavy pages can use an operator-run
  Crawl4AI endpoint via `-web-fetch-provider crawl4ai` (implied
  `-web-fetch-url`, optional `-web-fetch-key`); `web_timeout` defaults to 30
  seconds (max 120).
- An append-only v2 JSONL session log with `run_start`/`run_end` boundaries,
  exclusive/shared `flock`, torn-final-line repair, and a per-run `final`.
- `-resume <id>` continues a session in place; `-fork <id>` creates a new
  self-contained session with parent metadata. v0 (`v=1`) logs are rejected;
  there is no migration.
- Opt-in `-stream` assistant-text streaming for both dialects in batch mode.
  The TUI and `-ui-json` always stream. The canonical response is the only
  transcript record; deltas are display-only.
- The default interactive TUI: a full-screen transcript, a growing multi-line
  composer (`Shift-Enter`/`Alt-Enter` inserts a newline, `F4` opens
  `$VISUAL`/`$EDITOR`), streaming text, a display-only `thinking` block for
  model reasoning, folded thinking/tool blocks (`F3`), tool activity,
  steering, interrupt/cancel, rich/raw markdown with table overlays,
  a workspace file picker, catalog-backed next-run model and thinking-effort
  pickers, details and help overlays, workspace-contained images, and mouse
  wheel scrolling. The wheel capture also takes the terminal's own selection,
  so an empty transcript and the help overlay point at the escape hatch:
  `Shift`+drag to select and `Shift`+middle-click or `Ctrl+Shift+V` to paste.
  One process owns one session and may execute one run per submitted message.
  Composer slash commands mirror the actions (`/help`, `/model`, `/effort`,
  `/provider`, `/details`, `/files`, `/table`, `/image`, `/rich`/`/raw`,
  `/repaint`, `/refresh`, `/interrupt`, `/cancel`, `/quit`); typing `/` opens
  a filtered completion menu (`Tab` completes, `Enter` runs), and unrecognized
  `/text` is sent as a normal message. See the
  [slash-command reference](docs/commands.md).
- `-batch` forces the one-shot stdout contract. Non-TTY invocations batch
  automatically. `-ui-json` runs the same Bubble Tea program headlessly over a
  versioned JSONL protocol (`frames`, semantic `events`, and deterministic SVG
  `snapshots`), with no PTY required.
- `Esc` interrupts only the in-flight operation, the first `Ctrl-C` does the
  same, the second cancels the run, and `Ctrl-D` quits. `SIGTERM`/`SIGHUP`
  cancel, write terminal events, restore the terminal, and exit 130.
- Opt-in cumulative turn/token budgets, an optional per-run wall clock and
  per-call model timeout, context projection with deterministic compaction, and
  typed stop reasons. Run budgets default to unlimited (no deadline).
- One binary configured by flags, environment, an optional JSON config file
  (`-config` / `AHARNESS_CONFIG`, default `<state-dir>/config.json`), or built-in
  defaults, in that order; no daemon, provider matrix, or profile system. The
  optional `searxng_url` key configures `web_search`; omitting it disables
  search. `web_timeout`, `web_fetch_provider`, `web_fetch_url`, and
  `web_fetch_key` configure `web_fetch`; the provider defaults to `builtin`.
  See [docs/configuration.md](docs/configuration.md).

Out of scope: hooks, model-based summarization, subagents, RAG/embeddings,
MCP/plugins, additional provider dialects, built-in evals, price tracking,
workspace snapshots, built-in isolation/approval, session rotation, or v0 log
migration. The TUI adds no durable event type, schema version, tool, prompt
input, or security boundary.

## Documentation

| Document | What it defines |
|---|---|
| [architecture.md](docs/architecture.md) | System structure, packages, loop and tool contracts, CLI and configuration |
| [configuration.md](docs/configuration.md) | Precedence, the JSON config file, environment variables, and which limits are worth configuring |
| [limits.md](docs/limits.md) | Caps, limits, and timeouts |
| [coding-standard.md](docs/coding-standard.md) | Normative Go practice, dependencies, testing, and §8 review gates |
| [tui.md](docs/tui.md) | Shipped TUI modes, keys, `-ui-json` protocol, caps, and verification status |
| [commands.md](docs/commands.md) | Slash-command reference: syntax, autocomplete, arguments, key equivalents, idle/run behavior, `-ui-json` mapping, and caps |
| [testing-strategy.md](docs/testing-strategy.md) | Unit, integration, fuzz, and manual smoke checks |
| [definition-of-done.md](docs/definition-of-done.md) | Completion checklist for every change |
| [docs/README.md](docs/README.md) | Documentation map and placement rules |
| [AGENTS.md](AGENTS.md) | Instructions for coding agents working in this repository |

## Build and run

The CLI is specified in [architecture.md §9](docs/architecture.md#9-cli-and-configuration).

```sh
go build -trimpath -o bin/aharness ./cmd/aharness

export AHARNESS_PROVIDER_TOKEN=...   # optional for local no-auth endpoints

# Discover the usable opencode-go models, then exit.
./bin/aharness -list-models

# Force an unconditional catalog refresh and replace the 24-hour cache.
./bin/aharness -refresh-models

# Use the model's maximum advertised thinking effort for this run.
./bin/aharness -model deepseek-v4.1-flash -thinking-effort max -task "Fix the failing test"

# Clear an inherited effort while resuming and use the provider default.
./bin/aharness -batch -resume <session-id> -thinking-effort default -task "Now add the regression test"

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
`make install` builds the binary and installs it to `~/.local/bin`
(override with `INSTALL_DIR=/some/dir`).

In TUI and `-ui-json` modes a task is optional: a new session is created
eagerly, the header shows its ID, and the first submitted message supplies the
task. `-batch` keeps the v1 matrix: a new session needs exactly one of `-task`
or `-task-file`, `-fork` requires a task, and `-resume` requires a task only
after a completed run. `-model` (or `AHARNESS_MODEL`) is required for a new
session and must name a usable model in the discovered catalog. `-interactive`
was removed in the TUI cutover; passing it is an unknown-flag error. Session
logs default to `~/.aharness`, outside the workspace; `-state-dir` overrides
it. Session IDs resolve only under `<state>/sessions/<id>.jsonl`.

A new session resolves the model's dialect, base URL, context window, and input
cap from models.dev, with the normalized catalog cached at
`<state>/cache/models.dev/<provider>.json`. `-provider` (or
`AHARNESS_PROVIDER`) defaults to `opencode-go`. `-provider-base-url` (or
`AHARNESS_PROVIDER_BASE_URL`) overrides the discovered base URL and can point
the adapters at a fake or local OpenAI-compatible server; normal runs use the
catalog URL. Resume/fork inherit the recorded provider, model, dialect, base
URL, context/output limits, and thinking effort without a catalog fetch;
recorded run budgets are historical and are not inherited. An explicit model
change re-resolves through the catalog, and an explicit non-default thinking
effort is validated against the effective model (exit 2 if unsupported).

## Isolation

aharness does not sandbox itself and has no approval workflow. Tool calls write
to the real workspace and can use the host network. Run it inside bwrap,
Docker, or a VM whenever the repository or the task is untrusted. Interactive, resumed, and TUI runs extend the exposure of the same process and
tool contract; the TUI adds no approval step or sandbox.

## Contributing

Follow [AGENTS.md](AGENTS.md), the [coding standard](docs/coding-standard.md),
and the [definition of done](docs/definition-of-done.md); every change must pass
`make dod` (the automated definition-of-done checks) before review.
