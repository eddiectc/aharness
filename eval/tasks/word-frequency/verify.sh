#!/bin/sh
set -eu
diff -u "$WORKSPACE/top.txt" - <<'TASK_EOF'
blue 3
red 3
green 2
TASK_EOF
