package main

import (
	"math"
	"testing"
)

func TestRotateArms(t *testing.T) {
	t.Parallel()
	arms := []arm{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	tests := []struct {
		task   int
		repeat int
		seed   int64
		want   []string
	}{
		{0, 0, 1, []string{"b", "c", "a"}},
		{0, 1, 1, []string{"c", "a", "b"}},
		{1, 0, 1, []string{"c", "a", "b"}},
		{1, 1, 1, []string{"a", "b", "c"}},
		{1, 1, -1, []string{"b", "c", "a"}},
	}
	for _, test := range tests {
		got := armNames(rotateArms(arms, test.task, test.repeat, test.seed))
		if len(got) != len(test.want) {
			t.Fatalf("task %d repeat %d: got %v, want %v", test.task, test.repeat, got, test.want)
		}
		for i := range got {
			if got[i] != test.want[i] {
				t.Errorf("task %d repeat %d seed %d: got %v, want %v",
					test.task, test.repeat, test.seed, got, test.want)
				break
			}
		}
	}
}

func TestPercentile(t *testing.T) {
	t.Parallel()
	values := []float64{1, 2, 3, 4}
	if got := percentile(values, 0); got != 1 {
		t.Errorf("p0 = %v, want 1", got)
	}
	if got := percentile(values, 1); got != 4 {
		t.Errorf("p1 = %v, want 4", got)
	}
	if got := percentile(values, 0.5); got != 2.5 {
		t.Errorf("p50 = %v, want 2.5", got)
	}
	if got := percentile(nil, 0.5); !math.IsNaN(got) {
		t.Errorf("empty percentile = %v, want NaN", got)
	}
}

func TestBootstrapDeterminism(t *testing.T) {
	t.Parallel()
	samples := []diffSample{
		{task: "t1", cluster: "c1", diff: 1},
		{task: "t2", cluster: "c1", diff: -1},
		{task: "t3", cluster: "c2", diff: 0.5},
		{task: "t4", cluster: "c2", diff: 0},
		{task: "t5", cluster: "c3", diff: 2},
	}
	effect1, ci1, se1, clusterCI1, clusterN1 := bootstrapDiff(samples, 7, 0)
	effect2, ci2, se2, clusterCI2, clusterN2 := bootstrapDiff(samples, 7, 0)
	if effect1 != effect2 || ci1 != ci2 || clusterCI1 != clusterCI2 || clusterN1 != clusterN2 {
		t.Fatalf("bootstrap not deterministic: (%v %+v %+v %d) vs (%v %+v %+v %d)",
			effect1, ci1, clusterCI1, clusterN1, effect2, ci2, clusterCI2, clusterN2)
	}
	if (se1 == nil) != (se2 == nil) || (se1 != nil && *se1 != *se2) {
		t.Fatalf("cluster SE not deterministic: %v vs %v", se1, se2)
	}
	if ci1.Low > ci1.High || clusterCI1.Low > clusterCI1.High {
		t.Errorf("CI ordering violated: task %+v cluster %+v", ci1, clusterCI1)
	}
	if clusterN1 != 3 {
		t.Errorf("clusterN = %d, want 3", clusterN1)
	}
	want := 0.5
	if math.Abs(effect1-want) > 1e-12 {
		t.Errorf("effect = %v, want %v", effect1, want)
	}
}

func TestBootstrapSingleTask(t *testing.T) {
	t.Parallel()
	effect, ci, se, clusterCI, clusterN := bootstrapDiff([]diffSample{{task: "t", cluster: "c", diff: 3}}, 1, 0)
	if effect != 3 || ci.Low != 3 || ci.High != 3 || clusterCI.Low != 3 || clusterCI.High != 3 {
		t.Errorf("single-task bootstrap = effect %v ci %+v cluster %+v", effect, ci, clusterCI)
	}
	if se != nil {
		t.Errorf("cluster SE = %v, want nil with one cluster", *se)
	}
	if clusterN != 1 {
		t.Errorf("clusterN = %d, want 1", clusterN)
	}
}

func TestCompareMetricEffect(t *testing.T) {
	t.Parallel()
	reference := map[string]float64{"t1": 0, "t2": 1, "t3": 0.5}
	armValues := map[string]float64{"t1": 1, "t2": 1, "t3": 0}
	clusters := map[string]string{"t1": "c1", "t2": "c1", "t3": "c2"}
	metric := compareMetric("solve_rate", "ratio", reference, armValues, clusters, 1, 0)
	if metric.N != 3 {
		t.Fatalf("N = %d, want 3", metric.N)
	}
	wantEffect := (1.0 + 0 - 0.5) / 3
	if metric.Effect == nil || math.Abs(*metric.Effect-wantEffect) > 1e-12 {
		t.Errorf("effect = %v, want %v", metric.Effect, wantEffect)
	}
	if metric.ReferenceMean == nil || math.Abs(*metric.ReferenceMean-0.5) > 1e-12 {
		t.Errorf("reference mean = %v, want 0.5", metric.ReferenceMean)
	}
	if metric.ArmMean == nil || math.Abs(*metric.ArmMean-(2.0/3)) > 1e-12 {
		t.Errorf("arm mean = %v, want 0.6666", metric.ArmMean)
	}
	if metric.TaskCI95 == nil || metric.ClusterCI95 == nil {
		t.Error("missing task or cluster CI")
	}
	if metric.ClusterN != 2 || metric.BootstrapSamples != bootstrapResamples {
		t.Errorf("clusterN/samples = %d/%d, want 2/%d", metric.ClusterN, metric.BootstrapSamples, bootstrapResamples)
	}
}

func TestCompareMetricEmpty(t *testing.T) {
	t.Parallel()
	metric := compareMetric("turns", "turns", map[string]float64{"t1": 1}, map[string]float64{"t2": 2}, nil, 1, 0)
	if metric.N != 0 || metric.Effect != nil || metric.TaskCI95 != nil {
		t.Errorf("empty metric = %+v, want no pairs", metric)
	}
}

func TestSummarizeArmExcludesInvalidAndMissingUsage(t *testing.T) {
	t.Parallel()
	records := []runRecord{
		{
			Task: "t1", Arm: "a", Status: statusSolved,
			Metrics: runMetrics{
				SessionFound:          true,
				Turns:                 2,
				ToolCalls:             1,
				Responses:             2,
				UsageResponses:        0,
				MissingUsageResponses: 2,
			},
		},
		{
			Task: "t1", Arm: "a", Status: statusSolved, DurationMS: 100,
			Metrics: runMetrics{
				SessionFound:     true,
				Turns:            4,
				ToolCalls:        3,
				Responses:        1,
				UsageResponses:   1,
				InputTokens:      100,
				OutputTokens:     50,
				CostAvailable:    true,
				CostComplete:     true,
				CostResponses:    1,
				CostMicroUSD:     10,
				ReadCalls:        2,
				RepeatedReads:    1,
				ReadCallsPerFile: []fileCallCount{{Path: "a.go", Calls: 2}},
			},
		},
		{
			Task: "t3", Arm: "a", Status: statusInvalid,
		},
		{
			Task: "t2", Arm: "a", Status: statusUnsolved, DurationMS: 50,
			Metrics: runMetrics{SessionFound: true, Turns: 6, ToolCalls: 2, Responses: 1, UsageResponses: 0},
		},
	}
	summary := summarizeArm(records, "a")
	if summary.Runs != 4 || summary.Invalid != 1 || summary.ValidRuns != 3 {
		t.Errorf("runs/valid/invalid = %d/%d/%d, want 4/3/1", summary.Runs, summary.ValidRuns, summary.Invalid)
	}
	if summary.Solved != 2 || summary.Unsolved != 1 || summary.Errors != 0 {
		t.Errorf("solved/unsolved/errors = %d/%d/%d, want 2/1/0", summary.Solved, summary.Unsolved, summary.Errors)
	}
	if summary.SolveRate == nil || math.Abs(*summary.SolveRate-2.0/3) > 1e-12 {
		t.Errorf("solve rate = %v, want 2/3", summary.SolveRate)
	}
	if summary.MeanTurns == nil || *summary.MeanTurns != 4 {
		t.Errorf("mean turns = %v, want 4", summary.MeanTurns)
	}
	if summary.MeanToolCalls == nil || *summary.MeanToolCalls != 2 {
		t.Errorf("mean tool calls = %v, want 2", summary.MeanToolCalls)
	}
	if summary.Tokens.SolvedRuns != 2 || summary.Tokens.UsageRuns != 1 {
		t.Errorf("token runs = %d/%d, want 2/1", summary.Tokens.SolvedRuns, summary.Tokens.UsageRuns)
	}
	if summary.Tokens.Input == nil || *summary.Tokens.Input != 100 {
		t.Errorf("input tokens = %v, want 100 (missing usage excluded, not zero)", summary.Tokens.Input)
	}
	if summary.Tokens.Total == nil || *summary.Tokens.Total != 150 {
		t.Errorf("total tokens = %v, want 150", summary.Tokens.Total)
	}
	if summary.PricedRuns != 1 || summary.UnpricedRuns != 2 || summary.PartialCostRuns != 0 {
		t.Errorf("cost runs = priced %d partial %d unpriced %d, want 1/0/2",
			summary.PricedRuns, summary.PartialCostRuns, summary.UnpricedRuns)
	}
	if summary.MeanCostMicroUSD == nil || *summary.MeanCostMicroUSD != 10 {
		t.Errorf("mean cost = %v, want 10", summary.MeanCostMicroUSD)
	}
	if summary.MissingUsageResponses != 2 || summary.MissingUsageRuns != 1 {
		t.Errorf("missing usage = %d/%d, want 2/1", summary.MissingUsageResponses, summary.MissingUsageRuns)
	}
	if summary.ReadCalls != 2 || summary.RepeatedReads != 1 || len(summary.ReadCallsPerFile) != 1 {
		t.Errorf("read aggregate = %+v", summary)
	}
	if summary.WallTimeMS != 150 {
		t.Errorf("wall time = %d, want 150", summary.WallTimeMS)
	}
}
