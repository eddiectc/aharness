# aharness configuration

- **Date:** 2026-11-13
- **Status:** current for v1.
- **Related:** [architecture](architecture.md), [limits](limits.md),
  [coding standard](coding-standard.md), [documentation map](README.md).

aharness takes all runtime configuration from three layers plus built-in
defaults. There is no project-local config, no profile system, and no plugin
configuration.

## 1. Precedence

Highest first:

1. **Explicit flag**, for example `-model`.
2. **Environment variable**, for example `AHARNESS_MODEL`.
3. **Config file key**, for example `"model"` in `<state-dir>/config.json`.
4. **Built-in default**, for example "no model; required for a new session".

Resolution is per setting, not per layer: a config file can supply the model
while the environment supplies the workspace and a flag supplies the timeouts.
"Explicit" means supplied by any of layers 1–3. That matters where an explicit
value changes behavior beyond the value itself — a context window is clamped to
the model ceiling, and an output budget clears an inherited one — so a
file-supplied value behaves exactly like a flag-supplied one.

## 2. Config file

| Item | Value |
|---|---|
| Flag | `-config <path>` (an empty value disables the file) |
| Environment | `AHARNESS_CONFIG` |
| Default | `<state-dir>/config.json` |
| Format | one JSON object; unknown keys are rejected |
| Max size | 1 MiB |

An explicit `-config` or `AHARNESS_CONFIG` path must exist: a typo is a config
error (exit 2) rather than a silent fallback to defaults. The implicit
`<state-dir>/config.json` is optional; a missing one is not an error. The state
directory is resolved from `-state-dir`/`AHARNESS_STATE_DIR`/`~/.aharness`
before the file is read, and a key in the file cannot move its own location.

```json
{
  "model": "deepseek-v4.1-flash",
  "provider_base_url": "https://opencode.ai/inference/go/v1",
  "searxng_url": "http://127.0.0.1:8080",
  "max_turns": 40,
  "max_subagents": 8,
  "http_timeout": "10m",
  "thinking_effort": "high"
}
```

Durations are strings using Go duration syntax (`90s`, `10m`, `2h`); a bare
number is rejected so a unit typo cannot silently mean nanoseconds.

### 2.1 Keys

| Key | Type | Maps to | Notes |
|---|---|---|---|
| `model` | string | `-model` / `AHARNESS_MODEL` | Required for a new session. |
| `provider` | string | `-provider` / `AHARNESS_PROVIDER` | Only `opencode-go`. |
| `workspace` | string | `-workspace` / `AHARNESS_WORKSPACE` | Inherited on resume/fork unless explicit. |
| `provider_base_url` | string | `-provider-base-url` / `AHARNESS_PROVIDER_BASE_URL` | Validated like the flag. |
| `models_dev_url` | string | `AHARNESS_MODELS_DEV_URL` | No flag; HTTPS except loopback HTTP. |
| `searxng_url` | string | `-searxng-url` / `AHARNESS_SEARXNG_URL` | `web_search` base URL; empty disables search. Absolute http(s); http only for loopback hosts; no userinfo, query, or fragment; a path prefix is allowed; trailing slashes are trimmed; at most 8 KiB. |
| `web_timeout` | duration | `-web-timeout` / `AHARNESS_WEB_TIMEOUT` | One `web_fetch` request, including connect, redirects, and body reads. Must be positive and at most 120s; default 30s. Independent of `http_timeout`. |
| `web_fetch_provider` | string | `-web-fetch-provider` / `AHARNESS_WEB_FETCH_PROVIDER` | `builtin` (default) or `crawl4ai`; selected once at startup with no fallback. |
| `web_fetch_url` | string | `-web-fetch-url` / `AHARNESS_WEB_FETCH_URL` | Provider endpoint; empty for builtin and required for crawl4ai. Absolute http(s), no userinfo or fragment, at most 8 KiB; loopback and private hosts are allowed. |
| `web_fetch_key` | string | `-web-fetch-key` / `AHARNESS_WEB_FETCH_KEY` | Optional provider bearer token; sent only to the provider and never logged. Prefer the environment. |
| `thinking_effort` | string | `-thinking-effort` / `AHARNESS_THINKING_EFFORT` | `default` clears. |
| `max_turns` | integer | `-max-turns` / `AHARNESS_MAX_TURNS` | 0 is unlimited; negative is invalid. |
| `max_total_tokens` | integer | `-max-total-tokens` / `AHARNESS_MAX_TOTAL_TOKENS` | 0 is unlimited; negative is invalid. Child (delegated) tokens count against it. |
| `max_subagents` | integer | `-max-subagents` / `AHARNESS_MAX_SUBAGENTS` | Fresh delegated children per run; default 8, 0 is unlimited, negative is invalid. |
| `max_context_tokens` | integer | `-max-context-tokens` / `AHARNESS_MAX_CONTEXT_TOKENS` | Must be positive; clamped to the model ceiling. |
| `max_output_tokens` | integer | `-max-output-tokens` / `AHARNESS_MAX_OUTPUT_TOKENS` | 0 omits the request field. |
| `session_timeout` | duration | `-session-timeout` / `AHARNESS_SESSION_TIMEOUT` | 0 means no deadline. |
| `model_timeout` | duration | `-model-timeout` / `AHARNESS_MODEL_TIMEOUT` | 0 means no harness deadline. |
| `http_timeout` | duration | `-http-timeout` / `AHARNESS_HTTP_TIMEOUT` | Must be positive; default 15m. |
| `catalog_timeout` | duration | `-catalog-timeout` / `AHARNESS_CATALOG_TIMEOUT` | Must be positive; default 15s. |
| `require_usage` | boolean | `-require-usage` / `AHARNESS_REQUIRE_USAGE` | Per-run; not inherited. |
| `log_requests` | boolean | `-log-requests` / `AHARNESS_LOG_REQUESTS` | Records request debug events. |

