package tools

import "aharness/internal/model"

// v0Schemas are the hand-written tool declarations. They are versioned prompt
// material: a change requires a toolset version bump.
var v0Schemas = []model.ToolSchema{
	schema("read_file",
		"Read a UTF-8 text file from the workspace. Returns numbered lines and a 'lines X-Y of Z' header. offset is 1-based; limit defaults to 100 and is capped at 500. Rejects binary files and files over 2 MiB.",
		`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Workspace-relative or absolute path inside the workspace."},
    "offset": {"type": "integer", "minimum": 1, "default": 1, "description": "First line to return, 1-based."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 500, "default": 100, "description": "Maximum number of lines to return."}
  },
  "required": ["path"],
  "additionalProperties": false
}`),
	schema("edit_file",
		"Edit a workspace file by exact byte match. old_string must occur exactly once unless replace_all is true. Creates a new file only when old_string is empty and the file does not exist. Preserves line endings and file mode; rejects symlinks and .git paths.",
		`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Workspace-relative or absolute path inside the workspace."},
    "old_string": {"type": "string", "description": "Exact text to replace; empty only when creating a new file."},
    "new_string": {"type": "string", "description": "Replacement text."},
    "replace_all": {"type": "boolean", "default": false, "description": "Replace every occurrence instead of requiring a unique match."}
  },
  "required": ["path", "old_string", "new_string"],
  "additionalProperties": false
}`),
	schema("search",
		"Search workspace text files with a Go RE2 regular expression. Returns path:line: text. glob matches workspace-relative paths with path.Match (no **). Skips .git, binary files, and files over 1 MiB. max_results defaults to 100 and is capped at 200.",
		`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Go RE2 regular expression."},
    "path": {"type": "string", "default": ".", "description": "Directory to search, relative to the workspace."},
    "glob": {"type": "string", "description": "Optional path.Match glob against workspace-relative paths."},
    "case_sensitive": {"type": "boolean", "default": false, "description": "Match case-sensitively."},
    "max_results": {"type": "integer", "minimum": 1, "maximum": 200, "default": 100, "description": "Maximum matching lines to return."}
  },
  "required": ["pattern"],
  "additionalProperties": false
}`),
	schema("run_command",
		"Run a shell command with /bin/sh -c in the workspace. cwd must be inside the workspace. Non-zero exits are returned as normal results with exit_code. timeout_ms defaults to 120000 and is capped at 600000.",
		`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command to execute."},
    "cwd": {"type": "string", "default": ".", "description": "Working directory relative to the workspace."},
    "timeout_ms": {"type": "integer", "minimum": 1, "maximum": 600000, "default": 120000, "description": "Command timeout in milliseconds."}
  },
  "required": ["command"],
  "additionalProperties": false
}`),
}

// Schemas returns the v0 tool declarations.
func Schemas() []model.ToolSchema {
	return append([]model.ToolSchema(nil), v0Schemas...)
}
