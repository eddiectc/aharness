// Package engine is the host-agnostic composition layer shared by the root facade and
// host applications. It owns new/resume/fork resolution, workspace and state
// setup, session_start recording, per-run work directories, and runner wiring.
// It imports no terminal, flag, signal, or os.Exit code.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// Mode selects how Open treats SessionID.
type Mode int

const (
	// ModeNew creates a fresh session.
	ModeNew Mode = iota
	// ModeResume opens an existing session for append.
	ModeResume
	// ModeFork copies an existing session into a new self-contained log.
	ModeFork
)

// ConfigError describes an invalid engine construction input.
type ConfigError struct {
	Field  string
	Detail string
}

func (e *ConfigError) Error() string {
	if e.Field == "" {
		return "aharness: " + e.Detail
	}
	return fmt.Sprintf("aharness: %s: %s", e.Field, e.Detail)
}

func configError(field, format string, args ...any) error {
	return &ConfigError{Field: field, Detail: fmt.Sprintf(format, args...)}
}

// Spec is the resolved construction input for one Host. Public Config and the
// CLI map onto it. Zero model fields inherit the recorded session values on
// resume and fork; zero budgets and limits derive from the model or session.
type Spec struct {
	Workspace string
	StateDir  string
	Mode      Mode
	SessionID string // resume/fork source

	Provider string
	Dialect  string
	BaseURL  string
	Model    string
	Agent    string
	Limits   agent.Limits
	Pricing  model.Pricing
	// ThinkingEffort is tri-state: nil inherits/default, a pointer to "" clears
	// the inherited value, and a non-empty pointer sets it.
	ThinkingEffort *string
	Agents         []agent.AgentInfo
	Definitions    []agent.AgentDefinition
	ResolveModel   func(modelID, effort string) (agent.ModelResolution, error)

	NewCompleter      func(dialect model.Dialect, baseURL, sessionID string) model.Completer
	NewChildCompleter func(def agent.AgentDefinition, resolution agent.ModelResolution, sessionID string) model.Completer
	NewRegistry       func(ws *tools.Workspace, sessionID, workDir, tmpDir string) (*tools.Registry, error)
	CustomTools       []agent.CustomTool
	CustomToolsSet    bool
	// Grant is the tool-name grant. GrantSet distinguishes "inherit the
	// recorded contract" (resume/fork) from an explicit value; the value itself
	// preserves nil (all tools of the recorded version) versus empty (none).
	Grant    []string
	GrantSet bool
	// ExecutionGrant narrows dispatch for this process without changing the
	// recorded grant or projection. The CLI uses it to withhold delegate in
	// batch mode while preserving the session contract.
	ExecutionGrant    []string
	ExecutionGrantSet bool
	// ToolOptions carries the workspace-independent tool configuration. Work
	// directories are filled per run by the engine.
	ToolOptions tools.Options
	ToolPolicy  func(context.Context, agent.ToolPolicyRequest) error
	// MaxSubagents is the public convention: 0 selects the default of 8 and -1
	// means unlimited. Other negative values are invalid.
	MaxSubagents int

	SystemPrompt    string
	SystemPromptSet bool
	AppendPrompt    string
	AppendPromptSet bool
	PromptVersion   string
	ToolsetVersion  string
	MaxTurns        int
	MaxTotalTokens  int
	RunTimeout      time.Duration
	ModelTimeout    time.Duration
	RequireUsage    bool
	LogRequests     bool
	Version         string
	Logger          *slog.Logger
}

// RunSpec carries the per-run overrides that do not rewrite the session.
type RunSpec struct {
	Model             string
	Dialect           string
	BaseURL           string
	Limits            agent.Limits
	Pricing           model.Pricing
	ThinkingEffort    *string
	MaxTurns          int
	MaxTotalTokens    int
	RunTimeout        time.Duration
	ModelTimeout      time.Duration
	NewCompleter      func(dialect model.Dialect, baseURL, sessionID string) model.Completer
	ResolveModel      func(modelID, effort string) (agent.ModelResolution, error)
	NewChildCompleter func(def agent.AgentDefinition, resolution agent.ModelResolution, sessionID string) model.Completer
	MaxSubagents      *int
	Stream            bool
	Interactive       bool
}

