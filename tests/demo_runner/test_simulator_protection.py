"""Mock-only tests for optional private simulator protection configuration."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "simulator_protection", Path(__file__).resolve().parents[2] / "scripts/simulator_protection.py")
protection = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(protection)


class ProtectionTests(unittest.TestCase):
    def setUp(self):
        temporary_directory = tempfile.TemporaryDirectory()
        self.addCleanup(temporary_directory.cleanup)
        self.configuration_path = Path(temporary_directory.name) / "protected-simulators.json"

    def load(self):
        return protection.load_protected_simulator_ids(self.configuration_path, RuntimeError)

    def test_fresh_checkout_needs_no_configuration(self):
        self.assertEqual(self.load(), set())

    def test_configuration_normalizes_uuid_case(self):
        simulator_id = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
        self.configuration_path.write_text(json.dumps([simulator_id.lower(), simulator_id]))
        self.assertEqual(self.load(), {simulator_id})

    def test_malformed_configuration_fails_fast(self):
        for contents in ('invalid json', '{}', 'null', '[null]', '[123]', '["invalid"]',
                         '["11111111111141118111111111111111"]'):
            with self.subTest(contents=contents):
                self.configuration_path.write_text(contents)
                with self.assertRaisesRegex(RuntimeError, "protected simulator configuration"):
                    self.load()

    def test_unreadable_configuration_fails_fast(self):
        with patch.object(Path, "read_text", side_effect=PermissionError("private")):
            with self.assertRaisesRegex(RuntimeError, "protected simulator configuration"):
                self.load()

    def test_broken_symlink_is_not_optional_absence(self):
        self.configuration_path.symlink_to(self.configuration_path.with_name("missing.json"))
        with self.assertRaisesRegex(RuntimeError, "protected simulator configuration"):
            self.load()


if __name__ == "__main__":
    unittest.main()
