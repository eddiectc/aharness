// Package tools implements the validated tool registry, workspace
// containment policy, the built-in workspace/web tools, and the per-run todo
// list.
package tools

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/eddiectc/aharness/model"
)

// Version is the current toolset/prompt version.
const Version = "toolset-v2"

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
	// CodeBlockedAddress rejects a URL or redirect that resolves to a
	// non-global unicast or special-purpose address.
	CodeBlockedAddress Code = "blocked_address"
	// CodeUnsupportedContentType rejects a non-text media type or an
	// unsupported content encoding.
	CodeUnsupportedContentType Code = "unsupported_content_type"
	// CodeContentUnavailable reports a 2xx response with no usable text, such
	// as a bot challenge, JavaScript shell, or empty extraction.
	CodeContentUnavailable Code = "content_unavailable"
	// CodeSubagentLimit reports that at least one delegated child stopped at
	// its own budget, so its report is partial.
	CodeSubagentLimit Code = "subagent_limit"
	CodeInternal      Code = "internal"
	// CodePermissionDenied reports a host policy denial from Authorize. The
	// tool did not run; the model sees an actionable, non-retryable result.
	CodePermissionDenied Code = "permission_denied"
	// CodeToolError reports a custom tool that returned an error without a
	// more specific code.
	CodeToolError Code = "tool_error"
)

// Custom-tool recording caps. They bound session_start.custom_tools and the
// public ToolsConfig surface.
const (
	MaxCustomTools           = 32
	MaxCustomToolDescription = 2 << 10
	MaxCustomToolParams      = 32 << 10
	MaxCustomToolBytes       = 64 << 10
	MaxToolNameBytes         = 64
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
// UserAgent identifies the host on outbound web requests, for example
// "acode/1.2.0". It is required for web_search and web_fetch; a missing value
// makes those tools return an unavailable result.
//
// WebTimeout bounds one web_fetch request, covering connect, redirects, and
// body reads; zero selects WebTimeoutDefault. WebFetchProvider selects the
// compiled-in adapter (WebFetchProviderBuiltin or WebFetchProviderCrawl4AI)
// once; an empty value selects builtin. WebFetchURL is the Crawl4AI endpoint and
// WebFetchKey its bearer token, which is never logged. WebFetchUserAgent
// overrides UserAgent for web_fetch only. WebClient is a test seam that routes
// fixture requests; production leaves it nil, and constructors build a
// dedicated client with the address guard. WebAllowLoopback is the matching
// test-only seam: it relaxes the address guard so httptest servers are
// reachable. It is not a flag, environment variable, or config key, and
// production wiring never sets it.
//
// Spawning runs delegated `delegate` tasks. A nil value keeps the tool
// registered and returns an actionable unavailable result.
type Options struct {
	TmpDir            string
	WorkDir           string
	SearXNGURL        string
	UserAgent         string
	WebFetchUserAgent string
	SearchTimeout     time.Duration
	WebTimeout        time.Duration
	WebFetchProvider  string
	WebFetchURL       string
	WebFetchKey       string
	WebClient         *http.Client
	WebAllowLoopback  bool
	Spawning          Spawning
}

// Schema returns the canonical declaration for one tool.
func schema(name, description string, parameters string) model.ToolSchema {
	return model.ToolSchema{Name: name, Description: description, Parameters: json.RawMessage(parameters)}
}
