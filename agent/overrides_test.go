package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestAgentOverrideCloneCopiesEveryField(t *testing.T) {
	model, effort, turns, enabled := "m", "high", 3, true
	original := AgentOverride{Model: &model, ThinkingEffort: &effort, MaxTurns: &turns, Enabled: &enabled}
	clone := original.Clone()
	if !reflect.DeepEqual(original, clone) {
		t.Fatalf("Clone = %+v, want %+v", clone, original)
	}
	value := reflect.ValueOf(original)
	cloned := reflect.ValueOf(clone)
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if field.Type.Kind() != reflect.Pointer {
			continue
		}
		if value.Field(i).IsNil() {
			continue
		}
		if value.Field(i).Pointer() == cloned.Field(i).Pointer() {
			t.Fatalf("%s shares the original pointer", field.Name)
		}
	}
	*clone.Model = "changed"
	*clone.ThinkingEffort = "low"
	*clone.MaxTurns = 1
	*clone.Enabled = false
	if *original.Model != "m" || *original.ThinkingEffort != "high" || *original.MaxTurns != 3 || !*original.Enabled {
		t.Fatalf("mutating clone changed original: %+v", original)
	}
	if !(AgentOverride{}).Clone().Empty() {
		t.Fatal("empty clone is not empty")
	}
}

func overrideStr(value string) *string { return &value }
func overrideInt(value int) *int       { return &value }
func overrideBool(value bool) *bool    { return &value }

func TestValidateAgentOverride(t *testing.T) {
	tests := []struct {
		name     string
		agent    string
		override AgentOverride
		want     string
	}{
		{"empty is valid", "reviewer", AgentOverride{}, ""},
		{"model", "reviewer", AgentOverride{Model: overrideStr("deepseek-v4.1-flash")}, ""},
		{"effort", "reviewer", AgentOverride{ThinkingEffort: overrideStr("high")}, ""},
		{"max turns", "reviewer", AgentOverride{MaxTurns: overrideInt(3)}, ""},
		{"enabled", "reviewer", AgentOverride{Enabled: overrideBool(false)}, ""},
		{"bad agent name", "Reviewer", AgentOverride{}, "must match"},
		{"empty agent name", "", AgentOverride{}, "name is required"},
		{"empty model", "reviewer", AgentOverride{Model: overrideStr("")}, "model must not be empty"},
		{"long model", "reviewer", AgentOverride{Model: overrideStr(strings.Repeat("m", maxAgentOverrideModelBytes+1))}, "limit"},
		{"empty effort", "reviewer", AgentOverride{ThinkingEffort: overrideStr("")}, "thinking_effort must not be empty"},
		{"default effort", "reviewer", AgentOverride{ThinkingEffort: overrideStr("default")}, "thinking_effort"},
		{"invalid effort", "reviewer", AgentOverride{ThinkingEffort: overrideStr("Very High")}, "thinking_effort"},
		{"zero turns", "reviewer", AgentOverride{MaxTurns: overrideInt(0)}, "must be positive"},
		{"negative turns", "reviewer", AgentOverride{MaxTurns: overrideInt(-1)}, "must be positive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAgentOverride(tc.agent, tc.override)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidateAgentOverride = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateAgentOverride = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	if err := ValidateAgentOverride("reviewer", AgentOverride{Model: overrideStr("\xff")}); err == nil {
		t.Fatal("invalid UTF-8 model accepted")
	}
}

func overrideTestCatalog() *AgentCatalog {
	return &AgentCatalog{Agents: []AgentDefinition{
		{
			Name:           "explore",
			Description:    "Explore the repository.",
			Model:          "explore-model",
			ThinkingEffort: "low",
			Tools:          []string{"read_file", "search"},
			Prompt:         "Explore.",
			Source:         agentSourceBundled,
			MaxTurns:       9,
		},
		{
			Name:           "reviewer",
			Description:    "Review a change.",
			Model:          "review-model",
			ThinkingEffort: "high",
			Tools:          []string{"read_file", "search"},
			Prompt:         "Review.",
			Source:         agentSourceUser,
			Path:           "/state/agents/reviewer.md",
			MaxTurns:       12,
		},
	}}
}

func TestApplyAgentOverrides(t *testing.T) {
	base := overrideTestCatalog()
	applied, err := ApplyAgentOverrides(base, map[string]AgentOverride{
		"reviewer": {
			Model:    overrideStr("new-model"),
			MaxTurns: overrideInt(3),
			Enabled:  overrideBool(false),
		},
	})
	if err != nil {
		t.Fatalf("ApplyAgentOverrides: %v", err)
	}
	def, ok := applied.LookupDefinition("reviewer")
	if !ok {
		t.Fatal("reviewer missing from the applied catalog")
	}
	if def.Model != "new-model" || def.MaxTurns != 3 || !def.Disabled {
		t.Fatalf("effective settings = %+v", def)
	}
	// A model switch with no explicit effort resets to the model default.
	if def.ThinkingEffort != "" {
		t.Fatalf("effort = %q, want the model default after a model switch", def.ThinkingEffort)
	}
	if def.DeclaredModel != "review-model" || def.DeclaredThinkingEffort != "high" || def.DeclaredMaxTurns != 12 {
		t.Fatalf("declared values = %+v", def)
	}
	if def.Override == nil || def.Override.Model == nil || *def.Override.Model != "new-model" {
		t.Fatalf("override = %+v", def.Override)
	}
	if strings.Join(def.Tools, ",") != "read_file,search" || def.Prompt != "Review." {
		t.Fatalf("override changed the grant or prompt: %+v", def)
	}
	if def.Source != agentSourceUser || def.Path != "/state/agents/reviewer.md" {
		t.Fatalf("source/path changed: %+v", def)
	}

	// The base catalog is never mutated.
	original, _ := base.LookupDefinition("reviewer")
	if original.Model != "review-model" || original.ThinkingEffort != "high" ||
		original.MaxTurns != 12 || original.Disabled || original.Override != nil {
		t.Fatalf("base catalog mutated: %+v", original)
	}

	// A disabled definition is discoverable but not runnable.
	if _, ok := applied.Lookup("reviewer"); ok {
		t.Fatal("disabled reviewer is still runnable")
	}
	for _, info := range applied.Infos() {
		if info.Name == "reviewer" {
			t.Fatal("disabled reviewer is still in the prompt catalog")
		}
	}
	for _, info := range applied.SpawnInfos() {
		if info.Name == "reviewer" {
			t.Fatal("disabled reviewer is still offered by delegate")
		}
	}
	if _, ok := applied.Lookup("explore"); !ok {
		t.Fatal("explore is no longer runnable")
	}
}

func TestApplyAgentOverridesEffortRules(t *testing.T) {
	tests := []struct {
		name       string
		baseModel  string
		baseEffort string
		override   AgentOverride
		wantModel  string
		wantEffort string
	}{
		{
			name:      "effort only",
			baseModel: "review-model", baseEffort: "high",
			override:  AgentOverride{ThinkingEffort: overrideStr("low")},
			wantModel: "review-model", wantEffort: "low",
		},
		{
			name:      "same model keeps the declared effort",
			baseModel: "review-model", baseEffort: "high",
			override:  AgentOverride{Model: overrideStr("review-model")},
			wantModel: "review-model", wantEffort: "high",
		},
		{
			name:      "model switch resets the declared effort",
			baseModel: "review-model", baseEffort: "high",
			override:  AgentOverride{Model: overrideStr("other-model")},
			wantModel: "other-model", wantEffort: "",
		},
		{
			name:      "model and effort together",
			baseModel: "review-model", baseEffort: "high",
			override:  AgentOverride{Model: overrideStr("other-model"), ThinkingEffort: overrideStr("medium")},
			wantModel: "other-model", wantEffort: "medium",
		},
		{
			name:      "model switch with no declared effort",
			baseModel: "review-model", baseEffort: "",
			override:  AgentOverride{Model: overrideStr("other-model")},
			wantModel: "other-model", wantEffort: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := &AgentCatalog{Agents: []AgentDefinition{{
				Name: "reviewer", Description: "Review.", Model: tc.baseModel,
				ThinkingEffort: tc.baseEffort, Tools: []string{"read_file"}, Prompt: "Review.",
			}}}
			applied, err := ApplyAgentOverrides(base, map[string]AgentOverride{"reviewer": tc.override})
			if err != nil {
				t.Fatalf("ApplyAgentOverrides: %v", err)
			}
			def, _ := applied.Lookup("reviewer")
			if def.Model != tc.wantModel || def.ThinkingEffort != tc.wantEffort {
				t.Fatalf("model/effort = %q/%q, want %q/%q",
					def.Model, def.ThinkingEffort, tc.wantModel, tc.wantEffort)
			}
		})
	}
}

