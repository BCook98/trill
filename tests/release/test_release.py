import importlib.util
import os
import subprocess
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('release', Path(__file__).resolve().parents[2] / 'scripts/release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def test_repeat_build_keeps_marketing_version(self):
        for version in ['0.1.0', '2026.10.25']:
            for commit in ['a' * 12, 'b' * 12]:
                release.check_tag('v' + version + '-build.' + commit, version)

    def test_mismatched_or_unsafe_tag_rejected(self):
        for tag in ['v0.2.0-build.' + 'a' * 12, 'v0.1.0', 'v0.1.0-build.bad', 'v0.1.0-build.' + 'a' * 12 + '\n']:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.check_tag(tag, '0.1.0')

    def test_explicit_version_bump_preserves_build_and_comments(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'Version.xcconfig'
            path.write_text('// shared config\nMARKETING_VERSION = 0.1.0\nCURRENT_PROJECT_VERSION = 1\n')
            release.set_version('2026.10.25', path)
            self.assertEqual(release.read_version(path), ('2026.10.25', 1))
            self.assertTrue(path.read_text().startswith('// shared config\n'))
            for value in ['2026.10.25', '0.1.0', 'bad', '1.0.0\nOTHER = bad']:
                original = path.read_bytes()
                with self.assertRaises(ValueError):
                    release.set_version(value, path)
                self.assertEqual(path.read_bytes(), original)

    def test_new_train_requires_a_valid_zero_padded_date(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'Version.xcconfig'
            path.write_text('MARKETING_VERSION = 0.1.0\nCURRENT_PROJECT_VERSION = 7\n')
            original = path.read_bytes()
            for value in ['1.2.3', '2026.7.12', '2026.07.2', '2026.02.29',
                          '2026.13.01', '2026.04.31', '2026.00.12', '2026.12.00']:
                with self.subTest(value=value), self.assertRaises(ValueError):
                    release.set_version(value, path)
                self.assertEqual(path.read_bytes(), original)
            release.set_version('2028.02.29', path)
            self.assertEqual(release.read_version(path), ('2028.02.29', 7))

    def test_bad_configuration_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'Version.xcconfig'
            for text in ['MARKETING_VERSION = 1.0.0\n', 'MARKETING_VERSION = 1.0.0\nCURRENT_PROJECT_VERSION = 0\n',
                         'MARKETING_VERSION = 1.0.0\nMARKETING_VERSION = 2.0.0\nCURRENT_PROJECT_VERSION = 1\n']:
                path.write_text(text)
                with self.assertRaises(ValueError):
                    release.read_version(path)

    def test_cloud_hook_is_discoverable_and_works_outside_checkout(self):
        hook = release.ROOT / 'ios/ci_scripts/ci_post_clone.sh'
        environment = {k: v for k, v in os.environ.items() if k not in {'CI_TAG', 'CI_PRIMARY_REPOSITORY_PATH'}}
        with tempfile.TemporaryDirectory() as tmp:
            result = subprocess.run(['sh', str(hook)], cwd=tmp, env=environment, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('marketing_version', result.stdout)

    def test_cloud_hook_rejects_wrong_tag(self):
        environment = dict(os.environ, CI_PRIMARY_REPOSITORY_PATH=str(release.ROOT), CI_TAG='v99.99.99-build.aaaaaaaaaaaa')
        result = subprocess.run(['sh', str(release.ROOT / 'ios/ci_scripts/ci_post_clone.sh')], env=environment, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Release check failed', result.stderr)
