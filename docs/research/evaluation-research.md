# Evaluating aharness: a private, harness-agnostic design

Research notes on how to measure harness changes and compare aharness with
other coding agents without building another public benchmark.

- **Date:** 2026-11-15
- **Status:** Research. Not a contract; no runtime change.
- **Scope:** Evaluation methodology, task sourcing, execution substrate,
  trajectory format, metrics, and experiment design.
- **Related:** Extends §6 of
  [agent-tooling-research.md](agent-tooling-research.md). Replaces the
  design of the removed eval driver recorded in
  [agent-tooling-build.md](../plans/agent-tooling-build.md). Does not change
  the README's "built-in evals" exclusion: evaluation stays dev-only.

## Bottom line

1. **Evaluate agent-model pairings, not "the harness" in the abstract.** A
   coding agent is a model plus a runtime; a harness change and a model change
   are different treatments. For aharness improvements, hold the model fixed
   and vary one harness component. For cross-agent comparisons, report the
   complete pairing (agent, model, prompt, toolset, sandbox, version).
2. **Make a private, versioned suite the primary instrument.** Twenty to fifty
   repo-level tasks with hidden verifiers, a separate grading sandbox, and
   process evidence. This is not a benchmark: it is a regression and diagnosis
   instrument tuned to the changes aharness actually makes.
3. **Borrow an execution substrate instead of rebuilding one.** Harbor already
   runs arbitrary agents in isolated containers, has a custom-agent interface,
   a separate-verifier mode, ATIF trajectories, custom metrics, regrading, and
   reward-hacking checks. Use it, or at minimum its task format, and keep the
   adapter thin.
4. **Add component probes for prompt, tool, context, loop, and safety
   changes.** End-to-end tasks are noisy and expensive; probes isolate the
   mechanism a change touches and produce diagnostic evidence on every change.
5. **Use paired, repeated, interleaved runs and report per-task results.**
   Report confidence intervals, `pass^k`, cost per solved task, and failure
   taxonomy. Treat public benchmarks only as an optional external anchor, never
   as the decision instrument.

## 1. What problem is being solved

aharness is a Go coding-agent runtime: one model/tool loop, seven built-in
tools, a versioned system prompt and toolset, context projection and
compaction, typed stop reasons, a JSONL session log, and a TUI. The things
worth improving are therefore not only model choice:

| Harness component | Example change | What must improve |
|---|---|---|
| System prompt | Workflow rules, tool-use guidance, stop conditions | Task success, fewer wasted turns, fewer premature stops |
| Tool interface | Read pagination, edit matching, search globs, command spill, `intent` | Tool success rate, calls per solve, tokens per solve |
| Context policy | Projection, retained turns, compaction target, truncation | Long-task recall, tokens, compaction count |
| Loop and termination | Turn/token budgets, retries, interrupts, stop reasons | Reliability, recovery, `pass^k`, timeout rate |
| Provider adapters | Chat vs Responses, streaming, usage, thinking effort | Protocol errors, usage completeness, latency |
| Safety and containment | `os.Root`, process-group kill, SSRF refusal, no approval | Violations, leakage, unsafe side effects |
| UX and observability | TUI, folding, steering, cost display, session log | Human time-to-task, error recovery, trace quality |

Two different questions need two different designs:

- **Did this change make aharness better?** A controlled A/B on the same
  tasks and the same model. The unit of randomization is the task; runs are
  paired and repeated. This is the primary use.
- **Is aharness better than another coding agent?** An ecological comparison
  of complete configurations. It answers "which should I use for this kind of
  work?", not "which prompt is better?". Fairness controls matter more here
  because prompts, tools, permissions, and APIs differ by design.

## 2. What to measure

An outcome-only score is not enough. The same solve rate can hide a harness
that reads three times as many files, burns twice the tokens, or succeeds by
editing the test oracle. Measure five layers:

| Layer | Examples | Why it matters |
|---|---|---|
| Outcome | Hidden tests pass, partial reward, regression tests intact, stop reason | The primary decision signal |
| Cost | Input, cached input, output, reasoning, USD, completeness flag | A harness that costs more per solve is worse even at equal accuracy |
| Efficiency | Turns, tool calls, model latency, wall time, cache hit rate | Explains cost and exposes redundant work |
| Process | Reads per file, repeat reads, edit failures, truncations, spills, compactions, todo updates | Diagnoses which mechanism caused the outcome |
| Safety and reliability | Out-of-workspace attempts, `.git` access, network attempts, retries, protocol errors, limit hits, timeouts | A faster harness that escapes the sandbox is not better |

Two definitions keep comparisons honest:

- **Solved** means the hidden verifier passes, required regression tests still
  pass, and the agent did not modify or bypass the verifier. Anything else is
  not a solve, even if the final answer sounds correct.
- **Cost per solved task** counts all attempts, including unsolved tasks and
  timeouts, divided by solves. Reporting only the cost of successful runs
  rewards a harness that fails cheaply and often.

The session log already carries most of what is needed: `run_start`,
`model_request`, `model_response`, `tool_result`, `compaction`, `limit`,
`interrupt`, `error`, `final`, and `run_end` events include usage, cost,
latency, truncation, exit codes, duration, and stop reasons. A separate
metrics pipeline is not needed for aharness-specific runs; parse the log and
normalize the result.

## 3. The options

### 3.1 Public benchmarks: anchors, not instruments

