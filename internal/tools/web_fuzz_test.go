package tools

import (
	"net/url"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// FuzzExtractHTML exercises the extraction pipeline: no panic, valid UTF-8, no
// control characters, and bounded output.
func FuzzExtractHTML(f *testing.F) {
	f.Add(articleFixture, "text/html")
	f.Add(`<html><body><script>alert(1)</script><p>text</p></body></html>`, "text/html")
	f.Add(`<html><head><title>Just a moment...</title></head><body></body></html>`, "text/html")
	f.Add("plain text, not html", "text/plain")
	f.Add("", "text/html")

	base := &url.URL{Scheme: "https", Host: "example.com"}
	f.Fuzz(func(t *testing.T, document, contentType string) {
		if len(document) > 512<<10 {
			return
		}
		kind := classifyWebContent(contentType, []byte(document))
		if kind != webContentHTML {
			return
		}
		ext := extractWebHTML(document, base)
		if !utf8.ValidString(ext.Text) {
			t.Fatalf("extraction returned invalid UTF-8")
		}
		if len(ext.Text) > len(document)*4+1024 {
			t.Fatalf("extraction grew unbounded: %d bytes from %d", len(ext.Text), len(document))
		}
		for _, r := range ext.Text {
			if r == 0 || unicode.IsControl(r) && r != '\n' && r != '\t' {
				t.Fatalf("extraction contains control r %U", r)
			}
		}
		if strings.Contains(ext.Text, "\r") {
			t.Fatalf("extraction contains a carriage return")
		}
	})
}

// FuzzWebURL exercises the shared target validator: only absolute http(s) URLs
// with a host, no userinfo, no fragment, and no control characters pass.
func FuzzWebURL(f *testing.F) {
	f.Add("https://example.com/a?b=c#frag")
	f.Add("http://127.0.0.1:8080/")
	f.Add("file:///etc/passwd")
	f.Add("https://user:pass@example.com/")
	f.Add("https://example.com/" + strings.Repeat("a", 9000))
	f.Add("\x00")
	f.Add("")

	f.Fuzz(func(t *testing.T, raw string) {
		u, err := parseWebFetchTarget(raw)
		if err != nil {
			return
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			t.Fatalf("accepted scheme %q", u.Scheme)
		}
		if u.Host == "" || u.Hostname() == "" {
			t.Fatalf("accepted an empty host")
		}
		if u.User != nil {
			t.Fatalf("accepted userinfo")
		}
		if u.Fragment != "" || u.RawFragment != "" {
			t.Fatalf("accepted a fragment")
		}
		if hasControl(raw) {
			t.Fatalf("accepted control characters")
		}
		if len(raw) > maxWebURLBytes {
			t.Fatalf("accepted an oversized URL")
		}
	})
}

// FuzzParseWebFetchURL exercises the provider endpoint validator.
func FuzzParseWebFetchURL(f *testing.F) {
	f.Add("http://127.0.0.1:11235")
	f.Add("https://crawl.example/base/")
	f.Add("ftp://nope/")
	f.Add("http://user@host/")

	f.Fuzz(func(t *testing.T, raw string) {
		normalized, err := ParseWebFetchURL(raw)
		if err != nil || normalized == "" {
			return
		}
		u, err := url.Parse(normalized)
		if err != nil {
			t.Fatalf("normalized URL does not parse: %v", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			t.Fatalf("accepted scheme %q", u.Scheme)
		}
		if u.User != nil || u.Fragment != "" {
			t.Fatalf("accepted userinfo or fragment")
		}
		if strings.HasSuffix(u.Path, "/") {
			t.Fatalf("normalized path keeps a trailing slash")
		}
	})
}
