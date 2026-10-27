#!/usr/bin/env python3
"""Native Trill smoke tests against an explicitly owned synthetic backend (stdlib)."""
from __future__ import annotations

import argparse
import contextlib
from collections import deque
import datetime as dt
from decimal import Decimal, InvalidOperation
import fcntl
import hashlib
from html.parser import HTMLParser
import importlib.util
import ipaddress
import json
import math
import os
from pathlib import Path
import plistlib
import re
import secrets
import stat
import struct
import subprocess
import sys
import time
import urllib.parse
import uuid
import zlib

ROOT = Path(__file__).resolve().parents[1]
OWNER = "trill-demo-smoke-v1"
BUNDLE = "io.bcook.trill"
PROTECTED_SIMULATORS_PATH = ROOT / ".local/protected-simulators.json"
_PROTECTION_SPEC = importlib.util.spec_from_file_location("simulator_protection", ROOT / "scripts/simulator_protection.py")
_PROTECTION_MODULE = importlib.util.module_from_spec(_PROTECTION_SPEC)
_PROTECTION_SPEC.loader.exec_module(_PROTECTION_MODULE)
load_protected_simulator_ids = _PROTECTION_MODULE.load_protected_simulator_ids
PROFILES = {
    "iphone-17-pro": ("com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro", "com.apple.CoreSimulator.SimRuntime.iOS-27-0"),
    "iphone-duo": ("com.apple.CoreSimulator.SimDeviceType.iPhone-Duo", "com.apple.CoreSimulator.SimRuntime.iOS-27-1"),
}
SCENARIOS = ("happy", "lifecycle", "two-computers", "photo", "capacity", "busy", "provider-unavailable",
             "create-unconfirmed", "api-unavailable", "preview-unavailable", "upload-rejected", "drop-leases", "preview-expired")
SECRET = re.compile(r"trill://pair[^\s\"<>]*|\bBearer\s+\S+|-----BEGIN [^-]*PRIVATE KEY-----|\b(?:[a-f0-9]{64})\b|\bMOSH CONNECT\s+\S+\s+\S+", re.I)
SECRET_KEY = re.compile(r"token|secret|password|invitation|credential|private.?key|pair.?link|mosh.?key", re.I)
VISUAL_NONCE_ALPHABET = "abcdefghjkmnpqrstuvwxyz"
VISUAL_NONCE_LENGTH = 12  # More than 48 bits; no digits or ambiguous i/l/o in the nonce.
DUO_POSES = (("unfolded", "PORTRAIT", 180), ("book", "PORTRAIT", 90),
             ("tabletop", "LANDSCAPE_LEFT", 90))
KEYBOARD_TUTORIAL = "Speed up your typing by sliding your finger across the letters to compose a word."
ACTIONS = {"AXButton", "AXLink", "AXRadioButton", "AXPopUpButton", "AXTextField", "AXTextArea"}


class SmokeError(Exception):
    """An observed failed assertion. Messages must never contain raw tool output."""


class Blocked(SmokeError):
    """A prerequisite or observable assertion is unavailable; never a pass."""


class RegionChanged(Blocked):
    """Discard this read/capture attempt; a new stable native region is required."""


class ScopedContainerChanged(Blocked):
    """A pinned editor container changed; another AX client cannot rescue it."""


class Uncertain(SmokeError):
    """An edit/action may have been delivered. Do not retry automatically."""


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as file:
        for block in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def read_json(path):
    try:
        return json.loads(Path(path).read_text())
    except (OSError, ValueError) as exc:
        raise Blocked("Required local JSON metadata is missing or invalid") from exc


def write_json(path, value):
    path = Path(path)
    temporary = path.with_name(path.name + "." + uuid.uuid4().hex + ".tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w") as file:
            json.dump(value, file, indent=2, sort_keys=True)
            file.write("\n")
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def absolute(path):
    path = Path(path).expanduser()
    if not path.is_absolute():
        raise Blocked("Use an absolute path for app, binary and owned state")
    if path.is_symlink() or path.absolute() != path.resolve():
        # /tmp aliases are normal on macOS; reject symlinks below the canonical parent.
        if path.parent.resolve() / path.name != path.resolve():
            raise Blocked("Refusing a symlink as an owned resource")
    return path.resolve()


def private_directory(path):
    info = path.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise Blocked("Owned directories must belong to this user and have mode 0700")


def initialize(path):
    path = absolute(path)
    if path.exists():
        raise Blocked("Initialization requires a new directory; existing state is never adopted")
    path.mkdir(mode=0o700, parents=False)
    write_json(path / "owner.json", {"kind": OWNER, "id": str(uuid.uuid4()), "uid": os.getuid(), "createdAt": now()})
    write_json(path / "simulators.json", [])
    return path


def owned(path):
    path = absolute(path)
    private_directory(path)
    marker = read_json(path / "owner.json")
    if marker.get("kind") != OWNER or marker.get("uid") != os.getuid():
        raise Blocked("Harness ownership marker does not match")
    try:
        uuid.UUID(marker["id"])
    except (KeyError, TypeError, ValueError) as exc:
        raise Blocked("Invalid harness ownership identity") from exc
    return path, marker


