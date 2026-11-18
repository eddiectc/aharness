package tools

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// crawl4AIRequest is the single-URL /crawl request body. One URL per call,
// caching disabled, and robots.txt unchecked preserves the single-URL reader
// contract; both parameters are accepted by Crawl4AI's untrusted-config
// allowlist.
type crawl4AIRequest struct {
	URLs          []string              `json:"urls"`
	CrawlerConfig crawl4AICrawlerConfig `json:"crawler_config"`
}

type crawl4AICrawlerConfig struct {
	Type   string             `json:"type"`
	Params crawl4AIParameters `json:"params"`
}

type crawl4AIParameters struct {
	CacheMode      string `json:"cache_mode"`
	CheckRobotsTxt bool   `json:"check_robots_txt"`
}

// crawl4AIResult is the subset of one result the fetch contract needs.
type crawl4AIResult struct {
	Success       bool
	HTML          string
	StatusCode    int
	RedirectedURL string
	ContentType   string
	ErrorMessage  string
}

// crawl4AIEnvelope is the subset of the provider envelope the contract needs.
type crawl4AIEnvelope struct {
	Success      bool
	Results      []crawl4AIResult
	ErrorMessage string
}

// crawl4AIFetcher delegates rendering to an operator-configured Crawl4AI
// endpoint. The endpoint may be loopback or private; the provider's own guard
// is the egress control.
type crawl4AIFetcher struct {
	endpoint  string
	key       string
	userAgent string
	client    *http.Client
	timeout   time.Duration
}

// newCrawl4AIFetcher constructs the provider client. It has the web timeout and
// header cap but deliberately no address guard: the endpoint is the operator's
// service.
func newCrawl4AIFetcher(opts Options, endpoint string, timeout time.Duration) *crawl4AIFetcher {
	f := &crawl4AIFetcher{
		endpoint:  strings.TrimRight(endpoint, "/"),
		key:       opts.WebFetchKey,
		userAgent: webFetchUserAgent(opts),
		timeout:   timeout,
	}
	var client *http.Client
	if opts.WebClient != nil {
		clone := *opts.WebClient
		clone.Jar = nil
		if clone.Timeout <= 0 {
			clone.Timeout = timeout
		}
		if clone.Transport == nil {
			clone.Transport = &http.Transport{Proxy: nil, MaxResponseHeaderBytes: maxWebHeaderBytes}
		}
		client = &clone
	} else {
		client = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy:                  nil,
				ForceAttemptHTTP2:      true,
				TLSHandshakeTimeout:    min(10*time.Second, timeout),
				MaxResponseHeaderBytes: maxWebHeaderBytes,
				MaxIdleConns:           2,
				IdleConnTimeout:        30 * time.Second,
			},
		}
	}
	f.client = client
	return f
}

