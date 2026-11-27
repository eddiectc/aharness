package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// webFetchArgs is the typed argument shape for web_fetch. It must agree with
// the web_fetch schema in schemas.go.
type webFetchArgs struct {
	Intent string `json:"intent,omitempty"`
	URL    string `json:"url"`
	Offset *int   `json:"offset,omitempty"`
}

// webFetchTool executes one web fetch through the configured provider and owns
// target validation, extraction, sanitization, paging, and result construction.
type webFetchTool struct {
	provider  string
	userAgent string
	timeout   time.Duration
	fetcher   fetcher
}

// webFetchDetails is the stable, small details object for a fetch result.
type webFetchDetails struct {
	Provider    string `json:"provider"`
	FinalURL    string `json:"final_url,omitempty"`
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Title       string `json:"title,omitempty"`
	Extraction  string `json:"extraction,omitempty"`
	TextBytes   int    `json:"text_bytes"`
	Offset      int    `json:"offset"`
	NextOffset  int    `json:"next_offset,omitempty"`
	Truncated   bool   `json:"truncated"`
	Redirect    bool   `json:"redirect"`
	RedirectURL string `json:"redirect_url,omitempty"`
	Warning     string `json:"warning,omitempty"`
}

// newWebFetchTool constructs the immutable fetch tool for one registry.
func newWebFetchTool(opts Options) *webFetchTool {
	timeout := opts.WebTimeout
	if timeout <= 0 {
		timeout = WebTimeoutDefault
	}
	provider, err := ParseWebFetchProvider(opts.WebFetchProvider)
	if err != nil {
		provider = opts.WebFetchProvider
	}
	return &webFetchTool{
		provider:  provider,
		userAgent: webFetchUserAgent(opts),
		timeout:   timeout,
		fetcher:   newWebFetcher(opts, timeout),
	}
}

// webFetchUserAgent resolves the per-fetch override or the host identity. It
// returns an empty string only when the caller supplied neither, and the tool
// then refuses to send a request.
func webFetchUserAgent(opts Options) string {
	if opts.WebFetchUserAgent != "" {
		return opts.WebFetchUserAgent
	}
	return opts.UserAgent
}

// executeWebFetch validates the arguments and performs one fetch. Every
// failure maps to one result; the tool never retries automatically.
func (r *Registry) executeWebFetch(ctx context.Context, args json.RawMessage) Result {
	var a webFetchArgs
	if err := decodeArgs("web_fetch", args, &a); err != nil {
		return Failure(CodeInvalidArgs, err.Error())
	}
	offset := 0
	if a.Offset != nil {
		offset = *a.Offset
		if offset < 0 {
			return invalidArgs("web_fetch", "offset must be non-negative")
		}
	}
	target, err := parseWebFetchTarget(a.URL)
	if err != nil {
		return Failure(CodeInvalidArgs, "web_fetch: "+err.Error())
	}
	if strings.TrimSpace(r.web.userAgent) == "" {
		return Failure(CodeUnavailable,
			"web_fetch: a User-Agent is required; set it in the host configuration")
	}
	return r.web.fetch(ctx, target, offset)
}

// parseWebFetchTarget validates the model-supplied URL shape shared by every
// provider: absolute http(s), a host, no userinfo, no control characters, no
// fragment, and at most 8 KiB. Address and redirect policy belong to the
// provider.
func parseWebFetchTarget(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("url is required")
	}
	if len(raw) > maxWebURLBytes {
		return nil, fmt.Errorf("url is %d bytes; the limit is %d", len(raw), maxWebURLBytes)
	}
	if !utf8.ValidString(raw) {
		return nil, errors.New("url must be valid UTF-8")
	}
	if hasControl(raw) {
		return nil, errors.New("url must not contain control characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q: only http and https are allowed", u.Scheme)
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("url must include a host")
	}
	if u.User != nil {
		return nil, errors.New("userinfo is not allowed in a fetch URL")
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}

// fetch runs the provider and builds the model-visible result.
func (t *webFetchTool) fetch(ctx context.Context, target *url.URL, offset int) Result {
	start := time.Now()
	res, err := t.fetcher.Fetch(ctx, target)
	if err != nil {
		return webFetchErrorResult(err, start)
	}
	if res.Redirect {
		return t.redirectResult(target, res, start)
	}
	if res.HTML == "" && res.Text == "" {
		return webFetchErrorResult(webErrorf(CodeContentUnavailable,
			"web_fetch: %s returned no usable content (%s)", target, unavailableEmpty), start)
	}

	text, title, extraction, warning := "", "", "", ""
	if res.HTML != "" {
		if reason := detectWebBlockage(res.HTML, t.provider == WebFetchProviderBuiltin); reason != "" {
			return webFetchErrorResult(webErrorf(CodeContentUnavailable,
				"web_fetch: %s returned a %s page; the main content is not available", target, reason), start)
		}
		ext := extractWebHTML(res.HTML, target)
		text, title, extraction = ext.Text, ext.Title, ext.Method
		if text == "" {
			reason := unavailableEmpty
			if looksLikeJSShell(res.HTML) {
				reason = unavailableJavaScript
			}
			return webFetchErrorResult(webErrorf(CodeContentUnavailable,
				"web_fetch: %s returned no usable content (%s)", target, reason), start)
		}
		if t.provider == WebFetchProviderBuiltin && extractedTextRunes(text) < maxWebShortTextRunes && looksLikeJSShell(res.HTML) {
			return webFetchErrorResult(webErrorf(CodeContentUnavailable,
				"web_fetch: %s returned a %s page; the main content is not available", target, unavailableJavaScript), start)
		}
		if extractedTextRunes(text) < maxWebShortTextRunes && webPageTextRunes(res.HTML) >= maxWebSubstantialBody {
			warning = unavailableShortExtraction
		}
	} else {
		text = sanitizeWebText(res.Text)
		extraction = textExtractionLabel(res.ContentType)
		if text == "" {
			return webFetchErrorResult(webErrorf(CodeContentUnavailable,
				"web_fetch: %s returned no usable content (%s)", target, unavailableEmptyResponse), start)
		}
	}

	finalURL := res.FinalURL
	if finalURL == "" {
		finalURL = target.String()
	}
	return t.textResult(res, finalURL, text, title, extraction, warning, offset, start)
}
