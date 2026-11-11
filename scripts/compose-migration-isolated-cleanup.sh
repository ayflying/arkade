#!/bin/sh
# Explicitly cleans ONLY the two disposable engines and applications in ROOT.
# Leaves all logs, manifests, engine data and application data for inspection.
set -eu
ROOT=${1:?absolute isolation root required}
case "$ROOT" in /tmp/arkade-migration-e2e-*) ;; *) echo 'invalid root' >&2; exit 2;; esac
BASE=${ROOT#/tmp/}
case "$BASE" in *[!a-zA-Z0-9_.-]*|*..*) echo 'invalid root basename' >&2; exit 2;; esac
[ -d "$ROOT" ] && [ ! -L "$ROOT" ] || exit 2
[ "$(realpath -e "$ROOT")" = "$ROOT" ] || exit 2
[ "$(cat "$ROOT/manifest-root")" = "$ROOT" ] || exit 2
umask 077
fail() { echo "CLEANUP REFUSED: $*" >&2; exit 3; }
for evidence in host.netns.before host.docker0.before host.routes.before host.ids.before a/netns b/netns; do
 [ -f "$ROOT/$evidence" ] || fail "missing baseline $evidence"
done
[ -f "$ROOT/host.containers.before" ] || [ ! -s "$ROOT/host.ids.before" ] || fail 'missing host container baseline'
for side in a b; do
 [ -d "$ROOT/$side" ] && [ ! -L "$ROOT/$side" ] || fail "side path $side"
 [ "$(realpath -e "$ROOT/$side")" = "$ROOT/$side" ] || fail "side resolution"
done
HOST_NS=$(readlink /proc/self/ns/net)
[ "$HOST_NS" = "$(cat "$ROOT/host.netns.before")" ] || fail 'caller is not original host namespace'
[ "$(cat "$ROOT/a/netns")" != "$HOST_NS" ] || fail 'a uses host netns'
[ "$(cat "$ROOT/b/netns")" != "$HOST_NS" ] || fail 'b uses host netns'
[ "$(cat "$ROOT/a/netns")" != "$(cat "$ROOT/b/netns")" ] || fail 'engine namespaces coincide'

