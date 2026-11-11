#!/bin/sh
set -eu
diff -u "$WORKSPACE/formatted.txt" - <<'TASK_EOF'
3.00
0.00
12.00
-5.00
TASK_EOF