// Host owns one durable session, its workspace, and the resolved contract.
type Host struct {
	spec      Spec
	store     *session.Store
	workspace *tools.Workspace
	workDir   string
	tmpDir    string
	policy    func(context.Context, agent.ToolPolicyRequest) error
	closed    bool
}

// Open prepares a new, resumed, or forked session and returns its host. A
// returned error leaves no store or workspace open. New and fork write and
// sync session_start before returning; fork copies the parent history after
// it.
func Open(ctx context.Context, spec Spec) (*Host, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.StateDir == "" {
		return nil, configError("StateDir", "is required")
	}
	if spec.MaxSubagents < -1 {
		return nil, configError("MaxSubagents", "must be -1 or greater")
	}
	if err := prepareStateDir(spec.StateDir); err != nil {
		return nil, err
	}
	switch spec.Mode {
	case ModeNew:
		return openNew(spec)
	case ModeResume:
		return openResume(spec)
	case ModeFork:
		return openFork(spec)
	default:
		return nil, configError("Mode", "is not supported")
	}
}

func openNew(spec Spec) (*Host, error) {
	if spec.Workspace == "" {
		return nil, configError("Workspace", "is required")
	}
	if spec.Model == "" {
		return nil, configError("Model", "is required")
	}
	if spec.Limits.MaxContextTokens <= 0 {
		return nil, configError("Limits.MaxContextTokens", "must be positive")
	}
	if spec.Limits.MaxInputTokens <= 0 {
		return nil, configError("Limits.MaxInputTokens", "must be positive")
	}
	promptVersion := spec.PromptVersion
	if promptVersion == "" {
		promptVersion = agent.PromptVersion
	}
	if !agent.SupportsPrompt(promptVersion) {
		return nil, configError("PromptVersion", "unsupported prompt version %q", promptVersion)
	}
	toolsetVersion := spec.ToolsetVersion
	if toolsetVersion == "" {
		toolsetVersion = tools.Version
	}
	if !agent.SupportsToolset(toolsetVersion) {
		return nil, configError("ToolsetVersion", "unsupported toolset version %q", toolsetVersion)
	}
	if err := validateContract(spec, toolsetVersion, promptVersion); err != nil {
		return nil, err
	}
	ws, err := tools.OpenWorkspace(spec.Workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace %s: %w", spec.Workspace, err)
	}
	sessionID := spec.SessionID
	if sessionID == "" {
		sessionID = session.NewID()
	}
	store, err := session.Create(spec.StateDir, sessionID)
	if err != nil {
		_ = ws.Close()
		return nil, fmt.Errorf("create session log: %w", err)
	}
	prompt := resolvedNewPrompt(spec, promptVersion, ws.Dir())
	cfg := agent.Config{
		Provider:       spec.Provider,
		Dialect:        spec.Dialect,
		Model:          spec.Model,
		BaseURL:        spec.BaseURL,
		Workspace:      ws.Dir(),
		StateDir:       spec.StateDir,
		PromptVersion:  promptVersion,
		ToolsetVersion: toolsetVersion,
		HarnessVersion: spec.Version,
		SystemPrompt:   prompt,
		Agent:          spec.Agent,
		Tools:          cloneGrant(spec.Grant),
		CustomTools:    cloneCustomTools(spec.CustomTools),
		Limits:         spec.Limits,
		ThinkingEffort: effortValue(spec.ThinkingEffort, ""),
		Pricing:        spec.Pricing,
	}
	if err := agent.WriteSessionStart(store, cfg, nil); err != nil {
		_ = store.Close()
		_ = ws.Close()
		return nil, fmt.Errorf("record session start: %w", err)
	}
	spec.SessionID = store.ID()
	spec.Workspace = ws.Dir()
	spec.Model = cfg.Model
	spec.Dialect = cfg.Dialect
	spec.BaseURL = cfg.BaseURL
	spec.SystemPrompt = prompt
	spec.PromptVersion = promptVersion
	spec.ToolsetVersion = toolsetVersion
	spec.Grant = cloneGrant(spec.Grant)
	spec.CustomTools = cloneCustomTools(spec.CustomTools)
	return newHost(spec, store, ws), nil
}

