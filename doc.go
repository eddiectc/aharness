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
// MessageRecorded. A delegated child is reported on the run's own stream as
// SubagentStarted, SubagentProgress, SubagentFinished, and
// SubagentMessageRecorded; those carry counters and names only. SubagentProgress
// is throttled to at most one event per second per child, so its usage and cost
// are cumulative-so-far, not final. ResponseCompleted's cumulative fields work
// the same way: they include the completed response but a later turn may
// change them.
//
// DelegationConfig.ObserveChild opts into a second, per-child stream. It is
// called once per child that actually starts a run and returns a sink that
// receives that child's own display events (text, thinking, tool call and
// result, notices, and its own run start and finish) in that child's order,
// each stamped with the child session. Ordering is promised per child only: the
// host merges children into one view. Distinct child sinks, and a child sink
// versus the run's own stream, may run concurrently, so a sink must be safe for
// concurrent use and must not block. Registering an observer enables streaming
// for delegated children, which makes a post-delta transport failure terminal
// for that child instead of retried; an unobserved child keeps the counter-only
// stream and the full retry budget. A sink error or panic terminates that child
// as internal_error and leaves its siblings and the run untouched.
//
// Transcript reads the durable display transcript of any session under a state
// directory, root, child, or fork, without taking the session lock, so a
// running session stays readable; Agent.Transcript is the same read for this
// agent's own session. A delegated child's transcript is read by its session ID
// from ChildTarget.Session or SubagentStarted.ChildSession.
//
// The display callback is never invoked after the run returns. RunFinished is
// the terminal event and is emitted once; if the final append or sync fails,
// the event iterator closes with the result instead. RunOptions.OnEvent follows
// the same ordering for the blocking Run. ViewEvent projects any event to a
// flat JSON shape with a stable kind discriminator.
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
// read_image, edit_file, and search. The 16 KiB child-report cap is intentional: a child
// that must return more writes a host-routed report file and returns its path.
// See the architecture's large-child-report pattern.
//
// # Replacing the system prompt
//
// Config.Identity names the host product in the parent identity line and in
// each delegated child's line, for example "You are acode, a coding harness
// working in the workspace ...". Empty means "aharness". It is trimmed, limited
// to 64 bytes, must contain no control characters, and is not recorded, so it is
// re-supplied on every construction, including Resume and Fork; a fork's new
// children use the identity supplied to Fork.
//
// Prompt composition has three tiers. The recommended tier sets only
// Config.Identity and inherits the default body, agent.DefaultPromptBody: the
// loop invariants, the coding and coordination policy, and the catalog of the
// host's own agent definitions. A host whose product is not a coding agent can
// compose a body from agent.LoopRules, its own policy lines, and
// agent.AgentCatalogText, which renders the catalog under the SDK cap and
// omission marker. Config.SystemPrompt replaces the prompt wholesale;
// Config.Instructions composes host text into the effective prompt (the
// recorded text on a fork, or the rendered prompt when the log predates it).
//
// The SDK does not discover workspace files: a host that wants AGENTS.md, or an
// equivalent, in its prompts reads the file and passes the bytes to
// Config.Instructions. The parent records the text once as
// session_start.appended_prompt, and every fresh child composes it into its own
// prompt after the role body, including the children of a resumed or forked
// session; a continued child keeps it through its recorded prompt. A delegated
// child records neither appended_prompt nor prompt_tag. The recorded value is
// provenance, never a replay key, and is not necessarily a suffix of the
// prompt once truncation applies. The component agent layer never appends:
// agent.Config.AppendedPrompt only records text the caller already composed.
//
// Config.PromptTag records the host's own prompt attribution beside the SDK
// template version, for example "acode/prompt-v1". It is at most 64 bytes of
// printable ASCII matching [A-Za-z0-9._:/-]+, is never a replay key, and does
// not affect rendering or caps. Resume keeps the recorded tag; Fork may set a
// new one, and a fork that changes the prompt inherits none.
//
// LoopRules returns the invariants the loop depends on: a tool call is the only
// way to act on the workspace; every tool call receives exactly one result;
// commands run with the harness's privileges; tool errors are data; and a reply
// without a tool call ends the run. A replacement prompt that does not use
// DefaultPromptBody must keep them. The package does not parse or enforce a
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
// See docs/architecture.md and its area pages for the loop, tool, projection,
// session, and public facade contracts.
package aharness
