package tools

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"aharness/internal/model"
)

// toolFunc is the internal shape of one registered tool. Arguments are decoded
// by the tool implementation into its typed struct.
type toolFunc func(ctx context.Context, args json.RawMessage) Result

// Registry is the validated tool dispatcher. It owns the workspace, the
// harness-owned command environment, the search backend, the web fetch
// provider configuration, and the per-run todo list.
type Registry struct {
	ws     *Workspace
	opts   Options
	fs     map[string]toolFunc
	search *searchClient
	web    *webFetchTool
	todos  todoState
	grant  []string
}

// NewRegistry constructs the tool registry for one workspace.
func NewRegistry(ws *Workspace, opts Options) *Registry {
	r := &Registry{ws: ws, opts: opts, search: newSearchClient(opts), web: newWebFetchTool(opts)}
	r.fs = map[string]toolFunc{
		"read_file":   r.executeRead,
		"edit_file":   r.executeEdit,
		"search":      r.executeSearch,
		"run_command": r.executeRunCommand,
		"web_search":  r.executeWebSearch,
		"web_fetch":   r.executeWebFetch,
		"todo":        r.executeTodo,
	}
	return r
}

// SetGrant narrows the registry to a tool-name allowlist. A nil grant keeps
// every tool of the current toolset. The grant is enforced twice: Schemas
// returns only granted declarations, and Execute rejects a call to a tool the
// grant omits without running it.
func (r *Registry) SetGrant(names []string) {
	r.grant = append([]string(nil), names...)
	if names == nil {
		r.grant = nil
	}
}

// granted reports whether name is inside the current grant.
func (r *Registry) granted(name string) bool {
	if r.grant == nil {
		return true
	}
	for _, allowed := range r.grant {
		if allowed == name {
			return true
		}
	}
	return false
}

// Schemas returns a copy of the current tool declarations, narrowed to the
// recorded grant when one is set.
func (r *Registry) Schemas() []model.ToolSchema {
	schemas := Schemas()
	if r.grant == nil {
		return schemas
	}
	filtered, err := Filter(schemas, r.grant)
	if err != nil {
		return nil
	}
	return filtered
}

// Execute validates and runs one tool call without a parent call identifier.
// Panics are recovered at this boundary, converted to a panic result with a
// capped stack, and returned to the caller; pairing and stopping are the
// runner's responsibility. Tools are never retried automatically.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) Result {
	return r.ExecuteCall(ctx, Call{}, name, args)
}

// ExecuteCall runs one tool call with the parent-run facts a delegated tool
// needs, such as the tool-call ID recorded as a child's parent_call_id.
func (r *Registry) ExecuteCall(ctx context.Context, call Call, name string, args json.RawMessage) (res Result) {
	defer func() {
		if v := recover(); v != nil {
			res = panicResult(v)
		}
	}()
	if !r.granted(name) {
		return Failure(CodeUnknownTool, fmt.Sprintf("unknown tool %q: not granted to this session", name))
	}
	if name == "delegate" {
		return r.executeDelegate(ctx, args, call)
	}
	fn, ok := r.fs[name]
	if !ok {
		return Failure(CodeUnknownTool, fmt.Sprintf("unknown tool %q: not registered", name))
	}
	return fn(ctx, args)
}

// decodeArgs decodes tool arguments strictly: unknown object members are
// rejected so a stale or hallucinated field is model-visible instead of
// silently ignored. Each typed shape carries the shared display-only intent
// member so the TUI can title the tool block; execution never reads it.
func decodeArgs(name string, args json.RawMessage, v any) error {
	if err := jsonv2.Unmarshal(args, v, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// workspaceRel normalizes a tool path and adds the offending path to
// containment errors for the model.
func (r *Registry) workspaceRel(p string) (string, error) {
	rel, err := r.ws.rel(p)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, p)
	}
	return rel, nil
}

// invalidArgs builds a stable invalid-arguments result that names the tool.
func invalidArgs(tool, format string, args ...any) Result {
	return Failure(CodeInvalidArgs, tool+": "+fmt.Sprintf(format, args...))
}

// errorResult maps tool-internal sentinel and standard errors to stable,
// model-visible result codes.
func errorResult(err error) Result {
	if err == nil {
		return Failure(CodeInternal, "unknown tool error")
	}
	message := sanitizeUTF8(err.Error())
	switch {
	case errors.Is(err, ErrOutsideWorkspace):
		return Failure(CodeOutsideWorkspace, message)
	case errors.Is(err, ErrGitProtected):
		return Failure(CodeGitProtected, message)
	case errors.Is(err, ErrEditAmbiguous):
		return Failure(CodeAmbiguous, message)
	case errors.Is(err, ErrEditNotFound):
		return Failure(CodeNotFound, message)
	case errors.Is(err, ErrBinaryFile):
		return Failure(CodeBinaryFile, message)
	case errors.Is(err, ErrTooLarge):
		return Failure(CodeTooLarge, message)
	case errors.Is(err, errSymlink):
		return Failure(CodeOutsideWorkspace, message)
	case errors.Is(err, errNotRegular), errors.Is(err, errNotDir):
		return Failure(CodeInvalidArgs, message)
	case errors.Is(err, os.ErrNotExist):
		return Failure(CodeNotFound, message)
	case errors.Is(err, context.DeadlineExceeded):
		return Failure(CodeTimeout, message)
	case errors.Is(err, context.Canceled):
		return Failure(CodeCanceled, message)
	default:
		return Failure(CodeInternal, message)
	}
}

// panicResult converts a recovered panic into a result. The stack is capped so
// a pathological panic cannot flood the transcript.
func panicResult(v any) Result {
	stack, _ := capOutput(string(debug.Stack()), panicStackCap)
	return Result{
		OK:     false,
		Code:   CodePanic,
		Output: sanitizeUTF8(stack),
		Error:  sanitizeUTF8(strings.TrimSuffix(fmt.Sprintf("panic: %v", v), "\n")),
	}
}
