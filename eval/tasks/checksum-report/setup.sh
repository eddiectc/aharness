#!/bin/sh
set -eu

mkdir -p "$WORKSPACE/files"
mkdir -p "$WORKSPACE/files"
cat > "$WORKSPACE/files/a.txt" <<'TASK_EOF'
alpha
TASK_EOF
cat > "$WORKSPACE/files/b.txt" <<'TASK_EOF'
beta
TASK_EOF
