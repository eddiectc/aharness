package agent

import (
	"strings"
	"testing"
)

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
