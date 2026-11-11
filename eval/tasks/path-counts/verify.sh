#!/bin/sh
set -eu
diff -u "$WORKSPACE/paths.txt" - <<'TASK_EOF'
/a 3
/b 1
/c 1
TASK_EOF
