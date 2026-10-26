#!/usr/bin/env python3
"""Capture real Trill demos on explicitly owned iOS simulators (stdlib only)."""
from __future__ import annotations

import argparse
import contextlib
import datetime as dt
import fcntl
import hashlib
from html.parser import HTMLParser
import importlib.util
import json
import math
import os
from pathlib import Path
import plistlib
import re
import select
import stat
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
BUNDLE = "io.bcook.trill"
PROTECTED_SIMULATORS_PATH = ROOT / ".local/protected-simulators.json"
_PROTECTION_SPEC = importlib.util.spec_from_file_location("simulator_protection", ROOT / "scripts/simulator_protection.py")
_PROTECTION_MODULE = importlib.util.module_from_spec(_PROTECTION_SPEC)
_PROTECTION_SPEC.loader.exec_module(_PROTECTION_MODULE)
load_protected_simulator_ids = _PROTECTION_MODULE.load_protected_simulator_ids
SECRET = re.compile(r"trill://pair\S*|\bBearer\s+\S+|\b[a-fA-F0-9]{64}\b", re.I)
AGENT_PROMPTS = ("do you trust", "trust this folder", "trust this directory", "approve this",
                 "approval required", "would you like to run", "allow this command", "yes, proceed",
                 "press enter to continue")
FORBIDDEN = ("Pair a computer", "Pair computer", "Pairing token", "Allow once", "Open your terminal")


class DemoError(Exception):
    pass


def read_json(path):
    try:
        return json.loads(Path(path).read_text())
    except (ValueError, OSError) as exc:
        raise DemoError(f"Cannot read JSON file: {path}") from exc


def write_json(path, data):
    path = Path(path)
    temp = path.with_name(path.name + "." + uuid.uuid4().hex + ".tmp")
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w") as file:
            json.dump(data, file, indent=2, sort_keys=True)
            file.write("\n")
        os.replace(temp, path)
    finally:
        temp.unlink(missing_ok=True)


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as file:
        for block in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def png_size(path):
    header = Path(path).read_bytes()[:24]
    if len(header) != 24 or header[:8] != b"\x89PNG\r\n\x1a\n" or header[12:16] != b"IHDR":
        raise DemoError("IDB did not return a PNG screenshot")
    return list(struct.unpack(">II", header[16:24]))


def timestamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def bounded(value, low, high, name):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or not low <= value <= high:
        raise DemoError(f"{name} must be between {low} and {high}")
    return value


def safe_name(value):
    if not isinstance(value, str) or not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,63}", value):
        raise DemoError("Names must contain only lowercase letters, numbers and hyphens")
    return value


