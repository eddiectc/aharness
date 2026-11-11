package main

import (
	"math"
	rand "math/rand/v2"
	"sort"
)

// bootstrapResamples is fixed by the evaluation contract.
const bootstrapResamples = 10000

type interval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

type tokenMeans struct {
	SolvedRuns int      `json:"solved_runs"`
	UsageRuns  int      `json:"usage_runs"`
	Input      *float64 `json:"input_tokens"`
	Output     *float64 `json:"output_tokens"`
	Cached     *float64 `json:"cached_input_tokens"`
	Reasoning  *float64 `json:"reasoning_output_tokens"`
	Total      *float64 `json:"total_tokens"`
}

type estimatorStats struct {
	Pairs              int      `json:"pairs"`
	Skipped            int      `json:"skipped"`
	MeanAbsErrorTokens *float64 `json:"mean_abs_error_tokens"`
	Ratio              *float64 `json:"ratio"`
}

type armSummary struct {
	Name      string `json:"name"`
	Runs      int    `json:"runs"`
	ValidRuns int    `json:"valid_runs"`
	Solved    int    `json:"solved"`
	Unsolved  int    `json:"unsolved"`
	Errors    int    `json:"errors"`
	Invalid   int    `json:"invalid"`

	SolveRate     *float64 `json:"solve_rate"`
	SessionRuns   int      `json:"session_runs"`
	MeanTurns     *float64 `json:"mean_turns"`
	MeanToolCalls *float64 `json:"mean_tool_calls"`

	Tokens tokenMeans `json:"tokens_per_solved_task"`

	MeanCostMicroUSD *float64 `json:"mean_cost_micro_usd"`
	PricedRuns       int      `json:"priced_runs"`
	PartialCostRuns  int      `json:"partial_cost_runs"`
	UnpricedRuns     int      `json:"unpriced_runs"`

	MissingUsageResponses int `json:"missing_usage_responses"`
	MissingUsageRuns      int `json:"missing_usage_runs"`

	ReadCalls           int             `json:"read_calls"`
	UnattributedReads   int             `json:"unattributed_reads"`
	ReadCallsPerFile    []fileCallCount `json:"read_calls_per_file"`
	RepeatedReads       int             `json:"repeated_reads"`
	TruncatedResults    int             `json:"truncated_results"`
	ContinuationResults int             `json:"continuation_results"`
	Retries             int             `json:"retries"`
	ToolErrors          int             `json:"tool_errors"`
	WallTimeMS          int64           `json:"wall_time_ms"`

	Estimator estimatorStats `json:"estimator"`
}

type pairedMetric struct {
	Name             string    `json:"name"`
	Unit             string    `json:"unit"`
	N                int       `json:"n_tasks"`
	ReferenceMean    *float64  `json:"reference_mean"`
	ArmMean          *float64  `json:"arm_mean"`
	Effect           *float64  `json:"effect"`
	TaskCI95         *interval `json:"task_ci95"`
	ClusterSE        *float64  `json:"cluster_se"`
	ClusterCI95      *interval `json:"cluster_ci95"`
	ClusterN         int       `json:"cluster_n"`
	BootstrapSamples int       `json:"bootstrap_samples"`
}

type pairedComparison struct {
	Arm                   string         `json:"arm"`
	Reference             string         `json:"reference"`
	Repeats               int            `json:"repeats"`
	ReferenceCostMicroUSD *float64       `json:"reference_cost_micro_usd"`
	ArmCostMicroUSD       *float64       `json:"arm_cost_micro_usd"`
	CostDeltaMicroUSD     *float64       `json:"cost_delta_micro_usd"`
	Metrics               []pairedMetric `json:"metrics"`
}

