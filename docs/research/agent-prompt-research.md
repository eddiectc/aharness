# aharness agent exploration and prompt research

- **Date:** 2026-11-11
- **Status:** research complete; no code changed. Input to a future prompt-v1 plan.
- **Snapshot:** repository `2e747b2b4412`, prompt `prompt-v0`, toolset
  `toolset-v1`. Model `deepseek-v4.1-flash`, thinking effort `max`.
- **Scope:** why the agent spends many read-only turns before editing, how that
  differs from an omp run with the same model and effort, and what the system
  prompt can change. Reasoning display, cached-input telemetry, and tool caps
  are referenced only where they affect the analysis.
- **Related:** [architecture](../architecture.md), [limits](../limits.md),
  [testing strategy](../testing-strategy.md), and
  [agent tooling research](agent-tooling-research.md).

## 1. Summary

Two naturalistic runs of the same task, model, and thinking effort behaved very
differently:

1. **The model is not slow; the agent keeps deciding to explore.** The aharness
   run spent 45 turns and 129,842 reasoning tokens before its first edit, then
   executed eight edits in turns that took 5–14 seconds each. The cost was
   deliberation about when reading was sufficient, not tool latency, prefill, or
   generation throughput.
2. **The system prompt is the clearest controllable difference.** `prompt-v0`
   says "Search and read before editing" and gives no exploration budget,
   batching rule, or stop condition. The omp run's prompt says to batch
   independent reads, default to informed action, and stop once verified. Under
   the same model and effort, omp averaged 527 reasoning tokens per turn; the
   aharness run averaged 2,885 before the first edit.
3. **The tool surface amplifies the prompt.** `read_file` returns full numbered
   windows; omp's read returns a declaration outline for a bare path and full
   content only for explicit ranges. The aharness run used `run_command` for 66
   of its 89 pre-edit tool calls (34 grep, 29 sed), so it repeatedly re-sliced
   the same source instead of reading a compact structure first.
4. **The loop has no budget or stop signal.** `max_turns: 0`,
   `session_timeout_ms: 0`, compaction only at the 950k target, no plan/todo
   tool, and no read-only-turn guard. Nothing in the harness says "you have
   enough context".
5. **The comparison is confounded.** The runs overlapped in one worktree. The
   aharness agent edited files while the omp agent was still reading; omp
   discovered those changes, inspected the diff, and was interrupted. The omp
   run reached a concrete plan at 4m53s but never made an edit. Its "four
   minutes" is time-to-plan, not time-to-edit.

The actionable conclusion is a prompt change, not a model or provider change.
The prompt needs an exploration budget, a batching rule, an action bias, and a
completion condition. Tool-output density and a harness-side read-only notice
are the supporting changes.

## 2. Method

- Parsed both session event logs and computed per-turn latency, token usage,
  tool-call counts, and the first mutation time.
- Compared the prompt text and the first-request size, the tool result shapes,
  and the loop limits.
- No controlled A/B, no code changes, and no repeated trials. Effect sizes below
  are directional evidence from one run per arm, not an experiment.

### 2.1 Runs

| | Run A — aharness | Run B — omp |
|---|---|---|
| Session | `01a1cc61-f159-746f-80f9-829479f81f10` | `01a1cc8c-d183-764a-920f-cdf560a0c81b` |
| Window | 20:52:13–21:44:59 UTC | 21:39:34–21:44:37 UTC |
| Model / effort | deepseek-v4.1-flash / max | deepseek-v4.1-flash / max |
| Task | "the model thinking is not visible in TUI, fix it" | same |
| Prompt version | `prompt-v0` | omp system prompt |
| Interruptions | none | user stop at 21:41:18, "continue" at 21:42:07, stop at 21:44:37 |
| First edit | 21:43:41 (turn 46, 51m28s after start) | none; plan at 21:44:27 |

Both runs used `/home/tc/sr.ht/aharness` as the workspace. Run A's log is a
local, untracked artifact; Run B's log lives under
`~/.omp/agent/sessions/-sr.ht-aharness/`.

## 3. Measurements

### 3.1 Model behavior

| Metric | Run A (aharness) | Run B (omp) |
|---|---:|---:|
| Productive model turns | 45 before the first edit | 33 |
| Reasoning tokens | **129,842** (96% of output) | **17,383** (79% of output) |
| Output tokens | 135,023 | 22,104 |
| Reasoning per turn | 2,885 average | 527 average, 151 median |
| Median response latency | **44.5s** | **4.1s** |
| Maximum response latency | 285s | 30.5s |
| Tool calls | 89 before the first edit | 56 |
| First request input | 1,316 tokens | 8,586 + 512 cached |

