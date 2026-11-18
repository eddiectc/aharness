package aharness

import "runtime/debug"

// modulePath is this SDK's module path. Version uses it to find the SDK's own
// entry in the build information the linker embeds in the binary.
const modulePath = "github.com/eddiectc/aharness"

// Version returns the library's build version, or "devel" when the build does
// not carry one. It is recorded as harness_version in new sessions.
//
// When the SDK is a dependency, the version is the one resolved for this
// module in the consuming build, so an embedded SDK reports its own release
// rather than the host program's version. When the SDK is the main module, its
// own version is used. The lookup reads build information already embedded in
// the binary; it performs no I/O and no network access.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	return versionFromBuildInfo(info)
}

// versionFromBuildInfo reports the SDK version recorded in info. It is
// separate from Version so the resolution rules are testable without a real
// build.
func versionFromBuildInfo(info *debug.BuildInfo) string {
	if info == nil {
		return "devel"
	}
	for _, dep := range info.Deps {
		if dep == nil || dep.Path != modulePath {
			continue
		}
		if dep.Replace != nil {
			// A module replacement carries the version to report; a filesystem
			// replacement carries none, so the build has no SDK version.
			return versionOrDevel(dep.Replace.Version)
		}
		return versionOrDevel(dep.Version)
	}
	// The SDK is not a dependency, so it is the main module when the build
	// records one: its own version is the one to report. Any other main module
	// means the SDK was linked without module information, so there is no SDK
	// version to report.
	if info.Main.Path != modulePath {
		return "devel"
	}
	return versionOrDevel(info.Main.Version)
}

func versionOrDevel(version string) string {
	if version == "" || version == "(devel)" {
		return "devel"
	}
	return version
}
