package model

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testProvider = "opencode-go"
	testBaseURL  = "https://opencode.ai/inference/go/openai/v1"
)

const replacementCatalog = `{
  "opencode-go": {
    "npm": "@ai-sdk/openai-compatible",
    "api": "https://opencode.ai/inference/go/openai/v1",
    "models": {
      "replacement-model": {
        "name": "Replacement Model",
        "tool_call": true,
        "limit": {"context": 64000},
        "modalities": {"output": ["text"]}
      }
    }
  }
}`

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeCatalogServer struct {
	mu             sync.Mutex
	body           []byte
	status         int
	etag           string
	delay          time.Duration
	requests       int
	ifNoneMatch    []string
	authorizations []string
	userAgents     []string
	accepts        []string
}

func (f *fakeCatalogServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.ifNoneMatch = append(f.ifNoneMatch, r.Header.Get("If-None-Match"))
	f.authorizations = append(f.authorizations, r.Header.Get("Authorization"))
	f.userAgents = append(f.userAgents, r.Header.Get("User-Agent"))
	f.accepts = append(f.accepts, r.Header.Get("Accept"))
	body, status, etag, delay := f.body, f.status, f.etag, f.delay
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusNotModified {
		w.WriteHeader(status)
		return
	}
	w.WriteHeader(status)
	if status == http.StatusOK && len(body) > 0 {
		_, _ = w.Write(body)
	}
}

func (f *fakeCatalogServer) set(body []byte, status int, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body = body
	f.status = status
	f.etag = etag
}

type recordedRequests struct {
	count          int
	ifNoneMatch    []string
	authorizations []string
	userAgents     []string
	accepts        []string
}

func (f *fakeCatalogServer) recorded() recordedRequests {
	f.mu.Lock()
	defer f.mu.Unlock()
	return recordedRequests{
		count:          f.requests,
		ifNoneMatch:    slices.Clone(f.ifNoneMatch),
		authorizations: slices.Clone(f.authorizations),
		userAgents:     slices.Clone(f.userAgents),
		accepts:        slices.Clone(f.accepts),
	}
}

func startCatalogServer(t *testing.T, body []byte) (*fakeCatalogServer, *httptest.Server) {
	t.Helper()
	fake := &fakeCatalogServer{body: body, etag: `"v1"`}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return fake, server
}

func readCatalogFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/models_dev.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func catalogOptions(rawURL, stateDir string, clock *fakeClock) CatalogOptions {
	return CatalogOptions{
		URL:       rawURL,
		StateDir:  stateDir,
		UserAgent: "aharness/test",
		Now:       clock.Now,
		TTL:       CatalogTTL,
		Timeout:   5 * time.Second,
	}
}

func newCatalogSource(t *testing.T, opts CatalogOptions) *ModelsDevSource {
	t.Helper()
	source, err := NewModelsDevSource(opts)
	if err != nil {
		t.Fatalf("NewModelsDevSource: %v", err)
	}
	return source
}

func writeFileMode(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create cache directory: %v", err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func marshalCache(t *testing.T, cached catalogCacheFile) []byte {
	t.Helper()
	data, err := jsonv2.Marshal(cached)
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}
	return data
}

func TestCatalogFixtureNormalization(t *testing.T) {
	fixture := readCatalogFixture(t)
	catalog, err := parseCatalog(fixture, testProvider, DefaultCatalogURL)
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	if catalog.Provider != testProvider {
		t.Errorf("provider = %q, want %q", catalog.Provider, testProvider)
	}
	if catalog.Source != DefaultCatalogURL {
		t.Errorf("source = %q, want %q", catalog.Source, DefaultCatalogURL)
	}

	tests := []struct {
		id   string
		want CatalogModel
	}{
		{
			id: "chat-model",
			want: CatalogModel{
				ID: "chat-model", Name: "Chat Model", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 1000000, OutputLimit: 384000, ToolCall: true, Status: "alpha",
			},
		},
		{
			id: "responses-model",
			want: CatalogModel{
				ID: "responses-model", Name: "Responses Model", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai", NativeDialect: "responses",
				Dialect: DialectResponses, BaseURL: testBaseURL,
				ContextWindow: 1050000, InputLimit: 922000, OutputLimit: 128000, ToolCall: true,
			},
		},
		{
			id: "anthropic-model",
			want: CatalogModel{
				ID: "anthropic-model", Name: "Anthropic Model", Provider: testProvider,
				NativeSDK: "@ai-sdk/anthropic", NativeDialect: "messages",
				Dialect: DialectChat, BaseURL: testBaseURL, Translated: true,
				ContextWindow: 200000, OutputLimit: 64000, ToolCall: true,
			},
		},
		{
			id: "google-model",
			want: CatalogModel{
				ID: "google-model", Name: "Google Model", Provider: testProvider,
				NativeSDK: "@ai-sdk/google", NativeDialect: "generate-content",
				Dialect: DialectChat, BaseURL: testBaseURL, Translated: true,
				ContextWindow: 1000000, ToolCall: true,
			},
		},
		{
			id: "deprecated-model",
			want: CatalogModel{
				ID: "deprecated-model", Name: "Deprecated Model", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 262144, OutputLimit: 262144, ToolCall: true,
				Status: "deprecated", Deprecated: true,
			},
		},
		{
			id: "input-limited",
			want: CatalogModel{
				ID: "input-limited", Name: "Input Limited", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 100000, InputLimit: 90000, OutputLimit: 10000, ToolCall: true,
			},
		},
		{
			id: "missing-context",
			want: CatalogModel{
				ID: "missing-context", Name: "Missing Context", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				OutputLimit: 1000, ToolCall: true, Unavailable: "missing context limit",
			},
		},
		{
			id: "missing-output",
			want: CatalogModel{
				ID: "missing-output", Name: "Missing Output", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 200000, ToolCall: true,
			},
		},
		{
			id: "negative-output",
			want: CatalogModel{
				ID: "negative-output", Name: "Negative Output", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 200000, ToolCall: true,
			},
		},
		{
			id: "no-tool",
			want: CatalogModel{
				ID: "no-tool", Name: "No Tool", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 200000, Unavailable: "tool calls are not supported",
			},
		},
		{
			id: "non-text",
			want: CatalogModel{
				ID: "non-text", Name: "Non Text", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 200000, ToolCall: true,
				Unavailable: "output modalities do not include text",
			},
		},
		{
			id: "non-chat",
			want: CatalogModel{
				ID: "non-chat", Name: "Non Chat", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 200000, ToolCall: true,
				Unavailable: `unsupported model type "embedding"`,
			},
		},
		{
			id: "input-over-context",
			want: CatalogModel{
				ID: "input-over-context", Name: "Input Over Context", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 200000, InputLimit: 300000, ToolCall: true,
				Unavailable: "input limit 300000 exceeds context limit 200000",
			},
		},
		{
			id: "unknown-sdk",
			want: CatalogModel{
				ID: "unknown-sdk", Name: "Unknown SDK", Provider: testProvider,
				NativeSDK: "@ai-sdk/cohere", BaseURL: testBaseURL,
				ContextWindow: 200000, ToolCall: true,
				Unavailable: `unsupported AI SDK package "@ai-sdk/cohere"`,
			},
		},
		{
			id: "model-level-api",
			want: CatalogModel{
				ID: "model-level-api", Name: "Model Level API", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: "https://opencode.ai/inference/go/openai/v2",
				ContextWindow: 200000, ToolCall: true,
			},
		},
		{
			id: "inherited-npm",
			want: CatalogModel{
				ID: "inherited-npm", Name: "Inherited NPM", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, BaseURL: testBaseURL,
				ContextWindow: 200000, ToolCall: true,
			},
		},
		{
			id: "wrong-host",
			want: CatalogModel{
				ID: "wrong-host", Name: "Wrong Host", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, ContextWindow: 200000, ToolCall: true,
				Unavailable: "base URL host must be opencode.ai",
			},
		},
		{
			id: "http-base",
			want: CatalogModel{
				ID: "http-base", Name: "HTTP Base", Provider: testProvider,
				NativeSDK: "@ai-sdk/openai-compatible", NativeDialect: "chat-completions",
				Dialect: DialectChat, ContextWindow: 200000, ToolCall: true,
				Unavailable: "base URL must be https",
			},
		},
	}
	if len(catalog.Models) != len(tests) {
		t.Fatalf("normalized %d models, want %d", len(catalog.Models), len(tests))
	}
	for _, tc := range tests {
		got, ok := catalog.Models[tc.id]
		if !ok {
			t.Errorf("model %q missing from catalog", tc.id)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("model %q:\n got %#v\nwant %#v", tc.id, got, tc.want)
		}
	}

	usable := catalog.Usable()
	if got, want := len(usable), 10; got != want {
		t.Errorf("usable models = %d, want %d", got, want)
	}
	for i := 1; i < len(usable); i++ {
		if usable[i-1].ID > usable[i].ID {
			t.Errorf("usable models not sorted: %q before %q", usable[i-1].ID, usable[i].ID)
		}
	}
}

