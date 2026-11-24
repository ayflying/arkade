#!/usr/bin/env python3
"""维护「已发布迁移」的内容指纹清单。

背景：迁移一旦发布，数据库里就记下了它的 SHA256（arkade_migrate_checksums）。
之后再改迁移文件内容，存量实例启动时会校验失败并拒绝启动：

    checksum mismatch for migration vN

2026-11-24 的线上事故就是这么来的：v1 的 seed 从 90 改成 30，所有从旧版本
升级的实例全部进入崩溃循环。

规则：
- 已发布迁移只增不改。要改行为，请新增一个更高版本号的迁移。
- 新增迁移后运行本脚本，把新版本追加进 released.lock。
- 本脚本拒绝改写已存在条目的指纹；--force 只用于修复误登记。
"""

from __future__ import annotations

import hashlib
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MIGRATIONS = ROOT / "hub" / "internal" / "dbmigrate" / "migrations"
LOCK = MIGRATIONS / "released.lock"
HEADER = (
    "# 已发布迁移的内容指纹（SHA256，按 LF 计算）。只增不改，"
    "由 scripts/lock-migrations.py 维护。"
)


def checksum(path: Path) -> str:
    """按 LF 计算指纹。

    Windows 检出是 CRLF、CI 是 LF，而生产镜像在 Linux 构建；统一按 LF 计算，
    本地与 CI 才能给出同一个结论。
    """
    data = path.read_bytes().replace(b"\r\n", b"\n")
    return hashlib.sha256(data).hexdigest()


def migrations() -> list[tuple[int, Path]]:
    found = []
    for path in MIGRATIONS.glob("*.up.sql"):
        prefix = path.name.split("_", 1)[0]
        found.append((int(prefix), path))
    return sorted(found)


def read_lock() -> dict[int, str]:
    if not LOCK.exists():
        return {}
    locked: dict[int, str] = {}
    for line in LOCK.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        version, _, digest = line.partition(" ")
        locked[int(version)] = digest.strip()
    return locked


def main() -> int:
    force = "--force" in sys.argv[1:]
    locked = read_lock()
    current = migrations()
    known = {version for version, _ in current}

    removed = sorted(set(locked) - known)
    if removed:
        print(
            "指纹清单里有不存在的迁移版本: " + ", ".join(f"v{v}" for v in removed) + "\n"
            "已发布的迁移不能删除或改名。",
            file=sys.stderr,
        )
        return 1

    lines = [HEADER]
    added = []
    for version, path in current:
        digest = checksum(path)
        previous = locked.get(version)
        if previous is not None and previous != digest and not force:
            print(
                f"已发布迁移 {path.name} 的内容变了：\n"
                f"  清单: {previous}\n"
                f"  当前: {digest}\n"
                "已发布的迁移不能修改——数据库里记着旧指纹，改了会让存量实例启动报 "
                "checksum mismatch。\n要改行为请新增一个版本号；确属误登记才加 --force。",
                file=sys.stderr,
            )
            return 1
        if previous is None:
            added.append(path.name)
        lines.append(f"{version:06d} {digest}")

    LOCK.write_text("\n".join(lines) + "\n", encoding="utf-8")
    if added:
        print("已登记: " + ", ".join(added))
    else:
        print("指纹清单已是最新")
    return 0


if __name__ == "__main__":
    sys.exit(main())
