#!/usr/bin/env bash
# Official gf v2.9.6 generation, isolated from the application module.
set -euo pipefail
: "${GFGEN_DSN:?Set GFGEN_DSN to a dedicated PostgreSQL database URL}"
case "$GFGEN_DSN" in postgres://*|postgresql://*) ;; *) echo 'PostgreSQL URL required' >&2; exit 2;; esac
GF_BIN="${GF_BIN:-gf}"
HUB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$HUB_DIR"
WORK="$(mktemp -d -t arkade_gfgen_XXXXXXXX)"
cleanup() {
 # Resolve and validate the exact temporary directory before deleting.
 local resolved
 resolved="$(cd "$WORK" && pwd)"
 if [[ "$resolved" == "$WORK" && "${WORK##*/}" == arkade_gfgen_* ]]; then rm -rf -- "$WORK"; fi
}
trap cleanup EXIT
go run ./tools/gfgen/mkgen "$GFGEN_DSN"
TABLES="$(go run ./tools/gfgen/mktables "$GFGEN_DSN")"
[ -n "$TABLES" ] || { echo 'No business tables found' >&2; exit 1; }
printf '%s\n' 'module arkade/hub' '' 'go 1.24.0' '' 'require (' ' github.com/gogf/gf/contrib/drivers/pgsql/v2 v2.9.6' ' github.com/gogf/gf/v2 v2.9.6' ')' > "$WORK/go.mod"
cp go.sum "$WORK/go.sum"
mkdir -p "$WORK/manifest/config" "$WORK/internal"
go run ./tools/gfgen/mkconfig "$GFGEN_DSN" > "$WORK/manifest/config/config.json"
(cd "$WORK" && "$GF_BIN" gen dao -p internal -t "$TABLES")
for sub in dao model/do model/entity; do
 src="$WORK/internal/$sub"
 [ -d "$src" ] || continue
 while IFS= read -r -d '' f; do
  rel="${f#"$src"/}"
  mkdir -p "$HUB_DIR/internal/$sub/$(dirname "$rel")"
  cp "$f" "$HUB_DIR/internal/$sub/$rel"
 done < <(find "$src" -name '*.go' -type f -print0)
done
