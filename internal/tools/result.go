// Package tools implements the validated tool registry, workspace
// containment policy, the four v0 tools, and web search.
package tools

import (
	"encoding/json"
	"errors"

	"aharness/internal/model"
)

// Version is the current toolset/prompt version.
const Version = "toolset-v1"

// Code is a stable, model-visible tool result code.
type Code string

const (
	CodeOK               Code = "ok"
	CodeInvalidArgs      Code = "invalid_args"
	CodeUnknownTool      Code = "unknown_tool"
	CodeNotFound         Code = "not_found"
	CodeAmbiguous        Code = "ambiguous"
	CodeOutsideWorkspace Code = "outside_workspace"
	CodeGitProtected     Code = "git_protected"
	CodeBinaryFile       Code = "binary_file"
	CodeTooLarge         Code = "too_large"
	CodeTimeout          Code = "timeout"
	CodeCanceled         Code = "canceled"
	CodePanic            Code = "panic"
	CodeSpawnFailed      Code = "spawn_failed"
	CodeUnavailable      Code = "unavailable"
	CodeHTTPError        Code = "http_error"
	CodeFetchFailed      Code = "fetch_failed"
	CodeInternal         Code = "internal"
)

// Result is the canonical tool result exchanged with the model and logged in
// the transcript.
type Result struct {
	OK         bool            `json:"ok"`
	Code       Code            `json:"code"`
	Output     string          `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	ExitCode   *int            `json:"exit_code,omitempty"`
	TimedOut   bool            `json:"timed_out,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`
}

// Success returns a successful result with output.
func Success(output string) Result { return Result{OK: true, Code: CodeOK, Output: output} }

// Failure returns a failed result with a stable code and short message.
func Failure(code Code, message string) Result {
	return Result{OK: false, Code: code, Error: message}
}

// Sentinel errors let the dispatcher map tool-internal failures to stable
// codes.
var (
	ErrOutsideWorkspace = errors.New("path outside workspace")
	ErrGitProtected     = errors.New("git directory is protected")
	ErrEditAmbiguous    = errors.New("edit matches more than once")
	ErrEditNotFound     = errors.New("edit target not found")
	ErrBinaryFile       = errors.New("binary or non-utf8 file")
	ErrTooLarge         = errors.New("file too large")
)

// Options configures harness-owned tool resources. TmpDir is the per-run
// TMPDIR the run_command child receives; WorkDir is the per-run work directory
// used to spill oversized command output, and an empty WorkDir disables
// spilling. SearXNGURL is the validated base URL for web_search; an empty value
// keeps the tool registered but returns an actionable unavailable result.
// UserAgent identifies the harness on outbound tool requests.
type Options struct {
	TmpDir     string
	WorkDir    string
	SearXNGURL string
	UserAgent  string
}

// Schema returns the canonical declaration for one tool.
func schema(name, description string, parameters string) model.ToolSchema {
	return model.ToolSchema{Name: name, Description: description, Parameters: json.RawMessage(parameters)}
}
