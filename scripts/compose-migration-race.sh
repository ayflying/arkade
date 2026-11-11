#!/bin/sh
set -eu
export GOROOT=/tmp/arkade-race-toolchain/golang.org/toolchain@v0.0.1-go1.24.13.linux-amd64
export GOPATH=/mnt/d/gopath GOMODCACHE=/mnt/d/gopath/pkg/mod GOCACHE=/tmp/arkade-race-cache GOTOOLCHAIN=local CGO_ENABLED=1 GOPROXY=https://goproxy.cn,direct
cd /mnt/d/git/yunloli/arkade/hub
"$GOROOT/bin/go" test -race ./internal/stackmigrate ./internal/service/stackmigration ./internal/agentlink -count=1 -timeout=5m
cd ../agent
"$GOROOT/bin/go" test -race ./internal/compose/migrate ./internal/compose ./internal/handler ./internal/imagetransfer -count=1 -timeout=5m
