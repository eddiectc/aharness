package aharness

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/model"
)

// AgentDefinition is one runnable delegated agent.
type AgentDefinition struct {
	Name        string
	Description string
	Prompt      string
	// Tools is the child's grant. An empty list means no tools; delegate is
	// never valid because delegation depth is one.
	Tools          []ToolName
	Model          string
	ThinkingEffort string
	MaxTurns       int
}

// DelegationConfig enables the delegate tool and configures its definitions.
type DelegationConfig struct {
	// Agents is the runnable definition set. Nil or empty is a ConfigError;
	// use DefaultAgents to opt into the bundled set.
	Agents []AgentDefinition
	// MaxSubagents bounds fresh children per run. 0 selects the default of 8;
	// -1 means unlimited.
	MaxSubagents int
	// ResolveModel resolves a definition's non-empty Model/ThinkingEffort. Nil
	// uses the parent model and rejects definitions that name another model.
	ResolveModel func(ctx context.Context, modelID, effort string) (*ModelConfig, error)
}

// DefaultAgents returns copies of the bundled definitions.
func DefaultAgents() []AgentDefinition {
	source := agent.DefaultAgents()
	out := make([]AgentDefinition, 0, len(source))
	for _, def := range source {
		tools := make([]ToolName, len(def.Tools))
		for i, name := range def.Tools {
			tools[i] = ToolName(name)
		}
		out = append(out, AgentDefinition{
			Name:        def.Name,
			Description: def.Description,
			Prompt:      def.Prompt,
			Tools:       tools,
			Model:       def.Model,
			MaxTurns:    def.MaxTurns,
		})
	}
	return out
}

// delegationSetup is the per-run bridge between public definitions and the
// internal spawner.
type delegationSetup struct {
	cfg          *DelegationConfig
	parent       *ModelConfig
	parentLimits Limits
	definitions  []agent.AgentDefinition
	maxSubagents int

	mu       sync.Mutex
	resolved map[string]*ModelConfig
}

func newDelegationSetup(cfg *DelegationConfig, parent *ModelConfig, enabled map[ToolName]bool) (*delegationSetup, error) {
	if cfg == nil {
		return nil, nil
	}
	if len(cfg.Agents) == 0 {
		return nil, &ConfigError{Field: "Tools.Delegation.Agents", Detail: "must not be empty; use DefaultAgents for the bundled set"}
	}
	if cfg.MaxSubagents < -1 {
		return nil, &ConfigError{Field: "Tools.Delegation.MaxSubagents", Detail: "must be -1 or greater"}
	}
	if parent == nil || parent.Completer == nil {
		return nil, ErrModelRequired
	}
	parentLimits, err := deriveLimits(parent.Spec)
	if err != nil {
		return nil, err
	}
	setup := &delegationSetup{
		cfg:          cfg,
		parent:       parent,
		parentLimits: parentLimits,
		maxSubagents: cfg.MaxSubagents,
		resolved:     make(map[string]*ModelConfig),
	}
	seen := make(map[string]bool, len(cfg.Agents))
	for i, def := range cfg.Agents {
		if err := agent.ValidateAgentName(def.Name); err != nil {
			return nil, &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: fmt.Sprintf("definition %d: %v", i+1, err)}
		}
		if seen[def.Name] {
			return nil, &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "is duplicated"}
		}
		seen[def.Name] = true
		if strings.TrimSpace(def.Description) == "" {
			return nil, &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "description is required"}
		}
		if strings.TrimSpace(def.Prompt) == "" {
			return nil, &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "prompt is required"}
		}
		if def.MaxTurns < 0 {
			return nil, &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "max turns must not be negative"}
		}
		grant := make([]string, 0, len(def.Tools))
		for _, name := range def.Tools {
			if name == ToolDelegate {
				return nil, &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: "must not include delegate: delegation depth is one"}
			}
			if !enabled[name] {
				return nil, &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: fmt.Sprintf("names tool %q, which the parent session does not enable", name)}
			}
			grant = append(grant, string(name))
		}
		if def.ThinkingEffort != "" && !model.ValidReasoningEffort(def.ThinkingEffort) {
			return nil, &ConfigError{Field: "Tools.Delegation.Agents", Value: def.Name, Detail: fmt.Sprintf("invalid thinking effort %q", def.ThinkingEffort)}
		}
		setup.definitions = append(setup.definitions, agent.AgentDefinition{
			Name:           def.Name,
			Description:    def.Description,
			Prompt:         def.Prompt,
			Tools:          grant,
			Model:          def.Model,
			ThinkingEffort: def.ThinkingEffort,
			MaxTurns:       def.MaxTurns,
		})
	}
	return setup, nil
}

