package main

import (
	"bufio"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aharness/internal/session"
)

const testSessionID = "0199d0e0-0000-7000-8000-000000000001"

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeTaskTree creates a task directory with a valid manifest.
func writeTaskTree(t *testing.T, root, id, prompt, setup, verify string, meta taskMeta) string {
	t.Helper()
	dir := filepath.Join(root, id)
	writeFile(t, filepath.Join(dir, promptFile), prompt, 0o644)
	writeFile(t, filepath.Join(dir, setupFile), setup, 0o755)
	writeFile(t, filepath.Join(dir, verifyFile), verify, 0o755)
	metaJSON, err := jsonv2.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta for %s: %v", id, err)
	}
	writeFile(t, filepath.Join(dir, metaFile), string(metaJSON), 0o644)
	return dir
}

// testEvent builds a session event with a deterministic timestamp.
func testEvent(t *testing.T, typ session.EventType, turn int, ts time.Time, payload any) session.Event {
	t.Helper()
	ev, err := session.NewEvent(typ, turn, "", payload)
	if err != nil {
		t.Fatalf("new event %s: %v", typ, err)
	}
	ev.TS = ts
	return ev
}

// writeSessionFile writes a valid session JSONL with sequential events.
func writeSessionFile(t *testing.T, path string, events []session.Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create session file: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close session file: %v", err)
		}
	}()
	encoder := jsontext.NewEncoder(file)
	for i := range events {
		events[i].V = session.Version
		events[i].Seq = uint64(i + 1)
		events[i].Session = testSessionID
		if err := jsonv2.MarshalEncode(encoder, events[i]); err != nil {
			t.Fatalf("encode event %d: %v", i+1, err)
		}
	}
}

func mustPriceTable(t *testing.T, content string) *priceTable {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prices.json")
	writeFile(t, path, content, 0o644)
	table, err := loadPriceFile(path)
	if err != nil {
		t.Fatalf("load price file: %v", err)
	}
	return table
}

func readResultsFile(t *testing.T, path string) []runRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open results: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close results: %v", err)
		}
	}()
	var records []runRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record runRecord
		if err := jsonv2.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode results line: %v", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan results: %v", err)
	}
	return records
}

func readSummaryFile(t *testing.T, path string) summaryDoc {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var doc summaryDoc
	if err := jsonv2.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	return doc
}
