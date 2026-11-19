package agent

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

func TestRenderPromptVersions(t *testing.T) {
	v0, err := renderPrompt(PromptVersionV0, "/workspace", "aharness", nil)
	if err != nil {
		t.Fatalf("renderPrompt(v0): %v", err)
	}
	if !strings.Contains(v0, `"/workspace"`) {
		t.Fatalf("v0 prompt does not include the workspace: %q", v0)
	}
	if strings.Contains(v0, "delegate") || strings.Contains(v0, "todo") {
		t.Fatalf("frozen v0 prompt mentions a v1/v2 tool: %q", v0)
	}
	v1, err := renderPrompt(PromptVersionV1, "/workspace", "aharness", nil)
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
	v2, err := renderPrompt(PromptVersionV2, "/workspace", "aharness", nil)
	if err != nil {
		t.Fatalf("renderPrompt(v2): %v", err)
	}
	for _, want := range []string{"`todo` tracks multi-step work", "`delegate` runs independent work in a child agent"} {
		if !strings.Contains(v2, want) {
			t.Fatalf("v2 prompt lacks %q: %q", want, v2)
		}
	}
	for _, frozenOnly := range []string{"Every tool call receives exactly one result", "project instructions", "infeasible"} {
		if strings.Contains(v2, frozenOnly) {
			t.Fatalf("frozen v2 prompt gained v3 content %q: %q", frozenOnly, v2)
		}
	}
	if !strings.HasPrefix(v2, strings.TrimSuffix(v0, "\n")) {
		t.Fatal("v2 prompt does not keep the v0 clauses")
	}
	v3, err := renderPrompt(PromptVersion, "/workspace", "aharness", nil)
	if err != nil {
		t.Fatalf("renderPrompt(v3): %v", err)
	}
	for _, want := range []string{
		"- Tools are your only way to act on the workspace. Report an action as done only when a tool call performed it.",
		"- Every tool call receives exactly one result.",
		"- Search and read before editing. Keep changes minimal and focused on the task.",
		"- Run the relevant tests before reporting the change done, and say plainly what you could not verify.",
		"- If the workspace carries project instructions (AGENTS.md or the file it names), read them before acting on the code.",
		"- If a task is infeasible as stated, or a check is wrong, say so rather than working around it.",
		"- Commands run with the harness's privileges. The harness adds no sandbox and no approval step.",
		"- Tool errors are data: read them, adapt, and try a different approach instead of escalating.",
		"- Stop by replying without a tool call when the task is complete or when you are blocked; when blocked, say what blocks you and what remains.",
		"when the conclusion must be reached without the context that produced the work",
	} {
		if !strings.Contains(v3, want) {
			t.Fatalf("v3 prompt lacks %q: %q", want, v3)
		}
	}
	if strings.Contains(v3, promptV2Delegation) {
		t.Fatal("v3 prompt carries the frozen v2 delegation paragraph")
	}
	if !strings.HasPrefix(v3, "You are aharness, a coding harness working in the workspace \"/workspace\".\n\n") {
		t.Fatal("v3 prompt does not keep the identity line and blank separator")
	}
	if _, err := renderPrompt("prompt-v9", "/workspace", "aharness", nil); err == nil {
		t.Fatal("renderPrompt accepted an unknown version")
	}
	for _, version := range []string{PromptVersionV0, PromptVersionV1, PromptVersionV2, PromptVersion} {
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
	for _, version := range []string{PromptVersionV1, PromptVersionV2, PromptVersion} {
		text, err := renderPrompt(version, "/workspace", "aharness", agents)
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
	capped, err := renderPrompt(PromptVersion, "/workspace", "aharness", many)
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

func TestLoopRules(t *testing.T) {
	invariants := []string{
		"- Tools are your only way to act on the workspace. Report an action as done only when a tool call performed it.",
		"- Every tool call receives exactly one result.",
		"- Commands run with the harness's privileges. The harness adds no sandbox and no approval step.",
		"- Tool errors are data: read them, adapt, and try a different approach instead of escalating.",
		"- Stop by replying without a tool call when the task is complete or when you are blocked; when blocked, say what blocks you and what remains.",
	}
	rules := LoopRules()
	for _, want := range invariants {
		if !strings.Contains(rules, want) {
			t.Fatalf("LoopRules missing invariant %q: %q", want, rules)
		}
	}
	if lines := strings.Split(rules, "\n"); len(lines) != len(invariants) {
		t.Fatalf("LoopRules has %d lines, want %d: %q", len(lines), len(invariants), rules)
	}
	if strings.HasSuffix(rules, "\n") {
		t.Fatalf("LoopRules has a trailing newline: %q", rules)
	}
	for _, forbidden := range []string{
		"Search and read before editing",
		"Run the relevant tests",
		"project instructions",
		"AGENTS.md",
		"infeasible",
		"refuse",
	} {
		if strings.Contains(rules, forbidden) {
			t.Fatalf("LoopRules carries coding policy %q: %q", forbidden, rules)
		}
	}
}

func TestAgentCatalogText(t *testing.T) {
	if got := AgentCatalogText(nil); got != "" {
		t.Fatalf("AgentCatalogText(nil) = %q, want empty", got)
	}
	agents := []AgentInfo{
		{Name: "explore", Description: "Read-only exploration."},
		{Name: "reviewer", Description: "Review a change."},
	}
	want := "- explore: Read-only exploration.\n- reviewer: Review a change.\n"
	if got := AgentCatalogText(agents); got != want {
		t.Fatalf("AgentCatalogText = %q, want %q", got, want)
	}
	if lines := strings.Count(AgentCatalogText(agents), "\n"); lines != len(agents) {
		t.Fatalf("AgentCatalogText rendered %d lines for %d agents", lines, len(agents))
	}

	var many []AgentInfo
	for i := range 120 {
		many = append(many, AgentInfo{Name: "agent-" + string(rune('a'+i%26)), Description: strings.Repeat("d", 200)})
	}
	capped := AgentCatalogText(many)
	if !strings.Contains(capped, "agents omitted]") {
		t.Fatalf("capped catalog has no omission marker: %q", capped[len(capped)-200:])
	}
	rendered := strings.Count(capped, "- agent-")
	if want := fmt.Sprintf("[%d agents omitted]\n", len(many)-rendered); !strings.Contains(capped, want) {
		t.Fatalf("capped catalog lacks %q for %d rendered agents: %q", want, rendered, capped[len(capped)-200:])
	}
	if len(capped) > maxAgentCatalogBytes+64 {
		t.Fatalf("catalog is %d bytes, want it near the %d byte cap", len(capped), maxAgentCatalogBytes)
	}
	if !strings.HasSuffix(capped, "\n") {
		t.Fatalf("catalog does not end each line with a newline: %q", capped)
	}
}

func TestDefaultPromptBody(t *testing.T) {
	workspace := "/workspace"
	agents := []AgentInfo{
		{Name: "explore", Description: "Read-only exploration."},
		{Name: "reviewer", Description: "Review a change."},
	}
	for _, version := range []string{PromptVersionV0, PromptVersionV1, PromptVersionV2, PromptVersion} {
		body, err := DefaultPromptBody(version, agents)
		if err != nil {
			t.Fatalf("DefaultPromptBody(%s): %v", version, err)
		}
		rendered, err := renderPrompt(version, workspace, "aharness", agents)
		if err != nil {
			t.Fatalf("renderPrompt(%s): %v", version, err)
		}
		if want := identityLine("aharness", workspace) + "\n\n" + body; rendered != want {
			t.Fatalf("%s renderPrompt does not equal identity + DefaultPromptBody:\n got %q\nwant %q", version, rendered, want)
		}
		if version == PromptVersionV0 {
			if strings.Contains(body, "Available agents:") {
				t.Fatalf("v0 body gained a catalog: %q", body)
			}
			continue
		}
		if !strings.Contains(body, "Available agents:\n- explore: Read-only exploration.\n- reviewer: Review a change.\n") {
			t.Fatalf("%s body lacks the catalog: %q", version, body)
		}
	}

	v0Agents, err := DefaultPromptBody(PromptVersionV0, agents)
	if err != nil {
		t.Fatalf("DefaultPromptBody(v0, agents): %v", err)
	}
	v0None, err := DefaultPromptBody(PromptVersionV0, nil)
	if err != nil {
		t.Fatalf("DefaultPromptBody(v0, nil): %v", err)
	}
	if v0Agents != v0None {
		t.Fatalf("v0 ignores the catalog: agents %q, none %q", v0Agents, v0None)
	}
	if _, err := DefaultPromptBody("prompt-v9", nil); err == nil {
		t.Fatal("DefaultPromptBody accepted an unknown version")
	}
}

// frozenPromptPins are the exact v0/v1/v2 parent prompts recorded before
// prompt-v3, for workspace "/workspace" and no agents. They were computed from
// the pre-change code; a changed byte fails the pin.
var frozenPromptPins = map[string]struct {
	length int
	sha256 string
}{
	PromptVersionV0: {592, "8c2345eece3e4649b32e596476320482b686cdb25503786c1f3d77f647468ee1"},
	PromptVersionV1: {751, "9a3f363a62429272932f3e1c8cf4854a1984e0c30f5c655ad25a0314e97f65fe"},
	PromptVersionV2: {1195, "bd9109113850989c45be610e034ac3a8ccaea7233760bd869b579ee0a3600bc9"},
}

func assertFrozenPrompt(t *testing.T, version, got string) {
	t.Helper()
	pin, ok := frozenPromptPins[version]
	if !ok {
		t.Fatalf("no frozen pin for %q", version)
	}
	if len(got) != pin.length {
		t.Fatalf("%s prompt is %d bytes, want the frozen %d", version, len(got), pin.length)
	}
	if sum := fmt.Sprintf("%x", sha256.Sum256([]byte(got))); sum != pin.sha256 {
		t.Fatalf("%s prompt sha256 = %s, want the frozen %s", version, sum, pin.sha256)
	}
}

func TestRenderPromptFrozen(t *testing.T) {
	for version := range frozenPromptPins {
		got, err := renderPrompt(version, "/workspace", "", nil)
		if err != nil {
			t.Fatalf("renderPrompt(%s): %v", version, err)
		}
		assertFrozenPrompt(t, version, got)
	}

	// An empty identity selects the default and renders the same bytes as the
	// explicit name for every supported version.
	for _, version := range []string{PromptVersionV0, PromptVersionV1, PromptVersionV2, PromptVersion} {
		empty, err := renderPrompt(version, "/workspace", "", nil)
		if err != nil {
			t.Fatalf("renderPrompt(%s, empty): %v", version, err)
		}
		named, err := renderPrompt(version, "/workspace", "aharness", nil)
		if err != nil {
			t.Fatalf("renderPrompt(%s, aharness): %v", version, err)
		}
		if empty != named {
			t.Fatalf("%s empty identity rendered %q, want %q", version, empty, named)
		}
	}

	// v3 is a new body, not one of the frozen pins.
	v3, err := renderPrompt(PromptVersion, "/workspace", "", nil)
	if err != nil {
		t.Fatalf("renderPrompt(v3): %v", err)
	}
	if sum := fmt.Sprintf("%x", sha256.Sum256([]byte(v3))); sum == frozenPromptPins[PromptVersionV2].sha256 {
		t.Fatal("prompt-v3 renders the frozen prompt-v2 bytes")
	}
}

func TestChildSystemPromptIdentity(t *testing.T) {
	def, err := ParseAgentDefinition([]byte(validDefinition))
	if err != nil {
		t.Fatalf("ParseAgentDefinition: %v", err)
	}
	workspace := "/workspace"
	wantLine := `You are the acode "reviewer" agent, working in the workspace "/workspace".`
	got := childSystemPrompt(def, workspace, "acode", PromptVersion, "")
	if !strings.HasPrefix(got, wantLine+"\n\n") {
		t.Fatalf("acode child prompt = %q, want it to start with %q", got, wantLine)
	}
	if strings.Contains(got, "aharness") {
		t.Fatalf("acode child prompt still names aharness: %q", got)
	}
	empty := childSystemPrompt(def, workspace, "", PromptVersion, "")
	if !strings.HasPrefix(empty, `You are the aharness "reviewer" agent, working in the workspace "/workspace".`+"\n\n") {
		t.Fatalf("empty identity child prompt = %q, want the aharness default", empty)
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
			AppendedPrompt: "host text that projection must ignore",
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
	workspace := "/workspace"
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
	assertFrozenPrompt(t, PromptVersionV0, proj.Messages[0].Content)
	for _, schema := range proj.Schemas {
		if schema.Name == "delegate" {
			t.Fatal("a toolset-v1 session projected a delegate schema")
		}
	}
}

func TestProjectPromptV2Replay(t *testing.T) {
	workspace := "/workspace"
	events := []session.Event{
		newTestEvent(t, session.TypeSessionStart, 0, "", sessionStartData{
			PromptVersion:  PromptVersionV2,
			ToolsetVersion: tools.Version,
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
	assertFrozenPrompt(t, PromptVersionV2, proj.Messages[0].Content)
	if strings.Contains(proj.Messages[0].Content, "Every tool call receives exactly one result") {
		t.Fatal("a prompt-v2 session replayed with v3 rules")
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
	for _, version := range []string{PromptVersionV0, PromptVersionV1, PromptVersionV2, PromptVersion} {
		prompt := childSystemPrompt(def, "/workspace", "aharness", version, "")
		for _, want := range []string{"reviewer", "/workspace", "Review the assigned change", "Tools are your only way to act"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s child prompt missing %q: %q", version, want, prompt)
			}
		}
		if strings.Contains(prompt, "Available agents:") || strings.Contains(prompt, "`delegate`") || strings.Contains(prompt, "`todo`") {
			t.Fatalf("%s child prompt carries parent-only material: %q", version, prompt)
		}
	}

	// A child that is actually granted todo gets the todo guidance for v2 and
	// v3 only; v0 and v1 keep their frozen child contract.
	def.Tools = []string{"read_file", "todo"}
	for _, version := range []string{PromptVersionV2, PromptVersion} {
		prompt := childSystemPrompt(def, "/workspace", "aharness", version, "")
		if !strings.Contains(prompt, "`todo` tracks multi-step work") {
			t.Fatalf("%s child with todo grant lacks the todo guidance: %q", version, prompt)
		}
		if strings.Contains(prompt, "`delegate`") || strings.Contains(prompt, "Available agents:") {
			t.Fatalf("%s child prompt carries parent-only material: %q", version, prompt)
		}
	}
	for _, version := range []string{PromptVersionV0, PromptVersionV1} {
		if prompt := childSystemPrompt(def, "/workspace", "aharness", version, ""); strings.Contains(prompt, "`todo`") {
			t.Fatalf("%s child gained todo guidance: %q", version, prompt)
		}
	}

	// v2 children keep the frozen rules; v3 children get the v3 policy lines.
	v2 := childSystemPrompt(def, "/workspace", "aharness", PromptVersionV2, "")
	if strings.Contains(v2, "Every tool call receives exactly one result") || strings.Contains(v2, "project instructions") {
		t.Fatalf("v2 child gained v3 rules: %q", v2)
	}
	if !strings.Contains(v2, "Run the relevant tests after changing code") {
		t.Fatalf("v2 child lost its frozen test rule: %q", v2)
	}
	v3 := childSystemPrompt(def, "/workspace", "aharness", PromptVersion, "")
	if !strings.Contains(v3, "Every tool call receives exactly one result") ||
		!strings.Contains(v3, "Run the relevant tests before reporting the change done") ||
		!strings.Contains(v3, "If the workspace carries project instructions") {
		t.Fatalf("v3 child lacks the v3 rules: %q", v3)
	}
}

func TestChildSystemPromptCarriesHostText(t *testing.T) {
	def, err := ParseAgentDefinition([]byte(validDefinition))
	if err != nil {
		t.Fatalf("ParseAgentDefinition: %v", err)
	}
	host := "Workspace rules:\n- Keep the host text last."
	prompt := childSystemPrompt(def, "/workspace", "aharness", PromptVersion, host)
	roleAt := strings.Index(prompt, "## Your role")
	if roleAt < 0 || strings.Index(prompt, host) < roleAt || !strings.HasSuffix(prompt, "\n\n"+host) {
		t.Fatalf("host text is not after the role body: %q", prompt)
	}
	plain := childSystemPrompt(def, "/workspace", "aharness", PromptVersion, "")
	if plain != strings.TrimSuffix(prompt, "\n\n"+host) {
		t.Fatal("an empty append changed the child prompt bytes")
	}

	// The write point applies the single recorded-prompt cap, so an over-cap
	// host text yields exactly one marker after the role body stays intact.
	over := childSystemPrompt(def, "/workspace", "aharness", PromptVersion, strings.Repeat("x", 40_000))
	recorded := systemPromptText(Config{SystemPrompt: over})
	if got := strings.Count(recorded, "[truncated "); got != 1 {
		t.Fatalf("truncation markers = %d, want 1", got)
	}
	if !strings.Contains(recorded, "## Your role\n\nReview the assigned change. Lead with findings.") {
		t.Fatal("the cap cut into the role body")
	}
	if len(recorded) <= maxSystemPromptBytes {
		t.Fatalf("recorded prompt length = %d, want the cap applied", len(recorded))
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
