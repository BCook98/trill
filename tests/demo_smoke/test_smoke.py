"""Mock-only harness contract tests. These are never live app E2E evidence."""
import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch
import uuid

SPEC = importlib.util.spec_from_file_location("demo_smoke", Path(__file__).resolve().parents[2] / "scripts/demo-smoke.py")
smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(smoke)


def status(**overrides):
    value = {"kind": "trill-demo-v1", "running": True, "address": "https://localhost:19443", "profile": "mixed",
             "fingerprint": "a" * 64, "previewPort": 18080, "pid": 12345,
             "state": {"sessions": [{"id": "demo-xirp-1", "name": "Demo terminal", "provider": "xirp", "projectId": "demo-project",
                                     "attachable": True, "archived": False, "heading": "initial", "status": "running"}],
                       "projects": [{"id": "demo-project", "name": "Demo project"}], "uploads": []}}
    value.update(overrides)
    return value


def element(identifier="terminal-draft", value="", role="AXTextArea", label="Terminal draft", **overrides):
    result = {"AXUniqueId": identifier, "AXLabel": label, "AXValue": value, "enabled": True, "role": role,
              "frame": {"x": 20, "y": 40, "width": 80, "height": 60}}
    result.update(overrides)
    return result


class Temporary(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()
        self.protection_path = self.base / "protected-simulators.json"
        protection_patch = patch.object(smoke, "PROTECTED_SIMULATORS_PATH", self.protection_path)
        protection_patch.start()
        self.addCleanup(protection_patch.stop)
        self.root = smoke.initialize(self.base / "smoke")
        self.marker = smoke.read_json(self.root / "owner.json")


class OwnershipTests(Temporary):
    def test_init_cannot_adopt_existing_state(self):
        with self.assertRaises(smoke.Blocked):
            smoke.initialize(self.root)
        self.assertEqual(smoke.owned(self.root)[1], self.marker)

    def test_private_marker_and_permissions_required(self):
        self.root.chmod(0o755)
        with self.assertRaises(smoke.Blocked):
            smoke.owned(self.root)
        self.root.chmod(0o700)
        smoke.write_json(self.root / "owner.json", {**self.marker, "kind": "another-tool"})
        with self.assertRaises(smoke.Blocked):
            smoke.owned(self.root)

    def test_symlink_state_rejected(self):
        link = self.base / "alias"
        link.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(smoke.Blocked):
            smoke.owned(link)

    def test_relative_paths_rejected(self):
        with self.assertRaises(smoke.Blocked):
            smoke.absolute(Path("relative"))

    def test_lock_rejects_concurrent_runner(self):
        with smoke.locked(self.root / "runner.lock"):
            with self.assertRaises(smoke.Blocked):
                with smoke.locked(self.root / "runner.lock"):
                    self.fail("second lock acquired")

    def record(self):
        return {"kind": smoke.OWNER, "owner": self.marker["id"], "udid": str(uuid.uuid4()).upper(), "name": "Trill Smoke owned",
                "runtime": "ios-test", "device_type": "iphone-test"}

    def inventory(self, record):
        return {"devices": {"ios-test": [{"udid": record["udid"], "name": record["name"], "isAvailable": True,
                                          "deviceTypeIdentifier": record["device_type"]}]}}

    def test_simulator_identity_requires_all_ownership_fields(self):
        record = self.record()
        inventory = self.inventory(record)
        self.assertEqual(smoke.verify_simulator(record, inventory, self.marker["id"])["name"], record["name"])
        for key in ("owner", "kind", "name", "runtime", "device_type"):
            with self.subTest(key=key), self.assertRaises(smoke.Blocked):
                smoke.verify_simulator({**record, key: "wrong"}, inventory, self.marker["id"])

    def test_protected_simulator_never_adopted(self):
        record = {**self.record(), "udid": "11111111-1111-4111-8111-111111111111"}
        self.protection_path.write_text(json.dumps([record["udid"].lower()]))
        with self.assertRaisesRegex(smoke.Blocked, "Protected"):
            smoke.verify_simulator(record, self.inventory(record), self.marker["id"])

    def test_malformed_protection_config_blocks_before_commands(self):
        self.protection_path.write_text('["invalid-id"]')
        commands = Mock()
        with self.assertRaisesRegex(smoke.Blocked, "protected simulator configuration"):
            smoke.create_simulator(commands, self.root, self.marker, "iphone-17-pro", None)
        self.assertEqual(commands.mock_calls, [])

    def test_protected_idb_target_rejected_even_when_allowed(self):
        simulator_id = "11111111-1111-4111-8111-111111111111"
        self.protection_path.write_text(json.dumps([simulator_id]))
        commands = smoke.Commands()
        commands.allowed.add(simulator_id)
        with patch.object(commands, "call") as call:
            with self.assertRaisesRegex(smoke.Blocked, "owned simulator"):
                commands.idb(simulator_id, "launch", smoke.BUNDLE)
            call.assert_not_called()

    def test_create_rejects_protected_id_before_recording_ownership(self):
        profile = "iphone-17-pro"
        device_type, runtime = smoke.PROFILES[profile]
        simulator_id = "11111111-1111-4111-8111-111111111111"
        self.protection_path.write_text(json.dumps([simulator_id]))
        commands = Mock()
        commands.inventory.return_value = {"runtimes": [{"identifier": runtime, "isAvailable": True}],
                                           "devicetypes": [{"identifier": device_type}], "devices": {}}
        commands.simctl.return_value = simulator_id
        with self.assertRaisesRegex(smoke.Blocked, "existing/protected"):
            smoke.create_simulator(commands, self.root, self.marker, profile, None)
        commands.simctl.assert_called_once()
        commands.idb.assert_not_called()
        self.assertEqual(smoke.read_json(self.root / "simulators.json"), [])

    def test_create_rejects_any_preexisting_id_before_boot(self):
        profile = "iphone-17-pro"
        kind, runtime = smoke.PROFILES[profile]
        udid = str(uuid.uuid4()).upper()
        commands = Mock()
        commands.inventory.return_value = {"runtimes": [{"identifier": runtime, "isAvailable": True}],
                                           "devicetypes": [{"identifier": kind}], "devices": {runtime: [{"udid": udid}]}}
        commands.simctl.return_value = udid
        with self.assertRaises(smoke.Blocked):
            smoke.create_simulator(commands, self.root, self.marker, profile, None)
        commands.simctl.assert_called_once()
        self.assertEqual(smoke.read_json(self.root / "simulators.json"), [])

    def test_idb_target_gate_precedes_subprocess(self):
        commands = smoke.Commands()
        with patch.object(commands, "call") as call:
            with self.assertRaises(smoke.Blocked):
                commands.idb("personal-id", "launch", smoke.BUNDLE)
            call.assert_not_called()

    def test_backend_requires_own_slot_not_arbitrary_demo(self):
        binary = self.base / "trill"
        binary.write_bytes(b"#!/bin/false\n")
        binary.chmod(0o700)
        outside = self.base / "outside"
        outside.mkdir(mode=0o700)
        commands = Mock()
        with self.assertRaises(smoke.Blocked):
            smoke.Backend(commands, binary, outside, self.root)
        commands.call.assert_not_called()


class BackendTests(unittest.TestCase):
    def test_only_local_synthetic_backend_accepted(self):
        smoke.validate_status(status())
        for change in ({"kind": "trill-real"}, {"running": False}, {"address": "https://example.com:7443"},
                       {"address": "http://localhost:19443"}, {"address": "https://token@localhost:19443"},
                       {"address": "https://localhost:19443/private"}, {"profile": "unknown"}, {"fingerprint": ""},
                       {"pid": 1}, {"previewPort": 0}):
            with self.subTest(change=change), self.assertRaises(smoke.Blocked):
                smoke.validate_status(status(**change))

    def test_state_adapter_fails_closed_for_missing_receipts(self):
        with self.assertRaises(smoke.Blocked):
            smoke.StateView(status(state={})).records("sessions")
        value = status()
        value["state"]["sessions"][0].pop("heading")
        with self.assertRaises(smoke.Blocked):
            smoke.StateView(value).heading("demo-xirp-1")
        with self.assertRaises(smoke.SmokeError):
            smoke.StateView(status()).session("missing")

    def test_state_adapter_uses_catalog_and_global_heading(self):
        data = status()["state"]
        data["sessions"][0].pop("heading")
        view = smoke.StateView(status(state={"catalog": data, "heading": "global"}))
        self.assertEqual(view.heading("demo-xirp-1"), "global")
        self.assertEqual(view.live_session()["id"], "demo-xirp-1")

    def test_archived_sessions_never_chosen(self):
        value = status()
        value["state"]["sessions"][0]["archived"] = True
        with self.assertRaises(smoke.Blocked):
            smoke.StateView(value).live_session()

    def test_backend_identity_cannot_change_mid_run(self):
        backend = object.__new__(smoke.Backend)
        backend.identity = smoke.validate_status(status())
        backend.call = Mock(return_value=json.dumps(status(pid=54321)))
        with self.assertRaises(smoke.Blocked):
            backend.status()


class ProvenanceTests(Temporary):
    def app(self, **overrides):
        path = self.base / "Trill.app"
        path.mkdir()
        info = {"CFBundleIdentifier": smoke.BUNDLE, "CFBundleExecutable": "Trill", "DTPlatformName": "iphonesimulator",
                "CFBundleShortVersionString": "2026.10.25", "CFBundleVersion": "9"}
        info.update(overrides)
        (path / "Info.plist").write_bytes(plistlib.dumps(info))
        (path / "Trill").write_bytes(b"exact-binary")
        return path

    def test_exact_executable_hash_and_operator_source_are_recorded(self):
        identity = smoke.app_identity(self.app(), "b" * 40)
        self.assertEqual(identity["executable_sha256"], hashlib.sha256(b"exact-binary").hexdigest())
        self.assertEqual(identity["source_revision"], "b" * 40)
        self.assertEqual(identity["build"], "9")

    def test_physical_app_and_wrong_bundle_rejected(self):
        app = self.app(DTPlatformName="iphoneos")
        with self.assertRaises(smoke.Blocked):
            smoke.app_identity(app, "b" * 40)
        info = plistlib.loads((app / "Info.plist").read_bytes())
        info.update(DTPlatformName="iphonesimulator", CFBundleIdentifier="personal.app")
        (app / "Info.plist").write_bytes(plistlib.dumps(info))
        with self.assertRaises(smoke.Blocked):
            smoke.app_identity(app, "b" * 40)

    def test_executable_path_escape_and_unpinned_revision_rejected(self):
        app = self.app(CFBundleExecutable="../escape")
        with self.assertRaises(smoke.Blocked):
            smoke.app_identity(app, "b" * 40)
        with self.assertRaises(smoke.Blocked):
            smoke.app_identity(app, "main")


class RedactionTests(Temporary):
    def test_nested_tokens_links_and_keys_redacted(self):
        data = {"token": "secret-one", "payload": ["Bearer secret-two", "trill://pair?data=abc", "a" * 64,
                                               "MOSH CONNECT 60300 private-key", "custom-private"]}
        sanitized = json.dumps(smoke.redact(data, ["custom-private"]))
        for secret in ("secret-one", "secret-two", "trill://pair", "a" * 64, "private-key", "custom-private"):
            self.assertNotIn(secret, sanitized)

    def test_subprocess_failure_does_not_leak_output_or_argv(self):
        commands = smoke.Commands()
        with patch.object(smoke.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "secret-stdout", "secret-stderr")):
            with self.assertRaises(smoke.Uncertain) as caught:
                commands.call(["mise", "secret-argv"], mutate=True)
        self.assertNotIn("secret", str(caught.exception))
        self.assertNotIn("secret", json.dumps(commands.events))

    def test_pairing_screen_artifacts_are_suppressed(self):
        ui = smoke.UI(Mock(), "owned", self.root)
        ui.paired = True
        ui.tree = Mock(return_value=[element("confirm-pairing")])
        with self.assertRaises(smoke.Blocked):
            ui.receipt("private-pairing")
        self.assertFalse((self.root / "private-pairing.png").exists())

    def test_known_secret_anywhere_suppresses_capture(self):
        commands = Mock(secrets=["private-token"])
        ui = smoke.UI(commands, "owned", self.root)
        ui.paired = True
        ui.tree = Mock(return_value=[element(label="private-token")])
        with self.assertRaises(smoke.Blocked):
            ui.receipt("sensitive")
        commands.idb.assert_not_called()

    def test_ax_values_not_saved(self):
        commands = Mock(secrets=[])
        def idb(_udid, command, path, **_kwargs):
            Path(path).write_bytes(b"mock-image")
        commands.idb.side_effect = idb
        ui = smoke.UI(commands, "owned", self.root)
        ui.paired = True
        ui.tree = Mock(return_value=[element(value="arbitrary draft value")])
        receipt = ui.receipt("mock-receipt")
        self.assertEqual(len(receipt), 2)
        self.assertNotIn("arbitrary draft", (self.root / "mock-receipt.ax.json").read_text())
        self.assertEqual((self.root / "mock-receipt.png").stat().st_mode & 0o777, 0o600)

    def test_inherited_idb_routing_is_removed(self):
        with patch.dict(os.environ, {"IDB_UDID": "personal", "IDB_COMPANION": "elsewhere", "IDB_OTHER": "override"}):
            commands = smoke.Commands()
        self.assertFalse(any(k.startswith("IDB_") for k in commands.env))


