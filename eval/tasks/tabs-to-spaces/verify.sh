#!/bin/sh
set -eu
if grep -R -q "$(printf '\t')" "$WORKSPACE"; then
	echo "tab characters remain" >&2
	exit 1
fi
diff -u "$WORKSPACE/a.txt" - <<'TASK_EOF'
one    two
three    four
TASK_EOF
diff -u "$WORKSPACE/b.txt" - <<'TASK_EOF'
alpha    beta
TASK_EOF
diff -u "$WORKSPACE/nested/c.txt" - <<'TASK_EOF'
x    y    z
TASK_EOF
