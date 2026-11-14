package tools

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const webAcceptHeader = "text/markdown, text/html;q=0.9, text/plain;q=0.8, application/xhtml+xml;q=0.7, application/json;q=0.5"

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
	provider string
	timeout  time.Duration
	fetcher  fetcher
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
		provider: provider,
		timeout:  timeout,
		fetcher:  newWebFetcher(opts, timeout),
	}
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

// redirectResult reports a stopped cross-host or downgrade redirect as
// successful data; the model decides whether to fetch the target explicitly.
func (t *webFetchTool) redirectResult(target *url.URL, res fetchResult, start time.Time) Result {
	finalURL := res.FinalURL
	if finalURL == "" {
		finalURL = target.String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\n", finalURL)
	fmt.Fprintf(&b, "Provider: %s\n", t.provider)
	if res.Status != 0 {
		fmt.Fprintf(&b, "Status: %d\n", res.Status)
	}
	if ct := sanitizeWebText(res.ContentType); ct != "" {
		fmt.Fprintf(&b, "Content-Type: %s\n", ct)
	}
	fmt.Fprintf(&b, "Redirect: %s\n", sanitizeWebText(res.RedirectURL))
	b.WriteString("---\n")
	fmt.Fprintf(&b, "Redirect not followed (%s). Call web_fetch with url=%s to follow it explicitly.\n",
		redirectReason(target, res.RedirectURL), res.RedirectURL)

	details, err := marshalWebFetchDetails(webFetchDetails{
		Provider:    t.provider,
		FinalURL:    finalURL,
		Status:      res.Status,
		ContentType: res.ContentType,
		Redirect:    true,
		RedirectURL: res.RedirectURL,
	})
	if err != nil {
		return webFetchErrorResult(err, start)
	}
	return Result{
		OK:         true,
		Code:       CodeOK,
		Output:     b.String(),
		Details:    details,
		DurationMS: time.Since(start).Milliseconds(),
	}
}

// redirectReason names why the redirect was not followed.
func redirectReason(from *url.URL, to string) string {
	next, err := url.Parse(to)
	if err != nil {
		return "redirect target"
	}
	if from.Scheme == "https" && next.Scheme == "http" {
		return "HTTPS-to-HTTP downgrade"
	}
	return "cross-host redirect"
}

// textResult renders the header, the requested text page, and its details.
func (t *webFetchTool) textResult(res fetchResult, finalURL, text, title, extraction, warning string, offset int, start time.Time) Result {
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\n", sanitizeWebText(finalURL))
	fmt.Fprintf(&b, "Provider: %s\n", t.provider)
	if res.Status != 0 {
		fmt.Fprintf(&b, "Status: %d\n", res.Status)
	}
	if ct := sanitizeWebText(res.ContentType); ct != "" {
		fmt.Fprintf(&b, "Content-Type: %s\n", ct)
	}
	if title != "" {
		fmt.Fprintf(&b, "Title: %s\n", title)
	}
	if extraction != "" {
		fmt.Fprintf(&b, "Extraction: %s\n", extraction)
	}
	fmt.Fprintf(&b, "Text-Bytes: %d\n", len(text))
	if warning != "" {
		fmt.Fprintf(&b, "Warning: %s\n", warning)
	}
	b.WriteString("---\n")

	details := webFetchDetails{
		Provider:    t.provider,
		FinalURL:    finalURL,
		Status:      res.Status,
		ContentType: res.ContentType,
		Title:       title,
		Extraction:  extraction,
		TextBytes:   len(text),
		Offset:      offset,
		Warning:     warning,
	}

	if offset >= len(text) {
		fmt.Fprintf(&b, "offset %d is at or beyond the end of the text (%d bytes); no text returned.\n", offset, len(text))
		encoded, err := marshalWebFetchDetails(details)
		if err != nil {
			return webFetchErrorResult(err, start)
		}
		return Result{OK: true, Code: CodeOK, Output: b.String(), Details: encoded, DurationMS: time.Since(start).Milliseconds()}
	}
	if offset > 0 && !utf8.RuneStart(text[offset]) {
		for offset < len(text) && !utf8.RuneStart(text[offset]) {
			offset++
		}
		details.Offset = offset
		if offset == len(text) {
			fmt.Fprintf(&b, "offset %d is at or beyond the end of the text (%d bytes); no text returned.\n", offset, len(text))
			encoded, err := marshalWebFetchDetails(details)
			if err != nil {
				return webFetchErrorResult(err, start)
			}
			return Result{OK: true, Code: CodeOK, Output: b.String(), Details: encoded, DurationMS: time.Since(start).Milliseconds()}
		}
	}

	page := text[offset:]
	truncated := false
	if len(page) > maxWebOutputBytes {
		page = page[:safeHeadLen(page, maxWebOutputBytes)]
		truncated = true
	}
	b.WriteString(page)
	if truncated {
		if !strings.HasSuffix(page, "\n") {
			b.WriteByte('\n')
		}
		next := offset + len(page)
		fmt.Fprintf(&b, "[truncated at %d bytes; continue with offset=%d]\n", maxWebOutputBytes, next)
		details.NextOffset = next
	}
	details.Truncated = truncated

	encoded, err := marshalWebFetchDetails(details)
	if err != nil {
		return webFetchErrorResult(err, start)
	}
	return Result{
		OK:         true,
		Code:       CodeOK,
		Output:     b.String(),
		Truncated:  truncated,
		Details:    encoded,
		DurationMS: time.Since(start).Milliseconds(),
	}
}

func marshalWebFetchDetails(details webFetchDetails) (json.RawMessage, error) {
	encoded, err := jsonv2.Marshal(details)
	if err != nil {
		return nil, webErrorf(CodeInternal, "web_fetch: encode details: %v", err)
	}
	return encoded, nil
}

// webFetchErrorResult maps a coded fetch failure or a context error to one
// result and stamps the elapsed time.
func webFetchErrorResult(err error, start time.Time) Result {
	elapsed := time.Since(start).Milliseconds()
	var we *webError
	if errors.As(err, &we) {
		res := Failure(we.code, sanitizeUTF8(we.msg))
		res.DurationMS = elapsed
		return res
	}
	res := errorResult(err)
	res.DurationMS = elapsed
	return res
}

// extractedTextRunes counts non-space runes in extracted text.
func extractedTextRunes(text string) int {
	count := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		count++
	}
	return count
}

