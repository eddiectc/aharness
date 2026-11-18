package tools

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// crawl4AIProvider builds a fake provider that records the request and replies
// with the given status and body.
func crawl4AIProvider(t *testing.T, status int, body string, seen func(*http.Request, []byte)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
		}
		if seen != nil {
			seen(r, data)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func crawl4AIRegistry(providerURL string) *Registry {
	return NewRegistry(nil, Options{
		UserAgent:        "aharness/test",
		WebFetchProvider: WebFetchProviderCrawl4AI,
		WebFetchURL:      providerURL,
		WebFetchKey:      "provider-secret",
	})
}

func TestCrawl4AIRequestAndMapping(t *testing.T) {
	envelope := `{
		"success": true,
		"results": [{
			"success": true,
			"html": "<html><head><title>Rendered Page</title></head><body><article><p>Rendered body text.</p></article></body></html>",
			"cleaned_html": "<html>cleaned copy that must be skipped</html>",
			"markdown": "markdown copy that must be skipped",
			"status_code": 201,
			"redirected_url": "https://example.com/rendered",
			"response_headers": {"Content-Type": "text/html; charset=utf-8", "x-other": "ignored"},
			"error_message": ""
		}]
	}`
	server := crawl4AIProvider(t, http.StatusOK, envelope, func(r *http.Request, body []byte) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/crawl" {
			t.Errorf("path = %s, want /crawl", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer provider-secret" {
			t.Errorf("authorization = %q", got)
		}
		var request struct {
			URLs          []string `json:"urls"`
			CrawlerConfig struct {
				Type   string `json:"type"`
				Params struct {
					CacheMode      string `json:"cache_mode"`
					CheckRobotsTxt bool   `json:"check_robots_txt"`
				} `json:"params"`
			} `json:"crawler_config"`
		}
		if err := jsonv2.Unmarshal(body, &request); err != nil {
			t.Fatalf("decode request: %v (%s)", err, body)
		}
		if len(request.URLs) != 1 || request.URLs[0] != "https://example.com/start" {
			t.Errorf("urls = %v", request.URLs)
		}
		if request.CrawlerConfig.Type != "CrawlerRunConfig" {
			t.Errorf("crawler config type = %q", request.CrawlerConfig.Type)
		}
		if request.CrawlerConfig.Params.CacheMode != "disabled" {
			t.Errorf("cache_mode = %q, want disabled", request.CrawlerConfig.Params.CacheMode)
		}
		if request.CrawlerConfig.Params.CheckRobotsTxt {
			t.Errorf("check_robots_txt = true, want false")
		}
	})

	res := crawl4AIRegistry(server.URL).Execute(context.Background(), "web_fetch",
		json.RawMessage(`{"url":"https://example.com/start"}`))
	if res.Code != CodeOK {
		t.Fatalf("code = %s (%s)", res.Code, res.Error)
	}
	for _, want := range []string{"Rendered body text.", "Provider: crawl4ai", "Status: 201", "https://example.com/rendered", "Title: Rendered Page"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output missing %q:\n%s", want, res.Output)
		}
	}
	for _, unwanted := range []string{"cleaned copy", "markdown copy"} {
		if strings.Contains(res.Output, unwanted) {
			t.Errorf("output used the skipped field %q", unwanted)
		}
	}
	details := webFetchDetailsOf(t, res)
	if details.Provider != "crawl4ai" || details.Status != 201 || details.ContentType != "text/html; charset=utf-8" ||
		details.FinalURL != "https://example.com/rendered" {
		t.Errorf("details = %+v", details)
	}
}

func TestCrawl4AIResponseSkippingLargeFields(t *testing.T) {
	// A large field before the html/markdown fields must be skipped, not
	// decoded, and must not affect the result.
	large := strings.Repeat("x", 512<<10)
	envelope := `{"success":true,"screenshot":"` + large + `","results":[{"success":true,"markdown":"` + large + `","html":"<html><body><p>Skipped-field body.</p></body></html>"}]}`
	server := crawl4AIProvider(t, http.StatusOK, envelope, nil)
	res := crawl4AIRegistry(server.URL).Execute(context.Background(), "web_fetch",
		json.RawMessage(`{"url":"https://example.com/"}`))
	if res.Code != CodeOK {
		t.Fatalf("code = %s (%s)", res.Code, res.Error)
	}
	if !strings.Contains(res.Output, "Skipped-field body.") {
		t.Errorf("output missing the rendered body:\n%s", res.Output)
	}
}

