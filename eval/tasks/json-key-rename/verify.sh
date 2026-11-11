#!/bin/sh
set -eu
if grep -R -q 'user_name' "$WORKSPACE"; then
	echo "old key remains" >&2
	exit 1
fi
diff -u "$WORKSPACE/a.json" - <<'TASK_EOF'
{"username":"ada","role":"admin"}
TASK_EOF
diff -u "$WORKSPACE/config/b.json" - <<'TASK_EOF'
{"team":{"username":"grace"}}
TASK_EOF
