package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

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
	if strings.TrimSpace(spec.UserAgent) == "" {
		return nil, configError("UserAgent", "is required")
	}
	switch {
	case spec.ToolOptions.UserAgent == "":
		spec.ToolOptions.UserAgent = spec.UserAgent
	case spec.ToolOptions.UserAgent != spec.UserAgent:
		return nil, configError("ToolOptions.UserAgent", "must match UserAgent")
	}
	if spec.MaxSubagents < -1 {
		return nil, configError("MaxSubagents", "must be -1 or greater")
	}
	spec.Identity = strings.TrimSpace(spec.Identity)
	if spec.Identity == "" {
		spec.Identity = "aharness"
	}
	if err := validateIdentity(spec.Identity); err != nil {
		return nil, err
	}
	if err := validatePromptTag(spec.PromptTag); err != nil {
		return nil, err
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

// validateIdentity enforces the product-name shape: valid UTF-8, at most 64
// bytes, and no control characters. A product name is not arbitrary prompt
// text.
func validateIdentity(identity string) error {
	if !utf8.ValidString(identity) {
		return configError("Identity", "must be valid UTF-8")
	}
	if len(identity) > 64 {
		return configError("Identity", "must be at most 64 bytes")
	}
	for _, r := range identity {
		if unicode.IsControl(r) {
			return configError("Identity", "must not contain control characters")
		}
	}
	return nil
}

// validatePromptTag enforces the attribution-tag shape: empty means unset;
// otherwise at most 64 bytes, each a printable ASCII character in
// [A-Za-z0-9._:/-]. A tag is not prompt text.
func validatePromptTag(tag string) error {
	if tag == "" {
		return nil
	}
	if len(tag) > 64 {
		return configError("PromptTag", "must be at most 64 bytes")
	}
	for i := 0; i < len(tag); i++ {
		switch b := tag[i]; {
		case b >= 'A' && b <= 'Z',
			b >= 'a' && b <= 'z',
			b >= '0' && b <= '9',
			b == '.', b == '_', b == ':', b == '/', b == '-':
		default:
			return configError("PromptTag", "must contain only printable ASCII matching [A-Za-z0-9._:/-]")
		}
	}
	return nil
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
	if spec.Limits.MaxOutputTokens < 0 {
		return nil, configError("Limits.MaxOutputTokens", "must not be negative")
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
	appended := ""
	if spec.AppendPromptSet {
		appended = spec.AppendPrompt
	}
	cfg := agent.Config{
		Provider:       spec.Provider,
		Dialect:        spec.Dialect,
		Model:          spec.Model,
		BaseURL:        spec.BaseURL,
		Workspace:      ws.Dir(),
		StateDir:       spec.StateDir,
		Identity:       spec.Identity,
		PromptVersion:  promptVersion,
		PromptTag:      spec.PromptTag,
		ToolsetVersion: toolsetVersion,
		HarnessVersion: spec.Version,
		SystemPrompt:   prompt,
		AppendedPrompt: appended,
		Agent:          spec.Agent,
		Tools:          cloneGrant(spec.Grant),
		RootTools:      cloneGrant(spec.RootGrant),
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
	spec.RootGrant = cloneGrant(spec.RootGrant)
	spec.CustomTools = cloneCustomTools(spec.CustomTools)
	return newHost(spec, store, ws, appended), nil
}

func resolvedNewPrompt(spec Spec, promptVersion, workspace string) string {
	switch {
	case spec.SystemPromptSet:
		return agent.CapSystemPrompt(spec.SystemPrompt)
	default:
		rendered, err := agent.RenderSystemPrompt(promptVersion, workspace, spec.Identity, spec.Agents)
		if err != nil {
			rendered = workspace
		}
		if spec.AppendPromptSet {
			rendered += "\n\n" + spec.AppendPrompt
		}
		return agent.CapSystemPrompt(rendered)
	}
}

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
