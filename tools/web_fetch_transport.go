package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const webAcceptHeader = "text/markdown, text/html;q=0.9, text/plain;q=0.8, application/xhtml+xml;q=0.7, application/json;q=0.5"

// builtinFetcher performs the static HTTP fetch with the address guard.
type builtinFetcher struct {
	client        *http.Client
	dialer        *net.Dialer
	userAgent     string
	timeout       time.Duration
	allowLoopback bool
	// allowedOrigins holds normalized scheme://host:port entries. A blocked
	// address is permitted only for a request whose origin is in this set.
	allowedOrigins map[string]bool
}

// newBuiltinFetcher constructs the dedicated web client. It never uses
// http.DefaultClient, never reads proxy environment variables, never disables
// TLS verification, and keeps no cookie jar.
func newBuiltinFetcher(opts Options, timeout time.Duration, allowedOrigins []string) *builtinFetcher {
	connectTimeout := min(10*time.Second, timeout)
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = true
	}
	f := &builtinFetcher{
		dialer:         &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second},
		userAgent:      webFetchUserAgent(opts),
		timeout:        timeout,
		allowLoopback:  opts.WebAllowLoopback,
		allowedOrigins: allowed,
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
				DialContext:            f.dialContext,
				ForceAttemptHTTP2:      true,
				TLSHandshakeTimeout:    connectTimeout,
				MaxResponseHeaderBytes: maxWebHeaderBytes,
				MaxIdleConns:           2,
				IdleConnTimeout:        30 * time.Second,
			},
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	f.client = client
	return f
}

// Fetch implements fetcher. Redirects are followed manually so every hop can
// be revalidated and cross-host or downgrade redirects can be returned as data.
func (f *builtinFetcher) Fetch(ctx context.Context, target *url.URL) (fetchResult, error) {
	if err := f.checkHost(ctx, target, f.allowedOrigin(target)); err != nil {
		return fetchResult{}, err
	}
	current := target
	followed := 0
	for {
		resp, err := f.request(ctx, current)
		if err != nil {
			return fetchResult{}, err
		}
		if isWebRedirect(resp.StatusCode) {
			next, result, err := f.redirect(ctx, current, resp, followed)
			if err != nil {
				return fetchResult{}, err
			}
			if result.Redirect {
				return result, nil
			}
			current = next
			followed++
			continue
		}
		return f.readResponse(current, resp)
	}
}

// request sends one GET with the builtin headers and no credentials.
func (f *builtinFetcher) request(ctx context.Context, target *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(withWebAllowedOrigin(ctx, f.allowedOrigin(target)), http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, webErrorf(CodeFetchFailed, "web_fetch: build request for %s: %v", target, err)
	}
	req.Header.Set("Accept", webAcceptHeader)
	req.Header.Set("User-Agent", f.userAgent)
	// A redirect hop must not carry the previous body or a referrer.
	req.Header.Del("Referer")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, f.requestError(ctx, target, err)
	}
	return resp, nil
}

// requestError classifies a transport failure without matching error strings.
func (f *builtinFetcher) requestError(ctx context.Context, target *url.URL, err error) error {
	var blocked *blockedAddressError
	if errors.As(err, &blocked) {
		return webErrorf(CodeBlockedAddress, "%s", blocked.message())
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if isWebTimeout(err) {
		return webErrorf(CodeTimeout, "web_fetch: request to %s timed out after %s", target, f.timeout)
	}
	return webErrorf(CodeFetchFailed, "web_fetch: request to %s: %v", target, err)
}

// redirect inspects one 3xx response. A followed same-host redirect returns
// next; a stopped cross-host or downgrade redirect returns Redirect data.
func (f *builtinFetcher) redirect(ctx context.Context, from *url.URL, resp *http.Response, followed int) (*url.URL, fetchResult, error) {
	drainWebBody(resp.Body)
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return nil, fetchResult{}, webErrorf(CodeHTTPError,
			"web_fetch: %s returned HTTP %d without a Location header", from, resp.StatusCode)
	}
	next, err := from.Parse(location)
	if err != nil {
		return nil, fetchResult{}, webErrorf(CodeHTTPError,
			"web_fetch: %s returned an invalid redirect %q", from, location)
	}
	validated, err := parseWebFetchTarget(next.String())
	if err != nil {
		return nil, fetchResult{}, webErrorf(CodeHTTPError,
			"web_fetch: %s returned an invalid redirect: %v", from, err)
	}
	if followed >= maxWebRedirects {
		return nil, fetchResult{}, webErrorf(CodeFetchFailed,
			"web_fetch: %s exceeded the %d-redirect limit", from, maxWebRedirects)
	}
	if !sameWebAuthority(from, validated) || (from.Scheme == "https" && validated.Scheme == "http") {
		return nil, fetchResult{
			Redirect:    true,
			RedirectURL: validated.String(),
			Status:      resp.StatusCode,
			ContentType: resp.Header.Get("Content-Type"),
			FinalURL:    from.String(),
		}, nil
	}
	// Guard the next hop before dialing; the dialer checks again.
	if err := f.checkHost(ctx, validated, f.allowedOrigin(validated)); err != nil {
		return nil, fetchResult{}, err
	}
	return validated, fetchResult{}, nil
}

