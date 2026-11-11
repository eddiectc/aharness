#!/bin/sh
set -eu
printf 'one\ttwo\nthree\tfour\n' > "$WORKSPACE/a.txt"
printf 'alpha\tbeta\n' > "$WORKSPACE/b.txt"
mkdir -p "$WORKSPACE/nested"
printf 'x\ty\tz\n' > "$WORKSPACE/nested/c.txt"
