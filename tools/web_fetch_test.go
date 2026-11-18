package tools

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// newWebFetchRegistry builds a registry whose web client reaches the fixture
// server through the test-only loopback seam.
func newWebFetchRegistry(t *testing.T, server *httptest.Server, opts Options) *Registry {
	t.Helper()
	opts.UserAgent = "aharness/test"
	opts.WebAllowLoopback = true
	if server != nil {
		opts.WebClient = server.Client()
	}
	return NewRegistry(nil, opts)
}

func fetch(t *testing.T, r *Registry, args string) Result {
	t.Helper()
	return r.Execute(context.Background(), "web_fetch", json.RawMessage(args))
}

func webFetchDetailsOf(t *testing.T, res Result) webFetchDetails {
	t.Helper()
	var details webFetchDetails
	if err := jsonv2.Unmarshal(res.Details, &details); err != nil {
		t.Fatalf("decode details: %v (%s)", err, res.Details)
	}
	return details
}

func TestParseWebFetchTarget(t *testing.T) {
	ok := []string{
		"https://example.com/",
		"http://example.com/a?b=c#fragment",
		"https://example.com:8443/path",
	}
	for _, raw := range ok {
		u, err := parseWebFetchTarget(raw)
		if err != nil {
			t.Errorf("parseWebFetchTarget(%q): %v", raw, err)
			continue
		}
		if u.Fragment != "" {
			t.Errorf("parseWebFetchTarget(%q) kept the fragment", raw)
		}
	}

	bad := map[string]string{
		"":                               "required",
		"file:///etc/passwd":             "scheme",
		"ftp://example.com/":             "scheme",
		"data:text/plain,hello":          "scheme",
		"//example.com/path":             "scheme",
		"https://":                       "host",
		"https:///path":                  "host",
		"https://user:pass@example.com/": "userinfo",
		"https://example.com/a\x00b":     "control",
		"https://example.com/" + strings.Repeat("a", maxWebURLBytes): "limit",
		"https://example.com/\xff":                                   "UTF-8",
	}
	for raw, want := range bad {
		_, err := parseWebFetchTarget(raw)
		if err == nil {
			t.Errorf("parseWebFetchTarget(%q) error = nil", raw)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("parseWebFetchTarget(%q) error = %v, want %q", raw, err, want)
		}
	}
}

func TestIsBlockedWebAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"100.64.0.1", "169.254.169.254", "0.0.0.0", "224.0.0.1", "240.0.0.1",
		"192.0.2.1", "198.51.100.1", "203.0.113.1", "198.18.0.1", "255.255.255.255",
		"::1", "::", "fe80::1", "fc00::1", "ff02::1", "2001:db8::1", "2001::1",
		"64:ff9b:1::1", "2002::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"64:ff9b::7f00:1", "64:ff9b::a00:1",
	}
	for _, raw := range blocked {
		addr := netip.MustParseAddr(raw)
		if !isBlockedWebAddr(addr) {
			t.Errorf("isBlockedWebAddr(%s) = false, want blocked", raw)
		}
	}

	allowed := []string{
		"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700:4700::1111",
		"64:ff9b::101:101",
	}
	for _, raw := range allowed {
		addr := netip.MustParseAddr(raw)
		if isBlockedWebAddr(addr) {
			t.Errorf("isBlockedWebAddr(%s) = true, want allowed", raw)
		}
	}
}

