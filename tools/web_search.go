package tools

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// SearchTimeout bounds one SearXNG request, covering connect, headers, and
	// body. It is fixed for v1; do not make it configurable without a plan.
	SearchTimeout = 15 * time.Second

	defaultSearchResultCount = 5
	maxSearchResultCount     = 10
	maxSearchQueryBytes      = 2 << 10
	maxSearchCategoriesBytes = 256
	maxSearchLanguageBytes   = 32
	maxSearchResponseBytes   = 2 << 20
	maxSearchOutputBytes     = 60 << 10
	maxSearchSnippetRunes    = 1200
	maxSearchURLBytes        = 8 << 10
	maxUnresponsiveRunes     = 200
)

// webSearchArgs is the typed argument shape for web_search. It must agree with
// the web_search schema in schemas.go.
type webSearchArgs struct {
	Intent     string `json:"intent,omitempty"`
	Query      string `json:"query"`
	MaxResults *int   `json:"max_results,omitempty"`
	Categories string `json:"categories,omitempty"`
	Language   string `json:"language,omitempty"`
	TimeRange  string `json:"time_range,omitempty"`
}

type webSearchRequest struct {
	Query      string
	MaxResults int
	Categories string
	Language   string
	TimeRange  string
}

type searxResponse struct {
	Results             []searxResult `json:"results"`
	UnresponsiveEngines [][]string    `json:"unresponsive_engines"`
}

type searxResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Content       string `json:"content"`
	PublishedDate string `json:"publishedDate"`
}

type searchResult struct {
	Title     string
	URL       string
	Snippet   string
	Published string
}

type searchClient struct {
	baseURL   string
	userAgent string
	http      *http.Client
	timeout   time.Duration
}

type webSearchDetails struct {
	Results      int      `json:"results"`
	Truncated    bool     `json:"truncated"`
	Unresponsive []string `json:"unresponsive,omitempty"`
}

