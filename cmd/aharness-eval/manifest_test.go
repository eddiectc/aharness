package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadTasksValid(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTaskTree(t, root, "b-task", "do b\n", "#!/bin/sh\nexit 0\n", "#!/bin/sh\nexit 0\n",
		taskMeta{Cluster: "text", Language: "sh", Timeout: 5, Notes: "b"})
	writeTaskTree(t, root, "a-task", "do a\n", "#!/bin/sh\nexit 0\n", "#!/bin/sh\nexit 0\n",
		taskMeta{Cluster: "go", Language: "go"})
	writeFile(t, filepath.Join(root, "README.md"), "not a task", 0o644)

	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatalf("loadTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(tasks))
	}
	if tasks[0].ID != "a-task" || tasks[1].ID != "b-task" {
		t.Errorf("task order = %s, %s; want a-task, b-task", tasks[0].ID, tasks[1].ID)
	}
	if tasks[0].Timeout != time.Duration(defaultTaskTimeout)*time.Second {
		t.Errorf("default timeout = %s, want %ds", tasks[0].Timeout, defaultTaskTimeout)
	}
	if tasks[1].Timeout != 5*time.Second {
		t.Errorf("timeout = %s, want 5s", tasks[1].Timeout)
	}
	if tasks[0].Meta.Cluster != "go" || tasks[0].Meta.Language != "go" {
		t.Errorf("meta = %+v", tasks[0].Meta)
	}
	if !filepath.IsAbs(tasks[0].Dir) {
		t.Errorf("task dir %q is not absolute", tasks[0].Dir)
	}
}

func TestLoadTasksInvalid(t *testing.T) {
	t.Parallel()
	goodMeta := taskMeta{Cluster: "text", Language: "sh", Timeout: 5}
	tests := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{
			name: "missing meta",
			mutate: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, metaFile)); err != nil {
					t.Fatalf("remove meta: %v", err)
				}
			},
		},
		{
			name: "bad meta json",
			mutate: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, metaFile), "{", 0o644)
			},
		},
		{
			name: "empty cluster",
			mutate: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, metaFile), `{"cluster":"","language":"sh"}`, 0o644)
			},
		},
		{
			name: "empty language",
			mutate: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, metaFile), `{"cluster":"text","language":""}`, 0o644)
			},
		},
		{
			name: "negative timeout",
			mutate: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, metaFile), `{"cluster":"text","language":"sh","timeout":-1}`, 0o644)
			},
		},
		{
			name: "missing task prompt",
			mutate: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, promptFile)); err != nil {
					t.Fatalf("remove prompt: %v", err)
				}
			},
		},
		{
			name: "missing setup",
			mutate: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, setupFile)); err != nil {
					t.Fatalf("remove setup: %v", err)
				}
			},
		},
		{
			name: "missing verify",
			mutate: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, verifyFile)); err != nil {
					t.Fatalf("remove verify: %v", err)
				}
			},
		},
		{
			name: "empty prompt",
			mutate: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, promptFile), "", 0o644)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			dir := writeTaskTree(t, root, "task", "do it\n", "#!/bin/sh\nexit 0\n", "#!/bin/sh\nexit 0\n", goodMeta)
			test.mutate(t, dir)
			if _, err := loadTasks(root); err == nil {
				t.Fatalf("loadTasks(%s) succeeded, want error", test.name)
			}
		})
	}
}

func TestLoadTasksEmptySuite(t *testing.T) {
	t.Parallel()
	if _, err := loadTasks(t.TempDir()); err == nil {
		t.Fatal("loadTasks(empty) succeeded, want error")
	}
}

func TestLoadTasksMissingDir(t *testing.T) {
	t.Parallel()
	if _, err := loadTasks(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("loadTasks(missing) succeeded, want error")
	}
}