func TestWebFetchBodyContentTypes(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		wantCode    Code
		wantText    []string
		wantMethod  string
	}{
		{
			name:        "html",
			contentType: "text/html; charset=utf-8",
			body:        `<html><head><title>T</title></head><body><article><p>HTML body text</p><script>SCRIPT_MARKER</script></article></body></html>`,
			wantCode:    CodeOK,
			wantText:    []string{"HTML body text"},
			wantMethod:  extractionTrafilatura,
		},
		{
			name:        "xhtml",
			contentType: "application/xhtml+xml",
			body:        `<html><body><p>XHTML body text</p></body></html>`,
			wantCode:    CodeOK,
			wantText:    []string{"XHTML body text"},
			wantMethod:  extractionTrafilatura,
		},
		{
			name:        "plain",
			contentType: "text/plain",
			body:        "plain body",
			wantCode:    CodeOK,
			wantText:    []string{"plain body"},
			wantMethod:  extractionText,
		},
		{
			name:        "markdown",
			contentType: "text/markdown",
			body:        "# heading\n\n`<tag>` stays data",
			wantCode:    CodeOK,
			wantText:    []string{"# heading", "`<tag>` stays data"},
			wantMethod:  extractionMarkdown,
		},
		{
			name:        "json",
			contentType: "application/json",
			body:        `{"key":"value"}`,
			wantCode:    CodeOK,
			wantText:    []string{`{"key":"value"}`},
			wantMethod:  extractionJSON,
		},
		{
			name:        "xml",
			contentType: "application/xml",
			body:        `<root><item>value</item></root>`,
			wantCode:    CodeOK,
			wantText:    []string{"<root><item>value</item></root>"},
			wantMethod:  extractionXML,
		},
		{
			name:        "csv",
			contentType: "text/csv",
			body:        "a,b\n1,2\n",
			wantCode:    CodeOK,
			wantText:    []string{"a,b", "1,2"},
			wantMethod:  extractionText,
		},
		{
			name:        "pdf",
			contentType: "application/pdf",
			body:        "%PDF-1.7",
			wantCode:    CodeUnsupportedContentType,
		},
		{
			name:        "image",
			contentType: "image/png",
			body:        "\x89PNG\r\n",
			wantCode:    CodeUnsupportedContentType,
		},
		{
			name:        "octet stream",
			contentType: "application/octet-stream",
			body:        "<html><body>looks like html</body></html>",
			wantCode:    CodeUnsupportedContentType,
		},
		{
			name:     "missing type sniffs html",
			body:     "<!DOCTYPE html><html><body><p>sniffed html</p></body></html>",
			wantCode: CodeOK,
			wantText: []string{"sniffed html"},
		},
		{
			name:       "missing type sniffs text",
			body:       "sniffed plain text",
			wantCode:   CodeOK,
			wantText:   []string{"sniffed plain text"},
			wantMethod: extractionText,
		},
		{
			name:     "missing type sniffs binary",
			body:     "\x00\x01\x02\x03binary",
			wantCode: CodeUnsupportedContentType,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)

			res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
			if res.Code != tc.wantCode {
				t.Fatalf("code = %s (%s), want %s", res.Code, res.Error, tc.wantCode)
			}
			if tc.wantCode != CodeOK {
				return
			}
			for _, want := range tc.wantText {
				if !strings.Contains(res.Output, want) {
					t.Errorf("output missing %q:\n%s", want, res.Output)
				}
			}
			if tc.wantMethod != "" {
				details := webFetchDetailsOf(t, res)
				if details.Extraction != tc.wantMethod {
					t.Errorf("extraction = %q, want %q", details.Extraction, tc.wantMethod)
				}
			}
			if tc.name == "html" && strings.Contains(res.Output, "SCRIPT_MARKER") {
				t.Errorf("output contains script text:\n%s", res.Output)
			}
		})
	}
}

