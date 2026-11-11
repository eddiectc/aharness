#!/bin/sh
set -eu

cat > "$WORKSPACE/sales.csv" <<'TASK_EOF'
region,amount
north,10
south,20
west,12
TASK_EOF
