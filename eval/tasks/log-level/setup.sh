#!/bin/sh
set -eu

cat > "$WORKSPACE/app.log" <<'TASK_EOF'
INFO start
WARN disk low
INFO done
WARN disk low
TASK_EOF
cat > "$WORKSPACE/worker.log" <<'TASK_EOF'
INFO worker up
WARN retrying job
TASK_EOF