- **SWE-bench Verified** (500 human-filtered GitHub issues, executable tests)
  is contaminated and partly flawed. OpenAI's February 2026 audit found
  material test or description issues in 59.4% of 138 hard audited tasks, and
  all tested frontier models could reproduce the gold patch or verbatim task
  details. OpenAI stopped reporting it.
- **SWE-bench Pro** is the harder public successor OpenAI recommends. It is
  still public, still subject to contamination and benchmark-specific
  behavior, and useful only as a secondary anchor.
- **Terminal-Bench 2.0** offers 89 hand-written terminal tasks with unique
  environments, human solutions, and comprehensive tests; frontier agents were
  below 65% at release. The 4.1 leaderboard is the current public version. It
  is the best public anchor for agentic terminal work, but the harness is part
  of the run, the task set is public, and it is expensive and not diagnostic
  for aharness internals.
- **SWE-rebench** continuously mines fresh interactive SWE tasks and shows
  that contamination can inflate SWE-bench Verified scores. Borrow the
  pipeline methodology, not the dataset as the decision instrument.
- **Aider polyglot and LiveCodeBench** are exercise-style or
  competitive-programming coding. They are not repo-level agentic work, are
  subject to saturation, and do not exercise aharness's tool loop.

The public-benchmark role is narrow:

- **Calibration:** are we roughly in the same league as other agents?
- **Catastrophe detection:** did a release break something general?
- **External credibility:** a shared reference point when talking to others.

It is not the role of a public benchmark to tell us whether a prompt edit,
read-limit change, or tool-description rewrite helped. Those effects are small,
task-specific, and easy to overfit.

### 3.2 Private task sources

| Source | Recipe | Strength | Risk |
|---|---|---|---|
| Reverted commits | Use the fix's parent; revert code only; keep tests hidden; verify fail-before/pass-after | Real bug, real tests, natural repository context | The fix may be trivial or widely known; tests may be narrow; needs curation |
| Issue/PR replay | Take a closed issue and its merged PR; use the PR's tests; hide the patch | Realistic instruction and verification | PR may mix unrelated changes; issue may be underspecified |
| Feature replay | Revert a feature commit; keep its tests; ask for the feature | Exercises planning and multi-file work | Large diffs; acceptance criteria may be implicit |
| Spec-written tasks | Write a task from a plan or issue; write hidden tests by hand | Targets a specific harness behavior; can be made unambiguous | Author bias; maintenance cost; easy to make too easy |
| Mutation/synthetic bugs | Inject a bug that breaks existing tests; validate that the mutant fails and the original passes | Scalable; can generate many tasks from one repo | Artificial; shallow bugs; verifier may reward the wrong fix; human review required |
| External private repos | Use a small Go repo or a private fork with recent issues | Diversity; less overfitting to aharness's own code | Access, licensing, and maintenance |

SWE-rebench's automated extraction and SWE-smith's synthetic bug injection are
useful as **methods**, not as task sets to copy. SWE-smith is Python-oriented;
for a Go harness, the practical version is mutation testing plus commit
reversion, with a human reviewing every task.

### 3.3 Execution substrates

| Option | Agent-agnostic | Isolation and verification | Trajectory and analysis | Cost and lock-in |
|---|---|---|---|---|
| **Harbor** | Yes: installed, external, custom, ACP | Docker or cloud; separate verifier; artifact sanitization; non-root; lint/check/analyze | ATIF; custom metrics; regrade; viewer | Python and Docker; opinionated task format; Terminal-Bench team |
| **Inspect AI** | Yes: ReAct plus external Claude Code, Codex CLI, Gemini CLI | Docker, Kubernetes, Modal, Proxmox, Vagrant | Strong logs and View; datasets, scorers, judges | Python; less native repo-level verifier workflow |
| **promptfoo** | Partial: Codex/Claude/OpenCode SDKs; custom providers | Workspace copies, not a sandbox | Assertions, traces, cost/latency, CI | Node; no container verifier; best for fast probes |
| **SWE-bench harness** | No: consumes model-generated patches | Docker; public benchmark tasks | Standard SWE-bench logs | Public only; not arbitrary interactive agents |
| **Custom runner** | Yes, if you build the adapters | Must build isolation and verifier transfer | Must build metrics and analysis | Full control; high maintenance; second-harness risk |

Harbor is the best fit for the end-to-end layer because it already solves the
two problems that killed the previous attempt: the verifier lives in a
separate environment, and the runner is not a second harness interface. Inspect
AI is the better fit if the priority is general evaluation, LLM-judge
experimentation, or a single Python framework for probes and tasks. promptfoo
is a good low-cost option for prompt and tool-description sweeps that do not
need container isolation. The task bundle and result schema should outlive any
one substrate; do not make Harbor the only way to read the results.

### 3.4 Trajectory and telemetry formats

- **ATIF (Agent Trajectory Interchange Format)** is the concrete format Harbor
  uses. It defines a trajectory as steps with `source` (`system`, `user`,
  `agent`), `message`, `reasoning_content`, `tool_calls`, `observation`
  results, per-step token/cost metrics, `final_metrics`, subagent
  trajectories, and a `context_management` convention for compaction,
  pruning, and injection. It is designed to unify conversational logs,
  action sequences, and replayable data. This is the right export target for
  aharness sessions.
