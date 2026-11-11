# aharness default run-limit removal build plan

- **Date:** 2026-11-11
- **Status:** implemented (2026-11-11). The architecture, limits, testing
  strategy, and TUI docs now describe the unbounded-run contract; §11 records
  the implementation deviations from this plan.
- **Source:** the shipped v1 run budgets and the output-cap decision in
  [model-discovery-build.md](model-discovery-build.md) §3.4.
- **Target:** Go 1.27.3, module `aharness`.
- **Architecture:** [architecture.md](architecture.md) §4 (agent loop), §9
  (CLI/config), and §10 (defaults).
- **Limits:** [limits.md](limits.md) §1 and §5.
- **Testing:** [testing-strategy.md](testing-strategy.md) plus §6 here.
- **Definition of done:** §1.4.

## Decision summary

- Remove the default limits: max turns, cumulative total tokens, run wall
  clock, and model-call timeout default to unlimited (`0`).
- Keep the flags as opt-in budgets. An explicit positive value restores the
  current behavior; `0` means unlimited; negative values are config errors.
  Recorded budgets are historical and are not inherited on resume/fork.
- Expose the model-call timeout as `-model-timeout`, default `0`. It was
  previously a fixed 300-second constant.
- A resumed or forked session does not inherit recorded run budgets. Old
  `session_start`/`run_start` values remain historical metadata; new
  `run_start` events record `0` unless the operator sets a flag.
- Context management stays: the model context window, input cap, safety
  margin, compaction target, projection, and `token_limit` on an unrecoverable
  context fit are unchanged.
- The harness still terminates on completion, provider/protocol errors,
  interruption, context-fit failure, repeated output-length truncation, and
  repeated identical tool failures. Those are error/termination policies, not
  generation budgets.
- No session schema change. The v2 event fields remain; `0` is the unlimited
  value and old logs remain readable.

## 1. Scope

### 1.1 What this plan owns

| Area | Implementation |
|---|---|
| Turn budget | `-max-turns` default `0` (unlimited); explicit positive values enforce the cumulative turn check. |
| Token budget | `-max-total-tokens` default `0` (unlimited); explicit positive values enforce the cumulative usage check. |
| Run wall clock | `-session-timeout` default `0` (no deadline); explicit positive values enforce the per-run deadline. |
| Model call timeout | New `-model-timeout` flag, default `0` (no harness deadline); explicit positive values set a per-call context deadline. |
| Replay | Resume/fork ignores recorded turn/token/timeout budgets unless the operator sets a flag. |
| TUI | Unlimited values render as `unlimited`/`none`, not `0/0` or `0s`; the details overlay distinguishes unset from explicit. |
| Session log | Existing fields keep their names; new events record `0` for unlimited. No schema version bump. |
| Documentation | Architecture, limits, testing strategy, TUI doc, README, and this plan. |

### 1.2 What stays the same

- The context window, input cap, safety margin, compaction, projection, and
  `token_limit` on an unrecoverable context fit.
- Tool-call timeout, tool output caps, command timeout, provider request/body
  caps, context-overflow retry, transport attempts/backoff, and streaming
  behavior.
- Repeated identical tool-failure detection (`stuck`) and the consecutive
  output-length stop policy (`token_limit`); see §3.2.
- Steering, interruption, signal handling, and TUI cancellation.
- The v2 session schema, event pairing, durability, resume/fork, and `run_end`
  usage recording.
- The output-budget decision in
  [model-discovery-build.md](model-discovery-build.md) §3.4. This plan does not
  change `-max-output-tokens` or the no-default-output-cap contract.

### 1.3 Not in this plan

- Removing the context window, input cap, compaction, or safety margin. They
  are the only preemptive budget this plan keeps.
- Removing provider transport timeouts, retry attempts, tool timeouts, output
  body caps, or stuck/truncation policies. Those are failure handling, not
  runaway-generation budgets.
- Automatic cost accounting, run parking, log rotation, or background
  supervision for unbounded runs.
- Changing model discovery, provider selection, the output limit, prompt,
  tools, or the wire adapters.

### 1.4 Definition of done

- [x] Defaults are `0` for max turns, max total tokens, session timeout, and
      model timeout; no run is stopped by those budgets unless a flag is
      explicit.
