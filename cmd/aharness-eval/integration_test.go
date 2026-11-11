package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aharness/internal/session"
)

const fakeHarness = `#!/bin/sh
set -eu
ws=""
state=""
task=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		-workspace) ws=$2; shift 2 ;;
		-state-dir) state=$2; shift 2 ;;
		-task-file) task=$2; shift 2 ;;
		*) shift ;;
	esac
done
if [ -n "${FAKE_LOG:-}" ]; then
	printf '%s|%s\n' "${0##*/}" "$(cat "$task")" >> "$FAKE_LOG"
fi
if [ -n "${FAKE_SESSION:-}" ]; then
	mkdir -p "$state/sessions"
	cp "$FAKE_SESSION" "$state/sessions/0199d0e0-0000-7000-8000-000000000001.jsonl"
fi
case "$(cat "$task")" in
	SOLVE*) : > "$ws/answer.txt" ;;
esac
exit 0
`

const fakeSetup = `#!/bin/sh
set -eu
printf 'input\n' > "$WORKSPACE/input.txt"
`

const fakeVerify = `#!/bin/sh
set -eu
test -f "$WORKSPACE/answer.txt"
`

func TestEvalIntegrationFakeHarness(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "harness.log")
	sessionPath := filepath.Join(t.TempDir(), "fixture.jsonl")
	base := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	reported := true
	writeSessionFile(t, sessionPath, []session.Event{
		testEvent(t, "session_start", 0, base, map[string]any{"model": "fake-model"}),
		testEvent(t, "run_start", 0, base, map[string]any{"run_id": "r1", "model": "fake-model"}),
		testEvent(t, "model_request", 1, base, map[string]any{"estimated_input_tokens": 10}),
		testEvent(t, "model_response", 1, base, map[string]any{
			"model": "fake-model",
			"usage": usageJSON{InputTokens: 12, OutputTokens: 3, Reported: &reported},
		}),
		testEvent(t, "tool_result", 1, base, map[string]any{
			"call_id": "c1", "name": "read_file", "ok": true,
			"output": "input", "details": map[string]any{"path": "input.txt"},
		}),
		testEvent(t, "run_end", 1, base, map[string]any{"status": "ok", "run_turns": 1}),
	})
	t.Setenv("FAKE_LOG", logPath)
	t.Setenv("FAKE_SESSION", sessionPath)

	root := t.TempDir()
	writeTaskTree(t, root, "alpha", "SOLVE alpha", fakeSetup, fakeVerify,
		taskMeta{Cluster: "text", Language: "sh", Timeout: 20})
	writeTaskTree(t, root, "beta", "NOOP beta", fakeSetup, fakeVerify,
		taskMeta{Cluster: "text", Language: "sh", Timeout: 20})
	writeTaskTree(t, root, "gamma", "SOLVE gamma", "#!/bin/sh\nset -eu\nexit 3\n", fakeVerify,
		taskMeta{Cluster: "shell", Language: "sh", Timeout: 20})

	armDir := t.TempDir()
	beforePath := filepath.Join(armDir, "before.sh")
	afterPath := filepath.Join(armDir, "after.sh")
	writeFile(t, beforePath, fakeHarness, 0o755)
	writeFile(t, afterPath, fakeHarness, 0o755)

	outDir := filepath.Join(t.TempDir(), "results")
	args := []string{
		"-tasks", root,
		"-arm", "before=" + beforePath,
		"-arm", "after=" + afterPath,
		"-model", "fake-model",
		"-thinking-effort", "high",
		"-repeats", "2",
		"-seed", "1",
		"-prices", "",
		"-out", outDir,
	}
	if code := run(t.Context(), args, io.Discard, io.Discard); code != 0 {
		t.Fatalf("run = %d, want 0", code)
	}

	records := readResultsFile(t, filepath.Join(outDir, "results.jsonl"))
	if len(records) != 12 {
		t.Fatalf("records = %d, want 3 tasks * 2 arms * 2 repeats = 12", len(records))
	}
	type recordKey struct {
		task   string
		arm    string
		repeat int
	}
	byKey := make(map[recordKey]runRecord, len(records))
	for _, record := range records {
		byKey[recordKey{record.Task, record.Arm, record.Repeat}] = record
	}
	taskIndex := map[string]int{"alpha": 0, "beta": 1, "gamma": 2}
	arms := []arm{{Name: "before"}, {Name: "after"}}
	for task, index := range taskIndex {
		for _, armName := range []string{"before", "after"} {
			for repeat := 1; repeat <= 2; repeat++ {
				record, ok := byKey[recordKey{task, armName, repeat}]
				if !ok {
					t.Fatalf("missing record %s/%s/%d", task, armName, repeat)
				}
				wantOrder := armNames(rotateArms(arms, index, repeat-1, 1))
				if strings.Join(record.ArmOrder, ",") != strings.Join(wantOrder, ",") {
					t.Errorf("%s/%s/%d arm order = %v, want %v", task, armName, repeat, record.ArmOrder, wantOrder)
				}
				if record.Seed != 1 || record.Repeat != repeat {
					t.Errorf("%s/%s/%d seed/repeat = %d/%d", task, armName, repeat, record.Seed, record.Repeat)
				}
				switch task {
				case "alpha":
					if record.Status != statusSolved {
						t.Errorf("alpha/%s/%d status = %s, want solved (error %q)", armName, repeat, record.Status, record.Error)
					}
					if record.VerifyExit == nil || *record.VerifyExit != 0 {
						t.Errorf("alpha verify exit = %v, want 0", record.VerifyExit)
					}
					if record.HarnessExit == nil || *record.HarnessExit != 0 {
						t.Errorf("alpha harness exit = %v, want 0", record.HarnessExit)
					}
					if !record.Metrics.SessionFound || record.Metrics.Turns != 1 || record.Metrics.ToolCalls != 1 {
						t.Errorf("alpha metrics = %+v", record.Metrics)
					}
					if record.Metrics.ReadCalls != 1 || len(record.Metrics.ReadCallsPerFile) != 1 {
						t.Errorf("alpha read metrics = %+v", record.Metrics)
					}
					if record.Metrics.EstimatorPairs != 1 || record.Metrics.EstimatorAbsErrorSum != 2 {
						t.Errorf("alpha estimator = %+v", record.Metrics)
					}
					if record.SessionPath == "" {
						t.Error("alpha session path is empty")
					}
				case "beta":
					if record.Status != statusUnsolved {
						t.Errorf("beta/%s/%d status = %s, want unsolved", armName, repeat, record.Status)
					}
					if record.VerifyExit == nil || *record.VerifyExit == 0 {
						t.Errorf("beta verify exit = %v, want nonzero", record.VerifyExit)
					}
					if !strings.Contains(record.Error, "verify exited") {
						t.Errorf("beta error = %q, want verify failure", record.Error)
					}
				case "gamma":
					if record.Status != statusInvalid {
						t.Errorf("gamma/%s/%d status = %s, want invalid", armName, repeat, record.Status)
					}
					if record.SetupExit == nil || *record.SetupExit != 3 {
						t.Errorf("gamma setup exit = %v, want 3", record.SetupExit)
					}
					if record.VerifyExit != nil || record.HarnessExit != nil {
						t.Errorf("gamma verify/harness = %v/%v, want nil after invalid setup", record.VerifyExit, record.HarnessExit)
					}
					if record.Metrics.SessionFound || record.SessionPath != "" {
						t.Errorf("gamma metrics/session = %+v/%q, want absent", record.Metrics, record.SessionPath)
					}
				}
			}
		}
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read harness log: %v", err)
	}
	wantLog := strings.Join([]string{
		"after.sh|SOLVE alpha",
		"before.sh|SOLVE alpha",
		"before.sh|SOLVE alpha",
		"after.sh|SOLVE alpha",
		"before.sh|NOOP beta",
		"after.sh|NOOP beta",
		"after.sh|NOOP beta",
		"before.sh|NOOP beta",
	}, "\n")
	if got := strings.TrimSpace(string(logData)); got != wantLog {
		t.Errorf("harness invocation order:\ngot:\n%s\nwant:\n%s", got, wantLog)
	}

	doc := readSummaryFile(t, filepath.Join(outDir, "summary.json"))
	if doc.Tasks != 3 || doc.Clusters != 2 || doc.Repeats != 2 || doc.Seed != 1 {
		t.Errorf("summary shape = tasks %d clusters %d repeats %d seed %d",
			doc.Tasks, doc.Clusters, doc.Repeats, doc.Seed)
	}
	if doc.Runs != 12 || doc.InvalidRuns != 4 || doc.ErrorRuns != 0 {
		t.Errorf("summary runs = %d invalid %d error %d, want 12/4/0", doc.Runs, doc.InvalidRuns, doc.ErrorRuns)
	}
	if len(doc.Arms) != 2 {
		t.Fatalf("summary arms = %d, want 2", len(doc.Arms))
	}
	for _, summary := range doc.Arms {
		if summary.Runs != 6 || summary.ValidRuns != 4 || summary.Invalid != 2 {
			t.Errorf("%s runs/valid/invalid = %d/%d/%d, want 6/4/2",
				summary.Name, summary.Runs, summary.ValidRuns, summary.Invalid)
		}
		if summary.Solved != 2 || summary.Unsolved != 2 || summary.Errors != 0 {
			t.Errorf("%s solved/unsolved/errors = %d/%d/%d, want 2/2/0",
				summary.Name, summary.Solved, summary.Unsolved, summary.Errors)
		}
		if summary.SolveRate == nil || *summary.SolveRate != 0.5 {
			t.Errorf("%s solve rate = %v, want 0.5", summary.Name, summary.SolveRate)
		}
		if summary.SessionRuns != 4 {
			t.Errorf("%s session runs = %d, want 4", summary.Name, summary.SessionRuns)
		}
		if summary.Tokens.SolvedRuns != 2 || summary.Tokens.UsageRuns != 2 {
			t.Errorf("%s token runs = %+v, want 2/2", summary.Name, summary.Tokens)
		}
		if summary.Tokens.Input == nil || *summary.Tokens.Input != 12 {
			t.Errorf("%s input tokens = %v, want 12", summary.Name, summary.Tokens.Input)
		}
		if summary.Tokens.Total == nil || *summary.Tokens.Total != 15 {
			t.Errorf("%s total tokens = %v, want 15", summary.Name, summary.Tokens.Total)
		}
		if summary.PricedRuns != 0 || summary.UnpricedRuns != 4 {
			t.Errorf("%s priced/unpriced runs = %d/%d, want 0/4",
				summary.Name, summary.PricedRuns, summary.UnpricedRuns)
		}
		if summary.Estimator.Pairs != 4 {
			t.Errorf("%s estimator pairs = %d, want 4 (two valid tasks, two repeats; invalid task excluded)",
				summary.Name, summary.Estimator.Pairs)
		}
	}
	if len(doc.Paired) != 1 {
		t.Fatalf("paired comparisons = %d, want 1", len(doc.Paired))
	}
	pair := doc.Paired[0]
	if pair.Reference != "before" || pair.Arm != "after" || pair.Repeats != 2 {
		t.Errorf("pair = %+v", pair)
	}
	if pair.CostDeltaMicroUSD != nil {
		t.Errorf("cost delta = %v, want nil with no prices", *pair.CostDeltaMicroUSD)
	}
	metrics := make(map[string]pairedMetric)
	for _, metric := range pair.Metrics {
		metrics[metric.Name] = metric
	}
	wantN := map[string]int{"solve_rate": 2, "turns_per_run": 2, "tokens_per_solved_task": 1}
	for name, wantPairs := range wantN {
		metric, ok := metrics[name]
		if !ok {
			t.Fatalf("missing paired metric %s", name)
		}
		if metric.N != wantPairs {
			t.Errorf("%s N = %d, want %d paired tasks", name, metric.N, wantPairs)
		}
		if metric.Effect == nil || *metric.Effect != 0 {
			t.Errorf("%s effect = %v, want 0", name, metric.Effect)
		}
		if metric.TaskCI95 == nil || metric.TaskCI95.Low != 0 || metric.TaskCI95.High != 0 {
			t.Errorf("%s task CI = %+v, want [0,0]", name, metric.TaskCI95)
		}
	}
	if cost := metrics["cost_micro_usd"]; cost.N != 0 {
		t.Errorf("cost metric N = %d, want 0 with no priced runs", cost.N)
	}

	markdown, err := os.ReadFile(filepath.Join(outDir, "summary.md"))
	if err != nil {
		t.Fatalf("read summary.md: %v", err)
	}
	text := string(markdown)
	for _, want := range []string{"# aharness eval summary", "## Paired comparisons", "solve_rate", "Cost delta", "## Read calls per file"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary.md is missing %q", want)
		}
	}
}