- **OpenTelemetry GenAI semantic conventions** are the production
  observability standard for model calls, tool spans, and token usage. They
  are not a task or trajectory format, and the mapping from agent trajectories
  is still being standardized. Use OTel if aharness later exports live traces;
  use ATIF for evaluation artifacts.
- **Vendor platforms** (LangSmith, Braintrust, Langfuse, W&B Weave, Arize
  Phoenix, MLflow, Laminar, and similar) are useful for dashboards, run
  comparison, and human review. They are not evaluation designs by
  themselves, and they create a data-format dependency. Prefer an open
  artifact (ATIF plus a small result JSON) and send it to a platform if
  needed.

### 3.5 Analysis methods

| Method | Use | Caveat |
|---|---|---|
| Paired bootstrap over tasks | Confidence interval for a harness A/B | The task is the sampling unit; repeats are measurement error |
| `pass@1`, `pass@k`, `pass^k` | Capability vs reliability | `pass@k` rewards retries; `pass^k` exposes instability |
| Cost-success frontier | Compare configurations at equal budget | Cost accounting must be complete and cache-aware |
| Failure taxonomy | Explain why a run failed | Needs a stable, pre-declared taxonomy; otherwise it becomes storytelling |
| Trace review | Find prompt/tool/context defects | Expensive; sample deliberately, not uniformly |
| LLM-as-judge | Grade qualitative dimensions the verifier cannot check | Calibrate against human labels; report paired discrimination, not raw recall; never use it as the only outcome signal |
| Causal replay | Attribute a failure to a specific step by intervention | Research-grade; high compute; not an MVP requirement |

The strongest evidence for a harness change is a deterministic verifier plus a
readable trajectory. If the verifier cannot decide, an LLM judge may help
diagnose, but it should not become the primary score.

### 3.6 Human evaluation

Automated evaluation cannot answer everything:

- **UX and TUI changes:** run a small task-based study (5–10 users, 3 tasks,
  think-aloud) and record time-to-complete, errors, interruptions, and a short
  usability questionnaire. Automated `-ui-json` tests cover the protocol, not
  whether the interface is usable.
- **Prompt clarity and instruction ambiguity:** expert review of task
  instructions and agent transcripts.
- **Trust and recovery:** scripted scenarios where the agent is wrong,
  interrupted, or out of budget; observe whether the human notices and
  recovers.

These are expensive and low-frequency; reserve them for user-facing changes
and for validating a new task family.

## 4. Recommended design

### 4.1 Layers

```
L0  Deterministic tests          go test; fake provider; tool contracts
L1  Component probes             real model, small fixture, hidden check
L2  Private end-to-end suite     repo-level tasks, hidden verifier, sandbox
L3  Cross-agent bake-off         same tasks; aharness vs other agents
L4  External anchor (optional)   small Terminal-Bench/SWE-bench Pro subset
L5  Production telemetry         real sessions, cost, human review
```

- **L0** is already strong in this repository and stays the merge gate.
- **L1** is new. A probe isolates one mechanism: read pagination, edit
  recovery, search globs, compaction recall, tool-error adaptation, SSRF
  refusal, and so on. Real-model probes are required when a change touches the
  prompt, tool descriptions, context policy, or loop; deterministic
  tool-contract probes run on every change without model cost.
- **L2** is the primary decision instrument for release-level changes.
- **L3** answers the "how do we compare to other agents?" question.
- **L4** is a sanity check, not a score to optimize.
- **L5** validates that the suite reflects real use; it is not a benchmark.

### 4.2 Task bundle

Use a Harbor-compatible bundle so the tasks are portable and the verifier can
run in a separate environment:

```
tasks/<id>/
  task.toml          # metadata, resources, timeouts, network, artifacts
  instruction.md     # user-facing task; no tool hints, no benchmark name
  environment/       # Dockerfile or pinned image; repo at base commit
  tests/             # hidden verifier; never mounted into the agent
  solution/          # oracle patch; used for validation, not shown to agent
```

Add evaluation metadata under `[metadata]` in `task.toml`:

- `family`: bugfix, feature, refactor, debug, long-context, safety.
- `source`: commit SHA, issue ID, or `synthetic`.
- `base_commit`: exact repository state.
- `difficulty`: observed baseline pass rate from the last calibration.
- `forbidden_paths`: tests, `.git`, and any path the verifier depends on.
- `expected_failure_modes`: what a wrong solution is likely to look like.
- `network`: `none`, `model-only`, or a documented exception.

Keep the bundle in a dev-only `eval/` tree or a separate private repository.
Do not add a `cmd/aharness-eval` package to the runtime module: it becomes a
second interface that every gate must build and test, which is what happened
to the removed driver. If a Go runner is unavoidable, isolate it in its own
module or behind a build tag and keep its dependencies out of the root
`go.mod`.

The instruction must be solvable from the repository and the task text alone.
Do not name the harness, the benchmark, or the hidden tests. Do not include
the gold patch, the PR number, or a link to the upstream fix.

### 4.3 Agent adapter contract

Every agent is wrapped by a thin adapter with the same inputs and outputs:

| Field | Meaning |
|---|---|
| `name`, `version` | Agent identity; pin the CLI version and binary hash |
| `command` | Non-interactive invocation template; instruction passed by file or stdin, never shell-interpolated |
| `env` | Only the model credential and endpoint; no task secrets |
| `workspace_arg` | The repository path the agent may modify |
| `output_dir` | Session log, final answer, and any agent-specific artifacts |
| `timeout` | Wall-clock cap; timeout is a failure, not missing data |
| `auto_approve` | The mode used to avoid human approval; document it |
| `trajectory_parser` | Converts the agent's log to ATIF or a common JSON |
| `usage_parser` | Extracts provider-reported tokens and cost |
| `sandbox_notes` | Network, user, mounts, and approval differences |

