#!/usr/bin/env python3
"""Real HTTPS/Mosh demo integration check. Never targets an existing companion.

Requires a built trill binary and mosh-client. Creates its own private directory,
ports, registrations and processes. This is transport/API evidence, not iOS UI or
real-provider integration evidence.
"""
import argparse
import base64
import fcntl
import hashlib
import http.client
import hmac
import json
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import socket
import ssl
import struct
import subprocess
import tempfile
import termios
import time
import urllib.error
import urllib.request
import zlib


def free_port(sock_type=socket.SOCK_STREAM):
    with socket.socket(socket.AF_INET, sock_type) as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def wait_for(fn, timeout=12):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except (OSError, ValueError, subprocess.CalledProcessError, AssertionError):
            pass
        time.sleep(.15)
    raise AssertionError('timed out waiting for expected fixture state')


class PinnedHTTPSConnection(http.client.HTTPSConnection):
    def __init__(self, host, *, expected_pin, **kwargs):
        self.expected_pin = expected_pin
        super().__init__(host, **kwargs)

    def connect(self):
        super().connect()
        actual = hashlib.sha256(self.sock.getpeercert(binary_form=True)).hexdigest()
        if not hmac.compare_digest(actual, self.expected_pin):
            self.close()
            raise ssl.SSLError('demo TLS certificate fingerprint mismatch')