// ParseSearXNGURL validates and normalizes the operator-supplied SearXNG base
// URL. An empty value disables web_search. The returned URL has no trailing
// slash so the client can append "/search".
func ParseSearXNGURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > maxSearchURLBytes {
		return "", fmt.Errorf("invalid SearXNG URL: %d bytes exceeds the %d-byte limit", len(raw), maxSearchURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid SearXNG URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid SearXNG URL %q: scheme must be http or https", raw)
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("invalid SearXNG URL %q: missing host", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("invalid SearXNG URL %q: userinfo is not allowed", raw)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("invalid SearXNG URL %q: query is not allowed", raw)
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("invalid SearXNG URL %q: fragment is not allowed", raw)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("invalid SearXNG URL %q: http is allowed only for loopback hosts", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// isLoopbackHost reports whether host names the local machine. "localhost" is
// accepted in addition to the loopback address literals.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newSearchClient(opts Options) *searchClient {
	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = "aharness/devel"
	}
	timeout := opts.SearchTimeout
	if timeout <= 0 {
		timeout = SearchTimeout
	}
	return &searchClient{
		baseURL:   opts.SearXNGURL,
		userAgent: userAgent,
		http:      &http.Client{},
		timeout:   timeout,
	}
}

func (r *Registry) executeWebSearch(ctx context.Context, args json.RawMessage) Result {
	var a webSearchArgs
	if err := decodeArgs("web_search", args, &a); err != nil {
		return Failure(CodeInvalidArgs, err.Error())
	}
	req, err := validateWebSearchArgs(a)
	if err != nil {
		return Failure(CodeInvalidArgs, "web_search: "+err.Error())
	}
	if r.search.baseURL == "" {
		return Failure(CodeUnavailable,
			"SearXNG is not configured; set the web_search URL in the host configuration")
	}
	return r.search.search(ctx, req)
}

// validateWebSearchArgs enforces the argument contract and applies the
// default/cap policy before any network request.
func validateWebSearchArgs(a webSearchArgs) (webSearchRequest, error) {
	req := webSearchRequest{Query: a.Query}
	if strings.TrimSpace(a.Query) == "" {
		return webSearchRequest{}, errors.New("query is required")
	}
	if !utf8.ValidString(a.Query) {
		return webSearchRequest{}, errors.New("query must be valid UTF-8")
	}
	if len(a.Query) > maxSearchQueryBytes {
		return webSearchRequest{}, fmt.Errorf("query is %d bytes; the limit is %d", len(a.Query), maxSearchQueryBytes)
	}
	req.MaxResults = defaultSearchResultCount
	if a.MaxResults != nil {
		req.MaxResults = *a.MaxResults
		if req.MaxResults < 1 {
			return webSearchRequest{}, errors.New("max_results must be at least 1")
		}
		if req.MaxResults > maxSearchResultCount {
			req.MaxResults = maxSearchResultCount
		}
	}
	if len(a.Categories) > maxSearchCategoriesBytes {
		return webSearchRequest{}, fmt.Errorf("categories is %d bytes; the limit is %d", len(a.Categories), maxSearchCategoriesBytes)
	}
	if hasControl(a.Categories) {
		return webSearchRequest{}, errors.New("categories must not contain control characters")
	}
	if len(a.Language) > maxSearchLanguageBytes {
		return webSearchRequest{}, fmt.Errorf("language is %d bytes; the limit is %d", len(a.Language), maxSearchLanguageBytes)
	}
	if hasControl(a.Language) {
		return webSearchRequest{}, errors.New("language must not contain control characters")
	}
	switch a.TimeRange {
	case "", "day", "month", "year":
	default:
		return webSearchRequest{}, fmt.Errorf("time_range must be day, month, or year, got %q", a.TimeRange)
	}
	req.Categories = a.Categories
	req.Language = a.Language
	req.TimeRange = a.TimeRange
	return req, nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

func (c *searchClient) search(ctx context.Context, req webSearchRequest) Result {
	endpoint, err := c.endpoint(req)
	if err != nil {
		return Failure(CodeInternal, sanitizeUTF8(fmt.Sprintf("build SearXNG request: %v", err)))
	}
	sctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(sctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Failure(CodeFetchFailed, sanitizeUTF8(fmt.Sprintf("build SearXNG request: %v", err)))
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return searchRequestResult(ctx, sctx, endpoint, "request", err, c.timeout)
	}
	// The body is either read below or discarded for a non-2xx status; a close
	// error cannot change the result.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode == http.StatusForbidden {
			return Failure(CodeUnavailable,
				"SearXNG returned HTTP 403 Forbidden; enable json in search.formats or check instance access")
		}
		return Failure(CodeHTTPError, sanitizeUTF8(fmt.Sprintf("SearXNG returned HTTP %s", resp.Status)))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchResponseBytes+1))
	if err != nil {
		return searchRequestResult(ctx, sctx, endpoint, "read response", err, c.timeout)
	}
	if len(data) > maxSearchResponseBytes {
		return Failure(CodeTooLarge, fmt.Sprintf("SearXNG response exceeds %d bytes", maxSearchResponseBytes))
	}
	parsed, err := parseSearXNGResponse(data)
	if err != nil {
		return Failure(CodeFetchFailed, sanitizeUTF8(fmt.Sprintf("decode SearXNG response: %v", err)))
	}

	results := collectSearchResults(parsed, req.MaxResults)
	unresponsive, unresponsiveDetails := searchUnresponsive(parsed.UnresponsiveEngines)
	output, truncated, delivered := renderSearchResults(req.Query, results, unresponsive)

	details, err := jsonv2.Marshal(webSearchDetails{
		Results:      delivered,
		Truncated:    truncated,
		Unresponsive: unresponsiveDetails,
	})
	if err != nil {
		return Failure(CodeInternal, sanitizeUTF8(fmt.Sprintf("encode web_search details: %v", err)))
	}
	return Result{
		OK:        true,
		Code:      CodeOK,
		Output:    output,
		Truncated: truncated,
		Details:   details,
	}
}

func searchRequestResult(parent, timeoutCtx context.Context, endpoint, operation string, err error, timeout time.Duration) Result {
	if cerr := parent.Err(); cerr != nil {
		return errorResult(cerr)
	}
	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return Failure(CodeTimeout, sanitizeUTF8(fmt.Sprintf("SearXNG %s to %s timed out after %s", operation, endpoint, timeout)))
	}
	return Failure(CodeFetchFailed, sanitizeUTF8(fmt.Sprintf("SearXNG %s to %s: %v", operation, endpoint, err)))
}

func (c *searchClient) endpoint(req webSearchRequest) (string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/search"
	u.RawPath = ""
	u.Fragment = ""
	u.RawFragment = ""
	values := url.Values{}
	values.Set("q", req.Query)
	values.Set("format", "json")
	values.Set("pageno", "1")
	if req.Categories != "" {
		values.Set("categories", req.Categories)
	}
	if req.Language != "" {
		values.Set("language", req.Language)
	}
	if req.TimeRange != "" {
		values.Set("time_range", req.TimeRange)
	}
	u.RawQuery = values.Encode()
	return u.String(), nil
}

func parseSearXNGResponse(data []byte) (searxResponse, error) {
	var resp searxResponse
	if err := jsonv2.Unmarshal(data, &resp); err != nil {
		return searxResponse{}, err
	}
	return resp, nil
}

// collectSearchResults filters, sanitizes, deduplicates, and caps the result
// list. Non-http(s) URLs and URLs with userinfo are skipped. The first result
// for a fragment-stripped URL wins.
func collectSearchResults(resp searxResponse, max int) []searchResult {
	if max <= 0 {
		return nil
	}
	results := make([]searchResult, 0, min(max, len(resp.Results)))
	seen := make(map[string]struct{}, len(resp.Results))
	for _, raw := range resp.Results {
		if len(results) >= max {
			break
		}
		u, ok := searchResultURL(raw.URL)
		if !ok {
			continue
		}
		key := u.String()
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		item := searchResult{
			Title:   sanitizeSearchText(raw.Title),
			URL:     key,
			Snippet: sanitizeSearchSnippet(raw.Content),
		}
		if date, ok := parseSearchDate(raw.PublishedDate); ok {
			item.Published = date
		}
		results = append(results, item)
	}
	return results
}

