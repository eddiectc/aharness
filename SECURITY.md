# aharness security policy

- **Applies to:** the `github.com/eddiectc/aharness` module. The
  [acode](https://github.com/eddiectc/acode) host reports in its own repository.
- **Related:** [architecture](docs/architecture.md), [limits](docs/limits.md),
  [coding standard](docs/coding-standard.md)

What this repository treats as a vulnerability, what it records as a deliberate
non-goal, which version gets a fix, and how to report one. Read it before filing
a security report.

## 1. In scope

A report is in scope when a claim below stops holding for input the model can
reach. The linked contract is the owner; a difference between it and the code is
a bug even when the code looks deliberate.

| Surface | Code and contract | Report it when |
|---|---|---|
| `web_fetch` address guard and origin allowlist | `tools/web_fetch_addr.go`, `tools/web_origin.go`, `tools/web_fetch_transport.go`; [tools.md §5.2](docs/architecture/tools.md#52-shared-rules-and-builtin-contracts) | A metadata, link-local, unspecified, multicast, documentation, benchmarking, reserved, or protocol-assignment address is fetched; a loopback, private, or shared address is fetched without its exact origin in `WebFetchConfig.AllowedOrigins`; a redirect hop is followed without revalidation; or a body, header, or URL over its cap is accepted. |
| Command process cleanup | `tools/command.go`, `tools/command_unix.go`; [limits.md §4](docs/limits.md#4-tools) | A `run_command` child or grandchild outlives a timeout, a cancellation, or an interrupt. |
| Child environment | `tools/command.go` (`commandEnv`, `envDenied`); [limits.md §4](docs/limits.md#4-tools) | A name ending `_TOKEN`, `_SECRET`, `_KEY`, or `_PASSWORD` (case-insensitive), or any variable outside the documented allowlist, reaches a child process. |
| Sanitization, redaction, and caps | `tools/output.go`, `tools/web_extract.go`, `tools/web_search.go`, `agent/events.go`, `model/http.go`, `session/store.go`; [limits.md §2](docs/limits.md#2-provider-transport-and-streaming), [limits.md §4](docs/limits.md#4-tools), [tools.md §5.2](docs/architecture/tools.md#52-shared-rules-and-builtin-contracts) | Invalid UTF-8, or a payload over its cap, reaches the model, the transcript, or the session log; fetched or searched text keeps an ANSI escape or control character; or a provider error body is recorded unsanitized. |
| Workspace containment for file tools | `tools/root.go`, `tools/registry.go` (`workspaceRel`), `tools/read.go`, `tools/search.go`, `tools/edit.go`; [tools.md §5.2](docs/architecture/tools.md#52-shared-rules-and-builtin-contracts) | `read_file`, `read_image`, `search`, or `edit_file` accepts a path with `..`, an absolute name, a symlink escape, or a `.git` component that the contract says is rejected. |
| Vulnerable dependency or pinned tool | `Makefile` (`make vuln`), `.github/dependabot.yml`; [coding standard §3](docs/coding-standard.md#3-dependencies), [ci-release.md §1.3](docs/ci-release.md#13-tool-pinning) | A reachable vulnerability in a module dependency, or in a tool or action the workflows pin, that the latest commit still carries. |

## 2. Not in scope

These are recorded design decisions, not oversights. A report that lands in one
is closed with a pointer to the matching item.

- **No sandbox and no process confinement.** `run_command` runs with the host
  process's privileges and has no `Path`; `os.Root` containment for the file
  tools is policy, not isolation, so bind mounts, `/proc`, and device files stay
  reachable. Run without credentials and use external isolation - a container, a
  VM, or `bwrap` - when the model is untrusted. See
  [facade.md](docs/architecture/facade.md#321-public-library-facade) and
  [tools.md §5.2](docs/architecture/tools.md#52-shared-rules-and-builtin-contracts).
  A write outside the workspace by absolute path or through the shell, a read
  through the shell of any file the host process can open, and a secret a host
  expected a path deny-list to protect are all out of scope.
- **`.git` rejection is an accident guard.** The edit tool refuses any path with
  a `.git` component so a stray write does not corrupt the repository; the shell
  can still edit `.git/config` by absolute name.
  [tools.md §5.2](docs/architecture/tools.md#52-shared-rules-and-builtin-contracts).
- **The session log is not a tamper-proof audit trail.** It is a debugging and
  resume artifact; a same-UID process can edit it, and separating it from the
  shell needs a UID or process boundary the SDK does not create.
  [session.md §7](docs/architecture/session.md#7-session-log).
- **Process-group escape is out of scope.** Timeout, cancellation, and interrupt
  kill the whole process group, and the work directory cleanup is attempted on
  the normal, error, and caught-signal paths, but a child that detaches into a
  new session escapes the group, and nothing survives a host the kernel kills
  with `SIGKILL` or a power loss. A work directory that cannot be removed is
  reported for inspection instead of deleted.
  [limits.md §4](docs/limits.md#4-tools),
  [failures.md §8](docs/architecture/failures.md#8-failure-handling).
- **Resource exhaustion is bounded by host caps, not the SDK.** The session log
  has no rotation or retention policy and a child process has no CPU or memory
  cap; set the documented limits and isolate the process when that matters.
  [limits.md](docs/limits.md) §3, §5.

## 3. Supported versions

The newest tagged release is the only supported version. There are no
maintenance branches before `v1.0.0`: a fix lands on `main`, ships in the next
release, and reaches an older tag only by upgrading.

```sh
go list -m github.com/eddiectc/aharness
```

## 4. How to report

Use GitHub's private advisory form; it is the only channel for a security
report:

https://github.com/eddiectc/aharness/security/advisories/new

Include the in-scope row it matches, the tag or `Version()` output, the tool and
its arguments, and a minimal reproduction: the smallest prompt, workspace, or
host configuration that shows the failure. Say what you did not verify. Do not
paste a live credential or token, and rotate one already exposed. Do not open a
public issue for a suspected vulnerability; a non-security bug uses the
[bug form](https://github.com/eddiectc/aharness/issues/new?template=bug.yml).

## 5. What to expect

- One maintainer reads every report. There is no guaranteed response time and no
  guaranteed fix; the project is pre-1.0 and has no security team.
- A report that matches [§2](#2-not-in-scope) is closed with a pointer to the
  matching item.
- A confirmed report the maintainer can fix lands on `main` with a published
  advisory; if no fix is possible, the advisory records the limitation. State in
  the report whether you want to be credited.
