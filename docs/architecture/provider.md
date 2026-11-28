# 2. Provider and model

Part of the [SDK architecture](../architecture.md) reference; this page is
normative for §2.

aharness supports one provider, `opencode-go`, through two OpenAI wire
dialects: Chat Completions and Responses. The SDK supports the single
provider `opencode-go`; an unknown provider is a configuration error. The usable model set, display names, context/input limits, advertised
output ceilings, status, dialect, base URL, reasoning support, and per-model
reasoning effort levels are discovered from the
[models.dev](https://models.dev) catalog and normalized in `model`.
Deprecated models are selectable and labeled. Models without tool calls, text
output, a positive context window, or a supported SDK are retained in the
catalog with an `Unavailable` reason and omitted from the usable set. A new
session requires a usable model in `ModelSpec`; there is no hardcoded default model, fallback, routing, or provider matrix.

The harness opens network connections only through the dialect adapters, the
catalog source, the configured SearXNG instance, the builtin `web_fetch`
client, and the operator-configured `web_fetch` provider endpoint.
`web_search` contacts only the operator-configured base URL; it never fetches a
model-supplied URL. `web_fetch` contacts the model-supplied URL through the
builtin client, whose dial-time address guard refuses private, loopback,
link-local, metadata, and other special-purpose addresses; with a non-builtin
provider it contacts only the operator endpoint and moves target egress there.
`run_command` may use whatever network the host allows. Provider-specific
request and response shapes stay behind `model.OpenAIChat` and
`model.OpenAIResponses`.
The host supplies the provider token when it constructs the adapter. The token
is optional: when set, the selected adapter sends it as a Bearer token; when
unset or empty, it omits `Authorization`, which supports local no-auth endpoints
and `httptest` servers. An endpoint that requires auth surfaces a missing token
as `provider_error` (401); local no-auth endpoints run without one. The token is
kept in memory, redacted from logs, and excluded from tool environments.
Credential handling stays in the adapters; v1 does not scan unrelated host
configuration or discover credentials implicitly.

## 2.1 Wire format and SDKs

The two dialect adapters use plain HTTP against the resolved catalog base URL.
For `opencode-go`, a discovered base URL must be HTTPS with host `opencode.ai`
and no userinfo, query, or fragment; the documented new-session OpenAI base is
`https://opencode.ai/inference/go/openai/v1`. An explicit
`ModelSpec.BaseURL` wins over discovery and may use any valid HTTP(S) host,
including loopback test servers. Existing sessions
keep their recorded base URL.

- **Chat Completions:** `POST <base>/chat/completions`.
- **Responses:** `POST <base>/responses`.

The dialect comes from the model's AI SDK package (`model.provider.npm`,
falling back to `provider.npm`):

| SDK package | Native protocol | Effective dialect |
|---|---|---|
| `@ai-sdk/openai-compatible` | `chat-completions` | `openai-chat` |
| `@ai-sdk/openai` | `responses` | `openai-responses` |
| `@ai-sdk/anthropic` | `messages` | `openai-chat` (translated) |
| `@ai-sdk/google` | `generate-content` | `openai-chat` (translated) |
| anything else | unknown | unavailable |

Anthropic- and Google-native models use the OpenAI Chat base URL and adapter;
OpenCode documents that every text model also works through the other APIs, so
the harness translates through Chat rather than adding native adapters. Unknown
SDKs fail closed: the model is unavailable and an explicit selection reports
the SDK. The native protocol and SDK are retained on the catalog record for
display and diagnostics. If a model misbehaves through a dialect, fix that
adapter or add its native dialect behind the same canonical model; do not change
the loop. The wire dialect is not the internal schema; both adapters map into
the canonical model (Section 2.2).

Do not use an OpenCode SDK at this boundary. The published Go SDKs
(`opencode-sdk-go` and its community forks) are generated clients for the
OpenCode server REST API; adopting one would move the loop, tools, and session
state into OpenCode, which is the architecture this harness rejects. There is no official
Go SDK for the OpenCode Go inference endpoint: it is OpenAI-compatible HTTP, and
the stdlib adapters are smaller than the mapping layer an SDK would require. An
inference SDK would also own retries, error bodies, and decoding, hiding the
`model_retry` events and redaction the session log depends on. The available Go
clients also fit poorly as dependencies: the Stainless-generated client targets
an older server API, and the community fork that tracks upstream has no license.