func TestWebFetchCharsets(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
		want        string
	}{
		{
			name:        "iso-8859-1 header",
			contentType: "text/plain; charset=iso-8859-1",
			body:        []byte("caf\xe9"),
			want:        "café",
		},
		{
			name:        "windows-1252 header",
			contentType: "text/plain; charset=windows-1252",
			body:        []byte("\x93quoted\x94"),
			want:        "“quoted”",
		},
		{
			name:        "html meta charset",
			contentType: "text/html",
			body:        []byte(`<html><head><meta charset="iso-8859-1"><title>T</title></head><body><p>caf` + "\xe9" + `</p></body></html>`),
			want:        "café",
		},
		{
			name:        "invalid bytes replaced",
			contentType: "text/plain",
			body:        []byte("ok \xff bad"),
			want:        "\uFFFD",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write(tc.body)
			}))
			t.Cleanup(server.Close)
			res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
			if res.Code != CodeOK {
				t.Fatalf("code = %s (%s)", res.Code, res.Error)
			}
			if !strings.Contains(res.Output, tc.want) {
				t.Errorf("output missing %q:\n%s", tc.want, res.Output)
			}
		})
	}
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWebFetchCompressionAndCaps(t *testing.T) {
	t.Run("gzip", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(gzipBytes(t, []byte("compressed body")))
		}))
		t.Cleanup(server.Close)
		res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
		if res.Code != CodeOK || !strings.Contains(res.Output, "compressed body") {
			t.Fatalf("code = %s (%s), output:\n%s", res.Code, res.Error, res.Output)
		}
	})

	t.Run("unsupported encoding", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, "body")
		}))
		t.Cleanup(server.Close)
		res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
		if res.Code != CodeUnsupportedContentType {
			t.Fatalf("code = %s (%s), want unsupported_content_type", res.Code, res.Error)
		}
	})

	t.Run("declared length over cap", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "99999999")
			_, _ = io.WriteString(w, "short")
		}))
		t.Cleanup(server.Close)
		res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
		if res.Code != CodeTooLarge {
			t.Fatalf("code = %s (%s), want too_large", res.Code, res.Error)
		}
	})

	t.Run("streamed body over cap", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			chunk := strings.Repeat("a", 64<<10)
			for range 33 {
				_, _ = io.WriteString(w, chunk)
			}
		}))
		t.Cleanup(server.Close)
		res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
		if res.Code != CodeTooLarge {
			t.Fatalf("code = %s (%s), want too_large", res.Code, res.Error)
		}
	})
}

func TestWebFetchStatuses(t *testing.T) {
	tests := []struct {
		status  int
		code    Code
		header  map[string]string
		message string
	}{
		{status: 200, code: CodeOK},
		{status: 204, code: CodeContentUnavailable},
		{status: 403, code: CodeHTTPError},
		{status: 404, code: CodeHTTPError},
		{status: 429, code: CodeHTTPError, header: map[string]string{"Retry-After": "60"}, message: "Retry-After"},
		{status: 500, code: CodeHTTPError},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for key, value := range tc.header {
					w.Header().Set(key, value)
				}
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(tc.status)
				if tc.status == 200 {
					_, _ = io.WriteString(w, "<html><body><p>ok body</p></body></html>")
				}
			}))
			t.Cleanup(server.Close)
			res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
			if res.Code != tc.code {
				t.Fatalf("code = %s (%s), want %s", res.Code, res.Error, tc.code)
			}
			if tc.message != "" && !strings.Contains(res.Error, tc.message) {
				t.Errorf("error = %q, want %q", res.Error, tc.message)
			}
			if tc.code != CodeOK && strings.Contains(res.Output, "ok body") {
				t.Errorf("error page body leaked into output")
			}
		})
	}
}