// textExtractionLabel names the pass-through extraction kind from the media
// type. The tool never synthesizes Markdown.
func textExtractionLabel(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return extractionText
	}
	switch strings.ToLower(mediaType) {
	case "text/markdown":
		return extractionMarkdown
	case "application/json":
		return extractionJSON
	case "application/xml", "text/xml":
		return extractionXML
	default:
		return extractionText
	}
}

// webContentKind is the dispatch class for one response body.
type webContentKind int

const (
	webContentUnsupported webContentKind = iota
	webContentHTML
	webContentText
)

// classifyWebContent maps a response media type to a dispatch class. Missing
// or unrecognized types sniff up to 512 bytes with http.DetectContentType.
func classifyWebContent(contentType string, raw []byte) webContentKind {
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && mediaType != "" {
		if kind, ok := explicitWebContentKind(strings.ToLower(mediaType)); ok {
			return kind
		}
	}
	sniffed, _, err := mime.ParseMediaType(http.DetectContentType(raw[:min(len(raw), maxWebSniffBytes)]))
	if err != nil {
		return webContentUnsupported
	}
	switch strings.ToLower(sniffed) {
	case "text/html":
		return webContentHTML
	case "text/plain", "text/xml":
		return webContentText
	default:
		return webContentUnsupported
	}
}

// explicitWebContentKind reports the dispatch class for a recognized media
// type. ok is false when the type is unrecognized and should be sniffed.
func explicitWebContentKind(mediaType string) (kind webContentKind, ok bool) {
	switch mediaType {
	case "text/html", "application/xhtml+xml":
		return webContentHTML, true
	case "text/plain", "text/markdown", "application/json", "application/xml", "text/xml", "text/csv":
		return webContentText, true
	case "application/pdf", "application/octet-stream":
		return webContentUnsupported, true
	}
	if prefix, _, found := strings.Cut(mediaType, "/"); found {
		switch prefix {
		case "image", "audio", "video":
			return webContentUnsupported, true
		}
	}
	return webContentUnsupported, false
}

