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
| Provider | One `opencode-go` adapter behind provider-neutral types. |
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
price tracking or dollar-cost budgets, multi-provider support, approval
workflows, snapshot/rollback, or sandbox construction.

Deliberate deviations from the source's sketch: isolation and approval are the
harness user's responsibility, not a harness feature; built-in evals are not
part of v0; and dollar-cost tracking is deferred.

### 1.4 Definition of done

v0 is complete when every item in the [v0 definition of
done](definition-of-done.md) is satisfied.

## 2. Milestones

| Milestone | Deliverables | Exit criteria |
|---|---|---|
| **M0 — Foundation** | Five-package layout; config parsing in `cmd`; workspace policy in `tools`; session event types plus JSONL store using `encoding/json/v2` and `jsontext`; UUIDv7 session IDs; fake model; CLI wiring; `-version`; signal handling; `.golangci.yml` with the standard's targeted linters. | `goimports -l .`, `go vet ./...`, `golangci-lint run`, `go test -race -count=1 ./...`, `mkdir -p bin && go build -trimpath -o bin/aharness ./cmd/aharness`, and `govulncheck ./...` pass. A scripted run injects a fake completer, writes a valid session log, and exits 0 without network access. |
| **M1 — Loop and tools** | Runner with terminal states; concrete tool registry and four tools; argument validation; `os.Root` containment plus symlink policy; result codes; pairing; sequential multi-call handling; repeated-failure stop based on codes; panic pairing then `internal_error`. | A scripted model reads, edits, searches, runs a command, and finishes. Unknown/invalid/timeout/panic cases each produce exactly one error result; a panic also stops with `internal_error`. Path traversal, absolute-path conversion, and symlink escapes are rejected; `.git` components are blocked. |
| **M2 — Provider and context** | Concrete `opencode-go` adapter over OpenAI Chat Completions; both initial models; required `x-opencode-session` and `User-Agent` headers; retry/backoff; token usage accounting; `agent.Project`; truncation; deterministic compaction; provider error mapping; strict duplicate-name/UTF-8 decoding with unknown response members ignored. | Mock-provider tests cover 429→success, 401 fast-fail, malformed/duplicate-key/invalid-UTF-8 JSON, and context overflow→compaction. Both model IDs complete a tool-call round trip through the adapter. A real `opencode-go` model completes a sample task with an explicit confirmed `-max-context-tokens`. A forced large history compacts below the limit and preserves pairing. |
| **M3 — Hardening** | Process-group `Cancel`/`WaitDelay`; explicit environment allowlist with harness-owned `HOME`/`TMPDIR`; UTF-8 sanitization; session durability; signal cleanup; secret sentinel tests; startup config logging; manual smoke procedure. | Kill/timeout tests pass. SIGTERM leaves no child process. Provider token is absent from child environments and logs. The log lands in the configured state directory with modes 0700/0600. A smoke run with each model completes the sample task and produces a usable trace. |

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
| D2 | One `opencode-go` adapter over OpenAI Chat Completions with two initial models. | Matches the required provider; one wire dialect covers both models; avoids a premature provider matrix. | Medium; provider behavior is behind `model.OpenCode`, but prompts/tool-call mapping are adapter-specific. |
| D3 | Tools execute sequentially; no provider-side parallel guarantee. | Simpler correctness and deterministic logs; avoids concurrent writes. | Low; dispatcher can fan out later. |
| D4 | Deterministic compaction only, no summarization model call. | Predictable, cheap, testable; satisfies Tier 1. | Low; summarization can replace a policy function later. |
| D5 | No built-in isolation or approval. | The harness owns the loop, not the boundary; external isolation supplies it when needed. | High if built-in isolation is later required; it changes the process and tool contract. |
| D6 | Session log defaults to the XDG state directory. | Keeps the log out of the workspace diff without requiring an external mount. | Low; `-state-dir` overrides it. |
| D7 | No resume/fork in v0; event log is resume-ready. | Tier 2; smaller loop. | Low; the log format already records enough state. |
| D8 | No built-in evals or eval runner. | The harness ships the loop, not an evaluation platform. | Low; external tooling can consume the session log. |
| D9 | No dollar-cost tracking in v0. | Model-specific price tables go stale and should not be a user-supplied environment contract; token limits are enough. | Low; add a price table and budget policy later if needed. |
| D10 | Five-package layout; config in `cmd`, projection in `agent`, workspace policy in `tools`. | Fewer packages and interfaces; safety logic stays in focused files with tests. | Low; split when a second consumer appears. |
| D11 | A recovered tool panic is paired with a result, then the run stops with `internal_error`. | A panic is a bug; continuing risks corrupted state. | Low; can change to a counted continuation if evidence supports it. |
| D12 | No OpenCode SDK; a stdlib adapter speaks OpenAI Chat Completions for both initial models. | The available Go SDKs target the OpenCode server API, not the inference endpoint; adopting one would move the loop, tools, and session state into OpenCode. The inference API is OpenAI-compatible HTTP, and owning retries, decoding, and redaction keeps them visible in the event log. | Low; if aharness becomes an OpenCode client, use the server SDK there. Revisit if OpenCode publishes a supported Go inference SDK. |

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
| Provider schema, tool-call format, or Chat Completions translation drift. | Adapter tests with recorded fixtures and per-model tool-call round trips; typed errors; fall back to a model's native endpoint behind the same adapter. |
| Child process outlives the harness. | Process groups, `Cancel`, and `WaitDelay` on timeout/signal; a deliberately backgrounded grandchild on normal exit MAY survive, so use external isolation for hard containment. |
| Model context window is unknown. | Acceptance runs MUST set `-max-context-tokens` explicitly; the 200,000 default logs a warning until confirmed. |

### 4.3 Open prerequisites

1. **Provider integration:** confirm that Chat Completions translation preserves
   tool calls for both initial models, and confirm whether the hosted endpoint
   requires auth and the exact credential source behind
   `AHARNESS_PROVIDER_TOKEN`; local no-auth endpoints may omit the token
   ([architecture.md §2.1](architecture.md)). The dialect and headers are fixed
   there; the base URL is a constructor argument. Do not add a provider SDK.
2. **Model limits:** confirm the context window for `crouton-alpha-free` and
   `deepseek-v4.1-flash`. Until confirmed, acceptance runs MUST set
   `-max-context-tokens` explicitly; the 200,000 default is provisional.
3. **State directory:** confirm the XDG default is writable on the target
   systems, or document the required `-state-dir`. An unwritable state directory
   is a startup config error (exit 2).

## 5. Follow-on order

After v0 is reliable: streaming and interrupt/steering; resume/fork from the
event log; hooks; model-based compaction; then subagents, RAG, or MCP/plugins only
with evidence that the single loop is the bottleneck. External evaluation and
benchmarking are separate from the harness.