class NativeInputTests(Temporary):
    def ui(self):
        ui = smoke.UI(Mock(), "owned", self.root, timeout=0)
        ui.tap = Mock()
        ui.prepare_input = Mock(return_value=element())
        return ui

    def test_uncertain_write_not_retried(self):
        ui = self.ui()
        ui.value = Mock(side_effect=["", "partial"])
        with self.assertRaises(smoke.Uncertain):
            ui.type_once(smoke.selector("terminal-draft"), "demo heading public")
        ui.c.idb.assert_called_once()
        self.assertEqual(ui.c.idb.call_args.args[1:3], ("ui", "text"))

    def test_nonempty_draft_never_overwritten(self):
        ui = self.ui()
        ui.value = Mock(return_value="old draft")
        with self.assertRaises(smoke.Uncertain):
            ui.type_once(smoke.selector("terminal-draft"), "demo heading public")
        ui.c.idb.assert_not_called()

    def test_mutation_timeout_is_uncertain_and_not_retried(self):
        commands = smoke.Commands()
        with patch.object(smoke.subprocess, "run", side_effect=subprocess.TimeoutExpired("private-argv", 1)) as run:
            with self.assertRaises(smoke.Uncertain):
                commands.call(["mise", "private-argv"], mutate=True)
        run.assert_called_once()

    def test_verified_exact_input_uses_one_hid_write(self):
        ui = self.ui()
        ui.value = Mock(side_effect=["", "demo heading public"])
        ui.type_once(smoke.selector("terminal-draft"), "demo heading public")
        ui.c.idb.assert_called_once_with("owned", "ui", "text", "demo heading public", mutate=True)

    def test_selector_disambiguates_editor_and_static_duplicate(self):
        tree = [element(), element(role="AXStaticText")]
        self.assertEqual(smoke.matching(tree, smoke.selector("terminal-draft", role="AXTextArea")), [tree[0]])

    def test_ambiguous_selectors_send_no_action(self):
        ui = self.ui()
        ui.tap = smoke.UI.tap.__get__(ui)
        ui.tree = Mock(return_value=[element(), element()])
        with self.assertRaises(smoke.Blocked):
            ui.tap(smoke.selector("terminal-draft"))
        ui.c.idb.assert_not_called()

    def test_guarded_ax_press_uses_observed_identity(self):
        ui = self.ui()
        ui.tap = smoke.UI.tap.__get__(ui)
        ui.wait = Mock(return_value=element(identifier="terminal-insert", label="Insert", role="AXButton"))
        ui.tap(smoke.selector("terminal-insert"))
        args = ui.c.idb.call_args.args
        self.assertIn("ax-coresimulator", args)
        self.assertIn("terminal-insert", args)
        self.assertIn("--expected-value", args)


class JourneyTests(Temporary):
    def journey(self):
        ui = Mock(output=self.root)
        ui.receipt.return_value = []
        backend = Mock(identity=smoke.validate_status(status()))
        backend.view.return_value = smoke.StateView(status())
        manifest = {"scenarios": [], "device": {"profile": "iphone-17-pro"}}
        return smoke.Journey(ui, backend, None, manifest, 0)

    def test_insert_must_not_execute_before_return(self):
        journey = self.journey()
        changed = status()
        changed["state"]["sessions"][0]["heading"] = "already executed"
        journey.backend.view.side_effect = [smoke.StateView(status()), smoke.StateView(changed)]
        journey.session = status()["state"]["sessions"][0]
        with self.assertRaises(smoke.SmokeError):
            journey.command("new heading", "receipt")
        targets = [call.args[0]["value"] for call in journey.ui.tap.call_args_list]
        self.assertEqual(targets, ["terminal-write", "terminal-insert"])
        journey.backend.wait.assert_not_called()

    def test_return_is_sent_once_and_backend_ack_required(self):
        journey = self.journey()
        journey.session = status()["state"]["sessions"][0]
        journey.command("new heading", "receipt")
        targets = [call.args[0]["value"] for call in journey.ui.tap.call_args_list]
        self.assertEqual(targets.count("terminal-return"), 1)
        predicate = journey.backend.wait.call_args.args[0]
        self.assertFalse(predicate(smoke.StateView(status())))
        changed = status()
        changed["state"]["sessions"][0]["heading"] = "new heading"
        self.assertTrue(predicate(smoke.StateView(changed)))

    def test_no_scenarios_or_no_assertions_cannot_pass(self):
        self.assertEqual(smoke.overall([]), "blocked")
        journey = self.journey()
        journey.happy = Mock()
        journey.run(["happy"])
        self.assertEqual(journey.manifest["scenarios"][0]["outcome"], "blocked")

    def test_blocked_happy_stops_dependent_ui(self):
        journey = self.journey()
        journey.happy = Mock(side_effect=smoke.Blocked("selector unavailable"))
        journey.lifecycle = Mock()
        journey.run(["happy", "lifecycle"])
        journey.lifecycle.assert_not_called()
        self.assertEqual(smoke.overall(journey.manifest["scenarios"]), "blocked")

    def test_uncertain_input_stops_remaining_scenarios(self):
        journey = self.journey()
        journey.happy = Mock(side_effect=smoke.Uncertain("input delivery uncertain"))
        journey.photo = Mock()
        journey.run(["happy", "photo"])
        journey.photo.assert_not_called()
        self.assertEqual(smoke.overall(journey.manifest["scenarios"]), "fail")

    def test_assertion_requires_backend_evidence(self):
        journey = self.journey()
        journey.steps = []
        journey.backend.view.side_effect = smoke.Blocked("no safe state")
        with self.assertRaises(smoke.Blocked):
            journey.assertion("native action", lambda: [])
        self.assertEqual(journey.steps[0]["outcome"], "blocked")

    def test_assertion_receipt_omits_raw_heading_and_unknown_fields(self):
        journey = self.journey()
        journey.steps = []
        journey.session = status()["state"]["sessions"][0]
        journey.assertion("native action", lambda: [])
        evidence = journey.steps[0]["evidence"][0]
        receipt = smoke.read_json(self.root / evidence["file"])
        self.assertEqual(receipt["heading_sha256"], hashlib.sha256(b"initial").hexdigest())
        self.assertNotIn("heading", receipt["session"])
        self.assertEqual(journey.steps[0]["outcome"], "pass")

    def test_faults_never_claim_coverage_without_native_receipts(self):
        journey = self.journey()
        with self.assertRaises(smoke.Blocked):
            journey.fault("upload-rejected")
        journey.backend.call.assert_not_called()

    def test_photo_does_not_guess_thumbnail(self):
        journey = self.journey()
        journey.session = status()["state"]["sessions"][0]
        journey.backend.path = self.root / "backend-a"
        journey.ui.select_synthetic_photo.side_effect = smoke.Blocked("No uniquely matched generated thumbnail")
        with self.assertRaises(smoke.Blocked):
            journey.photo()
        labels = [call.args[0]["value"] for call in journey.ui.tap.call_args_list]
        self.assertNotIn("Photo", labels)
        self.assertIn("Allow photos for this computer", labels)

    def test_photo_fixture_is_generated_privately(self):
        image = self.root / "synthetic.png"
        smoke.make_photo(image)
        self.assertEqual(image.read_bytes()[:8], b"\x89PNG\r\n\x1a\n")
        self.assertEqual(image.stat().st_mode & 0o777, 0o600)
        with self.assertRaises(FileExistsError):
            smoke.make_photo(image)


class PixelAndAdapterTests(Temporary):
    def test_generated_checkerboard_can_be_identified_without_coordinates(self):
        image = self.root / "generated.png"
        smoke.make_photo(image)
        width, height, rgb = smoke.png_rgb(image)
        self.assertEqual((width,height), (96,96))
        self.assertEqual(smoke.checker_boxes(width,height,rgb), [[0,0,96,96]])

    def test_plain_green_image_is_not_a_matching_photo(self):
        self.assertEqual(smoke.checker_boxes(96,96,bytes((30,170,130))*96*96), [])

    def test_unknown_png_format_is_blocked(self):
        image = self.root / "bad.png"
        image.write_bytes(b"not a screenshot")
        with self.assertRaises(smoke.Blocked):
            smoke.png_rgb(image)

    def test_pairing_prefers_visible_initial_fixture_after_lifecycle_runs(self):
        data=status();data["state"]["sessions"].append({**data["state"]["sessions"][0],"id":"demo-created-1"})
        self.assertEqual(smoke.StateView(data).live_session()["id"],"demo-xirp-1")

    def test_equal_welcome_names_are_disambiguated_by_provider(self):
        tree = [element(None, role="AXButton", label="Welcome, Running, Demo, Demo · xirp"),
                element(None, role="AXButton", label="Welcome, Running, Demo, Demo · herdr")]
        expected = smoke.session_label({"name":"Welcome", "provider":"xirp"})
        self.assertEqual(smoke.matching(tree, expected), [tree[0]])

    def test_safe_status_adapter_uses_only_known_initial_fixture_html(self):
        backend = object.__new__(smoke.Backend)
        backend.path = self.root / "backend-a"
        path = backend.path / "fixtures/demo-xirp/web/index.html"
        path.parent.mkdir(parents=True, mode=0o700)
        path.write_text("<html><h1>public &amp; escaped</h1></html>")
        data = status()
        data["state"]["sessions"][0].update(id="demo-xirp-1",projectId="demo-xirp")
        data["state"]["sessions"][0].pop("heading")
        data["state"]["sessions"][0].pop("attachable")
        view = backend.adapt(data)
        self.assertEqual(view.heading("demo-xirp-1"), "public & escaped")
        self.assertEqual(view.live_session()["id"], "demo-xirp-1")

    def test_redirected_html_receipt_is_blocked(self):
        backend = object.__new__(smoke.Backend)
        backend.path = self.root / "backend-a"
        path = backend.path / "fixtures/demo-xirp/web/index.html"
        path.parent.mkdir(parents=True, mode=0o700)
        target = self.root / "other.html"; target.write_text("<h1>other</h1>")
        path.symlink_to(target)
        data = status(); data["state"]["sessions"][0].update(id="demo-xirp-1",projectId="demo-xirp")
        data["state"]["sessions"][0].pop("heading")
        with self.assertRaises(smoke.Blocked):
            backend.adapt(data)

    def test_fault_command_keeps_operand_before_flags(self):
        backend = object.__new__(smoke.Backend)
        backend.c, backend.path, backend.binary = Mock(), Path("/owned/backend-a"), Path("/owned/trill")
        backend.call("fault", "capacity")
        self.assertEqual(backend.c.call.call_args.args[0], [backend.binary,"demo","fault","capacity","--state-dir",backend.path])

    def test_replacement_checks_previous_text_and_does_not_retry(self):
        ui = smoke.UI(Mock(), "owned", self.root)
        ui.tap = Mock(); ui.value = Mock(return_value="unexpected draft")
        with self.assertRaises(smoke.Uncertain):
            ui.replace_once(smoke.selector("terminal-draft"), "replacement", "expected")
        ui.c.idb.assert_not_called()

    def test_malformed_backend_address_is_blocked_not_raw_value_error(self):
        for address in ("https://localhost:invalid", "https://[invalid:7443", None):
            with self.subTest(address=address), self.assertRaises(smoke.Blocked):
                smoke.validate_status(status(address=address))


