#!/bin/sh
set -eu
diff -u "$WORKSPACE/report.csv" - <<'TASK_EOF'
name,amount
ada,10
grace,30
linus,20
TASK_EOF
