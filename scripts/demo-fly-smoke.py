#!/usr/bin/env python3
"""Qualify a deployed Trill demo using trusted HTTPS and upstream mosh-client.

Read-only preflight is the default. --allow-demo-mutations authorizes disposable
sessions/registrations and GLOBAL fault checks: reserve a shared-sandbox maintenance
window first. Never run concurrently with reviewers or another smoke suite.

Example (after deployment, from this Mac):
  python3 scripts/demo-fly-smoke.py --app trill-demo --output /new/private/report.json
  python3 scripts/demo-fly-smoke.py --app trill-demo --allow-demo-mutations \
      --output /new/private/full-report.json

Requires authenticated fly CLI and, for the full suite, upstream mosh-client.
No backend is started/stopped, no deployment changed, no device/UI is operated.
Archived test sessions/worktree uploads remain subject to sandbox retention.
"""
import argparse
import contextlib
import datetime
import fcntl
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import pty
import re
import select
import shlex
import shutil
import signal
import ssl
import struct
import subprocess
import termios
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

_SPEC = importlib.util.spec_from_file_location('trill_local_api_smoke', Path(__file__).with_name('demo-api-smoke.py'))
BASE = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(BASE)
IDENTIFIER = re.compile(r'[A-Za-z0-9_-]{1,128}\Z')
CONNECTION_LINE = re.compile(r'Connecting to [0-9a-fA-F:.]+(?:\.\.\.)?(?: complete)?\r?\n')


class SmokeError(RuntimeError):
    """Only constant, credential-free codes may be put in reports."""


def strict_json(raw):
    """Allow only Fly's known connection preamble, then exactly one JSON value."""
    if len(raw) > 2 << 20:
        raise SmokeError('fly-output-too-large')
    try:
        text = raw.decode('utf-8') if isinstance(raw, bytes) else raw
        while CONNECTION_LINE.match(text):
            text = text[CONNECTION_LINE.match(text).end():]
        def pairs(items):
            obj = {}
            for key, value in items:
                if key in obj:
                    raise ValueError('duplicate key')
                obj[key] = value
            return obj
        return json.loads(text, object_pairs_hook=pairs,
                          parse_constant=lambda _: (_ for _ in ()).throw(ValueError('non-finite JSON')))
    except (UnicodeError, ValueError, TypeError):
        raise SmokeError('invalid-fly-json') from None


@contextlib.contextmanager
def sigterm_as_interrupt():
    """Give normal cancellation the same cleanup path as Ctrl-C (main thread)."""
    previous = signal.getsignal(signal.SIGTERM)
    cancelling = False
    def interrupt(_signum, _frame):
        nonlocal cancelling
        # A repeated termination request must not interrupt fault/FD cleanup.
        if not cancelling:
            cancelling = True
            raise KeyboardInterrupt
    def defer_termination():
        nonlocal cancelling
        cancelling = True
    signal.signal(signal.SIGTERM, interrupt)
    try:
        yield defer_termination
    finally:
        signal.signal(signal.SIGTERM, previous)