// readResponse applies the status, size, encoding, and content-type policy and
// returns the provider-neutral result.
func (f *builtinFetcher) readResponse(target *url.URL, resp *http.Response) (fetchResult, error) {
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		drainWebBody(resp.Body)
		message := fmt.Sprintf("web_fetch: %s returned HTTP %s", target, resp.Status)
		if retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After")); retryAfter != "" {
			message += fmt.Sprintf("; Retry-After: %s", sanitizeWebText(retryAfter))
		}
		return fetchResult{}, webErrorf(CodeHTTPError, "%s", message)
	}
	if resp.StatusCode == http.StatusNoContent {
		drainWebBody(resp.Body)
		return fetchResult{}, webErrorf(CodeContentUnavailable,
			"web_fetch: %s returned HTTP 204 with no content", target)
	}
	if resp.ContentLength > maxWebBodyBytes {
		drainWebBody(resp.Body)
		return fetchResult{}, webErrorf(CodeTooLarge,
			"web_fetch: %s declares %d bytes; the limit is %d", target, resp.ContentLength, maxWebBodyBytes)
	}
	if encoding := strings.TrimSpace(resp.Header.Get("Content-Encoding")); encoding != "" &&
		!strings.EqualFold(encoding, "identity") && !resp.Uncompressed {
		drainWebBody(resp.Body)
		return fetchResult{}, webErrorf(CodeUnsupportedContentType,
			"web_fetch: %s uses unsupported content encoding %q", target, encoding)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBodyBytes+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return fetchResult{}, f.readError(target, err)
	}
	// A close error cannot change an already complete body.
	_ = closeErr
	if len(raw) > maxWebBodyBytes {
		return fetchResult{}, webErrorf(CodeTooLarge,
			"web_fetch: %s exceeds %d bytes", target, maxWebBodyBytes)
	}
	if len(raw) == 0 {
		return fetchResult{}, webErrorf(CodeContentUnavailable,
			"web_fetch: %s returned an empty body", target)
	}

	kind := classifyWebContent(contentType, raw)
	if kind == webContentUnsupported {
		return fetchResult{}, webErrorf(CodeUnsupportedContentType,
			"web_fetch: %s returned unsupported content type %q", target, unsupportedMediaType(contentType, raw))
	}
	decoded, err := decodeWebBody(raw, contentType)
	if err != nil {
		return fetchResult{}, webErrorf(CodeFetchFailed,
			"web_fetch: %s decode body: %v", target, err)
	}
	result := fetchResult{
		FinalURL:    target.String(),
		Status:      resp.StatusCode,
		ContentType: contentType,
	}
	if kind == webContentHTML {
		result.HTML = sanitizeWebText(decoded)
	} else {
		result.Text = decoded
	}
	return result, nil
}

// readError classifies a body-read failure.
func (f *builtinFetcher) readError(target *url.URL, err error) error {
	if isWebTimeout(err) {
		return webErrorf(CodeTimeout, "web_fetch: read %s timed out after %s", target, f.timeout)
	}
	return webErrorf(CodeFetchFailed, "web_fetch: read %s: %v", target, err)
}

// isWebTimeout reports a net/http timeout without matching strings.
func isWebTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// isWebRedirect reports the statuses the builtin client follows manually.
func isWebRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

// sameWebAuthority compares host and effective port.
func sameWebAuthority(a, b *url.URL) bool {
	return strings.EqualFold(a.Hostname(), b.Hostname()) && webPort(a) == webPort(b)
}

// webPort returns the effective port for a URL.
func webPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return defaultWebPort(u.Scheme)
}

// defaultWebPort returns the scheme's default port.
func defaultWebPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

// drainWebBody consumes and closes a response body that will not be read.
func drainWebBody(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4<<10))
	_ = body.Close()
}

// unsupportedMediaType names the rejected media type, falling back to the
// sniffed type when the header is missing or invalid.
func unsupportedMediaType(contentType string, raw []byte) string {
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && mediaType != "" {
		return mediaType
	}
	sniffed, _, err := mime.ParseMediaType(http.DetectContentType(raw[:min(len(raw), maxWebSniffBytes)]))
	if err == nil && sniffed != "" {
		return sniffed
	}
	return "unknown"
}
