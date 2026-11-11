#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/count

go 1.21
TASK_EOF
cat > "$WORKSPACE/count.go" <<'TASK_EOF'
package count

import "strings"

// Words returns the number of whitespace-separated words in text.
func Words(text string) int {
	return len(strings.Split(text, " "))
}
TASK_EOF
cat > "$WORKSPACE/count_test.go" <<'TASK_EOF'
package count

import "testing"

func TestWords(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{"one two three", 3},
		{"  a  b ", 2},
		{"", 0},
		{" \t\n ", 0},
	}
	for _, tt := range tests {
		if got := Words(tt.text); got != tt.want {
			t.Errorf("Words(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}
TASK_EOF
