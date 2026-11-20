package model

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// effortCatalogModel is a usable model body with reasoning enabled. Callers
// append reasoning_options.
const effortCatalogModel = `"name":"Effort Model","tool_call":true,"limit":{"context":1000},"modalities":{"output":["text"]},"reasoning":true`

func effortCatalogBody(modelJSON string) []byte {
	return []byte(`{"opencode-go":{"npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/go/openai/v1","models":{"m":{` + modelJSON + `}}}}`)
}

func parseEffortCatalog(t *testing.T, modelJSON string) CatalogModel {
	t.Helper()
	catalog, err := parseCatalog(effortCatalogBody(modelJSON), testProvider, DefaultCatalogURL)
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	return catalog.Models["m"]
}

func TestValidReasoningEffort(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"single letter", "a", true},
		{"canonical", "high", true},
		{"lowercase digits and separators", "gpt-x_2", true},
		{"unknown value", "very-high", true},
		{"exactly max bytes", strings.Repeat("a", MaxThinkingEffortBytes), true},
		{"over max bytes", strings.Repeat("a", MaxThinkingEffortBytes+1), false},
		{"empty", "", false},
		{"reserved default", "default", false},
		{"leading digit", "1high", false},
		{"leading underscore", "_high", false},
		{"leading hyphen", "-high", false},
		{"uppercase", "High", false},
		{"inner space", "high low", false},
		{"trailing newline", "high\n", false},
		{"control character", "high\x01", false},
		{"non-ascii", "café", false},
		{"invalid utf8", string([]byte{0xff, 'a'}), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidReasoningEffort(tc.value); got != tc.want {
				t.Errorf("ValidReasoningEffort(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestCatalogReasoningNormalization(t *testing.T) {
	tests := []struct {
		name       string
		options    string
		want       []string
		wantUsable bool
	}{
		{
			name:       "effort values are trimmed deduped and ordered",
			options:    `[{"type":"effort","values":[" high ","low","high","default","High","very-high","medium"]}]`,
			want:       []string{"low", "medium", "high", "very-high"},
			wantUsable: true,
		},
		{
			name:       "multiple effort options flatten",
			options:    `[{"type":"effort","values":["max","low"]},{"type":"effort","values":["high","none"]}]`,
			want:       []string{"none", "low", "high", "max"},
			wantUsable: true,
		},
		{
			name:       "toggle budget and unknown types are ignored",
			options:    `[{"type":"toggle","values":["low"]},{"type":"budget_tokens","values":["high"],"max":1000},{"type":"mystery","values":["max"]}]`,
			want:       nil,
			wantUsable: true,
		},
		{
			name:       "empty options",
			options:    `[]`,
			want:       nil,
			wantUsable: true,
		},
		{
			name:       "malformed object shape",
			options:    `{}`,
			want:       nil,
			wantUsable: true,
		},
		{
			name:       "malformed string shape",
			options:    `"effort"`,
			want:       nil,
			wantUsable: true,
		},
		{
			name:       "malformed number shape",
			options:    `42`,
			want:       nil,
			wantUsable: true,
		},
		{
			name:       "null options",
			options:    `null`,
			want:       nil,
			wantUsable: true,
		},
		{
			name:       "invalid syntax is dropped",
			options:    `[{"type":"effort","values":["","High","high low","high\u0001","café","a b"]}]`,
			want:       nil,
			wantUsable: true,
		},
		{
			name:       "oversized values are dropped",
			options:    `[{"type":"effort","values":["` + strings.Repeat("a", MaxThinkingEffortBytes) + `","` + strings.Repeat("b", MaxThinkingEffortBytes+1) + `"]}]`,
			want:       []string{strings.Repeat("a", MaxThinkingEffortBytes)},
			wantUsable: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := parseEffortCatalog(t, effortCatalogModel+`,"reasoning_options":`+tc.options)
			if model.Usable() != tc.wantUsable {
				t.Errorf("Usable() = %v, want %v (unavailable: %q)", model.Usable(), tc.wantUsable, model.Unavailable)
			}
			if !model.Reasoning {
				t.Error("Reasoning = false, want true")
			}
			if !slices.Equal(model.ReasoningEfforts, tc.want) {
				t.Errorf("ReasoningEfforts = %q, want %q", model.ReasoningEfforts, tc.want)
			}
		})
	}
}

func TestCatalogReasoningEffortCapAndOrder(t *testing.T) {
	values := []string{"high", "low", "max", "minimal", "none", "medium", "xhigh"}
	for i := range 13 {
		values = append(values, fmt.Sprintf("a%02d", i))
	}
	options, err := jsonv2.Marshal([]modelsDevReasoningOption{{Type: "effort", Values: values}})
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}
	model := parseEffortCatalog(t, effortCatalogModel+`,"reasoning_options":`+string(options))

	want := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	for i := range 9 {
		want = append(want, fmt.Sprintf("a%02d", i))
	}
	if got := len(model.ReasoningEfforts); got != MaxReasoningEfforts {
		t.Fatalf("len(ReasoningEfforts) = %d, want %d: %q", got, MaxReasoningEfforts, model.ReasoningEfforts)
	}
	if !slices.Equal(model.ReasoningEfforts, want) {
		t.Errorf("ReasoningEfforts = %q, want %q", model.ReasoningEfforts, want)
	}
}

