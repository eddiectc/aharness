package tools

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// newSearchServer starts a fake SearXNG endpoint and closes it with the test.
func newSearchServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func newSearchRegistry(t *testing.T, baseURL string) *Registry {
	t.Helper()
	return NewRegistry(nil, Options{SearXNGURL: baseURL, UserAgent: "aharness/test"})
}

func decodeDetails(t *testing.T, result Result) webSearchDetails {
	t.Helper()
	var details webSearchDetails
	if err := jsonv2.Unmarshal(result.Details, &details); err != nil {
		t.Fatalf("decode details %q: %v", result.Details, err)
	}
	return details
}

func TestWebSearchRequestAndOutput(t *testing.T) {
	requests := make(chan *http.Request, 1)
	server := newSearchServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"results": [
				{"title": "One <b>x</b> &amp; y", "url": "https://example.com/a#frag", "content": "<p>Hello&nbsp;world</p>", "publishedDate": "2024-01-02T03:04:05Z", "score": 1, "unknown": true},
				{"title": "Duplicate", "url": "https://example.com/a", "content": "dup"},
				{"title": "No snippet", "url": "https://example.com/b", "content": ""},
				{"title": "FTP", "url": "ftp://example.com/c", "content": "no"},
				{"title": "No hostname", "url": "https://:8080/d", "content": "no"},
				{"title": "Userinfo", "url": "https://user@example.com/d", "content": "no"},
				{"title": "Three", "url": "https://example.com/c", "content": "line1\nline2\u0001"}
			],
			"unresponsive_engines": [["google", "timeout"], ["", ""]],
			"unknown_key": {"x": 1}
		}`)
	})
	registry := newSearchRegistry(t, server.URL+"/searxng/")

	result := executeArgs(t, registry, "web_search", map[string]any{
		"query":      "go & harness",
		"categories": "general,news",
		"language":   "en-US",
		"time_range": "month",
	})
	if !result.OK || result.Code != CodeOK {
		t.Fatalf("result = %+v, want ok", result)
	}
	if result.Truncated {
		t.Errorf("Truncated = true, want false")
	}

	var request *http.Request
	select {
	case request = <-requests:
	default:
		t.Fatal("SearXNG received no request")
	}
	if request.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", request.Method)
	}
	if request.URL.Path != "/searxng/search" {
		t.Errorf("path = %q, want the configured base path plus /search", request.URL.Path)
	}
	query := request.URL.Query()
	for name, want := range map[string]string{
		"q":          "go & harness",
		"format":     "json",
		"pageno":     "1",
		"categories": "general,news",
		"language":   "en-US",
		"time_range": "month",
	} {
		if got := query.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := request.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
	if got := request.Header.Get("User-Agent"); got != "aharness/test" {
		t.Errorf("User-Agent = %q", got)
	}

	for _, want := range []string{
		`Search results for "go & harness" (3):`,
		"1. One x & y\n",
		"   https://example.com/a\n",
		"   Hello world\n",
		"   Published: 2024-01-02\n",
		"2. No snippet\n",
		"   (no snippet)\n",
		"3. Three\n",
		"   line1 line2\n",
		"[warning: google: timeout]\n",
	} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output missing %q:\n%s", want, result.Output)
		}
	}
	if strings.Contains(result.Output, "#frag") || strings.Contains(result.Output, "ftp://") || strings.Contains(result.Output, "user@") || strings.Contains(result.Output, ":8080") {
		t.Errorf("output retained a filtered or fragment URL:\n%s", result.Output)
	}

	details := decodeDetails(t, result)
	if details.Results != 3 || details.Truncated {
		t.Errorf("details = %+v, want 3 results and no truncation", details)
	}
	if strings.Join(details.Unresponsive, ",") != "google: timeout" {
		t.Errorf("details.unresponsive = %v", details.Unresponsive)
	}
}

func TestWebSearchResultCapAndDedupe(t *testing.T) {
	results := make([]map[string]any, 0, 24)
	for i := range 12 {
		result := map[string]any{
			"title":   fmt.Sprintf("Result %d", i),
			"url":     fmt.Sprintf("https://example.com/%d", i),
			"content": "snippet",
		}
		results = append(results, result, result)
	}
	body, err := jsonv2.Marshal(map[string]any{"results": results})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	server := newSearchServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	registry := newSearchRegistry(t, server.URL)

	tests := []struct {
		name string
		args map[string]any
		want int
	}{
		{"default", map[string]any{"query": "q"}, 5},
		{"explicit", map[string]any{"query": "q", "max_results": 2}, 2},
		{"capped", map[string]any{"query": "q", "max_results": 50}, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := executeArgs(t, registry, "web_search", tc.args)
			if !result.OK {
				t.Fatalf("result = %+v", result)
			}
			details := decodeDetails(t, result)
			if details.Results != tc.want {
				t.Errorf("results = %d, want %d (dedupe before cap)", details.Results, tc.want)
			}
			if got := strings.Count(result.Output, "Result "); got != tc.want {
				t.Errorf("rendered %d results, want %d", got, tc.want)
			}
		})
	}
}

func TestWebSearchOutputCap(t *testing.T) {
	hugeTitle := strings.Repeat("h", 70<<10)
	body, err := jsonv2.Marshal(map[string]any{"results": []any{
		map[string]any{"title": "small", "url": "https://example.com/small", "content": "ok"},
		map[string]any{"title": hugeTitle, "url": "https://example.com/huge", "content": "big"},
	}})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	server := newSearchServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	registry := newSearchRegistry(t, server.URL)

	result := executeArgs(t, registry, "web_search", map[string]any{"query": "cap"})
	if !result.OK {
		t.Fatalf("result = %+v", result)
	}
	if !result.Truncated {
		t.Errorf("Truncated = false, want true")
	}
	if len(result.Output) > maxSearchOutputBytes {
		t.Errorf("output = %d bytes, want <= %d", len(result.Output), maxSearchOutputBytes)
	}
	if !strings.Contains(result.Output, "[output truncated at 61440 bytes]") {
		t.Errorf("output missing truncation marker:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "https://example.com/small") {
		t.Errorf("output dropped the result that still fits:\n%s", result.Output)
	}
	if strings.Contains(result.Output, "https://example.com/huge") {
		t.Errorf("output retained the result that did not fit")
	}
	details := decodeDetails(t, result)
	if details.Results != 1 || !details.Truncated {
		t.Errorf("details = %+v, want one delivered result and truncation", details)
	}
}

func TestWebSearchNoResultsAndWarnings(t *testing.T) {
	server := newSearchServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results": [], "unresponsive_engines": [["google", "timeout"], ["bing", "rate limited"]]}`)
	})
	registry := newSearchRegistry(t, server.URL)

	result := executeArgs(t, registry, "web_search", map[string]any{"query": "absent"})
	if !result.OK || result.Code != CodeOK {
		t.Fatalf("result = %+v, want a successful empty search", result)
	}
	if !strings.Contains(result.Output, `No results for "absent".`) {
		t.Errorf("output missing the empty-result message:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "[warning: google: timeout, bing: rate limited]") {
		t.Errorf("output missing the combined warning:\n%s", result.Output)
	}
	details := decodeDetails(t, result)
	if details.Results != 0 || details.Truncated {
		t.Errorf("details = %+v, want 0 results and no truncation", details)
	}
	if len(details.Unresponsive) != 2 {
		t.Errorf("details.unresponsive = %v, want two entries", details.Unresponsive)
	}
}

