#!/bin/sh
set -eu

cat > "$WORKSPACE/app.ini" <<'TASK_EOF'
debug = false
port = 8080
TASK_EOF
cat > "$WORKSPACE/worker.ini" <<'TASK_EOF'
debug=false
queue = jobs
TASK_EOF
