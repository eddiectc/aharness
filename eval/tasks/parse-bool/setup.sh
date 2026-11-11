#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/parsebool

go 1.21
TASK_EOF
cat > "$WORKSPACE/parse.go" <<'TASK_EOF'
package parsebool

import (
	"fmt"
	"strings"
)

// ParseBool parses a human-readable boolean.
func ParseBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "yes", "true":
		return true, nil
	case "no", "false":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q", value)
}
TASK_EOF
cat > "$WORKSPACE/parse_test.go" <<'TASK_EOF'
package parsebool

import "testing"

func TestParseBool(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{" YES ", true},
		{"True", true},
		{"no", false},
		{" FALSE ", false},
	}
	for _, tt := range tests {
		got, err := ParseBool(tt.input)
		if err != nil {
			t.Fatalf("ParseBool(%q): %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("ParseBool(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
	if _, err := ParseBool("maybe"); err == nil {
		t.Error("ParseBool(maybe) succeeded, want error")
	}
}
TASK_EOF