func openResume(spec Spec) (*Host, error) {
	if spec.SessionID == "" {
		return nil, configError("SessionID", "is required for resume")
	}
	if spec.SystemPromptSet || spec.AppendPromptSet {
		return nil, configError("SystemPrompt", "prompt overrides are not valid on resume; use Fork")
	}
	if spec.Model == "" {
		if spec.NewCompleter == nil {
			return nil, configError("Model.Completer", "is required on resume")
		}
	} else if spec.Limits.MaxContextTokens <= 0 || spec.Limits.MaxInputTokens <= 0 {
		return nil, configError("Limits", "an explicit model override requires a positive context and input limit")
	}
	store, err := session.Open(spec.StateDir, spec.SessionID)
	if err != nil {
		return nil, normalizeSessionError(err)
	}
	state, err := agent.Inspect(store.Events())
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("read session %s: %w", spec.SessionID, err)
	}
	if !agent.SupportsPrompt(state.PromptVersion) {
		_ = store.Close()
		return nil, configError("PromptVersion", "session prompt version %q is not supported", state.PromptVersion)
	}
	if !agent.SupportsToolset(state.ToolsetVersion) {
		_ = store.Close()
		return nil, configError("ToolsetVersion", "session toolset version %q is not supported", state.ToolsetVersion)
	}
	resolved, prompt, err := resolveInherited(spec, state, false)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	ws, err := openSessionWorkspace(spec.Workspace, state.Workspace)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	resolved.Workspace = ws.Dir()
	resolved.SessionID = store.ID()
	resolved.SystemPrompt = prompt
	return newHost(resolved, store, ws), nil
}

func openFork(spec Spec) (*Host, error) {
	if spec.SessionID == "" {
		return nil, configError("SessionID", "is required for fork")
	}
	source, err := session.OpenShared(spec.StateDir, spec.SessionID)
	if err != nil {
		return nil, normalizeSessionError(err)
	}
	parentEvents := source.Events()
	parentState, err := agent.Inspect(parentEvents)
	if err != nil {
		_ = source.Close()
		return nil, fmt.Errorf("read parent session %s: %w", spec.SessionID, err)
	}
	if !agent.SupportsPrompt(parentState.PromptVersion) {
		_ = source.Close()
		return nil, configError("PromptVersion", "parent prompt version %q is not supported", parentState.PromptVersion)
	}
	if !agent.SupportsToolset(parentState.ToolsetVersion) {
		_ = source.Close()
		return nil, configError("ToolsetVersion", "parent toolset version %q is not supported", parentState.ToolsetVersion)
	}
	resolved, prompt, err := resolveInherited(spec, parentState, true)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	ws, err := openSessionWorkspace(spec.Workspace, parentState.Workspace)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	resolved.Workspace = ws.Dir()
	resolved.SessionID = session.NewID()
	resolved.SystemPrompt = prompt
	parentPrompt := parentState.SystemPrompt
	if parentPrompt == "" {
		rendered, renderErr := agent.RenderSystemPrompt(parentState.PromptVersion, parentState.Workspace, spec.Agents)
		if renderErr == nil {
			parentPrompt = rendered
		}
	}
	promptChanged := prompt != parentPrompt
	toolsChanged := !sameGrant(resolved.Grant, parentState.Tools) || !sameCustomTools(resolved.CustomTools, parentState.CustomTools)
	var override *agent.ContractOverride
	if promptChanged || toolsChanged {
		override = &agent.ContractOverride{ParentSession: source.ID(), Prompt: promptChanged, Tools: toolsChanged}
	}
	store, err := session.Create(resolved.StateDir, resolved.SessionID)
	if err != nil {
		_ = source.Close()
		_ = ws.Close()
		return nil, fmt.Errorf("create forked session log: %w", err)
	}
	cfg := agent.Config{
		Provider:         resolved.Provider,
		Dialect:          resolved.Dialect,
		Model:            resolved.Model,
		BaseURL:          resolved.BaseURL,
		Workspace:        ws.Dir(),
		StateDir:         resolved.StateDir,
		PromptVersion:    resolved.PromptVersion,
		ToolsetVersion:   resolved.ToolsetVersion,
		HarnessVersion:   resolved.Version,
		SystemPrompt:     prompt,
		Agent:            resolved.Agent,
		Tools:            cloneGrant(resolved.Grant),
		CustomTools:      cloneCustomTools(resolved.CustomTools),
		ContractOverride: override,
		Limits:           resolved.Limits,
		ThinkingEffort:   effortValue(resolved.ThinkingEffort, ""),
		Pricing:          resolved.Pricing,
	}
	parent := &agent.ParentRef{Session: source.ID(), Seq: source.LastSeq()}
	if err := agent.WriteSessionStart(store, cfg, parent); err != nil {
		_ = store.Close()
		_ = ws.Close()
		_ = source.Close()
		return nil, fmt.Errorf("record forked session start: %w", err)
	}
	if err := store.CopyHistory(parentEvents); err != nil {
		_ = store.Close()
		_ = ws.Close()
		_ = source.Close()
		return nil, fmt.Errorf("copy parent history: %w", err)
	}
	if err := store.Sync(); err != nil {
		_ = store.Close()
		_ = ws.Close()
		_ = source.Close()
		return nil, fmt.Errorf("sync forked session: %w", err)
	}
	if err := source.Close(); err != nil {
		_ = store.Close()
		_ = ws.Close()
		return nil, fmt.Errorf("close parent session: %w", err)
	}
	return newHost(resolved, store, ws), nil
}

