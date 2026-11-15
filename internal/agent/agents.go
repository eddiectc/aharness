package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"aharness/internal/tools"
)

// Agent definition caps. They keep one definition file, one prompt, and the
// discovered catalog bounded.
const (
	maxAgentDefinitionBytes = 64 << 10
	maxAgentNameBytes       = 64
	maxAgentDescriptionSize = 200
	maxAgentPromptBytes     = 16 << 10
	maxAgents               = 32
	maxReportIntentBytes    = 200
)

// Agent sources recorded for the definitions browser and the catalog.
const (
	agentSourceBundled = "bundled"
	agentSourceUser    = "user"
)

// AgentDefinition is one agent definition: a strict header plus a markdown
// prompt body. An invalid entry carries Err instead of a usable definition.
type AgentDefinition struct {
	Name           string
	Description    string
	Model          string
	ThinkingEffort string
	Tools          []string
	MaxTurns       int
	Prompt         string
	// Source is agentSourceBundled or agentSourceUser.
	Source string
	// Path is the definition file for a user agent; empty when bundled.
	Path string
	// Err is the parse or validation error for an invalid entry.
	Err string
}

// Valid reports whether the entry can be run.
func (d AgentDefinition) Valid() bool { return d.Err == "" }

// AgentCatalog is the discovered, validated definition set for one process.
// Invalid holds the user files that were skipped so the definitions browser
// can explain why. The catalog is immutable after loading: a mid-run edit
// cannot change what a child in flight will run.
type AgentCatalog struct {
	Agents   []AgentDefinition
	Invalid  []AgentDefinition
	Warnings []string
}

// Lookup returns the active definition with the exact name.
func (c *AgentCatalog) Lookup(name string) (AgentDefinition, bool) {
	if c == nil {
		return AgentDefinition{}, false
	}
	for _, def := range c.Agents {
		if def.Name == name {
			return def, true
		}
	}
	return AgentDefinition{}, false
}

// Infos returns the name/description catalog rendered into the parent prompt.
func (c *AgentCatalog) Infos() []AgentInfo {
	if c == nil {
		return nil
	}
	infos := make([]AgentInfo, 0, len(c.Agents))
	for _, def := range c.Agents {
		infos = append(infos, AgentInfo{Name: def.Name, Description: def.Description})
	}
	return infos
}

// SpawnInfos returns the catalog shape the delegate tool validates against.
func (c *AgentCatalog) SpawnInfos() []tools.AgentInfo {
	if c == nil {
		return nil
	}
	infos := make([]tools.AgentInfo, 0, len(c.Agents))
	for _, def := range c.Agents {
		infos = append(infos, tools.AgentInfo{Name: def.Name, Description: def.Description})
	}
	return infos
}

// agentHeaderKeys is the closed set of definition header keys.
var agentHeaderKeys = [...]string{"name", "description", "model", "thinking_effort", "tools", "max_turns"}

