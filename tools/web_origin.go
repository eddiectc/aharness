package tools

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Origin allowlist caps. They bound operator configuration, not the model's
// input: the list is never read from fetched content.
const (
	maxWebAllowedOrigins    = 16
	maxWebAllowedOriginSize = 256
)

// ParseWebAllowedOrigins validates and normalizes an operator-supplied origin
// allowlist. Each entry is an absolute http(s) origin — scheme, host, and an
// optional port — with no userinfo, path, query, or fragment, for example
// "http://127.0.0.1:8888". The normalized form is scheme://host:port with the
// scheme's default port filled, lowercased. A duplicate is collapsed; an empty
// list stays empty. The values come from host configuration only, so fetched
// content can never widen the set.
func ParseWebAllowedOrigins(raw []string) ([]string, error) {
	if len(raw) > maxWebAllowedOrigins {
		return nil, fmt.Errorf("invalid web fetch allowed origins: %d entries exceeds the %d-entry limit", len(raw), maxWebAllowedOrigins)
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, entry := range raw {
		origin, err := parseWebAllowedOrigin(entry)
		if err != nil {
			return nil, err
		}
		if seen[origin] {
			continue
		}
		seen[origin] = true
		out = append(out, origin)
	}
	return out, nil
}

// parseWebAllowedOrigin validates and normalizes one allowlist entry.
func parseWebAllowedOrigin(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("invalid web fetch allowed origin: an entry is empty")
	}
	if len(raw) > maxWebAllowedOriginSize {
		return "", fmt.Errorf("invalid web fetch allowed origin: %d bytes exceeds the %d-byte limit", len(raw), maxWebAllowedOriginSize)
	}
	if hasControl(raw) {
		return "", fmt.Errorf("invalid web fetch allowed origin %q: control characters are not allowed", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid web fetch allowed origin %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("invalid web fetch allowed origin %q: only http and https are allowed", raw)
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("invalid web fetch allowed origin %q: a host is required", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("invalid web fetch allowed origin %q: userinfo is not allowed", raw)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" {
		return "", fmt.Errorf("invalid web fetch allowed origin %q: only scheme, host, and port are allowed", raw)
	}
	port := u.Port()
	if port == "" {
		port = defaultWebPort(scheme)
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}

// webOrigin returns the normalized origin of a fetch target, or "" when the URL
// has no usable scheme or host.
func webOrigin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		port = defaultWebPort(scheme)
	}
	if port == "" || u.Hostname() == "" {
		return ""
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// ValidateWebFetchAllowedOrigins checks an origin allowlist against the
// selected provider. The builtin fetcher enforces the list on every hop; the
// Crawl4AI provider sends one URL and lets Chromium fetch subresources itself,
// so an SDK-side list would bind only the top-level request. A list with that
// provider is therefore a configuration error rather than a silent no-op.
func ValidateWebFetchAllowedOrigins(provider string, origins []string) error {
	normalized, err := ParseWebFetchProvider(provider)
	if err != nil {
		return err
	}
	if len(origins) == 0 {
		return nil
	}
	if normalized != WebFetchProviderBuiltin {
		return fmt.Errorf("web fetch allowed origins require the %q provider: %q cannot enforce a per-resource origin list",
			WebFetchProviderBuiltin, WebFetchProviderCrawl4AI)
	}
	_, err = ParseWebAllowedOrigins(origins)
	return err
}
