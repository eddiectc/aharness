package session

import (
	"bytes"
	"testing"
)

func FuzzReadEvents(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte(`{"v":2,"seq":1,"ts":"1970-01-01T00:00:00Z","session":"` + testSession + `","type":"session_start","data":{}}` + "\n"))
	f.Add([]byte(`{"v":1,"seq":1,"ts":"1970-01-01T00:00:00Z","session":"` + testSession + `","type":"session_start","data":{}}` + "\n"))
	f.Add([]byte("{\"v\":2,\"seq\":1"))
	f.Fuzz(func(t *testing.T, data []byte) {
		stats, err := readEvents(bytes.NewReader(data), "fuzz")
		if err != nil {
			return
		}
		if stats.torn && stats.goodBytes < 0 {
			t.Fatalf("negative good offset: %d", stats.goodBytes)
		}
		if stats.goodBytes > int64(len(data)) {
			t.Fatalf("good offset %d exceeds input %d", stats.goodBytes, len(data))
		}
	})
}
