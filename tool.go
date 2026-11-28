package aharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/tools"
)

// ToolName is a stable tool identifier.
type ToolName string

const (
	ToolReadFile   ToolName = "read_file"
	ToolReadImage  ToolName = "read_image"
	ToolEditFile   ToolName = "edit_file"
	ToolSearch     ToolName = "search"
	ToolRunCommand ToolName = "run_command"
	ToolWebSearch  ToolName = "web_search"
	ToolWebFetch   ToolName = "web_fetch"
	ToolTodo       ToolName = "todo"
	ToolDelegate   ToolName = "delegate"
)

// Tool is one caller tool. Name, Description, InputSchema, and Run are
// required. InputSchema is a JSON Schema object; use {} for no arguments.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Run         func(ctx context.Context, call ToolCall) (ToolResult, error)
	// ConcurrencySafe declares that concurrent delegated children may call this
	// tool at the same time. It never permits concurrent calls within one run.
	ConcurrencySafe bool
}

// ToolResult is the structured result returned by a caller tool.
type ToolResult struct {
	OK        bool
	Code      string
	Output    string
	Error     string
	ExitCode  *int
	TimedOut  bool
	Truncated bool
	Details   json.RawMessage
	// Image is one encoded image to attach to the result: PNG, JPEG, or GIF.
	// The type comes from the bytes, and the SDK normalizes them exactly as
	// read_image does, so a full-page capture need not be pre-sized. An image it
	// cannot deliver fails the result with invalid_image, too_large, or
	// unavailable and is never recorded. Output is the text the model reads
	// beside it.
	Image []byte
}

// TextResult returns a successful text result.
func TextResult(text string) ToolResult { return ToolResult{OK: true, Code: "ok", Output: text} }

// ErrorResult returns a failed result with a stable code and message.
func ErrorResult(code, message string) ToolResult {
	return ToolResult{OK: false, Code: code, Error: message}
}

// ToolsConfig selects built-in tools, registers caller tools, and configures
// the web and delegation tools.
type ToolsConfig struct {
	// Disable omits built-in tools by name. Unknown, duplicate, or delegate
	// names are a ConfigError.
	Disable []ToolName
	// RootGrant narrows the tools offered to the root session for this run.
	// Nil keeps the session grant; a non-nil empty grant offers the root no
	// tools. It never widens authority and never changes a child definition's
	// Tools: child grants are still intersected with the session grant. Names
	// must be enabled built-ins or registered custom tools, and delegate
	// requires Delegation.
	RootGrant []ToolName
	// Extra registers caller tools. Names must be unique and must not collide
	// with a built-in name.
	Extra []Tool
	// WebSearch configures the SearXNG endpoint. Nil keeps the tool registered
	// and returns an actionable unavailable result.
	WebSearch *WebSearchConfig
	// WebFetch configures the web fetch provider. Nil selects the builtin
	// provider.
	WebFetch *WebFetchConfig
	// Delegation enables the delegate tool. Nil disables it.
	Delegation *DelegationConfig
}

// WebSearchConfig configures the SearXNG endpoint.
type WebSearchConfig struct {
	BaseURL string
	Timeout time.Duration
}

// WebFetchProvider selects the compiled-in web fetch adapter.
type WebFetchProvider string

const (
	WebFetchBuiltin  WebFetchProvider = "builtin"
	WebFetchCrawl4AI WebFetchProvider = "crawl4ai"
)

// WebFetchConfig configures the web fetch provider.
type WebFetchConfig struct {
	Provider  WebFetchProvider
	Endpoint  string
	Key       string
	Timeout   time.Duration
	UserAgent string
	// AllowedOrigins lists exact http(s) origins the builtin fetcher may reach
	// even when they resolve to loopback, private, or shared address space,
	// for example "http://127.0.0.1:8888". Each entry is scheme://host[:port];
	// a path, query, fragment, or userinfo is a ConfigError, and a duplicate is
	// collapsed. The list is host configuration: the model and fetched content
	// can never widen it. Loopback and private destinations are refused
	// without it, and metadata, link-local, unspecified, multicast,
	// documentation, benchmarking, reserved, IPv4-mapped, and NAT64-wrapped
	// addresses stay refused even with it. The Crawl4AI provider fetches
	// subresources itself and rejects a non-empty list.
	AllowedOrigins []string
}

// ToolRequest is the policy input for one tool execution. It is available for
// built-in and custom tools, parent and child.
type ToolRequest struct {
	Tool    ToolName
	Call    ToolCall
	Intent  string
	Turn    int
	Session string
	Agent   string
	// Path is the absolute, lexically cleaned target of a built-in file tool:
	// read_file, read_image, edit_file, or search. It is resolved against the
	// workspace and search defaults to the workspace root when its path is
	// omitted. Path is
	// empty for every other tool and when the arguments do not carry a usable
	// path. It is not symlink-resolved, and run_command has no Path because its
	// writes are not constrained by its cwd. The tools still perform their own
	// containment and symlink checks; Path is policy input, not validation.
	Path string
}

