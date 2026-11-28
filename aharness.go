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
	// AgentName labels the root agent definition. It is recorded in
	// session_start.agent and passed to tool policy and tool calls. Empty keeps
	// the root unnamed; Resume/Fork inherit it unless set.
	AgentName string
	// UserAgent identifies the host on outbound tool requests, for example
	// "acode/1.2.0". Required.
	UserAgent string
	// Identity is the host product name used in the parent prompt's identity
	// line and in a delegated child's line, for example "acode". Empty means
	// "aharness". It is trimmed and limited to 64 bytes with no control
	// characters. It is not recorded in the session log, so supply it on every
	// construction, including Resume and Fork; a fork's new children use the
	// identity supplied to Fork.
	Identity string
	// PromptTag attributes the host's prompt template, for example
	// "acode/prompt-v1". It is recorded in session_start beside the SDK
	// prompt template version and is never a replay key. Empty means unset;
	// otherwise it is at most 64 bytes of printable ASCII matching
	// [A-Za-z0-9._:/-]+. Resume keeps the recorded tag and rejects a different
	// explicit one; Fork prefers an explicit tag and otherwise inherits only
	// when the prompt is unchanged.
	PromptTag string

	// Tools selects built-in tools, registers custom ones, and configures the
	// web and delegation tools. Nil selects the default built-in set. On
	// Resume a non-nil value must match the recorded contract; on Fork it
	// replaces the copied contract.
	Tools *ToolsConfig

	// Limits caps the per-call context budget. The context window, safety
	// margin, input limit, and compaction target derive from Model.Spec;
	// MaxOutputTokens is a host choice, never the model's advertised output
	// ceiling. A negative MaxOutputTokens is a ConfigError, and 0 sends no
	// max_tokens/max_output_tokens. On Resume/Fork with Model.Spec.ID empty,
	// all-zero Limits inherit the recorded set and any non-zero value overrides
	// it, so an override that leaves MaxOutputTokens 0 clears that budget.
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

	// Instructions is composed into the effective system prompt after the SDK
	// body. It is valid for New and Fork; Resume requires it to be empty. The
	// parent records the text once as session_start.appended_prompt, and every
	// fresh child composes it into its own prompt after the role body, including
	// the children of a resumed or forked session; a continued child keeps it
	// through its recorded prompt. The SDK does not discover workspace files: a
	// host that wants AGENTS.md, or an equivalent, in the parent and in every
	// child reads the file and passes the bytes here. The recorded value is
	// provenance, never a replay key, and is capped independently, so after
	// truncation it is not necessarily a suffix of the recorded prompt.
	Instructions string
	// SystemPrompt replaces the harness system prompt entirely. Advanced; the
	// caller owns the tool and termination instructions. It is valid for New
	// and Fork; Resume requires it to be empty. To compose a replacement without
	// transcribing the loop invariants, use agent.LoopRules for the invariant
	// lines and agent.AgentCatalogText for the agent catalog, or inherit the
	// default body with only Identity set.
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
	mu        sync.Mutex
	host      *engine.Host
	cfg       Config
	setup     *toolSetup
	model     *ModelConfig
	closed    bool
	closeDone chan struct{}
	active    *Run
}

// New creates a new session. Workspace, StateDir, and Model are required.
func New(ctx context.Context, cfg Config) (*Agent, error) {
	return openAgent(ctx, cfg, engine.ModeNew, "")
}

// Resume opens an existing session for append. cfg.Model is required; a zero
// Spec inherits the recorded model identity, limits, and pricing. An explicit
// cfg.PromptTag must equal the recorded tag; use Fork to change it.
func Resume(ctx context.Context, cfg Config, sessionID string) (*Agent, error) {
	return openAgent(ctx, cfg, engine.ModeResume, sessionID)
}

