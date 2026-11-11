package main

import (
	"bufio"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// writeOutputs writes results.jsonl, summary.json, and summary.md atomically.
func writeOutputs(dir string, records []runRecord, doc summaryDoc) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory %s: %w", dir, err)
	}
	if err := writeAtomic(filepath.Join(dir, "results.jsonl"), func(w io.Writer) error {
		return writeResults(w, records)
	}); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, "summary.json"), func(w io.Writer) error {
		encoder := jsontext.NewEncoder(w, jsontext.WithIndent("  "))
		if err := jsonv2.MarshalEncode(encoder, doc); err != nil {
			return fmt.Errorf("encode summary: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "summary.md"), func(w io.Writer) error {
		return writeSummaryMarkdown(w, doc)
	})
}

func writeResults(w io.Writer, records []runRecord) error {
	buffered := bufio.NewWriter(w)
	encoder := jsontext.NewEncoder(buffered)
	for i := range records {
		if err := jsonv2.MarshalEncode(encoder, records[i]); err != nil {
			return fmt.Errorf("encode record %d: %w", i+1, err)
		}
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("flush results: %w", err)
	}
	return nil
}

// writeAtomic writes through a same-directory temporary file and renames it
// into place, so a failed run never leaves a truncated result.
func writeAtomic(path string, write func(io.Writer) error) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary %s: %w", path, err)
	}
	name := file.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = file.Close()
			_ = os.Remove(name)
		}
	}()
	if err := write(file); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	renamed = true
	return nil
}

type markdownWriter struct {
	w   io.Writer
	err error
}

func (m *markdownWriter) printf(format string, args ...any) {
	if m.err != nil {
		return
	}
	_, m.err = fmt.Fprintf(m.w, format, args...)
}

