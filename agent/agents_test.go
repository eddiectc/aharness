package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/tools"
)

const validDefinition = `name: reviewer
description: Reviews a change for correctness.
model: deepseek-v4.1-flash
thinking_effort: high
tools: read_file, search
max_turns: 12
---
Review the assigned change. Lead with findings.
`

func TestParseAgentDefinition(t *testing.T) {
	def, err := ParseAgentDefinition([]byte(validDefinition))
	if err != nil {
		t.Fatalf("ParseAgentDefinition: %v", err)
	}
	if def.Name != "reviewer" || def.Description != "Reviews a change for correctness." {
		t.Fatalf("name/description = %q/%q", def.Name, def.Description)
	}
	if def.Model != "deepseek-v4.1-flash" || def.ThinkingEffort != "high" || def.MaxTurns != 12 {
		t.Fatalf("settings = %+v", def)
	}
	if strings.Join(def.Tools, ",") != "read_file,search" {
		t.Fatalf("tools = %v", def.Tools)
	}
	if want := "Review the assigned change. Lead with findings.\n"; def.Prompt != want {
		t.Fatalf("prompt = %q, want %q", def.Prompt, want)
	}
}

func TestParseAgentDefinitionVariants(t *testing.T) {
	crlf := strings.ReplaceAll(validDefinition, "\n", "\r\n")
	noTrailing := strings.TrimSuffix(validDefinition, "\n")
	for _, tc := range []struct {
		name string
		data string
	}{
		{"crlf", crlf},
		{"no trailing newline", noTrailing},
		{"body blank line", validDefinition + "\nsecond paragraph\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAgentDefinition([]byte(tc.data)); err != nil {
				t.Fatalf("ParseAgentDefinition: %v", err)
			}
		})
	}
}

func TestParseAgentDefinitionRejects(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"empty file", "", "no ---"},
		{"missing separator", "name: x\ndescription: y\n", "no ---"},
		{"empty body", "name: x\ndescription: y\ntools: read_file\n---\n\n", "no prompt body"},
		{"missing name", "description: y\ntools: read_file\n---\nbody\n", "name is required"},
		{"missing description", "name: x\ntools: read_file\n---\nbody\n", "description is required"},
		{"missing tools", "name: x\ndescription: y\n---\nbody\n", "tools is required"},
		{"empty tools", "name: x\ndescription: y\ntools: ,\n---\nbody\n", "tools is required"},
		{"delegate tool", "name: x\ndescription: y\ntools: read_file, delegate\n---\nbody\n", "must not include delegate"},
		{"unknown tool", "name: x\ndescription: y\ntools: read_file, teleport\n---\nbody\n", "not defined by toolset"},
		{"unknown key", "name: x\ndescription: y\ntools: read_file\ncolour: red\n---\nbody\n", "unknown key"},
		{"duplicate key", "name: x\nname: y\ndescription: y\ntools: read_file\n---\nbody\n", "duplicate key"},
		{"bad name", "name: Reviewer\ndescription: y\ntools: read_file\n---\nbody\n", "must match"},
		{"max turns zero", "name: x\ndescription: y\ntools: read_file\nmax_turns: 0\n---\nbody\n", "must be positive"},
		{"max turns negative", "name: x\ndescription: y\ntools: read_file\nmax_turns: -2\n---\nbody\n", "must be positive"},
		{"max turns not a number", "name: x\ndescription: y\ntools: read_file\nmax_turns: many\n---\nbody\n", "not an integer"},
		{"line without colon", "name: x\ndescription: y\ntools: read_file\njunk\n---\nbody\n", "not `key: value`"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAgentDefinition([]byte(tc.data))
			if err == nil {
				t.Fatalf("ParseAgentDefinition accepted %q", tc.data)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestParseAgentDefinitionSizeCaps(t *testing.T) {
	huge := "name: x\ndescription: y\ntools: read_file\n---\n" + strings.Repeat("a", maxAgentDefinitionBytes)
	if _, err := ParseAgentDefinition([]byte(huge)); err == nil {
		t.Fatal("over-cap definition accepted")
	}
	if _, err := ParseAgentDefinition([]byte("name: x\xff\ndescription: y\n---\nbody\n")); err == nil {
		t.Fatal("invalid UTF-8 definition accepted")
	}
	bigPrompt := "name: x\ndescription: y\ntools: read_file\n---\n" + strings.Repeat("a", maxAgentPromptBytes+1)
	if _, err := ParseAgentDefinition([]byte(bigPrompt)); err == nil || !strings.Contains(err.Error(), "prompt body") {
		t.Fatalf("over-cap prompt error = %v", err)
	}
	bigDescription := "name: x\ndescription: " + strings.Repeat("d", maxAgentDescriptionSize+1) + "\ntools: read_file\n---\nbody\n"
	if _, err := ParseAgentDefinition([]byte(bigDescription)); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("over-cap description error = %v", err)
	}
}

func TestLoadAgentsBundledOnly(t *testing.T) {
	catalog := LoadAgents(t.TempDir())
	names := make([]string, 0, len(catalog.Agents))
	for _, def := range catalog.Agents {
		names = append(names, def.Name)
		if def.Source != agentSourceBundled {
			t.Errorf("%s source = %q, want bundled", def.Name, def.Source)
		}
		if !def.Valid() {
			t.Errorf("bundled %s is invalid: %s", def.Name, def.Err)
		}
		if len(def.Tools) == 0 {
			t.Errorf("bundled %s has no tools", def.Name)
		}
	}
	if got, want := strings.Join(names, ","), "explore,implementer,researcher,reviewer,task"; got != want {
		t.Fatalf("bundled names = %q, want %q", got, want)
	}
	if len(catalog.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", catalog.Warnings)
	}
	for _, def := range catalog.Agents {
		for _, name := range def.Tools {
			if name == "delegate" {
				t.Errorf("bundled %s grants delegate", def.Name)
			}
		}
	}
	task, ok := catalog.Lookup("task")
	if !ok || len(task.Tools) != len(tools.NamesWithout("delegate")) {
		t.Fatalf("task agent tools = %v", task.Tools)
	}
}

func TestLoadAgentsUserOverride(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "reviewer.md", `name: reviewer
description: A local reviewer.
model: local-model
thinking_effort: low
tools: read_file
---
Review locally.
`)
	catalog := LoadAgents(stateDir)
	def, ok := catalog.Lookup("reviewer")
	if !ok {
		t.Fatal("reviewer is missing")
	}
	if def.Source != agentSourceUser || !strings.HasSuffix(def.Path, "reviewer.md") {
		t.Fatalf("source/path = %q/%q", def.Source, def.Path)
	}
	if def.Model != "local-model" || def.ThinkingEffort != "low" || def.Prompt != "Review locally.\n" {
		t.Fatalf("override = %+v", def)
	}
	if len(catalog.Agents) != 5 {
		t.Fatalf("agents = %d, want the five bundled names", len(catalog.Agents))
	}
}

