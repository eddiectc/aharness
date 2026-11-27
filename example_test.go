package aharness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/eddiectc/aharness"
)

// exampleModel is a deterministic model used by the example.
type exampleModel struct{}

func (exampleModel) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
	return aharness.Response{
		Text:         "hello from aharness",
		FinishReason: aharness.FinishStop,
		Usage:        aharness.Usage{InputTokens: 3, OutputTokens: 4, Reported: true},
	}, nil
}

// Example runs one blocking task with a caller-supplied model.
func Example() {
	workspace, err := os.MkdirTemp("", "aharness-workspace-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	stateDir, err := os.MkdirTemp("", "aharness-state-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(stateDir) }()

	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "example/1.0.0",
		Model: aharness.CustomModel(aharness.ModelSpec{
			ID:            "example-model",
			Provider:      "example",
			Dialect:       aharness.DialectCustom,
			ContextWindow: 32_000,
		}, exampleModel{}),
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = ag.Close() }()

	result, err := ag.Run(context.Background(), "Say hello.", aharness.RunOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Status)
	fmt.Println(result.FinalText)
	// Output:
	// completed
	// hello from aharness
}

// exampleScriptedModel returns a fixed sequence of responses. It is
// self-contained so the examples do not depend on test helpers.
type exampleScriptedModel struct {
	mu        sync.Mutex
	responses []aharness.Response
}

func (m *exampleScriptedModel) Complete(_ context.Context, _ aharness.Request) (aharness.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.responses) == 0 {
		return aharness.Response{Text: "done", FinishReason: aharness.FinishStop}, nil
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

// Example_delegation shows the required host-supplied definition set, and is
// the starting template for a host's own agents. The parent model delegates one
// task to the read-only explorer, then answers.
func Example_delegation() {
	workspace, err := os.MkdirTemp("", "aharness-workspace-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	stateDir, err := os.MkdirTemp("", "aharness-state-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(stateDir) }()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("hello\n"), 0o600); err != nil {
		panic(err)
	}

	delegateArgs, err := json.Marshal(map[string]any{
		"intent": "ask the explorer for the note",
		"tasks": []map[string]string{{
			"agent": "explorer",
			"task":  "Read note.txt and report its contents.",
		}},
	})
	if err != nil {
		panic(err)
	}

	parent := &exampleScriptedModel{responses: []aharness.Response{
		{
			ToolCalls: []aharness.ToolCall{{
				ID:        "delegate-1",
				Name:      "delegate",
				Arguments: delegateArgs,
			}},
			FinishReason: aharness.FinishToolCalls,
		},
		{Text: "The explorer reported: note.txt says hello.", FinishReason: aharness.FinishStop},
	}}
	model := aharness.CustomModel(aharness.ModelSpec{
		ID:            "example-model",
		Provider:      "example",
		Dialect:       aharness.DialectCustom,
		ContextWindow: 32_000,
	}, parent)
	model.NewCompleter = func(string) aharness.Model {
		return &exampleScriptedModel{responses: []aharness.Response{{
			Text:         "note.txt says hello.",
			FinishReason: aharness.FinishStop,
		}}}
	}

	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		UserAgent: "example/1.0.0",
		Model:     model,
		Tools: &aharness.ToolsConfig{
			Delegation: &aharness.DelegationConfig{
				Agents: []aharness.AgentDefinition{
					{
						Name:        "explorer",
						Description: "Read-only exploration.",
						Prompt:      "Read the assigned input and report the relevant facts.",
						Tools:       []aharness.ToolName{aharness.ToolReadFile, aharness.ToolSearch},
					},
					{
						Name:        "implementer",
						Description: "Scoped implementation.",
						Prompt:      "Make the assigned change and report what changed.",
						Tools:       []aharness.ToolName{aharness.ToolReadFile, aharness.ToolEditFile, aharness.ToolSearch},
					},
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = ag.Close() }()

	result, err := ag.Run(context.Background(), "Read the note.", aharness.RunOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Status)
	fmt.Println(result.FinalText)
	for _, child := range result.Children {
		fmt.Printf("%s: %s\n", child.Agent, child.Status)
	}
	// Output:
	// completed
	// The explorer reported: note.txt says hello.
	// explorer: completed
}
