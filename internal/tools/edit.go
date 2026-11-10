package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// editFileArgs is the typed argument shape for edit_file. It must agree with
// the edit_file schema in schemas.go.
type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

const maxEditFileBytes = 2 << 20

func (r *Registry) executeEdit(ctx context.Context, args json.RawMessage) Result {
	if err := ctx.Err(); err != nil {
		return errorResult(err)
	}
	var a editFileArgs
	if err := decodeArgs("edit_file", args, &a); err != nil {
		return Failure(CodeInvalidArgs, err.Error())
	}
	if a.Path == "" {
		return invalidArgs("edit_file", "path is required")
	}
	if a.OldString == a.NewString {
		return invalidArgs("edit_file", "old_string and new_string are identical")
	}
	rel, err := r.workspaceRel(a.Path)
	if err != nil {
		return errorResult(err)
	}
	info, err := r.ws.lstatComponents(rel)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errorResult(err)
	}

	var (
		content []byte
		mode    os.FileMode
	)
	if exists {
		if !info.Mode().IsRegular() {
			return errorResult(fmt.Errorf("%w: %s", errNotRegular, a.Path))
		}
		if a.OldString == "" {
			return invalidArgs("edit_file", "old_string is empty but %s already exists", a.Path)
		}
		data, err := r.readEditFile(rel, a.Path, info)
		if err != nil {
			return errorResult(err)
		}
		content = data
		mode = info.Mode().Perm() | (info.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
	} else {
		if a.OldString != "" {
			return errorResult(fmt.Errorf("%w: %s", ErrEditNotFound, a.Path))
		}
		content = nil
		mode = 0o644
	}

	matches := bytes.Count(content, []byte(a.OldString))
	if exists {
		switch {
		case matches == 0:
			return errorResult(fmt.Errorf("%w: %s", ErrEditNotFound, a.Path))
		case matches > 1 && !a.ReplaceAll:
			return errorResult(fmt.Errorf("%w: %s has %d matches", ErrEditAmbiguous, a.Path, matches))
		}
	}

	var replaced []byte
	replacements := 0
	if a.OldString != "" {
		if a.ReplaceAll {
			replacements = matches
		} else {
			replacements = 1
		}
		delta := int64(len(a.NewString)) - int64(len(a.OldString))
		resulting := int64(len(content)) + int64(replacements)*delta
		if resulting > maxEditFileBytes {
			return errorResult(fmt.Errorf("%w: %s would be %d bytes (limit %d)", ErrTooLarge, a.Path, resulting, maxEditFileBytes))
		}
		if a.ReplaceAll {
			replaced = bytes.ReplaceAll(content, []byte(a.OldString), []byte(a.NewString))
		} else {
			replaced = bytes.Replace(content, []byte(a.OldString), []byte(a.NewString), 1)
		}
	} else {
		if len(a.NewString) > maxEditFileBytes {
			return errorResult(fmt.Errorf("%w: %s would be %d bytes (limit %d)", ErrTooLarge, a.Path, len(a.NewString), maxEditFileBytes))
		}
		replaced = []byte(a.NewString)
	}

	if err := r.ws.writeAtomic(rel, replaced, mode); err != nil {
		return errorResult(err)
	}
	if !exists {
		return Success(fmt.Sprintf("created %s (%d bytes)", a.Path, len(replaced)))
	}
	return Success(fmt.Sprintf("edited %s: replaced %d occurrence(s), %d bytes -> %d bytes",
		a.Path, replacements, len(content), len(replaced)))
}

// readEditFile reads the existing target with the same size cap as read_file.
func (r *Registry) readEditFile(rel, display string, info os.FileInfo) ([]byte, error) {
	if info.Size() > maxEditFileBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrTooLarge, display, info.Size(), maxEditFileBytes)
	}
	f, err := r.ws.root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", display, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxEditFileBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read %s: %w", display, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close %s: %w", display, closeErr)
	}
	if len(data) > maxEditFileBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, display, maxEditFileBytes)
	}
	return data, nil
}

// writeAtomic replaces rel with data using an O_EXCL temp file in the same
// directory, an fchmod to mode, a sync, and a root-scoped rename. The temp file
// is removed on every failure path.
func (w *Workspace) writeAtomic(rel string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(rel)
	var (
		f       *os.File
		tempRel string
	)
	for range 10 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return fmt.Errorf("generate temp name for %s: %w", rel, err)
		}
		tempRel = filepath.Join(dir, ".aharness-edit-"+hex.EncodeToString(suffix[:]))
		var err error
		f, err = w.root.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return fmt.Errorf("create temp file for %s: %w", rel, err)
	}
	if f == nil {
		return fmt.Errorf("create temp file for %s: repeated name collisions", rel)
	}

	removeTemp := func() {
		// Best effort: the original error is the actionable one.
		_ = w.root.Remove(tempRel)
	}
	discardTemp := func() {
		// Best-effort close of a temp file that will not be renamed.
		_ = f.Close()
		removeTemp()
	}
	if _, err := f.Write(data); err != nil {
		discardTemp()
		return fmt.Errorf("write temp file for %s: %w", rel, err)
	}
	if err := f.Chmod(mode); err != nil {
		discardTemp()
		return fmt.Errorf("chmod temp file for %s: %w", rel, err)
	}
	if err := f.Sync(); err != nil {
		discardTemp()
		return fmt.Errorf("sync temp file for %s: %w", rel, err)
	}
	if err := f.Close(); err != nil {
		removeTemp()
		return fmt.Errorf("close temp file for %s: %w", rel, err)
	}
	if err := w.root.Rename(tempRel, rel); err != nil {
		removeTemp()
		return fmt.Errorf("rename temp file to %s: %w", rel, err)
	}
	return nil
}
