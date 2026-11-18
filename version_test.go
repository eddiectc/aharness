package aharness

import (
	"runtime/debug"
	"testing"
)

func TestVersionFromBuildInfo(t *testing.T) {
	cases := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "no build info",
			info: nil,
			want: "devel",
		},
		{
			name: "main module version",
			info: &debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "v0.0.1"}},
			want: "v0.0.1",
		},
		{
			name: "main module devel",
			info: &debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "(devel)"}},
			want: "devel",
		},
		{
			name: "main module without version",
			info: &debug.BuildInfo{Main: debug.Module{Path: modulePath}},
			want: "devel",
		},
		{
			name: "embedded sdk reports its own version",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "github.com/eddiectc/acode", Version: "v0.4.2"},
				Deps: []*debug.Module{{Path: modulePath, Version: "v0.0.1"}},
			},
			want: "v0.0.1",
		},
		{
			name: "host version never leaks",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/host", Version: "v9.9.9"},
				Deps: []*debug.Module{{Path: modulePath, Version: "v0.0.1"}},
			},
			want: "v0.0.1",
		},
		{
			name: "unrelated dependencies ignored",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/host", Version: "v1.0.0"},
				Deps: []*debug.Module{
					{Path: "golang.org/x/net", Version: "v0.62.0"},
					{Path: "example.com/other", Version: "v2.0.0"},
				},
			},
			want: "devel",
		},
		{
			name: "subpackage path is not the module",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/host", Version: "v1.0.0"},
				Deps: []*debug.Module{{Path: modulePath + "/model", Version: "v0.0.1"}},
			},
			want: "devel",
		},
		{
			name: "nil dependency skipped",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/host", Version: "v1.0.0"},
				Deps: []*debug.Module{nil, {Path: modulePath, Version: "v0.0.1"}},
			},
			want: "v0.0.1",
		},
		{
			name: "module replacement reports replacement version",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/host", Version: "v1.0.0"},
				Deps: []*debug.Module{{
					Path:    modulePath,
					Version: "v0.0.1",
					Replace: &debug.Module{Path: modulePath, Version: "v0.0.2"},
				}},
			},
			want: "v0.0.2",
		},
		{
			name: "filesystem replacement has no version",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/host", Version: "v1.0.0"},
				Deps: []*debug.Module{{
					Path:    modulePath,
					Version: "v0.0.1",
					Replace: &debug.Module{Path: "../aharness"},
				}},
			},
			want: "devel",
		},
		{
			name: "devel dependency version",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/host", Version: "v1.0.0"},
				Deps: []*debug.Module{{Path: modulePath, Version: "(devel)"}},
			},
			want: "devel",
		},
		{
			name: "dependency without version",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/host", Version: "v1.0.0"},
				Deps: []*debug.Module{{Path: modulePath}},
			},
			want: "devel",
		},
		{
			name: "first sdk entry wins",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/host", Version: "v1.0.0"},
				Deps: []*debug.Module{
					{Path: modulePath, Version: "v0.0.1"},
					{Path: modulePath, Version: "v0.0.2"},
				},
			},
			want: "v0.0.1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionFromBuildInfo(tc.info); got != tc.want {
				t.Fatalf("versionFromBuildInfo() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestVersion proves Version reads the build information embedded in the test
// binary. The expected value is derived from the same build information, so the
// test holds whether the SDK is the main module or a dependency.
func TestVersion(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		if got := Version(); got != "devel" {
			t.Fatalf("Version() = %q, want %q without build info", got, "devel")
		}
		return
	}
	want := versionFromBuildInfo(info)
	if got := Version(); got != want {
		t.Fatalf("Version() = %q, want %q from build info", got, want)
	}
	if want == "" {
		t.Fatal("Version() returned an empty version")
	}
}
