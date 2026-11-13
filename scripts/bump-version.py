#!/usr/bin/env python3
"""在发布提交前递增根目录 VERSION 的补丁号；--check 仅校验并输出。"""
import argparse
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]


def parse_version(text):
    value = text.strip()
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", value):
        raise ValueError("VERSION 必须为不带前缀的三段非负整数，例如 0.0.1")
    return tuple(int(part) for part in value.split("."))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="仅校验，不递增")
    args = parser.parse_args()
    path = ROOT / "VERSION"
    try:
        major, minor, patch = parse_version(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        parser.exit(1, f"版本读取失败：{error}\n")
    if args.check:
        print(f"{major}.{minor}.{patch}")
        return
    value = f"{major}.{minor}.{patch + 1}"
    path.write_text(value + "\n", encoding="utf-8")
    print(value)


if __name__ == "__main__":
    main()
