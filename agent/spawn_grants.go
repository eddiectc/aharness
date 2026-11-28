package agent

import (
	"fmt"
	"slices"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

// maxConcurrentChildren caps the children one delegate call may run at once.
// It matches the schema's maxItems of 4.
const maxConcurrentChildren = 4

// isReadOnlyTool reports whether name is safe to run while sibling children
// share the workspace. The set is deliberately explicit: web_search and
// web_fetch reach the network but never write the workspace, and read_image
// only reads a file, while every other tool either writes or carries per-child
// state that fan-out should not share.
func isReadOnlyTool(name string) bool {
	switch name {
	case "read_file", "read_image", "search", "web_search", "web_fetch":
		return true
	default:
		return false
	}
}

// executionMode reports whether the call's tasks may run concurrently and why
// not when they may not. Every task must have a recorded or declared grant made
// only of read-only or concurrency-safe tools; an unreadable continuation grant
// is not classified optimistically.
func (s *Spawner) executionMode(specs []tools.SubagentSpec) (string, string) {
	for i, spec := range specs {
		grant, name, ok := s.taskGrant(spec)
		if !ok {
			return tools.ExecutionSequential, fmt.Sprintf("task %d has no readable read-only tool grant", i+1)
		}
		if !s.grantConcurrencySafe(grant) {
			return tools.ExecutionSequential, fmt.Sprintf("task %d: agent %q is not read-only or concurrency-safe", i+1, name)
		}
	}
	return tools.ExecutionConcurrent, ""
}

// taskGrant returns the effective tool grant for one task. A fresh task uses
// its definition; a continuation uses the child's recorded grant, because the
// definition may have changed since that child started.
func (s *Spawner) taskGrant(spec tools.SubagentSpec) ([]string, string, bool) {
	if spec.Session != "" {
		path := session.SessionPath(s.opts.Config.StateDir, spec.Session)
		head, err := session.ReadHead(path, 1)
		if err != nil || len(head) == 0 || head[0].Type != session.TypeSessionStart {
			return nil, spec.Agent, false
		}
		var start sessionStartData
		if err := decodeEventData(head[0], &start); err != nil {
			return nil, spec.Agent, false
		}
		if start.ParentSession != s.opts.ParentSession || start.ParentCallID == "" {
			return nil, start.Agent, false
		}
		name := start.Agent
		if name == "" {
			name = spec.Agent
		}
		// A nil recorded grant means every tool of the recorded version,
		// including writers; only an explicit read-only list is safe.
		return start.Tools, name, start.Tools != nil
	}
	def, ok := s.opts.Definitions.Lookup(spec.Agent)
	if !ok {
		return nil, spec.Agent, false
	}
	return def.Tools, def.Name, true
}

// isReadOnlyGrant reports whether every granted built-in is read-only. A nil or
// empty grant is never read-only: nil means the full recorded toolset.
func isReadOnlyGrant(grant []string) bool {
	if len(grant) == 0 {
		return false
	}
	for _, name := range grant {
		if !isReadOnlyTool(name) {
			return false
		}
	}
	return true
}

// grantConcurrencySafe reports whether every name in grant is a built-in
// read-only tool or a custom tool that declared ConcurrencySafe. The claim is
// host code, not verified by the harness.
func (s *Spawner) grantConcurrencySafe(grant []string) bool {
	if len(grant) == 0 {
		return false
	}
	custom := s.customToolDecls()
	for _, name := range grant {
		if isReadOnlyTool(name) {
			continue
		}
		if tool, ok := custom[name]; ok && tool.ConcurrencySafe {
			continue
		}
		return false
	}
	return true
}

// customToolDecls indexes the parent's recorded custom-tool declarations.
func (s *Spawner) customToolDecls() map[string]CustomTool {
	decls := make(map[string]CustomTool, len(s.opts.Config.CustomTools))
	for _, tool := range s.opts.Config.CustomTools {
		decls[tool.Schema.Name] = tool
	}
	return decls
}

// parentAllows reports whether the parent session may use name. A nil parent
// grant means every tool of the recorded toolset plus every registered custom
// tool; an explicit grant is the authority.
func (s *Spawner) parentAllows(name string) bool {
	if name == "delegate" {
		return false
	}
	if _, ok := s.customToolDecls()[name]; ok {
		// A custom tool must be registered on the parent to be inherited.
	} else {
		version := s.opts.Config.ToolsetVersion
		if version == "" {
			version = tools.Version
		}
		schemas, err := tools.SchemasFor(version)
		if err != nil {
			return false
		}
		found := false
		for _, schema := range schemas {
			if schema.Name == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if s.opts.Config.Tools == nil {
		return true
	}
	return slices.Contains(s.opts.Config.Tools, name)
}

// childGrant narrows a definition's requested grant to the parent's authority.
// It returns the denied name when the definition asks for something the parent
// does not have, so the task fails closed instead of widening authority.
func (s *Spawner) childGrant(def AgentDefinition) ([]string, string) {
	requested := def.Tools
	if requested == nil {
		requested = []string{}
	}
	grant := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, name := range requested {
		if name == "delegate" {
			return nil, "delegate"
		}
		if seen[name] {
			continue
		}
		if !s.parentAllows(name) {
			return nil, name
		}
		seen[name] = true
		grant = append(grant, name)
	}
	return grant, ""
}

// narrowChildGrant applies the current parent's authority to a resumed child's
// recorded grant. The result is always non-nil so an explicit empty child grant
// stays empty; delegate is never inherited because delegation depth is one.
func (s *Spawner) narrowChildGrant(recorded []string, toolsetVersion string) []string {
	if recorded == nil {
		return s.allParentTools(toolsetVersion)
	}
	grant := make([]string, 0, len(recorded))
	for _, name := range recorded {
		if name == "delegate" || !s.parentAllows(name) {
			continue
		}
		grant = append(grant, name)
	}
	return append([]string{}, grant...)
}

// allParentTools returns the parent's authority as an explicit non-nil grant:
// its recorded grant when present, otherwise every built-in of the child's
// recorded toolset except delegate plus every registered custom tool.
func (s *Spawner) allParentTools(toolsetVersion string) []string {
	if s.opts.Config.Tools != nil {
		grant := make([]string, 0, len(s.opts.Config.Tools))
		for _, name := range s.opts.Config.Tools {
			if name != "delegate" {
				grant = append(grant, name)
			}
		}
		return append([]string{}, grant...)
	}
	if toolsetVersion == "" {
		toolsetVersion = tools.Version
	}
	grant := make([]string, 0, 8)
	if schemas, err := tools.SchemasFor(toolsetVersion); err == nil {
		for _, schema := range schemas {
			if schema.Name != "delegate" {
				grant = append(grant, schema.Name)
			}
		}
	}
	for _, tool := range s.opts.Config.CustomTools {
		grant = append(grant, tool.Schema.Name)
	}
	return append([]string{}, grant...)
}

// reserveFits reports whether the fresh tasks in the call fit the remaining
// -max-subagents budget. It lets a concurrent call fall back to array order
// before any child starts, so the spawn a limit rejects is deterministic.
func (s *Spawner) reserveFits(specs []tools.SubagentSpec) bool {
	if s.opts.MaxSubagents <= 0 {
		return true
	}
	fresh := 0
	for _, spec := range specs {
		if spec.Session == "" {
			fresh++
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawned+fresh <= s.opts.MaxSubagents
}

// reserveChild consumes one fresh-child slot, or reports that the limit is
// reached. A zero limit is unlimited.
func (s *Spawner) reserveChild() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opts.MaxSubagents > 0 && s.spawned >= s.opts.MaxSubagents {
		return false
	}
	s.spawned++
	return true
}

// remainingBudget returns the token budget a child may spend: the parent's
// remaining budget, or 0 when the parent is unlimited.
func (s *Spawner) remainingBudget(call tools.Call) int {
	remaining, limited := s.remainingTokens(call)
	if !limited {
		return 0
	}
	if remaining < 0 {
		return 0
	}
	return remaining
}

// remainingTokens reports the parent's remaining token budget and whether one
// is configured.
func (s *Spawner) remainingTokens(call tools.Call) (int, bool) {
	limit := s.opts.Config.MaxTotalTokens
	if limit <= 0 {
		return 0, false
	}
	s.mu.Lock()
	used := s.childUse.Total()
	s.mu.Unlock()
	return limit - call.ParentUsage.Total() - used, true
}

// addChildUsage folds one finished task's spend into the run's child total.
func (s *Spawner) addChildUsage(usage model.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.childUse = s.childUse.Add(usage)
}