@contextlib.contextmanager
def locked(path):
    descriptor = os.open(path, os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0), 0o600)
    with os.fdopen(descriptor, "w") as file:
        try:
            fcntl.flock(file, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise Blocked("Another runner owns this smoke state") from exc
        yield


def redact(value, secrets=()):
    if isinstance(value, dict):
        return {key: ("[redacted]" if SECRET_KEY.search(str(key)) else redact(item, secrets))
                for key, item in value.items()}
    if isinstance(value, list):
        return [redact(item, secrets) for item in value]
    if isinstance(value, str):
        for secret in sorted((x for x in secrets if x), key=len, reverse=True):
            value = value.replace(secret, "[redacted]")
        return SECRET.sub("[redacted]", value)
    return value


class Commands:
    def __init__(self):
        self.env = dict(os.environ)
        for key in list(self.env):
            if key.startswith("IDB_"):
                self.env.pop(key)
        self.env["AI_AGENT"] = "trill-demo-smoke/1"
        self.secrets = []
        self.events = []
        self.allowed = set()

    def call(self, argv, *, timeout=30, mutate=False):
        start = time.monotonic()
        # Arguments/output never enter logs: open includes the invitation, text includes drafts.
        label = Path(str(argv[0])).name
        if str(argv[0]) == "mise":
            label = "idb"
            if len(argv) > 4 and argv[4] in ("ui", "open", "screenshot", "describe", "install", "launch", "terminate", "rotation", "hinge", "add-media"):
                label += "/" + argv[4]
                if argv[4] == "ui" and len(argv) > 5 and argv[5] in ("tap", "key", "text", "button", "describe-all", "describe-point", "set-value"):
                    label += "/" + argv[5]
        try:
            result = subprocess.run([str(x) for x in argv], env=self.env, cwd=ROOT,
                                    capture_output=True, text=True, timeout=timeout)
        except (OSError, subprocess.TimeoutExpired) as exc:
            error = Uncertain if mutate else Blocked
            raise error("Tool unavailable or timed out; no action retried") from exc
        self.events.append({"tool": label, "seconds": round(time.monotonic() - start, 3), "returncode": result.returncode})
        if result.returncode:
            error = Uncertain if mutate else Blocked
            raise error("Tool failed; raw output suppressed and no action retried")
        return result.stdout

    def simctl(self, *argv, **kwargs):
        return self.call(["xcrun", "simctl", *argv], **kwargs)

    def inventory(self):
        try:
            return json.loads(self.simctl("list", "-j"))
        except ValueError as exc:
            raise Blocked("Simulator inventory unavailable") from exc

    def capabilities(self):
        tap = self.call(["mise", "exec", "--", "idb", "ui", "tap", "--help"])
        describe = self.call(["mise", "exec", "--", "idb", "ui", "describe-all", "--help"])
        capture = self.call(["mise", "exec", "--", "idb", "screenshot", "--help"])
        if not all(flag in tap for flag in ("ax-coresimulator", "--expected-key", "--expected-value")) or "--client-type" not in describe or "--crop" not in capture:
            raise Blocked("Installed IDB lacks required guarded native AX/crop support")
        return {"idb_tap_help_sha256": hashlib.sha256(tap.encode()).hexdigest(),
                "idb_describe_help_sha256": hashlib.sha256(describe.encode()).hexdigest(),
                "xcode": self.call(["xcodebuild", "-version"]).strip()}

    def idb(self, udid, *argv, mutate=False, **kwargs):
        if udid not in self.allowed or udid in load_protected_simulator_ids(PROTECTED_SIMULATORS_PATH, Blocked):
            raise Blocked("IDB target is not a freshly verified owned simulator")
        split = 2 if argv[0] == "ui" else 1
        return self.call(["mise", "exec", "--", "idb", *argv[:split], "--udid", udid,
                          "--reason", "Test Trill on a dedicated smoke simulator", *argv[split:]],
                         mutate=mutate, **kwargs)


def app_identity(path, source_revision):
    app = absolute(path)
    try:
        info = plistlib.loads((app / "Info.plist").read_bytes())
        executable = app / info["CFBundleExecutable"]
        if info["CFBundleIdentifier"] != BUNDLE or executable.is_symlink() or executable.resolve().parent != app:
            raise Blocked("App bundle/executable identity mismatch")
        if info.get("DTPlatformName") != "iphonesimulator" and "iPhoneSimulator" not in info.get("CFBundleSupportedPlatforms", []):
            raise Blocked("Harness requires a prebuilt simulator app; physical TestFlight is a separate check")
        if not re.fullmatch(r"[a-f0-9]{40}", source_revision):
            raise Blocked("Supply the exact 40-character source revision used to build this app")
        return {"path": str(app), "bundle": BUNDLE, "executable_sha256": sha256(executable),
                "version": info.get("CFBundleShortVersionString"), "build": info.get("CFBundleVersion"),
                "source_revision": source_revision, "source_revision_basis": "operator-supplied build provenance; not inferred from working tree"}
    except (OSError, KeyError, ValueError, plistlib.InvalidFileException) as exc:
        raise Blocked("--app must be an exact prebuilt Trill simulator .app path") from exc


def validate_status(value):
    if not isinstance(value, dict) or value.get("kind") != "trill-demo-v1" or value.get("running") is not True:
        raise Blocked("Backend is not a running synthetic Trill demo")
    try:
        url = urllib.parse.urlsplit(value.get("address", ""))
        port = url.port
        loopback = url.hostname == "localhost" or ipaddress.ip_address(url.hostname or "").is_loopback
    except (ValueError, TypeError, AttributeError):
        raise Blocked("Demo address is invalid") from None
    if url.scheme != "https" or not loopback or not port or url.username or url.password or url.path not in ("", "/") or url.query or url.fragment:
        raise Blocked("Local smoke backend must advertise loopback HTTPS")
    if value.get("profile") not in ("herdr",):
        raise Blocked("Unknown demo capability profile")
    if not isinstance(value.get("fingerprint"), str):
        raise Blocked("Demo certificate identity is invalid")
    fingerprint = re.sub(r"[:\s]", "", value.get("fingerprint", "")).lower()
    if not re.fullmatch(r"[a-f0-9]{64}", fingerprint):
        raise Blocked("Demo certificate identity is missing")
    if type(value.get("pid")) is not int or value["pid"] <= 1 or type(value.get("previewPort")) is not int or not 1 <= value["previewPort"] <= 65535:
        raise Blocked("Backend process/preview identity is missing")
    return {"kind": value["kind"], "address": value["address"], "profile": value["profile"],
            "fingerprint": fingerprint, "pid": value["pid"], "previewPort": value["previewPort"]}


class StateView:
    """Small adapter for safe status state. Missing evidence is blocked, never inferred."""
    def __init__(self, status):
        self.state = status.get("state", {})
        if not isinstance(self.state, dict):
            raise Blocked("Demo safe state is unavailable")

    def records(self, key):
        value = self.state.get(key, self.state.get("catalog", {}).get(key))
        if isinstance(value, dict):
            value = list(value.values())
        if not isinstance(value, list) or any(not isinstance(x, dict) for x in value):
            raise Blocked("Demo safe state must expose " + key)
        return value

    def session(self, identifier):
        matches = [x for x in self.records("sessions") if x.get("id") == identifier]
        if len(matches) != 1:
            raise SmokeError("Expected fixture session is missing or duplicated")
        return matches[0]

    def live_session(self, provider=None):
        matches = [x for x in self.records("sessions") if x.get("attachable", x.get("status") in ("running", "waiting")) is True
                   and not x.get("archived", False) and x.get("status") not in ("stopped", "completed", "failed")
                   and (provider is None or x.get("provider") == provider)]
        if not matches:
            raise Blocked("No attachable fixture session is exposed in safe state")
        return sorted(matches, key=lambda x: (x.get("provider") != "herdr", x.get("id") != "demo-herdr-1", str(x.get("id", ""))))[0]

    def heading(self, session_id):
        session = self.session(session_id)
        value = session.get("heading", self.state.get("heading", self.state.get("site", {}).get("heading")))
        if not isinstance(value, str):
            raise Blocked("Safe state needs a heading receipt for the selected session")
        return value

    def uploads(self, session_id):
        return [x for x in self.records("uploads") if x.get("sessionId", x.get("sessionID")) == session_id]


class Backend:
    def __init__(self, commands, binary, state, root):
        self.c, self.binary, self.root = commands, absolute(binary), root
        if not self.binary.is_file() or not os.access(self.binary, os.X_OK):
            raise Blocked("--trill must name the exact executable")
        self.path = absolute(state)
        if self.path not in (root / "backend-a", root / "backend-b"):
            raise Blocked("Backend must use this harness's backend-a or backend-b directory")
        private_directory(self.path)
        self.identity = validate_status(self.status(check=False))
        self.binary_sha256 = sha256(self.binary)

    def call(self, command, *argv):
        operand, options = (argv[:1], argv[1:]) if command == "fault" else ((), argv)
        return self.c.call([self.binary, "demo", command, *operand, "--state-dir", self.path, *options], timeout=15,
                           mutate=command in ("pair", "fault"))

    def fault(self, name):
        self.status()
        self.call("fault", name)

    def status(self, check=True):
        try:
            value = json.loads(self.call("status", "--json"))
        except ValueError as exc:
            raise Blocked("Demo status was not JSON") from exc
        identity = validate_status(value)
        if check and identity != self.identity:
            raise Blocked("Backend identity changed during the smoke run")
        return value

    def view(self):
        return self.adapt(self.status())

    def adapt(self, status):
        state = status.get("state", {})
        sessions = state.get("sessions", [])
        # The CLI's minimal safe status deliberately excludes terminal transcripts and
        # headings. For initial fixture sessions, inspect only their known owned HTML
        # output as an independent command receipt. No credentials or persisted raw
        # terminal state are read. Unknown/new session directories stay unsupported.
        for session in sessions:
            identifier, project = session.get("id", ""), session.get("projectId", "")
            if "heading" not in session and re.fullmatch(r"demo-(herdr)-[1-6]", identifier) and project in ("demo-herdr",):
                path = self.path / "fixtures" / project / "web" / "index.html"
                if path.resolve() != path or not path.is_file() or path.stat().st_size > 1_048_576:
                    raise Blocked("Owned fixture HTML receipt is missing or redirected")
                parser = HeadingParser()
                parser.feed(path.read_text())
                if parser.heading is None:
                    raise Blocked("Owned fixture HTML has no heading receipt")
                session["heading"] = parser.heading
        if "projects" not in state:
            # Current fixed fixture names, checked against actual native UI before use.
            projects = {x.get("projectId"): x.get("provider") for x in sessions}
            state["projects"] = [{"id": key, "name": "Demo · " + str(provider).capitalize()}
                                 for key, provider in projects.items() if key]
        return StateView(status)

    def wait(self, predicate, timeout=20):
        deadline = time.monotonic() + timeout
        while True:
            status = self.status()
            if predicate(self.adapt(status)):
                return status
            if time.monotonic() >= deadline:
                raise SmokeError("Backend state did not acknowledge the native UI action")
            time.sleep(0.2)

    def invitation(self):
        self.status()
        value = self.call("pair", "--url").strip()
        if not value.startswith("trill://pair?") or any(x.isspace() for x in value):
            raise Blocked("Demo pair must return one normal invitation URL")
        self.c.secrets.append(value)
        # The complete URL and every payload/query component are private even on later failures.
        for _, component in urllib.parse.parse_qsl(urllib.parse.urlsplit(value).query):
            self.c.secrets.append(component)
        return value


def verify_simulator(record, inventory, owner_id):
    udid = record.get("udid", "").upper()
    try:
        uuid.UUID(udid)
    except (ValueError, TypeError) as exc:
        raise Blocked("Invalid owned simulator identity") from exc
    if (udid in load_protected_simulator_ids(PROTECTED_SIMULATORS_PATH, Blocked)
            or record.get("owner") != owner_id or record.get("kind") != OWNER):
        raise Blocked("Protected or unowned simulator")
    matches = [(runtime, device) for runtime, devices in inventory.get("devices", {}).items()
               for device in devices if device.get("udid", "").upper() == udid]
    if len(matches) != 1:
        raise Blocked("Owned simulator missing or duplicated")
    runtime, device = matches[0]
    if (runtime != record.get("runtime") or device.get("deviceTypeIdentifier") != record.get("device_type")
            or device.get("name") != record.get("name") or not device.get("isAvailable")):
        raise Blocked("Simulator ownership/runtime/type/name mismatch")
    return device


def create_simulator(commands, root, marker, profile, runtime):
    protected_simulator_ids = load_protected_simulator_ids(PROTECTED_SIMULATORS_PATH, Blocked)
    inventory = commands.inventory()
    device_type, default_runtime = PROFILES[profile]
    runtime = runtime or default_runtime
    runtimes = [x for x in inventory.get("runtimes", []) if x.get("identifier") == runtime and x.get("isAvailable")]
    if len(runtimes) != 1 or device_type not in {x.get("identifier") for x in inventory.get("devicetypes", [])}:
        raise Blocked("Requested native device type/runtime is unavailable")
    existing_simulator_ids = {device.get("udid", "").upper()
                              for devices in inventory.get("devices", {}).values() for device in devices}
    name = "Trill Smoke " + marker["id"][:8] + " " + profile + " " + uuid.uuid4().hex[:8]
    udid = commands.simctl("create", name, device_type, runtime, mutate=True).strip().upper()
    if udid in existing_simulator_ids or udid in protected_simulator_ids:
        raise Blocked("Simulator create returned an existing/protected identity; no UI action allowed")
    record = {"kind": OWNER, "owner": marker["id"], "udid": udid, "name": name,
              "profile": profile, "device_type": device_type, "runtime": runtime,
              "runtime_build": runtimes[0].get("buildversion"), "createdAt": now()}
    # Record before boot/install; never adopt a user supplied target.
    registry = read_json(root / "simulators.json")
    registry.append(record)
    write_json(root / "simulators.json", registry)
    verify_simulator(record, commands.inventory(), marker["id"])
    commands.allowed.add(udid)
    return record


def elements(value):
    if isinstance(value, dict):
        if any(key in value for key in ("AXLabel", "AXUniqueId", "role")):
            yield value
        for child in value.values():
            yield from elements(child)
    elif isinstance(value, list):
        for child in value:
            yield from elements(child)


class HeadingParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.inside = False
        self.parts = []
        self.heading = None

    def handle_starttag(self, tag, attrs):
        if tag == "h1":
            self.inside = True

    def handle_data(self, value):
        if self.inside:
            self.parts.append(value)

    def handle_endtag(self, tag):
        if tag == "h1" and self.inside:
            self.heading = "".join(self.parts)
            self.inside = False


def selector(value, key="AXUniqueId", *, role=None, prefix=False, contains=None):
    return {"value": value, "key": key, "role": role, "prefix": prefix, "contains": contains}


def label(value, role="AXButton", prefix=False, contains=None):
    return selector(value, "AXLabel", role=role, prefix=prefix, contains=contains)


def session_label(session):
    return label(session["name"], prefix=True, contains="Demo · " + session["provider"])


def visual_nonce():
    """Fresh public display marker; exact observation still required."""
    return "".join(secrets.choice(VISUAL_NONCE_ALPHABET) for _ in range(VISUAL_NONCE_LENGTH))


def frame_contains(outer, inner):
    return (inner.get("width", 0) > 0 and inner.get("height", 0) > 0
            and inner.get("x", -1) >= outer["x"] and inner.get("y", -1) >= outer["y"]
            and inner.get("x", 0) + inner.get("width", 0) <= outer["x"] + outer["width"]
            and inner.get("y", 0) + inner.get("height", 0) <= outer["y"] + outer["height"])


def editor_text(field):
    if "AXValue" not in field:
        raise Blocked("Native editor does not expose its value")
    value = field["AXValue"]
    if value is None and field.get("role") in ("AXTextArea", "AXTextField"):
        value = ""
    if not isinstance(value, str):
        raise Blocked("Native editor value is not text")
    return value


def catalog_context(tree, prompt):
    nodes = list(elements(tree))
    if any(item.get("role") in ("AXDialog", "AXAlert") or item.get("AXUniqueId") in ("preview-address", "browser-options")
           or (item.get("AXLabel") == "Page preview" and item.get("role") == "AXGroup") for item in nodes):
        return False
    sheets = [item for item in nodes if item.get("role") == "AXSheet"]
    if prompt == "Sessions, projects, or branches":
        return not sheets and len(matching(tree, selector("open-projects", role="AXButton"))) == 1
    if prompt not in ("Projects, sessions, or branches", "Sessions, agents, or branches"):
        return False
    if len(matching(tree, selector("close-projects", role="AXButton"))) != 1:
        return False
    # The known Projects sheet is allowed; unrelated overlaid sheets are not.
    if any(len(matching(sheet, selector("close-projects", role="AXButton"))) != 1 for sheet in sheets):
        return False
    headings = {str(item.get("AXLabel", "")).casefold() for item in nodes
                if item.get("role") in ("AXStaticText", "AXHeading", "AXNavigationBar")
                and item.get("enabled", True) and item.get("frame", {}).get("width", 0) > 0
                and item.get("frame", {}).get("height", 0) > 0}
    return "projects" in headings if prompt == "Projects, sessions, or branches" else {"current", "archived"} <= headings


def session_name_in_row(tree, row, name):
    names = matching(row, label(name, role="AXStaticText"))
    if not names:  # Some AX clients return a flat list rather than descendants.
        names = matching(tree, label(name, role="AXStaticText"))
    names = [item for item in names if frame_contains(row["frame"], item["frame"])]
    if len(names) != 1:
        raise Blocked("Session context menu needs one verified name inside its row")
    return names[0]


def native_application(tree, expected=None):
    applications = [item for item in elements(tree) if item.get("role") == "AXApplication"]
    if len(applications) != 1:
        raise Blocked("Native AX needs one unambiguous application root")
    app = applications[0]
    name, frame = app.get("AXLabel"), app.get("frame", {})
    if (not isinstance(name, str) or not name.strip() or not app.get("enabled", True)
            or any(not isinstance(frame.get(key), (int, float)) or not math.isfinite(frame[key])
                   for key in ("x", "y", "width", "height"))
            or frame["width"] <= 0 or frame["height"] <= 0):
        raise Blocked("Native AX application root is unnamed or has unusable geometry")
    if expected is not None and name != expected:
        raise Blocked("Expected native application identity is unavailable")
    return app


def native_absence_context(tree):
    app = native_application(tree, "Trill")
    for item in elements(tree):
        if (item.get("role") in ACTIONS and item.get("enabled", True)
                and frame_contains(app["frame"], item.get("frame", {}))
                and (item.get("AXUniqueId") or item.get("AXLabel")
                     or (item.get("role") in ("AXTextField", "AXTextArea") and "AXValue" in item))):
            return app
    raise Blocked("Native absence needs meaningful foreground Trill controls, not a root-only or loading snapshot")


def within(target, container, frame):
    return {**target, "container": {"selector": container, "frame": dict(frame)}}


def matching(tree, target):
    bounds = None
    if target.get("container"):
        scope = target["container"]
        containers = matching(tree, scope["selector"])
        if len(containers) != 1 or containers[0]["frame"] != scope["frame"]:
            raise ScopedContainerChanged("Scoped native container is missing, ambiguous or moved")
        bounds = containers[0]["frame"]
    matches = []
    for element in elements(tree):
        value = element.get(target["key"])
        expected = target["value"]
        equal = value == expected or (target["prefix"] and isinstance(value, str) and value.startswith(expected + ","))
        frame = element.get("frame", {})
        includes = not target.get("contains") or target["contains"].casefold() in str(value).casefold()
        if bounds is not None and not frame_contains(bounds, frame):
            continue
        if (equal and includes and (target["role"] is None or element.get("role") == target["role"])
                and element.get("enabled", True) and frame.get("width", 0) > 0 and frame.get("height", 0) > 0):
            matches.append(element)
    if len(matches) > 1:
        actions = [x for x in matches if x.get("role") in ACTIONS]
        if len(actions) == 1:
            return actions
    return matches


class UI:
    def __init__(self, commands, udid, output, timeout=25):
        self.c, self.udid, self.output, self.timeout = commands, udid, output, timeout
        self.last = None
        self.paired = False
        self.evidence = []
        self.ocr = None
        self.public_hashes = set()

    def tree(self, mode="xctest"):
        options = ["--api", "axbridge"] if mode == "web" else ["--api", "ax-coresimulator"]
        if mode in ("xctest", "voiceover"):
            options += ["--client-type", "xctest" if mode == "xctest" else "voiceOver"]
        try:
            tree = json.loads(self.c.idb(self.udid, "ui", "describe-all", "--json", "--filter", "all", *options))
        except ValueError as exc:
            raise Blocked("IDB accessibility response was not JSON") from exc
        self.last = tree
        if mode != "web":
            native_application(tree)
        return tree

    def wait(self, target, *, timeout=None, absent=False):
        deadline = time.monotonic() + (self.timeout if timeout is None else timeout)
        stable = None
        while True:
            available = False
            try:
                tree = self.tree()
                if absent:
                    native_absence_context(tree)
                matches = matching(tree, target)
                available = True
            except ScopedContainerChanged:
                raise
            except Blocked:
                matches = []
            if not matches and (not absent or not available):
                try:
                    tree = self.tree("voiceover")
                    if absent:
                        native_absence_context(tree)
                    matches = matching(tree, target)
                    available = True
                except ScopedContainerChanged:
                    raise
                except Blocked:
                    matches = []
            if len(matches) > 1:
                raise Blocked("Accessibility selector is ambiguous; no action sent")
            current = matches[0] if matches else None
            if absent:
                if available and not matches and stable is False:
                    return None
                stable = (False if not matches else True) if available else None
            elif current is not None and current == stable:
                return current
            else:
                stable = current
            if time.monotonic() >= deadline:
                raise Blocked("Expected native accessibility state is unavailable")
            time.sleep(0.2)

    def exists(self, target):
        for mode in ("xctest", "voiceover"):
            try:
                if matching(self.tree(mode), target):
                    return True
            except Blocked:
                continue  # Readiness probes may repeat reads, never actions.
        return False

    def tap(self, target, *, duration=None):
        element = self.wait(target)
        value = element[target["key"]]
        frame = element["frame"]
        x, y = round(frame["x"] + frame["width"] / 2), round(frame["y"] + frame["height"] / 2)
        # Native AX + exact identity guard avoids stale HID geometry on unfolded Duo.
        guard_key, guard_value = (target["key"], value) if value is not None else ("role", element["role"])
        args = ["ui", "tap", str(x), str(y), "--api", "ax-coresimulator", "--expected-key", guard_key, "--expected-value", guard_value]
        if duration is not None:
            parent = None
            if target["key"] == "AXLabel" and target["prefix"] and target.get("contains"):
                # A worktree row's center may be its provider/branch text. Resolve
                # the name inside the unique current row, not a global name match.
                tree = self.tree()
                rows = matching(tree, target)
                if (len(rows) != 1 or rows[0]["frame"] != frame
                        or rows[0].get("AXLabel") != value or rows[0].get("role") != element.get("role")):
                    raise Blocked("Session row changed before its context-menu press")
                parent = rows[0]
                element = session_name_in_row(tree, parent, target["value"])
                frame = element["frame"]
                guard_key, guard_value = "AXLabel", target["value"]
                x, y = round(frame["x"] + frame["width"] / 2), round(frame["y"] + frame["height"] / 2)
            # Context menus require an actual HID hold. The point must identify
            # this exact live name/element, including its role and geometry.
            try:
                hit = json.loads(self.c.idb(self.udid, "ui", "describe-point", str(x), str(y), "--api", "ax-coresimulator", "--client-type", "xctest", "--json"))
            except ValueError as exc:
                raise Blocked("Long-press native point check was invalid") from exc
            exact = any(item.get(guard_key) == guard_value and item.get("role") == element.get("role")
                        and item.get("frame") == frame for item in elements(hit))
            if parent is not None:
                def same_parent(item):
                    return all(item.get(key) == parent.get(key) for key in ("AXLabel", "role", "frame")) and item.get("enabled", True)
                buttons = [item for item in elements(hit) if item.get("role") == "AXButton"]
                parent_hit = len(buttons) == 1 and same_parent(buttons[0])
                if buttons and not parent_hit:
                    raise Blocked("Native name point identifies a different or moved parent row")
                if not (frame["x"] <= x < frame["x"] + frame["width"]
                        and frame["y"] <= y < frame["y"] + frame["height"]):
                    raise Blocked("Context-menu point is outside the verified session name")
                if parent_hit:
                    # Native hit testing often returns the enclosing AXButton.
                    # Re-read both row and name after the hit; never use the row
                    # center or accept a name that moved inside an unchanged row.
                    fresh = self.tree()
                    rows = matching(fresh, target)
                    if len(rows) != 1 or not same_parent(rows[0]):
                        raise Blocked("Session parent row changed during point verification")
                    current_name = session_name_in_row(fresh, rows[0], target["value"])
                    if current_name["frame"] != frame:
                        raise Blocked("Session name moved during point verification")
                exact = exact or parent_hit
            if not exact:
                raise Blocked("Fresh native point does not identify the intended context-menu target")
            # Expected-value dispatches AX press, where duration is rejected.
            args = ["ui", "tap", str(x), str(y), "--duration", str(duration), "--api", "hid"]
        self.c.idb(self.udid, *args, mutate=True)

    def overflow(self):
        # Both identifiers are observed native system placements: 17 Pro top
        # toolbar and Duo side rail. Refuse to guess when more than one is live.
        tree = self.tree()
        candidates = [key for key in ("OverflowBarButtonItem", "BottomOverflowBarButtonItem")
                      if matching(tree, selector(key, role="AXButton"))]
        if len(candidates) != 1:
            raise Blocked("Native overflow placement is missing or ambiguous")
        self.tap(selector(candidates[0], role="AXButton"))

    def tap_observed_prefix(self, prefix):
        deadline = time.monotonic() + self.timeout
        while True:
            candidates = [item for item in elements(self.tree()) if (str(item.get("AXLabel", "")).startswith(prefix) or (prefix == "Project," and item.get("AXLabel") == "Project"))
                          and item.get("role") in ("AXButton", "AXPopUpButton", "AXRadioButton") and item.get("enabled", True)]
            if len(candidates) == 1:
                return self.tap(label(candidates[0]["AXLabel"], role=candidates[0]["role"]))
            if len(candidates) > 1:
                raise Blocked("Observed picker label prefix is ambiguous")
            if time.monotonic() >= deadline:
                raise Blocked("Observed picker option is unavailable")
            time.sleep(0.2)

    def press_observed_button(self, button):
        frame = button["frame"]
        x, y = round(frame["x"] + frame["width"] / 2), round(frame["y"] + frame["height"] / 2)
        self.c.idb(self.udid, "ui", "tap", str(x), str(y), "--api", "ax-coresimulator",
                   "--expected-key", "AXLabel", "--expected-value", button["AXLabel"], mutate=True)

    def search_field(self, prompt):
        target = label(prompt, role="AXTextField")
        if self.exists(target):
            return target
        target = selector("AXTextField", "role", role="AXTextField")
        tree = self.tree()
        if not matching(tree, target):
            if not catalog_context(tree, prompt):
                raise Blocked("Search activation needs a verified catalog, not a browser or unknown modal")
            buttons = matching(tree, label("Search"))
            if len(buttons) != 1:
                raise Blocked("Catalog must expose one enabled native Search button")
            button = self.wait(label("Search"))
            fresh = self.tree()
            if not matching(fresh, target):
                current = matching(fresh, label("Search"))
                if (not catalog_context(fresh, prompt) or len(current) != 1
                        or current[0]["frame"] != button["frame"]):
                    raise Blocked("Catalog Search context changed before activation")
                self.press_observed_button(current[0])  # One activation, never a retry.
        field = self.wait(target)
        if field.get("AXLabel") not in (None, "", prompt):
            raise Blocked("Unique native field is not the expected catalog search")
        if not field.get("AXLabel"):
            tree = self.tree()
            fields = matching(tree, target)
            icons = [item for item in elements(tree) if item.get("role") == "AXImage"
                     and (item.get("AXUniqueId") == "magnifyingglass" or item.get("AXLabel") == "Search")
                     and frame_contains(field["frame"], item.get("frame", {}))]
            if len(fields) != 1 or fields[0]["frame"] != field["frame"] or not icons:
                raise Blocked("Unlabelled field needs a current native search affordance")
        return target

    def value(self, target):
        return editor_text(self.wait(target))

    def prepare_input(self, target, expected):
        def context(tree):
            fields = matching(tree, target)
            if len(fields) != 1 or fields[0].get("role") not in ("AXTextField", "AXTextArea"):
                raise Blocked("Input needs one current native editor")
            if editor_text(fields[0]) != expected:
                raise Uncertain("Editor changed before input; no text or replacement sent")
            return fields[0]
        tree = self.tree()
        field = context(tree)
        tips = matching(tree, label(KEYBOARD_TUTORIAL, role="AXStaticText"))
        buttons = matching(tree, label("Continue"))
        if not tips and not buttons:
            return field
        if len(tips) != 1 or len(buttons) != 1:
            raise Blocked("Unrecognized or ambiguous keyboard tutorial; no Continue or input sent")
        button = self.wait(label("Continue"))
        fresh = self.tree()
        current_field = context(fresh)
        current_tips = matching(fresh, label(KEYBOARD_TUTORIAL, role="AXStaticText"))
        current_buttons = matching(fresh, label("Continue"))
        if (len(current_tips) != 1 or len(current_buttons) != 1
                or current_buttons[0]["frame"] != button["frame"]
                or current_field["frame"] != field["frame"]):
            raise Blocked("Keyboard tutorial or editor context changed before Continue")
        self.press_observed_button(current_buttons[0])
        self.wait(label(KEYBOARD_TUTORIAL, role="AXStaticText"), absent=True)
        self.wait(label("Continue"), absent=True)
        return context(self.tree())  # Recheck the exact draft after dismissal.

    def wait_value(self, target, expected):
        deadline = time.monotonic() + self.timeout
        while True:
            if self.value(target) == expected:
                return
            if time.monotonic() >= deadline:
                raise Uncertain("Native edit was not verified; inspect before retrying")
            time.sleep(0.2)

    def type_once(self, target, text, *, empty=""):
        self.tap(target)
        previous = self.value(target)
        if previous not in (empty if isinstance(empty, tuple) else (empty,)):
            raise Uncertain("Editor is not empty; refusing to overwrite or append stale input")
        # The harness only generates public ASCII commands. HID Shift issues cannot silently change them.
        if not re.fullmatch(r"[a-z0-9 .:/_-]{1,512}", text):
            raise Blocked("Harness input must be bounded public lowercase ASCII")
        self.prepare_input(target, previous)
        self.c.idb(self.udid, "ui", "text", text, mutate=True)
        self.wait_value(target, text)  # Read-only retries only. Never resend text/Insert/Return.

    def replace_once(self, target, value, previous, *, select_all=False):
        self.tap(target)
        if self.value(target) != previous:
            raise Uncertain("Draft changed before the deliberate replacement; no write sent")
        field = self.prepare_input(target, previous)
        frame = field["frame"]
        point = [str(round(frame["x"] + frame["width"] / 2)), str(round(frame["y"] + frame["height"] / 2))]
        if field.get("role") == "AXTextField" and (select_all or field.get("AXLabel") is not None):
            if not re.fullmatch(r"[a-z0-9 .:/_-]{1,512}", value):
                raise Blocked("Field replacement needs bounded public lowercase text")
            # UISearchTextField rejects AX setters even when its label is absent.
            # Search explicitly requests native Select All; verify before Submit.
            self.c.idb(self.udid, "ui", "key", "4", "--command", mutate=True)
            self.c.idb(self.udid, "ui", "text", value, mutate=True)
        else:
            # AX alone does not notify the UITextView binding. A sentinel and one
            # real Backspace supplies its editing callback, as in the capture runner.
            self.c.idb(self.udid, "ui", "set-value", *point, "--api", "ax-coresimulator", "--value", value + " ", mutate=True)
            self.c.idb(self.udid, "ui", "key", "42", mutate=True)
        self.wait_value(target, value)

    def select_synthetic_photo(self, *, choose=True, name="synthetic-picker"):
        self.safe_screen()
        image = self.output / (name + ".png")
        deadline = time.monotonic() + max(self.timeout, 90)
        while True:
            self.c.idb(self.udid, "screenshot", image)
            os.chmod(image, 0o600)
            width, height, pixels = png_rgb(image)
            boxes = checker_boxes(width, height, pixels)
            if len(boxes) == 1:
                break
            if len(boxes) > 1 or time.monotonic() >= deadline:
                raise Blocked("Fresh picker image must contain exactly one matching generated checkerboard thumbnail")
            time.sleep(0.2)  # Wait for indexing/picker presentation; never repeat a selection.
        applications = [x for x in elements(self.tree()) if x.get("role") == "AXApplication"]
        if len(applications) != 1:
            raise Blocked("Picker coordinate scale is unavailable")
        screen = applications[0]["frame"]
        scale_x, scale_y = width / screen["width"], height / screen["height"]
        if abs(scale_x - scale_y) > 0.02:
            raise Blocked("Picker screenshot and live AX coordinate scale disagree")
        box = boxes[0]
        x, y = round((box[0] + box[2] / 2) / scale_x), round((box[1] + box[3] / 2) / scale_y)
        try:
            read = json.loads(self.c.idb(self.udid, "ui", "describe-point", str(x), str(y), "--api", "ax-coresimulator", "--json"))
        except ValueError as exc:
            raise Blocked("Picker thumbnail identity read was invalid") from exc
        candidates = [item for item in elements(read) if item.get("role") == "AXImage" and item.get("AXLabel") == "Photo"
                      and item.get("AXUniqueId") == "PXGGridLayout-Info"]
        if len(candidates) != 1:
            raise Blocked("Generated thumbnail point is not the observed native Photo grid element")
        bounds = candidates[0]["frame"]
        if not (bounds["x"] <= x <= bounds["x"] + bounds["width"] and bounds["y"] <= y <= bounds["y"] + bounds["height"]):
            raise Blocked("Fresh thumbnail frame does not contain the visually verified point")
        write_json(self.output / (name + "-match.json"), {"pixel_box": box, "points": [x, y], "scale": scale_x,
                                                               "native_id": "PXGGridLayout-Info", "udid": self.udid})
        for file in (image, self.output / (name + "-match.json")):
            self.evidence.append({"file": file.name, "sha256": sha256(file)})
        # This dynamic coordinate was derived from this screenshot and verified at the
        # live native grid. AX image press does not select a thumbnail; HID does.
        if choose:
            self.c.idb(self.udid, "ui", "tap", str(x), str(y), "--api", "hid", mutate=True)

    def safe_screen(self, tree=None):
        tree = self.tree() if tree is None else tree
        if not self.paired or any(x.get("AXUniqueId") in ("pairing-token", "confirm-pairing", "pairing-error") for x in elements(tree)):
            raise Blocked("No imagery or UI dump may be captured on a pairing screen")
        raw = json.dumps(tree)
        for public_hash in self.public_hashes:
            raw = raw.replace(public_hash, "[public-fixture-hash]")
        if SECRET.search(raw) or any(secret and secret in raw for secret in self.c.secrets):
            raise Blocked("Screen may contain credentials; screenshot suppressed")
        return tree

    def receipt(self, name, tree=None):
        if (self.output / (name + ".ax.json")).exists():
            name += "-" + uuid.uuid4().hex[:8]
        tree = self.safe_screen(tree)
        safe = []
        for x in elements(tree):
            # AXValue can hold arbitrary drafts; record presence, never the value itself.
            safe.append({"id": redact(x.get("AXUniqueId"), self.c.secrets),
                         "label": redact(x.get("AXLabel"), self.c.secrets),
                         "role": x.get("role"), "enabled": x.get("enabled"), "frame": x.get("frame"),
                         "has_value": "AXValue" in x})
        path = self.output / (name + ".ax.json")
        write_json(path, safe)
        image = self.output / (name + ".png")
        try:
            self.c.idb(self.udid, "screenshot", image)
            os.chmod(image, 0o600)
            evidence = [{"file": path.name, "sha256": sha256(path)}, {"file": image.name, "sha256": sha256(image)}]
        except BaseException:
            image.unlink(missing_ok=True)
            raise
        self.evidence += evidence
        return evidence

    def observe_error(self, message, name):
        deadline = time.monotonic() + self.timeout
        while True:
            for mode in ("voiceover", "xctest"):
                tree = self.tree(mode)
                if any(message.casefold() in str(item.get("AXLabel", "")).casefold() and item.get("enabled", True)
                       and item.get("frame", {}).get("width", 0) > 0 and item.get("frame", {}).get("height", 0) > 0
                       for item in elements(tree)):
                    # The retryable-capacity notice can last one second. Preserve the
                    # actual observed AX snapshot; later screenshot may show recovery.
                    evidence = self.receipt(name, tree=tree)
                    evidence[0]["observation"] = "native AX contained expected error; paired screenshot is a subsequent frame"
                    return evidence
            if time.monotonic() >= deadline:
                raise Blocked("Expected native error was not observed; automatic recovery alone is insufficient")
            time.sleep(0.05)

    def current_region(self, target, expected=None, mode="xctest"):
        tree = self.safe_screen(self.tree(mode))
        regions = matching(tree, target)
        if len(regions) != 1:
            raise Blocked("Native capture region disappeared or is ambiguous")
        frame = regions[0]["frame"]
        if expected is not None and frame != expected:
            raise RegionChanged("Native capture region moved during observation")
        return tree, dict(frame)

    def visible_text(self, text, name, *, source=False, region=None):
        # Bracket both AX and OCR observations with current, unique region reads.
        # Never reuse a frame obtained before a pane disappeared or moved.
        if (self.output / (name + ".ax.json")).exists():
            name += "-" + uuid.uuid4().hex[:8]
        target = region or label("Page preview", role="AXGroup")
        deadline = time.monotonic() + self.timeout
        while True:
            if source:
                tree = self.safe_screen(self.tree())
                if any(x.get("role") == "AXStaticText" and text in str(x.get("AXLabel", ""))
                       and "<html" in str(x.get("AXLabel", "")).lower() for x in elements(tree)):
                    return self.receipt(name, tree=tree)
            else:
                try:
                    _, frame = self.current_region(target)
                    for mode in ("xctest", "web"):
                        tree, _ = self.current_region(target, frame)
                        if mode == "web":
                            try:
                                tree = self.safe_screen(self.tree(mode))
                            except Blocked:
                                # Web-process AX is optional. Native region identity
                                # remains mandatory; scoped OCR is the fallback.
                                continue
                        for item in elements(tree):
                            bounds = item.get("frame", {})
                            inside = (bounds.get("width", 0) > 0 and bounds.get("height", 0) > 0
                                      and bounds.get("x", -10000) >= frame["x"] and bounds.get("y", -10000) >= frame["y"]
                                      and bounds.get("x", 0) + bounds.get("width", 0) <= frame["x"] + frame["width"]
                                      and bounds.get("y", 0) + bounds.get("height", 0) <= frame["y"] + frame["height"])
                            if inside and item.get("role") in ("AXStaticText", "AXHeading") and text in str(item.get("AXLabel", "")):
                                current, _ = self.current_region(target, frame)
                                return self.receipt(name, tree=current)
                    self.current_region(target, frame)
                    image = self.output / (name + ".crop.png")
                    try:
                        rect = ",".join(str(round(frame[key])) for key in ("x", "y", "width", "height"))
                        self.c.idb(self.udid, "screenshot", image, "--crop", rect, "--units", "points")
                        os.chmod(image, 0o600)
                        self.current_region(target, frame)
                        if self.ocr is None:
                            self.ocr = self.output / "screen-text"
                            self.c.call(["xcrun", "swiftc", ROOT / "scripts/demo/screen_text.swift", "-parse-as-library", "-o", self.ocr], timeout=120)
                        try:
                            lines = json.loads(self.c.call([self.ocr, image]))
                        except ValueError as exc:
                            raise Blocked("Local OCR returned invalid evidence") from exc
                        current, _ = self.current_region(target, frame)
                        if text in " ".join(lines):
                            crop = {"file": image.name, "sha256": sha256(image), "region_points": frame,
                                    "observation": "local Vision OCR matched text; unique region and unchanged geometry verified before/after capture and OCR",
                                    "region_selector": target}
                            evidence = self.receipt(name, tree=current)
                            self.evidence.append(crop)
                            return evidence + [crop]
                    finally:
                        if not any(item.get("file") == image.name for item in self.evidence):
                            image.unlink(missing_ok=True)
                except RegionChanged:
                    # The attempt's crop is deleted by finally. A fresh loop must
                    # resolve the region again and bracket an entirely new capture.
                    pass
            if time.monotonic() >= deadline:
                raise Blocked("Expected content is not observable in the file source or scoped native preview")
            time.sleep(0.2)


class Journey:
    def __init__(self, ui, backend, secondary, manifest, background_seconds=65):
        self.ui, self.backend, self.secondary, self.manifest = ui, backend, secondary, manifest
        self.background_seconds = background_seconds
        self.session = None
        self.heading = None
        self.steps = None

    def assertion(self, name, function):
        started = time.monotonic()
        result = {"name": name, "outcome": "running", "startedAt": now(), "evidence": []}
        self.steps.append(result)
        try:
            evidence = function()
            result["evidence"] = evidence if isinstance(evidence, list) else []
            # Keep an independent, allowlisted backend receipt for every native assertion.
            state = self.backend.view()
            receipt = {"backend": self.backend.identity, "observedAt": now(),
                       "session_count": len(state.records("sessions"))}
            if self.session is not None:
                current = state.session(self.session["id"])
                receipt["session"] = {key: current.get(key) for key in ("id", "provider", "archived", "attachable", "resumable", "status", "revision", "columns", "rows")}
                try:
                    receipt["heading_sha256"] = hashlib.sha256(state.heading(self.session["id"]).encode()).hexdigest()
                except Blocked:
                    pass  # Not every lifecycle session must expose a preview heading.
            path = self.ui.output / ("backend-" + uuid.uuid4().hex[:10] + ".json")
            write_json(path, receipt)
            result["evidence"].append({"file": path.name, "sha256": sha256(path)})
            result["outcome"] = "pass"
        except Blocked as exc:
            result.update(outcome="blocked", reason=str(exc))
            with contextlib.suppress(SmokeError, OSError):
                result["evidence"] += self.ui.receipt("blocked-" + str(len(self.steps)), tree=self.ui.last)
            raise
        except SmokeError as exc:
            result.update(outcome="fail", reason=str(exc), uncertain=isinstance(exc, Uncertain))
            with contextlib.suppress(SmokeError, OSError):
                result["evidence"] += self.ui.receipt("failed-" + str(len(self.steps)))
            raise
        except Exception as exc:
            result.update(outcome="blocked", reason="Unexpected harness error; raw diagnostic suppressed")
            raise Blocked("Unexpected harness error; raw diagnostic suppressed") from exc
        finally:
            result["seconds"] = round(time.monotonic() - started, 3)
            write_json(self.ui.output / "manifest.json", self.manifest)
            print(json.dumps({"assertion": name, "outcome": result["outcome"], "seconds": result["seconds"]}), flush=True)

    def pair(self, backend):
        self.ui.c.idb(self.ui.udid, "open", backend.invitation(), mutate=True)
        # Any URL-open OS consent must be positively observed. No blind retry/open loop.
        if self.ui.exists(label("Open")):
            self.ui.tap(label("Open"))
        self.ui.tap(selector("confirm-pairing"))
        session = backend.view().live_session()
        self.ui.wait(selector("confirm-pairing"), absent=True)
        self.ui.wait(selector("open-projects"))
        self.search_sessions(session["name"])
        self.ui.wait(session_label(session))
        self.ui.paired = True
        return session

    def open_terminal(self):
        self.ui.tap(session_label(self.session))
        self.ui.tap(label("Allow once"))
        self.ui.wait(selector("terminal-return", role="AXButton"))
        self.ui.wait(selector("remote-terminal", role="AXTextArea"))
        return self.ui.receipt("terminal-connected")

    def command(self, heading, receipt):
        before = self.backend.view().heading(self.session["id"])
        if before == heading:
            raise SmokeError("Heading assertion requires a new unique value")
        self.ui.tap(selector("terminal-write"))
        self.ui.type_once(selector("terminal-draft", role="AXTextArea"), "demo heading " + heading)
        self.ui.tap(selector("terminal-insert"))
        self.ui.wait(selector("terminal-return"))
        if self.backend.view().heading(self.session["id"]) != before:
            raise SmokeError("Insert executed a command before deliberate Return")
        self.ui.tap(selector("terminal-return"))
        self.backend.wait(lambda state: state.heading(self.session["id"]) == heading)
        self.heading = heading
        return self.ui.receipt(receipt)

    def source(self):
        if self.ui.exists(label("Close workspace")):
            self.ui.tap(label("Close workspace"))
        self.ui.tap(selector("session-workspace"))
        self.ui.tap(label("Files", role="AXRadioButton"))
        self.ui.tap(label("web", prefix=True))
        self.ui.tap(label("index.html", prefix=True))
        return self.ui.visible_text(self.heading, "source-heading", source=True)

    def static_preview(self):
        self.ui.tap(selector("file-render-preview"))
        return self.ui.visible_text(self.heading, "static-preview-heading")

    def live_preview(self):
        self.ui.overflow()
        self.ui.tap(label("Preview a page"))
        target = selector("preview-address", role="AXTextField")
        if not self.ui.exists(target):
            self.ui.tap(selector("browser-options"))
            self.ui.tap(label("Enter a web address"))
        address = "localhost:" + str(self.backend.identity["previewPort"])
        if self.ui.value(target) != address:
            self.ui.type_once(target, address, empty=("", "localhost:3000 or https://…"))
        self.ui.tap(label("Go"))
        return self.ui.visible_text(self.heading, "live-preview-heading")

    def reconnect(self):
        if self.ui.exists(label("Close workspace")):
            self.ui.tap(label("Close workspace"))
        before = self.backend.view().heading(self.session["id"])
        start = time.monotonic()
        self.ui.c.idb(self.ui.udid, "ui", "button", "HOME", mutate=True)
        home = self.ui.wait(label("SpringBoard", role="AXApplication"))
        native_application([home], "SpringBoard")
        evidence = self.ui.receipt("background-native-springboard", tree=self.ui.last)
        # State-based foreground assertion plus measured background dwell. No global network changes.
        dwell_started = time.monotonic()
        deadline = dwell_started + self.background_seconds
        while time.monotonic() < deadline:
            time.sleep(max(0, min(0.5, deadline - time.monotonic())))
        confirmed_dwell = time.monotonic() - dwell_started
        self.ui.c.idb(self.ui.udid, "launch", "--foreground-if-running", BUNDLE, mutate=True)
        foreground = self.ui.wait(label("Trill", role="AXApplication"))
        native_application([foreground], "Trill")
        evidence += self.ui.receipt("foreground-native-trill", tree=self.ui.last)
        self.ui.wait(selector("terminal-return", role="AXButton"))
        if self.backend.view().heading(self.session["id"]) != before:
            raise SmokeError("Backgrounding lost parent-owned fixture state")
        evidence += self.command("smoke resumed " + visual_nonce(), "resumed-command")
        self.manifest.setdefault("backgrounds", []).append({
            "scenario": self.manifest["scenarios"][-1]["name"] if self.manifest["scenarios"] else "manual",
            "background_application": {"name": home["AXLabel"], "frame": home["frame"]},
            "foreground_application": {"name": foreground["AXLabel"], "frame": foreground["frame"]},
            "requested_dwell_seconds": self.background_seconds, "confirmed_dwell_seconds": round(confirmed_dwell, 3),
            "roundtrip_seconds_including_fresh_input": round(time.monotonic() - start, 3)})
        return evidence

    def happy(self):
        def pairing():
            self.session = self.pair(self.backend)
            return self.ui.receipt("paired-catalog")
        self.assertion("normal TLS invitation redemption and native catalog", pairing)
        self.assertion("terminal consent and HTTPS stream connected", self.open_terminal)
        self.assertion("verified draft Insert then deliberate Return changes backend heading",
                       lambda: self.command("smoke heading " + visual_nonce(), "heading-command"))
        self.assertion("live proxy visibly renders changed heading", self.live_preview)
        self.assertion("file source contains terminal-produced heading", self.source)
        self.assertion("static preview visibly renders changed heading", self.static_preview)
        self.assertion("background recovery accepts fresh input without retry/replay", self.reconnect)

    def to_sessions(self):
        if self.ui.exists(selector("close-projects")):
            self.ui.tap(selector("close-projects"))
        if self.ui.exists(label("Close workspace")):
            self.ui.tap(label("Close workspace"))
        if not self.ui.exists(label("New session")) and self.ui.exists(label("Sessions")):
            self.ui.tap(label("Sessions"))
            self.ui.wait(selector("open-projects"))
        # Re-read after Back: list search can remain active and hide its toolbar.
        if not self.ui.exists(label("New session")) and self.ui.exists(selector("open-projects")):
            prompt = "Sessions, projects, or branches"
            if (self.ui.exists(label(prompt, role="AXTextField"))
                    or self.ui.exists(selector("AXTextField", "role", role="AXTextField"))) and self.ui.exists(label("close")):
                self.ui.search_field(prompt)  # Reject an ambiguous/unrelated fallback.
                self.ui.tap(label("close"))
        # An empty/loading tree never implies a Back or Cancel action.
        self.ui.wait(label("New session"))

    def search_sessions(self, text, *, projects=False):
        prompt = "Sessions, agents, or branches" if projects else "Sessions, projects, or branches"
        field = self.ui.search_field(prompt)
        previous = self.ui.value(field)
        empty = ("", prompt)
        if previous in empty:
            self.ui.type_once(field, text.lower(), empty=empty)
        else:
            self.ui.replace_once(field, text.lower(), previous, select_all=True)
        self.ui.c.idb(self.ui.udid, "ui", "key", "40", mutate=True)  # Native Search submit, not terminal input.

    def lifecycle(self, *, existing_session=None):
        self.to_sessions()
        before = {x["id"] for x in self.backend.view().records("sessions")}
        def create():
            self.ui.tap(label("New session"))
            self.ui.tap_observed_prefix("Space,")
            self.ui.tap_observed_prefix("Demo · Herdr")
            self.ui.tap(selector("new-session-submit"))
            self.backend.wait(lambda state: len({x["id"] for x in state.records("sessions")} - before) == 1)
            created = [x for x in self.backend.view().records("sessions") if x["id"] not in before]
            self.session = created[0]
            if self.session.get("provider") != "herdr":
                raise SmokeError("Native creation did not use the selected Herdr fixture space")
            self.ui.wait(label("New session"))  # The form dismisses to Sessions; it does not auto-open the new terminal.
            self.search_sessions(self.session["name"])
            self.ui.wait(session_label(self.session))
            return self.ui.receipt("created-terminal")
        if existing_session is None:
            self.assertion("create terminal once via native form and backend receipt", create)
        else:
            self.session = self.backend.view().session(existing_session["id"])
            self.search_sessions(self.session["name"])
            self.ui.wait(session_label(self.session))
            self.assertion("find previously created owned fixture in native catalog", lambda: self.ui.receipt("existing-fixture"))
        def rename():
            self.ui.tap(session_label(self.session), duration=1)
            self.ui.tap(label("Rename…"))
            name = "smoke run " + uuid.uuid4().hex[:8]
            sheet = label("Rename “" + self.session["name"] + "”?", role="AXSheet")
            container = self.ui.wait(sheet)
            field = within(selector(None, "AXLabel", role="AXTextField"), sheet, container["frame"])
            self.ui.replace_once(field, name, self.session["name"])
            if self.ui.value(field) != name:
                raise Uncertain("Rename draft changed before Save; no Save sent")
            self.ui.tap(label("Save"))
            self.backend.wait(lambda state: state.session(self.session["id"]).get("name") == name)
            self.session = self.backend.view().session(self.session["id"])
            self.search_sessions(name)
            self.ui.wait(session_label(self.session))
            return self.ui.receipt("renamed-terminal")
        self.assertion("rename through native persistent confirmation and backend receipt", rename)
    def computer_row(self, address):
        matches = [x for x in elements(self.ui.tree()) if x.get("role") == "AXButton"
                   and str(x.get("AXUniqueId", "")).startswith("computer-")
                   and address in str(x.get("AXLabel", ""))]
        if len(matches) != 1:
            raise Blocked("Saved computer row must expose a unique identifier and its fixture address")
        return selector(matches[0]["AXUniqueId"])

    def saved_computers(self):
        self.to_sessions()
        self.ui.overflow()
        self.ui.tap(label("Computers"))
        self.ui.wait(selector("add-computer"))

    def check_address(self, address, name):
        self.to_sessions()
        self.ui.overflow()
        self.ui.tap(label("Connection details"))
        self.ui.wait(label(address, role=None))
        evidence = self.ui.receipt(name)
        self.ui.tap(label("Done"))
        return evidence

    def computers(self):
        if self.secondary is None:
            raise Blocked("Two-computers scenario needs a separate owned backend-b process")
        self.to_sessions()
        self.pair(self.secondary)
        self.check_address(self.secondary.identity["address"], "second-computer-address")
        self.saved_computers()
        first = self.computer_row(self.backend.identity["address"])
        second = self.computer_row(self.secondary.identity["address"])
        self.ui.tap(first)
        self.check_address(self.backend.identity["address"], "switched-first-computer")
        self.ui.c.idb(self.ui.udid, "terminate", BUNDLE, mutate=True)
        self.ui.c.idb(self.ui.udid, "launch", "--foreground-if-running", BUNDLE, mutate=True)
        evidence = self.check_address(self.backend.identity["address"], "restored-first-computer")
        self.saved_computers()
        self.ui.wait(first)
        self.ui.wait(second)
        self.ui.tap(second, duration=1)
        self.ui.tap(label("Forget computer…"))
        self.ui.tap(label("Forget computer"))
        self.ui.wait(second, absent=True)
        self.ui.wait(first)
        evidence += self.ui.receipt("forgot-only-second-computer")
        self.ui.tap(label("Done"))
        return evidence

    def photo(self):
        self.ui.tap(selector("terminal-write"))
        image = self.ui.output / "synthetic-photo.png"
        make_photo(image)
        self.ui.c.idb(self.ui.udid, "add-media", image, mutate=True)
        digest = hashlib.sha256(self.session["id"].encode()).hexdigest()
        directory = self.backend.path / "attachments" / digest
        before = set(directory.glob("photo-*")) if directory.exists() else set()
        self.ui.tap(selector("terminal-add-photo"))
        self.ui.tap(label("Allow photos for this computer"))
        self.ui.select_synthetic_photo()
        deadline = time.monotonic() + self.ui.timeout
        while True:
            added = set(directory.glob("photo-*")) - before if directory.exists() else set()
            if len(added) > 1:
                raise SmokeError("One native photo selection produced multiple uploads")
            if len(added) == 1:
                break
            if time.monotonic() >= deadline:
                raise SmokeError("Native picker selection produced no owned backend upload")
            time.sleep(0.2)
        path = added.pop()
        if path.resolve() != path or path.stat().st_mode & 0o777 != 0o600 or not path.read_bytes().startswith(b"\xff\xd8"):
            raise SmokeError("Uploaded fixture photo is redirected, non-private or not JPEG")
        content = self.ui.output / "uploaded-photo-decoded.png"
        width, height = verify_uploaded_photo(self.ui.c, path, content)
        self.ui.public_hashes.add(digest)  # This known session-directory hash is not a bearer credential.
        draft = selector("terminal-draft", role="AXTextArea")
        quoted = "'" + str(path) + "'"
        self.ui.wait_value(draft, quoted)
        self.ui.wait(label("Photo uploaded. Insert adds its path to the terminal; Return sends it.", role=None))
        evidence = self.ui.receipt("photo-uploaded")
        proof = self.ui.output / "photo-backend.json"
        write_json(proof, {"session_id": self.session["id"], "bytes": path.stat().st_size, "sha256": sha256(path),
                           "mode": "0600", "format": "JPEG", "native_draft_matched_uploaded_path": True,
                           "decoded_reference": {"width": width, "height": height, "dense_tile_match": True,
                                                 "file": content.name, "sha256": sha256(content)}})
        evidence += [{"file": proof.name, "sha256": sha256(proof)}, {"file": content.name, "sha256": sha256(content)}]
        self.ui.replace_once(draft, "demo photo " + quoted, quoted)
        self.ui.tap(selector("terminal-insert"))
        self.ui.wait(selector("terminal-return"))
        self.ui.tap(selector("terminal-return"))
        evidence += self.ui.visible_text("Photo received.", "photo-ack", region=selector("remote-terminal", role="AXTextArea"))
        return evidence  # Remembered-consent repetition/cancellation is a separate native picker check.

    def disconnect(self):
        if self.ui.exists(label("Close workspace")):
            self.ui.tap(label("Close workspace"))
        self.ui.overflow()
        self.ui.tap(label("Disconnect"))
        self.ui.wait(label("Reconnect"))

    def fault(self, name):
        if name not in ("capacity", "busy", "drop-leases", "preview-expired"):
            raise Blocked("Fault " + name + " still needs a qualified native error/recovery assertion")
        try:
            if name in ("capacity", "busy"):
                self.disconnect()
                self.backend.fault(name)
                self.ui.tap(label("Reconnect"))
                self.ui.tap(label("Allow once"))
                message = "terminal capacity reached" if name == "capacity" else "terminal is already attached; disconnect its current direct client before retrying"
                evidence = self.ui.observe_error(message, name + "-native-error")
                if name in self.backend.status().get("state", {}).get("faults", []):
                    raise SmokeError("One-shot attach fault was not consumed")
                self.backend.fault("clear")
                if name == "busy":
                    self.ui.tap(label("Reconnect"))
                    self.ui.tap(label("Allow once"))
                self.ui.wait(selector("terminal-return", role="AXButton"))
                evidence += self.command("smoke recovered " + visual_nonce(), name + "-recovered")
                return evidence
            if name == "drop-leases":
                self.backend.fault(name)
                return self.reconnect()  # Resume watchdog must obtain a new HTTPS stream child.
            self.live_preview()
            self.backend.fault(name)
            # Produce a nonce after grant expiry through the real terminal. The old
            # cached document cannot contain it. Reopen the retained browser pane;
            # do not navigate a new address or mint a grant through the harness.
            self.ui.tap(label("Close workspace"))
            fresh = "smoke renewed " + visual_nonce()
            evidence = self.command(fresh, "post-expiry-command")
            self.ui.tap(selector("session-workspace"))
            self.ui.wait(label("Page preview", role="AXGroup"))
            self.ui.tap(label("Reload"))
            evidence += self.ui.visible_text(fresh, "preview-renewed-fresh-heading")
            return evidence
        finally:
            self.backend.fault("clear")

    def run(self, scenarios):
        for name in scenarios:
            entry = {"name": name, "outcome": "running", "assertions": [], "startedAt": now()}
            self.manifest["scenarios"].append(entry)
            self.steps = entry["assertions"]
            try:
                if name == "happy":
                    self.happy()
                elif self.session is None:
                    raise Blocked("Scenario depends on successful happy pairing/terminal flow")
                elif name == "lifecycle":
                    self.lifecycle()
                elif name == "two-computers":
                    self.assertion("pair/switch/relaunch/forget two computers", self.computers)
                elif name == "photo":
                    self.assertion("synthetic native photo selection/upload and draft", self.photo)
                else:
                    self.assertion("native deterministic error and recovery", lambda: self.fault(name))
                entry["outcome"] = "pass" if self.steps and all(x["outcome"] == "pass" for x in self.steps) else "blocked"
            except Blocked as exc:
                entry.update(outcome="blocked", reason=str(exc))
            except SmokeError as exc:
                entry.update(outcome="fail", reason=str(exc))
                # No following UI mutation after uncertain input or failed state assertion.
                return
            finally:
                entry["finishedAt"] = now()
                write_json(self.ui.output / "manifest.json", self.manifest)
            if entry["outcome"] != "pass":
                # The last known presentation may be a picker/sheet. Never guess how to recover it.
                return


def png_rgb(path):
    """Decode only bounded 8-bit RGB/RGBA screenshots; unknown formats fail closed."""
    data = Path(path).read_bytes()
    if not data.startswith(b"\x89PNG\r\n\x1a\n") or len(data) > 64 << 20:
        raise Blocked("Unsupported picker screenshot encoding")
    offset, payload, header = 8, bytearray(), None
    while offset + 12 <= len(data):
        size = struct.unpack(">I", data[offset:offset+4])[0]
        kind, chunk = data[offset+4:offset+8], data[offset+8:offset+8+size]
        if kind == b"IHDR":
            header = struct.unpack(">IIBBBBB", chunk)
        elif kind == b"IDAT":
            payload.extend(chunk)
        offset += size + 12
    if header is None:
        raise Blocked("Picker PNG header missing")
    width, height, depth, color, compression, filtering, interlace = header
    if depth != 8 or color not in (2, 6) or compression or filtering or interlace or width * height > 20_000_000:
        raise Blocked("Unsupported picker PNG pixel format")
    bpp = 3 if color == 2 else 4
    stride = width * bpp
    inflater = zlib.decompressobj()
    raw = inflater.decompress(payload, (stride + 1) * height + 1)
    if len(raw) != (stride + 1) * height:
        raise Blocked("Picker screenshot pixel count mismatch")
    previous, rgb = bytearray(stride), bytearray(width * height * 3)
    for y in range(height):
        start = y * (stride + 1)
        algorithm = raw[start]
        row = bytearray(raw[start + 1:start + 1 + stride])
        for index in range(stride):
            left = row[index-bpp] if index >= bpp else 0
            above = previous[index]
            upper_left = previous[index-bpp] if index >= bpp else 0
            if algorithm == 1:
                predictor = left
            elif algorithm == 2:
                predictor = above
            elif algorithm == 3:
                predictor = (left + above) // 2
            elif algorithm == 4:
                predictor_base = left + above - upper_left
                distances = (abs(predictor_base-left), abs(predictor_base-above), abs(predictor_base-upper_left))
                predictor = (left, above, upper_left)[distances.index(min(distances))]
            elif algorithm == 0:
                predictor = 0
            else:
                raise Blocked("Unknown PNG filter")
            row[index] = (row[index] + predictor) & 255
        if bpp == 3:
            rgb[y*width*3:(y+1)*width*3] = row
        else:
            for x in range(width):
                rgb[(y*width+x)*3:(y*width+x+1)*3] = row[x*4:x*4+3]
        previous = row
    return width, height, rgb


def verify_uploaded_photo(commands, uploaded, decoded):
    # Decode the actual backend JPEG with Apple ImageIO, never the picker PNG.
    commands.call(["sips", "-s", "format", "png", uploaded, "--out", decoded])
    os.chmod(decoded, 0o600)
    width, height, pixels = png_rgb(decoded)
    if (width, height) != (96, 96) or not checker_reference(width, height, pixels, [0, 0, width, height]):
        raise SmokeError("Uploaded JPEG content does not match the generated checkerboard reference")
    return width, height


def checker_color(rgb, width, x, y):
    offset = (y * width + x) * 3
    r, g, b = rgb[offset:offset+3]
    if r < 80 and 120 < g < 220 and 80 < b < 180:
        return 1
    if r > 180 and g > 160 and b < 80:
        return 2
    return 0


def checker_reference(width, height, rgb, box):
    """Dense reference comparison across every tile, including both sides of boundaries.

    Skip only the narrow 10% edge band for JPEG/thumbnail interpolation. Each
    tile must match at least 95% of 81 interior samples, and overall at least
    98%. A few correctly colored center dots cannot satisfy this comparison.
    """
    left, top, w, h = box
    if len(rgb) != width * height * 3 or min(w, h) < 64:
        return False
    total = 0
    for row in range(6):
        for col in range(6):
            expected = 2 if (row + col) % 2 == 0 else 1
            count = 0
            for iy in range(9):
                for ix in range(9):
                    x = round(left + (col + .1 + ix * .1) * w / 6)
                    y = round(top + (row + .1 + iy * .1) * h / 6)
                    if not (0 <= x < width and 0 <= y < height):
                        return False
                    count += checker_color(rgb, width, x, y) == expected
            if count < 77:
                return False
            total += count
    return total >= 2858


def checker_boxes(width, height, rgb):
    """Find the generated six-by-six green/yellow checkerboard, never a generic photo."""
    def color(x, y):
        return checker_color(rgb, width, x, y)
    step = 4
    columns, rows = width // step, height // step
    mask = bytearray(columns * rows)
    for y in range(rows):
        for x in range(columns):
            if color(x * step, y * step):
                for dy in (-1, 0, 1):
                    for dx in (-1, 0, 1):
                        if 0 <= x+dx < columns and 0 <= y+dy < rows:
                            mask[(y+dy)*columns+x+dx] = 1
    boxes = []
    for origin in range(len(mask)):
        if not mask[origin]:
            continue
        queue = deque([origin]); mask[origin] = 0
        left = right = origin % columns; top = bottom = origin // columns
        while queue:
            cell = queue.popleft(); x, y = cell % columns, cell // columns
            left, right, top, bottom = min(left,x), max(right,x), min(top,y), max(bottom,y)
            for dx, dy in ((1,0),(-1,0),(0,1),(0,-1)):
                nx, ny = x+dx, y+dy
                if 0 <= nx < columns and 0 <= ny < rows and mask[ny*columns+nx]:
                    mask[ny*columns+nx] = 0; queue.append(ny*columns+nx)
        w, h = (right-left+1)*step, (bottom-top+1)*step
        if min(w,h) < 64 or not 0.85 < w/h < 1.15:
            continue
        # Dilation joins resampled tiles but adds a border. Refine to actual
        # classified pixels before dense reference comparison.
        colored = [(x, y) for y in range(top*step, min(height, (bottom+1)*step))
                   for x in range(left*step, min(width, (right+1)*step)) if color(x, y)]
        if not colored:
            continue
        px, py = min(x for x,y in colored), min(y for x,y in colored)
        pw, ph = max(x for x,y in colored)-px+1, max(y for x,y in colored)-py+1
        box = [px, py, pw, ph]
        if checker_reference(width, height, rgb, box):
            boxes.append(box)
    return boxes


def make_photo(path):
    """Generate synthetic metadata-free RGB PNG with no external dependencies."""
    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xffffffff)
    width = height = 96
    raw = b"".join(b"\0" + b"".join(bytes((30, 170, 130) if (x // 16 + y // 16) % 2 else (240, 210, 30))
                                      for x in range(width)) for y in range(height))
    data = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as file:
        file.write(data)


def overall(entries):
    outcomes = [entry["outcome"] for entry in entries]
    if "fail" in outcomes:
        return "fail"
    return "pass" if outcomes and all(x == "pass" for x in outcomes) else "blocked"


def finalize_simulator(commands, record, owner_id):
    """No UI navigation: each mandatory teardown step gets an independent attempt."""
    steps = []
    try:
        verify_simulator(record, commands.inventory(), owner_id)
    except (Exception, KeyboardInterrupt):
        return [{"action": "verify-ownership", "outcome": "blocked", "reason": "Owned target could not be verified; no simulator mutation sent"},
                {"action": "terminate-app", "outcome": "not_attempted"},
                {"action": "shutdown-simulator", "outcome": "not_attempted"}]
    steps.append({"action": "verify-ownership", "outcome": "pass"})
    for action, operation in (
        ("terminate-app", lambda: commands.idb(record["udid"], "terminate", BUNDLE, mutate=True)),
        ("shutdown-simulator", lambda: commands.simctl("shutdown", record["udid"], mutate=True)),
    ):
        try:
            operation()
            steps.append({"action": action, "outcome": "pass"})
        except (Exception, KeyboardInterrupt):
            steps.append({"action": action, "outcome": "blocked", "reason": "Teardown failed or was interrupted; no action retried; raw diagnostic suppressed"})
    return steps


def set_duo_pose(commands, udid, rotation, angle):
    """Verify native controls, not rendered geometry or compliance with HIG."""
    commands.idb(udid, "rotation", rotation, mutate=True)
    commands.idb(udid, "hinge", str(angle), mutate=True)
    reported_rotation = commands.idb(udid, "rotation").strip()
    reported_hinge = commands.idb(udid, "hinge").strip()
    if reported_rotation != rotation:
        raise Blocked("IDB rotation read does not exactly confirm requested pose")
    try:
        degrees = Decimal(reported_hinge)
    except InvalidOperation as exc:
        raise Blocked("IDB hinge read is not an exact numeric angle") from exc
    if not degrees.is_finite() or degrees != angle:
        raise Blocked("IDB hinge read does not exactly confirm requested pose")
    return {"requested_rotation": rotation, "requested_hinge_degrees": angle,
            "observed_rotation": reported_rotation, "observed_hinge_degrees": str(degrees),
            "visual_layout": "not asserted"}


def run_one(commands, root, marker, args, app, backend, secondary, profile, appearance):
    output = root / ("run-" + uuid.uuid4().hex)
    output.mkdir(mode=0o700)
    manifest = {"schema": OWNER, "startedAt": now(), "outcome": "running", "execution": "live-idb",
                "suite": "synthetic-backend-app-e2e", "app": app, "appearance": appearance,
                "runner_sha256": sha256(Path(__file__)), "backend": {**backend.identity, "binary_sha256": backend.binary_sha256},
                "scenarios": [], "manual_gaps": ["physical TestFlight binary", "real Herdr integration", "dictation/camera/roaming", "transport loss/lease expiry", "video/metadata/accessibility", "visual HIG/layout across Duo poses"]}
    record, ui = None, None
    event_start = len(commands.events)
    try:
        backend.status()
        manifest["toolchain"] = commands.capabilities()
        if secondary:
            secondary.status()
            if secondary.identity["fingerprint"] == backend.identity["fingerprint"] or secondary.identity["address"] == backend.identity["address"]:
                raise Blocked("Two-computer fixtures must have independent TLS/address identities")
            manifest["backend_b"] = secondary.identity
        record = create_simulator(commands, root, marker, profile, args.runtime)
        manifest["device"] = record
        udid = record["udid"]
        commands.simctl("boot", udid, mutate=True)
        commands.simctl("bootstatus", udid, "-b", timeout=180)
        commands.idb(udid, "install", app["path"], timeout=180, mutate=True)
        description = json.loads(commands.idb(udid, "describe", "--json"))
        if description.get("udid", "").upper() != udid or description.get("target_type") != "simulator":
            raise Blocked("IDB target description does not match the newly owned simulator")
        manifest["device"]["os_version"] = description.get("os_version")
        manifest["device"]["screen_dimensions"] = description.get("screen_dimensions")
        commands.simctl("ui", udid, "appearance", appearance, mutate=True)
        if commands.simctl("ui", udid, "appearance").strip() != appearance:
            raise SmokeError("Requested appearance was not applied")
        if profile == "iphone-duo":
            manifest["initial_pose"] = {"name": "cover", **set_duo_pose(commands, udid, "PORTRAIT", 0)}
        else:
            commands.idb(udid, "rotation", "PORTRAIT", mutate=True)
        commands.idb(udid, "launch", "--foreground-if-running", BUNDLE, mutate=True)
        ui = UI(commands, udid, output, args.timeout)
        ui.wait(selector("pair-computer"))  # Fresh install: settle native launch before opening an invitation.
        journey = Journey(ui, backend, secondary, manifest, args.background_seconds)
        journey.run(args.scenarios)
        completed = {x["name"] for x in manifest["scenarios"]}
        for scenario in args.scenarios:
            if scenario not in completed:
                manifest["scenarios"].append({"name": scenario, "outcome": "blocked", "reason": "Earlier scenario did not finish safely", "assertions": []})
        if profile == "iphone-duo" and overall(manifest["scenarios"]) == "pass":
            # Native orientation enums differ from rendered display geometry.
            # These controls and fresh input do not establish visual/HIG correctness.
            for pose, rotation, angle in DUO_POSES:
                entry = {"name": pose, "requested_rotation": rotation, "requested_hinge_degrees": angle,
                         "visual_layout": "not asserted", "assertions": [], "outcome": "running"}
                manifest["scenarios"].append(entry)
                journey.steps = entry["assertions"]
                try:
                    entry["pose"] = set_duo_pose(commands, udid, rotation, angle)
                    def pose_command():
                        if ui.exists(label("Close workspace")):
                            ui.tap(label("Close workspace"))
                        return journey.command("smoke pose " + visual_nonce(), pose)
                    journey.assertion("terminal accepts fresh input after verified native rotation and hinge change", pose_command)
                    entry["outcome"] = "pass"
                except Blocked as exc:
                    entry.update(outcome="blocked", reason=str(exc))
                    break
                except SmokeError as exc:
                    entry.update(outcome="fail", reason=str(exc))
                    break
        manifest["outcome"] = overall(manifest["scenarios"])
    except Blocked as exc:
        manifest.update(outcome="blocked", reason=str(exc))
    except SmokeError as exc:
        manifest.update(outcome="fail", reason=str(exc))
    except KeyboardInterrupt:
        manifest.update(outcome="blocked", reason="Interrupted; no uncertain action retried")
    except Exception:
        manifest.update(outcome="blocked", reason="Unexpected harness error; raw diagnostic suppressed")
    finally:
        # Own simulator only. Never stop/reset a backend, clear global network state or delete pairings.
        # Native Disconnect is a scenario assertion, not a prerequisite for
        # finalization. Hidden tabs are released by the owned backend below.
        manifest["finalization_disconnect"] = "not_attempted; no native UI disconnect coverage claimed"
        if record is not None:
            steps = finalize_simulator(commands, record, marker["id"])
            manifest["cleanup_steps"] = steps
            if all(step["outcome"] == "pass" for step in steps):
                manifest["cleanup"] = "owned app terminated and simulator shut down; retained for inspection"
            else:
                manifest["cleanup"] = "blocked; inspect cleanup_steps and owned registry"
                if manifest["outcome"] == "pass":
                    manifest["outcome"] = "blocked"
        # This harness root is exclusive and may own hidden terminal tabs too.
        # Drop only these explicitly verified demo processes' tracked terminal streams,
        # so later fresh simulators cannot inherit an occupied controller/capacity.
        lease_cleanup = []
        for fixture in [backend] + ([secondary] if secondary else []):
            try:
                fixture.fault("drop-leases")
                lease_cleanup.append({"address": fixture.identity["address"], "outcome": "pass"})
            except SmokeError:
                lease_cleanup.append({"address": fixture.identity["address"], "outcome": "blocked"})
                if manifest["outcome"] == "pass":
                    manifest["outcome"] = "blocked"
        manifest["owned_lease_cleanup"] = lease_cleanup
        manifest["finishedAt"] = now()
        manifest["tool_events"] = commands.events[event_start:]
        manifest["artifacts"] = ui.evidence if ui else []
        # Build hashes and certificate fingerprints are intentional provenance, not credentials.
        write_json(output / "manifest.json", manifest)
        print(json.dumps({"manifest": str(output / "manifest.json"), "outcome": manifest["outcome"]}), flush=True)
    return manifest


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="command", required=True)
    for name in ("init", "run", "cleanup"):
        action = sub.add_parser(name)
        action.add_argument("--state-dir", required=True, type=Path)
        if name == "run":
            action.add_argument("--trill", required=True, type=Path)
            action.add_argument("--backend-state", required=True, type=Path)
            action.add_argument("--second-backend-state", type=Path)
            action.add_argument("--app", required=True, type=Path)
            action.add_argument("--app-source-revision", required=True)
            action.add_argument("--profile", choices=[*PROFILES, "all"], default="iphone-17-pro")
            action.add_argument("--appearance", choices=["light", "dark", "both"], default="both")
            action.add_argument("--runtime", help="Exact runtime override; for one explicit device profile")
            action.add_argument("--scenarios", nargs="+", choices=SCENARIOS, default=["happy"])
            action.add_argument("--timeout", type=float, default=25)
            action.add_argument("--background-seconds", type=float, default=65)
    return p


def execute(args):
    if args.command == "init":
        path = initialize(args.state_dir)
        print(json.dumps({"state_dir": str(path), "backend_a": str(path / "backend-a"), "backend_b": str(path / "backend-b")}))
        return 0
    load_protected_simulator_ids(PROTECTED_SIMULATORS_PATH, Blocked)
    root, marker = owned(args.state_dir)
    with locked(root / "runner.lock"):
        commands = Commands()
        if args.command == "cleanup":
            records = read_json(root / "simulators.json")
            inventory = commands.inventory()
            # Validate every record before any deletion. Never erase/adopt an existing personal target.
            for record in records:
                verify_simulator(record, inventory, marker["id"])
            for record in records:
                commands.simctl("delete", record["udid"], mutate=True)
            write_json(root / "simulators.json", [])
            return 0
        if not 1 <= args.timeout <= 120 or not 0 <= args.background_seconds <= 300:
            raise Blocked("Timeout/background duration out of bounds")
        if args.runtime and args.profile == "all":
            raise Blocked("A runtime override requires one explicit profile")
        if len(set(args.scenarios)) != len(args.scenarios):
            raise Blocked("Duplicate scenarios would repeat uncertain mutations")
        if args.scenarios[0] != "happy":
            raise Blocked("Run happy first to establish ordinary pairing and terminal consent")
        app = app_identity(args.app, args.app_source_revision)
        backend = Backend(commands, args.trill, args.backend_state, root)
        secondary = Backend(commands, args.trill, args.second_backend_state, root) if args.second_backend_state else None
        profiles = list(PROFILES) if args.profile == "all" else [args.profile]
        appearances = ["light", "dark"] if args.appearance == "both" else [args.appearance]
        manifests = []
        for profile in profiles:
            for appearance in appearances:
                manifests.append(run_one(commands, root, marker, args, app, backend, secondary, profile, appearance))
        outcome = overall(manifests)
        return {"pass": 0, "fail": 1, "blocked": 2}[outcome]


def main(argv=None):
    args = parser().parse_args(argv)
    try:
        return execute(args)
    except SmokeError as exc:
        outcome = "blocked" if isinstance(exc, Blocked) else "fail"
        # A preflight problem still gets a report when the root has valid ownership.
        # Refuse to write anywhere when ownership itself failed.
        try:
            root, marker = owned(args.state_dir)
            if args.command == "run":
                path = root / ("preflight-" + uuid.uuid4().hex + ".json")
                write_json(path, {"schema": OWNER, "execution": "live-preflight", "outcome": outcome,
                                  "reason": str(exc), "owner": marker["id"], "finishedAt": now(),
                                  "scenarios": [{"name": name, "outcome": "blocked", "assertions": []}
                                                for name in args.scenarios]})
                print(json.dumps({"manifest": str(path), "outcome": outcome}), flush=True)
        except (SmokeError, OSError):
            pass
        print(json.dumps({"outcome": outcome, "reason": str(exc)}), file=sys.stderr)
        return 2 if outcome == "blocked" else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Blocked as error:
        print(json.dumps({"outcome": "blocked", "reason": str(error)}), file=sys.stderr)
        sys.exit(2)
    except SmokeError as error:
        print(json.dumps({"outcome": "fail", "reason": str(error)}), file=sys.stderr)
        sys.exit(1)
