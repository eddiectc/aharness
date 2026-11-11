#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/search

go 1.21
TASK_EOF
cat > "$WORKSPACE/search.go" <<'TASK_EOF'
package search

// Index returns the position of target in sorted values, or -1.
func Index(values []int, target int) int {
	low, high := 0, len(values)-1
	for low <= high {
		mid := low + (high-low)/2
		switch {
		case values[mid] == target:
			return mid
		case values[mid] < target:
			low = mid + 1
		default:
			high = mid - 1
		}
	}
	return len(values)
}
TASK_EOF
cat > "$WORKSPACE/search_test.go" <<'TASK_EOF'
package search

import "testing"

func TestIndex(t *testing.T) {
	values := []int{1, 3, 5, 7, 9}
	if got := Index(values, 7); got != 3 {
		t.Errorf("Index(7) = %d, want 3", got)
	}
	if got := Index(values, 1); got != 0 {
		t.Errorf("Index(1) = %d, want 0", got)
	}
	if got := Index(values, 4); got != -1 {
		t.Errorf("Index(4) = %d, want -1", got)
	}
	if got := Index(nil, 1); got != -1 {
		t.Errorf("Index(nil) = %d, want -1", got)
	}
}
TASK_EOF