func (d *delegationSetup) resolve(modelID, effort string) (agent.ModelResolution, error) {
	if modelID == "" {
		return agent.ModelResolution{}, fmt.Errorf("no model is configured for this session")
	}
	if modelID == d.parent.Spec.ID {
		if err := validateEffort(effort, d.parent.Spec); err != nil {
			return agent.ModelResolution{}, err
		}
		return agent.ModelResolution{
			Dialect: string(d.parent.Spec.Dialect),
			BaseURL: d.parent.Spec.BaseURL,
			Limits:  toInternalLimits(d.parentLimits),
			Pricing: toInternalPricing(d.parent.Spec.Pricing),
		}, nil
	}
	if d.cfg.ResolveModel == nil {
		return agent.ModelResolution{}, fmt.Errorf("model %q requires Delegation.ResolveModel", modelID)
	}
	resolved, err := d.resolveConfig(modelID, effort)
	if err != nil {
		return agent.ModelResolution{}, err
	}
	limits, err := deriveLimits(resolved.Spec)
	if err != nil {
		return agent.ModelResolution{}, err
	}
	return agent.ModelResolution{
		Dialect: string(resolved.Spec.Dialect),
		BaseURL: resolved.Spec.BaseURL,
		Limits:  toInternalLimits(limits),
		Pricing: toInternalPricing(resolved.Spec.Pricing),
	}, nil
}

func (d *delegationSetup) resolveConfig(modelID, effort string) (*ModelConfig, error) {
	key := modelID + "\x00" + effort
	d.mu.Lock()
	if resolved, ok := d.resolved[key]; ok {
		d.mu.Unlock()
		return resolved, nil
	}
	d.mu.Unlock()
	resolved, err := d.cfg.ResolveModel(context.Background(), modelID, effort)
	if err != nil {
		return nil, err
	}
	if resolved == nil {
		return nil, fmt.Errorf("resolve model %q: resolver returned nil", modelID)
	}
	if resolved.Spec.ID != modelID {
		return nil, fmt.Errorf("resolve model %q: resolver returned model %q", modelID, resolved.Spec.ID)
	}
	if _, err := normalizeSpec(resolved.Spec); err != nil {
		return nil, err
	}
	if resolved.Completer == nil {
		return nil, fmt.Errorf("resolve model %q: completer is required", modelID)
	}
	if err := validateEffort(effort, resolved.Spec); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.resolved[key] = resolved
	d.mu.Unlock()
	return resolved, nil
}

func (d *delegationSetup) childCompleter(def agent.AgentDefinition, resolution agent.ModelResolution, sessionID string) model.Completer {
	modelID := def.Model
	if modelID == "" {
		modelID = d.parent.Spec.ID
	}
	config := d.parent
	if modelID != d.parent.Spec.ID {
		resolved, err := d.resolveConfig(modelID, def.ThinkingEffort)
		if err != nil {
			return newModelAdapter(failingModel{err: err})
		}
		config = resolved
	}
	if config.NewCompleter != nil {
		if child := config.NewCompleter(sessionID); child != nil {
			return newModelAdapter(child)
		}
	}
	if config.Completer == nil {
		return newModelAdapter(failingModel{err: fmt.Errorf("no completer for model %q", modelID)})
	}
	return newModelAdapter(modelForSession(config, sessionID))
}

// internalResolver adapts the setup to the spawner's resolver signature.
func (d *delegationSetup) internalResolver() agent.ModelResolver {
	if d == nil {
		return nil
	}
	return func(modelID, effort string) (agent.ModelResolution, error) {
		return d.resolve(modelID, effort)
	}
}

func (d *delegationSetup) internalChildCompleter() agent.ChildCompleterFunc {
	if d == nil {
		return nil
	}
	return d.childCompleter
}

func validateEffort(effort string, spec ModelSpec) error {
	if effort == "" || len(spec.ThinkingEfforts) == 0 {
		return nil
	}
	if slices.Contains(spec.ThinkingEfforts, effort) {
		return nil
	}
	return fmt.Errorf("thinking effort %q is not advertised by model %q", effort, spec.ID)
}

// failingModel reports a child-model resolution failure as a model error.
type failingModel struct{ err error }

func (f failingModel) Complete(context.Context, Request) (Response, error) {
	return Response{}, f.err
}