- [x] Explicit positive values still enforce the existing checks and emit the
      existing `limit`/stop statuses; `0` or an omitted flag is unlimited;
      negative values are config errors.
- [x] Resume/fork of a log with recorded `30`/`2,000,000`/`60m`/`300s` does not
      inherit those budgets; the new `run_start` records `0` unless a flag is
      explicit.
- [x] A default run can exceed 30 turns and 2,000,000 cumulative tokens and has
      no harness wall-clock or per-call deadline. It still stops on completion,
      error, interruption, context-fit failure, repeated truncation, or stuck
      detection.
- [x] The TUI shows unlimited values without `0/0` ratios and the details
      overlay distinguishes `unlimited` from an explicit budget.
- [x] `session_start`/`run_start` remain v2 and record `0` for unlimited; old
      logs decode, and old `max_turns`/`time_limit` terminal states still
      render.
- [x] Context compaction, safety margin, retries, and stuck detection are
      unchanged and covered by tests.
- [x] Unit, integration, fuzz, race, build, lint, and vulnerability gates pass.
- [x] README, architecture, limits, testing-strategy, and TUI docs are updated.

## 2. Current state

The shipped defaults are in `cmd/aharness/config.go`:

| Setting | Current default | Current enforcement |
|---|---:|---|
| `-max-turns` | 30 | `checkLimits` stops with `max_turns` before the next model call. |
| `-max-total-tokens` | 2,000,000 | `checkLimits` stops with `token_limit` before the next model call. |
| `-session-timeout` | 60 minutes | A run-context deadline; deadline expiry stops with `time_limit`. |
| model timeout | 300 seconds, not a flag | A per-call context deadline in `completeOnce`; expiry is a retryable transport error. |

Additional current behavior:

- `validateExplicitValues` and `validateEffective` reject zero or negative
  values for all four settings.
- `applySessionDefaults` inherits recorded turn/token/session-timeout values
  when the corresponding flag is not explicit; the model timeout is inherited
  whenever the record is positive.
- `validateSessionRules` rejects a resume/fork that is already at or above an
  inherited turn or token budget.
- `internal/agent/runner.go` already guards every check with `> 0`, so `0`
  already means "do not enforce" inside the loop. The blocker is the config
  layer, inheritance, UI, and docs.
- `session_start` and `run_start` record `max_turns`, `max_total_tokens`,
  `session_timeout_ms`, and `model_timeout_ms`; `agent.Inspect` reconstructs
  them. The values are part of the v2 payload but not the schema version.
- The TUI renders `turns N/M` and `tokens N/M` and lists both timeouts in the
  details overlay.
- Tests cover default values, zero/negative validation, inheritance, the
  runner checks, the session timeout, the model timeout retry, and TUI display.

## 3. Contract

### 3.1 CLI and precedence

| Flag | Default | Behavior |
|---|---|---|
| `-max-turns` | `0` | `0` means unlimited; a positive value is the cumulative model-turn budget; negative is a config error. |
| `-max-total-tokens` | `0` | `0` means unlimited; a positive value is the cumulative input+output token budget; negative is a config error. |
| `-session-timeout` | `0s` | `0s` means no per-run wall-clock deadline; a positive duration applies per run; negative is a config error. |
| `-model-timeout` | `0s` | `0s` means no per-call harness deadline; a positive duration applies per model attempt; negative is a config error. |

Precedence, highest first:

1. Explicit flag value.
2. Default `0` (unlimited).

The session record is historical and does not participate in resolution. A
missing flag and an explicit `0` both mean unlimited; the flag accepts `0` for
symmetry and clear scripting. This deliberately drops the old replay rule for
these four fields; see §3.3.

No environment variables are added. These four flags are per-invocation
controls; a budget is not sticky across runs or resumes. Repeat the flag on each
invocation that should enforce one.

### 3.2 Termination and error behavior

A default run has no turn, cumulative-token, run-clock, or per-call generation
deadline. It terminates only through:

| Condition | Status/exit | Behavior |
|---|---|---|
| Model stops without tool calls | `completed` / 0 | Normal completion. |
| Provider failure, content filter, retries exhausted | `provider_error` / 3 | Existing error event and message. |
| Protocol failure | `protocol_error` / 4 | Existing validation. |
| Batch signal, TUI cancel, or interrupt | `interrupted` / 130 | Whole operation/tool process group canceled. |
| Projection cannot fit the context input cap | `token_limit` / 2 | Existing compaction attempt; stop if it cannot fit. |
| Two consecutive `length` responses without a tool round | `token_limit` / 2 | Existing continuation hint; no change. |
| Repeated identical tool failures | `stuck` / 4 | Existing hint/stop thresholds; no change. |
| Explicit `-max-turns` reached | `max_turns` / 2 | Existing limit event and message. |
| Explicit `-max-total-tokens` reached | `token_limit` / 2 | Existing limit event and message. |
| Explicit `-session-timeout` reached | `time_limit` / 2 | Existing limit event and message. |
| Explicit `-model-timeout` reached | retry or `provider_error` | Existing transport retry policy; exhaustion is a provider error. |

The context window remains the only preemptive model budget. A provider
context-overflow error still uses the existing one-shot compaction retry; an
unrecoverable failure stops with `token_limit` or the provider's typed error.
The HTTP client timeout and transport attempt budget remain as network failure
handling: a request attempt is still bounded, but the harness does not impose a
separate generation deadline unless `-model-timeout` is explicit.

When an explicit budget is hit, the error names the flag and the observed value,
and the existing `limit` event records kind/configured/observed. When the value
is `0`, no `limit` event is emitted for that kind.

### 3.3 Replay and session log

- No schema change. `session_start` and `run_start` keep
  `max_turns`, `max_total_tokens`, `session_timeout_ms`, and
  `model_timeout_ms`.
- New sessions record `0` in all four fields unless the operator sets a flag.
- Resume/fork does not enforce or inherit the recorded values. Old logs remain
  valid historical records: their fields describe the policy of the run that
  wrote them.
- If the operator sets an explicit positive flag on resume/fork, it applies to
  the new run and is recorded in the new `run_start`. An omitted flag or an
  explicit `0` records unlimited.
- Old terminal states `max_turns` and `time_limit`, and old `limit` event kinds,
  remain decodable and displayable. New runs emit them only when an explicit
  budget is configured.
- `agent.Inspect` may continue to decode the fields for history and validation,
  but config resolution must not use them for enforcement. Dead helpers such as
  `MaxTurnsReached`/`TokenBudgetReached` are removed if unused.
- `validateSessionRules` keeps the at-or-over check only when the corresponding
  flag is explicit and positive.

### 3.4 TUI

- The status line shows `turns N` and `tokens N` when the budget is unlimited;
  it shows `N/M` only for an explicit positive budget.
- The details overlay shows `max turns: unlimited`, `total tokens: unlimited`,
  `run timeout: none`, and `model timeout: none` for `0`.
- A resumed session with old recorded budgets shows the new effective values
  (unlimited by default), not the historical ones. The run start values remain
  the source of truth for the next run.
- No new key or protocol frame is required. `-ui-json` frames reflect the same
  labels; no protocol version change.
- Interrupt/cancel behavior is unchanged and is the operator's manual stop for
  an unbounded run.

## 4. Package changes and wiring

| File | Change |
|---|---|
| `cmd/aharness/config.go` | Defaults become `0`; add `-model-timeout`; allow `0`, reject negative; stop inheriting the four budgets; skip session at-limit checks when `0`. |
| `cmd/aharness/run.go` | Pass `0` values through; log the effective budgets as `0`/unlimited; no other run-flow change. |
| `cmd/aharness/tui.go` | Pass `0`; update details/start info for unlimited display. |
| `internal/agent/types.go` | Document `0` as unlimited for the four config fields; keep stop reasons and exit mapping. |
| `internal/agent/runner.go` | No loop rewrite: `checkLimits`, the run deadline, and the model deadline are already guarded by `> 0`. Verify the guards and retain explicit-budget paths. |
| `internal/agent/state.go` | Keep decoding the four fields for compatibility; remove unused enforcement helpers if they become dead. |
| `internal/tui/app.go` | Start/Details fields carry `0`; tests cover unlimited values. |
| `internal/tui/view.go` | Render unlimited turns/tokens without `0/0`. |
| `internal/tui/widgets.go` | Details overlay renders `unlimited`/`none`. |
| `cmd/aharness/*_test.go`, `internal/agent/*_test.go`, `internal/tui/*_test.go` | Defaults, explicit 0, recorded-value ignoring, unlimited runs, explicit budgets, timeout paths, display. |

