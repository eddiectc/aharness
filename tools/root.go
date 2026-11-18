package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// errSymlink marks a path that contains a symbolic link where the tool policy
// forbids one.
var errSymlink = errors.New("path contains a symlink")

// errNotRegular marks a path whose filesystem type is not accepted by the
// calling tool.
var errNotRegular = errors.New("path is not a regular file")

// errNotDir marks a path that must be a directory but is not one.
var errNotDir = errors.New("path is not a directory")

// Workspace is the canonical workspace root and the os.Root that enforces
// containment for all file tools.
type Workspace struct {
	dir    string
	root   *os.Root
	closed bool
}

// OpenWorkspace resolves dir to a canonical absolute path, requires an
// existing directory, and opens it as an os.Root.
func OpenWorkspace(dir string) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace %s: %w", dir, err)
	}
	// Resolve symlinks once so Dir and filepath.Rel agree on one canonical
	// spelling of the workspace root.
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace %s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat workspace %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace %s is not a directory", abs)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open workspace %s: %w", abs, err)
	}
	return &Workspace{dir: abs, root: root}, nil
}

// Dir returns the canonical absolute workspace directory.
func (w *Workspace) Dir() string { return w.dir }

// Close releases the workspace root. Closing an already closed workspace is a
// no-op.
func (w *Workspace) Close() error {
	if w.closed || w.root == nil {
		return nil
	}
	w.closed = true
	err := w.root.Close()
	if err != nil {
		return fmt.Errorf("close workspace %s: %w", w.dir, err)
	}
	return nil
}

// rel normalizes a tool path. Root performs the real containment check.
func (w *Workspace) rel(p string) (string, error) {
	if filepath.IsAbs(p) {
		var err error
		if p, err = filepath.Rel(w.dir, p); err != nil {
			return "", err
		}
	}
	p = filepath.Clean(p)
	if !filepath.IsLocal(p) {
		return "", ErrOutsideWorkspace
	}
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if part == ".git" {
			return "", ErrGitProtected
		}
	}
	return p, nil
}

// checkResolved verifies that rel resolves to a path inside the workspace,
// following symlinks for the check. The os.Root operations remain the
// authority for containment; this check turns an escaped symlink into the
// stable outside-workspace failure before a root operation reports an opaque
// platform error.
func (w *Workspace) checkResolved(rel string) error {
	abs := filepath.Join(w.dir, rel)
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return fmt.Errorf("resolve %s: %w", rel, err)
	}
	rr, err := filepath.Rel(w.dir, real)
	if err != nil || !filepath.IsLocal(rr) {
		return fmt.Errorf("%w: %s resolves outside the workspace", ErrOutsideWorkspace, rel)
	}
	return nil
}

// lstatComponents checks every component of rel with Lstat, rejecting
// symlinks and non-directory parents. A missing final component is reported as
// os.ErrNotExist; a missing parent is reported as an error instead.
func (w *Workspace) lstatComponents(rel string) (os.FileInfo, error) {
	parts := strings.Split(rel, string(filepath.Separator))
	cur := ""
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := w.root.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && i == len(parts)-1 {
				return nil, os.ErrNotExist
			}
			return nil, fmt.Errorf("lstat %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s", errSymlink, cur)
		}
		last := i == len(parts)-1
		if !last && !info.IsDir() {
			return nil, fmt.Errorf("%w: %s is not a directory", errNotDir, cur)
		}
		if last {
			return info, nil
		}
	}
	return nil, os.ErrNotExist
}

// resolveDir resolves a workspace-relative directory to an absolute host path
// suitable for exec.Cmd.Dir, requiring a real in-workspace directory.
func (w *Workspace) resolveDir(rel string) (string, error) {
	info, err := w.root.Stat(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if cerr := w.checkResolved(rel); cerr != nil {
			return "", cerr
		}
		return "", fmt.Errorf("stat %s: %w", rel, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s", errNotDir, rel)
	}
	if err := w.checkResolved(rel); err != nil {
		return "", err
	}
	return filepath.Join(w.dir, rel), nil
}

// Contains reports whether p is the workspace root or an existing path inside
// it. The check resolves symlinks with the same containment rule as the file
// tools: a lexically inside path whose symlink resolves outside is not
// contained. A missing path, a .git component, or a path outside the workspace
// reports false.
func (w *Workspace) Contains(p string) (bool, error) {
	if p == "" {
		return false, nil
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(w.dir, abs)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(w.dir, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return false, nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" {
			return false, nil
		}
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("resolve %s: %w", p, err)
	}
	resolved, err := filepath.Rel(w.dir, real)
	if err != nil || !filepath.IsLocal(resolved) {
		return false, nil
	}
	return true, nil
}

// LocalDisplayPath reports whether p is a workspace-relative path the display
// layer may read, and returns its normalized relative form. It applies the
// display read policy: local, no .git component, no symlink component, and an
// existing entry. A relative input is resolved against the workspace.
func (w *Workspace) LocalDisplayPath(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(w.dir, p)
		if err != nil {
			return "", false
		}
		p = rel
	}
	p = filepath.Clean(p)
	if !filepath.IsLocal(p) {
		return "", false
	}
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if part == ".git" {
			return "", false
		}
	}
	if _, err := w.lstatComponents(p); err != nil {
		return "", false
	}
	return p, true
}

// DisplayEntry is one sanitized directory entry exposed to a host file
// picker.
type DisplayEntry struct {
	Name string
	Dir  bool
	Rel  string
}

// ListDisplayDir lists one workspace-relative directory for a host. It never
// follows symlinks, never includes a .git component, reads no file contents,
// and caps the entry count.
func (w *Workspace) ListDisplayDir(rel string, limit int) ([]DisplayEntry, error) {
	rel = filepath.Clean(rel)
	if rel == "" {
		rel = "."
	}
	if !filepath.IsLocal(rel) {
		return nil, ErrOutsideWorkspace
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" {
			return nil, ErrGitProtected
		}
	}
	info, err := w.root.Lstat(rel)
	if err != nil {
		return nil, fmt.Errorf("lstat %s: %w", rel, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", errNotDir, rel)
	}
	f, err := w.root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", rel, err)
	}
	defer f.Close() //nolint:errcheck
	entries, err := f.ReadDir(limit)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read directory %s: %w", rel, err)
	}
	out := make([]DisplayEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		child := entry.Name()
		if rel != "." {
			child = filepath.Join(rel, entry.Name())
		}
		out = append(out, DisplayEntry{Name: entry.Name(), Dir: entry.IsDir(), Rel: child})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ReadDisplayFile reads a workspace-contained regular file for display. It
// rejects symlink components, .git components, non-regular files, and files
// over max bytes. It is read-only and never creates or edits a path.
func (w *Workspace) ReadDisplayFile(rel string, max int64) ([]byte, error) {
	rel = filepath.Clean(rel)
	if rel == "" || !filepath.IsLocal(rel) {
		return nil, ErrOutsideWorkspace
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" {
			return nil, ErrGitProtected
		}
	}
	info, err := w.lstatComponents(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", errNotRegular, rel)
	}
	if info.Size() > max {
		return nil, fmt.Errorf("%w: %s is %d bytes, limit %d", ErrTooLarge, rel, info.Size(), max)
	}
	f, err := w.root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", rel, err)
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, rel, max)
	}
	return data, nil
}