class EvidenceRegressionTests(Temporary):
    def test_repeated_receipts_do_not_overwrite_earlier_evidence(self):
        commands = Mock(secrets=[])
        commands.idb.side_effect = lambda _udid, _command, path: Path(path).write_bytes(b"mock-image")
        ui = smoke.UI(commands, "owned", self.root)
        ui.paired = True; ui.tree = Mock(return_value=[element()])
        first = ui.receipt("reconnect"); second = ui.receipt("reconnect")
        self.assertNotEqual(first[0]["file"], second[0]["file"])
        for record in first + second:
            self.assertEqual(record["sha256"], smoke.sha256(self.root / record["file"]))

    def test_transient_error_keeps_the_observed_native_ax_snapshot(self):
        commands = Mock(secrets=[])
        commands.idb.side_effect = lambda _udid, _command, path: Path(path).write_bytes(b"subsequent-frame")
        ui = smoke.UI(commands,"owned",self.root);ui.paired=True
        ui.tree = Mock(return_value=[element(None,role="AXStaticText",label="mosh capacity reached Waiting 1 second before trying again.")])
        evidence = ui.observe_error("mosh capacity reached","capacity")
        self.assertIn("capacity reached",(self.root/evidence[0]["file"]).read_text())
        self.assertIn("subsequent",evidence[0]["observation"])
        self.assertEqual(ui.tree.call_count,1)

    def test_bad_app_preflight_writes_blocked_manifest_without_ui(self):
        args = ["run","--state-dir",str(self.root),"--trill",str(self.root/"trill"),
                "--backend-state",str(self.root/"backend-a"),"--app",str(self.root/"missing.app"),
                "--app-source-revision","b"*40,"--appearance","light"]
        with patch.object(smoke,"Commands") as commands, contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(smoke.main(args),2)
        commands.return_value.idb.assert_not_called()
        reports = list(self.root.glob("preflight-*.json"))
        self.assertEqual(len(reports),1)
        self.assertEqual(smoke.read_json(reports[0])["outcome"],"blocked")

    def test_old_idb_is_blocked_before_any_simulator_action(self):
        commands=smoke.Commands()
        commands.call=Mock(return_value="unpatched help")
        with self.assertRaises(smoke.Blocked):
            commands.capabilities()
        self.assertFalse(commands.allowed)

    def test_known_nonsecret_photo_hash_does_not_disable_other_secret_checks(self):
        commands=Mock(secrets=[])
        ui=smoke.UI(commands,"owned",self.root);ui.paired=True
        ui.public_hashes.add("a"*64)
        ui.tree=Mock(return_value=[element(value="/attachments/"+"a"*64+"/photo.jpg")])
        ui.safe_screen()
        ui.tree=Mock(return_value=[element(value="/attachments/"+"a"*64+"/photo.jpg Bearer private")])
        with self.assertRaises(smoke.Blocked):
            ui.safe_screen()


class BackgroundMetricTests(Temporary):
    def test_confirmed_dwell_excludes_foreground_input_time(self):
        ui=Mock(output=self.root,timeout=0)
        ui.exists.return_value=False
        ui.tree.return_value=[element(None,role="AXApplication",label="SpringBoard")]
        ui.receipt.return_value=[]
        ui.wait.side_effect=lambda target: element(None,role="AXApplication",label=target["value"])
        backend=Mock();backend.view.return_value=smoke.StateView(status())
        manifest={"scenarios":[{"name":"happy"}]}
        journey=smoke.Journey(ui,backend,None,manifest,background_seconds=0)
        journey.session=status()["state"]["sessions"][0]
        journey.command=Mock(return_value=[])
        journey.reconnect()
        metric=manifest["backgrounds"][0]
        self.assertEqual(metric["requested_dwell_seconds"],0)
        self.assertLess(metric["confirmed_dwell_seconds"],0.1)
        self.assertGreaterEqual(metric["roundtrip_seconds_including_fresh_input"],metric["confirmed_dwell_seconds"])
        self.assertNotIn("background_seconds_observed",manifest)


class IDBDispatchRegressionTests(Temporary):
    def test_long_press_checks_live_point_then_uses_hid_without_ax_guard_flags(self):
        commands=Mock();target=element(None,role="AXButton",label="Demo terminal, Running, Demo · xirp")
        commands.idb.side_effect=[json.dumps(target),""]
        ui=smoke.UI(commands,"owned",self.root);ui.wait=Mock(return_value=target)
        ui.tap(smoke.label(target["AXLabel"]),duration=1)
        first,second=commands.idb.call_args_list
        self.assertIn("describe-point",first.args)
        self.assertIn("--duration",second.args)
        self.assertIn("hid",second.args)
        self.assertNotIn("--expected-value",second.args)

    def test_unlabelled_alert_field_uses_native_role_guard(self):
        commands=Mock();ui=smoke.UI(commands,"owned",self.root)
        ui.wait=Mock(return_value=element(None,label=None,role="AXTextField"))
        ui.tap(smoke.selector(None,"AXLabel",role="AXTextField"))
        args=commands.idb.call_args.args
        self.assertEqual(args[-4:],("--expected-key","role","--expected-value","AXTextField"))
        self.assertNotIn(None,args)

    def test_mismatched_long_press_point_sends_no_hold(self):
        commands=Mock();commands.idb.return_value=json.dumps(element(None,role="AXButton",label="Other fixture"))
        ui=smoke.UI(commands,"owned",self.root);ui.wait=Mock(return_value=element(None,role="AXButton",label="Intended fixture"))
        with self.assertRaises(smoke.Blocked):
            ui.tap(smoke.label("Intended fixture"),duration=1)
        commands.idb.assert_called_once()
        self.assertIn("describe-point",commands.idb.call_args.args)

    def test_unlabelled_rename_field_uses_ax_and_real_edit_callback(self):
        commands=Mock();ui=smoke.UI(commands,"owned",self.root)
        ui.tap=Mock();ui.value=Mock(return_value="Demo terminal")
        ui.wait=Mock(return_value=element(None,label=None,role="AXTextField"));ui.wait_value=Mock()
        ui.prepare_input=Mock(return_value=ui.wait.return_value)
        target=smoke.selector(None,"AXLabel",role="AXTextField")
        ui.replace_once(target,"smoke run 123","Demo terminal")
        self.assertIn("set-value",commands.idb.call_args_list[0].args)
        self.assertEqual(commands.idb.call_args_list[0].args[-1],"smoke run 123 ")
        self.assertEqual(commands.idb.call_args_list[1].args,("owned","ui","key","42"))
        ui.wait_value.assert_called_once_with(target,"smoke run 123")

    def test_plain_field_replacement_uses_native_select_all_and_verifies_once(self):
        commands=Mock();ui=smoke.UI(commands,"owned",self.root)
        ui.tap=Mock();ui.value=Mock(return_value="old query")
        ui.wait=Mock(return_value=element(role="AXTextField"));ui.wait_value=Mock()
        ui.prepare_input=Mock(return_value=ui.wait.return_value)
        ui.replace_once(smoke.label("Search",role="AXTextField"),"new query","old query")
        self.assertEqual(commands.idb.call_count,2)
        self.assertEqual(commands.idb.call_args_list[0].args,("owned","ui","key","4","--command"))
        self.assertEqual(commands.idb.call_args_list[1].args,("owned","ui","text","new query"))
        ui.wait_value.assert_called_once_with(smoke.label("Search",role="AXTextField"),"new query")



class FalsePassRegressionTests(Temporary):
    def test_uploaded_jpeg_must_decode_to_matching_content(self):
        uploaded=self.root/"actual-backend.jpg";uploaded.write_bytes(b"\xff\xd8mock-jpeg")
        decoded=self.root/"decoded.png";commands=Mock()
        commands.call.side_effect=lambda argv: decoded.write_bytes(b"decoded-image")
        with patch.object(smoke,"png_rgb",return_value=(96,96,bytes((30,170,130))*96*96)):
            with self.assertRaises(smoke.SmokeError):
                smoke.verify_uploaded_photo(commands,uploaded,decoded)
        commands.call.assert_called_once_with(["sips","-s","format","png",uploaded,"--out",decoded])
        self.assertEqual(decoded.stat().st_mode & 0o777,0o600)

    def test_read_only_ax_read_failure_can_wait_for_real_element(self):
        ui=smoke.UI(Mock(),"owned",self.root,timeout=1)
        expected=element("ready",role="AXButton")
        ui.tree=Mock(side_effect=[smoke.Blocked("transient read failure"),[expected],[expected]])
        with patch.object(smoke.time,"sleep"):
            self.assertEqual(ui.wait(smoke.selector("ready")),expected)
        ui.c.idb.assert_not_called()

    def test_duo_uses_observed_side_rail_overflow_and_rejects_ambiguity(self):
        ui=smoke.UI(Mock(),"owned",self.root)
        duo=element("BottomOverflowBarButtonItem",role="AXButton",label="More")
        ui.tree=Mock(return_value=[duo]);ui.tap=Mock();ui.overflow()
        ui.tap.assert_called_once_with(smoke.selector("BottomOverflowBarButtonItem",role="AXButton"))
        ui.tap.reset_mock();ui.tree.return_value=[duo,element("OverflowBarButtonItem",role="AXButton")]
        with self.assertRaises(smoke.Blocked):ui.overflow()
        ui.tap.assert_not_called()

    def test_failed_ax_read_never_proves_absence(self):
        ui=smoke.UI(Mock(),"owned",self.root,timeout=1)
        ui.tree=Mock(side_effect=smoke.Blocked("unavailable read"))
        with self.assertRaises(smoke.Blocked):
            ui.wait(smoke.selector("forgotten"),absent=True)

    def test_loading_catalog_does_not_guess_back_navigation(self):
        ui=Mock();ui.exists.return_value=False
        smoke.Journey(ui,Mock(),None,{}).to_sessions()
        ui.tap.assert_not_called();ui.wait.assert_called_once_with(smoke.label("New session"))

    def test_native_search_is_closed_before_session_toolbar_wait(self):
        ui=Mock();journey=smoke.Journey(ui,Mock(),None,{})
        ui.exists.side_effect=lambda target: target in (smoke.selector("open-projects"),smoke.label("Sessions, projects, or branches",role="AXTextField"),smoke.label("close"))
        journey.to_sessions()
        ui.tap.assert_called_once_with(smoke.label("close"))
        ui.wait.assert_called_once_with(smoke.label("New session"))

    def test_yellow_center_dots_on_green_are_not_checkerboard(self):
        rgb = bytearray(bytes((30,170,130))*96*96)
        for row in range(6):
            for col in range(6):
                if (row+col)%2 == 0:
                    x,y=col*16+8,row*16+8
                    rgb[(y*96+x)*3:(y*96+x+1)*3]=bytes((240,210,30))
        self.assertEqual(smoke.checker_boxes(96,96,rgb),[])
        self.assertFalse(smoke.checker_reference(96,96,rgb,[0,0,96,96]))

    def test_wrong_tile_and_boundary_shift_reject_reference(self):
        image=self.root/"reference.png";smoke.make_photo(image)
        width,height,pixels=smoke.png_rgb(image)
        damaged=bytearray(pixels)
        for y in range(16):
            for x in range(16):
                damaged[(y*96+x)*3:(y*96+x+1)*3]=bytes((30,170,130))
        self.assertFalse(smoke.checker_reference(width,height,damaged,[0,0,96,96]))
        self.assertFalse(smoke.checker_reference(width,height,pixels,[4,0,92,96]))
        self.assertTrue(smoke.checker_reference(width,height,pixels,[0,0,96,96]))

    def ui_for_capture(self):
        commands=Mock(secrets=[])
        def idb(_udid,command,path,*args,**kwargs):
            Path(path).write_bytes(b"mock-crop")
            return ""
        commands.idb.side_effect=idb;commands.call.return_value=json.dumps(["unique new heading"])
        ui=smoke.UI(commands,"owned",self.root,timeout=0)
        ui.paired=True;ui.ocr=self.root/"already-compiled"
        ui.receipt=Mock(return_value=[])
        return ui

    def test_optional_web_ax_failure_uses_ocr_with_required_region_checks(self):
        ui=self.ui_for_capture();pane=element(None,role="AXGroup",label="Page preview")
        def read(mode="xctest"):
            if mode=="web":raise smoke.Blocked("optional web process AX unavailable")
            return [pane]
        ui.tree=Mock(side_effect=read)
        evidence=ui.visible_text("unique new heading","scoped-fallback")
        self.assertEqual(len(evidence),1)
        self.assertEqual(evidence[0]["region_points"],pane["frame"])
        self.assertTrue((self.root/"scoped-fallback.crop.png").exists())
        ui.c.call.assert_called_once()

    def test_ocr_zero_letter_confusion_still_blocks_and_discards_evidence(self):
        ui = self.ui_for_capture()
        ui.tree = Mock(return_value=[element(None, role="AXGroup", label="Page preview")])
        ui.c.call.return_value = json.dumps(["smoke heading", "Odedb035"])
        with self.assertRaises(smoke.Blocked):
            ui.visible_text("smoke heading 0dedb035", "exact-zero-letter")
        ui.receipt.assert_not_called()
        self.assertFalse((self.root / "exact-zero-letter.crop.png").exists())
        self.assertFalse(any(c.kwargs.get("mutate") for c in ui.c.idb.call_args_list))

    def test_new_letter_marker_requires_exact_ocr_in_current_region(self):
        for observed, matches in (("abcdefghjkmn", True), ("ABCDEFGHJKMN", False), ("abcdefghjkmx", False)):
            with self.subTest(observed=observed):
                ui = self.ui_for_capture()
                ui.tree = Mock(return_value=[element(None, role="AXGroup", label="Page preview")])
                ui.c.call.return_value = json.dumps(["smoke heading", observed])
                if matches:
                    self.assertTrue(ui.visible_text("smoke heading abcdefghjkmn", "letters-" + observed))
                    ui.receipt.assert_called_once()
                else:
                    with self.assertRaises(smoke.Blocked):
                        ui.visible_text("smoke heading abcdefghjkmn", "letters-" + observed)
                    ui.receipt.assert_not_called()

    def test_disappearing_preview_cannot_ocr_terminal_echo(self):
        ui=self.ui_for_capture()
        pane=element(None,role="AXGroup",label="Page preview")
        terminal=element("remote-terminal",value="unique new heading")
        # Initial region exists; after the first read it disappears, leaving echo.
        ui.tree=Mock(side_effect=[[pane],[terminal]])
        with self.assertRaises(smoke.Blocked):
            ui.visible_text("unique new heading","disappeared")
        ui.c.idb.assert_not_called();ui.receipt.assert_not_called()

    def test_disappearance_after_capture_rejects_even_matching_ocr(self):
        ui=self.ui_for_capture();pane=element(None,role="AXGroup",label="Page preview")
        alive=[pane];state={"captured":False}
        def capture(_udid,command,path,*args,**kwargs):
            Path(path).write_bytes(b"terminal-echo");state["captured"]=True
        ui.c.idb.side_effect=capture
        ui.tree=Mock(side_effect=lambda *args: [] if state["captured"] else alive)
        with self.assertRaises(smoke.Blocked):
            ui.visible_text("unique new heading","after-capture")
        ui.c.call.assert_not_called();ui.receipt.assert_not_called()
        self.assertFalse((self.root/"after-capture.crop.png").exists())

    def test_moved_region_cannot_use_old_crop(self):
        ui=self.ui_for_capture();pane=element(None,role="AXGroup",label="Page preview")
        moved={**pane,"frame":{**pane["frame"],"y":120}}
        ui.tree=Mock(side_effect=[[pane],[moved]])
        with self.assertRaises(smoke.Blocked):
            ui.visible_text("unique new heading","moved")
        ui.c.idb.assert_not_called();ui.receipt.assert_not_called()

    def test_ambiguous_region_never_captures(self):
        ui=self.ui_for_capture();pane=element(None,role="AXGroup",label="Page preview")
        ui.tree=Mock(return_value=[pane,pane])
        with self.assertRaises(smoke.Blocked):
            ui.visible_text("unique new heading","ambiguous")
        ui.c.idb.assert_not_called()

    def test_expired_preview_requires_post_expiry_nonce_not_cached_heading(self):
        ui=Mock(output=self.root);ui.visible_text.return_value=[];backend=Mock()
        journey=smoke.Journey(ui,backend,None,{})
        journey.heading="cached old heading";journey.live_preview=Mock()
        journey.command=Mock(return_value=[])
        order=Mock();order.attach_mock(backend.fault,"fault");order.attach_mock(journey.command,"command")
        journey.fault("preview-expired")
        fresh=journey.command.call_args.args[0]
        self.assertTrue(fresh.startswith("smoke renewed "))
        self.assertNotEqual(fresh,journey.heading)
        self.assertEqual(order.mock_calls[0].args,("preview-expired",))
        self.assertEqual(order.mock_calls[1].args[0],fresh)
        ui.visible_text.assert_called_once_with(fresh,"preview-renewed-fresh-heading")
        self.assertIn(smoke.label("Reload"),[c.args[0] for c in ui.tap.call_args_list])



