package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCostMicroUSDRounding(t *testing.T) {
	t.Parallel()
	r := rates{Input: 500000, Output: 500000, CacheRead: 500000, CacheWrite: 500000}
	tests := []struct {
		name  string
		usage usageTotals
		want  int64
	}{
		{"zero", usageTotals{}, 0},
		{"half rounds up", usageTotals{Input: 1}, 1},
		// uncached=0, cached=1 -> 0.5 micro-USD, rounds up to 1.
		{"cached half rounds up", usageTotals{Input: 1, Cached: 1}, 1},
	}
	for _, test := range tests {
		if got := costMicroUSD(r, test.usage); got != test.want {
			t.Errorf("%s: costMicroUSD = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestCostMicroUSDMath(t *testing.T) {
	t.Parallel()
	r := rates{Input: 150000, Output: 600000, CacheRead: 3000, CacheWrite: 180000}
	usage := usageTotals{Input: 1000, Output: 200, Cached: 400, CacheWrite: 100}
	const want = 214
	if got := costMicroUSD(r, usage); got != want {
		t.Errorf("costMicroUSD = %d, want %d", got, want)
	}
}

func TestCostMicroUSDNeverNegative(t *testing.T) {
	t.Parallel()
	r := rates{Input: 1000000}
	usage := usageTotals{Input: 10, Cached: 20, CacheWrite: 30}
	if got := costMicroUSD(r, usage); got != 0 {
		t.Errorf("costMicroUSD = %d, want 0 when cached exceeds input", got)
	}
}

const testPrices = `{
	"currency": "USD",
	"unit": "per_million_tokens",
	"models": {
		"m": {
			"input": 1.0,
			"output": 2.0,
			"cache_read": 0.1,
			"cache_write": 0.2,
			"peak": {
				"multiplier": 2.0,
				"windows_utc": [{"start": "23:00", "end": "01:30"}]
			}
		}
	}
}`

func TestPriceTablePeakWindows(t *testing.T) {
	t.Parallel()
	table := mustPriceTable(t, testPrices)
	usage := usageTotals{Input: 1000000}
	tests := []struct {
		name string
		ts   time.Time
		want int64
	}{
		{"midday", time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), 1000000},
		{"window start", time.Date(2026, 11, 11, 23, 0, 0, 0, time.UTC), 2000000},
		{"after midnight", time.Date(2026, 11, 12, 0, 30, 0, 0, time.UTC), 2000000},
		{"window end exclusive", time.Date(2026, 11, 12, 1, 30, 0, 0, time.UTC), 1000000},
	}
	for _, test := range tests {
		cost, ok := table.cost("m", usage, test.ts)
		if !ok {
			t.Fatalf("%s: model not found", test.name)
		}
		if cost != test.want {
			t.Errorf("%s: cost = %d, want %d", test.name, cost, test.want)
		}
	}
}

func TestPriceTablePeakUsesUTC(t *testing.T) {
	t.Parallel()
	table := mustPriceTable(t, testPrices)
	usage := usageTotals{Input: 1}
	// 23:30 at UTC+2 is 21:30 UTC: outside the peak window.
	ts := time.Date(2026, 11, 11, 23, 30, 0, 0, time.FixedZone("plus2", 2*60*60))
	if cost, _ := table.cost("m", usage, ts); cost != 1 {
		t.Errorf("cost = %d, want 1 (base rate) because the timestamp is outside the UTC window", cost)
	}
}

func TestPriceTableCacheFallbacks(t *testing.T) {
	t.Parallel()
	table := mustPriceTable(t, `{
		"currency": "USD",
		"unit": "per_million_tokens",
		"models": {"m": {"input": 1.0, "output": 2.0}}
	}`)
	usage := usageTotals{Input: 1000, Cached: 400, CacheWrite: 100}
	// cache_read falls back to input; absent cache_write is uncharged here.
	const want = 900
	if cost, _ := table.cost("m", usage, time.Time{}); cost != want {
		t.Errorf("cost = %d, want %d", cost, want)
	}
}

func TestPriceTableLookup(t *testing.T) {
	t.Parallel()
	table := mustPriceTable(t, testPrices)
	if _, ok := table.cost("other", usageTotals{Input: 1}, time.Time{}); ok {
		t.Error("unknown model reported a price")
	}
	var nilTable *priceTable
	if _, ok := nilTable.cost("m", usageTotals{Input: 1}, time.Time{}); ok {
		t.Error("nil price table reported a price")
	}
}

func TestPricingSnapshotRates(t *testing.T) {
	t.Parallel()
	snapshot := &pricingSnapshot{
		Available:      new(true),
		InputMicroUSD:  new(150000.0),
		OutputMicroUSD: new(600000.0),
	}
	r, ok := snapshot.costRates()
	if !ok {
		t.Fatal("costRates not available")
	}
	if r.Input != 150000 || r.Output != 600000 || r.CacheRead != 150000 || r.CacheWrite != 0 {
		t.Errorf("rates = %+v, want input/cache_read 150000, output 600000, cache_write 0", r)
	}
	if cost := costMicroUSD(r, usageTotals{Input: 1000, Cached: 400, CacheWrite: 100, Output: 200}); cost != 255 {
		t.Errorf("cost = %d, want 255", cost)
	}

	disabled := &pricingSnapshot{Available: new(false), InputMicroUSD: new(1.0), OutputMicroUSD: new(1.0)}
	if _, ok := disabled.costRates(); ok {
		t.Error("available=false produced rates")
	}
	incomplete := &pricingSnapshot{Available: new(true), InputMicroUSD: new(1.0)}
	if _, ok := incomplete.costRates(); ok {
		t.Error("missing output rate produced rates")
	}
	var nilSnapshot *pricingSnapshot
	if _, ok := nilSnapshot.costRates(); ok {
		t.Error("nil snapshot produced rates")
	}
}

func TestLoadPriceFileValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
	}{
		{"bad json", `{`},
		{"missing currency", `{"unit":"per_million_tokens","models":{"m":{"input":1,"output":2}}}`},
		{"bad unit", `{"currency":"USD","unit":"per_token","models":{"m":{"input":1,"output":2}}}`},
		{"no models", `{"currency":"USD","unit":"per_million_tokens","models":{}}`},
		{"negative input", `{"currency":"USD","unit":"per_million_tokens","models":{"m":{"input":-1,"output":2}}}`},
		{"missing output", `{"currency":"USD","unit":"per_million_tokens","models":{"m":{"input":1}}}`},
		{"empty peak windows", `{"currency":"USD","unit":"per_million_tokens","models":{"m":{"input":1,"output":2,"peak":{"multiplier":2,"windows_utc":[]}}}}`},
		{"zero multiplier", `{"currency":"USD","unit":"per_million_tokens","models":{"m":{"input":1,"output":2,"peak":{"multiplier":0,"windows_utc":[{"start":"01:00","end":"02:00"}]}}}}`},
		{"bad clock", `{"currency":"USD","unit":"per_million_tokens","models":{"m":{"input":1,"output":2,"peak":{"multiplier":2,"windows_utc":[{"start":"1:00","end":"02:00"}]}}}}`},
		{"empty window", `{"currency":"USD","unit":"per_million_tokens","models":{"m":{"input":1,"output":2,"peak":{"multiplier":2,"windows_utc":[{"start":"01:00","end":"01:00"}]}}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "prices.json")
			writeFile(t, path, test.content, 0o644)
			if _, err := loadPriceFile(path); err == nil {
				t.Fatalf("loadPriceFile(%s) succeeded, want error", test.name)
			}
		})
	}
}

func TestLoadPriceFileDisabled(t *testing.T) {
	t.Parallel()
	table, err := loadPriceFile("")
	if err != nil {
		t.Fatalf("loadPriceFile: %v", err)
	}
	if table != nil {
		t.Fatalf("table = %+v, want nil", table)
	}
}

func TestParseClock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  int
		ok    bool
	}{
		{"00:00", 0, true},
		{"23:59", 1439, true},
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"12:3", 0, false},
		{"aa:bb", 0, false},
	}
	for _, test := range tests {
		got, err := parseClock(test.input)
		if test.ok && (err != nil || got != test.want) {
			t.Errorf("parseClock(%q) = %d, %v; want %d", test.input, got, err, test.want)
		}
		if !test.ok && err == nil {
			t.Errorf("parseClock(%q) succeeded, want error", test.input)
		}
	}
}
