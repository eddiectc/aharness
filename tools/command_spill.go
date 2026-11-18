//go:build unix

package tools

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// spillDirName is the per-run directory, under Options.WorkDir, that holds
// oversized command output.
const spillDirName = "tool-output"

// spillWriter feeds a command stream to its inline capped buffer and, when
// enabled, to the spill capture. Spill failures never fail the command.
type spillWriter struct {
	inline  io.Writer
	capture *spillCapture
	stream  int
}

func (w spillWriter) Write(p []byte) (int, error) {
	n, err := w.inline.Write(p)
	if n > 0 {
		w.capture.write(w.stream, p[:n])
	}
	return n, err
}

// spillCapture records the full combined stdout/stderr bytes until the inline
// cap is exceeded, then streams them to a harness-owned file. Memory stays
// bounded: the buffered prefix is at most the inline cap plus one pipe write
// before the file is opened.
type spillCapture struct {
	mu        sync.Mutex
	workDir   string
	limit     int
	total     int64
	buffered  [2][]byte
	attempted bool
	file      *os.File
	path      string
	err       error
}

func newSpillCapture(workDir string, limit int) *spillCapture {
	if workDir == "" || limit <= 0 {
		return nil
	}
	return &spillCapture{workDir: workDir, limit: limit}
}

func (c *spillCapture) write(stream int, p []byte) {
	if c == nil || len(p) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(p))
	if c.attempted {
		if c.file != nil && c.err == nil {
			if _, err := c.file.Write(p); err != nil {
				c.err = err
			}
		}
		return
	}
	if stream < 0 || stream >= len(c.buffered) {
		return
	}
	c.buffered[stream] = append(c.buffered[stream], p...)
	if c.total > int64(c.limit) {
		c.startLocked()
	}
}

// startLocked opens the spill file and flushes the buffered stream prefixes.
// The file layout is the raw concatenation of stdout then stderr bytes seen so
// far, followed by later writes in arrival order; no labels are added so the
// model can page it with ordinary text tools.
func (c *spillCapture) startLocked() {
	if c.attempted {
		return
	}
	c.attempted = true
	path, f, err := createSpillFile(c.workDir)
	if err != nil {
		c.err = err
		return
	}
	c.file = f
	c.path = path
	for _, buf := range c.buffered {
		if len(buf) == 0 {
			continue
		}
		if _, err := f.Write(buf); err != nil {
			c.err = err
			break
		}
	}
	c.buffered = [2][]byte{}
}

// finish closes the spill file and returns its absolute path. A capture that
// never crossed the cap is spilled at finish time only when the caller reports
// that the rendered inline output was truncated.
func (c *spillCapture) finish() (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.attempted {
		c.startLocked()
	}
	if c.file == nil || c.err != nil {
		if c.file != nil {
			_ = c.file.Close()
			_ = os.Remove(c.path)
			c.file = nil
		}
		return "", false
	}
	if err := c.file.Close(); err != nil {
		_ = os.Remove(c.path)
		c.file = nil
		c.err = err
		return "", false
	}
	c.file = nil
	return c.path, true
}

type spillDetails struct {
	Spilled bool   `json:"spilled"`
	Path    string `json:"path"`
}

// finalizeCommandOutput appends the spill path when inline output was
// truncated, keeping the result at or under the inline cap.
func finalizeCommandOutput(out string, truncated bool, capture *spillCapture) (string, bool, json.RawMessage) {
	if !truncated {
		return out, false, nil
	}
	path, ok := capture.finish()
	if !ok {
		return out, true, nil
	}
	suffix := "\nfull output: " + path + "\n"
	if budget := commandOutputCap - len(suffix); budget < 1 || len(out) > budget {
		if budget < 1 {
			budget = 1
		}
		out, _ = capOutput(out, budget)
	}
	out += suffix
	raw, err := jsonv2.Marshal(spillDetails{Spilled: true, Path: path})
	if err != nil {
		return out, true, nil
	}
	return out, true, json.RawMessage(raw)
}

// createSpillFile creates a unique file under <workDir>/tool-output with mode
// 0600. The directory is 0700 and must not be a symlink. O_EXCL prevents
// clobbering and O_NOFOLLOW refuses to open a symlink as the final component.
func createSpillFile(workDir string) (string, *os.File, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve work dir: %w", err)
	}
	dir := filepath.Join(abs, spillDirName)
	if info, err := os.Lstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", nil, fmt.Errorf("spill directory %s is not a real directory", dir)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("stat spill directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create spill directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("secure spill directory: %w", err)
	}
	for range 16 {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", nil, fmt.Errorf("random spill name: %w", err)
		}
		path := filepath.Join(dir, "cmd-"+hex.EncodeToString(raw[:])+".log")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err == nil {
			return path, f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, fmt.Errorf("create spill file: %w", err)
		}
	}
	return "", nil, errors.New("create spill file: too many name collisions")
}
