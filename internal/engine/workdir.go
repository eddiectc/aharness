package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// resetWorkDirs removes any stale per-session work tree and recreates the
// harness-owned TMPDIR. A failure to clear it is a startup failure, not a
// silent reuse.
func resetWorkDirs(workDir, tmpDir string) error {
	if err := removeTree(workDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(workDir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(workDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(workDir, 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(tmpDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		return err
	}
	return nil
}

// removeTree removes dir and its contents, restoring owner access once if a
// child command left a read-only directory in the tree.
func removeTree(dir string) error {
	if dir == "" {
		return nil
	}
	err := os.RemoveAll(dir)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if chmodErr := restoreOwnerAccess(dir); chmodErr != nil {
		return errors.Join(err, chmodErr)
	}
	return os.RemoveAll(dir)
}

func restoreOwnerAccess(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	if !info.IsDir() {
		return nil
	}
	if err := os.Chmod(dir, info.Mode().Perm()|0o700); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := restoreOwnerAccess(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