type summaryDoc struct {
	Tasks              int      `json:"tasks"`
	Clusters           int      `json:"clusters"`
	ClusterNames       []string `json:"cluster_names"`
	Repeats            int      `json:"repeats"`
	Seed               int64    `json:"seed"`
	Model              string   `json:"model"`
	ThinkingEffort     string   `json:"thinking_effort"`
	BootstrapResamples int      `json:"bootstrap_resamples"`
	Currency           string   `json:"currency"`

	Runs        int `json:"runs"`
	InvalidRuns int `json:"invalid_runs"`
	ErrorRuns   int `json:"error_runs"`

	Arms      []armSummary       `json:"arms"`
	Paired    []pairedComparison `json:"paired"`
	Estimator estimatorStats     `json:"estimator"`
}

// taskArmAgg accumulates repeat-level observations for one task and arm.
type taskArmAgg struct {
	valid        int
	solved       int
	turnsSum     float64
	turnsN       int
	solvedUsage  int
	inputSum     int64
	outputSum    int64
	cachedSum    int64
	reasoningSum int64
	costSum      int64
	costN        int
}

func summarize(records []runRecord, tasks []task, cfg config, prices *priceTable) summaryDoc {
	clusterSet := make(map[string]bool)
	for _, t := range tasks {
		clusterSet[t.Meta.Cluster] = true
	}
	clusters := make([]string, 0, len(clusterSet))
	for name := range clusterSet {
		clusters = append(clusters, name)
	}
	sort.Strings(clusters)
	clusterOf := make(map[string]string, len(tasks))
	for _, t := range tasks {
		clusterOf[t.ID] = t.Meta.Cluster
	}
	doc := summaryDoc{
		Tasks:              len(tasks),
		Clusters:           len(clusters),
		ClusterNames:       clusters,
		Repeats:            cfg.Repeats,
		Seed:               cfg.Seed,
		Model:              cfg.Model,
		ThinkingEffort:     cfg.ThinkingEffort,
		BootstrapResamples: bootstrapResamples,
		Currency:           "USD",
		Runs:               len(records),
	}
	if prices != nil && prices.Currency != "" {
		doc.Currency = prices.Currency
	}
	for _, rec := range records {
		switch rec.Status {
		case statusInvalid:
			doc.InvalidRuns++
		case statusError:
			doc.ErrorRuns++
		}
	}
	aggs := make(map[string]map[string]*taskArmAgg, len(tasks))
	for _, t := range tasks {
		perArm := make(map[string]*taskArmAgg, len(cfg.Arms))
		for _, a := range cfg.Arms {
			perArm[a.Name] = &taskArmAgg{}
		}
		aggs[t.ID] = perArm
	}
	for _, rec := range records {
		if rec.Status == statusInvalid {
			continue
		}
		perArm := aggs[rec.Task]
		if perArm == nil {
			continue
		}
		agg := perArm[rec.Arm]
		if agg == nil {
			agg = &taskArmAgg{}
			perArm[rec.Arm] = agg
		}
		agg.valid++
		if rec.Status == statusSolved {
			agg.solved++
		}
		if rec.Metrics.SessionFound {
			agg.turnsSum += float64(rec.Metrics.Turns)
			agg.turnsN++
			if rec.Status == statusSolved && rec.Metrics.UsageResponses > 0 {
				agg.solvedUsage++
				agg.inputSum += rec.Metrics.InputTokens
				agg.outputSum += rec.Metrics.OutputTokens
				agg.cachedSum += rec.Metrics.CachedInputTokens
				agg.reasoningSum += rec.Metrics.ReasoningOutputTokens
			}
			if rec.Metrics.CostComplete {
				agg.costSum += rec.Metrics.CostMicroUSD
				agg.costN++
			}
		}
	}

	for _, a := range cfg.Arms {
		doc.Arms = append(doc.Arms, summarizeArm(records, a.Name))
	}
	doc.Estimator = aggregateEstimator(records, "")
	reference := cfg.Arms[0]
	for _, a := range cfg.Arms[1:] {
		doc.Paired = append(doc.Paired, compareArms(reference.Name, a.Name, cfg, aggs, clusterOf))
	}
	return doc
}

