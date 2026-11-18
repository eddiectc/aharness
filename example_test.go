package aharness_test

import (
	"context"
	"fmt"
	"os"

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
