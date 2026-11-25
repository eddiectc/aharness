package aharness

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
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

// ChildTarget identifies one delegated child before it starts. Its Session is
// the child's own session ID, which Transcript and InspectSession accept once
// the child's log exists.
type ChildTarget struct {
	CallID  string
	Index   int
	Agent   string
	Session string
	Intent  string
	Model   string
	Effort  string
	Tools   []ToolName
}

// DelegationConfig enables the delegate tool and configures its definitions.
type DelegationConfig struct {
	// Agents is the runnable definition set. Nil or empty is a ConfigError;
	// the host supplies every definition.
	Agents []AgentDefinition
	// MaxSubagents bounds fresh children per run. 0 selects the default of 8;
	// -1 means unlimited.
	MaxSubagents int
	// ResolveModel resolves a definition's non-empty Model/ThinkingEffort. Nil
	// uses the parent model and rejects definitions that name another model.
	ResolveModel func(ctx context.Context, modelID, effort string) (*ModelConfig, error)
	// ObserveChild, when non-nil, registers a live observation sink for one
	// delegated child. It is called once per child before that child's first
	// event, and never for a task that fails before its run starts. The
	// returned callback receives that child's own display events in that child's
	// order, with EventMeta.Session set to the child session; a nil return
	// observes nothing. It sees only events that have a public form, so an
	// internal transport event such as ModelStarted is not delivered.
	// Parent-level Subagent* events are not delivered to it and keep arriving on
	// the run's own stream.
	//
	// Registering any observer enables streaming for delegated children, which
	// makes a post-delta transport failure terminal instead of retried. Sinks
	// for different children may run concurrently, and concurrently with the
	// run's own event stream; a sink must not block.
	//
	// A returned error, or a panic, terminates that child with internal_error.
	// Siblings and the run are unaffected.
	ObserveChild func(ChildTarget) func(Event) error
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
		return nil, &ConfigError{Field: "Tools.Delegation.Agents", Detail: "must not be empty; supply the host's definition set"}
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

// internalObserve adapts the public per-child hook to the spawner's
// registration. Identity is structural: the registration names the child
// session, and every event the sink receives is converted with that session,
// because the internal delta events carry none.
func (d *delegationSetup) internalObserve() func(agent.ChildObservation) agent.ChildSink {
	if d == nil || d.cfg == nil || d.cfg.ObserveChild == nil {
		return nil
	}
	observe := d.cfg.ObserveChild
	return func(observation agent.ChildObservation) agent.ChildSink {
		sink := observe(ChildTarget{
			CallID:  observation.CallID,
			Index:   observation.Index,
			Agent:   observation.Agent,
			Session: observation.Session,
			Intent:  observation.Intent,
			Model:   observation.Model,
			Effort:  observation.Effort,
			Tools:   toolNames(observation.Tools),
		})
		if sink == nil {
			return nil
		}
		return childEventSink(observation.Session, sink)
	}
}

// toolNames copies a component grant into the public tool-name type.
func toolNames(names []string) []ToolName {
	if names == nil {
		return nil
	}
	out := make([]ToolName, len(names))
	for i, name := range names {
		out[i] = ToolName(name)
	}
	return out
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
