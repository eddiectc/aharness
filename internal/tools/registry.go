package tools

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"unicode/utf8"

	"github.com/eddiectc/aharness/internal/model"
)

// toolFunc is the internal shape of one built-in tool. Arguments are decoded by
// the tool implementation into its typed struct.
type toolFunc func(ctx context.Context, args json.RawMessage) Result

// customTool is one caller-registered tool. The call identifier and turn are
// passed through so the public adapter can expose the model's call facts.
type customTool struct {
	schema          model.ToolSchema
	fn              func(ctx context.Context, call Call, args json.RawMessage) Result
	concurrencySafe bool
}

// Registry is the validated tool dispatcher. It owns the workspace, the
// harness-owned command environment, the search backend, the web fetch
// provider configuration, and the per-run todo list.
type Registry struct {
	ws          *Workspace
	opts        Options
	fs          map[string]toolFunc
	custom      map[string]customTool
	customOrder []string
	search      *searchClient
	web         *webFetchTool
	todos       todoState
	grant       []string
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

// SetSpawning attaches the delegated-task runner to an already constructed
// registry. The engine builds the spawner before the registry so a custom-tool
// factory can share the same options; this closes the construction cycle.
func (r *Registry) SetSpawning(sp Spawning) {
	r.opts.Spawning = sp
}

// SetGrant narrows the registry to a tool-name allowlist. A nil grant keeps
// every tool of the current toolset; a non-nil empty grant allows no tools. The
// grant is enforced twice: Schemas returns only granted declarations, and
// Execute rejects a call to a tool the grant omits without running it.
func (r *Registry) SetGrant(names []string) {
	r.grant = cloneNames(names)
}

// cloneNames preserves the nil/non-nil distinction: nil means every tool, an
// empty non-nil slice means none.
func cloneNames(names []string) []string {
	if names == nil {
		return nil
	}
	return append([]string{}, names...)
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

// Register adds one caller tool to the registry. The schema name must not
// collide with a built-in or an already registered custom tool. Parameters
// must be a JSON Schema object; use {} for a tool with no arguments. The
// declaration is recorded separately by the agent, so registration is also
// validated by the public construction path.
func (r *Registry) Register(schema model.ToolSchema, fn func(ctx context.Context, call Call, args json.RawMessage) Result, concurrencySafe bool) error {
	if err := ValidateCustomTool(schema); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("register tool %q: run function is required", schema.Name)
	}
	if schema.Name == "delegate" {
		return fmt.Errorf("%w: delegate is controlled by Delegation", ErrToolConflict)
	}
	if _, ok := r.fs[schema.Name]; ok {
		return fmt.Errorf("%w: %q is a built-in tool", ErrToolConflict, schema.Name)
	}
	if _, ok := r.custom[schema.Name]; ok {
		return fmt.Errorf("%w: %q is already registered", ErrToolConflict, schema.Name)
	}
	if r.custom == nil {
		r.custom = make(map[string]customTool)
	}
	r.custom[schema.Name] = customTool{schema: schema, fn: fn, concurrencySafe: concurrencySafe}
	r.customOrder = append(r.customOrder, schema.Name)
	return nil
}

// ErrToolConflict marks a custom tool name that collides with a built-in or a
// previously registered custom tool.
var ErrToolConflict = errors.New("tool name conflict")

// ValidateCustomTool enforces the public custom-tool schema contract.
func ValidateCustomTool(schema model.ToolSchema) error {
	if err := ValidateToolName(schema.Name); err != nil {
		return err
	}
	if strings.TrimSpace(schema.Description) == "" {
		return fmt.Errorf("tool %q: description is required", schema.Name)
	}
	if !utf8.ValidString(schema.Description) {
		return fmt.Errorf("tool %q: description is not valid UTF-8", schema.Name)
	}
	if len(schema.Description) > MaxCustomToolDescription {
		return fmt.Errorf("tool %q: description is %d bytes, limit %d", schema.Name, len(schema.Description), MaxCustomToolDescription)
	}
	if len(schema.Parameters) == 0 {
		return fmt.Errorf("tool %q: input schema is required", schema.Name)
	}
	if len(schema.Parameters) > MaxCustomToolParams {
		return fmt.Errorf("tool %q: input schema is %d bytes, limit %d", schema.Name, len(schema.Parameters), MaxCustomToolParams)
	}
	if !jsontext.Value(schema.Parameters).IsValid() {
		return fmt.Errorf("tool %q: input schema is not valid JSON", schema.Name)
	}
	trimmed := strings.TrimSpace(string(schema.Parameters))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("tool %q: input schema must be a JSON object", schema.Name)
	}
	return nil
}

// ValidateToolName enforces the stable custom-tool name grammar.
func ValidateToolName(name string) error {
	if name == "" {
		return errors.New("tool name is required")
	}
	if len(name) > MaxToolNameBytes {
		return fmt.Errorf("tool name %q exceeds %d bytes", name, MaxToolNameBytes)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("tool name %q must use lowercase ASCII letters, digits, '_' or '-'", name)
		}
	}
	return nil
}

// CustomSchemas returns copies of the registered custom declarations in
// registration order.
func (r *Registry) CustomSchemas() []model.ToolSchema {
	if len(r.customOrder) == 0 {
		return nil
	}
	out := make([]model.ToolSchema, 0, len(r.customOrder))
	for _, name := range r.customOrder {
		out = append(out, r.custom[name].schema)
	}
	return out
}

// Schemas returns a copy of the current tool declarations (built-ins followed
// by registered custom tools), narrowed to the recorded grant when one is set.
func (r *Registry) Schemas() []model.ToolSchema {
	schemas := Schemas()
	schemas = append(schemas, r.CustomSchemas()...)
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
	if custom, ok := r.custom[name]; ok {
		return custom.fn(ctx, call, args)
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