class RunnerReliabilityTests(Temporary):
    def test_pair_filters_catalog_before_waiting_for_owned_offscreen_row(self):
        ui=Mock();ui.exists.return_value=False
        backend=Mock();backend.view.return_value=smoke.StateView(status())
        backend.invitation.return_value="private invitation in memory"
        journey=smoke.Journey(ui,backend,None,{})
        journey.search_sessions=Mock()
        order=Mock();order.attach_mock(ui.wait,"wait");order.attach_mock(journey.search_sessions,"search")
        session=journey.pair(backend)
        self.assertEqual(order.mock_calls[0].args,(smoke.selector("confirm-pairing"),))
        self.assertEqual(order.mock_calls[0].kwargs,{"absent":True})
        self.assertEqual(order.mock_calls[1].args,(smoke.selector("open-projects"),))
        self.assertEqual(order.mock_calls[2].args,(session["name"],))
        self.assertEqual(order.mock_calls[3].args,(smoke.session_label(session),))
        ui.c.idb.assert_called_once()
        ui.tap.assert_called_once_with(smoke.selector("confirm-pairing"))
        self.assertTrue(ui.paired)

    def test_pair_search_block_does_not_redeem_again_or_open_terminal(self):
        ui=Mock();ui.exists.return_value=False;ui.paired=False
        backend=Mock();backend.view.return_value=smoke.StateView(status())
        journey=smoke.Journey(ui,backend,None,{})
        journey.search_sessions=Mock(side_effect=smoke.Blocked("ambiguous native field"))
        with self.assertRaises(smoke.Blocked):journey.pair(backend)
        backend.invitation.assert_called_once();ui.c.idb.assert_called_once()
        ui.tap.assert_called_once_with(smoke.selector("confirm-pairing"))
        self.assertFalse(ui.paired)

    def native_search(self, *, label=None):
        field=element(None,role="AXTextField",label=label)
        icon=element("magnifyingglass",role="AXImage",label="Search",frame={"x":25,"y":45,"width":10,"height":10})
        return field,icon

    def test_unlabelled_search_requires_one_field_and_its_native_search_icon(self):
        ui=smoke.UI(Mock(),"owned",self.root)
        field,icon=self.native_search()
        ui.tree=Mock(return_value=[field,icon])
        with patch.object(smoke.time,"sleep"):
            target=ui.search_field("Sessions, projects, or branches")
        self.assertEqual(target,smoke.selector("AXTextField","role",role="AXTextField"))
        ui.c.idb.assert_not_called()

    def test_unlabelled_search_rejects_ambiguous_fields_or_alert_field(self):
        for extra in (True,False):
            with self.subTest(extra_field=extra):
                ui=smoke.UI(Mock(),"owned",self.root,timeout=0)
                field,icon=self.native_search()
                ui.tree=Mock(return_value=[field,icon,{**field,"frame":{**field["frame"],"y":200}}] if extra else [field])
                with self.assertRaises(smoke.Blocked):ui.search_field("Sessions, projects, or branches")
                ui.c.idb.assert_not_called()

    def test_unique_labelled_other_field_is_not_search(self):
        ui=smoke.UI(Mock(),"owned",self.root,timeout=0)
        ui.tree=Mock(return_value=[element(None,role="AXTextField",label="Preview address")])
        with self.assertRaises(smoke.Blocked):ui.search_field("Sessions, projects, or branches")
        ui.c.idb.assert_not_called()

    def test_unlabelled_search_replacement_uses_select_all_once(self):
        ui=smoke.UI(Mock(),"owned",self.root)
        field,_=self.native_search()
        ui.tap=Mock();ui.wait=Mock(return_value=field);ui.value=Mock(return_value="old search");ui.wait_value=Mock()
        ui.prepare_input=Mock(return_value=field)
        ui.replace_once(smoke.selector("AXTextField","role",role="AXTextField"),"new search","old search",select_all=True)
        self.assertEqual([c.args[1:4] for c in ui.c.idb.call_args_list],[("ui","key","4"),("ui","text","new search")])
        ui.wait_value.assert_called_once()

    def test_search_resolution_block_sends_no_text_or_submit(self):
        ui=Mock();ui.search_field.side_effect=smoke.Blocked("multiple fields")
        with self.assertRaises(smoke.Blocked):smoke.Journey(ui,Mock(),None,{}).search_sessions("public fixture")
        ui.type_once.assert_not_called();ui.replace_once.assert_not_called();ui.c.idb.assert_not_called()

    def test_back_rechecks_search_then_cancels_once_before_toolbar_wait(self):
        ui=Mock();state={"back":False,"closed":False};calls=[]
        def exists(target):
            if target==smoke.label("New session"):return state["closed"]
            if target==smoke.label("Sessions"):return not state["back"]
            if target in (smoke.selector("open-projects"),smoke.selector("AXTextField","role",role="AXTextField"),smoke.label("close")):
                return state["back"]
            return False
        def tap(target):
            calls.append(target)
            if target==smoke.label("Sessions"):state["back"]=True
            elif target==smoke.label("close"):state["closed"]=True
        ui.exists.side_effect=exists;ui.tap.side_effect=tap
        smoke.Journey(ui,Mock(),None,{}).to_sessions()
        self.assertEqual(calls,[smoke.label("Sessions"),smoke.label("close")])
        ui.search_field.assert_called_once_with("Sessions, projects, or branches")
        self.assertEqual(ui.wait.call_args_list[-1].args,(smoke.label("New session"),))

    def test_session_hold_uses_verified_name_in_tall_row_not_provider_center(self):
        name=element(None,role="AXStaticText",label="Owned worktree",frame={"x":30,"y":50,"width":120,"height":20})
        row=element(None,role="AXButton",label="Owned worktree, Running, Demo · xirp",frame={"x":20,"y":40,"width":300,"height":180},children=[name])
        ui=smoke.UI(Mock(),"owned",self.root);ui.wait=Mock(return_value=row);ui.tree=Mock(return_value=[row])
        ui.c.idb.side_effect=[json.dumps(name),""]
        ui.tap(smoke.session_label({"name":"Owned worktree","provider":"xirp"}),duration=1)
        probe,hold=ui.c.idb.call_args_list
        self.assertEqual(probe.args[3:5],("90","60"))
        self.assertEqual(hold.args[3:5],("90","60"))
        self.assertNotEqual(hold.args[4],"130")
        self.assertEqual(sum(bool(c.kwargs.get("mutate")) for c in ui.c.idb.call_args_list),1)

    def test_flat_row_name_is_scoped_to_unique_row_and_verified_at_point(self):
        name=element(None,role="AXStaticText",label="Owned worktree",frame={"x":30,"y":50,"width":120,"height":20})
        other={**name,"frame":{**name["frame"],"y":300}}
        row=element(None,role="AXButton",label="Owned worktree, Running, Demo · xirp",frame={"x":20,"y":40,"width":300,"height":180})
        ui=smoke.UI(Mock(),"owned",self.root);ui.wait=Mock(return_value=row);ui.tree=Mock(return_value=[row,name,other])
        ui.c.idb.side_effect=[json.dumps(name),""]
        ui.tap(smoke.session_label({"name":"Owned worktree","provider":"xirp"}),duration=1)
        self.assertEqual(ui.c.idb.call_count,2)

    def test_wrong_or_moved_point_sends_no_session_hold(self):
        for moved in (False,True):
            with self.subTest(moved=moved):
                name=element(None,role="AXStaticText",label="Owned worktree",frame={"x":30,"y":50,"width":120,"height":20})
                row=element(None,role="AXButton",label="Owned worktree, Running, Demo · xirp",frame={"x":20,"y":40,"width":300,"height":180},children=[name])
                hit={**name,"frame":{**name["frame"],"y":51}} if moved else element(None,role="AXStaticText",label="Demo · xirp")
                ui=smoke.UI(Mock(),"owned",self.root);ui.wait=Mock(return_value=row);ui.tree=Mock(return_value=[row])
                ui.c.idb.return_value=json.dumps(hit)
                with self.assertRaises(smoke.Blocked):ui.tap(smoke.session_label({"name":"Owned worktree","provider":"xirp"}),duration=1)
                ui.c.idb.assert_called_once()
                self.assertFalse(ui.c.idb.call_args.kwargs.get("mutate",False))

    def test_missing_or_duplicate_row_names_block_before_any_hold(self):
        for duplicate in (False,True):
            with self.subTest(duplicate=duplicate):
                name=element(None,role="AXStaticText",label="Owned worktree")
                row=element(None,role="AXButton",label="Owned worktree, Running, Demo · xirp",children=[name,name] if duplicate else [])
                ui=smoke.UI(Mock(),"owned",self.root);ui.wait=Mock(return_value=row);ui.tree=Mock(return_value=[row])
                with self.assertRaises(smoke.Blocked):ui.tap(smoke.session_label({"name":"Owned worktree","provider":"xirp"}),duration=1)
                ui.c.idb.assert_not_called()

    def capture_ui(self):
        ui=smoke.UI(Mock(secrets=[]),"owned",self.root,timeout=1)
        ui.paired=True;ui.ocr=self.root/"compiled-ocr";ui.receipt=Mock(return_value=[])
        pane=element(None,role="AXGroup",label="Page preview")
        return ui,pane

    def test_moving_capture_is_deleted_and_only_new_stable_capture_can_pass(self):
        ui,pane=self.capture_ui();state={"captures":0}
        moved={**pane,"frame":{**pane["frame"],"y":120}}
        ui.tree=Mock(side_effect=lambda *args:[moved if state["captures"] else pane])
        def capture(_udid,_command,path,*args):
            state["captures"]+=1
            Path(path).write_bytes(b"stale terminal echo" if state["captures"]==1 else b"fresh web render")
        ui.c.idb.side_effect=capture;ui.c.call.return_value=json.dumps(["unique new heading"])
        with patch.object(smoke.time,"sleep"):
            evidence=ui.visible_text("unique new heading","settled")
        self.assertEqual(state["captures"],2)
        self.assertEqual(ui.c.call.call_count,1)
        self.assertEqual(evidence[0]["region_points"],moved["frame"])
        self.assertEqual((self.root/evidence[0]["file"]).read_bytes(),b"fresh web render")
        self.assertEqual(evidence[0]["sha256"],hashlib.sha256(b"fresh web render").hexdigest())

    def test_matching_ocr_before_movement_is_not_reused_for_later_wrong_render(self):
        ui,pane=self.capture_ui();state={"ocr":0,"time":0}
        moved={**pane,"frame":{**pane["frame"],"y":120}}
        ui.tree=Mock(side_effect=lambda *args:[moved if state["ocr"] else pane])
        ui.c.idb.side_effect=lambda _udid,_command,path,*args:Path(path).write_bytes(b"unaccepted crop")
        def recognize(*args):
            state["ocr"]+=1
            if state["ocr"]>1:state["time"]=2
            return json.dumps(["unique new heading" if state["ocr"]==1 else "unrelated web content"])
        ui.c.call.side_effect=recognize
        with patch.object(smoke.time,"sleep"),patch.object(smoke.time,"monotonic",side_effect=lambda:state["time"]):
            with self.assertRaises(smoke.Blocked):ui.visible_text("unique new heading","discard-match")
        self.assertEqual(state["ocr"],2)
        self.assertFalse(ui.evidence);ui.receipt.assert_not_called()
        self.assertFalse((self.root/"discard-match.crop.png").exists())




