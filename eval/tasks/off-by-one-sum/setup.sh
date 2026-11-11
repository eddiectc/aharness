#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/sum

go 1.21
TASK_EOF
cat > "$WORKSPACE/sum.go" <<'TASK_EOF'
package sum

// Sum returns the total of values.
func Sum(values []int) int {
	total := 0
	for i := 0; i < len(values)-1; i++ {
		total += values[i]
	}
	return total
}
TASK_EOF
cat > "$WORKSPACE/sum_test.go" <<'TASK_EOF'
package sum

import "testing"

func TestSum(t *testing.T) {
	tests := []struct {
		name  string
		input []int
		want  int
	}{
		{"four values", []int{1, 2, 3, 4}, 10},
		{"empty", nil, 0},
		{"single", []int{7}, 7},
	}
	for _, tt := range tests {
		if got := Sum(tt.input); got != tt.want {
			t.Errorf("%s: Sum(%v) = %d, want %d", tt.name, tt.input, got, tt.want)
		}
	}
}
TASK_EOF
