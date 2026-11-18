package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenWorkspace(t *testing.T) {
	dir := t.TempDir()
	ws, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace(%q): %v", dir, err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	if got := ws.Dir(); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if !filepath.IsAbs(ws.Dir()) {
		t.Errorf("Dir() = %q, want an absolute path", ws.Dir())
	}
	if err := ws.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
	if err := ws.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}

func TestOpenWorkspaceRelative(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(filepath.Dir(dir))
	ws, err := OpenWorkspace(filepath.Base(dir))
	if err != nil {
		t.Fatalf("OpenWorkspace(relative): %v", err)
	}
	defer func() {
		if err := ws.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	}()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	if got := ws.Dir(); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

func TestOpenWorkspaceRejectsMissingAndFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenWorkspace(filepath.Join(dir, "missing")); err == nil {
		t.Errorf("OpenWorkspace(missing dir) = nil error, want error")
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := OpenWorkspace(file); err == nil {
		t.Errorf("OpenWorkspace(file) = nil error, want error")
	}
}

func TestWorkspaceRel(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	parent := filepath.Dir(dir)

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"relative", "a/b.txt", "a/b.txt", nil},
		{"cleans relative", "./a/../b.txt", "b.txt", nil},
		{"dot", ".", ".", nil},
		{"empty", "", ".", nil},
		{"parent traversal", "../x", "", ErrOutsideWorkspace},
		{"nested traversal", "a/../../x", "", ErrOutsideWorkspace},
		{"absolute inside", filepath.Join(dir, "sub", "f.txt"), "sub/f.txt", nil},
		{"absolute workspace", dir, ".", nil},
		{"absolute outside", filepath.Join(parent, "sibling"), "", ErrOutsideWorkspace},
		{"git", ".git/config", "", ErrGitProtected},
		{"nested git", "sub/.git/config", "", ErrGitProtected},
		{"cleaned git", "sub/../.git/x", "", ErrGitProtected},
		{"git-like name", "sub/.gitignore", "sub/.gitignore", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ws.rel(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("rel(%q) error = %v, want errors.Is %v", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("rel(%q) error = %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("rel(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestWorkspaceCheckResolved(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	mustWriteFile(t, dir, "real/file.txt", []byte("ok"))
	if err := os.Symlink(filepath.Join("real", "file.txt"), filepath.Join(dir, "inside-link")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	if err := ws.checkResolved("inside-link"); err != nil {
		t.Errorf("checkResolved(in-root symlink) = %v, want nil", err)
	}
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(dir, "escape-link")); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}
	if err := ws.checkResolved("escape-link"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Errorf("checkResolved(escaping symlink) = %v, want ErrOutsideWorkspace", err)
	}
	if err := ws.checkResolved("missing.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("checkResolved(missing) = %v, want os.ErrNotExist", err)
	}
}

func TestWorkspaceLstatComponents(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	mustWriteFile(t, dir, "file.txt", []byte("x"))
	mustWriteFile(t, dir, "real/inner.txt", []byte("y"))
	if err := os.Symlink("real", filepath.Join(dir, "linkdir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	if err := os.Symlink("file.txt", filepath.Join(dir, "linkfile")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}

	info, err := ws.lstatComponents("file.txt")
	if err != nil {
		t.Fatalf("lstatComponents(file): %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("lstatComponents(file) mode = %v, want regular", info.Mode())
	}
	if _, err := ws.lstatComponents("linkfile"); !errors.Is(err, errSymlink) {
		t.Errorf("lstatComponents(symlink file) = %v, want errSymlink", err)
	}
	if _, err := ws.lstatComponents("linkdir/inner.txt"); !errors.Is(err, errSymlink) {
		t.Errorf("lstatComponents(symlink parent) = %v, want errSymlink", err)
	}
	if _, err := ws.lstatComponents("missing.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lstatComponents(missing) = %v, want os.ErrNotExist", err)
	}
	if _, err := ws.lstatComponents("file.txt/child"); !errors.Is(err, errNotDir) {
		t.Errorf("lstatComponents(file child) = %v, want errNotDir", err)
	}
}

func TestWorkspaceResolveDir(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	got, err := ws.resolveDir("sub")
	if err != nil {
		t.Fatalf("resolveDir(sub): %v", err)
	}
	if want := filepath.Join(dir, "sub"); got != want {
		t.Errorf("resolveDir(sub) = %q, want %q", got, want)
	}
	if _, err := ws.resolveDir("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("resolveDir(missing) = %v, want os.ErrNotExist", err)
	}
	mustWriteFile(t, dir, "file.txt", []byte("x"))
	if _, err := ws.resolveDir("file.txt"); !errors.Is(err, errNotDir) {
		t.Errorf("resolveDir(file) = %v, want errNotDir", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}
	if _, err := ws.resolveDir("escape"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Errorf("resolveDir(escaping symlink) = %v, want ErrOutsideWorkspace", err)
	}
}

func TestWorkspaceListDisplayDirEmpty(t *testing.T) {
	ws, err := OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	entries, err := ws.ListDisplayDir(".", 100)
	if err != nil {
		t.Fatalf("ListDisplayDir on an empty workspace: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none", entries)
	}
}

func TestCloseThenUseFails(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	mustWriteFile(t, ws.Dir(), "f.txt", []byte("x"))
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r := NewRegistry(ws, Options{})
	res := executeArgs(t, r, "read_file", map[string]any{"path": "f.txt"})
	if res.OK {
		t.Errorf("read after Close OK = true, want failure")
	}
	if !strings.Contains(res.Error, "closed") && !strings.Contains(res.Error, "file already closed") {
		t.Logf("read after Close error: %q", res.Error)
	}
}
