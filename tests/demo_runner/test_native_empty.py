"""Regression coverage for UIKit's explicit-null empty native text value."""
import json
import subprocess
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

from test_demo import demo


class NativeEmptyValueTests(unittest.TestCase):
    def setUp(self):
        self.ui = object.__new__(demo.UI)
        self.ui.c = Mock()
        self.ui.udid = 'unit-test-no-device'
        self.ui.args = SimpleNamespace(timeout=0, allow_terminal_input=True)
        self.point = ('180', '290')
        self.target = 'terminal-draft'
        self.field = {
            'AXUniqueId': self.target,
            'AXValue': None,
            'enabled': True,
            'role': 'AXTextArea',
            'traits': ['TextEntry', 'IsEditing'],
        }

    def response(self, fields):
        return subprocess.CompletedProcess([], 0, json.dumps(fields), '')

    def read(self, field, expected=''):
        self.ui.c.idb.reset_mock()
        self.ui.c.idb.return_value = self.response([field])
        try:
            return self.ui.wait_native_value(self.point, self.target, expected)
        finally:
            self.ui.c.idb.assert_called_once_with(
                self.ui.udid, 'ui', 'describe-point', *self.point,
                '--api', 'ax-coresimulator', '--json', timeout=5)

    def test_exact_preview_placeholder_is_accepted_only_as_empty(self):
        self.target = 'preview-address'
        field = {'AXUniqueId': self.target, 'AXValue': 'localhost:3000 or https://…',
                 'enabled': True, 'role': 'AXTextField'}
        self.read(field)
        with self.assertRaises(demo.DemoError):
            self.read(field, 'http://localhost:18080/index.html')

    def test_preview_placeholder_rejects_wrong_target_role_or_enabled_state(self):
        placeholder = 'localhost:3000 or https://…'
        # The same text in an identified draft is stale content, not a placeholder.
        with self.assertRaises(demo.DemoError):
            self.read(dict(self.field, AXValue=placeholder, role='AXTextField'))
        self.target = 'preview-address'
        field = {'AXUniqueId': self.target, 'AXValue': placeholder,
                 'enabled': True, 'role': 'AXTextField'}
        for changes in ({'AXUniqueId': 'other-address'}, {'role': 'AXTextArea'},
                        {'role': 'AXButton'}, {'enabled': False}, {'enabled': 1}):
            with self.subTest(changes=changes), self.assertRaises(demo.DemoError):
                self.read(dict(field, **changes))

    def test_preview_placeholder_exception_rejects_real_urls_and_near_matches(self):
        self.target = 'preview-address'
        for value in ('http://localhost:18080/index.html', 'https://example.com',
                      'localhost:3000', 'localhost:3000 or https://...',
                      'localhost:3000 or https://… '):
            field = {'AXUniqueId': self.target, 'AXValue': value,
                     'enabled': True, 'role': 'AXTextField'}
            with self.subTest(value=value), self.assertRaises(demo.DemoError):
                self.read(field)

    def test_explicit_null_is_empty_for_enabled_text_entry_roles(self):
        for role in ('AXTextArea', 'AXTextField'):
            with self.subTest(role=role):
                self.read(dict(self.field, role=role))

    def test_null_requires_enabled_text_entry_role_and_traits(self):
        for changes in (
            {'enabled': False}, {'enabled': None}, {'enabled': 'true'},
            {'enabled': 1}, {'role': 'AXButton'}, {'role': None},
            {'traits': []}, {'traits': ['IsEditing']},
        ):
            with self.subTest(changes=changes), self.assertRaises(demo.DemoError):
                self.read(dict(self.field, **changes))
        for absent in ('enabled', 'role', 'traits'):
            field = dict(self.field)
            del field[absent]
            with self.subTest(absent=absent), self.assertRaises(demo.DemoError):
                self.read(field)

    def test_missing_value_and_nonstring_values_are_not_empty(self):
        missing = dict(self.field)
        del missing['AXValue']
        for field in (missing, dict(self.field, AXValue=0),
                      dict(self.field, AXValue=False), dict(self.field, AXValue=[])):
            with self.subTest(field=field), self.assertRaises(demo.DemoError):
                self.read(field)

    def test_wrong_missing_and_duplicate_identities_are_rejected(self):
        missing = dict(self.field)
        del missing['AXUniqueId']
        for fields in ([dict(self.field, AXUniqueId='other-draft')], [missing],
                       [self.field, dict(self.field)], []):
            self.ui.c.idb.reset_mock()
            self.ui.c.idb.return_value = self.response(fields)
            with self.subTest(fields=fields), self.assertRaises(demo.DemoError):
                self.ui.wait_native_value(self.point, self.target, '')
            self.assertEqual(self.ui.c.idb.call_count, 1)
            self.assertEqual(self.ui.c.idb.call_args.args[1:3], ('ui', 'describe-point'))

    def test_null_is_never_accepted_for_postwrite_verification(self):
        with self.assertRaises(demo.DemoError):
            self.read(self.field, 'aBc. d-e test')

    def test_postwrite_string_matching_is_exact(self):
        expected = 'aBc. d-e test'
        self.read(dict(self.field, AXValue=expected), expected)
        self.read(dict(self.field, AXValue=''))
        for wrong in ('aBC> D_E TEST', expected + ' ', expected.lower(), ''):
            with self.subTest(wrong=wrong), self.assertRaises(demo.DemoError):
                self.read(dict(self.field, AXValue=wrong), expected)
        with self.assertRaisesRegex(demo.DemoError, 'stale text'):
            self.read(dict(self.field, AXValue='old draft'))

    def test_null_after_first_prefix_stops_edits_without_retry_or_insert(self):
        self.ui.agent_check = Mock()
        self.ui.agent_ready_verified = True
        self.ui.tap = Mock()
        self.ui.wait = Mock()
        self.ui.native_input_point = Mock(return_value=self.point)
        # The empty preflight succeeds; null after the real edit must fail.
        self.ui.c.idb.return_value = self.response([self.field])
        with self.assertRaises(demo.DemoError):
            self.ui.text_input(self.target, 'abc. d-e test', 'draft-keyboard', 4, .2)
        self.assertEqual([call.args[2] for call in self.ui.c.idb.call_args_list],
                         ['describe-point', 'text', 'describe-point'])
        self.assertEqual(self.ui.c.idb.call_args_list[1].args[-1], 'abc.')
        self.ui.tap.assert_called_once_with(self.target, 'AXUniqueId')
        self.assertFalse(self.ui.agent_ready_verified)
        with self.assertRaisesRegex(demo.DemoError, 'Ask Codex'):
            self.ui.text_input(self.target, 'abc. d-e test', 'draft-keyboard', 4, .2)
        self.assertEqual(self.ui.c.idb.call_count, 3)


if __name__ == '__main__':
    unittest.main()
