#!/bin/sh
set -eu
test -f "$WORKSPACE/total.txt"
value=$(tr -d '[:space:]' < "$WORKSPACE/total.txt")
if [ "$value" != "42" ]; then
	echo "total = $value, want 42" >&2
	exit 1
fi
