#!/bin/sh
set -eu
if grep -nE '[[:blank:]]+$' "$WORKSPACE/config.ini" "$WORKSPACE/values.txt"; then
	echo "trailing whitespace remains" >&2
	exit 1
fi
diff -u "$WORKSPACE/config.ini" - <<'TASK_EOF'
name = aharness
version = 1
TASK_EOF
diff -u "$WORKSPACE/values.txt" - <<'TASK_EOF'
alpha
beta
TASK_EOF