// searchResultURL accepts only absolute http(s) result URLs with a host and no
// userinfo, and strips the fragment used for deduplication.
func searchResultURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false
	}
	if u.User != nil {
		return nil, false
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u, true
}

// sanitizeSearchText turns model-visible snippet/title text into one safe,
// plain line: valid UTF-8, decoded entities, no tags, no control characters,
// and collapsed whitespace.
func sanitizeSearchText(s string) string {
	s = sanitizeUTF8(s)
	s = html.UnescapeString(s)
	s = stripTags(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func sanitizeSearchSnippet(s string) string {
	return truncateRunes(sanitizeSearchText(s), maxSearchSnippetRunes)
}

// stripTags removes simple HTML tags while leaving a lone '<' that has no
// closing '>' in place. It is deliberately not an HTML parser: snippets only
// need a bounded plain-text rendering.
func stripTags(s string) string {
	if !strings.ContainsRune(s, '<') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '<' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		i += end + 1
	}
	return b.String()
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == max {
			return s[:i]
		}
		count++
	}
	return s
}

var searchDateLayouts = []string{
	"2006-01-02",
	"2006-01-02T15:04:05",
	time.RFC3339,
	time.RFC3339Nano,
	time.RFC1123Z,
	time.RFC1123,
	"2006-01-02 15:04:05",
}

func parseSearchDate(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	for _, layout := range searchDateLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.Format("2006-01-02"), true
		}
	}
	return "", false
}

// searchUnresponsive renders the engine warnings as one capped line. The
// details list contains only the entries that fit the line, so the result
// details stay bounded by the same 200-character policy.
func searchUnresponsive(entries [][]string) (string, []string) {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		name := sanitizeSearchText(entry[0])
		if name == "" {
			continue
		}
		part := name
		if len(entry) > 1 {
			if reason := sanitizeSearchText(entry[1]); reason != "" {
				part = name + ": " + reason
			}
		}
		parts = append(parts, part)
	}

	var warning strings.Builder
	details := make([]string, 0, len(parts))
	used := 0
	for _, part := range parts {
		candidate := part
		if used == 0 {
			candidate = truncateRunes(candidate, maxUnresponsiveRunes)
		}
		width := utf8.RuneCountInString(candidate)
		if used > 0 {
			width += 2 // ", "
		}
		if used+width > maxUnresponsiveRunes {
			break
		}
		if used > 0 {
			warning.WriteString(", ")
		}
		warning.WriteString(candidate)
		details = append(details, candidate)
		used += width
	}
	return warning.String(), details
}

// renderSearchResults builds the model-visible output. The header counts only
// the results actually delivered: when the byte cap drops a result, the count
// is reduced and an explicit truncation marker is appended.
func renderSearchResults(query string, results []searchResult, warning string) (string, bool, int) {
	tail := ""
	if warning != "" {
		tail = "[warning: " + warning + "]\n"
	}
	if len(results) == 0 {
		return fmt.Sprintf("No results for %q.\n%s", query, tail), false, 0
	}

	blocks := make([]string, len(results))
	for i, result := range results {
		blocks[i] = renderSearchResult(i+1, result)
	}
	marker := fmt.Sprintf("[output truncated at %d bytes]\n", maxSearchOutputBytes)

	for count := len(results); count >= 0; count-- {
		var b strings.Builder
		fmt.Fprintf(&b, "Search results for %q (%d):\n", query, count)
		for i := range count {
			b.WriteString(blocks[i])
		}
		b.WriteString(tail)
		truncated := count < len(results)
		if truncated {
			b.WriteString(marker)
		}
		if b.Len() <= maxSearchOutputBytes {
			return b.String(), truncated, count
		}
	}
	// The header, warning, and marker cannot exceed the cap for a valid query,
	// but keep a bounded fallback instead of returning an oversized result.
	out, _ := capHeadOutput(fmt.Sprintf("Search results for %q (0):\n%s%s", query, tail, marker), maxSearchOutputBytes)
	return out, true, 0
}

func renderSearchResult(n int, result searchResult) string {
	title := result.Title
	if title == "" {
		title = "(untitled)"
	}
	snippet := result.Snippet
	if snippet == "" {
		snippet = "(no snippet)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d. %s\n", n, title)
	fmt.Fprintf(&b, "   %s\n", result.URL)
	fmt.Fprintf(&b, "   %s\n", snippet)
	if result.Published != "" {
		fmt.Fprintf(&b, "   Published: %s\n", result.Published)
	}
	return b.String()
}