func TestCatalogFixtureMissingBaseURL(t *testing.T) {
	fixture := readCatalogFixture(t)
	catalog, err := parseCatalog(fixture, "no-api-provider", DefaultCatalogURL)
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	got, ok := catalog.Models["bare"]
	if !ok {
		t.Fatal(`model "bare" missing`)
	}
	if got.Unavailable != "missing base URL" {
		t.Errorf("unavailable = %q, want %q", got.Unavailable, "missing base URL")
	}
	if got.BaseURL != "" {
		t.Errorf("base URL = %q, want empty", got.BaseURL)
	}
}

func TestCatalogSDKMapping(t *testing.T) {
	raw := func(sdk string) modelsDevModel {
		return modelsDevModel{
			ToolCall:   true,
			Limit:      modelsDevLimit{Context: 1000},
			Modalities: modelsDevModalities{Output: []string{"text"}},
			Provider:   modelsDevModelProvider{NPM: sdk},
		}
	}
	tests := []struct {
		sdk        string
		dialect    Dialect
		native     string
		translated bool
		supported  bool
	}{
		{sdk: "@ai-sdk/openai-compatible", dialect: DialectChat, native: "chat-completions", supported: true},
		{sdk: "@ai-sdk/openai", dialect: DialectResponses, native: "responses", supported: true},
		{sdk: "@ai-sdk/anthropic", dialect: DialectChat, native: "messages", translated: true, supported: true},
		{sdk: "@ai-sdk/google", dialect: DialectChat, native: "generate-content", translated: true, supported: true},
		{sdk: "@ai-sdk/cohere"},
		{sdk: "@ai-sdk/mistral"},
		{sdk: "openai"},
	}
	for _, tc := range tests {
		t.Run(tc.sdk, func(t *testing.T) {
			got := normalizeCatalogModel(testProvider, "m", raw(tc.sdk), "@ai-sdk/openai-compatible", testBaseURL)
			if tc.supported {
				if got.Dialect != tc.dialect {
					t.Errorf("dialect = %q, want %q", got.Dialect, tc.dialect)
				}
				if got.NativeDialect != tc.native {
					t.Errorf("native dialect = %q, want %q", got.NativeDialect, tc.native)
				}
				if got.Translated != tc.translated {
					t.Errorf("translated = %v, want %v", got.Translated, tc.translated)
				}
				if got.Unavailable != "" {
					t.Errorf("unavailable = %q, want empty", got.Unavailable)
				}
				return
			}
			if got.Unavailable == "" {
				t.Fatal("unsupported SDK was marked available")
			}
			if !strings.Contains(got.Unavailable, "SDK") {
				t.Errorf("unavailable = %q, want an SDK diagnostic", got.Unavailable)
			}
			if got.Dialect != "" || got.NativeDialect != "" {
				t.Errorf("dialect = %q/%q, want empty for unsupported SDK", got.Dialect, got.NativeDialect)
			}
		})
	}
}

func TestCatalogModelNameFallback(t *testing.T) {
	raw := modelsDevModel{
		ToolCall:   true,
		Limit:      modelsDevLimit{Context: 1000},
		Modalities: modelsDevModalities{Output: []string{"text"}},
	}
	got := normalizeCatalogModel(testProvider, "fallback-id", raw, "@ai-sdk/openai-compatible", testBaseURL)
	if got.Name != "fallback-id" {
		t.Errorf("name = %q, want fallback %q", got.Name, "fallback-id")
	}
	if !got.Usable() {
		t.Errorf("model is unusable: %q", got.Unavailable)
	}
}

func TestDiscoveredBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "valid", raw: "https://opencode.ai/inference/go/openai/v1", want: "https://opencode.ai/inference/go/openai/v1"},
		{name: "missing", wantErr: "missing base URL"},
		{name: "http", raw: "http://opencode.ai/v1", wantErr: "https"},
		{name: "no host", raw: "https:///v1", wantErr: "host"},
		{name: "userinfo", raw: "https://user:pass@opencode.ai/v1", wantErr: "user information"},
		{name: "query", raw: "https://opencode.ai/v1?key=value", wantErr: "query or fragment"},
		{name: "empty query", raw: "https://opencode.ai/v1?", wantErr: "query or fragment"},
		{name: "fragment", raw: "https://opencode.ai/v1#fragment", wantErr: "query or fragment"},
		{name: "wrong host", raw: "https://evil.example/v1", wantErr: "opencode.ai"},
		{name: "subdomain", raw: "https://api.opencode.ai/v1", wantErr: "opencode.ai"},
		{name: "suffix", raw: "https://opencode.ai.evil.example/v1", wantErr: "opencode.ai"},
		{name: "port", raw: "https://opencode.ai:8443/v1", wantErr: "opencode.ai"},
		{name: "invalid", raw: "https://opencode.ai/%zz", wantErr: "invalid base URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := discoveredBaseURL(tc.raw)
			if tc.wantErr != "" {
				if !strings.Contains(reason, tc.wantErr) {
					t.Fatalf("reason = %q, want substring %q", reason, tc.wantErr)
				}
				if got != "" {
					t.Errorf("base URL = %q, want empty on rejection", got)
				}
				return
			}
			if reason != "" {
				t.Fatalf("unexpected rejection: %q", reason)
			}
			if got != tc.want {
				t.Errorf("base URL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewModelsDevSourceValidation(t *testing.T) {
	stateDir := t.TempDir()
	tests := []struct {
		name    string
		opts    CatalogOptions
		wantErr string
	}{
		{name: "default", opts: CatalogOptions{StateDir: stateDir}},
		{name: "https override", opts: CatalogOptions{URL: "https://mirror.example/api.json", StateDir: stateDir}},
		{name: "loopback v4", opts: CatalogOptions{URL: "http://127.0.0.1:1234/api.json", StateDir: stateDir}},
		{name: "loopback v6", opts: CatalogOptions{URL: "http://[::1]:1234/api.json", StateDir: stateDir}},
		{name: "loopback localhost", opts: CatalogOptions{URL: "http://localhost:1234/api.json", StateDir: stateDir}},
		{name: "http public host", opts: CatalogOptions{URL: "http://models.dev/api.json", StateDir: stateDir}, wantErr: "loopback"},
		{name: "non-http scheme", opts: CatalogOptions{URL: "ftp://models.dev/api.json", StateDir: stateDir}, wantErr: "scheme"},
		{name: "missing host", opts: CatalogOptions{URL: "https:///api.json", StateDir: stateDir}, wantErr: "absolute URL"},
		{name: "userinfo", opts: CatalogOptions{URL: "http://user:pass@127.0.0.1:1234/api.json", StateDir: stateDir}, wantErr: "user information"},
		{name: "https userinfo", opts: CatalogOptions{URL: "https://user@models.dev/api.json", StateDir: stateDir}, wantErr: "user information"},
		{name: "missing state dir", opts: CatalogOptions{URL: "https://models.dev/api.json"}, wantErr: "state directory"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, err := NewModelsDevSource(tc.opts)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("NewModelsDevSource succeeded, want error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewModelsDevSource: %v", err)
			}
			wantURL := tc.opts.URL
			if wantURL == "" {
				wantURL = DefaultCatalogURL
			}
			if source.url != wantURL {
				t.Errorf("source URL = %q, want %q", source.url, wantURL)
			}
		})
	}
}

func TestCatalogSourceProviderValidation(t *testing.T) {
	fixture := readCatalogFixture(t)
	fake, server := startCatalogServer(t, fixture)
	source := newCatalogSource(t, catalogOptions(server.URL, t.TempDir(), newFakeClock()))

	for _, provider := range []string{"", "OpenAI", "../opencode-go", "opencode_go", "opencode go"} {
		t.Run(provider, func(t *testing.T) {
			if _, err := source.Load(t.Context(), provider); err == nil {
				t.Fatalf("Load(%q) succeeded, want validation error", provider)
			}
			if _, err := source.Refresh(t.Context(), provider); err == nil {
				t.Fatalf("Refresh(%q) succeeded, want validation error", provider)
			}
		})
	}
	if got := fake.recorded().count; got != 0 {
		t.Errorf("HTTP requests = %d, want 0", got)
	}
}

