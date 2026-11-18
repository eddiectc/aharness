package aharness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/engine"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/tools"
)

// Config is the construction input for New, Resume, and Fork. The library has
// no implicit environment, config file, working directory, TTY, or signal
// layer.
type Config struct {
	// Workspace is the repository root the agent may read and write. Required
	// for New; empty inherits the recorded workspace on Resume/Fork.
	Workspace string
	// StateDir is the durable state root. Required.
	StateDir string
	// Model is the model implementation and identity. Required for New. On
	// Resume/Fork a zero Spec inherits the recorded identity.
	Model *ModelConfig
	// UserAgent identifies the host on outbound tool requests, for example
	// "acode/1.2.0". Required.
	UserAgent string

	// Tools selects built-in tools, registers custom ones, and configures the
	// web and delegation tools. Nil selects the default built-in set. On
	// Resume a non-nil value must match the recorded contract; on Fork it
	// replaces the copied contract.
	Tools *ToolsConfig

	// Limits overrides the per-call context budget. Zero fields are derived
	// from Model.Spec.
	Limits Limits

	// MaxTurns is the cumulative model-turn budget across this agent's runs.
	// 0 means unlimited. It is not inherited from the log.
	MaxTurns int
	// MaxTotalTokens is the cumulative input+output token budget. 0 means
	// unlimited. It is not inherited from the log.
	MaxTotalTokens int
	// RunTimeout bounds one Run. 0 means no harness deadline.
	RunTimeout time.Duration
	// ModelTimeout bounds one model attempt. 0 means no harness deadline.
	ModelTimeout time.Duration

	// ThinkingEffort is the reasoning effort. nil inherits the provider or
	// session default; a pointer to "" clears it; a non-empty value sets it.
	ThinkingEffort *string

	// Instructions is appended to the harness system prompt. It is valid for
	// New and Fork; Resume requires it to be empty.
	Instructions string
	// SystemPrompt replaces the harness system prompt entirely. Advanced; the
	// caller owns the tool and termination instructions. It is valid for New
	// and Fork; Resume requires it to be empty.
	SystemPrompt string

	// Authorize is called before every tool execution, including delegated
	// children. A returned error denies the call with a permission_denied
	// result unless it is a context cancellation. Nil allows every tool.
	Authorize func(ctx context.Context, req ToolRequest) error

	// RequireUsage makes a completed response without provider usage a
	// protocol error before any tool executes.
	RequireUsage bool
	// LogModelRequests records full model requests in the session log.
	LogModelRequests bool
	// Logger receives operational logs. Nil discards them.
	Logger *slog.Logger
}

// Agent is one durable session and the loop that advances it.
type Agent struct {
	mu         sync.Mutex
	host       *engine.Host
	cfg        Config
	setup      *toolSetup
	model      *ModelConfig
	closed     bool
	closeDone  chan struct{}
	active     *Run
	newSession bool
	started    bool
}

// New creates a new session. Workspace, StateDir, and Model are required.
func New(ctx context.Context, cfg Config) (*Agent, error) {
	return openAgent(ctx, cfg, engine.ModeNew, "")
}

// Resume opens an existing session for append. cfg.Model is required; a zero
// Spec inherits the recorded model identity, limits, and pricing.
func Resume(ctx context.Context, cfg Config, sessionID string) (*Agent, error) {
	return openAgent(ctx, cfg, engine.ModeResume, sessionID)
}

// Fork copies an existing session into a new self-contained session and opens
// it for append. Explicit Instructions, SystemPrompt, and Tools apply to the
// new session and are recorded with a contract_override.
func Fork(ctx context.Context, cfg Config, sessionID string) (*Agent, error) {
	return openAgent(ctx, cfg, engine.ModeFork, sessionID)
}

