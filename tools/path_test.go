package tools

import (
	"path/filepath"
	"testing"
)

func TestFileArgumentPath(t *testing.T) {
	workspace := filepath.Join(string(filepath.Separator), "work", "repo")
	tests := []struct {
		name string
		tool string
		args string
		want string
	}{
		{name: "read relative", tool: "read_file", args: `{"path":"notes/a.txt"}`, want: filepath.Join(workspace, "notes", "a.txt")},
		{name: "read absolute", tool: "read_file", args: `{"path":"/tmp/a.txt"}`, want: "/tmp/a.txt"},
		{name: "read cleaned", tool: "read_file", args: `{"path":"notes/../a.txt"}`, want: filepath.Join(workspace, "a.txt")},
		{name: "edit escaping", tool: "edit_file", args: `{"path":"../outside.txt"}`, want: filepath.Join(workspace, "..", "outside.txt")},
		{name: "search default", tool: "search", args: `{"pattern":"x"}`, want: workspace},
		{name: "search relative", tool: "search", args: `{"pattern":"x","path":"sub"}`, want: filepath.Join(workspace, "sub")},
		{name: "search empty", tool: "search", args: `{"pattern":"x","path":""}`, want: ""},
		{name: "read missing", tool: "read_file", args: `{"offset":1}`, want: ""},
		{name: "read invalid json", tool: "read_file", args: `{`, want: ""},
		{name: "read wrong path type", tool: "read_file", args: `{"path":1}`, want: ""},
		{name: "read unknown member", tool: "read_file", args: `{"path":"a.txt","extra":true}`, want: filepath.Join(workspace, "a.txt")},
		{name: "command has no path", tool: "run_command", args: `{"command":"true","cwd":"sub"}`, want: ""},
		{name: "custom has no path", tool: "lookup", args: `{"path":"a.txt"}`, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FileArgumentPath(workspace, tc.tool, []byte(tc.args)); got != tc.want {
				t.Fatalf("FileArgumentPath(%q, %s) = %q, want %q", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}