func TestCatalogReasoningMetadataRetainedWhenUnavailable(t *testing.T) {
	model := parseEffortCatalog(t, `"name":"Unavailable","tool_call":false,"limit":{"context":1000},"modalities":{"output":["text"]},"reasoning":true,"reasoning_options":[{"type":"effort","values":["high","low"]}]`)
	if model.Usable() {
		t.Fatal("Usable() = true, want false")
	}
	if !model.Reasoning {
		t.Error("Reasoning = false, want true for an unavailable model")
	}
	if want := []string{"low", "high"}; !slices.Equal(model.ReasoningEfforts, want) {
		t.Errorf("ReasoningEfforts = %q, want %q", model.ReasoningEfforts, want)
	}
}

func TestCatalogModelReasoningJSONOmission(t *testing.T) {
	data, err := jsonv2.Marshal(CatalogModel{ID: "m", Reasoning: true})
	if err != nil {
		t.Fatalf("marshal model: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := jsonv2.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode model: %v", err)
	}
	if _, present := raw["reasoning_efforts"]; present {
		t.Errorf("reasoning_efforts = %s, want omitted for an empty list", raw["reasoning_efforts"])
	}
	if got := string(raw["reasoning"]); got != "true" {
		t.Errorf("reasoning = %s, want true", got)
	}

	data, err = jsonv2.Marshal(CatalogModel{ID: "m", Reasoning: true, ReasoningEfforts: []string{"low", "high"}})
	if err != nil {
		t.Fatalf("marshal model with efforts: %v", err)
	}
	if err := jsonv2.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode model with efforts: %v", err)
	}
	var efforts []string
	if err := jsonv2.Unmarshal(raw["reasoning_efforts"], &efforts); err != nil {
		t.Fatalf("decode efforts: %v", err)
	}
	if want := []string{"low", "high"}; !slices.Equal(efforts, want) {
		t.Errorf("reasoning_efforts = %q, want %q", efforts, want)
	}
}