func TestEvalHarnessTimeout(t *testing.T) {
	root := t.TempDir()
	writeTaskTree(t, root, "timeout-task", "do nothing", "#!/bin/sh\nset -eu\n", "#!/bin/sh\nset -eu\nexit 0\n",
		taskMeta{Cluster: "shell", Language: "sh", Timeout: 0.3})
	sleeping := filepath.Join(t.TempDir(), "sleeping.sh")
	writeFile(t, sleeping, "#!/bin/sh\nset -eu\nsleep 5\nexit 0\n", 0o755)
	outDir := filepath.Join(t.TempDir(), "results")
	args := []string{
		"-tasks", root,
		"-arm", "slow=" + sleeping,
		"-model", "fake-model",
		"-repeats", "1",
		"-prices", "",
		"-out", outDir,
	}
	if code := run(t.Context(), args, io.Discard, io.Discard); code != 0 {
		t.Fatalf("run = %d, want 0", code)
	}
	records := readResultsFile(t, filepath.Join(outDir, "results.jsonl"))
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.Status != statusError {
		t.Errorf("status = %s, want error", record.Status)
	}
	if !strings.Contains(record.Error, "timed out") {
		t.Errorf("error = %q, want a timeout reason", record.Error)
	}
	if record.HarnessExit == nil || *record.HarnessExit != -1 {
		t.Errorf("harness exit = %v, want -1", record.HarnessExit)
	}
	if record.VerifyExit == nil || *record.VerifyExit != 0 {
		t.Errorf("verify exit = %v, want 0 (verification still runs)", record.VerifyExit)
	}
	doc := readSummaryFile(t, filepath.Join(outDir, "summary.json"))
	if len(doc.Arms) != 1 || doc.Arms[0].Errors != 1 || doc.Arms[0].Solved != 0 {
		t.Errorf("summary arm = %+v, want one error run", doc.Arms)
	}
	if doc.Arms[0].SolveRate == nil || *doc.Arms[0].SolveRate != 0 {
		t.Errorf("solve rate = %v, want 0 (error is unsolved for the rate)", doc.Arms[0].SolveRate)
	}
}
