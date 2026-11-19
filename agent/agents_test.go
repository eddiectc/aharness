package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

func TestLoadAgentsFixtureBase(t *testing.T) {
	base := testAgentDefinitions()
	catalog := LoadAgents(base, t.TempDir())
	names := make([]string, 0, len(catalog.Agents))
	for _, def := range catalog.Agents {
		names = append(names, def.Name)
		if def.Source != SourceDefault {
			t.Errorf("%s source = %q, want default", def.Name, def.Source)
		}
		if def.Path != "" {
			t.Errorf("%s path = %q, want empty for a base default", def.Name, def.Path)
		}
		if !def.Valid() {
			t.Errorf("base %s is invalid: %s", def.Name, def.Err)
		}
		if len(def.Tools) == 0 {
			t.Errorf("base %s has no tools", def.Name)
		}
	}
	want := make([]string, 0, len(base))
	for _, def := range base {
		want = append(want, def.Name)
	}
	slices.Sort(want)
	if got := strings.Join(names, ","); got != strings.Join(want, ",") {
		t.Fatalf("base names = %q, want %q", got, strings.Join(want, ","))
	}
	if len(catalog.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", catalog.Warnings)
	}
	for _, def := range catalog.Agents {
		for _, name := range def.Tools {
			if name == "delegate" {
				t.Errorf("base %s grants delegate", def.Name)
			}
		}
	}
}

// testAgentDefinitions returns the package's host-supplied fixture set. It is
// deliberately short and synthetic: tests need a valid base, not role text. The
// declaration order is not alphabetical so the tests can prove LoadAgents sorts
// the catalog.
func testAgentDefinitions() []AgentDefinition {
	return []AgentDefinition{
		{
			Name:        "writer",
			Description: "Test-only writer.",
			Tools:       []string{"read_file", "edit_file"},
			Prompt:      "Write the assigned test output.\n",
		},
		{
			Name:        "explore",
			Description: "Test-only read-only explorer.",
			Tools:       []string{"read_file", "search"},
			Prompt:      "Look at the assigned test input and report.\n",
		},
		{
			Name:        "implementer",
			Description: "Test-only writing agent.",
			Tools:       []string{"read_file", "edit_file", "search", "run_command"},
			Prompt:      "Make the assigned test edit and report.\n",
		},
		{
			Name:        "reader",
			Description: "Test-only reader.",
			Tools:       []string{"read_file"},
			Prompt:      "Read the assigned test input.\n",
		},
	}
}

func TestAgentSourceConstants(t *testing.T) {
	if SourceDefault != "default" || SourceUser != "user" {
		t.Fatalf("sources = %q/%q, want default/user", SourceDefault, SourceUser)
	}
}

func testBaseAgent(name, description string) AgentDefinition {
	return AgentDefinition{
		Name:        name,
		Description: description,
		Tools:       []string{"read_file"},
		Prompt:      "Base body.\n",
	}
}

