#!/bin/sh
set -eu
test -f "$WORKSPACE/errors.txt"
value=$(tr -d '[:space:]' < "$WORKSPACE/errors.txt")
if [ "$value" != "4" ]; then
	echo "errors = $value, want 4" >&2
	exit 1
fi
