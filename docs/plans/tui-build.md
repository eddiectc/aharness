# aharness TUI build plan

- **Date:** 2026-11-10
- **Status:** implemented (M0–M5) on 2026-11-10. The shipped surface, protocol,
  keys, and caps are recorded in [tui.md](../tui.md). Automated fake-provider and
  headless checks pass; the real-provider TUI smoke and the real-terminal Kitty/
  tmux image matrix remain pending environment/credentials. This is a post-v1
  feature. v1 M0–M3 remain implemented; the real-provider streamed v1 smoke is
  still the v1 release gate. The later [thinking effort](thinking-effort-build.md)
  feature adds `F2` and optional `thinking_effort` protocol fields; tui.md is
  authoritative for the shipped keys and protocol.
  Two shipped behaviors deviate from the mouse decisions in §1.1, §2.9, and
  T8 below: the scroll wheel is enabled, which captures the terminal's own
  selection and middle-click paste, and the empty transcript carries an
  onboarding tips block. Both are documented in
  [tui.md § Keys and § Terminal safety](../tui.md); the mouse capture is an
  accepted regression of native selection because no DEC mouse mode reports
  only the wheel, and the tips block plus the help overlay point at the
  terminal's `Shift` bypass instead of adding an in-app selection layer.
  The framework decision was revised again on 2026-11-10 after reviewing
  teatest/v2, Glamour v2, and ntcharts v2: **Bubble Tea v2 is the chosen
  application framework**, with Bubbles v2 for widgets, Glamour v2 for
  markdown, ntcharts v2 for charts/images, and `x/vt` for the headless
  agent-visible screen. The tcell v3 + in-house plan is the fallback if the
  framework dependency tree is rejected.
- **Source:** [v1 build plan](v1-build.md). The TUI wraps the v1 runner, tools,
  and v2 session log; it does not reopen v1 scope.
- **Target:** Go 1.27.3, module `aharness`.
- **Architecture:** [architecture.md](../architecture.md) defines the runtime
  contracts. [tui.md](../tui.md) is authoritative for the shipped TUI surface,
  keys, protocol, and caps; this plan records the rationale, milestones, and
  decisions.
- **Testing:** [testing-strategy.md](../testing-strategy.md) plus §4 here.
- **Limits:** [limits.md](../limits.md); the TUI caps proposed in §2.9 land in
  `limits.md` with the milestone that implements them.
- **Definition of done:** [project definition of done](../definition-of-done.md);
  feature criteria in §1.4, §1.4.1, and §1.4.2.

## Decision summary

Bubble Tea v2 is adopted because the requested UI features are no longer
small additions to a plain transcript:

- **Markdown:** Glamour v2 is Bubble Tea v2/Lip Gloss v2-compatible and renders
  headings, emphasis, code, lists, tables, and links to ANSI, which the Bubble
  Tea renderer consumes directly.
- **Widgets:** Bubbles v2 provides table, file picker, list, text input,
  textarea, and viewport components.
- **Charts and images:** ntcharts v2 is Bubble Tea v2-compatible and provides
  charts, `picture` (Kitty graphics plus universal half-block glyph fallback),
  and chart-image rendering. This covers the largest in-house gaps.
- **Agent visibility:** `teatest/v2` exists and is Bubble Tea v2-compatible,
  but it is still a raw-output test helper in `x/exp`; it does not expose a
  cell screen. The production `-ui-json` path therefore runs the same Bubble
  Tea program and feeds its ANSI output to `x/vt`, whose `CellAt` returns the
  rendered content and style for every cell. The SVG `snapshot` renders the
  same cells. `teatest/v2` remains a test helper, not the production headless
  driver.

The cost is a framework-owned loop/view and a large dependency tree. That is
the deliberate trade: feature coverage and agent-visible rendering are worth
more than a hand-built toolkit for this product. The tcell v3 + in-house plan
remains the fallback if the dependency review rejects the tree or if `x/vt`
does not pass its M1 screen-fidelity gate.

v1 is deliberately a one-shot, scriptable CLI. The missing piece for real use
is not another agent capability; it is an interactive front end that makes
streaming, tool activity, steering, and resume visible in one place. The TUI
is that front end. It is optional, it is not a daemon, and it does not change
the loop, the provider adapters, the tools, the prompt, or the durable session
schema.

## 1. Scope

### 1.1 What the TUI owns

| Area | Implementation |
|---|---|
| Invocation | The default when stdin and stdout are terminals. There is no `-tui` flag. `-batch` forces the one-shot CLI; non-TTY invocations are batch automatically. Supports a new session, `-resume`, and `-fork`; `-task`/`-task-file` seed the first message. |
| Terminal | Full-screen alternate-screen UI backed by Bubble Tea v2: raw mode, resize handling, bracketed paste, Unicode/grapheme width, and terminal restoration on every exit path. Mouse reporting is disabled by default. |
| Transcript | Prior runs reconstructed from the v2 log, plus live display-only streaming text, tool calls/results, notices, and run status. Assistant text is rendered as markdown via Glamour v2 (M4); tool output stays preformatted; images and charts use ntcharts v2 `picture` (M5). |
| Input | A composer for the next task or a steering message. Enter submits; while a run is active, a submission is steering; unacknowledged lines are recovered. Modal overlays own input while open: help/details, table, file picker, model picker, and image viewer. |
| Control | `Esc` cancels the current UI context and interrupts the in-flight model call or tool; `Ctrl-C` is the authoritative global interrupt (first interrupts, second cancels the run); `Ctrl-D` quits. `SIGTERM`/`SIGHUP` cancel and restore. |
| Runner integration | A narrow display-event stream from `agent` and a small host interface implemented in `cmd`; no UI logic in `agent`. |
| Automation | `-ui-json` runs the same Bubble Tea program headlessly with a fake input pipe and an `x/vt` emulator, so a coding agent can drive, assert, and visually inspect the UI without a PTY. Frames expose plain lines, style spans, cursor, image metadata, and an SVG snapshot. |
| Safety | All model/tool text and image paths are treated as untrusted terminal input. Escape sequences and control characters never pass through to the terminal; images are workspace-contained, size-capped, and decoded locally. |

### 1.2 What stays the same

- One provider (`opencode-go`), two dialects, and the accepted model registry;
  the TUI selects the same adapter and model inheritance as the CLI. The M4
  model picker selects the model for the next run only; each `run_start` already
  records the effective model/dialect, so there is no schema change and no
  mid-run switch.
- The four tools, their schemas, results, caps, workspace policy, and process
  group cancellation. Markdown rendering, the file picker, the model picker,
  and image/chart viewing are display-only UI features; they add no tool,
  prompt input, or durable event type. The file picker inserts a
  workspace-relative path into the composer; it does not attach file contents.
- The v2 session schema, event types, projection, compaction, retry policy,
  limits, and exit-code mapping for batch runs. The TUI adds no durable event
  type and no schema version.
- One process, one session, one writer. The session log remains the only
  durable truth; UI state is disposable.
- The external-sandbox posture. The TUI adds no approval step, sandbox, or
  secret handling. Interactive use does not make an untrusted workspace safe.
  Image viewing reads workspace-contained files through the same containment
  policy (`os.Root`, `filepath.IsLocal`, no `.git` or symlink components); it
  adds no network fetch and no new write path.
- The one-shot CLI under `-batch` (and automatic non-TTY invocation), including
  stdout/stderr separation, streaming, resume, fork, and exit codes.
  `-interactive` is removed in the TUI cutover; steering is a TUI capability,
  not a stdin-line feature. Batch `SIGINT`/`SIGTERM` remains terminal.

### 1.3 Not in this plan

- No schema change, no TUI state in the log, no replay of display deltas.
- No second application framework. Bubble Tea v2 is the chosen framework;
  tview and termbox are rejected. The tcell v3 + in-house plan is the fallback,
  not a second implementation in the same milestone. No plugin/theme system.
  (A later, harness-wide deviation added an optional JSON config file below
  flags and environment; it is not TUI-specific. See
  [configuration.md](../configuration.md).)
- Rich text and the table/file/model widgets are M4; image viewing is M5. The
  first usable TUI (M2/M3) renders text without markdown styling plus
  help/details overlays. There are no split panes, tabs, session picker, or
  session switching inside the process; a TUI process is bound to one session.
- Mouse reporting is disabled by default. Scroll-wheel/click support is a
  follow-on, not part of the first usable version.
- The model picker selects the model for the next run within the same provider;
  it is not a second session or a mid-run switch. The file picker inserts a
  workspace-relative path into the composer; there is no attachment model.
- Native chart blocks from a model-provided chart spec are a follow-on. M5
  displays agent-generated chart images through ntcharts `picture`; it does
  not add a new model-facing chart DSL to v1.
- No suspend/job control in the first usable version; `Ctrl-Z` is ignored
  unless a later milestone adds tested suspend/resume. No Windows claim.
- No approval UI, built-in isolation, subagents, RAG, MCP, hooks, or model-based
  summarization.
- No remote/web UI and no attach protocol. The TUI is local and uses the
  terminal directly. `-ui-json` is a local stdio automation protocol, not a
  daemon or a remote control channel.
- No machine-readable mode for the one-shot CLI beyond its existing stdout.
  `-ui-json` is the machine-readable TUI surface; scripts that want plain final
  text use `-batch`.

### 1.4 Definition of done

The TUI is complete when the [project definition of
done](../definition-of-done.md) holds and every item below holds.

- [ ] **Default startup.** `aharness -model <id>` in a terminal starts the TUI
      with no task, creates a v2 session, shows the session header, and waits
      for input. There is no `-tui` flag. `-batch` forces the one-shot CLI; a
      non-terminal stdin/stdout runs batch automatically. TUI-only `TERM`
      unset/`dumb` or config errors exit before raw mode with a clear message
      and the right exit code.
- [ ] **Live run.** Submitting a message records a `user_message`, streams
      assistant text before the provider completes, shows tool calls and
      results as they happen, and ends with a visible terminal status and
      updated turn/token usage.
- [ ] **Conversation.** A second message after a completed run starts a second
      `run_start`/`run_end` pair in the same session log. Prior runs are not
      reprinted into stdout. Cumulative limits stay cumulative; the per-run
      wall clock resets.
- [ ] **Resume and fork.** `-resume <id>` reconstructs the transcript from the
      log, including tool activity, interruptions, errors, limits, and pending
      calls, then accepts a new message. `-fork <id>` creates a new
      self-contained session; the parent is unchanged.
- [ ] **Steering.** Enter during a run sends steering text; it is recorded with
      `source=steer` and reaches the next model call at a turn boundary.
      A line not consumed before the run ends returns to the composer; it is
      not silently lost. `-interactive`/stdin steering is gone.
- [ ] **Interrupt/quit.** `Esc` while a model call or tool is active interrupts
      only that operation and the run continues; it never quits. First `Ctrl-C`
      does the same. Second `Ctrl-C` cancels the run and returns to idle.
      `Ctrl-D` while idle quits with terminal restoration. `SIGTERM`/`SIGHUP`
      cancel, write `final`/`run_end`, restore, and exit 130. No child process
      or process group survives.
- [ ] **Key safety.** Bare `Esc` is recognized only after the 50 ms escape
      timeout; Option/Alt sequences such as Option-`[` and Option-`]` are
      decoded as modifier sequences and never interrupt the run. The behavior is
      covered by raw-byte tests through the headless input pipe, not only by
      synthetic key events.
- [ ] **Terminal safety.** Alternate screen, cursor, bracketed paste, and raw
      mode are restored on normal exit, config/startup failure after entry,
      run error, signal, and a recovered UI panic. `stty -a` is unchanged after
      exit. Resize reflows without corrupting the screen; 80x24 and 200x60 both
      work. Bubble Tea's `Program` is the single terminal owner; the app does
      not write terminal escapes.
- [ ] **Untrusted text.** Assistant text, tool arguments, tool output, error
      messages, session paths, markdown link targets, and image paths are
      sanitized. ESC/CSI/OSC/DCS sequences, C0/C1 controls, and bidi overrides
      cannot reach the terminal as controls. Raw HTML in model output is never
      interpreted. A pasted or generated OSC 52 cannot set the clipboard.
- [ ] **Unicode.** UTF-8 input/output, CJK wide characters, combining marks,
      and common emoji do not misalign the layout; invalid UTF-8 is repaired
      once at the boundary.
- [ ] **Durability.** The log contains one `session_start`, one `run_start` per
      submitted message, exactly one `final`/`run_end` per initialized run, and
      no display deltas. The store is closed and synced on exit; the work
      directory is removed. A concurrent `-resume` fails with exit 2.
- [ ] **Batch CLI.** `-batch` preserves one-shot streaming, resume, fork, exit
      codes, stdout text, and stderr progress. Automatic non-TTY batch works
      without a TTY. `-interactive` is removed; passing it is a clear usage
      error, and the stdin steering reader is deleted.
- [ ] **Machine-readable UI.** `-ui-json` runs the same Bubble Tea program
      without a PTY through a fake input pipe and an `x/vt` emulator, emits
      deterministic versioned JSONL frames and events, and accepts JSONL
      text/key/paste/resize/wait/frame/snapshot/quit commands. Frames include
      plain lines, style spans, cursor state, overlay kind, and image metadata;
      `snapshot` returns a deterministic SVG of the same frame. A coding agent
      can start a session, submit a message, observe streaming/tool/run state,
      resize, interrupt with Esc or `ctrl-c`, inspect styles and images, and
      quit using only pipes. Protocol errors are clear and the session log
      remains the durable record.
- [ ] **Gates.** `goimports`, `go vet`, `golangci-lint`, `go test -race
      -count=1`, `go build -trimpath`, `govulncheck`, and `go mod tidy`/`verify`
      pass. TUI and `-ui-json` integration tests are hermetic and use the
      Bubble Tea headless harness plus `x/vt`; the manual terminal matrix and
      the real-provider TUI smoke are recorded.

### 1.4.1 Definition of done (rich content, M4)

The rich-content milestone is complete when every item below holds in addition
to §1.4.

- [ ] **Markdown.** Assistant text is rendered as markdown by default via
      Glamour v2: headings, paragraphs, emphasis, strong, inline code, fenced
      code, blockquotes, ordered/unordered/task lists, rules, tables, and
      links. Raw HTML is never interpreted; Glamour's sanitization and the
      TUI sanitizer keep it inert. Invalid or over-deep markdown falls back to
      plain text with a notice. `Ctrl-R` toggles rich/raw rendering. Tool
      output remains preformatted and is not parsed as markdown.
