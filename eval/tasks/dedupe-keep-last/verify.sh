#!/bin/sh
set -eu
diff -u "$WORKSPACE/deduped.csv" - <<'TASK_EOF'
id,value
a,3
c,4
b,5
TASK_EOF
