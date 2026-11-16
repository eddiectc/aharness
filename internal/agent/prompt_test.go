package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/internal/model"
	"github.com/eddiectc/aharness/internal/session"
	"github.com/eddiectc/aharness/internal/tools"
)

func TestRenderPromptVersions(t *testing.T) {
	v0, err := renderPrompt(PromptVersionV0, "/workspace", nil)
	if err != nil {
		t.Fatalf("renderPrompt(v0): %v", err)
	}
	if !strings.Contains(v0, `"/workspace"`) {
		t.Fatalf("v0 prompt does not include the workspace: %q", v0)
	}
	if strings.Contains(v0, "delegate") || strings.Contains(v0, "todo") {
		t.Fatalf("frozen v0 prompt mentions a v1/v2 tool: %q", v0)
	}
	v1, err := renderPrompt(PromptVersionV1, "/workspace", nil)
	if err != nil {
		t.Fatalf("renderPrompt(v1): %v", err)
	}
	if !strings.Contains(v1, "`delegate` starts an agent with no conversation history") {
		t.Fatalf("frozen v1 prompt lacks its delegation paragraph: %q", v1)
	}
	if strings.Contains(v1, "todo") {
		t.Fatalf("frozen v1 prompt mentions todo: %q", v1)
	}
	if !strings.HasPrefix(v1, strings.TrimSuffix(v0, "\n")) {
		t.Fatal("v1 prompt does not keep the v0 clauses")
	}
	v2, err := renderPrompt(PromptVersion, "/workspace", nil)
	if err != nil {
		t.Fatalf("renderPrompt(v2): %v", err)
	}
	for _, want := range []string{"`todo` tracks multi-step work", "`delegate` runs independent work in a child agent"} {
		if !strings.Contains(v2, want) {
			t.Fatalf("v2 prompt lacks %q: %q", want, v2)
		}
	}
	if !strings.HasPrefix(v2, strings.TrimSuffix(v0, "\n")) {
		t.Fatal("v2 prompt does not keep the v0 clauses")
	}
	if _, err := renderPrompt("prompt-v9", "/workspace", nil); err == nil {
		t.Fatal("renderPrompt accepted an unknown version")
	}
	for _, version := range []string{PromptVersionV0, PromptVersionV1, PromptVersion} {
		if !SupportsPrompt(version) {
			t.Fatalf("SupportsPrompt(%q) = false, want replay support", version)
		}
	}
	if SupportsPrompt("prompt-v9") {
		t.Fatal("SupportsPrompt accepted an unknown version")
	}
}

func TestRenderPromptCatalog(t *testing.T) {
	agents := []AgentInfo{
		{Name: "explore", Description: "Read-only exploration."},
		{Name: "reviewer", Description: "Review a change."},
	}
	for _, version := range []string{PromptVersionV1, PromptVersion} {
		text, err := renderPrompt(version, "/workspace", agents)
		if err != nil {
			t.Fatalf("renderPrompt(%s): %v", version, err)
		}
		if !strings.Contains(text, "Available agents:\n- explore: Read-only exploration.\n- reviewer: Review a change.\n") {
			t.Fatalf("%s catalog is missing or malformed: %q", version, text)
		}
	}

	// A catalog that does not fit the 4 KiB budget ends with an omission marker.
	var many []AgentInfo
	for i := range 120 {
		many = append(many, AgentInfo{Name: "agent-" + strings.Repeat("a", 3) + string(rune('a'+i%26)), Description: strings.Repeat("d", 200)})
	}
	capped, err := renderPrompt(PromptVersion, "/workspace", many)
	if err != nil {
		t.Fatalf("renderPrompt(capped): %v", err)
	}
	if !strings.Contains(capped, "agents omitted]") {
		t.Fatalf("capped catalog has no omission marker: %q", capped[len(capped)-200:])
	}
	catalogStart := strings.Index(capped, "Available agents:\n")
	catalog := capped[catalogStart:]
	if len(catalog) > maxAgentCatalogBytes+len("Available agents:\n")+64 {
		t.Fatalf("catalog is %d bytes, want it near the %d byte cap", len(catalog), maxAgentCatalogBytes)
	}
}

