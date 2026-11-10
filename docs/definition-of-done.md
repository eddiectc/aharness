# aharness v0 definition of done

- **Date:** 2026-11-09
- **Status:** normative completion checklist for v0
- **Related:** [system architecture](architecture.md), [Go coding standard](coding-standard.md)

v0 is complete when every item below holds.

## 1. Completion checklist

- [x] **End-to-end task.** A task runs end-to-end against a real provider
      model, makes changes only under the workspace, and returns a final answer.
      The `deepseek-v4.1-flash` Chat Completions smoke completed on 2026-11-09;
      the `muse-spark-1.3-contributor` Responses smoke completed on 2026-11-10.
- [x] **Tool protocol.** Every well-formed model tool call receives exactly one
      tool result before the next model call; unknown tools, invalid arguments,
      timeouts, and panics become error results. A recovered panic is paired and
      then stops the run with `internal_error`.
- [x] **Session log.** The session log is valid JSONL encoded with
      `encoding/json/v2` and `jsontext`, contains the complete event history for
      the run (payloads capped), and is written outside the workspace by
      default.
- [x] **Runtime cleanup.** A terminated run removes `<state>/work/<session-id>`;
      cleanup is attempted on normal, error, and signal paths, and a failure
      leaves the directory with its path reported.
- [x] **Limits.** Max turns, total tokens, and wall-clock limits stop the run
      with a typed status and a final summary event.
- [x] **Compaction.** A long synthetic history compacts below the context limit
      while preserving the task, the last turns, and tool-call/tool-result
      pairing.
- [x] **Models and credentials.** Each implemented dialect completes the smoke
      task through at least one assigned model; every accepted model completes
      a mock tool-call round trip through its assigned dialect; and provider
      credentials never appear in logs or child environments.
- [x] **Testing strategy.** The unit, integration, and manual smoke checks in
      the [testing strategy](testing-strategy.md) pass.
- [x] **Tooling gates.** The [coding standard](coding-standard.md) §8 gates
      pass: `goimports`, `go vet`, `golangci-lint`,
      `go test -race -count=1`, `go build -trimpath`, and `govulncheck`.

Provider note: OpenCode documents `crouton-alpha-free` as a free-tier model
restricted to OpenCode clients; other clients receive `403` even with an API
key. The Responses smoke therefore uses `muse-spark-1.3-contributor`, which
requires a Go subscription and the workspace privacy setting that allows paid
endpoints to train on request data. Both implemented dialects are verified; the
restricted free model remains registered but is not the smoke model.
