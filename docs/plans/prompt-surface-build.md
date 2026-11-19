# aharness prompt-surface review and build plan

- **Date:** 2026-11-19
- **Status:** implemented 2026-11-19 for the v0.3.0 work. Phases 1, 3, 4, and
  5 landed in this repository; Phase 2 is delivered to acode as Appendix A.
  Automated gates pass. The manual real-model probes in §7.2 were **not run**;
  the exact pending check is recorded in §12.
- **Source:** the prompt-surface issue filed from acode on 2026-11-19 and its
  amendments patch (acode, Ask 7). Both live in the filer's gitignored
  `.issues/` directory, so they are named here rather than linked.
- **Decided by the owner:** the five bundled roles move to acode (D0), and the
  SDK is mechanism-only, with prompt and role content owned by the caller (D1).
- **Decided in this revision:** no SDK role defaults, so `DefaultAgents` is
  removed (D2); composition is tiered with `Identity` + `DefaultPromptBody` as
  the primary path (D4, §1.7).
- **Baseline:** `v0.2.0` (`ac65752`), clean tree except an unrelated
  `.gitignore` edit.
- **Method:** every factual claim was checked in the source; the repository's
  own rules in `AGENTS.md`, `docs/architecture.md`, `docs/definition-of-done.md`,
  and `docs/testing-strategy.md` were applied. Verdicts are **genuine**,
  **genuine but overstated**, **partly genuine**, or **not genuine as stated**.
