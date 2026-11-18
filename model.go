package aharness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/tools"
)

// Role is a canonical message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// FinishReason is the normalized reason a model response ended.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
	FinishOther         FinishReason = "other"
)

// ToolCall is a canonical tool invocation.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
	// Intent is display metadata parsed from the arguments' "intent" member on
	// events. It is empty on model requests and is never acted on by the loop.
	Intent string
	// Session is the session that invoked the tool. It is set on the call
	// passed to Tool.Run and Config.Authorize: a delegated child sees its own
	// session ID, and the root session sees the root ID. It is empty on model
	// request, model response, and event DTOs.
	Session string
	// Agent is the delegated agent definition name for a child tool call and
	// empty for the root session. Like Session, it is set only on the call
	// passed to Tool.Run and Config.Authorize, and is empty on model request,
	// model response, and event DTOs.
	Agent string
}

// Message is one canonical conversation message.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	Name       string
}

// ToolSchema is a tool declaration sent to the provider.
type ToolSchema struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Request is one canonical model call. The system prompt, when present, is the
// first message.
type Request struct {
	Model           string
	Messages        []Message
	Tools           []ToolSchema
	MaxOutputTokens int
	ThinkingEffort  string
}

// Usage is normalized token usage for one response.
type Usage struct {
	InputTokens           int
	OutputTokens          int
	CachedInputTokens     int
	CacheWriteInputTokens int
	ReasoningOutputTokens int
	Reported              bool
}

// Total returns input plus output tokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// UncachedInputTokens returns the non-cached, non-cache-write input.
func (u Usage) UncachedInputTokens() int {
	uncached := u.InputTokens - u.CachedInputTokens - u.CacheWriteInputTokens
	if uncached < 0 {
		return 0
	}
	return uncached
}

// Response is one canonical model response. The returned response is
// authoritative; streamed deltas are display-only.
type Response struct {
	Text         string
	ToolCalls    []ToolCall
	FinishReason FinishReason
	Usage        Usage
	Reasoning    string
	Streamed     bool
}

// StreamHandler receives display-only deltas. Either callback may be nil.
type StreamHandler struct {
	Text      func(string) error
	Reasoning func(string) error
}

// Model is the provider-facing contract. Implementations are called on the run
// goroutine and must be safe for concurrent use when delegation shares them.
type Model interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// StreamingModel is the optional streaming contract. The returned Response is
// authoritative.
type StreamingModel interface {
	Model
	CompleteStream(ctx context.Context, req Request, stream StreamHandler) (Response, error)
}

// Pricing is an observational price snapshot in micro-USD per one million
// tokens. It never bills the caller.
type Pricing struct {
	Available          bool
	Currency           string
	Unit               string
	InputMicroUSD      int64
	OutputMicroUSD     int64
	CacheReadMicroUSD  int64
	CacheWriteMicroUSD int64
	Source             string
	FetchedAt          time.Time
}

// Dialect is the recorded wire dialect. It is metadata for the loop; only the
// caller's Model implementation acts on it.
type Dialect string

const (
	DialectChat      Dialect = "openai-chat"
	DialectResponses Dialect = "openai-responses"
	DialectCustom    Dialect = "custom"
)

// ModelSpec is the model identity and capabilities recorded in a session.
type ModelSpec struct {
	ID              string
	Provider        string
	Dialect         Dialect
	BaseURL         string
	ContextWindow   int
	MaxOutputTokens int
	InputLimit      int
	ThinkingEfforts []string
	Pricing         Pricing
}

// ModelConfig pairs a model implementation with its recorded identity.
type ModelConfig struct {
	// Spec is the model identity and capabilities. On Resume/Fork, an empty ID
	// inherits the recorded spec.
	Spec ModelSpec
	// Completer is required.
	Completer Model
	// NewCompleter, when non-nil, returns an independent implementation for a
	// delegated child. When nil, children share Completer.
	NewCompleter func(sessionID string) Model
}

// CustomModel pairs a caller implementation with a spec.
func CustomModel(spec ModelSpec, m Model) *ModelConfig {
	return &ModelConfig{Spec: spec, Completer: m}
}

