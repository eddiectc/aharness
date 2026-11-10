package tools

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFilePaging(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "f.txt", []byte("alpha\nbeta\ngamma\n"))

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "defaults",
			args: map[string]any{"path": "f.txt"},
			want: "lines 1-3 of 3\n1\talpha\n2\tbeta\n3\tgamma\n",
		},
		{
			name: "offset and limit",
			args: map[string]any{"path": "f.txt", "offset": 2, "limit": 1},
			want: "lines 2-2 of 3\n2\tbeta\n",
		},
		{
			name: "last page",
			args: map[string]any{"path": "f.txt", "offset": 3, "limit": 5},
			want: "lines 3-3 of 3\n3\tgamma\n",
		},
		{
			name: "past end",
			args: map[string]any{"path": "f.txt", "offset": 9},
			want: "lines 0-0 of 3\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := executeArgs(t, r, "read_file", tc.args)
			if !res.OK {
				t.Fatalf("read_file(%v) failed: code=%s error=%s", tc.args, res.Code, res.Error)
			}
			if res.Output != tc.want {
				t.Errorf("read_file(%v) output = %q, want %q", tc.args, res.Output, tc.want)
			}
			if res.Truncated {
				t.Errorf("read_file(%v) truncated = true, want false", tc.args)
			}
		})
	}
}

func TestReadFileLineCounting(t *testing.T) {
	r, dir := newTestRegistry(t)
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "lines 0-0 of 0\n"},
		{"no trailing newline", "one", "lines 1-1 of 1\n1\tone\n"},
		{"single newline", "\n", "lines 1-1 of 1\n1\t\n"},
		{"blank middle", "a\n\nb", "lines 1-3 of 3\n1\ta\n2\t\n3\tb\n"},
		{"trailing newline", "a\nb\n", "lines 1-2 of 2\n1\ta\n2\tb\n"},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := "lines-" + string(rune('a'+i)) + ".txt"
			mustWriteFile(t, dir, file, []byte(tc.content))
			res := executeArgs(t, r, "read_file", map[string]any{"path": file})
			if !res.OK {
				t.Fatalf("read_file failed: code=%s error=%s", res.Code, res.Error)
			}
			if res.Output != tc.want {
				t.Errorf("read_file(%q) output = %q, want %q", tc.content, res.Output, tc.want)
			}
		})
	}
}

func TestReadFilePreservesBytes(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "crlf.txt", []byte("a\r\nb\r\n"))
	res := executeArgs(t, r, "read_file", map[string]any{"path": "crlf.txt"})
	if !res.OK {
		t.Fatalf("read_file(crlf) failed: code=%s error=%s", res.Code, res.Error)
	}
	want := "lines 1-2 of 2\n1\ta\r\n2\tb\r\n"
	if res.Output != want {
		t.Errorf("read_file(crlf) output = %q, want %q", res.Output, want)
	}

	mustWriteFile(t, dir, "utf8.txt", []byte("héllo → world\n"))
	res = executeArgs(t, r, "read_file", map[string]any{"path": "utf8.txt"})
	if !res.OK {
		t.Fatalf("read_file(utf8) failed: code=%s error=%s", res.Code, res.Error)
	}
	if !strings.Contains(res.Output, "héllo → world") {
		t.Errorf("read_file(utf8) output = %q, want unicode content preserved", res.Output)
	}
}

func TestReadFileRejectsBinary(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "nul.bin", []byte{'a', 0, 'b'})
	mustWriteFile(t, dir, "invalid.bin", []byte{0xff, 0xfe, 'x'})
	for _, name := range []string{"nul.bin", "invalid.bin"} {
		res := executeArgs(t, r, "read_file", map[string]any{"path": name})
		if res.OK {
			t.Errorf("read_file(%s) OK = true, want binary rejection", name)
			continue
		}
		if res.Code != CodeBinaryFile {
			t.Errorf("read_file(%s) code = %s, want %s", name, res.Code, CodeBinaryFile)
		}
	}
}

func TestReadFileSizeCap(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "big.txt", bytes.Repeat([]byte("a"), maxReadFileBytes+1))
	res := executeArgs(t, r, "read_file", map[string]any{"path": "big.txt"})
	if res.OK {
		t.Fatalf("read_file(over 2 MiB) OK = true, want too_large")
	}
	if res.Code != CodeTooLarge {
		t.Errorf("code = %s, want %s", res.Code, CodeTooLarge)
	}
}