read_pid() {
 PID=
 IFS= read -r PID < "$1" || [ -n "$PID" ]
 case "$PID" in ''|*[!0-9]*) fail "invalid pid in $1";; esac
 [ "$PID" -gt 1 ] || fail 'unsafe pid'
}
# /proc cmdline reports stat size zero even for a live process. Read contents.
alive() {
 [ -r "/proc/$1/cmdline" ] || return 1
 ARGV=$( { tr '\000' '\n' < "/proc/$1/cmdline"; } 2>/dev/null) || return 1
 [ -n "$ARGV" ]
}
arg() { printf '%s\n' "$ARGV" | grep -Fx -- "$1" > /dev/null; }
check_app() {
 app=$1
 read_pid "$ROOT/$app.pid"
 if ! alive "$PID"; then return 0; fi
 arg "$ROOT/$app.toml" || fail "app $app config identity"
 arg '-c' || fail "app $app missing config flag"
 case "$app" in
 hub) arg "$ROOT/hub" || arg "$ROOT/hub.next" || fail 'hub executable identity';;
 *) arg "$ROOT/agent" || arg "$ROOT/agent.next" || arg "$ROOT/agent.final" || arg "$ROOT/agent.release" || fail 'agent executable identity';;
 esac
}
check_engine() {
 side=$1
 read_pid "$ROOT/$side/dockerd.pid"
 alive "$PID" || fail "engine $side is not live; inspect residuals manually"
 arg "--data-root=$ROOT/$side/data" || fail 'dockerd data identity'
 arg "--exec-root=$ROOT/$side/exec" || fail 'dockerd exec identity'
 arg "--pidfile=$ROOT/$side/dockerd.pid" || fail 'dockerd pidfile identity'
 arg "unix://$ROOT/$side/docker.sock" || fail 'dockerd socket identity'
 arg "--containerd=$ROOT/$side/containerd.sock" || fail 'dockerd containerd identity'
 [ "$(readlink /proc/$PID/ns/net)" = "$(cat "$ROOT/$side/netns")" ] || fail 'dockerd network namespace'
 DPID=$PID
 read_pid "$ROOT/$side/containerd.pid"
 alive "$PID" || fail "containerd $side is not live"
 arg "$ROOT/$side/data/containerd" || fail 'containerd root identity'
 arg "$ROOT/$side/exec/containerd" || fail 'containerd state identity'
 arg "$ROOT/$side/containerd.sock" || fail 'containerd socket identity'
 arg "$ROOT/empty-containerd.toml" || fail 'containerd config identity'
 [ "$(readlink /proc/$PID/ns/net)" = "$(cat "$ROOT/$side/netns")" ] || fail 'containerd network namespace'
 [ "$(readlink /proc/$PID/ns/mnt)" = "$(readlink /proc/$DPID/ns/mnt)" ] || fail 'containerd mount namespace'
 [ "$(readlink /proc/$PID/ns/mnt)" != "$(readlink /proc/self/ns/mnt)" ] || fail 'containerd shares host mount namespace'
 [ -S "$ROOT/$side/docker.sock" ] && [ ! -L "$ROOT/$side/docker.sock" ] || fail 'invalid docker socket'
 [ "$(docker -H "unix://$ROOT/$side/docker.sock" info --format '{{.DockerRootDir}}')" = "$ROOT/$side/data" ] || fail 'socket root does not match'
}
# Preflight every recorded process before sending any signal or engine mutation.
for app in hub a/agent b/agent; do check_app "$app"; done
for side in a b; do check_engine "$side"; done

wait_dead() {
 p=$1
 for n in $(seq 1 60); do
  if ! alive "$p"; then return 0; fi
  sleep 1
 done
 fail "process $p did not exit; no SIGKILL or directory removal attempted"
}
# Hub shuts down first while the Agents can still service cancellation/cleanup RPCs.
for app in hub a/agent b/agent; do
 check_app "$app"
 if alive "$PID"; then
  printf '%s\n' "$ARGV" > "$ROOT/$app.cleanup-argv"
  kill -TERM "$PID"
  wait_dead "$PID"
 fi
done

for side in a b; do
 check_engine "$side"
 socket="unix://$ROOT/$side/docker.sock"
 # A fully private verified engine owns all its containers, including helpers.
 docker -H "$socket" ps -aq --no-trunc > "$ROOT/$side/cleanup-container-ids"
 while IFS= read -r cid; do
  [ -n "$cid" ] || continue
  case "$cid" in *[!0-9a-f]*) fail 'invalid container id';; esac
  [ "${#cid}" -eq 64 ] || fail 'non-full container id'
  docker -H "$socket" inspect "$cid" >> "$ROOT/$side/cleanup-container-inspect.json"
  check_engine "$side"
  docker -H "$socket" rm -f "$cid" >> "$ROOT/$side/cleanup-containers.log"
 done < "$ROOT/$side/cleanup-container-ids"
 check_engine "$side"
 [ -z "$(docker -H "$socket" ps -aq)" ] || fail 'new containers appeared after app shutdown'
 docker -H "$socket" volume ls -q > "$ROOT/$side/cleanup-volume-names"
 while IFS= read -r volume; do
  [ -n "$volume" ] || continue
  case "$volume" in -*|*[!a-zA-Z0-9_.-]*) fail 'invalid volume name';; esac
  docker -H "$socket" volume inspect "$volume" >> "$ROOT/$side/cleanup-volume-inspect.json"
  check_engine "$side"
  docker -H "$socket" volume rm "$volume" >> "$ROOT/$side/cleanup-volumes.log"
 done < "$ROOT/$side/cleanup-volume-names"
 [ -z "$(docker -H "$socket" volume ls -q)" ] || fail 'volumes remain'
 check_engine "$side"
 read_pid "$ROOT/$side/dockerd.pid"
 alive "$PID" || fail 'dockerd disappeared before stop'
 arg "--data-root=$ROOT/$side/data" || fail 'dockerd identity changed before stop'
 arg "--containerd=$ROOT/$side/containerd.sock" || fail 'dockerd transport changed before stop'
 printf '%s\n' "$ARGV" > "$ROOT/$side/cleanup-last-verified-argv"
 kill -TERM "$PID"
 wait_dead "$PID"
 # Independent containerd must be stopped explicitly, never via the host service.
 read_pid "$ROOT/$side/containerd.pid"
 if alive "$PID"; then
  arg "$ROOT/$side/data/containerd" || fail 'containerd root changed before stop'
  arg "$ROOT/$side/exec/containerd" || fail 'containerd state changed before stop'
  arg "$ROOT/$side/containerd.sock" || fail 'containerd socket changed before stop'
  [ "$(readlink /proc/$PID/ns/net)" = "$(cat "$ROOT/$side/netns")" ] || fail 'containerd netns changed before stop'
  printf '%s\n' "$ARGV" > "$ROOT/$side/cleanup-containerd-argv"
  kill -TERM "$PID"
  wait_dead "$PID"
 fi