func normalizeSpec(spec ModelSpec) (ModelSpec, error) {
	spec.Provider = strings.TrimSpace(spec.Provider)
	if spec.Provider == "" {
		spec.Provider = "custom"
	}
	if spec.Dialect == "" {
		spec.Dialect = DialectCustom
	}
	switch spec.Dialect {
	case DialectChat, DialectResponses, DialectCustom:
	default:
		return ModelSpec{}, &ConfigError{Field: "Model.Spec.Dialect", Value: string(spec.Dialect), Detail: "must be openai-chat, openai-responses, or custom"}
	}
	if spec.ContextWindow <= 0 {
		return ModelSpec{}, &ConfigError{Field: "Model.Spec.ContextWindow", Detail: "must be positive"}
	}
	if spec.MaxOutputTokens < 0 {
		return ModelSpec{}, &ConfigError{Field: "Model.Spec.MaxOutputTokens", Detail: "must not be negative"}
	}
	if spec.InputLimit < 0 || (spec.InputLimit > 0 && spec.InputLimit > spec.ContextWindow) {
		return ModelSpec{}, &ConfigError{Field: "Model.Spec.InputLimit", Detail: "must be non-negative and no larger than ContextWindow"}
	}
	if spec.BaseURL != "" {
		if err := validateEndpoint(spec.BaseURL); err != nil {
			return ModelSpec{}, &ConfigError{Field: "Model.Spec.BaseURL", Value: spec.BaseURL, Detail: err.Error()}
		}
	}
	seen := make(map[string]bool, len(spec.ThinkingEfforts))
	for _, effort := range spec.ThinkingEfforts {
		if !model.ValidReasoningEffort(effort) {
			return ModelSpec{}, &ConfigError{Field: "Model.Spec.ThinkingEfforts", Value: effort, Detail: "is not a valid effort value"}
		}
		if seen[effort] {
			return ModelSpec{}, &ConfigError{Field: "Model.Spec.ThinkingEfforts", Value: effort, Detail: "is duplicated"}
		}
		seen[effort] = true
	}
	return spec, nil
}

func validateEndpoint(raw string) error {
	return model.ValidateBaseURL(raw)
}