func summarizeArm(records []runRecord, name string) armSummary {
	summary := armSummary{
		Name:             name,
		ReadCallsPerFile: []fileCallCount{},
	}
	var (
		tokenInput     int64
		tokenOutput    int64
		tokenCached    int64
		tokenReasoning int64
		tokenRuns      int
		turnsSum       float64
		turnsN         int
		callsSum       float64
		callsN         int
		costSum        int64
		costN          int
		reads          = make(map[string]int)
	)
	for _, rec := range records {
		if rec.Arm != name {
			continue
		}
		summary.Runs++
		summary.WallTimeMS += rec.DurationMS
		if rec.Status == statusInvalid {
			summary.Invalid++
			continue
		}
		summary.ValidRuns++
		switch rec.Status {
		case statusSolved:
			summary.Solved++
		case statusUnsolved:
			summary.Unsolved++
		case statusError:
			summary.Errors++
		}
		if !rec.Metrics.SessionFound {
			continue
		}
		summary.SessionRuns++
		turnsSum += float64(rec.Metrics.Turns)
		turnsN++
		callsSum += float64(rec.Metrics.ToolCalls)
		callsN++
		summary.ReadCalls += rec.Metrics.ReadCalls
		summary.UnattributedReads += rec.Metrics.UnattributedReads
		summary.RepeatedReads += rec.Metrics.RepeatedReads
		summary.TruncatedResults += rec.Metrics.TruncatedResults
		summary.ContinuationResults += rec.Metrics.ContinuationResults
		summary.Retries += rec.Metrics.Retries
		summary.ToolErrors += rec.Metrics.ToolErrors
		summary.MissingUsageResponses += rec.Metrics.MissingUsageResponses
		if rec.Metrics.MissingUsageResponses > 0 {
			summary.MissingUsageRuns++
		}
		for _, entry := range rec.Metrics.ReadCallsPerFile {
			reads[entry.Path] += entry.Calls
		}
		if rec.Status == statusSolved && rec.Metrics.UsageResponses > 0 {
			tokenRuns++
			tokenInput += rec.Metrics.InputTokens
			tokenOutput += rec.Metrics.OutputTokens
			tokenCached += rec.Metrics.CachedInputTokens
			tokenReasoning += rec.Metrics.ReasoningOutputTokens
		}
		switch {
		case rec.Metrics.CostComplete:
			summary.PricedRuns++
			costSum += rec.Metrics.CostMicroUSD
			costN++
		case rec.Metrics.CostAvailable:
			summary.PartialCostRuns++
		default:
			summary.UnpricedRuns++
		}
	}
	summary.SolveRate = ratio(summary.Solved, summary.ValidRuns)
	summary.MeanTurns = mean(turnsSum, turnsN)
	summary.MeanToolCalls = mean(callsSum, callsN)
	summary.Tokens = tokenMeans{
		SolvedRuns: summary.Solved,
		UsageRuns:  tokenRuns,
		Input:      mean(float64(tokenInput), tokenRuns),
		Output:     mean(float64(tokenOutput), tokenRuns),
		Cached:     mean(float64(tokenCached), tokenRuns),
		Reasoning:  mean(float64(tokenReasoning), tokenRuns),
		Total:      mean(float64(tokenInput+tokenOutput), tokenRuns),
	}
	summary.MeanCostMicroUSD = mean(float64(costSum), costN)
	summary.Estimator = aggregateEstimator(records, name)
	for path, calls := range reads {
		summary.ReadCallsPerFile = append(summary.ReadCallsPerFile, fileCallCount{Path: path, Calls: calls})
	}
	sort.Slice(summary.ReadCallsPerFile, func(i, j int) bool {
		return summary.ReadCallsPerFile[i].Path < summary.ReadCallsPerFile[j].Path
	})
	return summary
}