- [ ] **Table widget.** Inline markdown tables are rendered by Glamour.
      `Ctrl-T` opens the most recent table in a Bubbles table overlay with a
      header row, column width caps, and horizontal scrolling. Wide tables
      never corrupt the transcript or the composer. Tool output remains
      preformatted unless a later tool-detail view explicitly opts into the
      table widget.
- [ ] **File picker.** `Ctrl-O` opens a Bubbles file picker constrained to the
      workspace root. It lists directories/files with a filter, navigates with
      arrows/Enter, rejects `.git` components and symlink escapes, reads no
      file contents, and inserts a workspace-relative path into the composer on
      selection. Names are sanitized for display; control characters are
      rejected with a notice. `Esc` closes it without changing the composer.
- [ ] **Model picker.** `Ctrl-P` opens the accepted-model registry in a Bubbles
      list, marking the current effective model. Selecting a model applies to
      the next submitted run; the picker is disabled while a run is active. The
      next `run_start` records the selected model/dialect; the session schema is
      unchanged; resume inherits the latest run's model; there is no mid-run
      switch.
- [ ] **Overlays and focus.** Help/details, table (`Ctrl-T`), file picker, and
      model picker overlays share one focus manager (the image viewer joins in
      M5). `Esc` closes the top overlay; `Tab`/`Shift-Tab` move focus; the
      composer is unchanged underneath. The status bar shows the effective
      model, run state, and overlay hint.
- [ ] **Agent-visible styles.** `-ui-json` frames expose coalesced style spans
      (foreground, background, attributes) and cursor state from the `x/vt`
      screen for the real Bubble Tea renderer output. The `snapshot` command
      returns a deterministic SVG of the same frame with markdown styles.
      Frames contain no ANSI; the agent can assert headings, emphasis, code,
      table rules, and link text without screen-scraping escape sequences.
- [ ] **Caps and docs.** Markdown/table/list/frame caps in §2.9 are enforced
      and recorded in `limits.md`; the protocol schema and key table are
      documented; §8 gates pass.

### 1.4.2 Definition of done (images and charts, M5)

The image/chart milestone is complete when every item below holds in addition
to §1.4 and §1.4.1.

- [ ] **Image blocks.** Markdown image syntax (`![alt](path)`) and the image
      viewer accept workspace-contained PNG/JPEG/GIF files only. Paths are
      resolved with the workspace containment policy; `.git`, symlink escapes,
      absolute paths outside the workspace, network URLs, and SVG/XML are
      rejected with a notice.
- [ ] **Caps and decoding.** File size, dimensions, decoded pixels, images per
      transcript, and cache size are capped (§2.9). Decode errors, oversized
      images, and unsupported formats render a placeholder with alt text and
      path; they never crash or block the run.
- [ ] **Rendering.** ntcharts v2 `picture` renders the image: Kitty graphics
      when available, universal half-block glyph fallback otherwise. The
      transcript shows a bounded glyph thumbnail; `Ctrl-G` opens a
      fit-to-viewport viewer (Enter toggles fit/actual size) using Kitty or the
      glyph fallback. `Esc` closes it and restores the cells underneath.
      Scrolling the transcript never leaves a stale image on screen.
- [ ] **Charts.** Agent-generated chart images (for example a PNG produced by a
      tool) render through the same `picture` path. ntcharts chart widgets may
      be used for TUI-owned charts (for example token usage), but a
      model-provided chart DSL is not part of M5; that is a follow-on.
- [ ] **Protocols and fallback.** Capability detection is best-effort and has
      an explicit override (`AHARNESS_IMAGE_PROTOCOL=auto|kitty|glyph|none`);
      the glyph fallback works on any color terminal and a placeholder is used
      otherwise. Kitty image writes are cleared on close, resize, and exit.
      No clipboard/OSC 52 access and no raw image bytes in terminal text cells.
- [ ] **Agent visibility.** `-ui-json` forces glyph mode and includes image
      metadata (path, alt, rect, protocol) and the thumbnail cells, but not raw
      image bytes. The `snapshot` SVG renders the same glyph cells (or a
      placeholder with the path) so a coding agent can visually inspect the
      rendered UI and can open the source file directly.
- [ ] **Manual matrix.** Kitty and glyph paths are exercised on a real terminal
      at 80x24 and 200x60, including tmux; image close/resize/scroll and
      terminal restoration are recorded. Caps and the image protocol contract
      are documented; §8 gates pass.

## 2. Contract deltas

The TUI is a new front end over existing contracts. The deltas are limited to
the CLI surface, an internal display/history API, the Bubble Tea app/view
layer, and the display-only rich-content pipeline. The session schema and the
durable event set do not change.

### 2.1 Invocation and mode selection

The TUI is the default when `aharness` is attached to a terminal. There is no
`-tui` flag. Mode selection happens after flag parsing and before task
validation or session creation:

| Condition | Mode |
|---|---|
| `-version` | Print build info and exit 0. |
| `-ui-json` | Headless JSON UI on stdio; no TTY required; mutually exclusive with `-batch`. |
| `-batch` | One-shot CLI; the v1 non-interactive task/stream/exit contract. |
| stdin and stdout are terminals | TUI (default). |
| either stdin or stdout is not a terminal | Batch automatically. |

`-batch` is the deliberate opt-out: a TTY user must still be able to request
the v1 one-shot stdout contract, and making the TUI the default must not make
batch mode unreachable. Non-TTY invocations need no flag because a TUI cannot
own a pipe.

`-interactive` is removed in this cutover. It is not an alias or a no-op:
passing it is a normal unknown-flag error. The TUI owns interactive input and
signals; batch mode is one-shot and non-interactive. The stdin steering reader
(`cmd/steering.go`) and its tests are deleted. The runner's steering channel
remains for TUI submissions. This is a deliberate breaking CLI change,
recorded in the usage text and README with the release.

Examples:

```sh
# Default: TUI, wait for the first message, then continue conversationally.
aharness -model deepseek-v4.1-flash

# Default TUI, seeded with a first message.
aharness -model deepseek-v4.1-flash -task "Fix the failing test"

# One-shot batch, even on a terminal: final or streamed text on stdout.
aharness -batch -task "Fix the failing test" -model deepseek-v4.1-flash
aharness -batch -resume <session-id> -task "Now run the tests"
aharness -batch -fork <session-id> -task "Try a different approach"

# Headless TUI for a coding agent; commands and frames are JSONL.
aharness -ui-json -model deepseek-v4.1-flash -ui-size 100x30 < commands.jsonl
```

Flags:

| Flag | Meaning |
|---|---|
| `-batch` | Force one-shot mode. Works with or without a TTY. No TUI, no raw mode, no steering. |
| `-ui-json` | Run the TUI state machine headlessly on stdio (`-ui-json` protocol in §2.7). No TTY/`TERM` requirement; mutually exclusive with `-batch`. |
| `-ui-size WxH` | Initial headless frame size, default `100x30`, valid `20x5`–`500x200`; only with `-ui-json`. |

Validation:

- TUI and `-ui-json` follow the TUI task rules: a task is optional for a new
  session, resume, and fork; the first submitted message supplies it, and a
  seeded `-task`/`-task-file` auto-submits after the first frame. `-fork` does
  not require a task at parse time in these modes.
- `-batch` follows the v1 task matrix: a new session needs exactly one task
  source, `-fork` requires a task, and `-resume` requires a task only after a
  completed run.
- TUI and `-ui-json` always stream assistant text; `-stream` is accepted as
  redundant, and an explicit `-stream=false` is a config error that points at
  `-batch` for one-shot use. `-batch` retains v1 `-stream` behavior.
- `-ui-size` with any mode other than `-ui-json` is a config error.
- `-resume`/`-fork` still validate the session, workspace, prompt/toolset
  version, base URL, locks, and inherited limits before the terminal is put
  into raw mode. A session error is a normal stderr error and exit 2/1.
- The TUI requires `stdin` and `stdout` to be terminals. `stderr` may be a file
  or pipe; human progress is not written to it while the screen is active.
  `TERM` must be set and not `dumb`. The TTY/`TERM` check runs before session
  creation or lock acquisition, so an unsupported TUI invocation leaves no new
  session state. Bubble Tea screen initialization happens after the session is
  opened (startup step 4); a failure there is reported after Bubble Tea
  restores or never enters the screen and leaves a valid `session_start`-only
  log. Unix-like hosts only, matching v1's `flock`/process-group assumptions.
- `-ui-json` does not require a TTY and ignores `TERM`; it writes protocol
  JSONL to stdout and diagnostics to stderr. It runs the same Bubble Tea
  program with a fake input pipe and an `x/vt` emulator, so it does not enter
  raw mode or an alternate screen but still exercises the real view/renderer
  output, width handling, and clipping.
- A new session is created eagerly in TUI/`-ui-json` mode, before the first
  message, so the session ID and trace path can be shown. Quitting before the
  first message leaves a valid `session_start`-only log.
- TUI-typed tasks and `-ui-json` submissions use the new `user_message` source
  `tui`; seeded `-task`/`-task-file` keep their existing sources; steering
  keeps `steer`. Internal hints keep `interrupt-hint`, `continuation-hint`, and
  `stuck-hint`. There is no stdin-line steering source.
- The TUI is not scriptable: it does not print the final assistant text to
  stdout. On exit it restores the screen and prints the session ID, trace path,
  and a short status to stderr. The log and `-resume` remain the durable output.
  `-ui-json` is the scriptable UI surface; `-batch` is the plain-text
  one-shot surface.

Exit codes:

| Situation | Exit |
|---|---|
| TUI/`-ui-json`: clean quit while idle or EOF | 0 |
| TUI/`-ui-json`: quit while a run is active (`Ctrl-D`, `SIGTERM`/`SIGHUP`) | 130 |
| TUI/`-ui-json`: config, TTY/`TERM`, or protocol usage error | 2 |
| TUI/`-ui-json`: terminal setup, sync/close, protocol I/O, or `internal_error` after entry | 1 |
| `-version` | 0, before mode selection |
| `-batch` | v1 exit-code mapping from the run status (`completed` 0, limits 2, provider 3, protocol/stuck 4, interrupted 130, internal 1) |

A second `Ctrl-C` is not an exit: it cancels the run and returns to idle. The
last run's terminal status is shown in the UI or emitted as a `-ui-json` event,
but it is not the interactive process exit status. `-batch` remains the way to
get the run status as the process exit code.

### 2.2 Session and run model

The v2 schema already separates a session from a run. The TUI adds no schema
change, but it broadens the process/run relationship:

- One TUI process owns one session and holds its exclusive `flock` for the
  process lifetime. The session ID never changes inside the process.
- Each submitted message while idle starts exactly one `Run`: reset
  `<state>/work/<session-id>`, append `run_start` with `ResumedFromSeq` set to
  the current last sequence (zero for the first run in a new session), append
  the task `user_message`, run the loop, append exactly one `final` and one
  `run_end`, sync, remove the work directory. The process stays open.
- A run that ends `completed`, `max_turns`, `token_limit`, `time_limit`,
  `provider_error`, `protocol_error`, `stuck`, or `interrupted` returns the UI
  to idle. The next message starts a new run under the same session. An
  `internal_error` is fatal to the TUI because the store or an invariant is
  untrustworthy; the TUI restores the terminal and exits 1.
- Turns, tokens, and the prompt/toolset/workspace identity remain
  session-cumulative/inherited as in v1. Model/dialect are inherited from the
  latest `run_start` but are per-run: the M4 model picker may select a different
  accepted model for the next run, and that run's `run_start` records it. The
  `session_start` model remains the session's initial model. The host creates
  the adapter for the selected model per run; there is no mid-run switch and no
  schema change. The per-run wall clock resets on each message. The UI must show
  the effective model and cumulative budget; it must not imply a fresh token
  budget.
- Markdown rendering, the table/file/model overlays, and image/chart viewing
  are display-only. They do not change the durable event set, the model
  projection, or the one-writer contract. The file picker inserts text into the
  composer; the model picker changes the next run's model; images are read only
  for display and never become model input unless the user explicitly
  references their path in a message.
- The store's one-writer contract is unchanged. The TUI never reads
  `Store.Events` concurrently with a run. It reconstructs history at startup,
  before any run; during runs it consumes display events. The log is re-read
  only by a new process, never by a live writer.
- Unpaired tool calls are repaired by the next `Run` before its `run_start`,
  exactly as in v1. The display stream reports the repair results so the UI
  can turn pending calls into `canceled` entries.
- The lock is held across runs. A concurrent `-resume` of the same session
  returns the existing busy-session config error (exit 2) with the trace path.

This is a deliberate semantic delta: in TUI and `-ui-json` modes, a run is one
interactive exchange, not one process invocation. Batch mode (explicit
`-batch` or automatic non-TTY batch) still performs exactly one run per
process.

### 2.3 Display contract

The runner currently exposes only `Config.EmitText` and the final `Result`.
That is enough for stdout streaming but not for a UI that must show tool
activity, retries, limits, and history. Add a display-only event stream; it is
never durable state.

```go
// internal/agent/display.go

type DisplayEvent interface{ isDisplayEvent() }

type RunStarted struct {
	Turn           int
	ResumedFromSeq uint64
	Model          string
	Dialect        string
}

type ModelStarted struct {
	Turn    int
	Attempt int
}

type TextDelta struct {
	Text string
}

type ResponseCompleted struct {
	Turn         int
	Text         string
	ToolCalls    []model.ToolCall
	FinishReason model.FinishReason
	Usage        model.Usage
	Latency      time.Duration
	Streamed     bool
}

type ToolStarted struct {
	Turn int
	Call model.ToolCall
}

type ToolCompleted struct {
	Turn      int
	CallID    string
	Name      string
	OK        bool
	Code      tools.Code
	Output    string
	Error     string
	ExitCode  int
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
}

type MessageRecorded struct {
	Turn   int
	Source string
	Text   string
}

type Notice struct {
	Turn int
	Kind NoticeKind
	Text string
}

type RunFinished struct {
	Result   Result
	Reason   string
	Duration time.Duration
}
```

`Config.EmitText` is replaced by:

```go
// Display receives display-only progress on the runner goroutine. It must not
// block, call the runner, or write the session log.
Display func(DisplayEvent) error
```

Emission points and ordering:

