# aharness TUI (shipped)

- **Date:** 2026-11-11
- **Status:** implemented (TUI build plan M0–M5) with the catalog-backed model
  picker from [model discovery](model-discovery-build.md). The full contract and
  milestone rationale are in [tui-build.md](tui-build.md); this document records
  the shipped surface and verification status.
- **Verified:** fake-provider TUI/`-ui-json` integration (streaming, tools,
  steering, interrupts, resume, fork, signals), headless `x/vt` frames and SVG
  snapshots, race tests, and a manual PTY smoke under `script(1)` at 80x24.
  The real-provider TUI smoke remains pending provider credentials.
- **Not verified:** Kitty graphics and the real-terminal 200x60/tmux matrix
  (manual, needs a Kitty-capable terminal and tmux); real-provider streaming
  against the hosted endpoint.

## Modes

| Invocation | Mode |
|---|---|
| `-version` | print build info and exit 0 before mode selection |
| `-ui-json` | headless TUI on stdio; no TTY/`TERM` requirement; no alternate screen or raw mode |
| `-batch` | one-shot CLI, stdout/stderr contract and exit codes unchanged |
| stdin and stdout are terminals | TUI (default) |
| either is not a terminal | batch automatically |

`-interactive` is gone; it is an unknown-flag error. Batch mode is
non-interactive and has no steering.

TUI and `-ui-json`:

- create a new session eagerly and allow an omitted task;
- always stream assistant text; `-stream=false` is a config error;
- auto-submit `-task`/`-task-file` after the first frame;
- allow new/resume/fork, with `-fork` optionally having no task;
- never print final assistant text to stdout;
- exit 0 on a clean idle quit, 130 for a quit while a run is active or on
  `SIGTERM`/`SIGHUP`, 2 for config/TTY/`TERM`/protocol usage errors, and 1 for
  a terminal/setup/sync/internal failure.

The session log and the CLI batch contract are unchanged. A TUI process owns
one session, one exclusive `flock`, and one writer for its lifetime; each
submitted message while idle starts one run.

## Keys

| Key | Idle | Run active |
|---|---|---|
| `Enter` | submit a new run | submit steering |
| `Esc` | close overlay, then clear a non-empty composer; never quits | close overlay; otherwise interrupt the in-flight operation |
| `Ctrl-C` | clear a non-empty composer; otherwise show a quit hint | first: interrupt the operation; second: cancel the run |
| `Ctrl-D` | quit 0 | cancel the run and quit 130 |
| `Ctrl-L` | full repaint | full repaint |
| `Ctrl-U` | clear the composer | clear the composer |
| `Ctrl-A`/`Ctrl-E` | composer start/end | composer start/end |
| `Ctrl-W`/`Ctrl-K` | delete previous word / to end | delete previous word / to end |
| `Up`/`Down` | input history | input history |
| `PgUp`/`PgDn` | scroll the transcript | scroll the transcript |
| `Ctrl-R` | toggle rich/raw assistant rendering; in the model picker, refresh the catalog | toggle rich/raw |
| `Ctrl-T` | most recent markdown table overlay | table overlay |
| `Ctrl-G` | image viewer for the most recent image block | image viewer |
| `Ctrl-P` | catalog-backed model picker for the next run | disabled with a notice |
| `Ctrl-O` | workspace file picker (Bubbles list; see note) | disabled with a notice |
| `F1` | help overlay | help overlay |
| `Ctrl-X` | details overlay | details overlay |
| `Tab` | composer tab (four spaces) | composer tab (four spaces) |
| Overlay `Tab`/`Shift-Tab` | move focus | move focus |
| Overlay `Esc` | close the top overlay | close the top overlay |

A bare `Esc` is recognized only after the 50 ms escape timeout. Option/Alt
sequences, including Option-`[` and Option-`]`, are decoded as modified keys
and never interrupt; `Ctrl-C` remains the authoritative global interrupt.

## Model picker