// OpenAIModelOptions configures a built-in Chat Completions or Responses
// adapter for an explicit OpenAI-compatible endpoint.
type OpenAIModelOptions struct {
	Spec       ModelSpec
	APIKey     string
	UserAgent  string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// OpenAIModel builds a Chat Completions or Responses adapter. Spec.BaseURL is
// the endpoint. DialectCustom is for caller-supplied models.
func OpenAIModel(opts OpenAIModelOptions) (*ModelConfig, error) {
	spec, err := normalizeSpec(opts.Spec)
	if err != nil {
		return nil, err
	}
	if spec.ID == "" {
		return nil, &ConfigError{Field: "Model.Spec.ID", Detail: "is required"}
	}
	if spec.BaseURL == "" {
		return nil, &ConfigError{Field: "Model.Spec.BaseURL", Detail: "is required for the OpenAI adapter"}
	}
	if strings.TrimSpace(opts.UserAgent) == "" {
		return nil, &ConfigError{Field: "Model.UserAgent", Detail: "is required"}
	}
	var completer Model
	switch spec.Dialect {
	case DialectChat:
		completer = wrapInternalCompleter(model.NewOpenAIChatWithClient(spec.BaseURL, opts.APIKey, "", opts.UserAgent, opts.Timeout, opts.HTTPClient))
	case DialectResponses:
		completer = wrapInternalCompleter(model.NewOpenAIResponsesWithClient(spec.BaseURL, opts.APIKey, "", opts.UserAgent, opts.Timeout, opts.HTTPClient))
	default:
		return nil, &ConfigError{Field: "Model.Spec.Dialect", Value: string(spec.Dialect), Detail: "OpenAIModel supports openai-chat or openai-responses"}
	}
	return &ModelConfig{Spec: spec, Completer: completer}, nil
}

// CatalogConfig configures models.dev discovery.
type CatalogConfig struct {
	// URL is the catalog endpoint. Empty selects model.DefaultCatalogURL.
	URL string
	// StateDir is the persistent cache root. Required when discovery runs.
	StateDir string
	// HTTPClient overrides the bounded default client.
	HTTPClient *http.Client
	// TTL is the cache freshness window. Zero selects model.CatalogTTL.
	TTL time.Duration
	// Timeout bounds one catalog request. Zero selects the model default.
	Timeout time.Duration
	// UserAgent identifies the host on catalog requests. It is required only
	// when DiscoverModel or ListModels is called; constructing a Config with a
	// zero CatalogConfig never validates it.
	UserAgent string
}

func (c CatalogConfig) options() (model.CatalogOptions, error) {
	if c.StateDir == "" {
		return model.CatalogOptions{}, &ConfigError{Field: "Catalog.StateDir", Detail: "is required"}
	}
	if strings.TrimSpace(c.UserAgent) == "" {
		return model.CatalogOptions{}, &ConfigError{Field: "Catalog.UserAgent", Detail: "is required"}
	}
	if err := model.ValidateCatalogURL(c.URL); err != nil {
		return model.CatalogOptions{}, &ConfigError{Field: "Catalog.URL", Detail: err.Error()}
	}
	return model.CatalogOptions{
		URL:       c.URL,
		StateDir:  c.StateDir,
		Client:    c.HTTPClient,
		UserAgent: c.UserAgent,
		TTL:       c.TTL,
		Timeout:   c.Timeout,
	}, nil
}

// Validate checks a catalog configuration without allocating a source or
// touching the network.
func (c CatalogConfig) Validate() error {
	_, err := c.options()
	return err
}

func newCatalog(cfg CatalogConfig) (*model.ModelsDevSource, error) {
	opts, err := cfg.options()
	if err != nil {
		return nil, err
	}
	source, err := model.NewModelsDevSource(opts)
	if err != nil {
		return nil, publicError(err)
	}
	return source, nil
}

// DiscoverModelOptions resolves one model through models.dev and returns a
// ready ModelConfig. The API key goes only to the adapter, never to the
// catalog.
type DiscoverModelOptions struct {
	Catalog    CatalogConfig
	Provider   string
	Model      string
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// DiscoverModel resolves a model through models.dev and returns a ready
// ModelConfig.
func DiscoverModel(ctx context.Context, opts DiscoverModelOptions) (*ModelConfig, error) {
	if strings.TrimSpace(opts.Provider) == "" {
		return nil, &ConfigError{Field: "Provider", Detail: "is required"}
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, &ConfigError{Field: "Model", Detail: "is required"}
	}
	source, err := newCatalog(opts.Catalog)
	if err != nil {
		return nil, err
	}
	catalog, err := source.Load(ctx, opts.Provider)
	if err != nil {
		return nil, publicError(err)
	}
	entry, err := catalog.Lookup(opts.Model)
	if err != nil {
		return nil, publicError(err)
	}
	if !entry.Usable() {
		return nil, fmt.Errorf("model %q is not usable: %s", opts.Model, entry.Unavailable)
	}
	baseURL := entry.BaseURL
	if opts.BaseURL != "" {
		baseURL = opts.BaseURL
	}
	spec := ModelSpec{
		ID:              entry.ID,
		Provider:        entry.Provider,
		Dialect:         Dialect(entry.Dialect),
		BaseURL:         baseURL,
		ContextWindow:   entry.ContextWindow,
		MaxOutputTokens: entry.OutputLimit,
		InputLimit:      entry.InputLimit,
		ThinkingEfforts: append([]string(nil), entry.ReasoningEfforts...),
		Pricing:         fromInternalPricing(entry.Pricing(catalog.Source, catalog.FetchedAt)),
	}
	return OpenAIModel(OpenAIModelOptions{
		Spec:       spec,
		APIKey:     opts.APIKey,
		UserAgent:  opts.Catalog.UserAgent,
		HTTPClient: opts.HTTPClient,
		Timeout:    opts.Timeout,
	})
}

// CatalogModel is one normalized catalog entry for a model picker.
type CatalogModel struct {
	ID              string
	Name            string
	Provider        string
	Dialect         Dialect
	BaseURL         string
	ContextWindow   int
	InputLimit      int
	MaxOutputTokens int
	ToolCall        bool
	Reasoning       bool
	ThinkingEfforts []string
	Deprecated      bool
	Status          string
	Unavailable     string
	Pricing         Pricing
}

// CatalogListing is one provider's catalog snapshot.
type CatalogListing struct {
	Provider  string
	FetchedAt time.Time
	Models    []CatalogModel
	Warnings  []string
}

// ListModels returns the catalog for a provider using the same cache and
// stale-fallback policy as DiscoverModel. It never creates a session.
func ListModels(ctx context.Context, cfg CatalogConfig, provider string) (CatalogListing, error) {
	if strings.TrimSpace(provider) == "" {
		return CatalogListing{}, &ConfigError{Field: "Provider", Detail: "is required"}
	}
	source, err := newCatalog(cfg)
	if err != nil {
		return CatalogListing{}, err
	}
	catalog, err := source.Load(ctx, provider)
	if err != nil {
		return CatalogListing{}, publicError(err)
	}
	listing := CatalogListing{
		Provider:  catalog.Provider,
		FetchedAt: catalog.FetchedAt,
		Warnings:  append([]string(nil), catalog.Warnings...),
		Models:    make([]CatalogModel, 0, len(catalog.Models)),
	}
	for _, entry := range catalog.SortedModels() {
		listing.Models = append(listing.Models, CatalogModel{
			ID:              entry.ID,
			Name:            entry.Name,
			Provider:        entry.Provider,
			Dialect:         Dialect(entry.Dialect),
			BaseURL:         entry.BaseURL,
			ContextWindow:   entry.ContextWindow,
			InputLimit:      entry.InputLimit,
			MaxOutputTokens: entry.OutputLimit,
			ToolCall:        entry.ToolCall,
			Reasoning:       entry.Reasoning,
			ThinkingEfforts: append([]string(nil), entry.ReasoningEfforts...),
			Deprecated:      entry.Deprecated,
			Status:          entry.Status,
			Unavailable:     entry.Unavailable,
			Pricing:         fromInternalPricing(entry.Pricing(catalog.Source, catalog.FetchedAt)),
		})
	}
	return listing, nil
}

// builtinModelAdapter exposes an internal dialect adapter through the public
// Model contract.
type builtinModelAdapter struct {
	completer model.Completer
}

func wrapInternalCompleter(completer model.Completer) Model {
	return &builtinModelAdapter{completer: completer}
}

func (a *builtinModelAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	resp, err := a.completer.Complete(ctx, toInternalRequest(req))
	if err != nil {
		return Response{}, err
	}
	return fromInternalResponse(resp), nil
}

func (a *builtinModelAdapter) CompleteStream(ctx context.Context, req Request, stream StreamHandler) (Response, error) {
	if streaming, ok := a.completer.(model.StreamCompleter); ok {
		resp, err := streaming.CompleteStream(ctx, toInternalRequest(req), model.StreamHandler{Text: stream.Text, Reasoning: stream.Reasoning})
		if err != nil {
			return Response{}, err
		}
		return fromInternalResponse(resp), nil
	}
	return a.Complete(ctx, req)
}

func fromInternalResponse(resp model.Response) Response {
	return Response{
		Text:         resp.Text,
		ToolCalls:    fromInternalToolCalls(resp.ToolCalls),
		FinishReason: FinishReason(resp.FinishReason),
		Usage:        fromInternalUsage(resp.Usage),
		Reasoning:    resp.Reasoning,
		Streamed:     resp.Streamed,
	}
}

// modelAdapter converts the public model contract to the internal one.
type modelAdapter struct {
	model Model
}

func newModelAdapter(m Model) *modelAdapter { return &modelAdapter{model: m} }

func (a *modelAdapter) Complete(ctx context.Context, req model.Request) (resp model.Response, err error) {
	defer func() {
		if v := recover(); v != nil {
			resp = model.Response{}
			err = fmt.Errorf("%w: %v", agent.ErrModelPanic, v)
		}
	}()
	public, err := toPublicRequest(req)
	if err != nil {
		return model.Response{}, err
	}
	got, err := a.model.Complete(ctx, public)
	if err != nil {
		return model.Response{}, err
	}
	return fromPublicResponse(got), nil
}

func (a *modelAdapter) CompleteStream(ctx context.Context, req model.Request, stream model.StreamHandler) (resp model.Response, err error) {
	streamer, ok := a.model.(StreamingModel)
	if !ok {
		return a.Complete(ctx, req)
	}
	defer func() {
		if v := recover(); v != nil {
			resp = model.Response{}
			err = fmt.Errorf("%w: %v", agent.ErrModelPanic, v)
		}
	}()
	public, err := toPublicRequest(req)
	if err != nil {
		return model.Response{}, err
	}
	got, err := streamer.CompleteStream(ctx, public, StreamHandler{
		Text:      stream.Text,
		Reasoning: stream.Reasoning,
	})
	if err != nil {
		return model.Response{}, err
	}
	resp = fromPublicResponse(got)
	resp.Streamed = true
	return resp, nil
}

func (a *builtinModelAdapter) newCompleter(sessionID string) Model {
	switch completer := a.completer.(type) {
	case *model.OpenAIChat:
		return wrapInternalCompleter(completer.WithSessionID(sessionID))
	case *model.OpenAIResponses:
		return wrapInternalCompleter(completer.WithSessionID(sessionID))
	default:
		return a
	}
}

// modelForSession returns the parent model implementation with its provider
// session ID set when the built-in adapter supports it.
func modelForSession(config *ModelConfig, sessionID string) Model {
	if config == nil || config.Completer == nil {
		return nil
	}
	if clone, ok := config.Completer.(interface{ newCompleter(string) Model }); ok {
		return clone.newCompleter(sessionID)
	}
	return config.Completer
}

func toPublicRequest(req model.Request) (Request, error) {
	out := Request{
		Model:           req.Model,
		MaxOutputTokens: req.MaxOutputTokens,
		ThinkingEffort:  req.ThinkingEffort,
		Messages:        make([]Message, 0, len(req.Messages)),
		Tools:           make([]ToolSchema, 0, len(req.Tools)),
	}
	for _, message := range req.Messages {
		converted := Message{
			Role:       Role(message.Role),
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
			ToolCalls:  make([]ToolCall, 0, len(message.ToolCalls)),
		}
		for _, call := range message.ToolCalls {
			converted.ToolCalls = append(converted.ToolCalls, ToolCall{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: append(json.RawMessage(nil), call.Arguments...),
			})
		}
		out.Messages = append(out.Messages, converted)
	}
	for _, schema := range req.Tools {
		out.Tools = append(out.Tools, ToolSchema{
			Name:        schema.Name,
			Description: schema.Description,
			Parameters:  append(json.RawMessage(nil), schema.Parameters...),
		})
	}
	return out, nil
}

func fromPublicResponse(resp Response) model.Response {
	out := model.Response{
		Text:         resp.Text,
		FinishReason: model.FinishReason(resp.FinishReason),
		Usage:        toInternalUsage(resp.Usage),
		Reasoning:    resp.Reasoning,
		Streamed:     resp.Streamed,
		ToolCalls:    make([]model.ToolCall, 0, len(resp.ToolCalls)),
	}
	for _, call := range resp.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, model.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: append(json.RawMessage(nil), call.Arguments...),
		})
	}
	return out
}

