package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// makeReadOnlyTree builds a directory tree whose directories lack owner write
// permission, matching an extracted archive or module cache left in TMPDIR.
func makeReadOnlyTree(t *testing.T, root string) {
	t.Helper()
	deep := filepath.Join(root, "go", "pkg", "mod", "golang.org", "x", "term@v0.41.0")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(deep, "CONTRIBUTING.md")
	if err := os.WriteFile(file, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o444); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(root, "go", "pkg", "mod", "golang.org", "x", "term@v0.41.0"),
		filepath.Join(root, "go", "pkg", "mod", "golang.org", "x"),
	} {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	// t.TempDir cleanup cannot remove the tree without owner write permission.
	t.Cleanup(func() { _ = restoreOwnerAccess(root) })
}

func TestRemoveTreeReadOnlyDirs(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "work", "session")
	makeReadOnlyTree(t, root)
	if err := removeTree(root); err != nil {
		t.Fatalf("removeTree() error = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("stat after removeTree() = %v, want not exist", err)
	}
}

func TestRemoveTreeMissing(t *testing.T) {
	t.Parallel()
	if err := removeTree(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("removeTree(absent) error = %v", err)
	}
}

func TestRemoveWorkDirReportsLeftover(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	makeReadOnlyTree(t, filepath.Join(root, "work"))
	var stderr bytes.Buffer
	removeWorkDir(filepath.Join(root, "work"), &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty after successful removal", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "work")); !os.IsNotExist(err) {
		t.Fatalf("work tree remains: %v", err)
	}
}

func TestRestoreOwnerAccessSkipsSymlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	inside := filepath.Join(root, "tree")
	for _, dir := range []string{outside, inside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(outside, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outside, 0o700) })
	link := filepath.Join(inside, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := restoreOwnerAccess(inside); err != nil {
		t.Fatalf("restoreOwnerAccess() error = %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), fs.FileMode(0o500); got != want {
		t.Errorf("symlink target mode = %o, want unchanged %o", got, want)
	}
}
