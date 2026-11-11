#!/bin/sh
set -eu

cat > "$WORKSPACE/contacts.csv" <<'TASK_EOF'
name,email
Ada,ADA@Example.COM
Grace,grace@EXAMPLE.com
TASK_EOF