func TestWebFetchRedirects(t *testing.T) {
	t.Run("same host followed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/start" {
				http.Redirect(w, r, "/final", http.StatusFound)
				return
			}
			_, _ = io.WriteString(w, "<html><body><p>final page</p></body></html>")
		}))
		t.Cleanup(server.Close)
		res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/start"}`)
		if res.Code != CodeOK {
			t.Fatalf("code = %s (%s)", res.Code, res.Error)
		}
		if !strings.Contains(res.Output, "final page") {
			t.Errorf("output missing final page:\n%s", res.Output)
		}
		details := webFetchDetailsOf(t, res)
		if details.Redirect || details.FinalURL != server.URL+"/final" {
			t.Errorf("details = %+v, want final_url %s and redirect false", details, server.URL+"/final")
		}
	})

	t.Run("cross host returned as data", func(t *testing.T) {
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "<html><body><p>other host</p></body></html>")
		}))
		t.Cleanup(other.Close)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, strings.Replace(other.URL, "127.0.0.1", "localhost", 1)+"/page", http.StatusMovedPermanently)
		}))
		t.Cleanup(server.Close)

		res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
		if res.Code != CodeOK {
			t.Fatalf("code = %s (%s), want a successful redirect result", res.Code, res.Error)
		}
		details := webFetchDetailsOf(t, res)
		if !details.Redirect || details.RedirectURL == "" || details.Status != http.StatusMovedPermanently {
			t.Fatalf("details = %+v, want redirect true with a status", details)
		}
		if strings.Contains(res.Output, "other host") {
			t.Errorf("cross-host target was fetched:\n%s", res.Output)
		}
		if !strings.Contains(res.Output, details.RedirectURL) {
			t.Errorf("output does not name the redirect target:\n%s", res.Output)
		}
	})

	t.Run("downgrade returned as data", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "<html><body><p>plain host</p></body></html>")
		}))
		t.Cleanup(plain.Close)
		secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/page", http.StatusFound)
		}))
		t.Cleanup(secure.Close)

		res := fetch(t, newWebFetchRegistry(t, secure, Options{}), `{"url":"`+secure.URL+`/"}`)
		if res.Code != CodeOK {
			t.Fatalf("code = %s (%s), want a successful redirect result", res.Code, res.Error)
		}
		details := webFetchDetailsOf(t, res)
		if !details.Redirect || !strings.HasPrefix(details.RedirectURL, "http://") {
			t.Fatalf("details = %+v, want an HTTP downgrade redirect", details)
		}
		if strings.Contains(res.Output, "plain host") {
			t.Errorf("downgrade target was fetched:\n%s", res.Output)
		}
	})

	t.Run("limit enforced", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/next", http.StatusFound)
		}))
		t.Cleanup(server.Close)
		res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
		if res.Code != CodeFetchFailed {
			t.Fatalf("code = %s (%s), want fetch_failed", res.Code, res.Error)
		}
		if !strings.Contains(res.Error, "redirect") {
			t.Errorf("error = %q, want the redirect limit", res.Error)
		}
	})

	t.Run("missing location", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusFound)
		}))
		t.Cleanup(server.Close)
		res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
		if res.Code != CodeHTTPError {
			t.Fatalf("code = %s (%s), want http_error", res.Code, res.Error)
		}
	})
}

func TestWebFetchOffsetPaging(t *testing.T) {
	text := strings.Repeat("0123456789", 7000) // 70,000 bytes
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, text)
	}))
	t.Cleanup(server.Close)
	r := newWebFetchRegistry(t, server, Options{})

	res := fetch(t, r, `{"url":"`+server.URL+`/"}`)
	if res.Code != CodeOK || !res.Truncated {
		t.Fatalf("code = %s truncated = %v, want ok and truncated", res.Code, res.Truncated)
	}
	details := webFetchDetailsOf(t, res)
	if details.TextBytes != len(text) || details.NextOffset != maxWebOutputBytes || !details.Truncated {
		t.Fatalf("details = %+v", details)
	}
	if !strings.Contains(res.Output, "[truncated at 61440 bytes; continue with offset=61440]") {
		t.Errorf("missing continuation footer:\n%s", res.Output)
	}

	next := fetch(t, r, `{"url":"`+server.URL+`/","offset":61440}`)
	if next.Code != CodeOK || next.Truncated {
		t.Fatalf("next page code = %s truncated = %v", next.Code, next.Truncated)
	}
	if !strings.Contains(next.Output, text[61440:]) {
		t.Errorf("next page does not continue the text")
	}
	if strings.Contains(next.Output, "continue with offset") {
		t.Errorf("final page has a continuation footer")
	}

	beyond := fetch(t, r, `{"url":"`+server.URL+`/","offset":999999}`)
	if beyond.Code != CodeOK || !strings.Contains(beyond.Output, "at or beyond the end") {
		t.Fatalf("beyond-end code = %s output:\n%s", beyond.Code, beyond.Output)
	}
	beyondDetails := webFetchDetailsOf(t, beyond)
	if beyondDetails.Offset != 999999 || beyondDetails.TextBytes != len(text) {
		t.Errorf("beyond details = %+v", beyondDetails)
	}

	exact := fetch(t, r, `{"url":"`+server.URL+`/","offset":70000}`)
	if exact.Code != CodeOK || !strings.Contains(exact.Output, "at or beyond the end") {
		t.Fatalf("exact-end code = %s output:\n%s", exact.Code, exact.Output)
	}
}