func TestLoadAgentsInvalidFileKeepsBundled(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "reviewer.md", "name: reviewer\ntools: read_file\n---\nno description\n")
	catalog := LoadAgents(stateDir)
	def, ok := catalog.Lookup("reviewer")
	if !ok || def.Source != agentSourceBundled {
		t.Fatalf("bundled reviewer was replaced by an invalid file: %+v", def)
	}
	if len(catalog.Invalid) != 1 {
		t.Fatalf("invalid = %d, want 1", len(catalog.Invalid))
	}
	if !strings.Contains(catalog.Invalid[0].Err, "description") {
		t.Fatalf("invalid error = %q", catalog.Invalid[0].Err)
	}
	if len(catalog.Warnings) != 1 || !strings.Contains(catalog.Warnings[0], "skip agent definition") {
		t.Fatalf("warnings = %v", catalog.Warnings)
	}
}

func TestLoadAgentsDuplicateNameFirstWins(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "a-first.md", "name: custom\ndescription: first\ntools: read_file\n---\nfirst\n")
	writeAgentFile(t, stateDir, "b-second.md", "name: custom\ndescription: second\ntools: read_file\n---\nsecond\n")
	catalog := LoadAgents(stateDir)
	def, ok := catalog.Lookup("custom")
	if !ok || def.Description != "first" {
		t.Fatalf("duplicate resolution = %+v", def)
	}
	found := false
	for _, warning := range catalog.Warnings {
		if strings.Contains(warning, "duplicate agent name") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want a duplicate-name warning", catalog.Warnings)
	}
}

func TestLoadAgentsCapsCount(t *testing.T) {
	stateDir := t.TempDir()
	for i := range maxAgents + 4 {
		name := "agent-" + strings.Repeat("a", 1) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		writeAgentFile(t, stateDir, name+".md", "name: "+name+"\ndescription: d\ntools: read_file\n---\nbody\n")
	}
	catalog := LoadAgents(stateDir)
	if len(catalog.Agents) != maxAgents {
		t.Fatalf("agents = %d, want the %d cap", len(catalog.Agents), maxAgents)
	}
	if len(catalog.Warnings) == 0 {
		t.Fatal("no warning for the truncated catalog")
	}
}

func TestLoadAgentsSkipsNonMarkdownAndDirectories(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "notes.txt", "not a definition")
	if err := os.Mkdir(filepath.Join(stateDir, "agents", "nested.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog := LoadAgents(stateDir)
	if len(catalog.Agents) != 5 {
		t.Fatalf("agents = %d, want the five bundled names", len(catalog.Agents))
	}
	if len(catalog.Invalid) != 1 || !strings.Contains(catalog.Invalid[0].Err, "not a regular file") {
		t.Fatalf("invalid = %+v", catalog.Invalid)
	}
}

func TestLoadAgentsUnreadableDirectory(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "agents"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := LoadAgents(stateDir)
	if len(catalog.Agents) != 5 {
		t.Fatalf("agents = %d, want the bundled set", len(catalog.Agents))
	}
	if len(catalog.Warnings) == 0 {
		t.Fatal("no warning for an unreadable agent directory")
	}
}

func TestLoadAgentsReadOnlyStateDir(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "reviewer.md", "name: reviewer\ndescription: A local reviewer.\ntools: read_file\n---\nReview locally.\n")
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
	catalog := LoadAgents(stateDir)
	if _, ok := catalog.Lookup("reviewer"); !ok {
		t.Fatalf("read-only state directory did not load the user definition: %+v", catalog)
	}
	// Loading never writes: the directory still has no new entries.
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("state directory entries = %d, want only the agents directory", len(entries))
	}
}

func writeAgentFile(t *testing.T, stateDir, name, content string) {
	t.Helper()
	dir := filepath.Join(stateDir, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