func TestLoadAgentsBase(t *testing.T) {
	fileDefinition := func(name, description string) string {
		return "name: " + name + "\ndescription: " + description + "\ntools: read_file\n---\nFile body.\n"
	}
	stale := testBaseAgent("reviewer", "A base reviewer.")
	stale.Source = SourceUser
	stale.Path = "/stale/reviewer.md"
	tests := []struct {
		name           string
		base           []AgentDefinition
		files          map[string]string
		wantNames      []string
		wantSources    map[string]string
		wantInvalid    int
		wantNoWarnings bool
		wantWarnings   []string
	}{
		{
			name:           "nil base",
			wantNoWarnings: true,
		},
		{
			name:           "empty base",
			base:           []AgentDefinition{},
			wantNoWarnings: true,
		},
		{
			name:           "base only",
			base:           []AgentDefinition{stale},
			wantNames:      []string{"reviewer"},
			wantSources:    map[string]string{"reviewer": SourceDefault},
			wantNoWarnings: true,
		},
		{
			name:        "base replaced by same-name file",
			base:        []AgentDefinition{testBaseAgent("reviewer", "A base reviewer.")},
			files:       map[string]string{"reviewer.md": fileDefinition("reviewer", "A file reviewer.")},
			wantNames:   []string{"reviewer"},
			wantSources: map[string]string{"reviewer": SourceUser},
		},
		{
			name: "invalid base entry",
			base: []AgentDefinition{
				testBaseAgent("explore", "Explore."),
				{Name: "broken", Tools: []string{"read_file"}, Prompt: "Body."},
			},
			wantNames:    []string{"explore"},
			wantSources:  map[string]string{"explore": SourceDefault},
			wantInvalid:  1,
			wantWarnings: []string{"skip agent definition broken: description is required"},
		},
		{
			name:        "invalid base entry does not block same-name file",
			base:        []AgentDefinition{{Name: "reviewer", Tools: []string{"read_file"}, Prompt: "Body."}},
			files:       map[string]string{"reviewer.md": fileDefinition("reviewer", "A file reviewer.")},
			wantNames:   []string{"reviewer"},
			wantSources: map[string]string{"reviewer": SourceUser},
			wantInvalid: 1,
			wantWarnings: []string{
				"skip agent definition reviewer: description is required",
			},
		},
		{
			name: "duplicate base name first wins",
			base: []AgentDefinition{
				testBaseAgent("reviewer", "First."),
				testBaseAgent("reviewer", "Second."),
			},
			wantNames:    []string{"reviewer"},
			wantSources:  map[string]string{"reviewer": SourceDefault},
			wantWarnings: []string{"duplicate agent name", "the first definition wins"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			for name, content := range tc.files {
				writeAgentFile(t, stateDir, name, content)
			}
			catalog := LoadAgents(tc.base, stateDir)
			names := make([]string, 0, len(catalog.Agents))
			for _, def := range catalog.Agents {
				names = append(names, def.Name)
				if want := tc.wantSources[def.Name]; def.Source != want {
					t.Errorf("%s source = %q, want %q", def.Name, def.Source, want)
				}
				if def.Source == SourceDefault && def.Path != "" {
					t.Errorf("%s default path = %q, want empty", def.Name, def.Path)
				}
				if def.Source == SourceUser && def.Path == "" {
					t.Errorf("%s user path is empty", def.Name)
				}
				if !def.Valid() {
					t.Errorf("%s is invalid: %s", def.Name, def.Err)
				}
			}
			if strings.Join(names, ",") != strings.Join(tc.wantNames, ",") {
				t.Errorf("names = %v, want %v", names, tc.wantNames)
			}
			if len(catalog.Invalid) != tc.wantInvalid {
				t.Errorf("invalid = %d, want %d: %+v", len(catalog.Invalid), tc.wantInvalid, catalog.Invalid)
			}
			for _, def := range catalog.Invalid {
				if def.Err == "" {
					t.Errorf("invalid entry %q has no error", def.Name)
				}
				if def.Path == "" && def.Source != SourceDefault {
					t.Errorf("invalid base entry %q source = %q, want default", def.Name, def.Source)
				}
				if def.Path != "" && def.Source != SourceUser {
					t.Errorf("invalid file %q source = %q, want user", def.Path, def.Source)
				}
			}
			if tc.wantNoWarnings && len(catalog.Warnings) != 0 {
				t.Errorf("warnings = %v, want none", catalog.Warnings)
			}
			for _, want := range tc.wantWarnings {
				if !warningContains(catalog.Warnings, want) {
					t.Errorf("warnings = %v, want one containing %q", catalog.Warnings, want)
				}
			}
		})
	}
}

func TestLoadAgentsCopiesBase(t *testing.T) {
	model := "base-model"
	base := []AgentDefinition{testBaseAgent("reviewer", "A base reviewer.")}
	base[0].Override = &AgentOverride{Model: &model}

	catalog := LoadAgents(base, t.TempDir())
	def, ok := catalog.Lookup("reviewer")
	if !ok {
		t.Fatal("base reviewer is missing")
	}
	if def.Description != "A base reviewer." || len(def.Tools) != 1 || def.Tools[0] != "read_file" {
		t.Fatalf("catalog copy = %+v", def)
	}

	// Mutating the caller's slice, its tool slice, or its override must not
	// change the loaded catalog.
	base[0].Name = "renamed"
	base[0].Description = "changed"
	base[0].Tools[0] = "search"
	*base[0].Override.Model = "other-model"
	if _, ok := catalog.Lookup("renamed"); ok {
		t.Fatal("catalog aliased the caller's name")
	}
	def, ok = catalog.Lookup("reviewer")
	if !ok {
		t.Fatal("catalog lost the original name")
	}
	if def.Description != "A base reviewer." || def.Tools[0] != "read_file" {
		t.Fatalf("catalog aliased the caller's definition: %+v", def)
	}
	if def.Override == nil || def.Override.Model == nil || *def.Override.Model != "base-model" {
		t.Fatalf("catalog aliased the caller's override: %+v", def.Override)
	}
}

