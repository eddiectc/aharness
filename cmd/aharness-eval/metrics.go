package main

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"sort"
	"strings"
	"time"

	"aharness/internal/session"
)

// runMetrics is the per-run measurement extracted from one session log.
type runMetrics struct {
	SessionFound bool   `json:"session_found"`
	Model        string `json:"model,omitempty"`
	RunStatus    string `json:"run_status,omitempty"`

	Turns     int `json:"turns"`
	ToolCalls int `json:"tool_calls"`
	Responses int `json:"responses"`

	UsageResponses        int `json:"usage_responses"`
	MissingUsageResponses int `json:"missing_usage_responses"`

	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`

	ReadCalls           int             `json:"read_calls"`
	UnattributedReads   int             `json:"unattributed_reads"`
	ReadCallsPerFile    []fileCallCount `json:"read_calls_per_file"`
	RepeatedReads       int             `json:"repeated_reads"`
	TruncatedResults    int             `json:"truncated_results"`
	ContinuationResults int             `json:"continuation_results"`
	Retries             int             `json:"retries"`
	ToolErrors          int             `json:"tool_errors"`

	CostMicroUSD  int64 `json:"cost_micro_usd"`
	CostAvailable bool  `json:"cost_available"`
	CostComplete  bool  `json:"cost_complete"`
	CostResponses int   `json:"cost_responses"`

	EstimatorPairs        int     `json:"estimator_pairs"`
	EstimatorSkipped      int     `json:"estimator_skipped"`
	EstimatorAbsErrorSum  float64 `json:"estimator_abs_error_sum"`
	EstimatorEstimatedSum int64   `json:"estimator_estimated_sum"`
	EstimatorActualSum    int64   `json:"estimator_actual_sum"`
}

// fileCallCount is one row of the read-calls-per-file aggregate.
type fileCallCount struct {
	Path  string `json:"path"`
	Calls int    `json:"calls"`
}

type usageJSON struct {
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	Reported              *bool `json:"reported"`
}

// available reports whether the usage may be treated as observed. A present
// reported=true marker counts, and so do nonzero totals in old or fake logs
// that predate the flag.
func (u *usageJSON) available() bool {
	if u == nil {
		return false
	}
	if u.Reported != nil && *u.Reported {
		return true
	}
	return u.InputTokens+u.OutputTokens != 0
}

func (u *usageJSON) totals() usageTotals {
	return usageTotals{
		Input:      u.InputTokens,
		Output:     u.OutputTokens,
		Cached:     u.CachedInputTokens,
		CacheWrite: u.CacheWriteInputTokens,
		Reasoning:  u.ReasoningOutputTokens,
	}
}

type modelRequestJSON struct {
	EstimatedInputTokens int64 `json:"estimated_input_tokens"`
}

type toolCallJSON struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type modelResponseJSON struct {
	Model     string         `json:"model"`
	Usage     *usageJSON     `json:"usage"`
	ToolCalls []toolCallJSON `json:"tool_calls"`
}

type toolResultJSON struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	OK        *bool           `json:"ok"`
	Code      string          `json:"code"`
	Output    string          `json:"output"`
	Error     string          `json:"error"`
	Truncated bool            `json:"truncated"`
	Details   json.RawMessage `json:"details"`
}

type sessionStartJSON struct {
	Model string `json:"model"`
}

type runStartJSON struct {
	RunID   string           `json:"run_id"`
	Model   string           `json:"model"`
	Pricing *pricingSnapshot `json:"pricing"`
}

type runEndJSON struct {
	Status   string `json:"status"`
	RunTurns int    `json:"run_turns"`
}

type detailsJSON struct {
	Path string `json:"path"`
	File string `json:"file"`
}

type callInfo struct {
	name string
	path string
}

// extractMetrics decodes the session events defensively: usage, pricing, and
// cost details are optional and absent in old or synthetic logs.
func extractMetrics(events []session.Event, prices *priceTable) (runMetrics, error) {
	var (
		m               runMetrics
		sessionModel    string
		runModel        string
		pricing         *pricingSnapshot
		runTurns        int
		maxTurn         int
		pendingEstimate *int64
		readPaths       = make(map[string]int)
		calls           = make(map[string]callInfo)
	)
	m.SessionFound = true
	m.ReadCallsPerFile = []fileCallCount{}
	for i, ev := range events {
		if ev.Turn > maxTurn {
			maxTurn = ev.Turn
		}
		switch ev.Type {
		case session.TypeSessionStart:
			var data sessionStartJSON
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				return runMetrics{}, fmt.Errorf("event %d session_start: %w", i+1, err)
			}
			if data.Model != "" {
				sessionModel = data.Model
			}
		case session.TypeRunStart:
			var data runStartJSON
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				return runMetrics{}, fmt.Errorf("event %d run_start: %w", i+1, err)
			}
			if data.Model != "" {
				runModel = data.Model
			}
			if data.Pricing != nil {
				pricing = data.Pricing
			}
		case session.TypeModelRequest:
			var data modelRequestJSON
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				return runMetrics{}, fmt.Errorf("event %d model_request: %w", i+1, err)
			}
			if pendingEstimate != nil {
				m.EstimatorSkipped++
			}
			estimate := data.EstimatedInputTokens
			pendingEstimate = &estimate
		case session.TypeModelResponse:
			var data modelResponseJSON
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				return runMetrics{}, fmt.Errorf("event %d model_response: %w", i+1, err)
			}
			m.Responses++
			model := data.Model
			if model == "" {
				model = runModel
			}
			if model == "" {
				model = sessionModel
			}
			usage := data.Usage
			if usage.available() {
				m.UsageResponses++
				totals := usage.totals()
				m.InputTokens += totals.Input
				m.OutputTokens += totals.Output
				m.CachedInputTokens += totals.Cached
				m.CacheWriteInputTokens += totals.CacheWrite
				m.ReasoningOutputTokens += totals.Reasoning
				if cost, ok := responseCost(prices, pricing, model, totals, ev.TS); ok {
					m.CostMicroUSD += cost
					m.CostResponses++
				}
				if pendingEstimate != nil {
					actual := totals.Input
					diff := *pendingEstimate - actual
					if diff < 0 {
						diff = -diff
					}
					m.EstimatorAbsErrorSum += float64(diff)
					m.EstimatorEstimatedSum += *pendingEstimate
					m.EstimatorActualSum += actual
					m.EstimatorPairs++
				}
			} else {
				m.MissingUsageResponses++
			}
			if pendingEstimate != nil {
				if !usage.available() {
					m.EstimatorSkipped++
				}
				pendingEstimate = nil
			}
			for _, call := range data.ToolCalls {
				calls[call.ID] = callInfo{name: call.Name, path: readPath(call.Arguments)}
			}
		case session.TypeToolResult:
			var data toolResultJSON
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				return runMetrics{}, fmt.Errorf("event %d tool_result: %w", i+1, err)
			}
			m.ToolCalls++
			if data.Truncated {
				m.TruncatedResults++
			}
			if strings.Contains(data.Output, "continue with offset=") {
				m.ContinuationResults++
			}
			if !toolOK(data) {
				m.ToolErrors++
			}
			info := calls[data.CallID]
			name := data.Name
			if name == "" {
				name = info.name
			}
			if name == "read_file" {
				m.ReadCalls++
				path := info.path
				if path == "" {
					path = resultPath(data.Details)
				}
				if path == "" {
					m.UnattributedReads++
				} else {
					readPaths[path]++
				}
			}
		case session.TypeModelRetry:
			m.Retries++
		case session.TypeRunEnd:
			var data runEndJSON
			if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
				return runMetrics{}, fmt.Errorf("event %d run_end: %w", i+1, err)
			}
			if data.Status != "" {
				m.RunStatus = data.Status
			}
			if data.RunTurns > 0 {
				runTurns = data.RunTurns
			}
		}
	}
	if pendingEstimate != nil {
		m.EstimatorSkipped++
	}
	m.Model = runModel
	if m.Model == "" {
		m.Model = sessionModel
	}
	switch {
	case runTurns > 0:
		m.Turns = runTurns
	case maxTurn > 0:
		m.Turns = maxTurn
	default:
		m.Turns = m.Responses
	}
	m.CostAvailable = m.CostResponses > 0
	m.CostComplete = m.Responses > 0 && m.CostResponses == m.Responses
	for path, count := range readPaths {
		m.ReadCallsPerFile = append(m.ReadCallsPerFile, fileCallCount{Path: path, Calls: count})
		if count > 1 {
			m.RepeatedReads += count - 1
		}
	}
	sort.Slice(m.ReadCallsPerFile, func(i, j int) bool {
		return m.ReadCallsPerFile[i].Path < m.ReadCallsPerFile[j].Path
	})
	return m, nil
}

// responseCost prefers the price file, then the run_start catalog snapshot.
func responseCost(prices *priceTable, pricing *pricingSnapshot, model string, usage usageTotals, ts time.Time) (int64, bool) {
	if model != "" {
		if cost, ok := prices.cost(model, usage, ts); ok {
			return cost, true
		}
	}
	rates, ok := pricing.costRates()
	if !ok {
		return 0, false
	}
	return costMicroUSD(rates, usage), true
}

func toolOK(data toolResultJSON) bool {
	if data.OK != nil {
		return *data.OK
	}
	if data.Error != "" {
		return false
	}
	return data.Code == "" || data.Code == "ok"
}

func readPath(arguments json.RawMessage) string {
	if len(arguments) == 0 {
		return ""
	}
	var object struct {
		Path string `json:"path"`
	}
	if err := jsonv2.Unmarshal(arguments, &object); err == nil && object.Path != "" {
		return object.Path
	}
	var encoded string
	if err := jsonv2.Unmarshal(arguments, &encoded); err == nil && encoded != "" {
		if err := jsonv2.Unmarshal([]byte(encoded), &object); err == nil {
			return object.Path
		}
	}
	return ""
}

func resultPath(details json.RawMessage) string {
	if len(details) == 0 {
		return ""
	}
	var data detailsJSON
	if err := jsonv2.Unmarshal(details, &data); err != nil {
		return ""
	}
	if data.Path != "" {
		return data.Path
	}
	return data.File
}

// estimatorMeanAbs returns the mean absolute error in tokens.
func (m runMetrics) estimatorMeanAbs() *float64 {
	if m.EstimatorPairs == 0 {
		return nil
	}
	value := m.EstimatorAbsErrorSum / float64(m.EstimatorPairs)
	return &value
}

// estimatorRatio returns total estimated / total actual input tokens.
func (m runMetrics) estimatorRatio() *float64 {
	if m.EstimatorActualSum == 0 {
		return nil
	}
	value := float64(m.EstimatorEstimatedSum) / float64(m.EstimatorActualSum)
	return &value
}