class FlySmoke(BASE.Smoke):
    def __init__(self, app, output, allow_mutations=False, mosh_host='137.66.44.233'):
        if not re.fullmatch(r'[a-z0-9][a-z0-9-]{0,61}[a-z0-9]', app):
            raise SmokeError('invalid-fly-app-name')
        try:
            self.mosh_host = str(ipaddress.IPv4Address(mosh_host))
        except ipaddress.AddressValueError:
            raise SmokeError('invalid-dedicated-ipv4') from None
        self.app, self.output = app, Path(output)
        self.allow_mutations = allow_mutations
        self.address, self.state = 'https://' + app + '.fly.dev', Path('/data/state')
        self.api_port, self.preview_port = 8080, 18080
        self.clients, self.results = [], []
        self.token = self.device_id = self.session = None
        self.pairing_pending = False
        self.owned_sessions, self.session_names = set(), set()
        self.leases, self.grants = set(), set()
        self.faults_armed = False
        self.verified = False
        self.deployment = []
        self.cleanup_complete = False
        self.run_id = uuid.uuid4().hex[:12]
        # System CA roots AND hostname checking, including on the first request.
        self.tls_context = ssl.create_default_context()
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=self.tls_context), BASE.NoRedirects())

    def fly(self, *args):
        try:
            result = subprocess.run(['fly', *args], stdout=subprocess.PIPE,
                stderr=subprocess.PIPE, timeout=45, check=False)
        except (OSError, subprocess.SubprocessError):
            raise SmokeError('fly-command-unavailable-or-timeout') from None
        if result.returncode:
            # Neither Fly stderr nor stdout may enter logs (pairing credentials).
            raise SmokeError('fly-command-failed')
        return strict_json(result.stdout)

    def remote(self, *args):
        command = shlex.join(['setpriv', '--reuid', '10001', '--regid', '10001', '--clear-groups',
            '/usr/local/bin/trill', 'demo', *args, '--state-dir', '/data/state'])
        return self.fly('ssh', 'console', '-a', self.app, '-C', command)

    def check_manifest(self):
        value = self.remote('status', '--json')
        if not isinstance(value, dict) or value.get('kind') != 'trill-demo-v1' or value.get('running') is not True:
            raise SmokeError('not-a-running-trill-demo')
        if value.get('address') != self.address or value.get('fingerprint') != '' or value.get('previewPort') != self.preview_port:
            raise SmokeError('demo-origin-or-fly-tls-contract-mismatch')
        return value

    def mutation_guard(self):
        if not self.allow_mutations or not self.verified:
            raise SmokeError('demo-mutations-not-authorized-or-verified')

    def cli(self, *args):
        if args == ('status', '--json'):
            return json.dumps(self.check_manifest()).encode()
        self.mutation_guard()
        self.check_manifest()
        if args == ('pair', '--json'):
            value = self.remote(*args)
        elif len(args) == 2 and args[0] == 'fault' and args[1] in ('capacity', 'api-unavailable', 'invitation-expired', 'clear'):
            if args[1] == 'clear' and not self.faults_armed:
                raise SmokeError('cannot-clear-unowned-faults')
            self.faults_armed = True  # Arm before delivery, including an uncertain response.
            value = self.remote(*args, '--json')
            if args[1] == 'clear':
                self.faults_armed = False
        elif len(args) == 2 and args[0] == 'revoke' and self.device_id and args[1] == self.device_id:
            value = self.remote(*args, '--json')
        else:
            raise SmokeError('remote-command-outside-owned-scope')
        return json.dumps(value).encode()

    def metadata(self):
        machines = self.fly('machines', 'list', '-a', self.app, '--json')
        if not isinstance(machines, list):
            raise SmokeError('invalid-machine-metadata')
        active = [m for m in machines if isinstance(m, dict) and m.get('state') == 'started']
        if len(active) != 1:
            raise SmokeError('single-started-machine-required')
        m = active[0]
        image = m.get('image_ref', {})
        if not isinstance(image, dict):
            raise SmokeError('missing-deployment-image')
        # Never copy config/env or entire Fly responses into the report.
        result = {k: m.get(k) for k in ('id', 'state', 'region', 'instance_id')}
        result['image'] = {k: image[k] for k in ('registry', 'repository', 'tag', 'digest') if k in image}
        if not isinstance(result['id'], str) or not result['id'] or not result['image'].get('digest'):
            raise SmokeError('missing-deployment-provenance')
        return [result]

    def start(self):
        self.deployment = self.metadata()
        value = self.check_manifest()
        state = value.get('state')
        if not isinstance(state, dict) or state.get('faults') != [] or value.get('runtimeFaults') != []:
            raise SmokeError('sandbox-has-faults-or-missing-fault-status')
        self.verified = True
        self.request('GET', '/v1/health', token=False, expected=401)
        self.request('GET', '/v1/sessions', token=False, expected=401)

    def request(self, method, path, value=None, expected=200, token=True,
                content_type='application/json', headers=None):
        if not path.startswith('/') or path.startswith('//') or urllib.parse.urlsplit(path).scheme:
            raise SmokeError('request-must-stay-on-demo-origin')
        route = urllib.parse.urlsplit(path).path
        if method not in ('GET', 'HEAD'):
            self.mutation_guard()
            match = re.fullmatch(r'/v1/sessions/([^/]+)(?:/.*)?', route)
            if match and match[1] not in self.owned_sessions:
                raise SmokeError('cannot-mutate-an-unowned-session')
            if method == 'DELETE' and route.startswith('/v1/leases/') and route.rsplit('/', 1)[1] not in self.leases:
                raise SmokeError('cannot-release-an-unowned-lease')
            if method == 'DELETE' and route.startswith('/v1/previews/') and route.rsplit('/', 1)[1] not in self.grants:
                raise SmokeError('cannot-delete-an-unowned-preview')
        data = json.dumps(value).encode() if value is not None and not isinstance(value, bytes) else value
        h = {'Content-Type': content_type}
        if token and self.token:
            h['Authorization'] = 'Bearer ' + self.token
        h.update(headers or {})
        req = urllib.request.Request(self.address + path, data=data, method=method, headers=h)
        try:
            response = self.opener.open(req, timeout=20)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            body = response.read((2 << 20) + 1)
            allowed = expected if isinstance(expected, tuple) else (expected,)
            if response.status not in allowed:
                raise SmokeError('unexpected-http-status-' + str(response.status))
            if len(body) > 2 << 20:
                raise SmokeError('http-response-too-large')
            result = strict_json(body) if 'application/json' in response.headers.get('Content-Type', '') else body
        if isinstance(result, dict) and method == 'POST':
            if route.endswith('/attachments') and 'path' in result:
                photo_path = result['path']
                if not isinstance(photo_path, str) or '..' in Path(photo_path).parts or not Path(photo_path).is_relative_to(self.state):
                    raise SmokeError('upload-path-outside-demo-state')
            for suffix, key, owned in [('/mosh', 'leaseId', self.leases), ('/v1/previews', 'id', self.grants)]:
                if route.endswith(suffix) and key in result:
                    if not isinstance(result[key], str) or not IDENTIFIER.fullmatch(result[key]):
                        raise SmokeError('invalid-owned-resource-id')
                    owned.add(result[key])
        if method == 'DELETE' and route.startswith('/v1/leases/'):
            self.leases.discard(route.rsplit('/', 1)[1])
        return result

    def invitation(self, expired=False):
        invite = json.loads(self.cli('pair', '--json'))
        if invite.get('version') != 1 or invite.get('address') != self.address or invite.get('fingerprint') != '':
            raise SmokeError('invalid-native-pair-payload')
        code = invite.get('code', '')
        if not isinstance(code, str) or not re.fullmatch(r'[0-9a-fA-F]{64,256}', code):
            raise SmokeError('invalid-single-use-code')
        try:
            end = datetime.datetime.fromisoformat(invite['expiresAt'].replace('Z', '+00:00'))
            seconds = (end - datetime.datetime.now(datetime.timezone.utc)).total_seconds()
        except (KeyError, ValueError, TypeError):
            raise SmokeError('invalid-invitation-expiry') from None
        if not (-60 <= seconds <= 0 if expired else 0 < seconds <= 660):
            raise SmokeError('unexpected-invitation-expiry')
        return invite

    def pair(self):
        invite = self.invitation()
        self.pairing_pending = True
        auth = self.request('POST', '/v1/pair', {'code': invite['code'], 'name': 'Fly smoke ' + self.run_id}, token=False)
        self.device_id = auth.get('deviceId')
        if not isinstance(self.device_id, str) or not IDENTIFIER.fullmatch(self.device_id):
            self.device_id = None
            raise SmokeError('invalid-paired-device-id')
        self.pairing_pending = False
        self.token = auth.get('token')
        if not isinstance(self.token, str) or not self.token or len(self.token) > 1024:
            self.token = None
            raise SmokeError('invalid-device-token')
        self.request('POST', '/v1/pair', {'code': invite['code'], 'name': 'Replay'}, token=False, expected=403)
        self.cli('fault', 'invitation-expired')
        try:
            expired = self.invitation(expired=True)
            self.request('POST', '/v1/pair', {'code': expired['code'], 'name': 'Expired'}, token=False, expected=403)
        finally:
            self.cli('fault', 'clear')
        self.project = next(p for p in self.request('GET', '/v1/projects')['projects'] if p['capabilities']['createAgent'])

    def discover_owned(self):
        sessions = self.request('GET', '/v1/sessions')['sessions']
        for s in sessions:
            if s.get('name') in self.session_names and s.get('projectId') == self.project['id']:
                sid = s.get('id')
                if isinstance(sid, str) and IDENTIFIER.fullmatch(sid):
                    self.owned_sessions.add(sid)
        return sessions

    def create_owned(self, kind, worktree):
        name = 'Fly smoke ' + self.run_id + ' ' + kind
        self.session_names.add(name)  # Retain name if create response is uncertain.
        if kind == 'terminal':
            before = {s['id'] for s in self.request('GET', '/v1/sessions')['sessions']}
            # Production terminal creation intentionally accepts no name/goal.
            self.request('POST', '/v1/sessions', {'projectId': self.project['id'],
                'kind': 'terminal', 'useWorktree': False})
            made = [s for s in self.request('GET', '/v1/sessions')['sessions']
                    if s['id'] not in before and s.get('projectId') == self.project['id']]
            if len(made) != 1 or not IDENTIFIER.fullmatch(made[0]['id']):
                raise SmokeError('created-terminal-not-confirmed-do-not-retry')
            self.owned_sessions.add(made[0]['id'])
            self.request('PATCH', '/v1/sessions/' + made[0]['id'], {'name': name})
        else:
            self.request('POST', '/v1/sessions', {'projectId': self.project['id'], 'kind': kind,
                'name': name, 'goal': 'Exercise scripted demo only', 'useWorktree': worktree,
                **({'branch': 'demo/smoke-' + self.run_id} if worktree else {})})
        made = [s for s in self.discover_owned() if s.get('name') == name and s.get('projectId') == self.project['id']]
        if len(made) != 1:
            raise SmokeError('created-session-not-confirmed-do-not-retry')
        return made[0]['id']

    def lifecycle(self):
        self.session = self.create_owned('agent', True)
        renamed = 'Fly smoke ' + self.run_id + ' renamed'
        self.session_names.add(renamed)
        self.request('PATCH', '/v1/sessions/' + self.session, {'name': renamed})
        for action in ('archive', 'resume', 'stop', 'resume'):
            self.request('POST', '/v1/sessions/' + self.session + '/' + action)
            if action == 'archive':
                assert next(s for s in self.discover_owned() if s['id'] == self.session)['archived']
        final = next(s for s in self.discover_owned() if s['id'] == self.session)
        assert final['name'] == renamed and final['attachable'] and not final['archived']
        self.create_owned('terminal', False)

    def connect(self):
        lease = self.request('POST', '/v1/sessions/' + self.session + '/mosh', {'cols': 90, 'rows': 28})
        if lease.get('host') != self.mosh_host or type(lease.get('port')) is not int or lease['port'] not in (60300, 60301):
            raise SmokeError('mosh-endpoint-outside-dedicated-demo')
        if not isinstance(lease.get('key'), str) or not re.fullmatch(r'[A-Za-z0-9+/]{22}', lease['key']):
            raise SmokeError('invalid-mosh-key')
        master, slave = pty.openpty()
        try:
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 28, 90, 0, 0))
            env = dict(os.environ, MOSH_KEY=lease['key'], TERM='xterm-256color', LANG='en_US.UTF-8')
            process = subprocess.Popen(['mosh-client', lease['host'], str(lease['port'])],
                stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
        except BaseException:
            os.close(master)
            raise
        finally:
            os.close(slave)
        self.clients.append((process, master))
        output = bytearray()
        def ready():
            readable, _, _ = select.select([master], [], [], .1)
            if readable:
                output.extend(os.read(master, 65536))
                del output[:-262144]
            return b'Demo' in output or b'demo' in output
        BASE.wait_for(ready, timeout=30)
        return process, master, lease

    def previews(self):
        grant = self.request('POST', '/v1/previews', {'sessionId': self.session, 'port': self.preview_port}, expected=201)
        path = '/?session=' + urllib.parse.quote(self.session, safe='')
        page = self.request('GET', path, headers={'Cookie': 'trill_preview=' + grant['token']}, token=False)
        assert b'Reconnected through real Mosh' in page
        self.request('POST', '/v1/previews', {'sessionId': self.session, 'port': self.api_port}, expected=400)
        self.request('GET', path, token=False, expected=401)
        self.request('DELETE', '/v1/previews/' + grant['id'])
        self.grants.discard(grant['id'])

    def failures(self):
        try:
            self.cli('fault', 'capacity')
            self.request('POST', '/v1/sessions/' + self.session + '/mosh', {'cols': 90, 'rows': 28}, expected=429)
            self.cli('fault', 'clear')
            self.cli('fault', 'api-unavailable')
            self.request('GET', '/v1/sessions', expected=503)
        finally:
            if self.faults_armed:
                self.cli('fault', 'clear')
        self.request('GET', '/v1/sessions')

    def cleanup(self):
        errors = []
        def attempt(fn):
            try:
                fn()
            except Exception:
                errors.append('cleanup-step-failed')
        # Local process cleanup is independent of all remote/API failures.
        for process, master in self.clients:
            try:
                if process.poll() is None:
                    process.terminate()  # Exact Popen child only; never a name/global kill.
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=5)
            except Exception:
                errors.append('local-mosh-cleanup-failed')
            finally:
                attempt(lambda fd=master: os.close(fd))
        self.clients = []
        if self.faults_armed:
            attempt(lambda: self.cli('fault', 'clear'))
        if self.token:
            for lease in sorted(self.leases):
                attempt(lambda key=lease: self.request('DELETE', '/v1/leases/' + key, expected=(200, 404)))
            for grant in sorted(self.grants):
                attempt(lambda key=grant: self.request('DELETE', '/v1/previews/' + key, expected=(200, 404)))
            if self.session_names:
                attempt(self.discover_owned)
            for session in sorted(self.owned_sessions):
                attempt(lambda sid=session: self.request('POST', '/v1/sessions/' + sid + '/archive'))
        if self.device_id:
            attempt(lambda: self.cli('revoke', self.device_id))
            if self.token:
                attempt(lambda: self.request('GET', '/v1/sessions', expected=401))
        self.token = None
        if self.pairing_pending:
            errors.append('pairing-response-unconfirmed')
        if errors:
            raise SmokeError('owned-resource-cleanup-incomplete')

    def record(self, name, fn):
        started = time.monotonic()
        try:
            fn()
        except BaseException:
            self.results.append({'check': name, 'outcome': 'fail', 'seconds': round(time.monotonic() - started, 3)})
            raise
        self.results.append({'check': name, 'outcome': 'pass', 'seconds': round(time.monotonic() - started, 3)})

    def run(self):
        with sigterm_as_interrupt() as defer_termination:
            return self._run(defer_termination)

    def _run(self, defer_termination):
        # Reserve a private report before any remote action, never after mutations.
        self.output.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(self.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        outcome, failure = 'pass', None
        try:
            self.record('deployment, identity and trusted HTTPS', self.start)
            if self.allow_mutations:
                for name, fn in [('one-use pairing and expiry', self.pair), ('owned session lifecycle', self.lifecycle),
                    ('real Mosh input resize and reconnect', self.terminal), ('fixture files and private upload', self.media),
                    ('authenticated live browser proxy', self.previews), ('fault recovery', self.failures)]:
                    self.record(name, fn)
                if self.metadata() != self.deployment:
                    raise SmokeError('deployment-changed-during-smoke')
        except KeyboardInterrupt:
            outcome, failure = 'fail', 'interrupted'
        except Exception as exc:
            outcome = 'fail'
            # Never serialize exception text: a subprocess/JSON/API may contain secrets.
            failure = str(exc) if isinstance(exc, SmokeError) else type(exc).__name__
        finally:
            # Finish cleanup even if the first SIGTERM arrives during it.
            defer_termination()
            try:
                self.cleanup()
                self.cleanup_complete = True
            except Exception:
                outcome, failure = 'fail', 'owned-resource-cleanup-incomplete'
        report = {'kind': 'trill-demo-fly-smoke-v1', 'app': self.app, 'address': self.address,
            'mode': 'maintenance-window' if self.allow_mutations else 'read-only-preflight',
            'outcome': outcome, 'failure': failure, 'deployment': self.deployment, 'checks': self.results,
            'cleanup': 'complete' if self.cleanup_complete else 'incomplete; inspect before another run',
            'coverage': 'Mac trusted HTTPS/API and upstream mosh-client; scripted demo only; no iOS UI or real-provider claim',
            'retention': 'Cleanup archives only owned sessions, revokes only this device and releases only owned leases; archived worktrees/uploads follow sandbox retention. An uncertain pairing response cannot identify a registration to revoke safely.'}
        with os.fdopen(fd, 'w') as file:
            json.dump(report, file, indent=2)
            file.write('\n')
        print(json.dumps({'outcome': outcome, 'passed': sum(r['outcome'] == 'pass' for r in self.results), 'report': str(self.output)}))
        return outcome == 'pass'


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--app', default='trill-demo')
    parser.add_argument('--mosh-host', default='137.66.44.233', help='dedicated demo IPv4 expected in Mosh leases')
    parser.add_argument('--output', type=Path, required=True, help='new private JSON report; never overwritten')
    parser.add_argument('--allow-demo-mutations', action='store_true', help='authorize own sessions and GLOBAL faults during an exclusive sandbox maintenance window')
    args = parser.parse_args()
    if args.output.exists() or args.output.is_symlink():
        parser.error('output must be a new path')
    if not shutil.which('fly') or (args.allow_demo_mutations and not shutil.which('mosh-client')):
        parser.error('fly is required; full smoke also requires upstream mosh-client')
    try:
        smoke = FlySmoke(args.app, args.output, args.allow_demo_mutations, args.mosh_host)
        return 0 if smoke.run() else 1
    except SmokeError:
        parser.error('invalid demo target configuration')


if __name__ == '__main__':
    raise SystemExit(main())
