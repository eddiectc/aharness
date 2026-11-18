package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	jsonv2 "encoding/json/v2"

	"github.com/eddiectc/aharness/agent"
	"github.com/eddiectc/aharness/session"
)

// Snapshot is the durable session view a host needs for a resume screen. It
// never takes the exclusive session lock.
type Snapshot struct {
	ID           string
	State        agent.State
	FirstMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Inspect reads one session without taking its exclusive lock. A torn final
// line is ignored. The session ID is canonicalized before a path is built.
func Inspect(ctx context.Context, stateDir, sessionID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	id, err := session.CanonicalID(sessionID)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: %w", ErrInvalidSessionID, err)
	}
	path := session.SessionPath(stateDir, id)
	events, err := session.ReadSnapshot(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{}, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
		}
		return Snapshot{}, err
	}
	return snapshotFromEvents(id, events)
}

// List returns every canonical session under stateDir, newest first. It reads
// each log as a snapshot and tolerates a live writer; unreadable or corrupt
// logs are skipped rather than failing the whole listing.
func List(ctx context.Context, stateDir string) ([]Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(stateDir, "sessions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions in %s: %w", dir, err)
	}
	out := make([]Snapshot, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, ok := strings.CutSuffix(entry.Name(), ".jsonl")
		if !ok {
			continue
		}
		id, err := session.CanonicalID(name)
		if err != nil || id != name {
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		events, err := session.ReadSnapshot(filepath.Join(dir, entry.Name()))
		if err != nil || len(events) == 0 || events[0].Type != session.TypeSessionStart {
			continue
		}
		snapshot, err := snapshotFromEvents(id, events)
		if err != nil {
			continue
		}
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func snapshotFromEvents(id string, events []session.Event) (Snapshot, error) {
	return SnapshotFromEvents(id, events)
}

// SnapshotFromEvents reconstructs the durable session view from an already
// replayed event list.
func SnapshotFromEvents(id string, events []session.Event) (Snapshot, error) {
	state, err := agent.Inspect(events)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{ID: id, State: state}
	if len(events) > 0 {
		snapshot.CreatedAt = events[0].TS
		snapshot.UpdatedAt = events[len(events)-1].TS
		if snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
			snapshot.UpdatedAt = snapshot.CreatedAt
		}
	}
	for _, ev := range events {
		if ev.Type != session.TypeUserMessage {
			continue
		}
		var data struct {
			Text string `json:"text"`
		}
		if err := jsonv2.Unmarshal(ev.Data, &data); err != nil {
			continue
		}
		snapshot.FirstMessage = data.Text
		break
	}
	return snapshot, nil
}
