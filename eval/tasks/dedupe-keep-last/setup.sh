#!/bin/sh
set -eu

cat > "$WORKSPACE/events.csv" <<'TASK_EOF'
id,value
a,1
b,2
a,3
c,4
b,5
TASK_EOF
