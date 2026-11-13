#!/usr/bin/env python3
"""隔离验证补丁递增，绝不修改仓库的真实 VERSION。"""
import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("bump-version.py")
spec = importlib.util.spec_from_file_location("version_script", SCRIPT)
version = importlib.util.module_from_spec(spec)
spec.loader.exec_module(version)


class VersionTests(unittest.TestCase):
    def test_format(self):
        self.assertEqual(version.parse_version("0.0.1\n"), (0, 0, 1))
        for invalid in ("v0.0.1", "0.1", "01.0.1", "0.0.-1", "0.0.1;echo bad"):
            with self.assertRaises(ValueError):
                version.parse_version(invalid)

    def test_isolated_increment_and_check(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / "scripts"
            scripts.mkdir()
            script = scripts / SCRIPT.name
            script.write_bytes(SCRIPT.read_bytes())
            target = root / "VERSION"
            target.write_text("0.0.9\n", encoding="utf-8")
            result = subprocess.run([sys.executable, str(script), "--check"], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(target.read_text(), "0.0.9\n")
            result = subprocess.run([sys.executable, str(script)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(target.read_text(), "0.0.10\n")
            target.write_text("invalid\n", encoding="utf-8")
            result = subprocess.run([sys.executable, str(script)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(target.read_text(), "invalid\n")


if __name__ == "__main__":
    unittest.main()
