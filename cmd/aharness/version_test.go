package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteBuildInfo(t *testing.T) {
	var buf bytes.Buffer
	writeBuildInfo(&buf, buildInfo{
		Version:     "v1.2.3",
		GoVersion:   "go1.27.3",
		VCSRevision: "abc123",
		VCSModified: true,
		HasVCS:      true,
	})
	out := buf.String()
	for _, want := range []string{"aharness v1.2.3", "go: go1.27.3", "vcs revision: abc123", "vcs modified: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("writeBuildInfo output = %q, want substring %q", out, want)
		}
	}
}

func TestWriteBuildInfoUnknownVCS(t *testing.T) {
	var buf bytes.Buffer
	writeBuildInfo(&buf, buildInfo{
		Version:   "devel",
		GoVersion: "go1.27.3",
	})
	out := buf.String()
	for _, want := range []string{"vcs revision: unknown", "vcs modified: unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("writeBuildInfo output = %q, want substring %q", out, want)
		}
	}
}