class PinnedHTTPSHandler(urllib.request.HTTPSHandler):
    def __init__(self, pin):
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE
        # Trust is the exact private fixture certificate, checked on the SAME
        # connection before any HTTP request/credential is sent. This supports
        # local self-signed leaves without installing a CA in the OS keychain.
        super().__init__(context=context)
        self.pin = pin

    def https_open(self, req):
        def connection(host, **kwargs):
            return PinnedHTTPSConnection(host, expected_pin=self.pin, **kwargs)
        return self.do_open(connection, req, context=self._context)


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Smoke:
    def __init__(self, binary, output):
        self.binary = str(binary.resolve())
        self.output = output
        self.root = Path(tempfile.mkdtemp(prefix='td-smoke-')).resolve()
        self.state = self.root / 's'
        self.api_port = free_port()
        self.preview_port = free_port()
        self.udp_port = free_port(socket.SOCK_DGRAM)
        self.address = f'https://127.0.0.1:{self.api_port}'
        self.token = None
        self.clients = []
        self.results = []
        self.process = None
        self.log = None

    def cli(self, *args):
        return subprocess.check_output([self.binary, 'demo', *args, '--state-dir', str(self.state)], stderr=subprocess.PIPE, timeout=12)

    def request(self, method, path, value=None, expected=200, token=True, content_type='application/json', headers=None):
        data = json.dumps(value).encode() if value is not None and not isinstance(value, bytes) else value
        h = {'Content-Type': content_type}
        if token and self.token:
            h['Authorization'] = 'Bearer ' + self.token
        h.update(headers or {})
        req = urllib.request.Request(self.address + path, data=data, method=method, headers=h)
        try:
            response = self.opener.open(req, timeout=12)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            body = response.read(2 << 20)
            assert response.status == expected, f'{method} {path.split("?")[0]} returned {response.status}; expected {expected}'
            if 'application/json' in response.headers.get('Content-Type', ''):
                return json.loads(body)
            return body

    def record(self, name, fn):
        start = time.monotonic()
        fn()
        self.results.append({'check': name, 'outcome': 'pass', 'seconds': round(time.monotonic() - start, 3)})

    def start(self):
        self.log = (self.root / 'server.log').open('wb')
        os.chmod(self.root / 'server.log', 0o600)
        self.process = subprocess.Popen([self.binary, 'demo', 'serve', '--state-dir', str(self.state), '--listen', f'127.0.0.1:{self.api_port}', '--address', self.address, '--preview-listen', f'127.0.0.1:{self.preview_port}', '--udp-ports', f'{self.udp_port}:{self.udp_port}', '--max-leases', '2'], stdout=self.log, stderr=self.log, start_new_session=True)
        wait_for(lambda: json.loads(self.cli('status', '--json')).get('running'))
        der = ssl.PEM_cert_to_DER_cert((self.state / 'tls.pem').read_text().split('-----END CERTIFICATE-----')[0] + '-----END CERTIFICATE-----')
        pin = hashlib.sha256(der).hexdigest()
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), PinnedHTTPSHandler(pin), NoRedirects())

    def pair(self):
        self.request('GET', '/v1/sessions', token=False, expected=401)
        invitation = json.loads(self.cli('pair', '--json'))
        leaf = ssl.PEM_cert_to_DER_cert((self.state / 'tls.pem').read_text().split('-----END CERTIFICATE-----')[0] + '-----END CERTIFICATE-----')
        assert hashlib.sha256(leaf).hexdigest() == invitation['fingerprint']
        auth = self.request('POST', '/v1/pair', {'code': invitation['code'], 'name': 'API smoke'}, token=False)
        self.token = auth['token']
        self.device_id = auth['deviceId']
        self.request('POST', '/v1/pair', {'code': invitation['code'], 'name': 'Replay'}, expected=403, token=False)
        self.cli('fault', 'invitation-expired')
        expired = json.loads(self.cli('pair', '--json'))
        self.request('POST', '/v1/pair', {'code': expired['code'], 'name': 'Expired'}, expected=403, token=False)
        projects = self.request('GET', '/v1/projects')['projects']
        self.project = next(p for p in projects if p['capabilities']['createAgent'])
        sessions = self.request('GET', '/v1/sessions')['sessions']
        self.session = next(s for s in sessions if s['projectId'] == self.project['id'] and s['attachable'] and not s['archived'])['id']

    def lifecycle(self):
        base = {s['id'] for s in self.request('GET', '/v1/sessions')['sessions']}
        self.request('POST', '/v1/sessions', {'projectId': self.project['id'], 'kind': 'agent', 'name': 'Smoke worktree', 'goal': 'Demonstrate a sample change', 'useWorktree': True, 'branch': 'demo/smoke'})
        made = [s for s in self.request('GET', '/v1/sessions')['sessions'] if s['id'] not in base]
        assert len(made) == 1
        sid = made[0]['id']
        self.request('PATCH', f'/v1/sessions/{sid}', {'name': 'Renamed smoke'})
        self.request('POST', f'/v1/sessions/{sid}/archive')
        assert next(s for s in self.request('GET', '/v1/sessions')['sessions'] if s['id'] == sid)['archived']
        self.request('POST', f'/v1/sessions/{sid}/resume')
        self.request('POST', f'/v1/sessions/{sid}/stop')
        self.request('POST', f'/v1/sessions/{sid}/resume')
        final = next(s for s in self.request('GET', '/v1/sessions')['sessions'] if s['id'] == sid)
        assert final['name'] == 'Renamed smoke' and final['attachable'] and not final['archived']
        self.request('POST', '/v1/sessions', {'projectId': self.project['id'], 'kind': 'terminal', 'useWorktree': False})

    def connect(self):
        lease = self.request('POST', f'/v1/sessions/{self.session}/mosh', {'cols': 90, 'rows': 28})
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 28, 90, 0, 0))
        env = os.environ.copy()
        env.update(MOSH_KEY=lease['key'], TERM='xterm-256color', LANG='en_US.UTF-8')
        process = subprocess.Popen(['mosh-client', lease['host'], str(lease['port'])], stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
        os.close(slave)
        self.clients.append((process, master))
        output = bytearray()
        def ready():
            readable, _, _ = select.select([master], [], [], .1)
            if readable:
                output.extend(os.read(master, 65536))
                del output[:-262144]
            return b'Demo' in output or b'demo' in output
        wait_for(ready)
        return process, master, lease

    def file(self):
        data = self.request('GET', f'/v1/sessions/{self.session}/file?path=web/index.html')
        if isinstance(data, dict):
            # File APIs return metadata and UTF-8 text rather than raw HTML.
            return str(data.get('text', data.get('content', data)))
        return data.decode()

    def terminal(self):
        self.process1, master, self.lease1 = self.connect()
        command = b'demo heading Smoke through real Mosh'
        os.write(master, command)
        time.sleep(.3)
        assert 'Smoke through real Mosh' not in self.file(), 'input executed before Return'
        os.write(master, b'\r')
        wait_for(lambda: 'Smoke through real Mosh' in self.file())
        fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack('HHHH', 31, 93, 0, 0))
        os.killpg(self.process1.pid, signal.SIGWINCH)
        def resized():
            status = json.loads(self.cli('status', '--json'))
            return any(s['id'] == self.session and s.get('columns') == 93 and s.get('rows') == 31
                       for s in status['state']['sessions'])
        wait_for(resized)
        # Real reconnect replaces this paired device's previous Mosh lease.
        _, second, self.lease2 = self.connect()
        assert self.lease1['leaseId'] != self.lease2['leaseId'], 'reconnect reused lease identity'
        self.request('DELETE', '/v1/leases/' + self.lease1['leaseId'], expected=404)
        assert 'Smoke through real Mosh' in self.file()
        os.write(second, b'demo heading Reconnected through real Mosh\r')
        wait_for(lambda: 'Reconnected through real Mosh' in self.file())
        self.request('DELETE', '/v1/leases/' + self.lease2['leaseId'])

    def media(self):
        files = self.request('GET', f'/v1/sessions/{self.session}/files?path=media')
        assert any(e['name'] == 'sample.png' for e in files['entries'])
        self.request('GET', f'/v1/sessions/{self.session}/file?path=..%2Fdemo-owner.json', expected=400)
        # Known synthetic 1x1 PNG, no personal files or camera metadata.
        def chunk(kind, data):
            return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data))
        png = b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 1, 1, 8, 6, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(b'\x00\x88\x44\xff\xff')) + chunk(b'IEND', b'')
        photo = self.request('POST', f'/v1/sessions/{self.session}/attachments', png, expected=201, content_type='image/png')
        assert Path(photo['path']).is_relative_to(self.state)
        listed = self.request('GET', f'/v1/sessions/{self.session}/attachments')
        assert any(e['name'] == photo['fileName'] for e in listed['entries'])

    def previews(self):
        grant = self.request('POST', '/v1/previews', {'sessionId': self.session, 'port': self.preview_port}, expected=201)
        # The production proxy routes the unchanged path using its HttpOnly cookie.
        path = '/'
        page = self.request('GET', path, headers={'Cookie': 'trill_preview=' + grant['token']}, token=False)
        assert b'Reconnected through real Mosh' in page
        self.request('POST', '/v1/previews', {'sessionId': self.session, 'port': self.api_port}, expected=400)
        self.request('GET', path, token=False, expected=401)
        self.request('DELETE', '/v1/previews/' + grant['id'])

    def failures(self):
        self.cli('fault', 'capacity')
        self.request('POST', f'/v1/sessions/{self.session}/mosh', {'cols': 90, 'rows': 28}, expected=429)
        self.cli('fault', 'clear')
        self.cli('fault', 'api-unavailable')
        self.request('GET', '/v1/sessions', expected=503)
        self.cli('fault', 'clear')
        self.request('GET', '/v1/sessions')
        self.cli('revoke', self.device_id)
        self.request('GET', '/v1/sessions', expected=401)

    def cleanup(self):
        for process, master in self.clients:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
            os.close(master)
        if self.process and self.process.poll() is None:
            try:
                self.cli('stop')
                self.process.wait(timeout=12)
            except (OSError, subprocess.SubprocessError):
                os.killpg(self.process.pid, signal.SIGTERM)
                self.process.wait(timeout=12)
        if self.log:
            self.log.close()

    def run(self):
        outcome, failure = 'pass', None
        try:
            self.start()
            for name, fn in [('HTTPS pairing and invitation expiry', self.pair), ('session lifecycle', self.lifecycle), ('real Mosh input and reconnect', self.terminal), ('files and private uploads', self.media), ('authenticated browser proxy', self.previews), ('failure recovery and revocation', self.failures)]:
                self.record(name, fn)
        except Exception as exc:
            outcome, failure = 'fail', f'{type(exc).__name__}: {exc}'
        finally:
            try:
                self.cleanup()
            except Exception as exc:
                outcome, failure = "fail", "Owned process cleanup failed: " + type(exc).__name__
        manifest = {'kind': 'trill-demo-api-smoke-v1', 'outcome': outcome, 'binary_sha256': hashlib.sha256(Path(self.binary).read_bytes()).hexdigest(), 'checks': self.results, 'failure': failure, 'private_evidence': str(self.root), 'coverage': 'HTTPS/API and upstream mosh-client; no iOS UI or real provider claims'}
        self.output.parent.mkdir(parents=True, exist_ok=True)
        with os.fdopen(os.open(self.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as file:
            json.dump(manifest, file, indent=2)
        print(json.dumps({'outcome': outcome, 'passed': len(self.results), 'report': str(self.output)}))
        return outcome == 'pass'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--trill', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True, help='new private JSON report path')
    args = parser.parse_args()
    if not args.trill.is_file() or not shutil.which('mosh-client'):
        parser.error('built trill and mosh-client are required')
    if args.output.exists():
        parser.error('output must be a new path')
    raise SystemExit(0 if Smoke(args.trill, args.output).run() else 1)


if __name__ == '__main__':
    main()