func aggregateEstimator(records []runRecord, armName string) estimatorStats {
	var (
		pairs     int
		skipped   int
		absSum    float64
		estSum    int64
		actualSum int64
	)
	for _, rec := range records {
		if armName != "" && rec.Arm != armName {
			continue
		}
		if rec.Status == statusInvalid || !rec.Metrics.SessionFound {
			continue
		}
		pairs += rec.Metrics.EstimatorPairs
		skipped += rec.Metrics.EstimatorSkipped
		absSum += rec.Metrics.EstimatorAbsErrorSum
		estSum += rec.Metrics.EstimatorEstimatedSum
		actualSum += rec.Metrics.EstimatorActualSum
	}
	stats := estimatorStats{Pairs: pairs, Skipped: skipped}
	stats.MeanAbsErrorTokens = mean(absSum, pairs)
	if actualSum > 0 {
		value := float64(estSum) / float64(actualSum)
		stats.Ratio = &value
	}
	return stats
}

func compareArms(reference, name string, cfg config, aggs map[string]map[string]*taskArmAgg, clusterOf map[string]string) pairedComparison {
	taskIDs := make([]string, 0, len(aggs))
	for id := range aggs {
		taskIDs = append(taskIDs, id)
	}
	sort.Strings(taskIDs)

	solveRef := make(map[string]float64)
	solveArm := make(map[string]float64)
	turnsRef := make(map[string]float64)
	turnsArm := make(map[string]float64)
	tokensRef := make(map[string]float64)
	tokensArm := make(map[string]float64)
	costRef := make(map[string]float64)
	costArm := make(map[string]float64)
	for _, id := range taskIDs {
		ref := aggs[id][reference]
		arm := aggs[id][name]
		if ref == nil || arm == nil {
			continue
		}
		if value, ok := ratioValue(ref.solved, ref.valid); ok {
			if armValue, ok := ratioValue(arm.solved, arm.valid); ok {
				solveRef[id] = value
				solveArm[id] = armValue
			}
		}
		if ref.turnsN > 0 && arm.turnsN > 0 {
			turnsRef[id] = ref.turnsSum / float64(ref.turnsN)
			turnsArm[id] = arm.turnsSum / float64(arm.turnsN)
		}
		if ref.solvedUsage > 0 && arm.solvedUsage > 0 {
			tokensRef[id] = float64(ref.inputSum+ref.outputSum) / float64(ref.solvedUsage)
			tokensArm[id] = float64(arm.inputSum+arm.outputSum) / float64(arm.solvedUsage)
		}
		if ref.costN > 0 && arm.costN > 0 {
			costRef[id] = float64(ref.costSum) / float64(ref.costN)
			costArm[id] = float64(arm.costSum) / float64(arm.costN)
		}
	}

	comparison := pairedComparison{
		Arm:       name,
		Reference: reference,
		Repeats:   cfg.Repeats,
		Metrics: []pairedMetric{
			compareMetric("solve_rate", "ratio", solveRef, solveArm, clusterOf, cfg.Seed, 0),
			compareMetric("turns_per_run", "turns", turnsRef, turnsArm, clusterOf, cfg.Seed, 1),
			compareMetric("tokens_per_solved_task", "tokens", tokensRef, tokensArm, clusterOf, cfg.Seed, 2),
			compareMetric("cost_micro_usd", "micro_usd", costRef, costArm, clusterOf, cfg.Seed, 3),
		},
	}
	referenceCost := armCostMean(aggs, reference)
	armCost := armCostMean(aggs, name)
	comparison.ReferenceCostMicroUSD = referenceCost
	comparison.ArmCostMicroUSD = armCost
	if referenceCost != nil && armCost != nil {
		delta := *armCost - *referenceCost
		comparison.CostDeltaMicroUSD = &delta
	}
	return comparison
}

func armCostMean(aggs map[string]map[string]*taskArmAgg, name string) *float64 {
	var sum int64
	var n int
	for _, perArm := range aggs {
		agg := perArm[name]
		if agg == nil {
			continue
		}
		sum += agg.costSum
		n += agg.costN
	}
	return mean(float64(sum), n)
}

type diffSample struct {
	task    string
	cluster string
	diff    float64
}