func TestCatalogSourceFreshCacheSkipsHTTP(t *testing.T) {
	fixture := readCatalogFixture(t)
	fake, server := startCatalogServer(t, fixture)
	clock := newFakeClock()
	stateDir := t.TempDir()
	source := newCatalogSource(t, catalogOptions(server.URL, stateDir, clock))

	catalog, err := source.Load(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	if got, want := len(catalog.Models), 18; got != want {
		t.Fatalf("models = %d, want %d", got, want)
	}
	if !catalog.FetchedAt.Equal(clock.Now()) {
		t.Errorf("fetched at = %v, want %v", catalog.FetchedAt, clock.Now())
	}
	if catalog.ETag != `"v1"` {
		t.Errorf("etag = %q, want %q", catalog.ETag, `"v1"`)
	}

	recorded := fake.recorded()
	if recorded.count != 1 {
		t.Fatalf("HTTP requests = %d, want 1", recorded.count)
	}
	if recorded.ifNoneMatch[0] != "" {
		t.Errorf("first If-None-Match = %q, want empty", recorded.ifNoneMatch[0])
	}
	for i, authorization := range recorded.authorizations {
		if authorization != "" {
			t.Errorf("request %d sent Authorization %q", i, authorization)
		}
	}
	if recorded.userAgents[0] != "aharness/test" {
		t.Errorf("User-Agent = %q, want %q", recorded.userAgents[0], "aharness/test")
	}
	if recorded.accepts[0] != "application/json" {
		t.Errorf("Accept = %q, want %q", recorded.accepts[0], "application/json")
	}

	cachePath := CatalogCachePath(stateDir, testProvider)
	fileInfo, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("cache file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(cachePath))
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("cache dir mode = %o, want 700", got)
	}
	entries, err := os.ReadDir(filepath.Dir(cachePath))
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("cache dir entries = %d, want 1 (no temporary files)", len(entries))
	}

	var cached catalogCacheFile
	if err := jsonv2.Unmarshal(readFileBytes(t, cachePath), &cached); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if cached.CacheVersion != catalogCacheVersion {
		t.Errorf("cache version = %d, want %d", cached.CacheVersion, catalogCacheVersion)
	}
	if cached.Source != server.URL || cached.Provider != testProvider {
		t.Errorf("cache source/provider = %q/%q, want %q/%q", cached.Source, cached.Provider, server.URL, testProvider)
	}
	if cached.FetchedAt != clock.Now().UTC().Format(time.RFC3339) {
		t.Errorf("cache fetched_at = %q, want %q", cached.FetchedAt, clock.Now().UTC().Format(time.RFC3339))
	}
	if cached.ETag != `"v1"` {
		t.Errorf("cache etag = %q, want %q", cached.ETag, `"v1"`)
	}
	if len(cached.Models) != 18 {
		t.Fatalf("cache models = %d, want 18", len(cached.Models))
	}
	for i := 1; i < len(cached.Models); i++ {
		if cached.Models[i-1].ID > cached.Models[i].ID {
			t.Errorf("cache models not sorted: %q before %q", cached.Models[i-1].ID, cached.Models[i].ID)
		}
	}

	clock.Add(23 * time.Hour)
	if _, err := source.Load(t.Context(), testProvider); err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if got := fake.recorded().count; got != 1 {
		t.Errorf("HTTP requests after fresh cache = %d, want 1", got)
	}
}

func TestCatalogSourceUserAgentFallback(t *testing.T) {
	fixture := readCatalogFixture(t)
	fake, server := startCatalogServer(t, fixture)
	source := newCatalogSource(t, CatalogOptions{
		URL:      server.URL,
		StateDir: t.TempDir(),
		Now:      newFakeClock().Now,
	})
	if _, err := source.Load(t.Context(), testProvider); err != nil {
		t.Fatalf("Load: %v", err)
	}
	recorded := fake.recorded()
	if recorded.userAgents[0] != "aharness/unknown" {
		t.Errorf("User-Agent = %q, want %q", recorded.userAgents[0], "aharness/unknown")
	}
}

