package model

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	catalogCacheVersion     = 1
	maxCatalogModelIDBytes  = 256
	defaultCatalogUserAgent = "aharness/unknown"
	catalogCacheFileMode    = 0o600
	catalogCacheDirMode     = 0o700
)

// ModelsDevSource is the models.dev catalog source. It fetches the catalog,
// normalizes the selected provider, and persists a copy under the state
// directory. Construct it with NewModelsDevSource.
type ModelsDevSource struct {
	url       string
	stateDir  string
	client    *http.Client
	userAgent string
	now       func() time.Time
	ttl       time.Duration
	timeout   time.Duration
	maxBytes  int64
}

var _ CatalogSource = (*ModelsDevSource)(nil)

// NewModelsDevSource validates opts and returns a catalog source. The state
// directory is required. The catalog URL defaults to DefaultCatalogURL; an
// override must use HTTPS except for loopback HTTP test servers.
func NewModelsDevSource(opts CatalogOptions) (*ModelsDevSource, error) {
	if opts.StateDir == "" {
		return nil, fmt.Errorf("catalog state directory is required")
	}
	rawURL := opts.URL
	if rawURL == "" {
		rawURL = DefaultCatalogURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid catalog URL %q: %w", rawURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid catalog URL %q: an absolute URL with a host is required", rawURL)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("invalid catalog URL %q: user information is not allowed", rawURL)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLoopbackCatalogHost(parsed.Hostname()) {
			return nil, fmt.Errorf("invalid catalog URL %q: http is allowed only for loopback hosts", rawURL)
		}
	default:
		return nil, fmt.Errorf("invalid catalog URL %q: scheme must be https", rawURL)
	}

	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = defaultCatalogUserAgent
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = CatalogTTL
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = CatalogRequestTimeout
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = MaxCatalogBytes
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}
	return &ModelsDevSource{
		url:       parsed.String(),
		stateDir:  opts.StateDir,
		client:    client,
		userAgent: userAgent,
		now:       now,
		ttl:       ttl,
		timeout:   timeout,
		maxBytes:  maxBytes,
	}, nil
}

// Load returns the provider catalog, using a fresh cache without a network
// request and conditionally refreshing a stale one. A failed implicit refresh
// falls back to a valid stale cache with a warning.
func (s *ModelsDevSource) Load(ctx context.Context, provider string) (Catalog, error) {
	if err := s.validateProvider(provider); err != nil {
		return Catalog{}, err
	}
	cached, ok := s.readCache(provider)
	if ok && s.now().Sub(cached.FetchedAt) < s.ttl {
		return cached, nil
	}
	return s.refreshCached(ctx, provider, cached, ok)
}

// Revalidate bypasses the cache TTL while keeping the conditional request and
// the stale-cache fallback.
func (s *ModelsDevSource) Revalidate(ctx context.Context, provider string) (Catalog, error) {
	if err := s.validateProvider(provider); err != nil {
		return Catalog{}, err
	}
	cached, ok := s.readCache(provider)
	return s.refreshCached(ctx, provider, cached, ok)
}

// Refresh performs an unconditional fetch, replaces the cache on success, and
// never falls back to stale data. A cache write failure returns the fetched
// catalog with ErrCatalogCache so an in-memory consumer can still use it.
func (s *ModelsDevSource) Refresh(ctx context.Context, provider string) (Catalog, error) {
	if err := s.validateProvider(provider); err != nil {
		return Catalog{}, err
	}
	fetched, err := s.fetch(ctx, provider, "")
	if err != nil {
		return Catalog{}, err
	}
	if fetched.notModified {
		return Catalog{}, fmt.Errorf("%w: refresh catalog %s (cache %s): unexpected 304 not modified", ErrCatalogInvalid, s.url, s.cachePath(provider))
	}
	catalog, err := s.decodeCatalog(provider, fetched)
	if err != nil {
		return Catalog{}, err
	}
	if err := s.writeCache(catalog); err != nil {
		return catalog, fmt.Errorf("%w: refresh catalog %s: %w", ErrCatalogCache, s.url, err)
	}
	return catalog, nil
}

