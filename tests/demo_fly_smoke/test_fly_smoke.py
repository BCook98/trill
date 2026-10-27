import contextlib
import importlib.util
import io
import json
import os
import signal
from pathlib import Path
import ssl
import subprocess
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location('demo_fly_smoke', Path(__file__).parents[2] / 'scripts/demo-fly-smoke.py')
MOD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MOD)


def manifest(**changes):
    result = {'kind': 'trill-demo-v1', 'running': True, 'address': 'https://trill-demo.fly.dev',
              'fingerprint': '', 'previewPort': 18080, 'runtimeFaults': [], 'state': {'faults': []}}
    result.update(changes)
    return result


class FlySmokeTests(unittest.TestCase):
    def make(self, allow=True):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        return MOD.FlySmoke('trill-demo', Path(temporary.name) / 'report.json', allow)

    def test_strict_json_accepts_known_diagnostic_only(self):
        self.assertEqual(MOD.strict_json(b'Connecting to fdaa:0:123::2... complete\n{"kind":"trill-demo-v1"}\n'), {'kind': 'trill-demo-v1'})
        for raw in [b'warning {"token":"private"}', b'{}\nmore', b'{"a":1,"a":2}', b'{"x":NaN}', b'Error: private\n{}']:
            with self.subTest(raw=raw), self.assertRaises(MOD.SmokeError) as error:
                MOD.strict_json(raw)
            self.assertNotIn('private', str(error.exception))

    def test_tls_trust_and_redirect_policy_are_not_optional(self):
        smoke = self.make()
        self.assertTrue(smoke.tls_context.check_hostname)
        self.assertEqual(smoke.tls_context.verify_mode, ssl.CERT_REQUIRED)
        self.assertTrue(any(isinstance(h, MOD.BASE.NoRedirects) for h in smoke.opener.handlers))
        request = MOD.urllib.request.Request(smoke.address + '/v1/sessions', headers={'Authorization': 'Bearer private'})
        self.assertIsNone(MOD.BASE.NoRedirects().redirect_request(request, None, 302, 'redirect', {}, 'https://outside.example'))

    def test_no_mutation_flag_and_wrong_manifest_fail_before_action(self):
        smoke = self.make(allow=False)
        smoke.verified = True
        with mock.patch.object(smoke, 'remote') as remote:
            with self.assertRaises(MOD.SmokeError):
                smoke.cli('pair', '--json')
            with self.assertRaises(MOD.SmokeError):
                smoke.request('POST', '/v1/sessions', {})
            remote.assert_not_called()
        smoke = self.make()
        smoke.verified = True
        for wrong in [manifest(kind='production'), manifest(address='https://other.fly.dev'), manifest(fingerprint='nonempty')]:
            with mock.patch.object(smoke, 'remote', return_value=wrong) as remote:
                with self.assertRaises(MOD.SmokeError):
                    smoke.cli('pair', '--json')
                remote.assert_called_once_with('status', '--json')

    def test_remote_cli_never_allows_host_stop_reset_or_foreign_revoke(self):
        smoke = self.make()
        smoke.verified = True
        smoke.device_id = 'our-device'
        with mock.patch.object(smoke, 'remote', return_value=manifest()) as remote:
            for args in [('stop',), ('reset', '--yes'), ('serve',), ('revoke', 'foreign-device'), ('fault', 'drop-leases'), ('fault', 'clear')]:
                with self.subTest(args=args), self.assertRaises(MOD.SmokeError):
                    smoke.cli(*args)
            self.assertTrue(all(c.args == ('status', '--json') for c in remote.call_args_list))

    def test_scoped_session_and_lease_guards(self):
        smoke = self.make()
        smoke.verified = True
        with mock.patch.object(smoke.opener, 'open') as http:
            for method, route in [('POST', '/v1/sessions/seeded/archive'), ('DELETE', '/v1/leases/someone-else'), ('DELETE', '/v1/previews/someone-else')]:
                with self.subTest(route=route), self.assertRaises(MOD.SmokeError):
                    smoke.request(method, route)
            http.assert_not_called()

    def test_ssh_command_and_json_diagnostics_never_print_tokens(self):
        smoke = self.make()
        result = subprocess.CompletedProcess([], 0, b'Connecting to fdaa::123 complete\n{"code":"private-code"}\n', b'private-stderr')
        captured = io.StringIO()
        with mock.patch.object(MOD.subprocess, 'run', return_value=result) as command, contextlib.redirect_stdout(captured):
            self.assertEqual(smoke.remote('pair', '--json')['code'], 'private-code')
        args = command.call_args.args[0]
        self.assertEqual(args[:6], ['fly', 'ssh', 'console', '-a', 'trill-demo', '-C'])
        self.assertEqual(args[6], 'setpriv --reuid 10001 --regid 10001 --clear-groups /usr/local/bin/trill demo pair --json --state-dir /data/state')
        self.assertEqual(captured.getvalue(), '')
        with mock.patch.object(MOD.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, b'private-code', b'private-stderr')):
            with self.assertRaises(MOD.SmokeError) as error:
                smoke.remote('status', '--json')
            self.assertNotIn('private', str(error.exception))

    def test_stream_lease_has_no_external_endpoint(self):
        smoke = self.make()
        smoke.session = 'own-session'
        with mock.patch.object(smoke, 'request', return_value={'leaseId': 'lease', 'host': 'outside.example'}), mock.patch.object(MOD.BASE, 'TerminalStream') as stream:
            with self.assertRaises(AssertionError):
                smoke.connect()
            stream.assert_not_called()

    def test_stream_handshake_failure_closes_connection(self):
        connection = mock.Mock()
        connection.host, connection.port = 'localhost', 443
        connection.sock.recv.return_value = b''
        with self.assertRaises(EOFError):
            MOD.BASE.TerminalStream(connection, '/v1/leases/owned/terminal', 'synthetic')
        connection.close.assert_called_once()

    def test_cleanup_continues_locally_and_remotely_after_failure(self):
        smoke = self.make()
        stream = mock.Mock()
        stream.close.side_effect = OSError('already closed')
        smoke.clients = [stream]
        smoke.token, smoke.device_id = 'private-token', 'our-device'
        smoke.faults_armed = True
        smoke.owned_sessions = {'our-session'}
        smoke.leases = {'our-lease'}
        with mock.patch.object(smoke, 'cli') as cli, mock.patch.object(smoke, 'request') as request:
            with self.assertRaises(MOD.SmokeError):
                smoke.cleanup()
            cli.assert_any_call('fault', 'clear')
            cli.assert_any_call('revoke', 'our-device')
            request.assert_any_call('DELETE', '/v1/leases/our-lease', expected=(200, 404))
            request.assert_any_call('GET', '/v1/sessions', expected=401)
        stream.close.assert_called_once()
        self.assertEqual(smoke.clients, [])
        self.assertIsNone(smoke.token)

    def test_fault_delivery_failure_still_marks_cleanup_needed(self):
        smoke = self.make()
        smoke.verified = True
        with mock.patch.object(smoke, 'remote', side_effect=[manifest(), MOD.SmokeError('uncertain-delivery')]):
            with self.assertRaises(MOD.SmokeError):
                smoke.cli('fault', 'api-unavailable')
        self.assertTrue(smoke.faults_armed)

    def test_machine_report_omits_all_config_and_environment(self):
        smoke = self.make()
        machine = {'id': 'abc', 'state': 'started', 'region': 'iad', 'instance_id': 'instance',
                   'config': {'env': {'PASSWORD': 'private'}}, 'image_ref': {'digest': 'sha256:abc', 'repository': 'trill-demo', 'password': 'private'}}
        with mock.patch.object(smoke, 'fly', return_value=[machine]):
            result = smoke.metadata()
        self.assertNotIn('private', json.dumps(result))
        self.assertEqual(result[0]['image']['digest'], 'sha256:abc')
        with mock.patch.object(smoke, 'fly', return_value=[machine, machine]):
            with self.assertRaises(MOD.SmokeError):
                smoke.metadata()

    def test_failed_report_is_redacted_and_existing_output_prevents_actions(self):
        smoke = self.make()
        with mock.patch.object(smoke, 'start', side_effect=RuntimeError('Bearer private-token SESSION_TOKEN=private')), mock.patch.object(smoke, 'cleanup') as cleanup, contextlib.redirect_stdout(io.StringIO()):
            self.assertFalse(smoke.run())
            cleanup.assert_called_once()
        report = smoke.output.read_text()
        self.assertNotIn('private-token', report)
        self.assertNotIn('SESSION_TOKEN', report)
        self.assertEqual(smoke.output.stat().st_mode & 0o777, 0o600)
        with mock.patch.object(smoke, 'start') as start:
            with self.assertRaises(FileExistsError):
                smoke.run()
            start.assert_not_called()


    def test_preflight_requires_explicitly_clear_core_and_runtime_faults(self):
        smoke = self.make()
        bad = [manifest(runtimeFaults=['api-unavailable']),
               manifest(state={'faults': ['capacity']}), manifest(runtimeFaults=None),
               manifest(state={})]
        absent = manifest()
        del absent['runtimeFaults']
        bad.append(absent)
        for value in bad:
            with self.subTest(value=value), mock.patch.object(smoke, 'metadata', return_value=[]), mock.patch.object(smoke, 'check_manifest', return_value=value), mock.patch.object(smoke, 'request') as request:
                with self.assertRaises(MOD.SmokeError):
                    smoke.start()
                self.assertFalse(smoke.verified)
                request.assert_not_called()
        with mock.patch.object(smoke, 'metadata', return_value=[]), mock.patch.object(smoke, 'check_manifest', return_value=manifest()), mock.patch.object(smoke, 'request'):
            smoke.start()
        self.assertTrue(smoke.verified)

    def test_real_sigterm_clears_fault_and_closes_fd_then_restores_handler(self):
        smoke = self.make()
        previous = signal.getsignal(signal.SIGTERM)
        stream = mock.Mock()
        smoke.clients = [stream]
        def interrupt():
            self.assertIsNot(signal.getsignal(signal.SIGTERM), previous)
            smoke.faults_armed = True
            os.kill(os.getpid(), signal.SIGTERM)
            self.fail('SIGTERM did not interrupt the check')
        cleanup = smoke.cleanup
        def repeated_cancel_during_cleanup():
            os.kill(os.getpid(), signal.SIGTERM)
            cleanup()
        with mock.patch.object(smoke, 'start', side_effect=interrupt), mock.patch.object(smoke, 'cleanup', side_effect=repeated_cancel_during_cleanup), mock.patch.object(smoke, 'cli') as cli, contextlib.redirect_stdout(io.StringIO()):
            self.assertFalse(smoke.run())
        cli.assert_called_once_with('fault', 'clear')
        self.assertIs(signal.getsignal(signal.SIGTERM), previous)
        stream.close.assert_called_once()
        report = json.loads(smoke.output.read_text())
        self.assertEqual(report['failure'], 'interrupted')
        self.assertEqual(report['cleanup'], 'complete')

    def test_old_lease_404_discard_does_not_remove_new_lease(self):
        smoke = self.make()
        smoke.verified = True
        smoke.leases = {'old-lease', 'new-lease'}
        response = mock.MagicMock()
        response.__enter__.return_value = response
        response.status = 404
        response.headers = {'Content-Type': 'application/json'}
        response.read.return_value = b'{}'
        with mock.patch.object(smoke.opener, 'open', return_value=response):
            smoke.request('DELETE', '/v1/leases/old-lease', expected=404)
        self.assertEqual(smoke.leases, {'new-lease'})

    def terminal_mocks(self, smoke, status, same_lease=False):
        """Patch transport edges only; execute the inherited terminal regression."""
        stack = contextlib.ExitStack()
        self.addCleanup(stack.close)
        first, second = mock.Mock(pid=111111), mock.Mock(pid=222222)
        connect = stack.enter_context(mock.patch.object(smoke, 'connect', side_effect=[
            (first, {'leaseId': 'old-lease'}),
            (second, {'leaseId': 'old-lease' if same_lease else 'new-lease'})]))
        stack.enter_context(mock.patch.object(smoke, 'file', side_effect=[
            'Initial heading', 'Smoke through HTTPS', 'Reconnected through HTTPS']))
        stack.enter_context(mock.patch.object(smoke, 'cli', return_value=json.dumps(status).encode()))
        stack.enter_context(mock.patch.object(MOD.BASE.time, 'sleep'))
        events = []
        for stream in (first, second):
            stream.send.side_effect = lambda value: events.append(('send', value))
        request = stack.enter_context(mock.patch.object(smoke, 'request', side_effect=lambda *a, **kw: events.append(('request', a, kw))))
        predicates = []
        def require_true(predicate, **_kwargs):
            predicates.append(predicate)
            self.assertTrue(predicate(), 'ignored resize must fail')
        stack.enter_context(mock.patch.object(MOD.BASE, 'wait_for', side_effect=require_true))
        return connect, request, events, predicates, stack

    def test_inherited_terminal_fails_if_resize_is_ignored_or_other_session(self):
        for sessions in [[{'id': 'own', 'columns': 90, 'rows': 28}],
                         [{'id': 'someone-else', 'columns': 93, 'rows': 31}]]:
            with self.subTest(sessions=sessions):
                smoke = self.make()
                smoke.session = 'own'
                connect, request, events, predicates, stack = self.terminal_mocks(smoke, {'state': {'sessions': sessions}})
                try:
                    with self.assertRaisesRegex(AssertionError, 'ignored resize must fail'):
                        smoke.terminal()
                    connect.assert_called_once()
                    request.assert_not_called()
                    self.assertEqual(predicates[-1].__name__, 'resized')
                finally:
                    stack.close()

    def test_inherited_reconnect_requires_distinct_lease_ids(self):
        smoke = self.make()
        smoke.session = 'own'
        connect, request, events, _, _stack = self.terminal_mocks(smoke, {'state': {'sessions': [
            {'id': 'own', 'columns': 93, 'rows': 31}]}}, same_lease=True)
        with self.assertRaisesRegex(AssertionError, 'reconnect reused lease identity'):
            smoke.terminal()
        self.assertEqual(connect.call_count, 2)
        request.assert_not_called()

    def test_inherited_reconnect_checks_old_404_before_fresh_input(self):
        smoke = self.make()
        smoke.session = 'own'
        _, request, events, _, _stack = self.terminal_mocks(smoke, {'state': {'sessions': [
            {'id': 'own', 'columns': 93, 'rows': 31}]}})
        smoke.terminal()
        request.assert_any_call('DELETE', '/v1/leases/old-lease', expected=404)
        old = events.index(('request', ('DELETE', '/v1/leases/old-lease'), {'expected': 404}))
        fresh = next(index for index, event in enumerate(events) if event[0] == 'send' and event[1].get('bytes') == MOD.BASE.base64.b64encode(b'demo heading Reconnected through HTTPS\r').decode())
        self.assertLess(old, fresh)


    def test_terminal_create_payload_omits_name_goal_then_claims_and_renames(self):
        smoke = self.make()
        smoke.project = {'id': 'our-project'}
        before = [{'id': 'seeded', 'projectId': 'our-project', 'name': 'Existing'}]
        created = {'id': 'new-terminal', 'projectId': 'our-project', 'name': 'Terminal'}
        foreign = {'id': 'foreign-new', 'projectId': 'other-project', 'name': 'Other'}
        catalog = [dict(s) for s in before]
        events = []
        def request(method, path, value=None):
            events.append((method, path, value))
            if method == 'GET':
                return {'sessions': [dict(s) for s in catalog]}
            if method == 'POST':
                self.assertEqual(value, {'projectId': 'our-project', 'kind': 'terminal', 'useWorktree': False})
                self.assertNotIn('name', value)
                self.assertNotIn('goal', value)
                catalog.extend([dict(created), dict(foreign)])
                return {}
            self.assertEqual((method, path), ('PATCH', '/v1/sessions/new-terminal'))
            # Receipt ownership must be saved even if this rename later fails.
            self.assertEqual(smoke.owned_sessions, {'new-terminal'})
            self.assertEqual(value, {'name': 'Fly smoke ' + smoke.run_id + ' terminal'})
            catalog[1]['name'] = value['name']
            return {}
        with mock.patch.object(smoke, 'request', side_effect=request):
            self.assertEqual(smoke.create_owned('terminal', False), 'new-terminal')
        self.assertEqual([e[0] for e in events], ['GET', 'POST', 'GET', 'PATCH', 'GET'])
        self.assertEqual(smoke.owned_sessions, {'new-terminal'})

    def test_terminal_create_ambiguous_receipt_never_retries_or_claims(self):
        before = [{'id': 'seeded', 'projectId': 'our-project', 'name': 'Existing'}]
        cases = [[],
                 [{'id': 'new-a', 'projectId': 'our-project'}, {'id': 'new-b', 'projectId': 'our-project'}],
                 [{'id': 'invalid/id', 'projectId': 'our-project'}],
                 [{'id': 'foreign-new', 'projectId': 'other-project'}]]
        for new in cases:
            with self.subTest(new=new):
                smoke = self.make()
                smoke.project = {'id': 'our-project'}
                with mock.patch.object(smoke, 'request', side_effect=[{'sessions': before}, {}, {'sessions': before + new}]) as request:
                    with self.assertRaisesRegex(MOD.SmokeError, 'created-terminal-not-confirmed-do-not-retry'):
                        smoke.create_owned('terminal', False)
                self.assertEqual([c.args[0] for c in request.call_args_list], ['GET', 'POST', 'GET'])
                self.assertEqual(smoke.owned_sessions, set())

    def test_terminal_create_uncertain_post_is_not_retried(self):
        smoke = self.make()
        smoke.project = {'id': 'our-project'}
        with mock.patch.object(smoke, 'request', side_effect=[{'sessions': []}, MOD.SmokeError('uncertain-create-response')]) as request:
            with self.assertRaisesRegex(MOD.SmokeError, 'uncertain-create-response'):
                smoke.create_owned('terminal', False)
        self.assertEqual([c.args[0] for c in request.call_args_list], ['GET', 'POST'])
        self.assertEqual(smoke.owned_sessions, set())

    def test_terminal_create_rename_failure_retains_confirmed_id_for_cleanup(self):
        smoke = self.make()
        smoke.project = {'id': 'our-project'}
        made = {'id': 'new-terminal', 'projectId': 'our-project', 'name': 'Terminal'}
        with mock.patch.object(smoke, 'request', side_effect=[{'sessions': []}, {}, {'sessions': [made]}, MOD.SmokeError('uncertain-rename-response')]) as request:
            with self.assertRaisesRegex(MOD.SmokeError, 'uncertain-rename-response'):
                smoke.create_owned('terminal', False)
        self.assertEqual([c.args[0] for c in request.call_args_list], ['GET', 'POST', 'GET', 'PATCH'])
        self.assertEqual(smoke.owned_sessions, {'new-terminal'})


if __name__ == '__main__':
    unittest.main()