class ParentRowHitRegressionTests(Temporary):
    def fixture(self, *, hit_change=None, fresh_change=None, duplicate_fresh=False):
        name=element(None,role="AXStaticText",label="Owned worktree",frame={"x":30,"y":50,"width":120,"height":20})
        parent=element(None,role="AXButton",label="Owned worktree, Running, branch demo/owned, Demo · xirp",frame={"x":20,"y":40,"width":300,"height":180},children=[name])
        # The observed native describe-point response is the parent alone.
        hit={key:value for key,value in parent.items() if key!="children"}
        if hit_change:hit.update(hit_change)
        fresh={**parent,"children":[{**name}]}
        if fresh_change:fresh_change(fresh)
        ui=smoke.UI(Mock(),"owned",self.root);ui.wait=Mock(return_value=parent)
        ui.tree=Mock(side_effect=[[parent],[fresh,fresh] if duplicate_fresh else [fresh]])
        ui.c.idb.side_effect=[json.dumps(hit),""]
        return ui,smoke.session_label({"name":"Owned worktree","provider":"xirp"})

    def test_exact_observed_parent_hit_accepts_name_point_and_one_hold(self):
        ui,target=self.fixture();ui.tap(target,duration=1)
        self.assertEqual(ui.tree.call_count,2)
        probe,hold=ui.c.idb.call_args_list
        self.assertEqual(probe.args[3:5],("90","60"));self.assertEqual(hold.args[3:5],("90","60"))
        self.assertEqual(hold.args[5:],("--duration","1","--api","hid"))
        self.assertEqual(sum(bool(c.kwargs.get("mutate")) for c in ui.c.idb.call_args_list),1)

    def test_wrong_parent_identity_role_or_geometry_sends_no_hold(self):
        for change in ({"AXLabel":"Owned worktree, Running, branch demo/owned, Demo · herdr"},
                       {"AXLabel":"Different session, Running, Demo · xirp"},
                       {"role":"AXGroup"},
                       {"frame":{"x":20,"y":41,"width":300,"height":180}},
                       {"enabled":False}):
            with self.subTest(change=change):
                ui,target=self.fixture(hit_change=change)
                with self.assertRaises(smoke.Blocked):ui.tap(target,duration=1)
                ui.c.idb.assert_called_once();self.assertFalse(ui.c.idb.call_args.kwargs.get("mutate",False))

    def test_row_or_name_change_after_parent_hit_sends_no_hold(self):
        def moved_parent(row):row["frame"]={**row["frame"],"y":41}
        def changed_provider(row):row["AXLabel"]="Owned worktree, Running, Demo · herdr"
        def moved_name(row):row["children"][0]["frame"]={**row["children"][0]["frame"],"y":51}
        def missing_name(row):row["children"]=[]
        for change in (moved_parent,changed_provider,moved_name,missing_name):
            with self.subTest(change=change.__name__):
                ui,target=self.fixture(fresh_change=change)
                with self.assertRaises(smoke.Blocked):ui.tap(target,duration=1)
                ui.c.idb.assert_called_once();self.assertFalse(ui.c.idb.call_args.kwargs.get("mutate",False))

    def test_ambiguous_fresh_parent_is_rejected_without_hold(self):
        ui,target=self.fixture(duplicate_fresh=True)
        with self.assertRaises(smoke.Blocked):ui.tap(target,duration=1)
        ui.c.idb.assert_called_once();self.assertFalse(ui.c.idb.call_args.kwargs.get("mutate",False))


class FinalizationRegressionTests(Temporary):
    def run_case(self, *, profile="iphone-17-pro", ui_failure=False, terminate_failure=False, shutdown_failure=False, ownership_failure=False, placement="BottomOverflowBarButtonItem", pose_read_failure=None):
        udid=str(uuid.uuid4()).upper()
        record={"kind":smoke.OWNER,"owner":self.marker["id"],"udid":udid,"name":"Trill Smoke mock owned",
                "profile":profile,"runtime":"mock-runtime","device_type":"mock-type"}
        inventory={"devices":{"mock-runtime":[{"udid":udid,"name":record["name"],"isAvailable":True,"deviceTypeIdentifier":"mock-type"}]}}
        commands=Mock(events=[],secrets=[]);commands.inventory.return_value={} if ownership_failure else inventory
        commands.capabilities.return_value={"execution":"mock-only"}
        state={"hinge":"0", "rotation":"PORTRAIT"}
        def idb(target,action,*args,**kwargs):
            self.assertEqual(target,udid)
            if action=="describe":return json.dumps({"udid":udid,"target_type":"simulator","os_version":"mock"})
            if action in ("hinge", "rotation"):
                if args:state[action]=args[0]
                elif (pose_read_failure and action==pose_read_failure[0]
                      and state["hinge"]=="90" and state["rotation"]=="LANDSCAPE_LEFT"):
                    return pose_read_failure[1]
                return state[action]
            if action=="terminate" and terminate_failure:raise smoke.Uncertain("terminate failed")
            return ""
        def simctl(action,*args,**kwargs):
            if action=="ui" and len(args)==2:return "light"
            if action=="shutdown" and shutdown_failure:raise smoke.Blocked("shutdown failed")
            return ""
        commands.idb.side_effect=idb;commands.simctl.side_effect=simctl
        ui=Mock(evidence=[])
        # The old cleanup would find terminal-write, try the missing top menu,
        # fail there and skip both termination and shutdown.
        ui.exists.side_effect=lambda target:target==smoke.selector("terminal-write")
        ui.tree.return_value=[element(placement,role="AXButton",label="Session actions" if placement=="tabletop" else "More")]
        ui.tap.side_effect=smoke.Blocked("top overflow is unavailable")
        backend=Mock(identity=smoke.validate_status(status()),binary_sha256="b"*64)
        args=argparse.Namespace(runtime=None,timeout=1,background_seconds=0,scenarios=["happy"])
        def journey(_ui,_backend,_secondary,manifest,_background):
            obj=Mock()
            commands.journey=obj
            def run(_scenarios):
                manifest["scenarios"].append({"name":"happy","outcome":"fail" if ui_failure else "pass","assertions":[{"outcome":"fail" if ui_failure else "pass"}]})
                if ui_failure:raise smoke.Uncertain("Native input delivery unconfirmed; do not retry")
            obj.run.side_effect=run
            obj.assertion=Mock(side_effect=lambda _name,function:function())
            return obj
        with patch.object(smoke,"create_simulator",return_value=record),patch.object(smoke,"UI",return_value=ui),patch.object(smoke,"Journey",side_effect=journey),contextlib.redirect_stdout(io.StringIO()):
            result=smoke.run_one(commands,self.root,self.marker,args,{"path":"/mock/Trill.app"},backend,None,profile,"light")
        reports=list(self.root.glob('run-*/manifest.json'))
        self.assertEqual(len(reports),1)
        self.assertEqual(smoke.read_json(reports[0])["outcome"],result["outcome"])
        return result,commands,ui,backend,udid

    def assert_mandatory_calls(self,commands,backend,udid):
        terminations=[call for call in commands.idb.call_args_list if call.args[1]=="terminate"]
        shutdowns=[call for call in commands.simctl.call_args_list if call.args[0]=="shutdown"]
        self.assertEqual(len(terminations),1);self.assertEqual(terminations[0].args,(udid,"terminate",smoke.BUNDLE))
        self.assertEqual(len(shutdowns),1);self.assertEqual(shutdowns[0].args,("shutdown",udid))
        backend.fault.assert_called_once_with("drop-leases")

    def test_native_action_failure_preserved_while_teardown_still_runs(self):
        result,commands,ui,backend,udid=self.run_case(ui_failure=True)
        self.assertEqual(result["outcome"],"fail")
        self.assertEqual(result["reason"],"Native input delivery unconfirmed; do not retry")
        self.assertEqual(result["scenarios"][0]["outcome"],"fail")
        self.assertTrue(all(step["outcome"]=="pass" for step in result["cleanup_steps"]))
        self.assert_mandatory_calls(commands,backend,udid)
        ui.tap.assert_not_called()

    def test_duo_bottom_or_tabletop_menu_is_not_a_finalization_dependency(self):
        for placement in ("BottomOverflowBarButtonItem","tabletop"):
            with self.subTest(placement=placement):
                # Each subcase has its own independent mock-only artifact root.
                original=self.root;self.root=original/placement;self.root.mkdir(mode=0o700)
                try:result,commands,ui,backend,udid=self.run_case(profile="iphone-duo",placement=placement)
                finally:self.root=original
                self.assertEqual(result["outcome"],"pass")
                self.assertTrue(result["finalization_disconnect"].startswith("not_attempted"))
                self.assert_mandatory_calls(commands,backend,udid)
                ui.tap.assert_not_called();ui.overflow.assert_not_called()

    def test_duo_pose_mapping_is_verified_before_each_distinct_fresh_command(self):
        result, commands, _, backend, udid = self.run_case(profile="iphone-duo")
        self.assertEqual(result["outcome"], "pass")
        self.assertEqual(result["initial_pose"]["name"], "cover")
        poses = result["scenarios"][1:]
        self.assertEqual([p["name"] for p in poses], ["unfolded", "book", "tabletop"])
        self.assertEqual([(p["pose"]["observed_rotation"], p["pose"]["observed_hinge_degrees"])
                          for p in poses], [("PORTRAIT", "180"), ("PORTRAIT", "90"), ("LANDSCAPE_LEFT", "90")])
        self.assertTrue(all(p["visual_layout"] == p["pose"]["visual_layout"] == "not asserted" for p in poses))
        inputs = commands.journey.command.call_args_list
        self.assertEqual([c.args[1] for c in inputs], ["unfolded", "book", "tabletop"])
        self.assertEqual(len({c.args[0] for c in inputs}), 3)
        self.assertTrue(all(c.args[0].startswith("smoke pose ") for c in inputs))
        self.assert_mandatory_calls(commands, backend, udid)

    def test_each_duo_pose_command_uses_a_new_visual_nonce(self):
        nonces = ["abcdefghjkmn", "bcdefghjkmnp", "cdefghjkmnpq"]
        with patch.object(smoke, "visual_nonce", side_effect=nonces) as generate:
            result, commands, _, _, _ = self.run_case(profile="iphone-duo")
        self.assertEqual(result["outcome"], "pass")
        self.assertEqual(generate.call_count, 3)
        self.assertEqual([c.args for c in commands.journey.command.call_args_list], [
            ("smoke pose " + nonce, pose) for nonce, pose in zip(nonces, ("unfolded", "book", "tabletop"))])

    def test_wrong_tabletop_native_read_blocks_without_new_input_or_retry(self):
        for action, reported in (("rotation", "PORTRAIT"), ("hinge", "190.0")):
            with self.subTest(action=action):
                original = self.root
                self.root = original / action
                self.root.mkdir(mode=0o700)
                try:
                    result, commands, _, backend, udid = self.run_case(
                        profile="iphone-duo", pose_read_failure=(action, reported))
                finally:
                    self.root = original
                self.assertEqual(result["outcome"], "blocked")
                self.assertEqual(result["scenarios"][-1]["name"], "tabletop")
                self.assertEqual(result["scenarios"][-1]["outcome"], "blocked")
                self.assertEqual([c.args[1] for c in commands.journey.command.call_args_list], ["unfolded", "book"])
                rotations = [c for c in commands.idb.call_args_list if c.args[1:] == ("rotation", "LANDSCAPE_LEFT")]
                self.assertEqual(len(rotations), 1)
                self.assert_mandatory_calls(commands, backend, udid)

    def test_terminate_failure_does_not_skip_shutdown_or_make_case_pass(self):
        result,commands,ui,backend,udid=self.run_case(terminate_failure=True)
        self.assert_mandatory_calls(commands,backend,udid)
        self.assertEqual(result["outcome"],"blocked")
        steps={step["action"]:step["outcome"] for step in result["cleanup_steps"]}
        self.assertEqual(steps["terminate-app"],"blocked");self.assertEqual(steps["shutdown-simulator"],"pass")
        self.assertEqual(result["scenarios"][0]["outcome"],"pass")

    def test_prior_failure_is_preserved_alongside_cleanup_failure(self):
        result,commands,ui,backend,udid=self.run_case(ui_failure=True,terminate_failure=True)
        self.assert_mandatory_calls(commands,backend,udid)
        self.assertEqual(result["outcome"],"fail")
        self.assertEqual(result["reason"],"Native input delivery unconfirmed; do not retry")
        self.assertEqual(result["scenarios"][0]["outcome"],"fail")
        steps={step["action"]:step["outcome"] for step in result["cleanup_steps"]}
        self.assertEqual(steps["terminate-app"],"blocked");self.assertEqual(steps["shutdown-simulator"],"pass")

    def test_shutdown_failure_is_recorded_and_does_not_skip_lease_release(self):
        result,commands,ui,backend,udid=self.run_case(shutdown_failure=True)
        self.assert_mandatory_calls(commands,backend,udid)
        self.assertEqual(result["outcome"],"blocked")
        self.assertEqual(result["cleanup_steps"][-1]["outcome"],"blocked")
        self.assertEqual(result["owned_lease_cleanup"][0]["outcome"],"pass")

    def test_failed_ownership_verification_sends_no_simulator_mutations(self):
        result,commands,ui,backend,udid=self.run_case(ownership_failure=True)
        self.assertEqual(result["outcome"],"blocked")
        self.assertFalse(any(c.args[1]=="terminate" for c in commands.idb.call_args_list))
        self.assertFalse(any(c.args[0]=="shutdown" for c in commands.simctl.call_args_list))
        self.assertEqual(result["cleanup_steps"][0]["outcome"],"blocked")
        self.assertTrue(all(step["outcome"]=="not_attempted" for step in result["cleanup_steps"][1:]))
        backend.fault.assert_called_once_with("drop-leases")


