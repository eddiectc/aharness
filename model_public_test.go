package aharness_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eddiectc/aharness"
)

const catalogFixture = `{
  "testprovider": {
    "npm": "@ai-sdk/openai-compatible",
    "api": "https://opencode.ai/test/v1",
    "models": {
      "m1": {
        "name": "Model One",
        "tool_call": true,
        "limit": {"context": 1000, "input": 900, "output": 100},
        "modalities": {"output": ["text"]},
        "reasoning": true,
        "reasoning_options": [{"type": "effort", "values": ["low", "high"]}],
        "cost": {"input": 1.5, "output": 3.0},
        "provider": {"npm": "@ai-sdk/openai-compatible"}
      },
      "m2": {
        "name": "No Tools",
        "tool_call": false,
        "limit": {"context": 1000},
        "modalities": {"output": ["text"]},
        "provider": {"npm": "@ai-sdk/openai-compatible"}
      }
    }
  }
}`

func catalogServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("User-Agent"), "aharness-test/1.0"; got != want {
			t.Errorf("catalog User-Agent = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, catalogFixture)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestListModelsAndDiscoverModel(t *testing.T) {
	server := catalogServer(t)
	cfg := aharness.CatalogConfig{
		URL:       server.URL,
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
	}
	listing, err := aharness.ListModels(context.Background(), cfg, "testprovider")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if listing.Provider != "testprovider" || len(listing.Models) != 2 {
		t.Fatalf("listing = %+v", listing)
	}
	var usable, unavailable *aharness.CatalogModel
	for i := range listing.Models {
		switch listing.Models[i].ID {
		case "m1":
			usable = &listing.Models[i]
		case "m2":
			unavailable = &listing.Models[i]
		}
	}
	if usable == nil || !usable.ToolCall || usable.ContextWindow != 1000 || usable.MaxOutputTokens != 100 {
		t.Fatalf("usable entry = %+v", usable)
	}
	if !usable.Pricing.Available {
		t.Fatalf("usable pricing = %+v", usable.Pricing)
	}
	if len(usable.ThinkingEfforts) != 2 {
		t.Fatalf("usable efforts = %v", usable.ThinkingEfforts)
	}
	if unavailable == nil || unavailable.Unavailable == "" {
		t.Fatalf("unavailable entry = %+v", unavailable)
	}

	model, err := aharness.DiscoverModel(context.Background(), aharness.DiscoverModelOptions{
		Catalog:  cfg,
		Provider: "testprovider",
		Model:    "m1",
		APIKey:   "secret",
		BaseURL:  server.URL + "/v1",
	})
	if err != nil {
		t.Fatalf("DiscoverModel: %v", err)
	}
	if model.Spec.ID != "m1" || model.Spec.Dialect != aharness.DialectChat || model.Spec.BaseURL != server.URL+"/v1" {
		t.Fatalf("discovered model = %+v", model.Spec)
	}
	if model.Completer == nil {
		t.Fatal("discovered model has no completer")
	}
	if _, err := aharness.DiscoverModel(context.Background(), aharness.DiscoverModelOptions{
		Catalog:  cfg,
		Provider: "testprovider",
		Model:    "m2",
	}); err == nil {
		t.Fatal("DiscoverModel accepted an unusable entry")
	}
}

func TestOpenAIModelRunsAndClassifiesProviderError(t *testing.T) {
	var sawAuth, sawUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"chatcmpl","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"adapter hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer server.Close()

	model, err := aharness.OpenAIModel(aharness.OpenAIModelOptions{
		Spec: aharness.ModelSpec{
			ID:            "openai-test",
			Provider:      "test",
			Dialect:       aharness.DialectChat,
			BaseURL:       server.URL,
			ContextWindow: 10_000,
		},
		APIKey:     "token-1",
		UserAgent:  "aharness-test/1.0",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("OpenAIModel: %v", err)
	}
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
		Model:     model,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	result, err := ag.Run(context.Background(), "hello", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != aharness.StopCompleted || result.FinalText != "adapter hello" {
		t.Fatalf("result = %+v", result)
	}
	if sawAuth != "Bearer token-1" {
		t.Fatalf("authorization = %q", sawAuth)
	}
	if sawUserAgent != "aharness-test/1.0" {
		t.Fatalf("User-Agent = %q, want aharness-test/1.0", sawUserAgent)
	}

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer failServer.Close()
	failModel, err := aharness.OpenAIModel(aharness.OpenAIModelOptions{
		Spec: aharness.ModelSpec{
			ID:            "openai-fail",
			Dialect:       aharness.DialectChat,
			BaseURL:       failServer.URL,
			ContextWindow: 10_000,
		},
		UserAgent:  "aharness-test/1.0",
		HTTPClient: failServer.Client(),
	})
	if err != nil {
		t.Fatalf("OpenAIModel(fail): %v", err)
	}
	failAgent, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  t.TempDir(),
		UserAgent: "aharness-test/1.0",
		Model:     failModel,
	})
	if err != nil {
		t.Fatalf("New(fail): %v", err)
	}
	defer func() { _ = failAgent.Close() }()
	failResult, failErr := failAgent.Run(context.Background(), "hello", aharness.RunOptions{})
	if failResult.Status != aharness.StopProviderError {
		t.Fatalf("failure status = %q (err %v)", failResult.Status, failErr)
	}
	var providerErr *aharness.ProviderError
	if !errors.As(failErr, &providerErr) {
		t.Fatalf("failure error = %v, want *ProviderError", failErr)
	}
	if providerErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("provider status = %d", providerErr.StatusCode)
	}
}

func TestOpenAIModelRequiresUserAgent(t *testing.T) {
	_, err := aharness.OpenAIModel(aharness.OpenAIModelOptions{
		Spec: aharness.ModelSpec{
			ID:            "openai-test",
			Dialect:       aharness.DialectChat,
			BaseURL:       "https://provider.example",
			ContextWindow: 10_000,
		},
	})
	var configErr *aharness.ConfigError
	if !errors.As(err, &configErr) || configErr.Field != "Model.UserAgent" {
		t.Fatalf("error = %v, want ConfigError for Model.UserAgent", err)
	}
}

func TestCatalogRequiresUserAgent(t *testing.T) {
	_, err := aharness.ListModels(context.Background(), aharness.CatalogConfig{
		URL:      "https://models.dev/api.json",
		StateDir: t.TempDir(),
	}, "testprovider")
	var configErr *aharness.ConfigError
	if !errors.As(err, &configErr) || configErr.Field != "Catalog.UserAgent" {
		t.Fatalf("error = %v, want ConfigError for Catalog.UserAgent", err)
	}
}
