#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/fizzbuzz

go 1.21
TASK_EOF
cat > "$WORKSPACE/fizzbuzz.go" <<'TASK_EOF'
package fizzbuzz

import "strconv"

// FizzBuzz returns the classic label for n.
func FizzBuzz(n int) string {
	switch {
	case n%3 == 0:
		return "Fizz"
	case n%5 == 0:
		return "Buzz"
	case n%15 == 0:
		return "FizzBuzz"
	}
	return strconv.Itoa(n)
}
TASK_EOF
cat > "$WORKSPACE/fizzbuzz_test.go" <<'TASK_EOF'
package fizzbuzz

import "testing"

func TestFizzBuzz(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{3, "Fizz"},
		{5, "Buzz"},
		{15, "FizzBuzz"},
		{30, "FizzBuzz"},
		{7, "7"},
	}
	for _, tt := range tests {
		if got := FizzBuzz(tt.n); got != tt.want {
			t.Errorf("FizzBuzz(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
TASK_EOF
