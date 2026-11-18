// Package aharness is a headless Go SDK for a single-workspace coding agent.
// It runs the model/tool loop over a durable JSONL session log; it is not a
// subprocess wrapper or a general agent development kit. The first-party
// coding agent, acode, is a separate module built on this SDK.
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
// # Events
//
// Display events are observation only. Root activity is reported as
// TextDelta, ReasoningDelta, ToolStarted, ToolCompleted, Notice, and
// MessageRecorded. A delegated child is reported only as SubagentStarted,
// SubagentProgress, SubagentFinished, and SubagentMessageRecorded; its text,
// arguments, and tool output stay in the child transcript. SubagentProgress is
// throttled to at most one event per second per child, so its usage and cost
// are cumulative-so-far, not final. ResponseCompleted's cumulative fields work
// the same way: they include the completed response but a later turn may
// change them.
//
// The display callback is never invoked after the run returns. RunFinished is
// the terminal event and is emitted once; if the final append or sync fails,
// the event iterator closes with the result instead. RunOptions.OnEvent follows
// the same ordering for the blocking Run.
//
// Transport retries happen only before the first assistant-text delta.
// ModelStarted.Attempt greater than one therefore marks a retry with no
// partial text to reconcile. Once partial text has been displayed, a failure is
// terminal and the partial text is retained rather than reset.
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
// Tool.Run and Config.Authorize receive the calling session and delegated agent
// name on ToolCall, and Authorize also receives a normalized Path for read_file,
// edit_file, and search. The 16 KiB child-report cap is intentional: a child
// that must return more writes a host-routed report file and returns its path.
// See the architecture's large-child-report pattern.
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
