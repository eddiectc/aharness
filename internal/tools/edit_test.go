package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileUnique(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "f.txt", []byte("hello world\n"))
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "f.txt",
		"old_string": "world",
		"new_string": "there",
	})
	if !res.OK {
		t.Fatalf("edit_file failed: code=%s error=%s", res.Code, res.Error)
	}
	if got, want := mustReadFile(t, dir, "f.txt"), "hello there\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if !strings.Contains(res.Output, "edited f.txt") || !strings.Contains(res.Output, "1 occurrence") {
		t.Errorf("summary = %q, want edited path and occurrence count", res.Output)
	}
}

func TestEditFileZeroMatch(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "f.txt", []byte("hello\n"))
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "f.txt",
		"old_string": "absent",
		"new_string": "x",
	})
	if res.OK || res.Code != CodeNotFound {
		t.Errorf("edit_file(zero match) = ok=%v code=%s, want not_found", res.OK, res.Code)
	}
	if got := mustReadFile(t, dir, "f.txt"); got != "hello\n" {
		t.Errorf("content changed to %q", got)
	}
}

func TestEditFileAmbiguous(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "f.txt", []byte("x x x\n"))
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "f.txt",
		"old_string": "x",
		"new_string": "y",
	})
	if res.OK || res.Code != CodeAmbiguous {
		t.Errorf("edit_file(ambiguous) = ok=%v code=%s, want ambiguous", res.OK, res.Code)
	}
	if got := mustReadFile(t, dir, "f.txt"); got != "x x x\n" {
		t.Errorf("content changed to %q", got)
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "f.txt", []byte("x x x\n"))
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":        "f.txt",
		"old_string":  "x",
		"new_string":  "y",
		"replace_all": true,
	})
	if !res.OK {
		t.Fatalf("edit_file(replace_all) failed: code=%s error=%s", res.Code, res.Error)
	}
	if got, want := mustReadFile(t, dir, "f.txt"), "y y y\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if !strings.Contains(res.Output, "3 occurrence") {
		t.Errorf("summary = %q, want 3 occurrences", res.Output)
	}
}

func TestEditFileCreate(t *testing.T) {
	r, dir := newTestRegistry(t)
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "new.txt",
		"old_string": "",
		"new_string": "fresh\n",
	})
	if !res.OK {
		t.Fatalf("edit_file(create) failed: code=%s error=%s", res.Code, res.Error)
	}
	if got, want := mustReadFile(t, dir, "new.txt"), "fresh\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatalf("stat new file: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Errorf("new file mode = %o, want %o", got, want)
	}
	if !strings.Contains(res.Output, "created new.txt") {
		t.Errorf("summary = %q, want created path", res.Output)
	}
}

func TestEditFileCreateRequiresAbsentAndEmptyOld(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "exists.txt", []byte("content\n"))
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "exists.txt",
		"old_string": "",
		"new_string": "x",
	})
	if res.OK || res.Code != CodeInvalidArgs {
		t.Errorf("edit_file(create over existing) = ok=%v code=%s, want invalid_args", res.OK, res.Code)
	}

	res = executeArgs(t, r, "edit_file", map[string]any{
		"path":       "absent.txt",
		"old_string": "old",
		"new_string": "new",
	})
	if res.OK || res.Code != CodeNotFound {
		t.Errorf("edit_file(missing with old_string) = ok=%v code=%s, want not_found", res.OK, res.Code)
	}

	res = executeArgs(t, r, "edit_file", map[string]any{
		"path":       "nodir/new.txt",
		"old_string": "",
		"new_string": "x",
	})
	if res.OK {
		t.Errorf("edit_file(new file in missing dir) OK = true, want failure")
	}
}

func TestEditFileOldEqualsNew(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "f.txt", []byte("same\n"))
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "f.txt",
		"old_string": "same",
		"new_string": "same",
	})
	if res.OK || res.Code != CodeInvalidArgs {
		t.Errorf("edit_file(old==new) = ok=%v code=%s, want invalid_args", res.OK, res.Code)
	}
	if got := mustReadFile(t, dir, "f.txt"); got != "same\n" {
		t.Errorf("content changed to %q", got)
	}
}

func TestEditFilePreservesCRLF(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "crlf.txt", []byte("a\r\nb\r\n"))
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "crlf.txt",
		"old_string": "a\r\n",
		"new_string": "x\r\n",
	})
	if !res.OK {
		t.Fatalf("edit_file(crlf) failed: code=%s error=%s", res.Code, res.Error)
	}
	if got, want := mustReadFile(t, dir, "crlf.txt"), "x\r\nb\r\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestEditFilePreservesMode(t *testing.T) {
	r, dir := newTestRegistry(t)
	path := mustWriteFile(t, dir, "mode.txt", []byte("before\n"))
	if err := os.Chmod(path, 0o751); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "mode.txt",
		"old_string": "before",
		"new_string": "after",
	})
	if !res.OK {
		t.Fatalf("edit_file(mode) failed: code=%s error=%s", res.Code, res.Error)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o751); got != want {
		t.Errorf("mode = %o, want %o", got, want)
	}
}

