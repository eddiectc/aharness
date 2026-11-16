package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/internal/agent"
	"github.com/eddiectc/aharness/internal/session"
	"github.com/eddiectc/aharness/internal/tools"
)

func TestMaxSubagentsConfig(t *testing.T) {
	t.Parallel()
	cfg := cfgTestParseOK(t, cfgTestArgs(t), nil)
	if cfg.MaxSubagents != defaultMaxSubagents || cfg.maxSubagentsSet {
		t.Fatalf("default max subagents = %d (set %v), want %d", cfg.MaxSubagents, cfg.maxSubagentsSet, defaultMaxSubagents)
	}

	cfg = cfgTestParseOK(t, cfgTestArgs(t, "-max-subagents=3"), nil)
	if cfg.MaxSubagents != 3 || !cfg.maxSubagentsSet {
		t.Fatalf("max subagents = %d (set %v), want 3", cfg.MaxSubagents, cfg.maxSubagentsSet)
	}

	cfg = cfgTestParseOK(t, cfgTestArgs(t), map[string]string{"AHARNESS_MAX_SUBAGENTS": "0"})
	if cfg.MaxSubagents != 0 || !cfg.maxSubagentsSet {
		t.Fatalf("env max subagents = %d (set %v), want unlimited", cfg.MaxSubagents, cfg.maxSubagentsSet)
	}

	if err := cfgTestParseErr(t, cfgTestArgs(t, "-max-subagents=-1"), nil); !strings.Contains(err.Error(), "max subagents must not be negative") {
		t.Fatalf("negative flag error = %v", err)
	}
	if err := cfgTestParseErr(t, cfgTestArgs(t), map[string]string{"AHARNESS_MAX_SUBAGENTS": "-2"}); !strings.Contains(err.Error(), "max subagents must not be negative") {
		t.Fatalf("negative env error = %v", err)
	}
}

func TestMaxSubagentsConfigFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"max_subagents": 5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := cfgTestParseOK(t, cfgTestArgs(t, "-config", path), nil)
	if cfg.MaxSubagents != 5 {
		t.Fatalf("max subagents = %d, want 5 from the file", cfg.MaxSubagents)
	}
	if err := os.WriteFile(path, []byte(`{"max_subagents": -1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfgTestParseErr(t, cfgTestArgs(t, "-config", path), nil); !strings.Contains(err.Error(), "max_subagents must not be negative") {
		t.Fatalf("negative file error = %v", err)
	}
}

func TestBatchGrantExcludesDelegate(t *testing.T) {
	t.Parallel()
	grant := batchTools(config{Mode: ModeBatch, ToolsetVersion: tools.Version})
	if slices.Contains(grant, "delegate") {
		t.Fatalf("batch grant includes delegate: %v", grant)
	}
	if len(grant) != len(tools.NamesWithout("delegate")) {
		t.Fatalf("batch grant = %v, want every current tool except delegate", grant)
	}

	if got := batchTools(config{Mode: ModeTUI, ToolsetVersion: tools.Version}); got != nil {
		t.Fatalf("tui grant = %v, want nil (all tools of the version)", got)
	}

	// A resumed old session keeps its frozen schema set; a current-only name
	// must not leak into its grant.
	old := batchTools(config{Mode: ModeBatch, ToolsetVersion: "toolset-v0"})
	if len(old) != 4 || slices.Contains(old, "web_search") || slices.Contains(old, "delegate") {
		t.Fatalf("toolset-v0 batch grant = %v", old)
	}

	recorded := batchTools(config{Mode: ModeBatch, ToolsetVersion: tools.Version, Tools: []string{"read_file"}})
	if len(recorded) != 1 || recorded[0] != "read_file" {
		t.Fatalf("recorded batch grant = %v, want the recorded list", recorded)
	}
}

func TestRunnerConfigRecordsTheAgentContract(t *testing.T) {
	t.Parallel()
	cfg := config{
		Provider: "opencode-go", Model: "m", Dialect: "openai-chat",
		PromptVersion: agent.PromptVersion, ToolsetVersion: tools.Version,
		SystemPrompt: "child prompt", Agent: "explore", Tools: []string{"read_file"},
		Agents:   []agent.AgentInfo{{Name: "explore", Description: "explore"}},
		MaxTurns: 4,
	}
	rcfg := runnerConfig(cfg)
	if rcfg.SystemPrompt != "child prompt" || rcfg.Agent != "explore" {
		t.Fatalf("run config = %+v", rcfg)
	}
	if len(rcfg.Tools) != 1 || rcfg.Tools[0] != "read_file" {
		t.Fatalf("run config grant = %v", rcfg.Tools)
	}
	if len(rcfg.Agents) != 1 || rcfg.Agents[0].Name != "explore" {
		t.Fatalf("run config catalog = %v", rcfg.Agents)
	}
}

func TestResumePickerExcludesChildren(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	workspace := t.TempDir()
	parent := createTestSession(t, stateDir, workspace, "model-a", "", "parent task")

	cfg := testSessionConfig(t, stateDir, workspace, "model-a")
	cfg.Agent = "explore"
	cfg.Tools = []string{"read_file"}
	child := session.NewID()
	store, err := session.Create(stateDir, child)
	if err != nil {
		t.Fatalf("create child session: %v", err)
	}
	parentRef := &agent.ParentRef{Session: parent, Seq: 1, CallID: "call-1"}
	if err := agent.WriteSessionStart(store, runnerConfig(cfg), parentRef); err != nil {
		_ = store.Close()
		t.Fatalf("write child session start: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close child session: %v", err)
	}

	choices, err := listPreviousSessions(stateDir, workspace, "")
	if err != nil {
		t.Fatalf("listPreviousSessions: %v", err)
	}
	if len(choices) != 1 || choices[0].ID != parent {
		t.Fatalf("choices = %+v, want only the parent session", choices)
	}

	// The child is still addressable by ID.
	state, err := agent.Inspect(readSessionEvents(t, session.SessionPath(stateDir, child)))
	if err != nil {
		t.Fatalf("Inspect(child): %v", err)
	}
	if state.ParentSession != parent || state.ParentCallID != "call-1" || state.Agent != "explore" {
		t.Fatalf("child state = %+v", state)
	}
}

func TestSessionGrantNarrowsPreDelegateSessions(t *testing.T) {
	t.Parallel()
	// A recorded grant always wins.
	if got := sessionGrant([]string{"read_file"}, tools.Version); len(got) != 1 || got[0] != "read_file" {
		t.Fatalf("recorded grant = %v", got)
	}
	// A toolset that declares delegate needs no narrowing.
	if got := sessionGrant(nil, tools.Version); got != nil {
		t.Fatalf("current toolset grant = %v, want nil (no narrowing)", got)
	}
	// An older toolset cannot reach delegate at dispatch.
	for _, version := range []string{"toolset-v0", "toolset-v1"} {
		grant := sessionGrant(nil, version)
		if len(grant) == 0 || slices.Contains(grant, "delegate") {
			t.Fatalf("%s grant = %v, want the version's tools without delegate", version, grant)
		}
		if version == "toolset-v0" && !slices.Contains(grant, "read_file") {
			t.Fatalf("toolset-v0 grant = %v", grant)
		}
	}
}