// toolSetup is the resolved tool contract and registry factory.
type toolSetup struct {
	grant     []string
	rootGrant []string
	custom    []agent.CustomTool
	options   tools.Options
	enabled   map[ToolName]bool
	factory   func(ws *tools.Workspace, sessionID, workDir, tmpDir string) (*tools.Registry, error)
}

// resolveToolSetup normalizes ToolsConfig into an explicit grant, the recorded
// custom declarations, tool options, and a registry factory.
func resolveToolSetup(cfg *ToolsConfig, userAgent string, delegation bool) (*toolSetup, error) {
	setup := &toolSetup{enabled: make(map[ToolName]bool)}
	builtinSchemas, err := tools.SchemasFor(tools.Version)
	if err != nil {
		return nil, publicError(err)
	}
	// selected preserves declaration order. delegate is included only when
	// delegation is configured.
	selected := make([]string, 0, len(builtinSchemas))
	known := make(map[ToolName]bool, len(builtinSchemas))
	for _, schema := range builtinSchemas {
		name := ToolName(schema.Name)
		known[name] = true
		if name == ToolDelegate && !delegation {
			continue
		}
		selected = append(selected, schema.Name)
	}
	if cfg != nil {
		disabled := make(map[ToolName]bool, len(cfg.Disable))
		for _, name := range cfg.Disable {
			if name == ToolDelegate {
				return nil, &ConfigError{Field: "Tools.Disable", Value: string(name), Detail: "delegate is controlled only by Delegation"}
			}
			if !known[name] {
				return nil, &ConfigError{Field: "Tools.Disable", Value: string(name), Detail: "is not a built-in tool"}
			}
			if disabled[name] {
				return nil, &ConfigError{Field: "Tools.Disable", Value: string(name), Detail: "is duplicated"}
			}
			disabled[name] = true
		}
		if len(disabled) > 0 {
			kept := selected[:0]
			for _, name := range selected {
				if !disabled[ToolName(name)] {
					kept = append(kept, name)
				}
			}
			selected = kept
		}
		for _, tool := range cfg.Extra {
			if tool.Run == nil {
				return nil, &ConfigError{Field: "Tools.Extra", Value: tool.Name, Detail: "Run is required"}
			}
			schema := ToolSchema{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema}
			if err := tools.ValidateCustomTool(toInternalSchema(schema)); err != nil {
				return nil, &ConfigError{Field: "Tools.Extra", Value: tool.Name, Detail: err.Error()}
			}
			if known[ToolName(tool.Name)] {
				return nil, toolConflictError(tool.Name, "collides with a built-in tool")
			}
			for _, existing := range setup.custom {
				if existing.Schema.Name == tool.Name {
					return nil, toolConflictError(tool.Name, "is registered more than once")
				}
			}
			setup.custom = append(setup.custom, agent.CustomTool{Schema: toInternalSchema(schema), ConcurrencySafe: tool.ConcurrencySafe})
		}
	}
	if err := agent.ValidateCustomTools(setup.custom); err != nil {
		return nil, &ConfigError{Field: "Tools.Extra", Detail: err.Error()}
	}
	for _, tool := range setup.custom {
		selected = append(selected, tool.Schema.Name)
	}
	setup.grant = append([]string{}, selected...)
	for _, name := range setup.grant {
		setup.enabled[ToolName(name)] = true
	}
	if cfg != nil && cfg.RootGrant != nil {
		root := make([]string, 0, len(cfg.RootGrant))
		seen := make(map[ToolName]bool, len(cfg.RootGrant))
		for _, name := range cfg.RootGrant {
			if seen[name] {
				return nil, &ConfigError{Field: "Tools.RootGrant", Value: string(name), Detail: "is duplicated"}
			}
			seen[name] = true
			if !setup.enabled[name] {
				return nil, &ConfigError{Field: "Tools.RootGrant", Value: string(name), Detail: "is not an enabled tool of this session"}
			}
			root = append(root, string(name))
		}
		setup.rootGrant = root
	}
	if cfg != nil && cfg.WebSearch != nil {
		baseURL, err := tools.ParseSearXNGURL(cfg.WebSearch.BaseURL)
		if err != nil {
			return nil, &ConfigError{Field: "Tools.WebSearch.BaseURL", Value: cfg.WebSearch.BaseURL, Detail: err.Error()}
		}
		setup.options.SearXNGURL = baseURL
		setup.options.SearchTimeout = cfg.WebSearch.Timeout
	}
	if cfg != nil && cfg.WebFetch != nil {
		provider, err := tools.ParseWebFetchProvider(string(cfg.WebFetch.Provider))
		if err != nil {
			return nil, &ConfigError{Field: "Tools.WebFetch.Provider", Value: string(cfg.WebFetch.Provider), Detail: err.Error()}
		}
		endpoint, err := tools.ParseWebFetchURL(cfg.WebFetch.Endpoint)
		if err != nil {
			return nil, &ConfigError{Field: "Tools.WebFetch.Endpoint", Value: cfg.WebFetch.Endpoint, Detail: err.Error()}
		}
		if err := tools.ValidateWebFetchConfig(provider, endpoint, cfg.WebFetch.Key); err != nil {
			return nil, &ConfigError{Field: "Tools.WebFetch", Detail: err.Error()}
		}
		origins, err := tools.ParseWebAllowedOrigins(cfg.WebFetch.AllowedOrigins)
		if err != nil {
			return nil, &ConfigError{Field: "Tools.WebFetch.AllowedOrigins", Detail: err.Error()}
		}
		if err := tools.ValidateWebFetchAllowedOrigins(provider, origins); err != nil {
			return nil, &ConfigError{Field: "Tools.WebFetch.AllowedOrigins", Detail: err.Error()}
		}
		setup.options.WebAllowedOrigins = origins
		setup.options.WebFetchProvider = provider
		setup.options.WebFetchURL = endpoint
		setup.options.WebFetchKey = cfg.WebFetch.Key
		setup.options.WebFetchUserAgent = cfg.WebFetch.UserAgent
	}
	setup.options.WebTimeout = 0
	if cfg != nil && cfg.WebFetch != nil {
		setup.options.WebTimeout = cfg.WebFetch.Timeout
	}
	setup.options.UserAgent = userAgent
	setup.factory = func(ws *tools.Workspace, sessionID, workDir, tmpDir string) (*tools.Registry, error) {
		opts := setup.options
		opts.WorkDir = workDir
		opts.TmpDir = tmpDir
		registry := tools.NewRegistry(ws, opts)
		if cfg != nil {
			for _, tool := range cfg.Extra {
				if err := registerPublicTool(registry, tool); err != nil {
					return nil, err
				}
			}
		}
		return registry, nil
	}
	return setup, nil
}