func toInternalRequest(req Request) model.Request {
	out := model.Request{
		Model:           req.Model,
		MaxOutputTokens: req.MaxOutputTokens,
		ThinkingEffort:  req.ThinkingEffort,
		Messages:        make([]model.Message, 0, len(req.Messages)),
		Tools:           make([]model.ToolSchema, 0, len(req.Tools)),
	}
	for _, message := range req.Messages {
		converted := model.Message{
			Role:       model.Role(message.Role),
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
			ToolCalls:  make([]model.ToolCall, 0, len(message.ToolCalls)),
		}
		for _, call := range message.ToolCalls {
			converted.ToolCalls = append(converted.ToolCalls, model.ToolCall{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: append(json.RawMessage(nil), call.Arguments...),
			})
		}
		out.Messages = append(out.Messages, converted)
	}
	for _, schema := range req.Tools {
		out.Tools = append(out.Tools, model.ToolSchema{
			Name:        schema.Name,
			Description: schema.Description,
			Parameters:  append(json.RawMessage(nil), schema.Parameters...),
		})
	}
	return out
}

func fromInternalToolCalls(calls []model.ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, fromInternalToolCall(call))
	}
	return out
}

func fromInternalToolCall(call model.ToolCall) ToolCall {
	return ToolCall{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: append(json.RawMessage(nil), call.Arguments...),
		Intent:    tools.Intent(call.Arguments),
	}
}