func openAgent(ctx context.Context, cfg Config, mode engine.Mode, sessionID string) (*Agent, error) {
	if cfg.StateDir == "" {
		return nil, &ConfigError{Field: "StateDir", Detail: "is required"}
	}
	if mode == engine.ModeNew && cfg.Workspace == "" {
		return nil, &ConfigError{Field: "Workspace", Detail: "is required for New"}
	}
	if cfg.Model == nil {
		return nil, ErrModelRequired
	}
	if cfg.Model.Completer == nil {
		return nil, &ConfigError{Field: "Model.Completer", Detail: "is required"}
	}
	if strings.TrimSpace(cfg.UserAgent) == "" {
		return nil, &ConfigError{Field: "UserAgent", Detail: "is required"}
	}
	if cfg.MaxTurns < 0 {
		return nil, &ConfigError{Field: "MaxTurns", Detail: "must not be negative"}
	}
	if cfg.MaxTotalTokens < 0 {
		return nil, &ConfigError{Field: "MaxTotalTokens", Detail: "must not be negative"}
	}
	if cfg.RunTimeout < 0 {
		return nil, &ConfigError{Field: "RunTimeout", Detail: "must not be negative"}
	}
	if cfg.ModelTimeout < 0 {
		return nil, &ConfigError{Field: "ModelTimeout", Detail: "must not be negative"}
	}
	if cfg.Instructions != "" && cfg.SystemPrompt != "" {
		return nil, &ConfigError{Field: "SystemPrompt", Detail: "must not be combined with Instructions"}
	}
	if mode == engine.ModeResume && (cfg.Instructions != "" || cfg.SystemPrompt != "") {
		return nil, &ConfigError{Field: "SystemPrompt", Detail: "prompt overrides are not valid on resume; use Fork"}
	}
	modelCfg := *cfg.Model
	if mode == engine.ModeNew || modelCfg.Spec.ID != "" {
		spec, err := normalizeSpec(modelCfg.Spec)
		if err != nil {
			return nil, err
		}
		if mode == engine.ModeNew && spec.ID == "" {
			return nil, &ConfigError{Field: "Model.Spec.ID", Detail: "is required for New"}
		}
		modelCfg.Spec = spec
	}
	if err := validateEffortValue(cfg.ThinkingEffort, modelCfg.Spec); err != nil {
		return nil, err
	}
	setup, err := resolveToolSetup(cfg.Tools, cfg.UserAgent, delegationConfigured(cfg.Tools))
	if err != nil {
		return nil, err
	}
	if cfg.Tools != nil && cfg.Tools.Delegation != nil {
		if err := validateDelegationDefinitions(cfg.Tools.Delegation, setup.enabled); err != nil {
			return nil, err
		}
	}

	espec := engine.Spec{
		Workspace:       cfg.Workspace,
		StateDir:        cfg.StateDir,
		Mode:            mode,
		SessionID:       sessionID,
		UserAgent:       cfg.UserAgent,
		Provider:        modelCfg.Spec.Provider,
		Dialect:         string(modelCfg.Spec.Dialect),
		BaseURL:         modelCfg.Spec.BaseURL,
		Model:           modelCfg.Spec.ID,
		ThinkingEffort:  cfg.ThinkingEffort,
		Agents:          promptAgents(cfg.Tools),
		CustomTools:     setup.custom,
		Grant:           setup.grant,
		ToolOptions:     setup.options,
		ToolPolicy:      authorizeAdapter(cfg.Authorize),
		SystemPrompt:    cfg.SystemPrompt,
		SystemPromptSet: cfg.SystemPrompt != "",
		AppendPrompt:    cfg.Instructions,
		AppendPromptSet: cfg.Instructions != "",
		MaxTurns:        cfg.MaxTurns,
		MaxTotalTokens:  cfg.MaxTotalTokens,
		RunTimeout:      cfg.RunTimeout,
		ModelTimeout:    cfg.ModelTimeout,
		RequireUsage:    cfg.RequireUsage,
		LogRequests:     cfg.LogModelRequests,
		Version:         Version(),
		Logger:          cfg.Logger,
		NewRegistry:     setup.factory,
		NewCompleter: func(dialect model.Dialect, baseURL, sessionID string) model.Completer {
			return newModelAdapter(modelForSession(&modelCfg, sessionID))
		},
	}
	if cfg.Tools != nil && cfg.Tools.Delegation != nil {
		espec.Definitions = internalDefinitions(cfg.Tools.Delegation)
		espec.MaxSubagents = cfg.Tools.Delegation.MaxSubagents
	}
	if modelCfg.Spec.ID != "" {
		limits, err := mergeLimits(modelCfg.Spec, cfg.Limits)
		if err != nil {
			return nil, err
		}
		espec.Limits = toInternalLimits(limits)
		espec.Pricing = toInternalPricing(modelCfg.Spec.Pricing)
	} else {
		espec.Limits = toInternalLimits(cfg.Limits)
	}
	switch mode {
	case engine.ModeNew:
		espec.GrantSet = true
		espec.CustomToolsSet = true
	case engine.ModeFork:
		if cfg.Tools != nil {
			espec.GrantSet = true
			espec.CustomToolsSet = true
		}
	default:
		// Resume inherits the recorded contract unless the caller supplied an
		// explicit one, in which case the engine requires an exact match.
		if cfg.Tools != nil {
			espec.GrantSet = true
			espec.CustomToolsSet = true
		}
	}
	host, err := engine.Open(ctx, espec)
	if err != nil {
		return nil, publicError(err)
	}
	if mode != engine.ModeNew && host.Spec().Grant == nil && !delegationConfigured(cfg.Tools) && toolsetHasDelegate(host.Spec().ToolsetVersion) {
		_ = host.Close()
		return nil, fmt.Errorf("%w: session records no tool grant, which includes delegate; supply Tools.Delegation or Fork with an explicit Tools value", ErrToolMismatch)
	}
	effective := modelCfg
	if effective.Spec.ID == "" {
		resolved := host.Spec()
		effective.Spec = ModelSpec{
			ID:              resolved.Model,
			Provider:        resolved.Provider,
			Dialect:         Dialect(resolved.Dialect),
			BaseURL:         resolved.BaseURL,
			ContextWindow:   resolved.Limits.MaxContextTokens,
			MaxOutputTokens: resolved.Limits.MaxOutputTokens,
			Pricing:         fromInternalPricing(resolved.Pricing),
		}
	}
	a := &Agent{
		host:       host,
		cfg:        cfg,
		setup:      setup,
		model:      &effective,
		closeDone:  make(chan struct{}),
		newSession: mode == engine.ModeNew,
	}
	return a, nil
}