func TestSearchUnresponsiveCap(t *testing.T) {
	entries := make([][]string, 0, 40)
	for i := range 40 {
		entries = append(entries, []string{fmt.Sprintf("engine-%02d", i), strings.Repeat("x", 20)})
	}
	warning, details := searchUnresponsive(entries)
	if got := utf8.RuneCountInString(warning); got > maxUnresponsiveRunes {
		t.Errorf("warning = %d runes, want <= %d", got, maxUnresponsiveRunes)
	}
	if len(details) == 0 || len(details) >= len(entries) {
		t.Errorf("details = %d entries, want a bounded non-empty prefix", len(details))
	}
	for _, detail := range details {
		if !strings.Contains(warning, detail) {
			t.Errorf("detail %q is not in the warning %q", detail, warning)
		}
	}
}

func TestSanitizeSearchText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"entities and tags", "a &amp; b <i>c</i>", "a & b c"},
		{"control characters", "a\x00b\nc\td", "a b c d"},
		{"lone angle bracket", "1 < 2", "1 < 2"},
		{"non-breaking space", "a&nbsp;b", "a b"},
		{"invalid utf8", "a\xffb", "a\uFFFDb"},
		{"script tags", "<script>alert(1)</script>text", "alert(1)text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeSearchText(tc.in); got != tc.want {
				t.Errorf("sanitizeSearchText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := utf8.RuneCountInString(sanitizeSearchSnippet(strings.Repeat("é", maxSearchSnippetRunes+100))); got != maxSearchSnippetRunes {
		t.Errorf("snippet runes = %d, want %d", got, maxSearchSnippetRunes)
	}
}