Each adapter is constructed with the base URL, provider token, session ID, and
the host's User-Agent identity. It sends `Authorization: Bearer <token>`,
`Content-Type: application/json`, `x-opencode-session: <session-id>`, and the
caller-supplied `User-Agent`. The SDK has no default identity: an empty value is
rejected before the request is built. An empty token omits the
`Authorization` header, supporting local no-auth endpoints and tests. OpenCode
Go requires the session header; the value is the run's UUIDv7 session ID. The
base URL is a constructor argument so tests and local deployments can point the
adapters at another OpenAI-compatible server. The caller resolves the base URL from an explicit `ModelSpec.BaseURL`, then
the catalog entry; the override also supports integration tests and local
endpoints.
Production runs use the catalog URL.

Both adapters send the full projected input on every call and keep no
server-side conversation state. The Responses adapter sets `store: false`,
never uses `previous_response_id`, and asks for
`reasoning.encrypted_content` so a stateless tool round can replay reasoning;
servers that return plaintext reasoning (DeepSeek's Responses API) ignore the
include field and are covered by the same raw-item replay. Continuation is
adapter-owned in both dialects; the canonical model never carries raw provider
shapes.

## 2.2 Canonical model and dialect adapters

The `model` package defines one internal canonical model. Every provider
dialect is an adapter that maps its wire format into these types; the loop sees
only the canonical model. The canonical model covers what the loop can act on:
system and user text, assistant text, display-only thinking text, tool calls and
results correlated by ID, one optional request-side control (the
provider-neutral thinking effort), image parts on a tool result, a normalized
finish reason, token usage, and normalized errors.

Canonical does not mean lossless. Provider-specific material — encrypted
reasoning items, reasoning identifiers, citations, safety metadata, cache
controls, opaque options, and streaming events — stays in the adapter and is
dropped unless the loop or the dialect's continuation needs it. Thinking text is
the one exception: it is mapped into the typed display-only
`model.Response.Reasoning` field so a host can display it, and it is never logged
or projected. A dialect adapter may separately retain the provider's own
continuation material in memory; that is adapter state, not a canonical field.
If a future feature needs another provider field, add a typed canonical field
when the loop consumes it; do not add a raw provider-shaped field. A lossless
union schema for OpenAI Responses, OpenAI Chat, Anthropic, and Gemini would push
provider-specific optional fields and invalid states into the loop; that is the
thing to avoid. OpenAI Chat is a dialect, not a neutral standard, so it is not
the canonical model either.

Each adapter owns the wire format, authentication, tool-schema translation, and
the mapping of finish reasons, usage, and errors. It uses one `http.Client` with
a timeout and `http.NewRequestWithContext`, always closes and caps the response
body at 1 MiB, and never uses `http.DefaultClient`. The client timeout defaults
to 15 minutes and is selectable on the adapter. Provider error bodies are capped and redacted before logging.

| Layer | Owns |
|---|---|
| Canonical `model` types | System/text messages, display-only thinking text, tool calls/results, optional request-side thinking effort, normalized finish reason, usage, normalized errors. |
| `model.OpenAIChat` | Chat Completions wire format, nested function schemas, `tool_calls[].id`, `reasoning_content`/`reasoning` thinking text and tool-round continuation, prompt/completion usage. |
| `model.OpenAIResponses` | Responses wire format, flat function tools, `function_call.call_id`, reasoning summaries and raw reasoning-item continuation, input/output usage, `store: false` full replay. |
| Shared `model` HTTP helper | Request execution, auth/session headers, 1 MiB body cap, typed error decoding. |

Image input:

- `model.ImagePart` carries `media_type`, the decoded bytes, and the delivered
  dimensions, and `model.Message.Images` holds at most one entry per tool
  result. Only `image/png` and `image/jpeg` cross this boundary; the built-in
  tool normalizes anything else. An adapter that is handed another media type
  returns an error instead of forwarding a mislabeled payload.
- A dialect that accepts image parts inside a tool result maps them in place. A
  dialect that does not maps the tool result to its text form and emits one
  adjacent `user` message carrying the caption and the image. Both built-in
  adapters take the second path because image parts inside a tool message are
  accepted by some gateways and silently dropped by others.
- The image bytes are base64 in a `data:` URL, so one image is about 1.37 times
  its declared size on the wire. The 512 KiB delivered cap bounds that.

Image capability follows the model in use, not the construction call:

- `Config.Model.Spec.ImageInput` sets the gate for a session's first run.
  `RunOptions.Model` (facade) or `engine.RunSpec.ImageInput` (component) replaces
  it for that run, because the registry is built per run. Switching to a model
  whose capability is unknown clears the gate rather than keeping a stale
  answer, so the provider decides.
- A delegated child resolves its own model. `agent.ModelResolution.ImageInput`
  carries that model's capability, and a child on a different model follows it;
  a child that keeps the parent's model keeps the parent's gate.
- Neither path records the capability in the log. It is a property of the model
  a host selects at run time, like the base URL and limits that `run_start`
  already records per run, and a session resumed with no explicit model leaves
  the gate unknown.

Normalization rules:

- The canonical system prompt is the first message. Adapters place it in the
  dialect's system slot (`system` message, top-level `system`, or
  `systemInstruction`).
