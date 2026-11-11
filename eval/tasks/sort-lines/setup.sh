#!/bin/sh
set -eu

cat > "$WORKSPACE/names.txt" <<'TASK_EOF'
pear
apple
cherry
apple
TASK_EOF