func TestWebSearchErrors(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		registry := NewRegistry(nil, Options{})
		result := executeArgs(t, registry, "web_search", map[string]any{"query": "q"})
		if result.OK || result.Code != CodeUnavailable {
			t.Fatalf("result = %+v, want unavailable", result)
		}
		if !strings.Contains(result.Error, "-searxng-url") || !strings.Contains(result.Error, "AHARNESS_SEARXNG_URL") {
			t.Errorf("error = %q, want the actionable setting names", result.Error)
		}
	})

	t.Run("json disabled", func(t *testing.T) {
		server := newSearchServer(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		})
		result := executeArgs(t, newSearchRegistry(t, server.URL), "web_search", map[string]any{"query": "q"})
		if result.OK || result.Code != CodeUnavailable {
			t.Fatalf("result = %+v, want unavailable", result)
		}
		if !strings.Contains(result.Error, "json") || !strings.Contains(result.Error, "search.formats") {
			t.Errorf("error = %q, want JSON-format guidance", result.Error)
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := newSearchServer(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})
		result := executeArgs(t, newSearchRegistry(t, server.URL), "web_search", map[string]any{"query": "q"})
		if result.OK || result.Code != CodeHTTPError {
			t.Fatalf("result = %+v, want http_error", result)
		}
		if !strings.Contains(result.Error, "500") {
			t.Errorf("error = %q, want the HTTP status", result.Error)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		server := newSearchServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"results": [`)
		})
		result := executeArgs(t, newSearchRegistry(t, server.URL), "web_search", map[string]any{"query": "q"})
		if result.OK || result.Code != CodeFetchFailed {
			t.Fatalf("result = %+v, want fetch_failed", result)
		}
		if !strings.Contains(result.Error, "decode") {
			t.Errorf("error = %q, want a decode error", result.Error)
		}
	})

	t.Run("oversized response", func(t *testing.T) {
		server := newSearchServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(make([]byte, maxSearchResponseBytes+1))
		})
		result := executeArgs(t, newSearchRegistry(t, server.URL), "web_search", map[string]any{"query": "q"})
		if result.OK || result.Code != CodeTooLarge {
			t.Fatalf("result = %+v, want too_large", result)
		}
		if !strings.Contains(result.Error, fmt.Sprint(maxSearchResponseBytes)) {
			t.Errorf("error = %q, want the cap value", result.Error)
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		baseURL := server.URL
		server.Close()
		result := executeArgs(t, newSearchRegistry(t, baseURL), "web_search", map[string]any{"query": "q"})
		if result.OK || result.Code != CodeFetchFailed {
			t.Fatalf("result = %+v, want fetch_failed", result)
		}
		if !strings.Contains(result.Error, "request") {
			t.Errorf("error = %q, want the operation and URL", result.Error)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		server := newSearchServer(t, func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		})
		registry := newSearchRegistry(t, server.URL)
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		result := registry.Execute(ctx, "web_search", jsonRaw(t, map[string]any{"query": "q"}))
		if result.OK || result.Code != CodeTimeout {
			t.Fatalf("result = %+v, want timeout", result)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		registry := newSearchRegistry(t, "http://127.0.0.1:1")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		result := registry.Execute(ctx, "web_search", jsonRaw(t, map[string]any{"query": "q"}))
		if result.OK || result.Code != CodeCanceled {
			t.Fatalf("result = %+v, want canceled", result)
		}
	})
}

func TestWebSearchInvalidArgs(t *testing.T) {
	registry := newSearchRegistry(t, "https://search.example.com")
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"missing query", `{}`, "query"},
		{"blank query", `{"query":"   "}`, "query"},
		{"long query", fmt.Sprintf(`{"query":%q}`, strings.Repeat("q", maxSearchQueryBytes+1)), "query"},
		{"zero results", `{"query":"q","max_results":0}`, "max_results"},
		{"long categories", fmt.Sprintf(`{"query":"q","categories":%q}`, strings.Repeat("c", maxSearchCategoriesBytes+1)), "categories"},
		{"categories control", `{"query":"q","categories":"a\nb"}`, "categories"},
		{"long language", fmt.Sprintf(`{"query":"q","language":%q}`, strings.Repeat("l", maxSearchLanguageBytes+1)), "language"},
		{"language control", `{"query":"q","language":"e\n"}`, "language"},
		{"bad time range", `{"query":"q","time_range":"week"}`, "time_range"},
		{"unknown member", `{"query":"q","unknown":true}`, "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := executeRaw(t, registry, "web_search", tc.raw)
			if result.OK || result.Code != CodeInvalidArgs {
				t.Fatalf("result = %+v, want invalid_args", result)
			}
			if !strings.Contains(result.Error, tc.want) {
				t.Errorf("error = %q, want %q", result.Error, tc.want)
			}
		})
	}
}

func TestParseSearchDate(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"2024-01-02", "2024-01-02", true},
		{"2024-01-02T03:04:05", "2024-01-02", true},
		{"2024-01-02T03:04:05Z", "2024-01-02", true},
		{"2024-01-02T03:04:05+02:00", "2024-01-02", true},
		{"not a date", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, ok := parseSearchDate(tc.raw)
			if got != tc.want || ok != tc.ok {
				t.Errorf("parseSearchDate(%q) = %q, %t; want %q, %t", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseSearXNGURL(t *testing.T) {
	valid := []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"https://search.example.com", "https://search.example.com"},
		{"https://search.example.com/", "https://search.example.com"},
		{"https://search.example.com/searxng/", "https://search.example.com/searxng"},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"http://localhost:8080/", "http://localhost:8080"},
		{"http://[::1]:8080", "http://[::1]:8080"},
	}
	for _, tc := range valid {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseSearXNGURL(tc.raw)
			if err != nil {
				t.Fatalf("ParseSearXNGURL(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ParseSearXNGURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	invalid := []string{
		"ftp://search.example.com",
		"https://",
		"https://:8080",
		"https://user@search.example.com",
		"https://search.example.com/?q=1",
		"https://search.example.com/#frag",
		"http://search.example.com",
		"http://0.0.0.0:8080",
		"https://search.example.com:bad",
		"https://search.example.com/" + strings.Repeat("a", maxSearchURLBytes),
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			if got, err := ParseSearXNGURL(raw); err == nil {
				t.Errorf("ParseSearXNGURL(%q) = %q, want error", raw, got)
			}
		})
	}
}

func jsonRaw(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := jsonv2.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return raw
}

func FuzzSearchJSON(f *testing.F) {
	f.Add([]byte(`{"results":[{"title":"<b>x</b>","url":"https://example.com/a#frag","content":"&amp; snippet","publishedDate":"2024-01-02"}]}`))
	f.Add([]byte(`{"results":[],"unresponsive_engines":[["google","timeout"]]}`))
	f.Add([]byte(`{"results":[{"title":"bad","url":"javascript:alert(1)","content":"x"}]}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		response, err := parseSearXNGResponse(data)
		if err != nil {
			return
		}
		results := collectSearchResults(response, maxSearchResultCount)
		if len(results) > maxSearchResultCount {
			t.Fatalf("results = %d, want <= %d", len(results), maxSearchResultCount)
		}
		for _, result := range results {
			parsed, parseErr := url.Parse(result.URL)
			if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
				t.Fatalf("unsafe result URL %q", result.URL)
			}
			if parsed.Fragment != "" {
				t.Fatalf("result URL %q retained a fragment", result.URL)
			}
			if utf8.RuneCountInString(result.Snippet) > maxSearchSnippetRunes {
				t.Fatalf("snippet runes = %d, want <= %d", utf8.RuneCountInString(result.Snippet), maxSearchSnippetRunes)
			}
			if strings.IndexFunc(result.Snippet, func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' }) >= 0 {
				t.Fatalf("snippet retained control whitespace: %q", result.Snippet)
			}
		}
		warning, details := searchUnresponsive(response.UnresponsiveEngines)
		if utf8.RuneCountInString(warning) > maxUnresponsiveRunes {
			t.Fatalf("warning runes = %d, want <= %d", utf8.RuneCountInString(warning), maxUnresponsiveRunes)
		}
		for _, detail := range details {
			if !strings.Contains(warning, detail) {
				t.Fatalf("detail %q missing from warning %q", detail, warning)
			}
		}
	})
}
