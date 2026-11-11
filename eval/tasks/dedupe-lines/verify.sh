#!/bin/sh
set -eu
diff -u "$WORKSPACE/ids.txt" - <<'TASK_EOF'
b
a
c
TASK_EOF