done

# Host engine and network are evidence-only: NEVER mutate them, including on drift.
docker -H unix:///var/run/docker.sock ps -aq | sort > "$ROOT/host.ids.after-cleanup"
: > "$ROOT/host.containers.after-cleanup"
while IFS= read -r cid; do
 [ -n "$cid" ] || continue
 docker -H unix:///var/run/docker.sock inspect --format '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}' "$cid"
done < "$ROOT/host.ids.after-cleanup" | sort > "$ROOT/host.containers.after-cleanup"
ip -j link show docker0 > "$ROOT/host.docker0.after-cleanup"
ip -j route > "$ROOT/host.routes.after-cleanup"
readlink /proc/self/ns/net > "$ROOT/host.netns.after-cleanup"
# Missing original empty-container evidence is equivalent to an empty baseline.
[ -f "$ROOT/host.containers.before" ] || [ ! -s "$ROOT/host.ids.before" ] || fail 'missing host container baseline'
# Concurrent unrelated work may add host containers. Keep the original baseline
# immutable and report its identities separately from all current host identities.
comm -13 "$ROOT/host.ids.before" "$ROOT/host.ids.after-cleanup" > "$ROOT/host.ids.new-after-cleanup"
comm -23 "$ROOT/host.ids.before" "$ROOT/host.ids.after-cleanup" > "$ROOT/host.ids.missing-after-cleanup"
: > "$ROOT/host.original-containers.after-cleanup"
ORIGINAL_INSPECT_FAILED=0
while IFS= read -r cid; do
 [ -n "$cid" ] || continue
 if ! docker -H unix:///var/run/docker.sock inspect --format '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}' "$cid" >> "$ROOT/host.original-containers.after-cleanup"; then
  ORIGINAL_INSPECT_FAILED=1
 fi
done < "$ROOT/host.ids.before"
sort "$ROOT/host.original-containers.after-cleanup" > "$ROOT/host.original-containers.after-cleanup.sorted"
if [ "$ORIGINAL_INSPECT_FAILED" -eq 0 ] && [ ! -s "$ROOT/host.ids.missing-after-cleanup" ]; then
 if [ -f "$ROOT/host.containers.before" ]; then
  if cmp "$ROOT/host.containers.before" "$ROOT/host.original-containers.after-cleanup.sorted"; then
   echo ORIGINAL_HOST_CONTAINERS_UNCHANGED
  else
   echo 'WARNING: original host container state drift' >&2
  fi
 elif [ ! -s "$ROOT/host.original-containers.after-cleanup.sorted" ]; then
  echo ORIGINAL_HOST_CONTAINERS_UNCHANGED
 fi
else
 echo 'WARNING: original host container missing or unreadable' >&2
fi
if [ -s "$ROOT/host.ids.new-after-cleanup" ]; then
 echo 'WARNING: host has additional containers from concurrent environment activity; untouched by cleanup:' >&2
 cat "$ROOT/host.ids.new-after-cleanup" >&2
fi
printf 'ISOLATED_RESOURCES_CLEANED_EVIDENCE_RECORDED %s\n' "$ROOT"
DRIFT=0
cmp "$ROOT/host.ids.before" "$ROOT/host.ids.after-cleanup" || DRIFT=1
if [ -f "$ROOT/host.containers.before" ]; then
 cmp "$ROOT/host.containers.before" "$ROOT/host.containers.after-cleanup" || DRIFT=1
else
 [ ! -s "$ROOT/host.containers.after-cleanup" ] || DRIFT=1
fi
cmp "$ROOT/host.docker0.before" "$ROOT/host.docker0.after-cleanup" || DRIFT=1
cmp "$ROOT/host.routes.before" "$ROOT/host.routes.after-cleanup" || DRIFT=1
cmp "$ROOT/host.netns.before" "$ROOT/host.netns.after-cleanup" || DRIFT=1
[ "$DRIFT" -eq 0 ] || fail 'host evidence drift; inspect logs manually, no repair attempted'
printf 'ISOLATED_CLEANUP_COMPLETE_HOST_UNCHANGED %s\n' "$ROOT"