class NativeAXGuardTests(Temporary):
    def root_node(self, name="Trill", **changes):
        return element(None, role="AXApplication", label=name, **changes)

    def test_native_read_rejects_unknown_zero_missing_and_ambiguous_roots(self):
        bad = [[self.root_node(None)], [self.root_node(" ")], [],
               [self.root_node(), self.root_node("SpringBoard")],
               [self.root_node(frame={"x":0,"y":0,"width":0,"height":0})],
               [self.root_node(frame={"x":0,"y":0,"width":float("nan"),"height":60})],
               [self.root_node(enabled=False)]]
        for tree in bad:
            with self.subTest(tree=tree), self.assertRaises(smoke.Blocked):
                smoke.native_application(tree)

    def test_tree_dispatches_explicit_xctest_and_case_sensitive_voiceOver(self):
        c = Mock();c.idb.return_value = json.dumps([self.root_node()])
        ui = smoke.UI(c, "owned", self.root)
        ui.tree();ui.tree("voiceover")
        self.assertEqual([call.args[-2:] for call in c.idb.call_args_list], [
            ("--client-type", "xctest"), ("--client-type", "voiceOver")])
        self.assertTrue(all("ax-coresimulator" in call.args for call in c.idb.call_args_list))

    def test_unusable_tree_cannot_prove_presence_even_with_a_stale_button(self):
        c = Mock();c.idb.return_value = json.dumps([self.root_node(None), element("ready", role="AXButton")])
        ui = smoke.UI(c, "owned", self.root, timeout=0)
        with self.assertRaises(smoke.Blocked):ui.wait(smoke.selector("ready"))
        self.assertFalse(any(call.kwargs.get("mutate") for call in c.idb.call_args_list))

    def test_unknown_empty_or_other_application_never_proves_absence(self):
        for tree in ([], [self.root_node(None)], [self.root_node("SpringBoard")], [self.root_node()],
                     [self.root_node(),element(None,role="AXGroup",label="Container")],
                     [self.root_node(),element(None,role="AXStaticText",label="Loading…")],
                     [self.root_node(frame={"x":0,"y":0,"width":0,"height":0})]):
            with self.subTest(tree=tree):
                ui = smoke.UI(Mock(), "owned", self.root, timeout=0);ui.tree = Mock(return_value=tree)
                with self.assertRaises(smoke.Blocked):ui.wait(smoke.selector("gone"), absent=True)
                ui.c.idb.assert_not_called()

    def test_absence_requires_visible_controls_inside_the_named_app(self):
        for field in (element("ready-context",role="AXButton"),element(None,role="AXTextField",label=None)):
            with self.subTest(role=field["role"]):
                ui=smoke.UI(Mock(),"owned",self.root,timeout=1)
                ui.tree=Mock(return_value=[self.root_node(),field])
                with patch.object(smoke.time,"sleep"):ui.wait(smoke.selector("gone"),absent=True)
                ui.c.idb.assert_not_called()
        ui=smoke.UI(Mock(),"owned",self.root,timeout=0)
        outside=element("unrelated-offscreen",role="AXButton",frame={"x":200,"y":200,"width":80,"height":60})
        ui.tree=Mock(return_value=[self.root_node(),outside])
        with self.assertRaises(smoke.Blocked):ui.wait(smoke.selector("gone"),absent=True)
        ui.c.idb.assert_not_called()

    def test_unusable_read_resets_two_observation_absence_proof(self):
        valid = [self.root_node(),element("ready-context",role="AXButton")];unknown = [self.root_node(None)]
        ui = smoke.UI(Mock(), "owned", self.root, timeout=1)
        ui.tree = Mock(side_effect=[valid, unknown, unknown, valid, valid])
        with patch.object(smoke.time, "sleep"):
            ui.wait(smoke.selector("gone"), absent=True)
        self.assertEqual(ui.tree.call_count, 5)
        ui.c.idb.assert_not_called()

    def test_explicit_voiceOver_can_supply_usable_read_after_unusable_xctest(self):
        c = Mock()
        def read(*args, **kwargs):
            return json.dumps([self.root_node(),element("ready",role="AXButton")] if args[-1]=="voiceOver" else [self.root_node(None)])
        c.idb.side_effect = read;ui = smoke.UI(c, "owned", self.root, timeout=1)
        with patch.object(smoke.time, "sleep"):
            self.assertEqual(ui.wait(smoke.selector("ready"))["AXUniqueId"], "ready")
        self.assertEqual([call.args[-1] for call in c.idb.call_args_list], ["xctest","voiceOver","xctest","voiceOver"])
        self.assertFalse(any(call.kwargs.get("mutate") for call in c.idb.call_args_list))

    def reconnect(self, home=None, foreground=None):
        ui = Mock(output=self.root,timeout=0);ui.exists.return_value=False;ui.receipt.return_value=[]
        home = self.root_node("SpringBoard") if home is None else home
        foreground = self.root_node() if foreground is None else foreground
        ui.wait.side_effect=lambda target: home if target==smoke.label("SpringBoard",role="AXApplication") else foreground
        backend=Mock();backend.view.return_value=smoke.StateView(status())
        journey=smoke.Journey(ui,backend,None,{"scenarios":[]},0)
        journey.session=status()["state"]["sessions"][0];journey.command=Mock(return_value=[])
        return journey

    def test_unknown_or_wrong_home_identity_stops_before_dwell_launch_and_input(self):
        for home in (self.root_node(None),self.root_node(),self.root_node("Photos"),
                     self.root_node("SpringBoard",frame={"x":0,"y":0,"width":0,"height":0})):
            with self.subTest(home=home):
                journey=self.reconnect(home=home)
                with patch.object(smoke.time,"sleep") as sleep, self.assertRaises(smoke.Blocked):journey.reconnect()
                sleep.assert_not_called();journey.command.assert_not_called()
                self.assertEqual([call.args[1:3] for call in journey.ui.c.idb.call_args_list],[("ui","button")])
                self.assertNotIn("backgrounds",journey.manifest)

    def test_unknown_foreground_cannot_send_resumed_input(self):
        journey=self.reconnect(foreground=self.root_node(None))
        with self.assertRaises(smoke.Blocked):journey.reconnect()
        journey.command.assert_not_called()
        self.assertEqual([call.args[1] for call in journey.ui.c.idb.call_args_list],["ui","launch"])
        self.assertNotIn("backgrounds",journey.manifest)

    def test_positive_home_precedes_dwell_and_positive_trill_precedes_fresh_input(self):
        journey=self.reconnect();order=[]
        journey.ui.c.idb.side_effect=lambda _udid, action,*args,**kwargs: order.append(action)
        wait=journey.ui.wait.side_effect
        def observed(target):order.append("read-"+target["value"]);return wait(target)
        journey.ui.wait.side_effect=observed
        journey.ui.receipt.side_effect=lambda name,**kwargs: order.append(name) or []
        journey.command.side_effect=lambda *args:order.append("fresh-input") or []
        journey.reconnect()
        self.assertEqual(order,["ui","read-SpringBoard","background-native-springboard","launch",
                                "read-Trill","foreground-native-trill","read-terminal-return","fresh-input"])
        metric=journey.manifest["backgrounds"][0]
        self.assertEqual(metric["background_application"]["name"],"SpringBoard")
        self.assertEqual(metric["foreground_application"]["name"],"Trill")
        journey.command.assert_called_once()


