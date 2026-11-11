#!/bin/sh
set -eu

mkdir -p "$WORKSPACE/docs"
cat > "$WORKSPACE/README.md" <<'TASK_EOF'
# {{PROJECT}}

{{PROJECT}} is a harness.
TASK_EOF
cat > "$WORKSPACE/docs/guide.md" <<'TASK_EOF'
Use {{PROJECT}} for evaluation.
TASK_EOF
