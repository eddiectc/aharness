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

	"github.com/eddiectc/aharness/tools"
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
	SourceDefault = "default" // a host-provided base default
	SourceUser    = "user"    // <state-dir>/agents/*.md
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
	// Source is SourceDefault for a host-provided base definition or SourceUser
	// for a definition file.
	Source string
	// Path is the definition file for a user agent; empty for a default.
	Path string
	// Err is the parse or validation error for an invalid entry.
	Err string

	// DeclaredModel, DeclaredThinkingEffort, and DeclaredMaxTurns are the
	// definition's values before an operator override. They let the definitions
	// browser show both the effective value and where it came from.
	DeclaredModel          string
	DeclaredThinkingEffort string
	DeclaredMaxTurns       int
	// Override is the operator override applied to this definition; nil when
	// none applies. It is a copy, so mutating it cannot change the catalog.
	Override *AgentOverride
	// Disabled is set by an operator override with enabled=false. A disabled
	// definition stays discoverable in the browser but is not runnable.
	Disabled bool
}

// Valid reports whether the entry can be run.
func (d AgentDefinition) Valid() bool { return d.Err == "" }

// AgentCatalog is the discovered, validated definition set for one process.
// Invalid holds the base entries and user files that were skipped so the
// definitions browser can explain why. The catalog is immutable after loading:
// a mid-run edit cannot change what a child in flight will run.
type AgentCatalog struct {
	Agents   []AgentDefinition
	Invalid  []AgentDefinition
	Warnings []string
}

// Lookup returns the runnable definition with the exact name. A disabled
// definition is discoverable but not runnable, so it is reported as absent;
// Use LookupDefinition when the caller must explain that state.
func (c *AgentCatalog) Lookup(name string) (AgentDefinition, bool) {
	def, ok := c.LookupDefinition(name)
	if !ok || def.Disabled {
		return AgentDefinition{}, false
	}
	return def, true
}

// LookupDefinition returns the definition with the exact name whether or not
// an operator override disabled it. It is for the definitions browser and for
// continuation diagnostics, never for starting a fresh child.
func (c *AgentCatalog) LookupDefinition(name string) (AgentDefinition, bool) {
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
// A disabled agent is omitted: the model must not be offered a name that
// delegate rejects.
func (c *AgentCatalog) Infos() []AgentInfo {
	if c == nil {
		return nil
	}
	infos := make([]AgentInfo, 0, len(c.Agents))
	for _, def := range c.Agents {
		if def.Disabled {
			continue
		}
		infos = append(infos, AgentInfo{Name: def.Name, Description: def.Description})
	}
	return infos
}

// SpawnInfos returns the catalog shape the delegate tool validates against.
// Disabled agents are omitted for the same reason as Infos.
func (c *AgentCatalog) SpawnInfos() []tools.AgentInfo {
	if c == nil {
		return nil
	}
	infos := make([]tools.AgentInfo, 0, len(c.Agents))
	for _, def := range c.Agents {
		if def.Disabled {
			continue
		}
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

	def := AgentDefinition{Source: SourceUser, Prompt: body}
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

// ValidateAgentName enforces the stable identifier shape shared by definition
// files and operator overrides: lowercase letters, digits, and inner hyphens,
// at most maxAgentNameBytes.
func ValidateAgentName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > maxAgentNameBytes {
		return fmt.Errorf("name is %d bytes; the limit is %d", len(name), maxAgentNameBytes)
	}
	if !validAgentName(name) {
		return fmt.Errorf("name %q must match [a-z][a-z0-9-]*", name)
	}
	return nil
}

// validateAgentDefinition enforces the closed contract: a stable name, a
// description, a non-empty tool grant without delegate, and a non-empty prompt
// body. A child with no tools cannot act, and depth is one.
func validateAgentDefinition(def *AgentDefinition) error {
	if err := ValidateAgentName(def.Name); err != nil {
		return err
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

// LoadAgents resolves the effective definition set: the host-supplied base
// first, then `<state-dir>/agents/*.md` in file-name order, replacing a base
// entry of the same name and appending new names. A nil or empty base means no
// host defaults. Invalid base entries and files are skipped with a warning and
// never replace a valid entry; a missing agent directory is not an error. The
// 32-agent cap applies to the combined set.
func LoadAgents(base []AgentDefinition, stateDir string) *AgentCatalog {
	catalog := &AgentCatalog{}
	seen := make(map[string]bool, len(base))
	for i, supplied := range base {
		def := cloneAgentDefinition(supplied)
		def.Source = SourceDefault
		def.Path = ""
		def.Err = ""
		label := def.Name
		if label == "" {
			label = fmt.Sprintf("base[%d]", i)
		}
		if err := validateAgentDefinition(&def); err != nil {
			def.Err = err.Error()
			catalog.Invalid = append(catalog.Invalid, def)
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("skip agent definition %s: %v", label, err))
			continue
		}
		if seen[def.Name] {
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("skip agent definition %s: duplicate agent name %q; the first definition wins", label, def.Name))
			continue
		}
		seen[def.Name] = true
		catalog.Agents = append(catalog.Agents, def)
	}
	if len(catalog.Agents) > maxAgents {
		catalog.Agents = catalog.Agents[:maxAgents]
		catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("only the first %d agents are loaded; later definitions are ignored", maxAgents))
	}
	loadAgentDirectory(catalog, stateDir)
	slices.SortFunc(catalog.Agents, func(a, b AgentDefinition) int {
		return strings.Compare(a.Name, b.Name)
	})
	return catalog
}

// cloneAgentDefinition returns a catalog-safe copy of a caller-supplied base
// definition, including its owned tool slice and override.
func cloneAgentDefinition(def AgentDefinition) AgentDefinition {
	def.Tools = slices.Clone(def.Tools)
	if def.Override != nil {
		override := def.Override.Clone()
		def.Override = &override
	}
	return def
}

// loadAgentDirectory adds the user definitions under <state-dir>/agents to
// catalog, replacing a base entry of the same name. A missing directory is not
// an error; an invalid entry is recorded in Invalid with a warning and never
// replaces anything.
func loadAgentDirectory(catalog *AgentCatalog, stateDir string) {
	dir := filepath.Join(stateDir, "agents")
	root, err := os.OpenRoot(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("read agent directory %s: %v", dir, err))
		}
		return
	}
	defer func() { _ = root.Close() }()

	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("read agent directory %s: %v", dir, err))
		return
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if !entry.Type().IsRegular() {
			catalog.Invalid = append(catalog.Invalid, AgentDefinition{
				Name: entry.Name(), Source: SourceUser, Path: path, Err: "not a regular file",
			})
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("skip agent definition %s: not a regular file", path))
			continue
		}
		def, err := readAgentDefinition(root, entry.Name())
		if err != nil {
			catalog.Invalid = append(catalog.Invalid, AgentDefinition{
				Name: entry.Name(), Source: SourceUser, Path: path, Err: err.Error(),
			})
			catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("skip agent definition %s: %v", path, err))
			continue
		}
		def.Source = SourceUser
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

// replaceOrAppendAgent installs def, replacing a base agent of the same name in
// place and otherwise appending it.
func replaceOrAppendAgent(agents []AgentDefinition, def AgentDefinition) []AgentDefinition {
	for i, existing := range agents {
		if existing.Name == def.Name {
			agents[i] = def
			return agents
		}
	}
	return append(agents, def)
}
