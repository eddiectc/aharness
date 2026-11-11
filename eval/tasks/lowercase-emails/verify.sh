#!/bin/sh
set -eu
diff -u "$WORKSPACE/contacts.csv" - <<'TASK_EOF'
name,email
Ada,ada@example.com
Grace,grace@example.com
TASK_EOF