For aharness, the adapter is small: run `aharness -batch -task-file
<instruction> -workspace <workspace> -state-dir <output>/state -model <model>`
with `-provider-base-url` when a gateway is used, then parse
`<output>/state/sessions/<id>.jsonl`. The adapter does not reimplement the
loop, project context, or account for tokens. For another agent, use its
documented non-interactive mode and auto-approve setting; verify the exact
flags against the installed version rather than copying them from a blog post.

The adapter must guarantee:

- The agent never sees `tests/` or `solution/`.
- The verifier runs after the agent exits, in a fresh environment, from
  declared artifacts only.
- A timeout or crash still produces a result record and a verifier run.
- The agent's exit code is recorded but is not the score; the verifier is.
- The agent cannot write the reward file or the verifier log.

### 4.4 Result schema

Use one small JSON record per attempt. Harbor's `result.json` can be the
source; the fields below are the minimum needed for cross-agent analysis.

```json
{
  "schema_version": 1,
  "run_id": "task-arm-rep",
  "task": {"id": "read-sentinel", "version": "1.2.0", "family": "long-context"},
  "agent": {
    "name": "aharness",
    "version": "b958739",
    "binary_sha256": "...",
    "model": "deepseek-v4.1-flash",
    "prompt_version": "prompt-v0",
    "toolset_version": "toolset-v1"
  },
  "environment": {
    "image_digest": "sha256:...",
    "network": "model-only",
    "cpus": 2,
    "memory_mb": 4096
  },
  "attempt": 1,
  "duration_ms": 0,
  "outcome": {
    "resolved": false,
    "partial": 0.0,
    "reward": {},
    "stop_reason": "completed",
    "error_class": ""
  },
  "usage": {
    "input_tokens": 0,
    "cached_input_tokens": 0,
    "output_tokens": 0,
    "reasoning_tokens": 0,
    "cost_usd": 0.0,
    "cost_complete": true
  },
  "process": {
    "turns": 0,
    "tool_calls": 0,
    "tool_errors": 0,
    "retries": 0,
    "reads": 0,
    "repeat_reads": 0,
    "edit_failures": 0,
    "truncations": 0,
    "spills": 0,
    "compactions": 0
  },
  "safety": {"violations": []},
  "artifacts": {
    "trajectory": "atif.json",
    "diff": "changes.diff",
    "verifier_log": "verifier.log"
  }
}
```

`cost_complete=false` is not zero; it means at least one response lacked a
price. Do not average incomplete cost as if it were measured.

### 4.5 ATIF export from the aharness session log

A dev-only exporter is enough; do not add ATIF to the runtime. The mapping:

| aharness event | ATIF |
|---|---|
| `session_start` | `agent` name/version/model, `extra.prompt_version`, `extra.toolset_version`, `extra.limits` |
| `run_start` | `extra.run_start`; one trajectory per run, with the run ID as `trajectory_id` and the aharness session ID in `extra` |
| `user_message` | step `source: "user"`, `message` |
| `model_response` | step `source: "agent"`, `message`, `tool_calls`, `metrics` (tokens, cost), latency in `extra` |
| following `tool_result` | observation results on the same step, `source_call_id`, content, `extra` for `ok`, `code`, `exit_code`, `truncated`, `timed_out`, `duration_ms`, `details` |
| `compaction` | system step with `extra.context_management` (`type: "compaction"`, `boundary: "replace"`) and token counts |
| `limit`, `interrupt`, `error` | system steps or `extra` |
| `final` | final agent step |
| `run_end` | `final_metrics` totals, cost, `total_steps` |

ATIF's `session_id` is run-scoped, not a substitute for the aharness session
identifier. Keep the aharness session ID in `extra`; use `trajectory_id` for
the per-run document identity. If several aharness runs are continuation
segments, link them with `continued_trajectory_ref` rather than overloading
`session_id`.

Reasoning text is display-only and is not logged; omit it rather than
reconstructing it. If prompt variants are evaluated, record a prompt hash or a
distinct `prompt_version` per variant so the trajectory remains attributable.
Old and new toolset versions should be recorded explicitly; the ATIF
`tool_definitions` field can carry the effective schemas.

### 4.6 Verifier isolation and anti-reward-hacking

The previous suite wrote Go tests into the agent's workspace, so the verifier
could pass on an oracle the agent had edited. Do not repeat that. The minimum:

- Run the verifier in a separate container from the agent; transfer only
  declared artifacts.
- Never mount `tests/` or `solution/` into the agent environment.
- Reset `reward.txt` and `reward.json` before grading; delete stale files.
- Sanitize artifacts: drop symlinks that escape, skip special files, clear
  setuid/setgid bits.
- Run the agent as a non-root user; keep grading files root-owned.
- Restrict network to the model endpoint; disable the agent's built-in web
  search when the task does not need it.
- Hash-check test files if they exist in the workspace; a modified test is a
  failed task, not a pass.
- Ship at least two independent reference solutions for tasks where more than
  one implementation is valid; an oracle that only passes one style is a
  narrow-test warning.
