#!/bin/sh
set -eu

cat > "$WORKSPACE/users.csv" <<'TASK_EOF'
id,name
1,ada
2,grace
3,linus
TASK_EOF
cat > "$WORKSPACE/orders.csv" <<'TASK_EOF'
id,amount
2,30
1,10
3,20
TASK_EOF