// resolveInherited applies the resume/fork inheritance matrix. It validates
// prompt and tool overrides before any new log exists.
func resolveInherited(spec Spec, state agent.State, fork bool) (Spec, string, error) {
	resolved := spec
	resolved.PromptVersion = state.PromptVersion
	resolved.ToolsetVersion = state.ToolsetVersion
	explicitModel := spec.Model != ""
	if !explicitModel {
		resolved.Model = state.Model
		if resolved.Model == "" {
			resolved.Model = state.InitialModel
		}
		if resolved.Model == "" {
			return Spec{}, "", configError("Model", "session does not record a model; set a model")
		}
		resolved.Dialect = state.Dialect
		if resolved.Dialect == "" {
			resolved.Dialect = state.InitialDialect
		}
		resolved.BaseURL = state.BaseURL
		resolved.Limits = state.Limits
		if limitsSet(spec.Limits) {
			resolved.Limits = clampLimits(spec.Limits, state.Limits)
		}
		if resolved.Limits.MaxContextTokens <= 0 {
			return Spec{}, "", configError("Limits", "session does not record a context limit; supply an explicit model")
		}
		resolved.Pricing = state.Pricing
	}
	if spec.NewCompleter == nil {
		return Spec{}, "", configError("Model.Completer", "is required")
	}
	if explicitModel {
		if resolved.Dialect == "" {
			resolved.Dialect = state.Dialect
		}
		if resolved.BaseURL == "" {
			resolved.BaseURL = state.BaseURL
		}
		resolved.Limits = clampLimits(resolved.Limits, agent.Limits{})
	}
	// Thinking effort: nil inherits (or clears on an explicit model switch),
	// "" clears, a non-empty value sets.
	switch {
	case spec.ThinkingEffort == nil && explicitModel:
		resolved.ThinkingEffort = new("")
	case spec.ThinkingEffort == nil:
		resolved.ThinkingEffort = new(state.ThinkingEffort)
	case *spec.ThinkingEffort == "":
		resolved.ThinkingEffort = new("")
	default:
		resolved.ThinkingEffort = new(*spec.ThinkingEffort)
	}
	// Prompt: resume inherits; fork applies explicit overrides.
	prompt := state.SystemPrompt
	if prompt == "" {
		rendered, err := agent.RenderSystemPrompt(state.PromptVersion, state.Workspace, spec.Agents)
		if err != nil {
			return Spec{}, "", configError("SystemPrompt", "render recorded prompt: %v", err)
		}
		prompt = rendered
	}
	if fork {
		switch {
		case spec.SystemPromptSet && spec.AppendPromptSet:
			return Spec{}, "", configError("SystemPrompt", "must not be combined with Instructions")
		case spec.SystemPromptSet:
			prompt = agent.CapSystemPrompt(spec.SystemPrompt)
		case spec.AppendPromptSet:
			rendered, err := agent.RenderSystemPrompt(state.PromptVersion, state.Workspace, spec.Agents)
			if err != nil {
				return Spec{}, "", configError("SystemPrompt", "render prompt: %v", err)
			}
			prompt = agent.CapSystemPrompt(rendered + "\n\n" + spec.AppendPrompt)
		}
	}
	resolved.SystemPrompt = prompt
	resolved.SystemPromptSet = false
	resolved.AppendPrompt = ""
	resolved.AppendPromptSet = false
	// Tools: an explicit set is applied on fork and matched on resume. A nil
	// set inherits the recorded contract.
	recordedCustom := state.CustomTools
	if spec.CustomToolsSet {
		if err := agent.VerifyCustomTools(recordedCustom, spec.CustomTools); err != nil {
			if !fork {
				return Spec{}, "", err
			}
			if err := agent.ValidateCustomTools(spec.CustomTools); err != nil {
				return Spec{}, "", configError("Tools.Extra", "%v", err)
			}
		}
		resolved.CustomTools = cloneCustomTools(spec.CustomTools)
	} else {
		resolved.CustomTools = cloneCustomTools(recordedCustom)
	}
	if spec.GrantSet {
		if !fork && !sameGrantSet(spec.Grant, state.Tools, state.ToolsetVersion, recordedCustom) {
			return Spec{}, "", fmt.Errorf("%w: recorded grant %v, caller supplied %v", agent.ErrToolMismatch, state.Tools, spec.Grant)
		}
		resolved.Grant = cloneGrant(spec.Grant)
	} else {
		resolved.Grant = cloneGrant(state.Tools)
	}
	if !fork && !spec.CustomToolsSet && len(recordedCustom) > 0 {
		return Spec{}, "", fmt.Errorf("%w: session records %d custom tool(s) but none were supplied; supply matching Tools.Extra", agent.ErrToolMismatch, len(recordedCustom))
	}
	if err := validateToolsForVersion(resolved.Grant, resolved.CustomTools, resolved.ToolsetVersion, len(spec.Definitions) > 0); err != nil {
		return Spec{}, "", err
	}
	return resolved, prompt, nil
}