func writeSummaryMarkdown(w io.Writer, doc summaryDoc) error {
	md := &markdownWriter{w: w}
	md.printf("# aharness eval summary\n\n")
	md.printf("| Setting | Value |\n| --- | --- |\n")
	md.printf("| Tasks | %d |\n", doc.Tasks)
	md.printf("| Clusters | %d (%s) |\n", doc.Clusters, mdEscape(strings.Join(doc.ClusterNames, ", ")))
	md.printf("| Repeats (k) | %d |\n", doc.Repeats)
	md.printf("| Seed | %d |\n", doc.Seed)
	md.printf("| Model | %s |\n", mdEscape(doc.Model))
	md.printf("| Thinking effort | %s |\n", mdEscape(doc.ThinkingEffort))
	md.printf("| Bootstrap resamples | %d |\n", doc.BootstrapResamples)
	md.printf("| Currency | %s |\n", mdEscape(doc.Currency))
	md.printf("| Runs | %d |\n", doc.Runs)
	md.printf("| Invalid runs | %d |\n", doc.InvalidRuns)
	md.printf("| Error runs | %d |\n", doc.ErrorRuns)

	md.printf("\n## Outcomes and means\n\n")
	md.printf("| Arm | Runs | Valid | Solved | Unsolved | Error | Invalid | Solve rate | Mean turns | Mean tool calls | Wall time (s) |\n")
	md.printf("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, a := range doc.Arms {
		md.printf("| %s | %d | %d | %d | %d | %d | %d | %s | %s | %s | %.3f |\n",
			mdEscape(a.Name), a.Runs, a.ValidRuns, a.Solved, a.Unsolved, a.Errors, a.Invalid,
			formatPercent(a.SolveRate), formatNumber(a.MeanTurns, 2), formatNumber(a.MeanToolCalls, 2),
			float64(a.WallTimeMS)/1000)
	}

	md.printf("\n## Tokens per solved task\n\n")
	md.printf("Solved runs with reported usage are the denominator; runs with missing usage are excluded, not counted as zero.\n\n")
	md.printf("| Arm | Solved runs | Usage runs | Input | Output | Cached | Reasoning | Total |\n")
	md.printf("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, a := range doc.Arms {
		md.printf("| %s | %d | %d | %s | %s | %s | %s | %s |\n",
			mdEscape(a.Name), a.Tokens.SolvedRuns, a.Tokens.UsageRuns,
			formatNumber(a.Tokens.Input, 1), formatNumber(a.Tokens.Output, 1),
			formatNumber(a.Tokens.Cached, 1), formatNumber(a.Tokens.Reasoning, 1),
			formatNumber(a.Tokens.Total, 1))
	}

	md.printf("\n## Cost and usage completeness\n\n")
	md.printf("| Arm | Mean cost (%s) | Priced runs | Partial cost runs | Unpriced runs | Missing usage responses | Missing usage runs |\n", mdEscape(doc.Currency))
	md.printf("| --- | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, a := range doc.Arms {
		md.printf("| %s | %s | %d | %d | %d | %d | %d |\n",
			mdEscape(a.Name), formatCost(a.MeanCostMicroUSD, doc.Currency),
			a.PricedRuns, a.PartialCostRuns, a.UnpricedRuns,
			a.MissingUsageResponses, a.MissingUsageRuns)
	}

	md.printf("\n## Tooling and read metrics\n\n")
	md.printf("| Arm | Read calls | Unattributed | Repeated reads | Truncated results | Continuations | Retries | Tool errors |\n")
	md.printf("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, a := range doc.Arms {
		md.printf("| %s | %d | %d | %d | %d | %d | %d | %d |\n",
			mdEscape(a.Name), a.ReadCalls, a.UnattributedReads, a.RepeatedReads,
			a.TruncatedResults, a.ContinuationResults, a.Retries, a.ToolErrors)
	}

	md.printf("\n## Estimator error\n\n")
	md.printf("Compares model_request.estimated_input_tokens with the next model_response usage input tokens.\n\n")
	md.printf("| Arm | Pairs | Skipped | Mean absolute error (tokens) | Estimated/actual ratio |\n")
	md.printf("| --- | ---: | ---: | ---: | ---: |\n")
	md.printf("| all | %d | %d | %s | %s |\n", doc.Estimator.Pairs, doc.Estimator.Skipped,
		formatNumber(doc.Estimator.MeanAbsErrorTokens, 1), formatRatio(doc.Estimator.Ratio))
	for _, a := range doc.Arms {
		md.printf("| %s | %d | %d | %s | %s |\n", mdEscape(a.Name), a.Estimator.Pairs, a.Estimator.Skipped,
			formatNumber(a.Estimator.MeanAbsErrorTokens, 1), formatRatio(a.Estimator.Ratio))
	}

	md.printf("\n## Paired comparisons\n\n")
	md.printf("Each metric is paired by task and averaged over repeats. Task CI is a 95%% percentile bootstrap with %d resamples. Cluster CI resamples meta.json clusters with replacement and keeps every task in the selected clusters.\n", doc.BootstrapResamples)
	for _, pair := range doc.Paired {
		md.printf("\n### %s vs %s\n\n", mdEscape(pair.Arm), mdEscape(pair.Reference))
		md.printf("- Repeats (k): %d\n", pair.Repeats)
		md.printf("- Reference mean cost per fully priced run: %s\n", formatCost(pair.ReferenceCostMicroUSD, doc.Currency))
		md.printf("- Arm mean cost per fully priced run: %s\n", formatCost(pair.ArmCostMicroUSD, doc.Currency))
		md.printf("- Cost delta: %s\n\n", formatDeltaCost(pair.CostDeltaMicroUSD, doc.Currency))
		md.printf("| Metric | Tasks | Reference | Arm | Effect | Task 95%% CI | Cluster SE | Cluster 95%% CI | Clusters |\n")
		md.printf("| --- | ---: | ---: | ---: | ---: | --- | ---: | --- | ---: |\n")
		for _, metric := range pair.Metrics {
			md.printf("| %s | %d | %s | %s | %s | %s | %s | %s | %d |\n",
				mdEscape(metric.Name), metric.N,
				formatNumber(metric.ReferenceMean, 3), formatNumber(metric.ArmMean, 3),
				formatNumber(metric.Effect, 3), formatInterval(metric.TaskCI95),
				formatNumber(metric.ClusterSE, 3), formatInterval(metric.ClusterCI95),
				metric.ClusterN)
		}
	}

	md.printf("\n## Read calls per file\n\n")
	rows := 0
	for _, a := range doc.Arms {
		for _, entry := range a.ReadCallsPerFile {
			if rows == 0 {
				md.printf("| Arm | Path | Calls |\n| --- | --- | ---: |\n")
			}
			md.printf("| %s | %s | %d |\n", mdEscape(a.Name), mdEscape(entry.Path), entry.Calls)
			rows++
		}
	}
	if rows == 0 {
		md.printf("_No attributed read calls._\n")
	}

	md.printf("\n## Limitations\n\n")
	md.printf("- Results contain prompts, model output, and tails of harness stdout/stderr.\n")
	md.printf("- A real-provider baseline requires configured provider credentials and a built aharness binary; see eval/baseline.json for the recorded status.\n")
	md.printf("- Costs come from the price file when the model is listed, then from run_start pricing; unpriced responses are excluded from cost means.\n")
	md.printf("- A 30-task suite is a smoke and call-count/token instrument, not a 5 percentage-point solve-rate detector.\n")
	if md.err != nil {
		return fmt.Errorf("write summary markdown: %w", md.err)
	}
	return nil
}

func mdEscape(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}

func formatPercent(value *float64) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", *value*100)
}

func formatNumber(value *float64, digits int) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.*f", digits, *value)
}

func formatRatio(value *float64) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.3f", *value)
}

func formatCost(microUSD *float64, currency string) string {
	if microUSD == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.6f %s", *microUSD/1e6, currency)
}

func formatDeltaCost(microUSD *float64, currency string) string {
	if microUSD == nil {
		return "n/a"
	}
	sign := ""
	if *microUSD > 0 {
		sign = "+"
	}
	return fmt.Sprintf("%s%.6f %s", sign, *microUSD/1e6, currency)
}

func formatInterval(value *interval) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("[%.4f, %.4f]", value.Low, value.High)
}
