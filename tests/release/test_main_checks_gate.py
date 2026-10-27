import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class MainChecksGateTests(unittest.TestCase):
    def run_gate(self, **overrides):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            github = directory / 'gh'
            github.write_text('''#!/bin/sh
printf '%s\\n' "$*" >> "$CI_TEST_CALLS"
case "$2" in
  list) printf '%s' "${CI_TEST_RUN_ID-123}" ;;
  watch) exit "${CI_TEST_WATCH_EXIT-0}" ;;
  view) printf '%s' "${CI_TEST_CONCLUSION-success}" ;;
  *) exit 9 ;;
esac
''')
            github.chmod(0o700)
            sleeper = directory / 'sleep'
            sleeper.write_text('#!/bin/sh\nexit 0\n')
            sleeper.chmod(0o700)
            environment = dict(os.environ, PATH=str(directory) + os.pathsep + os.environ['PATH'],
                               GITHUB_REPOSITORY='BCook98/trill', CI_TEST_CALLS=str(directory / 'calls'), **overrides)
            result = subprocess.run(['bash', str(ROOT / 'scripts/ci-require-main-checks.sh')],
                                    cwd=ROOT, env=environment, capture_output=True, text=True)
            calls = (directory / 'calls').read_text()
            commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
            self.assertIn('--commit ' + commit, calls)
            self.assertIn('--workflow checks.yml', calls)
            self.assertIn('--branch main --event push', calls)
            return result

    def test_successful_exact_commit_checks_allow_release(self):
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_missing_check_run_blocks_release(self):
        result = self.run_gate(CI_TEST_RUN_ID='')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('No main Checks run', result.stderr)

    def test_failed_watch_blocks_release(self):
        self.assertNotEqual(self.run_gate(CI_TEST_WATCH_EXIT='1').returncode, 0)

    def test_only_successful_conclusions_allow_release(self):
        for conclusion in ('failure', 'cancelled', 'skipped', 'neutral', 'timed_out', ''):
            with self.subTest(conclusion=conclusion):
                result = self.run_gate(CI_TEST_CONCLUSION=conclusion)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('did not succeed', result.stderr)
