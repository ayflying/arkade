#!/bin/sh
set -eu
ROOT=${1:?root required}
case "$ROOT" in /tmp/arkade-migration-e2e-*) ;; *) exit 2;; esac
[ "$(cat "$ROOT/manifest-root")" = "$ROOT" ]
for side in a b; do
 pid=$(cat "$ROOT/$side/dockerd.pid")
 [ "$(readlink /proc/$pid/ns/net)" = "$(cat "$ROOT/$side/netns")" ]
 cpid=$(cat "$ROOT/$side/containerd.pid")
 [ "$(readlink /proc/$cpid/ns/net)" = "$(cat "$ROOT/$side/netns")" ]
 tr '\000' '\n' < "/proc/$cpid/cmdline" > "$ROOT/$side/containerd-argv.current"
 grep -Fx "$ROOT/$side/containerd.sock" "$ROOT/$side/containerd-argv.current"
 grep -Fx "$ROOT/$side/data/containerd" "$ROOT/$side/containerd-argv.current"
 grep -Fx "$ROOT/$side/exec/containerd" "$ROOT/$side/containerd-argv.current"
 ctr --address "$ROOT/$side/containerd.sock" version
done
docker -H unix:///var/run/docker.sock ps -aq | sort > "$ROOT/host.ids.current"
cmp "$ROOT/host.ids.before" "$ROOT/host.ids.current"
xargs docker -H unix:///var/run/docker.sock inspect --format '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}' < "$ROOT/host.ids.before" | sort > "$ROOT/host.containers.current"
cmp "$ROOT/host.containers.before" "$ROOT/host.containers.current"
ip -j link show docker0 > "$ROOT/host.docker0.current"
ip -j route > "$ROOT/host.routes.current"
cmp "$ROOT/host.docker0.before" "$ROOT/host.docker0.current"
cmp "$ROOT/host.routes.before" "$ROOT/host.routes.current"
echo HOST_UNCHANGED
