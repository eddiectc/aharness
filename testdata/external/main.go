package main

import (
	"context"
	"fmt"
	"os"

	"github.com/eddiectc/aharness"
)

type externalModel struct{}

func (externalModel) Complete(context.Context, aharness.Request) (aharness.Response, error) {
	return aharness.Response{
		Text:         "ok",
		FinishReason: aharness.FinishStop,
		Usage:        aharness.Usage{InputTokens: 1, OutputTokens: 1, Reported: true},
	}, nil
}

func main() {
	workspace, err := os.MkdirTemp("", "external-workspace-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	stateDir, err := os.MkdirTemp("", "external-state-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(stateDir) }()

	ag, err := aharness.New(context.Background(), aharness.Config{
		Workspace: workspace,
		StateDir:  stateDir,
		Model: aharness.CustomModel(aharness.ModelSpec{
			ID:            "external-model",
			Provider:      "external",
			Dialect:       aharness.DialectCustom,
			ContextWindow: 16_000,
		}, externalModel{}),
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = ag.Close() }()
	result, err := ag.Run(context.Background(), "build me", aharness.RunOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Status)
}
