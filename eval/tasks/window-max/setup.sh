#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/window

go 1.21
TASK_EOF
cat > "$WORKSPACE/window.go" <<'TASK_EOF'
package window

// MaxWindow returns the maximum of each contiguous window of the given size.
func MaxWindow(values []int, size int) []int {
	var result []int
	for i := 0; i+size <= len(values); i++ {
		result = append(result, maxOf(values[i:i+size]))
	}
	return result
}
TASK_EOF
cat > "$WORKSPACE/util.go" <<'TASK_EOF'
package window

func maxOf(values []int) int {
	best := 0
	for _, value := range values {
		if value > best {
			best = value
		}
	}
	return best
}
TASK_EOF
cat > "$WORKSPACE/window_test.go" <<'TASK_EOF'
package window

import (
	"reflect"
	"testing"
)

func TestMaxWindow(t *testing.T) {
	got := MaxWindow([]int{-5, -2, -9, -1}, 2)
	want := []int{-2, -2, -1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MaxWindow = %v, want %v", got, want)
	}
	got = MaxWindow([]int{1, 3, 2, 5, 4}, 3)
	want = []int{3, 5, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MaxWindow = %v, want %v", got, want)
	}
}
TASK_EOF
