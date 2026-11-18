package tools

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSearchBasic(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "a.txt", []byte("hello\nworld\nHello\n"))
	mustWriteFile(t, dir, "sub/b.txt", []byte("hello sub\n"))
	mustWriteFile(t, dir, "sub/c.go", []byte("package x\n"))
	mustWriteFile(t, dir, ".git/config", []byte("hello git\n"))

	res := executeArgs(t, r, "search", map[string]any{"pattern": "hello"})
	if !res.OK {
		t.Fatalf("search failed: code=%s error=%s", res.Code, res.Error)
	}
	want := "a.txt:1: hello\na.txt:3: Hello\nsub/b.txt:1: hello sub\n"
	if res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
	if strings.Contains(res.Output, ".git") {
		t.Errorf("output includes .git content: %q", res.Output)
	}
}

func TestSearchCaseSensitive(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "a.txt", []byte("hello\nworld\nHello\n"))
	res := executeArgs(t, r, "search", map[string]any{
		"pattern":        "hello",
		"case_sensitive": true,
	})
	if !res.OK {
		t.Fatalf("search(case sensitive) failed: %s", res.Error)
	}
	want := "a.txt:1: hello\n"
	if res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
}

func TestSearchGlob(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "a.txt", []byte("hit root\n"))
	mustWriteFile(t, dir, "sub/b.txt", []byte("hit sub\n"))
	mustWriteFile(t, dir, "sub/c.go", []byte("hit go\n"))

	res := executeArgs(t, r, "search", map[string]any{"pattern": "hit", "glob": "*.txt"})
	if !res.OK {
		t.Fatalf("search(glob *.txt) failed: %s", res.Error)
	}
	if want := "a.txt:1: hit root\n"; res.Output != want {
		t.Errorf("search(glob *.txt) output = %q, want %q", res.Output, want)
	}

	res = executeArgs(t, r, "search", map[string]any{"pattern": "hit", "glob": "sub/*.txt"})
	if !res.OK {
		t.Fatalf("search(glob sub/*.txt) failed: %s", res.Error)
	}
	if want := "sub/b.txt:1: hit sub\n"; res.Output != want {
		t.Errorf("search(glob sub/*.txt) output = %q, want %q", res.Output, want)
	}
}

func TestSearchDoubleStarRecursive(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "root.go", []byte("package root\n"))
	mustWriteFile(t, dir, "sub/c.go", []byte("package x\n"))
	mustWriteFile(t, dir, "deep/nested/d.go", []byte("package deep\n"))
	res := executeArgs(t, r, "search", map[string]any{"pattern": "package", "glob": "**/*.go"})
	if !res.OK {
		t.Fatalf("search(**) failed: %s", res.Error)
	}
	want := "deep/nested/d.go:1: package deep\nroot.go:1: package root\nsub/c.go:1: package x\n"
	if res.Output != want {
		t.Errorf("search(**/*.go) output = %q, want %q", res.Output, want)
	}

	res = executeArgs(t, r, "search", map[string]any{"pattern": "package", "glob": "*.go"})
	if !res.OK {
		t.Fatalf("search(*.go) failed: %s", res.Error)
	}
	if want := "root.go:1: package root\n"; res.Output != want {
		t.Errorf("search(*.go) output = %q, want %q (single * must not cross '/')", res.Output, want)
	}

	res = executeArgs(t, r, "search", map[string]any{"pattern": "package", "glob": "deep/**/*.go"})
	if !res.OK {
		t.Fatalf("search(deep/**/*.go) failed: %s", res.Error)
	}
	if want := "deep/nested/d.go:1: package deep\n"; res.Output != want {
		t.Errorf("search(deep/**/*.go) output = %q, want %q", res.Output, want)
	}
}

func TestSearchInvalidRegexAndGlob(t *testing.T) {
	r, _ := newTestRegistry(t)
	tests := []map[string]any{
		{"pattern": "("},
		{"pattern": "ok", "glob": "["},
		{"pattern": ""},
		{"pattern": "ok", "max_results": 0},
		{"pattern": "ok", "path": "missing"},
		{"pattern": "ok", "path": ".."},
		{"pattern": "ok", "path": ".git"},
	}
	wantCodes := []Code{
		CodeInvalidArgs,
		CodeInvalidArgs,
		CodeInvalidArgs,
		CodeInvalidArgs,
		CodeNotFound,
		CodeOutsideWorkspace,
		CodeGitProtected,
	}
	for i, args := range tests {
		res := executeArgs(t, r, "search", args)
		if res.OK {
			t.Errorf("search(%v) OK = true, want failure", args)
			continue
		}
		if res.Code != wantCodes[i] {
			t.Errorf("search(%v) code = %s, want %s (error %q)", args, res.Code, wantCodes[i], res.Error)
		}
	}
}

func TestSearchSkipsBinaryLargeAndSymlinks(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "real/target.txt", []byte("hello linktarget\n"))
	mustWriteFile(t, dir, "binary.bin", append([]byte("hello"), 0, 1, 2))
	mustWriteFile(t, dir, "invalid.bin", []byte{'h', 'e', 'l', 'l', 'o', 0xff})
	mustWriteFile(t, dir, "large.txt", append([]byte("hello "), bytes.Repeat([]byte("a"), maxSearchFileBytes)...))
	if err := os.Symlink("real/target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "linkdir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	res := executeArgs(t, r, "search", map[string]any{"pattern": "hello linktarget"})
	if !res.OK {
		t.Fatalf("search failed: %s", res.Error)
	}
	want := "real/target.txt:1: hello linktarget\n" +
		"[skipped 5 files: 1 over 1 MiB, 2 binary, 2 symlink]\n"
	if res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}

	for _, name := range []string{"binary.bin", "invalid.bin", "large.txt", "link.txt", "linkdir"} {
		if strings.Contains(res.Output, name) {
			t.Errorf("output includes %s: %q", name, res.Output)
		}
	}
	if res.Truncated {
		t.Errorf("Truncated = true for skipped files, want false")
	}
}

