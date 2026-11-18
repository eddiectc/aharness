# Changelog

All notable changes to this module are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html). Before `v1.0.0`, a
minor version may contain a breaking change to the public surface; those
changes are called out under **Changed** and **Removed**.

## [Unreleased]

### Added

- `Config.AgentName` names the root agent. It is recorded in
  `session_start.agent`, passed to tool policy and tool calls, inherited by
  `Resume` and `Fork` when omitted, and exposed as `State.AgentName`.
- `engine.Spec.UserAgent` is now required host identity. When
  `Spec.ToolOptions.UserAgent` is empty the engine fills it from the spec; a
  differing value is a `ConfigError` naming `ToolOptions.UserAgent`.

### Changed

- Direct `engine` hosts must now set `Spec.UserAgent`; `engine.Open` rejects an
  empty identity with a `ConfigError`. The model completer closure still
  captures the same identity independently because the engine cannot inspect
  its outbound headers.

## [v0.1.0] - 2026-11-18

### Changed

- Outbound `User-Agent` values are now required host input and are sent
  verbatim. `Config.UserAgent`, `OpenAIModelOptions.UserAgent`, and
  `CatalogConfig.UserAgent` are required; the model adapters and catalog source
  reject a missing identity. The SDK no longer defaults any request to
  `aharness/<version>`; web tools return `unavailable` when the host supplies no
  identity.
- `model.NewOpenAIChat`, `model.NewOpenAIChatWithClient`,
  `model.NewOpenAIResponses`, and `model.NewOpenAIResponsesWithClient` now take
  the full `User-Agent` identity instead of a version string. A request without
  one fails with `model.ErrUserAgentRequired`.

## [v0.0.1] - 2026-11-18

The first tagged release of the aharness SDK. The module is a library: it ships
no binary, CLI, or TUI, and `go.mod` is unchanged by the release.

### Added

- Root facade `github.com/eddiectc/aharness`: `Config`, `Agent`,
  `New`/`Resume`/`Fork`, blocking `Run` and streaming `Start`, public events and
  results, model DTOs and adapters, catalog discovery, tool and delegation
  configuration, state inspection, and typed errors. The facade copies DTOs at
  the component boundary.
- Component packages `agent`, `engine`, `model`, `tools`, and `session` for
  hosts that need the runner, composition layer, canonical model and dialect
  adapters, tool registry, or durable store directly.
- Built-in tools behind one validated registry with `os.Root` workspace
  containment: `read_file`, `edit_file`, `search`, `run_command`, `web_search`,
  `web_fetch`, and `todo`, plus `delegate` when delegation is configured.
  Caller tools via `ToolsConfig.Extra` and policy via `Config.Authorize`.
- Delegation with depth one: `AgentDefinition`, `DelegationConfig`, and
  `DefaultAgents`, with child grants intersected with the parent's authority and
  `ConcurrencySafe` read-only fan-out.
- Durable v2 JSONL session log with locking, torn-line repair, replay, fork
  copy, and lock-free inspection and listing. `SchemaVersion` is `2` and is
  independent of the module version. Every event carries `harness_version`,
  recorded from `Version`.
- OpenAI-compatible Chat Completions and Responses adapters with buffered and
  SSE streaming transport, `CustomModel` for caller implementations, and
  models.dev catalog discovery with an ETag-validated cache.
- MIT license.

### Notes

- Requires Go 1.27.3 or later, matching `go.mod`.
- `Version` reports the version resolved for this module in the consuming
  build, so an embedded SDK reports its own release rather than the host
  program's version. It returns `"devel"` for an untagged or filesystem-replaced
  build, and reads build information only; it performs no I/O or network access.
- The first-party coding agent, [acode](https://github.com/eddiectc/acode), is a
  separate module and is not part of this release.
- The recorded decision was `v0.1.0` for the first tag. This release uses
  `v0.0.1` to signal a preview of the facade; the public surface may still
  change before a stable release.
- Real-provider smokes remain a release prerequisite for a stable release; the
  automated suite runs against fake providers.

[v0.1.0]: https://github.com/eddiectc/aharness/compare/v0.0.1...v0.1.0
[v0.0.1]: https://github.com/eddiectc/aharness/releases/tag/v0.0.1