func delegationConfigured(cfg *ToolsConfig) bool {
	return cfg != nil && cfg.Delegation != nil
}

func toolsetHasDelegate(version string) bool {
	schemas, err := tools.SchemasFor(version)
	if err != nil {
		return false
	}
	for _, schema := range schemas {
		if schema.Name == string(ToolDelegate) {
			return true
		}
	}
	return false
}

func promptAgents(cfg *ToolsConfig) []agent.AgentInfo {
	if !delegationConfigured(cfg) {
		return nil
	}
	infos := make([]agent.AgentInfo, 0, len(cfg.Delegation.Agents))
	for _, def := range cfg.Delegation.Agents {
		infos = append(infos, agent.AgentInfo{Name: def.Name, Description: def.Description})
	}
	return infos
}

func authorizeAdapter(authorize func(context.Context, ToolRequest) error) func(context.Context, agent.ToolPolicyRequest) error {
	if authorize == nil {
		return nil
	}
	return func(ctx context.Context, req agent.ToolPolicyRequest) error {
		return authorize(ctx, ToolRequest{
			Tool: ToolName(req.Name),
			Call: ToolCall{
				ID:        req.Call.ID,
				Name:      req.Name,
				Arguments: append([]byte(nil), req.Args...),
				Intent:    tools.Intent(req.Args),
				Session:   req.Call.Session,
				Agent:     req.Call.Agent,
			},
			Intent:  tools.Intent(req.Args),
			Turn:    req.Turn,
			Session: req.Session,
			Agent:   req.Agent,
			Path:    req.Path,
		})
	}
}

func validateEffortValue(effort *string, spec ModelSpec) error {
	if effort == nil || *effort == "" {
		return nil
	}
	if !model.ValidReasoningEffort(*effort) {
		return &ConfigError{Field: "ThinkingEffort", Value: *effort, Detail: "is not a valid effort value"}
	}
	if len(spec.ThinkingEfforts) > 0 {
		if slices.Contains(spec.ThinkingEfforts, *effort) {
			return nil
		}
		return &ConfigError{Field: "ThinkingEffort", Value: *effort, Detail: fmt.Sprintf("model %q does not advertise it", spec.ID)}
	}
	return nil
}

func mergeLimits(spec ModelSpec, override Limits) (Limits, error) {
	derived, err := deriveLimits(spec)
	if err != nil {
		return Limits{}, err
	}
	context := derived.MaxContextTokens
	if override.MaxContextTokens > 0 && override.MaxContextTokens < context {
		context = override.MaxContextTokens
	}
	output := derived.MaxOutputTokens
	if override.MaxOutputTokens > 0 {
		output = override.MaxOutputTokens
	}
	margin := agent.SafetyMargin(context)
	input := context - margin
	if spec.InputLimit > 0 && spec.InputLimit < input {
		input = spec.InputLimit
	}
	if override.MaxInputTokens > 0 && override.MaxInputTokens < input {
		input = override.MaxInputTokens
	}
	if input <= 0 {
		return Limits{}, &ConfigError{Field: "Limits.MaxContextTokens", Detail: "leaves no positive input budget"}
	}
	target := input
	if override.CompactionTarget > 0 && override.CompactionTarget < target {
		target = override.CompactionTarget
	}
	return Limits{
		MaxContextTokens: context,
		MaxOutputTokens:  output,
		SafetyMargin:     margin,
		MaxInputTokens:   input,
		CompactionTarget: target,
	}, nil
}