`Ctrl-P` opens the catalog-backed model picker while idle; a run disables it
with a notice. Entries are the usable models from models.dev, sorted by ID. Each
row shows the display name, ID, dialect, context and input limits, advertised
output ceiling, and a `deprecated` / `alpha` / `beta` marker and a `translated`
marker when applicable. Deprecated models remain selectable and are never chosen
automatically. The advertised output ceiling is catalog metadata, not the
effective request budget: unless `-max-output-tokens` is explicit, output is
unset and the request omits the field.

Selecting a model changes only the next run's dialect, base URL, and limits;
the current run and existing log are untouched. An explicit context limit is
clamped to the new model's window; an explicit output budget is sent unchanged.
The details overlay (`Ctrl-X`) shows the effective values for the current or
next run and displays `max output: unset` when no explicit budget applies.

`Ctrl-R` in the picker performs the same unconditional refresh as
`-refresh-models`: the footer shows `refreshing models…`, a second `Ctrl-R`
while the request is in flight is ignored, and closing the picker cancels the
request without changing the loaded entries. On success the entries are
replaced in place, preserving the highlighted model and filter when they still
exist; otherwise the first usable model is highlighted. On failure the loaded
entries remain and a notice reports the error. Outside the picker, `Ctrl-R`
keeps its rich/raw meaning. With the picker open, `-ui-json` `ctrl-r` also means
refresh; the protocol version does not change.

On resume without an explicit model, the picker may skip the network. If the
catalog is unavailable, it contains only the recorded current model with a
notice that switching is disabled; resume still works. `Ctrl-R` retries the
fetch and, on success, expands the picker to the usable catalog and enables
switching. A recorded model absent from the catalog is still shown and usable
from the session record; switching to another model requires catalog metadata.

## `-ui-json` protocol v1

Every stdin line is one JSON object with `"v":1` and `"type"`. Unknown fields
are ignored; an unknown version, type, or key is a protocol error (exit 2).
Commands: `text`, `key`, `paste`, `resize`, `wait`, `frame`, `snapshot`,
`quit`; EOF is equivalent to `quit`. Keys accepted by `key` are the shipped
key table plus `enter`, `esc`, `up`, `down`, `left`, `right`, `home`, `end`,
`pgup`, `pgdown`, `backspace`, `delete`, `tab`, `shift-tab`, `ctrl-c`,
`ctrl-d`, `ctrl-l`, `ctrl-u`, `ctrl-w`, `ctrl-a`, `ctrl-e`, `ctrl-k`, `ctrl-p`,
`ctrl-o`, `ctrl-r`, `ctrl-t`, `ctrl-g`, `ctrl-x`, and `f1`.

Output lines:

- `frame`: `seq`, `width`, `height`, `state`, `overlay`, `cursor`
  (`row`, `col`, `visible`, `shape`), `lines` (exactly height entries), `styles`
  (coalesced `row`, `col`, `width`, `text`, `fg`, `bg`, `attrs`), `images`
  (`id`, `path`, `alt`, `row`, `col`, `width`, `height`, `protocol`),
  `color_profile`, `no_color`, `image_protocol`, `styles_truncated`. Frames
  contain no ANSI and no raw image bytes.
