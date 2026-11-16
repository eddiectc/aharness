// Package aharness is the embeddable form of the aharness coding agent. It is
// a headless, in-process library over the existing model/tool loop and durable
// JSONL session log; it is not a second harness, a subprocess wrapper, or a
// general agent development kit.
//
// # Lifecycle
//
// New creates a session, Resume opens one for append, and Fork copies one into
// a new self-contained log. Each returns an *Agent that owns one workspace, one
// session store, and one model configuration. Agent.Run is the blocking
// convenience; Agent.Start returns a *Run whose Events iterator streams
// display-only events with backpressure. Interrupt cancels the in-flight model
// or tool operation and continues the run; Cancel terminates it; Steer and
// SteerChild enqueue messages at turn boundaries; Close is idempotent.
//
// The Result returned by a run is authoritative. Result.Status is the typed
// stop reason, Result.Err is its diagnostic cause, and Agent.Run returns that
// same cause so errors.Is and errors.As work. Events are observation only: the
// session log is the only durable truth.
//
// # Tools and policy
//
// Built-in tools can be disabled with ToolsConfig.Disable and caller tools are
// registered with ToolsConfig.Extra. Every tool execution, including delegated
// children, passes through Config.Authorize first. A denial becomes a
// model-visible permission_denied result; it is policy, not a sandbox. The
// library adds no OS isolation, approval UI, or network boundary. Caller tools
// run with the host process's privileges; run_command inherits the host
// environment through the existing allowlist.
//
// # Replacing the system prompt
//
// Config.SystemPrompt replaces the harness prompt entirely; Config.Instructions
// appends to it. A replacement must keep the invariants the loop depends on: a
// tool call is the only way to act on the workspace; every tool call receives
// exactly one result; the model stops by replying without a tool call; and tool
// errors are data to read and adapt to. The package does not parse or enforce a
// caller prompt.
//
// # Parallelism
//
// Tool calls within one model response execute sequentially in arrival order.
// That order is the durable log order, replay order, authorization order, and
// stuck-detection order. Two parallel patterns are supported: a batch custom
// tool that fans out internally and returns one ordered ToolResult, and
// delegated children whose grants are read-only or declare
// Tool.ConcurrencySafe. ConcurrencySafe never permits concurrent calls within
// one run. Delegation depth is one.
//
// Resume inherits the recorded prompt and tool contract and rejects changes.
// Fork is the supported contract-change path: it applies explicit prompt or
// tool overrides, records contract_override, and copies history under the new
// contract without rewriting the parent log.
//
// See docs/architecture.md for the loop, tool, projection, session, and public
// facade contracts.
package aharness