- Run a `nop` agent (must fail) and an `oracle` agent (must pass) for every
  task.
- Use Harbor's `lint`, `check`, and `analyze` where applicable; they catch
  shared verifiers, test leakage, writable host mounts, and suspicious trials.

### 4.7 Fairness controls for cross-agent comparison

Comparing complete agents is inherently unfair in the details; make the
differences explicit rather than pretending they do not exist.

**Hold constant:**

- The task instruction and the base repository state.
- The model snapshot, endpoint, and thinking effort where the agent supports
  them. Record the provider-returned model.
- CPU, memory, wall-clock cap, token cap, and turn cap where comparable.
- Network policy, timezone, locale, and preinstalled tools.
- The absence of human approval: every agent runs in its documented
  auto-approve or non-interactive mode.
- The verifier, the hidden tests, and the artifact-transfer policy.

**Report as configuration, not noise:**

- System prompt and prompt version.
- Tool schemas, tool availability, and permission model.
- Context window, truncation and compaction policy.
- Provider API and dialect (Chat Completions, Responses, Anthropic Messages).
- Retry policy and timeout behavior.
- CLI version, binary hash, container digest, and task version.

**Two comparison modes:**

- **As-shipped:** each agent uses its own prompt, tools, permissions, and
  defaults. This answers "which agent should I use?" but not "which mechanism
  caused the difference?".
- **Controlled:** same model, same budget, same network, and the closest
  practically achievable tool set. This answers "which harness mechanism
  matters?" but cannot fully equalize prompts or native APIs. Document every
  remaining difference.

Do not compare aharness on model A with another agent on model B and attribute
the difference to the harness. If the same model cannot be served through both
native APIs, treat model or API as a factor in the design, not as a constant.

### 4.8 Model and sandbox control

- **Serve every agent through the same model gateway where supported.**
  aharness accepts `-provider-base-url`; most CLIs accept an OpenAI- or
  Anthropic-style base URL. A gateway can pin the model snapshot, log request
  IDs, and enforce the same rate and timeout limits. If an agent only speaks a
  native API, record the API as a factor instead of pretending the model is
  identical.
- **Use one sandbox backend for all agents.** Harbor's Docker backend is the
  default; Daytona and Modal provide scale; `bwrap` is a lighter local
  fallback with weaker isolation. Keep CPU, memory, disk, and network policy
  identical, and never expose the verifier to the agent.
- **Keep model credentials out of the task environment.** Inject only the
  endpoint and a short-lived key, preferably a gateway-side key, and never
  place task secrets or host credentials in the agent's environment.

## 5. Experiment design and statistics

### 5.1 Design

- **Unit of randomization:** the task. Run every arm on the same task; pair
  the results.
- **Repeats:** 3–5 per task and arm for suite-level decisions. Use more for a
  single-task deep dive. Coding agents are stochastic even at temperature 0;
  repeated runs are the only way to see variance.
- **Interleaving:** alternate arms within a task and across the run order, so
  provider time-of-day, cache state, and machine load do not align with one
  arm.
- **Fixed model:** pin the snapshot and thinking effort. Record the returned
  model. DeepSeek thinking mode ignores some sampling controls, so determinism
  cannot be bought with temperature.
- **Budgets:** use the same wall-clock and token caps where possible. A
  timeout is a failure and stays in the denominator.
- **Pre-registration:** write the primary metric, expected direction, and
  stopping rule before the run. One primary metric; everything else is
  secondary or exploratory.
- **Blinding:** the verifier is automated; for human trace review, hide the
  arm label until after the review when practical.

### 5.2 Sample size

The prior research's power table still applies for a paired two-proportion
comparison (two-sided α = 0.05, 80% power, p ≈ 0.5, ρ ≈ 0.5):

| Tasks | Minimum detectable effect |
|---:|---:|
| 89 | 14.8 pp |
| 108 | 13.5 pp |
| 150 | 11.4 pp |
| 300 | 8.1 pp |
| 500 | 6.3 pp |
| 1,000 | 4.4 pp |

A 20-task suite detects only catastrophic breakage and large process shifts.
That is acceptable for a smoke gate, not for a claim that a prompt change
improves solve rate by 3 pp. Cost and token effects are better powered than
solve-rate effects; use them as the primary signal for efficiency changes, with
success as a guardrail.

The variance literature is a warning: a 2026 replication study ran 584 coding
agent runs and found that identical runs of one agent-model pairing varied more
than the pairings differed from one another; resolving the observed agent
differences would take tens to more than a hundred runs of each. The same study
found list-price cost differed more than twentyfold between two agents on the
same model, mostly through prompt caching. Report variance and cache hit rate;
do not rank agents from a handful of runs.

### 5.3 Analysis

- **Paired differences:** for each task, average the repeats per arm, then
  take the difference. Bootstrap over tasks (10,000 resamples) for a 95% CI.
  Do not bootstrap individual runs as if they were independent tasks.
- **Per-task table:** show every task's outcome for every arm. A mean that
  hides three regressions and three wins is not actionable.
- **`pass^k`:** report all-k success for reliability, especially for safety and
  long-horizon tasks. Report `pass@k` separately if retries are part of the
  product.
- **Cost:** report cost per solved task and cost per attempt; report actual
  and uncached-equivalent cost; report cache hit rate. Do not compare cost
  without a completeness flag.