func TestEditFileRejectsSymlinks(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "target.txt", []byte("target\n"))
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "link.txt",
		"old_string": "target",
		"new_string": "changed",
	})
	if res.OK || res.Code != CodeOutsideWorkspace {
		t.Errorf("edit_file(symlink file) = ok=%v code=%s, want outside_workspace", res.OK, res.Code)
	}
	if got := mustReadFile(t, dir, "target.txt"); got != "target\n" {
		t.Errorf("symlink target changed to %q", got)
	}

	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "linkdir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	res = executeArgs(t, r, "edit_file", map[string]any{
		"path":       "linkdir/new.txt",
		"old_string": "",
		"new_string": "x",
	})
	if res.OK || res.Code != CodeOutsideWorkspace {
		t.Errorf("edit_file(symlink parent) = ok=%v code=%s, want outside_workspace", res.OK, res.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "real", "new.txt")); !os.IsNotExist(err) {
		t.Errorf("new file appeared through symlinked parent: stat err = %v", err)
	}
}

func TestEditFileContainment(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, ".git/config", []byte("secret\n"))
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	tests := []struct {
		name string
		args map[string]any
		code Code
	}{
		{
			name: "git",
			args: map[string]any{"path": ".git/config", "old_string": "secret", "new_string": "x"},
			code: CodeGitProtected,
		},
		{
			name: "nested git",
			args: map[string]any{"path": "sub/.git/config", "old_string": "secret", "new_string": "x"},
			code: CodeGitProtected,
		},
		{
			name: "outside relative",
			args: map[string]any{"path": "../outside.txt", "old_string": "outside", "new_string": "x"},
			code: CodeOutsideWorkspace,
		},
		{
			name: "outside absolute",
			args: map[string]any{"path": outsideFile, "old_string": "outside", "new_string": "x"},
			code: CodeOutsideWorkspace,
		},
		{
			name: "directory",
			args: map[string]any{"path": ".", "old_string": "a", "new_string": "b"},
			code: CodeInvalidArgs,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := executeArgs(t, r, "edit_file", tc.args)
			if res.OK || res.Code != tc.code {
				t.Errorf("edit_file(%v) = ok=%v code=%s, want %s (error %q)", tc.args, res.OK, res.Code, tc.code, res.Error)
			}
		})
	}
	if got := mustReadFile(t, dir, ".git/config"); got != "secret\n" {
		t.Errorf(".git content = %q, want unchanged", got)
	}
}

func TestEditFileTooLargeResult(t *testing.T) {
	r, dir := newTestRegistry(t)
	newContent := strings.Repeat("a", maxEditFileBytes+1)
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "big.txt",
		"old_string": "",
		"new_string": newContent,
	})
	if res.OK || res.Code != CodeTooLarge {
		t.Errorf("edit_file(create over limit) = ok=%v code=%s, want too_large", res.OK, res.Code)
	}

	original := strings.Repeat("b", 1500*1024)
	mustWriteFile(t, dir, "grow.txt", []byte(original))
	res = executeArgs(t, r, "edit_file", map[string]any{
		"path":        "grow.txt",
		"old_string":  "b",
		"new_string":  "cc",
		"replace_all": true,
	})
	if res.OK || res.Code != CodeTooLarge {
		t.Errorf("edit_file(grow over limit) = ok=%v code=%s, want too_large", res.OK, res.Code)
	}
	if got := mustReadFile(t, dir, "grow.txt"); got != original {
		t.Errorf("large edit changed the file despite rejection")
	}
}

func TestEditFileNoTempLeftBehind(t *testing.T) {
	r, dir := newTestRegistry(t)
	mustWriteFile(t, dir, "f.txt", []byte("before\n"))
	res := executeArgs(t, r, "edit_file", map[string]any{
		"path":       "f.txt",
		"old_string": "before",
		"new_string": "after",
	})
	if !res.OK {
		t.Fatalf("edit_file failed: %s", res.Error)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".aharness-edit-") {
			t.Errorf("temp file left behind: %s", entry.Name())
		}
	}
}

func TestEditFileUnknownMember(t *testing.T) {
	r, _ := newTestRegistry(t)
	res := executeRaw(t, r, "edit_file", `{"path":"x","old_string":"a","new_string":"b","surprise":1}`)
	if res.OK || res.Code != CodeInvalidArgs {
		t.Errorf("edit_file(unknown member) = ok=%v code=%s, want invalid_args", res.OK, res.Code)
	}
}
