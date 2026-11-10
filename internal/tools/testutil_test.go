package tools

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
)

func newTestWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace(%q): %v", dir, err)
	}
	t.Cleanup(func() {
		if err := ws.Close(); err != nil {
			t.Errorf("Close workspace: %v", err)
		}
	})
	return ws, ws.Dir()
}

func newTestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	ws, dir := newTestWorkspace(t)
	return NewRegistry(ws, Options{}), dir
}

func executeArgs(t *testing.T, r *Registry, name string, args any) Result {
	t.Helper()
	raw, err := jsonv2.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s args: %v", name, err)
	}
	return r.Execute(t.Context(), name, raw)
}

func executeRaw(t *testing.T, r *Registry, name, raw string) Result {
	t.Helper()
	return r.Execute(t.Context(), name, json.RawMessage(raw))
}

func mustWriteFile(t *testing.T, dir, rel string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if parent := filepath.Dir(path); parent != dir {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			t.Fatalf("create parent for %s: %v", rel, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

func mustReadFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}