class ScopedRenameFieldTests(Temporary):
    def setUp(self):
        super().setUp()
        sleeper = patch.object(smoke.time, "sleep")
        sleeper.start();self.addCleanup(sleeper.stop)

    def native(self, *, nested=False):
        sheet_selector = smoke.label('Rename “Demo terminal”?', role="AXSheet")
        frame = {"x": 41, "y": 186, "width": 320, "height": 236}
        state = {"value": "Demo terminal", "search": "Demo terminal", "frame": dict(frame),
                 "missing": False, "duplicate": False, "extra_field": False, "wrong_role": False,
                 "move_on_focus": False, "misdirect_edit": False}
        target = smoke.within(smoke.selector(None, "AXLabel", role="AXTextField"), sheet_selector, frame)
        ui = smoke.UI(Mock(), "owned", self.root, timeout=0.01)
        def tree(*args):
            search = element(None, role="AXTextField", label=None, value=state["search"],
                             frame={"x": 28, "y": 808, "width": 286, "height": 28})
            field = element(None, role="AXTextField", label=None, value=state["value"],
                            frame={"x": 71, "y": 297, "width": 260, "height": 35})
            sheet = element(None, role="AXGroup" if state["wrong_role"] else "AXSheet",
                            label='Rename “Demo terminal”?', frame=dict(state["frame"]))
            fields = [field, {**field, "frame": {**field["frame"], "y": 340}}] if state["extra_field"] else [field]
            nodes = [search]
            if not state["missing"]:
                if nested:sheet["children"] = fields
                nodes += [sheet]
                if state["duplicate"]:nodes += [dict(sheet)]
            if not nested:nodes += fields
            return nodes
        def idb(udid, *args, **kwargs):
            self.assertEqual(udid, "owned")
            self.assertEqual(kwargs, {"mutate": True})
            if args[:2] == ("ui", "tap"):
                self.assertEqual(args[2:4], ("201", "314"))
                if state["move_on_focus"]:state["frame"]["y"] += 1
            elif args[:2] == ("ui", "set-value"):
                state["search" if state["misdirect_edit"] else "value"] = args[-1]
            elif args[:3] == ("ui", "key", "42"):
                key = "search" if state["misdirect_edit"] else "value"
                state[key] = state[key][:-1]
            else:self.fail("Unexpected scoped edit command")
            return ""
        ui.tree = Mock(side_effect=tree);ui.c.idb.side_effect = idb
        return ui, target, state

    def test_flat_and_nested_alert_field_remain_identifiable_after_value_changes(self):
        for nested in (False, True):
            with self.subTest(nested=nested):
                ui, target, state = self.native(nested=nested)
                with patch.object(smoke.time, "sleep"):
                    ui.replace_once(target, "smoke run abcdefgh", "Demo terminal")
                    self.assertEqual(ui.value(target), "smoke run abcdefgh")
                self.assertEqual(state["search"], "Demo terminal")
                self.assertEqual([c.args[1:3] for c in ui.c.idb.call_args_list], [
                    ("ui", "tap"), ("ui", "set-value"), ("ui", "key")])

    def test_missing_ambiguous_moved_or_wrong_sheet_sends_no_input(self):
        for problem in ("missing", "duplicate", "moved", "wrong_role", "extra_field"):
            with self.subTest(problem=problem):
                ui, target, state = self.native()
                if problem == "moved":state["frame"]["y"] += 1
                else:state[problem] = True
                with self.assertRaises(smoke.Blocked):
                    ui.replace_once(target, "smoke run abcdefgh", "Demo terminal")
                ui.c.idb.assert_not_called()

    def test_another_ax_client_cannot_rescue_a_changed_pinned_container(self):
        ui, target, _ = self.native()
        original = ui.tree.side_effect
        valid = original()
        ui.tree.side_effect = lambda mode="xctest": valid if mode == "voiceover" else []
        with self.assertRaises(smoke.ScopedContainerChanged):
            ui.replace_once(target, "smoke run abcdefgh", "Demo terminal")
        ui.tree.assert_called_once_with()
        ui.c.idb.assert_not_called()

    def test_sheet_moving_after_focus_blocks_before_any_edit(self):
        ui, target, state = self.native();state["move_on_focus"] = True
        with self.assertRaises(smoke.Blocked):ui.replace_once(target, "smoke run abcdefgh", "Demo terminal")
        self.assertEqual([c.args[1:3] for c in ui.c.idb.call_args_list], [("ui", "tap")])
        self.assertEqual(state["value"], "Demo terminal")

    def test_underlying_search_matching_new_name_cannot_satisfy_alert_readback(self):
        ui, target, state = self.native();state["misdirect_edit"] = True
        with patch.object(smoke.time, "sleep"), self.assertRaises(smoke.Uncertain):
            ui.replace_once(target, "smoke run abcdefgh", "Demo terminal")
        self.assertEqual(state["search"], "smoke run abcdefgh")
        self.assertEqual(state["value"], "Demo terminal")
        self.assertEqual(len(ui.c.idb.call_args_list), 3)  # No edit retry.

    def lifecycle_rename(self, readback):
        ui = Mock(output=self.root)
        ui.wait.return_value = element(None, role="AXSheet", label='Rename “Demo terminal”?',
                                       frame={"x": 41, "y": 186, "width": 320, "height": 236})
        ui.value.return_value = readback
        backend = Mock();backend.view.return_value = smoke.StateView(status())
        journey = smoke.Journey(ui, backend, None, {})
        journey.to_sessions = Mock();journey.search_sessions = Mock()
        journey.assertion = Mock(side_effect=lambda name, fn: fn() if name.startswith("rename through") else [])
        order = Mock();order.attach_mock(ui.replace_once, "replace");order.attach_mock(ui.value, "readback");order.attach_mock(ui.tap, "tap")
        return journey, order

    def test_lifecycle_rechecks_exact_scoped_draft_before_save_and_backend_equality(self):
        journey, order = self.lifecycle_rename("smoke run abcdefgh")
        with patch.object(smoke.uuid, "uuid4", return_value=Mock(hex="abcdefgh")):
            journey.lifecycle(existing_session=status()["state"]["sessions"][0])
        target = journey.ui.replace_once.call_args.args[0]
        self.assertEqual(target["container"]["selector"], smoke.label('Rename “Demo terminal”?', role="AXSheet"))
        self.assertNotIn("AXValue", target)
        self.assertEqual([(c[0],c.args) for c in order.mock_calls[-3:]], [
            ("replace",(target,"smoke run abcdefgh","Demo terminal")),
            ("readback",(target,)), ("tap",(smoke.label("Save"),))])
        predicate = journey.backend.wait.call_args.args[0]
        changed=status();changed["state"]["sessions"][0]["name"]="smoke run abcdefgh"
        self.assertTrue(predicate(smoke.StateView(changed)))
        changed["state"]["sessions"][0]["name"]="Smoke run abcdefgh"
        self.assertFalse(predicate(smoke.StateView(changed)))

    def test_changed_draft_at_final_readback_never_saves(self):
        journey, _ = self.lifecycle_rename("partial prediction")
        with patch.object(smoke.uuid, "uuid4", return_value=Mock(hex="abcdefgh")), self.assertRaises(smoke.Uncertain):
            journey.lifecycle(existing_session=status()["state"]["sessions"][0])
        self.assertNotIn(smoke.label("Save"), [c.args[0] for c in journey.ui.tap.call_args_list])
        journey.backend.wait.assert_not_called()


class VisualHeadingNonceTests(Temporary):
    def test_nonce_excludes_ambiguous_glyphs_retains_entropy_and_is_fresh(self):
        alphabet = smoke.VISUAL_NONCE_ALPHABET
        self.assertTrue(alphabet.isascii() and alphabet.isalpha() and alphabet.islower())
        self.assertFalse(set(alphabet) & set("0123456789oilOIL"))
        self.assertEqual(len(set(alphabet)), len(alphabet))
        self.assertGreaterEqual(len(alphabet) ** smoke.VISUAL_NONCE_LENGTH, 2 ** 48)
        samples = "abcdefghjkmn" + "bcdefghjkmnp"
        with patch.object(smoke.secrets, "choice", side_effect=samples) as random_letter:
            first, second = smoke.visual_nonce(), smoke.visual_nonce()
        self.assertEqual((first, second), ("abcdefghjkmn", "bcdefghjkmnp"))
        self.assertNotEqual(first, second)
        self.assertEqual(len(first), smoke.VISUAL_NONCE_LENGTH)
        self.assertEqual(random_letter.call_count, 2 * smoke.VISUAL_NONCE_LENGTH)
        self.assertTrue(all(c.args == (alphabet,) for c in random_letter.call_args_list))

    def test_initial_resume_attach_recovery_and_expiry_each_generate_distinct_markers(self):
        ui = Mock(output=self.root, timeout=0)
        ui.exists.return_value = False
        ui.tree.return_value = [element(None, role="AXApplication", label="SpringBoard")]
        ui.wait.side_effect = lambda target: element(None, role="AXApplication", label=target["value"])
        ui.receipt.return_value = []
        ui.observe_error.return_value = []
        ui.visible_text.return_value = []
        backend = Mock(identity=smoke.validate_status(status()))
        backend.view.return_value = smoke.StateView(status())
        backend.status.return_value = {"state": {"faults": []}}
        journey = smoke.Journey(ui, backend, None, {"scenarios": []}, background_seconds=0)
        journey.session = status()["state"]["sessions"][0]
        journey.pair = Mock(return_value=journey.session)
        journey.open_terminal = Mock()
        journey.live_preview = Mock()
        journey.source = Mock()
        journey.static_preview = Mock()
        journey.disconnect = Mock()
        journey.command = Mock(return_value=[])
        journey.assertion = Mock(side_effect=lambda name, function: function())
        nonces = ["abcdefghjkmn", "bcdefghjkmnp", "cdefghjkmnpq", "defghjkmnpqr", "efghjkmnpqrs", "fghjkmnpqrst"]
        with patch.object(smoke, "visual_nonce", side_effect=nonces) as generate:
            journey.happy()  # Initial heading, then resume.
            journey.fault("capacity")
            journey.fault("busy")
            journey.fault("drop-leases")  # The same verified resume path.
            journey.fault("preview-expired")
        expected_prefixes = ["smoke heading ", "smoke resumed ", "smoke recovered ",
                             "smoke recovered ", "smoke resumed ", "smoke renewed "]
        expected = [prefix + nonce for prefix, nonce in zip(expected_prefixes, nonces)]
        self.assertEqual([c.args[0] for c in journey.command.call_args_list], expected)
        self.assertEqual(generate.call_count, len(expected))
        self.assertEqual(len(set(expected)), len(expected))
        ui.visible_text.assert_called_once_with(expected[-1], "preview-renewed-fresh-heading")


class DuoPoseRegressionTests(unittest.TestCase):
    def native(self, *, rotation=None, hinge=None):
        commands = Mock()
        state = {}
        def idb(udid, action, *args, **kwargs):
            self.assertEqual(udid, "owned")
            if args:
                self.assertEqual(kwargs, {"mutate": True})
                state[action] = args[0]
                return ""
            self.assertEqual(kwargs, {})
            return rotation if action == "rotation" and rotation is not None else (
                hinge if action == "hinge" and hinge is not None else state[action])
        commands.idb.side_effect = idb
        return commands

    def test_pose_names_require_exact_native_rotation_and_angle_mapping(self):
        self.assertEqual(smoke.DUO_POSES, (("unfolded", "PORTRAIT", 180),
                                         ("book", "PORTRAIT", 90), ("tabletop", "LANDSCAPE_LEFT", 90)))
        for name, rotation, angle in (("cover", "PORTRAIT", 0), *smoke.DUO_POSES):
            with self.subTest(name=name):
                commands = self.native(hinge=str(angle) + ".0\n", rotation=rotation + "\n")
                pose = smoke.set_duo_pose(commands, "owned", rotation, angle)
                self.assertEqual(pose["observed_rotation"], rotation)
                self.assertEqual(pose["observed_hinge_degrees"], str(angle) + ".0")
                self.assertEqual(pose["visual_layout"], "not asserted")
                self.assertEqual([c.args[1:] for c in commands.idb.call_args_list], [
                    ("rotation", rotation), ("hinge", str(angle)), ("rotation",), ("hinge",)])

    def test_wrong_rotation_or_hinge_read_is_never_accepted_as_substring(self):
        for rotation, hinge in (("PORTRAIT", "90"), ("LANDSCAPE_RIGHT", "90"),
                                ("LANDSCAPE_LEFT extra", "90"), ("LANDSCAPE_LEFT", "190"),
                                ("LANDSCAPE_LEFT", "90.0000000000000000001"),
                                ("LANDSCAPE_LEFT", "angle 90.0"), ("LANDSCAPE_LEFT", "NaN"),
                                ("LANDSCAPE_LEFT", "Infinity"), ("LANDSCAPE_LEFT", "")):
            with self.subTest(rotation=rotation, hinge=hinge):
                commands = self.native(rotation=rotation, hinge=hinge)
                with self.assertRaises(smoke.Blocked):
                    smoke.set_duo_pose(commands, "owned", "LANDSCAPE_LEFT", 90)
                self.assertEqual(len(commands.idb.call_args_list), 4)
                self.assertEqual(sum(bool(c.kwargs.get("mutate")) for c in commands.idb.call_args_list), 2)

    def test_failed_pose_mutation_is_not_retried(self):
        commands = Mock()
        commands.idb.side_effect = smoke.Uncertain("Mock rotation delivery unconfirmed")
        with self.assertRaises(smoke.Uncertain):
            smoke.set_duo_pose(commands, "owned", "LANDSCAPE_LEFT", 90)
        commands.idb.assert_called_once_with("owned", "rotation", "LANDSCAPE_LEFT", mutate=True)