func TestCatalogSourceStaleConditional304(t *testing.T) {
	fixture := readCatalogFixture(t)
	fake, server := startCatalogServer(t, fixture)
	clock := newFakeClock()
	stateDir := t.TempDir()
	source := newCatalogSource(t, catalogOptions(server.URL, stateDir, clock))

	first, err := source.Load(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	clock.Add(CatalogTTL + time.Minute)
	fake.set(nil, http.StatusNotModified, "")

	catalog, err := source.Load(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("conditional Load: %v", err)
	}
	recorded := fake.recorded()
	if recorded.count != 2 {
		t.Fatalf("HTTP requests = %d, want 2", recorded.count)
	}
	if recorded.ifNoneMatch[1] != `"v1"` {
		t.Errorf("If-None-Match = %q, want %q", recorded.ifNoneMatch[1], `"v1"`)
	}
	if !catalog.FetchedAt.Equal(clock.Now()) {
		t.Errorf("fetched at = %v, want %v", catalog.FetchedAt, clock.Now())
	}
	if !catalog.FetchedAt.After(first.FetchedAt) {
		t.Errorf("fetched at was not refreshed: %v vs %v", catalog.FetchedAt, first.FetchedAt)
	}
	if catalog.ETag != `"v1"` {
		t.Errorf("etag = %q, want %q", catalog.ETag, `"v1"`)
	}
	if len(catalog.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", catalog.Warnings)
	}
	if got, want := len(catalog.Models), len(first.Models); got != want {
		t.Errorf("models = %d, want %d", got, want)
	}

	var cached catalogCacheFile
	if err := jsonv2.Unmarshal(readFileBytes(t, CatalogCachePath(stateDir, testProvider)), &cached); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if cached.FetchedAt != clock.Now().UTC().Format(time.RFC3339) {
		t.Errorf("cache fetched_at = %q, want %q", cached.FetchedAt, clock.Now().UTC().Format(time.RFC3339))
	}
	if cached.ETag != `"v1"` {
		t.Errorf("cache etag = %q, want %q", cached.ETag, `"v1"`)
	}

	if _, err := source.Load(t.Context(), testProvider); err != nil {
		t.Fatalf("fresh Load after 304: %v", err)
	}
	if got := fake.recorded().count; got != 2 {
		t.Errorf("HTTP requests after refreshed cache = %d, want 2", got)
	}
}

func TestCatalogSourceStale200ReplacesCache(t *testing.T) {
	fixture := readCatalogFixture(t)
	fake, server := startCatalogServer(t, fixture)
	clock := newFakeClock()
	stateDir := t.TempDir()
	source := newCatalogSource(t, catalogOptions(server.URL, stateDir, clock))

	if _, err := source.Load(t.Context(), testProvider); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	clock.Add(CatalogTTL + time.Minute)
	fake.set([]byte(replacementCatalog), http.StatusOK, `"v2"`)

	catalog, err := source.Load(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("conditional Load: %v", err)
	}
	recorded := fake.recorded()
	if recorded.count != 2 {
		t.Fatalf("HTTP requests = %d, want 2", recorded.count)
	}
	if recorded.ifNoneMatch[1] != `"v1"` {
		t.Errorf("If-None-Match = %q, want %q", recorded.ifNoneMatch[1], `"v1"`)
	}
	if _, ok := catalog.Models["replacement-model"]; !ok {
		t.Fatalf("replacement model missing from %v", catalog.Models)
	}
	if _, ok := catalog.Models["chat-model"]; ok {
		t.Error("old model still present after 200 replacement")
	}
	if catalog.ETag != `"v2"` {
		t.Errorf("etag = %q, want %q", catalog.ETag, `"v2"`)
	}
	if len(catalog.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", catalog.Warnings)
	}

	var cached catalogCacheFile
	if err := jsonv2.Unmarshal(readFileBytes(t, CatalogCachePath(stateDir, testProvider)), &cached); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if len(cached.Models) != 1 || cached.Models[0].ID != "replacement-model" {
		t.Errorf("cache models = %+v, want only replacement-model", cached.Models)
	}
	if cached.ETag != `"v2"` {
		t.Errorf("cache etag = %q, want %q", cached.ETag, `"v2"`)
	}
}

func TestCatalogSourceStaleFallback(t *testing.T) {
	fixture := readCatalogFixture(t)
	tests := []struct {
		name      string
		configure func(*fakeCatalogServer, *httptest.Server)
	}{
		{
			name: "server error",
			configure: func(fake *fakeCatalogServer, _ *httptest.Server) {
				fake.set(nil, http.StatusInternalServerError, "")
			},
		},
		{
			name: "invalid json",
			configure: func(fake *fakeCatalogServer, _ *httptest.Server) {
				fake.set([]byte(`{"broken"`), http.StatusOK, "")
			},
		},
		{
			name: "invalid schema",
			configure: func(fake *fakeCatalogServer, _ *httptest.Server) {
				fake.set([]byte(`{"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/v1","models":{}}}`), http.StatusOK, "")
			},
		},
		{
			name: "transport",
			configure: func(_ *fakeCatalogServer, server *httptest.Server) {
				server.Close()
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, server := startCatalogServer(t, fixture)
			clock := newFakeClock()
			stateDir := t.TempDir()
			source := newCatalogSource(t, catalogOptions(server.URL, stateDir, clock))

			seeded, err := source.Load(t.Context(), testProvider)
			if err != nil {
				t.Fatalf("seed Load: %v", err)
			}
			cachePath := CatalogCachePath(stateDir, testProvider)
			before := readFileBytes(t, cachePath)
			clock.Add(CatalogTTL + time.Minute)
			tc.configure(fake, server)

			catalog, err := source.Load(t.Context(), testProvider)
			if err != nil {
				t.Fatalf("stale Load returned error: %v", err)
			}
			if len(catalog.Warnings) == 0 {
				t.Fatal("stale fallback emitted no warning")
			}
			if !strings.Contains(strings.Join(catalog.Warnings, "\n"), "using cached catalog") {
				t.Errorf("warnings = %v, want stale-cache diagnostic", catalog.Warnings)
			}
			if !reflect.DeepEqual(catalog.Models, seeded.Models) {
				t.Error("stale fallback did not return the cached models")
			}
			if after := readFileBytes(t, cachePath); !reflect.DeepEqual(after, before) {
				t.Error("stale fallback modified the cache")
			}
		})
	}
}

func TestCatalogSourceNoCacheFailures(t *testing.T) {
	fixture := readCatalogFixture(t)
	tests := []struct {
		name    string
		body    []byte
		status  int
		wantErr error
	}{
		{name: "server error", status: http.StatusInternalServerError, wantErr: ErrCatalogUnavailable},
		{name: "invalid json", body: []byte(`{"broken"`), status: http.StatusOK, wantErr: ErrCatalogInvalid},
		{name: "missing provider", body: []byte(`{"other":{"models":{}}}`), status: http.StatusOK, wantErr: ErrCatalogInvalid},
		{name: "empty models", body: []byte(`{"opencode-go":{"models":{}}}`), status: http.StatusOK, wantErr: ErrCatalogInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == nil {
				body = fixture
			}
			fake, server := startCatalogServer(t, body)
			if tc.status != 0 {
				fake.set(body, tc.status, "")
			}
			stateDir := t.TempDir()
			source := newCatalogSource(t, catalogOptions(server.URL, stateDir, newFakeClock()))

			_, err := source.Load(t.Context(), testProvider)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Load error = %v, want %v", err, tc.wantErr)
			}
			cachePath := CatalogCachePath(stateDir, testProvider)
			if !strings.Contains(err.Error(), server.URL) {
				t.Errorf("error = %q, want source URL %q", err, server.URL)
			}
			if !strings.Contains(err.Error(), cachePath) {
				t.Errorf("error = %q, want cache path %q", err, cachePath)
			}
			if _, statErr := os.Stat(cachePath); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("cache file exists after failed Load: %v", statErr)
			}
		})
	}
}

func TestCatalogSourceTimeout(t *testing.T) {
	fixture := readCatalogFixture(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(slow.Close)

	opts := catalogOptions(slow.URL, t.TempDir(), newFakeClock())
	opts.Timeout = 10 * time.Millisecond
	source := newCatalogSource(t, opts)

	_, err := source.Load(t.Context(), testProvider)
	if !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("Load error = %v, want %v", err, ErrCatalogUnavailable)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Load error = %v, want context deadline exceeded", err)
	}
}

