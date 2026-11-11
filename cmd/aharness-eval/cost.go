package main

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"time"
)

// usageTotals is the usage of one model response or an aggregate.
type usageTotals struct {
	Input      int64
	Output     int64
	Cached     int64
	CacheWrite int64
	Reasoning  int64
}

// rates holds prices as micro-USD per million tokens. Integer micro-USD is
// used for the final cost; the rates stay floating point so a fractional cent
// per million tokens can be represented exactly enough before rounding.
type rates struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// peakPricing is a time-of-day multiplier over UTC windows.
type peakPricing struct {
	Multiplier float64
	Windows    []utcWindow
}

// utcWindow is a [start, end) minute-of-day interval; start may exceed end for
// a window that wraps midnight.
type utcWindow struct {
	Start int
	End   int
}

// modelPrice is one price-file entry with resolved micro-USD rates.
type modelPrice struct {
	Rates rates
	Peak  *peakPricing
}

// priceTable is a parsed price file.
type priceTable struct {
	Currency string
	Unit     string
	Models   map[string]modelPrice
}

// pricingSnapshot mirrors the optional run_start.data.pricing object written by
// the runtime. All rate fields are micro-USD per million tokens.
type pricingSnapshot struct {
	Available          *bool    `json:"available"`
	Currency           string   `json:"currency"`
	Unit               string   `json:"unit"`
	InputMicroUSD      *float64 `json:"input_micro_usd"`
	OutputMicroUSD     *float64 `json:"output_micro_usd"`
	CacheReadMicroUSD  *float64 `json:"cache_read_micro_usd"`
	CacheWriteMicroUSD *float64 `json:"cache_write_micro_usd"`
	Source             string   `json:"source"`
	FetchedAt          string   `json:"fetched_at"`
}

type priceFile struct {
	Currency string                    `json:"currency"`
	Unit     string                    `json:"unit"`
	Models   map[string]priceFileModel `json:"models"`
}

type priceFileModel struct {
	Input      *float64  `json:"input"`
	Output     *float64  `json:"output"`
	CacheRead  *float64  `json:"cache_read"`
	CacheWrite *float64  `json:"cache_write"`
	Peak       *peakJSON `json:"peak"`
}

type peakJSON struct {
	Multiplier *float64     `json:"multiplier"`
	Windows    []windowJSON `json:"windows_utc"`
}

type windowJSON struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

const maxPriceBytes = 1 << 20

// loadPriceFile reads a price file. An empty path disables price-table lookup.
func loadPriceFile(path string) (*priceTable, error) {
	if path == "" {
		return nil, nil
	}
	data, err := readLimited(path, maxPriceBytes)
	if err != nil {
		return nil, fmt.Errorf("read price file: %w", err)
	}
	var raw priceFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode price file %s: %w", path, err)
	}
	if raw.Currency == "" {
		return nil, fmt.Errorf("price file %s: currency is required", path)
	}
	if raw.Unit != "per_million_tokens" {
		return nil, fmt.Errorf("price file %s: unit %q, want per_million_tokens", path, raw.Unit)
	}
	if len(raw.Models) == 0 {
		return nil, fmt.Errorf("price file %s: no models", path)
	}
	table := &priceTable{
		Currency: raw.Currency,
		Unit:     raw.Unit,
		Models:   make(map[string]modelPrice, len(raw.Models)),
	}
	for model, entry := range raw.Models {
		if model == "" {
			return nil, fmt.Errorf("price file %s: empty model id", path)
		}
		price, err := entry.resolve(model)
		if err != nil {
			return nil, fmt.Errorf("price file %s: model %s: %w", path, model, err)
		}
		table.Models[model] = price
	}
	return table, nil
}

func (entry priceFileModel) resolve(model string) (modelPrice, error) {
	if entry.Input == nil {
		return modelPrice{}, fmt.Errorf("input rate is required")
	}
	if entry.Output == nil {
		return modelPrice{}, fmt.Errorf("output rate is required")
	}
	inputMicro, err := usdToMicro(*entry.Input, "input")
	if err != nil {
		return modelPrice{}, err
	}
	outputMicro, err := usdToMicro(*entry.Output, "output")
	if err != nil {
		return modelPrice{}, err
	}
	rates := rates{
		Input:      inputMicro,
		Output:     outputMicro,
		CacheRead:  inputMicro,
		CacheWrite: 0,
	}
	if entry.CacheRead != nil {
		rate, err := usdToMicro(*entry.CacheRead, "cache_read")
		if err != nil {
			return modelPrice{}, err
		}
		rates.CacheRead = rate
	}
	if entry.CacheWrite != nil {
		rate, err := usdToMicro(*entry.CacheWrite, "cache_write")
		if err != nil {
			return modelPrice{}, err
		}
		rates.CacheWrite = rate
	}
	price := modelPrice{Rates: rates}
	if entry.Peak != nil {
		peak, err := entry.Peak.resolve()
		if err != nil {
			return modelPrice{}, err
		}
		price.Peak = peak
	}
	return price, nil
}

func usdToMicro(value float64, field string) (float64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("%s rate must be a non-negative finite number", field)
	}
	return value * 1e6, nil
}