func resolvedNewPrompt(spec Spec, promptVersion, workspace string) string {
	switch {
	case spec.SystemPromptSet:
		return agent.CapSystemPrompt(spec.SystemPrompt)
	default:
		rendered, err := agent.RenderSystemPrompt(promptVersion, workspace, spec.Agents)
		if err != nil {
			rendered = workspace
		}
		if spec.AppendPromptSet {
			rendered += "\n\n" + spec.AppendPrompt
		}
		return agent.CapSystemPrompt(rendered)
	}
}

// validateContract validates a new session's prompt and tool contract.
func validateContract(spec Spec, toolsetVersion, promptVersion string) error {
	if spec.SystemPromptSet && spec.AppendPromptSet {
		return configError("SystemPrompt", "must not be combined with Instructions")
	}
	if spec.AppendPromptSet && len(spec.AppendPrompt) > 0 && promptVersion == "" {
		return configError("Instructions", "cannot be appended to an unknown prompt version")
	}
	if err := agent.ValidateCustomTools(spec.CustomTools); err != nil {
		return configError("Tools.Extra", "%v", err)
	}
	return validateToolsForVersion(spec.Grant, spec.CustomTools, toolsetVersion, len(spec.Definitions) > 0)
}

// validateToolsForVersion verifies that every granted name exists in the
// recorded built-in toolset or the custom set, and that delegate follows the
// delegation configuration.
func validateToolsForVersion(grant []string, custom []agent.CustomTool, toolsetVersion string, delegation bool) error {
	if err := agent.ValidateCustomTools(custom); err != nil {
		return configError("Tools.Extra", "%v", err)
	}
	if grant == nil {
		return nil
	}
	schemas, err := tools.SchemasFor(toolsetVersion)
	if err != nil {
		return configError("ToolsetVersion", "unsupported toolset version %q", toolsetVersion)
	}
	known := make(map[string]bool, len(schemas)+len(custom))
	for _, schema := range schemas {
		known[schema.Name] = true
	}
	for _, tool := range custom {
		known[tool.Schema.Name] = true
	}
	for _, name := range grant {
		if !known[name] {
			return configError("Tools", "tool %q is not defined by toolset %s", name, toolsetVersion)
		}
		if name == "delegate" && !delegation {
			return configError("Tools", "delegate requires a DelegationConfig")
		}
	}
	return nil
}

