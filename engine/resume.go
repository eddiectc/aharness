package engine

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

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
	resolved, prompt, appended, err := resolveInherited(spec, state, false)
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
	return newHost(resolved, store, ws, appended), nil
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
	resolved, prompt, appended, err := resolveInherited(spec, parentState, true)
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
		rendered, renderErr := agent.RenderSystemPrompt(parentState.PromptVersion, parentState.Workspace, spec.Identity, spec.Agents)
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
		Identity:         resolved.Identity,
		PromptVersion:    resolved.PromptVersion,
		PromptTag:        resolved.PromptTag,
		ToolsetVersion:   resolved.ToolsetVersion,
		HarnessVersion:   resolved.Version,
		SystemPrompt:     prompt,
		AppendedPrompt:   appended,
		Agent:            resolved.Agent,
		Tools:            cloneGrant(resolved.Grant),
		RootTools:        cloneGrant(resolved.RootGrant),
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
	return newHost(resolved, store, ws, appended), nil
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