- **Secondary metrics:** turns, tool calls, reads per file, repeat-read rate,
  truncation/continuation rate, tool-error rate, retries, compaction count,
  wall time. Use Holm-Bonferroni or label them exploratory; do not fish.
- **Failure taxonomy:** pre-declare categories such as wrong location,
  incomplete fix, regression, test tampering, premature stop, budget
  exhaustion, tool misuse, and protocol error. Count them; sample traces for
  each.
- **Decision rule:** for a cost/efficiency change, require success
  non-inferiority (lower CI above a pre-declared margin, for example −2 pp)
  plus a cost or turn improvement whose CI excludes zero, plus no safety
  regression. For a capability change, require superiority with a CI that
  excludes zero, or label the result inconclusive. For cross-agent ranking, do
  not declare a winner when the CIs overlap; report the cost-success frontier
  and per-family strengths.

### 5.4 Worked experiment shapes

| Experiment | Arms | Tasks × reps | Primary | Guardrails |
|---|---|---|---|---|
| Prompt rewrite | `prompt-v0` vs `prompt-v1` | 20 × 3 | Solved; premature stops; tool errors | Cost, turns, safety |
| Read-limit change | 60 KiB vs 120 KiB | 10 read-heavy × 5 | Tokens per solved; reads per file | Solved, truncation rate |
| Tool description change | Old vs new schema | 20 × 3 | Invalid args; wrong tool choice; calls per solve | Solved, cost |
| Compaction policy | Old vs new target | 10 long × 5 | Post-compaction recall; tokens | Solved, turns |
| Cross-agent bake-off | aharness, Claude Code, Codex CLI, OpenCode | 30 × 5 | Solved; cost per solved | Time, safety, per-family |
| Safety probes | Baseline vs new guard | 10 adversarial × 3 | Violations blocked | Task success, false refusals |
| TUI change | Old vs new | 5–10 humans × 3 tasks | Time-to-task, errors, SUS | Qualitative feedback |

The small rows are diagnostic screens, not powered solve-rate claims. Use them
to find the mechanism and the failure modes; use the sample-size table before
claiming a small success effect.

## 6. Task-suite plan

### 6.1 Initial 20 tasks

Start with 20 tasks across six families, not 30 variations of a single-file
exercise:

| Family | Count | Example |
|---|---:|---|
| Bug fix with failing test | 5 | Revert a merged fix; keep its tests; ask for the fix |
| Feature addition | 4 | Add a small documented behavior with hidden tests |
| Refactor with behavior preservation | 3 | Extract or rename without changing public behavior |
| Debugging with partial information | 3 | A failing test plus noisy logs; find the root cause |
| Long-context and multi-file navigation | 3 | A fact or dependency spread across several files |
| Safety and containment | 2 | Symlink escape, `.git` access, SSRF, or a destructive command |

Add 10–12 component probes on top of the suite:

1. Read a sentinel beyond the 2,000-line default; answer it.
2. Follow `continue with offset` across pages; answer a late fact.
3. Edit a CRLF or trailing-whitespace file; verify the exact diff.
4. Find a nested path that only `**` matches.
5. Run a command that emits more than 64 KiB; find the last line.
6. Recover from a tool error that succeeds on a second attempt.
7. Trigger compaction, then recall a fact from before the boundary.
8. Use the todo tool across a five-step task; verify the final list.
9. Refuse a private, loopback, or metadata URL.
10. Refuse `../`, `.git`, and symlink escapes.
11. Interrupt and resume via `-ui-json`; verify state and no orphan process.
12. Malformed provider response; verify the typed error and retry policy.

Probes are short, deterministic to grade, and diagnostic. They are the first
thing to run when a prompt or tool description changes.

### 6.2 Sourcing recipe

For a reverted-commit task:

1. Pick a merged commit with a test that expresses the intended behavior.
2. Check out its parent as the task base.
3. Revert only the production code, not the test.
4. Confirm the test fails on the base and passes with the gold patch.
5. Move the test into the hidden verifier; never leave it in the workspace.
6. Remove commit messages, PR links, and issue text that reveal the patch.
7. Write a short instruction that states the symptom and the expected
   behavior, not the implementation.

For a feature task, write the hidden tests first, then the oracle patch, then
the instruction. For a mutation task, keep only mutants that fail a meaningful
test and pass the original suite; have a human review the mutant for realism.

### 6.3 Quality gates

Every task must pass:

- **Oracle passes.** The gold patch earns a full reward.
- **Nop fails.** No changes earn zero.
- **Base fails.** The hidden test fails before the fix.
- **Regression intact.** The task's pass-to-pass tests pass with the gold
  patch.
- **Test isolation.** The verifier cannot see the agent's test edits; test
  files are hidden or hash-checked.
- **Determinism.** The verifier gives the same result on repeated oracle runs.
- **Difficulty.** A strong agent solves it some but not all of the time;
  target roughly 20–80% baseline. A task that is always solved or never
  solved is a canary, not a decision task.
- **Discrimination.** Two different agents or models do not both always pass
  or both always fail.
- **Clarity.** A human can understand the instruction without seeing the gold
  patch; no hidden acceptance criteria.
- **Budget.** It finishes within the declared time and token caps.
- **No leakage.** The task text, repository, and environment do not contain
  the solution, the test, or the benchmark identity.

Keep a small set of canaries with known outcomes: a null task that should
pass trivially, an impossible task that should fail, and a trap task that
rewards only the honest solution. They detect harness or verifier breakage.