Implementation order: config and validation first, then TUI display and tests,
then docs. The runner behavior is already correct for `0`; verify rather than
rewrite it.

## 5. Milestones

### M0 — Config defaults and opt-in budgets

- Change the four defaults to `0`; add `-model-timeout` default `0`.
- Allow `0`, reject negative values, and update help text.
- Stop inheriting recorded budgets; apply explicit flags only.
- Skip at-limit session checks when the budget is `0`.
- Unit tests for precedence, recorded-value ignoring, and validation.

Acceptance: a new config has all four values `0`; explicit positive values set
them; an omitted or explicit `0` is unlimited; negative values fail with exit 2.

### M1 — Run behavior, TUI, and replay

- Verify the runner's `>0` guards and limit/stop events under unlimited and
  explicit settings.
- Update TUI status/details for unlimited values.
- Verify old-log resume/fork ignores recorded budgets and records `0` in the new
  `run_start`.
- Integration tests with a fake provider and a scripted model.

Acceptance: a default run completes after more than 30 turns and more than 2M
cumulative tokens; an explicit budget still stops at the configured value; an
old session with recorded limits resumes unbounded; interruption still works.

### M2 — Documentation and gates

- Update README, architecture, limits, testing strategy, and TUI doc.
- Run the coding-standard §8 gates.
- Run the manual smoke.

Acceptance: docs describe unlimited defaults and opt-in budgets; old terminal
states are documented as legacy/explicit-only; gates pass.

## 6. Testing

### Unit

- Config defaults, precedence, explicit `0`/omitted handling, negative
  rejection, recorded-value ignoring, and session-rule checks at `0`.
- Agent loop: unlimited turns/tokens; explicit turn/token limits; session
  timeout `0` and explicit; model timeout `0` and explicit; limit event
  emission only for explicit budgets.
- A fake model exceeds 31 turns and 2M cumulative usage with defaults and
  produces no budget event; explicit low budgets still stop.
- State decoding of old and new payloads; old `max_turns`/`time_limit` terminal
  states and old `limit` kinds.
- TUI view/widget formatting for unlimited (`turns 31`, `tokens 2.1M`) and
  explicit (`N/M`) values.

### Integration

- Fake provider plus fake model with scripted turns and usage.
- Default run exceeds the old 30-turn and 2M-token thresholds and completes.
- Explicit budgets still stop with the existing statuses and exit codes.
- Resume/fork of a seeded old log ignores recorded budgets and records `0`.
- Interruption and context-fit failure still terminate correctly.
- TUI status/details and `-ui-json` frames for unlimited values; old terminal
  states remain renderable.

### Manual smoke

Run against a local fake provider with a script that exceeds 30 turns, and
verify the run completes without a budget event. Resume a seeded session with
old recorded budgets and confirm it continues. Exercise an explicit
`-max-turns`, `-max-total-tokens`, `-session-timeout`, and `-model-timeout` to
confirm the opt-in paths still stop as documented. A real-provider smoke is
optional and should use a short task; unbounded cost is the operator's
responsibility.

## 7. Limits updates

Update [limits.md](limits.md) §1:

| Limit | New behavior |
|---|---|
| Max turns | `0` default = unlimited; explicit positive = cumulative cap. |
| Max total tokens | `0` default = unlimited; explicit positive = cumulative cap. |
| Session wall clock | `0` default = none; explicit positive = per-run deadline. |
| Model call timeout | `0` default = none; explicit positive = per-call deadline. |
| Max input tokens | Unchanged by this plan. |
| Compaction target | Unchanged. |
| Context-overflow retry | Unchanged. |
| Consecutive output-length stops | Unchanged. |
| Stuck detection | Unchanged. |
| HTTP client timeout / transport attempts | Unchanged; network failure handling. |

Update §5 of `limits.md` for the four flags and the new `-model-timeout`
default.

`session_start`/`run_start` remain v2. Old logs are not migrated. Document that
recorded run budgets on old logs are historical and are not re-enforced.

## 8. Risks and assumptions