func TestSearchNoMatches(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "a.txt", []byte("nothing here\n"))
	res := executeArgs(t, r, "search", map[string]any{"pattern": "zzz"})
	if !res.OK {
		t.Fatalf("search(no matches) OK = false: code=%s error=%s", res.Code, res.Error)
	}
	if !strings.Contains(res.Output, "no matches") {
		t.Errorf("output = %q, want a clear no-matches message", res.Output)
	}
}

func TestSearchRootMustBeRealDirectory(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "real/a.txt", []byte("hit\n"))
	mustWriteFile(t, dir, "file.txt", []byte("hit\n"))
	if err := os.Symlink("real", filepath.Join(dir, "linkdir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	res := executeArgs(t, r, "search", map[string]any{"pattern": "hit", "path": "linkdir"})
	if res.OK || res.Code != CodeInvalidArgs {
		t.Errorf("search(symlink root) = ok=%v code=%s, want invalid_args", res.OK, res.Code)
	}
	res = executeArgs(t, r, "search", map[string]any{"pattern": "hit", "path": "file.txt"})
	if res.OK || res.Code != CodeInvalidArgs {
		t.Errorf("search(file root) = ok=%v code=%s, want invalid_args", res.OK, res.Code)
	}
}

func TestSearchMaxResultsClamped(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "clamp.txt", []byte(strings.Repeat("hit\n", 250)))
	res := executeArgs(t, r, "search", map[string]any{"pattern": "hit", "max_results": 1000})
	if !res.OK {
		t.Fatalf("search(max_results over max) failed: %s", res.Error)
	}
	if got := strings.Count(res.Output, "clamp.txt:"); got != maxSearchResults {
		t.Errorf("matched lines = %d, want %d", got, maxSearchResults)
	}
	if !strings.Contains(res.Output, "[results capped at 200]") {
		t.Errorf("output missing capped marker for the clamped limit")
	}
}

func TestSearchMaxResultsCap(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "cap.txt", []byte("hit one\ntwo hit\nhit three\n"))
	res := executeArgs(t, r, "search", map[string]any{"pattern": "hit", "max_results": 1})
	if !res.OK {
		t.Fatalf("search(cap 1) failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "cap.txt:1: hit one") {
		t.Errorf("output missing first match: %q", res.Output)
	}
	if strings.Contains(res.Output, "cap.txt:3") {
		t.Errorf("output includes match past the cap: %q", res.Output)
	}
	if !strings.Contains(res.Output, "[results capped at 1]") {
		t.Errorf("output missing capped marker: %q", res.Output)
	}
	if res.Truncated {
		t.Errorf("Truncated = true for a result-count cap, want false")
	}
}

func TestSearchByteCap(t *testing.T) {
	r, dir := newTestRegistry(t)
	line := "hit" + strings.Repeat("a", 300)
	mustWriteFile(t, dir, "big.txt", []byte(strings.Repeat(line+"\n", 300)))
	res := executeArgs(t, r, "search", map[string]any{"pattern": "hit", "max_results": 200})
	if !res.OK {
		t.Fatalf("search(byte cap) failed: %s", res.Error)
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true")
	}
	if len(res.Output) > fileOutputCap {
		t.Errorf("output length = %d, want <= %d", len(res.Output), fileOutputCap)
	}
	if !strings.Contains(res.Output, "[truncated ") {
		t.Errorf("output missing byte truncation marker")
	}
	if !strings.Contains(res.Output, "[results capped at 200]") {
		t.Errorf("output missing results-capped marker in tail")
	}
}

func TestSearchLongLineClipped(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "long.txt", []byte("needle"+strings.Repeat("x", 2000)+"\n"))
	res := executeArgs(t, r, "search", map[string]any{"pattern": "needle"})
	if !res.OK {
		t.Fatalf("search(long line) failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "needle") {
		t.Errorf("output missing match: %q", res.Output)
	}
	if len(res.Output) > 600 {
		t.Errorf("long line was not clipped: output length %d", len(res.Output))
	}
	if strings.Contains(res.Output, strings.Repeat("x", 700)) {
		t.Errorf("output still contains the full long line")
	}
	if !utf8.ValidString(res.Output) {
		t.Errorf("output is not valid UTF-8")
	}
}

func TestSearchCRLF(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "crlf.txt", []byte("hello\r\nworld\r\n"))
	res := executeArgs(t, r, "search", map[string]any{"pattern": "^hello$"})
	if !res.OK {
		t.Fatalf("search(crlf) failed: %s", res.Error)
	}
	if want := "crlf.txt:1: hello\n"; res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
}

func TestSearchUnicodePathAndContent(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "héllo.txt", []byte("héllo wörld\n"))
	res := executeArgs(t, r, "search", map[string]any{"pattern": "wörld"})
	if !res.OK {
		t.Fatalf("search(unicode) failed: %s", res.Error)
	}
	want := "héllo.txt:1: héllo wörld\n"
	if res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
	if !utf8.ValidString(res.Output) {
		t.Errorf("output is not valid UTF-8")
	}
}

func TestSearchUnknownMember(t *testing.T) {
	r, _ := newTestRegistry(t)
	res := executeRaw(t, r, "search", `{"pattern":"x","surprise":1}`)
	if res.OK || res.Code != CodeInvalidArgs {
		t.Errorf("search(unknown member) = ok=%v code=%s, want invalid_args", res.OK, res.Code)
	}
}