Deliberately **not** configurable in the file:

| Setting | Why |
|---|---|
| Provider token (`AHARNESS_PROVIDER_TOKEN`) | A secret does not belong on disk in this harness; keep it in the environment or a secret manager. |
| State directory (`-state-dir` / `AHARNESS_STATE_DIR`) | It locates the file itself, so it cannot be self-referential. |
| Task, `-task-file`, `-resume`, `-fork` | Per-invocation inputs; a sticky task or session would be a footgun. |
| Agent definitions | They are files under `<state-dir>/agents/`, not config keys: a definition carries a tool grant and a prompt, and a settings surface must not widen authority without editing a file. |
| `-batch`, `-ui-json`, `-ui-size`, `-stream`, `-version`, `-list-models`, `-refresh-models` | Front-end and terminal-action selection is per invocation. |
| `AHARNESS_IMAGE_PROTOCOL` | A terminal capability, not harness configuration. |

Every key is validated even when a higher layer wins, so a broken file is
reported instead of hiding behind a flag.

The agent directory is derived, not configured. Definitions live in
`<state-dir>/agents/*.md`; there is no `-agents-dir` flag, environment
variable, or config key. The state directory is already the harness-owned,
operator-scoped location, and a workspace-level agent directory would be
repository-controlled prompt material with no trust model. Moving the state
directory moves the definitions with it. `-max-subagents` is the one new
delegation setting because a fan-out guard depends on the operator's patience
and provider plan, while the definitions themselves are content.

## 3. Environment variables

| Variable | Maps to |
|---|---|
| `AHARNESS_CONFIG` | `-config` |
| `AHARNESS_MODEL` | `-model` |
| `AHARNESS_PROVIDER` | `-provider` |
| `AHARNESS_WORKSPACE` | `-workspace` |
| `AHARNESS_STATE_DIR` | `-state-dir` |
| `AHARNESS_PROVIDER_TOKEN` | No flag; in memory only. |
| `AHARNESS_PROVIDER_BASE_URL` | `-provider-base-url` |
| `AHARNESS_MODELS_DEV_URL` | No flag. |
| `AHARNESS_SEARXNG_URL` | `-searxng-url` |
| `AHARNESS_WEB_TIMEOUT` | `-web-timeout` |
| `AHARNESS_WEB_FETCH_PROVIDER` | `-web-fetch-provider` |
| `AHARNESS_WEB_FETCH_URL` | `-web-fetch-url` |
| `AHARNESS_WEB_FETCH_KEY` | `-web-fetch-key` |
| `AHARNESS_THINKING_EFFORT` | `-thinking-effort` |
| `AHARNESS_MAX_TURNS` | `-max-turns` |
| `AHARNESS_MAX_TOTAL_TOKENS` | `-max-total-tokens` |
| `AHARNESS_MAX_SUBAGENTS` | `-max-subagents` |
| `AHARNESS_MAX_CONTEXT_TOKENS` | `-max-context-tokens` |
| `AHARNESS_MAX_OUTPUT_TOKENS` | `-max-output-tokens` |
| `AHARNESS_SESSION_TIMEOUT` | `-session-timeout` |
| `AHARNESS_MODEL_TIMEOUT` | `-model-timeout` |
| `AHARNESS_HTTP_TIMEOUT` | `-http-timeout` |
| `AHARNESS_CATALOG_TIMEOUT` | `-catalog-timeout` |
| `AHARNESS_REQUIRE_USAGE` | `-require-usage` |
| `AHARNESS_LOG_REQUESTS` | `-log-requests` |

