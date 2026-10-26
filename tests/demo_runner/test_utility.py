import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, call, patch

from test_demo import demo


class UtilityTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        self.state = self.root / 'lab'
        self.project = self.state / 'project'
        self.project.mkdir(parents=True)
        self.index = self.project / 'index.html'
        self.index.write_text('<h1>Original heading</h1>')
        self.lab = {'state_dir': str(self.state), 'files_root': str(self.project), 'session_name': 'actual-demo'}
        self.scenario = demo.assets('scenarios')['utility']
        self.args = demo.parser().parse_args(['capture', '--allow-terminal-input'])

    def tearDown(self):
        self.temp.cleanup()

    def ui(self):
        profile = demo.assets('profiles')['iphone-17-pro']
        return demo.UI(Mock(), 'owned-simulator', self.args, profile, {}, self.root)

    def test_opt_in_rejected_before_any_commands_or_state_writes(self):
        with patch.object(demo.subprocess, 'run', side_effect=AssertionError('command ran')):
            with self.assertRaisesRegex(demo.DemoError, '--allow-terminal-input'):
                demo.main(['capture', '--scenario', 'utility', '--state-dir', str(self.root / 'absent'), '--dry-run'])
        self.assertFalse(demo.runner_state(self.root / 'absent').exists())

    def test_custom_mutating_operations_cannot_omit_opt_in_metadata(self):
        for step in [{'op': 'text-input'}, {'op': 'tap', 'target': 'terminal-return'}, {'op': 'tap', 'target': 'terminal-insert'}]:
            with self.assertRaises(demo.DemoError):
                demo.authorize_input(demo.parser().parse_args(['capture']), {'steps': [step]})

    def test_opted_in_dry_run_is_read_only(self):
        with patch.object(demo.subprocess, 'run', side_effect=AssertionError('command ran')), contextlib.redirect_stdout(io.StringIO()) as out:
            demo.main(['capture', '--scenario', 'utility', '--allow-terminal-input', '--dry-run'])
        self.assertTrue(json.loads(out.getvalue())['terminal_input_authorized'])

    def test_primary_headings_and_alternate_on_idempotent_retry(self):
        before = self.index.read_bytes()
        for appearance, expected in [('light', 'make room for good ideas.'), ('dark', 'find a little room to think.')]:
            plan = demo.utility_plan(self.scenario, appearance, self.lab, self.state)
            self.assertEqual(plan['heading'], expected)
            self.assertIn(expected, plan['request'])
            self.assertIn('node --check app.js', plan['request'])
            self.assertLess(len(plan['request']), 150)
            self.assertEqual(plan['request'], plan['request'].lower())
        self.assertEqual(self.index.read_bytes(), before)
        self.index.write_text('<h1>make room for good ideas.</h1>')
        plan = demo.utility_plan(self.scenario, 'light', self.lab, self.state)
        self.assertNotEqual(plan['heading'], 'make room for good ideas.')

    def test_file_readiness_requires_changed_hash_and_actual_h1(self):
        plan = demo.utility_plan(self.scenario, 'light', self.lab, self.state)
        self.index.write_text('<p>' + plan['heading'] + '</p><h1>Wrong</h1>')
        commands = Mock()
        with patch.object(demo.time, 'monotonic', side_effect=[0, 121, 121, 121]):
            with self.assertRaises(demo.DemoError):
                demo.wait_heading(commands, self.lab, self.state, plan, 120)
        self.index.write_text('<h1>make room for <em>good ideas.</em></h1>')
        after = self.index.read_bytes()
        with patch.object(demo.time, 'monotonic', side_effect=[0, 0, .4, .8, .8]), patch.object(demo.time, 'sleep'):
            self.assertNotEqual(demo.wait_heading(commands, self.lab, self.state, plan, 120), plan['before_sha256'])
        self.assertEqual(self.index.read_bytes(), after)
        self.assertEqual(commands.event.call_args.kwargs['outcome'], 'ready')

    def test_unchanged_expected_heading_never_satisfies_readiness(self):
        self.index.write_text('<h1>Same</h1>')
        digest, _ = demo.public_index(self.lab, self.state)
        with patch.object(demo.time, 'monotonic', side_effect=[0, 121, 121, 121]), self.assertRaises(demo.DemoError):
            demo.wait_heading(Mock(), self.lab, self.state, {'heading': 'Same', 'before_sha256': digest}, 120)

    def test_readiness_rejects_escape_and_symlink_without_reading_secret(self):
        outside = self.root / 'outside'
        outside.mkdir()
        (outside / 'index.html').write_text('private')
        with self.assertRaises(demo.DemoError):
            demo.public_index({**self.lab, 'files_root': str(outside)}, self.state)
        self.index.unlink()
        self.index.symlink_to(outside / 'index.html')
        with self.assertRaises(OSError):
            demo.public_index(self.lab, self.state)

    def test_first_last_and_explicit_pose_scheduling(self):
        scenario = {'steps': [{'pose': x, 'op': 'pause', 'seconds': 0} for x in ['first', 'last', 'all', 'cover', 'unfolded']]}
        self.assertEqual([x['pose'] for x in demo.steps_for_pose(scenario, 0, 2, 'cover')], ['first', 'all', 'cover'])
        self.assertEqual([x['pose'] for x in demo.steps_for_pose(scenario, 1, 2, 'unfolded')], ['last', 'all', 'unfolded'])
        single = demo.steps_for_pose(self.scenario, 0, 1, 'portrait')
        self.assertLess(next(i for i,s in enumerate(single) if s.get('target') == 'terminal-return'), next(i for i,s in enumerate(single) if s['op'] == 'wait-heading'))
        self.assertEqual(self.scenario['initial_snapshot'], 'sessions')
        self.assertEqual(self.scenario['subsequent_snapshot'], 'terminal')

    def test_liveness_rejects_shell_and_ambiguous_status(self):
        for status in [{}, {'agent_running': False}, {'agent_running': 'true'}]:
            commands = Mock()
            commands.lab.return_value = subprocess.CompletedProcess([], 0, json.dumps(status), '')
            with self.assertRaisesRegex(demo.DemoError, 'start it explicitly'):
                demo.require_agent(commands, self.state)
        commands.lab.return_value = subprocess.CompletedProcess([], 0, '{"agent_running":true}', '')
        demo.require_agent(commands, self.state)

    def test_native_input_needs_liveness_and_visible_idle_evidence(self):
        ui = self.ui()
        with self.assertRaises(demo.DemoError):
            ui.text_input('terminal-draft', 'Public request', 'draft-keyboard')
        ui.agent_check = Mock()
        with self.assertRaisesRegex(demo.DemoError, 'Ask Codex'):
            ui.text_input('terminal-draft', 'Public request', 'draft-keyboard')
        ui.c.idb.assert_not_called()

    def input_ui(self):
        ui = self.ui()
        ui.agent_check = Mock()
        ui.agent_ready_verified = True
        ui.tap = Mock()
        ui.wait = Mock()
        ui.native_input_point = Mock(return_value=('180', '290'))
        return ui

    def test_hid_chunks_focus_verify_empty_and_each_cumulative_value(self):
        ui = self.input_ui()
        order = []
        ui.tap.side_effect = lambda *args: order.append('focus')
        ui.wait.side_effect = lambda *args: order.append('keyboard')
        ui.wait_native_value = Mock(side_effect=lambda *args: order.append(('verify', args[-1])))
        ui.c.idb.side_effect = lambda *args: order.append(args)
        with patch.object(demo.time, 'sleep'):
            ui.text_input('terminal-draft', "abc. d-e 'test', 0123456789", 'draft-keyboard', 4, .2)
        self.assertEqual(order[:3], ['focus', 'keyboard', ('verify', '')])
        writes = ui.c.idb.call_args_list
        value = "abc. d-e 'test', 0123456789"
        chunks = [value[i:i+4] for i in range(0, len(value), 4)]
        self.assertEqual([call.args[-1] for call in writes], chunks)
        self.assertTrue(all(call.args[1:3] == ('ui', 'text') for call in writes))
        expected = [''] + [value[:end] for end in range(4, len(value), 4)] + [value]
        self.assertEqual([call.args[-1] for call in ui.wait_native_value.call_args_list], expected)
        self.assertEqual(ui.agent_check.call_count, 2)

    def test_shift_or_unsupported_input_is_rejected_before_focus_or_writes(self):
        for value in ('Capital', 'under_score', 'https://localhost', 'hello!', 'double"quote',
                      'parentheses()', 'brackets[]', 'slash/path', 'café', 'line\nbreak', 'tab\tkey'):
            ui = self.input_ui()
            with self.subTest(value=value), self.assertRaises(demo.DemoError):
                ui.text_input('terminal-draft', value, 'draft-keyboard')
            ui.tap.assert_not_called()
            ui.c.idb.assert_not_called()

    def test_utility_uses_native_source_preview_without_url_entry(self):
        steps = self.scenario['steps']
        source = next(i for i, step in enumerate(steps) if step.get('name') == 'source')
        self.assertEqual(steps[source + 2], {'op': 'tap', 'target': 'file-render-preview', 'key': 'AXUniqueId'})
        self.assertEqual([step['op'] for step in steps[source + 3:]], ['wait-preview', 'pause', 'screenshot'])
        self.assertFalse(any(step.get('target') in {'source-back', 'Browser', 'preview-address'} or step['op'] == 'set-preview' for step in steps))

    def test_native_value_reads_exact_identity_and_value_with_no_writes(self):
        ui = self.ui()
        def result(value, identity='terminal-draft'):
            return subprocess.CompletedProcess([], 0, json.dumps([{'AXUniqueId': identity, 'AXValue': value}]), '')
        ui.c.idb.side_effect = [result('old'), result('aBc. d-e')]
        with patch.object(demo.time, 'sleep'):
            ui.wait_native_value(('180', '290'), 'terminal-draft', 'aBc. d-e')
        self.assertEqual(ui.c.idb.call_count, 2)
        self.assertTrue(all(call.args[1:3] == ('ui', 'describe-point') for call in ui.c.idb.call_args_list))
        self.assertIn('--json', ui.c.idb.call_args.args)
        self.assertNotIn('--client-type', ui.c.idb.call_args.args)
        for response in [result('', 'other-field'), result(None), subprocess.CompletedProcess([], 0, 'not JSON', '')]:
            ui.c.idb.side_effect = None
            ui.c.idb.return_value = response
            with self.assertRaises(demo.DemoError):
                ui.wait_native_value(('180', '290'), 'terminal-draft', '')

    def test_stale_draft_is_never_replaced(self):
        ui = self.input_ui()
        ui.c.idb.return_value = subprocess.CompletedProcess([], 0, json.dumps({'AXUniqueId': 'terminal-draft', 'AXValue': 'old draft'}), '')
        with self.assertRaisesRegex(demo.DemoError, 'stale text'):
            ui.text_input('terminal-draft', 'fresh request', 'draft-keyboard')
        self.assertEqual(ui.c.idb.call_count, 1)
        self.assertEqual(ui.c.idb.call_args.args[1:3], ('ui', 'describe-point'))
        self.assertFalse(ui.agent_ready_verified)

    def test_binding_verification_failure_stops_without_next_prefix_or_insert(self):
        ui = self.input_ui()
        reads = iter(['', 'ABC>'])  # A held Shift must stop submission, not retry typing.
        def respond(udid, group, command, *args, **kwargs):
            if command == 'describe-point':
                return subprocess.CompletedProcess([], 0, json.dumps({'AXUniqueId': 'terminal-draft', 'AXValue': next(reads)}), '')
        ui.c.idb.side_effect = respond
        with patch.object(demo.time, 'monotonic', side_effect=[0, 0, 0, 6]), self.assertRaisesRegex(demo.DemoError, 'did not match'):
            ui.text_input('terminal-draft', 'abc. d-e test', 'draft-keyboard', 4, .2)
        self.assertEqual([call.args[2] for call in ui.c.idb.call_args_list], ['describe-point', 'text', 'describe-point'])
        self.assertEqual(ui.tap.call_args_list[0].args, ('terminal-draft', 'AXUniqueId'))
        self.assertEqual(ui.tap.call_count, 1)

    def test_native_input_points_use_reviewed_map_or_live_ax_center(self):
        ui = self.ui()
        ui.wait = Mock(return_value={'frame': {'x': 10, 'y': 20, 'width': 40, 'height': 60}})
        self.assertEqual(ui.native_input_point('terminal-draft'), ('30', '50'))
        ui.map = {'poses': {}}
        ui.screen = lambda: contextlib.nullcontext(self.root / 'native.png')
        ui.map_pose = Mock(return_value={'points': [402, 874], 'targets': {'terminal-draft': {'tap': [180, 290]}}})
        self.assertEqual(ui.native_input_point('terminal-draft'), ('180', '290'))
        ui.map_pose.return_value['targets']['terminal-draft']['tap'] = [403, 290]
        with self.assertRaises(demo.DemoError):
            ui.native_input_point('terminal-draft')

    def test_preview_uses_verified_empty_native_field_and_binding_callback(self):
        ui = self.input_ui()
        ui.wait_native_value = Mock()
        ui.preview('http://localhost:18080/index.html')
        self.assertEqual([call.args[-1] for call in ui.wait_native_value.call_args_list], ['', 'http://localhost:18080/index.html'])
        self.assertEqual([call.args[2] for call in ui.c.idb.call_args_list], ['set-value', 'key'])
        self.assertEqual(ui.tap.call_args_list[-1].args, ('Go',))
        ui.c.idb.reset_mock()
        ui.tap.reset_mock()
        ui.wait_native_value.side_effect = demo.DemoError('value mismatch')
        with self.assertRaises(demo.DemoError):
            ui.preview('http://localhost:18080/index.html')
        ui.c.idb.assert_not_called()
        self.assertEqual(ui.tap.call_count, 1)

    def test_idle_prompt_accepts_verified_cursor_occluded_initial_letter(self):
        ui = self.ui()
        ui.agent_check = Mock()
        ui.assert_no_agent_prompt = Mock(return_value='sk Codex to do anything')
        ui.wait_agent()
        self.assertTrue(ui.agent_ready_verified)
        ui.agent_check.assert_called_once()

    def test_text_bounds_reject_controls_secrets_and_excess(self):
        for text in ['', 'a\nReturn', 'a\x1b', 'Bearer private', 'x' * 513]:
            with self.assertRaises(demo.DemoError):
                demo.bounded_text(text)

    def test_idle_prompt_gate_rejects_trust_instead_of_accepting(self):
        ui = self.ui()
        ui.agent_check = Mock()
        ui.screen = lambda: contextlib.nullcontext(self.root / 'screen.png')
        ui.vision_text = lambda path: 'Do you trust this folder? Ask Codex'
        with self.assertRaisesRegex(demo.DemoError, 'no automatic approval'):
            ui.wait_agent()
        ui.c.idb.assert_not_called()

    def test_quiet_is_targeted_bounded_and_uses_positional_timeout(self):
        ui = self.ui()
        ui.quiet()
        args, kwargs = ui.c.idb.call_args
        self.assertEqual(args, ('owned-simulator', 'ui', 'quiet', '--bundle-id', demo.BUNDLE, '--quiet-window-ms', '400', '5'))
        self.assertEqual(kwargs['timeout'], 10)

    def test_sessions_return_honors_quiet_opt_out_and_waits_before_ready(self):
        for enabled in (False, True):
            with self.subTest(ui_quiet=enabled):
                ui = self.ui()
                ui.args.ui_quiet = enabled
                actions = Mock()
                ui.tap = actions.tap
                ui.wait = actions.wait
                ui.quiet = actions.quiet
                ui.c.event = actions.event
                ui.return_to_sessions()
                expected = [call.tap('sessions-back', 'AXUniqueId'),
                            call.wait('sessions-list', 'AXUniqueId')]
                if enabled:
                    expected.append(call.quiet())
                expected.append(call.event('sessions_ready_before_recording'))
                self.assertEqual(actions.mock_calls, expected)

    def test_sessions_map_requires_explicit_reviewed_navigation_entries(self):
        ui = self.ui()
        valid = {'sessions-back': {'text': 'Mosh', 'tap': [1, 2]},
                 'sessions-list': {'text': 'Sessions'}}
        cases = [None, {}, {'sessions-back': valid['sessions-back']},
                 dict(valid, **{'sessions-list': {'text': ''}}),
                 dict(valid, **{'sessions-back': {'text': 'Mosh'}})]
        for targets in cases:
            with self.subTest(targets=targets):
                ui.map = None if targets is None else {'poses': {ui.pose: {'targets': targets}}}
                with self.assertRaisesRegex(demo.DemoError, 'Sessions start'):
                    ui.require_sessions_map()
        ui.map = {'poses': {ui.pose: {'targets': valid}}}
        ui.require_sessions_map()
        ui.c.idb.assert_not_called()

    def test_partial_file_writes_reset_stability(self):
        plan = {'before_sha256': 'before', 'heading': 'Expected'}
        samples = [('partial', ['Expected']), ('finished', ['Expected']),
                   ('finished', ['Expected']), ('finished', ['Expected'])]
        commands = Mock()
        with patch.object(demo, 'public_index', side_effect=samples) as read, patch.object(demo.time, 'monotonic', side_effect=[0,0,.4,.8,1.2,1.2]), patch.object(demo.time, 'sleep'):
            self.assertEqual(demo.wait_heading(commands, self.lab, self.state, plan, 120), 'finished')
        self.assertEqual(read.call_count, 4)
        self.assertIn('not agent completion', commands.event.call_args.kwargs['evidence'])

    def test_denied_or_dead_agent_never_writes_native_input(self):
        ui = self.ui()
        ui.args.allow_terminal_input = False
        with self.assertRaises(demo.DemoError):
            ui.text_input('terminal-draft', 'Actual request', 'draft-keyboard')
        ui.c.idb.assert_not_called()
        ui.args.allow_terminal_input = True
        ui.agent_check = Mock(side_effect=demo.DemoError('Agent is a shell'))
        ui.agent_ready_verified = True
        with self.assertRaises(demo.DemoError):
            ui.text_input('terminal-draft', 'Actual request', 'draft-keyboard')
        ui.c.idb.assert_not_called()

    def test_partial_native_typing_failure_is_not_retried_or_cleared(self):
        for failed_call in (1, 2):
            ui = self.input_ui()
            ui.wait_native_value = Mock()
            ui.c.idb.side_effect = [None] * (failed_call - 1) + [demo.DemoError('uncertain delivery')]
            with patch.object(demo.time, 'sleep'), self.assertRaises(demo.DemoError):
                ui.text_input('terminal-draft', 'a real bounded request', 'draft-keyboard', 4, .2)
            self.assertEqual(ui.c.idb.call_count, failed_call)
            with self.assertRaisesRegex(demo.DemoError, 'Ask Codex'):
                ui.text_input('terminal-draft', 'a real bounded request', 'draft-keyboard')
            self.assertEqual(ui.c.idb.call_count, failed_call)

    def test_source_readiness_is_scoped_and_accepts_inline_h1_markup(self):
        ui = self.ui()
        ui.map = {'poses': {'portrait': {'targets': {'source-content': {'rect': [0,140,402,600]}}, 'points': [402,874]}}}
        ui.screen = lambda: contextlib.nullcontext(self.root / 'full.png')
        ui.map_pose = lambda path: ui.map['poses']['portrait']
        ui.vision_text = lambda path: '<h1>make room for <br>good ideas.</h1>' if path.name == 'source-content-readiness.png' else 'Wrong'
        ui.wait_source('make room for good ideas.')
        self.assertIn('0,140,402,600', ui.c.idb.call_args.args)

    def test_browser_readiness_cannot_use_echoed_terminal_heading(self):
        ui = self.ui()
        ui.map = {'poses': {'portrait': {'targets': {'preview-content': {'rect': [0,287,402,550]}}, 'points': [402,874]}}}
        ui.screen = lambda: contextlib.nullcontext(self.root / 'full.png')
        ui.map_pose = lambda path: ui.map['poses']['portrait']
        ui.text = lambda *args: 'make room for good ideas.'  # Deliberately visible only in terminal.
        paths = []
        ui.vision_text = lambda path: paths.append(path.name) or 'Wrong browser heading'
        with patch.object(demo.time, 'monotonic', side_effect=[0, 40]), self.assertRaises(demo.DemoError):
            ui.wait_preview('make room for good ideas.')
        self.assertEqual(paths, ['preview-content-readiness.png'])
        self.assertIn('0,287,402,550', ui.c.idb.call_args.args)


if __name__ == '__main__':
    unittest.main()
