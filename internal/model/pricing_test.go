package model

import (
	"math"
	"testing"
	"time"
)

func TestNormalizeCatalogCost(t *testing.T) {
	tests := []struct {
		name string
		raw  *modelsDevCost
		want *CatalogCost
	}{
		{name: "nil", raw: nil, want: nil},
		{
			name: "input and output only",
			raw:  &modelsDevCost{Input: new(0.15), Output: new(0.6)},
			want: &CatalogCost{InputMicroUSD: 150000, OutputMicroUSD: 600000},
		},
		{
			name: "all tiers",
			raw: &modelsDevCost{
				Input: new(0.15), Output: new(0.6),
				CacheRead: new(0.003), CacheWrite: new(0.18),
			},
			want: &CatalogCost{
				InputMicroUSD: 150000, OutputMicroUSD: 600000,
				CacheReadMicroUSD: new(int64(3000)), CacheWriteMicroUSD: new(int64(180000)),
			},
		},
		{
			name: "rounds half up",
			raw:  &modelsDevCost{Input: new(0.0000005), Output: new(float64(0))},
			want: &CatalogCost{InputMicroUSD: 1, OutputMicroUSD: 0},
		},
		{name: "missing output", raw: &modelsDevCost{Input: new(0.1)}, want: nil},
		{name: "negative input", raw: &modelsDevCost{Input: new(-0.1), Output: new(0.1)}, want: nil},
		{name: "infinite output", raw: &modelsDevCost{Input: new(0.1), Output: new(math.Inf(1))}, want: nil},
		{name: "nan cache read", raw: &modelsDevCost{Input: new(0.1), Output: new(0.1), CacheRead: new(math.NaN())}, want: nil},
		{name: "negative cache write", raw: &modelsDevCost{Input: new(0.1), Output: new(0.1), CacheWrite: new(float64(-1))}, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeCatalogCost(tc.raw)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("normalizeCatalogCost() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("normalizeCatalogCost() = nil, want cost")
			}
			if got.InputMicroUSD != tc.want.InputMicroUSD || got.OutputMicroUSD != tc.want.OutputMicroUSD {
				t.Errorf("rates = %+v, want %+v", got, tc.want)
			}
			comparePtr := func(name string, got, want *int64) {
				t.Helper()
				if (got == nil) != (want == nil) {
					t.Errorf("%s = %v, want %v", name, got, want)
					return
				}
				if got != nil && *got != *want {
					t.Errorf("%s = %d, want %d", name, *got, *want)
				}
			}
			comparePtr("cache_read", got.CacheReadMicroUSD, tc.want.CacheReadMicroUSD)
			comparePtr("cache_write", got.CacheWriteMicroUSD, tc.want.CacheWriteMicroUSD)
		})
	}
}

func TestCatalogModelPricing(t *testing.T) {
	fetched := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	model := CatalogModel{Cost: &CatalogCost{
		InputMicroUSD: 150000, OutputMicroUSD: 600000,
		CacheReadMicroUSD: new(int64(3000)), CacheWriteMicroUSD: new(int64(180000)),
	}}
	got := model.Pricing("https://models.dev/api.json", fetched)
	want := Pricing{
		Available: true, Currency: "USD", Unit: "per_million_tokens",
		InputMicroUSD: 150000, OutputMicroUSD: 600000,
		CacheReadMicroUSD: 3000, CacheWriteMicroUSD: 180000,
		Source: "https://models.dev/api.json", FetchedAt: fetched,
	}
	if got != want {
		t.Errorf("Pricing() = %+v, want %+v", got, want)
	}

	noCache := CatalogModel{Cost: &CatalogCost{InputMicroUSD: 150000, OutputMicroUSD: 600000}}
	got = noCache.Pricing("source", fetched)
	if got.CacheReadMicroUSD != 150000 {
		t.Errorf("cache read fallback = %d, want input rate 150000", got.CacheReadMicroUSD)
	}
	if got.CacheWriteMicroUSD != 0 {
		t.Errorf("cache write fallback = %d, want 0", got.CacheWriteMicroUSD)
	}

	if got := (CatalogModel{}).Pricing("source", fetched); got.Available {
		t.Errorf("Pricing() for a model without cost = %+v, want unavailable", got)
	}
}

