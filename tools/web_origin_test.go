package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestParseWebAllowedOrigins(t *testing.T) {
	valid := []struct {
		in   string
		want string
	}{
		{"http://127.0.0.1:8888", "http://127.0.0.1:8888"},
		{"HTTP://LocalHost", "http://localhost:80"},
		{"https://example.com/", "https://example.com:443"},
		{"http://[::1]:5173", "http://[::1]:5173"},
		{"https://example.com:8443", "https://example.com:8443"},
	}
	for _, tc := range valid {
		got, err := ParseWebAllowedOrigins([]string{tc.in})
		if err != nil {
			t.Errorf("ParseWebAllowedOrigins(%q): %v", tc.in, err)
			continue
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("ParseWebAllowedOrigins(%q) = %v, want [%s]", tc.in, got, tc.want)
		}
	}

	deduped, err := ParseWebAllowedOrigins([]string{"http://localhost:8888", "http://LOCALHOST:8888/"})
	if err != nil {
		t.Fatalf("ParseWebAllowedOrigins duplicate: %v", err)
	}
	if len(deduped) != 1 {
		t.Fatalf("duplicate origins = %v, want one entry", deduped)
	}

	invalid := []string{
		"",
		"example.com:80",
		"ftp://example.com",
		"https://user:pw@example.com",
		"https://example.com/path",
		"https://example.com/?q=1",
		"https://example.com/#fragment",
		"http://",
		"https://example.com\x00",
		"https://" + strings.Repeat("a", maxWebAllowedOriginSize) + ".example.com",
	}
	for _, raw := range invalid {
		if _, err := ParseWebAllowedOrigins([]string{raw}); err == nil {
			t.Errorf("ParseWebAllowedOrigins(%q) accepted an invalid entry", raw)
		}
	}

	tooMany := make([]string, maxWebAllowedOrigins+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("http://127.0.0.1:%d", 8000+i)
	}
	if _, err := ParseWebAllowedOrigins(tooMany); err == nil {
		t.Error("ParseWebAllowedOrigins accepted too many entries")
	}
}

func TestValidateWebFetchAllowedOrigins(t *testing.T) {
	if err := ValidateWebFetchAllowedOrigins(WebFetchProviderBuiltin, []string{"http://127.0.0.1:8888"}); err != nil {
		t.Errorf("builtin with a valid origin: %v", err)
	}
	if err := ValidateWebFetchAllowedOrigins(WebFetchProviderBuiltin, nil); err != nil {
		t.Errorf("builtin with no origins: %v", err)
	}
	if err := ValidateWebFetchAllowedOrigins(WebFetchProviderCrawl4AI, nil); err != nil {
		t.Errorf("crawl4ai with no origins: %v", err)
	}
	if err := ValidateWebFetchAllowedOrigins(WebFetchProviderCrawl4AI, []string{"http://127.0.0.1:8888"}); err == nil {
		t.Error("crawl4ai accepted an origin list it cannot enforce")
	}
	if err := ValidateWebFetchAllowedOrigins(WebFetchProviderBuiltin, []string{"ftp://example.com"}); err == nil {
		t.Error("builtin accepted an invalid origin")
	}
	if err := ValidateWebFetchAllowedOrigins("bogus", nil); err == nil {
		t.Error("an unknown provider was accepted")
	}
}

func TestWebAddressClassification(t *testing.T) {
	tests := []struct {
		addr       string
		always     bool
		configured bool
	}{
		{"127.0.0.1", false, true},
		{"127.10.0.1", false, true},
		{"::1", false, true},
		{"10.1.2.3", false, true},
		{"172.16.0.1", false, true},
		{"192.168.1.1", false, true},
		{"100.64.0.1", false, true},
		{"fd00::1", false, true},
		{"::ffff:127.0.0.1", false, true},
		{"64:ff9b::7f00:1", false, true},
		{"169.254.169.254", true, false},
		{"0.0.0.0", true, false},
		{"198.18.0.1", true, false},
		{"fe80::1", true, false},
		{"ff02::1", true, false},
		{"2001:db8::1", true, false},
		{"64:ff9b::a9fe:a9fe", true, false},
		{"8.8.8.8", false, false},
		{"2606:4700:4700::1111", false, false},
	}
	for _, tc := range tests {
		addr := netip.MustParseAddr(tc.addr)
		if got := isAlwaysBlockedWebAddr(addr); got != tc.always {
			t.Errorf("isAlwaysBlockedWebAddr(%s) = %v, want %v", tc.addr, got, tc.always)
		}
		if got := isConfigurableBlockedWebAddr(addr); got != tc.configured {
			t.Errorf("isConfigurableBlockedWebAddr(%s) = %v, want %v", tc.addr, got, tc.configured)
		}
		wantBlocked := tc.always || tc.configured
		if got := isBlockedWebAddr(addr); got != wantBlocked {
			t.Errorf("isBlockedWebAddr(%s) = %v, want %v", tc.addr, got, wantBlocked)
		}
	}
}

// originServer serves one plain-text body on a loopback httptest server.
func originServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestWebAllowedOriginPermitsLoopback(t *testing.T) {
	server := originServer(t, "dev server page")
	r := NewRegistry(nil, Options{UserAgent: "aharness/test", WebAllowedOrigins: []string{server.URL}})
	res := fetch(t, r, `{"url":"`+server.URL+`/"}`)
	if !res.OK {
		t.Fatalf("fetch with an allowed origin: code = %s (%s)", res.Code, res.Error)
	}
	if !strings.Contains(res.Output, "dev server page") {
		t.Errorf("output = %q, want the page text", res.Output)
	}
}

func TestWebAllowedOriginRefusesWithoutMatch(t *testing.T) {
	server := originServer(t, "dev server page")
	other := originServer(t, "other server page")
	tests := []struct {
		name    string
		origins []string
		url     string
	}{
		{"no allowlist", nil, server.URL},
		{"different port", []string{other.URL}, server.URL},
		{"different host", []string{"http://localhost:" + portOf(t, server.URL)}, server.URL},
		{"different scheme", []string{strings.Replace(server.URL, "http://", "https://", 1)}, server.URL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry(nil, Options{UserAgent: "aharness/test", WebAllowedOrigins: tc.origins})
			res := fetch(t, r, `{"url":"`+tc.url+`/"}`)
			if res.Code != CodeBlockedAddress {
				t.Fatalf("code = %s (%s), want %s", res.Code, res.Error, CodeBlockedAddress)
			}
			if !strings.Contains(res.Error, "loopback or private") {
				t.Errorf("error = %q, want the allowlist hint", res.Error)
			}
		})
	}
}

func TestWebAllowedOriginDoesNotUnblockMetadata(t *testing.T) {
	const metadata = "http://169.254.169.254/latest/meta-data/"
	r := NewRegistry(nil, Options{UserAgent: "aharness/test", WebAllowedOrigins: []string{"http://169.254.169.254"}})
	res := fetch(t, r, `{"url":"`+metadata+`"}`)
	if res.Code != CodeBlockedAddress {
		t.Fatalf("code = %s (%s), want %s", res.Code, res.Error, CodeBlockedAddress)
	}
	if strings.Contains(res.Error, "allowed origins") {
		t.Errorf("error = %q, must not suggest the allowlist for a metadata address", res.Error)
	}
}

func portOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Port()
}
