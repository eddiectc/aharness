package tools

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Web fetch providers. Providers are compiled in and selected once by
// configuration; there is no plugin loading, discovery, or per-call routing.
const (
	// WebFetchProviderBuiltin is the in-process static fetcher.
	WebFetchProviderBuiltin = "builtin"
	// WebFetchProviderCrawl4AI delegates rendering to an operator-configured
	// Crawl4AI server.
	WebFetchProviderCrawl4AI = "crawl4ai"
)

// Web fetch bounds. These are part of the runtime contract; changing one
// requires updating docs/limits.md.
const (
	// WebTimeoutDefault is the default bound on one web_fetch request,
	// covering connect, redirects, and body reads.
	WebTimeoutDefault = 30 * time.Second
	// WebTimeoutMax is the largest accepted web_timeout.
	WebTimeoutMax = 120 * time.Second

	maxWebURLBytes        = 8 << 10
	maxWebKeyBytes        = 4 << 10
	maxWebBodyBytes       = 2 << 20
	maxWebProviderBytes   = 16 << 20
	maxWebOutputBytes     = 60 << 10
	maxWebHeaderBytes     = 64 << 10
	maxWebRedirects       = 5
	maxWebTitleRunes      = 200
	maxWebSniffBytes      = 512
	maxWebChallengeBytes  = 4 << 10
	maxWebShortTextRunes  = 100
	maxWebSubstantialBody = 1_000
)

// fetcher retrieves one URL. Providers fetch only; the tool owns target
// validation, extraction, sanitization, caps, paging, and result construction
// so every provider produces the same contract.
type fetcher interface {
	Fetch(ctx context.Context, target *url.URL) (fetchResult, error)
}

// fetchResult is a provider-neutral fetch outcome. HTML carries rendered or raw
// markup for extraction; Text carries non-HTML text passed through unchanged
// (builtin only). FinalURL, Status, and ContentType are zero when the provider
// does not supply them and must never be fabricated. Redirect marks a stopped
// cross-host or downgrade redirect; RedirectURL is then the target.
type fetchResult struct {
	HTML        string
	Text        string
	FinalURL    string
	Status      int
	ContentType string
	Redirect    bool
	RedirectURL string
}

// webError is a fetch failure with a stable, model-visible result code. It is
// mapped at the tool boundary; callers never match error strings.
type webError struct {
	code Code
	msg  string
}

// Error implements error.
func (e *webError) Error() string { return e.msg }

// webErrorf builds a coded web fetch failure.
func webErrorf(code Code, format string, args ...any) error {
	return &webError{code: code, msg: fmt.Sprintf(format, args...)}
}

// ParseWebFetchProvider validates an operator-supplied provider name. An empty
// value selects the builtin provider.
func ParseWebFetchProvider(raw string) (string, error) {
	switch raw {
	case "":
		return WebFetchProviderBuiltin, nil
	case WebFetchProviderBuiltin, WebFetchProviderCrawl4AI:
		return raw, nil
	default:
		return "", fmt.Errorf("invalid web fetch provider %q: must be %q or %q",
			raw, WebFetchProviderBuiltin, WebFetchProviderCrawl4AI)
	}
}

// ParseWebFetchURL validates and normalizes the operator-supplied provider
// endpoint. An empty value is returned unchanged; the caller decides whether
// the selected provider requires one. Unlike a fetch target, the provider
// endpoint may be loopback or private: it is the operator's own service.
func ParseWebFetchURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > maxWebURLBytes {
		return "", fmt.Errorf("invalid web fetch URL: %d bytes exceeds the %d-byte limit", len(raw), maxWebURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid web fetch URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid web fetch URL %q: scheme must be http or https", raw)
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("invalid web fetch URL %q: missing host", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("invalid web fetch URL %q: userinfo is not allowed", raw)
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("invalid web fetch URL %q: fragment is not allowed", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), nil
}

// ValidateWebFetchConfig checks the cross-field provider rules: builtin takes
// no endpoint, and crawl4ai requires one. The key is optional for both; it must
// be a header-safe bearer token when set.
func ValidateWebFetchConfig(provider, endpoint, key string) error {
	normalized, err := ParseWebFetchProvider(provider)
	if err != nil {
		return err
	}
	if err := ValidateWebFetchKey(key); err != nil {
		return err
	}
	switch normalized {
	case WebFetchProviderBuiltin:
		if endpoint != "" {
			return fmt.Errorf("web_fetch_url must be empty when web_fetch_provider is %q", WebFetchProviderBuiltin)
		}
	case WebFetchProviderCrawl4AI:
		if endpoint == "" {
			return fmt.Errorf("web_fetch_url is required when web_fetch_provider is %q", WebFetchProviderCrawl4AI)
		}
		if _, err := ParseWebFetchURL(endpoint); err != nil {
			return err
		}
	}
	return nil
}

// ValidateWebFetchKey checks an optional provider bearer token.
func ValidateWebFetchKey(key string) error {
	if len(key) > maxWebKeyBytes {
		return fmt.Errorf("invalid web fetch key: %d bytes exceeds the %d-byte limit", len(key), maxWebKeyBytes)
	}
	if hasControl(key) {
		return fmt.Errorf("invalid web fetch key: control characters are not allowed")
	}
	return nil
}

// newWebFetcher constructs the configured provider once. An inconsistent
// configuration yields a fetcher that fails closed instead of guessing a
// provider; configuration validation normally rejects it before this point.
func newWebFetcher(opts Options, timeout time.Duration) fetcher {
	provider, err := ParseWebFetchProvider(opts.WebFetchProvider)
	if err != nil {
		return failingFetcher{err: &webError{code: CodeUnavailable, msg: err.Error()}}
	}
	endpoint, err := ParseWebFetchURL(opts.WebFetchURL)
	if err != nil {
		return failingFetcher{err: &webError{code: CodeUnavailable, msg: err.Error()}}
	}
	if err := ValidateWebFetchConfig(provider, endpoint, opts.WebFetchKey); err != nil {
		return failingFetcher{err: &webError{code: CodeUnavailable, msg: err.Error()}}
	}
	allowedOrigins, err := ParseWebAllowedOrigins(opts.WebAllowedOrigins)
	if err != nil {
		return failingFetcher{err: &webError{code: CodeUnavailable, msg: err.Error()}}
	}
	if err := ValidateWebFetchAllowedOrigins(provider, allowedOrigins); err != nil {
		return failingFetcher{err: &webError{code: CodeUnavailable, msg: err.Error()}}
	}
	if provider == WebFetchProviderCrawl4AI {
		return newCrawl4AIFetcher(opts, endpoint, timeout)
	}
	return newBuiltinFetcher(opts, timeout, allowedOrigins)
}

// failingFetcher reports a construction failure without touching the network.
type failingFetcher struct {
	err error
}

// Fetch implements fetcher.
func (f failingFetcher) Fetch(context.Context, *url.URL) (fetchResult, error) {
	return fetchResult{}, f.err
}