- Every canonical tool call has a non-empty ID and raw JSON arguments. If a
  dialect omits call IDs (Gemini function calls may), the adapter synthesizes a
  deterministic ID for that response and uses it for the matching result. For a
  dialect that supplies IDs, a missing or duplicate wire ID is `protocol_error`;
  the adapter does not repair it. The Responses adapter uses
  `function_call.call_id` for correlation, not the output item `id`.
- Canonical finish reasons are `stop`, `length`, `tool_calls`,
  `content_filter`, and `other`; adapters map dialect-specific stop reasons.
- Canonical usage has input and output totals plus three additive subsets:
  cached input, cache-write input, and reasoning output. Cached and cache-write
  tokens are disjoint subsets of input; reasoning tokens are a subset of
  output. `Total()` remains input+output and never adds a subset. `Reported`
  distinguishes a provider-supplied usage object (including an all-zero
  object) from an absent one. Adapters reject a usage object whose subsets
  exceed their totals as `protocol_error`. The Chat adapter prefers
  `prompt_tokens_details.cached_tokens` and falls back to
  `prompt_cache_hit_tokens`; both dialects map cache-write and reasoning
  details when present. Old v2 logs decode with `reported=false` and zero
  subsets. Every aggregate — run, cumulative, subagent, reconstructed state,
  and recorded delegate details — adds all fields, so the subsets survive the
  fold; an aggregate is `reported` when any folded response reported usage.
- Canonical errors are typed (`HTTPError`, `TransportError`, `ProtocolError`,
  and context overflow); adapters map HTTP statuses and provider error bodies.
- Both adapters replay the canonical projection; the Responses adapter sets
  `store: false` and never uses `previous_response_id`. Each adapter also keeps
  a bounded, adapter-owned copy of its native reasoning continuation material
  and replays that material with the matching assistant output; it is not a
  canonical message and is not projected from the log.

**Thinking effort.** The public name is *thinking effort*; the provider and
catalog call the same control *reasoning effort*. `model.Request` carries one
optional provider-neutral field:

```go
type Request struct {
	Model           string
	Messages        []Message
	Tools           []ToolSchema
	MaxOutputTokens int
	ThinkingEffort  string // empty means the provider/model default
}
```

| Canonical | Chat Completions | Responses | Empty |
|---|---|---|---|
| `ThinkingEffort` | top-level `reasoning_effort` | `reasoning: {"effort": "<value>", "summary": "auto"}` | Field omitted in both dialects. |

- Both adapters construct buffered and streaming requests with the same
  `payload` function, so the mapping cannot diverge between paths. Tool-call
  rounds reuse the run configuration, so every model call in a run carries the
  same value.
- The value is sent unchanged; adapters do not rename it between providers,
  do not validate catalog membership, and never synthesize a value from a
  toggle or token-budget control. Resolution and membership checks happen in
  `cmd` before the run (Section 9).