| Event | Emitted after | Notes |
|---|---|---|
| `RunStarted` | `run_start` is appended and synced | Also after pending-call repair; carries model/dialect. |
| `ModelStarted` | Before each transport attempt | Shows "model call in progress". |
| `TextDelta` | Each non-empty streamed delta, or the fallback response text | Display-only; never logged as a delta. |
| `ResponseCompleted` | `model_response` is appended | Canonical text and tool calls; the UI replaces delta text with this. |
| `ToolStarted` | `model_response` is durable, immediately before execution | Not durable; carries the call arguments. |
| `ToolCompleted` | `tool_result` is appended and synced | Canonical result, including canceled/panic results. |
| `MessageRecorded` | A task/steer/hint `user_message` is appended and synced | Used as the steering acknowledgment and for hint notices. |
| `Notice` | A retry, compaction, limit, interrupt, repair, or warning is recorded | Text is sanitized/capped; not a new durable type. |
| `RunFinished` | `final` and `run_end` are appended and synced | Carries status, reason, duration, and cumulative usage. |

Rules:

- The callback runs on the runner goroutine. It must return promptly and must
  not call back into the runner, the store, or the tools. The Bubble Tea
  adapter sends each event as a `tea.Msg` through a bounded channel; the app
  processes them in order. After shutdown the adapter drains and drops display
  events rather than deadlocking the runner. Events are delivered in emission
  order, especially `MessageRecorded` before `RunFinished` for steering
  acknowledgment.
- A non-nil display error before `RunFinished` is an internal error: record an
  `error` event when possible, stop the run, and let the caller restore the
  terminal. A display failure during a tool round follows the existing
  cancellation path: pair the current and unstarted calls with `canceled`
  before stopping. A display error on `RunFinished` is returned to the caller
  but does not rewrite the already-durable terminal status.
- Display events carry no provider token and no unsanitized terminal control
  sequences. Tool arguments and output are the same capped strings the log
  records; the UI sanitizes again before rendering.
- The UI keys live tool items by `CallID`. `ResponseCompleted` creates one
  pending item per call; `ToolStarted` marks the matching item running (and
  supplies arguments if it did not already have them); `ToolCompleted` fills
  the result. It must not append a second item for the same call.
- `RunStarted` carries the effective model/dialect recorded in `run_start`; the
  UI updates the header/status immediately. A model-picker selection updates
  the next-run label before the run starts; the run's model is fixed once
  `RunStarted` arrives.
- For a buffered or fallback response, emit the single `TextDelta` before
  `ResponseCompleted`; the UI settles its streaming item from the canonical
  response. Never emit the same response text twice in one turn.
- The CLI replaces its `EmitText` adapter with a display adapter that handles
  `TextDelta` and ignores other events. Its stdout behavior must not change.
  Streaming still emits a response once: deltas when streamed, the fallback
  text once, and no reprint after deltas.
- Assistant text is markdown source, not pre-rendered ANSI. The UI passes it to
  Glamour at render time; raw HTML is never interpreted. Tool output is
  rendered as preformatted text and is not parsed as markdown. Display events
  never carry ANSI or image bytes.
- Images are display-only markdown blocks. The runner emits the assistant text;
  the UI resolves image links through the workspace and renders them with
  ntcharts `picture`. No new display event, durable event, or tool is added.
- `agent` does not import `internal/tui`. The event types live in `agent`
  because the runner produces them; the UI consumes them.

### 2.4 Transcript reconstruction

The TUI must show prior runs from the log without replaying the model
projection. Add a pure function in `agent`:

```go
// internal/agent/history.go

type HistoryKind string

const (
	HistoryRun       HistoryKind = "run"
	HistoryUser      HistoryKind = "user"
	HistoryAssistant HistoryKind = "assistant"
	HistoryTool      HistoryKind = "tool"
	HistoryNotice    HistoryKind = "notice"
)

type HistoryItem struct {
	Kind      HistoryKind
	Run       int
	Turn      int
	Source    string
	Text      string
	CallID    string
	Name      string
	Arguments json.RawMessage
	Pending   bool
	OK        bool
	Code      string
	Output    string
	Error     string
	ExitCode  int
	Truncated bool
	TimedOut  bool
	Status    StopReason
	Reason    string
	Model     string
	Dialect   string
	Duration  time.Duration
	Turns     int
	Usage     model.Usage
}

func History(events []session.Event) ([]HistoryItem, error)
```

Behavior:

- Order by `seq` as stored. Ignore unknown event types and unknown fields
  within v2, matching the reader.
- `run_start` appends a `HistoryRun`; `run_end` fills its status, reason,
  duration, turns, and usage. A run without `run_end` remains visibly
  incomplete. The `HistoryRun` carries the effective model/dialect from
  `run_start`; the header and details overlay show it, and a model-picker
  change appears on the next run without a schema change.
- Assistant text is markdown source; `History` stores it as capped text and
  does not parse or render it. The view applies Glamour and the image resolver
  at display time, so resume uses the same pipeline as live output. A missing
  or rejected image link becomes a notice in the view; it does not make history
  unreadable.
- `user_message` appends a user item except internal hints. `interrupt-hint`,
  `continuation-hint`, and `stuck-hint` become dim notices. `steer` shows as a
  user message with its source.
- `model_response` appends the assistant text (if non-empty) and one pending
  tool item per call, in response order. The arguments are the logged raw
  JSON, capped for display.
- `tool_result` fills the pending item matched by `CallID`. A result with no
  matching call is retained as a standalone tool item with a visible anomaly;
  it is not discarded. Missing or duplicate call IDs (possible when a
  `model_response` was recorded before the loop rejected it) are retained as
  separate anomalous items; display pairing is best-effort and never drops a
  recorded result. A repaired call from resume becomes a canceled result with
  the outcome-unknown message.
- `interrupt`, `error`, `limit`, `compaction`, `session_repair`, and
  `model_retry` become notices. Their payload text is the capped log text.
- An `interrupt` or `error` carrying `partial_text` appends a display-only
  partial assistant notice with the capped text and byte count, marked
  interrupted or failed. The live UI keeps its already-emitted deltas and
  labels them the same way. Partial text is never projected as a response.
- A `final` event does not append its text again; the assistant text was
  already appended from `model_response`. The run header carries the terminal
  status. This avoids duplicating the last answer after resume.
- `session_start` with `parent_session`/`parent_seq` yields a "forked from"
  notice for the header.
- Known payload decode failures return an error with the event type and `seq`.
  The caller treats this as an unreadable session state (exit 1) rather than
  rendering a half-transcript.
- History is a snapshot. The host calls it at startup before any run; during a
  run the TUI uses display events. It never reads the store concurrently with
  a writer.
- History is display-only and lossy: it shows capped text, drops debug
  `model_request` payloads, and does not expose full environments or prompts.
  The raw log remains the complete record.

### 2.5 Input, steering, and signals

#### Keybinding basis

There is no single cross-terminal keyboard standard, but three conventions
are consistent enough to build on:

- **Esc means cancel/dismiss.** Apple's HIG defines Esc as cancelling the
  current action or process, and `cancelAction` as Escape with no modifiers.
  GNOME's HIG says Esc activates the cancel action in dialogs.
- **Ctrl-C means interrupt.** Unix terminals deliver it as SIGINT. A raw-mode
  TUI cannot rely on the signal, so it maps the key to the same operation
  interrupt and commonly uses a second press to cancel/exit.
- **Agent TUIs often split the two.** Claude Code documents Esc as interrupting
  a response mid-turn and Ctrl-C as cancelling input or exiting on an empty
  prompt.

The technical complication is that Esc is also the prefix terminals use for
Alt/Option and escape sequences. A bare Esc is ambiguous until an escape
timeout expires, and Option-`[`/Option-`]` have been observed as accidental
interrupts in terminal agents. Bubble Tea v2 uses ultraviolet's input reader,
which waits 50 ms for a possible sequence and reports a bare Esc as `esc`;
recognized Option/Alt sequences arrive as modified key events. The app maps
only a bare `esc` to the contextual cancel and never infers Esc from a raw
byte. We do not write a second input decoder.

Sources: [Apple keyboard HIG][apple-kb], [Apple `cancelAction`][apple-cancel],
[GNOME dialogs][gnome-dialogs], [Claude Code shortcuts][claude-keys],
[Esc/Alt interrupt issue][esc-alt].

[apple-kb]: https://developer.apple.com/design/human-interface-guidelines/keyboards
[apple-cancel]: https://developer.apple.com/documentation/swiftui/keyboardshortcut/cancelaction
[gnome-dialogs]: https://developer.gnome.org/hig/patterns/feedback/dialogs.html
[claude-keys]: https://support.claude.com/en/articles/14553413-claude-code-cheatsheet
[esc-alt]: https://github.com/NVIDIA-NeMo/labs-OO-Agents/issues/195

The composer is active at all times. Its meaning depends on the run state:

| Key | Idle | Run active |
|---|---|---|
| `Enter` | Submit the composer as a new run task | Submit the composer as steering |
| `Esc` | Close an overlay, then clear a non-empty composer; never quits | Close an overlay if one is open; otherwise interrupt the in-flight operation. Never cancels the run or quits. |
| `Ctrl-C` | Clear a non-empty composer; otherwise show a quit hint (use `Ctrl-D`) | First: interrupt the in-flight operation; second: cancel the run |
| `Ctrl-D` | Quit cleanly (0) | Cancel the run and quit (130) |
| `Ctrl-L` | Full repaint | Full repaint |
| `Ctrl-U` | Clear the composer | Clear the composer |
| `Ctrl-A`/`Ctrl-E` | Composer start/end | Composer start/end |
| `Ctrl-W`/`Ctrl-K` | Delete previous word / to end | Delete previous word / to end |
| `PgUp`/`PgDn` | Scroll the transcript | Scroll the transcript |
| `Up`/`Down` | Previous/next input history | Previous/next input history |
| Printable/UTF-8 | Insert at the cursor | Insert at the cursor |
| Bracketed paste | Insert sanitized text | Insert sanitized text |

Key rules:

- `Esc` and the first `Ctrl-C` call the same operation controller. `Esc` is
  the contextual cancel; `Ctrl-C` is the global terminal interrupt. Only
  `Ctrl-C` can cancel the run (second press) or lead to a hard exit (`Ctrl-D`).
- Bubble Tea/ultraviolet reports a bare Esc as `esc`; modified keys
  (Alt/Option, Shift, Ctrl) are distinct events and never interrupt. The app
  does not treat an unknown sequence as Esc. Raw Option-`[`/Option-`]`
  regression tests cover this through the headless input pipe.
- `Ctrl-C` in raw mode is a Bubble Tea key because `ISIG` is cleared; `SIGINT`
  from `kill -INT` or a terminal that still sends signals is mapped to the same
  app-level interrupt event.
- Overlays (help/details/table/file/model/image) own Esc while open. `Ctrl-C`
  and `Ctrl-D` remain global even with an overlay open; the run continues
  underneath.

Widget and overlay keys (M4/M5; the base composer table above still applies):

| Key | Context | Action |
|---|---|---|
| `Ctrl-P` | idle | Open the model picker for the next run. Disabled during a run with a notice. |
| `Ctrl-O` | idle | Open the workspace file picker; selection inserts a relative path into the composer. |
| `Ctrl-R` | any | Toggle rich (markdown) / raw text rendering for assistant items. |
| `Ctrl-T` | any | Open the most recent markdown table in a table overlay; `Esc` closes it. |
| `Ctrl-G` | any | Open the image viewer for the most recent image block, or close it. |
| `Tab`/`Shift-Tab` | overlay | Move focus between the overlay's list/table/viewport and its actions. |
| `Up`/`Down` | overlay | Move the selection or scroll a list. |
| `Left`/`Right` | table | Scroll the table horizontally when focused. |
| `PgUp`/`PgDn` | table/viewer | Page the table or image list. |
| `Enter` | overlay | Activate the selected item; in the image viewer, toggle fit/actual size. |
| `Esc` | overlay | Close the top overlay and restore the underlying frame. |

In the composer, `Tab` inserts a tab (rendered as four spaces); in an overlay
it moves focus. The model picker is disabled while a run is active because the
run's model is fixed at `run_start`; the selection applies to the next
submitted message. The file picker is idle-only so it cannot race a steering
submission. The table overlay is available whenever the transcript contains a
table. The image viewer is available whenever the transcript contains an image
block.

Steering details:

- While a run is active, submissions are sent on the run's steering channel.
  The runner consumes them at turn boundaries and records them as
  `user_message` with `source=steer`.
- The TUI composer (or `-ui-json` `text` + `enter`) is the only steering
  source. `-interactive` and stdin-line steering are removed; batch mode has no
  steering and remains non-interactive.
- The UI shows a submitted steering message as "queued". A
  `MessageRecorded{Source: "steer", Text: ...}` display event acknowledges it;
  the UI marks it sent. The queue is FIFO and matched by text because the
  existing steering channel carries strings and the durable payload has no
  client-side correlation ID.
- If the run ends before a queued line is acknowledged, the UI removes it from
  the run queue and places it back in the composer with a notice. It is not
  lost and is not silently turned into a second run.
- EOF does not exist in a TUI; `Ctrl-D` is the quit key. `-ui-json` treats EOF
  on stdin as a clean quit. Empty and whitespace-only submissions are ignored.
- The TUI steering message cap is 64 KiB, matching the v1 steering line cap.
  A message can contain newlines; it is recorded as one `user_message`, not
  split into CLI lines. Longer messages are rejected with a visible notice and
  the run continues.

Interruption and signals:

- While a run is active, a first `Esc` or `Ctrl-C` with no operation in flight
  does nothing (the UI may show a brief notice). A first one during a model
  call or tool cancels only that operation; the run continues with the
  existing interrupt hint/tool pairing rules. In idle, these keys follow the
  composer/overlay behavior in the table.
- `SIGTERM` and `SIGHUP` cancel the run, wait for `final`/`run_end` and store
  sync, quit the Bubble Tea program, and exit 130. Bubble Tea handles
  `SIGWINCH`/resize and terminal restoration; the app does not write escapes.
- The signal handler performs no I/O and mutates no session state; it only
  cancels a context or sends on a bounded channel. The UI goroutine quits the
  program. This follows the v1 signal rule.
- Bubble Tea's input reader is the input loop. The app exits on program quit or
  context cancellation; it does not close stdin to unblock a read and does not
  mutate run state from the input goroutine.
- A second `Ctrl-C` cancels the run but leaves the TUI alive, because the
  session is intact and can accept another message. Only `Ctrl-D` or a signal
  exits the process while idle. An `internal_error` is the exception: the TUI
  exits 1 after restoring.
- `-ui-json` has no real terminal or signal-driven UI: it consumes JSONL
  commands, runs the Bubble Tea program against the fake input/output and
  `x/vt`, cancels on `ctrl-c` commands, and exits cleanly on `quit`/EOF.
  `SIGTERM` still cancels the active run and exits 130.