// Fork copies an existing session into a new self-contained session and opens
// it for append. Explicit Instructions, SystemPrompt, Tools, and PromptTag
// apply to the new session and are recorded with a contract_override. Identity
// is not recorded, so the fork and its new children use the Config.Identity
// supplied to Fork. With no explicit PromptTag, the fork inherits the parent
// tag only when its resulting prompt is unchanged.
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
	// read_image refuses early only when the resolved model is known to be
	// text-only. Resume and fork resolve no catalog entry, so the pointer stays
	// nil and the provider decides.
	setup.options.ImageInput = modelCfg.Spec.ImageInput
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
		Identity:        cfg.Identity,
		PromptTag:       cfg.PromptTag,
		Provider:        modelCfg.Spec.Provider,
		Dialect:         string(modelCfg.Spec.Dialect),
		BaseURL:         modelCfg.Spec.BaseURL,
		Model:           modelCfg.Spec.ID,
		Agent:           cfg.AgentName,
		ThinkingEffort:  cfg.ThinkingEffort,
		Agents:          promptAgents(cfg.Tools),
		CustomTools:     setup.custom,
		Grant:           setup.grant,
		RootGrant:       setup.rootGrant,
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
		host:      host,
		cfg:       cfg,
		setup:     setup,
		model:     &effective,
		closeDone: make(chan struct{}),
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

// ValidEffort reports whether value is a syntactically valid reasoning effort.
// It delegates to model.ValidReasoningEffort and reserves the clear sentinel
// "default" (and the empty string) as invalid.
func ValidEffort(value string) bool {
	return model.ValidReasoningEffort(value)
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
	if override.MaxOutputTokens < 0 {
		return Limits{}, &ConfigError{Field: "Limits.MaxOutputTokens", Detail: "must not be negative"}
	}
	context := spec.ContextWindow
	if override.MaxContextTokens > 0 && override.MaxContextTokens < context {
		context = override.MaxContextTokens
	}
	// The model's advertised output ceiling is display metadata, never a
	// request budget: only an explicit Limits.MaxOutputTokens is sent.
	output := override.MaxOutputTokens
	inputLimit := spec.InputLimit
	if override.MaxInputTokens > 0 && (inputLimit <= 0 || override.MaxInputTokens < inputLimit) {
		inputLimit = override.MaxInputTokens
	}
	// A context override below the input limit narrows the input budget rather
	// than failing: the documented per-call input limit is the smaller of the
	// model limit and context minus the safety margin.
	if inputLimit > context {
		inputLimit = context
	}
	internal, err := agent.NewLimits(context, output, inputLimit)
	if err != nil {
		return Limits{}, &ConfigError{Field: "Limits.MaxContextTokens", Detail: err.Error()}
	}
	limits := fromInternalLimits(internal)
	if override.CompactionTarget > 0 && override.CompactionTarget < limits.CompactionTarget {
		limits.CompactionTarget = override.CompactionTarget
	}
	return limits, nil
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

// Transcript reads this agent's display transcript. It reads a byte snapshot of
// the session log, without taking the session lock or reading the in-memory
// event slice, so it is safe while a run is active. A torn final line is
// ignored. It returns ErrClosed after Close.
//
// TranscriptItem.Text is markdown source; the host renders it.
func (a *Agent) Transcript() ([]TranscriptItem, error) {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	return readTranscript(a.host.StateDir(), a.ID())
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

// runLimits returns the per-call budget for one run. The session's resolved
// limits apply unchanged unless RunOptions.Model switches the model, in which
// case the new spec supplies the context window and input limit while the
// session keeps its explicit output budget.
func (a *Agent) runLimits(effective *ModelConfig, opts RunOptions) (Limits, error) {
	session := fromInternalLimits(a.host.Spec().Limits)
	if opts.Model == nil {
		return session, nil
	}
	return deriveLimits(effective.Spec, session.MaxOutputTokens)
}

// buildDelegation constructs the per-run delegation bridge for the effective
// parent model and its per-call limits.
func (a *Agent) buildDelegation(parent *ModelConfig, limits Limits) (*delegationSetup, error) {
	if a.cfg.Tools == nil || a.cfg.Tools.Delegation == nil {
		return nil, nil
	}
	return newDelegationSetup(a.cfg.Tools.Delegation, parent, a.setup.enabled, limits)
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
		return &ConfigError{Field: "Tools.Delegation.Agents", Detail: "must not be empty; supply the host's definition set"}
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
