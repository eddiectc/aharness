#!/bin/sh
set -eu

cat > "$WORKSPACE/words.txt" <<'TASK_EOF'
red blue red green blue red
green blue yellow
TASK_EOF
