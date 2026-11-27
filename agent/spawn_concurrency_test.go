package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
	"github.com/eddiectc/aharness/tools"
)

func TestIsReadOnlyGrant(t *testing.T) {
	tests := []struct {
		name  string
		grant []string
		want  bool
	}{
		{"nil means the full toolset", nil, false},
		{"empty is not a grant", []string{}, false},
		{"read_file and search", []string{"read_file", "search"}, true},
		{"web tools", []string{"web_search", "web_fetch"}, true},
		{"mixed read-only", []string{"read_file", "search", "web_search", "web_fetch"}, true},
		{"edit_file", []string{"read_file", "edit_file"}, false},
		{"run_command", []string{"run_command"}, false},
		{"todo", []string{"todo"}, false},
		{"delegate", []string{"delegate"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadOnlyGrant(tc.grant); got != tc.want {
				t.Fatalf("isReadOnlyGrant(%v) = %v, want %v", tc.grant, got, tc.want)
			}
		})
	}
}

func TestSpawnerRunsReadOnlyTasksConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		rec := &concurrencyRecorder{}
		barrier := make(chan struct{})
		f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
			return &recorderCompleter{rec: rec, barrier: barrier, text: "report"}
		}
		// The display contract stays single-threaded: the spawner's mutex must
		// keep concurrent children from entering the callback together. The
		// -race run also exercises the callback from several child goroutines.
		var displayMu sync.Mutex
		displayActive, displayMax := 0, 0
		f.spawner.opts.Display = func(DisplayEvent) error {
			displayMu.Lock()
			displayActive++
			if displayActive > displayMax {
				displayMax = displayActive
			}
			displayMu.Unlock()
			displayMu.Lock()
			displayActive--
			displayMu.Unlock()
			return nil
		}

		done := make(chan tools.SpawnResult, 1)
		go func() {
			done <- f.spawner.Spawn(t.Context(), tools.Call{ID: "call-1"}, []tools.SubagentSpec{
				{Agent: "explore", Task: "one"},
				{Agent: "explore", Task: "two"},
				{Agent: "explore", Task: "three"},
			})
		}()
		synctest.Wait()
		started := rec.startedCount()
		if started != 3 {
			close(barrier)
			t.Fatalf("children in flight = %d, want 3 before any completes", started)
		}
		select {
		case <-done:
			t.Fatal("Spawn returned before its children finished")
		default:
		}
		close(barrier)
		synctest.Wait()
		outcome := <-done

		if outcome.Execution != tools.ExecutionConcurrent {
			t.Fatalf("execution = %q, want concurrent", outcome.Execution)
		}
		if len(outcome.Reports) != 3 {
			t.Fatalf("reports = %d, want 3", len(outcome.Reports))
		}
		seen := make(map[string]bool, 3)
		for _, report := range outcome.Reports {
			if report.Status != tools.SubagentCompleted || report.Text != "report" {
				t.Fatalf("report = %+v", report)
			}
			seen[report.Session] = true
		}
		if len(seen) != 3 {
			t.Fatalf("sessions = %v, want three distinct children", seen)
		}
		if maxActive := rec.maxConcurrent(); maxActive != 3 {
			t.Fatalf("overlapping completions = %d, want 3", maxActive)
		}
		displayMu.Lock()
		gotDisplayMax := displayMax
		displayMu.Unlock()
		if gotDisplayMax != 1 {
			t.Fatalf("display callbacks in flight = %d, want serialized", gotDisplayMax)
		}
	})
}

func TestSpawnerWriteCapableTaskForcesSequential(t *testing.T) {
	f := newSpawnFixture(t, testConfig())
	rec := &concurrencyRecorder{}
	f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
		return &recorderCompleter{rec: rec, text: "report"}
	}
	outcome := f.spawnOutcome(t, tools.Call{ID: "call-1"},
		tools.SubagentSpec{Agent: "explore", Task: "read"},
		tools.SubagentSpec{Agent: "implementer", Task: "write"})
	if outcome.Execution != tools.ExecutionSequential {
		t.Fatalf("execution = %q, want sequential", outcome.Execution)
	}
	if !strings.Contains(outcome.ExecutionReason, "implementer") || !strings.Contains(outcome.ExecutionReason, "not read-only") {
		t.Fatalf("execution reason = %q", outcome.ExecutionReason)
	}
	if len(outcome.Reports) != 2 || outcome.Reports[0].Status != tools.SubagentCompleted || outcome.Reports[1].Status != tools.SubagentCompleted {
		t.Fatalf("reports = %+v", outcome.Reports)
	}
	if maxActive := rec.maxConcurrent(); maxActive != 1 {
		t.Fatalf("overlapping completions = %d, want a sequential call", maxActive)
	}
}

