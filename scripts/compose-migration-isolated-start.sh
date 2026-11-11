#!/bin/sh
# Creates two disposable engines. Never addresses the host engine except read-only evidence/image save.
set -eu
ROOT=${1:?absolute new isolation root required}
case "$ROOT" in /tmp/arkade-migration-e2e-*) ;; *) echo 'invalid isolation root' >&2; exit 2;; esac
BASE=${ROOT#/tmp/}
case "$BASE" in *[!a-zA-Z0-9_.-]*|*..*) echo 'invalid root basename' >&2; exit 2;; esac
[ "$(realpath -m "$ROOT")" = "$ROOT" ] || exit 2
[ ! -e "$ROOT" ] || { echo 'root already exists' >&2; exit 2; }
umask 077
mkdir -p "$ROOT"
printf '%s\n' "$ROOT" > "$ROOT/manifest-root"
readlink /proc/self/ns/net > "$ROOT/host.netns.before"
ip -j link show docker0 > "$ROOT/host.docker0.before"
ip -j route > "$ROOT/host.routes.before"
docker -H unix:///var/run/docker.sock ps -aq | sort > "$ROOT/host.ids.before"
if [ -s "$ROOT/host.ids.before" ]; then
 xargs docker -H unix:///var/run/docker.sock inspect --format '{{.Id}} {{.State.StartedAt}} {{.RestartCount}}' < "$ROOT/host.ids.before" | sort > "$ROOT/host.containers.before"
fi
docker -H unix:///var/run/docker.sock image save alpine:3.20 -o "$ROOT/alpine.tar"
printf '{}\n' > "$ROOT/empty.json"
printf 'version = 3\n' > "$ROOT/empty-containerd.toml"
cat > "$ROOT/engine-child.sh" <<'CHILD'
#!/bin/sh
set -eu
ROOT=$1
SIDE=$2
mount --make-rprivate /
# Docker's global netns registry must also be isolated in this mount namespace.
mount -t tmpfs -o mode=755 tmpfs /run/docker/netns
ip link set lo up
mkdir -p "$ROOT/$SIDE/exec/containerd" "$ROOT/$SIDE/data/containerd"
containerd --config "$ROOT/empty-containerd.toml" --root "$ROOT/$SIDE/data/containerd" --state "$ROOT/$SIDE/exec/containerd" \
 --address "$ROOT/$SIDE/containerd.sock" > "$ROOT/$SIDE/containerd.log" 2>&1 &
echo "$!" > "$ROOT/$SIDE/containerd.pid"
for n in $(seq 1 30); do
 if ctr --address "$ROOT/$SIDE/containerd.sock" version > /dev/null 2>&1; then break; fi
 kill -0 "$(cat "$ROOT/$SIDE/containerd.pid")" || exit 1
 sleep 1
done
exec dockerd --config-file="$ROOT/empty.json" \
 --data-root="$ROOT/$SIDE/data" --exec-root="$ROOT/$SIDE/exec" \
 --pidfile="$ROOT/$SIDE/dockerd.pid" -H "unix://$ROOT/$SIDE/docker.sock" \
 --containerd="$ROOT/$SIDE/containerd.sock" \
 --bridge=none --iptables=false --ip6tables=false --ip-forward=false --ip-masq=false \
 --userland-proxy=false --storage-driver=vfs --exec-opt native.cgroupdriver=cgroupfs \
 --containerd-namespace="arkmig-$SIDE" --containerd-plugins-namespace="arkmig-plugin-$SIDE"
CHILD
chmod 700 "$ROOT/engine-child.sh"
for side in a b; do
 mkdir -p "$ROOT/$side"
 nohup unshare --net --mount --propagation private sh "$ROOT/engine-child.sh" "$ROOT" "$side" > "$ROOT/$side/daemon.log" 2>&1 < /dev/null &
 echo "$!" > "$ROOT/$side/launcher.pid"
 for attempt in $(seq 1 60); do
  if docker -H "unix://$ROOT/$side/docker.sock" info > "$ROOT/$side/info.txt" 2>/dev/null; then break; fi
  kill -0 "$(cat "$ROOT/$side/launcher.pid")" || { cat "$ROOT/$side/daemon.log"; exit 1; }
  sleep 1
 done
 docker -H "unix://$ROOT/$side/docker.sock" info --format '{{.DockerRootDir}}' > "$ROOT/$side/root.txt"
 [ "$(cat "$ROOT/$side/root.txt")" = "$ROOT/$side/data" ] || exit 1
 cp "$ROOT/$side/containerd.pid" "$ROOT/$side/containerd-pid-evidence"
 tr '\000' '\n' < "/proc/$(cat "$ROOT/$side/containerd.pid")/cmdline" > "$ROOT/$side/containerd-argv"
 grep -Fx "$ROOT/$side/containerd.sock" "$ROOT/$side/containerd-argv" > /dev/null
 grep -Fx "$ROOT/$side/data/containerd" "$ROOT/$side/containerd-argv" > /dev/null
 readlink "/proc/$(cat "$ROOT/$side/dockerd.pid")/ns/net" > "$ROOT/$side/netns"
 [ "$(readlink /proc/$(cat "$ROOT/$side/containerd.pid")/ns/net)" = "$(cat "$ROOT/$side/netns")" ]
 [ "$(cat "$ROOT/$side/netns")" != "$(cat "$ROOT/host.netns.before")" ] || exit 1
 docker -H "unix://$ROOT/$side/docker.sock" load -i "$ROOT/alpine.tar" > "$ROOT/$side/load.log"
 docker -H "unix://$ROOT/$side/docker.sock" image inspect alpine:3.20 --format '{{json .RootFS}} {{.Os}}/{{.Architecture}}' > "$ROOT/$side/image.txt"
done
[ "$(cat "$ROOT/a/netns")" != "$(cat "$ROOT/b/netns")" ]
cmp "$ROOT/a/image.txt" "$ROOT/b/image.txt"
ip -j link show docker0 > "$ROOT/host.docker0.after-start"
ip -j route > "$ROOT/host.routes.after-start"
cmp "$ROOT/host.docker0.before" "$ROOT/host.docker0.after-start"
cmp "$ROOT/host.routes.before" "$ROOT/host.routes.after-start"
printf 'ISOLATED_ENGINES_READY %s\n' "$ROOT"
