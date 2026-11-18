package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
	"github.com/eddiectc/aharness/session"
)

type engineModel struct{}

func (engineModel) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: "ok", FinishReason: model.FinishStop}, nil
}

func baseSpec(t *testing.T, mode Mode) Spec {
	t.Helper()
	return Spec{
		Workspace:      t.TempDir(),
		StateDir:       t.TempDir(),
		Mode:           mode,
		UserAgent:      "test-host/1.0",
		Provider:       "test",
		Dialect:        "custom",
		Model:          "engine-model",
		Limits:         agent.Limits{MaxContextTokens: 10_000, MaxOutputTokens: 100, SafetyMargin: 1000, MaxInputTokens: 9000, CompactionTarget: 9000},
		Grant:          []string{},
		GrantSet:       true,
		CustomToolsSet: true,
		NewCompleter: func(model.Dialect, string, string) model.Completer {
			return engineModel{}
		},
	}
}

func TestOpenNewPreservesEmptyAndNilGrants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grant []string
		nil   bool
	}{
		{name: "empty", grant: []string{}},
		{name: "nil", grant: nil, nil: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseSpec(t, ModeNew)
			spec.Grant = tc.grant
			host, err := Open(context.Background(), spec)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = host.Close() }()
			state, err := host.State()
			if err != nil {
				t.Fatalf("State: %v", err)
			}
			if tc.nil {
				if state.Tools != nil {
					t.Fatalf("nil grant recorded as %#v", state.Tools)
				}
			} else {
				if state.Tools == nil || len(state.Tools) != 0 {
					t.Fatalf("empty grant recorded as %#v", state.Tools)
				}
			}
		})
	}
}

func TestOpenRequiresUserAgentAndFillsToolOptions(t *testing.T) {
	for _, value := range []string{"", "   "} {
		spec := baseSpec(t, ModeNew)
		spec.UserAgent = value
		_, err := Open(context.Background(), spec)
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Field != "UserAgent" {
			t.Fatalf("Open with UserAgent %q: err = %v, want ConfigError for UserAgent", value, err)
		}
	}

	spec := baseSpec(t, ModeNew)
	spec.UserAgent = "host/1.0"
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = host.Close() }()
	if got := host.Spec().ToolOptions.UserAgent; got != "host/1.0" {
		t.Fatalf("ToolOptions.UserAgent = %q, want the host identity", got)
	}

	spec = baseSpec(t, ModeNew)
	spec.UserAgent = "host/1.0"
	spec.ToolOptions.UserAgent = "other/2.0"
	_, err = Open(context.Background(), spec)
	var configErr *ConfigError
	if !errors.As(err, &configErr) || configErr.Field != "ToolOptions.UserAgent" {
		t.Fatalf("mismatched ToolOptions.UserAgent: err = %v, want ConfigError", err)
	}
}

func TestCleanupWorkRemovesAndBuildRunnerRecreates(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = host.Close() }()
	if _, err := host.BuildRunner(RunSpec{}, nil, nil, nil); err != nil {
		t.Fatalf("BuildRunner: %v", err)
	}
	workDir := host.WorkDir()
	expected := filepath.Join(host.StateDir(), "work", host.ID())
	if workDir != expected {
		t.Fatalf("WorkDir = %q, want %q", workDir, expected)
	}
	if _, err := os.Stat(filepath.Join(workDir, "tmp")); err != nil {
		t.Fatalf("tmp dir missing: %v", err)
	}
	// A child command may leave a read-only directory behind.
	readOnly := filepath.Join(workDir, "locked")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatalf("mkdir read-only: %v", err)
	}
	if err := host.CleanupWork(); err != nil {
		t.Fatalf("CleanupWork: %v", err)
	}
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("work dir still exists: %v", err)
	}
	if _, err := host.BuildRunner(RunSpec{}, nil, nil, nil); err != nil {
		t.Fatalf("BuildRunner after cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "tmp")); err != nil {
		t.Fatalf("tmp dir was not recreated: %v", err)
	}
	if err := host.CleanupWork(); err != nil {
		t.Fatalf("second CleanupWork: %v", err)
	}
}

func TestHostReadSurface(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = host.Close() }()
	todoDetails := json.RawMessage(`{"items":[{"content":"one","status":"pending"}]}`)
	events := []struct {
		typ     session.EventType
		role    string
		payload any
	}{
		{session.TypeUserMessage, "user", map[string]string{"text": "hello", "source": "task"}},
		{session.TypeToolResult, "tool", map[string]any{
			"call_id": "call-1", "name": "todo", "ok": true, "code": "ok", "details": todoDetails,
		}},
	}
	for _, item := range events {
		ev, err := session.NewEvent(item.typ, 1, item.role, item.payload)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if err := host.Store().Append(ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	history, err := host.History()
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) == 0 || history[0].Text != "hello" {
		t.Fatalf("history = %+v", history)
	}
	todo := host.Todo()
	if len(todo) != 1 || todo[0].Content != "one" || todo[0].Status != "pending" {
		t.Fatalf("todo = %+v", todo)
	}
	if got, want := host.LastSeq(), host.Store().LastSeq(); got != want {
		t.Fatalf("LastSeq = %d, want %d", got, want)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := host.History(); err == nil {
		t.Fatal("History after Close succeeded")
	}
}

func TestResumeInheritsModelAndLimits(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open new: %v", err)
	}
	sessionID := host.ID()
	stateDir := host.StateDir()
	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, err := Open(context.Background(), Spec{
		StateDir:  stateDir,
		Mode:      ModeResume,
		SessionID: sessionID,
		UserAgent: "test-host/1.0",
		NewCompleter: func(model.Dialect, string, string) model.Completer {
			return engineModel{}
		},
	})
	if err != nil {
		t.Fatalf("Open resume: %v", err)
	}
	defer func() { _ = resumed.Close() }()
	got := resumed.Spec()
	if got.Model != "engine-model" || got.Limits.MaxContextTokens != 10_000 {
		t.Fatalf("resumed spec = %+v", got)
	}
	if got.Grant == nil || len(got.Grant) != 0 {
		t.Fatalf("resumed grant = %#v, want explicit empty", got.Grant)
	}
}

func TestBuildRunnerCreatesAndCloseRemovesWorkDir(t *testing.T) {
	spec := baseSpec(t, ModeNew)
	host, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	workDir := host.workDir
	if _, err := host.BuildRunner(RunSpec{}, nil, nil, nil); err != nil {
		t.Fatalf("BuildRunner: %v", err)
	}
	if info, err := os.Stat(host.tmpDir); err != nil || !info.IsDir() {
		t.Fatalf("tmp dir missing: %v", err)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("work dir still exists after Close: %v", err)
	}
}
