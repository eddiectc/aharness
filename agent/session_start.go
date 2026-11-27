package agent

import (
	"errors"
	"fmt"

	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// sessionWriter is the write side needed to start a session.
type sessionWriter interface {
	Append(session.Event) error
	Sync() error
	Events() []session.Event
}

// WriteSessionStart writes and syncs the once-per-session session_start event
// for a new or forked session. The store must have no events.
func WriteSessionStart(store sessionWriter, cfg Config, parent *ParentRef) error {
	if len(store.Events()) != 0 {
		return errors.New("start session: session already has events")
	}
	data := sessionStartData{
		PromptVersion:    promptVersion(cfg),
		PromptTag:        cfg.PromptTag,
		ToolsetVersion:   toolsetVersion(cfg),
		Workspace:        cfg.Workspace,
		StateDir:         cfg.StateDir,
		Provider:         cfg.Provider,
		Dialect:          cfg.Dialect,
		Model:            cfg.Model,
		BaseURL:          cfg.BaseURL,
		Limits:           cfg.Limits,
		MaxTurns:         cfg.MaxTurns,
		MaxTotalTokens:   cfg.MaxTotalTokens,
		SessionTimeoutMS: cfg.SessionTimeout.Milliseconds(),
		ModelTimeoutMS:   cfg.ModelTimeout.Milliseconds(),
		HarnessVersion:   cfg.HarnessVersion,
		ThinkingEffort:   cfg.ThinkingEffort,
		SystemPrompt:     systemPromptText(cfg),
		AppendedPrompt:   capText(cfg.AppendedPrompt, maxSystemPromptBytes),
		Agent:            cfg.Agent,
		Tools:            cloneGrant(cfg.Tools),
		RootTools:        cloneGrant(cfg.RootTools),
		CustomTools:      recordedCustomTools(cfg.CustomTools),
		ContractOverride: cfg.ContractOverride,
	}
	if parent != nil {
		data.ParentSession = parent.Session
		data.ParentSeq = parent.Seq
		data.ParentCallID = parent.CallID
	}
	ev, err := session.NewEvent(session.TypeSessionStart, 0, "", data)
	if err != nil {
		return fmt.Errorf("build session_start event: %w", err)
	}
	if err := store.Append(ev); err != nil {
		return fmt.Errorf("append session_start event: %w", err)
	}
	return store.Sync()
}

func promptVersion(cfg Config) string {
	if cfg.PromptVersion != "" {
		return cfg.PromptVersion
	}
	return PromptVersion
}

func toolsetVersion(cfg Config) string {
	if cfg.ToolsetVersion != "" {
		return cfg.ToolsetVersion
	}
	return tools.Version
}

// StartSession writes the once-per-session session_start event for a new or
// forked session and syncs it. It must be called before Run and only when the
// store has no events.
func (r *Runner) StartSession(parent *ParentRef) error {
	return WriteSessionStart(r.store, r.cfg, parent)
}
