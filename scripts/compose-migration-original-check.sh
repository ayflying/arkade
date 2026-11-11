#!/bin/sh
set -eu
ROOT=${1:?root}
case "$ROOT" in /tmp/arkade-migration-e2e-*) ;; *) exit 2;; esac
[ "$(cat "$ROOT/manifest-root")" = "$ROOT" ]
xargs docker -H unix:///var/run/docker.sock inspect --format '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}' < "$ROOT/host.ids.before" | sort > "$ROOT/host.original.current"
cmp "$ROOT/host.containers.before" "$ROOT/host.original.current"
echo ORIGINAL_BUSINESS_CONTAINERS_UNCHANGED
