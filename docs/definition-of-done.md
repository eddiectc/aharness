# aharness v0 definition of done

- **Date:** 2026-11-09
- **Status:** normative completion checklist for v0
- **Related:** [system architecture](architecture.md), [Go coding standard](coding-standard.md)

v0 is complete when every item below holds.

## 1. Completion checklist

- [ ] **End-to-end task.** A task runs end-to-end against a real provider
      model, makes changes only under the workspace, and returns a final answer.
- [ ] **Tool protocol.** Every well-formed model tool call receives exactly one
      tool result before the next model call; unknown tools, invalid arguments,
      timeouts, and panics become error results. A recovered panic is paired and
      then stops the run with `internal_error`.
- [ ] **Session log.** The session log is valid JSONL encoded with
      `encoding/json/v2` and `jsontext`, contains the complete event history for
      the run (payloads capped), and is written outside the workspace by
      default.
- [ ] **Limits.** Max turns, total tokens, and wall-clock limits stop the run
      with a typed status and a final summary event.
- [ ] **Compaction.** A long synthetic history compacts below the context limit
      while preserving the task, the last turns, and tool-call/tool-result
      pairing.
- [ ] **Models and credentials.** Both initial models complete the smoke task,
      and provider credentials never appear in logs or child environments.
- [ ] **Testing strategy.** The unit, integration, and manual smoke checks in
      the [testing strategy](testing-strategy.md) pass.
- [ ] **Tooling gates.** The [coding standard](coding-standard.md) §8 gates
      pass: `goimports`, `go vet`, `golangci-lint`,
      `go test -race -count=1`, `go build -trimpath`, and `govulncheck`.