func TestLoadAgentsCapsBase(t *testing.T) {
	base := make([]AgentDefinition, 0, maxAgents+4)
	for i := range maxAgents + 4 {
		base = append(base, testBaseAgent(fmt.Sprintf("agent-%02d", i), "Base."))
	}
	catalog := LoadAgents(base, t.TempDir())
	if len(catalog.Agents) != maxAgents {
		t.Fatalf("agents = %d, want the %d cap", len(catalog.Agents), maxAgents)
	}
	if !warningContains(catalog.Warnings, fmt.Sprintf("only the first %d agents are loaded", maxAgents)) {
		t.Fatalf("warnings = %v, want the truncation warning", catalog.Warnings)
	}
	if _, ok := catalog.Lookup("agent-32"); ok {
		t.Fatal("an agent beyond the cap was loaded")
	}
}

func TestLoadAgentsCapsCombinedBaseAndFiles(t *testing.T) {
	stateDir := t.TempDir()
	base := make([]AgentDefinition, 0, maxAgents-2)
	for i := range maxAgents - 2 {
		base = append(base, testBaseAgent(fmt.Sprintf("base-%02d", i), "Base."))
	}
	for i := range 4 {
		name := fmt.Sprintf("user-%02d", i)
		writeAgentFile(t, stateDir, name+".md", "name: "+name+"\ndescription: d\ntools: read_file\n---\nbody\n")
	}
	catalog := LoadAgents(base, stateDir)
	if len(catalog.Agents) != maxAgents {
		t.Fatalf("agents = %d, want the %d cap", len(catalog.Agents), maxAgents)
	}
	if !warningContains(catalog.Warnings, fmt.Sprintf("only the first %d agents are loaded", maxAgents)) {
		t.Fatalf("warnings = %v, want the truncation warning", catalog.Warnings)
	}
}

func warningContains(warnings []string, want string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, want) {
			return true
		}
	}
	return false
}

func TestLoadAgentsUserOverride(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "explore.md", `name: explore
description: A local explorer.
model: local-model
thinking_effort: low
tools: read_file
---
Explore locally.
`)
	catalog := LoadAgents(testAgentDefinitions(), stateDir)
	def, ok := catalog.Lookup("explore")
	if !ok {
		t.Fatal("explore is missing")
	}
	if def.Source != SourceUser || !strings.HasSuffix(def.Path, "explore.md") {
		t.Fatalf("source/path = %q/%q", def.Source, def.Path)
	}
	if def.Model != "local-model" || def.ThinkingEffort != "low" || def.Prompt != "Explore locally.\n" {
		t.Fatalf("override = %+v", def)
	}
	if len(catalog.Agents) != len(testAgentDefinitions()) {
		t.Fatalf("agents = %d, want the %d fixture names", len(catalog.Agents), len(testAgentDefinitions()))
	}
}

func TestLoadAgentsInvalidFileKeepsBase(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "explore.md", "name: explore\ntools: read_file\n---\nno description\n")
	catalog := LoadAgents(testAgentDefinitions(), stateDir)
	def, ok := catalog.Lookup("explore")
	if !ok || def.Source != SourceDefault {
		t.Fatalf("base explore was replaced by an invalid file: %+v", def)
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
	catalog := LoadAgents(nil, stateDir)
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
	catalog := LoadAgents(nil, stateDir)
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
	catalog := LoadAgents(testAgentDefinitions(), stateDir)
	if len(catalog.Agents) != len(testAgentDefinitions()) {
		t.Fatalf("agents = %d, want the %d fixture names", len(catalog.Agents), len(testAgentDefinitions()))
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
	catalog := LoadAgents(testAgentDefinitions(), stateDir)
	if len(catalog.Agents) != len(testAgentDefinitions()) {
		t.Fatalf("agents = %d, want the fixture set", len(catalog.Agents))
	}
	if len(catalog.Warnings) == 0 {
		t.Fatal("no warning for an unreadable agent directory")
	}
}

func TestLoadAgentsReadOnlyStateDir(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentFile(t, stateDir, "explore.md", "name: explore\ndescription: A local explorer.\ntools: read_file\n---\nExplore locally.\n")
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
	catalog := LoadAgents(testAgentDefinitions(), stateDir)
	if _, ok := catalog.Lookup("explore"); !ok {
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