@contextlib.contextmanager
def lock(path):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open("a+") as file:
        try:
            fcntl.flock(file, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise DemoError(f"Resource is busy; lock: {path}") from exc
        file.seek(0)
        file.truncate()
        file.write(json.dumps({"pid": os.getpid(), "started_at": timestamp()}))
        file.flush()
        try:
            yield
        finally:
            fcntl.flock(file, fcntl.LOCK_UN)


def simulator_lock(udid):
    # Global across worktrees/state directories. Never remove a live lock inode.
    return Path(tempfile.gettempdir()) / f"trill-demo-{os.getuid()}" / f"{udid}.lock"


def assets(folder):
    return {path.stem: read_json(path) for path in sorted((ROOT / "demos" / folder).glob("*.json"))}


def validate_profile(profile):
    safe_name(profile["id"])
    if not profile["device_type"].startswith("com.apple.CoreSimulator.SimDeviceType."):
        raise DemoError("Invalid device type")
    if not profile["runtime"].startswith("com.apple.CoreSimulator.SimRuntime.iOS-"):
        raise DemoError("Invalid runtime")
    if "orientation" in profile and (
            not isinstance(profile["orientation"], str) or profile["orientation"] not in {
                "PORTRAIT", "PORTRAIT_UPSIDE_DOWN", "LANDSCAPE_LEFT", "LANDSCAPE_RIGHT"}):
        raise DemoError("Invalid profile orientation")
    names = []
    for pose in profile["poses"]:
        names.append(safe_name(pose["id"]))
        if pose.get("hinge") is not None:
            bounded(pose["hinge"], 0, 180, "hinge")
    if not names or len(names) != len(set(names)):
        raise DemoError("Profile must have unique poses")


def bounded_text(value):
    values = value.values() if isinstance(value, dict) and set(value) == {"light", "dark"} else [value]
    for text in values:
        if not isinstance(text, str) or not 1 <= len(text) <= 512 or not text.isprintable() or SECRET.search(text):
            raise DemoError("Text input must contain 1 to 512 printable, credential-free characters")


def needs_terminal_input(scenario):
    return scenario.get("requires_terminal_input", False) or any(
        step["op"] == "text-input" or (step["op"] == "tap" and step.get("target") in
        {"terminal-insert", "terminal-return", "Insert", "Return"}) for step in scenario["steps"])


def authorize_input(args, scenario):
    if needs_terminal_input(scenario) and not args.allow_terminal_input:
        raise DemoError("This scenario requires explicit --allow-terminal-input")


def steps_for_pose(scenario, index, total, pose_id=None):
    return [step for step in scenario["steps"] if step.get("pose", "last") == "all"
            or (step.get("pose", "last") == "first" and index == 0)
            or (step.get("pose", "last") == "last" and index == total - 1)
            or step.get("pose", "last") == pose_id]


class HeadingParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.inside = False
        self.parts = []
        self.headings = []

    def handle_starttag(self, tag, attrs):
        if tag == "h1":
            self.inside, self.parts = True, []
        elif self.inside and tag == "br":
            self.parts.append(" ")

    def handle_data(self, data):
        if self.inside:
            self.parts.append(data)

    def handle_endtag(self, tag):
        if tag == "h1" and self.inside:
            self.headings.append(" ".join("".join(self.parts).split()))
            self.inside = False


def public_index(lab, state):
    # The parent lab authenticates ownership on start. Accept only its public tree.
    state = Path(state).resolve()
    root = Path(lab["files_root"])
    if lab["state_dir"] != str(state) or not root.is_absolute() or root.resolve() != root or state not in root.parents:
        raise DemoError("Public files root must be a real directory inside the owned lab state")
    with contextlib.ExitStack() as stack:
        directory = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        stack.callback(os.close, directory)
        file = os.open("index.html", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        stack.callback(os.close, file)
        if not stat.S_ISREG(os.fstat(file).st_mode):
            raise DemoError("Public index.html must be a regular file")
        with os.fdopen(os.dup(file), "rb") as source:
            data = source.read(1024 * 1024 + 1)
    if len(data) > 1024 * 1024:
        raise DemoError("Public index.html exceeds the 1 MiB readiness limit")
    parser = HeadingParser()
    parser.feed(data.decode("utf-8"))
    return hashlib.sha256(data).hexdigest(), parser.headings


def utility_plan(scenario, appearance, lab, state):
    digest, headings = public_index(lab, state)
    choices = [heading.lower() for heading in scenario["headings"][appearance]]
    expected = next((heading for heading in choices if heading not in headings), None)
    if expected is None:
        raise DemoError("No utility heading differs from the existing public page")
    request = f"change the main heading in index.html to '{expected}' keep other design. run node --check app.js."
    bounded_text(request)
    return {"heading": expected, "request": request, "before_sha256": digest}


def wait_heading(commands, lab, state, plan, timeout):
    bounded(timeout, 1, 120, "file readiness timeout")
    started = time.monotonic()
    outcome = "error"
    stable_digest, stable_count, stable_since = None, 0, None
    try:
        while True:
            try:
                digest, headings = public_index(lab, state)
            except (FileNotFoundError, UnicodeDecodeError):
                digest, headings = None, []  # Atomic replacement/partial writes are not readiness.
            now = time.monotonic()
            if digest and digest != plan["before_sha256"] and plan["heading"] in headings:
                if digest == stable_digest:
                    stable_count += 1
                else:
                    stable_digest, stable_count, stable_since = digest, 1, now
                if stable_count >= 3 and now - stable_since >= 0.75:
                    outcome = "ready"
                    return digest
            else:
                stable_digest, stable_count, stable_since = None, 0, None
            if now - started >= timeout:
                outcome = "timeout"
                raise DemoError("Timed out waiting for the real public index.html heading change")
            time.sleep(0.4)
    finally:
        commands.event("file_readiness", outcome=outcome, duration_seconds=round(time.monotonic() - started, 3),
                       evidence="changed public index.html and stable matching h1; not agent completion or node check success")


def require_agent(commands, state):
    try:
        status = json.loads(commands.lab(state, "status").stdout)
    except (ValueError, AttributeError) as exc:
        raise DemoError("Cannot verify real Codex liveness; no terminal input sent") from exc
    if not isinstance(status, dict) or status.get("agent_running") is not True:
        raise DemoError("Real Codex is not running; start it explicitly with python3 scripts/demo-lab.py codex before utility capture")


def step_value(value, appearance, lab, plan):
    if isinstance(value, dict):
        value = value[appearance]
    return {"$session_name": lab["session_name"], "$heading": plan.get("heading"),
            "$request": plan.get("request")}.get(value, value)


def validate_scenario(scenario):
    safe_name(scenario["id"])
    if SECRET.search(json.dumps(scenario)):
        raise DemoError("Scenario must not contain credentials or pairing URLs")
    safe_name(scenario["initial_snapshot"])
    if "subsequent_snapshot" in scenario:
        safe_name(scenario["subsequent_snapshot"])
    if scenario.get("start_at", "terminal") not in {"terminal", "sessions"}:
        raise DemoError("Invalid initial native screen")
    if "headings" in scenario:
        if set(scenario["headings"]) != {"light", "dark"}:
            raise DemoError("Utility headings require both appearances")
        for choices in scenario["headings"].values():
            if not isinstance(choices, list) or len(choices) < 2:
                raise DemoError("Utility headings need a primary and alternate")
            for heading in choices:
                bounded_text(heading)
    for step in scenario["steps"]:
        op = step["op"]
        if op not in {"tap", "wait", "set-preview", "pause", "screenshot", "text-input", "wait-heading", "wait-preview", "wait-agent", "wait-source", "ensure-workspace"}:
            raise DemoError(f"Unsupported scenario operation: {op}")
        if step.get("pose", "last") not in {"first", "last", "all"}:
            safe_name(step["pose"])
        if op == "text-input":
            bounded_text(step["text"])
            bounded(step.get("chunk_chars", 24), 1, 80, "text chunk size")
            if not isinstance(step.get("chunk_chars", 24), int):
                raise DemoError("Text chunk size must be an integer")
            bounded(step.get("interval_seconds", 0.2), 0, 1, "text chunk interval")
            if not step.get("ready_target"):
                raise DemoError("Text input requires a native keyboard readiness target")
        elif op == "wait-heading":
            bounded(step.get("timeout", 120), 1, 120, "file readiness timeout")
        elif op == "pause":
            bounded(step["seconds"], 0, 60, "pause")
        elif op == "screenshot":
            safe_name(step["name"])
        if op in {"tap", "wait", "text-input"}:
            if not isinstance(step.get("target"), str) or SECRET.search(step["target"]):
                raise DemoError("Invalid UI target")
            if step.get("key", "AXLabel") not in {"AXLabel", "AXUniqueId"}:
                raise DemoError("Invalid UI match key")


class Commands:
    def __init__(self, repo):
        self.repo = Path(repo).resolve()
        self.env = dict(os.environ)
        # Inherited companion/target overrides must never route to a personal target.
        for key in ("IDB_UDID", "IDB_COMPANION", "IDB_COMPANION_TLS"):
            self.env.pop(key, None)
        # Preserve DEVELOPER_DIR; when absent, xcrun uses xcode-select.
        self.env["AI_AGENT"] = "trill-demo/1/runner"
        self.events = []
        self.started = time.monotonic()

    def event(self, name, **fields):
        event = {"event": name, "elapsed_seconds": round(time.monotonic() - self.started, 3), **fields}
        self.events.append(event)
        if name in {"appearance", "pose_ready", "url_open_consent", "paired_and_consented", "video_started", "video_stopped", "tap_started"}:
            print(json.dumps({"progress": event}), flush=True)

    def call(self, args, *, timeout=60, check=True):
        label = str(args[0])
        if args[0] == "mise" and len(args) > 4:
            label = "idb " + str(args[4]) + (" " + str(args[5]) if args[4] == "ui" else "")
        elif args[0] == "xcrun" and len(args) > 2:
            label = " ".join(map(str, args[:3]))
        try:
            result = subprocess.run([str(x) for x in args], cwd=self.repo, env=self.env,
                                    capture_output=True, text=True, timeout=timeout)
        except (OSError, subprocess.TimeoutExpired) as exc:
            # Never include argv, stderr, or stdout: invitation URLs are argv to IDB open.
            raise DemoError(f"Command unavailable or timed out: {label}") from exc
        if check and result.returncode:
            raise DemoError(f"Command failed ({result.returncode}): {label}; raw output suppressed")
        return result

    def simctl(self, *args, **kwargs):
        return self.call(["xcrun", "simctl", *args], **kwargs)

    def idb_args(self, udid, *args):
        split = 2 if args[0] == "ui" else 1
        return ["mise", "exec", "--", "idb", *args[:split], "--udid", udid,
                "--reason", "Capture a real isolated Trill demo", *args[split:]]

    def idb(self, udid, *args, **kwargs):
        return self.call(self.idb_args(udid, *args), **kwargs)

    def inventory(self):
        try:
            return json.loads(self.simctl("list", "-j").stdout)
        except ValueError as exc:
            raise DemoError("simctl inventory was not JSON") from exc

    def lab(self, state, command, *args):
        script = self.repo / "scripts/demo-lab.py"
        if not script.is_file():
            raise DemoError("Missing lab implementation; pass --repo pointing to the integration repository")
        return self.call([sys.executable, script, command, "--state-dir", state, *args], timeout=180)


def app_identity(app):
    app = Path(app).resolve()
    try:
        info = plistlib.loads((app / "Info.plist").read_bytes())
        if info["CFBundleIdentifier"] != BUNDLE:
            raise DemoError(f"App bundle must be {BUNDLE}")
        executable = (app / info["CFBundleExecutable"]).resolve()
        if executable.parent != app:
            raise DemoError("App executable must be inside its bundle")
        return {"path": str(app), "executable_sha256": sha256(executable),
                "version": info.get("CFBundleShortVersionString"), "build": info.get("CFBundleVersion")}
    except (OSError, KeyError, ValueError, plistlib.InvalidFileException) as exc:
        raise DemoError("--app must point to a prebuilt Trill.app") from exc


def find_device(inventory, udid):
    for runtime, devices in inventory["devices"].items():
        for device in devices:
            if device["udid"].upper() == udid.upper():
                return runtime, device
    raise DemoError("Owned simulator no longer exists; preserve metadata and use a new --state-dir")


def verify_owned(record, profile, inventory, requested=None):
    udid = record.get("udid", "").upper()
    try:
        uuid.UUID(udid)
    except ValueError as exc:
        raise DemoError("Invalid simulator ownership record") from exc
    if (udid in load_protected_simulator_ids(PROTECTED_SIMULATORS_PATH, DemoError)
            or (requested and requested.upper() != udid)):
        raise DemoError("Refusing protected or unowned simulator")
    runtime, device = find_device(inventory, udid)
    if (record.get("owner") != "trill-demo-v1" or record.get("profile") != profile["id"]
            or record.get("runtime") != profile["runtime"] or runtime != profile["runtime"]
            or device["deviceTypeIdentifier"] != profile["device_type"]
            or device["name"] != record.get("name") or not device.get("isAvailable", False)):
        raise DemoError("Simulator ownership/type/runtime/name mismatch; refusing to operate it")
    return udid, device


def runner_state(state):
    # Lab owns devices.json (credentials) and its initial directory/owner marker.
    return state.with_name(state.name + "-runner")


def prepare(commands, args, profile, state):
    protected_simulator_ids = load_protected_simulator_ids(PROTECTED_SIMULATORS_PATH, DemoError)
    app = app_identity(args.app)
    registry_path = runner_state(state) / "devices.json"
    registry = read_json(registry_path) if registry_path.exists() else {}
    inventory = commands.inventory()
    runtimes = {x["identifier"]: x for x in inventory["runtimes"] if x.get("isAvailable")}
    if profile["runtime"] not in runtimes:
        raise DemoError(f"Required runtime unavailable: {profile['runtime']}")
    if profile["device_type"] not in {x["identifier"] for x in inventory["devicetypes"]}:
        raise DemoError("Required simulator device type unavailable")
    record = registry.get(profile["id"])
    if record is None:
        if args.udid:
            raise DemoError("--udid cannot adopt an existing simulator; prepare creates dedicated simulators")
        existing_simulator_ids = {device["udid"].upper() for devices in inventory["devices"].values() for device in devices}
        name = f"Trill Demo - {profile['id']} - {uuid.uuid4().hex[:8]}"
        udid = commands.simctl("create", name, profile["device_type"], profile["runtime"]).stdout.strip().upper()
        try:
            uuid.UUID(udid)
        except ValueError as exc:
            raise DemoError("simctl create returned no simulator ID; inspect simulator list before retrying") from exc
        if udid in existing_simulator_ids or udid in protected_simulator_ids:
            raise DemoError("Simulator create returned an existing/protected identity; no UI action allowed")
        record = {"owner": "trill-demo-v1", "profile": profile["id"], "udid": udid, "name": name,
                  "runtime": profile["runtime"], "created_at": timestamp()}
        registry[profile["id"]] = record
        write_json(registry_path, registry)  # Persist ownership before any boot/install operation.
        inventory = commands.inventory()
    udid, device = verify_owned(record, profile, inventory, args.udid)
    with lock(simulator_lock(udid)):
        if device["state"] != "Booted":
            commands.simctl("boot", udid)
        commands.simctl("bootstatus", udid, "-b", timeout=180)
        commands.idb(udid, "install", app["path"], timeout=180)
        if profile.get("orientation") is not None:
            commands.idb(udid, "rotation", profile["orientation"])
        first_pose = profile["poses"][0]
        if first_pose.get("hinge") is not None:
            commands.idb(udid, "hinge", str(first_pose["hinge"]))
        commands.idb(udid, "launch", "--foreground-if-running", BUNDLE)
        record["app"] = app
        record["prepared_at"] = timestamp()
        write_json(registry_path, registry)
    return {"profile": profile["id"], "udid": udid, "lock": str(simulator_lock(udid)), "state_dir": str(state)}


class UI:
    def __init__(self, commands, udid, args, profile, record, private):
        self.c = commands
        self.udid = udid
        self.args = args
        self.private = private
        self.pose = profile["poses"][0]["id"]
        self.orientation = profile.get("orientation")
        self.map = None
        self.ocr = None
        self.ocr_process = None
        self.demo_session = "field-notes"
        self.agent_check = None
        self.agent_ready_verified = False
        if args.ui_map:
            self.map = read_json(args.ui_map)
            if (self.map.get("profile") != profile["id"] or self.map.get("runtime") != profile["runtime"]
                    or self.map.get("app_sha256") != record["app"]["executable_sha256"]
                    or not self.map.get("reviewed_by") or not self.map.get("reviewed_at")):
                raise DemoError("UI map must be reviewed and match this exact profile, runtime and app hash")
            self.ocr = private / "screen-text"
            self.c.call(["xcrun", "swiftc", ROOT / "scripts/demo/screen_text.swift", "-parse-as-library", "-o", self.ocr], timeout=120)

    @contextlib.contextmanager
    def screen(self):
        path = self.private / f"screen-{uuid.uuid4().hex}.png"
        try:
            self.c.idb(self.udid, "screenshot", str(path))
            yield path
        finally:
            path.unlink(missing_ok=True)

    def map_pose(self, path):
        pose = self.map.get("poses", {}).get(self.pose, {})
        if pose.get("pixels") != png_size(path):
            raise DemoError("Coordinate map screen dimensions do not match; refusing input/capture")
        points = pose.get("points", [])
        if len(points) != 2 or any(not isinstance(x, (float, int)) or x <= 0 for x in points):
            raise DemoError("UI map needs verified point dimensions")
        return pose

    def close(self):
        if self.ocr_process is not None:
            self.ocr_process.stdin.close()
            try:
                self.ocr_process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.ocr_process.terminate()
                try:
                    self.ocr_process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.ocr_process.kill()
                    self.ocr_process.wait(timeout=5)
            self.ocr_process = None

    def vision_text(self, path):
        if self.ocr is None:
            self.ocr = self.private / "screen-text"
            self.c.call(["xcrun", "swiftc", ROOT / "scripts/demo/screen_text.swift", "-parse-as-library", "-o", self.ocr], timeout=120)
        if self.ocr_process is None:
            self.ocr_process = subprocess.Popen([str(self.ocr), "--server"], stdin=subprocess.PIPE,
                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, env=self.c.env)
        try:
            self.ocr_process.stdin.write(str(path) + "\n")
            self.ocr_process.stdin.flush()
            if not select.select([self.ocr_process.stdout], [], [], 60)[0]:
                raise DemoError("Local screen OCR timed out")
            return "\n".join(json.loads(self.ocr_process.stdout.readline()))
        except (ValueError, BrokenPipeError) as exc:
            raise DemoError("Local screen OCR failed; raw screen text suppressed") from exc

    @staticmethod
    def elements(value):
        if isinstance(value, dict):
            yield value
            for child in value.values():
                yield from UI.elements(child)
        elif isinstance(value, list):
            for child in value:
                yield from UI.elements(child)

    def tree(self):
        result = self.c.idb(self.udid, "ui", "describe-all", "--json", "--filter", "interactable",
                            "--api", "ax-coresimulator", "--client-type", "xctest", timeout=15)
        try:
            return json.loads(result.stdout)
        except ValueError as exc:
            raise DemoError("Accessibility unavailable; provide an independently reviewed --ui-map") from exc

    def text(self, path=None):
        if self.map:
            if path is None:
                with self.screen() as image:
                    return self.text(image)
            self.map_pose(path)
            return self.vision_text(path)
        return json.dumps(self.tree(), ensure_ascii=False)

    def wait(self, target, key="AXLabel", timeout=None):
        timeout = self.args.timeout if timeout is None else timeout
        if not self.map or (key == "AXUniqueId" and target == "remote-terminal"):
            # Connected terminals no longer display the transport in their title.
            # Verify the actual native terminal surface even when using a map.
            deadline = time.monotonic() + timeout
            stable, stable_count = None, 0
            began = time.monotonic()
            while True:
                matches = [x for x in self.elements(self.tree()) if isinstance(x.get(key), str) and (x[key] == target if key == "AXUniqueId" else target in x[key])
                           and x.get("enabled", True) and x.get("frame", {}).get("width", 0) > 0 and x.get("frame", {}).get("height", 0) > 0]
                if matches:
                    matches.sort(key=lambda x: (x.get(key) != target, x.get("role") not in {"AXButton", "AXLink"}))
                    current = (matches[0].get(key), matches[0]["frame"])
                    stable_count = stable_count + 1 if current == stable else 0
                    stable = current
                    if stable_count >= 2:
                        return matches[0]
                else:
                    stable, stable_count = None, 0
                if target == "Field Notes" and time.monotonic() - began > 3:
                    with self.screen() as image:
                        if "field notes" in self.vision_text(image).casefold():
                            return None
                if time.monotonic() >= deadline:
                    raise DemoError(f"Timed out waiting for native UI target: {target}")
                time.sleep(0.4)
        marker = self.map["poses"][self.pose].get("targets", {}).get(target, {}).get("text", target)
        deadline = time.monotonic() + timeout
        while True:
            if marker.casefold() in self.text().casefold():
                return
            if time.monotonic() >= deadline:
                raise DemoError("Timed out waiting for reviewed visible UI state; no capture published")
            time.sleep(0.5)

    def tap(self, target, key="AXLabel"):
        if self.args.ui_quiet:
            self.quiet()
        element = self.wait(target, key)
        self.c.event("tap_started", target=target, pose=self.pose)
        if not self.map:
            frame = element["frame"]
            # Marker actions cannot request the xctest client tree on this IDB version.
            # Deliver an explicit-duration HID touch at its verified live AX center.
            # Zero-duration taps are dropped on these simulator seeds.
            self.c.idb(self.udid, "ui", "tap", str(round(frame["x"] + frame["width"] / 2)),
                       str(round(frame["y"] + frame["height"] / 2)), "--duration", "0.1")
        else:
            with self.screen() as image:
                pose = self.map_pose(image)
                entry = pose.get("targets", {}).get(target, {})
                xy = entry.get("tap", [])
                if len(xy) != 2:
                    raise DemoError("Missing reviewed tap coordinates")
                for value, limit in zip(xy, pose["points"]):
                    bounded(value, 0, limit, "coordinate")
                api = entry.get("api", "hid")
                if api not in {"hid", "ax-coresimulator"}:
                    raise DemoError("Unsupported reviewed tap backend")
                options = ["--duration", "0.1"] if api == "hid" else ["--api", api]
                if api != "hid" and entry.get("expected_id"):
                    options += ["--expected-key", "AXUniqueId", "--expected-value", entry["expected_id"]]
                self.c.idb(self.udid, "ui", "tap", *(str(round(value)) for value in xy), *options)
        self.c.event("tap", target=target, pose=self.pose)

    def quiet(self):
        seconds = min(self.args.timeout, 5)
        self.c.idb(self.udid, "ui", "quiet", "--bundle-id", BUNDLE,
                   "--quiet-window-ms", "400", str(seconds), timeout=seconds + 5)

    def assert_no_agent_prompt(self):
        with self.screen() as image:
            visible = self.vision_text(image)
        if any(prompt in visible.casefold() for prompt in AGENT_PROMPTS):
            raise DemoError("Real agent shows a trust/approval prompt; operator setup is required, no automatic approval")
        return visible

    def wait_agent(self):
        if self.agent_check is None:
            raise DemoError("Cannot verify real agent liveness")
        deadline = time.monotonic() + self.args.timeout
        while True:
            self.agent_check()
            visible = self.assert_no_agent_prompt()
            idle_lines = {" ".join(line.casefold().split()).lstrip(">› ") for line in visible.splitlines()}
            if idle_lines & {"ask codex to do anything", "sk codex to do anything"}:
                self.agent_ready_verified = True
                self.c.event("agent_ready", evidence="live process and visible Ask Codex prompt")
                return
            if time.monotonic() >= deadline:
                raise DemoError("Real Codex is not visibly idle at Ask Codex; complete operator setup before capture")
            time.sleep(0.25)

    def native_input_point(self, target):
        if self.map:
            with self.screen() as image:
                pose = self.map_pose(image)
                point = pose.get("targets", {}).get(target, {}).get("tap", [])
                if len(point) != 2:
                    raise DemoError("Missing reviewed native input coordinates")
                for value, limit in zip(point, pose["points"]):
                    bounded(value, 0, limit, "coordinate")
        else:
            frame = self.wait(target, "AXUniqueId")["frame"]
            for name in ("x", "y", "width", "height"):
                bounded(frame.get(name), 0, 10000, "native input frame")
            if not frame["width"] or not frame["height"]:
                raise DemoError("Native input frame is empty")
            point = [frame["x"] + frame["width"] / 2, frame["y"] + frame["height"] / 2]
        return tuple(str(round(value)) for value in point)

    def wait_native_value(self, point, target, expected):
        deadline = time.monotonic() + min(self.args.timeout, 5)
        while True:
            result = self.c.idb(self.udid, "ui", "describe-point", *point,
                                "--api", "ax-coresimulator", "--json", timeout=5)
            try:
                matches = [element for element in self.elements(json.loads(result.stdout))
                           if element.get("AXUniqueId") == target]
            except (ValueError, TypeError) as exc:
                raise DemoError("Native input verification was not JSON; inspect before rerunning") from exc
            if len(matches) != 1:
                raise DemoError("Native input identity/value unavailable; inspect before rerunning")
            field = matches[0]
            # UIKit exposes an empty, active text editor as an explicit null.
            # Missing values and non-text controls still fail closed.
            if (expected == "" and "AXValue" in field and field["AXValue"] is None
                    and field.get("role") in {"AXTextArea", "AXTextField"}
                    and field.get("enabled") is True and "TextEntry" in field.get("traits", [])):
                return
            if not isinstance(field.get("AXValue"), str):
                raise DemoError("Native input identity/value unavailable; inspect before rerunning")
            actual = field["AXValue"]
            if (expected == "" and target == "preview-address" and field.get("role") == "AXTextField"
                    and field.get("enabled") is True and actual == "localhost:3000 or https://…"):
                return  # UIKit exposes this pinned native placeholder as the empty value.
            if actual == expected:
                return
            if expected == "":
                raise DemoError("Native input is not empty; refusing to replace stale text")
            if time.monotonic() >= deadline:
                raise DemoError("Native input value did not match; inspect before rerunning, no write retried")
            time.sleep(0.2)  # Read-only wait: never repeat an uncertain edit.

    def set_native_value(self, point, target, value):
        # AX setting alone does not notify UITextView's SwiftUI binding. A real
        # Backspace removes the sentinel space and fires native editing callbacks.
        self.c.idb(self.udid, "ui", "set-value", *point, "--api", "ax-coresimulator", "--value", value + " ")
        self.c.idb(self.udid, "ui", "key", "42", "--duration", "0.1")
        self.wait_native_value(point, target, value)

    def text_input(self, target, value, ready_target, chunk_chars=24, interval_seconds=0.2):
        if not self.args.allow_terminal_input:
            raise DemoError("Text input requires explicit --allow-terminal-input")
        if self.agent_check is None:
            raise DemoError("Text input requires a real-agent liveness check")
        self.agent_check()
        if not self.agent_ready_verified:
            raise DemoError("Verify the visible idle Ask Codex prompt before opening the native draft")
        bounded_text(value)
        if not re.fullmatch(r"[a-z0-9 .,'-]+", value):
            raise DemoError("Native draft HID input permits only no-Shift ASCII letters, digits, spaces, periods, commas, apostrophes and hyphens")
        bounded(chunk_chars, 1, 80, "text chunk size")
        bounded(interval_seconds, 0, 1, "text chunk interval")
        self.tap(target, "AXUniqueId")  # Focus the actual native draft before keyboard input.
        self.wait(ready_target)
        self.agent_check()
        started = time.monotonic()
        self.agent_ready_verified = False
        point = self.native_input_point(target)
        self.wait_native_value(point, target, "")
        chunks = [value[start:start + chunk_chars] for start in range(0, len(value), chunk_chars)]
        cumulative = ""
        for index, chunk in enumerate(chunks):
            self.c.idb(self.udid, "ui", "text", chunk)
            cumulative += chunk
            self.wait_native_value(point, target, cumulative)
            if index + 1 < len(chunks):
                time.sleep(interval_seconds)
        self.c.event("text_input", characters=len(value), chunks=len(chunks),
                     transport="no-Shift ASCII HID text, exact cumulative AXValue verified",
                     duration_seconds=round(time.monotonic() - started, 3))

    def workspace_button(self):
        matches = [element for element in self.elements(self.tree())
                   if element.get("AXUniqueId") == "session-workspace"]
        if (len(matches) != 1 or matches[0].get("enabled") is not True
                or matches[0].get("role") != "AXButton"
                or matches[0].get("AXLabel") not in {"Files and browser", "Hide files and browser"}):
            raise DemoError("Workspace state unavailable or ambiguous; refusing to toggle")
        return matches[0]

    def ensure_workspace(self):
        if self.args.ui_quiet:
            self.quiet()
        if self.workspace_button()["AXLabel"] == "Hide files and browser":
            self.c.event("workspace_ready", already_open=True)
            return
        point = self.native_input_point("session-workspace")
        result = self.c.idb(self.udid, "ui", "describe-point", *point,
                            "--api", "ax-coresimulator", "--json", timeout=5)
        try:
            buttons = [element for element in self.elements(json.loads(result.stdout))
                       if element.get("AXUniqueId") == "session-workspace"]
        except (ValueError, TypeError) as exc:
            raise DemoError("Workspace point identity unavailable; refusing to toggle") from exc
        if (len(buttons) != 1 or buttons[0].get("enabled") is not True
                or buttons[0].get("role") != "AXButton"
                or buttons[0].get("AXLabel") != "Files and browser"):
            raise DemoError("Workspace point is not the closed native toggle; refusing to press")
        # Use the native coordinate system after a fold, with the closed label
        # as an action-time guard. Never retry an uncertain toggle.
        self.c.idb(self.udid, "ui", "tap", *point, "--api", "ax-coresimulator",
                   "--expected-key", "AXLabel", "--expected-value", "Files and browser")
        deadline = time.monotonic() + self.args.timeout
        while self.workspace_button()["AXLabel"] != "Hide files and browser":
            if time.monotonic() >= deadline:
                raise DemoError("Workspace did not open; inspect before rerunning, no toggle retried")
            time.sleep(0.2)
        self.c.event("workspace_ready", already_open=False)

    def require_sessions_map(self):
        # These are reviewed map aliases, not identifiers exposed by RootView.
        targets = (self.map or {}).get("poses", {}).get(self.pose, {}).get("targets", {})
        for name in ("sessions-back", "sessions-list"):
            entry = targets.get(name)
            if (not isinstance(entry, dict) or not isinstance(entry.get("text"), str)
                    or not entry["text"].strip()):
                raise DemoError(f"Sessions start requires a reviewed {name} map entry for {self.pose}")
        point = targets["sessions-back"].get("tap")
        if not isinstance(point, list) or len(point) != 2:
            raise DemoError("Sessions start requires reviewed sessions-back tap coordinates")

    def require_preview_map(self, scenario, profile):
        if self.map is None:
            return
        for index, pose in enumerate(profile["poses"]):
            steps = steps_for_pose(scenario, index, len(profile["poses"]), pose["id"])
            if not any(step["op"] == "set-preview" for step in steps):
                continue
            targets = self.map.get("poses", {}).get(pose["id"], {}).get("targets", {})
            for name in ("preview-address", "Go", "Field Notes"):
                entry = targets.get(name)
                if (not isinstance(entry, dict) or not isinstance(entry.get("text"), str)
                        or not entry["text"].strip()):
                    raise DemoError(f"URL preview needs a reviewed {name} map entry for {pose['id']}; use utility or review the current native UI")
                if name != "Field Notes":
                    point = entry.get("tap")
                    if (not isinstance(point, list) or len(point) != 2
                            or any(type(value) not in (int, float) or not math.isfinite(value) or value < 0 for value in point)):
                        raise DemoError(f"URL preview needs reviewed {name} tap coordinates")

    def return_to_sessions(self):
        self.tap("sessions-back", "AXUniqueId")
        self.wait("sessions-list", "AXUniqueId")
        if self.args.ui_quiet:
            self.quiet()
        self.c.event("sessions_ready_before_recording")

    def wait_region(self, expected, target, timeout=None):
        deadline = time.monotonic() + (timeout or self.args.timeout)
        while True:
            with self.screen() as image:
                if self.map:
                    pose = self.map_pose(image)
                    rect = pose.get("targets", {}).get(target, {}).get("rect")
                else:
                    views = [x["frame"] for x in self.elements(self.tree())
                             if x.get("role") in {"AXWebArea", "AXWebView"} and x.get("frame")]
                    frame = max(views, key=lambda f: f["width"] * f["height"]) if views else None
                    rect = [frame[k] for k in ("x", "y", "width", "height")] if frame else None
                    pose = {"points": [png_size(image)[0], png_size(image)[1]]}
                if not isinstance(rect, list) or len(rect) != 4:
                    raise DemoError("Scoped readiness needs a reviewed content rectangle or a native WebView frame")
                x, y, width, height = rect
                for value in rect:
                    bounded(value, 0, 10000, "preview rectangle")
                if width <= 0 or height <= 0 or x + width > pose["points"][0] or y + height > pose["points"][1]:
                    raise DemoError("Preview rectangle is outside the verified display")
                crop = self.private / (target + "-readiness.png")
                try:
                    self.c.idb(self.udid, "screenshot", str(crop), "--crop", ",".join(str(round(v)) for v in rect), "--units", "points")
                    visible = " ".join(self.vision_text(crop).split())
                finally:
                    crop.unlink(missing_ok=True)
            if target == "source-content":
                visible = " ".join(re.sub(r"<[^>]*>", " ", visible).split())
            if expected.casefold() in visible.casefold():
                return
            if time.monotonic() >= deadline:
                raise DemoError("Timed out waiting for the heading in the reviewed native content region")
            time.sleep(0.25)

    def wait_preview(self, expected):
        return self.wait_region(expected, "preview-content")

    def wait_source(self, expected):
        return self.wait_region(expected, "source-content")

    def accept_open(self):
        # A new simulator asks for system consent before opening a custom-scheme URL.
        if self.map:
            entry = self.map["poses"][self.pose].get("targets", {}).get("Open")
            if entry and "open in" in self.text().casefold():
                self.tap("Open")
            return
        def elements(value):
            if isinstance(value, dict):
                yield value
                for child in value.values():
                    yield from elements(child)
            elif isinstance(value, list):
                for child in value:
                    yield from elements(child)
        deadline = time.monotonic() + min(self.args.timeout, 8)
        while time.monotonic() < deadline:
            tree = list(elements(self.tree()))
            if any(x.get("AXUniqueId") == "confirm-pairing" for x in tree):
                return
            buttons = [x for x in tree if x.get("AXLabel") == "Open" and x.get("frame")]
            if len(buttons) == 1:
                frame = buttons[0]["frame"]
                self.c.event("tap_started", target="Open", pose=self.pose)
                self.c.idb(self.udid, "ui", "tap", str(round(frame["x"] + frame["width"] / 2)),
                           str(round(frame["y"] + frame["height"] / 2)), "--duration", "0.1")
                self.c.event("url_open_consent", source="exact live AX label and verified HID touch")
                return
            time.sleep(0.3)

    def preview(self, address):
        if SECRET.search(address):
            raise DemoError("Lab returned a credential-bearing preview URL")
        self.tap("preview-address", "AXUniqueId")
        point = self.native_input_point("preview-address")
        self.wait_native_value(point, "preview-address", "")
        self.set_native_value(point, "preview-address", address)
        self.tap("Go")

    def assert_safe(self, path=None):
        text = self.text(path)
        # An AX identity cannot vouch for pixels in a terminal or WKWebView.
        # Every supplied image is scanned, even if native AX already identifies it.
        visible = self.vision_text(path) if path is not None and not self.map else text
        for sample in (text, visible):
            if not sample.strip() or SECRET.search(sample) or any(word.casefold() in sample.casefold() for word in FORBIDDEN):
                raise DemoError("Capture gate rejected empty UI, a pairing/consent screen, or possible credentials")
        identity = (self.demo_session.casefold() in text.casefold() or "field notes" in text.casefold()
                    or all(name in text for name in ("index.html", "style.css", "app.js"))
                    or "field notes" in visible.casefold())
        if not identity and self.map:
            # Exact OCR variants reviewed against this pinned app/runtime map.
            # These identify pixels only; pairing still selects the owned lab.
            aliases = self.map.get("identity_ocr_aliases", {}).get(self.demo_session, [])
            lines = {" ".join(line.casefold().split()) for line in text.splitlines()}
            identity = any(" ".join(alias.casefold().split()) in lines for alias in aliases if isinstance(alias, str) and len(alias) >= 8)
        if not identity and self.map:
            pose = self.map.get("poses", {}).get(self.pose, {})
            point = pose.get("identity_heading_point")
            if point:
                if len(point) != 2 or len(pose.get("points", [])) != 2:
                    raise DemoError("Invalid reviewed native heading point")
                for value, limit in zip(point, pose["points"]):
                    bounded(value, 0, limit, "native heading point")
                result = self.c.idb(self.udid, "ui", "describe-point", *(str(round(v)) for v in point),
                                    "--api", "ax-coresimulator", "--json", timeout=5)
                try:
                    headings = [element for element in self.elements(json.loads(result.stdout))
                                if element.get("role") == "AXHeading" and element.get("enabled") is True
                                and element.get("AXLabel") == self.demo_session]
                    identity = len(headings) == 1
                except (ValueError, TypeError) as exc:
                    raise DemoError("Native heading identity unavailable") from exc
        if not identity:
            raise DemoError("Capture gate cannot identify the dedicated demo session or Field Notes preview")

    def screenshot(self, dest):
        with self.screen() as image:
            self.assert_safe(image)
            size = png_size(image)
            shutil.copyfile(image, dest)
        return size

    def pose_ready(self, pose):
        self.pose = pose["id"]
        if self.orientation is not None:
            self.c.idb(self.udid, "rotation", self.orientation)
        if pose.get("hinge") is not None:
            self.c.idb(self.udid, "hinge", str(pose["hinge"]))
        # Sample real framebuffer dimensions until stable, rather than assume a resize time.
        deadline = time.monotonic() + self.args.timeout
        previous, count = None, 0
        while time.monotonic() < deadline:
            with self.screen() as image:
                size = png_size(image)
                if self.map and self.map.get("poses", {}).get(self.pose, {}).get("pixels") != size:
                    count = 0
                    time.sleep(0.5)
                    continue
                count = count + 1 if size == previous else 0
                previous = size
            if count >= 2:
                if self.args.ui_quiet:
                    self.quiet()
                self.c.event("pose_ready", pose=self.pose, hinge=pose.get("hinge"), pixels=size)
                return size
            time.sleep(0.5)
        raise DemoError("Simulator display did not settle after pose change")


@contextlib.contextmanager
def record_video(commands, udid, output, args):
    argv = commands.idb_args(udid, "video", str(output), "--fps", str(args.fps),
                             "--scale-factor", str(args.scale), "--bitrate", str(args.bitrate))
    process = subprocess.Popen(argv, cwd=commands.repo, env=commands.env, stdout=subprocess.DEVNULL,
                               stderr=subprocess.DEVNULL, start_new_session=True)
    failed = False
    try:
        # IDB has no recording-ready event. Allow startup and verify process stays alive.
        time.sleep(args.record_startup)
        if process.poll() is not None:
            raise DemoError("IDB recorder exited before recording started")
        commands.event("video_started", file=output.name)
        yield
        if process.poll() is not None:
            raise DemoError("IDB recorder exited before the scenario finished")
    except BaseException:
        failed = True
        raise
    finally:
        if process.poll() is None:
            process.send_signal(signal.SIGINT)
        try:
            code = process.wait(timeout=20)
        except subprocess.TimeoutExpired:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            output.unlink(missing_ok=True)
            if not failed:
                raise DemoError("IDB did not finalize video after SIGINT; discarded incomplete recording")
        else:
            if not failed and (code not in (0, 130, -signal.SIGINT) or not output.exists() or output.stat().st_size < 128):
                output.unlink(missing_ok=True)
                raise DemoError("IDB did not produce a finalized MP4")
        commands.event("video_stopped", file=output.name)


def video_info(commands, path):
    if not shutil.which("ffprobe"):
        raise DemoError("ffprobe is required to validate recorded MP4 segments (install ffmpeg)")
    result = commands.call(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
                            "stream=codec_name,width,height,avg_frame_rate:format=duration", "-of", "json", path])
    try:
        info = json.loads(result.stdout)
        stream = info["streams"][0]
        duration = float(info["format"]["duration"])
        if duration <= 0 or stream["width"] <= 0 or stream["height"] <= 0:
            raise ValueError()
        return {"duration_seconds": duration, **stream}
    except (ValueError, KeyError, IndexError) as exc:
        raise DemoError("Recorded MP4 has no valid video stream") from exc


def compose_video(commands, output, artifacts, appearance, profile_id, fps):
    segments = [x for x in artifacts if x["type"] == "video"]
    final = output / f"{appearance}-{profile_id}.mp4"
    if len(segments) == 1:
        commands.call(["ffmpeg", "-nostdin", "-v", "error", "-n", "-i", output / segments[0]["file"],
                       "-map", "0:v:0", "-c:v", "copy", "-an", "-movflags", "+faststart", final], timeout=120)
    else:
        width = max(x["width"] for x in segments)
        height = max(x["height"] for x in segments)
        width += width % 2
        height += height % 2
        argv = ["ffmpeg", "-nostdin", "-v", "error", "-n"]
        for segment in segments:
            argv += ["-i", output / segment["file"]]
        filters = [f"[{i}:v]fps={fps},scale={width}:{height}:force_original_aspect_ratio=decrease,"
                   f"pad={width}:{height}:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,setpts=PTS-STARTPTS[v{i}]"
                   for i in range(len(segments))]
        filters.append("".join(f"[v{i}]" for i in range(len(segments))) + f"concat=n={len(segments)}:v=1:a=0[out]")
        argv += ["-filter_complex", ";".join(filters), "-map", "[out]", "-c:v", "libx264", "-crf", "18",
                 "-pix_fmt", "yuv420p", "-movflags", "+faststart", final]
        commands.call(argv, timeout=180)
    return {"file": final.name, "type": "website-video", "appearance": appearance,
            "edit": ("lossless remux of actual recording with faststart" if len(segments) == 1 else "hard cut between actual pose recordings; fit and black pad; no fabricated hinge animation"),
            "sources": [x["file"] for x in segments], "sha256": sha256(final), **video_info(commands, final)}


def pair(commands, ui, state, private):
    lab = read_lab(commands.lab(state, "start").stdout)
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", lab["session_name"]):
        raise DemoError("Lab must return a valid dedicated tmux session name")
    ui.demo_session = lab["session_name"]
    invitation = private / "invitation.url"  # Must not exist: lab writes a fresh single-use invitation.
    try:
        commands.lab(state, "invite", "--output", invitation)
        if invitation.is_symlink() or (invitation.stat().st_mode & 0o777) != 0o600:
            raise DemoError("Invitation file must be a private regular 0600 file")
        url = invitation.read_text().strip()
        if not url.startswith("trill://pair?") or "\n" in url:
            raise DemoError("Lab returned an invalid invitation")
        commands.idb(ui.udid, "open", url)
        ui.accept_open()
        ui.tap("confirm-pairing", "AXUniqueId")
    finally:
        invitation.unlink(missing_ok=True)
    ui.tap(lab["session_name"])
    ui.tap("open-terminal", "AXUniqueId")
    ui.tap("Allow once")
    ui.wait("remote-terminal", "AXUniqueId")
    if ui.args.ui_quiet:
        ui.quiet()
    ui.assert_safe()
    commands.event("paired_and_consented")
    return lab


def read_lab(stdout):
    try:
        lab = json.loads(stdout)
        keys = {"state_dir", "address", "name", "session_name", "preview_url", "files_root"}
        if not keys <= lab.keys() or any(not isinstance(lab[k], str) for k in keys):
            raise ValueError()
        if any(SECRET.search(lab[k]) for k in keys):
            raise ValueError()
        from urllib.parse import urlsplit
        preview = urlsplit(lab["preview_url"])
        if preview.scheme != "http" or preview.hostname not in {"localhost", "127.0.0.1"} or preview.username or preview.password or preview.query or preview.fragment:
            raise ValueError()
        return {key: lab[key] for key in keys}
    except (ValueError, TypeError, AttributeError) as exc:
        raise DemoError("Lab start must return the public lab contract with a credential-free localhost preview URL") from exc


def capture(commands, args, profile, scenario, state, appearance):
    authorize_input(args, scenario)
    if scenario.get("start_at") == "sessions" and not args.ui_map:
        raise DemoError("Utility requires reviewed --ui-map or --ui-map-dir targets; its navigation markers are map keys, not app AX identifiers")
    registry = read_json(runner_state(state) / "devices.json")
    if profile["id"] not in registry:
        raise DemoError("Run prepare first for this profile")
    record = registry[profile["id"]]
    inventory = commands.inventory()
    udid, device = verify_owned(record, profile, inventory, args.udid)
    if device["state"] != "Booted":
        raise DemoError("Owned simulator is not booted; run prepare first")
    if "app" not in record or app_identity(record["app"]["path"]) != record["app"]:
        raise DemoError("Prepared app changed; run prepare again")
    runtime = next(x for x in inventory["runtimes"] if x["identifier"] == profile["runtime"])
    commit = commands.call(["git", "rev-parse", "HEAD"]).stdout.strip()
    dirty = bool(commands.call(["git", "status", "--porcelain"]).stdout.strip())
    run_id = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
    output = (args.output or (state.parent / "artifacts")) / run_id / profile["id"] / appearance
    output.mkdir(parents=True, exist_ok=False, mode=0o700)
    manifest = {"schema": 1, "appearance": appearance, "status": "preparing", "run_id": run_id, "started_at": timestamp(),
                "device": {"profile": profile["id"], "udid": udid, "name": device["name"],
                           "runtime": profile["runtime"], "runtime_build": runtime["buildversion"]},
                "app": record["app"], "source_commit": commit, "source_dirty": dirty,
                "runner_commit": subprocess.run(["git", "-C", str(ROOT), "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip(),
                "terminal_input_authorized": args.allow_terminal_input, "runner_script_sha256": sha256(Path(__file__)), "scenario": scenario, "profile": profile, "recording": {"fps": args.fps, "scale": args.scale, "bitrate": args.bitrate,
                "pose_transition": ("actual continuous recording across hinge changes; IDB fits later frames into the initial canvas" if args.hinge_recording == "continuous" else "separate actual segments; hinge changes occur between recordings")},
                "ui_backend": "reviewed-coordinates-and-vision-ocr" if args.ui_map else "accessibility", "artifacts": []}
    if args.ui_map:
        manifest["ui_map_sha256"] = sha256(args.ui_map)
    ui = None
    try:
        with lock(simulator_lock(udid)), tempfile.TemporaryDirectory(prefix="private-", dir=runner_state(state)) as temp:
            try:
                ui = UI(commands, udid, args, profile, record, Path(temp))
                ui.require_preview_map(scenario, profile)
                if scenario.get("start_at") == "sessions":
                    ui.require_sessions_map()
                if needs_terminal_input(scenario):
                    ui.agent_check = lambda: require_agent(commands, state)
                commands.simctl("ui", udid, "appearance", appearance)
                if commands.simctl("ui", udid, "appearance").stdout.strip() != appearance:
                    raise DemoError("Simulator did not accept requested appearance")
                commands.event("appearance", value=appearance)
                if scenario.get("start_at") == "sessions":
                    commands.idb(udid, "set", "hardware-keyboard", "disable")
                # Reset only this owned app process: retained SwiftUI navigation after re-pair
                # can otherwise expose a stale detail route with no session title.
                commands.idb(udid, "terminate", BUNDLE, check=False)
                commands.idb(udid, "launch", "--foreground-if-running", BUNDLE)
                ui.pose_ready(profile["poses"][0])
                lab = pair(commands, ui, state, Path(temp))
                manifest["lab"] = lab
                plan = utility_plan(scenario, appearance, lab, state) if "headings" in scenario else {}
                if plan:
                    require_agent(commands, state)
                    manifest["utility"] = plan
                if scenario.get("start_at") == "sessions":
                    ui.return_to_sessions()
                continuous = len(profile["poses"]) > 1 and args.hinge_recording == "continuous" and not args.screenshots_only
                continuous_file = output / f"{appearance}-continuous.mp4"
                # The actual hinge event occurs inside the recorder, without a generated transition.
                with record_video(commands, udid, continuous_file, args) if continuous else contextlib.nullcontext():
                    for index, pose in enumerate(profile["poses"]):
                        if index:
                            ui.pose_ready(pose)
                            ui.wait("remote-terminal", "AXUniqueId")
                        # All pairing and terminal consent is complete before a public artifact exists.
                        ui.assert_safe()
                        video = output / f"{appearance}-{index + 1:02d}-{pose['id']}.mp4"
                        with record_video(commands, udid, video, args) if not args.screenshots_only and not continuous else contextlib.nullcontext():
                            snapshot = scenario["initial_snapshot"] if index == 0 else scenario.get("subsequent_snapshot", scenario["initial_snapshot"])
                            steps = [{"op": "screenshot", "name": snapshot}, {"op": "pause", "seconds": args.hold}]
                            steps += steps_for_pose(scenario, index, len(profile["poses"]), pose["id"])
                            for step in steps:
                                op = step["op"]
                                if op == "tap":
                                    if step["target"] in {"terminal-insert", "terminal-return", "Insert", "Return"}:
                                        require_agent(commands, state)
                                        if step["target"] in {"terminal-return", "Return"}:
                                            ui.assert_no_agent_prompt()
                                    ui.tap(step_value(step["target"], appearance, lab, plan), step.get("key", "AXLabel"))
                                elif op == "wait":
                                    ui.wait(step_value(step["target"], appearance, lab, plan), step.get("key", "AXLabel"))
                                elif op == "ensure-workspace":
                                    ui.ensure_workspace()
                                elif op == "wait-agent":
                                    ui.wait_agent()
                                elif op == "text-input":
                                    ui.text_input(step["target"], step_value(step["text"], appearance, lab, plan),
                                                  step["ready_target"], step.get("chunk_chars", 24), step.get("interval_seconds", 0.2))
                                elif op == "wait-heading":
                                    plan["after_sha256"] = wait_heading(commands, lab, state, plan, step.get("timeout", 120))
                                elif op == "wait-preview":
                                    ui.wait_preview(plan["heading"])
                                elif op == "wait-source":
                                    ui.wait_source(plan["heading"])
                                elif op == "set-preview":
                                    ui.preview(lab["preview_url"])
                                elif op == "pause":
                                    time.sleep(step["seconds"])
                                elif op == "screenshot":
                                    dest = output / f"{appearance}-{index + 1:02d}-{pose['id']}-{step['name']}.png"
                                    size = ui.screenshot(dest)
                                    manifest["artifacts"].append({"file": dest.name, "type": "screenshot", "pose": pose["id"], "pixels": size, "sha256": sha256(dest)})
                                commands.event("step", op=op, pose=pose["id"])
                            with ui.screen() as checked_image:
                                ui.assert_safe(checked_image)
                        if not args.screenshots_only and not continuous:
                            manifest["artifacts"].append({"file": video.name, "type": "video", "pose": pose["id"], "sha256": sha256(video), **video_info(commands, video)})
                if continuous:
                    manifest["artifacts"].append({"file": continuous_file.name, "type": "video", "pose": "cover-to-unfolded",
                                                   "sha256": sha256(continuous_file), **video_info(commands, continuous_file)})
                if not args.screenshots_only:
                    final = compose_video(commands, output, manifest["artifacts"], appearance, profile["id"], args.fps)
                    manifest["artifacts"].append(final)
            finally:
                # Keep ownership locked through cleanup on success, failure and interruption.
                with contextlib.suppress(DemoError, OSError):
                    commands.idb(udid, "terminate", BUNDLE, check=False)
                if ui is not None:
                    with contextlib.suppress(OSError, subprocess.TimeoutExpired):
                        ui.close()
        manifest["status"] = "complete"
    except BaseException:
        # Failed runs publish no unreviewed imagery, raw command output, or secret-bearing diagnostics.
        for path in output.iterdir():
            if path.suffix in {".png", ".mp4"}:
                path.unlink()
        manifest["status"] = "failed"
        manifest["artifacts"] = []
        print(json.dumps({"failed_manifest": str(output / "manifest.json")}), flush=True)
        raise
    finally:
        manifest["finished_at"] = timestamp()
        manifest["events"] = commands.events
        write_json(output / "manifest.json", manifest)
    return {"profile": profile["id"], "appearance": appearance, "udid": udid, "manifest": str(output / "manifest.json")}


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("command", choices=["list", "prepare", "capture", "run"])
    p.add_argument("--repo", type=Path, default=ROOT, help="Integration repo containing scripts/demo-lab.py; Xcode follows DEVELOPER_DIR or xcode-select")
    p.add_argument("--state-dir", type=Path, help="Shared lab state directory (default: REPO/.local/demo-capture/lab)")
    p.add_argument("--output", type=Path, help="Artifact parent directory; every invocation gets a new run directory")
    p.add_argument("--profile", default="iphone-17-pro", help="Profile ID or all (strictly sequential)")
    p.add_argument("--appearance", choices=["light", "dark", "both"], default="both", help="Real simulator appearance; both runs sequentially")
    p.add_argument("--scenario", default="terminal", help="Built-in scenario ID or JSON path")
    p.add_argument("--app", type=Path, help="Prebuilt Trill.app; required by prepare/run; no app builds")
    p.add_argument("--udid", help="Assert a previously owned ID; never adopt an existing simulator")
    p.add_argument("--ui-map", type=Path, help="Reviewed coordinate fallback map, bound to exact app/runtime/dimensions")
    p.add_argument("--ui-map-dir", type=Path, help="Directory of PROFILE.json reviewed maps; supports sequential all-device runs")
    p.add_argument("--allow-terminal-input", action="store_true", help="Explicitly authorize native draft typing, Insert and terminal Return")
    p.add_argument("--ui-quiet", action="store_true", default=True, help="Wait at most five seconds for native app idle before taps and after pose changes (default)")
    p.add_argument("--no-ui-quiet", dest="ui_quiet", action="store_false", help="Explicit compatibility opt-out for IDB versions without ui quiet")
    p.add_argument("--timeout", type=float, default=35)
    p.add_argument("--fps", type=int, default=30)
    p.add_argument("--scale", type=float, default=1.0)
    p.add_argument("--bitrate", type=int, default=8000000)
    p.add_argument("--hold", type=float, default=4.0)
    p.add_argument("--hinge-recording", choices=["continuous", "segments"], default="continuous", help="Continuous actual hinge recording (initial canvas), or separate actual pose segments")
    p.add_argument("--record-startup", type=float, default=2.0)
    p.add_argument("--screenshots-only", action="store_true")
    p.add_argument("--dry-run", action="store_true", help="Validate and print plan without commands, file writes, lab or simulator operations")
    return p


def main(argv=None):
    os.umask(0o077)
    args = parser().parse_args(argv)
    profiles, scenarios = assets("profiles"), assets("scenarios")
    if args.command == "list":
        print(json.dumps({"profiles": profiles, "scenarios": scenarios}, indent=2))
        return 0
    load_protected_simulator_ids(PROTECTED_SIMULATORS_PATH, DemoError)
    selected = list(profiles.values()) if args.profile == "all" else [profiles.get(args.profile)]
    if any(profile is None for profile in selected):
        raise DemoError("Unknown profile; see list")
    if args.profile == "all" and (args.udid or args.ui_map):
        raise DemoError("--udid and --ui-map require one explicit profile")
    scenario = scenarios.get(args.scenario) or read_json(Path(args.scenario))
    validate_scenario(scenario)
    authorize_input(args, scenario)
    for profile in selected:
        validate_profile(profile)
    for value, low, high, name in [(args.timeout, 1, 180, "timeout"), (args.fps, 1, 60, "fps"),
            (args.scale, 0.1, 1, "scale"), (args.bitrate, 100000, 100000000, "bitrate"),
            (args.hold, 0, 60, "hold"), (args.record_startup, 0.5, 10, "record-startup")]:
        bounded(value, low, high, name)
    if args.command in {"prepare", "run"} and not args.app:
        raise DemoError("--app is required; use a prebuilt Trill.app")
    args.repo = args.repo.resolve()
    state = (args.state_dir or args.repo / ".local/demo-capture/lab").resolve()
    if args.output:
        args.output = args.output.resolve()
    if args.dry_run:
        print(json.dumps({"command": args.command, "profiles": selected, "scenario": scenario,
                          "repo": str(args.repo), "state_dir": str(state), "lab_script": str(args.repo / "scripts/demo-lab.py"),
                          "app": str(args.app) if args.app else None, "appearance": args.appearance, "sequential": True,
                          "terminal_input_authorized": args.allow_terminal_input,
                          "policy": "create/reuse owned IDs only; fresh UI pairing and consent before recording; separate real pose segments"}, indent=2))
        return 0
    if args.command in {"capture", "run"} and not args.screenshots_only and (not shutil.which("ffprobe") or not shutil.which("ffmpeg")):
        raise DemoError("ffprobe is required to verify recordings; install ffmpeg or use --screenshots-only")
    runner_state(state).mkdir(parents=True, exist_ok=True, mode=0o700)
    results = []
    # Also serializes shared lab ports and ownership registry for all profiles.
    with lock(runner_state(state) / "runner.lock"):
        for profile in selected:
            if args.ui_map_dir:
                args.ui_map = args.ui_map_dir / (profile["id"] + ".json")
            commands = Commands(args.repo)
            if args.command in {"prepare", "run"}:
                result = prepare(commands, args, profile, state)
                print(json.dumps({"prepared": result}), flush=True)
            if args.command in {"capture", "run"}:
                for appearance in (["light", "dark"] if args.appearance == "both" else [args.appearance]):
                    result = capture(commands, args, profile, scenario, state, appearance)
                    results.append(result)
                    print(json.dumps({"captured": result}), flush=True)
            else:
                results.append(result)
    print(json.dumps({"results": results}, indent=2))
    return 0


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    try:
        sys.exit(main())
    except DemoError as exc:
        print(f"demo: {exc}", file=sys.stderr)
        sys.exit(1)
    except (OSError, KeyError, ValueError, TypeError) as exc:
        print(f"demo: invalid input or unavailable local resource ({type(exc).__name__}); raw details suppressed", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("demo: interrupted; recorder stopped and incomplete imagery discarded", file=sys.stderr)
        sys.exit(130)