func TestCrawl4AIErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		code   Code
		want   string
	}{
		{
			name:   "success false",
			status: http.StatusOK,
			body:   `{"success":false,"error_message":"render failed"}`,
			code:   CodeFetchFailed,
			want:   "render failed",
		},
		{
			name:   "result success false",
			status: http.StatusOK,
			body:   `{"success":true,"results":[{"success":false,"error_message":"target refused"}]}`,
			code:   CodeFetchFailed,
			want:   "target refused",
		},
		{
			name:   "empty html",
			status: http.StatusOK,
			body:   `{"success":true,"results":[{"success":true,"html":""}]}`,
			code:   CodeContentUnavailable,
			want:   unavailableEmpty,
		},
		{
			name:   "no results",
			status: http.StatusOK,
			body:   `{"success":true,"results":[]}`,
			code:   CodeFetchFailed,
			want:   "no results",
		},
		{
			name:   "unauthorized",
			status: http.StatusUnauthorized,
			body:   `{"detail":"bad token"}`,
			code:   CodeFetchFailed,
			want:   "rejected",
		},
		{
			name:   "rate limited",
			status: http.StatusTooManyRequests,
			body:   `{"detail":"slow down"}`,
			code:   CodeFetchFailed,
			want:   "rate limiting",
		},
		{
			name:   "gateway timeout",
			status: http.StatusGatewayTimeout,
			body:   `{"detail":"timeout"}`,
			code:   CodeTimeout,
		},
		{
			name:   "server error",
			status: http.StatusInternalServerError,
			body:   `{"detail":"boom"}`,
			code:   CodeFetchFailed,
		},
		{
			name:   "invalid json",
			status: http.StatusOK,
			body:   `{not json`,
			code:   CodeFetchFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := crawl4AIProvider(t, tc.status, tc.body, nil)
			res := crawl4AIRegistry(server.URL).Execute(context.Background(), "web_fetch",
				json.RawMessage(`{"url":"https://example.com/"}`))
			if res.Code != tc.code {
				t.Fatalf("code = %s (%s), want %s", res.Code, res.Error, tc.code)
			}
			if tc.want != "" && !strings.Contains(res.Error, tc.want) {
				t.Errorf("error = %q, want %q", res.Error, tc.want)
			}
		})
	}
}

func TestCrawl4AIUnreachableDoesNotFallBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	res := crawl4AIRegistry(url).Execute(context.Background(), "web_fetch",
		json.RawMessage(`{"url":"https://example.com/"}`))
	if res.Code != CodeFetchFailed {
		t.Fatalf("code = %s (%s), want fetch_failed", res.Code, res.Error)
	}
	if !strings.Contains(res.Error, "provider") {
		t.Errorf("error = %q, want the provider host", res.Error)
	}
}

func TestCrawl4AIHTMLCap(t *testing.T) {
	html := strings.Repeat("a", maxWebBodyBytes+1)
	envelope := `{"success":true,"results":[{"success":true,"html":"` + html + `"}]}`
	server := crawl4AIProvider(t, http.StatusOK, envelope, nil)
	res := crawl4AIRegistry(server.URL).Execute(context.Background(), "web_fetch",
		json.RawMessage(`{"url":"https://example.com/"}`))
	if res.Code != CodeTooLarge {
		t.Fatalf("code = %s (%s), want too_large", res.Code, res.Error)
	}
}

func TestCrawl4AIProviderResponseCap(t *testing.T) {
	large := strings.Repeat("x", maxWebProviderBytes+1)
	server := crawl4AIProvider(t, http.StatusOK, `{"success":true,"results":[{"html":"`+large+`"}]}`, nil)
	res := crawl4AIRegistry(server.URL).Execute(context.Background(), "web_fetch",
		json.RawMessage(`{"url":"https://example.com/"}`))
	if res.Code != CodeTooLarge {
		t.Fatalf("code = %s (%s), want too_large", res.Code, res.Error)
	}
}

func TestCrawl4AIProviderConfigValidation(t *testing.T) {
	tests := map[string]Options{
		"missing url":  {WebFetchProvider: WebFetchProviderCrawl4AI},
		"builtin url":  {WebFetchProvider: WebFetchProviderBuiltin, WebFetchURL: "http://127.0.0.1:1/"},
		"unknown type": {WebFetchProvider: "firecrawl"},
		"bad key":      {WebFetchProvider: WebFetchProviderCrawl4AI, WebFetchURL: "http://127.0.0.1:1/", WebFetchKey: "bad\nkey"},
	}
	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			res := NewRegistry(nil, opts).Execute(context.Background(), "web_fetch",
				json.RawMessage(`{"url":"https://example.com/"}`))
			if res.Code != CodeUnavailable {
				t.Fatalf("code = %s (%s), want unavailable", res.Code, res.Error)
			}
		})
	}
}

func TestParseWebFetchProviderAndURL(t *testing.T) {
	if got, err := ParseWebFetchProvider(""); err != nil || got != WebFetchProviderBuiltin {
		t.Errorf("empty provider = %q, %v", got, err)
	}
	if _, err := ParseWebFetchProvider("jina"); err == nil {
		t.Errorf("unknown provider accepted")
	}
	if got, want := mustFetchURL(t, "http://127.0.0.1:11235/base/"), "http://127.0.0.1:11235/base"; got != want {
		t.Errorf("normalized URL = %q, want %q", got, want)
	}
	for _, raw := range []string{"ftp://x/", "http://user:pass@x/", "http://x/#frag"} {
		if _, err := ParseWebFetchURL(raw); err == nil {
			t.Errorf("ParseWebFetchURL(%q) error = nil", raw)
		}
	}
	if err := ValidateWebFetchConfig(WebFetchProviderBuiltin, "", ""); err != nil {
		t.Errorf("builtin config: %v", err)
	}
	if err := ValidateWebFetchConfig(WebFetchProviderCrawl4AI, "http://127.0.0.1:11235", ""); err != nil {
		t.Errorf("crawl4ai config: %v", err)
	}
}

func mustFetchURL(t *testing.T, raw string) string {
	t.Helper()
	got, err := ParseWebFetchURL(raw)
	if err != nil {
		t.Fatalf("ParseWebFetchURL(%q): %v", raw, err)
	}
	return got
}