// ID returns the canonical session UUID.
func (a *Agent) ID() string { return a.host.ID() }

// SessionPath returns <StateDir>/sessions/<id>.jsonl.
func (a *Agent) SessionPath() string { return a.host.Path() }

// Workspace returns the canonical absolute workspace.
func (a *Agent) Workspace() string { return a.host.Workspace() }

// State returns the recorded durable state. It returns ErrBusy during a run
// and ErrClosed after Close.
func (a *Agent) State() (State, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return State{}, ErrClosed
	}
	if a.active != nil && !a.active.isDone() {
		return State{}, ErrBusy
	}
	snapshot, err := a.host.Snapshot()
	if err != nil {
		return State{}, publicError(err)
	}
	return stateFromSnapshot(snapshot), nil
}

// Close cancels an active run, drains it, and closes the store, workspace, and
// work directory. It is idempotent.
func (a *Agent) Close() error {
	a.mu.Lock()
	if a.closed {
		done := a.closeDone
		a.mu.Unlock()
		<-done
		return nil
	}
	a.closed = true
	active := a.active
	a.active = nil
	done := a.closeDone
	a.mu.Unlock()

	var runErr error
	if active != nil {
		runErr = active.Close()
	}
	hostErr := a.host.Close()
	close(done)
	return errors.Join(runErr, publicError(hostErr))
}

// isDone reports whether the run reached a terminal state.
func (r *Run) isDone() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// effectiveModelForRun resolves the model configuration for one run.
func (a *Agent) effectiveModelForRun(opts RunOptions) (*ModelConfig, error) {
	if opts.Model == nil {
		return a.model, nil
	}
	spec, err := normalizeSpec(opts.Model.Spec)
	if err != nil {
		return nil, err
	}
	if spec.ID == "" {
		return nil, &ConfigError{Field: "RunOptions.Model.Spec.ID", Detail: "is required for an override"}
	}
	if opts.Model.Completer == nil {
		return nil, &ConfigError{Field: "RunOptions.Model.Completer", Detail: "is required for an override"}
	}
	effective := *opts.Model
	effective.Spec = spec
	return &effective, nil
}

// buildDelegation constructs the per-run delegation bridge for the effective
// parent model.
func (a *Agent) buildDelegation(parent *ModelConfig) (*delegationSetup, error) {
	if a.cfg.Tools == nil || a.cfg.Tools.Delegation == nil {
		return nil, nil
	}
	return newDelegationSetup(a.cfg.Tools.Delegation, parent, a.setup.enabled)
}

// internalDefinitions converts validated public definitions for the engine.
func internalDefinitions(cfg *DelegationConfig) []agent.AgentDefinition {
	out := make([]agent.AgentDefinition, 0, len(cfg.Agents))
	for _, def := range cfg.Agents {
		tools := make([]string, 0, len(def.Tools))
		for _, name := range def.Tools {
			tools = append(tools, string(name))
		}
		out = append(out, agent.AgentDefinition{
			Name:           def.Name,
			Description:    def.Description,
			Prompt:         def.Prompt,
			Tools:          tools,
			Model:          def.Model,
			ThinkingEffort: def.ThinkingEffort,
			MaxTurns:       def.MaxTurns,
		})
	}
	return out
}

func validateDelegationDefinitions(cfg *DelegationConfig, enabled map[ToolName]bool) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.Agents) == 0 {
		return &ConfigError{Field: "Tools.Delegation.Agents", Detail: "must not be empty; use DefaultAgents for the bundled set"}
	}
	if cfg.MaxSubagents < -1 {
		return &ConfigError{Field: "Tools.Delegation.MaxSubagents", Detail: "must be -1 or greater"}
	}
	seen := make(map[string]bool, len(cfg.Agents))
	for i, def := range cfg.Agents {
		if err := agent.ValidateAgentName(def.Name); err != nil {
			return &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: fmt.Sprintf("definition %d: %v", i+1, err)}
		}
		if seen[def.Name] {
			return &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "is duplicated"}
		}
		seen[def.Name] = true
		if strings.TrimSpace(def.Description) == "" {
			return &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "description is required"}
		}
		if strings.TrimSpace(def.Prompt) == "" {
			return &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "prompt is required"}
		}
		if def.MaxTurns < 0 {
			return &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "max turns must not be negative"}
		}
		for _, name := range def.Tools {
			if name == ToolDelegate {
				return &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "must not include delegate"}
			}
			if !enabled[name] {
				return &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: fmt.Sprintf("names tool %q, which the parent session does not enable", name)}
			}
		}
	}
	return nil
}