- **Runaway cost and latency.** No default budget means a model/tool loop can
  run until it completes, errors, or is interrupted. Mitigate with the opt-in
  flags, TUI cancel, batch signals, provider errors, stuck detection, and the
  context window. This is the deliberate trade-off requested.
- **Replay change.** Old sessions resume unbounded even though their logs
  recorded budgets. This is a deliberate departure from strict recorded-limit
  replay; the new `run_start` records the effective policy. An operator who
  wants the old budget must pass the flag.
- **Long-run log growth.** The session log has no rotation or retention; a very
  long run increases disk use. Log rotation remains out of scope.
- **Network hangs.** Removing the model timeout leaves the HTTP client timeout
  (5 minutes) and the transport attempt budget as the outer bound. A provider
  that never responds still fails after those attempts.
- **Tests must terminate.** Remove any test that blocks indefinitely; use
  `testing/synctest` and deterministic fake completions. Do not use real
  `time.Sleep`.
- **UI width.** Unlimited labels are longer than `N/M`; keep the status line
  within the existing frame cap and truncate safely.
- **Explicit-budget compatibility.** The flags and stop reasons remain, so
  operators can restore the previous behavior without a code change. The model
  timeout is newly exposed as a flag with the same semantics as before.
- **Plan overlap.** The output-budget change belongs to
  [model-discovery-build.md](model-discovery-build.md) §3.4. If both plans land
  together, merge the `config.go` defaults/validation changes and keep `0` as
  the unset value for every optional budget.

## 9. Decisions and alternatives

| Decision | Choice | Why |
|---|---|---|
| Default budgets | `0` for turns, total tokens, run clock, model call | No artificial default limit; the operator opts in. |
| Flags | Keep them, default `0`; add `-model-timeout` | Preserves compatibility and gives cost/latency controls without imposing them. |
| Explicit `0` | Same as an omitted flag: unlimited | Recorded budgets are not inherited, so there is no inherited value to clear; `0` is accepted for symmetry and scripting. |
| Old session records | Historical only; not inherited | Otherwise removing the default still requires a new session to escape an old cap. |
| Context management | Keep unchanged | Compaction is required to stay within the model window. |
| Stuck/length/retry policies | Keep unchanged | They terminate or report failure loops; they are not generation budgets. |
| Session schema | No bump; record `0` | Additive-compatible with old logs; no migration. |
| Model timeout | Flag default `0`; HTTP timeout stays | The harness no longer imposes a generation deadline, but network calls still fail predictably. |
| Output budget | Leave to model discovery | Avoid duplicating the §3.4 contract. |

## 10. Documentation updates

- `README.md`: add this plan to the documentation table; update the bounded-loop
  bullets to say budgets are opt-in.
- `docs/architecture.md`: §4.1 loop checks, §4.2 terminal-state meanings, §9 CLI
  flags/defaults/precedence, §10 defaults table.
- `docs/limits.md`: §1 rows and §5 flag table; mark the old values as opt-in and
  keep the context/transport/tool limits unchanged.
- `docs/testing-strategy.md`: unlimited-run, explicit-0, replay, and TUI
  display scenarios.
- `docs/tui.md`: unlimited status/details labels and no `0/0`.
- `docs/limits-removal-build.md`: status becomes implemented with the
  milestone/date and any deviations.
- `docs/model-discovery-build.md`: cross-reference this plan for the run-budget
  boundary if both are revised together.

## 11. Implementation deviations

The plan landed as written except for the following; the contract above remains
the record.

- **Runner guards needed no rewrite.** `internal/agent/runner.go` already
  guarded the turn/token budgets, the run deadline, and the model deadline with
  `> 0`. Implementation only verified those guards and retained the
  explicit-budget paths.
- **Parent-context deadline without `-session-timeout`.** When the run context
  carries a deadline from its parent but no explicit `-session-timeout` is set,
  the run still terminates with `time_limit`; it emits no `limit` event because
  no configured budget was crossed. With an explicit timeout, the existing
  `session_timeout` limit event is emitted.
- **TUI details layout.** The overlay shows the observed `turns: N` and
  `tokens: <human>` under the session block and the configured labels
  `max turns: unlimited`, `total tokens: unlimited`, `run timeout: none`, and
  `model timeout: none` under configuration, rather than mixing observed and
  configured values in one row.
