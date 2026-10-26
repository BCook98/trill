"""Toolchain routing checks with fake commands; never invoke Xcode or simctl."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("toolchain_demo", ROOT / "scripts/demo.py")
demo = importlib.util.module_from_spec(spec)
spec.loader.exec_module(demo)


class ToolchainTests(unittest.TestCase):
    def test_demo_preserves_explicit_xcode_and_removes_personal_idb_routing(self):
        environment = {"PATH": "/usr/bin:/bin", "DEVELOPER_DIR": "/custom/Xcode.app/Contents/Developer",
                       "IDB_UDID": "personal", "IDB_COMPANION": "personal", "IDB_COMPANION_TLS": "personal"}
        with patch.dict(os.environ, environment, clear=True):
            commands = demo.Commands(ROOT)
        self.assertEqual(commands.env["DEVELOPER_DIR"], environment["DEVELOPER_DIR"])
        self.assertFalse(any(key.startswith("IDB_") for key in commands.env))
        with patch.object(demo.subprocess, "run", return_value=Mock(returncode=0)) as run:
            commands.simctl("list", "-j")
        self.assertEqual(run.call_args.kwargs["env"]["DEVELOPER_DIR"], environment["DEVELOPER_DIR"])

    def test_demo_leaves_xcode_select_in_control_when_no_override_is_set(self):
        with patch.dict(os.environ, {"PATH": "/usr/bin:/bin"}, clear=True):
            commands = demo.Commands(ROOT)
        self.assertNotIn("DEVELOPER_DIR", commands.env)
        with patch.object(demo.subprocess, "run", return_value=Mock(returncode=0)) as run:
            commands.simctl("list", "-j")
        self.assertNotIn("DEVELOPER_DIR", run.call_args.kwargs["env"])

    def build(self, sdk, developer_dir=None):
        with tempfile.TemporaryDirectory() as value:
            folder = Path(value)
            (folder / "xcrun").write_text('#!/bin/sh\nprintf "%s\\n" "$FAKE_SDK"\n')
            (folder / "xcodebuild").write_text('#!/bin/sh\nprintf "%s\\n" "${DEVELOPER_DIR-unset}" > "$BUILD_LOG"\n')
            for name in ("xcrun", "xcodebuild"):
                (folder / name).chmod(0o700)
            log = folder / "build.log"
            environment = {"PATH": str(folder) + ":/usr/bin:/bin", "FAKE_SDK": sdk, "BUILD_LOG": str(log)}
            if developer_dir is not None:
                environment["DEVELOPER_DIR"] = developer_dir
            result = subprocess.run(["/bin/sh", str(ROOT / "scripts/build-ios.sh")],
                                    env=environment, capture_output=True, text=True)
            return result, log.read_text().strip() if log.exists() else None

    def test_build_inherits_selected_toolchain_without_private_path_fallback(self):
        for selected in (None, "/custom/Xcode.app/Contents/Developer"):
            with self.subTest(selected=selected):
                result, actual = self.build("27.1", selected)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(actual, selected if selected is not None else "unset")

    def test_build_rejects_older_sdk_before_calling_xcodebuild(self):
        for sdk in ("18.5", "26.0", "27.0"):
            with self.subTest(sdk=sdk):
                result, actual = self.build(sdk)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("iOS 27.1 SDK", result.stderr)
                self.assertIsNone(actual)
        result, _ = self.build("28.0")
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
