#!/bin/sh
set -eu
diff -u "$WORKSPACE/filtered.csv" - <<'TASK_EOF'
id,amount
b,12
c,20
TASK_EOF
