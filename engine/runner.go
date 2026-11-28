package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/tools"
)

// BuildRunner constructs the per-run registry, spawner, and runner. It never
// writes session_start; calling it once per run is supported. A display nil
// disables events; a controller nil disables interactive interruption.
func (h *Host) BuildRunner(run RunSpec, display func(agent.DisplayEvent) error, steering <-chan agent.SteeringLine, controller agent.Controller) (*agent.Runner, error) {
	runner, _, err := h.BuildRunnerWithSpawner(run, display, steering, controller)
	return runner, err
}

// BuildRunnerWithSpawner is BuildRunner that also returns the per-run spawner
// so a host can route SteerChild calls to live delegated children.
func (h *Host) BuildRunnerWithSpawner(run RunSpec, display func(agent.DisplayEvent) error, steering <-chan agent.SteeringLine, controller agent.Controller) (*agent.Runner, *agent.Spawner, error) {
	if h.closed {
		return nil, nil, ErrClosed
	}
	if err := resetWorkDirs(h.workDir, h.tmpDir); err != nil {
		return nil, nil, fmt.Errorf("prepare work directory: %w", err)
	}

	cfg, err := h.runConfig(run, display, steering, controller)
	if err != nil {
		return nil, nil, err
	}
	effectiveGrant := h.spec.Grant
	// The registry is the root's authority. The spawner keeps reading the
	// session grant from Config.Tools, so a narrow root never narrows a child.
	rootGrant := h.spec.RootGrant
	if rootGrant == nil {
		rootGrant = effectiveGrant
	}
	cfg.Tools = cloneGrant(effectiveGrant)
	// An explicit per-run model replaces the construction-time image
	// capability. Children inherit this value so a child that keeps the
	// parent's model keeps the parent's gate.
	rootImageInput := h.spec.ToolOptions.ImageInput
	if run.Model != "" {
		rootImageInput = run.ImageInput
	}
	catalog, err := h.agentCatalog()
	if err != nil {
		return nil, nil, err
	}
	var spawner *agent.Spawner
	if len(h.spec.Definitions) > 0 {
		resolve := h.spec.ResolveModel
		if run.ResolveModel != nil {
			resolve = run.ResolveModel
		}
		childCompleter := h.spec.NewChildCompleter
		if run.NewChildCompleter != nil {
			childCompleter = run.NewChildCompleter
		}
		maxSubagents := h.spec.MaxSubagents
		if run.MaxSubagents != nil {
			maxSubagents = *run.MaxSubagents
		}
		observeChild := h.spec.ObserveChild
		if run.ObserveChild != nil {
			observeChild = run.ObserveChild
		}
		spawner = agent.NewSpawner(agent.SpawnerOptions{
			Config:            cfg,
			Workspace:         h.workspace,
			Definitions:       catalog,
			Resolve:           resolve,
			NewCompleter:      h.spec.NewCompleter,
			NewChildCompleter: childCompleter,
			NewToolOptions: func(sessionID, workDir, tmpDir string) tools.Options {
				opts := h.spec.ToolOptions
				opts.WorkDir = workDir
				opts.TmpDir = tmpDir
				opts.Spawning = nil
				opts.ImageInput = rootImageInput
				return opts
			},
			NewRegistry:   h.spec.NewRegistry,
			Logger:        h.spec.Logger,
			Display:       display,
			Observe:       observeChild,
			ParentSession: h.ID(),
			ParentSeq:     h.store.LastSeq,
			MaxSubagents:  internalMaxSubagents(maxSubagents),
		})
	}

	opts := h.spec.ToolOptions
	opts.WorkDir = h.workDir
	opts.TmpDir = h.tmpDir
	var registry *tools.Registry
	if h.spec.NewRegistry != nil {
		registry, err = h.spec.NewRegistry(h.workspace, h.ID(), h.workDir, h.tmpDir)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare tool registry: %w", err)
		}
		if registry == nil {
			return nil, nil, fmt.Errorf("prepare tool registry: factory returned nil")
		}
		if spawner != nil {
			registry.SetSpawning(spawner)
		}
	} else {
		registry = tools.NewRegistry(h.workspace, opts)
		if spawner != nil {
			registry.SetSpawning(spawner)
		}
	}
	if err := verifyRegisteredCustomTools(registry, h.spec.CustomTools); err != nil {
		return nil, nil, err
	}
	registry.SetGrant(cloneGrant(rootGrant))
	registry.SetImageInput(rootImageInput)
	registry.SeedTodo(agent.RestoreTodo(h.store.Events()))
	completer := run.NewCompleter
	if completer == nil {
		completer = h.spec.NewCompleter
	}
	if completer == nil {
		return nil, nil, configError("Model.Completer", "is required")
	}
	return agent.New(cfg, completer(model.Dialect(cfg.Dialect), cfg.BaseURL, h.ID()), registry, h.store), spawner, nil
}

