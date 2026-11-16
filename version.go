package aharness

import "runtime/debug"

// Version returns the library's build version, or "devel" when the build does
// not carry one. It is recorded as harness_version in new sessions.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "devel"
	}
	return info.Main.Version
}