class KeyboardOnboardingTests(Temporary):
    """Exercise the real preparation and input paths; only IDB transport is mocked."""
    def setUp(self):
        super().setUp()
        sleeper = patch.object(smoke.time, "sleep")
        sleeper.start()
        self.addCleanup(sleeper.stop)

    def editor(self, *, tip=True, value="", role="AXTextArea"):
        state = {"tip": tip, "value": value, "role": role, "buttons": 1 if tip else 0,
                 "copy": smoke.KEYBOARD_TUTORIAL, "after_continue": None, "fail_text": False,
                 "frame": {"x": 36, "y": 186, "width": 318, "height": 156}}
        ui = smoke.UI(Mock(), "owned", self.root, timeout=0.01)
        ui.tap = Mock()  # The initial field focus is independently guarded by UI.tap.
        def tree(*args):
            field = element(value=state["value"], role=state["role"], frame=dict(state["frame"]))
            nodes = [element(None, role="AXApplication", label="Trill",frame={"x":0,"y":0,"width":402,"height":874}), field,
                     element(value=state["value"], role="AXStaticText")]
            if state["tip"]:
                nodes.append(element(None, role="AXStaticText", label=state["copy"]))
            for index in range(state["buttons"]):
                nodes.append(element(None, role="AXButton", label="Continue",
                                     frame={"x": 86, "y": 613 + 40 * index, "width": 293, "height": 33}))
            if state["buttons"]:
                nodes.append(element(None, role="AXStaticText", label="Continue"))
            return nodes
        def dispatch(udid, *args, **kwargs):
            self.assertEqual(udid, "owned")
            self.assertEqual(kwargs, {"mutate": True})
            if args[:2] == ("ui", "tap"):
                self.assertEqual(args[-4:], ("--expected-key", "AXLabel", "--expected-value", "Continue"))
                self.assertEqual(state["value"], value)  # No early clear or replacement.
                state["tip"], state["buttons"] = False, 0
                if state["after_continue"] is not None:
                    state["value"] = state["after_continue"]
            elif args[:2] == ("ui", "text"):
                if state["fail_text"]:
                    raise smoke.Uncertain("Mock invalidated HID connection; delivery unconfirmed")
                state["value"] = args[2]
            elif args[:2] == ("ui", "set-value"):
                state["value"] = args[-1]
            elif args[:3] == ("ui", "key", "42"):
                state["value"] = state["value"][:-1]
            elif args[:3] != ("ui", "key", "4"):
                self.fail("Unexpected input command")
            return ""
        ui.tree = Mock(side_effect=tree)
        ui.c.idb.side_effect = dispatch
        return ui, state

    def writes(self, ui):
        return [call.args[1:3] for call in ui.c.idb.call_args_list]

    def test_no_tutorial_uses_normal_single_text_write(self):
        ui, state = self.editor(tip=False)
        ui.type_once(smoke.selector("terminal-draft"), "demo heading normal")
        self.assertEqual(state["value"], "demo heading normal")
        self.assertEqual(self.writes(ui), [("ui", "text")])

    def test_no_tutorial_replacement_keeps_normal_single_edit_path(self):
        ui, state = self.editor(tip=False, value="old draft", role="AXTextField")
        ui.replace_once(smoke.selector("terminal-draft"), "new draft", "old draft")
        self.assertEqual(state["value"], "new draft")
        self.assertEqual(self.writes(ui), [("ui", "key"), ("ui", "text")])

    def test_disabled_continue_is_not_an_available_tutorial_action(self):
        ui, _ = self.editor()
        tree = ui.tree.side_effect
        def disabled(*args):
            nodes = tree(*args)
            for node in smoke.matching(nodes, smoke.label("Continue")):
                node["enabled"] = False
            return nodes
        ui.tree.side_effect = disabled
        with self.assertRaises(smoke.Blocked):
            ui.type_once(smoke.selector("terminal-draft"), "demo heading blocked")
        ui.c.idb.assert_not_called()

    def test_exact_tutorial_and_button_descendant_dismiss_once_before_typing(self):
        ui, state = self.editor()
        ui.type_once(smoke.selector("terminal-draft"), "demo heading tutorial")
        self.assertEqual(state["value"], "demo heading tutorial")
        self.assertEqual(self.writes(ui), [("ui", "tap"), ("ui", "text")])

    def test_existing_draft_preserved_until_tutorial_dismissed_and_rechecked(self):
        for role in ("AXTextArea", "AXTextField"):
            with self.subTest(role=role):
                ui, state = self.editor(value="old draft", role=role)
                ui.replace_once(smoke.selector("terminal-draft"), "new draft", "old draft")
                self.assertEqual(state["value"], "new draft")
                expected = [("ui", "tap"), ("ui", "set-value"), ("ui", "key")] if role == "AXTextArea" else [
                    ("ui", "tap"), ("ui", "key"), ("ui", "text")]
                self.assertEqual(self.writes(ui), expected)

    def test_unrelated_continue_or_ambiguous_buttons_never_dismiss_or_type(self):
        for changes in ({"tip": False}, {"copy": "A different tutorial"}, {"buttons": 2}, {"buttons": 0}):
            with self.subTest(changes=changes):
                ui, state = self.editor()
                state.update(changes)
                with self.assertRaises(smoke.Blocked):
                    ui.type_once(smoke.selector("terminal-draft"), "demo heading blocked")
                ui.c.idb.assert_not_called()

    def test_changed_draft_after_continue_blocks_text_and_both_replacement_paths(self):
        for role, replace in (("AXTextArea", False), ("AXTextArea", True), ("AXTextField", True)):
            with self.subTest(role=role, replace=replace):
                previous = "old draft" if replace else ""
                ui, state = self.editor(value=previous, role=role)
                state["after_continue"] = "unexpected composition"
                with self.assertRaises(smoke.Uncertain):
                    if replace:
                        ui.replace_once(smoke.selector("terminal-draft"), "new draft", previous)
                    else:
                        ui.type_once(smoke.selector("terminal-draft"), "demo heading blocked")
                self.assertEqual(self.writes(ui), [("ui", "tap")])
                self.assertEqual(state["value"], "unexpected composition")

    def test_editor_change_or_ambiguity_before_continue_sends_no_action(self):
        for problem in ("value", "geometry", "ambiguous", "role"):
            with self.subTest(problem=problem):
                ui, state = self.editor()
                wait = ui.wait
                def changed(target, **kwargs):
                    result = wait(target, **kwargs)
                    if target == smoke.label("Continue"):
                        if problem == "value": state["value"] = "changed draft"
                        elif problem == "geometry": state["frame"]["y"] += 10
                        elif problem == "role": state["role"] = "AXButton"
                        else:
                            tree = ui.tree.side_effect
                            ui.tree.side_effect = lambda *args: tree(*args) + [element()]
                    return result
                ui.wait = Mock(side_effect=changed)
                with self.assertRaises((smoke.Blocked, smoke.Uncertain)):
                    ui.type_once(smoke.selector("terminal-draft"), "demo heading blocked")
                ui.c.idb.assert_not_called()

    def test_failed_text_after_tutorial_is_never_retried(self):
        ui, state = self.editor()
        state["fail_text"] = True
        with self.assertRaises(smoke.Uncertain):
            ui.type_once(smoke.selector("terminal-draft"), "demo heading once")
        self.assertEqual(self.writes(ui), [("ui", "tap"), ("ui", "text")])
        self.assertEqual(state["value"], "")

    def test_tutorial_that_remains_visible_stops_after_one_continue(self):
        ui, state = self.editor()
        def did_not_dismiss(*args, **kwargs):
            return ""
        ui.c.idb.side_effect = did_not_dismiss
        with self.assertRaises(smoke.Blocked):
            ui.type_once(smoke.selector("terminal-draft"), "demo heading blocked")
        self.assertEqual(self.writes(ui), [("ui", "tap")])


class CatalogSearchActivationTests(Temporary):
    SESSIONS = "Sessions, projects, or branches"
    PROJECTS = "Projects, sessions, or branches"
    PROJECT_SESSIONS = "Sessions, agents, or branches"

    def setUp(self):
        super().setUp()
        sleeper = patch.object(smoke.time, "sleep")
        sleeper.start()
        self.addCleanup(sleeper.stop)

    def catalog(self, prompt=None, *, expanded=False, unlabeled=False):
        prompt = prompt or self.SESSIONS
        state = {"expanded": expanded, "search_buttons": 1, "wrong_context": False,
                 "modal": None, "project_context": True}
        ui = smoke.UI(Mock(), "owned", self.root, timeout=0.01)
        def tree(*args):
            if prompt == self.SESSIONS:
                nodes = [element("open-projects", role="AXButton", label="Projects")]
            else:
                nodes = [element("close-projects", role="AXButton", label="Close")]
                if state["project_context"]:
                    titles = ["Projects"] if prompt == self.PROJECTS else ["Current", "Archived"]
                    nodes += [element(None, role="AXStaticText", label=title) for title in titles]
            if state["expanded"]:
                field = element(None, role="AXTextField", label=None if unlabeled else prompt, value="")
                nodes += [field, element("magnifyingglass", role="AXImage", label="Search",
                                        frame={"x": 25, "y": 45, "width": 10, "height": 10})]
            else:
                nodes += [element(None, role="AXButton", label="Search",
                                  frame={"x": 10, "y": 200 + 40 * index, "width": 30, "height": 30})
                          for index in range(state["search_buttons"])]
            if prompt != self.SESSIONS:
                nodes = [{"role": "AXSheet", "children": nodes}]
            if state["wrong_context"]:
                nodes.append(element("preview-address", role="AXTextField", label="Address"))
            if state["modal"]:
                nodes.append({"role": state["modal"], "children": []})
            return nodes
        def dispatch(udid, *args, **kwargs):
            self.assertEqual(udid, "owned")
            self.assertEqual(args[:2], ("ui", "tap"))
            self.assertEqual(args[-4:], ("--expected-key", "AXLabel", "--expected-value", "Search"))
            self.assertEqual(kwargs, {"mutate": True})
            self.assertFalse(state["expanded"])
            state["expanded"] = True
            return ""
        ui.tree = Mock(side_effect=tree)
        ui.c.idb.side_effect = dispatch
        return ui, state

    def test_side_rail_search_activated_once_then_field_verified(self):
        for unlabeled in (False, True):
            with self.subTest(unlabeled=unlabeled):
                ui, state = self.catalog(unlabeled=unlabeled)
                target = ui.search_field(self.SESSIONS)
                self.assertTrue(state["expanded"])
                self.assertEqual(len(smoke.matching(ui.tree(), target)), 1)
                ui.c.idb.assert_called_once()

    def test_existing_labeled_or_unlabeled_field_never_retaps_search(self):
        for unlabeled in (False, True):
            with self.subTest(unlabeled=unlabeled):
                ui, _ = self.catalog(expanded=True, unlabeled=unlabeled)
                ui.search_field(self.SESSIONS)
                ui.c.idb.assert_not_called()

    def test_missing_or_ambiguous_search_sends_no_action(self):
        for count in (0, 2):
            with self.subTest(count=count):
                ui, state = self.catalog()
                state["search_buttons"] = count
                with self.assertRaises(smoke.Blocked): ui.search_field(self.SESSIONS)
                ui.c.idb.assert_not_called()

    def test_disabled_search_or_missing_catalog_anchor_sends_no_action(self):
        for problem in ("disabled", "missing_catalog"):
            with self.subTest(problem=problem):
                ui, _ = self.catalog()
                tree = ui.tree.side_effect
                def unavailable(*args):
                    nodes = tree(*args)
                    if problem == "missing_catalog":
                        return [node for node in nodes if node.get("AXUniqueId") != "open-projects"]
                    for node in smoke.matching(nodes, smoke.label("Search")):
                        node["enabled"] = False
                    return nodes
                ui.tree.side_effect = unavailable
                with self.assertRaises(smoke.Blocked): ui.search_field(self.SESSIONS)
                ui.c.idb.assert_not_called()

    def test_browser_or_unknown_modal_sends_no_action(self):
        for changes in ({"wrong_context": True}, {"modal": "AXDialog"}, {"modal": "AXSheet"}):
            with self.subTest(changes=changes):
                ui, state = self.catalog()
                state.update(changes)
                with self.assertRaises(smoke.Blocked): ui.search_field(self.SESSIONS)
                ui.c.idb.assert_not_called()

    def test_unknown_catalog_and_unobserved_projects_context_block(self):
        for prompt in ("Unknown search", self.PROJECTS, self.PROJECT_SESSIONS):
            with self.subTest(prompt=prompt):
                ui, state = self.catalog(prompt)
                state["project_context"] = False
                with self.assertRaises(smoke.Blocked): ui.search_field(prompt)
                ui.c.idb.assert_not_called()

    def test_hidden_projects_heading_does_not_establish_current_context(self):
        ui, _ = self.catalog(self.PROJECTS)
        tree = ui.tree.side_effect
        def hidden(*args):
            nodes = tree(*args)
            for node in smoke.elements(nodes):
                if node.get("role") == "AXStaticText": node["frame"]["height"] = 0
            return nodes
        ui.tree.side_effect = hidden
        with self.assertRaises(smoke.Blocked): ui.search_field(self.PROJECTS)
        ui.c.idb.assert_not_called()

    def test_projects_list_and_current_archived_contexts_activate_search_once(self):
        for prompt in (self.PROJECTS, self.PROJECT_SESSIONS):
            with self.subTest(prompt=prompt):
                ui, _ = self.catalog(prompt)
                ui.search_field(prompt)
                ui.c.idb.assert_called_once()

    def test_changed_context_or_moved_button_blocks_before_activation(self):
        for problem in ("context", "geometry"):
            with self.subTest(problem=problem):
                ui, state = self.catalog()
                wait = ui.wait
                def changed(target, **kwargs):
                    result = wait(target, **kwargs)
                    if target == smoke.label("Search"):
                        if problem == "context": state["modal"] = "AXSheet"
                        else:
                            tree = ui.tree.side_effect
                            def moved(*args):
                                nodes = tree(*args)
                                for node in smoke.matching(nodes, smoke.label("Search")):
                                    node["frame"]["y"] += 10
                                return nodes
                            ui.tree.side_effect = moved
                    return result
                ui.wait = Mock(side_effect=changed)
                with self.assertRaises(smoke.Blocked): ui.search_field(self.SESSIONS)
                ui.c.idb.assert_not_called()

    def test_field_appearing_during_readiness_does_not_trigger_second_activation(self):
        ui, state = self.catalog()
        wait = ui.wait
        def appeared(target, **kwargs):
            result = wait(target, **kwargs)
            if target == smoke.label("Search"): state["expanded"] = True
            return result
        ui.wait = Mock(side_effect=appeared)
        ui.search_field(self.SESSIONS)
        ui.c.idb.assert_not_called()

    def test_guarded_search_failure_is_not_retried(self):
        ui, _ = self.catalog()
        ui.c.idb.side_effect = smoke.Uncertain("Search dispatch unconfirmed")
        with self.assertRaises(smoke.Uncertain): ui.search_field(self.SESSIONS)
        ui.c.idb.assert_called_once()


if __name__ == "__main__":
    unittest.main()
