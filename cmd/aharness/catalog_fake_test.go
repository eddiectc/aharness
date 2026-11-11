package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

// testCatalogBaseURL is the documented OpenAI base for the opencode-go
// provider. New sessions must discover exactly this value.
const testCatalogBaseURL = "https://opencode.ai/inference/go/openai/v1"

// fakeCatalogModel describes one model in the test models.dev catalog.
type fakeCatalogModel struct {
	Name       string
	SDK        string // per-model provider.npm; empty inherits the provider npm
	Context    int
	Input      int
	Output     int
	Status     string
	ToolCall   bool
	OutputMods []string // defaults to ["text"]
	Type       string
	Reasoning  bool
	Efforts    []string // reasoning_options effort values, raw and unordered
}

// testCatalogModel builds a model that advertises tool calls and text output,
// matching the real catalog's common shape.
func testCatalogModel(name, sdk string, context, input, output int) fakeCatalogModel {
	return fakeCatalogModel{
		Name:     name,
		SDK:      sdk,
		Context:  context,
		Input:    input,
		Output:   output,
		ToolCall: true,
	}
}

// defaultFakeCatalogModels is the models.dev fixture shared by the cmd tests.
// The numeric limits match the contract examples in §3.4.
func defaultFakeCatalogModels() map[string]fakeCatalogModel {
	deprecated := testCatalogModel("Old Deprecated", "@ai-sdk/openai-compatible", 100_000, 0, 8_192)
	deprecated.Status = "deprecated"
	noTools := testCatalogModel("No Tools", "@ai-sdk/openai-compatible", 200_000, 0, 8_192)
	noTools.ToolCall = false
	noContext := testCatalogModel("No Context", "@ai-sdk/openai", 0, 0, 8_192)
	unsupported := testCatalogModel("Exotic SDK", "@ai-sdk/cohere", 100_000, 0, 8_192)
	return map[string]fakeCatalogModel{
		"deepseek-v4.1-flash":        testCatalogModel("DeepSeek V4.1 Flash", "", 1_000_000, 0, 384_000),
		"gpt-6-luna":                 testCatalogModel("GPT-6 Luna", "@ai-sdk/openai", 1_050_000, 922_000, 128_000),
		"kimi-k2.7-code":             testCatalogModel("Kimi K2.7 Code", "@ai-sdk/openai-compatible", 262_144, 0, 262_144),
		"qwen3.7-plus":               testCatalogModel("Qwen3.7 Plus", "@ai-sdk/anthropic", 131_072, 0, 8_192),
		"crouton-alpha-free":         testCatalogModel("Crouton Alpha Free", "@ai-sdk/openai", 200_000, 0, 8_192),
		"muse-spark-1.3-contributor": testCatalogModel("Muse Spark 1.3 Contributor", "@ai-sdk/openai", 200_000, 0, 8_192),
		"old-deprecated":             deprecated,
		"no-tools":                   noTools,
		"no-context":                 noContext,
		"exotic-sdk":                 unsupported,
	}
}

// catalogPayload renders a models.dev api.json body for the opencode-go
// provider.
func catalogPayload(models map[string]fakeCatalogModel) []byte {
	encoded := make(map[string]any, len(models))
	for id, m := range models {
		outputMods := m.OutputMods
		if outputMods == nil {
			outputMods = []string{"text"}
		}
		limit := map[string]any{}
		if m.Context > 0 {
			limit["context"] = m.Context
		}
		if m.Input > 0 {
			limit["input"] = m.Input
		}
		if m.Output > 0 {
			limit["output"] = m.Output
		}
		entry := map[string]any{
			"name":       m.Name,
			"tool_call":  m.ToolCall,
			"limit":      limit,
			"modalities": map[string]any{"output": outputMods},
		}
		if m.SDK != "" {
			entry["provider"] = map[string]any{"npm": m.SDK}
		}
		if m.Status != "" {
			entry["status"] = m.Status
		}
		if m.Type != "" {
			entry["type"] = m.Type
		}
		if m.Reasoning {
			entry["reasoning"] = true
		}
		if len(m.Efforts) > 0 {
			entry["reasoning_options"] = []any{
				map[string]any{"type": "effort", "values": m.Efforts},
			}
		}
		encoded[id] = entry
	}
	document := map[string]any{
		providerName: map[string]any{
			"npm":    "@ai-sdk/openai-compatible",
			"api":    testCatalogBaseURL,
			"models": encoded,
		},
	}
	body, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return body
}

// fakeModelsDev is one test models.dev server. It supports ETags, forced
// failures, and swappable model sets so cache and refresh paths are testable.
type fakeModelsDev struct {
	server *httptest.Server

	mu       sync.Mutex
	models   map[string]fakeCatalogModel
	rawBody  []byte
	etag     string
	failCode int
	failBody string
	calls    int
	headers  []http.Header
	block    chan struct{}
}

func newFakeModelsDev() *fakeModelsDev {
	f := &fakeModelsDev{
		models: defaultFakeCatalogModels(),
		etag:   `"test-catalog-v1"`,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *fakeModelsDev) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.headers = append(f.headers, r.Header.Clone())
	code := f.failCode
	failBody := f.failBody
	etag := f.etag
	rawBody := f.rawBody
	body := rawBody
	if body == nil {
		body = catalogPayload(f.models)
	}
	block := f.block
	f.block = nil
	f.mu.Unlock()

	if block != nil {
		<-block
	}
	if code != 0 {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(failBody))
		return
	}
	if etag != "" {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (f *fakeModelsDev) URL() string { return f.server.URL + "/api.json" }

func (f *fakeModelsDev) Close() { f.server.Close() }

func (f *fakeModelsDev) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeModelsDev) Headers() []http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]http.Header(nil), f.headers...)
}

func (f *fakeModelsDev) SetModels(models map[string]fakeCatalogModel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models = models
	f.rawBody = nil
}

func (f *fakeModelsDev) SetRawBody(body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rawBody = body
}

// SetFailure makes every response fail with code and body. A zero code
// restores the catalog.
func (f *fakeModelsDev) SetFailure(code int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCode = code
	f.failBody = body
}

func (f *fakeModelsDev) SetETag(etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.etag = etag
}

// BlockNext makes the next request wait until the returned channel is closed.
func (f *fakeModelsDev) BlockNext() chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block = make(chan struct{})
	return f.block
}

// defaultTestModelsDev is the package-wide catalog used by integration runs
// that do not need to inspect catalog requests themselves.
var defaultTestModelsDev *fakeModelsDev

func TestMain(m *testing.M) {
	defaultTestModelsDev = newFakeModelsDev()
	code := m.Run()
	defaultTestModelsDev.Close()
	os.Exit(code)
}

func testCatalogEnv() string {
	return "AHARNESS_MODELS_DEV_URL=" + defaultTestModelsDev.URL()
}

// startFakeModelsDev returns a per-test catalog server.
func startFakeModelsDev(t *testing.T) *fakeModelsDev {
	t.Helper()
	f := newFakeModelsDev()
	t.Cleanup(f.Close)
	return f
}

// envHasKey reports whether extra already sets key, so helpers can honor an
// explicit test override.
func envHasKey(extra []string, key string) bool {
	prefix := key + "="
	for _, entry := range extra {
		if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
