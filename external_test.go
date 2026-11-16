package aharness_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestExternalModuleBuild proves the root package is importable from a
// separate module through a replace directive.
func TestExternalModuleBuild(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "external")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", binary, ".")
	cmd.Dir = "testdata/external"
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("external module build: %v\n%s", err, output)
	}
}