- `Ctrl-Z` is ignored in the first usable version. The process does not
  suspend. If suspend support lands later, it must restore the terminal before
  `SIGTSTP`, re-enter raw/alternate mode on `SIGCONT`, and repaint from the
  model.

### 2.6 Rendering, layout, and terminal safety

Layout (the exact glyphs may change; the regions are the contract):

```text
┌ aharness  deepseek-v4.1-flash  session 0190…  ~/src/repo ┐
│ run 4 · completed · 6 turns · 18.2k tokens               │
│                                                           │
│ user  Fix the failing test and chart the results          │
│ assistant ## Results                                      │
│           All package tests pass.                          │
│           | package | time |                               │
│           |---------|------|                               │
│           | agent   | 1.2s |                               │
│           ![timing chart](charts/timing.png)               │
│           [image 640x480 · Ctrl-G view]                    │
│ tool  run_command  go test ./...  ok · 2.1s                │
│       ok  ./...                                            │
│                                                           │
│ > add a regression test_                                  │
└ turns 4/30 · idle · rich · Ctrl-P model · Ctrl-O file · Ctrl-D quit ┘
```

M2/M3 use the same regions with unstyled text. M4 adds Glamour markdown,
Bubbles table/file/model overlays, and rich style spans in `-ui-json`; M5 adds
the ntcharts image thumbnail and viewer.

Rendering rules:

- **Framework.** The TUI is a Bubble Tea v2 `tea.Model`. `View()` returns a
  `tea.View` with the composed content, alternate screen enabled, mouse mode
  off, bracketed paste enabled, and the cursor at the composer. Bubble Tea owns
  the program loop, renderer, raw mode, alternate screen, resize, paste, and
  terminal restoration.
- **Composition.** Lip Gloss v2 composes the header, transcript, composer, and
  status bar. Glamour v2 renders assistant markdown to ANSI; Bubbles v2
  provides the table, file picker, list, text input, textarea, and viewport;
  ntcharts v2 `picture` renders images and charts. The app composes their
  `View()` strings; it does not reimplement their layout.
- **Headless screen.** `-ui-json` runs the same program with `tea.WithInput`,
  `tea.WithOutput`, `tea.WithWindowSize`, and `tea.WithoutSignals`. The output
  writer feeds `x/vt` (`vt.NewSafeEmulator(w,h)`); `CellAt` returns an
  ultraviolet `Cell` with `Content`, `Style`, and `Width`, and
  `CursorPosition`/`CursorHidden`/`CursorStyle`/`IsAltScreen` expose the
  terminal state. The headless projector maps the ultraviolet style to the
  protocol's `fg`/`bg`/`attrs`. Frames and the SVG snapshot come from this
  screen, not from a separate view mock.
- **ANSI boundary.** The app never writes terminal escapes directly. Bubble
  Tea's renderer, Lip Gloss, Glamour, Bubbles, and ntcharts produce the only
  ANSI; untrusted text is sanitized before it reaches them. `x/vt` parses the
  trusted renderer output in headless mode.
- **Diffing and coalescing.** Bubble Tea's renderer diffs and batches output.
  The app coalesces streaming text and elapsed-time ticks; state changes flush
  immediately. `-ui-json` has no wall-clock timer; it snapshots after the
  renderer output has been written and the emulator has processed it.
- **Layout.** Header (model, session, workspace), transcript viewport,
  composer, status bar. Overlays (help/details, table, file picker, model
  picker, image viewer) are modal regions over the transcript; the underlying
  frame remains visible and is restored on close. The composer is always
  present except in a full-screen image/table overlay that explicitly owns the
  screen.
- **Transcript blocks.** User messages, assistant markdown, tool calls/results,
  notices, run headers, and image blocks. Assistant text is rendered by
  Glamour; tool output is preformatted; image blocks carry alt text, a bounded
  thumbnail, and a viewer hint. The run header shows the effective
  model/dialect and terminal status.
- **Wrapping and scrolling.** Lip Gloss/Glamour wrap at the viewport width;
  tabs expand to four spaces. Long tool lines wrap. Tables and code blocks may
  scroll horizontally when focused; the transcript itself scrolls vertically
  only. Keep the viewport at the tail while following; scrolling up stops
  following until the user returns to the bottom or submits.
- **Minimum size.** 40x10. Below that, draw a single centered "terminal too
  small" line and keep accepting quit/resize keys.
- **Colors.** Use Lip Gloss styles and honor `NO_COLOR` (any non-empty value)
  by disabling color while keeping attributes. Status is not conveyed by color
  alone; use labels such as `ok`, `error`, `canceled`, `streaming`, `idle`, and
  `rich`/`raw`.
- **Status bar and details.** The status bar shows the effective model, session
  ID (truncated), workspace basename/path, run number, turns/max, tokens/max,
  run state, and elapsed time. The details overlay shows the full session path,
  base URL host, limits, prompt/toolset versions, terminal name/version, color
  profile, and image protocol. Never show the provider token.
- **Markdown (M4).** Glamour v2 renders CommonMark/GFM: headings, paragraphs,
  emphasis/strong, inline code, fenced code, blockquotes, ordered/unordered/task
  lists, rules, tables, and links. Raw HTML is never interpreted; Glamour's
  sanitization and the TUI sanitizer keep it inert. Links show text plus a dim
  URL; no OSC 8 hyperlinks. Invalid/over-deep input falls back to plain text
  with a notice. `Ctrl-R` toggles rich/raw. Parse each assistant item once,
  cache its rendered blocks, and re-render only on width or rich/raw changes;
  do not re-render on every frame.
- **Tables (M4).** Inline markdown tables are rendered by Glamour. `Ctrl-T`
  opens the most recent table in a Bubbles table overlay with a header row,
  column width caps, and horizontal scroll when focused. Wide tables never
  corrupt the transcript or composer. Tool output remains preformatted unless
  a later tool-detail view explicitly uses the widget.
- **Images and charts (M5).** Markdown image links resolve only within the
  workspace (`os.Root`, `filepath.IsLocal`, no `.git`/symlink components).
  PNG/JPEG/GIF only; no network, SVG, or XML. ntcharts `picture` renders the
  image in Kitty mode when available and glyph (half-block) mode otherwise.
  The transcript shows a bounded glyph thumbnail (or a placeholder with alt
  text/path); `Ctrl-G` opens a fit-to-viewport viewer. The viewer clears its
  image on close/resize/exit. Capability detection is best-effort and
  overridable with `AHARNESS_IMAGE_PROTOCOL=auto|kitty|glyph|none`;
  unsupported terminals get the glyph fallback or a placeholder. No
  clipboard/OSC 52 access. Agent-generated chart images use the same path.
- **Snapshot (M4/M5).** `-ui-json` `snapshot` renders the `x/vt` screen to a
  deterministic SVG: cell backgrounds, text runs with styles, cursor, table
  rules, and glyph image previews. SVG text and attributes are XML-escaped; no
  untrusted string is interpolated raw. No timestamps or random IDs. This is
  the coding agent's visual view of the same cells the user sees.

Terminal safety:

- **Single sanitizer.** All model-generated, tool-generated, session-path, and
  error text passes through one sanitizer before it reaches Glamour, Bubbles,
  or the transcript. Valid UTF-8 is enforced; C0/C1 controls other than
  tab/newline become visible glyphs; ESC is never passed through; bidi
  override/isolate controls are stripped; OSC/CSI/DCS/APC/PM sequences are
  rendered as literal text or dropped. Glamour/Bubbles/Lip Gloss output is
  trusted library ANSI, not untrusted text.
- **Markdown and images.** Raw HTML is not interpreted; link targets are
  sanitized; image paths use the workspace containment policy. Image bytes are
  decoded locally and passed to ntcharts `picture`; they are never written as
  terminal text. ntcharts' Kitty protocol parameters are generated by the
  library from validated image data, never interpolated from model/tool text.
  No terminal queries beyond Bubble Tea's startup negotiation and the optional
  image cell-size probe; no clipboard query/response and no OSC 52.
- **Mouse and focus.** Do not enable mouse reporting or terminal focus
  reporting in the first usable version. Bracketed paste is input only.
- **Secrets.** Tool arguments/output may contain secrets, just as they do in
  the session log. The TUI adds no content redaction beyond keeping the
  provider token out of every path. Use external isolation and a clean
  environment when this matters.
- **Width.** Use Bubble Tea/ultraviolet's grapheme-cluster/display-width
  handling for wrapping and clipping; do not split a UTF-8 sequence or a
  grapheme cluster. Common CJK, combining accents, emoji, and ZWJ sequences are
  tested. There is no second in-house width table.
- **Panics.** Do not recover arbitrary panics. Recover at the documented UI
  boundary and at the host's run goroutine so Bubble Tea can restore the
  terminal and the failure can be reported as `internal_error`; re-panic from
  the UI boundary after restoration only if no status can be returned.

### 2.7 Machine-readable UI mode (`-ui-json`)

The real TUI needs a PTY; a coding agent should not need one to create or test
the UI. `-ui-json` runs the same Bubble Tea program with a fake input pipe, a
captured output writer, and an `x/vt` emulator instead of a real terminal. It
reads JSONL commands from stdin and writes JSONL frames and events to stdout.
It is a supported local automation surface, not a remote control protocol or a
daemon.

```sh
aharness -ui-json -model deepseek-v4.1-flash -ui-size 100x30
aharness -ui-json -resume <session-id> -ui-size 80x24
```

`-ui-json` and `-batch` are mutually exclusive. `-ui-size WxH` is required only
for this mode (default `100x30`, valid `20x5`–`500x200`). Task rules follow the
TUI: a task is optional and a seeded task auto-submits; `-stream` is implied.
There is no TTY/`TERM` check, no raw mode, and no alternate screen. The Bubble
Tea program runs with `WithInput`, `WithOutput`, `WithWindowSize`, and
`WithoutSignals`; the output writer feeds `x/vt`. Images use glyph mode in
this mode so the agent sees the same cells without a graphics protocol. The
process still opens the session, provider, tools, and workspace exactly like
the TUI.

#### Protocol v1

Every line is one JSON object with `"v":1` and `"type"`. Unknown fields are
ignored; an unknown version, type, or key is a protocol error. All text is
sanitized with the same terminal-safe view used by the real TUI; frames never
contain ANSI escapes or other terminal controls.

Commands (stdin):

| type | Fields | Behavior |
|---|---|---|
| `text` | `text` | Insert text into the focused input: the composer, or the filter field of an open list/table overlay. Newlines are allowed in the composer. |
| `key` | `key` | Deliver one named key to the focused input/overlay: `enter`, `esc`, `ctrl-c`, `ctrl-d`, `up`, `down`, `left`, `right`, `home`, `end`, `pgup`, `pgdown`, `backspace`, `delete`, `tab`, `shift-tab`, `ctrl-l`, `ctrl-u`, `ctrl-w`, `ctrl-a`, `ctrl-e`, `ctrl-k`, `ctrl-p`, `ctrl-o`, `ctrl-r`, `ctrl-t`, `ctrl-g`. |
| `paste` | `text` | Insert sanitized bracketed-paste text into the focused input. |
| `resize` | `width`, `height` | Resize the headless frame within the allowed range. |
| `wait` | `until`, `timeout_ms` | Wait for `idle`, `run_started`, or `run_finished`. Default timeout 30,000 ms; max 300,000 ms. |
| `frame` | none | Emit the current frame immediately. |
| `snapshot` | `format` | Emit a deterministic SVG snapshot of the current frame. `format` must be `svg` in v1. |
| `quit` | none | Clean shutdown. EOF is equivalent. |

Output (stdout):

| type | Fields | Meaning |
|---|---|---|
| `frame` | `seq`, `width`, `height`, `state`, `overlay`, `cursor`, `lines`, `styles`, `images`, `color_profile`, `no_color`, `image_protocol`, `styles_truncated` | The real rendered cell view. `seq` starts at 1 and increments per frame. |
| `snapshot` | `seq`, `width`, `height`, `format`, `svg` | A deterministic visual snapshot of the same frame. |
| `event` | `event`, plus event fields | Semantic state transition (below). |
| `error` | `code`, `message` | Protocol or UI error. |

A frame's `lines` has exactly `height` entries, is plain UTF-8, and has trailing
spaces trimmed. `cursor` is a 0-based `{row,col,visible,shape}` clipped to the
frame. `state` is one of `idle`, `running`, `streaming`, `tool`, or `error`.
`overlay` is `none` or the top overlay kind: `help`, `details`, `table`,
`file`, `model`, or `image`. Style positions refer to the full frame grid;
trimming trailing spaces from `lines` does not shift them. `shape` is `block`,
`underline`, `bar`, or `hidden`.

`styles` is a coalesced array of `{row,col,width,text,fg,bg,attrs}` spans for
the rendered cells; `row`/`col` are 0-based cell coordinates. `fg`/`bg` are
`"default"`, `"#rrggbb"`, or `"ansi:N"`; `attrs` is a sorted array of `bold`,
`italic`, `underline`, `reverse`, `dim`, `blink`, and `strike`. Adjacent cells
with the same style merge; a wide grapheme is one span with `width:2`.
`styles_truncated` is true when the frame's style cap was hit; `lines` remain
complete and the agent can use `snapshot` for the visual. `-ui-json` omits
wall-clock text (elapsed time is `--`) so frames do not depend on real time.

`images` is an array of `{id,path,alt,row,col,width,height,protocol}` for image
blocks in the frame; the rect uses 0-based cell coordinates. `path` is
workspace-relative when possible and sanitized; the thumbnail itself is in
`lines`/`styles` at the given rect. Raw image bytes are never part of a frame.
`color_profile` is `truecolor`, `256`, `16`, `8`, or `none`; `no_color` is a
bool; and `image_protocol` is `kitty`, `glyph`, or `none`.

`snapshot` returns an SVG of the same cells and styles. Its `seq` is the current
frame sequence number; requesting a snapshot does not itself emit a frame. It
contains no timestamps, random IDs, or wall-clock text. Glyph image previews
are already cells; the SVG renders them directly. The source path is included
in `images` so the agent can open the file. The SVG uses a generic monospace
font and fixed cell metrics; it is a structural/visual aid, not a pixel-perfect
terminal emulator. A snapshot whose SVG payload or encoded line exceeds the cap
emits `error{code:"snapshot_too_large"}` and does not exit; the agent can
resize or use `frame`.

Semantic events:

| event | Fields |
|---|---|
| `run_started` | `run`, `model`, `dialect` |
| `run_finished` | `status`, `reason`, `run_turns`, `run_usage`, `turns`, `usage` |
| `tool_started` | `call_id`, `name`, `arguments` |
| `tool_finished` | `call_id`, `name`, `ok`, `code`, `exit_code`, `truncated`, `timed_out` |
| `model_changed` | `model`, `dialect` |
| `notice` | `kind`, `text` |
| `error` | `code`, `message` |

`usage` uses `input_tokens` and `output_tokens`. `call_id` is the canonical
provider tool-call ID. `arguments` is the raw argument JSON as a JSON value when
valid; malformed arguments are emitted as a capped JSON string so the protocol
itself always stays valid JSON. Events are display-only like the TUI; the
session log remains the durable record.

Example:

```json
{"v":1,"type":"text","text":"Fix the failing test"}
{"v":1,"type":"key","key":"enter"}
{"v":1,"type":"wait","until":"idle","timeout_ms":30000}
{"v":1,"type":"frame"}
{"v":1,"type":"snapshot","format":"svg"}
{"v":1,"type":"quit"}
```

```json
{"v":1,"type":"frame","seq":1,"width":100,"height":30,"state":"idle","overlay":"none","cursor":{"row":1,"col":2,"visible":true,"shape":"block"},"color_profile":"truecolor","no_color":false,"image_protocol":"glyph","styles_truncated":false,"lines":["..."],"styles":[{"row":1,"col":0,"width":7,"text":"aharness","fg":"#89b4fa","bg":"default","attrs":["bold"]}],"images":[]}
{"v":1,"type":"event","event":"run_started","run":1,"model":"deepseek-v4.1-flash","dialect":"chat"}
{"v":1,"type":"event","event":"tool_started","call_id":"call_1","name":"run_command","arguments":{"command":"go test ./..."}}
{"v":1,"type":"event","event":"tool_finished","call_id":"call_1","name":"run_command","ok":true,"code":"ok","exit_code":0,"truncated":false,"timed_out":false}
{"v":1,"type":"event","event":"run_finished","status":"completed","reason":"model completed the task","run_turns":3,"run_usage":{"input_tokens":1200,"output_tokens":300},"turns":3,"usage":{"input_tokens":1200,"output_tokens":300}}
{"v":1,"type":"snapshot","seq":7,"width":100,"height":30,"format":"svg","svg":"<svg ...>"}
```

Timing and determinism:

- A frame is emitted after every command that can change the view, after a
  `wait` completes, and after a non-text state transition (run start/finish,
  tool start/finish, model change, notice, error). `snapshot` and `quit` do not
  emit an extra frame. Text deltas update the model but do not emit a frame;
  use `frame` to snapshot streaming text. The headless driver waits for the
  renderer output to be written and processed by `x/vt` before snapshotting.
- The same command sequence with the same provider/tool responses and the same
  terminal capabilities (color profile and `AHARNESS_IMAGE_PROTOCOL`) produces
  the same frame, event, and snapshot sequence. Frames and snapshots contain no
  random session/run IDs, timestamps, or durations. Image glyph previews come
  from the same local file and are deterministic for a fixed file. Black-box
  tests pin the color profile and image protocol so a developer's terminal does
  not change the expected output.
- `wait` is the only blocking command. A timeout emits an `error` event with
  `code="timeout"` and leaves the run active; it does not exit.
- A malformed command, unknown key/type/version, or an over-cap command line
  emits an `error` and exits 2. An encoded frame over the 1 MiB line cap emits
  `error{code:"frame_too_large"}` and does not exit; the agent can `resize` or
  request `snapshot`. EOF/`quit` exits 0 after the active run is terminated and
  the store is closed. An internal/session failure exits 1. `SIGTERM` cancels
  and exits 130.

This mode is the black-box test surface for coding agents. It runs the real
Bubble Tea program and renderer, the real `x/vt` screen, the real host, and the
real store/runner, so it catches layout, wrapping, styles, image placement,
input, state, and protocol regressions. It does not exercise raw mode or real
signals; those remain manual PTY checks. `teatest/v2` is used in tests for raw
output/golden checks, but it is not the production headless driver. The protocol
is versioned: v1 is the supported automation contract, and a future incompatible
change adds v2 rather than silently changing v1.

### 2.8 Package layout and wiring

```text
internal/tui/
  app.go          # tea.Model, Host/Run interfaces, program lifecycle
  model.go        # transcript, composer, status, scroll, run state, overlays
  view.go         # Lip Gloss composition, transcript/tool/markdown/image views
  markdown.go     # Glamour v2 rendering, raw/rich toggle, table extraction (M4)
  widgets.go      # Bubbles table/filepicker/list, overlay/focus (M4)
  image.go        # workspace image resolution, ntcharts picture, viewer (M5)
  protocol.go     # -ui-json v1 command/event/frame/snapshot schema
  headless.go     # Bubble Tea program + fake input/output + x/vt screen
  screen.go       # x/vt cell/style/cursor projection and SVG snapshot
  sanitize.go     # terminal-safe text sanitizer
  *_test.go
internal/agent/
  display.go      # display events (new)
  history.go      # transcript reconstruction (new)
  runner.go       # emits display events; no UI imports
  retry.go        # emits retry notices
cmd/aharness/
  tui.go          # default TUI wiring, -ui-json wiring, and the TUI host
  run.go          # extracted session shell and one-run helper
  control.go      # TUI operation controller; batch signal delivery
  config.go       # mode selection, -batch/-ui-json/-ui-size, TTY check
```

Wiring:

- `internal/tui` defines consumer-side interfaces:

```go
type ModelChoice struct {
	ID      string
	Dialect model.Dialect
	Default bool
}

type Info struct {
	SessionID, TracePath, Model, Dialect, Workspace string
	MaxTurns, MaxTotalTokens                        int
	Turns                                           int
	Usage                                           model.Usage
}

type Submission struct {
	Text   string
	Source string // "tui", "-task", or "-task-file"
}

type RunOptions struct {
	Model string // empty inherits the current model
}

type Run interface {
	Interrupt()
	Terminate()
	Done() <-chan agent.Result
}

type Host interface {
	Info() Info
	Models() []ModelChoice
	History() ([]agent.HistoryItem, error)
	Start(sub Submission, opts RunOptions, steer <-chan string, display func(agent.DisplayEvent) error) (Run, error)
	Close() error
}
```

- `cmd` implements `Host` over the existing `session.Store`, `tools.Workspace`,
  and provider adapters. `Models` returns the accepted registry with dialects.
  `Start` validates `opts.Model` (empty inherits the current model), creates the
  adapter for that model, resets the work directories, creates a new runner and
  controller for that run, starts the run in a goroutine, and returns a handle.
  `Done` receives the result after the work directory is removed and the store
  is synced. `Close` closes the workspace and store only after the final run has
  finished. The model picker changes only the next `Start`; the run's model is
  fixed at `run_start`.
- Refactor `cmd/aharness/run.go` into a reusable session shell:
  `openSession` (new/resume/fork) and `startRun` (work dirs, runner, one run,
  cleanup) are used by the batch CLI, the TUI host, and the `-ui-json` host.
  The existing `executeNew`/`executeResume`/`executeFork` become thin
  error/printing wrappers; their behavior and exit codes do not change.
- `cmd` selects the mode before task validation: explicit `-ui-json`, explicit
  `-batch`, otherwise TUI when stdin/stdout are terminals, otherwise batch.
  TUI and `-ui-json` share the same Bubble Tea app/model and host; only the
  input/output driver differs (real terminal vs fake pipe + `x/vt`). Batch
  bypasses the TUI entirely.
- The Bubble Tea app starts a run with a `tea.Cmd` that calls `Host.Start` and
  a second `tea.Cmd` that reads the display-event channel and returns each
  event as a `tea.Msg`. `Run.Done` becomes a `tea.Msg`; the app does not block
  `Update` on the run. The display adapter sends to a bounded channel and
  selects on the app's done channel.
- Split the v1 `interactiveControl` into a reusable operation state machine
  (`BeginOperation`/`Interrupt`/`Terminate`) and batch signal delivery. The TUI
  calls `Interrupt`/`Terminate` from `Esc`/`Ctrl-C`/`Ctrl-D`; batch keeps the
  ordinary `SIGINT`/`SIGTERM` terminal behavior. Delete the `-interactive`
  flag, `cmd/steering.go`, and the stdin steering tests; there is no
  deprecation shim.
- The host's run goroutine recovers a panic at its boundary, converts it to a
  fatal `internal_error` result/notice, and exits the run so the app can quit
  the program and restore the terminal. This is a documented boundary, not
  general panic recovery.
- `internal/tui` is a new top-level package because the app/view, widgets,
  headless screen, and protocol are an independent responsibility with their
  own tests. It imports `agent` for display/history, `model` for canonical
  types, Bubble Tea/Bubbles/Glamour/ntcharts for the UI, and `x/vt` only in the
  headless driver. `agent` never imports it. No `pkg/`, `util`, `common`, or
  `types` package is added.
- The TUI never writes the session log and never appends display events. It
  reads history only when no run is active. The runner remains the only store
  writer.
- The TUI host does not pass a stdout/stderr logger into the run. The existing
  `slog` progress lines would corrupt the screen. Display events replace
  model/tool progress; startup and close failures are returned to the UI or
  printed after the terminal is restored.

#### Startup and shutdown order

1. Parse flags, select the mode (`-ui-json`, `-batch`, or default TUI), apply
   the task rules for that mode, and run the TTY/`TERM` check only for the TUI.
2. Prepare the state directory, open the workspace, create/open/fork the
   store (lock, validation, repair), and call `agent.Inspect`.
3. Build `Info` and `History` from the pre-run snapshot.
4. TUI: construct the Bubble Tea program with the app model, alternate screen,
   mouse off, paste on, and the initial window size; run it, render the initial
   frame, and submit a seeded task. `-ui-json`: construct the same program with
   a fake input pipe, a capture output writer, `WithWindowSize`, and
   `WithoutSignals`; feed the writer to `x/vt`, start the protocol reader,
   emit the first frame, and process commands. Batch: skip the UI and run the
   one-shot shell.
5. On exit: stop the protocol/input, terminate and wait for any active run,
   quit the Bubble Tea program and wait for it to restore the terminal,
   close/sync the store, close the workspace, and print the session summary to
   stderr (TUI) or finish the protocol stream (`-ui-json`). Return the exit
   code.

A failure in steps 1–3 is reported before entering the UI. A failure in step 4
or later quits the program (Bubble Tea restores the terminal or the fake
driver is closed), runs the step 5 cleanup, and closes the protocol cleanly
before returning an error.

#### Dependency decision

The TUI now needs an application framework, markdown renderer, widgets, chart
and image rendering, and a headless terminal screen. The chosen stack is:

- `charm.land/bubbletea/v2` v2.1.1 (MIT): program loop, cell-based renderer,
  input, alternate screen, paste, colors, cursor, and declarative views.
- `charm.land/lipgloss/v2` v2.0.6 or later (MIT): layout and styles for the
  header, transcript, composer, status bar, and overlays.
- `charm.land/bubbles/v2` v2.2.1 (MIT): table, file picker, list, text input,
  textarea, and viewport components.
- `charm.land/glamour/v2` v2.0.1 (MIT): markdown rendering for Lip Gloss v2 /
  Bubble Tea v2.
- `github.com/NimbleMarkets/ntcharts/v2` v2.9.0 (MIT): charts, `picture`
  (Kitty graphics plus universal half-block glyph fallback), and chart-image
  rendering.
- `github.com/charmbracelet/x/vt` v0.1.0 (MIT): the headless virtual terminal
  used only by `-ui-json` and tests. It provides `Write`, `CellAt` (content,
  style, width), cursor state, and alternate-screen state.
- `github.com/charmbracelet/x/exp/teatest/v2` (optional, test-only): a Bubble
  Tea v2-compatible test helper in `x/exp`. It captures raw renderer output;
  it does not expose cells. The production headless driver uses the same
  `WithInput`/`WithOutput` approach plus `x/vt`, not teatest. If the test
  dependency is rejected, the x/vt harness covers the same assertions.

Justification per the coding standard §3:

- **Framework-scale coverage:** the requested features are not a few dozen
  lines. Markdown, tables, file/model pickers, charts, images, and a
  cell-level headless screen are provided by maintained modules; rebuilding
  them in-house would be a much larger and ongoing effort. This is the
  explicit trade the user asked for when reviewing the framework decision.
- **Agent visibility:** `x/vt` gives the agent the rendered Bubble Tea screen
  (content, style, cursor, alternate-screen state) without a PTY. This is the
  requirement that previously favored tcell. `teatest/v2` alone is not enough;
  it is a raw-output test helper, which is why the production driver uses
  `x/vt`.
- **Maintenance/security:** Bubble Tea, Bubbles, Glamour, and ntcharts are
  MIT-licensed, actively maintained, and pure Go. `x/vt` is v0.1.0 and is
  isolated to the headless/test path; pin it and gate it in M1. `teatest/v2`
  stays test-only. Run `govulncheck` on the full tree.
- **Binary impact:** measure `go build -trimpath` before/after and record the
  result in the change. Do not guess. This is the largest dependency decision
  in the project; the measurement is part of the review.
- **Fallback:** if the dependency tree or framework ownership is rejected, use
  the tcell v3 + in-house plan from the previous revision. That plan has a
  smaller tree and a public cell screen, but requires building markdown,
  widgets, charts, and image protocols in-house. A fallback implementation is
  a new decision record, not a silent swap.

##### Framework research

The choice was re-evaluated again on 2026-11-10 with the v2 ecosystem in scope.

| Option | What it provides | Dependency/ownership cost | Verdict |
|---|---|---|---|
| Bubble Tea v2 + Bubbles v2 + Glamour v2 + ntcharts v2 + x/vt | Program loop, cell renderer, input, widgets, markdown, charts, Kitty/glyph images, headless cell screen | Large framework-scale tree; framework owns loop/view; `x/vt` is v0.1.0; `teatest/v2` is test-only/`x/exp` | **Chosen.** The v2 ecosystem covers the requested features and `x/vt` preserves agent-visible cells. |
| tcell v3 + in-house App/view/widgets/markdown | Cell screen, input, width, paste, resize, sixel region locking, fake-TTY headless screen | 6 direct + 1 indirect modules; App/view/widgets/markdown/charts/images remain ours; exact cells | **Fallback** if the Bubble Tea tree or `x/vt` is rejected. Smaller tree, much more in-house UI work. |
| `golang.org/x/term` + in-house everything | Raw mode, size, restore; own input/width/renderer/widgets | 2 modules; most code; no public cell screen | Rejected: does not account for rich text/widgets/images. |
| tview | Widgets over tcell v2 | tcell v2, own application model, no markdown/charts | Rejected. |
| termbox-go | Older cell library | Superseded by tcell | Rejected. |