- The catalog supplies the capability: only `reasoning_options` entries whose
  type is `effort` contribute values. Values are trimmed, validated against
  `[a-z][a-z0-9_-]*` and at most 32 bytes, the reserved clear sentinel
  `default` is dropped, duplicates are removed, the list is ordered `none`,
  `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, then unknown values
  lexicographically, and it is capped at 16 values. `reasoning` is retained
  separately so the host can distinguish a model that supports reasoning
  without an effort control from one that does not reason at all.
- Thinking text is display-only. `model.Response.Reasoning` carries the
  provider's thinking: Chat reads `reasoning_content` (DeepSeek-compatible) and
  falls back to `reasoning`; Responses concatenates `reasoning` item summary
  and content text and ignores encrypted content for display. The Responses
  adapter always pairs an explicit effort with `"summary": "auto"`: without it
  the endpoint returns encrypted reasoning only (empty `summary`, reasoning
  tokens still billed), so no thinking text can be displayed in either the
  buffered terminal item or the `response.reasoning_summary_text.delta` /
  `response.reasoning_text.delta` stream. It is capped at
  64 KiB, never counted against the canonical response cap, and never written
  to the log or projected. Usage already counts reasoning tokens as output
  tokens, so no accounting change is needed. Both adapters additionally retain
  the provider's own continuation material in a bounded in-memory cache (4 MiB
  per response, 16 MiB per adapter) and replay it before the matching assistant
  output when the loop sends a tool result. Chat replays thinking text as
  `reasoning_content`; Responses replays the raw `reasoning` output items,
  including encrypted content or plaintext `reasoning_text`. A Responses
  summary without encrypted content or plaintext reasoning is display-only and
  is not replayed. The cache is keyed by the canonical assistant message, is
  never logged, lives only as long as the adapter (one run), and is not a
  reason to add a canonical reasoning message shape.
- A provider rejection of a value the catalog advertised (for example 400 or
  422) is a normal typed `provider_error`. The harness does not retry without
  the field or fall back to the provider default.

Provider responses are decoded with `encoding/json/v2` defaults: duplicate
object names and invalid UTF-8 are rejected and map to `protocol_error`; unknown
members are ignored for forward compatibility. Tool arguments are stricter:
typed decoding uses `RejectUnknownMembers(true)` (Section 5). Do not set
`RejectUnknownMembers(true)` for provider responses.

v1 has no opaque provider options or per-adapter capability knobs. Catalog
reasoning support and effort values are discovery metadata resolved by the host,
not adapter options. Add adapter-specific knobs only when a dialect requires
them, and keep them out of the loop.

v1 ships `model.OpenAIChat` and `model.OpenAIResponses` over the `opencode-go`
base URL. Add `anthropic` or `gemini` adapters when a real provider requires
them; each maps into the same canonical model without changing the loop. Share
HTTP, retry, and fixture plumbing; do not share a universal request/response
schema beyond the canonical model.

## 2.3 Streaming

Each adapter optionally implements:

```go
type StreamHandler struct {
	Text      func(string) error // assistant text
	Reasoning func(string) error // thinking text
}

