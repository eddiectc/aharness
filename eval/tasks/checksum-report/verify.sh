#!/bin/sh
set -eu
test -f "$WORKSPACE/checksums.txt"
[ "$(wc -l < "$WORKSPACE/checksums.txt")" -eq 2 ]
expected_a=$(sha256sum "$WORKSPACE/files/a.txt" | awk '{print $1}')
got_a=$(awk '$2 == "files/a.txt" {print $1}' "$WORKSPACE/checksums.txt")
[ "$got_a" = "$expected_a" ]
expected_b=$(sha256sum "$WORKSPACE/files/b.txt" | awk '{print $1}')
got_b=$(awk '$2 == "files/b.txt" {print $1}' "$WORKSPACE/checksums.txt")
[ "$got_b" = "$expected_b" ]
sort -c "$WORKSPACE/checksums.txt"