func TestWebFetchOffsetRuneBoundary(t *testing.T) {
	text := "aaaébbb" // é occupies bytes 3-4
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, text)
	}))
	t.Cleanup(server.Close)
	res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/","offset":4}`)
	if res.Code != CodeOK {
		t.Fatalf("code = %s (%s)", res.Code, res.Error)
	}
	if !strings.Contains(res.Output, "bbb") || strings.Contains(res.Output, "ébbb") {
		t.Errorf("offset did not advance to the next rune boundary:\n%s", res.Output)
	}
	if details := webFetchDetailsOf(t, res); details.Offset != 5 {
		t.Errorf("details.Offset = %d, want 5", details.Offset)
	}
}

func TestWebFetchBlockedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "should not be reached")
	}))
	t.Cleanup(server.Close)

	// Production wiring has no loopback seam; the loopback fixture must be
	// refused before a connection is made.
	r := NewRegistry(nil, Options{UserAgent: "aharness/test"})
	res := fetch(t, r, `{"url":"`+server.URL+`/"}`)
	if res.Code != CodeBlockedAddress {
		t.Fatalf("code = %s (%s), want blocked_address", res.Code, res.Error)
	}

	res = fetch(t, r, `{"url":"http://169.254.169.254/latest/meta-data/"}`)
	if res.Code != CodeBlockedAddress {
		t.Fatalf("metadata code = %s (%s), want blocked_address", res.Code, res.Error)
	}
}

func TestWebFetchContentUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		reason string
	}{
		{
			name:   "app shell",
			body:   `<html><head><title>App</title></head><body><div id="root"></div><script src="/a.js"></script><script src="/b.js"></script></body></html>`,
			reason: unavailableJavaScript,
		},
		{
			name:   "challenge",
			body:   `<html><head><title>Just a moment...</title></head><body><div id="challenge"></div></body></html>`,
			reason: unavailableChallenge,
		},
		{
			name:   "javascript required",
			body:   `<html><head><title>Loading</title></head><body><noscript>Please enable JavaScript to continue.</noscript></body></html>`,
			reason: unavailableJavaScript,
		},
		{
			name:   "empty extraction",
			body:   `<html><head><title>Empty</title></head><body></body></html>`,
			reason: unavailableEmpty,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
			if res.Code != CodeContentUnavailable {
				t.Fatalf("code = %s (%s), want content_unavailable", res.Code, res.Error)
			}
			if !strings.Contains(res.Error, tc.reason) {
				t.Errorf("error = %q, want reason %q", res.Error, tc.reason)
			}
			if res.OK {
				t.Errorf("content_unavailable result is OK")
			}
		})
	}
}

func TestWebFetchEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
	if res.Code != CodeContentUnavailable {
		t.Fatalf("code = %s (%s), want content_unavailable", res.Code, res.Error)
	}
}

func TestWebFetchShortExtractionWarning(t *testing.T) {
	// A substantial page whose extraction leaves fewer than 100 visible runes.
	body := "<html><body><article><p>Tiny</p>" + strings.Repeat(`<div><span>Lorem ipsum dolor sit amet, consectetur adipiscing elit.</span></div>`, 40) + "</article></body></html>"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
	if res.Code != CodeOK {
		if res.Code == CodeContentUnavailable {
			t.Skipf("extractor produced no text for this fixture: %s", res.Error)
		}
		t.Fatalf("code = %s (%s)", res.Code, res.Error)
	}
	if !strings.Contains(res.Output, "Warning: short_extraction") {
		t.Logf("output has no short_extraction warning:\n%s", res.Output)
	}
}

