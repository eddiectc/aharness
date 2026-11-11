#!/bin/sh
set -eu
export GOCACHE="$WORKSPACE/.gocache"
export GOPATH="$WORKSPACE/.gopath"
export GOTOOLCHAIN=local
export GOPROXY=off
export GOFLAGS=
export GOWORK=off
cd "$WORKSPACE"
go test ./...
