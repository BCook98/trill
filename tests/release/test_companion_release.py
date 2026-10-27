import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock
import zipfile

spec = importlib.util.spec_from_file_location('companion_release', Path(__file__).resolve().parents[2] / 'scripts/build-companion-release.py')
companion_release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(companion_release)


class CompanionReleaseTests(unittest.TestCase):
    def fake_build_command(self, arguments, **kwargs):
        if arguments[:2] == ['go', 'build']:
            Path(arguments[arguments.index('-o') + 1]).write_text('binary fixture')
        if arguments[0] == 'codesign' and '--test-requirement' in arguments:
            self.assertTrue(arguments[arguments.index('--test-requirement') + 1].startswith('=anchor apple generic'))
        return subprocess.CompletedProcess(arguments, 0, stdout='{"status":"Accepted"}')

    def test_unsigned_fixture_cannot_look_like_a_publishable_release(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'release'
            with mock.patch.object(companion_release, 'configuration', return_value=('0.1.0', 'a' * 40, '')):
                with mock.patch.object(companion_release, 'run', side_effect=self.fake_build_command) as commands:
                    with mock.patch.object(companion_release.sys, 'platform', 'darwin'):
                        companion_release.build(output, unsigned=True)
            self.assertFalse((output / 'manifest.json').exists())
            self.assertFalse((output / 'install-companion.sh').exists())
            self.assertFalse((output / 'trill.rb').exists())
            self.assertTrue((output / 'UNSIGNED-NOT-FOR-DISTRIBUTION.json').exists())
            self.assertEqual(len(commands.call_args_list), 4)
            for architecture in ('arm64', 'amd64'):
                with zipfile.ZipFile(output / f'trill-companion-0.1.0-darwin-{architecture}.zip') as archive:
                    self.assertEqual(set(archive.namelist()), {'trill', 'trillctl', 'trilld'})
            for call in commands.call_args_list:
                self.assertIn('buildinfo.Version=dev', call.args[0][4])

    def test_failed_notarization_leaves_no_publishable_manifest(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'release'
            with mock.patch.object(companion_release, 'configuration', return_value=('0.1.0', 'a' * 40, 'ABCDEFGHIJ')):
                with mock.patch.object(companion_release, 'run', side_effect=self.fake_build_command):
                    with mock.patch.object(companion_release, 'notarize', side_effect=ValueError('not accepted')):
                        with mock.patch.dict(os.environ, {'COMPANION_SIGNING_IDENTITY': 'Developer ID Application: Fixture'}):
                            with mock.patch.object(companion_release.sys, 'platform', 'darwin'):
                                with self.assertRaisesRegex(ValueError, 'not accepted'):
                                    companion_release.build(output)
            self.assertFalse((output / 'manifest.json').exists())
            self.assertFalse((output / 'install-companion.sh').exists())

    def test_success_requires_both_notarized_architectures_and_embeds_trust(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'release'
            with mock.patch.object(companion_release, 'configuration', return_value=('0.1.0', 'a' * 40, 'ABCDEFGHIJ')):
                with mock.patch.object(companion_release, 'run', side_effect=self.fake_build_command):
                    with mock.patch.object(companion_release, 'notarize') as notarize:
                        with mock.patch.object(companion_release.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, stdout='100\n')):
                            with mock.patch.dict(os.environ, {'COMPANION_SIGNING_IDENTITY': 'Developer ID Application: Fixture'}):
                                with mock.patch.object(companion_release.sys, 'platform', 'darwin'):
                                    companion_release.build(output)
            self.assertEqual(notarize.call_count, 2)
            manifest = json.loads((output / 'manifest.json').read_text())
            self.assertEqual(set(manifest['artifacts']), {'arm64', 'amd64'})
            self.assertEqual(manifest['tag'], 'v0.1.0-build.' + 'a' * 12)
            self.assertTrue(manifest['build'].isdigit())
            installer = (output / 'install-companion.sh').read_text()
            self.assertIn('expected_signing_team="ABCDEFGHIJ"', installer)
            self.assertNotIn('UNCONFIGURED_SIGNING_TEAM', installer)
            self.assertIn('--test-requirement "=$requirement"', installer)
            cask = (output / 'trill.rb').read_text()
            self.assertIn('version "0.1.0,100"', cask)
            self.assertNotRegex(cask, r'\b(?:mosh|tmux)\b')
            self.assertIn('v0.1.0-build.' + 'a' * 12, cask)
            self.assertIn(manifest['artifacts']['arm64']['sha256'], cask)
            self.assertIn(manifest['artifacts']['amd64']['sha256'], cask)

    def test_cask_rejects_untrusted_release_metadata(self):
        manifest = {'version': '2026.10.25', 'commit': 'a' * 40, 'build': '10',
                    'tag': 'v2026.10.25-build.' + 'a' * 12, 'artifacts': {}}
        for architecture in ('arm64', 'amd64'):
            manifest['artifacts'][architecture] = {'name': f'trill-companion-2026.10.25-darwin-{architecture}.zip', 'sha256': 'b' * 64}
        self.assertIn('version "2026.10.25,10"', companion_release.render_homebrew_cask(manifest))
        manifest['tag'] = 'untrusted"; system("bad")'
        with self.assertRaises(ValueError):
            companion_release.render_homebrew_cask(manifest)

    def test_missing_public_team_fails_before_signing(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'sidecar/distribution').mkdir(parents=True)
            (root / 'sidecar/distribution/signing-team.txt').write_text('\n')
            with mock.patch.object(companion_release, 'ROOT', root):
                with mock.patch.object(companion_release.subprocess, 'check_output', side_effect=[json.dumps({'marketing_version': '0.1.0'}), 'a' * 40]):
                    with self.assertRaisesRegex(ValueError, 'public Apple team'):
                        companion_release.configuration(unsigned=False)

    def test_companion_uses_the_shared_ios_version(self):
        version, commit, _ = companion_release.configuration(unsigned=True)
        shared = json.loads(subprocess.check_output([companion_release.sys.executable, str(companion_release.ROOT / 'scripts/release.py'), 'check'], text=True))
        self.assertEqual(version, shared['marketing_version'])
        self.assertEqual(len(commit), 40)

    def test_checkout_installer_refuses_unconfigured_distribution(self):
        result = subprocess.run(['bash', str(companion_release.ROOT / 'scripts/install-companion.sh'), '0.1.0'], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('not configured', result.stderr)


if __name__ == '__main__':
    unittest.main()