Run A used 66 `run_command`, 22 `read_file`, and 1 `search` call before the
first edit; the shell calls were mostly `grep` and `sed`. Run B used 36 `bash`,
17 `read`, and 3 `grep` calls. Both runs averaged roughly two tool calls per
model turn, so batching alone does not explain the difference.

### 3.2 Phase split

| Run A phase | Turns | Reasoning | Wall time | Median response |
|---|---:|---:|---:|---:|
| Exploration before first edit | 45 | 129,842 | 2,896s | 44.5s |
| First edit and follow-up edits | 9 | 7,981 | 236s | 11.2s |
| After the first edit (turns 47–54) | 8 | **340** | 5–14s per turn | ~10s |

The last row is the control: with the same harness, context (~153k tokens), and
model, once the agent stopped exploring, it stopped generating long reasoning.
The wall clock was not consumed by request setup or tool execution. Run A's
tool execution totalled 6.9 seconds; Run B used the same provider and model.

### 3.3 Prompt

`prompt-v0` is seven bullets in
[`internal/agent/context.go`](../../internal/agent/context.go):

```text
You are aharness, a coding harness working in the workspace %q.

- Tools are your only way to act on the workspace. Do not claim an action you did not take through a tool call.
- Search and read before editing. Keep changes minimal and focused on the task.
- Run the relevant tests after changing code.
- Commands run with the harness's privileges. The harness adds no sandbox and no approval step.
- Tool errors are data: read them, adapt, and try a different approach instead of escalating.
- Stop by replying without a tool call when the task is complete or when you are blocked.
```

The prompt never says when reading is complete. "Search and read before
editing" is an unbounded instruction; the only stop condition is task
completion, so the model can keep gathering context while still feeling
incomplete.

The omp prompt is much larger (the first request carried ~9k input tokens
including tool schemas). Its relevant rules are direct:

- "Every turn costs seconds: batch all independent reads/searches/checks into
  one turn."
- "MUST default to informed action; do not ask for confirmation when tools or
  repo context can answer."
- "NEVER open guessed files. Read small files whole; ranges only for large
  files."
- "Once the change is verified, stop and report; no extra audits, broad suites,
  or repo-wide checks unless asked."
- A workflow section that decomposes, implements, and verifies proportionally,
  and a completion checklist before yielding.

These rules give the model an external stopping criterion: batch the reads,
make the change, verify it, stop. Run B followed that shape.

### 3.4 Tool output shape

Run A's `read_file` contract returns one full contiguous window of numbered
lines (default 2,000, cap 4,000, 60 KiB). Run B's `read` returns a declaration
outline for a bare path, with bodies elided, and full content only for an
explicit range. Run B's `grep` also returns a digest (`grep: 39 matches in 7
files`) with grouped context; Run A's model mostly invoked raw
`grep -rn`/`sed -n` through `run_command`.

The raw-byte totals were similar (about 476 KB for Run A and 386 KB for Run B),
so the issue is not total volume. It is information density: Run B saw the
shape of four files in one turn and then expanded only the two files it needed;
Run A pulled full windows and long test files, then re-sliced them.

### 3.5 Cached-input telemetry

Run A logs `cached_input_tokens: 0` on every turn. The provider console reports
cache hits for the same requests. The log field is therefore not trustworthy.
This is a separate usage-parsing/telemetry bug, not an explanation for the wall
clock: the post-decision turns at the same context were fast. The likely places
to check are the adapter's `prompt_tokens_details.cached_tokens` handling and
the DeepSeek-compatible `prompt_cache_hit_tokens` fallback. Track that work
separately from prompt changes.

### 3.6 Reasoning in history

Run A's projection rebuilds assistant messages from the recorded response text
and tool calls only; reasoning text is not stored in the session and
`chatMessage` has no reasoning field. Run B's log preserves thinking blocks.
Whether replaying reasoning across tool rounds reduces re-reasoning is untested.
A display-only reasoning change does not affect this behavior; the in-progress
fix keeps reasoning out of history. This is an open question, not a finding.

## 4. Findings

1. **The exploration budget is missing from the prompt.** The model obeyed
   "Search and read before editing" literally for 45 turns. It had enough
   information to design the change much earlier: Run B reached the same plan
   in 33 turns and ~17k reasoning tokens.
2. **Per-turn reasoning, not tool latency, dominates.** Run A's median response
   was 44.5s and its reasoning grew from hundreds of tokens early to 13,362 in
   one late turn. Run B's median response was 4.1s. The model and effort level
   were identical.
3. **The model can execute quickly under the same harness.** After the first
   edit, Run A made eight edits in 5–14s turns with ~340 total reasoning tokens.
   The harness does not impose a 40-minute floor.
4. **Raw read/search output encourages re-reading.** Run A used the shell for
   most inspection, and its result format lacked the declaration outline that
   let Run B identify the relevant ranges quickly.
5. **There is no harness-side stop aid.** No `max_turns`, no session timeout, no
   compaction pressure before the 950k target, no plan/todo tool, and no notice
   after repeated read-only turns. A prompt rule can compensate, but the harness
   currently cannot.
6. **The two runs are not a controlled experiment.** Concurrent edits changed
   the omp agent's worktree mid-run; omp was interrupted twice; the omp run never
   edited. The 51-minute versus 4-minute comparison is time-to-first-edit versus
   time-to-plan, and it is anecdotal.

## 5. Recommendations

### 5.1 Prompt v1

Keep the useful constraints from v0 ("read before editing", "minimal changes",
"tests after change", "stop when complete"), but make the exploration budget and
the stop condition explicit. Suggested section:

```text
## Exploration budget

