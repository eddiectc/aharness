# aharness transcript folding build plan

- **Date:** 2026-11-13
- **Status:** implemented 2026-11-14. The shipped behavior is documented in
  [tui.md](../tui.md) and [limits.md](../limits.md); §1.4 records verification
  and §10 records deviations.
- **Source:** UX review of the `thinking` transcript block introduced by
  [thinking display](../tui.md#thinking) and the tool-output fold in
  `internal/tui/view.go`. Model reasoning is capped at 64 KiB but rendered
  fully expanded, so one response can occupy hundreds of wrapped rows.
- **Target:** Go 1.27.3, module `aharness`.
- **Architecture:** [architecture.md](../architecture.md) §3.1 (package layout),
  §3.3 (TUI, display, and history contracts), and §1 (the TUI as a
  display-only front end).
- **Working surface:** [tui.md](../tui.md) keys, thinking, caps, and terminal
  sections; [limits.md](../limits.md) §6 display caps.
- **Testing:** [testing-strategy.md](../testing-strategy.md) plus §5 here.
- **Definition of done:** [project definition of done](../definition-of-done.md);
  feature criteria in §1.4.

## Decision summary

- Thinking blocks and tool output are **collapsed by default**. A collapsed
  block keeps its header plus the **last three rendered rows** of its body, and
  an elision row above those rows states how many lines are hidden.
- **One global toggle.** `F3` expands every fold; `F3` again collapses every
  fold. There is no per-block state, no slash command, and no mouse binding.
- The mode is **sticky**: it survives new turns, new runs, streaming deltas,
  and item trimming until the operator presses `F3`. It is not persisted to the
  config file and does not survive a restart, matching `Ctrl-R`.
- **Expanded is not unbounded.** Inline expansion is bounded by a display cap;
  the full text stays reachable and the existing safety caps are untouched.
- The **viewport must not move under the operator**. While following the live
  tail, a toggle stays pinned to the newest row. While scrolled back, the
  toggle preserves the **transcript item** the operator is reading.
- **Tool output switches from head to tail.** Today the fold keeps the first
  40 lines; the last lines carry the failure, trace, or test verdict.
- **Scoped out:** the summary-versus-raw reasoning duplication in the Responses
  adapter (§1.3). It is a real verbosity source but a separate change.
- No session schema change, no new event, no protocol version bump. `-ui-json`
  gains one accepted key name and changes which rows a frame contains.

## 1. Scope

### 1.1 What this plan owns

| Area | Change |
|---|---|
| Folding state | New `App.expandAll bool`, mirroring the existing `App.rich` flag. |
| Key | `F3` in the global key switch, active while idle and during a run; inert while an overlay is open. |
| Thinking render | `renderThinking` renders header + elision row + last three rows when collapsed. |
| Tool render | `renderTool` keeps its header and collapses the body the same way. |
| Shared helper | One `renderFolded` used by both kinds so the two cannot drift. |
| Render cache | Per-item folded output is cached by width and fold state so streaming does not re-fold 64 KiB every 16 ms. |
| Anchor | Toggle preserves the reading position: pinned to the tail while following, item-anchored while scrolled back. |
| Notices | `expanded blocks` / `collapsed blocks`, following the `setRich` precedent. |
| Help and tips | `F3` in the `F1` key list; tips block only if it still fits 40×10. |
| `-ui-json` | Accept `"f3"`; document that collapsed frames omit folded rows. |
| Docs | [tui.md](../tui.md) keys, thinking, caps, and `-ui-json` sections; [limits.md](../limits.md) display-cap table; README feature bullet. |

### 1.2 What stays the same

- The 64 KiB safety caps (`maxThinkingItemText`, `maxTranscriptItemText`) and
  the `CapText` truncation marker. The fold is a display concern layered on top
  of them, never a replacement.
- The 40-line / 4 KiB tool-body cap as the bound on **inline expansion**; it
  becomes a limit on expanded rendering rather than the collapsed render.
- Thinking remains display-only: never logged, replayed, projected, or sent to
  the provider, and absent from a resumed or forked session.
- Batch mode continues to print assistant text only.
- Transcript item and byte caps, the omitted-history marker, and the 16 ms
  frame coalescing.
- Mouse behavior: clicks, releases, and motion stay inert; only the wheel
  scrolls. The terminal-selection bypass documentation is unchanged.

### 1.3 Not in this plan

- **Summary-versus-raw reasoning.** `openai_responses.go` currently concatenates
  both `summary` and `content` parts, and `reasoning_test.go` asserts the
  duplicated string. Preferring summary and demoting raw content is a separate
  adapter change with its own provider-compatibility risk, including whether to
  request a summary at all.
- **Per-block expansion.** Expanding one block while leaving others folded
  requires a block cursor and collides with the `Up`/`Down` input-history
  binding. Recorded as a known limitation (§9).
- **Auto-collapse on turn change.** Contradicts the sticky requirement.
- **Display as thinking effort.** Effort is what the model does; folding is
  what the operator sees. They stay separate settings.
- **Content-aware folding.** No heuristics that favor error-looking lines; the
  fold is a plain tail so collapsed output stays deterministic and testable.

### 1.4 Definition of done

In addition to the [project definition of done](../definition-of-done.md), the
feature is complete when every item below holds.

- [x] Thinking and tool blocks render collapsed by default: header, elision
      row with a hidden-line count, and exactly the last three rendered rows.
- [x] `F3` expands every fold, and `F3` again collapses every fold, while idle
      and during a run, with a `expanded blocks` / `collapsed blocks` notice.
- [x] The mode is sticky across turns, runs, streaming deltas, retries, and
      item trimming, and persists until `F3` is pressed again.
- [x] There is no slash command, no per-block binding, and no mouse binding for
      folding; `MouseClickMsg` handling is unchanged.
- [x] Tool blocks always show the command header with name, args, status,
      reason code, truncation, timeout, and duration regardless of fold state.
- [x] Collapsed thinking shows the tail of the reasoning, so a settled block
      ends on the model's conclusion rather than its opening restatement.
- [x] Toggling while following the live tail leaves the viewport on the newest
      row, during streaming and when idle.
- [x] Toggling while scrolled back preserves the anchored item on screen; the
      only exception that sets `follow` is a collapse that lands the viewport
      at the bottom.
- [x] Expanded rendering is bounded; over-cap text still shows the `CapText`
      truncation marker, and thinking never claims to be in the session log.
- [x] `-ui-json` accepts `f3`, existing protocol 1 clients parse frames, and
      `docs/tui.md` states that collapsed rows are absent from frame lines.
- [x] Frames and snapshots stay within their line and size caps with a
      maximally populated transcript.
- [x] Unit, TUI, headless, race, lint, build, and vulnerability gates pass, and
      `docs/tui.md`, `docs/limits.md`, and `README.md` are updated.
- [ ] A real-provider smoke with at least one reasoning-capable model confirms
      the fold against live thinking text. Credentials are unavailable in the
      implementation environment; this is a release prerequisite, not a claim.

## 2. Current state

Verified against the tree at the date above.

| Concern | Today | Consequence |
|---|---|---|
| Thinking render | `renderThinking` (`internal/tui/view.go:439`) wraps and renders the whole 64 KiB body. | One response can fill hundreds of rows; older turns never shrink. |
| Tool body | `renderTool` (`internal/tui/view.go:473`) keeps the **first** 40 lines / 4 KiB and prints `[output folded; the full result is in the session log]`. | The head is usually setup noise; failures, traces, and test verdicts live at the end. |
| Fold state | None. No `expandAll`, no per-item flag. | — |
| Toggle precedent | `setRich` (`internal/tui/app.go:679`) sets a flag, calls `invalidateRenders`, `ensureContent`, and a notice. | The exact shape `F3` should reuse. |
| Viewport | `follow` (`internal/tui/model.go:146`) tracks the live tail; `ensureContent` (`internal/tui/view.go:270`) rebuilds all lines and calls `GotoBottom` when following. | Rebuilding is already unconditional on every dirty frame. |
| Row identity | `transcriptLines` (`internal/tui/view.go:291`) emits a flat `[]string`; only images record a row (`imageRefs`, `internal/tui/image.go:94`). | No item-to-row map exists for anchoring. |
| Rendering cost | `SetContentLines` receives pre-wrapped rows; `vp.SoftWrap` is on (`internal/tui/model.go:213`). | Wrapping is per render, so folding must not add a second full pass per frame per item. |
| Key surface | `f1` and `f2` are bound; `f3` is unused (`internal/tui/app.go:536`). | `F3` collides with nothing. |
| Protocol keys | `protocolKey` accepts `f1`/`f2` (`internal/tui/protocol.go:97`). | `"f3"` must be added or headless clients cannot drive it. |
| Word budget | `CapText` appends `[truncated N bytes]` (`internal/tui/sanitize.go:44`). | The fold must not swallow that marker. |

## 3. Contract

### 3.1 Fold state and the `F3` key

One boolean on the app, not per item:

```go
expandAll bool // false: thinking and tool bodies render collapsed
```

Rationale: `F3` is a global mode. Per-item state would need a reconciliation
rule for items created *after* the toggle, and with a sticky global mode a new
thinking block arriving mid-run must inherit the current mode or the screen
flickers between states. A single flag makes that automatic.

Behavior:

| Context | `F3` |
|---|---|
| Idle | Toggle all folds; notice `expanded blocks` or `collapsed blocks`. |
| Run active | Same. The mode is independent of run state; collapsing does not interrupt. |
| Overlay open | **Not bound.** Overlay keys win; `F3` falls through to the overlay and does nothing if unhandled. |

`F3` is deliberately *not* a slash command and *not* a mouse target. The click
invariant in [tui.md](../tui.md) ("clicks, releases, and motion are ignored")
stays true.

### 3.2 Collapsed rendering

Both block kinds share one helper:

```go
// renderFolded renders header plus a body that is either full or a tail.
// collapsed keeps the last tailRows rendered rows and prefixes an elision row.
func renderFolded(t uiTheme, header string, body []string, tailRows int, collapsed bool) []string
```

Collapsed shape:

```
thinking 3 · 14s · 1.2k chars
  … 312 earlier lines
  <row n-2>
  <row n-1>
  <row n>
```

Rules:

- **Three is a count of rendered rows, not source lines.** A provider "line"
  can wrap to several rows at 80 columns. Counting wrapped rows keeps collapsed
  height constant at `1 header + 1 elision + 3`, so the layout does not jump as
  text streams.
- **The elision row sits above the tail**, because the tail is what is shown. A
  three-row block without it reads as complete.
- **The hidden count is in rendered rows** and is labelled to say so.
- **Headers are identical in both states.** Only the body grows or shrinks, so
  the operator's eye anchor does not move.
- **Tool headers are never folded.** Name, sanitized args, status, reason code,
  truncation, timeout, and duration always render, satisfying "the command is
  always shown".
- **The `CapText` marker survives.** If the safety cap bit, its `[truncated N
  bytes]` row is part of the body and therefore visible in both states; the
  tail keeps it because it is the last row.
- **Marker wording differs by kind.** Tool output may state that the full
  result is in the session log. Thinking must not: it is never logged, so the
  hidden text exists only in memory. Use a neutral elision row for both and
  keep any log sentence tool-only.

### 3.3 Expanded rendering and caps

Expansion is bounded, because 64 KiB inline simply converts collapse fatigue
into scroll fatigue:

- **Thinking expanded:** the full body, up to the per-item display cap.
- **Tool expanded:** the existing 40-line / 4 KiB body cap, now applied to the
  expanded view.
- Text beyond the inline display cap is not silently lost: the block shows an
  explicit marker stating that the remainder is not shown inline.
- The safety caps are unchanged. [limits.md](../limits.md) records the new
  display fold as its own row rather than editing the 64 KiB rows.

### 3.4 Viewport anchor

This is the part that decides whether the feature feels correct.

The rebuild path recreates every row and the viewport clamps offsets to the new
content height, so **a saved numeric offset is not a valid anchor**: expansion
changes each item's row count, and soft wrap means the mapping from item to row
is not stable across a toggle.

The invariant is: *the content the operator was reading stays put on screen.*

- **Following (`follow == true`).** The desired result is the newest row, which
  is what `ensureContent` already does. Keep `GotoBottom` last so the viewport's
  own clamp cannot run afterwards. Free behavior; do not add anchoring here.
- **Detached (`follow == false`).** Capture the **item** at the top of the
  viewport and the row offset within that item before rebuilding. After the
  rebuild, set the offset to that item's new start row plus the same intra-item
  offset, clamped to the new maximum.
- **Anchor to the item, not the row of text.** Item identity survives a
  collapse because the item still exists and only its height changed. This is
  the agreed scope; it is exact whenever the operator is reading the item's
  early rows and degrades gracefully otherwise.
- **Degenerate case.** If the anchored item shrank below the saved intra-item
  offset (the operator was deep inside a block they just collapsed), clamp so
  the item's last row stays on screen. If the item no longer exists because
  trimming dropped it, fall back to keeping the offset pinned to the bottom.
- **`follow` is never modified by a toggle, with one exception.** A detached
  operator stays detached, even if the new content happens to fit entirely in
  the viewport. The sole exception is the clamp-to-bottom rule below.
- **Clamp-to-bottom rule.** If a collapse shrinks content so the target offset
  exceeds the new maximum, the operator lands at the bottom with no further
  rows below to scroll to. Set `follow = true` in that case, matching the
  documented rule that reaching the bottom re-attaches the live tail. This must
  be an explicit test, not an emergent accident.

### 3.5 Notices, help, and tips

- Notice text matches `setRich`: `expanded blocks` / `collapsed blocks` at the
  head of the status line.
- `F3` is added to the `F1` key list next to `F1`/`F2`. Because the mode is
  global, the per-block elision row does **not** advertise the key; a hint on
  every block would nag an operator who has already chosen to fold.
- The empty-transcript tips block gains `F3` only if the block still fits the
  40×10 minimum; otherwise it is omitted there and documented in `F1` alone.

### 3.6 `-ui-json`

- `protocolKey` accepts `"f3"`, raising no protocol version.
- **Frames change content, not shape.** A collapsed block contributes fewer
  `lines`, so a client that scrapes frames for reasoning text sees the tail
  only. This must be documented in [tui.md](../tui.md) as a display change, the
  same way thinking deltas are already documented as frame-only.
- No semantic event is added. Folding is a render mode, like `Ctrl-R`.
- The interaction with the 1 MiB frame line cap and 512 KiB snapshot cap must be
  verified with a maximally populated transcript; both caps should now trigger
  less often, never more.

### 3.7 Documentation deltas

| Document | Change |
|---|---|
| [tui.md](../tui.md) | Keys table row for `F3`; rewrite the Thinking section around folded default and the three-row tail; caps table; note that collapsed rows are absent from frames. |
| [limits.md](../limits.md) | Display-fold row: three-row tail, elision count, inline expansion bound. Leave the 64 KiB safety rows intact. |
| [README.md](../README.md) | Feature bullet for folded thinking and tool output. |
| This plan | Status, §1.4 checklist, and a deviations section. |

## 4. Implementation

### 4.1 File map

| Area | Files | Change |
|---|---|---|
| Fold state | `internal/tui/model.go` | Add `expandAll` to `App` next to `rich`; add fold fields to the per-item render cache. |
| Toggle | `internal/tui/app.go` | `case "f3"` in the global switch; `setExpandAll` modeled on `setRich`. |
| Render | `internal/tui/view.go` | Extract `renderFolded`; rewrite `renderThinking` and the `renderTool` body path to use it; record the item-to-row map while emitting rows. |
| Cache invalidation | `internal/tui/app.go` | Extend `invalidateRenders` to clear fold caches. |
| Anchor | `internal/tui/view.go`, `internal/tui/app.go` | Capture the top item and intra-item offset before a rebuild when detached; restore after. |
| Help | `internal/tui/widgets.go` | `F3` row in the key list. |
| Protocol | `internal/tui/protocol.go` | Accept `"f3"` in `protocolKey`. |
| Tests | `internal/tui/*_test.go` | Fold render, sticky mode, anchor, protocol, caps (§5). |
| Docs | `docs/tui.md`, `docs/limits.md`, `README.md`, this plan | §3.7. |

### 4.2 Implementation sketch

The render cache matters more than it looks. `transcriptLines` is called on
every dirty frame, and streaming marks dirty roughly every 16 ms. Folding is a
tail slice, which is cheap, but the *header* for thinking currently needs the
elapsed time and size, so the work is not zero. Cache the folded body per item
keyed by `(width, expandAll, renderedSet)` the way assistant markdown already
caches by `(width, rich)`:

```go
// transcriptItem additions
foldedWidth    int
foldedExpanded bool
foldedSet      bool
folded         []string
```

The item-to-row map can be a `[]int` parallel to `m.items`, rebuilt inside
`transcriptLines`, holding each item's start row. It replaces the ad-hoc
tracking used for images and gives the anchor its lookup:

```go
// rebuilt with the lines; rowOf[i] is the first row of m.items[i]
rowOf []int
```

`transcriptLines` already walks items once, so recording a start row is O(1) per
item and adds no extra pass.

The anchor capture must happen **before** the lines are thrown away, so the
toggle path is ordered: capture, invalidate, rebuild, restore. Do not call
`ensureContent` twice; a double rebuild would visibly flash at 16 ms cadence.

### 4.3 Milestones

| Milestone | Work | Acceptance |
|---|---|---|
| **M0 — Fold rendering** | `renderFolded`, thinking and tool call sites, three-row tail, elision row, tool header preserved, `CapText` marker retained. | Golden-ish render tests prove collapsed height is constant, the tail is the tail, and headers are byte-identical between states. No key yet; state forced in tests. |
| **M1 — Toggle and stickiness** | `expandAll`, `F3`, `setExpandAll`, notice, `invalidateRenders` extension, help row. | Toggle expands and collapses every block; mode survives a new turn, a new run, streaming deltas, and a retry; `F3` is inert in overlays. |
| **M2 — Anchor** | Item-to-row map, capture/restore on toggle, clamp rules, `follow` preservation. | Following → newest row stays visible; detached → anchored item stays on screen; no toggle ever sets or clears `follow` except the explicit clamp-to-bottom rule. |
| **M3 — Protocol, caps, docs** | `protocolKey` `f3`, frame-cap verification, `tui.md`/`limits.md`/`README.md`, §1.4 record. | Headless test drives `f3` and asserts folded frames; docs-check, lint, race, build, and vuln gates pass. |

## 5. Testing

| Layer | Cases |
|---|---|
| Render unit | Collapsed thinking shows header + elision + last 3 rows; expanded shows the full body; header identical in both states; a long wrapped line counts as one rendered row, not one source line; empty body renders the existing `…` placeholder; `CapText` marker visible in both states. |
| Render unit (tool) | Header keeps name, args, status, code, truncation, timeout, depth; collapsed body is the tail; expanded body honours 40 lines / 4 KiB; thinking elision never claims a session log. |
| Toggle unit | `F3` flips `expandAll` and sets the documented notice; mode persists across `ResponseCompleted`, `ModelStarted`, and a retry; a new item created while expanded renders expanded; `F3` does nothing inside an overlay. |
| Anchor unit | Following: toggle leaves `YOffset` at the new maximum and `follow` true, while streaming and while idle. Detached: anchored item's first row is unchanged after expand and after collapse. Anchored item shrank: last row stays visible. Anchor item trimmed: falls back to bottom. Detached collapse that lands at the bottom sets `follow` true. |
| Interaction regression | Existing `mouse_test.go` guarantees still hold: wheel up detaches, wheel to bottom re-attaches, clicks and motion inert. |
| Protocol | `protocolKey("f3")` succeeds; a headless run drives `f3` and observes expanded then collapsed frames; existing protocol 1 clients parse both; no new event type. |
| Caps | Maximally populated transcript stays under the 1 MiB frame cap and 512 KiB snapshot cap, and collapse strictly reduces `lines` count. |
| Determinism | Frames and snapshots are byte-identical across repeated toggles of identical content (no timestamp or map-order leakage into folded output). |

Tests use the existing Bubble Tea fake input/output, `t.TempDir`, and
`testing/synctest`; no real network.

## 6. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Collapsing during streaming hides the conclusion the operator was watching. | The tail is shown, not the head, so the newest reasoning stays visible while the header keeps `thinking…` liveness. |
| `F3` surprises an operator who expected a per-block toggle. | Notice, `F1` row, `README` bullet, and a documented limitation naming the future block-cursor extension. |
| Viewport jumps on toggle, the exact failure this plan exists to avoid. | Anchor by item identity, clamp explicitly, and cover all four anchor paths with tests before docs claim it. |
| Double rebuild causes a visible flash. | One ordered capture → invalidate → rebuild → restore path; never call `ensureContent` twice in the same toggle. |
| Streaming re-folds a large body every 16 ms. | Cache folded output per item keyed by width and fold state, as assistant markdown already does. |
| `SoftWrap` makes row counts differ from source lines. | Count rendered rows for the three-row tail and for the hidden-line count; test with a deliberately over-long single line. |
| Headless clients scrape frames for reasoning text and silently see less. | Document it in `tui.md` under Thinking and `-ui-json`; folded text stays available by expanding. |
| Tool tail hides the command's own echoed input. | The header always shows the command; the body is output only, which is what the requirement asks for. |
| Fold hides the truncation marker and implies completeness. | Marker is inside the body and is the last row in the tail; a separate test asserts it renders in both states. |
| Item trimming invalidates the anchor mid-session. | Explicit fallback to the bottom when the anchored item is gone. |

## 7. Decisions and alternatives

| Decision | Rationale | Cost if reversed |
|---|---|---|
| Global `expandAll`, not per-item state. | One key, no reconciliation rule for items created after a toggle, no stale flags on trimmed items. | Would need a block cursor, a selection model, and a key that does not collide with input history. |
| Collapsed by default. | Reasoning and tool output are asides; the answer is the primary artifact. | Per-item expansion state would have to be tracked and persisted per item. |
| Three **rendered** rows. | Constant collapsed height under soft wrap; no jump while streaming. | Logical-line counting makes collapsed height vary with word wrapping. |
| Tail, not head. | The verdict, failure, and conclusion are at the end of both reasoning and command output. | Head-folding is what ships today for tools and is strictly worse for diagnostics. |
| `F3` only; no slash command, no click. | Keeps the click-inert invariant, avoids registry and autocomplete churn, and works headlessly. | Adding a key hint per elision row would nag a deliberate choice. |
| Sticky until toggled. | Predictable; the operator's mode is never silently overridden mid-run. | Auto-collapse would keep long runs calmer but contradicts the requirement. |
| Item-level anchor, not row-level text anchor. | Item identity survives a toggle; row indices do not under soft wrap. | Row-level is more precise when the operator is deep inside a block they collapse; costs more bookkeeping. |
| Bounded expansion. | Infinite expansion converts collapse fatigue into scroll fatigue and risks frame caps. | Unbounded would need the overlay route for overflow, a larger change. |
| Not persisted to config. | Matches `Ctrl-R`; a display preference should not silently differ between machines. | Persisting would add a config key and a precedence question. |
| Summary/raw reasoning deferred. | Separate adapter change with provider-compatibility risk; folding alone fixes the reported density problem. | The duplication stays visible in the expanded tail. |

## 8. Documentation

When implemented, update in the same change: `docs/tui.md` (keys, thinking,
caps, `-ui-json`), `docs/limits.md` (display-fold row), `README.md` (feature
bullet), and this plan (status, §1.4, deviations). `docs/commands.md` is
unchanged because no command is added. `docs/architecture.md` is unchanged: no
package, event, or wire contract moves.

## 9. Known limitations and follow-ups

- **No per-block expansion.** `F3` is all-or-nothing, so debugging one failed
  command expands every tool output in the transcript. The natural extension is
  a block cursor with `Enter` toggling one block, reusing the same
  `renderFolded` helper over the `rowOf` map this plan introduces.
- **Anchor precision.** Item-level anchoring is exact for an operator reading
  the early rows of an item and approximate when they are deep inside a block
  they collapse.
- **Reasoning duplication** in `openai_responses.go` remains; preferring
  `summary` over `content` is the recommended follow-up.

## 10. Deviations from the plan

- **Thinking header text.** The illustrative collapsed shape in §3.2 showed
  `thinking 3 · 14s · 1.2k chars`. The implementation keeps the shipped
  `thinking` / `thinking…` header and does not add per-block timing or size
  counters: no definition-of-done criterion asks for them, timing would make
  headless frames nondeterministic, and §3.2's binding rule is only that the
  header is identical in both fold states.
- **Tips.** `F3` was not added to the empty-transcript tips block. At the 40×10
  minimum the block already drops its last tip to fit, so `F3` is documented in
  the `F1` key list and `docs/tui.md` instead, as §3.5 permits.
- **Body indent.** Thinking body rows are indented two spaces, matching the
  collapsed shape in §3.2 and the tool body, instead of aligning under the
  header label. The elision row and the tail therefore share one left edge, and
  wrapped-row counting does not depend on the header width.
- **Elision wording.** The row renders `… N earlier lines` with `N` counted in
  wrapped rows, matching the §3.2 shape while keeping the count's unit in the
  rendered-line vocabulary used by frames.
- **Tool body wrapping.** Tool output rows now wrap at the transcript width
  rather than being clipped at the right edge. This is what makes the three-row
  tail a count of rendered rows for both block kinds and keeps failures visible
  instead of cutting their text.
- **CapText marker in expanded tools.** When the 64 KiB safety cap appended its
  marker beyond the 40-line inline window, the expanded view appends the marker
  explicitly before the inline-cap sentence, so the marker is visible in both
  fold states.
- **Fold cache shape.** The per-item cache stores the unfolded, wrapped body
  keyed by width, fold mode, and source text, and the existing `lines` cache
  stores the final folded rows. A toggle therefore re-folds a body without
  re-splitting or re-wrapping it; streaming content invalidates the body by
  source identity.
