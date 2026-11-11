#!/bin/sh
set -eu

cat > "$WORKSPACE/ids.txt" <<'TASK_EOF'
b
a
b
c
a
TASK_EOF