func TestApplyAgentOverridesRejects(t *testing.T) {
	base := overrideTestCatalog()
	tests := []struct {
		name      string
		overrides map[string]AgentOverride
		want      string
	}{
		{"unknown agent", map[string]AgentOverride{"nobody": {Model: overrideStr("m")}}, "no agent definition"},
		{"invalid value", map[string]AgentOverride{"explore": {MaxTurns: overrideInt(0)}}, "must be positive"},
		{"invalid name", map[string]AgentOverride{"Not-Valid": {}}, "must match"},
		{"too many", func() map[string]AgentOverride {
			out := make(map[string]AgentOverride, maxAgentOverrides+1)
			for i := 0; i <= maxAgentOverrides; i++ {
				out["agent-"+string(rune('a'+i%26))+string(rune('a'+i/26))] = AgentOverride{}
			}
			return out
		}(), "limit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ApplyAgentOverrides(base, tc.overrides); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ApplyAgentOverrides = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	if out, err := ApplyAgentOverrides(nil, nil); err != nil || out == nil || len(out.Agents) != 0 {
		t.Fatalf("nil catalog with no overrides = %+v, %v", out, err)
	}
	if _, err := ApplyAgentOverrides(nil, map[string]AgentOverride{"x": {}}); err == nil {
		t.Fatal("nil catalog with an override accepted")
	}
}

func TestApplyAgentOverridesEmptyEntryIgnored(t *testing.T) {
	base := overrideTestCatalog()
	applied, err := ApplyAgentOverrides(base, map[string]AgentOverride{"reviewer": {}})
	if err != nil {
		t.Fatalf("ApplyAgentOverrides: %v", err)
	}
	def, _ := applied.LookupDefinition("reviewer")
	if def.Override != nil || def.DeclaredModel != "" || def.Model != "review-model" {
		t.Fatalf("empty override changed the definition: %+v", def)
	}
}
