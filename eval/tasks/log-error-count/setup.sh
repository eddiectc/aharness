#!/bin/sh
set -eu

cat > "$WORKSPACE/app.log" <<'TASK_EOF'
INFO boot
ERROR disk
WARN slow
ERROR disk
INFO ok
ERROR queue
ERROR disk
TASK_EOF
