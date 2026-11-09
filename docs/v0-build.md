# aharness v0 build plan

- **Date:** 2026-11-09
- **Source:** [Bare-Minimum Agent Harness](agent-harness-minimum.md)
- **Target:** Go 1.27.3, module `aharness` (see [`go.mod`](../go.mod))
- **Status:** greenfield implementation plan
- **Architecture:** [System architecture and contracts](architecture.md)
- **Testing strategy:** [v0 testing strategy](testing-strategy.md)
- **Definition of done:** [v0 definition of done](definition-of-done.md)
- **Coding standard:** [Go coding standard](coding-standard.md)

v0 is a single-process, single-agent coding harness. It accepts one task, runs a
bounded model/tool loop against a workspace, appends every event to a JSONL
session log, and exits with a final answer or a typed stop reason.

The source document defines the feature floor. System structure and contracts
are in [`architecture.md`](architecture.md). This document defines the v0 slice,
milestones, acceptance tests, decisions, and risks. If this plan and the
architecture document conflict on scope, this plan wins; on structure or runtime
contracts, the architecture document wins.

## 1. Scope

### 1.1 What v0 owns

| Area | v0 implementation |
|---|---|
| Provider | One `opencode-go` provider with two dialect adapters, OpenAI Chat Completions and Responses, behind one canonical model. |
| Loop | Bounded turn loop with explicit terminal states; one assistant turn at a time. |
| Tools | `read_file`, `edit_file`, `search`, `run_command` behind one validated registry. |
| Transcript | Append-only JSONL event log; the model input is a projection rebuilt each turn. |
| Context | Per-call projection, tool-result truncation, deterministic compaction, token estimates. |
| Failure handling | Model retries, tool timeouts, panic pairing and typed stop, error results, repeated-failure stop. |
| Budgets | Max turns, max total tokens, session wall-clock deadline. |
| CLI | One binary with flags and environment configuration; no daemon or UI. |

### 1.2 Isolation and approvals

aharness does not provide isolation or approval; if isolation is needed, launch
it inside bwrap, Docker, or a VM.

### 1.3 Not in v0

No streaming, interactive interrupt/steering, resume/fork, model-based
summarization, subagents, RAG/embeddings, MCP/plugin systems, hooks, custom UI,
graph DSLs, planner frameworks, fine-tuning, built-in evals or an eval runner,
price tracking or dollar-cost budgets, multi-provider support (one provider with
two dialects is in v0), approval workflows, snapshot/rollback, or sandbox
construction.

Deliberate deviations from the source's sketch: isolation and approval are the
harness user's responsibility, not a harness feature; built-in evals are not
part of v0; and dollar-cost tracking is deferred.

### 1.4 Definition of done

v0 is complete when every item in the [v0 definition of
done](definition-of-done.md) is satisfied.

## 2. Milestones

| Milestone | Deliverables | Exit criteria |
|---|---|---|
| **M0 — Foundation** | Five-package layout; config parsing in `cmd`; workspace policy in `tools`; session event types plus JSONL store using `encoding/json/v2` and `jsontext`; UUIDv7 session IDs; fake model; minimal single-turn runner path for the fake completer; CLI wiring; `-version`; signal handling; `.golangci.yml` with the standard's targeted linters. | `goimports -l .`, `go vet ./...`, `golangci-lint run`, `go test -race -count=1 ./...`, `mkdir -p bin && go build -trimpath -o bin/aharness ./cmd/aharness`, and `govulncheck ./...` pass. A scripted run injects a fake completer, writes a valid session log, and exits 0 without network access. |
| **M1 — Loop and tools** | Runner with the tool loop and all terminal states; concrete tool registry and four tools; argument validation; `os.Root` containment plus symlink policy; result codes; pairing; sequential multi-call handling; repeated-failure stop based on codes; panic pairing then `internal_error`. | A scripted model reads, edits, searches, runs a command, and finishes. Unknown/invalid/timeout/panic cases each produce exactly one error result; a panic also stops with `internal_error`. Path traversal, absolute-path conversion, and symlink escapes are rejected; `.git` components are blocked. |
| **M2 — Provider and context** | Two concrete dialect adapters — OpenAI Chat Completions and OpenAI Responses — over the shared `opencode-go` base URL; both initial models; required `x-opencode-session` and `User-Agent` headers; retry/backoff; token usage accounting; `agent.Project`; truncation; deterministic compaction; provider error mapping; strict duplicate-name/UTF-8 decoding with unknown response members ignored. | Mock-provider tests cover both dialects: 429→success, 401 fast-fail, malformed/duplicate-key/invalid-UTF-8 JSON, and context overflow→compaction. Both model IDs complete a tool-call round trip through their assigned adapter. A real model for each dialect completes a sample task with an explicit confirmed `-max-context-tokens`. A forced large history compacts below the limit and preserves pairing. |
| **M3 — Hardening** | Process-group `Cancel`/`WaitDelay`; explicit environment allowlist with harness-owned `HOME`/`TMPDIR`; per-session work-directory cleanup on normal, error, and signal paths; UTF-8 sanitization; session durability; signal cleanup; secret sentinel tests; startup config logging; manual smoke procedure. | Kill/timeout tests pass. SIGTERM leaves no child process. Provider token is absent from child environments and logs. The log lands in the configured state directory with modes 0700/0600. A run that reaches a harness terminal path removes its per-session `<state>/work/<session-id>` subtree, unless cleanup fails and reports the leftover path; cleanup is tested on normal, error, and signal paths. A smoke run with each model completes the sample task and produces a usable trace. |