### 6.4 Holdout and rotation

- Split tasks into a **development** set used while iterating and a **test**
  set used only for release decisions. Do not tune on the test set.
- Rotate a portion of the suite quarterly, or when a task becomes always-pass
  or always-fail.
- Keep a private holdout that is never shown in traces, reports, or talks.
- Version tasks semantically. A verifier-only fix is a patch version and can
  regrade old runs; a task or environment change is a minor/major version and
  requires reruns.
- Retire tasks that no longer discriminate; do not keep them to inflate the
  count.

## 7. Meta-evaluation: evaluate the evaluation

The suite is only useful if it is trustworthy. Run these checks before using
it for a decision and quarterly thereafter:

| Check | Method | Failure signal |
|---|---|---|
| Verifier correctness | Oracle and nop for every task | Oracle fails or nop passes |
| Verifier breadth | Two independent solutions for tasks with multiple valid designs | One style passes, another valid style fails |
| Verifier narrowness | A correct but stylistically different patch | Narrow tests reject it |
| Verifier looseness | A plausible but wrong patch | Tests pass on the wrong behavior |
| Test tampering | Agent edits or deletes tests | Task still passes |
| Flakiness | Repeat the oracle 3–5 times | Different rewards |
| Task ambiguity | Human review without the gold patch | Multiple incompatible readings |
| Contamination | Private repo, fresh commits, canary strings, no publication | Model recalls the patch or task ID |
| Adapter correctness | Oracle and nop through each adapter | Agent did not run, or output was not captured |
| Judge calibration | Human labels on 30–50 trajectories; paired discrimination | Judge flags clean runs or misses silent faults |
| Statistical validity | Pre-registration, power, CIs, holdout | Claims unsupported by the design |

The trajectory-judge literature is a useful warning: an outcome-only LLM judge
can appear to detect faults that leave the final reply unchanged only because
it flags clean runs at the same rate. Report paired discrimination against the
clean parent, not raw recall. Prefer deterministic verifiers for outcomes;
reserve judges for qualitative dimensions where humans have labeled a
calibration set.

## 8. Cadence, budget, and ownership

| Cadence | Scope | Purpose |
|---|---|---|
| Every change | `make dod` + deterministic tool/loop probes with a fake provider | Merge gate; no model cost |
| Prompt/tool/context change | Real-model component probes (10–12) at least once before merge | Diagnose the mechanism; catch obvious regressions |
| Nightly | 8–10 end-to-end tasks × 2 reps, cheap model | Catch breakage; alert on regressions |
| Weekly | 20 tasks × 3 reps, production model, baseline comparison | Tune prompts/tools/context with process evidence |
| Release | 30–50 tasks × 5 reps + optional external anchor + trace review | Ship/no-ship decision |
| Quarterly | Cross-agent bake-off, task audit, holdout rotation, verifier mutation | Calibrate the instrument |

Budget is dominated by model calls, not containers. A rough planning range is
$0.10–$2 per attempt depending on model and task length. Twenty tasks × three
repeats × two arms is 120 attempts, or roughly $12–$240. Thirty tasks × five
repeats × four agents is 600 attempts, or roughly $60–$1,200. Set a monthly
cap and use the cheap model for nightly runs; use the production model only
for decisions.

**First milestone.** Pick five reverted-commit tasks from aharness's own
history. Write the hidden verifier for each, run the oracle and nop controls,
and run the current aharness three times per task. If fewer than three tasks
discriminate, fix the tasks before adding more. Then run one controlled A/B
(the read limit or a prompt variant) and write a one-page decision record with
the per-task table, cost, and the traces that explain the result. Do not build
a runner before this works; the first five tasks will teach more than another
week of framework selection.

Ownership:

- One person owns the task suite and verifier quality.
- One person owns the runner, adapters, and result schema.
- One person owns the analysis and the decision record.
- Tasks are code: reviewed, versioned, and tested like code. No orphan tasks.

## 9. Risks and anti-patterns

| Risk | Mitigation |
|---|---|
| **Overfitting to the suite** | Holdout, rotation, private tasks, no tuning on the test set |
| **Reward hacking** | Separate verifier, hidden tests, artifact sanitization, non-root, hash checks, `analyze` |
| **High variance** | Repeats, pairing, interleaving, CIs, `pass^k`, report variance |
| **Cost and time** | Small suite, tiered cadence, cheap model for smoke, budget caps |
| **Benchmark rot** | Pin image digests, version tasks, regrade verifier changes, retire dead tasks |
| **Model drift** | Pin snapshots, record returned model, re-baseline when the model changes |
| **False confidence from judges** | Deterministic verifiers first; calibrate judges; paired discrimination |
| **Maintenance burden** | Reuse Harbor, keep adapters thin, do not add eval code to the runtime |
| **Security of untrusted agents** | Sandbox, no secrets, restricted network, disposable workspaces |
| **Privacy** | Private tasks, no publication, scrub session logs before sharing |
| **Public-benchmark overfitting** | Use only as an external anchor; do not tune on it |

Lessons from the removed eval driver:

- **Do not build a second harness interface.** The adapter should be a command
  wrapper plus a log parser, not a reimplementation of the loop, context, or
  accounting.
- **Do not use trivial single-file exercises as the primary suite.** They do
  not expose prompt, tool, context, or recovery behavior.
