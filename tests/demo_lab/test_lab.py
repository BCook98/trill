import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("demo_lab", ROOT / "scripts/demo-lab.py")
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)

class DemoLabSafetyTests(unittest.TestCase):
    def test_unowned_state_is_rejected_without_starting_anything(self):
        with tempfile.TemporaryDirectory() as value:
            path = Path(value)
            with patch.object(lab, "command") as command:
                with self.assertRaises((RuntimeError, FileNotFoundError)):
                    lab.start(path, 19443, 18080)
                command.assert_not_called()

    def test_production_or_shared_ports_are_rejected(self):
        with tempfile.TemporaryDirectory() as value:
            for api, preview in [(7443, 18080), (19443, 7443), (19443, 19443), (80, 18080)]:
                with self.assertRaises(RuntimeError):
                    lab.start(Path(value) / "lab", api, preview)
            self.assertFalse((Path(value) / "lab").exists())

    def test_pid_reuse_never_kills_an_unrelated_process(self):
        item = {"pid": 12345, "identity": "original process"}
        with patch.object(lab, "process_identity", return_value="different process"), patch.object(os, "kill") as kill:
            lab.stop_owned_process(item)
            kill.assert_not_called()

    def test_marker_cannot_be_reused_for_another_directory(self):
        with tempfile.TemporaryDirectory() as value:
            path = Path(value)
            lab.private_json(path / "owner.json", {"kind": lab.KIND, "state_dir": "/another/lab"})
            with self.assertRaises(RuntimeError):
                lab.owned(path)

    def test_interrupted_old_write_does_not_block_atomic_update(self):
        with tempfile.TemporaryDirectory() as value:
            path = Path(value) / "owner.json"
            path.with_suffix(".tmp").write_text("interrupted old write")
            lab.private_json(path, {"version": 2})
            self.assertEqual(json.loads(path.read_text()), {"version": 2})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(list(path.parent.glob("owner.json.*.tmp")), [])

    def test_stale_codex_flag_does_not_claim_an_unrelated_process(self):
        with tempfile.TemporaryDirectory() as value:
            data = {"codex_started": True, "session_name": lab.SESSION}
            with patch.object(lab, "demo_panes", return_value=[[lab.SESSION, "%0", "vim"]]), patch.object(lab.shutil, "which", return_value="/bin/codex"), patch.object(lab, "command") as command:
                with self.assertRaisesRegex(RuntimeError, "not idle"):
                    lab.start_codex(Path(value), data)
                command.assert_not_called()

    def test_live_codex_is_reused_without_sending_input(self):
        data = {"session_name": lab.SESSION}
        with patch.object(lab, "demo_panes", return_value=[[lab.SESSION, "%0", "codex"]]), patch.object(lab, "command") as command:
            self.assertTrue(lab.start_codex(Path("/private/demo"), data)["already_started"])
            command.assert_not_called()

    def test_failed_agent_launch_is_not_persisted_as_success(self):
        with tempfile.TemporaryDirectory() as value:
            project = Path(value) / "project"
            (project / ".git").mkdir(parents=True)
            data = {"session_name": lab.SESSION, "files_root": str(project), "preview_url": "http://localhost:18080/index.html"}
            with patch.object(lab, "demo_panes", return_value=[[lab.SESSION, "%0", "zsh"]]), patch.object(lab.shutil, "which", return_value="/bin/codex"), patch.object(lab, "command"), patch.object(lab.time, "sleep"), patch.object(lab, "private_json") as persist:
                with self.assertRaisesRegex(RuntimeError, "not confirmed"):
                    lab.start_codex(Path(value), data)
                persist.assert_not_called()
                self.assertNotIn("codex_started", data)

    def test_state_is_private_and_public_metadata_omits_processes_and_keys(self):
        with tempfile.TemporaryDirectory() as value:
            path = Path(value)
            data = {"kind": lab.KIND, "state_dir": str(path), "address": "https://localhost:19443", "name": "Demo Mac", "session_name": lab.SESSION, "preview_url": "http://localhost:18080", "files_root": str(path / "project"), "processes": {"x": {"pid": 1}}, "fingerprint": "pin"}
            lab.private_json(path / "owner.json", data)
            self.assertEqual((path / "owner.json").stat().st_mode & 0o777, 0o600)
            self.assertEqual(lab.owned(path), data)
            self.assertNotIn("processes", lab.public_state(data))
            self.assertNotIn("fingerprint", lab.public_state(data))

if __name__ == "__main__":
    unittest.main()