// Fetch implements fetcher. A provider failure is never retried against the
// builtin path.
func (f *crawl4AIFetcher) Fetch(ctx context.Context, target *url.URL) (fetchResult, error) {
	body, err := jsonv2.Marshal(crawl4AIRequest{
		URLs: []string{target.String()},
		CrawlerConfig: crawl4AICrawlerConfig{
			Type:   "CrawlerRunConfig",
			Params: crawl4AIParameters{CacheMode: "disabled", CheckRobotsTxt: false},
		},
	})
	if err != nil {
		return fetchResult{}, webErrorf(CodeInternal, "web_fetch: encode provider request: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint+"/crawl", bytes.NewReader(body))
	if err != nil {
		return fetchResult{}, webErrorf(CodeFetchFailed, "web_fetch: build provider request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", f.userAgent)
	if f.key != "" {
		req.Header.Set("Authorization", "Bearer "+f.key)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return fetchResult{}, f.requestError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		drainWebBody(resp.Body)
		message := fmt.Sprintf("web_fetch: provider %s returned HTTP %s", providerHost(f.endpoint), resp.Status)
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			message += " (the provider rejected the configured key)"
		case http.StatusTooManyRequests:
			message += " (the provider is rate limiting)"
		}
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusGatewayTimeout {
			return fetchResult{}, webErrorf(CodeTimeout, "%s", message)
		}
		return fetchResult{}, webErrorf(CodeFetchFailed, "%s", message)
	}

	envelope, err := decodeCrawl4AIResponse(resp.Body)
	if err != nil {
		if errors.Is(err, errWebResponseTooLarge) {
			return fetchResult{}, webErrorf(CodeTooLarge,
				"web_fetch: provider response exceeds %d bytes", maxWebProviderBytes)
		}
		return fetchResult{}, webErrorf(CodeFetchFailed,
			"web_fetch: decode provider response: %v", err)
	}
	if !envelope.Success {
		return fetchResult{}, webErrorf(CodeFetchFailed,
			"web_fetch: provider failed: %s", providerMessage(envelope.ErrorMessage, "unknown error"))
	}
	if len(envelope.Results) == 0 {
		return fetchResult{}, webErrorf(CodeFetchFailed, "web_fetch: provider returned no results")
	}
	result := envelope.Results[0]
	if !result.Success {
		return fetchResult{}, webErrorf(CodeFetchFailed,
			"web_fetch: provider failed: %s", providerMessage(result.ErrorMessage, "unknown error"))
	}
	html := result.HTML
	if html == "" {
		return fetchResult{}, webErrorf(CodeContentUnavailable,
			"web_fetch: provider returned no rendered HTML (%s)", unavailableEmpty)
	}
	if len(html) > maxWebBodyBytes {
		return fetchResult{}, webErrorf(CodeTooLarge,
			"web_fetch: provider HTML exceeds %d bytes", maxWebBodyBytes)
	}
	return fetchResult{
		HTML:        html,
		FinalURL:    firstNonEmpty(result.RedirectedURL, target.String()),
		Status:      result.StatusCode,
		ContentType: result.ContentType,
	}, nil
}

// requestError classifies a provider transport failure.
func (f *crawl4AIFetcher) requestError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if isWebTimeout(err) {
		return webErrorf(CodeTimeout, "web_fetch: provider %s timed out after %s", providerHost(f.endpoint), f.timeout)
	}
	return webErrorf(CodeFetchFailed, "web_fetch: provider %s: %v", providerHost(f.endpoint), err)
}

// providerHost returns the endpoint host for model-visible errors, never the
// full endpoint or key.
func providerHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "endpoint"
	}
	return u.Host
}

// providerMessage sanitizes and bounds a provider error message.
func providerMessage(message, fallback string) string {
	cleaned := sanitizeWebText(message)
	if cleaned == "" {
		return fallback
	}
	return truncateRunes(strings.Join(strings.Fields(cleaned), " "), 400)
}

// firstNonEmpty returns the first non-empty value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// errWebResponseTooLarge reports a provider body that exceeded its cap.
var errWebResponseTooLarge = errors.New("provider response too large")

// decodeCrawl4AIResponse stream-decodes the Crawl4AI envelope and skips fields
// the tool does not use, so a page with cleaned_html, markdown, media, links,
// screenshots, or network requests does not double memory.
func decodeCrawl4AIResponse(r io.Reader) (crawl4AIEnvelope, error) {
	decoder := jsontext.NewDecoder(&limitedReader{r: r, remaining: maxWebProviderBytes})
	var envelope crawl4AIEnvelope

	if err := expectKind(decoder, '{'); err != nil {
		return crawl4AIEnvelope{}, err
	}
	for decoder.PeekKind() != '}' {
		key, err := readObjectKey(decoder)
		if err != nil {
			return crawl4AIEnvelope{}, err
		}
		switch key {
		case "success":
			if err := decodeBoolValue(decoder, &envelope.Success); err != nil {
				return crawl4AIEnvelope{}, err
			}
		case "error_message":
			if err := decodeStringValue(decoder, &envelope.ErrorMessage); err != nil {
				return crawl4AIEnvelope{}, err
			}
		case "results":
			results, err := decodeCrawl4AIResults(decoder)
			if err != nil {
				return crawl4AIEnvelope{}, err
			}
			envelope.Results = results
		default:
			if err := decoder.SkipValue(); err != nil {
				return crawl4AIEnvelope{}, err
			}
		}
	}
	if err := expectKind(decoder, '}'); err != nil {
		return crawl4AIEnvelope{}, err
	}
	return envelope, nil
}

