package main

import (
	"path/filepath"
	"testing"
	"time"

	"aharness/internal/session"
)

func TestUsageAvailable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		usage *usageJSON
		want  bool
	}{
		{"nil", nil, false},
		{"reported zero", &usageJSON{Reported: new(true)}, true},
		{"unreported zero", &usageJSON{}, false},
		{"reported false zero", &usageJSON{Reported: new(false)}, false},
		{"old nonzero", &usageJSON{InputTokens: 5}, true},
		{"fake nonzero totals", &usageJSON{OutputTokens: 2}, true},
	}
	for _, test := range tests {
		if got := test.usage.available(); got != test.want {
			t.Errorf("%s: available() = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestReadPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		arguments string
		want      string
	}{
		{"object", `{"path":"a.go"}`, "a.go"},
		{"string encoded", `"{\"path\":\"b.go\"}"`, "b.go"},
		{"missing", `{"limit":10}`, ""},
		{"empty", ``, ""},
	}
	for _, test := range tests {
		if got := readPath([]byte(test.arguments)); got != test.want {
			t.Errorf("%s: readPath() = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestExtractMetricsFixture(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	reported := true
	base := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	events := []session.Event{
		testEvent(t, "session_start", 0, base, map[string]any{"model": "m"}),
		testEvent(t, "run_start", 0, base, map[string]any{
			"run_id": "r1", "model": "m",
			"pricing": map[string]any{
				"available": true, "currency": "USD", "unit": "per_million_tokens",
				"input_micro_usd": 150000, "output_micro_usd": 600000,
				"cache_read_micro_usd": 3000, "cache_write_micro_usd": 180000,
			},
		}),
		testEvent(t, "model_request", 1, base, map[string]any{"estimated_input_tokens": 1000}),
		testEvent(t, "model_response", 1, base.Add(time.Minute), map[string]any{
			"model": "m",
			"usage": usageJSON{
				InputTokens: 900, OutputTokens: 100, CachedInputTokens: 400,
				CacheWriteInputTokens: 50, ReasoningOutputTokens: 20, Reported: &reported,
			},
			"tool_calls": []map[string]any{{
				"id": "c1", "name": "read_file", "arguments": map[string]any{"path": "src/a.go"},
			}},
		}),
		testEvent(t, "tool_result", 1, base.Add(time.Minute), map[string]any{
			"call_id": "c1", "name": "read_file", "ok": true,
			"output": "lines 1-3 of 10\ncontinue with offset=4", "truncated": true,
			"details": map[string]any{"path": "src/a.go"},
		}),
		testEvent(t, "model_request", 2, base.Add(2*time.Minute), map[string]any{"estimated_input_tokens": 1200}),
		testEvent(t, "model_response", 2, base.Add(2*time.Minute), map[string]any{
			"model": "m",
			"tool_calls": []map[string]any{{
				"id": "c2", "name": "read_file", "arguments": map[string]any{"path": "src/a.go"},
			}},
		}),
		testEvent(t, "tool_result", 2, base.Add(2*time.Minute), map[string]any{
			"call_id": "c2", "name": "read_file", "ok": false, "error": "not found",
		}),
		testEvent(t, "model_retry", 2, base.Add(2*time.Minute), map[string]any{"attempt": 2, "class": "rate_limit"}),
		testEvent(t, "model_request", 3, base.Add(90*time.Minute), map[string]any{"estimated_input_tokens": 1000}),
		testEvent(t, "model_response", 3, base.Add(90*time.Minute), map[string]any{
			"model": "m",
			"usage": usageJSON{InputTokens: 1100, OutputTokens: 50, CachedInputTokens: 100, Reported: &reported},
		}),
		testEvent(t, "run_end", 3, base.Add(90*time.Minute), map[string]any{"status": "ok", "run_turns": 3}),
	}
	sessionPath := filepath.Join(dir, "sessions", testSessionID+".jsonl")
	writeSessionFile(t, sessionPath, events)

	parsed, err := session.Read(sessionPath)
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	metrics, err := extractMetrics(parsed, nil)
	if err != nil {
		t.Fatalf("extractMetrics: %v", err)
	}
	if metrics.Turns != 3 {
		t.Errorf("Turns = %d, want 3", metrics.Turns)
	}
	if metrics.ToolCalls != 2 {
		t.Errorf("ToolCalls = %d, want 2", metrics.ToolCalls)
	}
	if metrics.Responses != 3 || metrics.UsageResponses != 2 || metrics.MissingUsageResponses != 1 {
		t.Errorf("responses = %d usage = %d missing = %d, want 3/2/1",
			metrics.Responses, metrics.UsageResponses, metrics.MissingUsageResponses)
	}
	if metrics.InputTokens != 2000 || metrics.OutputTokens != 150 {
		t.Errorf("tokens = %d/%d, want 2000/150", metrics.InputTokens, metrics.OutputTokens)
	}
	if metrics.CachedInputTokens != 500 || metrics.CacheWriteInputTokens != 50 || metrics.ReasoningOutputTokens != 20 {
		t.Errorf("details = %d/%d/%d, want 500/50/20",
			metrics.CachedInputTokens, metrics.CacheWriteInputTokens, metrics.ReasoningOutputTokens)
	}
	if metrics.ReadCalls != 2 || metrics.RepeatedReads != 1 || metrics.UnattributedReads != 0 {
		t.Errorf("reads = %d repeated = %d unattributed = %d, want 2/1/0",
			metrics.ReadCalls, metrics.RepeatedReads, metrics.UnattributedReads)
	}
	if len(metrics.ReadCallsPerFile) != 1 || metrics.ReadCallsPerFile[0] != (fileCallCount{Path: "src/a.go", Calls: 2}) {
		t.Errorf("ReadCallsPerFile = %+v, want src/a.go:2", metrics.ReadCallsPerFile)
	}
	if metrics.TruncatedResults != 1 || metrics.ContinuationResults != 1 {
		t.Errorf("truncated = %d continuations = %d, want 1/1",
			metrics.TruncatedResults, metrics.ContinuationResults)
	}
	if metrics.Retries != 1 || metrics.ToolErrors != 1 {
		t.Errorf("retries = %d tool errors = %d, want 1/1", metrics.Retries, metrics.ToolErrors)
	}
	if !metrics.CostAvailable || metrics.CostComplete || metrics.CostResponses != 2 {
		t.Errorf("cost available = %v complete = %v responses = %d, want true/false/2",
			metrics.CostAvailable, metrics.CostComplete, metrics.CostResponses)
	}
	if metrics.CostMicroUSD != 318 {
		t.Errorf("CostMicroUSD = %d, want 318", metrics.CostMicroUSD)
	}
	if metrics.EstimatorPairs != 2 || metrics.EstimatorSkipped != 1 {
		t.Errorf("estimator pairs = %d skipped = %d, want 2/1",
			metrics.EstimatorPairs, metrics.EstimatorSkipped)
	}
	if metrics.EstimatorAbsErrorSum != 200 || metrics.EstimatorEstimatedSum != 2000 || metrics.EstimatorActualSum != 2000 {
		t.Errorf("estimator sums = %v/%d/%d, want 200/2000/2000",
			metrics.EstimatorAbsErrorSum, metrics.EstimatorEstimatedSum, metrics.EstimatorActualSum)
	}
	if metrics.RunStatus != "ok" || metrics.Model != "m" {
		t.Errorf("run status/model = %q/%q, want ok/m", metrics.RunStatus, metrics.Model)
	}
	if got := metrics.estimatorMeanAbs(); got == nil || *got != 100 {
		t.Errorf("estimator mean abs = %v, want 100", got)
	}
	if got := metrics.estimatorRatio(); got == nil || *got != 1 {
		t.Errorf("estimator ratio = %v, want 1", got)
	}
}

func TestExtractMetricsPriceFileWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	reported := true
	base := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	prices := mustPriceTable(t, `{
		"currency": "USD",
		"unit": "per_million_tokens",
		"models": {"m": {"input": 2.0, "output": 4.0, "cache_read": 0.5, "cache_write": 1.0}}
	}`)
	events := []session.Event{
		testEvent(t, "run_start", 0, base, map[string]any{
			"model": "m",
			"pricing": map[string]any{
				"available": true, "currency": "USD", "unit": "per_million_tokens",
				"input_micro_usd": 100000, "output_micro_usd": 100000,
			},
		}),
		testEvent(t, "model_request", 1, base, map[string]any{"estimated_input_tokens": 1000000}),
		testEvent(t, "model_response", 1, base, map[string]any{
			"model": "m",
			"usage": usageJSON{InputTokens: 1000000, Reported: &reported},
		}),
	}
	sessionPath := filepath.Join(dir, "sessions", testSessionID+".jsonl")
	writeSessionFile(t, sessionPath, events)
	parsed, err := session.Read(sessionPath)
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	metrics, err := extractMetrics(parsed, prices)
	if err != nil {
		t.Fatalf("extractMetrics: %v", err)
	}
	if metrics.CostMicroUSD != 2000000 {
		t.Errorf("CostMicroUSD = %d, want 2000000 from price file", metrics.CostMicroUSD)
	}
}

func TestExtractMetricsMissingCost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	reported := true
	base := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	events := []session.Event{
		testEvent(t, "model_response", 1, base, map[string]any{
			"model": "unknown",
			"usage": usageJSON{InputTokens: 10, OutputTokens: 5, Reported: &reported},
		}),
	}
	sessionPath := filepath.Join(dir, "sessions", testSessionID+".jsonl")
	writeSessionFile(t, sessionPath, events)
	parsed, err := session.Read(sessionPath)
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	metrics, err := extractMetrics(parsed, nil)
	if err != nil {
		t.Fatalf("extractMetrics: %v", err)
	}
	if metrics.CostAvailable || metrics.CostComplete || metrics.CostResponses != 0 {
		t.Errorf("cost available = %v complete = %v responses = %d, want false/false/0",
			metrics.CostAvailable, metrics.CostComplete, metrics.CostResponses)
	}
}