func TestCatalogSourceCorruptCacheIsIgnored(t *testing.T) {
	tests := []struct {
		name string
		data func(t *testing.T, source string, base func(string) catalogCacheFile) []byte
	}{
		{
			name: "corrupt json",
			data: func(t *testing.T, _ string, _ func(string) catalogCacheFile) []byte {
				t.Helper()
				return []byte(`{"cache_version":`)
			},
		},
		{
			name: "version mismatch",
			data: func(t *testing.T, source string, base func(string) catalogCacheFile) []byte {
				t.Helper()
				cached := base(source)
				cached.CacheVersion = catalogCacheVersion + 1
				return marshalCache(t, cached)
			},
		},
		{
			name: "source mismatch",
			data: func(t *testing.T, source string, base func(string) catalogCacheFile) []byte {
				t.Helper()
				cached := base(source)
				cached.Source = "https://mirror.example/api.json"
				return marshalCache(t, cached)
			},
		},
		{
			name: "provider mismatch",
			data: func(t *testing.T, source string, base func(string) catalogCacheFile) []byte {
				t.Helper()
				cached := base(source)
				cached.Provider = "other-provider"
				return marshalCache(t, cached)
			},
		},
		{
			name: "empty models",
			data: func(t *testing.T, source string, base func(string) catalogCacheFile) []byte {
				t.Helper()
				cached := base(source)
				cached.Models = nil
				return marshalCache(t, cached)
			},
		},
		{
			name: "invalid model id",
			data: func(t *testing.T, source string, base func(string) catalogCacheFile) []byte {
				t.Helper()
				cached := base(source)
				cached.Models = []CatalogModel{{ID: "", Provider: testProvider}}
				return marshalCache(t, cached)
			},
		},
		{
			name: "tampered usable base URL",
			data: func(t *testing.T, source string, base func(string) catalogCacheFile) []byte {
				t.Helper()
				cached := base(source)
				for i := range cached.Models {
					if cached.Models[i].Unavailable == "" {
						cached.Models[i].BaseURL = "https://attacker.example/v1"
						break
					}
				}
				return marshalCache(t, cached)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := readCatalogFixture(t)
			fake, server := startCatalogServer(t, fixture)
			stateDir := t.TempDir()
			clock := newFakeClock()
			source := newCatalogSource(t, catalogOptions(server.URL, stateDir, clock))

			base := func(sourceURL string) catalogCacheFile {
				catalog, err := parseCatalog(fixture, testProvider, sourceURL)
				if err != nil {
					t.Fatalf("parse fixture: %v", err)
				}
				return catalogCacheFile{
					CacheVersion: catalogCacheVersion,
					Source:       sourceURL,
					Provider:     testProvider,
					FetchedAt:    clock.Now().Add(-time.Hour).Format(time.RFC3339),
					ETag:         `"stale"`,
					Models:       catalog.SortedModels(),
				}
			}
			cachePath := CatalogCachePath(stateDir, testProvider)
			writeFileMode(t, cachePath, tc.data(t, server.URL, base), 0o600)

			catalog, err := source.Load(t.Context(), testProvider)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			recorded := fake.recorded()
			if recorded.count != 1 {
				t.Errorf("HTTP requests = %d, want 1", recorded.count)
			}
			if recorded.ifNoneMatch[0] != "" {
				t.Errorf("If-None-Match = %q, want empty for ignored cache", recorded.ifNoneMatch[0])
			}
			if len(catalog.Models) != 18 {
				t.Errorf("loaded models = %d, want 18", len(catalog.Models))
			}
			var cached catalogCacheFile
			if err := jsonv2.Unmarshal(readFileBytes(t, cachePath), &cached); err != nil {
				t.Fatalf("decode rewritten cache: %v", err)
			}
			if cached.CacheVersion != catalogCacheVersion || cached.Source != server.URL || cached.Provider != testProvider {
				t.Errorf("cache not rewritten: %+v", cached)
			}
			if cached.ETag != `"v1"` {
				t.Errorf("cache etag = %q, want %q", cached.ETag, `"v1"`)
			}
		})
	}
}

