# aharness TUI (shipped)

- **Date:** 2026-11-14
- **Status:** implemented (TUI build plan M0–M5) with the catalog-backed model
  picker from [model discovery](plans/model-discovery-build.md), the thinking
  effort controls from [thinking effort](plans/thinking-effort-build.md), and
  the transcript folding from [transcript folding](plans/transcript-folding-build.md).
  The full contract and milestone rationale are in
  [tui-build.md](plans/tui-build.md); this document records the shipped surface
  and verification status.
- **Verified:** fake-provider TUI/`-ui-json` integration (streaming, tools,
  steering, interrupts, resume, fork, signals, and `F3` folding), headless
  `x/vt` frames and SVG snapshots, race tests, and a manual PTY smoke under
  `script(1)` at 80x24. The real-provider TUI smoke with a reasoning-capable
  model remains pending provider credentials.
- **Not verified:** Kitty graphics and the real-terminal 200x60/tmux matrix
  (manual, needs a Kitty-capable terminal and tmux); real-provider streaming and
  transcript folding against the hosted endpoint.

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
- show model thinking as a separate, display-only `thinking` transcript block
  under the run it belongs to, folded by default (see [Thinking](#thinking));
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
| `Shift-Enter`/`Alt-Enter` | insert a composer newline | insert a composer newline |
| `Esc` | close overlay, then clear a non-empty composer; never quits | close overlay; otherwise interrupt the in-flight operation |
| `Ctrl-C` | clear a non-empty composer; otherwise show a quit hint | first: interrupt the operation; second: cancel the run |
| `Ctrl-D` | quit 0 | cancel the run and quit 130 |
| `Ctrl-L` | full repaint | full repaint |
| `Ctrl-U` | clear the composer | clear the composer |
| `Ctrl-A`/`Ctrl-E` | composer start/end | composer start/end |
| `Ctrl-W`/`Ctrl-K` | delete previous word / to end | delete previous word / to end |
| `Up`/`Down` | input history | input history |
| `PgUp`/`PgDn` | scroll the transcript | scroll the transcript |
| Wheel | scroll the transcript or the top overlay | scroll the transcript or the top overlay |
| `Shift`+drag, `Shift`+middle-click | terminal-side text selection and paste (the mouse capture takes both otherwise) | terminal-side text selection and paste |
| `Ctrl-R` | toggle rich/raw assistant rendering; in the model picker, refresh the catalog | toggle rich/raw |
| `Ctrl-T` | most recent markdown table overlay | table overlay |
| `Ctrl-G` | image viewer for the most recent image block | image viewer |
| `Ctrl-P` | catalog-backed model picker for the next run | disabled with a notice |
| `Ctrl-O` | workspace file picker (Bubbles list; see note) | disabled with a notice |
| `F1` | help overlay | help overlay |
| `F2` | thinking-effort picker for the next run | disabled with a notice |
| `F3` | expand/collapse all thinking and tool blocks; the mode is sticky | same |
| `F4` | edit the composer in `$VISUAL`/`$EDITOR` | disabled with a notice |
| `Ctrl-X` | details overlay | details overlay |
| `Tab` | composer tab (four spaces) | composer tab (four spaces) |
| Overlay `Tab`/`Shift-Tab` | move focus | move focus |
| Overlay `Esc` | close the top overlay | close the top overlay |

A bare `Esc` is recognized only after the 50 ms escape timeout. Option/Alt
sequences, including Option-`[` and Option-`]`, are decoded as modified keys
and never interrupt; `Ctrl-C` remains the authoritative global interrupt. The
help overlay (`F1`) lists the shipped keys and slash commands, including `F2`,
`F3`, `F4`, and the overlay keys. While the slash-command menu is open,
`Up`/`Down` move its selection, `Tab` completes, `Enter` runs the highlighted
command, and `Esc` dismisses the menu before the normal `Esc` action.

The composer is a multi-line text area. It grows with the draft up to six
rows, and past the sixth row it scrolls internally. `Shift-Enter` and
`Alt-Enter` insert a newline; plain `Enter` submits or steers. `F4` writes the
current draft to a temporary Markdown file, runs `$VISUAL` (or `$EDITOR` when
`$VISUAL` is unset) in the terminal, and replaces the composer with the edited
text when the editor exits successfully. A failed editor leaves the draft
unchanged. The editor is idle-only so a blocking process cannot stall display
events from an active run, and the result is read back under the normal 64 KiB
composer cap.

An empty transcript shows a short tips block (`F1`, `Shift`+drag,
`Ctrl+Shift+V`, the wheel, and the pickers). It disappears with the first
transcript item and is dropped for a transcript too short to hold it, which
keeps the one-line prompt and the composer. No `-ui-json` key or protocol
change is involved; the block is ordinary frame text.

## Slash commands

The composer executes a registered command instead of starting or steering a
run. Typing `/` opens a filtered completion menu: `Up`/`Down` select, `Tab`
completes, `Enter` runs the highlighted command, and `Esc` dismisses. Command
names are case-insensitive; a space starts argument mode; unknown `/text` is
sent as a normal message, and `-task`/`-task-file` text is always literal.
While a run is active, commands still work, but actions that only affect the
next run are disabled with a notice. The full reference — syntax, arguments,
key equivalents, idle/active behavior, `-ui-json` mapping, and examples — is
in [commands.md](commands.md). The help overlay (`F1`) lists every command.

## Model picker

`Ctrl-P` opens the catalog-backed model picker while idle; a run disables it
with a notice. Entries are the usable models from models.dev, sorted by ID. Each
row shows the display name, ID, dialect, context and input limits, advertised
output ceiling, the normalized supported effort values (`effort low/high/max`,
or `effort -` when the model advertises none), and a `deprecated` / `alpha` /
`beta` marker and a `translated` marker when applicable. Deprecated models
remain selectable and are never chosen automatically. The advertised output
ceiling is catalog metadata, not the
effective request budget: unless `-max-output-tokens` is explicit, output is
unset and the request omits the field.

Selecting a model changes only the next run's dialect, base URL, and limits;
the current run and existing log are untouched. An explicit context limit is
clamped to the new model's window; an explicit output budget is sent unchanged.
A next-run thinking effort is kept when the new model advertises it; otherwise
it is cleared to the provider default with a notice, and the next `run_start`
records the cleared value. The details overlay (`Ctrl-X`) shows the effective
values for the current or next run and displays `max output: unset` when no
explicit budget applies.

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

## Thinking effort

The public name is *thinking effort*; the provider and catalog call it
*reasoning effort*. Values are model-specific and come from the effective
model's normalized catalog entry; there is no harness-wide enum.

`F2` opens a picker for the next run while idle. It lists `provider default`
first, then the effective model's advertised values in canonical order
(`none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, then unknown
values lexicographically). Selecting a value changes only the next run: the
active run, the transcript, and existing log events are untouched, and the
value is recorded in the next `run_start`. Selecting `provider default` clears
the setting and omits the wire field.

- If the effective model advertises no effort values (including a model whose
  reasoning is model-fixed), `F2` shows a notice and opens no overlay.
- During a run, `F2` shows the same disabled notice as the model picker.
- `default` is a clear sentinel, never a catalog level; a toggle-only or
  token-budget-only control is not an effort level.

The details overlay (`Ctrl-X`) shows `thinking effort: <value>` or
`thinking effort: provider default`. A model switch keeps a supported
next-run value and clears an unsupported one with a notice, as described under
[model picker](#model-picker).

## Thinking

Model thinking (provider reasoning) is shown in the transcript as a separate
`thinking` block, indented under the assistant text it precedes. It is
display-only and never durable: it is not written to the session log, not
replayed on resume or fork, and not counted against the response cap. The
active adapter keeps a bounded in-memory copy of its native continuation
material (thinking text for Chat, raw reasoning items for Responses) so the
model can continue a tool round; that copy is not part of the transcript and
disappears with the run. A resumed or forked session therefore shows only new
thinking.

- While the model is still thinking the block header is `thinking…`; it becomes
  `thinking` when assistant text arrives or the response completes.
- Thinking blocks are folded by default. A collapsed block shows its header,
  one elision row with the number of hidden rendered rows, and the last three
  rendered rows of the body. The three-row tail is a count of wrapped rows, so
  a long provider line can contribute several of them and the collapsed height
  stays constant while text streams. The tail means a settled block ends on the
  model's conclusion rather than its opening restatement.
- `F3` expands every thinking block, and `F3` again collapses them, while idle
  or during a run. The mode is sticky across turns, runs, retries, streaming
  deltas, and item trimming; it is not persisted to the config file and does not
  survive a restart, matching `Ctrl-R`. There is no per-block or mouse binding.
  `F3` is inert while an overlay is open.
- Expanded thinking is bounded by the 64 KiB per-item display cap. The `CapText`
  `[truncated N bytes]` marker is part of the body and remains visible in both
  states. Thinking has no session-log sentence in its elision row because it is
  never logged; the hidden text exists only in memory.
- DeepSeek-compatible Chat endpoints stream `reasoning_content`;
  OpenAI-compatible endpoints may use `reasoning`. The Responses adapter streams
  `response.reasoning_summary_text.delta` and `response.reasoning_text.delta`,
  and uses the terminal reasoning item's summary/content as authoritative.
- A buffered response, or a provider that only reports reasoning on its
  completed response, is displayed once before the assistant text.
- Long lines wrap; the block is preformatted and never parsed as markdown.
- Models and providers that expose no thinking produce no block; nothing
  changes for them. `Ctrl-R` rich/raw applies only to assistant markdown, so a
  thinking block renders the same in both modes.
- The block is not part of `-ui-json` semantic events; it appears in `frame`
  lines and snapshots like any other transcript text, so the protocol version
  stays 1. A client that waits for a frame during streaming does not see
  thinking deltas until it requests `frame`. A collapsed block contributes only
  its header, elision row, and tail to `lines`, so a client scraping frames for
  reasoning text sees the tail only until it sends `key` `f3`.
- Batch mode never prints thinking: `-batch`/`-stream` stdout remains assistant
  text only, so the one-shot stdout contract is unchanged. Thinking is also not
  part of `-ui-json` events.

## Status line and details

The header line shows `effort <value>` for the next run, or `effort default`
when the provider/model default applies, so the effective effort stays visible
without opening an overlay.

The status line shows `turns N` and `ctx N/L` and is refreshed while a run is
active, not only when it ends. `turns` counts successful model responses
cumulatively over the session; it shows `turns N/M` only when `-max-turns` is
explicit and positive. `ctx` is context occupancy — the latest model response's
input plus output tokens — against `L`, the effective per-call context window
(the catalog `limit.context`, or an explicit `-max-context-tokens` clamped to
it). It is deliberately not the session-cumulative token sum: that sum includes
every tool round and retry, grows with each call, and is not comparable to a
context window. A response that reported no usage leaves the last known
occupancy in place instead of resetting it. When the context window is unknown
the ratio is omitted and only the occupancy is shown; unlimited turn budgets
never render as `0/0`.

The details overlay (`Ctrl-X`) shows the observed `turns: N`,
`tokens: <human>` (the session-cumulative sum), `ctx: <human>/<limit>`, and
`cost: $<micro-USD>` under the session block, the
effective `thinking effort: <value>` (or `provider default`), and the
configured limits under
configuration: `max turns: unlimited` (or the integer), `total tokens:
unlimited` (or a human token count), `run timeout: none` (or a duration),
`model timeout: none` (or a duration), and `http timeout: <duration>`. Run and
model timeouts never render `0` as a duration, because `0` means "no harness
deadline"; the HTTP timeout is always positive and always shown.

A resumed or forked session shows the new run's effective values, which are
unlimited by default; historical recorded budgets are not shown as the current
configuration. The cost line is an observational catalog-price estimate and
is marked `(incomplete)` when any model response lacked a price. No new key or
`-ui-json` protocol version is involved; frames carry the same labels.

Usage objects in `-ui-json` frames may carry the additive optional members
`cached_input_tokens`, `cache_write_input_tokens`,
`reasoning_output_tokens`, `reported`, and `cost_micro_usd`; older consumers
ignore them and the protocol version stays 1.

## `-ui-json` protocol v1

Every stdin line is one JSON object with `"v":1` and `"type"`. Unknown fields
are ignored; an unknown version, type, or key is a protocol error (exit 2).
Commands: `text`, `key`, `paste`, `resize`, `wait`, `frame`, `snapshot`,
`quit`; EOF is equivalent to `quit`. Slash commands and their autocomplete are
ordinary composer text: send `text` followed by `key` `enter` (or `tab`,
`up`, `down`, `esc`) to drive them, with no protocol change. See
[commands.md](commands.md#-ui-json-mapping). Keys accepted by `key` are the
shipped key table plus `enter`, `esc`, `up`, `down`, `left`, `right`, `home`,
`end`, `pgup`, `pgdown`, `backspace`, `delete`, `tab`, `shift-tab`, `ctrl-c`,
`ctrl-d`, `ctrl-l`, `ctrl-u`, `ctrl-w`, `ctrl-a`, `ctrl-e`, `ctrl-k`, `ctrl-p`,
`ctrl-o`, `ctrl-r`, `ctrl-t`, `ctrl-g`, `ctrl-x`, `f1`, `f2`, `f3`, `f4`,
`shift-enter`, and `alt-enter`. The external editor (`f4`) is unavailable in
`-ui-json` and reports a notice; it needs a real terminal.

Output lines:

- `frame`: `seq`, `width`, `height`, `state`, `overlay`, `cursor`
  (`row`, `col`, `visible`, `shape`), `lines` (exactly height entries), `styles`
  (coalesced `row`, `col`, `width`, `text`, `fg`, `bg`, `attrs`), `images`
  (`id`, `path`, `alt`, `row`, `col`, `width`, `height`, `protocol`),
  `color_profile`, `no_color`, `image_protocol`, `styles_truncated`. Frames
  contain no ANSI and no raw image bytes.
- `event`: `run_started`, `run_finished`, `tool_started`, `tool_finished`,
  `model_changed`, `notice`, `error`, with the fields documented in
  [tui-build.md §2.7](plans/tui-build.md#27-machine-readable-ui-mode---ui-json).
  `run_started` and `model_changed` carry an optional `thinking_effort` field
  for the effective value; an omitted field means the provider default.
  Selecting an effort with `F2` emits a `notice` with
  `kind: "thinking_effort"`. No new event type is introduced, and the protocol
  version stays 1; clients that do not know the optional field ignore it.
- `snapshot`: a deterministic SVG of the current `x/vt` cells at the current
  frame `seq`; it contains no timestamps, random IDs, durations, or ANSI, and
  does not emit an extra frame.
- `error`: `code` and `message` for protocol/UI errors; a wait timeout emits
  `error{code:"timeout"}` and leaves the run active.

Frames are emitted after every view-changing command, after a completed
`wait`, and after each non-text semantic transition. Text deltas update the
model but do not emit a frame; request `frame` to snapshot streaming text.
Collapsed thinking and tool blocks contribute fewer `lines` to a frame; send
`key` `f3` to expand them. This is a display-only content change, not a new
event or protocol version.
Frames and snapshots are deterministic for a fixed provider/tool response,
size, color profile, and image protocol: `-ui-json` hides the session ID,
elapsed time, and durations. `-ui-json` always uses glyph image mode.

`-ui-size WxH` sets the initial frame size (default `100x30`, valid
`20x5`–`500x200`); `resize` is bounded the same way.

## Caps

| Cap | Value | Behavior |
|---|---|---|
| Task/steering message | 64 KiB | Longer is rejected with a notice; the run continues. |
| External editor draft | 64 KiB | `F4` reads the edited temp file under the composer cap; an over-cap or failed edit leaves the composer unchanged. Available while idle only. |
| Transcript items / text | 20,000 / 16 MiB | Oldest UI items are dropped with an "older history omitted" marker; the log is unchanged. |
| Assistant text per item | 64 KiB | Truncation marker; the canonical response remains in the log. |
| Thinking text per item | 64 KiB | Provider reasoning is display-only and never logged; the marker is shown, and a resumed session has no prior thinking. Collapsed blocks show the last three rendered rows; expanded blocks show the full text up to this cap. |
| Markdown source / blocks / nesting | 64 KiB / 4,000 / 16 | Over-cap input falls back to plain text with a notice. |
| Table columns / rows | 12 / 500 | Column width cap 40 cells; the overlay scrolls horizontally. |
| File picker entries / filter | 2,000 per directory / 64 bytes | No recursion, no file contents; `.git`, symlinks, control-character names, and escapes are rejected. |
| Overlay stack | 4 | Deeper requests close the oldest overlay. |
| Thinking effort values | 16 per model, 32 bytes each | Catalog-normalized; `F2` lists `provider default` plus the model's canonical values and opens no overlay when none exist. |
| Image file / dimensions / pixels | 8 MiB / 4096×4096 / 16 Mi | PNG/JPEG/GIF only; `DecodeConfig` runs before decode. |
| Images per transcript / thumbnail | 32 / 60×20 cells | Older images fall off; thumbnails are half-block glyphs. |
| Tool output | 4 KiB or 40 lines expanded | Collapsed tool blocks show the last three rendered rows of the full result; expanded output is bounded to the first 40 lines / 4 KiB, and the remainder stays in the log. |
| Transcript fold | 3 rendered rows | Thinking and tool bodies are collapsed by default: header, one elision row with the hidden rendered-row count, and the last three wrapped rows. `F3` expands all folds; the mode is sticky and not persisted. |
| Tool arguments preview | 2 KiB | Full arguments remain in `model_response`. |
| Input history | 100 entries | In-memory only. |
| Minimum terminal | 40×10 | Smaller shows a resize message and keeps quit/resize keys. |
| Frame coalescing | 16 ms | Text deltas coalesce; state changes flush immediately. |
| Escape timeout | 50 ms | Bare Esc; modified sequences never interrupt. |
| Mouse wheel | 3 lines per notch | Scrolls the transcript, or the top overlay when one is open. Clicks, releases, motion, and horizontal wheel events are ignored; focus reporting stays off. Terminal selection and middle-click paste need the terminal's `Shift` bypass; see [Terminal safety](#terminal-safety-and-images). |
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
stripped. Tool output is preformatted and never parsed as markdown; the same
holds for thinking text.

Images are display-only workspace files resolved with `os.Root`,
`filepath.IsLocal`, no `.git` or symlink components, and no network fetch.
SVG/XML and unsupported formats become placeholders. ntcharts `picture`
renders transcript thumbnails as half-block glyphs; the viewer can use Kitty
when `AHARNESS_IMAGE_PROTOCOL=auto|kitty` selects it. `glyph` and `none` force
the universal fallback or placeholders; `-ui-json` always forces glyph. Kitty
placements are cleared on viewer close, resize, and exit.

Bubble Tea/ultraviolet owns raw mode, the alternate screen, bracketed paste,
resize, cursor, and restoration.

Mouse reporting is limited to the scroll wheel. `View()` selects Bubble Tea cell
motion (SGR button events), which reports clicks, releases, and wheel notches but
not hover; clicks, releases, and motion are ignored. The wheel scrolls the
transcript by three lines per notch, so `PgUp`/`PgDn` remain the keyboard
equivalent. Scrolling up detaches the live tail and scrolling back to the bottom
re-attaches it. With an overlay open the wheel scrolls that overlay instead: the
help and details panels scroll their text, the table moves its row cursor, and
the file/model/effort pickers move their selection. Horizontal wheel events do
nothing. Terminal focus reporting stays disabled.

No terminal mouse mode reports only the wheel, so enabling the wheel stops the
terminal from selecting text and handling middle-click paste itself. The
terminal's own selection, copy, and paste stay reachable through its bypass
modifier: hold `Shift` while dragging (Option-drag in iTerm2) to select and copy
with the native terminal selection, and `Shift`+middle-click to paste.
`Ctrl+Shift+V` (or the terminal's own paste binding) always works because
bracketed paste remains enabled. An empty transcript lists these tips; the help
overlay repeats them.
