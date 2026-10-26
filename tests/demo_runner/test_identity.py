import unittest
import json
import contextlib
import subprocess
from pathlib import Path
from unittest.mock import Mock, patch

from test_demo import demo


class IdentityAliasTests(unittest.TestCase):
    def ui(self, *, mapped=True, session="field-notes"):
        profile = demo.assets("profiles")["iphone-17-pro"]
        args = demo.parser().parse_args(["capture"])
        commands = Mock()
        record = {"app": {"executable_sha256": "pinned-demo-build"}}
        mapping = {
            "profile": profile["id"],
            "runtime": profile["runtime"],
            "app_sha256": record["app"]["executable_sha256"],
            "reviewed_by": "independent-review-test",
            "reviewed_at": "2026-10-24",
            "identity_ocr_aliases": {"field-notes": ["fiela-notes"]},
        }
        if mapped:
            args.ui_map = Path("reviewed-map.json")
        with patch.object(demo, "read_json", return_value=mapping):
            ui = demo.UI(commands, "owned-simulator", args, profile, record, Path("unused-private"))
        ui.demo_session = session
        ui.text = Mock(return_value="fiela-notes\nMosh encrypted")
        ui.vision_text = Mock(return_value="fiela-notes\nMosh encrypted")
        commands.reset_mock()
        return ui

    def test_reviewed_alias_accepts_observed_title_for_actual_session(self):
        ui = self.ui()
        ui.assert_safe(Path("supplied-screen.png"))
        ui.c.idb.assert_not_called()

    def test_alias_requires_a_reviewed_map(self):
        ui = self.ui(mapped=False)
        with self.assertRaisesRegex(demo.DemoError, "cannot identify"):
            ui.assert_safe(Path("supplied-screen.png"))

    def test_alias_for_another_session_is_not_reused(self):
        ui = self.ui(session="another-demo")
        with self.assertRaisesRegex(demo.DemoError, "cannot identify"):
            ui.assert_safe(Path("supplied-screen.png"))

    def test_unlisted_ocr_variant_and_wrong_session_are_rejected(self):
        for visible in ("fie1a-notes\nMosh encrypted", "personal-session\nMosh encrypted"):
            with self.subTest(visible=visible):
                ui = self.ui()
                ui.text.return_value = visible
                with self.assertRaisesRegex(demo.DemoError, "cannot identify"):
                    ui.assert_safe(Path("supplied-screen.png"))

    def test_alias_does_not_match_inside_another_session_title(self):
        ui = self.ui()
        ui.text.return_value = "personal-fiela-notes-backup\nMosh encrypted"
        with self.assertRaisesRegex(demo.DemoError, "cannot identify"):
            ui.assert_safe(Path("supplied-screen.png"))

    def test_credentials_are_rejected_before_accepting_alias(self):
        for secret in ("Bearer private", "trill://pair?data=private", "a" * 64):
            with self.subTest(secret_type=secret.split()[0]):
                ui = self.ui()
                ui.text.return_value = "fiela-notes\n" + secret
                with self.assertRaisesRegex(demo.DemoError, "Capture gate rejected"):
                    ui.assert_safe(Path("supplied-screen.png"))

    def test_pairing_and_consent_screens_still_fail_with_alias(self):
        for forbidden in demo.FORBIDDEN:
            with self.subTest(forbidden=forbidden):
                ui = self.ui()
                ui.text.return_value = "fiela-notes\n" + forbidden
                with self.assertRaisesRegex(demo.DemoError, "Capture gate rejected"):
                    ui.assert_safe(Path("supplied-screen.png"))

    def test_native_heading_fallback_requires_exact_enabled_heading(self):
        for role, label, enabled, accepted in [('AXHeading', 'field-notes', True, True), ('AXHeading', 'personal-session', True, False), ('AXButton', 'field-notes', True, False), ('AXHeading', 'field-notes', False, False)]:
            ui = self.ui()
            ui.pose = 'cover'
            ui.map['poses'] = {'cover': {'points': [466, 678], 'identity_heading_point': [80, 48]}}
            ui.text.return_value = 'unrecognized title\nMosh encrypted'
            ui.c.idb.return_value = subprocess.CompletedProcess([], 0, json.dumps({'role': role, 'AXLabel': label, 'enabled': enabled}), '')
            if accepted:
                ui.assert_safe(Path('screen.png'))
            else:
                with self.assertRaises(demo.DemoError): ui.assert_safe(Path('screen.png'))
            self.assertEqual(ui.c.idb.call_args.args[1:3], ('ui', 'describe-point'))

    def test_native_heading_never_bypasses_pixel_secret_scan(self):
        ui = self.ui()
        ui.pose = 'cover'
        ui.map['poses'] = {'cover': {'points': [466, 678], 'identity_heading_point': [80, 48]}}
        ui.text.return_value = 'unrecognized title\nBearer private'
        with self.assertRaises(demo.DemoError): ui.assert_safe(Path('screen.png'))
        ui.c.idb.assert_not_called()

    def test_reviewed_native_press_avoids_stale_hid_geometry(self):
        ui = self.ui()
        ui.args.ui_quiet = False
        ui.pose = 'unfolded'
        ui.wait = Mock()
        ui.screen = lambda: contextlib.nullcontext(Path('screen.png'))
        pose = {'points': [951, 669], 'targets': {'session-workspace': {'tap': [903, 196], 'api': 'ax-coresimulator', 'expected_id': 'session-workspace'}}}
        ui.map_pose = Mock(return_value=pose)
        ui.tap('session-workspace', 'AXUniqueId')
        ui.c.idb.assert_called_once_with(ui.udid, 'ui', 'tap', '903', '196', '--api', 'ax-coresimulator', '--expected-key', 'AXUniqueId', '--expected-value', 'session-workspace')
        ui.c.idb.reset_mock()
        pose['targets']['session-workspace']['api'] = 'unknown'
        with self.assertRaises(demo.DemoError): ui.tap('session-workspace', 'AXUniqueId')
        ui.c.idb.assert_not_called()

    def test_supplied_pixels_are_scanned_even_with_valid_ax_identity(self):
        ui = self.ui(mapped=False)
        ui.text.return_value = "field-notes\nMosh encrypted"
        ui.vision_text.return_value = "Bearer private"
        with self.assertRaisesRegex(demo.DemoError, "Capture gate rejected"):
            ui.assert_safe(Path("supplied-screen.png"))
        ui.vision_text.assert_called_once_with(Path("supplied-screen.png"))


if __name__ == "__main__":
    unittest.main()