func TestPricingCost(t *testing.T) {
	pricing := Pricing{
		Available:     true,
		InputMicroUSD: 150000, OutputMicroUSD: 600000,
		CacheReadMicroUSD: 3000, CacheWriteMicroUSD: 180000,
	}
	tests := []struct {
		name  string
		usage Usage
		want  int64
	}{
		{
			name:  "uncached only",
			usage: Usage{InputTokens: 1000, OutputTokens: 0},
			want:  150,
		},
		{
			name: "all tiers",
			usage: Usage{
				InputTokens: 1000, OutputTokens: 200,
				CachedInputTokens: 400, CacheWriteInputTokens: 100,
			},
			want: 214,
		},
		{
			name:  "reasoning is not charged twice",
			usage: Usage{InputTokens: 0, OutputTokens: 1000, ReasoningOutputTokens: 900},
			want:  600,
		},
		{
			name:  "half up rounding",
			usage: Usage{InputTokens: 10, OutputTokens: 0},
			want:  2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pricing.Cost(tc.usage)
			if !ok {
				t.Fatal("Cost() ok = false, want true")
			}
			if got != tc.want {
				t.Errorf("Cost() = %d, want %d", got, tc.want)
			}
		})
	}

	if _, ok := (Pricing{}).Cost(Usage{InputTokens: 1}); ok {
		t.Error("Cost() for unavailable pricing ok = true, want false")
	}
	if cost, ok := pricing.Cost(Usage{Reported: true}); !ok || cost != 0 {
		t.Errorf("Cost() for present zero usage = %d/%v, want 0/true", cost, ok)
	}
}

func TestPricingCostOverflow(t *testing.T) {
	pricing := Pricing{Available: true, InputMicroUSD: math.MaxInt64}
	if _, ok := pricing.Cost(Usage{InputTokens: 2}); ok {
		t.Error("Cost() overflow ok = true, want false")
	}
}

func TestCatalogCostValid(t *testing.T) {
	tests := []struct {
		cost CatalogCost
		want bool
	}{
		{CatalogCost{}, true},
		{CatalogCost{InputMicroUSD: 1}, true},
		{CatalogCost{InputMicroUSD: -1}, false},
		{CatalogCost{OutputMicroUSD: -1}, false},
		{CatalogCost{CacheReadMicroUSD: new(int64(-1))}, false},
		{CatalogCost{CacheWriteMicroUSD: new(int64(-1))}, false},
	}
	for _, tc := range tests {
		if got := tc.cost.valid(); got != tc.want {
			t.Errorf("valid(%+v) = %v, want %v", tc.cost, got, tc.want)
		}
	}
}

func TestParseCatalogCost(t *testing.T) {
	data := []byte(`{
	  "opencode-go": {
	    "api": "https://opencode.ai/inference/go/openai/v1",
	    "models": {
	      "priced": {
	        "name": "Priced",
	        "tool_call": true,
	        "limit": {"context": 1000},
	        "modalities": {"output": ["text"]},
	        "cost": {"input": 0.15, "output": 0.6, "cache_read": 0.003, "cache_write": 0.18}
	      },
	      "unpriced": {
	        "name": "Unpriced",
	        "tool_call": true,
	        "limit": {"context": 1000},
	        "modalities": {"output": ["text"]}
	      },
	      "bad": {
	        "name": "Bad",
	        "tool_call": true,
	        "limit": {"context": 1000},
	        "modalities": {"output": ["text"]},
	        "cost": {"input": -1, "output": 0.6}
	      }
	    }
	  }
	}`)
	cat, err := parseCatalog(data, "opencode-go", "https://models.dev/api.json")
	if err != nil {
		t.Fatalf("parseCatalog() error = %v", err)
	}
	priced := cat.Models["priced"]
	if priced.Cost == nil {
		t.Fatal("priced.Cost = nil, want cost")
	}
	if priced.Cost.InputMicroUSD != 150000 || priced.Cost.OutputMicroUSD != 600000 {
		t.Errorf("priced cost = %+v", priced.Cost)
	}
	if unpriced := cat.Models["unpriced"]; unpriced.Cost != nil {
		t.Errorf("unpriced.Cost = %+v, want nil", unpriced.Cost)
	}
	if bad := cat.Models["bad"]; bad.Cost != nil {
		t.Errorf("bad.Cost = %+v, want nil for invalid rates", bad.Cost)
	}
}