The concrete findings:

- Bubble Tea v2.1.1's `go.mod` lists `ultraviolet`, `x/ansi`,
  `colorprofile`, `x/term`, `go-colorful`, `cancelreader`, `x/sys`, and
  `x/exp/golden` (8 direct + 9 indirect). Its Cursed Renderer is cell-based and
  ANSI-output based; the renderer is internal. `WithInput`, `WithOutput`,
  `WithWindowSize`, and `WithoutSignals` support a headless program.
- Bubbles v2.2.1 provides table, filepicker, list, textinput, textarea,
  viewport, and related components. They are Bubble Tea models; the file
  picker must be constrained to the workspace and the model picker uses the
  harness registry.
- Glamour v2.0.1 uses Lip Gloss v2 and renders markdown to ANSI; Bubble Tea
  consumes it directly. It brings chroma, bluemonday, goldmark, and related
  modules. Raw HTML is sanitized; the TUI sanitizer still runs first.
- ntcharts v2.9.0 uses Bubble Tea v2.1.1, Bubbles v2.2.1, Lip Gloss v2.0.6,
  ultraviolet, bubblezone, and `x/image`. Its `picture` model supports Kitty
  graphics and universal glyph fallback; charts include line, bar, heatmap,
  OHLC, sparkline, time series, and more. Its API is still subject to change;
  pin v2.9.0 and isolate it behind the TUI's image/chart views.
- `teatest/v2` exists at
  `github.com/charmbracelet/x/exp/teatest/v2` and requires Bubble Tea v2. It
  is a test helper that captures raw output (`Output`, `FinalOutput`,
  `RequireEqualOutput`); it does not provide a cell screen. It remains in
  `x/exp` and uses real-time waits, so it is not the production headless
  driver.
- `x/vt` v0.1.0 builds and provides `Emulator.Write`, `CellAt` (ultraviolet
  `Cell` with content/style/width), cursor state, and alternate-screen state.
  This is the chosen production headless screen. It is v0.1.0; M1 must validate
  it against a screen/style fixture matrix. If it fails, fall back to a small
  ANSI screen parser for the subset Bubble Tea emits, or re-open the tcell
  fallback.
- tcell v3.6.0 remains a viable fallback: public `tty.Tty` +
  `NewTerminfoScreenFromTty`, `Screen.Get` cells/styles, sixel region locking,
  and a small dependency tree. It is not chosen because the Bubble Tea v2
  ecosystem now covers markdown, widgets, charts, and images, and `x/vt`
  covers the agent-visible screen.

The decision is reversible at the app/view boundary: the Host/Run contract,
session/run model, display/history API, and `-ui-json` protocol do not depend
on Bubble Tea types outside `internal/tui`. A tcell fallback would replace the
app/view/headless layer and keep the contracts. Do not adopt a second
framework without a new decision record.

References: [Bubble Tea][bubbletea], [Bubble Tea `go.mod`][bt-mod],
[Bubbles][bubbles], [Glamour][glamour], [ntcharts][ntcharts],
[`teatest/v2`][teatest], [`x/vt`][xvt], [tcell][tcell],
[tcell `Tty`][tcell-tty], [`NewTerminfoScreenFromTty`][tcell-screen].

[bubbletea]: https://github.com/charmbracelet/bubbletea
[bt-mod]: https://github.com/charmbracelet/bubbletea/blob/main/go.mod
[bubbles]: https://github.com/charmbracelet/bubbles
[glamour]: https://github.com/charmbracelet/glamour
[ntcharts]: https://github.com/NimbleMarkets/ntcharts
[teatest]: https://github.com/charmbracelet/x/tree/main/exp/teatest/v2
[xvt]: https://github.com/charmbracelet/x/tree/main/vt
[tcell]: https://github.com/gdamore/tcell
[tcell-tty]: https://pkg.go.dev/github.com/gdamore/tcell/v3/tty#Tty
[tcell-screen]: https://pkg.go.dev/github.com/gdamore/tcell/v3#NewTerminfoScreenFromTty

`go.mod` moves from "stdlib only" to Bubble Tea v2 + Lip Gloss v2 + `x/vt` in
M1; M4 adds Glamour/Bubbles; M5 adds ntcharts (sharing the same Bubble Tea,
Bubbles, and Lip Gloss versions where possible). The exact direct/indirect set
is determined by `go mod tidy` and recorded with the binary-impact measurement.
README and architecture dependency statements are updated in the milestone
that adds each dependency.

### 2.9 Caps

These are display-only caps; none changes the session log or the model
contract. User-visible values are recorded in `limits.md` when implemented.

| Cap | Proposed value | Behavior |
|---|---|---|
| TUI task/steering message | 64 KiB | Same as the v1 steering line; longer is rejected with a notice, the run continues. |
| Transcript items retained | 20,000 | Oldest items are dropped with an "older history omitted" marker; the log is unchanged. |
| Transcript text retained | 16 MiB | Oldest items are dropped; the log is unchanged. |
| Assistant text per item | 64 KiB displayed | Marker with total bytes; the full response stays in the log. |
| Markdown source per item | 64 KiB | Same as the assistant cap; longer text is rendered as plain text with a marker. |
| Markdown blocks per item | 4,000 | Excess blocks are dropped with a notice; the raw text remains in the log. |
| Markdown nesting | 16 levels | Deeper input falls back to plain text with a notice. |
| Table columns / rows | 12 / 500 displayed | Column width cap 40 cells; excess columns/rows scroll or fold with a marker. |
| Widget list items | 1,000 | File/model pickers filter before rendering; excess entries are paged. |
| File picker entries | 2,000 per directory | No recursive read; filter query capped at 256 bytes; no file contents read. |
| Overlay stack | 4 | Deeper requests close the oldest non-modal overlay with a notice. |
| Image file / dimensions | 8 MiB / 4096x4096 | Larger files are rejected with a placeholder notice; `image.DecodeConfig` runs before full decode. |
| Decoded image pixels | 16 Mi (4096x4096 max) | Larger images are downsampled or rejected before allocation. |
| Images per transcript / cache | 32 / 64 MiB | Oldest thumbnails/decoded images are evicted; the transcript item remains. |
| Image thumbnail | 60x20 cells or the available width, whichever is smaller | Glyph preview; Kitty only in the viewer. |
| Tool output | 4 KiB or 40 lines folded; 64 KiB expanded | Expanded output never exceeds the logged tool-result cap. |
| Tool arguments preview | 2 KiB or 8 lines | The full arguments remain in `model_response`/the log. |
| Input history | 100 entries | In-memory only; not persisted. |
| Minimum terminal size | 40x10 | Below shows a resize message; no layout. |
| Frame coalescing | 16 ms | Text deltas coalesce; state changes flush immediately. |
| Escape timeout | 50 ms | A lone Esc is delivered; partial CSI/SS3 and Alt/Option sequences are bounded. |
| Display queue | 256 events | Backpressure, no silent drops; text deltas coalesce in the app loop. |
| `-ui-json` command line | 1 MiB | Longer lines are a protocol error (exit 2). |
| `-ui-json` text/paste | 64 KiB per command | Longer text is rejected with a protocol error. |
| `-ui-json` frame size | 20x5–500x200 | `-ui-size`/`resize` outside the range is a config/protocol error. |
| `-ui-json` wait timeout | 30,000 ms default; 300,000 ms max | A timeout emits `error{code:"timeout"}` and does not exit. |
| `-ui-json` frames | One JSON line per frame | `lines`, styles, cursor, and image metadata; no ANSI; bounded by the 1 MiB line cap. |
| `-ui-json` style spans | 16,000 or 256 KiB encoded | Excess styles are coalesced/dropped and `styles_truncated` is true; `lines` remain complete and the frame stays under the 1 MiB line cap. |
| `-ui-json` snapshot | 512 KiB SVG payload | The encoded JSON line stays under the 1 MiB protocol cap; over-cap snapshots emit `snapshot_too_large` and do not exit. |

## 3. Milestones

| Milestone | Deliverables | Exit criteria |
|---|---|---|
| **M0 — Observation and session shell** | `agent.DisplayEvent` and `Config.Display`; replace `EmitText` with a CLI text-sink adapter; `agent.History`; extract `openSession` and `startRun` from the one-shot CLI; split the control state machine from signal delivery; add the `tui` user-message source. No new dependency and no mode change yet. | The batch CLI's stdout/stderr/exit behavior and session log semantics are unchanged; display events have the documented order for streamed, buffered, tool, interrupt, retry, and error paths; history pairs calls/results, ignores unknown events, and never duplicates final text; no `EmitText` remains; §8 gates pass. |
| **M1 — Bubble Tea core and headless screen** | Bubble Tea v2 and Lip Gloss v2 dependencies with the §2.8 justification; `internal/tui` app/model/view skeleton, Lip Gloss layout, sanitizer, key mapping, headless driver (fake input/output + `x/vt`), cell/style/cursor projection, SVG snapshot skeleton, and app loop with a fake host; fuzz targets; a manual harness test guarded by `AHARNESS_TUI_MANUAL=1` (skipped by default). No mode cutover yet. | A headless app renders fixture history, edits/submits, scrolls, resizes, and quits; `x/vt` cells/styles/cursor match the expected fixture; raw Option-`[`/Option-`]` bytes never become a bare Esc; ESC/OSC/CSI injection tests pass; the manual harness enters and leaves cleanly and `stty -a` is unchanged; sanitizer and protocol fuzz seeds pass under `-race`; §8 gates pass. |
| **M2 — Default TUI and automation** | Mode selection (TUI default, `-batch`, `-ui-json`, `-ui-size`); TTY/`TERM` checks only for the TUI; `cmd` host for new/resume/fork; `Host.Models`/`RunOptions.Model` wired (picker UI is M4); live streaming and tool activity; history on resume; per-message runs; steering queue/ack; Esc/`Ctrl-C`/`Ctrl-D`; `SIGTERM`/`SIGHUP`; resize; status bar; work-dir and store lifecycle; remove `-interactive`, `cmd/steering.go`, and stdin steering tests; implement the §2.7 JSONL protocol (lines/styles/cursor/image metadata) and black-box tests; docs for the CLI/TUI contract. | The core §1.4 items — default startup, live run, conversation, resume/fork, steering, interrupt/quit, terminal restoration, durability, batch compatibility, `-ui-json` — hold. `-interactive` is gone. The in-process integration suite passes with the headless Bubble Tea/`x/vt` harness, fake provider, and the real store/runner; `-ui-json` passes a scripted agent-style session (submit, stream, tool, interrupt, resize, frame, snapshot, quit); a real terminal smoke shows streaming, tools, steering, Esc/`Ctrl-C`, quit, and resume; no leaked goroutines or children; §8 gates pass. |
| **M3 — Hardening and polish** | Multi-line composer and bracketed-paste integration; scrollback follow/indicator polish; tool expansion; input history; `NO_COLOR`; help/details overlays; overlay/focus foundation; bidi/Unicode hardening and tests; error/auth/limit UX; display caps and `limits.md`; optional suspend/resume; terminal matrix; README/architecture/testing-strategy/protocol docs; real-provider TUI smoke. | Full §1.4 checklist; scrollback and paste behave on 80x24 and 200x60; CJK/emoji/combining text stays aligned; an OSC 52/CSI output test cannot inject; display caps drop only UI history; the `-ui-json` v1 schema is documented and stable; docs and limits match the shipped behavior; real-provider smoke is recorded; §8 gates pass. |
| **M4 — Rich text and widgets** | Glamour v2 and Bubbles v2 dependencies; markdown rendering and raw/rich toggle; Bubbles table/filepicker/list; workspace-constrained file picker; model picker over the accepted registry plus per-run adapter selection; overlay/focus manager; `Ctrl-R`/`Ctrl-P`/`Ctrl-O`/`Ctrl-T`; `-ui-json` style spans and `snapshot` SVG; caps/docs/tests. | §1.4.1 holds: markdown fixtures render headings/emphasis/code/lists/tables/links; raw HTML is inert; table/file/model overlays work and restore the frame; model picker changes the next `run_start` only; `-ui-json` styles and SVG snapshots are deterministic and contain no ANSI; docs/limits match; §8 gates pass. |
| **M5 — Images and charts** | ntcharts v2 dependency; workspace-contained PNG/JPEG/GIF decode with caps; `picture` Kitty/glyph rendering; glyph thumbnails; fit-to-viewport viewer; `AHARNESS_IMAGE_PROTOCOL`; `Ctrl-G`; agent-generated chart images; `-ui-json` image metadata and glyph snapshot; manual image matrix; docs/limits. | §1.4.2 holds: images render as glyph thumbnails and in the viewer; Kitty path works where available; unsupported/oversized/missing images become placeholders; no raw image bytes in frames; the snapshot renders the glyph preview; close/resize/scroll leave no stale image; the real-terminal matrix is recorded; §8 gates pass. |

Milestone rules:

- Repository documentation is updated with the milestone that changes the
  contract, not in a final pass: M0 updates the architecture display/history
  and testing unit lists; M1 records the Bubble Tea/`x/vt` dependency and
  headless-screen policy; M2 updates the mode/CLI/TUI/`-ui-json` contract and
  README usage; M3 updates limits, the testing strategy's manual matrix, the
  protocol schema, and the security/terminal notes; M4 records Glamour/Bubbles,
  markdown/widget keys, style spans, and the snapshot schema; M5 records
  ntcharts, image/chart caps/protocols, and the image matrix.
- Every milestone is gated by the coding standard §8 checks. Do not defer lint,
  vet, race, or vulnerability checks. Run `go fix ./...` before review and
  `go doc -ex` for new public-in-package examples; do not run `go fix` as a CI
  check.
- If a milestone cannot pass, fix the interface. Do not pull a later feature
  forward or ship a stub terminal mode. M1 is a component milestone; the
  default-TUI cutover lands only in M2, after the TUI and `-ui-json` acceptance
  tests pass and the `-interactive` replacement is proven. M4 and M5 are
  post-first-usable milestones: they may be split or reordered, but they must
  not weaken §1.4 or introduce a second view/render path.

## 4. Testing strategy

### 4.1 Unit

- Display contract: event order for a scripted runner; `RunStarted`
  model/dialect; `TextDelta` once per delta; buffered fallback text emitted
  once; no text after a completed streamed response; tool-started before
  execution and tool-completed after sync; retry/limit/interrupt notices;
  `RunFinished` after `final`/`run_end`; display errors mapped to
  `internal_error`; nil display disables events.