func TestCatalogSourceRevalidateBypassesTTL(t *testing.T) {
	fixture := readCatalogFixture(t)
	fake, server := startCatalogServer(t, fixture)
	clock := newFakeClock()
	source := newCatalogSource(t, catalogOptions(server.URL, t.TempDir(), clock))

	if _, err := source.Load(t.Context(), testProvider); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	fake.set(nil, http.StatusNotModified, "")
	catalog, err := source.Revalidate(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	recorded := fake.recorded()
	if recorded.count != 2 {
		t.Fatalf("HTTP requests = %d, want 2", recorded.count)
	}
	if recorded.ifNoneMatch[1] != `"v1"` {
		t.Errorf("If-None-Match = %q, want %q", recorded.ifNoneMatch[1], `"v1"`)
	}
	if !catalog.FetchedAt.Equal(clock.Now()) {
		t.Errorf("fetched at = %v, want %v", catalog.FetchedAt, clock.Now())
	}
	if len(catalog.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", catalog.Warnings)
	}

	fake.set(nil, http.StatusInternalServerError, "")
	stale, err := source.Revalidate(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("Revalidate fallback: %v", err)
	}
	if len(stale.Warnings) == 0 {
		t.Fatal("Revalidate fallback emitted no warning")
	}
	if got := fake.recorded().count; got != 3 {
		t.Errorf("HTTP requests = %d, want 3", got)
	}
}

func TestCatalogSourceUnexpected304(t *testing.T) {
	t.Run("no cache", func(t *testing.T) {
		fake, server := startCatalogServer(t, readCatalogFixture(t))
		fake.set(nil, http.StatusNotModified, "")
		source := newCatalogSource(t, catalogOptions(server.URL, t.TempDir(), newFakeClock()))

		_, err := source.Load(t.Context(), testProvider)
		if !errors.Is(err, ErrCatalogInvalid) {
			t.Fatalf("Load error = %v, want %v", err, ErrCatalogInvalid)
		}
		if !strings.Contains(err.Error(), "304") {
			t.Errorf("error = %q, want 304 diagnostic", err)
		}
	})

	t.Run("stale cache without etag", func(t *testing.T) {
		fixture := readCatalogFixture(t)
		fake, server := startCatalogServer(t, fixture)
		clock := newFakeClock()
		stateDir := t.TempDir()
		source := newCatalogSource(t, catalogOptions(server.URL, stateDir, clock))

		seeded, err := parseCatalog(fixture, testProvider, server.URL)
		if err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		cached := catalogCacheFile{
			CacheVersion: catalogCacheVersion,
			Source:       server.URL,
			Provider:     testProvider,
			FetchedAt:    clock.Now().Add(-CatalogTTL - time.Hour).Format(time.RFC3339),
			Models:       seeded.SortedModels(),
		}
		writeFileMode(t, CatalogCachePath(stateDir, testProvider), marshalCache(t, cached), 0o600)
		fake.set(nil, http.StatusNotModified, "")

		catalog, err := source.Load(t.Context(), testProvider)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(catalog.Warnings) == 0 {
			t.Fatal("unexpected 304 did not warn when using stale cache")
		}
		recorded := fake.recorded()
		if recorded.ifNoneMatch[0] != "" {
			t.Errorf("If-None-Match = %q, want empty", recorded.ifNoneMatch[0])
		}
	})
}

func TestCatalogSourceRefreshReplacesCache(t *testing.T) {
	fixture := readCatalogFixture(t)
	fake, server := startCatalogServer(t, fixture)
	stateDir := t.TempDir()
	source := newCatalogSource(t, catalogOptions(server.URL, stateDir, newFakeClock()))

	if _, err := source.Load(t.Context(), testProvider); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	before := readFileBytes(t, CatalogCachePath(stateDir, testProvider))
	fake.set([]byte(replacementCatalog), http.StatusOK, `"v2"`)

	catalog, err := source.Refresh(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	recorded := fake.recorded()
	if recorded.count != 2 {
		t.Fatalf("HTTP requests = %d, want 2", recorded.count)
	}
	if recorded.ifNoneMatch[1] != "" {
		t.Errorf("Refresh sent If-None-Match %q, want none", recorded.ifNoneMatch[1])
	}
	for i, authorization := range recorded.authorizations {
		if authorization != "" {
			t.Errorf("request %d sent Authorization %q", i, authorization)
		}
	}
	if _, ok := catalog.Models["replacement-model"]; !ok {
		t.Fatalf("replacement model missing from %v", catalog.Models)
	}
	if catalog.ETag != `"v2"` {
		t.Errorf("etag = %q, want %q", catalog.ETag, `"v2"`)
	}

	cachePath := CatalogCachePath(stateDir, testProvider)
	after := readFileBytes(t, cachePath)
	if reflect.DeepEqual(before, after) {
		t.Error("Refresh did not replace the cache")
	}
	var cached catalogCacheFile
	if err := jsonv2.Unmarshal(after, &cached); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if cached.ETag != `"v2"` || len(cached.Models) != 1 || cached.Models[0].ID != "replacement-model" {
		t.Errorf("cache = %+v, want refreshed replacement catalog", cached)
	}
}

func TestCatalogSourceRefreshFailuresPreserveCache(t *testing.T) {
	fixture := readCatalogFixture(t)
	tests := []struct {
		name       string
		configure  func(*fakeCatalogServer, *httptest.Server)
		maxBytes   int64
		wantErr    error
		errContain string
	}{
		{
			name: "304 not modified",
			configure: func(fake *fakeCatalogServer, _ *httptest.Server) {
				fake.set(nil, http.StatusNotModified, "")
			},
			wantErr:    ErrCatalogInvalid,
			errContain: "304",
		},
		{
			name: "invalid json",
			configure: func(fake *fakeCatalogServer, _ *httptest.Server) {
				fake.set([]byte(`{"broken"`), http.StatusOK, "")
			},
			wantErr: ErrCatalogInvalid,
		},
		{
			name: "invalid schema",
			configure: func(fake *fakeCatalogServer, _ *httptest.Server) {
				fake.set([]byte(`{"opencode-go":{"models":{}}}`), http.StatusOK, "")
			},
			wantErr: ErrCatalogInvalid,
		},
		{
			name: "server error",
			configure: func(fake *fakeCatalogServer, _ *httptest.Server) {
				fake.set(nil, http.StatusServiceUnavailable, "")
			},
			wantErr: ErrCatalogUnavailable,
		},
		{
			name: "body cap",
			configure: func(fake *fakeCatalogServer, _ *httptest.Server) {
				fake.set(fixture, http.StatusOK, "")
			},
			maxBytes:   32,
			wantErr:    ErrCatalogInvalid,
			errContain: "exceeds",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, server := startCatalogServer(t, fixture)
			stateDir := t.TempDir()
			opts := catalogOptions(server.URL, stateDir, newFakeClock())

			seeder := newCatalogSource(t, opts)
			if _, err := seeder.Load(t.Context(), testProvider); err != nil {
				t.Fatalf("seed Load: %v", err)
			}
			cachePath := CatalogCachePath(stateDir, testProvider)
			before := readFileBytes(t, cachePath)

			tc.configure(fake, server)
			refreshOpts := opts
			refreshOpts.MaxBytes = tc.maxBytes
			refresher := newCatalogSource(t, refreshOpts)

			_, err := refresher.Refresh(t.Context(), testProvider)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Refresh error = %v, want %v", err, tc.wantErr)
			}
			if tc.errContain != "" && !strings.Contains(err.Error(), tc.errContain) {
				t.Errorf("error = %q, want substring %q", err, tc.errContain)
			}
			if after := readFileBytes(t, cachePath); !reflect.DeepEqual(after, before) {
				t.Error("failed Refresh modified the cache")
			}
			recorded := fake.recorded()
			if recorded.count != 2 {
				t.Errorf("HTTP requests = %d, want 2", recorded.count)
			}
			if recorded.ifNoneMatch[1] != "" {
				t.Errorf("Refresh sent If-None-Match %q, want none", recorded.ifNoneMatch[1])
			}
		})
	}
}

func TestCatalogSourceRefreshTransportFailurePreservesCache(t *testing.T) {
	fixture := readCatalogFixture(t)
	_, server := startCatalogServer(t, fixture)
	stateDir := t.TempDir()
	source := newCatalogSource(t, catalogOptions(server.URL, stateDir, newFakeClock()))

	if _, err := source.Load(t.Context(), testProvider); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	cachePath := CatalogCachePath(stateDir, testProvider)
	before := readFileBytes(t, cachePath)
	server.Close()

	_, err := source.Refresh(t.Context(), testProvider)
	if !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("Refresh error = %v, want %v", err, ErrCatalogUnavailable)
	}
	if !strings.Contains(err.Error(), server.URL) || !strings.Contains(err.Error(), cachePath) {
		t.Errorf("error = %q, want source URL and cache path", err)
	}
	if after := readFileBytes(t, cachePath); !reflect.DeepEqual(after, before) {
		t.Error("failed Refresh modified the cache")
	}
}

func TestCatalogSourceRefreshCacheWriteFailure(t *testing.T) {
	fixture := readCatalogFixture(t)
	_, server := startCatalogServer(t, fixture)
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "cache"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block cache directory: %v", err)
	}
	source := newCatalogSource(t, catalogOptions(server.URL, stateDir, newFakeClock()))

	catalog, err := source.Refresh(t.Context(), testProvider)
	if !errors.Is(err, ErrCatalogCache) {
		t.Fatalf("Refresh error = %v, want %v", err, ErrCatalogCache)
	}
	if len(catalog.Models) != 18 {
		t.Errorf("Refreshed catalog models = %d, want 18 for in-memory use", len(catalog.Models))
	}
	if !strings.Contains(err.Error(), server.URL) {
		t.Errorf("error = %q, want source URL", err)
	}
	if !strings.Contains(err.Error(), CatalogCachePath(stateDir, testProvider)) {
		t.Errorf("error = %q, want cache path", err)
	}
	if _, statErr := os.Stat(CatalogCachePath(stateDir, testProvider)); statErr == nil {
		t.Error("cache file exists after write failure")
	}
}

