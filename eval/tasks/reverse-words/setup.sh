#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/words

go 1.21
TASK_EOF
cat > "$WORKSPACE/reverse.go" <<'TASK_EOF'
package words

import "strings"

// ReverseWords reverses the order of whitespace-separated words.
func ReverseWords(s string) string {
	parts := strings.Fields(s)
	for i := len(parts) - 1; i > 0; i-- {
		parts[i], parts[i-1] = parts[i-1], parts[i]
	}
	return strings.Join(parts, " ")
}
TASK_EOF
cat > "$WORKSPACE/reverse_test.go" <<'TASK_EOF'
package words

import "testing"

func TestReverseWords(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"one two three", "three two one"},
		{"  a b  ", "b a"},
		{"single", "single"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ReverseWords(tt.input); got != tt.want {
			t.Errorf("ReverseWords(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
TASK_EOF