Every milestone is gated by the coding standard's §8 checks; do not defer lint,
vet, race, or vulnerability checks to M3. Run `go fix ./...` before review and
use `go doc -ex` for examples; do not run `go fix` as a CI check.

Do not pull Tier 2 features forward to unblock a milestone. If a milestone
cannot pass, fix the interface instead of adding features.

## 3. Testing strategy

The v0 testing strategy is in [testing-strategy.md](testing-strategy.md). It
covers unit, integration, and manual smoke checks.

## 4. Decisions and risks

### 4.1 Decisions

| # | Decision | Rationale | Reversal risk |
|---|---|---|---|
| D1 | Go standard library only at runtime, including Go 1.27 `os.Root`, `uuid`, and `encoding/json/v2`; a test-only `go-cmp` exception is allowed by the coding standard. | Small, auditable, no dependency supply chain. | Low; concrete types and package boundaries isolate most code. |
| D2 | One provider (`opencode-go`) with two dialect adapters — OpenAI Chat Completions and OpenAI Responses — and two initial models, each mapped to its dialect. | Each test model is exposed through its OpenAI dialect; sharing one base URL and canonical model avoids a provider matrix while proving the adapter boundary. | Medium; prompts/tool-call mapping are adapter-specific, but the loop sees only the canonical model. |
| D3 | Tools execute sequentially; no provider-side parallel guarantee. | Simpler correctness and deterministic logs; avoids concurrent writes. | Low; dispatcher can fan out later. |
| D4 | Deterministic compaction only, no summarization model call. | Predictable, cheap, testable; satisfies Tier 1. | Low; summarization can replace a policy function later. |
| D5 | No built-in isolation or approval. | The harness owns the loop, not the boundary; external isolation supplies it when needed. | High if built-in isolation is later required; it changes the process and tool contract. |
| D6 | Session log defaults to the XDG state directory. | Keeps the log out of the workspace diff without requiring an external mount. | Low; `-state-dir` overrides it. |
| D7 | No resume/fork in v0; event log is resume-ready. | Tier 2; smaller loop. | Low; the log format already records enough state. |
| D8 | No built-in evals or eval runner. | The harness ships the loop, not an evaluation platform. | Low; external tooling can consume the session log. |
| D9 | No dollar-cost tracking in v0. | Model-specific price tables go stale and should not be a user-supplied environment contract; token limits are enough. | Low; add a price table and budget policy later if needed. |
| D10 | Five-package layout; config in `cmd`, projection in `agent`, workspace policy in `tools`. | Fewer packages and interfaces; safety logic stays in focused files with tests. | Low; split when a second consumer appears. |
| D11 | A recovered tool panic is paired with a result, then the run stops with `internal_error`. | A panic is a bug; continuing risks corrupted state. | Low; can change to a counted continuation if evidence supports it. |
| D12 | No OpenCode SDK; stdlib adapters speak OpenAI Chat Completions and OpenAI Responses for the initial models. | The available Go SDKs target the OpenCode server API, not the inference endpoint; adopting one would move the loop, tools, and session state into OpenCode. The inference APIs are OpenAI-compatible HTTP, and owning retries, decoding, and redaction keeps them visible in the event log. | Low; if aharness becomes an OpenCode client, use the server SDK there. Revisit if OpenCode publishes a supported Go inference SDK. |
| D13 | One internal canonical model; each dialect is an adapter that maps into it. v0 ships Chat Completions and Responses adapters; future Anthropic and Gemini adapters add mapping code, not loop branches. | Keeps the loop provider-neutral without turning the canonical model into a lossless union of provider features. Provider-specific fields stay adapter-side unless the loop consumes them. | Low for added dialects; medium if the canonical model must later become lossless and provider-shaped. |
| D14 | Per-session `<state>/work/<session-id>` holds the child `HOME` and `TMPDIR` and is removed on termination; cleanup is best-effort. | Keeps scratch state out of the user's home and workspace and prevents unbounded state growth; the session log remains the durable record. Host `/tmp` is rejected because it may be `noexec`, size-limited, or cleaned mid-session; the state dir is configurable. | Low; a debug keep flag or startup sweep can be added later. |