// ParseAgentDefinition parses one definition file: `key: value` lines, a `---`
// line, then the prompt body verbatim. The grammar is deliberately smaller
// than YAML: values are opaque trimmed strings with no quoting, escaping,
// nesting, or sequence syntax, and the only comma-separated value is tools.
func ParseAgentDefinition(data []byte) (AgentDefinition, error) {
	if len(data) > maxAgentDefinitionBytes {
		return AgentDefinition{}, fmt.Errorf("definition is %d bytes; the limit is %d", len(data), maxAgentDefinitionBytes)
	}
	if !utf8.Valid(data) {
		return AgentDefinition{}, errors.New("definition is not valid UTF-8")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	separator := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == "---" {
			separator = i
			break
		}
	}
	if separator < 0 {
		return AgentDefinition{}, errors.New("definition has no --- header terminator")
	}
	header := strings.Join(lines[:separator], "\n")
	body := strings.Join(lines[separator+1:], "\n")
	if strings.TrimSpace(body) == "" {
		return AgentDefinition{}, errors.New("definition has no prompt body after the --- header terminator")
	}

	def := AgentDefinition{Source: agentSourceUser, Prompt: body}
	seen := make(map[string]bool, len(agentHeaderKeys))
	for i, line := range strings.Split(header, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			return AgentDefinition{}, fmt.Errorf("header line %d is not `key: value`", i+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !slices.Contains(agentHeaderKeys[:], key) {
			return AgentDefinition{}, fmt.Errorf("header line %d: unknown key %q", i+1, key)
		}
		if seen[key] {
			return AgentDefinition{}, fmt.Errorf("header line %d: duplicate key %q", i+1, key)
		}
		seen[key] = true
		switch key {
		case "name":
			def.Name = value
		case "description":
			def.Description = value
		case "model":
			def.Model = value
		case "thinking_effort":
			def.ThinkingEffort = value
		case "tools":
			def.Tools = splitAgentTools(value)
		case "max_turns":
			n, err := strconv.Atoi(value)
			if err != nil {
				return AgentDefinition{}, fmt.Errorf("max_turns %q is not an integer", value)
			}
			if n <= 0 {
				return AgentDefinition{}, fmt.Errorf("max_turns must be positive, got %d", n)
			}
			def.MaxTurns = n
		}
	}
	if err := validateAgentDefinition(&def); err != nil {
		return AgentDefinition{}, err
	}
	return def, nil
}

// splitAgentTools parses the comma-separated tools value. An empty value
// yields no names; validation rejects an empty grant.
func splitAgentTools(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		if name := strings.TrimSpace(part); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// validateAgentDefinition enforces the closed contract: a stable name, a
// description, a non-empty tool grant without delegate, and a non-empty prompt
// body. A child with no tools cannot act, and depth is one.
func validateAgentDefinition(def *AgentDefinition) error {
	if def.Name == "" {
		return errors.New("name is required")
	}
	if len(def.Name) > maxAgentNameBytes {
		return fmt.Errorf("name is %d bytes; the limit is %d", len(def.Name), maxAgentNameBytes)
	}
	if !validAgentName(def.Name) {
		return fmt.Errorf("name %q must match [a-z][a-z0-9-]*", def.Name)
	}
	if def.Description == "" {
		return errors.New("description is required")
	}
	if len(def.Description) > maxAgentDescriptionSize {
		return fmt.Errorf("description is %d bytes; the limit is %d", len(def.Description), maxAgentDescriptionSize)
	}
	if len(def.Tools) == 0 {
		return errors.New("tools is required: a child with no tools cannot act")
	}
	for _, name := range def.Tools {
		if name == "delegate" {
			return errors.New("tools must not include delegate: a child cannot delegate")
		}
	}
	if err := tools.ValidateGrant(def.Tools); err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	if strings.TrimSpace(def.Prompt) == "" {
		return errors.New("prompt body is empty")
	}
	if len(def.Prompt) > maxAgentPromptBytes {
		return fmt.Errorf("prompt body is %d bytes; the limit is %d", len(def.Prompt), maxAgentPromptBytes)
	}
	return nil
}

// validAgentName reports whether name is a stable identifier for logs and
// prompts: lowercase letters, digits, and inner hyphens.
func validAgentName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	prevHyphen := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			prevHyphen = false
		case c == '-':
			if prevHyphen || i == len(name)-1 {
				return false
			}
			prevHyphen = true
		default:
			return false
		}
	}
	return true
}