func fromInternalUsage(usage model.Usage) Usage {
	return Usage{
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		CacheWriteInputTokens: usage.CacheWriteInputTokens,
		ReasoningOutputTokens: usage.ReasoningOutputTokens,
		Reported:              usage.Reported,
	}
}

func toInternalUsage(usage Usage) model.Usage {
	return model.Usage{
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		CacheWriteInputTokens: usage.CacheWriteInputTokens,
		ReasoningOutputTokens: usage.ReasoningOutputTokens,
		Reported:              usage.Reported,
	}
}

func fromInternalPricing(pricing model.Pricing) Pricing {
	return Pricing{
		Available:          pricing.Available,
		Currency:           pricing.Currency,
		Unit:               pricing.Unit,
		InputMicroUSD:      pricing.InputMicroUSD,
		OutputMicroUSD:     pricing.OutputMicroUSD,
		CacheReadMicroUSD:  pricing.CacheReadMicroUSD,
		CacheWriteMicroUSD: pricing.CacheWriteMicroUSD,
		Source:             pricing.Source,
		FetchedAt:          pricing.FetchedAt,
	}
}

func toInternalPricing(pricing Pricing) model.Pricing {
	return model.Pricing{
		Available:          pricing.Available,
		Currency:           pricing.Currency,
		Unit:               pricing.Unit,
		InputMicroUSD:      pricing.InputMicroUSD,
		OutputMicroUSD:     pricing.OutputMicroUSD,
		CacheReadMicroUSD:  pricing.CacheReadMicroUSD,
		CacheWriteMicroUSD: pricing.CacheWriteMicroUSD,
		Source:             pricing.Source,
		FetchedAt:          pricing.FetchedAt,
	}
}
