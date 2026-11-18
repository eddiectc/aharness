package engine

import (
	"context"
	"os"
	"testing"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/model"
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
