package main

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	metaFile   = "meta.json"
	promptFile = "task.md"
	setupFile  = "setup.sh"
	verifyFile = "verify.sh"

	maxMetaBytes = 64 << 10
	maxTaskBytes = 1 << 20
)

// taskMeta is the parsed meta.json for one task.
type taskMeta struct {
	Cluster  string  `json:"cluster"`
	Language string  `json:"language"`
	Timeout  float64 `json:"timeout"`
	Notes    string  `json:"notes"`
}

// task is one manifest entry with resolved paths and timeout.
type task struct {
	ID      string
	Dir     string
	Meta    taskMeta
	Timeout time.Duration
}

// loadTasks reads and validates every task directory beneath dir. Entries are
// returned sorted by task id, which keeps run ordering deterministic.
func loadTasks(dir string) ([]task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read task suite %s: %w", dir, err)
	}
	var tasks []task
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if id == "" || !filepath.IsLocal(id) {
			return nil, fmt.Errorf("task %q: invalid task id", id)
		}
		absDir, err := filepath.Abs(filepath.Join(dir, id))
		if err != nil {
			return nil, fmt.Errorf("task %s: resolve path: %w", id, err)
		}
		taskDir, err := filepath.EvalSymlinks(absDir)
		if err != nil {
			return nil, fmt.Errorf("task %s: resolve path: %w", id, err)
		}
		meta, err := readMeta(taskDir)
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", id, err)
		}
		for _, name := range []string{promptFile, setupFile, verifyFile} {
			info, err := os.Stat(filepath.Join(taskDir, name))
			if err != nil {
				return nil, fmt.Errorf("task %s: %w", id, err)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("task %s: %s is not a regular file", id, name)
			}
		}
		if err := checkReadable(filepath.Join(taskDir, promptFile), maxTaskBytes); err != nil {
			return nil, fmt.Errorf("task %s: %w", id, err)
		}
		tasks = append(tasks, task{
			ID:      id,
			Dir:     taskDir,
			Meta:    meta,
			Timeout: metaTimeout(meta),
		})
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("task suite %s contains no tasks", dir)
	}
	return tasks, nil
}

func readMeta(taskDir string) (taskMeta, error) {
	path := filepath.Join(taskDir, metaFile)
	data, err := readLimited(path, maxMetaBytes)
	if err != nil {
		return taskMeta{}, err
	}
	var meta taskMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return taskMeta{}, fmt.Errorf("decode %s: %w", path, err)
	}
	meta.Cluster = strings.TrimSpace(meta.Cluster)
	meta.Language = strings.TrimSpace(meta.Language)
	if meta.Cluster == "" {
		return taskMeta{}, fmt.Errorf("%s: cluster is required", path)
	}
	if meta.Language == "" {
		return taskMeta{}, fmt.Errorf("%s: language is required", path)
	}
	if math.IsNaN(meta.Timeout) || math.IsInf(meta.Timeout, 0) || meta.Timeout < 0 {
		return taskMeta{}, fmt.Errorf("%s: timeout must be a non-negative finite number", path)
	}
	return meta, nil
}

func metaTimeout(meta taskMeta) time.Duration {
	seconds := meta.Timeout
	if seconds == 0 {
		seconds = defaultTaskTimeout
	}
	return time.Duration(seconds * float64(time.Second))
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Read-only close; a failure cannot affect the decoded payload.
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return data, nil
}

func checkReadable(path string, limit int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > limit {
		return fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	if info.Size() == 0 {
		return errors.New(path + " is empty")
	}
	return nil
}
