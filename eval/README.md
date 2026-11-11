# aharness evaluation suite

Dev-only A/B tooling for `aharness`: a task suite, a driver that runs one or
more harness binaries, session-JSONL metrics, tier-aware cost, and paired
bootstrap comparisons. It is not part of the shipped runtime and imports only
the standard library plus `internal/session`.

## Layout

```text
eval/
  tasks/<id>/
    task.md       # prompt passed to the harness
    setup.sh      # creates the fresh workspace; nonzero exit marks the run invalid
    verify.sh     # exits 0 only when the task is solved
    meta.json     # {"cluster","language","timeout","notes"}
  tasks/...       # 30 tasks across go-fix, text-transform, shell-pipeline
  prices.json     # USD per 1M tokens, optional UTC peak windows
  baseline.json   # recorded baseline status (not a fabricated result)
  fixtures/fake-harness.sh  # development smoke arm; writes a synthetic session
  results/        # generated output (git-ignored)
```

`cmd/aharness-eval` is the driver. Task scripts run as
`/bin/sh <script>` with cwd set to the task directory and these environment
variables:

- `WORKSPACE`: a fresh temporary workspace for this run.
- `TASK_DIR`: absolute task directory.
- `TASK_ID`: the task id (directory name).

Every run gets a new workspace and state directory under one temporary root.
The harness is invoked as:

```sh
<arm> -batch -workspace <tmp> -state-dir <tmp-state> \
  -task-file <task>/task.md -model <model> \
  -thinking-effort <effort> -require-usage
```

The driver inherits its own environment (so provider credentials are
available) and additionally sets `AHARNESS_LOG_REQUESTS=1` for the harness
phase only. That produces the `model_request` events used by the estimator
metric. Setup and verification keep the unmodified environment.

## Invocation

```sh
go run ./cmd/aharness-eval \
  -tasks eval/tasks \
  -arm before=./bin/aharness-before \
  -arm after=./bin/aharness-after \
  -model deepseek-v4.1-flash \
  -thinking-effort high \
  -repeats 3 \
  -prices eval/prices.json \
  -seed 1 \
  -out eval/results/read-limits
```

Flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-tasks` | `eval/tasks` | task suite directory |
| `-arm` | none (repeatable, required) | `name=path`; path must be an executable regular file |
| `-model` | none (required) | model id passed to each arm |
| `-thinking-effort` | `default` | effort passed to each arm |
| `-repeats` | `3` | repetitions per task and arm |
| `-seed` | `1` | arm rotation and bootstrap seed |
| `-prices` | `eval/prices.json` | price file; pass an empty value to rely on `run_start.pricing` |
| `-out` | `eval/results/latest` | output directory |

Arm order is deterministic and balanced. For task index `i`, zero-based repeat
`r`, seed `s`, and `n` arms, the first arm is
`(i + r + s) mod n`; remaining arms follow cyclically. Arms are interleaved
within each task/repeat, and every run records the full `arm_order`.

A development smoke that needs no provider credentials:

```sh
go run ./cmd/aharness-eval \
  -tasks eval/tasks \
  -arm fake=eval/fixtures/fake-harness.sh \
  -model deepseek-v4.1-flash -thinking-effort high \
  -repeats 1 -prices eval/prices.json -seed 1 \
  -out /tmp/aharness-eval-smoke
