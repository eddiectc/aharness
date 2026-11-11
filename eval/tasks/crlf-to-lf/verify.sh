#!/bin/sh
set -eu
if grep -q "$(printf '\r')" "$WORKSPACE/data.csv"; then
	echo "carriage returns remain" >&2
	exit 1
fi
diff -u "$WORKSPACE/data.csv" - <<'TASK_EOF'
id,name
1,ada
2,grace
TASK_EOF
