#!/bin/sh
set -eu
ROOT=${1:?root required}
APP=${2:?app name required}
case "$ROOT" in /tmp/arkade-migration-e2e-*) ;; *) exit 2;; esac
case "$APP" in hub|a/agent|b/agent) ;; *) exit 2;; esac
pid=$(cat "$ROOT/$APP.pid")
tr '\000' '\n' < "/proc/$pid/cmdline" | grep -Fx "$ROOT/$APP.toml" > /dev/null || exit 3
kill -TERM "$pid"
for n in $(seq 1 30); do
 [ ! -s "/proc/$pid/cmdline" ] && break
 sleep 1
done
if [ "$APP" = hub ]; then
 mv "$ROOT/hub.next" "$ROOT/hub"
 chmod 700 "$ROOT/hub"
 nohup "$ROOT/hub" -c "$ROOT/hub.toml" >> "$ROOT/hub.log" 2>&1 < /dev/null &
 echo "$!" > "$ROOT/hub.pid"
else
 side=${APP%%/*}
 # Agent binary replacement is separate, shared binary path is only changed while stopped.
 binary="$ROOT/agent.next"
 [ ! -f "$ROOT/agent.final" ] || binary="$ROOT/agent.final"
 [ ! -f "$ROOT/agent.release" ] || binary="$ROOT/agent.release"
 DOCKER_HOST="unix://$ROOT/$side/docker.sock" nohup "$binary" run -c "$ROOT/$APP.toml" >> "$ROOT/$APP.log" 2>&1 < /dev/null &
 echo "$!" > "$ROOT/$APP.pid"
fi