func (s *ModelsDevSource) refreshCached(ctx context.Context, provider string, cached Catalog, haveCache bool) (Catalog, error) {
	ifNoneMatch := ""
	if haveCache {
		ifNoneMatch = cached.ETag
	}
	fetched, err := s.fetch(ctx, provider, ifNoneMatch)
	if err != nil {
		if haveCache {
			return withStaleWarning(cached, err), nil
		}
		return Catalog{}, err
	}
	if fetched.notModified {
		unexpected := fmt.Errorf("%w: fetch catalog %s (cache %s): unexpected 304 not modified", ErrCatalogInvalid, s.url, s.cachePath(provider))
		if ifNoneMatch == "" {
			if haveCache {
				return withStaleWarning(cached, unexpected), nil
			}
			return Catalog{}, unexpected
		}
		updated := cached
		updated.FetchedAt = s.now().UTC()
		if fetched.etag != "" {
			updated.ETag = fetched.etag
		}
		updated.Warnings = nil
		if err := s.writeCache(updated); err != nil {
			updated.Warnings = append(updated.Warnings, fmt.Sprintf("catalog cache write failed: %v", err))
		}
		return updated, nil
	}
	catalog, err := s.decodeCatalog(provider, fetched)
	if err != nil {
		if haveCache {
			return withStaleWarning(cached, err), nil
		}
		return Catalog{}, err
	}
	if err := s.writeCache(catalog); err != nil {
		catalog.Warnings = append(catalog.Warnings, fmt.Sprintf("catalog cache write failed: %v", err))
	}
	return catalog, nil
}

func withStaleWarning(catalog Catalog, cause error) Catalog {
	catalog.Warnings = append(catalog.Warnings, fmt.Sprintf(
		"catalog refresh failed: %v; using cached catalog from %s",
		cause, catalog.FetchedAt.UTC().Format(time.RFC3339),
	))
	return catalog
}

type catalogFetch struct {
	body        []byte
	etag        string
	notModified bool
}

func (s *ModelsDevSource) fetch(ctx context.Context, provider, ifNoneMatch string) (catalogFetch, error) {
	requestCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, s.url, nil)
	if err != nil {
		return catalogFetch{}, fmt.Errorf("build catalog request %s (cache %s): %w", s.url, s.cachePath(provider), err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.userAgent)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return catalogFetch{}, fmt.Errorf("%w: fetch catalog %s (cache %s): %w", ErrCatalogUnavailable, s.url, s.cachePath(provider), err)
	}
	// The body is either fully read below or intentionally abandoned; the
	// close error cannot change the result.
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return catalogFetch{notModified: true, etag: resp.Header.Get("ETag")}, nil
	case http.StatusOK:
	default:
		return catalogFetch{}, fmt.Errorf("%w: fetch catalog %s (cache %s): server returned status %d", ErrCatalogUnavailable, s.url, s.cachePath(provider), resp.StatusCode)
	}

	bodyLimit := s.maxBytes
	if bodyLimit < math.MaxInt64 {
		bodyLimit++
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return catalogFetch{}, fmt.Errorf("%w: read catalog %s (cache %s): %w", ErrCatalogUnavailable, s.url, s.cachePath(provider), err)
	}
	if int64(len(body)) > s.maxBytes {
		return catalogFetch{}, fmt.Errorf("%w: read catalog %s (cache %s): body exceeds %d bytes", ErrCatalogInvalid, s.url, s.cachePath(provider), s.maxBytes)
	}
	return catalogFetch{body: body, etag: resp.Header.Get("ETag")}, nil
}

func (s *ModelsDevSource) decodeCatalog(provider string, fetched catalogFetch) (Catalog, error) {
	catalog, err := parseCatalog(fetched.body, provider, s.url)
	if err != nil {
		return Catalog{}, fmt.Errorf("%w: parse catalog %s (cache %s): %w", ErrCatalogInvalid, s.url, s.cachePath(provider), err)
	}
	catalog.ETag = fetched.etag
	catalog.FetchedAt = s.now().UTC()
	return catalog, nil
}

func (s *ModelsDevSource) validateProvider(provider string) error {
	if provider == "" {
		return fmt.Errorf("invalid catalog provider %q: must match [a-z0-9-]+", provider)
	}
	for _, r := range provider {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return fmt.Errorf("invalid catalog provider %q: must match [a-z0-9-]+", provider)
	}
	return nil
}

func (s *ModelsDevSource) cachePath(provider string) string {
	return CatalogCachePath(s.stateDir, provider)
}

type catalogCacheFile struct {
	CacheVersion int            `json:"cache_version"`
	Source       string         `json:"source"`
	Provider     string         `json:"provider"`
	FetchedAt    string         `json:"fetched_at"`
	ETag         string         `json:"etag"`
	Models       []CatalogModel `json:"models"`
}