func (peak peakJSON) resolve() (*peakPricing, error) {
	if peak.Multiplier == nil {
		return nil, errors.New("peak multiplier is required")
	}
	multiplier := *peak.Multiplier
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 {
		return nil, fmt.Errorf("peak multiplier %v must be positive and finite", multiplier)
	}
	if len(peak.Windows) == 0 {
		return nil, errors.New("peak windows_utc is required")
	}
	windows := make([]utcWindow, 0, len(peak.Windows))
	for i, window := range peak.Windows {
		start, err := parseClock(window.Start)
		if err != nil {
			return nil, fmt.Errorf("peak window %d start: %w", i, err)
		}
		end, err := parseClock(window.End)
		if err != nil {
			return nil, fmt.Errorf("peak window %d end: %w", i, err)
		}
		if start == end {
			return nil, fmt.Errorf("peak window %d is empty", i)
		}
		windows = append(windows, utcWindow{Start: start, End: end})
	}
	return &peakPricing{Multiplier: multiplier, Windows: windows}, nil
}

func parseClock(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, fmt.Errorf("invalid time %q: want HH:MM", value)
	}
	hour, err := parseTwoDigits(value[:2])
	if err != nil {
		return 0, fmt.Errorf("invalid time %q: %w", value, err)
	}
	minute, err := parseTwoDigits(value[3:])
	if err != nil {
		return 0, fmt.Errorf("invalid time %q: %w", value, err)
	}
	if hour > 23 || minute > 59 {
		return 0, fmt.Errorf("invalid time %q: out of range", value)
	}
	return hour*60 + minute, nil
}

func parseTwoDigits(value string) (int, error) {
	n := 0
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(value[i]-'0')
	}
	return n, nil
}

// matches reports whether ts falls inside the peak pricing windows.
func (p *peakPricing) matches(ts time.Time) bool {
	if p == nil {
		return false
	}
	utc := ts.UTC()
	minute := utc.Hour()*60 + utc.Minute()
	for _, window := range p.Windows {
		if window.Start < window.End {
			if minute >= window.Start && minute < window.End {
				return true
			}
			continue
		}
		if minute >= window.Start || minute < window.End {
			return true
		}
	}
	return false
}

// ratesAt returns the effective rates for a timestamp.
func (p modelPrice) ratesAt(ts time.Time) rates {
	if p.Peak == nil || !p.Peak.matches(ts) {
		return p.Rates
	}
	multiplier := p.Peak.Multiplier
	r := p.Rates
	r.Input *= multiplier
	r.Output *= multiplier
	r.CacheRead *= multiplier
	r.CacheWrite *= multiplier
	return r
}

// cost returns the integer micro-USD cost for usage at model and timestamp.
// The second result is false when no price source covers the model.
func (t *priceTable) cost(model string, usage usageTotals, ts time.Time) (int64, bool) {
	if t == nil {
		return 0, false
	}
	entry, ok := t.Models[model]
	if !ok {
		return 0, false
	}
	return costMicroUSD(entry.ratesAt(ts), usage), true
}

// costRates resolves the catalog fallback snapshot into micro-USD rates.
func (p *pricingSnapshot) costRates() (rates, bool) {
	if p == nil {
		return rates{}, false
	}
	if p.Available != nil && !*p.Available {
		return rates{}, false
	}
	if p.InputMicroUSD == nil || p.OutputMicroUSD == nil {
		return rates{}, false
	}
	r := rates{
		Input:      *p.InputMicroUSD,
		Output:     *p.OutputMicroUSD,
		CacheRead:  *p.InputMicroUSD,
		CacheWrite: 0,
	}
	if r.Input < 0 || r.Output < 0 || math.IsNaN(r.Input) || math.IsNaN(r.Output) ||
		math.IsInf(r.Input, 0) || math.IsInf(r.Output, 0) {
		return rates{}, false
	}
	if p.CacheReadMicroUSD != nil {
		if *p.CacheReadMicroUSD < 0 || math.IsNaN(*p.CacheReadMicroUSD) || math.IsInf(*p.CacheReadMicroUSD, 0) {
			return rates{}, false
		}
		r.CacheRead = *p.CacheReadMicroUSD
	}
	if p.CacheWriteMicroUSD != nil {
		if *p.CacheWriteMicroUSD < 0 || math.IsNaN(*p.CacheWriteMicroUSD) || math.IsInf(*p.CacheWriteMicroUSD, 0) {
			return rates{}, false
		}
		r.CacheWrite = *p.CacheWriteMicroUSD
	}
	return r, true
}

// costMicroUSD computes round-half-up((uncached*input + cached*cache_read +
// cache_write*cache_write + output*output) / 1e6). Rates are micro-USD per
// million tokens, so the division yields integer micro-USD.
func costMicroUSD(r rates, usage usageTotals) int64 {
	uncached := usage.Input - usage.Cached - usage.CacheWrite
	if uncached < 0 {
		uncached = 0
	}
	numerator := float64(uncached)*r.Input +
		float64(usage.Cached)*r.CacheRead +
		float64(usage.CacheWrite)*r.CacheWrite +
		float64(usage.Output)*r.Output
	if numerator <= 0 {
		return 0
	}
	return int64(math.Floor(numerator/1e6 + 0.5))
}
