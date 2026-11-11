#!/bin/sh
set -eu
sort -c "$WORKSPACE/names.txt"
diff -u "$WORKSPACE/names.txt" - <<'TASK_EOF'
apple
apple
cherry
pear
TASK_EOF
