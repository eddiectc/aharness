#!/bin/sh
set -eu
grep -Eq '^debug *= *true$' "$WORKSPACE/app.ini"
grep -Eq '^debug *= *true$' "$WORKSPACE/worker.ini"
if grep -Eq '^debug *= *false$' "$WORKSPACE/app.ini" "$WORKSPACE/worker.ini"; then
	echo "debug is still false" >&2
	exit 1
fi
