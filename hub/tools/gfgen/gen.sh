#!/usr/bin/env bash
# GoFrame DAO 代码生成（隔离环境版）。
#
# 为什么不能在 hub/ 直接跑 gf gen dao，见同目录 README.md。
#
# 用法：
#   bash ./tools/gfgen/gen.sh
#
# 环境变量：
#   GF_BIN  gf CLI 可执行文件路径（默认找 gf296 下的 v2.9.6）
set -euo pipefail

HUB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$HUB_DIR"

# ---------- 定位 gf CLI ----------
GF_BIN="${GF_BIN:-}"
if [ -z "$GF_BIN" ]; then
	for cand in \
		"$HOME/gopath/gf296/gf" \
		"/d/gopath/gf296/gf.exe" \
		"$(command -v gf 2>/dev/null || true)"
	do
		if [ -n "$cand" ] && [ -x "$cand" ]; then
			GF_BIN="$cand"
			break
		fi
	done
fi
if [ -z "$GF_BIN" ] || [ ! -x "$GF_BIN" ]; then
	echo "错误：找不到 gf CLI（v2.9.6）。用 GF_BIN 指定路径。" >&2
	exit 1
fi
echo "使用 gf CLI: $GF_BIN"

# ---------- 建临时生成环境 ----------
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/manifest/config" "$WORK/internal"
DB_PATH="$WORK/gen.db"

# ---------- 1. 用 hub 的真实迁移建库 ----------
echo "==> 建库（走 hub 的真实迁移）"
CGO_ENABLED=0 go run ./tools/gfgen/mkgen "$DB_PATH"

# ---------- 2. 生成环境配置 ----------
# module 名与 hub 相同：生成物里会出现 "arkade/hub/internal/dao/internal"，
# 这样原样拷贝进 hub 就是正确 import，无需事后改写。
cat > "$WORK/go.mod" <<'EOF'
module arkade/hub

go 1.24.0

require (
	github.com/gogf/gf/contrib/drivers/sqlite/v2 v2.9.6
	github.com/gogf/gf/v2 v2.9.6
)
EOF
cp go.sum "$WORK/go.sum"

# 绝对路径，正斜杠（YAML 里反斜杠会被当转义符）。
DB_FWD="$(echo "$DB_PATH" | tr '\\' '/')"
cat > "$WORK/manifest/config/config.yaml" <<EOF
database:
  default:
    type: sqlite
    name: $DB_FWD
EOF

# ---------- 3. 读出业务表名 ----------
# 排除迁移体系自建的两张表：它们不是业务表，不该生成 DAO。
TABLES="$(CGO_ENABLED=0 go run ./tools/gfgen/mktables "$DB_PATH")"
if [ -z "$TABLES" ]; then
	echo "错误：没有找到任何业务表" >&2
	exit 1
fi
echo "==> 生成表的 DAO: $TABLES"

# ---------- 4. 调用 gf CLI ----------
echo "==> gf gen dao"
( cd "$WORK" && "$GF_BIN" gen dao -p internal -t "$TABLES" )

# ---------- 5. 覆盖回 hub ----------
# 只覆盖本次生成的文件，绝不删整个目录。
# dao/ 下可能有手写的扩展文件（惯例 custom_*.go），那些不属于生成物，
# 删掉就是不可逆的代码丢失。
#
# 生成物中已删除的表不会自动清理——我们无法区分"表被删了"和
# "这个文件是手写的"，宁可留一个孤儿文件让人看见，也不要误删。
echo "==> 覆盖到 hub/internal（只覆盖生成物，保留手写文件）"
COPIED=0
for sub in dao model/do model/entity; do
	src="$WORK/internal/$sub"
	[ -d "$src" ] || continue
	dst="$HUB_DIR/internal/$sub"
	mkdir -p "$dst"
	while IFS= read -r f; do
		rel="${f#"$src"/}"
		mkdir -p "$dst/$(dirname "$rel")"
		cp -f "$f" "$dst/$rel"
		COPIED=$((COPIED + 1))
	done < <(find "$src" -name '*.go' -type f)
done
echo "==> 覆盖 $COPIED 个生成文件（其余文件保持原样）"

echo "==> 完成。文件数：$(find "$HUB_DIR/internal/dao" "$HUB_DIR/internal/model/do" "$HUB_DIR/internal/model/entity" -name '*.go' | wc -l)"