### 4.2 Risks

| Risk | Mitigation |
|---|---|
| Running against an untrusted repository without external isolation exposes the host. | Operator responsibility; launch inside external isolation. The harness makes no isolation claim. |
| Provider token is readable by a child through the process environment or `/proc`. | Env scrubbing is hygiene, not isolation; use external isolation when that matters. Never log the token. |
| The workspace is a real repository, so in-place mistakes are real. | Use version control or a disposable copy; the harness does not create worktrees. |
| `os.Root` is mistaken for isolation. | It is an accident guard; it does not block bind mounts, `/proc`, or device files. Use external isolation. |
| JSON v2 strictness rejects duplicate object names or invalid UTF-8 from providers or tools. | Sanitize command output; map malformed provider JSON to `protocol_error`; do not fall back to v1 silently. |
| Log tampering by a same-UID shell. | Accepted v0 limitation; separate UID/VM if audit integrity is required. |
| Context estimator drift causes provider overflow. | Conservative estimate, reserved output, provider-usage reconciliation, one compaction retry. |
| Huge tool output or huge edits exhaust memory or context. | Caps at capture, projection, and log-field level; explicit truncation markers. |
| Search performance on large repositories. | Go walk with skip rules and result cap; shell out to `rg` only if measurements justify it. |
| Provider schema, tool-call format, or dialect translation drift. | Per-dialect adapter tests with recorded fixtures and tool-call round trips; typed errors; keep each dialect behind its adapter. |
| Responses reasoning items may be required to continue a tool-call loop when `store:false`. | Confirm in M2; if required, carry a bounded adapter-owned continuation alongside the canonical projection and persist it in the log, or revisit `previous_response_id`; do not silently drop reasoning. |
| Work-directory cleanup fails or a surviving child keeps writing into it. | Best-effort removal after log close; report the leftover path on stderr and leave it for inspection. A hard-killed process can still leave a work dir; a startup sweep is deferred. |
| Child process outlives the harness. | Process groups, `Cancel`, and `WaitDelay` on timeout/signal; a deliberately backgrounded grandchild on normal exit MAY survive, so use external isolation for hard containment. |
| Model context window is unknown. | Acceptance runs MUST set `-max-context-tokens` explicitly; the 200,000 default logs a warning until confirmed. |

### 4.3 Open prerequisites

1. **Provider integration:** confirm the model-to-dialect mapping, the
   Responses path on the hosted endpoint, that each dialect preserves tool
   calls for its assigned model, and whether the endpoint requires auth and the
   exact credential source behind `AHARNESS_PROVIDER_TOKEN`; local no-auth
   endpoints may omit the token ([architecture.md §2.1](architecture.md)). The
   dialects and headers are fixed there; the base URL is a constructor
   argument. Do not add a provider SDK.
2. **Model limits:** confirm the context window for `crouton-alpha-free` and
   `deepseek-v4.1-flash`. Until confirmed, acceptance runs MUST set
   `-max-context-tokens` explicitly; the 200,000 default is provisional.
3. **State directory:** confirm the XDG default is writable on the target
   systems, or document the required `-state-dir`. An unwritable state directory
   is a startup config error (exit 2).
4. **Integration-test transport:** the testing strategy runs the built binary
   against a fake `opencode-go` server, but the CLI keeps the hosted base URL
   and each adapter's base URL is only a constructor argument. Settle this
   before M0 freezes the CLI/config surface: either integration tests invoke
   `cmd/aharness`'s `run` in-process, or the CLI exposes an explicit base-URL
   override used by tests and local endpoints. Do not intercept the hosted URL
   with a proxy or hosts-file override.
5. **Responses continuation:** confirm whether the Responses model requires
   reasoning items or encrypted content to be replayed across tool-call rounds
   when `store:false`. If it does, choose a bounded adapter-owned continuation
   carried alongside the canonical projection and persisted in the session log,
   or server-side chaining with `previous_response_id`; do not drop reasoning
   silently.