- History: v2 fixtures with multiple runs; run header model/dialect;
  user/assistant/tool pairing; canceled/panic/repaired results; pending calls;
  interruptions, errors, limits, compaction, repair, retry notices; hints
  rendered as notices; unknown events/fields; malformed known payloads; final
  text not duplicated; fork parent notice; caps.
- Headless screen: `x/vt` cell content/style/width for styled text, wide/CJK,
  combining/emoji, cursor position/visibility/style, alternate-screen state,
  and scrollback; a fixture matrix that compares `CellAt` against expected
  cells; malformed ANSI is bounded and does not panic.
- Input mapping: raw bytes through the fake input pipe for printable UTF-8,
  split UTF-8 runes, Enter, Ctrl keys, arrows/Home/End/PgUp/PgDn/Delete, split
  CSI/SS3 sequences, unknown sequences, bracketed paste with newlines and an
  embedded end marker, and resize. Bare Esc with the 50 ms timeout; raw
  Option-`[`/Option-`]` and Alt-prefixed sequences never become a bare Esc.
- Key behavior: Esc interrupts only when it is a bare Esc and no overlay owns
  it; Esc never cancels the run or quits; Ctrl-C first interrupts and second
  cancels the run; Ctrl-D quits; Ctrl-C idle clears/hints; overlay context;
  `Ctrl-P`/`Ctrl-O`/`Ctrl-R`/`Ctrl-T`/`Ctrl-G` routing.
- Markdown (M4): Glamour fixtures for headings, paragraphs, emphasis, strong,
  inline code, fenced code, blockquotes, ordered/unordered/task lists, rules,
  tables, links, and images; raw HTML is inert; malformed and over-deep input
  falls back to plain text; `Ctrl-R` toggles; styles map to Lip Gloss/ANSI;
  caps.
- Widgets (M4): Bubbles table column widths/wrapping/clipping/horizontal
  scroll; file picker navigation, filter, workspace containment (`..`, `.git`,
  symlink, absolute path), and composer insertion; model picker
  registry/selection/disabled-during-run; overlay focus/Esc/Tab; underlying
  frame restoration.
- Images and charts (M5): decode caps (size, dimensions, pixels); PNG/JPEG/GIF;
  missing, oversized, unsupported, and out-of-workspace paths become
  placeholders; glyph thumbnail downsampling; Kitty/glyph mode selection;
  viewer close/resize/scroll clears the image; agent-generated chart images
  use the same path; no raw bytes in text cells.
- Snapshot (M4/M5): deterministic SVG for a fixed `x/vt` screen; styles,
  cursor, table rules, glyph previews/placeholders; no timestamps/random IDs;
  cap behavior; no ANSI.
- Composer: insert/backspace/delete; cursor movement; home/end; kill/clear;
  history recall; submit; cap; cursor visibility.
- App/model: idle/running/streaming/tool/error states; follow-tail and
  scroll-off; steering queue/ack/requeue; first/second interrupt; quit while
  active; resize; details/help/table/file/model/image overlays; fatal status;
  model picker applies to the next run only.
- UI JSON protocol: command decoding for
  text/key/paste/resize/wait/frame/snapshot/quit; unknown version/type/key;
  command-line and text caps; frame `seq`/size/state/overlay/cursor/lines/
  styles/images/color profile; `styles_truncated`; snapshot SVG; deterministic
  event order; `wait` immediate/running/timeout; over-cap `frame_too_large`/
  `snapshot_too_large` errors; EOF/quit; protocol errors map to exit 2; no ANSI
  in frames or snapshots.
- Mode/CLI: default TUI vs automatic non-TTY batch vs explicit `-batch` vs
  `-ui-json`; TTY/`TERM` checks only for the TUI; `-ui-json`/`-batch` mutual
  exclusion; `-ui-size` only with `-ui-json`; per-mode task rules; `-interactive`
  is an unknown flag; source strings; one run per submission; work-dir reset
  and cleanup; store close/sync; concurrent resume lock.

### 4.2 Integration

Use a fake Bubble Tea input/output pair plus `x/vt` for TUI tests, JSONL pipes
for `-ui-json` tests, and a fake provider; run the real `cmd` host, `agent`
runner, `session.Store`, and Bubble Tea app in a temp workspace/state
directory. Mode-selection tests inject the TTY predicate; the fake input/output
drives the program after selection. This exercises the real wiring and renderer
without requiring a PTY in CI:

- Mode selection: a TTY with no flags starts the TUI; a TTY with `-batch`
  prints the one-shot result and does not initialize the terminal; a non-TTY
  invocation runs batch automatically; `-ui-json` runs without a TTY; `-batch`
  and `-ui-json` together are a config error; `-interactive` is an unknown
  flag; `-ui-size` outside `-ui-json` is a config error.
- New TUI session with no task: type a task, see streamed text before the fake
  completes, see a tool call/result, see the final status; the log has exactly
  one `run_start`/`run_end`; no delta events; work dir removed.
- Second message: two runs in one log; cumulative turns/tokens; per-run clock;
  prior final text is not reprinted to stdout.
- Resume: pre-populate a session (including an interrupted run and a pending
  call); start the TUI or `-ui-json`; assert history; submit; assert repair
  results and a new run.
- Fork: complete a session, fork with no task, submit; parent hash unchanged;
  new log has the copied prefix and the new run.
- Steering: block the fake provider; submit a second line; assert it becomes
  `user_message{source=steer}` and appears in the next request; submit a line
  just before completion and assert it is returned to the composer, not lost.
- Interrupt: block the provider and send `Esc`; assert `interrupt` and the
  hint, then continue. Repeat with first `Ctrl-C`. Send raw Option-`[`/Option-`]`
  bytes and assert no interrupt. Send second `Ctrl-C`; assert the run cancels
  and the UI returns to idle without exiting.
- Tool interrupt: run `sh -c 'echo $$; sleep 60'`; interrupt; assert
  `code=canceled`, the process group is gone, and the run continues.
- Quit/signals: `Ctrl-D` while idle closes the store and quits the program;
  `Ctrl-D` while active cancels, writes terminal events, exits 130; `SIGTERM`
  writes `final`/`run_end`, restores, and exits 130; `internal_error` restores
  and exits 1.
- `-ui-json` black-box: pipe a scripted session (text/enter, wait idle, frame,
  resize, `ctrl-c`, quit); assert the JSONL frames/events, deterministic
  ordering, no ANSI, session log contents, and exit codes. Include a protocol
  error (unknown type/key/version) and a wait timeout. Add `snapshot`, assert
  the SVG is deterministic and matches the frame's cell/style content.
- Rich content (M4): submit assistant text with headings, emphasis, a list, a
  fenced code block, a table, and a link; assert the frame lines/styles and
  the snapshot; `Ctrl-R` toggles raw text; raw HTML is inert. Open and close
  help/details/`Ctrl-T` table overlays; assert the underlying frame is
  restored.
- Model picker (M4): open `Ctrl-P`, select a different accepted model, submit;
  assert `model_changed`, the next `run_start` model/dialect, and that the
  previous run is unchanged. Assert the picker is disabled during a run and
  that resume inherits the latest run's model.
- File picker (M4): open `Ctrl-O`, filter/navigate, select a workspace file;
  assert the relative path is inserted into the composer, no file content was
  read, and `..`/`.git`/symlink/absolute escapes are rejected. `Esc` leaves the
  composer unchanged.
- Images (M5): create a PNG under the workspace, submit an assistant markdown
  image link, and assert the image metadata and glyph thumbnail cells in the
  frame and the bounded preview in the snapshot; `Ctrl-G` opens/closes the
  viewer; missing, oversized, and out-of-workspace paths become placeholders;
  no raw image bytes appear in text cells.
- Batch: with a fake provider, `-batch` preserves v1 stdout/stderr, streamed
  text, resume/fork, and exit codes; a non-TTY invocation without `-batch`
  behaves the same.
- Terminal safety: feed model/tool output containing OSC 52, CSI cursor moves,
  bidi controls, raw HTML, and image links with `..`/`.git`/symlink/absolute
  paths; assert the frames contain no ESC, the clipboard was not addressed,
  the HTML is inert, and out-of-workspace images are rejected; resize
  mid-stream; assert reflow and no panic.
- Lock: pause a run in the provider; a second CLI `-resume` exits 2 with the
  trace path.

### 4.3 Manual terminal smoke

The headless screen does not prove raw-mode or real image-protocol behavior.
Run the built binary in a real terminal on the Unix host:

- Start with no flags (`aharness -model ...`) and confirm the TUI is the
  default; start with `-batch` and confirm one-shot stdout. At least one local
  emulator and tmux, at 80x24 and 200x60. Record the terminal and `TERM` value.
- Resize during streaming and during a tool call; scrollback; long lines;
  CJK/emoji/combining input and output; bracketed paste of code.
- `Esc`, first/second `Ctrl-C`, and raw Option-`[`/Option-`]` during a model
  call and a command; `SIGTERM`; `SIGHUP` (close the terminal window or
  `kill -HUP`); `Ctrl-D`.
- Confirm `stty -a` before/after, no raw mode or hidden cursor remains, no
  child process or process group survives, and the work directory is gone.
- Rich content (M4): render headings, lists, code, a wide table, links, and
  raw HTML; toggle `Ctrl-R`; open/close help/details/table overlays; use
  `Ctrl-P` to switch models and verify the next run's header/log; use `Ctrl-O`
  to insert a file path. Confirm the composer and frame restore cleanly.
- Images (M5): render a PNG chart thumbnail; open `Ctrl-G`; verify Kitty and
  glyph paths where available; close the viewer and scroll/resize; confirm no
  stale image remains and the terminal is restored. Record the protocol
  detected and any fallback.
- Run a real-provider session: default startup, streamed text, a tool round,
  steering, Esc interrupt, second `Ctrl-C`, a second message, quit, resume, and
  fork. If a hosted model does not stream, record the limitation; do not claim
  streaming support for it.
- Drive `-ui-json` as a coding agent would: pipe a command script, parse the
  JSONL frames/events/styles/images, request `snapshot`, view the SVG, assert
  the final status, and confirm the session log is the durable record. This is
  the automated black-box smoke; it still does not prove raw-mode restoration.
- Optional: use `script(1)` to allocate a PTY for a smoke run if available.
  This is a manual/optional check, not a CI gate; CI must not depend on
  util-linux or terminal emulator availability.

### 4.4 Fuzzing and gates

- Fuzz the terminal sanitizer, the markdown input path, and the image
  path/protocol parser with seed corpora under `internal/tui/testdata/fuzz`.
  The targets must not touch a real terminal or network and must bound memory.
  Bubble Tea/ultraviolet input parsing is upstream code; we fuzz our mapping
  and sanitizer, not a second decoder.
- Fuzz the `-ui-json` command decoder and frame/event/snapshot encoder:
  arbitrary bytes must never panic, emit raw ESC/control text, exceed the
  line/text/style/snapshot caps, or produce malformed JSON/SVG.
- Fuzz `agent.History` with arbitrary v2 event payloads to verify no panic and
  bounded output. It is a decoder of logs that may be edited, even though the
  log is trusted input in v1.
- Run the coding standard §8 gates in one Linux job. `go test -race -count=1`
  must include the TUI unit/integration tests; the headless Bubble Tea/`x/vt`
  harness means no PTY is required. `govulncheck` covers the full Bubble Tea
  stack and `x/vt`. CI also runs `go mod tidy`/`verify` and fails on a dirty
  `go.mod`/`go.sum`.
- No real network in CI. Fuzzing may run on a schedule.

## 5. Decisions, risks, and assumptions

### 5.1 Decisions

| # | Decision | Rationale | Reversal risk |
|---|---|---|---|
| T1 | The TUI is the default when stdin/stdout are terminals. `-batch` forces the one-shot CLI; non-TTY invocations are batch automatically. There is no `-tui` flag. | Makes the interactive path the default without breaking pipes/scripts; one binary and one loop. | Medium; changes the default behavior of interactive invocations. |
| T2 | A TUI process may execute multiple runs, one per submitted message. | Makes a session conversational without inventing a second loop or a long-lived run; the v2 log already supports multiple runs. | High; run/CLI semantics and user expectations. |
| T3 | Display events are display-only and add no durable event type. | The log stays proportional to complete responses; UI state is disposable; resume does not depend on the UI. | Low; a new event type would be a schema change. |
| T4 | `agent.History` reconstructs the view from the log; the TUI does not parse stdout or tail the file. | One decoder for display, deterministic tests, and resume shows exactly what is durable. | Medium; history API must track payload changes. |
| T5 | Replace `EmitText` with a single display callback. | One observation path for CLI streaming and TUI; avoids duplicate text delivery and ordering bugs. | Medium; internal API and tests change. |
| T6 | Use Bubble Tea v2 as the application framework, with Bubbles v2, Glamour v2, and ntcharts v2; use `x/vt` only for the headless screen. | The v2 ecosystem covers markdown, widgets, charts, and images; `x/vt` preserves agent-visible cells. This is a deliberate framework-scale trade for feature coverage. | High; framework ownership and a large dependency tree. |
| T7 | The tcell v3 + in-house plan is the fallback, not the primary plan. | Smaller tree and public cell screen, but requires building markdown/widgets/charts/images in-house. | Medium; a fallback is a new decision record. |
| T8 | Sanitize all untrusted text and never pass escape sequences through. | Model/tool output is attacker-influenced; OSC 52 and C0/C1 injection are real terminal risks. | Low; behavior is deliberately lossy. |
| T9 | `Esc` cancels/dismisses and interrupts a bare active operation; `Ctrl-C` is the authoritative global interrupt (second press cancels the run); `Ctrl-D` quits. | Esc matches Apple/GNOME cancel semantics and Claude Code's interrupt gesture; Ctrl-C preserves SIGINT convention and remains reliable despite Esc/Alt ambiguity. | Medium; keybinding expectations and escape-decoding complexity. |
| T10 | Steering uses the existing string channel from the TUI composer only; acknowledgments are matched by text, and unacknowledged lines return to the composer. | Avoids a durable correlation-ID field for a display-only problem; FIFO text matching is sufficient for a local composer; no stdin steering path remains. | Low; duplicate-text edge case. |
| T11 | A new session is created eagerly and a task is optional in TUI/`-ui-json` mode. | The header and trace path are known immediately, and the user can start typing; an empty `session_start`-only log is valid. | Low. |
| T12 | One session per TUI process; no in-process session picker or switching. | Keeps the lock, store, workspace, and model inherited from one config; switching would be a second lifecycle. | Medium; a picker is a follow-on. |
| T13 | UI history is capped and may drop the oldest items; the log is never capped by the UI. | Bounds memory for long sessions while preserving the durable record for resume and inspection. | Low. |
| T14 | TUI supports new/resume/fork at startup, but no approval/sandbox or new tool capabilities. | UI completeness without changing the security model or product scope. | Low; isolation remains a separate decision. |
| T15 | `-ui-json` is the supported machine-readable TUI mode, with a versioned v1 JSONL command/frame/event protocol. | Gives coding agents a deterministic, PTY-free way to create and test the real view/host; local stdio only, no daemon/remote surface. | Medium; protocol consumers and schema compatibility. |
| T16 | Remove `-interactive` and the stdin steering reader in the TUI cutover; batch remains non-interactive. | Two interactive paths would drift and keep the v1 signal/steering complexity alive; the TUI fully replaces it. | Medium; breaking CLI flag/behavior for existing users. |
| T17 | Assistant text is markdown rendered by Glamour v2; tool output is preformatted; raw HTML is inert. | Glamour is Bubble Tea v2-compatible and covers CommonMark/GFM without an in-house renderer. | Medium; markdown dialect and style expectations. |
| T18 | `-ui-json` frames and the SVG `snapshot` are generated from the same Bubble Tea renderer output through `x/vt`. | The coding agent must see what the user sees; `x/vt` gives cell content/style/cursor without a PTY. `teatest/v2` alone captures only raw output. | Medium; `x/vt` is v0.1.0 and must pass M1 fidelity tests. |
| T19 | The model picker selects the model for the next run only; `run_start` records it; no mid-run switch and no schema change. | The v2 log already records per-run model/dialect, so the picker is a UI/host change, not a durable-schema change. | Medium; model identity/inheritance expectations. |
| T20 | The file picker inserts a workspace-relative path into the composer and reads no file contents. | Avoids inventing an attachment/projection contract; the agent can read the file with its existing tools when the message asks. | Low; users may expect content attachment. |
| T21 | Images and charts use ntcharts v2 `picture` (Kitty plus glyph fallback); images are display-only markdown blocks. No new tool or durable event. | Covers agent-generated chart images and image viewing without changing the agent capability or session schema; glyph fallback keeps unsupported terminals usable. | Medium; ntcharts API stability and Kitty/glyph behavior. |
| T22 | Widgets use Bubbles v2 components behind the TUI's overlay/focus manager. | The components are maintained and Bubble Tea v2-compatible; the TUI still owns workspace containment and the model registry. | Low; widget scope creep. |