func TestProjectUsesRecordedPrompt(t *testing.T) {
	workspace := "/workspace"
	recorded := "You are the recorded agent prompt for " + workspace + "."
	events := []session.Event{
		newTestEvent(t, session.TypeSessionStart, 0, "", sessionStartData{
			PromptVersion:  PromptVersion,
			ToolsetVersion: tools.Version,
			Workspace:      workspace,
			Provider:       "opencode-go",
			Dialect:        "openai-chat",
			Model:          "test-model",
			Limits:         testConfig().Limits,
			SystemPrompt:   recorded,
			Agent:          "reviewer",
			Tools:          []string{"search", "read_file"},
			ParentSession:  "parent",
			ParentCallID:   "call-1",
		}),
	}
	proj, err := Project(events, Limits{MaxInputTokens: 1_000_000, CompactionTarget: 1_000_000})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if got := proj.Messages[0].Content; got != recorded {
		t.Fatalf("system message = %q, want the recorded prompt", got)
	}
	wantSchemas, err := tools.Filter(mustSchemas(t, tools.Version), []string{"search", "read_file"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if !reflect.DeepEqual(proj.Schemas, wantSchemas) {
		t.Fatalf("schemas = %v, want the recorded grant in order", schemaNames(proj.Schemas))
	}
	for _, schema := range proj.Schemas {
		if schema.Name == "delegate" {
			t.Fatal("a child grant produced a delegate schema")
		}
	}

	state, err := Inspect(events)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.SystemPrompt != recorded || state.Agent != "reviewer" || state.ParentCallID != "call-1" {
		t.Fatalf("state lost the recorded fields: %+v", state)
	}
	if !reflect.DeepEqual(state.Tools, []string{"search", "read_file"}) {
		t.Fatalf("state tools = %v", state.Tools)
	}
}

func TestProjectPromptV0Replay(t *testing.T) {
	workspace := "/old/session"
	events := []session.Event{
		newTestEvent(t, session.TypeSessionStart, 0, "", sessionStartData{
			PromptVersion:  PromptVersionV0,
			ToolsetVersion: "toolset-v1",
			Workspace:      workspace,
			Provider:       "opencode-go",
			Dialect:        "openai-chat",
			Model:          "test-model",
			Limits:         testConfig().Limits,
		}),
		newTestEvent(t, session.TypeUserMessage, 0, string(model.RoleUser), userMessageData{Text: "task", Source: "-task"}),
	}
	proj, err := Project(events, Limits{MaxInputTokens: 1_000_000, CompactionTarget: 1_000_000})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	wantV0, err := renderPrompt(PromptVersionV0, workspace, nil)
	if err != nil {
		t.Fatalf("renderPrompt: %v", err)
	}
	if proj.Messages[0].Content != wantV0 {
		t.Fatalf("v0 replay prompt = %q, want the frozen template", proj.Messages[0].Content)
	}
	for _, schema := range proj.Schemas {
		if schema.Name == "delegate" {
			t.Fatal("a toolset-v1 session projected a delegate schema")
		}
	}
}

func TestProjectRejectsUnknownGrantName(t *testing.T) {
	events := []session.Event{
		newTestEvent(t, session.TypeSessionStart, 0, "", sessionStartData{
			PromptVersion:  PromptVersion,
			ToolsetVersion: tools.Version,
			Workspace:      "/w",
			Provider:       "opencode-go",
			Dialect:        "openai-chat",
			Model:          "test-model",
			Limits:         testConfig().Limits,
			Tools:          []string{"teleport"},
		}),
	}
	_, err := Project(events, Limits{MaxInputTokens: 1_000_000, CompactionTarget: 1_000_000})
	if err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("Project error = %v, want the unknown grant name", err)
	}
}

func TestSubagentIntent(t *testing.T) {
	tests := []struct {
		name string
		task string
		want string
	}{
		{"first non-empty line", "\n\n  find   the\thandler \nsecond", "find the handler"},
		{"empty", "\n \n", ""},
		{"control characters", "read\x1b[31m the file\x00", "read\u241b[31m the file\u2400"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := subagentIntent(tc.task); got != tc.want {
				t.Fatalf("subagentIntent(%q) = %q, want %q", tc.task, got, tc.want)
			}
		})
	}
	long := strings.Repeat("word ", 100)
	got := subagentIntent(long)
	if len(got) > maxReportIntentBytes+len("…") {
		t.Fatalf("intent is %d bytes, want <= %d", len(got), maxReportIntentBytes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("capped intent does not end with an ellipsis: %q", got)
	}
}

func TestChildSystemPrompt(t *testing.T) {
	def, err := ParseAgentDefinition([]byte(validDefinition))
	if err != nil {
		t.Fatalf("ParseAgentDefinition: %v", err)
	}
	for _, version := range []string{PromptVersionV0, PromptVersionV1, PromptVersion} {
		prompt := childSystemPrompt(def, "/workspace", version)
		for _, want := range []string{"reviewer", "/workspace", "Review the assigned change", "Tools are your only way to act"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s child prompt missing %q: %q", version, want, prompt)
			}
		}
		if strings.Contains(prompt, "Available agents:") || strings.Contains(prompt, "`delegate`") || strings.Contains(prompt, "`todo`") {
			t.Fatalf("%s child prompt carries parent-only material: %q", version, prompt)
		}
	}

	// A child that is actually granted todo gets the v2 todo guidance. Only
	// prompt-v2 children do; a v1 parent keeps its frozen child contract.
	def.Tools = []string{"read_file", "todo"}
	v2 := childSystemPrompt(def, "/workspace", PromptVersion)
	if !strings.Contains(v2, "`todo` tracks multi-step work") {
		t.Fatalf("v2 child with todo grant lacks the todo guidance: %q", v2)
	}
	if strings.Contains(v2, "`delegate`") || strings.Contains(v2, "Available agents:") {
		t.Fatalf("v2 child prompt carries parent-only material: %q", v2)
	}
	v1 := childSystemPrompt(def, "/workspace", PromptVersionV1)
	if strings.Contains(v1, "`todo`") {
		t.Fatalf("v1 child prompt gained v2 todo guidance: %q", v1)
	}
}

func mustSchemas(t *testing.T, version string) []model.ToolSchema {
	t.Helper()
	schemas, err := tools.SchemasFor(version)
	if err != nil {
		t.Fatalf("SchemasFor(%s): %v", version, err)
	}
	return schemas
}