func TestSpawnerContinuationJoinsReadOnlyFanOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		f.completer.steps = []scriptStep{completedStep("first run", model.Usage{InputTokens: 1, OutputTokens: 1})}
		first := onlyReport(t, f.spawn(t, tools.Call{ID: "call-1"}, tools.SubagentSpec{Agent: "explore", Task: "one"}))

		rec := &concurrencyRecorder{}
		barrier := make(chan struct{})
		f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
			return &recorderCompleter{rec: rec, barrier: barrier, text: "second"}
		}
		done := make(chan tools.SpawnResult, 1)
		go func() {
			done <- f.spawner.Spawn(t.Context(), tools.Call{ID: "call-2"}, []tools.SubagentSpec{
				{Agent: "explore", Task: "fresh"},
				{Task: "continue", Session: first.Session},
			})
		}()
		synctest.Wait()
		if started := rec.startedCount(); started != 2 {
			close(barrier)
			t.Fatalf("children in flight = %d, want 2", started)
		}
		close(barrier)
		synctest.Wait()
		outcome := <-done
		if outcome.Execution != tools.ExecutionConcurrent {
			t.Fatalf("execution = %q, reason %q, want concurrent", outcome.Execution, outcome.ExecutionReason)
		}
		if outcome.Reports[1].Session != first.Session || outcome.Reports[1].Status != tools.SubagentCompleted {
			t.Fatalf("continuation report = %+v", outcome.Reports[1])
		}
	})
}

func TestSpawnerConcurrentCancellationStopsEveryChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		rec := &concurrencyRecorder{}
		f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
			return &recorderCompleter{rec: rec, blockOnContext: true}
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan tools.SpawnResult, 1)
		go func() {
			done <- f.spawner.Spawn(ctx, tools.Call{ID: "call-1"}, []tools.SubagentSpec{
				{Agent: "explore", Task: "one"},
				{Agent: "explore", Task: "two"},
				{Agent: "explore", Task: "three"},
			})
		}()
		synctest.Wait()
		if started := rec.startedCount(); started != 3 {
			t.Fatalf("children in flight = %d, want 3", started)
		}
		cancel()
		outcome := <-done
		if outcome.Execution != tools.ExecutionConcurrent {
			t.Fatalf("execution = %q, want concurrent", outcome.Execution)
		}
		for i, report := range outcome.Reports {
			if report.Status != tools.SubagentInterrupted || report.Code != tools.CodeCanceled {
				t.Fatalf("report %d = %+v, want interrupted/canceled", i, report)
			}
			events := f.readLog(t, report.Session)
			if events[len(events)-1].Type != session.TypeRunEnd {
				t.Fatalf("child %d last event = %s, want run_end", i, events[len(events)-1].Type)
			}
			final := 0
			for _, ev := range events {
				if ev.Type == session.TypeFinal {
					final++
				}
			}
			if final != 1 {
				t.Fatalf("child %d final events = %d, want 1", i, final)
			}
		}
	})
}

func TestDelegateCancellationMidCallPairsCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newSpawnFixture(t, testConfig())
		rec := &concurrencyRecorder{}
		f.spawner.opts.NewCompleter = func(model.Dialect, string, string) model.Completer {
			return &recorderCompleter{rec: rec, blockOnContext: true}
		}
		registry := tools.NewRegistry(f.workspace, tools.Options{Spawning: f.spawner})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan tools.Result, 1)
		go func() {
			done <- registry.ExecuteCall(ctx, tools.Call{ID: "call-1"}, "delegate",
				json.RawMessage(`{"intent":"fan out","tasks":[`+
					`{"agent":"explore","task":"one"},`+
					`{"agent":"explore","task":"two"},`+
					`{"agent":"explore","task":"three"}]}`))
		}()
		synctest.Wait()
		if started := rec.startedCount(); started != 3 {
			t.Fatalf("children in flight = %d, want 3", started)
		}
		cancel()
		res := <-done
		if res.Code != tools.CodeCanceled {
			t.Fatalf("delegate code = %s (%s), want canceled", res.Code, res.Error)
		}
		details, ok := tools.ParseDelegateDetails(res.Details)
		if !ok || details.Execution != tools.ExecutionConcurrent || len(details.Tasks) != 3 {
			t.Fatalf("delegate details = %+v", details)
		}
		for i, task := range details.Tasks {
			if task.Status != tools.SubagentInterrupted {
				t.Fatalf("task %d status = %q, want interrupted", i+1, task.Status)
			}
		}
	})
}