func TestCatalogSourceLoadCacheWriteWarning(t *testing.T) {
	fixture := readCatalogFixture(t)
	_, server := startCatalogServer(t, fixture)
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "cache"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block cache directory: %v", err)
	}
	source := newCatalogSource(t, catalogOptions(server.URL, stateDir, newFakeClock()))

	catalog, err := source.Load(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(catalog.Models) != 18 {
		t.Errorf("models = %d, want 18", len(catalog.Models))
	}
	if len(catalog.Warnings) == 0 {
		t.Fatal("cache write failure did not produce a warning")
	}
	if !strings.Contains(strings.Join(catalog.Warnings, "\n"), "cache write failed") {
		t.Errorf("warnings = %v, want cache write diagnostic", catalog.Warnings)
	}
}

func TestCatalogSourceBodyLimit(t *testing.T) {
	fixture := readCatalogFixture(t)
	_, server := startCatalogServer(t, fixture)
	opts := catalogOptions(server.URL, t.TempDir(), newFakeClock())
	opts.MaxBytes = int64(len(fixture) - 1)
	source := newCatalogSource(t, opts)

	_, err := source.Load(t.Context(), testProvider)
	if !errors.Is(err, ErrCatalogInvalid) {
		t.Fatalf("Load error = %v, want %v", err, ErrCatalogInvalid)
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want body cap diagnostic", err)
	}
}

func TestCatalogSourceInvalidCatalogs(t *testing.T) {
	modelJSON := `{"name":"M","tool_call":true,"limit":{"context":1000},"modalities":{"output":["text"]}}`
	longID := strings.Repeat("a", maxCatalogModelIDBytes+1)
	tests := []struct {
		name string
		body string
	}{
		{
			name: "duplicate provider",
			body: `{"opencode-go":{"models":{}},"opencode-go":{"models":{}}}`,
		},
		{
			name: "duplicate model",
			body: fmt.Sprintf(`{"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/v1","models":{"m":%s,"m":%s}}}`, modelJSON, modelJSON),
		},
		{
			name: "missing provider",
			body: `{"other-provider":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/v1","models":{"m":` + modelJSON + `}}}`,
		},
		{
			name: "empty models",
			body: `{"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/v1","models":{}}}`,
		},
		{
			name: "null document",
			body: `null`,
		},
		{
			name: "model id mismatch",
			body: `{"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/v1","models":{"m":{"id":"other","tool_call":true,"limit":{"context":1000},"modalities":{"output":["text"]}}}}}`,
		},
		{
			name: "control character id",
			body: `{"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/v1","models":{"bad\u0001id":{"tool_call":true,"limit":{"context":1000},"modalities":{"output":["text"]}}}}}`,
		},
		{
			name: "overlong id",
			body: fmt.Sprintf(`{"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/v1","models":{%q:{"tool_call":true,"limit":{"context":1000},"modalities":{"output":["text"]}}}}}`, longID),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, server := startCatalogServer(t, []byte(tc.body))
			stateDir := t.TempDir()
			source := newCatalogSource(t, catalogOptions(server.URL, stateDir, newFakeClock()))

			_, err := source.Load(t.Context(), testProvider)
			if !errors.Is(err, ErrCatalogInvalid) {
				t.Fatalf("Load error = %v, want %v", err, ErrCatalogInvalid)
			}
			if got := fake.recorded().count; got != 1 {
				t.Errorf("HTTP requests = %d, want 1", got)
			}
			if _, statErr := os.Stat(CatalogCachePath(stateDir, testProvider)); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("invalid catalog wrote a cache file: %v", statErr)
			}
		})
	}
}

func FuzzCatalogDecoder(f *testing.F) {
	if fixture, err := os.ReadFile("testdata/models_dev.json"); err == nil {
		f.Add(fixture)
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/v1","models":{"m":{"tool_call":true,"limit":{"context":1000},"modalities":{"output":["text"]}}}}}`))
	f.Add([]byte(`{"opencode-go":{"models":{"m":{"id":"other","tool_call":true,"limit":{"context":-1},"modalities":{}}}}}`))
	f.Add(effortCatalogBody(effortCatalogModel + `,"reasoning_options":[{"type":"effort","values":["high","low"]}]`))
	f.Add(effortCatalogBody(effortCatalogModel + `,"reasoning_options":[{"type":"toggle","values":["low"]},{"type":"budget_tokens","max":1000}]`))
	f.Add(effortCatalogBody(effortCatalogModel + `,"reasoning_options":[{"type":"mystery","values":["max"]}]`))
	f.Add(effortCatalogBody(effortCatalogModel + `,"reasoning_options":[{"type":"effort","values":["high","high","default","High","high low","high\u0001","` + strings.Repeat("a", MaxThinkingEffortBytes+1) + `"]}]`))
	f.Add(effortCatalogBody(effortCatalogModel + `,"reasoning_options":{}`))
	f.Add(effortCatalogBody(effortCatalogModel + `,"reasoning_options":"effort"`))
	f.Add(effortCatalogBody(effortCatalogModel + `,"reasoning_options":42`))
	f.Fuzz(func(t *testing.T, data []byte) {
		catalog, err := parseCatalog(data, testProvider, DefaultCatalogURL)
		if err != nil {
			return
		}
		if catalog.Provider != testProvider {
			t.Fatalf("provider = %q, want %q", catalog.Provider, testProvider)
		}
		if len(catalog.Models) == 0 {
			t.Fatal("successful parse returned an empty catalog")
		}
		second, err := parseCatalog(data, testProvider, DefaultCatalogURL)
		if err != nil {
			t.Fatalf("second parse error = %v, want deterministic success", err)
		}
		if !reflect.DeepEqual(catalog.Models, second.Models) {
			t.Fatal("catalog normalization is not deterministic")
		}
		for id, m := range catalog.Models {
			if !validReasoningEfforts(m.ReasoningEfforts) {
				t.Fatalf("model %q efforts = %q, want normalized values", id, m.ReasoningEfforts)
			}
		}
	})
}
