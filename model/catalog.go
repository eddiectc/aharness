package model

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	// Cost is the optional models.dev price snapshot in integer micro-USD per
	// one million tokens. Nil means the catalog did not publish usable prices.
	Cost *CatalogCost `json:"cost,omitempty"`
}

// CatalogCost is a normalized models.dev price set. Rates are integer
// micro-USD per one million tokens ($0.15 -> 150000). CacheReadMicroUSD and
// CacheWriteMicroUSD are nil when the catalog omitted that tier.
type CatalogCost struct {
	InputMicroUSD      int64  `json:"input_micro_usd"`
	OutputMicroUSD     int64  `json:"output_micro_usd"`
	CacheReadMicroUSD  *int64 `json:"cache_read_micro_usd,omitempty"`
	CacheWriteMicroUSD *int64 `json:"cache_write_micro_usd,omitempty"`
}

// Pricing is the effective per-run price snapshot recorded in run_start. It
// is observational: it estimates cost from catalog prices and is never a
// billing authority. Rates are micro-USD per one million tokens.
type Pricing struct {
	Available          bool      `json:"available"`
	Currency           string    `json:"currency,omitempty"`
	Unit               string    `json:"unit,omitempty"`
	InputMicroUSD      int64     `json:"input_micro_usd,omitempty"`
	OutputMicroUSD     int64     `json:"output_micro_usd,omitempty"`
	CacheReadMicroUSD  int64     `json:"cache_read_micro_usd,omitempty"`
	CacheWriteMicroUSD int64     `json:"cache_write_micro_usd,omitempty"`
	Source             string    `json:"source,omitempty"`
	FetchedAt          time.Time `json:"fetched_at,omitzero"`
}

// Pricing currency and unit values.
const (
	PricingCurrencyUSD          = "USD"
	PricingUnitPerMillionTokens = "per_million_tokens"
	microUSDPerCurrencyUnit     = 1_000_000
	pricingRoundHalfUpMicro     = microUSDPerCurrencyUnit / 2
)

// Pricing returns the effective price snapshot for the model. source and
// fetchedAt identify the catalog snapshot. The cache-read tier falls back to
// the input rate when models.dev omitted it; the cache-write tier falls back
// to zero.
func (m CatalogModel) Pricing(source string, fetchedAt time.Time) Pricing {
	if m.Cost == nil {
		return Pricing{}
	}
	cacheRead := m.Cost.InputMicroUSD
	if m.Cost.CacheReadMicroUSD != nil {
		cacheRead = *m.Cost.CacheReadMicroUSD
	}
	var cacheWrite int64
	if m.Cost.CacheWriteMicroUSD != nil {
		cacheWrite = *m.Cost.CacheWriteMicroUSD
	}
	return Pricing{
		Available:          true,
		Currency:           PricingCurrencyUSD,
		Unit:               PricingUnitPerMillionTokens,
		InputMicroUSD:      m.Cost.InputMicroUSD,
		OutputMicroUSD:     m.Cost.OutputMicroUSD,
		CacheReadMicroUSD:  cacheRead,
		CacheWriteMicroUSD: cacheWrite,
		Source:             source,
		FetchedAt:          fetchedAt.UTC(),
	}
}

// Cost estimates one response's cost in integer micro-USD. ok is false when
// pricing is unavailable or an arithmetic overflow would make the estimate
// meaningless. Reasoning tokens are already part of OutputTokens and are not
// charged twice.
func (p Pricing) Cost(u Usage) (cost int64, ok bool) {
	if !p.Available {
		return 0, false
	}
	uncached := u.UncachedInputTokens()
	parts := []struct {
		tokens int
		rate   int64
	}{
		{uncached, p.InputMicroUSD},
		{u.CachedInputTokens, p.CacheReadMicroUSD},
		{u.CacheWriteInputTokens, p.CacheWriteMicroUSD},
		{u.OutputTokens, p.OutputMicroUSD},
	}
	var sum int64
	for _, part := range parts {
		if part.tokens <= 0 || part.rate <= 0 {
			continue
		}
		tokens := int64(part.tokens)
		if tokens > (math.MaxInt64-sum)/part.rate {
			return 0, false
		}
		sum += tokens * part.rate
	}
	return (sum + pricingRoundHalfUpMicro) / microUSDPerCurrencyUnit, true
}

// valid reports whether every provided rate is a non-negative integer.
func (c CatalogCost) valid() bool {
	if c.InputMicroUSD < 0 || c.OutputMicroUSD < 0 {
		return false
	}
	if c.CacheReadMicroUSD != nil && *c.CacheReadMicroUSD < 0 {
		return false
	}
	if c.CacheWriteMicroUSD != nil && *c.CacheWriteMicroUSD < 0 {
		return false
	}
	return true
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

// ErrModelUnavailable marks a catalog model that cannot be selected.
var ErrModelUnavailable = errors.New("model is not usable")

// Usable reports whether the harness can select the model.
func (m CatalogModel) Usable() bool { return m.Unavailable == "" }

// Validate reports why a model cannot be selected. An unavailable model, a
// dialect this harness cannot drive, and a missing or invalid base URL all
// satisfy errors.Is(err, ErrModelUnavailable).
func (m CatalogModel) Validate() error {
	if m.Unavailable != "" {
		return fmt.Errorf("%w: %s", ErrModelUnavailable, m.Unavailable)
	}
	if m.Dialect != DialectChat && m.Dialect != DialectResponses {
		return fmt.Errorf("%w: unsupported dialect %q", ErrModelUnavailable, m.Dialect)
	}
	if _, reason := discoveredBaseURL(m.BaseURL); reason != "" {
		return fmt.Errorf("%w: %s", ErrModelUnavailable, reason)
	}
	if m.ContextWindow <= 0 {
		return fmt.Errorf("%w: missing context limit", ErrModelUnavailable)
	}
	if m.InputLimit < 0 || m.InputLimit > m.ContextWindow {
		return fmt.Errorf("%w: invalid input limit %d", ErrModelUnavailable, m.InputLimit)
	}
	if m.OutputLimit < 0 {
		return fmt.Errorf("%w: invalid output limit %d", ErrModelUnavailable, m.OutputLimit)
	}
	return nil
}

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

// Price returns the model's pricing snapshot with this catalog's source and
// fetch time. ok is false when the model is absent or has no usable price.
func (c Catalog) Price(id string) (Pricing, bool) {
	m, ok := c.Models[id]
	if !ok {
		return Pricing{}, false
	}
	pricing := m.Pricing(c.Source, c.FetchedAt)
	return pricing, pricing.Available
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

// CatalogOptions configures the models.dev source. StateDir and UserAgent are
// required; other zero values select production defaults. Tests inject a
// client, clock, and temporary state directory.
type CatalogOptions struct {
	// URL is the catalog source. Empty selects DefaultCatalogURL.
	URL string
	// StateDir is the persistent state directory. The cache lives under
	// <StateDir>/cache/models.dev/<provider>.json.
	StateDir string
	// Client is the HTTP client. Nil selects a client bounded by Timeout.
	Client *http.Client
	// UserAgent is sent with every request and identifies the host, for example
	// "acode/1.2.0". Required.
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