// limitsSet reports whether any explicit per-call limit was supplied.
func limitsSet(limits agent.Limits) bool {
	return limits.MaxContextTokens != 0 || limits.MaxOutputTokens != 0 ||
		limits.SafetyMargin != 0 || limits.MaxInputTokens != 0 ||
		limits.CompactionTarget != 0
}

func clampLimits(limits, recorded agent.Limits) agent.Limits {
	context := limits.MaxContextTokens
	if context <= 0 {
		context = recorded.MaxContextTokens
	}
	if recorded.MaxContextTokens > 0 && context > recorded.MaxContextTokens {
		context = recorded.MaxContextTokens
	}
	output := limits.MaxOutputTokens
	if output <= 0 {
		output = recorded.MaxOutputTokens
	}
	if context <= 0 {
		return limits
	}
	margin := agent.SafetyMargin(context)
	input := context - margin
	if limits.MaxInputTokens > 0 && limits.MaxInputTokens < input {
		input = limits.MaxInputTokens
	} else if recorded.MaxInputTokens > 0 && recorded.MaxInputTokens < input {
		input = recorded.MaxInputTokens
	}
	target := input
	if limits.CompactionTarget > 0 && limits.CompactionTarget < target {
		target = limits.CompactionTarget
	}
	return agent.Limits{
		MaxContextTokens: context,
		MaxOutputTokens:  output,
		SafetyMargin:     margin,
		MaxInputTokens:   input,
		CompactionTarget: target,
	}
}

func sameGrant(one, two []string) bool {
	if one == nil || two == nil {
		return one == nil && two == nil
	}
	if len(one) != len(two) {
		return false
	}
	for i := range one {
		if one[i] != two[i] {
			return false
		}
	}
	return true
}

// sameGrantSet compares an explicit caller grant with the recorded semantic
// grant. A recorded nil grant means every tool of the recorded version.
func sameGrantSet(grant, recorded []string, toolsetVersion string, custom []agent.CustomTool) bool {
	if recorded == nil {
		schemas, err := tools.SchemasFor(toolsetVersion)
		if err != nil {
			return false
		}
		want := make(map[string]bool, len(schemas)+len(custom))
		for _, schema := range schemas {
			want[schema.Name] = true
		}
		for _, tool := range custom {
			want[tool.Schema.Name] = true
		}
		if len(grant) != len(want) {
			return false
		}
		for _, name := range grant {
			if !want[name] {
				return false
			}
		}
		return true
	}
	return sameGrant(grant, recorded)
}

func sameCustomTools(one, two []agent.CustomTool) bool {
	return agent.VerifyCustomTools(two, one) == nil
}

func cloneGrant(names []string) []string {
	if names == nil {
		return nil
	}
	return append([]string{}, names...)
}

func cloneCustomTools(custom []agent.CustomTool) []agent.CustomTool {
	if len(custom) == 0 {
		return nil
	}
	out := make([]agent.CustomTool, len(custom))
	for i, tool := range custom {
		out[i] = agent.CustomTool{
			Schema: model.ToolSchema{
				Name:        tool.Schema.Name,
				Description: tool.Schema.Description,
				Parameters:  append([]byte(nil), tool.Schema.Parameters...),
			},
			ConcurrencySafe: tool.ConcurrencySafe,
		}
	}
	return out
}

func effortValue(effort *string, fallback string) string {
	if effort == nil {
		return fallback
	}
	return *effort
}