// toolConflictError preserves both the ConfigError shape and errors.Is with
// ErrToolConflict.
func toolConflictError(name, detail string) error {
	return fmt.Errorf("%w: %w", ErrToolConflict, &ConfigError{Field: "Tools.Extra", Value: name, Detail: detail})
}

func registerPublicTool(registry *tools.Registry, tool Tool) error {
	schema := toInternalSchema(ToolSchema{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema})
	run := func(ctx context.Context, call tools.Call, args json.RawMessage) tools.Result {
		publicCall := ToolCall{
			ID:        call.ID,
			Name:      tool.Name,
			Arguments: append(json.RawMessage(nil), args...),
			Intent:    tools.Intent(args),
			Session:   call.Session,
			Agent:     call.Agent,
		}
		result, err := tool.Run(ctx, publicCall)
		if err != nil {
			var deny *Deny
			switch {
			case errors.As(err, &deny):
				return tools.Failure(tools.CodePermissionDenied, capToolText(deny.Error(), 2<<10))
			case isContextError(err):
				return tools.Failure(tools.CodeCanceled, capToolText(err.Error(), 2<<10))
			default:
				return tools.Failure(tools.CodeToolError, capToolText(err.Error(), 2<<10))
			}
		}
		out := fromPublicToolResult(result)
		if len(result.Image) > 0 {
			img, err := registry.NormalizeImage(tool.Name, result.Image)
			if err != nil {
				return rejectedImageResult(result, err)
			}
			out.Images = []tools.Image{img}
		}
		return out
	}
	return registry.Register(schema, run, tool.ConcurrencySafe)
}

// rejectedImageResult maps an image the SDK will not deliver to the failure the
// model sees. The caller's Output survives so a caption or partial observation
// is not lost, and the code matches read_image's for the same cause.
func rejectedImageResult(result ToolResult, err error) tools.Result {
	code := tools.CodeToolError
	switch {
	case errors.Is(err, tools.ErrInvalidImage):
		code = tools.CodeInvalidImage
	case errors.Is(err, tools.ErrTooLarge):
		code = tools.CodeTooLarge
	case errors.Is(err, tools.ErrNoImageSupport):
		code = tools.CodeUnavailable
	}
	out := tools.Failure(code, capToolText(err.Error(), 2<<10))
	out.Output = strings.ToValidUTF8(result.Output, "\uFFFD")
	return out
}

func fromPublicToolResult(result ToolResult) tools.Result {
	code := result.Code
	if code == "" {
		if result.OK {
			code = string(tools.CodeOK)
		} else {
			code = string(tools.CodeToolError)
		}
	}
	out := tools.Result{
		OK:        result.OK,
		Code:      tools.Code(code),
		Output:    strings.ToValidUTF8(result.Output, "\uFFFD"),
		Error:     strings.ToValidUTF8(result.Error, "\uFFFD"),
		ExitCode:  result.ExitCode,
		TimedOut:  result.TimedOut,
		Truncated: result.Truncated,
		Details:   capToolDetails(result.Details),
	}
	return out
}

func capToolDetails(details json.RawMessage) json.RawMessage {
	const limit = 64 << 10
	if len(details) <= limit {
		return details
	}
	message, _ := json.Marshal(fmt.Sprintf("[tool details truncated: %d bytes]", len(details)))
	return message
}

func capToolText(text string, limit int) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func toInternalSchema(schema ToolSchema) model.ToolSchema {
	return model.ToolSchema{Name: schema.Name, Description: schema.Description, Parameters: append(json.RawMessage(nil), schema.Parameters...)}
}
