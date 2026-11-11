#!/bin/sh
set -eu
if grep -R -q '{{PROJECT}}' "$WORKSPACE"; then
	echo "placeholder remains" >&2
	exit 1
fi
diff -u "$WORKSPACE/README.md" - <<'TASK_EOF'
# aharness

aharness is a harness.
TASK_EOF
diff -u "$WORKSPACE/docs/guide.md" - <<'TASK_EOF'
Use aharness for evaluation.
TASK_EOF
