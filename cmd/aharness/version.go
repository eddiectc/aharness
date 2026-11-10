package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

// buildInfo is the subset of runtime/debug build metadata the CLI reports.
type buildInfo struct {
	Version     string
	GoVersion   string
	VCSRevision string
	VCSModified bool
	HasVCS      bool
}

func readBuildInfo() buildInfo {
	info := buildInfo{Version: "devel", GoVersion: runtime.Version()}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if bi.GoVersion != "" {
		info.GoVersion = bi.GoVersion
	}
	if bi.Main.Version != "" {
		info.Version = bi.Main.Version
	}
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.VCSRevision = setting.Value
			info.HasVCS = true
		case "vcs.modified":
			info.VCSModified = setting.Value == "true"
			info.HasVCS = true
		}
	}
	return info
}

func buildVersion() string {
	return readBuildInfo().Version
}

func printVersion(w io.Writer) {
	writeBuildInfo(w, readBuildInfo())
}

func writeBuildInfo(w io.Writer, info buildInfo) {
	revision := info.VCSRevision
	if revision == "" {
		revision = "unknown"
	}
	modified := "unknown"
	if info.HasVCS {
		modified = fmt.Sprintf("%t", info.VCSModified)
	}
	_, _ = fmt.Fprintf(w, "aharness %s\n", info.Version)
	_, _ = fmt.Fprintf(w, "go: %s\n", info.GoVersion)
	_, _ = fmt.Fprintf(w, "vcs revision: %s\n", revision)
	_, _ = fmt.Fprintf(w, "vcs modified: %s\n", modified)
}