An **empty** variable means unset, so the config file below it applies. A
malformed value in any layer is a config error naming the variable; it never
falls through to the next layer.

## 4. Which limits are worth configuring

[limits.md](limits.md) is the single source for values. Most entries are safety
ceilings that should stay fixed, but these are operationally worth a flag,
environment variable, or config key because a reasonable value depends on the
deployment, the provider plan, or the task:

| Limit | Why it is worth configuring | Layer |
|---|---|---|
| **HTTP client timeout** (15m) | The single most useful knob. A provider that stalls holds a run until the outer bound; a smaller value fails fast, a larger one tolerates long generations. | flag, env, config |
| **Model call timeout** (none) | A per-attempt harness deadline distinct from the transport bound; useful when a stalled turn should retry. | flag, env, config |
| **Session wall clock** (none) | Caps an unattended run regardless of how many turns or tokens it uses. | flag, env, config |
| **Max turns / max total tokens** (unlimited) | Spend guard for CI or an automated sweep. | flag, env, config |
| **Max context / max output tokens** (catalog / unset) | Lower the context for a small model or pin an output budget for reproducibility. | flag, env, config |
| **Thinking effort** (unset) | A per-model choice an operator reuses across runs. | flag, env, config |
| **Catalog request timeout** (15s) | Mirrors and offline/air-gapped setups need a different bound. | flag, env, config |
| **Catalog URL** (`models.dev`) | Test and mirror override. | env, config |
| **SearXNG base URL** (unset) | The self-hosted search backend differs per operator; unset disables `web_search`. The URL is validated, and only loopback instances may use HTTP. | flag, env, config |
| **Web request timeout** (30s) | A web fetch is not a model generation; the 15-minute provider timeout is wrong for it. The 120s ceiling keeps a single tool call bounded. | flag, env, config |
| **Web fetch provider** (`builtin`) | A JavaScript-heavy page needs an operator-run renderer. `builtin` keeps everything in-process; `crawl4ai` delegates rendering and moves the target egress boundary to the provider. | flag, env, config |
| **Web provider endpoint/key** (unset) | The Crawl4AI host and token differ per deployment; the endpoint is required only for that provider, and the key belongs in the environment where possible. | flag, env, config |
| **Model / provider / base URL / workspace** | The everyday selectors. | flag, env, config |

Intentionally **not** configurable. Each is a container-format or protocol
invariant; changing one changes what the harness means, not how it is deployed:

- Provider response body 1 MiB, error body 4 KiB, SSE event 4 MiB, total stream
  8 MiB, assembled response 1 MiB. A larger response is a protocol error, and
  the parser, not a knob, owns that boundary.
- Tool result caps (`read_file` 60 KiB / 2,000 lines, `search` 60 KiB / 200
  results, `run_command` 64 KiB) and the `run_command` 120s default with its
  600s ceiling. These shape the model-visible transcript; an operator-tunable
  transcript cap would change replay semantics.
- `web_search` response 2 MiB, results default 5 / max 10, output 60 KiB,
  snippet 1,200 characters, and the fixed 15-second search timeout. One
  SearXNG request per tool call; no retry, pagination loop, or caching.
- `web_fetch` body 2 MiB, header 64 KiB, 5 redirects, 8 KiB URL, 60 KiB text
  page, and the 16 MiB Crawl4AI response cap. These are transcript and safety
  boundaries; `web_timeout` is the configurable part.
- Session line 4 MiB, session schema version, cache version 3, file modes
  0600/0700, and the retention policy (none).
- Transport attempt budget (5), retry backoff, `Retry-After` cap, and the
  context-overflow retry count.
- TUI display caps, `-ui-json` frame caps and protocol version.

The distinction is deliberate: a value is configurable when two competent
operators could reasonably choose differently for the same binary. A value that
defines the wire format, the replay contract, or a display-only safety cap is
not.

## 5. Inspecting the effective configuration

The batch startup log line records the resolved values, including
`http_timeout_ms`, `search_configured`, the fixed `search_timeout_ms`, and
`config` (the resolved path, or `none` when disabled). It deliberately does not
log the SearXNG URL. The TUI details overlay shows the run timeouts;
`aharness -h` lists the environment variables and the precedence order.
