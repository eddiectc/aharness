#!/bin/sh
set -eu

mkdir -p "$WORKSPACE/config"
cat > "$WORKSPACE/a.json" <<'TASK_EOF'
{"user_name":"ada","role":"admin"}
TASK_EOF
cat > "$WORKSPACE/config/b.json" <<'TASK_EOF'
{"team":{"user_name":"grace"}}
TASK_EOF