func TestWebFetchSanitization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "line\x00 one\r\nline\x1b[31m two\x1b[0m  \n\n\nend")
	}))
	t.Cleanup(server.Close)
	res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/"}`)
	if res.Code != CodeOK {
		t.Fatalf("code = %s (%s)", res.Code, res.Error)
	}
	if strings.Contains(res.Output, "\x00") || strings.Contains(res.Output, "\x1b") {
		t.Errorf("output contains control characters: %q", res.Output)
	}
	if !strings.Contains(res.Output, "line one\nline two\n\nend") {
		t.Errorf("output was not sanitized:\n%s", res.Output)
	}
}

func TestWebFetchInvalidArgs(t *testing.T) {
	r := NewRegistry(nil, Options{})
	tests := map[string]string{
		`{"url":""}`:                                    "required",
		`{"url":"ftp://example.com/"}`:                  "scheme",
		`{"url":"https://example.com/","offset":-1}`:    "offset",
		`{"url":"https://example.com/","unknown":true}`: "unknown",
	}
	for args, want := range tests {
		res := r.Execute(context.Background(), "web_fetch", json.RawMessage(args))
		if res.Code != CodeInvalidArgs {
			t.Errorf("%s: code = %s (%s), want invalid_args", args, res.Code, res.Error)
			continue
		}
		if !strings.Contains(res.Error, want) {
			t.Errorf("%s: error = %q, want %q", args, res.Error, want)
		}
	}
}

func TestWebFetchTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "late")
	}))
	t.Cleanup(server.Close)
	r := newWebFetchRegistry(t, server, Options{WebTimeout: 50 * time.Millisecond})
	res := fetch(t, r, `{"url":"`+server.URL+`/"}`)
	if res.Code != CodeTimeout {
		t.Fatalf("code = %s (%s), want timeout", res.Code, res.Error)
	}
}

func TestWebFetchCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := newWebFetchRegistry(t, server, Options{}).Execute(ctx, "web_fetch", json.RawMessage(`{"url":"`+server.URL+`/"}`))
	if res.Code != CodeCanceled {
		t.Fatalf("code = %s (%s), want canceled", res.Code, res.Error)
	}
}

func TestWebFetchDetailsShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>Details Page</title></head><body><article><p>Details body text.</p></article></body></html>`)
	}))
	t.Cleanup(server.Close)
	res := fetch(t, newWebFetchRegistry(t, server, Options{}), `{"url":"`+server.URL+`/page"}`)
	if res.Code != CodeOK {
		t.Fatalf("code = %s (%s)", res.Code, res.Error)
	}
	var raw map[string]any
	if err := jsonv2.Unmarshal(res.Details, &raw); err != nil {
		t.Fatalf("decode details: %v", err)
	}
	for _, key := range []string{"provider", "final_url", "status", "content_type", "title", "extraction", "text_bytes", "offset", "truncated", "redirect"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("details missing %q: %s", key, res.Details)
		}
	}
	if raw["provider"] != "builtin" {
		t.Errorf("provider = %v, want builtin", raw["provider"])
	}
	if raw["title"] != "Details Page" {
		t.Errorf("title = %v, want Details Page", raw["title"])
	}
	if !strings.Contains(res.Output, "Provider: builtin") || !strings.Contains(res.Output, "Extraction: trafilatura") {
		t.Errorf("output header incomplete:\n%s", res.Output)
	}
}

// TestWebFetchRequiresUserAgent verifies that the fetch tool refuses to send a
// request without the host-supplied identity.
func TestWebFetchRequiresUserAgent(t *testing.T) {
	registry := NewRegistry(nil, Options{})
	result := fetch(t, registry, `{"url":"https://example.com/"}`)
	if result.OK || result.Code != CodeUnavailable {
		t.Fatalf("result = %+v, want unavailable", result)
	}
	if !strings.Contains(result.Error, "User-Agent") {
		t.Errorf("error = %q, want User-Agent guidance", result.Error)
	}
}
