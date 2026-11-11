package main

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func evalTreePath(t *testing.T, parts ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	all := append([]string{filepath.Dir(file), "..", "..", "eval"}, parts...)
	return filepath.Join(all...)
}

func TestTaskSuiteShape(t *testing.T) {
	t.Parallel()
	tasks, err := loadTasks(evalTreePath(t, "tasks"))
	if err != nil {
		t.Fatalf("loadTasks: %v", err)
	}
	if len(tasks) != 30 {
		t.Fatalf("tasks = %d, want 30", len(tasks))
	}
	clusterCounts := make(map[string]int)
	for _, task := range tasks {
		clusterCounts[task.Meta.Cluster]++
		if task.Meta.Cluster == "" || task.Meta.Language == "" {
			t.Errorf("%s: empty cluster or language", task.ID)
		}
		if task.Meta.Notes == "" {
			t.Errorf("%s: empty notes", task.ID)
		}
		if task.Timeout <= 0 {
			t.Errorf("%s: timeout = %s, want positive", task.ID, task.Timeout)
		}
		for _, name := range []string{setupFile, verifyFile} {
			path := filepath.Join(task.Dir, name)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("%s: %v", task.ID, err)
			}
			if info.Mode().Perm()&0o111 == 0 {
				t.Errorf("%s: %s is not executable (mode %v)", task.ID, name, info.Mode())
			}
		}
	}
	if len(clusterCounts) < 3 {
		t.Fatalf("clusters = %d, want at least 3", len(clusterCounts))
	}
	for cluster, count := range clusterCounts {
		if count < 10 {
			t.Errorf("cluster %s has %d tasks, want at least 10", cluster, count)
		}
	}
}

func TestBaselineStatusRecord(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(evalTreePath(t, "baseline.json"))
	if err != nil {
		t.Fatalf("read baseline.json: %v", err)
	}
	var baseline struct {
		Status   string `json:"status"`
		Reason   string `json:"reason"`
		Tasks    int    `json:"tasks"`
		Clusters int    `json:"clusters"`
		Seed     int64  `json:"seed"`
		Repeats  int    `json:"repeats"`
	}
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("decode baseline.json: %v", err)
	}
	if baseline.Status != "unmeasured" {
		t.Errorf("status = %q, want unmeasured (never fabricate a model baseline)", baseline.Status)
	}
	if baseline.Reason == "" {
		t.Error("baseline reason is empty")
	}
	if baseline.Tasks != 30 || baseline.Clusters != 3 || baseline.Seed != 1 || baseline.Repeats != 3 {
		t.Errorf("baseline = %+v, want tasks 30, clusters 3, seed 1, repeats 3", baseline)
	}
}

func TestShippedPriceFile(t *testing.T) {
	t.Parallel()
	table, err := loadPriceFile(evalTreePath(t, "prices.json"))
	if err != nil {
		t.Fatalf("loadPriceFile: %v", err)
	}
	const model = "deepseek-v4.1-flash"
	entry, ok := table.Models[model]
	if !ok {
		t.Fatalf("model %s missing from prices.json", model)
	}
	want := rates{Input: 150000, Output: 600000, CacheRead: 3000, CacheWrite: 180000}
	if entry.Rates != want {
		t.Errorf("%s rates = %+v, want %+v", model, entry.Rates, want)
	}
	if entry.Peak == nil || entry.Peak.Multiplier <= 1 {
		t.Errorf("%s peak = %+v, want an example multiplier", model, entry.Peak)
	}
}