func TestReadFileLimitsAndDefaults(t *testing.T) {
	r, dir := newTestRegistry(t)
	content := strings.Repeat("line\n", 150)
	mustWriteFile(t, dir, "many.txt", []byte(content))

	res := executeArgs(t, r, "read_file", map[string]any{"path": "many.txt"})
	if !res.OK {
		t.Fatalf("read_file(default limit) failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Output, "lines 1-100 of 150\n") {
		t.Errorf("read_file(default limit) output starts %q, want lines 1-100 of 150", firstLine(res.Output))
	}

	res = executeArgs(t, r, "read_file", map[string]any{"path": "many.txt", "offset": 101, "limit": 500})
	if !res.OK {
		t.Fatalf("read_file(limit 500) failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Output, "lines 101-150 of 150\n") {
		t.Errorf("read_file(offset 101) output starts %q, want lines 101-150 of 150", firstLine(res.Output))
	}

	for _, args := range []map[string]any{
		{"path": "many.txt", "limit": 0},
		{"path": "many.txt", "offset": 0},
		{"path": "many.txt", "offset": -1},
	} {
		res := executeArgs(t, r, "read_file", args)
		if res.OK || res.Code != CodeInvalidArgs {
			t.Errorf("read_file(%v) = ok=%v code=%s, want invalid_args", args, res.OK, res.Code)
		}
	}

	mustWriteFile(t, dir, "huge.txt", []byte(strings.Repeat("line\n", 600)))
	res = executeArgs(t, r, "read_file", map[string]any{"path": "huge.txt", "limit": 1000})
	if !res.OK {
		t.Fatalf("read_file(limit over max) failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Output, "lines 1-500 of 600\n") {
		t.Errorf("read_file(limit over max) output starts %q, want the limit capped at 500", firstLine(res.Output))
	}
}

func TestReadFileContainment(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, ".git/config", []byte("secret"))
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	tests := []struct {
		name string
		path string
		code Code
	}{
		{"outside relative", "../outside.txt", CodeOutsideWorkspace},
		{"outside absolute", filepath.Join(outside, "outside.txt"), CodeOutsideWorkspace},
		{"git", ".git/config", CodeGitProtected},
		{"missing", "missing.txt", CodeNotFound},
		{"directory", ".", CodeInvalidArgs},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := executeArgs(t, r, "read_file", map[string]any{"path": tc.path})
			if res.OK {
				t.Fatalf("read_file(%q) OK = true, want failure", tc.path)
			}
			if res.Code != tc.code {
				t.Errorf("read_file(%q) code = %s, want %s (error %q)", tc.path, res.Code, tc.code, res.Error)
			}
		})
	}
}

func TestReadFileSymlinks(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "target.txt", []byte("target\n"))
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	res := executeArgs(t, r, "read_file", map[string]any{"path": "link.txt"})
	if !res.OK {
		t.Fatalf("read_file(in-root symlink) failed: code=%s error=%s", res.Code, res.Error)
	}
	if !strings.Contains(res.Output, "1\ttarget") {
		t.Errorf("read_file(in-root symlink) output = %q, want target contents", res.Output)
	}

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	relativeTarget, err := filepath.Rel(dir, outsideFile)
	if err != nil {
		t.Fatalf("rel outside target: %v", err)
	}
	if err := os.Symlink(relativeTarget, filepath.Join(dir, "escape.txt")); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}
	res = executeArgs(t, r, "read_file", map[string]any{"path": "escape.txt"})
	if res.OK {
		t.Fatalf("read_file(escaping symlink) OK = true, want failure")
	}
	if res.Code != CodeOutsideWorkspace {
		t.Errorf("read_file(escaping symlink) code = %s, want %s", res.Code, CodeOutsideWorkspace)
	}
}

func TestReadFileTruncation(t *testing.T) {
	r, dir := newTestRegistry(t)
	line := strings.Repeat("0123456789", 10)
	mustWriteFile(t, dir, "wide.txt", []byte(strings.Repeat(line+"\n", 500)))
	res := executeArgs(t, r, "read_file", map[string]any{"path": "wide.txt", "limit": 500})
	if !res.OK {
		t.Fatalf("read_file(wide) failed: %s", res.Error)
	}
	if !res.Truncated {
		t.Errorf("read_file(wide) truncated = false, want true")
	}
	if len(res.Output) > fileOutputCap {
		t.Errorf("read_file(wide) output length = %d, want <= %d", len(res.Output), fileOutputCap)
	}
	if !strings.HasPrefix(res.Output, "lines 1-500 of 500\n") {
		t.Errorf("read_file(wide) lost header: %q", firstLine(res.Output))
	}
	if !strings.Contains(res.Output, "[truncated ") {
		t.Errorf("read_file(wide) output missing truncation marker")
	}
}

func TestReadFileAbsolutePathInsideWorkspace(t *testing.T) {
	r, dir := newTestRegistry(t)
	path := mustWriteFile(t, dir, "absolute.txt", []byte("absolute\n"))
	res := executeArgs(t, r, "read_file", map[string]any{"path": path})
	if !res.OK {
		t.Fatalf("read_file(absolute inside) failed: code=%s error=%s", res.Code, res.Error)
	}
	if want := "lines 1-1 of 1\n1\tabsolute\n"; res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
}

func TestReadFileUnknownMember(t *testing.T) {
	r, _ := newTestRegistry(t)
	res := executeRaw(t, r, "read_file", `{"path":"x","surprise":true}`)
	if res.OK || res.Code != CodeInvalidArgs {
		t.Errorf("read_file(unknown member) = ok=%v code=%s, want invalid_args", res.OK, res.Code)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
