# aharness

aharness is a Go SDK for building a coding agent over a single workspace. It
runs a model/tool loop against the files and commands the caller grants, appends
every event to a durable JSONL session log, and ends a run with a final answer
or a typed stop reason. The loop is bounded by the model context window,
termination policies, and errors; turn and token budgets are opt-in and
unlimited by default.

The SDK is headless. It adds no CLI, TUI, environment, config-file, TTY, or
signal layer. [acode](https://github.com/eddiectc/acode) is the first-party
coding agent built on these packages; embedders can use the root facade
directly.

## Two levels of API

- **Root facade:** `github.com/eddiectc/aharness` exposes `Config`, `Agent`,
  `New`/`Resume`/`Fork`, `Run`/`Start`, public events and results, model
  adapters, catalog discovery, tool configuration, delegation, and typed errors.
  This is the recommended embedding surface.
- **Component packages:** `agent`, `engine`, `model`, `tools`, and `session`
  expose the lower-level runner, composition layer, canonical model/adapter,
  tool registry, and durable store. They are public for first-party hosts and
  advanced integrations that need display events, history, session inspection,
  or direct adapter wiring.

## Use as a Go library

The root package is the high-level, embeddable form of the loop: durable
sessions, builtin and caller tools, model adapters, delegation, events, and
typed results. The recorded contracts, including the public facade surface, are
in [architecture.md §3.2.1](docs/architecture/facade.md#321-public-library-facade).

This module requires Go 1.27.4 or later. Add it to an existing module; it ships
no binary and needs no separate installation:

```sh
go get github.com/eddiectc/aharness@latest
```

Release history is in [CHANGELOG.md](CHANGELOG.md).

### Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/eddiectc/aharness"
)

type myModel struct{}

func (myModel) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
	return aharness.Response{
		Text:         "done",
		FinishReason: aharness.FinishStop,
		Usage:        aharness.Usage{InputTokens: 10, OutputTokens: 2, Reported: true},
	}, nil
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: "/repo",
		StateDir:  filepath.Join(home, ".aharness"),
		UserAgent: "my-product/1.0.0",
		Model: aharness.CustomModel(aharness.ModelSpec{
			ID:            "my-model",
			Provider:      "my-provider",
			Dialect:       aharness.DialectCustom,
			ContextWindow: 128_000,
		}, myModel{}),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = ag.Close() }()

	result, err := ag.Run(context.Background(), "Fix the failing test", aharness.RunOptions{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.FinalText)
}
```

`New` creates a session, `Resume` opens one for append, and `Fork` copies one
into a new self-contained log; `Resume` rejects a changed prompt or tool
contract, and `Fork` is the supported path for a contract change.
`InspectSession` reads a session without taking its exclusive lock. The full
surface, field-level rules, and compatibility rules are in
[architecture §3.2.1](docs/architecture/facade.md#321-public-library-facade);
`example_test.go` holds the runnable examples.

### What the SDK provides

- **Runs and results.** `Run` is the blocking convenience and returns an
  authoritative `Result`. `Status` is the typed stop reason and `Err` is its
  cause and the error `Run` returns, so `errors.Is` and `errors.As` work.
  `RunOptions.OnEvent` observes the same events without switching to `Start`.
  See [architecture §4](docs/architecture/loop.md).
- **Observation.** `Start` returns a `*Run` whose `Events()` iterator streams
  text and reasoning deltas, tool start and completion, notices, recorded
  messages, delegated-child progress, and the terminal `RunFinished`. Events
  are display-only; the session log is the durable record. `ViewEvent` projects
  an event to flat JSON with a `kind` discriminator, and `Transcript` and
  `Agent.Transcript` read a display transcript from a byte snapshot without the
  session lock, so a running session stays readable. See
  [architecture §3.3](docs/architecture/packages.md#33-host-observation-contracts).
- **Models and identity.** `OpenAIModel` builds a Chat Completions or Responses
  adapter for an explicit endpoint, `DiscoverModel` and `ListModels` resolve
  models and limits from the [models.dev](https://models.dev) catalog, and
  `CustomModel` wraps any `Model`; implement the optional `StreamingModel` to
  emit deltas. `Config.UserAgent`, `OpenAIModelOptions.UserAgent`, and
  `CatalogConfig.UserAgent` are required when that caller runs: the SDK sends
  the caller's identity and never substitutes its own. A `CatalogConfig` is
  validated only when `DiscoverModel` or `ListModels` is called, and a direct
  `engine` host must set `engine.Spec.UserAgent`. See
  [architecture §2](docs/architecture/provider.md).
- **Budgets and prompt.** `Config.MaxTurns` and `MaxTotalTokens` bound the
  agent's cumulative turn and token usage; `RunTimeout` and `ModelTimeout`
  bound one run and one model attempt, and a zero value is unlimited.
  `Config.Limits` caps one model call, and an unset output budget omits
  `max_tokens`/`max_output_tokens` so the model's advertised ceiling is never
  sent. Prompt composition has three tiers: `Identity` plus the default body, a
  body built from `agent.LoopRules`, your own policy lines, and
  `agent.AgentCatalogText`, or `Config.SystemPrompt` wholesale;
  `Config.Instructions` composes host text into the resolved prompt. The prompt
  is versioned and recorded, and a wholesale replacement must keep the
  invariants `agent.LoopRules` returns. See
  [architecture §4.3](docs/architecture/loop.md#43-system-prompt) and
  [limits.md](docs/limits.md).
- **Delegation.** `DelegationConfig.Agents` is required: the SDK ships no role
  definitions, and `Example_delegation` in `example_test.go` is the starting
  template. `ObserveChild` opts into a live per-child view. Depth is one. See
  [architecture §3.2.1](docs/architecture/facade.md#321-public-library-facade).

### Tools and policy

The SDK ships a validated builtin tool registry with `os.Root` workspace
containment, plus `delegate` when delegation is configured. The tools, their
parameters, and their behavior are enumerated in
[architecture §5](docs/architecture/tools.md#5-tools); the caps and timeouts
are in [limits.md](docs/limits.md).

`ToolsConfig.Disable` removes a builtin, `Extra` registers caller tools, and
`RootGrant` narrows what the root is offered and may call without changing the
session grant. `Config.Authorize` runs before every tool execution, including
delegated children; returning `&aharness.Deny{}` or another non-cancellation
error produces a model-visible `permission_denied` result.

Authorization is policy, not a sandbox. Caller tools run with the host
process's privileges, `run_command` is not confined, and OS isolation is the
host's responsibility.

Tool calls within one run execute sequentially in arrival order, which is also
the durable log, replay, authorization, and stuck-detection order. Independent
work belongs in one batch tool that fans out internally, or in delegated
children whose grants are read-only or declare `ConcurrencySafe`.

Host integration patterns — a state directory inside the workspace, large
child reports, and tool-call context — are in
[architecture §3.2.1](docs/architecture/facade.md#321-public-library-facade).

### Scope of the library

aharness is a coding-agent SDK, not a general agent development kit. It adds
no provider matrix, graph or workflow orchestration, memory or RAG, pluggable
session store, background or nested delegation, structured-output DTOs, prompt
templating, or public TUI package. A change that alters a recorded
contract—session schema, prompt, tool grant, projection, or concurrency
model—is a different product, not an extension. The recorded boundary is in
[architecture](docs/architecture.md#recorded-boundary).

The tool surface follows the same rule: a builtin is added only when the SDK
must understand the tool to keep the loop's guarantees — result pairing,
projection, caps, model capability handling, and replay. A tool that needs an
environment, a browser, a service, approval, or domain knowledge belongs to the
host through `ToolsConfig.Extra`; rendering and domain tools are host tools.
The test is in [architecture §5.1](docs/architecture/tools.md#51-where-a-tool-belongs).

## Component packages

The component packages are the SDK's lower-level contracts, documented in
[architecture §3](docs/architecture/packages.md#3-packages-and-contracts).
Prefer the root facade unless the host needs one of these contracts; the
facade copies DTOs at the boundary so component types can evolve.

## Documentation

The [documentation map](docs/README.md) indexes the current reference:
[architecture](docs/architecture.md), [coding standard](docs/coding-standard.md),
[limits](docs/limits.md), [testing strategy](docs/testing-strategy.md), and
[definition of done](docs/definition-of-done.md). The first-party coding agent,
[acode](https://github.com/eddiectc/acode), keeps its CLI, TUI, and
configuration docs in its own repository.

## Contributing

Follow [AGENTS.md](AGENTS.md), the [coding standard](docs/coding-standard.md),
and the [definition of done](docs/definition-of-done.md). `make dod` runs
`docs-check`, formatting, `go vet`, `golangci-lint`, `go test -race`,
`go build ./...`, and `govulncheck`.

`main` is protected: work on a short-lived feature branch and open a pull
request targeting `main`; never push directly to `main`. See
[ci-release.md §1.5](docs/ci-release.md#15-branching-and-protection) for the
branching workflow and §2 for releases.

## License

MIT. See [LICENSE](LICENSE).
