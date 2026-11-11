#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/csvtotal

go 1.21
TASK_EOF
cat > "$WORKSPACE/parse.go" <<'TASK_EOF'
package csvtotal

import (
	"fmt"
	"strconv"
	"strings"
)

// SumCSV returns the sum of integer rows after the header line.
func SumCSV(data string) (int, error) {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) < 2 {
		return 0, nil
	}
	total := 0
	for _, line := range lines {
		value, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			return 0, fmt.Errorf("parse %q: %w", line, err)
		}
		total += value
	}
	return total, nil
}
TASK_EOF
cat > "$WORKSPACE/parse_test.go" <<'TASK_EOF'
package csvtotal

import "testing"

func TestSumCSV(t *testing.T) {
	got, err := SumCSV("amount\n10\n20\n12\n")
	if err != nil {
		t.Fatalf("SumCSV: %v", err)
	}
	if got != 42 {
		t.Fatalf("SumCSV = %d, want 42", got)
	}
	got, err = SumCSV("amount\n")
	if err != nil {
		t.Fatalf("SumCSV(header only): %v", err)
	}
	if got != 0 {
		t.Fatalf("SumCSV(header only) = %d, want 0", got)
	}
}
TASK_EOF
