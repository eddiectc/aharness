#!/bin/sh
set -eu
for old in a.log b.log sub/c.log; do
	if [ -e "$WORKSPACE/$old" ]; then
		echo "old file remains: $old" >&2
		exit 1
	fi
done
test -f "$WORKSPACE/a.txt"
test -f "$WORKSPACE/b.txt"
test -f "$WORKSPACE/sub/c.txt"
diff -u "$WORKSPACE/a.txt" - <<'TASK_EOF'
alpha
TASK_EOF
diff -u "$WORKSPACE/b.txt" - <<'TASK_EOF'
beta
TASK_EOF
diff -u "$WORKSPACE/sub/c.txt" - <<'TASK_EOF'
gamma
TASK_EOF
diff -u "$WORKSPACE/renamed.txt" - <<'TASK_EOF'
a.txt
b.txt
sub/c.txt
TASK_EOF
