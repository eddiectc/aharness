#!/bin/sh
set -eu
printf 'name = aharness   \nversion = 1  \n' > "$WORKSPACE/config.ini"
printf 'alpha\t\nbeta \n' > "$WORKSPACE/values.txt"
