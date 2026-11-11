#!/bin/sh
set -eu

cat > "$WORKSPACE/data.csv" <<'TASK_EOF'
id,amount
a,5
b,12
c,20
d,9
TASK_EOF
