import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, call, patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("demo", ROOT / "scripts/demo.py")
demo = importlib.util.module_from_spec(spec)
spec.loader.exec_module(demo)


class DemoTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.protection_path = self.root / "protected-simulators.json"
        protection_patch = patch.object(demo, "PROTECTED_SIMULATORS_PATH", self.protection_path)
        protection_patch.start()
        self.addCleanup(protection_patch.stop)
        self.state = self.root / "lab"
        self.profile = demo.assets("profiles")["iphone-17-pro"]
        self.udid = "8112DCC0-F538-40AF-8647-ABFA50D143EB"
        self.record = {"owner": "trill-demo-v1", "profile": self.profile["id"], "udid": self.udid,
                       "runtime": self.profile["runtime"], "name": "Trill Demo - test"}
        self.device = {"udid": self.udid, "name": self.record["name"], "isAvailable": True,
                       "deviceTypeIdentifier": self.profile["device_type"], "state": "Booted"}
        self.inventory = {"devices": {self.profile["runtime"]: [self.device]}, "runtimes": [], "devicetypes": []}
        self.app = self.root / "Trill.app"
        self.app.mkdir()
        (self.app / "Trill").write_bytes(b"native app binary")
        (self.app / "Info.plist").write_bytes(plistlib.dumps({"CFBundleIdentifier": demo.BUNDLE,
                                                            "CFBundleExecutable": "Trill"}))

    def tearDown(self):
        self.tmp.cleanup()

    def test_builtin_assets_validate(self):
        for profile in demo.assets("profiles").values():
            demo.validate_profile(profile)
        for scenario in demo.assets("scenarios").values():
            demo.validate_scenario(scenario)

    def test_profile_orientation_is_optional_and_validated(self):
        demo.validate_profile(self.profile)
        for value in ('PORTRAIT', 'PORTRAIT_UPSIDE_DOWN', 'LANDSCAPE_LEFT', 'LANDSCAPE_RIGHT'):
            demo.validate_profile(dict(self.profile, orientation=value))
        for value in (None, '', 'portrait', 'LANDSCAPE', 1, []):
            with self.subTest(value=value), self.assertRaisesRegex(demo.DemoError, 'orientation'):
                demo.validate_profile(dict(self.profile, orientation=value))
        self.assertEqual(demo.assets('profiles')['iphone-duo']['orientation'], 'PORTRAIT')
        self.assertNotIn('orientation', demo.assets('profiles')['iphone-17-pro'])

    def test_prepare_applies_explicit_rotation_before_hinge_only_when_requested(self):
        for orientation in (None, 'PORTRAIT'):
            with self.subTest(orientation=orientation):
                profile = dict(self.profile, poses=[{'id': 'cover', 'hinge': 0}])
                if orientation:
                    profile['orientation'] = orientation
                runner = demo.runner_state(self.state)
                runner.mkdir(exist_ok=True)
                demo.write_json(runner / 'devices.json', {profile['id']: self.record})
                self.inventory['runtimes'] = [{'identifier': profile['runtime'], 'isAvailable': True}]
                self.inventory['devicetypes'] = [{'identifier': profile['device_type']}]
                commands = Mock()
                commands.inventory.return_value = self.inventory
                args = demo.parser().parse_args(['prepare', '--app', str(self.app)])
                with patch.object(demo, 'lock', return_value=contextlib.nullcontext()):
                    demo.prepare(commands, args, profile, self.state)
                expected = [call(self.udid, 'install', str(self.app.resolve()), timeout=180)]
                if orientation:
                    expected.append(call(self.udid, 'rotation', orientation))
                expected += [call(self.udid, 'hinge', '0'),
                             call(self.udid, 'launch', '--foreground-if-running', demo.BUNDLE)]
                self.assertEqual(commands.idb.call_args_list, expected)

    def test_pose_rotation_precedes_hinge_and_preserves_exact_dimensions(self):
        for orientation in (None, 'PORTRAIT'):
            with self.subTest(orientation=orientation):
                profile = dict(self.profile)
                if orientation:
                    profile['orientation'] = orientation
                args = demo.parser().parse_args(['capture', '--no-ui-quiet'])
                ui = demo.UI(Mock(), self.udid, args, profile, self.record, self.root)
                ui.map = {'poses': {'cover': {'pixels': [1398, 2034]}}}
                ui.screen = lambda: contextlib.nullcontext(self.root / 'screen.png')
                # Landscape frames must not count as settled portrait frames.
                sizes = [[2034, 1398]] * 3 + [[1398, 2034]] * 3
                with patch.object(demo, 'png_size', side_effect=sizes) as size, patch.object(demo.time, 'sleep'):
                    self.assertEqual(ui.pose_ready({'id': 'cover', 'hinge': 0}), [1398, 2034])
                self.assertEqual(size.call_count, 6)
                expected = [call(self.udid, 'rotation', orientation)] if orientation else []
                expected.append(call(self.udid, 'hinge', '0'))
                self.assertEqual(ui.c.idb.call_args_list, expected)

    def test_dry_run_all_has_no_commands_or_state_writes(self):
        with patch.object(demo.subprocess, "run", side_effect=AssertionError("command executed")), contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(demo.main(["run", "--profile", "all", "--app", str(self.app), "--state-dir", str(self.state), "--dry-run"]), 0)
        self.assertFalse(self.state.exists())
        self.assertFalse(demo.runner_state(self.state).exists())
        plan = json.loads(out.getvalue())
        self.assertTrue(plan["sequential"])
        self.assertEqual(len(plan["profiles"]), 2)

    def test_protected_ids_always_refused_even_with_ownership(self):
        self.protection_path.write_text(json.dumps([self.udid.lower()]))
        with self.assertRaisesRegex(demo.DemoError, "protected"):
            demo.verify_owned(self.record, self.profile, self.inventory)

    def test_malformed_protection_config_blocks_before_commands(self):
        self.protection_path.write_text('{"invalid": true}')
        commands = Mock()
        args = demo.parser().parse_args(["prepare", "--app", str(self.app)])
        with self.assertRaisesRegex(demo.DemoError, "protected simulator configuration"):
            demo.prepare(commands, args, self.profile, self.state)
        self.assertEqual(commands.mock_calls, [])

    def test_owned_device_is_explicit(self):
        self.assertEqual(demo.verify_owned(self.record, self.profile, self.inventory)[0], self.udid)
        with self.assertRaises(demo.DemoError):
            demo.verify_owned(self.record, self.profile, self.inventory, "unowned")

    def test_ownership_checks_name_type_runtime_and_availability(self):
        for key, value in [("name", "personal phone"), ("deviceTypeIdentifier", "other"), ("isAvailable", False)]:
            original = self.device[key]
            self.device[key] = value
            with self.assertRaises(demo.DemoError):
                demo.verify_owned(self.record, self.profile, self.inventory)
            self.device[key] = original
        self.record["runtime"] = "another runtime"
        with self.assertRaises(demo.DemoError):
            demo.verify_owned(self.record, self.profile, self.inventory)

    def test_cannot_adopt_existing_simulator(self):
        commands = Mock()
        self.inventory["runtimes"] = [{"identifier": self.profile["runtime"], "isAvailable": True}]
        self.inventory["devicetypes"] = [{"identifier": self.profile["device_type"]}]
        commands.inventory.return_value = self.inventory
        args = demo.parser().parse_args(["prepare", "--app", str(self.app), "--udid", self.udid])
        with self.assertRaisesRegex(demo.DemoError, "cannot adopt"):
            demo.prepare(commands, args, self.profile, self.state)
        commands.simctl.assert_not_called()

    def test_create_rejects_preexisting_or_protected_id_before_recording_ownership(self):
        for protected in (False, True):
            with self.subTest(protected=protected):
                self.inventory["runtimes"] = [{"identifier": self.profile["runtime"], "isAvailable": True}]
                self.inventory["devicetypes"] = [{"identifier": self.profile["device_type"]}]
                self.inventory["devices"][self.profile["runtime"]] = [] if protected else [self.device]
                if protected:
                    self.protection_path.write_text(json.dumps([self.udid]))
                commands = Mock()
                commands.inventory.return_value = self.inventory
                commands.simctl.return_value = subprocess.CompletedProcess([], 0, self.udid, "")
                args = demo.parser().parse_args(["prepare", "--app", str(self.app)])
                with self.assertRaisesRegex(demo.DemoError, "existing/protected"):
                    demo.prepare(commands, args, self.profile, self.state)
                commands.simctl.assert_called_once()
                commands.idb.assert_not_called()
                self.assertFalse((demo.runner_state(self.state) / "devices.json").exists())

    def test_runner_registry_is_separate_from_private_lab_devices(self):
        self.state.mkdir()
        (self.state / "devices.json").write_text('{"credential":"private"}')
        runner = demo.runner_state(self.state)
        runner.mkdir()
        demo.write_json(runner / "devices.json", self.record)
        self.assertEqual((self.state / "devices.json").read_text(), '{"credential":"private"}')
        self.assertEqual((runner / "devices.json").stat().st_mode & 0o777, 0o600)

    def test_command_environment_removes_implicit_personal_target(self):
        with patch.dict(os.environ, {"IDB_UDID": "personal", "IDB_COMPANION": "personal:123"}):
            commands = demo.Commands(self.root)
        self.assertNotIn("IDB_UDID", commands.env)
        self.assertNotIn("IDB_COMPANION", commands.env)
        argv = commands.idb_args(self.udid, "screenshot", "out.png")
        self.assertEqual(argv[:4], ["mise", "exec", "--", "idb"])
        self.assertEqual(argv[argv.index("--udid") + 1], self.udid)

    def test_command_errors_do_not_expose_secret_output_or_argv(self):
        commands = demo.Commands(self.root)
        secret = "trill://pair?data=DO_NOT_PRINT"
        with patch.object(demo.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, secret, secret)):
            with self.assertRaises(demo.DemoError) as error:
                commands.call(["mise", "exec", "--", "idb", "open", secret])
        self.assertNotIn("DO_NOT_PRINT", str(error.exception))

    def test_invalid_app_bundle_rejected(self):
        (self.app / "Info.plist").write_bytes(plistlib.dumps({"CFBundleIdentifier": "personal", "CFBundleExecutable": "Trill"}))
        with self.assertRaises(demo.DemoError):
            demo.app_identity(self.app)

    def test_app_identity_detects_binary_change(self):
        before = demo.app_identity(self.app)
        (self.app / "Trill").write_bytes(b"changed")
        self.assertNotEqual(before, demo.app_identity(self.app))

    def test_scenario_rejects_path_traversal_and_secrets(self):
        for step in [{"op": "screenshot", "name": "../../secret"}, {"op": "pause", "seconds": float("nan")},
                     {"op": "tap", "target": "trill://pair?data=secret"}, {"op": "shell", "command": "anything"}]:
            with self.assertRaises(demo.DemoError):
                demo.validate_scenario({"id": "test", "initial_snapshot": "start", "steps": [step]})

    def test_public_lab_contract_rejects_credentials_and_remote_preview(self):
        lab = {"state_dir": "/private/lab", "address": "https://localhost:19443", "name": "Demo Mac", "session_name": "field-notes",
               "preview_url": "http://localhost:18080/index.html", "files_root": "/private/lab/project"}
        self.assertEqual(demo.read_lab(json.dumps(lab)), lab)
        for url in ["http://remote.example/index.html", "http://localhost/?token=secret", "http://user:secret@localhost/"]:
            lab["preview_url"] = url
            with self.assertRaises(demo.DemoError):
                demo.read_lab(json.dumps(lab))

    def test_recorder_sigint_finalizes_only_its_child(self):
        commands = demo.Commands(self.root)
        process = Mock()
        process.poll.return_value = None
        process.wait.return_value = 0
        args = demo.parser().parse_args(["capture"])
        output = self.root / "real.mp4"
        with patch.object(demo.subprocess, "Popen", return_value=process), patch.object(demo.time, "sleep"):
            with demo.record_video(commands, self.udid, output, args):
                output.write_bytes(b"real video" * 100)
        process.send_signal.assert_called_once_with(signal.SIGINT)
        process.kill.assert_not_called()

    def test_recorder_failure_never_passes_empty_video(self):
        commands = demo.Commands(self.root)
        process = Mock()
        process.poll.return_value = None
        process.wait.return_value = 1
        args = demo.parser().parse_args(["capture"])
        with patch.object(demo.subprocess, "Popen", return_value=process), patch.object(demo.time, "sleep"):
            with self.assertRaises(demo.DemoError):
                with demo.record_video(commands, self.udid, self.root / "empty.mp4", args):
                    pass

    def test_capture_gate_rejects_credentials_pairing_and_wrong_session(self):
        args = demo.parser().parse_args(["capture"])
        ui = demo.UI(Mock(), self.udid, args, self.profile, self.record, self.root)
        for text in ["", "Personal session", "Trill demo Pairing token", "Trill demo trill://pair?data=x", "Trill demo Bearer secret", "Trill demo " + "a" * 64]:
            with patch.object(ui, "text", return_value=text), self.assertRaises(demo.DemoError):
                ui.assert_safe()
        with patch.object(ui, "text", return_value="Trill demo Mosh · encrypted Field Notes"):
            ui.assert_safe()

    def test_mapped_terminal_readiness_requires_exact_enabled_native_surface(self):
        args = demo.parser().parse_args(["capture", "--timeout", "0.01"])
        ui = demo.UI(Mock(), self.udid, args, self.profile, self.record, self.root)
        ui.map = {"poses": {}}
        terminal = {"AXUniqueId": "remote-terminal", "enabled": True,
                    "frame": {"x": 0, "y": 0, "width": 382, "height": 570}}
        with patch.object(ui, "tree", return_value=[terminal]), patch.object(demo.time, "sleep"):
            self.assertEqual(ui.wait("remote-terminal", "AXUniqueId"), terminal)
        for invalid in (dict(terminal, AXUniqueId="remote-terminal-other"),
                        dict(terminal, enabled=False),
                        dict(terminal, frame={"width": 0, "height": 570})):
            with self.subTest(invalid=invalid), patch.object(ui, "tree", return_value=[invalid]), patch.object(demo.time, "sleep"):
                with self.assertRaisesRegex(demo.DemoError, "Timed out"):
                    ui.wait("remote-terminal", "AXUniqueId")

    def test_ui_map_is_bound_to_exact_native_app(self):
        args = demo.parser().parse_args(["capture", "--ui-map", str(self.root / "map.json")])
        demo.write_json(args.ui_map, {"profile": self.profile["id"], "runtime": self.profile["runtime"], "app_sha256": "wrong", "reviewed_by": "operator", "reviewed_at": "today"})
        self.record["app"] = demo.app_identity(self.app)
        with self.assertRaisesRegex(demo.DemoError, "exact profile"):
            demo.UI(Mock(), self.udid, args, self.profile, self.record, self.root)

    def test_dimension_mismatch_refuses_coordinate_input(self):
        args = demo.parser().parse_args(["capture"])
        ui = demo.UI(Mock(), self.udid, args, self.profile, self.record, self.root)
        ui.map = {"poses": {"portrait": {"pixels": [12, 34], "points": [4, 11]}}}
        with patch.object(demo, "png_size", return_value=[56, 78]), self.assertRaises(demo.DemoError):
            ui.map_pose(Path("screen.png"))
        ui.c.idb.assert_not_called()

    def test_invite_file_removed_on_native_pair_failure(self):
        commands = Mock()
        lab = {"state_dir": str(self.state), "address": "https://localhost:19443", "name": "Demo Mac", "session_name": "field-notes",
               "preview_url": "http://localhost:18080/index.html", "files_root": str(self.state / "project")}
        def lab_command(state, command, *args):
            if command == "start":
                return subprocess.CompletedProcess([], 0, json.dumps(lab), "")
            path = Path(args[1])
            self.assertFalse(path.exists())
            path.write_text("trill://pair?data=secret")
            path.chmod(0o600)
        commands.lab.side_effect = lab_command
        ui = Mock()
        ui.tap.side_effect = demo.DemoError("native Pair failed")
        with self.assertRaises(demo.DemoError):
            demo.pair(commands, ui, self.state, self.root)
        self.assertFalse((self.root / "invitation.url").exists())
        self.assertEqual(commands.idb.call_args.args[1], "open")

    def test_launch_target_flags_precede_argparse_remainder(self):
        argv = demo.Commands(self.root).idb_args(self.udid, "launch", "--foreground-if-running", demo.BUNDLE)
        self.assertLess(argv.index("--udid"), argv.index(demo.BUNDLE))

    def test_native_open_consent_uses_exact_live_label(self):
        args = demo.parser().parse_args(["capture"])
        commands = Mock()
        commands.idb.return_value = subprocess.CompletedProcess([], 0, json.dumps([
            {"AXLabel": "Open in Trill?", "frame": {"x": 0, "y": 0, "width": 100, "height": 10}},
            {"AXLabel": "Open", "frame": {"x": 20, "y": 40, "width": 60, "height": 20}}
        ]), "")
        ui = demo.UI(commands, self.udid, args, self.profile, self.record, self.root)
        ui.accept_open()
        self.assertEqual(commands.idb.call_args.args, (self.udid, "ui", "tap", "50", "50", "--duration", "0.1"))

    def test_both_appearance_and_all_devices_run_strictly_in_order(self):
        seen = []
        def capture(commands, args, profile, scenario, state, appearance):
            seen.append((profile["id"], appearance))
            return {"profile": profile["id"], "appearance": appearance}
        with patch.object(demo, "capture", side_effect=capture), patch.object(demo.shutil, "which", return_value="/tool"), contextlib.redirect_stdout(io.StringIO()):
            demo.main(["capture", "--profile", "all", "--state-dir", str(self.state)])
        self.assertEqual(seen, [("iphone-17-pro", "light"), ("iphone-17-pro", "dark"),
                                ("iphone-duo", "light"), ("iphone-duo", "dark")])

    def test_duo_composition_uses_real_segments_with_hard_cut(self):
        commands = Mock()
        output = self.root
        artifacts = [{"type": "video", "file": "cover.mp4", "width": 1080, "height": 2000},
                     {"type": "video", "file": "unfolded.mp4", "width": 2800, "height": 2000}]
        final = output / "dark-iphone-duo.mp4"
        def compose(argv, **kwargs):
            self.assertEqual(argv.count("-i"), 2)
            filters = argv[argv.index("-filter_complex") + 1]
            self.assertIn("concat=n=2:v=1:a=0", filters)
            self.assertIn("pad=2800:2000", filters)
            self.assertNotIn("xfade", filters)
            final.write_bytes(b"validated real segments")
        commands.call.side_effect = compose
        with patch.object(demo, "video_info", return_value={"duration_seconds": 8}):
            result = demo.compose_video(commands, output, artifacts, "dark", "iphone-duo", 30)
        self.assertEqual(result["sources"], ["cover.mp4", "unfolded.mp4"])
        self.assertEqual(result["appearance"], "dark")

    def test_failure_after_screenshot_discards_all_imagery(self):
        args = demo.parser().parse_args(["capture", "--screenshots-only", "--hold", "0"])
        args.output = self.root / "artifacts"
        runner = demo.runner_state(self.state)
        runner.mkdir()
        self.record["app"] = demo.app_identity(self.app)
        demo.write_json(runner / "devices.json", {self.profile["id"]: self.record})
        self.inventory["runtimes"] = [{"identifier": self.profile["runtime"], "buildversion": "test"}]
        commands = Mock()
        commands.inventory.return_value = self.inventory
        commands.call.return_value = subprocess.CompletedProcess([], 0, "commit", "")
        commands.simctl.return_value = subprocess.CompletedProcess([], 0, "light", "")
        commands.events = []
        lock_state = {"held": False, "cleanup_checked": False}
        @contextlib.contextmanager
        def owned_lock(path):
            lock_state["held"] = True
            try:
                yield
            finally:
                lock_state["held"] = False
        def idb(udid, command, *args, **kwargs):
            if command == "terminate":
                self.assertTrue(lock_state["held"])
                lock_state["cleanup_checked"] = True
        commands.idb.side_effect = idb
        ui = Mock(unsafe=True)
        checks = [None, demo.DemoError("gate failed")]
        ui.assert_safe.side_effect = checks
        ui.screen.return_value = contextlib.nullcontext(self.root / "screen.png")
        def screenshot(dest):
            dest.write_bytes(b"private unverified image")
            return [100, 200]
        ui.screenshot.side_effect = screenshot
        with patch.object(demo, "lock", side_effect=owned_lock), patch.object(demo, "UI", return_value=ui), patch.object(demo, "pair", return_value={"preview_url": "http://localhost/"}), contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(demo.DemoError):
                demo.capture(commands, args, self.profile, demo.assets("scenarios")["terminal"], self.state, "light")
        manifests = list(args.output.rglob("manifest.json"))
        self.assertEqual(len(manifests), 1)
        manifest = json.loads(manifests[0].read_text())
        self.assertEqual(manifest["status"], "failed")
        self.assertEqual(manifest["artifacts"], [])
        self.assertFalse(list(args.output.rglob("*.png")))
        self.assertTrue(lock_state["cleanup_checked"])

    def test_ax_identity_never_skips_supplied_pixel_secret_scan(self):
        args = demo.parser().parse_args(["capture"])
        ui = demo.UI(Mock(), self.udid, args, self.profile, self.record, self.root)
        with patch.object(ui, "text", return_value="field-notes Mosh encrypted"), patch.object(ui, "vision_text", return_value="Bearer secret") as ocr:
            with self.assertRaises(demo.DemoError):
                ui.assert_safe(self.root / "screen.png")
        ocr.assert_called_once_with(self.root / "screen.png")

    def test_lock_excludes_concurrent_runner(self):
        path = self.root / "runner.lock"
        with demo.lock(path):
            with self.assertRaisesRegex(demo.DemoError, "busy"):
                with demo.lock(path):
                    pass


if __name__ == "__main__":
    unittest.main()