// LoadAgents resolves the effective definition set: the bundled agents, then
// `<state-dir>/agents/*.md` in file-name order replacing a bundled agent of the
// same name. Invalid files are skipped with a warning and never replace a
// bundled agent of the same name. A missing agent directory is not an error.
func LoadAgents(stateDir string) *AgentCatalog {
	catalog := &AgentCatalog{Agents: bundledAgents()}
	dir := filepath.Join(stateDir, "agents")
	root, err := os.OpenRoot(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("read agent directory %s: %v", dir, err))
		}
		return catalog
	}
	defer func() { _ = root.Close() }()

	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("read agent directory %s: %v", dir, err))
		return catalog
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if !entry.Type().IsRegular() {
			catalog.Invalid = append(catalog.Invalid, AgentDefinition{
				Name: entry.Name(), Source: agentSourceUser, Path: path, Err: "not a regular file",
			})
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("skip agent definition %s: not a regular file", path))
			continue
		}
		def, err := readAgentDefinition(root, entry.Name())
		if err != nil {
			catalog.Invalid = append(catalog.Invalid, AgentDefinition{
				Name: entry.Name(), Source: agentSourceUser, Path: path, Err: err.Error(),
			})
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("skip agent definition %s: %v", path, err))
			continue
		}
		def.Source = agentSourceUser
		def.Path = path
		if seen[def.Name] {
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("skip agent definition %s: duplicate agent name %q; the first file wins", path, def.Name))
			continue
		}
		seen[def.Name] = true
		catalog.Agents = replaceOrAppendAgent(catalog.Agents, def)
		if len(catalog.Agents) > maxAgents {
			catalog.Agents = catalog.Agents[:maxAgents]
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("only the first %d agents are loaded; later definitions are ignored", maxAgents))
			break
		}
	}
	slices.SortFunc(catalog.Agents, func(a, b AgentDefinition) int {
		return strings.Compare(a.Name, b.Name)
	})
	return catalog
}

// readAgentDefinition reads one definition file through the state-directory
// root, so no path outside the agent directory is ever read.
func readAgentDefinition(root *os.Root, name string) (AgentDefinition, error) {
	file, err := root.Open(name)
	if err != nil {
		return AgentDefinition{}, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxAgentDefinitionBytes+1))
	if err != nil {
		return AgentDefinition{}, fmt.Errorf("read: %w", err)
	}
	if len(data) > maxAgentDefinitionBytes {
		return AgentDefinition{}, fmt.Errorf("definition exceeds %d bytes", maxAgentDefinitionBytes)
	}
	return ParseAgentDefinition(data)
}

// replaceOrAppendAgent installs def, replacing a bundled agent of the same
// name in place and otherwise appending it.
func replaceOrAppendAgent(agents []AgentDefinition, def AgentDefinition) []AgentDefinition {
	for i, existing := range agents {
		if existing.Name == def.Name {
			agents[i] = def
			return agents
		}
	}
	return append(agents, def)
}

// bundledAgents returns a fresh copy of the compiled-in default set.
func bundledAgents() []AgentDefinition {
	taskTools := tools.NamesWithout("delegate")
	defs := []AgentDefinition{
		{
			Name:        "explore",
			Description: "Read-only codebase exploration: find where something lives and report it.",
			Tools:       []string{"read_file", "search"},
			Source:      agentSourceBundled,
			Prompt:      "Explore the assigned area of the repository and report what you found. Lead with the answer, cite file and line, and include only the facts the caller needs. Do not edit files; if a change is needed, describe it precisely instead.",
		},
		{
			Name:        "implementer",
			Description: "Scoped implementation and verification: edit files and run the tests for one change.",
			Tools:       []string{"read_file", "edit_file", "search", "run_command"},
			Source:      agentSourceBundled,
			Prompt:      "Implement the assigned change, keeping it minimal and focused. Read the surrounding code first, run the relevant tests after editing, and report what changed, which commands you ran, and anything you could not verify.",
		},
		{
			Name:        "researcher",
			Description: "Exploration plus web sources: answer a question from the repository and the web.",
			Tools:       []string{"read_file", "search", "web_search", "web_fetch"},
			Source:      agentSourceBundled,
			Prompt:      "Research the assigned question using the workspace and the web. Cite every claim with a file and line or a URL. Separate what you verified from what you inferred, and say plainly when the evidence is insufficient.",
		},
		{
			Name:        "reviewer",
			Description: "Correctness, regression, and test-gap review of a change.",
			Tools:       []string{"read_file", "search"},
			Source:      agentSourceBundled,
			Prompt:      "Review the assigned change. Lead with concrete findings, cite file and line, and say plainly when you find nothing. Prioritize correctness, regressions, and missing tests, and do not propose unrelated refactors.",
		},
		{
			Name:        "task",
			Description: "General-purpose work with a model-written prompt and every tool except delegate.",
			Tools:       taskTools,
			Source:      agentSourceBundled,
			Prompt:      "Carry out the assigned task end to end. Read before editing, keep changes minimal, run the relevant tests, and report what you changed, what you verified, and what remains.",
		},
	}
	return defs
}
