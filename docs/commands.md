# Slash commands (shipped)

- **Date:** 2026-11-11
- **Status:** implemented. This document is the reference for composer slash
  commands in the interactive TUI and the `-ui-json` headless driver.
- **Related:** [tui.md](tui.md) for the shipped TUI surface, keys, model
  picker, thinking effort, and `-ui-json` protocol;
  [architecture.md](architecture.md) for the loop and CLI contracts;
  [limits.md](limits.md) for caps.

Batch mode has no composer and therefore no slash commands. A command is
display/control input and does not append a command event of its own.
Next-run commands change the next `run_start`; session commands replace the
active store; run-control commands (`/interrupt`, `/cancel`, `/quit`) act
through the runner and produce the same durable events as their key
equivalents.

## Invocation

- Commands are submitted from the composer. A seeded `-task` or `-task-file`
  is always literal text, even when it starts with `/`.
- The first word after `/` must match a registered command name or alias.
  Names are case-insensitive; arguments are case-sensitive. Only recognized
  commands are intercepted; any other text, including an unknown `/word`, is
  submitted as a normal message.
- Prefix a recognized command with a space to send it literally (for example,
  as steering while a run is active).
- One optional argument is accepted. Leading and trailing whitespace is
  trimmed. `/model` and `/effort` validate their argument; other commands
  ignore extra words.
- The normal 64 KiB composer/submission cap applies.
- Commands work while a run is active. Next-run settings, editor-only actions,
  and session switching (`/model`, `/effort`, `/files`, `/refresh`, `/new`,
  `/resume`) are disabled with a status-line notice; display and run-control
  commands still work.
- Command feedback uses the status line or an overlay. `/model` and `/effort`
  also emit display-only `model_changed` and `notice` events in `-ui-json`
  mode; `/new` and `/resume` emit a display-only `notice` event with kind
  `session`. None is written to the session log.

## Autocomplete

Typing `/` opens a completion menu filtered by command name or alias as the
user types.

| Key | Action |
|---|---|
| `Up` / `Down` | Move the highlighted command (clamped, no wrap) |
| `Tab` | Complete the highlighted command; `/model` and `/effort` get a trailing space |
| `Enter` | Run the highlighted command |
| `Shift-Enter` / `Alt-Enter` | Insert a composer newline instead of running |
| `Esc` | Dismiss the menu without clearing the composer |

A space after the command name starts argument mode and closes the menu.
History recall does not reopen the menu until the recalled text is edited.
The menu shows at most eight rows, scrolls to keep the selection visible, and
shrinks as needed so the header, at least one transcript row, the composer, and
the status line remain visible. The help overlay (`F1`) lists every canonical
command.

In `-ui-json` mode, autocomplete uses ordinary input: send `text` to type and
`key` `enter`, `tab`, `up`, `down`, or `esc` to drive it. There is no protocol
change and frames expose the resulting composer, menu rows, and overlay.

## Summary

| Command | Argument | Key equivalent | Scope |
|---|---|---|---|
| `/help` | none | `F1` | help overlay with keys and commands |
| `/model [id]` | optional exact model ID | `Ctrl-P` | next-run model; argument selects directly |
| `/effort [value]` | optional effort value, or `default` | `F2` | next-run thinking effort |
| `/provider` | none | `Ctrl-X` | active provider plus configuration details |
| `/details` | none | `Ctrl-X` | session, terminal, and configuration details |
| `/new` | none | none | replace the active session with a fresh local session (idle) |
| `/resume` | none | none | pick a previous session in this workspace and resume it (idle) |
| `/files`, `/file` | none | `Ctrl-O` | workspace file picker (idle) |
| `/table` | none | `Ctrl-T` | most recent markdown table |
| `/agent-hub` | none | none | live delegated-child roster; Enter focuses a child for viewing and steering; works during a run |
| `/agents` | none | none | agent definitions browser; edits model, effort, max_turns, and enabled |
| `/image` | none | `Ctrl-G` | image viewer for the most recent image block |
| `/rich` | none | `Ctrl-R` toggles | set rich assistant rendering |
| `/raw` | none | `Ctrl-R` toggles | set raw assistant rendering |
| `/repaint` | none | `Ctrl-L` | full terminal repaint |
| `/refresh` | none | `Ctrl-P` then `Ctrl-R` | refresh the model catalog and open the picker (idle) |
| `/interrupt` | none | `Esc` during a run | interrupt the in-flight operation |
| `/cancel` | none | second `Ctrl-C` | cancel the active run |
| `/quit`, `/exit` | none | `Ctrl-D` | quit; cancels an active run and exits 130 |

