#!/bin/sh
set -eu

mkdir -p "$WORKSPACE/sub"
cat > "$WORKSPACE/a.log" <<'TASK_EOF'
alpha
TASK_EOF
cat > "$WORKSPACE/b.log" <<'TASK_EOF'
beta
TASK_EOF
cat > "$WORKSPACE/sub/c.log" <<'TASK_EOF'
gamma
TASK_EOF
cat > "$WORKSPACE/notes.txt" <<'TASK_EOF'
keep
TASK_EOF
