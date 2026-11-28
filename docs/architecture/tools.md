# 5. Tools

Part of the [SDK architecture](../architecture.md) reference; this page is
normative for §5. The built-in set is the same one the first-party
[acode](https://github.com/eddiectc/acode) agent uses.

## 5.1 Where a tool belongs

Adding a tool is a product decision with a versioned cost, so the question is
not "do more than one host want it?" but "does the SDK have to know about it to
keep its own guarantees?". A request usually belongs in exactly one of three
homes:

| Home | What belongs there | Test |
|---|---|---|
| Builtin (versioned toolset) | A tool whose result is part of the loop contract | Would the SDK have to change projection, result pairing, stop policy, model capability handling, or replay to support it? |
| Mechanism (SDK API, host-supplied values, deny by default) | A policy the SDK must enforce uniformly | Would every host otherwise reimplement it, and would one get it wrong? |
| Host tool (`ToolsConfig.Extra`) | A tool that needs an environment, a service, a dependency, approval, or product knowledge | Does it depend on how one host is deployed, or on domain semantics the loop does not have? |

A builtin is expensive: its description and schema are prompt material in every
session, it is attack surface in every deployment, and adding one changes the
recorded toolset, so it needs a new frozen version beside the existing replay
sets and a changelog entry. Two consumers asking for the same tool is a signal
to look, never a reason to ship: duplicate a private wrapper or share it outside
the SDK instead.

The common shape is a split: the mechanism lives in the SDK with a strict
default, and the host supplies the values. Workspace containment (`os.Root`)
versus the host's chosen workspace directory is the existing example, and the
`web_fetch` origin allowlist (`WebFetchConfig.AllowedOrigins`) is the second:
the SDK owns the two address sets and the per-hop check, the host owns which
concrete origins are named, so a host can permit a local development server
without a global "allow private" switch. The SDK never reads the host's
configuration file or environment for those values, and content an agent fetches
can never widen them.

Concrete consequences:

- Rendering, screenshots, browser control, accessibility trees, and page
  geometry are host tools. They need a browser or a rendering service, a
  dependency policy, an approval surface, and a host-owned scratch or
  configuration location; `run_command` plus `read_image` already carry the
  render-and-read path, and a custom tool returns the text measurements or the
  image itself.
- Domain tools (a quote lookup, a portfolio query, a report writer) are host
  tools. Their declarations are recorded per session and can appear in a
  delegated child's grant, so shipping them needs no SDK change.
- A new builtin is justified only when the SDK's own contract changes with it,
  for example a result channel that the projection, model capability handling,
  and recording must understand.

## 5.2 Shared rules and builtin contracts

All tools share these rules:

- Canonicalize the workspace to an absolute path once at startup, then open
  `os.OpenRoot(workspace)` and reuse it. Normalize tool paths relative to the
  workspace root; convert absolute inputs with `filepath.Rel`, then require
  `filepath.IsLocal`.
- `os.Root` enforces containment: it rejects `..`, absolute names, and symlink
  escapes. It rejects absolute symlinks even if they point inside. It is an
  accident guard, not a sandbox; bind mounts, `/proc`, and device files remain
  reachable.
- Writes to any path with a `.git` component are rejected by the edit tool,
  including `sub/.git/config`. This is an accident guard, not a security
  boundary.
- Symlink policy: `read_file` MAY follow in-root symlinks; `search` does not
  descend into symlinked directories and requires a real directory root;
  `edit_file` rejects a symlink anywhere in the path.
- File tools restrict reach to the workspace. This is not isolation; the shell
  still runs with the harness's privileges.
- `run_command` executes with the harness's privileges. It does not create a
  sandbox; use external isolation when commands are untrusted.
- Do not rely on path deny-lists for secrets. The real boundary is running
  without credentials and with external isolation.
- Invalid arguments, missing files, ambiguous edits, timeouts, and non-zero
  command exits are model-visible results, not loop exceptions. Every result
  carries a stable `code`: `ok` on success, and `invalid_args`, `unknown_tool`,
  `not_found`, `ambiguous`, `outside_workspace`, `git_protected`, `binary_file`,
  `too_large`, `timeout`, `canceled`, `panic`, `spawn_failed`, `unavailable`,
  `http_error`, `fetch_failed`, `subagent_limit`, `permission_denied`,
  `tool_error`, or `internal` on failure. A
  `subagent_limit` result means at least one delegated child stopped at its own
  budget, so its report is partial; the per-task `status` says which
  (`max_turns`, `token_limit`, or `time_limit`). A non-zero command
  exit is `ok=true` with an `exit_code`; `ok=false` is reserved for dispatch,
  validation, timeout, cancellation, panic, spawn failures, network failures,
  and file restrictions such as binary or too large.
- Every current tool schema declares a shared `intent` string argument: one
  short sentence naming why the call is needed. It is display-only. A host
  uses it as the tool block title and falls back to the raw argument preview
  when it is absent. Tools decode it for strict argument agreement but never
  act on it, and it is recorded only as part of the model response's tool-call
  arguments. A missing intent is never an execution error, so replayed
  `toolset-v0`/`toolset-v1` calls and non-compliant models still run.
- Tool results are capped before entering the transcript. File-content results
  are head-only and contiguous: `read_file` never keeps a head and tail that
  hide middle lines, and `search` keeps a head window. Command output keeps head
  and tail with an explicit `[truncated N bytes]` marker; oversized command
  output is also spilled to a harness-owned file. The full raw output is not
  sent to the model. Command output is sanitized to valid UTF-8 before
  encoding.
- Tool calls are never automatically retried. Side effects are not idempotent;
  the model decides whether to retry after seeing the error.

Path normalization:

```go
// rel normalizes a tool path. Root performs the real containment check.
func (w *Workspace) rel(p string) (string, error) {
	if filepath.IsAbs(p) {
		var err error
		if p, err = filepath.Rel(w.dir, p); err != nil {
			return "", err
		}
	}
	p = filepath.Clean(p)
	if !filepath.IsLocal(p) {
		return "", ErrOutsideWorkspace
	}
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if part == ".git" {
			return "", ErrGitProtected
		}
	}
	return p, nil
}
```

| Tool | Inputs | Behavior and limits |
|---|---|---|
| `read_file` | `intent`, `path`, `offset` (1-based, default 1), `limit` (default 2000, max 4000) | Returns one contiguous numbered line window plus a `lines X–Y of Z` header describing the lines actually delivered; line counting splits on `\n` (a trailing newline adds no line, an empty file has zero lines), and bytes are preserved. When `B < Z`, the result ends with `continue with offset=B+1`. A byte-capped page keeps whole lines and sets `truncated`; a single line that cannot fit is delivered as a rune-safe prefix with `[line N truncated at X bytes]` and no misleading continuation. Result cap 60 KiB, chosen so the rendered result and marker stay under the 64 KiB tool-result field cap. Rejects binary/non-UTF-8 files and files over 2 MiB. |
| `read_image` | `intent`, `path` | Reads one workspace image and returns it to the model as an image part plus a one-line caption. The media type comes from the bytes, never the extension: PNG, JPEG, and GIF (first frame) are accepted; everything else is `invalid_image`, and a text type adds a `read_file` hint. A source within 2048 px on its longest edge, 2,000,000 pixels, and 512 KiB is forwarded byte for byte; otherwise it is downscaled with a box filter, composited over white when it has alpha, and re-encoded as JPEG through a 85/75/65/55 quality ladder with 0.75 size steps down to a 256 px floor. Source caps are 2 MiB and 16,000,000 pixels, checked from the header before a full decode, so a decompression bomb is `too_large` rather than an allocation. Output is deterministic: the same source produces the same bytes and the same SHA-256, which is recorded in `details` beside the source and delivered dimensions and the transform (`passthrough`, `recompressed`, or `downscaled`). Image contents are untrusted data. Returns `unavailable` without reading the file when the resolved model is known to reject image input. |
| `edit_file` | `intent`, `path`, `old_string`, `new_string`, `replace_all` (default false) | Requires a unique exact byte match unless `replace_all`; zero or multiple matches are actionable errors; `old_string == new_string` is an error. Rejects a symlink anywhere in the path. Writes atomically via an `O_EXCL` temp file in the same directory plus rename; preserves mode with fchmod; preserves bytes, including CRLF. Creates a file only when it does not exist and `old_string` is empty, using mode 0644 and requiring the parent directory to exist; an existing file with an empty `old_string` is an error. Rejects an edit whose resulting file would exceed 2 MiB. Returns a short diff summary. |
| `todo` | `intent`, `items` (optional array; each item is `content` plus `status` = `pending`, `in_progress`, or `completed`) | Maintains one in-memory checklist for multi-step work, seeded at run start from the last successful `todo` result in the session log. A present `items` replaces the whole list, an empty list clears it, and an omitted `items` reads it; the full list is returned after every call and the same items are recorded in `details` so a later run can restore them without parsing the rendered text. A host may render the current list in a persistent capped widget, updated from those details and restored on resume, fork, and session switch. At most 50 items, at most 500 bytes per normalized item, and at most one `in_progress` item; violations are `invalid_args`. Content is sanitized to valid UTF-8 with controls replaced and whitespace collapsed to one line. Rendering is `[ ]`, `[~]`, and `[x]` with per-status counts. The list is harness state, not a workspace file; it is not a durable event of its own and does not survive a log without the recorded `details`. |
| `delegate` | `intent`, `tasks` (1–4 items; each is `agent`, a self-contained `task`, and an optional child `session`) | Runs each task in its own child session under `<state>/sessions`, recording `parent_session`, `parent_seq`, and `parent_call_id`. A child is a normal run: fresh store, own `run_start`/`run_end`, exactly one `final`, recorded prompt, recorded tool grant, resolved model/effort, inherited workspace. A call whose every task's effective grant is read-only (`read_file`, `read_image`, `search`, `web_search`, `web_fetch`) or a custom tool declared `ConcurrencySafe` runs its tasks concurrently under a cap of four; any other call keeps array order and runs sequentially, and `details` records `execution` (`concurrent` or `sequential`) plus an `execution_reason` when sequential. The consolidated result has one `[task N agent=… status=… session=…]` section per task in array order and a `details` table with agent, intent, model, effort, turns, context tokens, usage, cost, and report size. `OK` is true when at least one task produced a report. Precedence: `invalid_args`/`not_found` before any task runs (unknown agent, bad or duplicate `session`, over-cap `task`), else `canceled`, else `subagent_limit`, else the failure code when every task failed, else `ok`. A child cannot call `delegate`: the tool is absent from its schemas and its registry grant rejects the call with `unknown_tool`. Depth is one. The report is sanitized, marked once when it matches an instruction-shaped pattern, and capped at 16 KiB; `details` is capped at 4 KiB. |
| `search` | `intent`, `pattern` (Go RE2 regex), `path` (default `.`), `glob` (optional, matched against workspace-relative paths), `case_sensitive` (default false), `max_results` (default 100, max 200) | Walks the workspace, skipping `.git`, binary files, symlinks, and files over 1 MiB, and reports skipped counts by reason. Returns `path:line: text`, with long lines clipped. Requires a real directory root. Invalid regex, invalid glob, and no matches are distinct results. `**` matches zero or more whole path segments; `*` does not cross `/`. Output is capped at 60 KiB head-only. A result-count cap is marked `[results capped at N]` and does not set `truncated`; dropped bytes do. |
| `web_search` | `intent`, `query`, `max_results` (default 5, max 10), `categories`, `language`, `time_range` (`day`/`month`/`year`) | Sends one GET to `<searxng_url>/search?format=json` with the host-supplied `User-Agent`, a fixed 15-second timeout, and a 2 MiB response cap. A missing identity returns `unavailable`. Filters to absolute http(s) result URLs with no userinfo, strips fragments for deduplication, and sanitizes title/URL/snippet: invalid UTF-8 replaced, entities decoded, tags stripped, control characters removed, whitespace collapsed; snippets are capped at 1,200 characters. Returns title/URL/snippet only and never fetches a result page. Output is capped at 60 KiB head-only; when the cap drops results the header count reflects what was delivered and `truncated` is set. A 403 is `unavailable` with JSON-format guidance, other non-2xx is `http_error`, transport/decode failures are `fetch_failed`, and an oversized body is `too_large`. An unset backend returns `unavailable` with the setting names. `details` records delivered results, truncation, and unresponsive engines. |
| `web_fetch` | `intent`, `url`, `offset` (byte offset into the extracted UTF-8 text, default 0) | One GET through the configured provider. Builtin: absolute http(s) only, no userinfo or fragment, max 8 KiB; every address is resolved and checked at dial time and on every hop. Loopback, private, and shared addresses (including IPv4-mapped and NAT64-wrapped forms) refuse the request unless its exact `scheme://host:port` origin is in `WebFetchConfig.AllowedOrigins`; metadata, link-local, unspecified, multicast, documentation, benchmarking, reserved, and protocol-assignment addresses are refused even for an allowed origin. The allowlist is host configuration and is applied per hop, so a redirect cannot leave it. At most 5 redirects; same-host hops are revalidated and followed, HTTPS-to-HTTP and cross-host hops stop and return the target as successful data. The request sends `Accept` and the host-supplied `User-Agent` (a missing identity returns `unavailable`), no cookie jar, no `Authorization`, no `Referer`, and no proxy environment. Body 2 MiB after decompression, headers 64 KiB, timeout from `WebFetchConfig.Timeout` (30s default, 120s max). HTML is decoded (Content-Type/meta charset/BOM, UTF-8 default), stripped of script/style/nav/header/footer/aside/form/hidden nodes, and extracted with `go-trafilatura` (conservative DOM fallback); text/markdown/JSON/XML/CSV pass through as text; images, audio, video, PDF, and octet-stream are `unsupported_content_type`; a missing type is sniffed. An app shell, bot challenge, or empty extraction is `content_unavailable`, never empty success. Text is sanitized (valid UTF-8, LF, no controls or ANSI escapes) and capped at 60 KiB per call with `[truncated at 61440 bytes; continue with offset=N]`. The Crawl4AI provider posts one URL to `<endpoint>/crawl` with `cache_mode: "disabled"` and `check_robots_txt: false`, stream-decodes `results[0].html` (2 MiB cap, 16 MiB response cap), and applies the same extraction, caps, and paging. A provider failure is `fetch_failed` and is never retried against the builtin path. A non-2xx provider response carries the sanitized provider body detail, read once under a 4 KiB cap and capped at 400 characters, so a named refusal such as a blocked target is visible to the model instead of collapsing to a bare status. |

| `run_command` | `intent`, `command`, `cwd` (default workspace root), `timeout_ms` (default 120000, max 600000) | Runs `/bin/sh -c` in its own process group with `Setpgid`, a `Cancel` that kills the group, and `WaitDelay`; stdin is nil. Captures stdout and stderr separately, sanitizes to valid UTF-8, and caps at 64 KiB combined with head+tail. When combined output exceeds the cap, the full combined stream is written to a unique `<state>/work/<session-id>/tool-output/*.log` file (directory 0700, file 0600, `O_EXCL`, no symlink following); the inline result ends with `full output: <absolute path>` and `details` records `spilled`/`path`. The per-run work cleanup removes the file; after resume the path is historical and its content is gone. Returns `exit_code`, `timed_out`, and labeled streams. Non-zero exit is a normal result (`ok=true`). On timeout or cancellation, kills the whole process group. Rejects `cwd` outside the workspace. |

`search` is implemented with a Go walk; the harness does not shell out to `rg`;
measure before adding it.

Tool descriptions are versioned prompt material. New sessions record
`toolset-v3`. The sets are frozen per version: `toolset-v0` (four tools),
`toolset-v1` (`web_search`, `web_fetch`, `todo`, and the shared `intent`
argument added under the earlier no-backward-compatibility decision),
`toolset-v2` (v1 plus `delegate`), and `toolset-v3` (v2 plus `read_image`).
Each bump pays for exactly one new tool; per-role tool grants are recorded
session data (`session_start.tools`) rather than new versions, so a child
narrows its recorded set without creating a version per role. A session that
recorded an older version keeps its frozen descriptions and cannot call a tool
the version does not define. `tools.SchemasFor(version)` selects the set,
`tools.Filter` narrows it to the recorded grant in recorded order,
`agent.Project` carries the result in `Projection.Schemas`, and the runner sends
that set. An unknown version or a granted name the version does not define is a
replay error. A session that recorded v0 or v1 keeps its frozen descriptions
and, because `delegate` is absent from both, cannot delegate. Read-only
concurrent fan-out is execution behavior, not new prompt material, so the
`toolset-v2` descriptions stay unchanged and no version bump was needed; the
per-call `details.execution` records what ran.
The `web_search` description carries the untrusted-snippet warning and the
`web_fetch` description labels fetched content as untrusted data.

`edit_file` writes atomically: create a temp file in the same directory with
`O_CREATE|O_EXCL` and a random name, write, call `f.Chmod(existingMode)` through
the open file (fchmod, not path-based `Chmod`), `f.Sync`, close, then
`Root.Rename`; remove the temp file on error. New files use mode 0644.

`run_command` receives an explicit allowlist, never `os.Environ`: `PATH`,
`LANG`, `LC_ALL`, `TERM`, `HOME`, and a harness-set `TMPDIR`. `HOME` is
inherited unchanged: it is where git, ssh, and language toolchains find user
identity and configuration, and replacing it silently breaks `git commit` with
no `user.email`. A harness-owned `HOME` would buy no containment anyway, because
the child runs with the harness's privileges and can read any path by absolute
name; the boundary is external isolation. `TMPDIR` points at a harness-owned
directory under the state dir, `<state>/work/<session-id>/tmp`, created with mode
0700 when the session starts, because the host `/tmp` may be mounted `noexec`,
size-limited, or cleaned while a long session runs.
It is session scratch, not durable storage: the engine resets
`<state>/work/<session-id>` before each run and removes it at `Host.Close` or
`Host.CleanupWork`; a delegated child removes its own work directory when the
child run ends. `Host.WorkDir` exposes the path and `Host.CleanupWork` is the
idle-only explicit cleanup. Removal is best-effort; if
removal fails, the host reports the leftover path and leaves it for
inspection, and because a child may leave directories without owner write
permission, cleanup restores owner access before retrying once. The workspace is
the intended place for durable task artifacts; child `TMPDIR` is ephemeral. An
explicit denylist rejects any allowlisted name matching `*_TOKEN`, `*_SECRET`,
`*_KEY`, or `*_PASSWORD` (case-insensitive); this is defense in depth, not a
boundary. The child never inherits provider credentials or unrelated host
variables. This is hygiene, not isolation; a child process can still inspect the
harness process and read the user's home by absolute path. Use external
isolation when that matters, and run the harness inside bwrap, a container, or a
VM with a container-scoped home when the child needs config isolation. The state
dir is configurable; point it at fast local disk if needed.

The tool descriptions and JSON schemas are versioned prompt material, not
afterthoughts. Schemas are hand-written `json.RawMessage` constants. Arguments
are decoded into typed structs with `RejectUnknownMembers(true)`; tests assert
that each schema and its argument struct agree. `read_file` returns line
numbers so the model can reference what it saw; `search` returns locations so
the model can page; `edit_file` errors state whether the match was absent or
ambiguous. These ACI choices are the highest-leverage prompt work in v1.

Caller tools are registered through the public facade. Their declarations are
recorded in `session_start.custom_tools` (at most 32 tools, 64 KiB total,
2 KiB description, 32 KiB parameters) and projected by
`agent.ProjectWithTools`; a resume must supply the identical declarations in the
same order, and a missing or changed declaration is `ErrToolMismatch`. Names are
lowercase ASCII letters, digits, `_`, and `-`, at most 64 bytes, and cannot
collide with a built-in (enabled or disabled) or with `delegate`. A custom tool
panic is recovered like a built-in panic; a returned error becomes
`tool_error`, a returned `*aharness.Deny` becomes `permission_denied`, and a
returned context error follows the operation-interrupt path. `Details` is capped
at 64 KiB before recording.

`ToolResult.Image` attaches one image to the result. The facade normalizes it
exactly as `read_image` does (`tools.NormalizeImage`: the type comes from the
bytes, the source caps apply, and the delivered image is downscaled to the
`read_image` limits) and records it in `tool_result.images`. An image the SDK
cannot deliver fails the result with `invalid_image`, `too_large`, or
`unavailable` and is never recorded; the caller's `Output` is kept. Display
events stay image-free, so a host that wants to show the picture uses the bytes
its own handler produced.

`Config.Authorize` (public) becomes `agent.Config.ToolPolicy` and runs after
`ToolStarted` and before dispatch, for built-ins and custom tools, parent and
children. A denial is a model-visible `tool_result` with
`code=permission_denied`; it is excluded from the repeated-failure key and never
becomes `StopStuck`. The hook is policy, not a sandbox. The engine serializes it
with one mutex shared by the parent run and its delegated children.

Tool calls within one model response execute strictly sequentially in arrival
order. That fixes log order, replay order, authorization order, canonical-
argument stuck detection, and the guarantee that two `edit_file` calls in one
response cannot race. `Tool.ConcurrencySafe` (public) is recorded per custom tool
and only permits concurrent *delegated children*; it never changes in-run order.
A batch custom tool is the supported way to parallelize inside one call.