```

The fake arm only writes a synthetic session; real `verify.sh` scripts still
decide the outcome, so the smoke normally reports `unsolved`, not a solve rate.

## Run statuses

- `invalid`: setup exited nonzero, timed out, or could not start. Excluded from
  solve rates, token/cost means, and paired statistics. A record is still
  written.
- `error`: the harness timed out, failed to start, exited nonzero, or the run
  did not produce exactly one readable session log. Counted as unsolved in the
  solve-rate denominator, never as solved. Verify still runs so the record
  shows whether the work happened to verify.
- `unsolved`: verify exited nonzero or timed out.
- `solved`: verify exited 0 and the harness exited 0 with a readable session.

Exit code `-1` in `setup_exit`, `verify_exit`, or `harness_exit` means the
process was killed by a timeout or failed to start; `error` carries the reason.
A missing phase (for example verify after an invalid setup) is JSON `null`.

## Metrics

`results.jsonl` has one record per run. `metrics` contains values extracted
from the single session log:

- `turns`: `run_end.run_turns`, falling back to the largest envelope turn or
  the response count for synthetic logs.
- `tool_calls`: number of `tool_result` events (executed calls).
- `read_calls_per_file`, `read_calls`, `unattributed_reads`, `repeated_reads`:
  `read_file` calls attributed by tool-call arguments `path`, then by
  `tool_result.details.path`/`details.file`. Repeated reads are
  `sum(calls-1)` over paths with more than one call.
- `truncated_results`: tool results with `truncated=true`.
- `continuation_results`: tool results whose output contains
  `continue with offset=`.
- `retries`: `model_retry` events. `tool_errors`: tool results not `ok`.
- Token totals and `usage_responses`: only responses whose usage is observed.
  Usage counts as observed when `usage.reported` is true or the input+output
  totals are nonzero (old/fake logs without the flag). A missing usage object
  is never averaged as zero: it increments `missing_usage_responses`, and token
  means use only observed usage.
- `estimator_*`: `model_request.estimated_input_tokens` compared with the next
  `model_response` input tokens. Sums are recorded per run; the summary reports
  mean absolute error and the total estimated/actual ratio.
- `cost_micro_usd`, `cost_responses`, `cost_complete`: computed cost and
  completeness. A run is fully priced only when every response has observed
  usage and a usable price.

`duration_ms` is the wall time of the harness process.

## Cost and prices

`prices.json` is USD per 1M tokens:

```json
{
  "currency": "USD",
  "unit": "per_million_tokens",
  "models": {
    "model-id": {
      "input": 0.15,
      "output": 0.6,
      "cache_read": 0.003,
      "cache_write": 0.18,
      "peak": {
        "multiplier": 2.0,
        "windows_utc": [{"start": "01:00", "end": "04:00"}]
      }
    }
  }
}
```

For each `model_response`:

```text
uncached = max(0, input - cached - cache_write)
cost_micro_usd = round_half_up(
    (uncached*input_rate + cached*cache_read_rate
     + cache_write*cache_write_rate + output*output_rate) / 1e6
)
```

Rates are converted to micro-USD per 1M tokens. The response event timestamp
(UTC) selects the peak multiplier; windows are `[start, end)` and may wrap
midnight. `cache_read` falls back to `input`; an absent `cache_write` rate is
charged at zero. If the model is not in the price file, the driver uses the
`run_start.data.pricing` snapshot (already micro-USD per 1M tokens) when
available; otherwise cost is unavailable and the run is excluded from cost
means. Cost means use only fully priced runs; the summary reports priced,
partial-cost, and unpriced run counts per arm.

## Statistics

`summary.json` and `summary.md` report, per arm: runs, valid runs, solved,
unsolved, errors, invalid, solve rate, mean turns, mean tool calls, tokens per
solved task (input/output/cached/reasoning/total), mean cost, cost
completeness, missing usage, per-file read calls, repeated reads,
truncated/continuation counts, retries, tool errors, wall time, and estimator
error.

For every non-reference arm vs the first arm, the driver reports paired
comparisons for solve rate, turns per run, tokens per solved task
(input+output), and cost per fully priced run:

- Values are paired by task, averaging repeats within a task. A task only
  contributes when both arms have a defined value (for example, tokens per
  solved task requires a solved run with observed usage).
- Effect is the mean of per-task differences (arm minus reference).
- Task CI is a 95% percentile bootstrap over tasks with 10,000 resamples.
- Cluster CI resamples `meta.json` clusters with replacement and keeps all
  tasks in each selected cluster; `cluster_se` is the bootstrap standard
  deviation, `cluster_ci95` the percentile interval, `cluster_n` the number of
  clusters contributing pairs.
- Resampling is deterministic for a given seed, metric, and arm order.
- `cost_delta_micro_usd` is the difference of per-run fully priced means over
  all valid runs, independent of the paired pairing.

Output ordering is deterministic: tasks by id, arms by flag order, read-call
rows sorted by path, and paired comparisons in arm order. No timestamps are
written into the summaries.

## Limitations

- **Baseline is unmeasured.** `eval/baseline.json` records
  `{"status":"unmeasured","reason":"provider credentials unavailable",...}`.
  A real solve-rate baseline requires configured provider credentials and a
  built `aharness` binary; do not invent one.
- Results contain prompts, model output, and capped harness stdout/stderr
  tails. Treat result directories as potentially sensitive.
- `session_path` is the temporary path used during the run. The temporary
  workspace and state directory are deleted after metrics are read, so the
  path is historical.
- A 30-task suite is a smoke and call-count/token instrument, not a 5
  percentage-point solve-rate detector. Expand and hold out tasks before making
  strong solve-rate claims.
- Task scripts must stay deterministic and offline. The Go cluster runs
  `go test` with `GOPROXY=off`, `GOTOOLCHAIN=local`, and a workspace-local
  build cache.