## Session and configuration

### `/help`

Opens the help overlay, listing the shipped keys and every slash command.
Works while idle or running. `Esc` closes the overlay.

### `/provider`

Reports the active provider in the status line and opens the details overlay.
The overlay shows the provider and base URL under configuration. This is
informational in v1: `-provider` / `AHARNESS_PROVIDER` selects the single
supported provider (`opencode-go`) at process start, so there is no in-session
provider switch. An explicit provider on resume or fork must match the
recorded one; omitting it inherits the record.

### `/details`

Opens the details overlay: session and trace paths, workspace, effective
model and dialect, thinking effort, cumulative turns and tokens, terminal
capabilities, provider and base URL, prompt/toolset/harness versions, state
directory, context and output limits, and configured run/model timeouts.
Works while idle or running. `Esc` closes the overlay.

### `/new`

Creates a fresh session log in the same state directory and workspace,
replacing the active session for the rest of the process. The transcript,
cumulative counters, run numbering, and display caches are reset. The current
next-run model and thinking effort are written into the new `session_start`, so
the first run starts with the settings shown in the status line. The previous
session is closed and remains available to `/resume` (or `-resume` in a later
process). Runs must be idle: while a run is active the command reports that
session switching is disabled and changes nothing.

`/new` writes only the new session's `session_start`; it emits no command event
and does not copy history or todo state from the old session.

### `/resume`

Opens a picker of previous sessions recorded for the current workspace, newest
first. The active session is excluded. Each row shows the first user message
(or a short session ID when there is none), the session ID prefix, the last
modification time, and the recorded model. Typing filters the rows; `Up`/`Down`
select, `Enter` resumes, and `Esc` closes the picker without switching.

On selection the harness opens the log under `<state>/sessions` with an
exclusive lock, validates it, and loads its history and cumulative counters.
Only after those steps succeed does it close the current log and install the
resumed session, so a locked, corrupt, or incompatible session leaves the
active one untouched and reports the failure in the status line. A session
whose recorded workspace differs from the current canonical workspace is not
listed and cannot be resumed here.

The resumed session inherits its recorded model, dialect, base URL, context and
output limits, and thinking effort. Explicit run budgets and timeouts from the
current invocation remain in effect, matching the `-resume` CLI precedence.
A display-only `notice` event with kind `session` reports success. Idle only;
the active run keeps the lock and store.

### `/quit` and `/exit`

Idle: exits 0, like `Ctrl-D`. Active: cancels the run, writes the terminal
events, and exits 130, like `Ctrl-D` during a run.

## Next-run settings

`/model` and `/effort` change only the next run. They never change the active
run, the existing transcript, or historical log events. During a run they are
disabled with a notice.

### `/model [id]`

