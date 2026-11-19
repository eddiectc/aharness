package aharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/eddiectc/aharness"
	"github.com/eddiectc/aharness/session"
)

// sessionStartPayload decodes the first event of a session log. It is the raw
// recorded shape, so a test can assert that an optional field is absent rather
// than merely empty.
func sessionStartPayload(t *testing.T, stateDir, sessionID string) map[string]json.RawMessage {
	t.Helper()
	events, err := session.Read(session.SessionPath(stateDir, sessionID))
	if err != nil {
		t.Fatalf("read session %s: %v", sessionID, err)
	}
	if len(events) == 0 || events[0].Type != session.TypeSessionStart {
		t.Fatalf("session %s has no session_start", sessionID)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(events[0].Data, &payload); err != nil {
		t.Fatalf("decode session_start: %v", err)
	}
	return payload
}

func recordedPromptTag(t *testing.T, stateDir, sessionID string) string {
	t.Helper()
	raw, ok := sessionStartPayload(t, stateDir, sessionID)["prompt_tag"]
	if !ok {
		return ""
	}
	var tag string
	if err := json.Unmarshal(raw, &tag); err != nil {
		t.Fatalf("decode prompt_tag: %v", err)
	}
	return tag
}

func promptTagTools() *aharness.ToolsConfig { return &aharness.ToolsConfig{} }

func TestPromptTagRecordedAndInherited(t *testing.T) {
	const tag = "acode/prompt-v1"
	stateDir := t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
		PromptTag: tag,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := ag.ID()
	state, err := ag.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.PromptTag != tag {
		t.Fatalf("State.PromptTag = %q, want %q", state.PromptTag, tag)
	}
	if got := recordedPromptTag(t, stateDir, id); got != tag {
		t.Fatalf("recorded prompt_tag = %q, want %q", got, tag)
	}
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, err := aharness.Resume(context.Background(), aharness.Config{
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
	}, id)
	if err != nil {
		t.Fatalf("Resume with no tag: %v", err)
	}
	state, err = resumed.State()
	if err != nil {
		t.Fatalf("resumed State: %v", err)
	}
	if state.PromptTag != tag {
		t.Fatalf("resumed State.PromptTag = %q, want inherited %q", state.PromptTag, tag)
	}
	if err := resumed.Close(); err != nil {
		t.Fatalf("resumed Close: %v", err)
	}

	equal, err := aharness.Resume(context.Background(), aharness.Config{
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
		PromptTag: tag,
	}, id)
	if err != nil {
		t.Fatalf("Resume with equal tag: %v", err)
	}
	if err := equal.Close(); err != nil {
		t.Fatalf("equal Close: %v", err)
	}

	_, err = aharness.Resume(context.Background(), aharness.Config{
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
		PromptTag: "acode/prompt-v2",
	}, id)
	if err == nil {
		t.Fatal("Resume accepted a different prompt tag")
	}
	var configErr *aharness.ConfigError
	if !errors.As(err, &configErr) || configErr.Field != "PromptTag" {
		t.Fatalf("Resume different tag error = %v, want ConfigError for PromptTag", err)
	}
	if !strings.Contains(err.Error(), "recorded; fork to change it") {
		t.Fatalf("Resume different tag error = %v, want the fork hint", err)
	}
}

func TestPromptTagUnsetAndLegacyResume(t *testing.T) {
	stateDir := t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := ag.ID()
	if _, ok := sessionStartPayload(t, stateDir, id)["prompt_tag"]; ok {
		t.Fatal("New without PromptTag recorded one")
	}
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = aharness.Resume(context.Background(), aharness.Config{
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
		PromptTag: "acode/prompt-v1",
	}, id)
	if err == nil {
		t.Fatal("Resume accepted a tag for a session that records none")
	}
	var configErr *aharness.ConfigError
	if !errors.As(err, &configErr) || configErr.Field != "PromptTag" {
		t.Fatalf("Resume unrecorded tag error = %v, want ConfigError for PromptTag", err)
	}
	if !strings.Contains(err.Error(), "fork") {
		t.Fatalf("Resume unrecorded tag error = %v, want the fork hint", err)
	}
}

func TestResumeRejectsPromptTagChange(t *testing.T) {
	const tag = "acode/prompt-v1"
	stateDir := t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
		PromptTag: tag,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := ag.ID()
	if err := ag.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = aharness.Resume(context.Background(), aharness.Config{
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
		PromptTag: "acode/prompt-v2",
	}, id)
	if err == nil {
		t.Fatal("Resume accepted a different prompt tag")
	}
	var configErr *aharness.ConfigError
	if !errors.As(err, &configErr) || configErr.Field != "PromptTag" {
		t.Fatalf("Resume error = %v, want ConfigError for PromptTag", err)
	}
	if !strings.Contains(err.Error(), tag) || !strings.Contains(err.Error(), "fork to change it") {
		t.Fatalf("Resume error = %v, want the recorded tag and the fork hint", err)
	}
}

func TestForkWithoutOverridesKeepsTag(t *testing.T) {
	const tag = "acode/prompt-v1"
	workspace := t.TempDir()
	stateDir := t.TempDir()
	parent, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
		PromptTag: tag,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parentID := parent.ID()
	if err := parent.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	forked, err := aharness.Fork(context.Background(), aharness.Config{
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
	}, parentID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = forked.Close() }()
	state, err := forked.State()
	if err != nil {
		t.Fatalf("fork State: %v", err)
	}
	if state.PromptTag != tag {
		t.Fatalf("fork State.PromptTag = %q, want inherited %q", state.PromptTag, tag)
	}
	if got := recordedPromptTag(t, stateDir, forked.ID()); got != tag {
		t.Fatalf("fork recorded prompt_tag = %q, want %q", got, tag)
	}
	if state.ContractOverride != nil {
		t.Fatalf("fork with no overrides recorded %+v", state.ContractOverride)
	}
}

func TestForkToolsChangeKeepsTag(t *testing.T) {
	const tag = "acode/prompt-v1"
	stateDir := t.TempDir()
	parent, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     promptTagTools(),
		PromptTag: tag,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parentID := parent.ID()
	if err := parent.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	forked, err := aharness.Fork(context.Background(), aharness.Config{
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     testModel(&scriptedModel{}),
		Tools:     &aharness.ToolsConfig{Disable: []aharness.ToolName{aharness.ToolWebSearch}},
	}, parentID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = forked.Close() }()
	state, err := forked.State()
	if err != nil {
		t.Fatalf("fork State: %v", err)
	}
	if state.PromptTag != tag {
		t.Fatalf("tools-only fork State.PromptTag = %q, want %q", state.PromptTag, tag)
	}
	if state.ContractOverride == nil || state.ContractOverride.Prompt {
		t.Fatalf("tools-only fork contract override = %+v, want tools only", state.ContractOverride)
	}
}

func TestForkPromptChangeDropsInheritedTag(t *testing.T) {
	const tag = "acode/prompt-v1"
	for _, tc := range []struct {
		name string
		cfg  aharness.Config
	}{
		{name: "system prompt", cfg: aharness.Config{SystemPrompt: "you are a reviewer"}},
		{name: "instructions", cfg: aharness.Config{Instructions: "extra rule"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			parent, err := aharness.New(context.Background(), aharness.Config{
				Workspace:    t.TempDir(),
				StateDir:     stateDir,
				UserAgent:    "aharness-test/1.0",
				Model:        testModel(&scriptedModel{}),
				Tools:        promptTagTools(),
				SystemPrompt: "you are an analyst",
				PromptTag:    tag,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			parentID := parent.ID()
			if err := parent.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			cfg := tc.cfg
			cfg.StateDir = stateDir
			cfg.UserAgent = "aharness-test/1.0"
			cfg.Model = testModel(&scriptedModel{})
			cfg.Tools = promptTagTools()
			forked, err := aharness.Fork(context.Background(), cfg, parentID)
			if err != nil {
				t.Fatalf("Fork: %v", err)
			}
			defer func() { _ = forked.Close() }()
			state, err := forked.State()
			if err != nil {
				t.Fatalf("fork State: %v", err)
			}
			if state.PromptTag != "" {
				t.Fatalf("fork State.PromptTag = %q, want none for a changed prompt", state.PromptTag)
			}
			if _, ok := sessionStartPayload(t, stateDir, forked.ID())["prompt_tag"]; ok {
				t.Fatal("fork recorded a prompt_tag for a changed prompt")
			}
			if state.ContractOverride == nil || !state.ContractOverride.Prompt {
				t.Fatalf("fork contract override = %+v, want a prompt override", state.ContractOverride)
			}
		})
	}
}

func TestForkExplicitTagWinsOverChangedPrompt(t *testing.T) {
	const (
		parentTag = "acode/prompt-v1"
		forkTag   = "acode/prompt-v2"
	)
	stateDir := t.TempDir()
	parent, err := aharness.New(context.Background(), aharness.Config{
		Workspace:    t.TempDir(),
		StateDir:     stateDir,
		UserAgent:    "aharness-test/1.0",
		Model:        testModel(&scriptedModel{}),
		Tools:        promptTagTools(),
		SystemPrompt: "you are an analyst",
		PromptTag:    parentTag,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parentID := parent.ID()
	if err := parent.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	forked, err := aharness.Fork(context.Background(), aharness.Config{
		StateDir:     stateDir,
		UserAgent:    "aharness-test/1.0",
		Model:        testModel(&scriptedModel{}),
		Tools:        promptTagTools(),
		SystemPrompt: "you are a reviewer",
		PromptTag:    forkTag,
	}, parentID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	defer func() { _ = forked.Close() }()
	state, err := forked.State()
	if err != nil {
		t.Fatalf("fork State: %v", err)
	}
	if state.PromptTag != forkTag {
		t.Fatalf("fork State.PromptTag = %q, want explicit %q", state.PromptTag, forkTag)
	}
	if got := recordedPromptTag(t, stateDir, forked.ID()); got != forkTag {
		t.Fatalf("fork recorded prompt_tag = %q, want %q", got, forkTag)
	}
	if state.ContractOverride == nil || !state.ContractOverride.Prompt {
		t.Fatalf("fork contract override = %+v, want a prompt override", state.ContractOverride)
	}
}

func TestDelegatedChildDoesNotRecordParentPromptTag(t *testing.T) {
	parentModel := &scriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "delegate-1",
				Name:      "delegate",
				Arguments: delegateArgs(map[string]string{"agent": "analyst", "task": "one"}),
			}},
			FinishReason: aharness.FinishToolCalls,
			Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		},
		{Text: "parent done", FinishReason: aharness.FinishStop, Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true}},
	}}
	modelCfg := testModel(parentModel)
	modelCfg.NewCompleter = func(string) aharness.Model {
		return &scriptedModel{responses: []aharness.Response{{
			Text: "child done", FinishReason: aharness.FinishStop,
			Usage: aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
		}}}
	}
	stateDir := t.TempDir()
	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: t.TempDir(),
		StateDir:  stateDir,
		UserAgent: "aharness-test/1.0",
		Model:     modelCfg,
		Tools:     &aharness.ToolsConfig{Delegation: readOnlyDelegation(1)},
		PromptTag: "acode/prompt-v1",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = ag.Close() }()
	result, err := ag.Run(context.Background(), "delegate", aharness.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Children) != 1 || result.Children[0].Session == "" {
		t.Fatalf("Children = %+v, want one started child", result.Children)
	}
	if got := recordedPromptTag(t, stateDir, ag.ID()); got != "acode/prompt-v1" {
		t.Fatalf("parent recorded prompt_tag = %q", got)
	}
	payload := sessionStartPayload(t, stateDir, result.Children[0].Session)
	if _, ok := payload["prompt_tag"]; ok {
		t.Fatalf("child session_start recorded the parent prompt tag: %s", payload["prompt_tag"])
	}
}
