package agent

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzLoadAgentsBase asserts the host-supplied base path never panics, never
// returns an invalid runnable entry, never exceeds the agent cap, and always
// records why an entry was skipped.
func FuzzLoadAgentsBase(f *testing.F) {
	f.Add(validDefinition, false)
	f.Add("name: broken\n---\nbody\n", false)
	f.Add("", true)
	f.Fuzz(func(t *testing.T, data string, overCap bool) {
		var base []AgentDefinition
		def, err := ParseAgentDefinition([]byte(data))
		if err != nil {
			base = append(base, AgentDefinition{Name: "broken", Prompt: data})
		} else {
			base = append(base, def)
		}
		if overCap {
			for i := range maxAgents + 4 {
				base = append(base, AgentDefinition{
					Name:        fmt.Sprintf("agent-%02d", i),
					Description: "test definition",
					Tools:       []string{"read_file"},
					Prompt:      "body\n",
				})
			}
		}
		catalog := LoadAgents(base, t.TempDir())
		if len(catalog.Agents) > maxAgents {
			t.Fatalf("catalog has %d agents, over the %d cap", len(catalog.Agents), maxAgents)
		}
		for _, def := range catalog.Agents {
			if !def.Valid() {
				t.Fatalf("runnable entry is invalid: %+v", def)
			}
			if def.Source != SourceDefault || def.Path != "" {
				t.Fatalf("base entry metadata = %q/%q", def.Source, def.Path)
			}
		}
		for _, def := range catalog.Invalid {
			if def.Err == "" {
				t.Fatalf("invalid entry has no error: %+v", def)
			}
		}
	})
}

// FuzzParseAgentDefinition asserts the parser never panics, never accepts
// anything outside the grammar, and never returns a definition that validation
// would reject or that exceeds the recorded caps.
func FuzzParseAgentDefinition(f *testing.F) {
	seeds := []string{
		validDefinition,
		"name: x\ndescription: y\ntools: read_file\n---\nbody\n",
		"---\n",
		"\n\n",
		"name: x\r\ndescription: y\r\ntools: read_file\r\n---\r\nbody",
		"name: x\ndescription: y\ntools: read_file\nheader: no\n---\nbody\n",
		"name: x\ndescription: y\ntools: read_file\n---\n" + strings.Repeat("b", 1024) + "\n",
		"name: a\nname: b\ndescription: c\ntools: read_file\n---\np",
		"name: x\ndescription: y\ntools: read_file,\n---\nbody\n",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data string) {
		def, err := ParseAgentDefinition([]byte(data))
		if err != nil {
			return
		}
		if !def.Valid() {
			t.Fatalf("parsed definition carries an error: %+v", def)
		}
		if err := validateAgentDefinition(&def); err != nil {
			t.Fatalf("parsed definition fails validation: %v", err)
		}
		if len(def.Prompt) > maxAgentPromptBytes || len(def.Name) > maxAgentNameBytes || len(def.Description) > maxAgentDescriptionSize {
			t.Fatalf("parsed definition exceeds a recorded cap: %+v", def)
		}
	})
}