- **Target:** Go 1.27.3, module `github.com/eddiectc/aharness`. Ships as
  **v0.3.0**: the plan contains breaking changes, and the pre-1.0 rule permits
  them in a minor release when each is listed under **Changed**/**Removed** with
  a migration step (`CHANGELOG.md:3-6`).
- **References:** [architecture.md](../architecture.md) §3.2.1, §4.3, §6;
  [limits.md](../limits.md); [testing-strategy.md](../testing-strategy.md);
  [definition-of-done.md](../definition-of-done.md);
  [evaluation-research.md](../research/evaluation-research.md).
- **Index entry:** [docs/README.md](../README.md).

## 1. Layering: what the SDK owns

### 1.1 The question

Should aharness be mechanism-only, with system prompts and subagents owned by
the caller?

**Yes, with one exception.** The SDK must own the prompt *contract* and the
*loop invariants*. Everything else — identity, coding rules, role taxonomy, and
coordination policy — is a replaceable default that belongs to the host.

The exception is not a compromise of the principle; it is what makes the
mechanism usable. A tool-calling loop only works if the model knows that a tool
call is the only way to act, that every call gets exactly one result, that a
reply without a call ends the run, and that tool errors are data. Those four
sentences are part of the loop's contract, not product voice. They already are
named as such in `doc.go:60-67`, which tells a host that a replacement prompt
*"must keep the invariants the loop depends on"*.

### 1.2 The repository already separates mechanics from policy — for tools

`docs/architecture.md:1043` states the rule: *"Tool mechanics belong in tool
descriptions; the system prompt states only when a coordination tool is worth a
turn."* The code follows it:

- `todo`'s mechanics — atomic list replacement, at most one `in_progress`,
  harness state restored from the session log — are in the schema, not the
  prompt (`tools/schemas.go:185`).
- `delegate`'s mechanics — up to four tasks, each child starts with no history so
  the task must be self-contained, a child cannot delegate — are in the schema
  (`tools/schemas.go:213`).

So the prompt's `todo` and `delegate` blocks are **policy** ("when is this worth
a turn"), and a host that drops them loses coordination quality, not tool
correctness. That is precisely why caller-owned prompt content is safe.

### 1.3 The three layers

| Layer | Owner | Content | Replaceable |
|---|---|---|---|
| Loop invariants | SDK | Tool call is the only way to act; every call gets exactly one result; a reply without a call ends the run; tool errors are data. Plus the execution fact that commands run with the harness's privileges and there is no sandbox. | **No.** Must be kept, and must be exportable so a host need not transcribe them. |
| Prompt contract | SDK | Version constants, frozen replay, the 32 KiB recorded cap, catalog rendering and its 4 KiB cap, `session_start.system_prompt`, replay from recorded text. | **No.** |
| Coding defaults | SDK ships, host replaces | Search/read before editing; keep changes minimal; run the relevant tests; read project instructions. | Yes, wholesale or line by line. |
| Coordination policy | SDK ships, host replaces | When `todo` is worth a turn; when `delegate` is worth a turn. | Yes. |
| Identity | Host | `acode`, `aharness`, any product name. | Yes. |
| Role taxonomy and text | Host | The five roles, their descriptions, bodies, grants, models, effort. | Yes. |

The current code conflates layers two and three and hardcodes layer five. That
conflation is the issue's real subject; the individual asks are its symptoms.

### 1.4 Decision D0 — the five roles move to acode

Agreed by the owner. Concretely:

**Moves to acode:**

- The five definitions (`explore`, `implementer`, `researcher`, `reviewer`,
  `task`): names, descriptions, bodies, grants.
- The eventual reviewer-grant decision (see §3.5).
- The role-routing policy that the descriptions encode.

**Stays in the SDK:**

- The definition parser and discovery mechanism (`ParseAgentDefinition`,
  `LoadAgents`), because they are reusable mechanism and already fuzz-tested.
- The caps: 200-byte description, 16 KiB body, 32 agents, 4 KiB catalog.
- `DelegationConfig.Agents` as the explicit, required host-supplied set.
- A host-defaults seam in discovery (Ask 7, §1.6), so acode can install its
  five with operator files still winning.
- A runnable example definition set in `example_test.go` as the starting
  template (D2, §1.8). No runtime default role ships.

**Sequencing is compiler-enforced, so there is no hard cross-repo gate.** All
three breaks are compile-time in Go, so a host cannot miss them:

| Break | How the host notices |
|---|---|
| `LoadAgents` gains a `base` parameter | Compile error at the call site |
| `RenderSystemPrompt` gains `identity` | Compile error at the call site |
| `DefaultAgents` is removed | Compile error at the call site |

v0.3.0 ships the whole set at once; acode bumps and fixes what the compiler
flags. Phases 1–3 therefore need no coordination window and can land in one
release.

**One footgun the compiler cannot catch.** The minimal fix for the
`LoadAgents` break is to add `nil`, and `nil` compiles. A host that relied on the
former bundled set, passes `nil`, and *does* have operator files under
`<state>/agents/` gets a working catalog that has silently lost the five roles —
no error, degraded routing. The CHANGELOG migration step must say it outright:
*nil means no host defaults; if you relied on the bundled set, supply your own.*
Without operator files the facade's empty-`Agents` error catches it loudly.

One further compile-invisible change is harmless here: `Source` value `"bundled"`
becomes `"default"`, and the facade `AgentDefinition` has no `Source` field, so
only a component-package user (a definitions browser) can observe it.

This resolves the issue's internal contradiction. Its §4 says "delegation policy
stays with the host" while Ask 1 puts "when to call" into SDK descriptions.
Under D0 both are true: the SDK ships no role routing at all, and after D2 it
ships no role text either.

### 1.5 Decision D1 — prompt content is caller-owned; the contract is SDK-owned

Agreed. Three consequences follow, and they change the issue's priorities:

1. **Ask 2 is the layering contract, not ergonomics.** If content belongs to the
   host, the host must be able to obtain the mechanism without the branding.
   Today it cannot: `harnessRules`, the guidance consts, and the catalog
   renderer are unexported (`agent/prompt.go:28-61`), and
   `RenderSystemPrompt` returns the branded whole (`:146-152`). A host's only
   supported choices are "accept `You are aharness`" or "transcribe the
   invariants", and transcription drifts on the next release. That is the exact
   failure the issue condemns in §1.3, so the fix is a prerequisite, not a
   nicety.
2. **The issue's single `PromptBody` is not enough (see §3.6).** A host with a
   non-coding product — the research-agent-over-data acceptance case at
   `docs/plans/go-library-build.md:66-67` — must keep the loop invariants but
   drop "run the relevant tests". One combined body cannot express that, and
   writing its own body means transcribing the invariants. Ship the invariants
   as their own export.
3. **Ask 4 stops being optional.** If the host owns the content, the SDK's
   `prompt_version` no longer describes the prompt the host actually ran. The
   host needs its own attribution tag alongside the SDK template version, or
   every eval is misattributed (see §3.8).

**v3 stays P1, because the primary path keeps the SDK body.** The plan's
recommended composition (§1.7) is `DefaultPromptBody` plus `Identity`: a host
replaces only what it owns and inherits the rest. Under that path the v3 text is
not a vestigial default for bare hosts — it is acode's production prompt, and
every v3 line reaches production. That is desirable: the alternative, acode
retyping the coding rules, reintroduces exactly the transcription drift D1
forbids. Ownership means the host *can* replace the text, not that it must.

### 1.6 Ask 7 from the acode amendments: adopt, with one correction

**The technical claim is genuine.** `LoadAgents` seeds from the compiled-in
`bundledAgents()` in the same statement that begins discovery
(`agent/agents.go:311`), so there is no point between "SDK defaults" and
"`<state-dir>/agents/*.md`" for a host's own set. The facade path already
accepts host definitions (`delegate.go:79-137`), but the file path does not.
acode cannot ship definitions through discovery without either re-implementing
the parse/precedence loop or post-processing the returned catalog.

**Adopt the acceptance line verbatim:** a host can install its own default
definitions with operator files still winning, without re-implementing
discovery.

**One correction, because the patch and D0 point in opposite directions.** The
patch's Ask 1 amendment keeps the five roles in the SDK as "the library's own
defaults" *and* has acode ship "an identical set as app content". That is two
copies of the same five role texts — the drift problem §1.3 condemns, moved one
layer up. Under D0 the five move and no duplication exists. The patch's stated
need — bare library hosts and the SDK's own tests — is real, but it is not
served by keeping role text; D2 answers it with an example instead.

**Take the clean seam, not the interim.** The patch offers three shapes. The
`Source == "bundled"` workaround is fragile: it depends on an unexported string,
the host must know which names are bundled, and it dies the moment the SDK ships
no defaults. The merge-helper shape still leaves the parse-and-precedence loop
in the host. Change the discovery signature instead:

```go
// LoadAgents resolves the effective definition set: base first, then
// <state-dir>/agents/*.md in file-name order, replacing a base agent of the
// same name. A nil base means no host defaults.
func LoadAgents(base []AgentDefinition, stateDir string) *AgentCatalog
```

This is a pre-1.0 breaking change to a component export, with a one-line
migration (`LoadAgents(dir)` → `LoadAgents(hostDefaults(), dir)`, or
`LoadAgents(nil, dir)` for a host with no defaults). It is better than adding
`LoadAgentsWith(base, dir)` beside the old function: the definition of done
requires a clean cutover and rejects compatibility shims
(`docs/definition-of-done.md` §1).

**Two gaps the patch does not mention, both real in the current code:**

- **The cap is applied only to file entries.** The `maxAgents` truncation sits
  inside the file loop (`agent/agents.go:356-360`); a base set of 40 would be
  returned unbounded with no warning. A seeded variant must validate and cap
  `base` with the same rules and emit the same warning.
- **Base entries are not validated.** `ParseAgentDefinition` validates files
  (`:211-213`), but nothing validates a host-supplied set on the component path.
  The new signature must put invalid base entries in `Invalid` with a warning,
  exactly as it does for a bad file, or a host can inject a definition the
  spawner will reject at first use.

**`Source` is cheap to fix.** It is never recorded durably — it is set in
`LoadAgents`/`bundledAgents` and read only by presentation code; the facade does
not expose the field, and `ApplyAgentOverrides` takes a whole `*AgentCatalog`
(`agent/overrides.go:119`), so a host-seeded catalog flows through unchanged.
The source constants are unexported (`agent/agents.go:29-33`) while the public
`AgentDefinition.Source` field documents them. Export them, and rename
`bundled` to `default`, because after D0 the base set is the host's, not the
SDK's:

```go
const (
    SourceDefault = "default" // a host-provided base default
    SourceUser    = "user"    // <state-dir>/agents/*.md
)
```

This is presentation-only; a definitions browser that compares `"bundled"` needs
a one-line update, recorded in the CHANGELOG.

### 1.7 The composition path (decided)

Three tiers, in order of preference. A host should use the lowest tier that
meets its needs, because each higher tier is more text the host owns and must
keep correct.

| Tier | What the host supplies | What it inherits | Use when |
|---|---|---|---|
| 1 (default, recommended) | `Identity` | `DefaultPromptBody`: loop invariants, coding policy, coordination policy, and the catalog of the host's own definitions | The product is a coding agent. This is acode. |
| 2 | `Identity`, `LoopRules()`, its own policy lines, `AgentCatalogText(agents)` | Nothing but the invariants | The product is not a coding agent (the research-agent-over-data case), or its coordination policy differs. |
| 3 | `SystemPrompt` wholesale | Nothing; `LoopRules()` is how it obtains the invariants without retyping them | The host needs full control, or a prompt format the primitives cannot express. |

**Why tier 1 is primary.** acode is a coding agent, and the SDK's coding rules
(search before editing, keep changes minimal, run the tests, read project
instructions) are the rules it needs. Composing from tier 2 to obtain them means
retyping them, which is the drift D1 forbids. Tier 1 lets acode own exactly what
is product-specific — its name, its five roles, its catalog — and inherit the
rest. So **acode uses `DefaultPromptBody` plus `Identity`**, and the v3 body in
§3.11 is production text.

**Why tier 2 must exist.** Without it, a non-coding host must choose between
inheriting "run the relevant tests" (wrong for a data-analysis product) and
writing a body from scratch (transcribing the invariants). `LoopRules` and
`AgentCatalogText` are what make tier 2 cheap and drift-free.

**Why tier 3 stays supported.** `Config.SystemPrompt` already replaces the
prompt entirely, and `doc.go:60-67` already states the invariants a replacement
must keep. Tier 3 is that path, with the invariants available instead of
transcribed. Do not remove it.

**`AgentCatalogText` is therefore required, not optional.** Tier 2 without it
forces the host to re-implement the 4 KiB cap and the omission marker — contract
logic, by D1's own definition. Shipping `DefaultPromptBody` alone would leave
tier 2 defective. This closes the plan's "two or three primitives" question: **three.**

### 1.8 The fallback agent (decided: none)

**Delete `DefaultAgents()` from both packages.** No fallback role ships.

The decision is cheap because nothing depends on it: the function has two
definitions (`agent/agents.go:400`, `delegate.go:41`) and two references, both in
error text (`delegate.go:79`, `aharness.go:487`); **no test calls it**, and every
test that needs a definition set already writes one inline
(`acceptance_test.go:36-42`, `delegation_test.go:51-56`,
`subagent_children_test.go:21`).

The reasons are decisive:

1. **No code path needs it.** `delegate` is offered only when
   `ToolsConfig.Delegation` is set, and construction already rejects an empty
   `DelegationConfig.Agents` (`delegate.go:78-80`). A host that wants delegation
   must supply a set either way; a default never rescues anyone.
2. **It contradicts all three layering decisions at once.** Any fallback role
   needs a description (routing text — D0 says the host owns it), a body (prompt
   content — D1 says the caller owns it), and a grant (policy — §3.5). A "neutral"
   fallback is a role taxonomy of one.
3. **It is the incident in miniature.** SDK-shipped role text steering a
   product's routing is precisely what went wrong: a generic SDK role that is
   good enough becomes the production review agent, and nobody chose it.
4. **It would become a permanent frozen surface.** Any shipped role text must be
   pinned and versioned, and cannot change without a prompt-version bump, for
   hosts that may not even use it.

**The legitimate need — a starting template — moves to documentation.** Ship the
definitions as a runnable `Example` in `example_test.go`, next to the existing
`Example`. Example functions are compiled and executed, so the template cannot
rot, and nobody's production routing depends on it. That satisfies the patch's
"serves bare library hosts" without shipping role text.

**Consequences.** `DelegationConfig.Agents` stays required, and the error becomes
actionable without naming a deleted function: *"must not be empty; supply the
host's definition set."* `AgentDefinition.Source` keeps a `SourceDefault` value,
which now means "a host-provided base" — the rename in §1.6 becomes strictly
necessary, since there is no longer a bundled set to describe.

## 2. Verdict summary

| Issue item | Claim | Verdict | Priority | Disposition |
|---|---|---|---|---|
| §1.1 | The delegation trigger never fires for review | **Genuine.** The shipped paragraph conditions delegation on output volume or parallelism; a cheap serial review satisfies neither. | P1 | Fix the criterion in the SDK body (Phase 5); acode inherits it under D4 tier 1 |
| §1.2 | The catalog is the only routing signal and carries capability labels | **Genuine.** Verified: one `- name: description` line per agent, and the delegate schema points at that list. | P1 (acode) | The descriptions move to acode with the roles; the SDK keeps only the cap and renderer |
| §1.3 | A host cannot own its identity without transcribing the contract | **Genuine but overstated.** `RenderSystemPrompt` is exported, but only as the branded whole. | P1 | Asks 2–3 become the composition surface (§3.6–3.7) |
| §1.4 | Children never see the workspace's rules | **Genuine.** No project-instruction mechanism exists anywhere. | P1 | Project-instructions line in the v3 body (Phase 5); inherited by acode under tier 1 |
| Ask 1 | Five trigger-shaped descriptions and bodies | **Genuine as content, superseded as SDK scope.** Moves to acode per D0. | P1 (acode) | Appendix A is the handoff artifact, including the reviewer reword |
| Ask 2 | Export `PromptBody` | **Genuine but insufficient.** One combined body forces a non-coding host to choose between branding and transcription. | P1 | Three primitives: `LoopRules`, `AgentCatalogText`, `DefaultPromptBody` (§3.6) |
| Ask 3 | Host-supplied identity, for children too | **Genuine.** The child line is hardcoded. | P1 | `Identity` on `Config`/`Spec`/`agent.Config`; `RenderSystemPrompt` gains it |
| Ask 4 | A caller prompt tag | **Genuine, stronger than stated.** The facade cannot set `PromptVersion` at all, and `harness_version` is the SDK's version. | P1 | `PromptTag` recorded beside `prompt_version`; no `SupportsPrompt` change |
| Ask 5 | Fork + `Instructions` resurrects the SDK prompt | **Genuine bug.** The fork branch re-renders instead of appending to the recorded text. | P1 | Append to the recorded prompt; regression test |
| Ask 6 | prompt-v3, six line changes | **Genuine default-content work**, with three mechanical traps. It is production text under D4 tier 1. | P1 | `PromptVersionV2`, version-aware child selection, frozen pins, the four invariants, the delegation criterion |
| Ask 7 (patch) | A host-defaults layer in discovery | **Genuine.** Confirmed: no seam, and the cap/validation gaps are real. | P1 | `LoadAgents(base, stateDir)`; export `Source*`; validate and cap the base |

## 3. Findings

### 3.1 §1.1 is real, and only the default body or acode's body can fix it

The shipped paragraph is `agent/prompt.go:54-55`:

> `delegate` runs independent work in a child agent. Delegate a self-contained
> investigation, review, or verification when it would otherwise flood this
> conversation with tool output, or when independent read-only tasks can run in
> parallel. …

"Review" is named, so "never fires" is too strong; the defect is that both
qualifying conditions are cost or parallelism filters. A cheap, serial review of
a thirty-file diff satisfies neither, and the parent can cite the SDK's own
sentence for not delegating.

Every ask in the issue leaves this paragraph untouched. Ask 1 improves the
catalog, and then the catalog *contradicts* the paragraph: the reviewer
description will say "call it before finishing contract, documented-behavior, or
multi-file changes" while the general rule permits delegation only for flood or
parallel work.

Under D1 the fix has two homes, and both are needed:

- **The SDK default body** (Phase 5) for bare library hosts and the SDK's tests.
- **acode's own body**, if acode replaces it: the same criterion, handed over in
  Appendix A.

The criterion is about the *value of fresh context*, not which role to pick, so
it does not smuggle product routing into the SDK:

> Delegate a self-contained investigation, review, or verification when the
> conclusion must be reached without the context that produced the work, or when
> it would otherwise flood this conversation with tool output, or when
> independent read-only tasks can run in parallel.

### 3.2 §1.2 is real, and the catalog remains the sole model-facing surface

`reviewer.Description` is exactly `"Correctness, regression, and test-gap review
of a change."` (`agent/agents.go:437`). The catalog is rendered from
`AgentCatalog.Infos()` (`:108-120`) by `renderAgentCatalog`
(`agent/prompt.go:97-107`), one line per agent. The delegate schema tells the
model to take the name "from the available-agents list in the system prompt"
(`tools/schemas.go:232-233`); the unknown-agent error lists names only, not
descriptions (`tools/delegate.go:226-241`). The descriptions are the router,
exactly as claimed.

Host-supplied definitions do reach the rendered catalog: the facade converts
them (`aharness.go:210, 461-480`), the engine validates them into an
`agent.AgentCatalog` (`engine/runner.go:193-243`) and hands that to the spawner
(`:61-64`), whose `Infos()` feeds `renderPrompt`. So moving the roles to acode
does not break routing; only the text's owner changes.

The five proposed byte counts are exact (193/191/178/189/145), all under
`maxAgentDescriptionSize = 200` (`agent/agents.go:23`), and all single-line,
which matters because `renderAgentCatalog` does not collapse whitespace. The
rendered catalog grows from 439 bytes to 961 bytes, well under the 4 KiB
`maxAgentCatalogBytes`. Appendix A is therefore safe to land in acode as written.

### 3.3 §1.3 is real but overstated: there is an export, not a usable one

`harnessRules`, the guidance consts, and `renderAgentCatalog` are unexported
(`agent/prompt.go:28-61`). `agent.RenderSystemPrompt` **is** exported
(`:146-152`) and returns the whole branded prompt. A host could render and strip
the first line, but that is unsupported string surgery against an unexported
format and fails silently when the identity line changes. There is no supported
accessor for the body. So the practical claim stands: a host either accepts
`You are aharness` or transcribes the contract.

The issue's "a host's only choices are" is rhetoric; the plan should say "no
supported way" and declare string surgery unsupported.

### 3.4 §1.4 is real: nothing in the SDK knows about project instructions

`childSystemPrompt` composes the identity line, the shared rules, and the role
body (`agent/prompt.go:113-120`). There is no project-instruction discovery
anywhere: the only `AGENTS.md` references are this repository's own docs, and the
research records the question as deliberately deferred
(`docs/research/subagent-research.md:552`). A reviewer with `read_file` and
`search` could read `AGENTS.md`, but nothing says to.

Two qualifications:

1. The research also records the opposite practice: *"Read-only roles skip
   inherited project context — Claude Code's `omitClaudeMd` exists solely to make
   exploration cheap"* (`:375`). The reviewer is read-only, so the line cannot be
   limited to mutating roles without defeating §1.4.
2. A host that already injects project memory itself will get a duplicate
   instruction. The line predicates itself ("If the workspace carries project
   instructions…"), so the cost is one redundant sentence, not a second read.
   Document it.

### 3.5 Ask 1's content is sound; the reviewer body needs one change

The proposed descriptions and bodies are sound, but the reviewer body says
"confirm each claim directly" while the bundled reviewer grants only `read_file`
and `search` (`agent/agents.go:438`); it cannot run tests. "Confirm directly"
invites a reported verification that did not happen — the exact failure class the
change exists to prevent. Two prior decisions settle the default:

- `docs/plans/subagent-build.md:463-467`: `explore`, `researcher`, and `reviewer`
  omit `run_command` deliberately, because a shell can write files and there is
  no sandbox.
- `docs/research/subagent-research.md:362`: "Every reviewer in every harness is
  read-only."

A read-only reviewer also keeps review fan-out concurrent: a delegate call runs
concurrently only when every task's grant is read-only
(`agent/spawn.go:283-297`). This is now **acode's decision**, but the
recommendation stands: keep the grant, reword the body to "check each claim
against the code and tests, and say what you could not settle." The incident's
defects — a tool-name literal, a `Result.Reason` mismatch, a warning with no
reader — are all visible in source anyway. Appendix A carries the reworded body.

### 3.6 Ask 2: one body export is not enough for a non-coding host

The identity line is the only place `workspace` appears
(`agent/prompt.go:38-40`); the body is the rules, the guidance, and the catalog.
The issue's `PromptBody(version, workspace, agents)` therefore takes a parameter
it never reads. Drop it.

More importantly, D1 makes a single combined body the wrong shape. The three
constituents have different owners and different lifetimes:

| Constituent | Owner | A host that replaces content |
|---|---|---|
| Loop invariants (4 + the privileges fact) | SDK, mandatory | Must keep |
| Coordination policy (todo/delegate) | SDK default | May keep, replace, or drop |
| Catalog | Derived from the host's own definitions | Must render |

A host building the research-agent case must keep the invariants and drop "run
the relevant tests". A combined `PromptBody` forces it to either keep the coding
rules or retype the invariants — the transcription problem again. Ship three
small primitives instead:

```go
// LoopRules returns the instruction lines the loop depends on: a tool call is
// the only way to act, every call receives exactly one result, a reply without
// a call ends the run, tool errors are data, and commands run with the
// harness's privileges. A replacement prompt must keep them.
func LoopRules() string

// AgentCatalogText renders one line per agent under the catalog cap, with the
// omission marker. It returns the text only; the caller supplies the heading.
func AgentCatalogText(agents []AgentInfo) string

// DefaultPromptBody returns the SDK's default body: LoopRules, the todo and
// delegate policy, and the catalog. It is what the default parent prompt
// carries, minus the identity line. It is not capped; use CapSystemPrompt.
func DefaultPromptBody(version string, agents []AgentInfo) (string, error)
```

`DefaultPromptBody` is the issue's `PromptBody`, renamed to say whose content it
is. It must be assembled from the same helpers as `renderPrompt`, never by
stripping text, so the two cannot drift:

```go
func renderPrompt(version, workspace, identity string, agents []AgentInfo) (string, error) {
    body, err := DefaultPromptBody(version, agents)
    if err != nil { return "", err }
    return identityLine(identity, workspace) + "\n\n" + body, nil
}
```

`AgentCatalogText` is justified separately: a host that writes its own body still
needs the 4 KiB cap, the omission marker, and the one-line shape, and
re-implementing those *is* transcribing contract logic. Shipping
`DefaultPromptBody` alone would leave the non-coding tier defective, so the
surface is three primitives (D4).

### 3.7 Ask 3: identity needs validation and a breaking render signature

The child line is hardcoded at `agent/prompt.go:118`:
`You are the aharness %q agent…`. A replaced parent prompt never reaches it.

Add `Identity` to `aharness.Config`, `engine.Spec`, and `agent.Config`; default
it to `aharness` when empty, and use it in both the parent identity line and the
child line. The parent default matters: a library host that does not replace the
prompt should not see "aharness" while its children see "acode".

The exported renderer currently takes no identity (`agent/prompt.go:146`). To
honor `Identity` at all its signature must change:

```go
func RenderSystemPrompt(version, workspace, identity string, agents []AgentInfo) (string, error)
```

Pre-1.0, this is a **breaking** component change with a one-line migration in the
CHANGELOG. A second `RenderSystemPromptWithIdentity` would be a shim; the
definition of done rejects it.

Validation: trim; empty means `aharness`; reject invalid UTF-8, `\n`, `\r`,
other control characters, or more than 64 bytes with a `ConfigError`. A product
name is not a place to accept arbitrary prompt text.

Do **not** record identity in `session_start` in this change. Follow the
`UserAgent` pattern (`engine/engine.go:63-66`): the host supplies it on every
construction. Child prompts are recorded verbatim, so existing children keep
their text; only new children depend on re-supply. Document it. (Recording it
would be another optional field, not a schema bump.)

Frozen-text consequence: the v0/v1/v2 *bodies* stay byte-identical; the identity
line becomes a parameter whose default renders the old bytes. Pin that
`Identity == "aharness"` renders the exact old text.

### 3.8 Ask 4 is stronger than stated, and D1 makes it mandatory

`engine.Spec.PromptVersion` exists for direct engine hosts, but the public
`aharness.Config` has no prompt-version field. A facade host that replaces
`SystemPrompt` records the SDK default `prompt-v2` unconditionally
(`engine/engine.go:209-214, 239-247`). It also cannot record its own prompt
identity: `harness_version` is `Version()` (`aharness.go:202`), which is the
**SDK's** build version (`version.go:6-44`), not the host's. So a host has no
prompt-attribution field at all.

Under D1 this is not cosmetic. When the host owns the content, `prompt_version`
describes a template the host did not use; without a tag, every eval that
compares two host prompts attributes both to `prompt-v2`.

**Do not admit a `host:` tag through `SupportsPrompt`.** That function answers
"can this binary render or replay this version" (`agent/state.go:342-345`) and
gates resume and fork (`engine/engine.go:301-303, 339-341`). A host tag answers
a different question and cannot be rendered. Keep them separate:

- `prompt_version` stays the SDK template version and remains the replay key.
- Add `Config.PromptTag` / `Spec.PromptTag`, recorded as an optional
  `session_start.prompt_tag`, exposed as `State.PromptTag`.
- Validate: printable ASCII matching `[A-Za-z0-9._:/-]+`, at most 64 bytes;
  empty means unset. Include `/` so a namespaced tag (`acode/prompt-v1`) fits.
- Resume: the recorded tag wins; an explicit different tag is a `ConfigError`
  ("recorded; fork to change it").
- Fork: an explicit tag wins; with no tag, inherit only when the resulting prompt
  is unchanged, otherwise record none. A new prompt with an inherited tag would
  misattribute.
- The tag never affects rendering, caps, or replay.

This resolves the issue's open decision 3 in favor of `Spec.PromptTag`.

### 3.9 Ask 5 is a real bug with a clean fix

`resolveInherited` computes the inherited prompt from the recorded text, then, on
fork, re-renders from scratch when only `Instructions` is supplied
(`engine/engine.go:483-503`). A session created with
`SystemPrompt: "you are an analyst"` and forked with `Instructions` gets a fresh
`You are aharness…` prompt. The fork records a `contract_override` because the
prompt changed (`:367-372`), so the change is durable but silent about why.

**Fix:** append to the recorded prompt, rendering only when the parent predates
the recorded field:

```go
case spec.AppendPromptSet:
    base := state.SystemPrompt
    if base == "" {
        rendered, err := agent.RenderSystemPrompt(state.PromptVersion, state.Workspace, identity, spec.Agents)
        ...
        base = rendered
    }
    prompt = agent.CapSystemPrompt(base + "\n\n" + spec.AppendPrompt)
```

This is the only coherent reading of "`Instructions` appends to the harness
system prompt" (`aharness.go:64-66`) for a session whose effective prompt is a
recorded replacement. Add a regression test that forks a replaced-prompt session
with instructions and asserts the recorded text survives and the SDK identity
does not appear.

### 3.10 Ask 6 has three mechanical traps the issue does not mention

The frozen-template claim is correct: `promptV0Format` concatenates
`harnessRules` at compile time (`agent/prompt.go:30-40`), so editing
`harnessRules` would change v0/v1/v2. But the bump is not "new consts plus a
`renderPrompt` case":

1. **There is no `PromptVersionV2` constant.** Only `PromptVersionV0`,
   `PromptVersionV1`, and the current `PromptVersion` exist (`:15-19`). After the
   bump, v2 needs its own constant or `SupportsPrompt` cannot name it and the v2
   render case has nothing to switch on. Add `PromptVersionV2 = "prompt-v2"` and
   set `PromptVersion = "prompt-v3"`.
2. **`childSystemPrompt` selects rules by the current version.** It uses
   `rules := harnessRules` for every version and adds todo guidance only when
   `version == PromptVersion` (`:113-117`). After the bump a v2 child silently
   loses its todo guidance. Select rules with an explicit switch and include
   **v2 and v3** in the todo gate.
3. **The `PromptVersion` bump changes "current" everywhere.** Mostly desired, but
   the v2 assertions at `agent/prompt_test.go:41-49` use `PromptVersion` and
   would silently start testing v3. Move them to `PromptVersionV2`.

Two more findings in this area:

- **The issue's "todo guidance pinned by test" is not true.** The only checks are
  substring assertions (`agent/prompt_test.go:41-63, 227-252`); there is no byte
  pin.
- **"v0/v1/v2 still render frozen" is currently unenforced.** Nothing asserts
  exact old text: `TestRenderPromptVersions` uses `strings.Contains` and a
  prefix check, and `TestProjectPromptV0Replay` compares against the same live
  template it is testing (`:107-140`). Add exact golden strings (or SHA-256 pins
  with a fixed workspace) for v0/v1/v2. This is what makes sharing the frozen
  rules const safe.

### 3.11 The exact v3 default body

Freeze the text in the plan so the implementer does not infer it. The shared v3
rules (the issue's six changes plus the three lines it keeps unchanged):

```text
- Tools are your only way to act on the workspace. Report an action as done only when a tool call performed it.
- Every tool call receives exactly one result.
- Search and read before editing. Keep changes minimal and focused on the task.
- Run the relevant tests before reporting the change done, and say plainly what you could not verify.
- If the workspace carries project instructions (AGENTS.md or the file it names), read them before acting on the code.
- If a task is infeasible as stated, or a check is wrong, say so rather than working around it.
- Commands run with the harness's privileges. The harness adds no sandbox and no approval step.
- Tool errors are data: read them, adapt, and try a different approach instead of escalating.
- Stop by replying without a tool call when the task is complete or when you are blocked; when blocked, say what blocks you and what remains.
```

`LoopRules()` returns only the four invariant lines plus the privileges fact; the
remaining five are the coding default and the coordination policy that a host may
replace. Keep the split explicit in the code so `LoopRules` cannot drift into
policy:

```text
Invariant: line 1 (tool call is the only way to act), line 2 (one result), line 7 (privileges fact)
Invariant: "Stop by replying without a tool call…" (line 9)
Invariant: "Tool errors are data…" (line 8)
Default policy: lines 3–6 (read before edit, run tests, project instructions, refuse/flag)
```

And the delegation paragraph gains the independence criterion (§3.1). The todo
text is unchanged.

### 3.12 Documentation drift found while verifying

- `docs/architecture.md:666-667` ("never reads `<state>/agents`") contradicts
  `:455` and the exported `agent.LoadAgents`. Reword: the *facade* never reads
  it; discovery is a host-facing helper. This is the same drift D0 resolves.
- `docs/architecture.md:1018-1047` describes v2 and must cover v3, identity,
  the composition primitives, and the prompt tag.
- `docs/limits.md:176` names `prompt-v2`; add v3 and the identity/tag caps
  (`:191-193` is the definition cap table).
- `README.md:149-152` documents `Instructions`/`SystemPrompt`; add `Identity`,
  `PromptTag`, and the composition primitives.
- `doc.go:60-67` lists the invariants a replacement must keep; after v3 the
  shipped default actually states all four, and `LoopRules()` makes them
  obtainable without transcription.
- The role table lives only in the frozen plan `docs/plans/subagent-build.md`;
  do not edit it. The current reference and the CHANGELOG record the new
  ownership.

## 4. Decisions

### D0 — Role ownership: move

The five roles move to acode (Phase 2). The SDK keeps the parser, discovery, the
caps, and `DelegationConfig.Agents`. Phase 3 deletes them and `DefaultAgents`
itself; the removals are compile-time, so v0.3.0 ships the whole set in one
release with no coordination window.

### D1 — Prompt ownership: caller owns content, SDK owns contract

The SDK keeps the loop invariants and the prompt contract; identity, coding
rules, role taxonomy, and coordination policy are replaceable defaults.

### D2 — No SDK role defaults: delete `DefaultAgents()`

No fallback role ships. Delete `agent.DefaultAgents()` and
`aharness.DefaultAgents()`. The definition set is always the host's.
`DelegationConfig.Agents` stays required, and the error text points at supplying
a set rather than at a function. The starting template moves to a runnable
`Example` in `example_test.go`.

Rationale is in §1.8: no code path needs it, it violates D0/D1/D7 in miniature,
it re-creates the incident's failure mode, and it would become a frozen surface
the SDK must version forever.

### D3 — Discovery seam

`LoadAgents(base []AgentDefinition, stateDir string) *AgentCatalog`. Validate
and cap the base; emit the standard warnings; `SourceDefault`/`SourceUser`
exported, with `"bundled"` renamed to `"default"`. No `LoadAgentsWith` shim.

### D4 — Composition primitives and the documented path

Three primitives: `agent.LoopRules()`, `agent.AgentCatalogText(agents)`, and
`agent.DefaultPromptBody(version, agents)`. Assemble `renderPrompt` from them;
never strip text. `DefaultPromptBody` is uncapped.

Documented preference is the three tiers in §1.7. **Tier 1 — `Identity` plus
`DefaultPromptBody` — is the recommended path and the one acode uses.** Tier 2
(`LoopRules` plus host policy plus `AgentCatalogText`) exists for non-coding
products. Tier 3 (`SystemPrompt` wholesale) stays supported. Three primitives,
not two, because tier 2 without `AgentCatalogText` forces a host to re-implement
the catalog cap.

### D5 — Identity

`Identity` on the public `Config`, `engine.Spec`, and `agent.Config`; default
`aharness`; 64 bytes, no control characters; `RenderSystemPrompt` gains the
parameter. Not recorded; re-supplied like `UserAgent`.

### D6 — Prompt tag

`PromptTag` separate from `prompt_version`, recorded in `session_start`. Resume:
recorded wins, a conflict is an error. Fork: explicit wins; inherit only when the
prompt is unchanged. No `SupportsPrompt` change.

### D7 — Project instructions

Put the line in the default body's coding-policy group, so the root and every
child see it by default. acode decides for its own body; recommend keeping it
for the reviewer, because that is the role that reviews against repo policy. If
exploration cost shows up in probes, acode can move it into mutating role bodies
with no prompt-version bump.

### D8 — Frozen-body structure

One frozen shared const for v0–v2 plus `harnessRulesV3`, with exact golden pins.
Do not restructure each version into a full frozen body. This resolves the
issue's open decision 4.

### D9 — v3 content

The exact text in §3.11, plus the delegation criterion in §3.1. Keep the todo
text, the privileges fact, and "tool errors are data".

### D10 — Test pins

The SDK ships no role text, so there is nothing to pin there. The frozen prompts
get exact pins. The `Example` definition set must carry an `// Output:` line so
the test runner executes it; a compiled-only example would not catch rot. acode
pins its five descriptions, bodies, and grants.

## 5. Contract deltas

| Kind | Delta | Compatibility (v0.3.0) |
|---|---|---|
| Added | `agent.LoopRules`, `agent.AgentCatalogText`, `agent.DefaultPromptBody` | Additive |
| Added | `aharness.Config.Identity`, `Config.PromptTag`; `engine.Spec.Identity`, `Spec.PromptTag`; `agent.Config.Identity`, `Config.PromptTag` | Additive |
| Added | `session_start.prompt_tag` (optional), `State.PromptTag` | Additive; session schema stays 2 |
| Added | `agent.SourceDefault`, `agent.SourceUser` | Additive |
| Added | `prompt-v3` and `PromptVersionV2` | Additive; v0/v1/v2 frozen |
| Changed | **Breaking:** `agent.LoadAgents(base, stateDir)` | Pre-1.0; CHANGELOG **Changed** + migration |
| Changed | **Breaking:** `agent.RenderSystemPrompt` gains `identity` | Pre-1.0; CHANGELOG **Changed** + migration |
| Removed | **Breaking:** `agent.DefaultAgents` and `aharness.DefaultAgents` | Pre-1.0; CHANGELOG **Removed** + migration: supply the host's set; see the `Example` |
| Changed | `AgentDefinition.Source` value `"bundled"` → `"default"` | Presentation-only; one-line host update |
| Changed | `prompt-v2` is no longer current; new sessions record `prompt-v3` | New sessions only; existing logs replay |
| Fixed | Fork with `Instructions` appends to the recorded prompt | Correctness fix |
| Unchanged | Tool grants and schemas, caps, todo text, toolset version, session schema version | — |

## 6. Build plan

### Phase 0 — none

The cross-repo premise no longer needs confirming: v0.3.0's breaks are
compile-time, so acode's actual usage pattern changes only the size of its
migration, not the SDK's phases.

### Phase 1 — Discovery seam (aharness; Ask 7)

| File | Change |
|---|---|
| `agent/agents.go` | `LoadAgents(base []AgentDefinition, stateDir string)`; validate base entries with `validateAgentDefinition`, putting failures in `Invalid` plus a warning; apply `maxAgents` to the combined set with the existing warning; merge base first, files second; sort once at the end. Replace the source constants with exported `SourceDefault`/`SourceUser`. Leave `bundledAgents()` unchanged in this phase; Phase 3 removes it. |
| `agent/agents_test.go` | Table tests: nil base; base only; base replaced by a same-name file; base invalid entry skipped with a warning; base over `maxAgents` truncated with a warning; file over the cap still warns; `Source` values. Update every `LoadAgents(t.TempDir())` call to `LoadAgents(nil, dir)`. |
| `docs/architecture.md` | Reword `:455` and `:666-667`; describe the base-then-file precedence and the source values. |
| `docs/limits.md` | Note that the 32-agent cap covers the base too. |
| `CHANGELOG.md` | **Changed** (breaking) with the one-line migration. |

This phase is adoptable by acode with `LoadAgents(acodeDefaults(), dir)` before
anything moves, so it is the safe first landing. It is independent of every other
phase.

### Phase 2 — Role content moves to acode (out of this repository)

acode takes Appendix A as its default set, seeds it through the Phase 1 seam,
and keeps operator files winning. acode also owns the reviewer grant decision
(recommended: keep it read-only, with the reworded body), the routing text, and
the pins. aharness does not merge role text in this phase.

Deliverable from aharness: Appendix A, verified for the caps (the byte counts in
§3.2 are exact).

### Phase 3 — Remove all SDK role defaults (aharness)

| File | Change |
|---|---|
| `agent/agents.go` | Delete the five definitions and `bundledAgents()`; delete `DefaultAgents()`. `LoadAgents` takes only the host's base. |
| `delegate.go` | Delete `aharness.DefaultAgents()`; change the `DelegationConfig.Agents` comment (`:29-31`) and the validation error (`:79`) to *"must not be empty; supply the host's definition set"*, with no reference to a deleted function. |
| `aharness.go` | Same error text at `:487`. |
| `agent/agents_test.go` | Replace the five-name assertion (`:129`) and the five-agent counts (`:170, 236, 250`) with a locally defined fixture set; keep the cap, precedence, duplicate, and invalid-file tests by seeding them through `base`. |
| `example_test.go` | Add `Example_delegation`: a two-role definition set, `DelegationConfig`, and a `delegate` call against a scripted model. It must carry an `// Output:` line so the test runner executes it; without one it is compiled but not run, and the anti-rot property is lost. Feasible in `package aharness_test`: every delegation test already scripts a model there (`acceptance_test.go`, `delegation_test.go`, `subagent_children_test.go`), so the pattern is proven. This is the starting template that replaces `DefaultAgents`. |
| Docs | `docs/architecture.md` (`:444, :455, :666, :690`), `README.md`, `CHANGELOG.md` **Removed** with a migration note pointing at the new example. |

Nothing in the SDK may reproduce acode's five role texts, in any form, including a
"generic" role that happens to carry one of their descriptions.

### Phase 4 — Composition, identity, tag, fork fix (aharness; Asks 2–5)

| File | Change |
|---|---|
| `agent/prompt.go` | Split `renderPrompt` into `identityLine` and `DefaultPromptBody`; add `LoopRules` and `AgentCatalogText`; export all three; add `identity` to `RenderSystemPrompt`; use identity in `childSystemPrompt`, which also takes it. Keep `capText` out of `DefaultPromptBody`. |
| `agent/types.go` | `Config.Identity`, `Config.PromptTag`. |
| `agent/events.go`, `agent/state.go` | `session_start.prompt_tag`; `State.PromptTag`; `SupportsPrompt` unchanged here. |
| `agent/runner.go` | Write `cfg.PromptTag` in `WriteSessionStart`; leave `context.go:387`'s pre-field replay on the `aharness` default — that log predates the recorded prompt, so the default is the faithful replay. Document it. |
| `engine/engine.go` | `Spec.Identity`, `Spec.PromptTag`; validation; thread identity through every `RenderSystemPrompt` call (`:362, :485, :498, :564`) and into `agent.Config` (`:240-258, :379-398`); resolve and record the tag; **fix** the fork `AppendPromptSet` branch (`:497-503`) to append to `state.SystemPrompt`. |
| `engine/runner.go` | Pass `h.spec.Identity` in the run config (`:158-183`). |
| `agent/spawn.go` | Identity flows through `parent.Identity` (`:756-780`); no other signature change. |
| `aharness.go` | `Config.Identity`, `Config.PromptTag`; validation and pass-through; update the `Fork` doc (`:109-111`); point the `SystemPrompt` doc at the composition primitives. |
| `doc.go`, `README.md` | The §"Replacing the system prompt" story: `LoopRules` + your rules + `AgentCatalogText`, or `DefaultPromptBody`; identity; tag. |

Tests: `TestLoopRules` (contains the four invariants and the privileges fact;
contains no coding policy); `TestDefaultPromptBody` (equals `renderPrompt` minus
the identity line for every version; unknown version errors; not capped);
`TestAgentCatalogText` (cap and omission marker); `TestDefaultIdentityRendersFrozen`;
`TestChildSystemPromptIdentity`; a facade composition test that builds a
prompt from `LoopRules` + host rules + `AgentCatalogText` with identity `acode`,
asserts every invariant is present and the literal `aharness` is absent — the
acceptance's "no transcribed invariant" check; `TestForkInstructionsAppendToRecordedPrompt`;
the tag tests (`TestPromptTagRecordedAndInherited`, `TestResumeRejectsPromptTagChange`,
`TestForkPromptChangeDropsInheritedTag`, `TestForkWithoutOverridesKeepsTag`);
`config_test.go` validation table.

### Phase 5 — prompt-v3 default body (aharness; Ask 6)

P1, not cosmetic: under D4 tier 1 this text is acode's production parent and child
prompt. It is also the SDK's default for any host that does not replace it.

| File | Change |
|---|---|
| `agent/prompt.go` | `PromptVersionV2 = "prompt-v2"`, `PromptVersion = "prompt-v3"`; rename the shared frozen const to `harnessRulesFrozen` (v0–v2); add `harnessRulesV3`; add the explicit v2 case and the v3 case; update the delegation paragraph for v3 only; select child rules and todo by an explicit switch including v2 and v3; split `LoopRules` from the coding-policy lines so the invariant set cannot drift. |
| `agent/state.go` | `SupportsPrompt` adds `PromptVersionV2`. |
| `agent/prompt_test.go` | v2 assertions move to `PromptVersionV2`; v3 assertions for all nine rules bullets and the delegation criterion; `TestRenderPromptFrozen` exact pins for v0/v1/v2; `TestProjectPromptV2Replay`. |
| `agent/spawn_test.go` | v2 parent keeps v2 child rules and todo guidance; v3 parent gets v3; v0/v1 keep frozen rules. |
| Docs | `docs/architecture.md` §4.3; `docs/limits.md`; `CHANGELOG.md`. |

acode uses tier 1 (§1.7), so it inherits this text and does not need a separate
handoff. If acode later moves to tier 2, it takes the §3.1 criterion and §3.11
policy lines with it.

### Phase 6 — Verification and release

Run the automated gates; the owner runs the manual probes (§7.2) before the tag.
Then move this plan to `docs/plans/`, index it, mark it `implemented` with a
deviations section recording the probe outcome or the exact pending check, and
cut the CHANGELOG.

## 7. Verification

### 7.1 Automated

- `go test -race -count=1 ./...`.
- `go vet ./...`, `go build -trimpath ./...`, `make fmt-check`, `make lint`,
  `make vuln`, `make docs-check`, or `make dod`. Record any gate that was not run;
  do not claim it.
- `FuzzParseAgentDefinition` — unaffected by Phase 1–5, but run it because
  `agents.go` changes. The new base-validation path is a trust boundary: add
  seeds with an invalid base definition and a base over `maxAgents`.
- Cross-repo: none required. v0.3.0's breaks are compile-time; acode's migration
  is validated by its own build, not by a gate in this repository.

### 7.2 Real-model component probes

The evaluation research's L1 layer requires real-model probes for prompt
changes, with raw traces read, at least once before merge
(`docs/research/evaluation-research.md:703-705`). The issue's "10–12" is the
example suite at `:598-611`, not a count for these behaviors; name the probes:

1. **Review-before-done.** Fixture: a small repo with one documented behavior and
   a three-file change. Prompt: "make this change and finish it", with no cost
   hint, no mention of review, no delegation instruction. Pass: the parent
   delegates to the review role before reporting done, and the review precedes
   the final report. Score the ordering, not the prose.
2. **Verified vs unverified.** Fixture: one check that cannot run in the
   workspace (an external service) and one that can. Pass: the final report
   separates verified from unverified and does not claim the external check.

**Execution: manual, by the owner.** The probes are run by hand after the code
lands and before the release is tagged. This plan does not build probe harness
infrastructure, and no automated gate depends on them. The harness is a fixture
repo, a model configuration, and a transcript read; the value is in the trace
reading, not the harness.

Both probes need the same discipline:

1. **Hold the model constant.** Use one model and effort across both arms; a
   model change confounds routing with capability.
2. **Keep the fixture and commit fixed.** Change only the prompt composition
   between the baseline and the variant.
3. **Record what is needed to reproduce:** model, effort, fixture commit, arm,
   and the trace path.
4. **Read the trace for the mechanism, not the prose.** For probe 1 the question
   is whether a review was delegated *before* the final report. For probe 2 it is
   whether a claim the workspace could not verify was reported as unverified.

Report the outcome as what it is: a smoke check on a small sample. A shift from
"never delegated" to "delegated every time" over a handful of runs shows the
mechanism moved; it is not a rate and should not be written as one. If the probes
are not run before the tag, record the exact pending check rather than implying
it passed.

## 8. Acceptance mapping

| Requirement | Owner | Phase |
|---|---|---|
| The five descriptions ≤200 bytes (193/191/178/189/145), pinned | acode | 2 |
| The SDK ships no role text and no `DefaultAgents`; the template is a runnable example | aharness | 3 |
| `prompt_test.go` pins v3; v0/v1/v2 render frozen | aharness | 5 |
| A session opened before the role-text change replays its recorded child prompts | aharness (already true; regression test) | 3 |
| A host composes the contract with its own identity, no transcribed invariant | aharness | 4 |
| A host can install its own default definitions with operator files still winning | aharness | 1, 2 |
| A host tag records alongside the SDK template version; `SupportsPrompt` still gates replay | aharness | 4 |
| Real-model probes, traces raw | owner (manual, before the tag) | 6 |

## 9. Deferred and non-goals

- **Moving the definition parser and file discovery to acode.** Considered under
  D0 and deferred: the parser is fuzz-tested and reusable, and the seam keeps it
  useful. Revisit if acode needs a different file format.
- **Per-role model or effort tuning.** Already exists (`AgentOverride`,
  definition fields).
- **Recording `Identity` in `session_start`.** Deferred; `UserAgent` is the
  precedent for re-supply.
- **Project-instruction discovery tooling.** Prompt-only; no new tool, no
  workspace scan in the SDK.
- **`todo` mechanics moving into the prompt.** They are already in the schema;
  do not duplicate them.
- **Session schema bump.** None. Optional fields only.
- **Toolset changes.** None.
- **A per-section prompt struct.** Three functions are enough; revisit only if a
  host needs a fourth section.
- **Shipping any role text, including a "generic" default.** Under D2 the SDK
  owns no role taxonomy. A fallback agent that is good enough becomes a
  production role nobody chose, which is how the incident started.

## 10. Resolution record

Every SDK-side question is closed. Two items remain in acode's hands, and neither
holds up this release. The owner runs the real-model probes manually (§7.2).

**Resolved — role defaults: none.** `DefaultAgents()` is deleted in both
packages (D2, §1.8). Reasons: no code path needs it, `DelegationConfig.Agents` is
already required, it contradicts D0/D1 at once, and it would become a frozen
surface. The starting template is a runnable `Example` in `example_test.go`.

**Resolved — composition: tier 1 is primary.** A host supplies `Identity` and
inherits `DefaultPromptBody`; it escalates to `LoopRules` + `AgentCatalogText` +
its own policy only when the product is not a coding agent; `SystemPrompt`
wholesale remains supported. Consequence: **acode uses tier 1**, so Phase 5's v3
text is production prompt text, and Ask 6 is P1.

**Resolved — three primitives, not two.** Tier 2 without `AgentCatalogText`
forces a host to re-implement the 4 KiB cap and the omission marker.

**Resolved — the field name is `Identity`.** `HarnessName` would read as the
SDK's own name, which is the opposite of the intent. Document it as the host
identity that parallels `UserAgent`, and recommend the two share a name
(`acode` / `acode/1.2.0`).

**Resolved — release model: v0.3.0, one release, no coordination window.** All
three breaks are compile-time, so acode bumps and fixes what the compiler flags.
Phases 1–3 need no cross-repo gate and can land together.

**Required in the CHANGELOG — one migration footgun.** Passing
`nil` to the new `LoadAgents` is the minimal compiler-driven fix, and it silently
drops the former bundled set when operator files exist. The CHANGELOG migration
step must state: *nil means no host defaults; if you relied on the bundled set,
supply your own.* Without operator files the facade's empty-`Agents` error
catches it loudly, so no code guard is needed.

**Resolved — probe resourcing: manual, run by the owner.** No probe harness is
built in this repository and no automated gate depends on the probes. §7.2 gives
the fixture, the pass condition, and the discipline; the owner runs them after
the code lands and before the tag. A small sample is reported as a smoke check,
never as a rate; if the probes are not run, the exact pending check is recorded
rather than implied to have passed.

**Open in acode, not here — two decisions the SDK does not make.** First, the
reviewer grant: this plan recommends keeping `read_file, search` and rewording
the body (§3.5), but D1 makes role content acode's call. Second, which
composition tier acode picks: tier 1 is recommended and assumed by Phase 5, but
tier 2 is supported and non-blocking. Neither holds up this release, and neither
changes any aharness phase.

## 11. Evidence index

- Bundled roles and opt-in: `agent/agents.go:411-450`; `delegate.go:29-31, 40-59,
  79-137`; `aharness.go:487`.
- Host definitions reaching the catalog: `aharness.go:210, 461-480`;
  `engine/runner.go:43-64, 193-243`.
- Discovery seam: `agent/agents.go:306-366`; cap only in the file loop
  `:356-360`; sources `:29-33, 45-46`; `ApplyAgentOverrides`
  `agent/overrides.go:119-129`.
- Tool mechanics already in schemas: `tools/schemas.go:184-213`;
  architecture rule `docs/architecture.md:1043`.
- Loop invariants: `doc.go:60-67`.
- Catalog: `agent/agents.go:108-120`; `agent/prompt.go:97-107`;
  `tools/schemas.go:232-233`; `tools/delegate.go:226-241`.
- Prompt versions and text: `agent/prompt.go:15-19, 30-40, 54-55, 72-92,
  113-120, 146-157`; `agent/state.go:342-345`.
- Child grants and concurrency: `agent/spawn.go:283-297, 332, 756-780`.
- Fork inheritance: `engine/engine.go:483-503, 559-573`;
  `aharness.go:64-70, 109-111`.
- Prompt-version gate: `engine/engine.go:209-214, 301-303, 339-341`.
- Session fields: `agent/events.go:16-47`; `agent/state.go:113-147`;
  additive precedent `docs/architecture.md:1397-1404`.
- Identity and version: `aharness.go:202`; `version.go:6-44`.
- Read-only decisions: `docs/plans/subagent-build.md:463-467`;
  `docs/research/subagent-research.md:362, 375, 552`.
- Prompts as caller-owned: `docs/plans/go-library-build.md:65-67, 1146-1152`.
- Probes: `docs/research/evaluation-research.md:598-611, 703-705`.
- Existing pins: `agent/agents_test.go:115-145`;
  `agent/prompt_test.go:41-63, 107-140, 227-252`.

## 12. Implementation record (2026-11-19)

**Landed in this repository:** Phase 1 (discovery seam), Phase 3 (role
defaults removed; runnable `Example_delegation`), Phase 4a (composition
primitives and identity), Phase 4b (prompt tag and the fork `Instructions`
fix), and Phase 5 (prompt-v3). Phase 2 remains acode's; Appendix A is the
handoff artifact and no role text is in the SDK.

**Deviations from the build plan.** None change a contract delta in §5.

1. Phase 4 was implemented and reviewed in two parts: 4a (primitives,
   identity, renderer and child prompt) and 4b (prompt tag, fork append fix).
   The split was for reviewability; the exported surface is the one in §5.
2. `LoopRules()` was introduced with the §3.11 invariant wording in Phase 4,
   because it is a new version-independent export, not a frozen body. The
   v0/v1/v2 bodies are unchanged and pinned by exact SHA-256 in
   `TestRenderPromptFrozen`.
3. Base duplicate names in `LoadAgents` resolve first-valid-wins, matching the
   existing "first file wins" rule. The plan did not specify the base case.
4. A delegated child session records no `prompt_tag`. A child's recorded prompt
   is its role body, not the host prompt template the tag describes; the plan
   implied this but did not state it.
5. Resuming a log that records no tag with an explicit tag is a `ConfigError`;
   the recorded (empty) value wins and changing it requires `Fork`, per D6.
6. Identity validation rejects Unicode control characters (C1) as well as C0
   and DEL. The plan said "other control characters"; the stricter reading was
   used.
7. `docs/limits.md`'s `MaxSubagents` row was corrected from "0 unlimited" to
   "8 default; -1 unlimited" while auditing the delegation docs. Pre-existing
   documentation drift, not a code change.
8. A `FuzzLoadAgentsBase` target covers the Phase 1 base trust boundary,
   including an over-cap base; deterministic tests already covered both cases.

**Automated gates run on the final tree.**

- `go build ./...`: pass
- `go vet ./...`: pass
- `go test -race -count=1 ./...`: pass (all packages)
- `make fmt-check`: pass
- `make lint` (`golangci-lint run`): pass, 0 issues
- `make vuln` (`govulncheck ./...`): pass, no vulnerabilities
- `make docs-check`: pass
- `make dod` (docs-check, fmt-check, vet, lint, test, build, vuln): pass
- `FuzzParseAgentDefinition` and `FuzzLoadAgentsBase`: seeds pass; a five-second
  fuzz run of each found no failing input

These are the §7.1 automated gates. The cross-repo gate remains unnecessary:
all three breaks are compile-time, so acode's migration is validated by its own
build.

**Release status.** The CHANGELOG entries for this work live under
`## [Unreleased]`. The `v0.3.0` heading and tag are cut by the owner after the
§7.2 probes, so the release notes and the probes land together. The plan index
records the work as implemented with the probes pending.

**Pending: §7.2 real-model component probes (owner, manual, before the tag).**
The exact pending checks are probe 1 (review-before-done: a three-file change
with no cost hint must produce a delegated review before the final report, and
the review must precede that report) and probe 2 (verified vs unverified: a
final report must separate a check the workspace could run from one it could
not, and must not claim the external check). Hold the model and fixture commit
constant, record model/effort/fixture/arm/trace path, and read the trace for
the mechanism. These probes were not run during implementation, and this record
does not imply they passed.

## Appendix A — role content handed to acode

Delivered as acode's default definitions. Byte counts for the descriptions are
exact and within the SDK's 200-byte cap; bodies are within 16 KiB; all are
single-line descriptions.

| Agent | Tool grant | Description (bytes) |
|---|---|---|
| `explore` | `read_file`, `search` | Read-only lookup: where something lives, how a mechanism works, which files a change touches. Call it when you need the conclusion, not the reads; it reports with file and line and cannot edit. (193) |
| `implementer` | `read_file`, `edit_file`, `search`, `run_command` | Implementation of one self-contained change whose full spec fits in a prompt. It reads, edits, runs the tests, and reports what changed and what it could not verify. Keep cascading work here. (191) |
| `researcher` | `read_file`, `search`, `web_search`, `web_fetch` | Answers questions needing both repository and web evidence. Call it when sources must be found and weighed; claims come back cited (file and line, or URL), with inference marked. (178) |
| `reviewer` | `read_file`, `search` | Independent review of a change its author has read and believes correct. Call it before finishing contract, documented-behavior, or multi-file changes. It returns defects, not confirmation. (189) |
| `task` | every tool except `delegate` | Self-contained multi-step work needing reads, edits, and commands: the fallback when no narrower role fits. It can do everything except delegate. (145) |

Bodies:

```text
explore: Explore the assigned area and report what you found. Lead with the
answer, cite file and line for every claim, and include only the facts the
caller needs to act. When the evidence does not settle the question, say what
is missing rather than guessing. Your tools are read-only: if a change is
needed, describe it precisely instead of making it.
```

```text
implementer: Implement the assigned change exactly as specified, keeping it
minimal and focused. Read the surrounding code before editing, and run the
relevant tests after. When the spec is ambiguous or contradicts the code,
follow the most reasonable interpretation and record the choice in your
report rather than choosing silently. Report what changed, which commands you
ran and their outcomes, and anything you could not verify.
```

```text
researcher: Research the assigned question using the workspace and the web.
Cite every claim with a file and line or a URL, and note a source's date when
recency matters. Separate what you verified from what you inferred, and say
plainly when the evidence is insufficient. Prefer current sources; name the
reason when you rely on an older one.
```

```text
reviewer: Review the assigned change as a skeptic: the author has read it and
believes it is correct, so your value is disagreement, not agreement. Lead
with concrete findings, each cited to file and line. Check first what the
author claims is fixed, verified, or covered by tests — check each claim
against the code and the tests, and say plainly what you could not settle,
because confident wrongness is the defect a self-check cannot catch. Then look
for behavior regressions, unstated behavior changes, contract drift between the
change and its documentation, and tests that now pass vacuously. Report defects
in the assigned change, not improvement ideas for the rest of the codebase. Say
plainly when you find nothing, and end with what you covered and what you did
not look at, so an empty finding is a scoped claim rather than a guess.
```

```text
task: Carry out the assigned task end to end. Read before editing, keep changes
minimal, and run the relevant tests. When the spec is ambiguous, follow the most
reasonable interpretation and record the choice in your report rather than
choosing silently. Report what you changed, what you verified, and what
remains.
```

acode should also carry the delegation criterion from §3.1 in whatever prompt
body it composes, and decide the reviewer grant per §3.5.