func newHost(spec Spec, store *session.Store, ws *tools.Workspace) *Host {
	workDir := filepath.Join(spec.StateDir, "work", store.ID())
	host := &Host{
		spec:      spec,
		store:     store,
		workspace: ws,
		workDir:   workDir,
		tmpDir:    filepath.Join(workDir, "tmp"),
	}
	if spec.ToolPolicy != nil {
		var mu sync.Mutex
		host.policy = func(ctx context.Context, req agent.ToolPolicyRequest) error {
			mu.Lock()
			defer mu.Unlock()
			return spec.ToolPolicy(ctx, req)
		}
	}
	return host
}

// ID returns the canonical session identifier.
func (h *Host) ID() string { return h.store.ID() }

// Path returns the session log path.
func (h *Host) Path() string { return h.store.Path() }

// Workspace returns the canonical absolute workspace directory.
func (h *Host) Workspace() string { return h.workspace.Dir() }

// StateDir returns the durable state root.
func (h *Host) StateDir() string { return h.spec.StateDir }

// Store exposes the session store to the engine's own runner wiring. The
// facade never exposes it.
func (h *Host) Store() *session.Store { return h.store }

// WorkspaceHandle exposes the workspace to the engine's own runner wiring.
func (h *Host) WorkspaceHandle() *tools.Workspace { return h.workspace }

// Spec returns a copy of the resolved construction spec.
func (h *Host) Spec() Spec { return h.spec }

// State reconstructs the durable session state. It is the caller's
// responsibility to ensure no run is active; the facade enforces ErrBusy.
func (h *Host) State() (agent.State, error) {
	if h.closed {
		return agent.State{}, configError("Agent", "is closed")
	}
	return agent.Inspect(h.store.Events())
}

// Snapshot reconstructs the durable session view, including timestamps and the
// first user message.
func (h *Host) Snapshot() (Snapshot, error) {
	if h.closed {
		return Snapshot{}, configError("Agent", "is closed")
	}
	return SnapshotFromEvents(h.store.ID(), h.store.Events())
}

// Close syncs and closes the store, closes the workspace, and removes the
// per-session work directory. It is idempotent.
func (h *Host) Close() error {
	if h.closed {
		return nil
	}
	h.closed = true
	storeErr := h.store.Close()
	wsErr := h.workspace.Close()
	if workErr := removeTree(h.workDir); workErr != nil && h.spec.Logger != nil {
		h.spec.Logger.Warn("cleanup leftover work directory", "path", h.workDir, "error", workErr.Error())
	}
	return errors.Join(storeErr, wsErr)
}

func normalizeSessionError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	case errors.Is(err, session.ErrLocked):
		return fmt.Errorf("%w: %w", ErrSessionLocked, err)
	case errors.Is(err, session.ErrInvalidID):
		return fmt.Errorf("%w: %w", ErrInvalidSessionID, err)
	default:
		return err
	}
}

// Engine sentinels map directly onto the public facade errors.
var (
	ErrSessionNotFound  = errors.New("session not found")
	ErrSessionLocked    = errors.New("session is locked")
	ErrInvalidSessionID = errors.New("invalid session id")
)

func prepareStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat state directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("state path %s is not a directory", dir)
	}
	file, err := os.CreateTemp(dir, ".aharness-preflight-*")
	if err != nil {
		return fmt.Errorf("state directory %s is not writable: %w", dir, err)
	}
	name := file.Name()
	_, writeErr := file.Write([]byte("aharness\n"))
	closeErr := file.Close()
	removeErr := os.Remove(name)
	if writeErr != nil {
		return fmt.Errorf("state directory %s is not writable: %w", dir, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("state directory %s is not writable: %w", dir, closeErr)
	}
	return removeErr
}

func openSessionWorkspace(requested, recorded string) (*tools.Workspace, error) {
	if requested == "" {
		requested = recorded
	}
	if requested == "" {
		return nil, configError("Workspace", "is required")
	}
	ws, err := tools.OpenWorkspace(requested)
	if err != nil {
		return nil, fmt.Errorf("open workspace %s: %w", requested, err)
	}
	if recorded != "" && ws.Dir() != recorded {
		_ = ws.Close()
		return nil, configError("Workspace", "%s does not match the session workspace %s", ws.Dir(), recorded)
	}
	return ws, nil
}
