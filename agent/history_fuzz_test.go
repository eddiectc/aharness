package agent

import (
	"testing"

	"github.com/eddiectc/aharness/session"
)

func FuzzHistory(f *testing.F) {
	types := []session.EventType{
		session.TypeSessionStart, session.TypeRunStart, session.TypeRunEnd,
		session.TypeUserMessage, session.TypeModelResponse, session.TypeToolResult,
		session.TypeInterrupt, session.TypeError, session.TypeLimit,
		session.TypeCompaction, session.TypeSessionRepair, session.TypeModelRetry,
		session.TypeFinal, session.TypeModelRequest,
	}
	f.Add([]byte(`{"text":"hi","source":"-task"}`), 3)
	f.Add([]byte(`not json`), 4)
	f.Add([]byte(`null`), 5)
	f.Fuzz(func(t *testing.T, data []byte, kind int) {
		if kind < 0 {
			kind = -kind
		}
		ev := session.Event{
			V: session.Version, Seq: 1, Type: types[kind%len(types)],
			Data: data,
		}
		items, err := History([]session.Event{ev})
		if err != nil {
			return
		}
		if len(items) > maxHistoryItems+1 {
			t.Fatalf("items = %d", len(items))
		}
		for _, item := range items {
			if len(item.Text) > maxHistoryItemBytes+128 {
				t.Fatalf("text = %d bytes", len(item.Text))
			}
		}
	})
}