func TestCatalogReasoningCacheRoundTrip(t *testing.T) {
	body := effortCatalogBody(effortCatalogModel + `,"reasoning_options":[{"type":"effort","values":["high","low"]}]`)
	fake, server := startCatalogServer(t, body)
	clock := newFakeClock()
	stateDir := t.TempDir()
	source := newCatalogSource(t, catalogOptions(server.URL, stateDir, clock))

	first, err := source.Load(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	firstModel := first.Models["m"]
	if !firstModel.Reasoning || !slices.Equal(firstModel.ReasoningEfforts, []string{"low", "high"}) {
		t.Fatalf("first model = %+v, want reasoning with low, high", firstModel)
	}

	cachePath := CatalogCachePath(stateDir, testProvider)
	var cached catalogCacheFile
	if err := jsonv2.Unmarshal(readFileBytes(t, cachePath), &cached); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if cached.CacheVersion != catalogCacheVersion {
		t.Errorf("cache version = %d, want %d", cached.CacheVersion, catalogCacheVersion)
	}
	if len(cached.Models) != 1 {
		t.Fatalf("cached models = %d, want 1", len(cached.Models))
	}
	if got := cached.Models[0]; !got.Reasoning || !slices.Equal(got.ReasoningEfforts, []string{"low", "high"}) {
		t.Errorf("cached model = %+v, want reasoning with low, high", got)
	}

	clock.Add(23 * time.Hour)
	second, err := source.Load(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if got := fake.recorded().count; got != 1 {
		t.Errorf("HTTP requests = %d, want 1 for a fresh cache", got)
	}
	secondModel := second.Models["m"]
	if !secondModel.Reasoning || !slices.Equal(secondModel.ReasoningEfforts, []string{"low", "high"}) {
		t.Errorf("cache round trip model = %+v, want reasoning with low, high", secondModel)
	}
}

func TestCatalogReasoningV1CacheRejectedAndRefetched(t *testing.T) {
	body := effortCatalogBody(effortCatalogModel + `,"reasoning_options":[{"type":"effort","values":["high","low"]}]`)
	fake, server := startCatalogServer(t, body)
	clock := newFakeClock()
	stateDir := t.TempDir()

	parsed, err := parseCatalog(body, testProvider, server.URL)
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	cachePath := CatalogCachePath(stateDir, testProvider)
	writeFileMode(t, cachePath, marshalCache(t, catalogCacheFile{
		CacheVersion: 1,
		Source:       server.URL,
		Provider:     testProvider,
		FetchedAt:    clock.Now().Add(-time.Hour).Format(time.RFC3339),
		ETag:         `"v1"`,
		Models:       parsed.SortedModels(),
	}), 0o600)

	source := newCatalogSource(t, catalogOptions(server.URL, stateDir, clock))
	catalog, err := source.Load(t.Context(), testProvider)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	recorded := fake.recorded()
	if recorded.count != 1 {
		t.Errorf("HTTP requests = %d, want 1 for a rejected v1 cache", recorded.count)
	}
	if recorded.ifNoneMatch[0] != "" {
		t.Errorf("If-None-Match = %q, want empty for a rejected cache", recorded.ifNoneMatch[0])
	}
	if got := catalog.Models["m"]; !slices.Equal(got.ReasoningEfforts, []string{"low", "high"}) {
		t.Errorf("loaded efforts = %q, want low, high", got.ReasoningEfforts)
	}

	var rewritten catalogCacheFile
	if err := jsonv2.Unmarshal(readFileBytes(t, cachePath), &rewritten); err != nil {
		t.Fatalf("decode rewritten cache: %v", err)
	}
	if rewritten.CacheVersion != catalogCacheVersion {
		t.Errorf("rewritten cache version = %d, want %d", rewritten.CacheVersion, catalogCacheVersion)
	}
	if len(rewritten.Models) != 1 || !slices.Equal(rewritten.Models[0].ReasoningEfforts, []string{"low", "high"}) {
		t.Errorf("rewritten cache models = %+v, want low, high", rewritten.Models)
	}
}

func TestCatalogReasoningTamperedCacheRejected(t *testing.T) {
	body := effortCatalogBody(effortCatalogModel + `,"reasoning_options":[{"type":"effort","values":["high","low"]}]`)
	parsed, err := parseCatalog(body, testProvider, DefaultCatalogURL)
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	base := parsed.Models["m"]

	tooMany := make([]string, 0, MaxReasoningEfforts+1)
	for i := range MaxReasoningEfforts + 1 {
		tooMany = append(tooMany, fmt.Sprintf("z%02d", i))
	}
	tests := []struct {
		name   string
		mutate func([]string) []string
	}{
		{"unsorted", func([]string) []string { return []string{"high", "low"} }},
		{"duplicate", func([]string) []string { return []string{"low", "low"} }},
		{"invalid syntax", func([]string) []string { return []string{"High"} }},
		{"reserved default", func([]string) []string { return []string{"default"} }},
		{"too many", func([]string) []string { return tooMany }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, server := startCatalogServer(t, body)
			clock := newFakeClock()
			stateDir := t.TempDir()

			tampered := base
			tampered.ReasoningEfforts = tc.mutate(slices.Clone(base.ReasoningEfforts))
			cachePath := CatalogCachePath(stateDir, testProvider)
			writeFileMode(t, cachePath, marshalCache(t, catalogCacheFile{
				CacheVersion: catalogCacheVersion,
				Source:       server.URL,
				Provider:     testProvider,
				FetchedAt:    clock.Now().Add(-time.Hour).Format(time.RFC3339),
				ETag:         `"old"`,
				Models:       []CatalogModel{tampered},
			}), 0o600)

			source := newCatalogSource(t, catalogOptions(server.URL, stateDir, clock))
			catalog, err := source.Load(t.Context(), testProvider)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			recorded := fake.recorded()
			if recorded.count != 1 {
				t.Errorf("HTTP requests = %d, want 1 for a rejected cache", recorded.count)
			}
			if recorded.ifNoneMatch[0] != "" {
				t.Errorf("If-None-Match = %q, want empty for a rejected cache", recorded.ifNoneMatch[0])
			}
			if got := catalog.Models["m"]; !slices.Equal(got.ReasoningEfforts, []string{"low", "high"}) {
				t.Errorf("loaded efforts = %q, want low, high", got.ReasoningEfforts)
			}

			var rewritten catalogCacheFile
			if err := jsonv2.Unmarshal(readFileBytes(t, cachePath), &rewritten); err != nil {
				t.Fatalf("decode rewritten cache: %v", err)
			}
			if rewritten.CacheVersion != catalogCacheVersion {
				t.Errorf("rewritten cache version = %d, want %d", rewritten.CacheVersion, catalogCacheVersion)
			}
			if len(rewritten.Models) != 1 || !slices.Equal(rewritten.Models[0].ReasoningEfforts, []string{"low", "high"}) {
				t.Errorf("rewritten cache models = %+v, want low, high", rewritten.Models)
			}
		})
	}
}

func TestOpenAIChatReasoningEffortBody(t *testing.T) {
	const responseBody = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	tests := []struct {
		name      string
		effort    string
		streaming bool
	}{
		{"buffered with effort", "high", false},
		{"streaming with effort", "high", true},
		{"buffered without effort", "", false},
		{"streaming without effort", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ = readRequestBody(t, r)
				serveJSON(t, w, http.StatusOK, responseBody)
			}))
			t.Cleanup(srv.Close)

			adapter := NewOpenAIChat(srv.URL, testToken, testSessionID, testVersion, 0)
			req := Request{Model: "deepseek-v4.1-flash", ThinkingEffort: tc.effort}
			if tc.streaming {
				if _, err := adapter.CompleteStream(t.Context(), req, StreamHandler{}); err != nil {
					t.Fatalf("CompleteStream() error = %v", err)
				}
			} else if _, err := adapter.Complete(t.Context(), req); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}

			var raw map[string]json.RawMessage
			if err := jsonv2.Unmarshal(body, &raw); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if tc.effort == "" {
				if _, present := raw["reasoning_effort"]; present {
					t.Errorf("reasoning_effort = %s, want absent", raw["reasoning_effort"])
				}
			} else if got := string(raw["reasoning_effort"]); got != `"`+tc.effort+`"` {
				t.Errorf("reasoning_effort = %s, want %q", got, tc.effort)
			}
			if _, present := raw["reasoning"]; present {
				t.Errorf("nested reasoning = %s, want absent in Chat", raw["reasoning"])
			}
			if tc.streaming && string(raw["stream"]) != "true" {
				t.Errorf("stream = %s, want true", raw["stream"])
			}
		})
	}
}

