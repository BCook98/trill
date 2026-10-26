#!/usr/bin/env python3
"""Real, private tmux/mosh/HTTPS fixtures for IDB demos. No personal sessions."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import socket
import ssl
import subprocess
import sys
import time
import tempfile

ROOT = Path(__file__).resolve().parent.parent
KIND = "trill-demo-lab-v1"
SESSION = "field-notes"


def command(args, **kwargs):
    return subprocess.run([str(a) for a in args], check=True, capture_output=True, text=True, **kwargs)


def private_json(path, value):
    fd, name = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp", dir=path.parent)
    temporary = Path(name)
    try:
        with os.fdopen(fd, "w") as out:
            json.dump(value, out, indent=2)
            out.write("\n")
            out.flush()
            os.fsync(out.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def owned(path):
    if path.is_symlink() or not path.is_dir():
        raise RuntimeError("Demo state must be a real owned directory")
    st = path.stat()
    if st.st_uid != os.getuid() or st.st_mode & 0o077:
        raise RuntimeError("Demo state must be user-owned with mode 0700")
    marker = path / "owner.json"
    if marker.is_symlink():
        raise RuntimeError("Refusing a linked ownership marker")
    data = json.loads(marker.read_text())
    if data.get("kind") != KIND or data.get("state_dir") != str(path):
        raise RuntimeError("Refusing an unowned demo state directory")
    return data


def process_identity(pid):
    result = subprocess.run(["ps", "-p", str(pid), "-o", "lstart=", "-o", "command="], capture_output=True, text=True)
    return result.stdout.strip() if result.returncode == 0 else None


def running(item):
    return bool(item and process_identity(item["pid"]) == item["identity"])


def stop_owned_process(item):
    if running(item):
        os.kill(item["pid"], signal.SIGTERM)
        for _ in range(50):
            if not running(item):
                return
            time.sleep(0.1)
        raise RuntimeError("Demo process did not exit; left it intact rather than escalating")


def available(port):
    with socket.socket() as sock:
        try:
            sock.bind(("127.0.0.1", port))
        except OSError:
            return False
    return True


def spawn(args, log):
    fd = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    with os.fdopen(fd, "ab") as output:
        process = subprocess.Popen([str(a) for a in args], stdin=subprocess.DEVNULL,
                                   stdout=output, stderr=output, start_new_session=True)
    time.sleep(0.1)
    identity = process_identity(process.pid)
    if process.poll() is not None or not identity:
        raise RuntimeError("Demo helper failed to start; see private lab logs")
    return {"pid": process.pid, "identity": identity}


def public_state(data):
    return {key: data[key] for key in ("state_dir", "address", "name", "session_name", "preview_url", "files_root")}


def healthy(data):
    # Match the exact leaf before sending a request; never install system trust.
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    from urllib.parse import urlsplit
    api = urlsplit(data["address"])
    preview = urlsplit(data["preview_url"])
    if api.hostname != "localhost" or preview.hostname != "localhost":
        raise RuntimeError("Demo health checks require owned loopback endpoints")
    with socket.create_connection(("127.0.0.1", api.port), timeout=2) as raw:
        with context.wrap_socket(raw, server_hostname="localhost") as secure:
            if hashlib.sha256(secure.getpeercert(binary_form=True)).hexdigest() != data["fingerprint"]:
                raise RuntimeError("Demo TLS fingerprint mismatch")
            secure.sendall(b"GET /v1/health HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
            status = secure.recv(4096).split(b"\r\n", 1)[0]
            if not status.startswith((b"HTTP/1.1 401", b"HTTP/1.1 200")):
                raise RuntimeError("Demo companion health check failed")
    with socket.create_connection(("127.0.0.1", preview.port), timeout=2) as page:
        page.sendall(b"GET /index.html HTTP/1.0\r\nHost: localhost\r\n\r\n")
        if b" 200 " not in page.recv(4096).split(b"\r\n", 1)[0]:
            raise RuntimeError("Demo preview health check failed")
    return True


def demo_panes(path):
    result = command(["tmux", "-S", path / "tmux.sock", "list-panes", "-a", "-F",
                      "#{session_name}\t#{pane_id}\t#{pane_current_command}"])
    return [line.split("\t") for line in result.stdout.splitlines() if line.split("\t")[0] == SESSION]


def start(path, api_port, preview_port):
    if api_port == preview_port or not all(1024 <= p <= 65535 and p != 7443 for p in (api_port, preview_port)):
        raise RuntimeError("Choose distinct unprivileged demo ports, never the production companion port 7443")
    if not path.exists():
        path.mkdir(parents=True, mode=0o700)
        private_json(path / "owner.json", {"kind": KIND, "state_dir": str(path)})
    data = owned(path)
    old = data.get("processes", {})
    if running(old.get("companion")) and running(old.get("preview")):
        healthy(data)
        if not demo_panes(path):
            raise RuntimeError("Demo session is missing; stop the owned helpers and start again to recreate it")
        return public_state(data)
    for item in old.values():
        stop_owned_process(item)
    if not available(api_port) or not available(preview_port):
        raise RuntimeError("A requested demo port is occupied; no existing process was stopped")
    socket_path = path / "tmux.sock"
    if len(os.fsencode(socket_path)) >= 100:
        raise RuntimeError("Demo state path is too long for a tmux socket")
    binaries = path / "bin"
    binaries.mkdir(exist_ok=True, mode=0o700)
    for target, package in [("trilld", "./cmd/trilld"), ("demo-invite", "./tools/demo-invite")]:
        command(["go", "build", "-o", binaries / target, package], cwd=ROOT / "sidecar")
    cert, key = path / "cert.pem", path / "key.pem"
    if not cert.exists() or not key.exists():
        config = path / "openssl.cnf"
        config.write_text("[req]\nprompt=no\ndistinguished_name=dn\nx509_extensions=ext\n[dn]\nCN=localhost\n[ext]\nsubjectAltName=DNS:localhost,IP:127.0.0.1\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n")
        command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30", "-config", config, "-keyout", key, "-out", cert])
        cert.chmod(0o600); key.chmod(0o600)
    der = ssl.PEM_cert_to_DER_cert(cert.read_text())
    files = path / "project"
    if not files.exists():
        shutil.copytree(ROOT / "demos/fixtures/field-notes", files)
    data.update(address="https://localhost:%d" % api_port, name="Demo Mac", session_name=SESSION,
                preview_url="http://localhost:%d/index.html" % preview_port, files_root=str(files),
                fingerprint=hashlib.sha256(der).hexdigest(), processes={})
    private_json(path / "owner.json", data)
    base = ["tmux", "-S", str(socket_path)]
    exists = subprocess.run(base + ["has-session", "-t", "=" + SESSION], capture_output=True).returncode == 0
    if not exists:
        data["initialized"] = False
        data["codex_started"] = False
        command(base + ["-f", "/dev/null", "new-session", "-d", "-s", SESSION, "-c", str(files), "-x", "90", "-y", "28", "/bin/zsh -f"])
        command(base + ["set-option", "-g", "status", "off"])
        command(base + ["set-option", "-g", "default-terminal", "xterm-256color"])
    if not data.get("initialized"):
        panes = command(base + ["list-panes", "-a", "-F", "#{session_name}\t#{pane_id}"]).stdout.splitlines()
        pane = next(line.split("\t")[1] for line in panes if line.split("\t")[0] == SESSION)
        # Real commands in the shell we just created, never an agent transcript.
        line = "PROMPT='demo % '; RPS1=''; clear; printf '\\033[1;32mField Notes\\033[0m\\nLocal development workspace\\n\\n'; ls; printf '\\nPreview: %s\\n\\n' " + shlex.quote(data["preview_url"])
        command(base + ["send-keys", "-t", pane, "-l", line])
        command(base + ["send-keys", "-t", pane, "Enter"])
        data["initialized"] = True
        private_json(path / "owner.json", data)
    try:
        data["processes"]["preview"] = spawn([sys.executable, "-m", "http.server", str(preview_port), "--bind", "127.0.0.1", "--directory", files], path / "preview.log")
        private_json(path / "owner.json", data)
        data["processes"]["companion"] = spawn([binaries / "trilld", "--listen", "127.0.0.1:%d" % api_port,
            "--cert", cert, "--key", key, "--pairing-store", path / "devices.json", "--allow-control",
            "--tmux-socket", socket_path, "--lab-files-root", files, "--mosh-host", "127.0.0.1", "--udp-ports", "60400:60420"], path / "companion.log")
        private_json(path / "owner.json", data)
        last_error = None
        for _ in range(50):
            try:
                if healthy(data):
                    return public_state(data)
            except (OSError, RuntimeError) as error:
                last_error = error
                time.sleep(0.1)
        raise RuntimeError("Demo companion did not become healthy: " + str(last_error))
    except Exception:
        for item in data["processes"].values():
            stop_owned_process(item)
        raise


def start_codex(path, data):
    """Explicit opt-in: a real coding agent in the owned shell, never fabricated output."""
    panes = demo_panes(path)
    if len(panes) == 1 and panes[0][2] == "codex":
        return {"started": False, "already_started": True, "session_name": data["session_name"]}
    executable = shutil.which("codex")
    if not executable:
        raise RuntimeError("Install/sign in to Codex before starting a real demo agent")
    base = ["tmux", "-S", str(path / "tmux.sock")]
    matching = panes
    if len(matching) != 1 or matching[0][2] not in ("zsh", "bash", "sh"):
        raise RuntimeError("Demo shell is not idle; no input was sent")
    project = Path(data["files_root"])
    if not (project / ".git").exists():
        command(["git", "init", "-q", project])
        command(["git", "-C", project, "add", "."])
        command(["git", "-C", project, "-c", "user.name=Trill Demo", "-c", "user.email=demo@localhost", "commit", "-qm", "Start Field Notes demo"])
    goal = ("Improve this Field Notes web project. Add automatic light and dark appearance using prefers-color-scheme, "
            "keeping the quiet editorial design and strong text contrast at narrow and wide widths. "
            "Add an accessible bookmark toggle to each story, persisted locally. Use no dependencies or external services. "
            "Check the JavaScript and inspect the actual diff. Work only inside this project; do not deploy, access personal "
            "sessions, or modify other repositories. This is a real demo task: never fabricate test results or agent output. "
            "Finish with a concise summary and this clickable preview URL: " + data["preview_url"])
    args = [executable, "--no-daemon", "--sandbox", "workspace-write", "--ask-for-approval", "on-request", "--model", "gpt-6.1-sol", "--cd", str(project), goal]
    command(base + ["send-keys", "-t", matching[0][1], "-l", shlex.join(args)])
    command(base + ["send-keys", "-t", matching[0][1], "Enter"])
    for _ in range(50):
        current = demo_panes(path)
        if len(current) == 1 and current[0][1] == matching[0][1] and current[0][2] == "codex":
            data["codex_started"] = True
            private_json(path / "owner.json", data)
            return {"started": True, "session_name": data["session_name"], "project": str(project)}
        time.sleep(0.1)
    raise RuntimeError("Codex startup was not confirmed; inspect the owned terminal before retrying")


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["start", "status", "invite", "stop", "codex"])
    parser.add_argument("--state-dir", type=Path, default=ROOT / ".local/demo-capture/lab")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--api-port", type=int, default=19443)
    parser.add_argument("--preview-port", type=int, default=18080)
    args = parser.parse_args()
    path = args.state_dir.expanduser().absolute()
    if path.is_symlink():
        raise RuntimeError("Demo state cannot be a symlink")
    if args.action == "start" and not path.exists():
        path.mkdir(parents=True, mode=0o700)
        private_json(path / "owner.json", {"kind": KIND, "state_dir": str(path)})
    owned(path)
    lock_fd = os.open(path / "operation.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(lock_fd, "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if args.action == "start":
            result = start(path, args.api_port, args.preview_port)
        else:
            data = owned(path)
            if args.action == "codex":
                result = start_codex(path, data)
            elif args.action == "invite":
                if args.output is None or not running(data.get("processes", {}).get("companion")):
                    raise RuntimeError("Running demo companion and --output are required")
                command([path / "bin/demo-invite", "--state-dir", path, "--output", args.output.absolute()])
                result = {"invitation_file": str(args.output.absolute())}
            elif args.action == "stop":
                for item in data.get("processes", {}).values():
                    stop_owned_process(item)
                data["processes"] = {}
                private_json(path / "owner.json", data)
                result = {"stopped": True, "tmux_preserved": True}
            else:
                result = public_state(data)
                result["running"] = all(running(item) for item in data.get("processes", {}).values()) and len(data.get("processes", {})) == 2
                try:
                    panes = demo_panes(path)
                    result["agent_running"] = len(panes) == 1 and panes[0][2] == "codex"
                except subprocess.CalledProcessError:
                    result["agent_running"] = False
    print(json.dumps(result))


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, subprocess.CalledProcessError) as error:
        # Avoid dumping subprocess arguments: invitation links never reach logs.
        print("demo lab: " + ("helper command failed; inspect private logs" if isinstance(error, subprocess.CalledProcessError) else str(error)), file=sys.stderr)
        sys.exit(1)
