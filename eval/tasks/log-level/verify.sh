#!/bin/sh
set -eu
if grep -R -q 'WARN ' "$WORKSPACE"; then
	echo "old WARN token remains" >&2
	exit 1
fi
diff -u "$WORKSPACE/app.log" - <<'TASK_EOF'
INFO start
WARNING disk low
INFO done
WARNING disk low
TASK_EOF
diff -u "$WORKSPACE/worker.log" - <<'TASK_EOF'
INFO worker up
WARNING retrying job
TASK_EOF