type StreamCompleter interface {
	CompleteStream(ctx context.Context, req Request, stream StreamHandler) (Response, error)
}
```

`CompleteStream` is used when streaming is requested. Batch enables it through
`Start`; `Run` is buffered, and an explicit request not to stream
there is a config error. The agent keeps `Completer` and falls back to `Complete`
when the adapter does not implement streaming. A nil callback discards that
kind. Both callbacks are display-only; the returned canonical `Response` is
authoritative and is the only transcript record. Tool calls are assembled inside
the adapter and are never exposed as deltas.

- Chat Completions sends `stream: true` and
  `stream_options: {"include_usage": true}`. It assembles text, thinking text
  (`reasoning_content` then `reasoning`), tool-call fragments by index, finish
  reason, and usage from the chunks.
- Streaming requests use the same adapter `payload` builder as buffered
  requests, so `max_output_tokens` and `thinking_effort` mapping is identical
  on both paths; the only additions are `stream` and the dialect's stream
  options.
- Responses consumes `response.output_text.delta`,
  `response.reasoning_summary_text.delta`, `response.reasoning_text.delta`,
  `response.function_call_arguments.delta`, and the terminal
  `response.completed`/`response.incomplete`/`response.failed` events. The
  completed response is authoritative; its reasoning item, when present,
  replaces the accumulated streamed thinking.
- A 2xx response whose content type is not `text/event-stream` is decoded as a
  normal JSON response, so local OpenAI-compatible servers keep working.
- Final usage is required. A completed stream without usage is a
  `protocol_error`; the harness does not silently undercount tokens.
- Retries happen only before the first display output. After the first text or
  thinking delta, any failure is terminal and is wrapped in `PartialError`
  carrying capped partial text and the emitted byte count; it is never retried,
  because a retry would duplicate the display and could duplicate tool calls.
- Caps: 4 MiB per SSE event, 1 MiB for the assembled canonical response, 8 MiB
  for the total stream, and 64 KiB for partial text on an interrupt or failed
  stream. The event cap is larger than the response cap because a terminal
  `response.completed` event wraps the full response in JSON. A response over
  1 MiB is a `protocol_error` before any tool executes. Invalid JSON, duplicate
  object names, and invalid UTF-8 are `protocol_error`. Error bodies are capped,
  redacted, and never logged whole.
- The durable transcript keeps the executed response: the `model_response`
  payload is capped at 1 MiB total canonical content, and the session line cap
  is 4 MiB so JSON escaping and envelope fields fit. A tool result's text stays
  at 64 KiB; an image is a separate channel with its own 512 KiB encoded cap,
  and its base64 plus envelope stays well inside one session line. This avoids a
  mismatch where a large response is executed but its logged fields are
  truncated, which would break resume.
- A failed stream after deltas appends one `error` event with the partial byte
  count and capped partial text; no `model_response` is written and the partial
  text is not projected. An interactive interrupt appends `interrupt` instead.
- No per-delta events are written. The log size and projection behavior stay
  proportional to complete responses.

With `Start`, text and thinking deltas are emitted as they arrive; a response's
final text is not re-emitted if any delta for it was emitted. If the adapter
falls back to non-streaming, the response text is emitted once. With `Run`, the
final assistant text is returned in `Result.FinalText`.

## 2.4 Continuation for a new dialect

Reasoning continuation is an adapter obligation, not a loop or log feature.
When adding a dialect adapter that serves reasoning models, or enabling a
native provider that already has one, the adapter MUST:

- Retain the provider's exact continuation material — thinking text, raw
  reasoning items, signed thinking blocks, or whatever opaque state the
  provider requires — never the display summary and never a reconstructed
  string.
- Request the dialect-specific switch that exposes replayable material:
  Responses sets `include: ["reasoning.encrypted_content"]` for `store: false`;
  Anthropic enables thinking and receives signed `thinking` /
  `redacted_thinking` blocks; Gemini returns `thought` parts with
  `thoughtSignature`. Chat needs no request-side switch.
- Key the material with `continuationKey` (assistant content or tool-call IDs)
  and replay it in provider order, immediately before the assistant output it
  explains. Never append it at the end of the input: DeepSeek's Responses API
  rejects input that ends with an assistant or reasoning item, and OpenAI
  expects the item to precede the corresponding output.
- Bound the cache (4 MiB per response, 16 MiB per adapter), never log it or add
  it to `model.Message`, and drop it with the adapter (one run). A new run or a
  resumed process starts empty.
- Add a two-round test for both buffered and streaming paths. The second wire
  request must contain the native material unchanged, in order, and before the
  matching tool call; the first request must carry the dialect's
  `include`/thinking switch. Chat and Responses have template tests in
  `model/*_continuation_test.go`. Add a real-provider smoke before
  claiming support.
- Do not add a raw provider field to the canonical model or session log. If a
  provider eventually requires continuation across runs, that is a separate
  durable-reasoning decision, not an adapter cache extension.

Native Anthropic and Gemini adapters are not shipped; those models are
currently translated through the Chat adapter, and the gateway owns the
signature round trip. If a native adapter replaces that path, the checklist
above applies to its SDK types, not to `reasoning_content`.
