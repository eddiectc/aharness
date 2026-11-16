package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/tools"
)

func settingsStr(value string) *string { return &value }
func settingsInt(value int) *int       { return &value }
func settingsBool(value bool) *bool    { return &value }

func settingsCatalog() *agent.AgentCatalog {
	return &agent.AgentCatalog{
		Agents: []agent.AgentDefinition{
			{
				Name: "explore", Description: "Explore the repository.", Source: "bundled",
				Tools: []string{"read_file", "search"}, Prompt: "Explore.\nSecond line.",
			},
			{
				Name: "reviewer", Description: "Review a change.", Source: "user",
				Path: "/state/agents/reviewer.md", Model: "file-model", ThinkingEffort: "high",
				Tools: []string{"read_file"}, MaxTurns: 12, Prompt: "Review.\nSecond line.",
			},
		},
		Invalid: []agent.AgentDefinition{{
			Name: "broken.md", Source: "user", Path: "/state/agents/broken.md",
			Err: "description is required",
		}},
	}
}

func TestAgentViewsProvenance(t *testing.T) {
	base := settingsCatalog()
	effective, err := agent.ApplyAgentOverrides(base, map[string]agent.AgentOverride{
		"reviewer": {
			Model:    settingsStr("new-model"),
			MaxTurns: settingsInt(3),
			Enabled:  settingsBool(false),
		},
	})
	if err != nil {
		t.Fatalf("ApplyAgentOverrides: %v", err)
	}
	views := agentViews(effective)
	byName := make(map[string]int, len(views))
	for i, view := range views {
		byName[view.Name] = i
	}
	reviewer := views[byName["reviewer"]]
	if reviewer.Model != "new-model" || reviewer.ModelSource != "override" {
		t.Fatalf("model = %q/%q, want new-model/override", reviewer.Model, reviewer.ModelSource)
	}
	if reviewer.Effort != "" || reviewer.EffortSource != "model default" {
		t.Fatalf("effort = %q/%q, want the model default after a model switch",
			reviewer.Effort, reviewer.EffortSource)
	}
	if reviewer.MaxTurns != 3 || reviewer.MaxTurnsSource != "override" {
		t.Fatalf("max_turns = %d/%q, want 3/override", reviewer.MaxTurns, reviewer.MaxTurnsSource)
	}
	if reviewer.DeclaredModel != "file-model" || reviewer.DeclaredEffort != "high" || reviewer.DeclaredMaxTurns != 12 {
		t.Fatalf("declared values = %+v", reviewer)
	}
	if reviewer.Override == nil || reviewer.Override.Model == nil || *reviewer.Override.Model != "new-model" {
		t.Fatalf("override = %+v", reviewer.Override)
	}
	if reviewer.Enabled || reviewer.Status != "disabled" {
		t.Fatalf("enabled/status = %t/%q, want false/disabled", reviewer.Enabled, reviewer.Status)
	}
	if strings.Join(reviewer.Tools, ",") != "read_file" || reviewer.PromptFirstLine != "Review." {
		t.Fatalf("grant or prompt changed: %+v", reviewer)
	}

	explore := views[byName["explore"]]
	if explore.Model != "" || explore.ModelSource != "inherit" ||
		explore.Effort != "" || explore.EffortSource != "inherit" ||
		explore.MaxTurns != 0 || explore.MaxTurnsSource != "inherit" {
		t.Fatalf("unoverridden provenance = %+v", explore)
	}
	if !explore.Enabled || explore.Status != "active" {
		t.Fatalf("unoverridden enabled/status = %t/%q", explore.Enabled, explore.Status)
	}

	broken := views[byName["broken.md"]]
	if broken.Status != "invalid" || broken.Err == "" {
		t.Fatalf("invalid row = %+v", broken)
	}
	if got := agentViews(nil); got != nil {
		t.Fatalf("nil catalog views = %+v, want nil", got)
	}
}

func TestAgentViewsEffortOnlyOverride(t *testing.T) {
	effective, err := agent.ApplyAgentOverrides(settingsCatalog(), map[string]agent.AgentOverride{
		"reviewer": {ThinkingEffort: settingsStr("low")},
	})
	if err != nil {
		t.Fatalf("ApplyAgentOverrides: %v", err)
	}
	for _, view := range agentViews(effective) {
		if view.Name != "reviewer" {
			continue
		}
		if view.Model != "file-model" || view.ModelSource != "definition" {
			t.Fatalf("model = %q/%q, want the definition value", view.Model, view.ModelSource)
		}
		if view.Effort != "low" || view.EffortSource != "override" {
			t.Fatalf("effort = %q/%q, want low/override", view.Effort, view.EffortSource)
		}
		return
	}
	t.Fatal("reviewer missing from the views")
}

func TestTUIHostSavesAgentOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	base := agent.LoadAgents(dir)
	host := &tuiHost{cfg: config{ConfigPath: path}, baseAgents: base}

	if err := host.SetAgentOverride("reviewer", agent.AgentOverride{
		MaxTurns: settingsInt(4), Enabled: settingsBool(false),
	}); err != nil {
		t.Fatalf("SetAgentOverride: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 600", perm)
	}
	loaded, err := loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	reviewer := loaded.AgentOverrides["reviewer"]
	if reviewer.MaxTurns == nil || *reviewer.MaxTurns != 4 || reviewer.Enabled == nil || *reviewer.Enabled {
		t.Fatalf("saved reviewer override = %+v", reviewer)
	}
	disabled := false
	for _, view := range host.Agents() {
		if view.Name == "reviewer" {
			if view.Status != "disabled" || view.Enabled {
				t.Fatalf("reviewer view = %+v, want disabled", view)
			}
			disabled = true
		}
	}
	if !disabled {
		t.Fatal("reviewer missing from the browser view")
	}

	// A second save preserves the first entry.
	if err := host.SetAgentOverride("explore", agent.AgentOverride{Model: settingsStr("explore-model")}); err != nil {
		t.Fatalf("second SetAgentOverride: %v", err)
	}
	loaded, err = loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(loaded.AgentOverrides) != 2 {
		t.Fatalf("overrides = %+v, want reviewer and explore", loaded.AgentOverrides)
	}
	if loaded.AgentOverrides["explore"].Model == nil || *loaded.AgentOverrides["explore"].Model != "explore-model" {
		t.Fatalf("explore override = %+v", loaded.AgentOverrides["explore"])
	}
	if loaded.AgentOverrides["reviewer"].MaxTurns == nil || *loaded.AgentOverrides["reviewer"].MaxTurns != 4 {
		t.Fatalf("reviewer override lost on merge: %+v", loaded.AgentOverrides["reviewer"])
	}

	// Clearing one agent leaves the other entry intact.
	if err := host.ClearAgentOverrides("reviewer"); err != nil {
		t.Fatalf("ClearAgentOverrides: %v", err)
	}
	loaded, err = loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := loaded.AgentOverrides["reviewer"]; ok {
		t.Fatalf("reviewer override survived a clear: %+v", loaded.AgentOverrides)
	}
	if _, ok := loaded.AgentOverrides["explore"]; !ok {
		t.Fatalf("explore override lost on clear: %+v", loaded.AgentOverrides)
	}

	// An unknown agent is rejected without writing.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := host.SetAgentOverride("nobody", agent.AgentOverride{}); err == nil {
		t.Fatal("unknown agent accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("a rejected override changed the config file")
	}

	// A disabled config path is an actionable error.
	empty := &tuiHost{baseAgents: base}
	if err := empty.SetAgentOverride("reviewer", agent.AgentOverride{}); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("empty config path error = %v, want a disabled-file message", err)
	}
}

func TestTUIHostRefusesWorkspaceConfig(t *testing.T) {
	workspace := t.TempDir()
	ws, err := tools.OpenWorkspace(workspace)
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	path := filepath.Join(workspace, "config.json")
	host := &tuiHost{
		cfg:        config{ConfigPath: path},
		baseAgents: agent.LoadAgents(t.TempDir()),
		ws:         ws,
	}
	err = host.SetAgentOverride("reviewer", agent.AgentOverride{Model: settingsStr("m")})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("workspace config save = %v, want a refusal", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("workspace config file was created: %v", statErr)
	}
}

func TestTUIHostRefusesSymlinkedWorkspaceConfig(t *testing.T) {
	outside := t.TempDir()
	workspace := t.TempDir()
	link := filepath.Join(outside, "link")
	if err := os.Symlink(workspace, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ws, err := tools.OpenWorkspace(workspace)
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	host := &tuiHost{
		cfg:        config{ConfigPath: filepath.Join(link, "config.json")},
		baseAgents: agent.LoadAgents(t.TempDir()),
		ws:         ws,
	}
	if err := host.SetAgentOverride("reviewer", agent.AgentOverride{Model: settingsStr("m")}); err == nil ||
		!strings.Contains(err.Error(), "workspace") {
		t.Fatalf("symlinked workspace config save = %v, want a refusal", err)
	}
}

func TestTUIHostSaveRejectsBrokenConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"unknown_key": true}`), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	host := &tuiHost{cfg: config{ConfigPath: path}, baseAgents: agent.LoadAgents(dir)}
	err := host.SetAgentOverride("reviewer", agent.AgentOverride{Model: settingsStr("m")})
	if err == nil || !strings.Contains(err.Error(), "unknown_key") {
		t.Fatalf("save override = %v, want the broken-file error", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if string(raw) != `{"unknown_key": true}` {
		t.Fatalf("broken config was rewritten: %s", raw)
	}
}