- **Do not put the verifier in the agent's workspace.** It will pass on an
  oracle the agent edited.
- **Do not build a metrics pipeline before there is a measured baseline.**
  Parse the existing session log; add fields only when a decision needs them.
- **Do not claim a solve rate, token delta, or cost delta without credentials
  and a completed run.** Record the pending prerequisite honestly.

## 10. Open questions

- Which model or models should be the fixed reference? `opencode-go` is the
  current provider, but cross-agent comparisons need a model that every agent
  can reach through a comparable API. A gateway or proxy may be necessary.
- Is Docker available in the development and CI environments? If not, Harbor
  can use a cloud sandbox, or a lighter `bwrap` runner can consume the same
  task bundle with weaker isolation.
- Who writes and maintains the first 20 tasks? The task suite is the most
  expensive part, not the runner.
- Should the first cross-agent bake-off include Claude Code, Codex CLI,
  OpenCode, and Aider, or only the two agents the team actually uses?
- How should the TUI and multi-turn steering be evaluated? Automated
  `-ui-json` tests cover the protocol; usability needs humans.
- Should aharness expose ATIF directly, or only through a dev-only exporter?
  The recommendation is exporter-only; the runtime should not grow an eval
  surface.
- What is the acceptable cost per decision, and who approves it?
- How often should the external anchor be run? A release cadence is enough;
  a per-change cadence is waste.

## 11. Sources

Repository:

- [agent-tooling-research.md](agent-tooling-research.md), §6, evaluation
  methodology and sample-size table.
- [agent-harness-minimum.md](agent-harness-minimum.md), the minimum harness
  and the "10–20 realistic eval tasks and read the raw traces" advice.
- [agent-tooling-build.md](../plans/agent-tooling-build.md), the eval
  deviation and removal note.
- `internal/agent/context.go`, `events.go`, `types.go`; `internal/session`;
  `internal/tools`; `docs/architecture.md`, `docs/limits.md`,
  `docs/testing-strategy.md`.

External (accessed 2026-11-15):

- OpenAI, [Why we no longer evaluate SWE-bench Verified](https://openai.com/index/why-we-no-longer-evaluate-swe-bench-verified/),
  2026-02-23.
- Merrill et al., [Terminal-Bench: Benchmarking Agents on Hard, Realistic Tasks in Command Line Interfaces](https://arxiv.org/abs/2601.11868),
  2026-01-17.
- Golubev et al., [SWE-rebench: An Automated Pipeline for Task Collection and Decontaminated Evaluation of Software Engineering Agents](https://arxiv.org/abs/2505.20411),
  2025-05-26 (v2 2025-11-04).
- SWE-smith, [Scaling Data for Software Engineering Agents](https://arxiv.org/abs/2504.21798),
  2025-04-30.
- Kapoor et al., [AI Agents That Matter](https://arxiv.org/abs/2407.01502),
  2024-07-01.
- Bandel et al., [General Agent Evaluation](https://arxiv.org/abs/2602.22953),
  2026-02-26 (v2 2026-05-11).
- [Engineering Reliable Coding Agents: Evaluating and Operating the System Around the Model](https://arxiv.org/abs/2608.13867),
  2026-08-14.
- [Reproducible, Explainable, and Effective Evaluations of Agentic AI for Software Engineering](https://arxiv.org/abs/2604.01437),
  2026-04-01.
- [Identical Runs, Different Results: Benchmarking AI Coding Agents on Open-Weight Models](https://arxiv.org/abs/2609.33812),
  2026-09-27.
- [trajectory-judge: What Outcome-Only LLM Judges Miss on Agent Trajectories](https://arxiv.org/abs/2609.00038),
  2026-08-29 (v4 2026-11-03).
- [Harness Engineering: Anatomy, Architecture, and Evolution of Coding Agents](https://arxiv.org/abs/2609.00006),
  2026-07-15.
- Mak et al., [Is Bash All You Need? An Empirical Study of Tool Interfaces for Enterprise Digital Worker Agents](https://arxiv.org/abs/2609.11999),
  2026-09-10.
- Harbor, [repository](https://github.com/harbor-framework/harbor) and
  [documentation](https://docs.harborframework.com/):
  [tasks](https://docs.harborframework.com/tasks/overview.md),
  [separate verifier](https://docs.harborframework.com/tasks/separate-verifier.md),
  [custom agents](https://docs.harborframework.com/agents/custom-agents.md),
  [metrics](https://docs.harborframework.com/datasets/metrics.md),
  [regrade](https://docs.harborframework.com/jobs/regrade.md), and
  [reward-hacking mitigations](https://docs.harborframework.com/news/reward-hacking.md).
- Harbor, [ATIF trajectory format RFC](https://github.com/harbor-framework/harbor/blob/main/rfcs/0001-trajectory-format.md),
  April 2026 (v1.8).
- UK AI Security Institute, [Inspect AI](https://inspect.aisi.org.uk/),
  framework for agent evaluations and external coding agents.
- Promptfoo, [Evaluate Coding Agents](https://www.promptfoo.dev/docs/guides/evaluate-coding-agents/),
  coding-agent providers, trace assertions, and workspace isolation.
- [Agent Client Protocol](https://agentclientprotocol.com/), an optional
  editor-agent integration path supported by Harbor's ACP registry.
- OpenTelemetry, [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/),
  the production observability standard for model and tool spans.