func TestOpenAIResponsesReasoningEffortBody(t *testing.T) {
	const responseBody = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
	tests := []struct {
		name      string
		effort    string
		streaming bool
	}{
		{"buffered with effort", "high", false},
		{"streaming with effort", "high", true},
		{"buffered without effort", "", false},
		{"streaming without effort", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ = readRequestBody(t, r)
				serveJSON(t, w, http.StatusOK, responseBody)
			}))
			t.Cleanup(srv.Close)

			adapter := NewOpenAIResponses(srv.URL, testToken, testSessionID, testVersion, 0)
			req := Request{Model: "crouton-alpha-free", ThinkingEffort: tc.effort}
			if tc.streaming {
				if _, err := adapter.CompleteStream(t.Context(), req, StreamHandler{}); err != nil {
					t.Fatalf("CompleteStream() error = %v", err)
				}
			} else if _, err := adapter.Complete(t.Context(), req); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}

			var raw map[string]json.RawMessage
			if err := jsonv2.Unmarshal(body, &raw); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if tc.effort == "" {
				if _, present := raw["reasoning"]; present {
					t.Errorf("reasoning = %s, want absent", raw["reasoning"])
				}
			} else {
				var nested struct {
					Effort  string `json:"effort"`
					Summary string `json:"summary"`
				}
				if err := jsonv2.Unmarshal(raw["reasoning"], &nested); err != nil {
					t.Fatalf("decode reasoning: %v", err)
				}
				if nested.Effort != tc.effort {
					t.Errorf("reasoning.effort = %q, want %q", nested.Effort, tc.effort)
				}
				if nested.Summary != "auto" {
					t.Errorf("reasoning.summary = %q, want %q", nested.Summary, "auto")
				}
			}
			if _, present := raw["reasoning_effort"]; present {
				t.Errorf("top-level reasoning_effort = %s, want absent in Responses", raw["reasoning_effort"])
			}
			if tc.streaming && string(raw["stream"]) != "true" {
				t.Errorf("stream = %s, want true", raw["stream"])
			}
		})
	}
}
