import json
import subprocess
import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch

from test_demo import demo


class WorkspaceTests(unittest.TestCase):
    def setUp(self):
        self.ui = object.__new__(demo.UI)
        self.ui.args = SimpleNamespace(ui_quiet=False, timeout=0)
        self.ui.udid = 'owned-simulator'
        self.ui.c = Mock()
        self.ui.tree = Mock()
        self.ui.native_input_point = Mock(return_value=('100', '200'))
        self.closed = dict(AXUniqueId='session-workspace', AXLabel='Files and browser',
                           role='AXButton', enabled=True)
        self.opened = dict(self.closed, AXLabel='Hide files and browser')
        self.ui.c.idb.return_value = subprocess.CompletedProcess([], 0, json.dumps([self.closed]), '')

    def test_open_workspace_never_resolves_point_or_presses(self):
        self.ui.tree.return_value = [self.opened]
        self.ui.ensure_workspace()
        self.ui.c.idb.assert_not_called()
        self.ui.native_input_point.assert_not_called()

    def test_closed_workspace_uses_guarded_native_press_once(self):
        self.ui.tree.side_effect = [[self.closed], [self.opened]]
        self.ui.ensure_workspace()
        self.ui.native_input_point.assert_called_once_with('session-workspace')
        calls = self.ui.c.idb.call_args_list
        self.assertEqual(calls[0].args, ('owned-simulator', 'ui', 'describe-point', '100', '200',
                                       '--api', 'ax-coresimulator', '--json'))
        self.assertEqual(calls[1].args, ('owned-simulator', 'ui', 'tap', '100', '200',
                                       '--api', 'ax-coresimulator', '--expected-key', 'AXLabel',
                                       '--expected-value', 'Files and browser'))
        self.assertEqual(len(calls), 2)

    def test_unknown_disabled_and_duplicate_states_never_press(self):
        for tree in ([], [self.closed, self.opened],
                     [dict(self.closed, AXUniqueId='other')],
                     [dict(self.closed, AXLabel='Files and browser extra')],
                     [dict(self.closed, enabled=False)], [dict(self.closed, role='AXTextArea')]):
            with self.subTest(tree=tree):
                self.ui.tree.return_value = tree
                with self.assertRaises(demo.DemoError):
                    self.ui.ensure_workspace()
                self.ui.c.idb.assert_not_called()

    def test_stale_or_wrong_point_never_presses(self):
        self.ui.tree.return_value = [self.closed]
        for point in ([self.opened], [], [self.closed, self.closed],
                      [dict(self.closed, AXUniqueId='other')], [dict(self.closed, enabled=False)]):
            with self.subTest(point=point):
                self.ui.c.idb.reset_mock()
                self.ui.c.idb.return_value.stdout = json.dumps(point)
                with self.assertRaises(demo.DemoError):
                    self.ui.ensure_workspace()
                self.assertEqual(self.ui.c.idb.call_count, 1)

    def test_unconfirmed_or_failed_press_is_never_retried(self):
        self.ui.tree.return_value = [self.closed]
        with self.assertRaisesRegex(demo.DemoError, 'no toggle retried'):
            self.ui.ensure_workspace()
        self.assertEqual(self.ui.c.idb.call_count, 2)
        self.ui.c.idb.reset_mock()
        self.ui.c.idb.side_effect = [subprocess.CompletedProcess([], 0, json.dumps([self.closed]), ''),
                                   demo.DemoError('uncertain press')]
        with self.assertRaisesRegex(demo.DemoError, 'uncertain press'):
            self.ui.ensure_workspace()
        self.assertEqual(self.ui.c.idb.call_count, 2)

    def test_utility_uses_ensure_instead_of_workspace_toggle(self):
        scenario = demo.assets('scenarios')['utility']
        demo.validate_scenario(scenario)
        self.assertEqual(sum(s['op'] == 'ensure-workspace' for s in scenario['steps']), 1)
        self.assertFalse(any(s['op'] == 'tap' and s.get('target') == 'session-workspace'
                             for s in scenario['steps']))

    def test_current_duo_legacy_workspace_rejects_before_device_commands(self):
        profile = demo.assets('profiles')['iphone-duo']
        self.ui.map = json.loads((demo.ROOT / 'demos/profiles/maps/iphone-duo.json').read_text())
        with self.assertRaisesRegex(demo.DemoError, 'reviewed preview-address'):
            self.ui.require_preview_map(demo.assets('scenarios')['workspace'], profile)
        self.ui.c.assert_not_called()
        self.ui.c.idb.assert_not_called()
        self.ui.require_preview_map(demo.assets('scenarios')['utility'], profile)

    def test_url_preview_requires_all_reviewed_targets_for_its_pose(self):
        profile = dict(poses=[dict(id='cover'), dict(id='unfolded')])
        scenario = dict(steps=[dict(op='set-preview')])
        targets = {name: dict(text=name, tap=[100, 200])
                   for name in ('preview-address', 'Go', 'Field Notes')}
        self.ui.map = dict(poses=dict(unfolded=dict(targets=targets)))
        self.ui.require_preview_map(scenario, profile)
        for name in list(targets):
            with self.subTest(name=name):
                entry = targets.pop(name)
                try:
                    with self.assertRaises(demo.DemoError):
                        self.ui.require_preview_map(scenario, profile)
                finally:
                    targets[name] = entry
        targets['Go']['tap'] = [float('nan'), 100]
        with self.assertRaisesRegex(demo.DemoError, 'tap coordinates'):
            self.ui.require_preview_map(scenario, profile)
