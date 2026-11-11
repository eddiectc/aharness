package tools

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
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
			want: "lines 2-2 of 3\n2\tbeta\ncontinue with offset=3\n",
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
	mustWriteFile(t, dir, "many.txt", []byte(strings.Repeat("line\n", 5000)))

	res := executeArgs(t, r, "read_file", map[string]any{"path": "many.txt"})
	if !res.OK {
		t.Fatalf("read_file(default limit) failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Output, "lines 1-2000 of 5000\n") {
		t.Errorf("read_file(default limit) output starts %q, want lines 1-2000 of 5000", firstLine(res.Output))
	}
	if !strings.HasSuffix(res.Output, "continue with offset=2001\n") {
		t.Errorf("read_file(default limit) output does not end with the continuation footer")
	}
	if res.Truncated {
		t.Errorf("read_file(default limit) truncated = true, want false for normal paging")
	}

	res = executeArgs(t, r, "read_file", map[string]any{"path": "many.txt", "offset": 2001, "limit": 4000})
	if !res.OK {
		t.Fatalf("read_file(limit 4000) failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Output, "lines 2001-5000 of 5000\n") {
		t.Errorf("read_file(offset 2001) output starts %q, want lines 2001-5000 of 5000", firstLine(res.Output))
	}
	if strings.Contains(res.Output, "continue with offset=") {
		t.Errorf("read_file(offset 2001) emitted an unnecessary continuation footer")
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

	res = executeArgs(t, r, "read_file", map[string]any{"path": "many.txt", "limit": 10000})
	if !res.OK {
		t.Fatalf("read_file(limit over max) failed: %s", res.Error)
	}
	if !strings.HasPrefix(res.Output, "lines 1-4000 of 5000\n") {
		t.Errorf("read_file(limit over max) output starts %q, want the limit capped at 4000", firstLine(res.Output))
	}
	if res.Truncated {
		t.Errorf("read_file(limit over max) truncated = true, want false")
	}
}

func TestReadFileOneCallDefault(t *testing.T) {
	r, dir := newTestRegistry(t)
	var content strings.Builder
	for line := 1; line <= 1175; line++ {
		fmt.Fprintf(&content, "%04d %s\n", line, strings.Repeat("x", 30))
	}
	if got := content.Len(); got < 40_000 || got > 44_000 {
		t.Fatalf("fixture size = %d bytes, want roughly 42 KB", got)
	}
	mustWriteFile(t, dir, "source.go", []byte(content.String()))

	res := executeArgs(t, r, "read_file", map[string]any{"path": "source.go"})
	if !res.OK {
		t.Fatalf("read_file(42 KB) failed: %s", res.Error)
	}
	if res.Truncated {
		t.Errorf("read_file(42 KB) truncated = true, want false")
	}
	if !strings.HasPrefix(res.Output, "lines 1-1175 of 1175\n") {
		t.Errorf("read_file(42 KB) output starts %q", firstLine(res.Output))
	}
	if !strings.HasSuffix(res.Output, "1175\t1175 "+strings.Repeat("x", 30)+"\n") {
		t.Errorf("read_file(42 KB) did not deliver the last line in one call")
	}
	if strings.Contains(res.Output, "continue with offset=") {
		t.Errorf("read_file(42 KB) emitted a continuation footer")
	}
}

func TestReadFilePageNoMiddleLoss(t *testing.T) {
	r, dir := newTestRegistry(t)
	const lines = 1000
	var content strings.Builder
	for line := 1; line <= lines; line++ {
		fmt.Fprintf(&content, "%04d %s\n", line, strings.Repeat("y", 74))
	}
	mustWriteFile(t, dir, "wide.txt", []byte(content.String()))

	res := executeArgs(t, r, "read_file", map[string]any{"path": "wide.txt", "limit": 500})
	if !res.OK {
		t.Fatalf("read_file(500-line page) failed: %s", res.Error)
	}
	if res.Truncated {
		t.Errorf("read_file(500-line page) truncated = true, want false")
	}
	header := "lines 1-500 of 1000\n"
	footer := "continue with offset=501\n"
	if !strings.HasPrefix(res.Output, header) {
		t.Fatalf("read_file(500-line page) header = %q, want %q", firstLine(res.Output), strings.TrimSuffix(header, "\n"))
	}
	if !strings.HasSuffix(res.Output, footer) {
		t.Fatalf("read_file(500-line page) footer missing")
	}
	body := strings.TrimSuffix(strings.TrimPrefix(res.Output, header), footer)
	got := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(got) != 500 {
		t.Fatalf("delivered lines = %d, want 500", len(got))
	}
	for i, line := range got {
		n := i + 1
		want := strconv.Itoa(n) + "\t" + fmt.Sprintf("%04d %s", n, strings.Repeat("y", 74))
		if line != want {
			t.Fatalf("delivered line %d = %q, want %q", n, line, want)
		}
	}
	if strings.Contains(res.Output, "501\t") {
		t.Errorf("read_file(500-line page) included line 501 past the limit")
	}
}

func TestReadFileByteTruncation(t *testing.T) {
	r, dir := newTestRegistry(t)
	const total = 2000
	line := strings.Repeat("a", 99)
	mustWriteFile(t, dir, "many.txt", []byte(strings.Repeat(line+"\n", total)))

	res := executeArgs(t, r, "read_file", map[string]any{"path": "many.txt"})
	if !res.OK {
		t.Fatalf("read_file(byte cap) failed: %s", res.Error)
	}
	if !res.Truncated {
		t.Fatalf("read_file(byte cap) truncated = false, want true")
	}
	if len(res.Output) > fileOutputCap {
		t.Errorf("output length = %d, want <= %d", len(res.Output), fileOutputCap)
	}
	if strings.Contains(res.Output, "[truncated ") {
		t.Errorf("read_file still used head+tail truncation: %q", res.Output[:200])
	}
	start, end, gotTotal := parseReadHeader(t, res.Output)
	if start != 1 || gotTotal != total {
		t.Fatalf("header = %d-%d of %d, want 1-? of %d", start, end, gotTotal, total)
	}
	if end >= total {
		t.Fatalf("delivered end = %d, want less than %d under the byte cap", end, total)
	}
	if !strings.Contains(res.Output, fmt.Sprintf("continue with offset=%d\n", end+1)) {
		t.Errorf("byte-truncated read missing continuation with offset=%d", end+1)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(res.Output, firstLine(res.Output)+"\n"),
		fmt.Sprintf("continue with offset=%d\n", end+1))
	delivered := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(delivered) != end {
		t.Fatalf("delivered lines = %d, header end = %d", len(delivered), end)
	}
	for i, got := range delivered {
		want := strconv.Itoa(i+1) + "\t" + line
		if got != want {
			t.Fatalf("delivered line %d = %q, want %q", i+1, got, want)
		}
	}
}

func TestReadFileLongLinePartial(t *testing.T) {
	r, dir := newTestRegistry(t)
	line := strings.Repeat("x", 100_000)
	mustWriteFile(t, dir, "long.txt", []byte(line+"\n"))

	res := executeArgs(t, r, "read_file", map[string]any{"path": "long.txt"})
	if !res.OK {
		t.Fatalf("read_file(long line) failed: %s", res.Error)
	}
	if !res.Truncated {
		t.Fatalf("read_file(long line) truncated = false, want true")
	}
	if len(res.Output) > fileOutputCap {
		t.Errorf("output length = %d, want <= %d", len(res.Output), fileOutputCap)
	}
	if !strings.HasPrefix(res.Output, "lines 1-1 of 1\n") {
		t.Errorf("header = %q, want lines 1-1 of 1", firstLine(res.Output))
	}
	if !strings.Contains(res.Output, "[line 1 truncated at ") {
		t.Errorf("output missing the line truncation marker")
	}
	if !strings.Contains(res.Output, "use search or run_command to inspect the rest of line 1") {
		t.Errorf("output missing the inspection hint")
	}
	if strings.Contains(res.Output, "continue with offset=") {
		t.Errorf("partial line emitted a misleading continuation footer")
	}
	if !utf8.ValidString(res.Output) {
		t.Errorf("output is not valid UTF-8")
	}
}

func TestReadFileLongMultibyteLinePartial(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "long.txt", []byte(strings.Repeat("é", 60_000)+"\n"))
	res := executeArgs(t, r, "read_file", map[string]any{"path": "long.txt"})
	if !res.OK {
		t.Fatalf("read_file(multibyte long line) failed: %s", res.Error)
	}
	if !res.Truncated {
		t.Fatalf("read_file(multibyte long line) truncated = false, want true")
	}
	if !utf8.ValidString(res.Output) {
		t.Errorf("output is not valid UTF-8")
	}
	if strings.Contains(res.Output, "\uFFFD") {
		t.Errorf("output contains a replacement rune; prefix was not rune-safe")
	}
	if len(res.Output) > fileOutputCap {
		t.Errorf("output length = %d, want <= %d", len(res.Output), fileOutputCap)
	}
}

func TestReadFileBoundaries(t *testing.T) {
	r, dir := newTestRegistry(t)
	tests := []struct {
		name    string
		content string
		args    map[string]any
		want    string
		trunc   bool
	}{
		{
			name:    "empty file",
			content: "",
			args:    map[string]any{"path": "f"},
			want:    "lines 0-0 of 0\n",
		},
		{
			name:    "offset past end",
			content: "a\nb\n",
			args:    map[string]any{"path": "f", "offset": 10},
			want:    "lines 0-0 of 2\n",
		},
		{
			name:    "offset equals total",
			content: "a\nb\n",
			args:    map[string]any{"path": "f", "offset": 2},
			want:    "lines 2-2 of 2\n2\tb\n",
		},
		{
			name:    "trailing newline and crlf",
			content: "a\r\nb\r\n",
			args:    map[string]any{"path": "f", "offset": 1, "limit": 1},
			want:    "lines 1-1 of 2\n1\ta\r\ncontinue with offset=2\n",
		},
		{
			name:    "multibyte",
			content: "héllo\nwörld\n",
			args:    map[string]any{"path": "f", "limit": 1},
			want:    "lines 1-1 of 2\n1\théllo\ncontinue with offset=2\n",
		},
		{
			name:    "no trailing newline",
			content: "only",
			args:    map[string]any{"path": "f"},
			want:    "lines 1-1 of 1\n1\tonly\n",
		},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := "boundary-" + strconv.Itoa(i)
			mustWriteFile(t, dir, file, []byte(tc.content))
			tc.args["path"] = file
			res := executeArgs(t, r, "read_file", tc.args)
			if !res.OK {
				t.Fatalf("read_file failed: code=%s error=%s", res.Code, res.Error)
			}
			if res.Output != tc.want {
				t.Errorf("output = %q, want %q", res.Output, tc.want)
			}
			if res.Truncated != tc.trunc {
				t.Errorf("truncated = %v, want %v", res.Truncated, tc.trunc)
			}
		})
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

func parseReadHeader(t *testing.T, output string) (start, end, total int) {
	t.Helper()
	header := firstLine(output)
	if _, err := fmt.Sscanf(header, "lines %d-%d of %d", &start, &end, &total); err != nil {
		t.Fatalf("header %q: %v", header, err)
	}
	return start, end, total
}