// decodeWebBody decodes the declared charset to UTF-8. A Content-Type charset
// and an HTML meta charset are honored, a BOM is honored, and everything else
// defaults to UTF-8 with invalid sequences replaced.
func decodeWebBody(raw []byte, contentType string) (string, error) {
	label := ""
	if mediaType, params, err := mime.ParseMediaType(contentType); err == nil {
		label = params["charset"]
		if label == "" && (mediaType == "text/html" || mediaType == "application/xhtml+xml") {
			label = declaredMetaCharset(raw)
		}
	} else {
		label = declaredMetaCharset(raw)
	}
	if label == "" {
		if _, name, certain := charset.DetermineEncoding(raw, ""); certain {
			label = name
		}
	}
	if label == "" {
		return sanitizeUTF8(string(raw)), nil
	}
	reader, err := charset.NewReaderLabel(label, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return sanitizeUTF8(string(decoded)), nil
}

// declaredMetaCharset scans the first KiB for an HTML meta charset declaration.
func declaredMetaCharset(raw []byte) string {
	prefix := raw[:min(len(raw), 1024)]
	tokenizer := html.NewTokenizer(bytes.NewReader(prefix))
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := tokenizer.TagName()
			if !hasAttr || !bytes.EqualFold(name, []byte("meta")) {
				continue
			}
			charsetLabel := ""
			httpEquiv := ""
			content := ""
			for {
				key, value, more := tokenizer.TagAttr()
				switch strings.ToLower(string(key)) {
				case "charset":
					charsetLabel = string(value)
				case "http-equiv":
					httpEquiv = string(value)
				case "content":
					content = string(value)
				}
				if !more {
					break
				}
			}
			if charsetLabel != "" {
				return charsetLabel
			}
			if strings.EqualFold(httpEquiv, "content-type") {
				if _, params, err := mime.ParseMediaType(content); err == nil {
					if label := params["charset"]; label != "" {
						return label
					}
				}
			}
		}
	}
}

// builtinFetcher performs the static HTTP fetch with the address guard.
type builtinFetcher struct {
	client        *http.Client
	dialer        *net.Dialer
	userAgent     string
	timeout       time.Duration
	allowLoopback bool
}

// newBuiltinFetcher constructs the dedicated web client. It never uses
// http.DefaultClient, never reads proxy environment variables, never disables
// TLS verification, and keeps no cookie jar.
func newBuiltinFetcher(opts Options, timeout time.Duration) *builtinFetcher {
	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = "aharness/devel"
	}
	connectTimeout := min(10*time.Second, timeout)
	f := &builtinFetcher{
		dialer:        &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second},
		userAgent:     userAgent,
		timeout:       timeout,
		allowLoopback: opts.WebAllowLoopback,
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
	if err := f.checkHost(ctx, target.Hostname()); err != nil {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
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
		return webErrorf(CodeBlockedAddress, "web_fetch: %s resolves to a blocked address", blocked.host)
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
	if err := f.checkHost(ctx, validated.Hostname()); err != nil {
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
	switch u.Scheme {
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

// checkHost resolves a host and refuses it when any address is blocked.
func (f *builtinFetcher) checkHost(ctx context.Context, host string) error {
	addrs, err := f.resolve(ctx, host)
	if err != nil {
		return webErrorf(CodeFetchFailed, "web_fetch: resolve %s: %v", host, err)
	}
	for _, addr := range addrs {
		if f.blocked(addr) {
			return webErrorf(CodeBlockedAddress, "web_fetch: %s resolves to a blocked address", host)
		}
	}
	return nil
}

// dialContext resolves, checks every address, and dials a checked address so a
// rebinding resolver cannot swap in a blocked destination.
func (f *builtinFetcher) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := f.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		if f.blocked(addr) {
			return nil, &blockedAddressError{host: host}
		}
	}
	var lastErr error
	for _, addr := range addrs {
		conn, err := f.dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s has no usable address", host)
	}
	return nil, lastErr
}

// resolve returns the literal address or the resolver's answer.
func (f *builtinFetcher) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses")
	}
	return addrs, nil
}

// blocked reports whether an address is outside the allowed public unicast
// set. The test-only loopback seam allows loopback literals and nothing else.
func (f *builtinFetcher) blocked(addr netip.Addr) bool {
	if f.allowLoopback {
		if unmap := addr.Unmap(); unmap.IsLoopback() {
			return false
		}
	}
	return isBlockedWebAddr(addr)
}

// blockedAddressError reports a hostname whose resolution is refused. It stays
// typed so the tool can return blocked_address instead of fetch_failed.
type blockedAddressError struct {
	host string
}

// Error implements error.
func (e *blockedAddressError) Error() string {
	return fmt.Sprintf("%s resolves to a blocked address", e.host)
}

// blockedWebPrefixes are the IANA special-purpose ranges that are not globally
// routable unicast destinations.
var blockedWebPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// nat64Prefix embeds an IPv4 destination in an IPv6 address.
var nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// isBlockedWebAddr reports whether addr is not a globally routable unicast
// address. IPv4-mapped and NAT64 addresses are checked through the embedded
// IPv4 address.
func isBlockedWebAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.Unmap()
	if addr.Is6() && nat64Prefix.Contains(addr) {
		embedded := addr.As16()
		return isBlockedWebAddr(netip.AddrFrom4([4]byte{embedded[12], embedded[13], embedded[14], embedded[15]}))
	}
	if !addr.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range blockedWebPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
