#!/bin/sh
set -eu
ROOT=${1:?root required}
case "$ROOT" in /tmp/arkade-migration-e2e-*) ;; *) exit 2;; esac
[ "$(cat "$ROOT/manifest-root")" = "$ROOT" ]
[ -x "$ROOT/hub" ] && [ -x "$ROOT/agent" ]
umask 077
TOKEN=$(openssl rand -hex 24)
PASS=$(openssl rand -hex 16)
printf '%s\n' "$PASS" > "$ROOT/admin-password"
cat > "$ROOT/hub.toml" <<EOF
[server]
addr = "127.0.0.1:18191"
base_url = "http://127.0.0.1:18191"
[database]
path = "$ROOT/hub.db"
[auth]
initial_user = "admin"
initial_pass = "$PASS"
agent_token = "$TOKEN"
[web]
dist = "$ROOT/web"
EOF
# All processes native on host, but runtime API and Compose explicitly use private sockets.
# No default socket fallback is permitted.
nohup "$ROOT/hub" -c "$ROOT/hub.toml" > "$ROOT/hub.log" 2>&1 < /dev/null &
echo "$!" > "$ROOT/hub.pid"
for attempt in $(seq 1 30); do
 if curl -fsS http://127.0.0.1:18191/healthz > "$ROOT/health.json"; then break; fi
 sleep 1
done
for side in a b; do
 cat > "$ROOT/$side/agent.toml" <<EOF
hub_url = "ws://127.0.0.1:18191/agent/connect"
token = "$TOKEN"
agent_id = "arkmig-$side"
data_dir = "$ROOT/$side/agent-data"
name = "isolated-migration-$side"
socket = "unix://$ROOT/$side/docker.sock"
runtime_kind = "docker"
heartbeat_sec = 2
log_level = "info"
EOF
 DOCKER_HOST="unix://$ROOT/$side/docker.sock" nohup "$ROOT/agent" run -c "$ROOT/$side/agent.toml" > "$ROOT/$side/agent.log" 2>&1 < /dev/null &
 echo "$!" > "$ROOT/$side/agent.pid"
done
printf 'ISOLATED_APPS_STARTED %s\n' "$ROOT"