// decodeCrawl4AIResults decodes the results array, keeping the first result and
// skipping the rest.
func decodeCrawl4AIResults(decoder *jsontext.Decoder) ([]crawl4AIResult, error) {
	if err := expectKind(decoder, '['); err != nil {
		return nil, err
	}
	var results []crawl4AIResult
	for decoder.PeekKind() != ']' {
		if decoder.PeekKind() != '{' {
			if err := decoder.SkipValue(); err != nil {
				return nil, err
			}
			continue
		}
		result, err := decodeCrawl4AIResult(decoder)
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			results = append(results, result)
		}
	}
	if err := expectKind(decoder, ']'); err != nil {
		return nil, err
	}
	return results, nil
}

// decodeCrawl4AIResult decodes one result object, ignoring every other field.
func decodeCrawl4AIResult(decoder *jsontext.Decoder) (crawl4AIResult, error) {
	var result crawl4AIResult
	if err := expectKind(decoder, '{'); err != nil {
		return crawl4AIResult{}, err
	}
	for decoder.PeekKind() != '}' {
		key, err := readObjectKey(decoder)
		if err != nil {
			return crawl4AIResult{}, err
		}
		switch key {
		case "success":
			err = decodeBoolValue(decoder, &result.Success)
		case "html":
			err = decodeStringValue(decoder, &result.HTML)
		case "status_code":
			err = decodeIntValue(decoder, &result.StatusCode)
		case "redirected_url":
			err = decodeStringValue(decoder, &result.RedirectedURL)
		case "error_message":
			err = decodeStringValue(decoder, &result.ErrorMessage)
		case "response_headers":
			err = decodeContentTypeHeader(decoder, &result.ContentType)
		default:
			err = decoder.SkipValue()
		}
		if err != nil {
			return crawl4AIResult{}, err
		}
	}
	if err := expectKind(decoder, '}'); err != nil {
		return crawl4AIResult{}, err
	}
	return result, nil
}

// decodeContentTypeHeader finds the content type in the response_headers
// object, which Crawl4AI reports with lower-case header names.
func decodeContentTypeHeader(decoder *jsontext.Decoder, target *string) error {
	if err := expectKind(decoder, '{'); err != nil {
		return err
	}
	for decoder.PeekKind() != '}' {
		key, err := readObjectKey(decoder)
		if err != nil {
			return err
		}
		if strings.EqualFold(key, "content-type") {
			if err := decodeStringValue(decoder, target); err != nil {
				return err
			}
			continue
		}
		if err := decoder.SkipValue(); err != nil {
			return err
		}
	}
	return expectKind(decoder, '}')
}

// readObjectKey reads one object member name.
func readObjectKey(decoder *jsontext.Decoder) (string, error) {
	token, err := decoder.ReadToken()
	if err != nil {
		return "", err
	}
	if token.Kind() != '"' {
		return "", fmt.Errorf("expected object key, got %s", token.Kind())
	}
	return token.String(), nil
}

// expectKind reads one structural token of the expected kind.
func expectKind(decoder *jsontext.Decoder, kind jsontext.Kind) error {
	token, err := decoder.ReadToken()
	if err != nil {
		return err
	}
	if token.Kind() != kind {
		return fmt.Errorf("expected %s, got %s", kind, token.Kind())
	}
	return nil
}

// decodeBoolValue decodes one JSON boolean value.
func decodeBoolValue(decoder *jsontext.Decoder, target *bool) error {
	value, err := decoder.ReadValue()
	if err != nil {
		return err
	}
	return jsonv2.Unmarshal(value, target)
}

// decodeStringValue decodes one JSON string value.
func decodeStringValue(decoder *jsontext.Decoder, target *string) error {
	value, err := decoder.ReadValue()
	if err != nil {
		return err
	}
	return jsonv2.Unmarshal(value, target)
}

// decodeIntValue decodes one JSON integer value.
func decodeIntValue(decoder *jsontext.Decoder, target *int) error {
	value, err := decoder.ReadValue()
	if err != nil {
		return err
	}
	return jsonv2.Unmarshal(value, target)
}

// limitedReader fails once the provider body exceeds its cap instead of
// silently truncating a JSON document.
type limitedReader struct {
	r         io.Reader
	remaining int
}

// Read implements io.Reader. It allows one byte past the cap so an exactly
// sized response still ends with io.EOF instead of a false too_large.
func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining < 0 {
		return 0, errWebResponseTooLarge
	}
	if len(p) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	l.remaining -= n
	if l.remaining < 0 {
		return n, errWebResponseTooLarge
	}
	return n, err
}