func (s *ModelsDevSource) readCache(provider string) (Catalog, bool) {
	path := s.cachePath(provider)
	file, err := os.Open(path)
	if err != nil {
		return Catalog{}, false
	}
	// The cache file is read-only; a close error cannot change the result.
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, MaxCatalogBytes+1))
	if err != nil || len(data) > MaxCatalogBytes {
		return Catalog{}, false
	}
	var cached catalogCacheFile
	if err := jsonv2.Unmarshal(data, &cached); err != nil {
		return Catalog{}, false
	}
	if cached.CacheVersion != catalogCacheVersion || cached.Source != s.url || cached.Provider != provider {
		return Catalog{}, false
	}
	if len(cached.Models) == 0 {
		return Catalog{}, false
	}
	fetchedAt, err := time.Parse(time.RFC3339, cached.FetchedAt)
	if err != nil {
		return Catalog{}, false
	}
	models := make(map[string]CatalogModel, len(cached.Models))
	for _, m := range cached.Models {
		if !validCatalogModelID(m.ID) || m.Provider != provider {
			return Catalog{}, false
		}
		if _, exists := models[m.ID]; exists {
			return Catalog{}, false
		}
		// A usable cached entry must still satisfy the network validation;
		// otherwise a tampered cache could redirect the provider token.
		if m.Unavailable == "" && !validUsableCachedModel(m) {
			return Catalog{}, false
		}
		models[m.ID] = m
	}
	return Catalog{
		Provider:  provider,
		Source:    cached.Source,
		FetchedAt: fetchedAt.UTC(),
		ETag:      cached.ETag,
		Models:    models,
	}, true
}

func validUsableCachedModel(m CatalogModel) bool {
	if m.Dialect != DialectChat && m.Dialect != DialectResponses {
		return false
	}
	if !m.ToolCall || m.ContextWindow <= 0 {
		return false
	}
	if m.InputLimit < 0 || (m.InputLimit > 0 && m.InputLimit > m.ContextWindow) {
		return false
	}
	if m.OutputLimit < 0 {
		return false
	}
	if _, reason := discoveredBaseURL(m.BaseURL); reason != "" {
		return false
	}
	return true
}

func (s *ModelsDevSource) writeCache(catalog Catalog) error {
	path := s.cachePath(catalog.Provider)
	models := catalog.SortedModels()
	data, err := jsonv2.Marshal(catalogCacheFile{
		CacheVersion: catalogCacheVersion,
		Source:       s.url,
		Provider:     catalog.Provider,
		FetchedAt:    catalog.FetchedAt.UTC().Format(time.RFC3339),
		ETag:         catalog.ETag,
		Models:       models,
	})
	if err != nil {
		return fmt.Errorf("write catalog cache %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, catalogCacheDirMode); err != nil {
		return fmt.Errorf("write catalog cache %s: %w", path, err)
	}
	if err := os.Chmod(dir, catalogCacheDirMode); err != nil {
		return fmt.Errorf("write catalog cache %s: %w", path, err)
	}

	temp, err := os.CreateTemp(dir, ".catalog-*.tmp")
	if err != nil {
		return fmt.Errorf("write catalog cache %s: %w", path, err)
	}
	tempName := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
		}
	}()

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write catalog cache %s: %w", path, err)
	}
	if err := temp.Chmod(catalogCacheFileMode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write catalog cache %s: %w", path, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write catalog cache %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write catalog cache %s: %w", path, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("write catalog cache %s: %w", path, err)
	}
	removeTemp = false
	return nil
}

type modelsDevCatalog map[string]modelsDevProvider

type modelsDevProvider struct {
	NPM    string                    `json:"npm"`
	API    string                    `json:"api"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID         *string                `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	ToolCall   bool                   `json:"tool_call"`
	Status     string                 `json:"status"`
	Provider   modelsDevModelProvider `json:"provider"`
	Limit      modelsDevLimit         `json:"limit"`
	Modalities modelsDevModalities    `json:"modalities"`
}

type modelsDevModelProvider struct {
	NPM string `json:"npm"`
	API string `json:"api"`
}

type modelsDevLimit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

type modelsDevModalities struct {
	Output []string `json:"output"`
}

// parseCatalog decodes a models.dev api.json payload and normalizes the
// selected provider. Malformed or schema-invalid data returns an error; a
// successfully parsed catalog always has at least one model.
func parseCatalog(data []byte, provider, source string) (Catalog, error) {
	var document modelsDevCatalog
	if err := jsonv2.Unmarshal(data, &document); err != nil {
		return Catalog{}, fmt.Errorf("decode models.dev catalog: %w", err)
	}
	selected, ok := document[provider]
	if !ok {
		return Catalog{}, fmt.Errorf("provider %q is not present in models.dev catalog", provider)
	}
	if len(selected.Models) == 0 {
		return Catalog{}, fmt.Errorf("provider %q has no models", provider)
	}

	models := make(map[string]CatalogModel, len(selected.Models))
	for id, raw := range selected.Models {
		if reason := catalogModelIDReason(id); reason != "" {
			return Catalog{}, errors.New(reason)
		}
		if raw.ID != nil && *raw.ID != id {
			return Catalog{}, fmt.Errorf("model %q declares a mismatched id", id)
		}
		models[id] = normalizeCatalogModel(provider, id, raw, selected.NPM, selected.API)
	}
	return Catalog{
		Provider: provider,
		Source:   source,
		Models:   models,
	}, nil
}

