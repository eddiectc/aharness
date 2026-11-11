package model

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"time"
	"unicode/utf8"
)

// DefaultCatalogURL is the public models.dev catalog used by default.
const DefaultCatalogURL = "https://models.dev/api.json"

// CatalogTTL is how long a cached catalog is considered fresh.
const CatalogTTL = 24 * time.Hour

// CatalogRequestTimeout bounds one catalog HTTP request, including body reads.
const CatalogRequestTimeout = 15 * time.Second

// MaxCatalogBytes caps the catalog response body. A larger body is a protocol
// error rather than a partial parse.
const MaxCatalogBytes = 16 << 20

// MaxThinkingEffortBytes bounds one effort value. The same limit applies to
// catalog values, cache entries, the CLI, and the environment.
const MaxThinkingEffortBytes = 32

// MaxReasoningEfforts bounds the normalized effort values retained per model.
const MaxReasoningEfforts = 16

// Catalog errors are stable categories used by the CLI to select exit codes.
var (
	// ErrCatalogUnavailable marks a transport, timeout, or provider failure.
	ErrCatalogUnavailable = errors.New("model catalog unavailable")
	// ErrCatalogInvalid marks malformed or schema-invalid catalog data.
	ErrCatalogInvalid = errors.New("model catalog invalid")
	// ErrCatalogCache marks a cache persistence failure.
	ErrCatalogCache = errors.New("model catalog cache")
	// ErrModelNotFound marks a model ID that is absent from the catalog.
	ErrModelNotFound = errors.New("model not found in catalog")
)

// CatalogModel is one normalized models.dev entry. Unavailable is non-empty
// when the model exists in the catalog but cannot be used by this harness;
// the reason is retained so an explicit selection can explain the failure.
type CatalogModel struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Provider      string  `json:"provider"`
	NativeSDK     string  `json:"native_sdk,omitempty"`
	NativeDialect string  `json:"native_dialect,omitempty"`
	Dialect       Dialect `json:"dialect"`
	BaseURL       string  `json:"base_url"`
	ContextWindow int     `json:"context_window"`
	InputLimit    int     `json:"input_limit,omitempty"`
	OutputLimit   int     `json:"output_limit,omitempty"`
	ToolCall      bool    `json:"tool_call"`
	Status        string  `json:"status,omitempty"`
	Deprecated    bool    `json:"deprecated,omitempty"`
	Translated    bool    `json:"translated,omitempty"`
	Unavailable   string  `json:"unavailable,omitempty"`
	// Reasoning reports whether the model supports reasoning. It stays
	// meaningful even when ReasoningEfforts is empty: the model may reason
	// without exposing an effort control.
	Reasoning bool `json:"reasoning"`
	// ReasoningEfforts is the normalized, deduplicated, canonically ordered
	// set of effort values the model advertises. Empty means no effort
	// control.
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
}

// SupportsEffort reports whether the model advertises value as an effort.
func (m CatalogModel) SupportsEffort(value string) bool {
	return m.Reasoning && slices.Contains(m.ReasoningEfforts, value)
}

// ValidReasoningEffort reports whether value is a syntactically valid catalog
// effort. The reserved clear sentinel "default" is not a catalog value.
func ValidReasoningEffort(value string) bool {
	if value == "" || value == "default" || len(value) > MaxThinkingEffortBytes || !utf8.ValidString(value) {
		return false
	}
	for i, r := range value {
		switch {
		case i == 0 && r >= 'a' && r <= 'z':
		case i > 0 && ((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// Usable reports whether the harness can select the model.
func (m CatalogModel) Usable() bool { return m.Unavailable == "" }

// Catalog is one normalized provider catalog. Models retains unavailable
// entries; use Usable to filter them.
type Catalog struct {
	Provider  string
	Source    string
	FetchedAt time.Time
	ETag      string
	Models    map[string]CatalogModel
	// Warnings records non-fatal cache or refresh fallbacks that the caller
	// should surface to the operator.
	Warnings []string
}

// Lookup returns the normalized entry for id.
func (c Catalog) Lookup(id string) (CatalogModel, error) {
	m, ok := c.Models[id]
	if !ok {
		return CatalogModel{}, fmt.Errorf("%w: %q for provider %s", ErrModelNotFound, id, c.Provider)
	}
	return m, nil
}

// Usable returns selectable models sorted by ID.
func (c Catalog) Usable() []CatalogModel {
	models := make([]CatalogModel, 0, len(c.Models))
	for _, m := range c.Models {
		if !m.Usable() {
			continue
		}
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

// CatalogSource loads normalized provider catalogs. Load honors the cache TTL
// and conditional refresh; Revalidate bypasses the TTL but keeps stale-cache
// fallback; Refresh is an unconditional fetch with no fallback. Refresh may
// return a non-zero Catalog alongside ErrCatalogCache when the fetch succeeded
// but persistence failed, so an in-memory consumer can use the result.
type CatalogSource interface {
	Load(ctx context.Context, provider string) (Catalog, error)
	Revalidate(ctx context.Context, provider string) (Catalog, error)
	Refresh(ctx context.Context, provider string) (Catalog, error)
}

// CatalogOptions configures the models.dev source. StateDir is required;
// other zero values select production defaults. Tests inject a client, clock,
// and temporary state directory.
type CatalogOptions struct {
	// URL is the catalog source. Empty selects DefaultCatalogURL.
	URL string
	// StateDir is the persistent state directory. The cache lives under
	// <StateDir>/cache/models.dev/<provider>.json.
	StateDir string
	// Client is the HTTP client. Nil selects a client bounded by Timeout.
	Client *http.Client
	// UserAgent is sent with the request, for example "aharness/<version>".
	UserAgent string
	// Now supplies the clock. Nil selects time.Now.
	Now func() time.Time
	// TTL is the cache freshness window. Zero selects CatalogTTL.
	TTL time.Duration
	// Timeout bounds one catalog request. Zero selects
	// CatalogRequestTimeout.
	Timeout time.Duration
	// MaxBytes caps the response body. Zero selects MaxCatalogBytes.
	MaxBytes int64
}

// CatalogCachePath returns the normalized cache path for provider under the
// given state directory.
func CatalogCachePath(stateDir, provider string) string {
	return filepath.Join(stateDir, "cache", "models.dev", provider+".json")
}

// SortedModels returns every normalized entry, including unavailable ones,
// sorted by ID. Diagnostic and listing code that needs the complete catalog
// uses this instead of Usable.
func (c Catalog) SortedModels() []CatalogModel {
	models := make([]CatalogModel, 0, len(c.Models))
	for _, m := range c.Models {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}
