#!/bin/sh
set -eu
ROOT=${1:?root required}
case "$ROOT" in /tmp/arkade-migration-e2e-*) ;; *) exit 2;; esac
[ "$(cat "$ROOT/manifest-root")" = "$ROOT" ]
# Never operate on a PID unless its exact process argv names this side's own data root.
for side in a b; do
 [ -f "$ROOT/$side/dockerd.pid" ] || continue
 pid=$(cat "$ROOT/$side/dockerd.pid")
 if [ -r "/proc/$pid/cmdline" ]; then
  tr '\000' '\n' < "/proc/$pid/cmdline" | grep -Fx -- "--data-root=$ROOT/$side/data" > /dev/null || exit 3
  kill -TERM "$pid"
  for n in $(seq 1 30); do
   [ ! -r "/proc/$pid/cmdline" ] && break
   [ -z "$(tr '\000' ' ' < "/proc/$pid/cmdline")" ] && break
   sleep 1
  done
 fi
done
for side in a b; do
 [ -f "$ROOT/$side/containerd.pid" ] || continue
 pid=$(cat "$ROOT/$side/containerd.pid")
 if [ -r "/proc/$pid/cmdline" ] && [ -n "$(tr '\000' ' ' < "/proc/$pid/cmdline")" ]; then
  tr '\000' '\n' < "/proc/$pid/cmdline" | grep -Fx "$ROOT/$side/containerd.sock" > /dev/null || exit 3
  kill -TERM "$pid"
  for n in $(seq 1 30); do
   [ ! -r "/proc/$pid/cmdline" ] && break
   [ -z "$(tr '\000' ' ' < "/proc/$pid/cmdline")" ] && break
   sleep 1
  done
 fi
done
# No directory/network/volume removal here. Leave all evidence for manual review.
echo ISOLATED_DAEMONS_STOPPED