func validCatalogModelID(id string) bool {
	return catalogModelIDReason(id) == ""
}

func catalogModelIDReason(id string) string {
	switch {
	case id == "":
		return "model id is empty"
	case len(id) > maxCatalogModelIDBytes:
		return fmt.Sprintf("model id exceeds %d bytes", maxCatalogModelIDBytes)
	case !utf8.ValidString(id):
		return "model id is not valid UTF-8"
	case strings.IndexFunc(id, unicode.IsControl) >= 0:
		return fmt.Sprintf("model id %q contains a control character", id)
	default:
		return ""
	}
}

// diagnosticValue bounds untrusted catalog strings included in reasons so a
// malformed catalog cannot inflate an error or warning.
func diagnosticValue(value string) string {
	const maxBytes = 64
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes] + "..."
}

func normalizeCatalogModel(provider, id string, raw modelsDevModel, providerNPM, providerAPI string) CatalogModel {
	model := CatalogModel{
		ID:         id,
		Name:       raw.Name,
		Provider:   provider,
		Status:     raw.Status,
		Deprecated: raw.Status == "deprecated",
		ToolCall:   raw.ToolCall,
	}
	if model.Name == "" {
		model.Name = id
	}

	sdk := raw.Provider.NPM
	if sdk == "" {
		sdk = providerNPM
	}
	model.NativeSDK = sdk
	dialect, nativeDialect, translated, supportedSDK := catalogSDK(sdk)
	if supportedSDK {
		model.Dialect = dialect
		model.NativeDialect = nativeDialect
		model.Translated = translated
	}

	model.ContextWindow = raw.Limit.Context
	if raw.Limit.Input > 0 {
		model.InputLimit = raw.Limit.Input
	}
	if raw.Limit.Output > 0 {
		model.OutputLimit = raw.Limit.Output
	}

	baseURLSource := raw.Provider.API
	if baseURLSource == "" {
		baseURLSource = providerAPI
	}
	if nativeDialect == "messages" || nativeDialect == "generate-content" {
		baseURLSource = providerAPI
	}
	baseURL, baseURLReason := discoveredBaseURL(baseURLSource)
	if baseURLReason == "" {
		model.BaseURL = baseURL
	}

	var inputReason string
	switch {
	case raw.Limit.Input < 0:
		inputReason = fmt.Sprintf("invalid input limit %d", raw.Limit.Input)
	case raw.Limit.Input > model.ContextWindow:
		inputReason = fmt.Sprintf("input limit %d exceeds context limit %d", raw.Limit.Input, model.ContextWindow)
	}

	switch {
	case raw.Type != "" && raw.Type != "chat":
		model.Unavailable = fmt.Sprintf("unsupported model type %q", diagnosticValue(raw.Type))
	case !raw.ToolCall:
		model.Unavailable = "tool calls are not supported"
	case !slices.Contains(raw.Modalities.Output, "text"):
		model.Unavailable = "output modalities do not include text"
	case !supportedSDK:
		model.Unavailable = fmt.Sprintf("unsupported AI SDK package %q", diagnosticValue(sdk))
	case model.ContextWindow <= 0:
		model.Unavailable = "missing context limit"
	case inputReason != "":
		model.Unavailable = inputReason
	case baseURLReason != "":
		model.Unavailable = baseURLReason
	}
	return model
}

func catalogSDK(sdk string) (dialect Dialect, nativeDialect string, translated, supported bool) {
	switch sdk {
	case "@ai-sdk/openai-compatible":
		return DialectChat, "chat-completions", false, true
	case "@ai-sdk/openai":
		return DialectResponses, "responses", false, true
	case "@ai-sdk/anthropic":
		return DialectChat, "messages", true, true
	case "@ai-sdk/google":
		return DialectChat, "generate-content", true, true
	default:
		return "", "", false, false
	}
}

func discoveredBaseURL(raw string) (string, string) {
	if raw == "" {
		return "", "missing base URL"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" {
		return "", "invalid base URL"
	}
	if parsed.Scheme != "https" {
		return "", "base URL must be https"
	}
	if parsed.Hostname() == "" {
		return "", "base URL must include a host"
	}
	if parsed.User != nil {
		return "", "base URL must not include user information"
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", "base URL must not include a query or fragment"
	}
	if parsed.Hostname() != "opencode.ai" || parsed.Port() != "" {
		return "", "base URL host must be opencode.ai"
	}
	return raw, ""
}

func isLoopbackCatalogHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}