### 5.2 Risks

| Risk | Mitigation |
|---|---|
| A crash leaves the terminal in raw mode, alternate screen, or with a hidden cursor. | Bubble Tea's `Program` owns setup/restore; quit/kill it on every path and wait for `Run` to return; recover at the UI and host-run boundaries; handle `SIGTERM`/`SIGHUP`; manual `stty -a` checks; document `stty sane`/`reset` as operator recovery. |
| Model/tool output injects terminal control sequences. | A single sanitizer before Glamour/Bubbles; strip ESC/C0/C1/bidi; fuzz it; never write raw transcript text; no OSC queries or clipboard access. Glamour/Bubbles output is trusted library ANSI. |
| Unicode width errors corrupt the layout. | Use Bubble Tea/ultraviolet width handling; tests for CJK, combining, emoji, and ZWJ; never split UTF-8; record any terminal-specific mismatch as an upstream limitation rather than adding a second width table. |
| The display path blocks the runner on a slow terminal. | Bounded display queue, Bubble Tea renderer diffing, 16 ms coalescing, and a synchronous callback contract; measure frame time; if it stalls, move queued rendering off the runner rather than dropping log durability. |
| Defaulting to the TUI surprises an interactive command that expected one-shot stdout. | Non-TTY invocations automatically batch; `-batch` forces one-shot on a terminal; docs/usage show both; integration tests cover the mode matrix. |
| Removing `-interactive` breaks existing scripts or habits. | Make it an explicit unknown-flag error, not a silent no-op; document `-batch` for one-shot and the TUI for interactive use; call out the breaking change in the release notes and README. |
| Bare Esc is confused with an Alt/Option prefix and interrupts accidentally. | Ctrl-C remains the authoritative global interrupt; bare Esc requires the 50 ms timeout and recognized Alt sequences never interrupt; raw Option-`[`/Option-`]` regression tests. |
| `-ui-json` becomes a de facto protocol that is hard to change. | Version every line (`v:1`), document v1 as the supported automation contract, require a v2 for incompatible changes, and cover the schema with black-box tests. |
| A headless `-ui-json` pass is mistaken for proof that the real terminal works. | The protocol runs the real Bubble Tea program/renderer and `x/vt`, but not raw mode, real signals, or Kitty graphics; keep the manual PTY matrix as a separate gate. |
| `x/vt` v0.1.0 has bugs or API churn. | Pin it, isolate it in `headless.go`/`screen.go`, validate a cell/style/cursor fixture matrix in M1, and fall back to a small ANSI parser or the tcell plan if it fails. |
| The Bubble Tea dependency tree is rejected or grows too large. | The §2.8 justification and binary-impact measurement; the tcell fallback is documented. Do not add a second framework in the same milestone. |
| ntcharts v2 API is still subject to change. | Pin v2.9.0, isolate chart/picture usage behind `internal/tui/image.go`/view helpers, and keep a glyph fallback. |
| Glamour renders something the sanitizer misses. | Sanitize before Glamour; treat Glamour output as trusted ANSI; test raw HTML, OSC/CSI text, and bidi; no OSC 8 links. |
| TUI multi-run semantics confuse budgets or crash recovery. | Keep v2 run events and cumulative limits unchanged; show turns/tokens per session; document that a message is a run; resume behavior is the same as v1. |
| Steering is lost or races with run completion. | Acknowledgment events; FIFO queue; return unacknowledged lines to the composer; tests that submit just before completion. |
| Large logs consume memory and make history slow. | History is a capped display projection; drop oldest UI items with a marker; the log stays on disk; measure a large fixture. |
| PTY behavior is not exercised in CI. | The headless Bubble Tea/`x/vt` harness covers cells, wrapping, styles, and alt-screen output; a manual terminal matrix covers raw mode, real resize/signals, and Kitty/glyph images; an optional `script(1)` smoke is not a gate. |
| Signal/key races around completion. | The controller ignores interrupts with no operation; the app waits for the run `Done` message before starting a new run; `SIGTERM` cancels and waits for a durable terminal event; no signal handler I/O. |
| TUI use encourages running an untrusted repository interactively. | Keep the README isolation warning prominent; the TUI adds no approval/sandbox and does not pass the provider token to tools. Use bwrap/Docker/VM for untrusted work. |
| A terminal or SSH link is slow and backpressures the run. | Streaming is display-only; the run still records complete responses; the UI may lag, but the log and resume are correct. Document the behavior. |
| Windows or non-ANSI terminals are unsupported. | Unix-only build assumptions, `TERM` checks, a clear startup error, and documentation. Windows remains unclaimed as in v1. |

### 5.3 Assumptions expensive to reverse

These are the assumptions a late correction would pay for across the runner
API, the CLI, the TUI, or the session lifecycle. Confirm each before the
milestone that freezes it.

| Assumption | If it is false | Confirm by |
|---|---|---|
| The TUI is the default only when stdin/stdout are terminals; `-batch` forces one-shot; non-TTY runs batch; there is no `-tui` flag. | The CLI defaults, task validation, scripting behavior, and docs change; a forced TUI would break pipes. | M2, before the default-TUI cutover. |
| `-interactive` is removed rather than deprecated; batch is non-interactive. | Users/scripts need a compatibility shim or a different flag; the CLI and test matrix change. | M2, before deleting the flag and steering reader. |
| A TUI process may perform multiple runs over one session, one per submitted message. | The CLI/run definition, UI lifecycle, limits, and tests change; a single-run TUI would not be conversational. | M2, before the default-TUI cutover. |
| The TUI adds no durable schema or new session event types. | The v2 reader, history, resume, and external tooling change; a schema version is needed. | M0, before display/history land. |
| The display event stream is the only runner-to-UI observation contract and `EmitText` is removed. | Every streaming call site and test changes; a second path could drift on final-text duplication. | M0. |
| History is reconstructed from the log and is display-only; UI state is never a source of truth. | Resume/restart needs UI snapshots, and the log is no longer the sole record. | M0/M2. |
| Bubble Tea v2 is the approved application framework; Bubbles/Glamour/ntcharts are approved; `x/vt` is the headless screen; tcell is the fallback. | The app/view layer, dependency tree, and headless path change; a fallback decision is needed. | M1, before the dependency lands. |
| `-ui-json` v1 is a stable, supported automation contract, local to stdio, and includes styles/cursor/image metadata plus `snapshot`. | Coding agents and tests depend on a schema change; a remote/attach protocol or v2 migration is needed. | M2, before the protocol is documented as supported; M4/M5 fill styles/images. |
| Assistant text is markdown rendered by Glamour v2; raw HTML is inert; tool output is preformatted. | The renderer, style contract, and snapshot change; a different markdown library or dialect is needed. | M4. |
| The model picker selects the next run's model only; `run_start` is authoritative; no mid-run switch. | Session inheritance, host adapter lifecycle, and the UI header change; a durable model-selection event is needed. | M4. |
| Images/charts use ntcharts `picture` with Kitty and glyph fallback; images are display-only workspace files. | Image support becomes an agent capability or a schema change; protocol scope expands. | M5. |
| `Esc` cancels/interrupts contextually, first/second `Ctrl-C` interrupts/cancels, `Ctrl-D` quits; a clean quit exits 0. | Keybindings, signal handling, and exit codes change; automation expectations change. | M2. |
| Steering remains a string channel and unacknowledged lines return to the composer. | The steering API needs IDs or a durable correlation field; the log payload changes. | M2. |
| One process owns one session and one store writer; there is no session picker or attach protocol. | Locking, lifecycle, and UI navigation change; a picker is a second session runtime. | M2. |
| TUI history caps may hide old items while the log remains complete. | The UI needs a paged log reader or a separate transcript store; memory/latency contracts change. | M3. |
| The TUI remains Unix-only and local; no Windows or remote/web support. | The terminal layer, signals, and test matrix expand substantially. | M1/M2. |
| Isolation/approval remains external; the TUI does not add a security boundary. | Tool, process, credential, and approval contracts change; this is the highest-cost product reversal. | M2, before recommending real-work use. |

### 5.4 Prerequisites and open confirmations

1. **Default mode:** confirm that the TUI is the default when stdin/stdout are
   terminals, that `-batch` is the opt-out name, and that non-TTY invocations
   batch automatically.
2. **`-interactive` removal:** confirm that the flag and stdin steering reader
   are deleted in the TUI cutover, not deprecated. Batch becomes the only
   non-interactive one-shot path.
3. **Key contract:** confirm `Esc` cancels/interrupts contextually, first
   `Ctrl-C` interrupts, second `Ctrl-C` cancels the run, `Ctrl-D` quits, and
   bare Esc is guarded by the 50 ms timeout/Alt-sequence handling.
4. **Automation protocol:** confirm the `-ui-json` name, `-ui-size`, and the v1
   JSONL command/frame/event/snapshot schema, including style spans, cursor,
   image metadata, and the SVG snapshot. This is the coding-agent test and
   design surface.
5. **Framework approval:** accept Bubble Tea v2, Bubbles v2, Glamour v2, and
   ntcharts v2 under coding standard §3, or block the plan. Confirm that no
   second framework is adopted. Confirm `x/vt` v0.1.0 for the headless screen
   and `teatest/v2` as test-only.
6. **Multi-run semantics:** confirm that a TUI process may execute one run per
   submitted message over one session, rather than one run per process.
7. **Session ownership:** confirm that the TUI/`-ui-json` own stdin/stdout, the
   TUI is not scriptable, and `-batch` is the plain-text output path.
8. **Task rules:** confirm that a task is optional at TUI/`-ui-json` startup
   for new, resume, and fork, and that a new session is created eagerly.
9. **Scope:** confirm the first usable version (M2/M3) has no mouse, images,
   rich text, widgets, split panes, session picker, suspend, Windows, or
   remote/web. Confirm M4 (markdown, table, file picker, model picker) and M5
   (images/charts) as post-first-usable milestones.
10. **History policy:** confirm that prior final text is shown once via the
    assistant history item and not reprinted; the UI may drop the oldest items
    under the display caps.
11. **Fork in the first usable version:** confirm that `-fork` is in M2, or
    defer it to M3; the plan currently includes it.
12. **Terminal baseline:** confirm ANSI/xterm-class terminals and tmux as the
    support target, with a clear error for `TERM` unset/`dumb` in TUI mode.
13. **Real-provider smoke:** make provider credentials available for the M3
    TUI smoke (new, stream, tool, steer, interrupt, resume, fork). If a hosted
    model does not stream, record the limitation.
14. **Rich text/widgets:** confirm Glamour v2 + Bubbles v2, the table overlay,
    the workspace file picker, and the per-run model picker as M4 scope.
    Confirm raw HTML is inert and tool output stays preformatted.
15. **Model picker:** confirm it selects only the next run's model, is disabled
    during a run, and does not require a durable schema change or mid-run
    adapter switch.
16. **Images/charts:** confirm ntcharts `picture` (Kitty + glyph fallback) and
    agent-generated chart images as M5 scope. Confirm SVG/network images and a
    model-provided chart DSL are out.
17. **Agent-visible UI:** confirm that `-ui-json` frames plus the SVG snapshot
    are the supported coding-agent surface for styles, layout, and images, and
    that they are generated from the same Bubble Tea renderer output through
    `x/vt`.

## 6. Follow-ons

After the default-TUI cutover and the M4/M5 rich-content milestones are
reliable, the next candidates are: a session picker/switcher, transcript
search, tool-detail paging or a pager handoff, mouse scrolling (not mouse
reporting for selection), a theme/color scheme, suspend/resume (`Ctrl-Z`), a
diff-focused file-edit view, syntax highlighting in code blocks, a
model-provided native chart DSL rendered by ntcharts, iTerm2/sixel image
protocols, SVG image support, `-ui-json` protocol extensions (only under a new
version), and an attach/remote protocol. Each is a separate contract with its
own lifecycle and security review. Approval/sandbox and additional agent
capabilities remain separate product decisions; they are not implied by the
TUI. The `-interactive` removal and the Bubble Tea dependency decision are part
of the cutover/M1 gate, not follow-ons; markdown/widgets and images/charts are
M4/M5, not follow-ons.