Without an argument, opens the catalog-backed model picker described in
[tui.md § Model picker](tui.md#model-picker). With an ID, selects that exact
catalog entry for the next run.

- The ID must be a usable catalog entry. Deprecated models remain selectable
  and are labeled. Unavailable or unknown models are rejected with a notice.
- Selecting a model changes the next run's dialect, base URL, context/input
  limits, and advertised effort values.
- An explicit context limit is clamped to the new model's window; an explicit
  output budget is sent unchanged.
- A next-run effort is kept when the new model advertises it; otherwise it is
  cleared to the provider default with a warning. The next `run_start` records
  the effective values.
- On resume without an explicit catalog fetch, only the recorded model may be
  available; switching is disabled until the picker's `Ctrl-R` refresh
  succeeds.

### `/effort [value]`

Without an argument, opens the thinking-effort picker described in
[tui.md § Thinking effort](tui.md#thinking-effort). With a value, applies it
directly to the next run.

- Accepted values are the effective model's advertised effort values, in
  catalog order. There is no harness-wide enum.
- `default` clears the setting; the next request omits the effort field and
  uses the provider/model default.
- An unsupported value is rejected with a notice naming the model and its
  advertised values; a model with no effort values reports that it advertises
  none.
- Selecting an effort with `F2` or `/effort` emits a display-only `notice`
  event with kind `thinking_effort` in `-ui-json` mode. The next `run_start`
  records the effective value.

### `/refresh`

Opens the model picker and performs the same unconditional catalog refresh as
`Ctrl-R` in the picker: the same 15-second timeout, 16 MiB body cap, and
atomic cache replacement as `-refresh-models`, with no stale fallback. Idle
only.

The footer shows `refreshing models…` while the request is in flight. A second
refresh is ignored. Closing the picker cancels the request without changing
the loaded entries. Success replaces the entries in place and reports
`refreshed <n> models`; failure keeps the loaded entries and reports the error
in the status line.

## Workspace and content

### `/files` (alias `/file`)

Opens the workspace file picker, equivalent to `Ctrl-O`. Idle only. The picker
lists the current directory through `tools.Workspace.ListDisplayDir` and
inserts a workspace-relative path into the composer. It never reads file
contents, does not recurse, rejects `.git`, symlinks, and control-character
names, and caps a directory at 2,000 entries. `Esc` closes it without changing
the composer.

### `/table`

Opens the most recent markdown table from assistant text, equivalent to
`Ctrl-T`. If the transcript has no table, the status line reports it and no
overlay opens. The overlay scrolls horizontally; displayed columns and rows are
capped by [limits.md](limits.md).

### `/image`

Opens the viewer for the most recent workspace image block, equivalent to
`Ctrl-G`. If the transcript has no image, the status line reports it. The
viewer is display-only: images are resolved with `os.Root` and
`filepath.IsLocal`, never fetched from the network, and unsupported or
oversized files become placeholders. `AHARNESS_IMAGE_PROTOCOL` selects auto,
Kitty, glyph, or no rendering; `-ui-json` always uses glyph mode.

## Display

### `/rich` and `/raw`

Set rich or raw assistant rendering. `Ctrl-R` toggles between them. This
changes only display rendering (Glamour markdown versus source text); it does
not change the transcript content, session log, or model input. The status
line shows the active mode.

### `/repaint`

Requests a full terminal repaint, equivalent to `Ctrl-L`. It does not modify
the transcript, composer, run, or session.

## Delegation

### `/agent-hub`

Opens a live roster of the delegated children in the current run: agent,
intent, model, effort, elapsed, context tokens, turns, cost, and status, with
columns dropping in a documented order as the terminal narrows. It is
display-only and works while a run is active, because it is a display command.
`Up`/`Down`/`PgUp`/`PgDn`/wheel scroll the rows; eight rows are visible plus a
header and a `+N more` row. `Esc` closes it and does not interrupt a child.
Elapsed time is the TUI's own clock from the start event, so it keeps ticking
between progress events. Rows are keyed by child session and a finished child
keeps its terminal status for the rest of the run. Idle, it reports that no
child is running. Trailing words are accepted and ignored, like every
no-argument command. It adds no protocol event: the roster appears in
`-ui-json` `frames` and `snapshots`.

`Enter` on a row opens that child's transcript and focuses it. While the child
runs, composer input is steering for that child rather than the parent: the
child consumes it at its next turn boundary and records it with
`source=subagent-steer`, which the child view labels `delegation steer`. The
parent transcript is unchanged. A finished child opens read-only and rejects
steering with a notice. `Esc` returns to the parent without interrupting the
child; with a non-empty composer it clears the draft first, and a second `Esc`
at the parent interrupts as usual. The focused view is not an overlay, so
`-ui-json` renders it in frame lines and reports `overlay: "none"`.

### `/agents`

Opens the agent definitions browser: name, source (`bundled` or `user` with the
file path), effective model and effort (`inherit` when absent, `default` after a
model switch), tool count, `max_turns`, and status (`active`, `disabled`, or
`invalid`). A `*` marks a value set by an operator override. `Enter` expands a
row in place to show the tool list, the first prompt line, each setting's
provenance (override, definition, or inherit), the declared value an override
replaced, and, for a malformed file, the parse error; `Esc` closes.

The four routing settings are editable in place: `m` edits `model`, `e` edits
`thinking_effort`, `t` edits `max_turns`, `d` toggles `enabled`, and `r`
removes every override for the row. Empty input clears that override field. The
editor shows its prompt and any error in a footer; `Enter` saves and `Esc`
cancels the edit without closing the browser. Rows are sorted by name. An
invalid file appears as an `invalid` row while the bundled agent of the same
name stays active.

A save rewrites the active config file (`-config`/`AHARNESS_CONFIG`, default
`<state-dir>/config.json`) with `agent_overrides`, atomically and in 0600, and
preserves every other key. A path inside the workspace is refused, because
operator settings must not become repository-controlled state, and `-config ""`
disables the file, so the browser reports that settings cannot be saved. A saved override applies to the next
process: the browser shows it immediately, but the catalog loaded for this
process is unchanged, so neither a child in flight nor a new run in this process
is affected. The tool grant and prompt body stay file-only; `/agents` cannot
widen either. An invalid value or a failed write is reported in the footer, and
a malformed config file is left untouched.

## Run control

### `/interrupt`

Interrupts the in-flight model call or tool in an active run, equivalent to
`Esc` during a run after overlays and the completion menu are closed. An
interrupted model call appends `interrupt` and an `interrupt-hint` message and
continues without counting a turn; an interrupted tool kills its process
group, pairs the call with `canceled`, and continues. Idle: reports that there
is no operation to interrupt.

### `/cancel`

Cancels the active run, equivalent to the second `Ctrl-C`: the runner writes
`final` and `run_end`, syncs, removes the work directory, and returns the TUI
to idle. Idle: reports that no run is active.

## Examples

```text
/model deepseek-v4.1-flash  select a catalog model for the next run
/effort high                set the next run's thinking effort
/effort default             clear the effort and use the provider default
/new                        start a fresh session in this workspace
/resume                     pick a previous session for this workspace
/files                      insert a workspace-relative path into the composer
/agent-hub                  live roster of the children in the current run
/agents                     agent definitions and settings
/interrupt                  stop the current model call; the run continues
/cancel                     end the active run and return to idle
/quit                       exit; an active run is canceled with exit code 130
```

## `-ui-json` mapping

Commands are composer text, not a new protocol command. Send:

```json
{"v":1,"type":"text","text":"/model"}
{"v":1,"type":"key","key":"enter"}
```

The same text/key stream drives autocomplete (`tab`, `up`, `down`, `esc`).
Resulting overlays appear in `frame.overlay` (`help`, `details`, `model`,
`effort`, `session`, `file`, `table`, `image`, `agent-hub`, or `agents`); status
feedback appears in the frame lines. `/model` and `/effort` also emit the existing `model_changed`
and `notice` events. `tool_started` and `tool_finished` gain an optional
`subagent_tasks` array for a `delegate` call; there is no new event kind and no
protocol version bump. `/new` and `/resume` emit a `notice` event with kind
`session` after a successful switch. The protocol version stays 1.

## Caps

The command surface adds no durable cap. Composer submissions use the existing
64 KiB task/steering cap, the completion menu is a display-only list, and
overlays, tables, and images keep their existing display caps. The `/resume`
picker scans at most 64 events from each candidate log and returns at most 100
sessions per workspace; longer first messages are sanitized and cut for the row
preview. The canonical values live in [limits.md §6](limits.md#6-tui-and--ui-json).
Commands write no command event; run-control commands cause the documented
runner events, and session commands write only a new session's `session_start`.
