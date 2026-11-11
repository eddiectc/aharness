#!/bin/sh
set -eu

cat > "$WORKSPACE/go.mod" <<'TASK_EOF'
module example.com/slug

go 1.21
TASK_EOF
cat > "$WORKSPACE/slug.go" <<'TASK_EOF'
package slug

import "strings"

// Slug converts a display string into a lowercase hyphenated slug.
func Slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.ReplaceAll(value, " ", "-")
}
TASK_EOF
cat > "$WORKSPACE/slug_test.go" <<'TASK_EOF'
package slug

import "testing"

func TestSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"  Hello  World  ", "hello-world"},
		{"Go 1.27", "go-1.27"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Slug(tt.input); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
TASK_EOF
