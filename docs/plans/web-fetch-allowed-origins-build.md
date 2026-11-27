# Web fetch allowed origins

- **Date:** 2026-11-27
- **Status:** implemented; unit-tested, no live-provider or real-Crawl4AI run
- **Scope:** `tools/web_fetch.go`, `tools/web_origin.go`,
  `tools/web_fetch_provider.go`, `tools/result.go`, `tool.go`,
  `docs/architecture.md`, and `docs/limits.md`
- **Related:** [web-fetch-build.md](web-fetch-build.md) §3.4 and its non-goals;
  aharness #17 and #18; acode #17

## 1. Why

The builtin `web_fetch` refused every destination that is not globally routable
unicast, with no production setting to change it. That was deliberate: the URL
is model-supplied and the model reads untrusted content, so the fetcher must not
be the tool that probes the internal network. But it left no safe way to reach a
local development server, which is a routine target for a coding agent, and the
only nearby switch — the provider's `CRAWL4AI_ALLOW_INTERNAL_URLS` — is
server-wide and cannot be enforced per request.

Two facts bounded the design:

1. `run_command` already reaches anything the OS allows, so the guard was never
   an isolation boundary; it exists so the harness's own fetch identity does not
   acquire internal reach.
2. Delegated read-only roles (`explore`, `reviewer`, `researcher` in the
   first-party host) do not carry `run_command`. For them `web_fetch` is the
   entire egress surface, so removing the guard would widen authority the parent
   deliberately withheld.

## 2. Decision

A mechanism split, matching the SDK's existing `os.Root`-versus-workspace
pattern:

- **The SDK owns two address sets and the per-hop check.** Metadata,
  link-local, unspecified, multicast, documentation, benchmarking, reserved,
  and protocol-assignment addresses stay refused unconditionally, including
  IPv4-mapped and NAT64-wrapped forms. Loopback, private, and shared address
  space is refused by default and permitted only for an exact allowed origin.
- **The host owns the values.** `WebFetchConfig.AllowedOrigins` (facade) and
  `tools.Options.WebAllowedOrigins` (component) carry exact
  `scheme://host:port` entries. The SDK never reads a config file or
  environment, and neither model input nor fetched content can add an entry.
- **The builtin fetcher is the only enforcer.** The Crawl4AI provider sends one
  URL and lets Chromium fetch subresources itself, so an SDK-side list would
  bind only the top-level request; a non-empty list with that provider is a
  configuration error rather than a silent no-op. `CRAWL4AI_ALLOW_INTERNAL_URLS`
  stays `false`.

Enforcement points:

- `Fetch` checks the target's origin and resolved addresses before the first
  request, and re-checks every followed redirect hop.
- The dialer repeats the check on the freshly resolved address, and the request
  context carries the origin decision, so a rebinding resolver cannot swap in a
  destination the origin does not cover.
- A refusal for a merely configurable address names the allowlist in the
  model-visible error, so an operator reading the transcript sees the fix. The
  model cannot act on it, and metadata refusals never carry that hint.

## 3. Contract deltas

| Surface | Delta |
|---|---|
| `aharness.WebFetchConfig` | Added `AllowedOrigins []string`. A path, query, fragment, userinfo, control character, non-http(s) scheme, or more than 16 entries is a `ConfigError`; duplicates collapse. |
| `tools.Options` | Added `WebAllowedOrigins []string`; the test-only `WebAllowLoopback` seam is unchanged and still never wired in production. |
| `tools` helpers | Added `ParseWebAllowedOrigins` and `ValidateWebFetchAllowedOrigins`. |
| `blocked_address` message | A blocked loopback/private address now says it can be permitted by the host's allowed origins. The code is unchanged. |
| Address sets | `blockedWebPrefixes` became `alwaysBlockedWebPrefixes` and `configurableBlockedWebPrefixes`; `isBlockedWebAddr` keeps its meaning (blocked without an allowed origin), and `isAlwaysBlockedWebAddr` / `isConfigurableBlockedWebAddr` expose the split internally. |
| Session schema | No change. Web fetch options are host configuration and were never recorded; a resume with different values behaves as it does today. |

## 4. Alternatives rejected

- **A global "allow private" boolean.** One flip would expose every intranet
  host and every local service, not the one dev server the operator meant.
- **Documenting the provider's `CRAWL4AI_ALLOW_INTERNAL_URLS=true`.** It is
  server-wide, the SDK cannot see which target it admitted, and it contradicts
  the builtin guard for no benefit; loopback inside the provider container is
  also not the operator's host loopback.
- **SDK-side file discovery of allowed origins.** The SDK does not read host
  configuration; the host wires the list.
- **A builtin renderer or screenshot tool.** Out of the SDK's recorded tool
  boundary: an observation tool needs a browser or rendering service, a
  dependency choice, approval, and a host-owned scratch location, so it belongs
  to the host through `ToolsConfig.Extra`.

## 5. Verification

Unit tests, `make dod` green:

- `TestParseWebAllowedOrigins`: normalization, default ports, IPv6, duplicates,
  and every rejected shape.
- `TestValidateWebFetchAllowedOrigins`: builtin accepted, crawl4ai rejected,
  empty list allowed, invalid entries and unknown providers rejected.
- `TestWebAddressClassification`: the two sets, IPv4-mapped and NAT64 forms,
  and the unchanged default verdict.
- `TestWebAllowedOriginPermitsLoopback`: a loopback dev server is fetched
  through the real guard with no test seam.
- `TestWebAllowedOriginRefusesWithoutMatch`: no list, a different port, a
  different host spelling, and a different scheme all return `blocked_address`
  with the allowlist hint.
- `TestWebAllowedOriginDoesNotUnblockMetadata`: `169.254.169.254` is refused
  even when its origin is listed, and the error carries no allowlist hint.
- `TestConfigValidation`: the facade rejects a crawl4ai allowlist and an
  invalid origin.

Not verified: no live Crawl4AI run (the provider rejects the list before any
request, so there is nothing to exercise there), and no real public-page fetch.

## 6. Supersession

This plan supersedes the following `web-fetch-build.md` statements for the
builtin fetcher: the "Private/intranet fetch, a user-facing
`-web-allow-private` escape hatch" non-goal, "No production flag bypasses the
guard" (§3.4), and "the only loopback allowance is the internal test-only
option". The earlier rationale — the guard is an accident/SSRF boundary, not a
sandbox — still holds, and the provider must still keep
`CRAWL4AI_ALLOW_INTERNAL_URLS=false`.
