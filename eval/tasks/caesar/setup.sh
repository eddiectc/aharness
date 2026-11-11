#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/caesar

go 1.21
TASK_EOF
cat > "$WORKSPACE/caesar.go" <<'TASK_EOF'
package caesar

// Shift rotates ASCII letters by n positions, wrapping within each case.
func Shift(text string, n int) string {
	out := []byte(text)
	for i, c := range out {
		switch {
		case c >= 'a' && c <= 'z':
			out[i] = 'a' + (c-'a'+byte(n))%26
		case c >= 'A' && c <= 'Z':
			out[i] = 'A' + (c-'A'+byte(n))%26
		}
	}
	return string(out)
}
TASK_EOF
cat > "$WORKSPACE/caesar_test.go" <<'TASK_EOF'
package caesar

import "testing"

func TestShift(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"abc", 1, "bcd"},
		{"abc", -1, "zab"},
		{"xyz", 2, "zab"},
		{"Az", 1, "Ba"},
		{"a b!", 0, "a b!"},
	}
	for _, tt := range tests {
		if got := Shift(tt.input, tt.n); got != tt.want {
			t.Errorf("Shift(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
		}
	}
}
TASK_EOF