- Read until you can name the files to change and the invariant to preserve.
  Then stop reading and make the smallest correct change.
- Batch independent reads, searches, and checks into one turn.
- Do not re-read a file you have already read; use offset and limit.
- Do not ask for confirmation when the repository can answer the question.
- Use read_file and search for inspection. Use run_command for builds, tests,
  and commands that need a shell, not for routine grep or sed.
- When the change builds and the relevant tests pass, stop. Do not run broad
  audits or repo-wide checks unless the task requires them.
```

Two constraints on the change:

- `prompt-v0` is frozen prompt material. A prompt change MUST bump
  `PromptVersion` so existing sessions still replay with the prompt they were
  recorded under.
- The budget should not become "edit first, read never". The invariant-naming
  clause keeps the v0 requirement to understand the code before changing it.
  The failure mode to avoid is unbounded reading, not reading itself.

### 5.2 Tool-output density

- Consider a declaration-outline mode for `read_file`, or a separate summary
  view, so the first pass over a large file is cheap and the model expands only
  the relevant range.
- Make `search` output grouped-by-file with a match count; keep the raw
  `path:line:text` form available only when needed.
- Update the prompt to prefer `read_file`/`search` over shell `grep`/`sed` for
  inspection. This is a prompt rule first; the tool change is optional.

### 5.3 Harness-side stop aid

- Add a display notice after N consecutive read-only turns ("you have enough
  context; state the change and edit") or a soft turn budget. This is a
  guardrail, not a replacement for the prompt.
- If a plan/todo tool is added, require one plan item before the first edit.
  This is optional; Run B reached a plan without a visible todo tool.

### 5.4 Measurement

The next comparison should control the confounds:

1. Same workspace snapshot; one agent at a time; no concurrent worktree edits.
2. Same task, model, effort, and prompt except the experimental delta.
3. At least three runs per arm; report turns to first edit, wall time, reasoning
   tokens, tool calls, and whether the change builds and passes the relevant
   tests.
4. Record provider-reported reasoning tokens and cached input separately. Fix
   the cached-input parsing before using it as a metric.

## 6. Open questions

- Does replaying reasoning across tool rounds reduce reasoning volume, or does
  the provider's thinking+tool contract require it to be dropped? The current
  display fix does not answer this.
- Is the outline read a material cause, or is the prompt alone sufficient? An
  ablation with the same prompt but full-window reads would separate them.
- Does a text plan checkpoint reduce exploration or merely add a turn? Measure
  it rather than assuming.
- Does a soft read-only notice help, or does it cause premature edits? The
  invariant-naming requirement is the safety valve.

## 7. Sources

- Run A: `01a1cc61-f159-746f-80f9-829479f81f10.jsonl` (workspace root, local
  artifact; 54 turns).
- Run B: `~/.omp/agent/sessions/-sr.ht-aharness/2026-11-11T21-39-03-171Z_01a1cc8c-d183-764a-920f-cdf560a0c81b.jsonl`
  (local omp session; 35 assistant messages, two user interrupts).
- Prompt and projection: [`internal/agent/context.go`](../../internal/agent/context.go),
  [`internal/model/openai_chat.go`](../../internal/model/openai_chat.go),
  [`internal/model/openai_responses.go`](../../internal/model/openai_responses.go).
- Tool contracts: [`internal/tools/schemas.go`](../../internal/tools/schemas.go),
  [`docs/architecture.md`](../architecture.md), [`docs/limits.md`](../limits.md).
- Prior measurement analysis: [`agent-tooling-research.md`](agent-tooling-research.md)
  §3 and §6.