- `event`: `run_started`, `run_finished`, `tool_started`, `tool_finished`,
  `model_changed`, `notice`, `error`, with the fields documented in
  [tui-build.md §2.7](tui-build.md#27-machine-readable-ui-mode---ui-json).
- `snapshot`: a deterministic SVG of the current `x/vt` cells at the current
  frame `seq`; it contains no timestamps, random IDs, durations, or ANSI, and
  does not emit an extra frame.
- `error`: `code` and `message` for protocol/UI errors; a wait timeout emits
  `error{code:"timeout"}` and leaves the run active.

Frames are emitted after every view-changing command, after a completed
`wait`, and after each non-text semantic transition. Text deltas update the
model but do not emit a frame; request `frame` to snapshot streaming text.
Frames and snapshots are deterministic for a fixed provider/tool response,
size, color profile, and image protocol: `-ui-json` hides the session ID,
elapsed time, and durations. `-ui-json` always uses glyph image mode.

`-ui-size WxH` sets the initial frame size (default `100x30`, valid
`20x5`–`500x200`); `resize` is bounded the same way.

## Caps

| Cap | Value | Behavior |
|---|---|---|
| Task/steering message | 64 KiB | Longer is rejected with a notice; the run continues. |
| Transcript items / text | 20,000 / 16 MiB | Oldest UI items are dropped with an "older history omitted" marker; the log is unchanged. |
| Assistant text per item | 64 KiB | Truncation marker; the canonical response remains in the log. |
| Markdown source / blocks / nesting | 64 KiB / 4,000 / 16 | Over-cap input falls back to plain text with a notice. |
| Table columns / rows | 12 / 500 | Column width cap 40 cells; the overlay scrolls horizontally. |
| File picker entries / filter | 2,000 per directory / 64 bytes | No recursion, no file contents; `.git`, symlinks, control-character names, and escapes are rejected. |
| Overlay stack | 4 | Deeper requests close the oldest overlay. |
| Image file / dimensions / pixels | 8 MiB / 4096×4096 / 16 Mi | PNG/JPEG/GIF only; `DecodeConfig` runs before decode. |
| Images per transcript / thumbnail | 32 / 60×20 cells | Older images fall off; thumbnails are half-block glyphs. |
| Tool output | 4 KiB or 40 lines folded | Expanded output remains in the log, up to the logged cap. |
| Tool arguments preview | 2 KiB | Full arguments remain in `model_response`. |
| Input history | 100 entries | In-memory only. |
| Minimum terminal | 40×10 | Smaller shows a resize message and keeps quit/resize keys. |
| Frame coalescing | 16 ms | Text deltas coalesce; state changes flush immediately. |
| Escape timeout | 50 ms | Bare Esc; modified sequences never interrupt. |
| Display queue | 256 events | Backpressure; display text deltas are not durable. |
| `-ui-json` command line / text | 1 MiB / 64 KiB | Longer is a protocol error. |
| `-ui-json` frame line | 1 MiB | Over-cap emits `frame_too_large` and continues. |
| `-ui-json` style spans | 16,000 or 256 KiB | Excess styles are dropped and `styles_truncated` is true; `lines` stay complete. |
| `-ui-json` wait timeout | 30 s default, 300 s max | Timeout emits an error and does not exit. |
| `-ui-json` snapshot | 512 KiB SVG | Over-cap emits `snapshot_too_large` and continues. |

The file picker is built on Bubbles' list/filter component rather than
Bubbles' `filepicker`, because `filepicker` reads directories directly and
cannot enforce `os.Root`, `.git`, and symlink containment. It lists only
`tools.Workspace.ListDisplayDir` entries and inserts a workspace-relative path;
it never reads file contents.

## Terminal safety and images

All model/tool/session/error text passes through the TUI sanitizer before it
reaches Glamour, Bubbles, or a transcript cell: invalid UTF-8 is repaired,
tabs expand to four spaces, C0/C1 controls and ESC become visible glyphs,
bidi overrides/isolates are stripped, and OSC/CSI/DCS text cannot become a
control. Raw HTML is never interpreted. Glamour's OSC 8 hyperlinks are
stripped. Tool output is preformatted and never parsed as markdown.

Images are display-only workspace files resolved with `os.Root`,
`filepath.IsLocal`, no `.git` or symlink components, and no network fetch.
SVG/XML and unsupported formats become placeholders. ntcharts `picture`
renders transcript thumbnails as half-block glyphs; the viewer can use Kitty
when `AHARNESS_IMAGE_PROTOCOL=auto|kitty` selects it. `glyph` and `none` force
the universal fallback or placeholders; `-ui-json` always forces glyph. Kitty
placements are cleared on viewer close, resize, and exit.

Mouse and focus reporting are disabled. Bubble Tea/ultraviolet owns raw mode,
the alternate screen, bracketed paste, resize, cursor, and restoration.