func (h *Host) runConfig(run RunSpec, display func(agent.DisplayEvent) error, steering <-chan agent.SteeringLine, controller agent.Controller) (agent.Config, error) {
	modelID := h.spec.Model
	dialect := h.spec.Dialect
	baseURL := h.spec.BaseURL
	limits := h.spec.Limits
	pricing := h.spec.Pricing
	if run.Model != "" {
		modelID = run.Model
		dialect = run.Dialect
		baseURL = run.BaseURL
		limits = run.Limits
		pricing = run.Pricing
		if limits.MaxContextTokens <= 0 || limits.MaxInputTokens <= 0 {
			return agent.Config{}, configError("RunOptions.Model", "requires positive context and input limits")
		}
		if limits.MaxOutputTokens < 0 {
			return agent.Config{}, configError("Limits.MaxOutputTokens", "must not be negative")
		}
	}
	effort := effortValue(h.spec.ThinkingEffort, "")
	if run.ThinkingEffort != nil {
		effort = *run.ThinkingEffort
	}
	maxTurns := h.spec.MaxTurns
	if run.MaxTurns != 0 {
		maxTurns = run.MaxTurns
	}
	maxTokens := h.spec.MaxTotalTokens
	if run.MaxTotalTokens != 0 {
		maxTokens = run.MaxTotalTokens
	}
	timeout := h.spec.RunTimeout
	if run.RunTimeout != 0 {
		timeout = run.RunTimeout
	}
	modelTimeout := h.spec.ModelTimeout
	if run.ModelTimeout != 0 {
		modelTimeout = run.ModelTimeout
	}
	return agent.Config{
		Provider:       h.spec.Provider,
		Dialect:        dialect,
		Model:          modelID,
		BaseURL:        baseURL,
		Workspace:      h.workspace.Dir(),
		StateDir:       h.spec.StateDir,
		Identity:       h.spec.Identity,
		PromptVersion:  h.spec.PromptVersion,
		PromptTag:      h.spec.PromptTag,
		ToolsetVersion: h.spec.ToolsetVersion,
		HarnessVersion: h.spec.Version,
		SystemPrompt:   h.spec.SystemPrompt,
		AppendedPrompt: h.appendedPrompt,
		SessionID:      h.ID(),
		Agent:          h.spec.Agent,
		Tools:          cloneGrant(h.spec.Grant),
		CustomTools:    cloneCustomTools(h.spec.CustomTools),
		ToolPolicy:     h.policy,
		Agents:         h.spec.Agents,
		MaxTurns:       maxTurns,
		MaxTotalTokens: maxTokens,
		Limits:         limits,
		SessionTimeout: timeout,
		ModelTimeout:   modelTimeout,
		LogRequests:    h.spec.LogRequests,
		RequireUsage:   h.spec.RequireUsage,
		Pricing:        pricing,
		ThinkingEffort: effort,
		Interactive:    run.Interactive,
		Stream:         run.Stream,
		Display:        display,
		Steering:       steering,
		Controller:     controller,
		Logger:         h.spec.Logger,
	}, nil
}

// agentCatalog validates and converts the public definition set for the
// spawner. Empty Tools is permitted and becomes an explicit no-tools grant.
func (h *Host) agentCatalog() (*agent.AgentCatalog, error) {
	if len(h.spec.Definitions) == 0 {
		return &agent.AgentCatalog{}, nil
	}
	builtins := make(map[string]bool)
	if schemas, err := tools.SchemasFor(h.spec.ToolsetVersion); err == nil {
		for _, schema := range schemas {
			builtins[schema.Name] = true
		}
	}
	custom := make(map[string]bool, len(h.spec.CustomTools))
	for _, tool := range h.spec.CustomTools {
		custom[tool.Schema.Name] = true
	}
	catalog := &agent.AgentCatalog{Agents: make([]agent.AgentDefinition, 0, len(h.spec.Definitions))}
	seen := make(map[string]bool, len(h.spec.Definitions))
	for i, def := range h.spec.Definitions {
		if err := agent.ValidateAgentName(def.Name); err != nil {
			return nil, configError("Delegation.Agents", "definition %d: %v", i+1, err)
		}
		if seen[def.Name] {
			return nil, configError("Delegation.Agents", "duplicate definition %q", def.Name)
		}
		seen[def.Name] = true
		if strings.TrimSpace(def.Description) == "" {
			return nil, configError("Delegation.Agents", "definition %q has no description", def.Name)
		}
		if strings.TrimSpace(def.Prompt) == "" {
			return nil, configError("Delegation.Agents", "definition %q has no prompt", def.Name)
		}
		if def.MaxTurns < 0 {
			return nil, configError("Delegation.Agents", "definition %q has a negative MaxTurns", def.Name)
		}
		grant := def.Tools
		if grant == nil {
			grant = []string{}
		}
		for _, name := range grant {
			if name == "delegate" {
				return nil, configError("Delegation.Agents", "definition %q must not include delegate: delegation depth is one", def.Name)
			}
			if !builtins[name] && !custom[name] {
				return nil, configError("Delegation.Agents", "definition %q names unknown tool %q", def.Name, name)
			}
		}
		def.Tools = append([]string{}, grant...)
		def.Err = ""
		catalog.Agents = append(catalog.Agents, def)
	}
	return catalog, nil
}

func internalMaxSubagents(value int) int {
	switch {
	case value == 0:
		return 8
	case value < 0:
		return 0
	default:
		return value
	}
}

// verifyRegisteredCustomTools ensures a caller registry factory installed
// every declared custom tool. Execution would otherwise fail only when the
// model happened to call the missing tool.
func verifyRegisteredCustomTools(registry *tools.Registry, expected []agent.CustomTool) error {
	if len(expected) == 0 {
		return nil
	}
	registered := registry.CustomSchemas()
	if len(registered) != len(expected) {
		return fmt.Errorf("%w: tool registry registered %d custom tool(s), contract declares %d", agent.ErrToolMismatch, len(registered), len(expected))
	}
	byName := make(map[string]bool, len(registered))
	for _, schema := range registered {
		byName[schema.Name] = true
	}
	for _, tool := range expected {
		if !byName[tool.Schema.Name] {
			return fmt.Errorf("%w: tool registry did not register custom tool %q", agent.ErrToolMismatch, tool.Schema.Name)
		}
	}
	return nil
}

// ErrClosed reports an operation on a closed host.
var ErrClosed = errors.New("aharness: agent is closed")