func compareMetric(name, unit string, reference, arm map[string]float64, clusterOf map[string]string, seed int64, stream int) pairedMetric {
	var samples []diffSample
	var refSum, armSum float64
	for taskID, refValue := range reference {
		armValue, ok := arm[taskID]
		if !ok {
			continue
		}
		samples = append(samples, diffSample{
			task:    taskID,
			cluster: clusterOf[taskID],
			diff:    armValue - refValue,
		})
		refSum += refValue
		armSum += armValue
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].task < samples[j].task })
	metric := pairedMetric{Name: name, Unit: unit, N: len(samples)}
	if len(samples) == 0 {
		return metric
	}
	metric.ReferenceMean = mean(refSum, len(samples))
	metric.ArmMean = mean(armSum, len(samples))
	effect, taskCI, clusterSE, clusterCI, clusterN := bootstrapDiff(samples, seed, stream)
	metric.Effect = &effect
	metric.TaskCI95 = &taskCI
	metric.ClusterSE = clusterSE
	if clusterN > 0 {
		metric.ClusterCI95 = &clusterCI
	}
	metric.ClusterN = clusterN
	metric.BootstrapSamples = bootstrapResamples
	return metric
}

// bootstrapDiff returns the paired mean difference, its task-level percentile
// CI, and a cluster-aware bootstrap standard error and CI.
func bootstrapDiff(samples []diffSample, seed int64, stream int) (effect float64, taskCI interval, clusterSE *float64, clusterCI interval, clusterN int) {
	diffs := make([]float64, len(samples))
	var sum float64
	clusterValues := make(map[string][]float64)
	for i, sample := range samples {
		diffs[i] = sample.diff
		sum += sample.diff
		clusterValues[sample.cluster] = append(clusterValues[sample.cluster], sample.diff)
	}
	effect = sum / float64(len(samples))

	taskMeans := bootstrapTaskMeans(diffs, newRand(seed, stream*2))
	taskCI = interval{
		Low:  percentile(taskMeans, 0.025),
		High: percentile(taskMeans, 0.975),
	}

	clusters := make([]string, 0, len(clusterValues))
	for cluster := range clusterValues {
		clusters = append(clusters, cluster)
	}
	sort.Strings(clusters)
	groups := make([][]float64, len(clusters))
	for i, cluster := range clusters {
		groups[i] = clusterValues[cluster]
	}
	clusterN = len(groups)
	clusterMeans := bootstrapClusterMeans(groups, newRand(seed, stream*2+1))
	clusterCI = interval{
		Low:  percentile(clusterMeans, 0.025),
		High: percentile(clusterMeans, 0.975),
	}
	if clusterN >= 2 {
		clusterSE = stddev(clusterMeans)
	}
	return effect, taskCI, clusterSE, clusterCI, clusterN
}

func newRand(seed int64, stream int) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(seed)+uint64(stream)*0x9e3779b97f4a7c15, uint64(stream)+1))
}

func bootstrapTaskMeans(values []float64, rng *rand.Rand) []float64 {
	means := make([]float64, bootstrapResamples)
	n := len(values)
	for i := range means {
		var sum float64
		for range n {
			sum += values[rng.IntN(n)]
		}
		means[i] = sum / float64(n)
	}
	return means
}

func bootstrapClusterMeans(groups [][]float64, rng *rand.Rand) []float64 {
	means := make([]float64, bootstrapResamples)
	for i := range means {
		var sum float64
		var count int
		for range len(groups) {
			for _, value := range groups[rng.IntN(len(groups))] {
				sum += value
				count++
			}
		}
		means[i] = sum / float64(count)
	}
	return means
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	position := p * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func stddev(values []float64) *float64 {
	if len(values) < 2 {
		return nil
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	meanValue := sum / float64(len(values))
	var squares float64
	for _, value := range values {
		diff := value - meanValue
		squares += diff * diff
	}
	value := math.Sqrt(squares / float64(len(values)-1))
	return &value
}

func mean(sum float64, n int) *float64 {
	if n == 0 {
		return nil
	}
	value := sum / float64(n)
	return &value
}

func ratio(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

func ratioValue(numerator, denominator int) (float64, bool) {
	if denominator == 0 {
		return 0, false
	}
	return float64(numerator) / float64(denominator), true
}