### 4.4 Assumptions expensive to reverse

The decisions above name the chosen design; D5 (no built-in isolation) is
already the highest-cost reversal. This table names the remaining assumptions
the plan inherits without deciding, where a late correction would cross a
package, the persisted log, the CLI, or the tool/prompt contract. Provider
dialect, auth, and model IDs are covered by §4.3; two rows repeat §4.3 items
because their failure mode would change the log or test surface. The most
expensive reversals are contracts that outlive the binary — the session log,
the CLI surface, and the prompt/tool material — so confirm each before the
milestone that freezes it.

| Assumption | If it is false | Confirm by |
|---|---|---|
| v0 targets Unix-like hosts; `/bin/sh`, process groups, `SIGINT`/`SIGTERM`, and `//go:build unix` command handling are sufficient. | `run_command`, signal handling, and parts of path/search behavior need per-OS implementations; Windows remains unclaimed. | M0/M1, before the CLI contract freezes. |
| Built-binary integration tests can redirect the adapters to a fake server while normal runs use the hosted URL. | The testing strategy's "build the binary and run it against a fake server" check cannot run; the test must move in-process or the CLI needs a test/local base-URL override. | M0 (§4.3 item 4). |
| The Responses model accepts full input replay with `store:false` and does not require reasoning-item or encrypted-content continuation for tool rounds. | The adapter needs a continuation mechanism or server-side response chaining; the canonical request/response and session log contract change. | M2, with a real Responses call (§4.3 item 5). |
| The CLI surface — flag and environment names, exit codes, the stdout/stderr split, and the state-directory/session-file layout — is a stable public contract from the first release. | Scripts, CI, and operational tooling break; renames need aliases and a migration period, and changed exit codes fail silently in automation. | M0, before the CLI/config surface freezes. |
| The session envelope and tool result codes evolve additively within `v=1`: readers ignore unknown event types, fields, and codes, and old logs stay decodable. | Resume/fork or external consumers need a schema migration or `v` bump, and old traces cannot be replayed faithfully. | M0 for the envelope and reader tolerance; M1 for result codes. |
| Session durability ends at turn boundaries: `Sync` at every turn and on exit, with a torn final line tolerated on read; a crash may lose events appended since the last `Sync` but never invalidates earlier lines. | Per-event fsync, checkpoints, or a write-ahead scheme becomes necessary; the store contract, write latency, and recovery rules change. | M0. |
| Recorded prompt and toolset versions stay compiled into the binary, so a session log can be re-projected with the prompt and schemas it recorded. | Logs must embed the full prompt and schemas, or exact replay/resume is dropped or migrated; every prompt change adds permanent replay code. | M1/M2, before `prompt-v0` and `toolset-v0` are frozen. |
| The four-tool set, its schemas, descriptions, and ACI behavior — including create-via-`edit_file`, no dedicated write/delete/move tool, and text-only completion with no `finish` tool — are versioned prompt material; changing them after fixtures exist invalidates behavioral baselines. | Toolset and prompt versions change; adapter fixtures and recorded traces must be regenerated, and prompt-quality comparisons lose their baseline. | M1/M2, before fixtures are recorded. |
| The projection constants (`ceil(bytes/4)`, 8 tokens per message, 16 per tool schema, four verbatim turns, oldest-result-first elision) are conservative for both models and their real tokenizers. | Compaction either elides useful context too early or still overflows; the one-shot provider-overflow retry may not save the run. | M2 with provider-reported usage, then the smoke run. |
| `os.Root` plus same-directory temp-file/rename semantics cover every file-tool operation on the target filesystem. | Edit/write/search need OS-specific fallbacks or stronger containment checks, and the containment tests change. | M1. |
| Secret handling is scoped to the provider token and transport headers; the harness does not detect arbitrary secrets in tool arguments, command output, or file contents before logging them. | A general secret-scrubbing contract changes the log's redaction policy, and already-written logs cannot be recalled. | M0 for the policy; M3 for the sentinel tests. |

## 5. Follow-on order

After v0 is reliable: streaming and interrupt/steering; resume/fork from the
event log; hooks; model-based compaction; then subagents, RAG, or MCP/plugins only
with evidence that the single loop is the bottleneck. External evaluation and
benchmarking are separate from the harness. Further provider dialects are
adapters over the canonical model; they add mapping code and fixtures, not loop
branches.
