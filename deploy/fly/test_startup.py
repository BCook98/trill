#!/usr/bin/env python3
"""Offline native-nft startup probes; never invoke a real firewall or kernel API."""
import ipaddress
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
FAKE_TOOL = r'''
import json, os, pathlib, sys
root = pathlib.Path(os.environ['PROBE_ROOT'])
name = pathlib.Path(sys.argv[0]).name
with (root / 'calls.jsonl').open('a') as f:
    f.write(json.dumps([name] + sys.argv[1:]) + '\n')
if name == 'getent':
    if os.environ.get('PROBE_FAIL') == 'getent':
        sys.exit(1)
    print(os.environ.get('PROBE_BIND', '172.19.0.2') + ' STREAM fly-global-services')
    sys.exit(0)
if name == 'nft':
    (root / 'nft.rules').write_text(sys.stdin.read())
    sys.exit(1 if os.environ.get('PROBE_FAIL') == 'nft' else 0)
sys.exit(2)
'''


def parse_policy(text):
    """Recognize only this deployment's owned-table transaction and rule vocabulary.

    This is a predicate model, not an nft parser or kernel qualification. Unknown
    commands/rules fail the check instead of being silently ignored.
    """
    transaction = re.fullmatch(
        r'\s*add table inet trill_demo\s+delete table inet trill_demo\s+'
        r'table inet trill_demo\s*\{\s*chain service\s*\{(?P<service>.*?)\}\s*'
        r'chain output\s*\{(?P<output>.*?)\}\s*\}\s*', text, re.S)
    if not transaction:
        raise AssertionError('expected one transaction replacing only inet trill_demo')
    output = re.fullmatch(
        r'\s*type filter hook output priority (?P<priority>-?\d+); policy accept;\s*'
        r'meta skuid (?P<uid>\d+) jump service\s*', transaction['output'])
    if not output:
        raise AssertionError('unexpected output hook or additional UID/base-chain rules')
    rules = []
    for line in transaction['service'].strip().splitlines():
        line = line.strip()
        if not line:
            continue
        if match := re.fullmatch(r'ct state ([a-z,]+) accept', line):
            rules.append(('state', set(match[1].split(','))))
        elif match := re.fullmatch(r'oifname "([^"]+)" accept', line):
            rules.append(('interface', match[1]))
        elif match := re.fullmatch(r'ip daddr \{ ([0-9./, ]+) \} reject', line):
            rules.append(('private4', [ipaddress.IPv4Network(v.strip()) for v in match[1].split(',')]))
        elif line == 'counter reject':
            rules.append(('reject', None))
        else:
            raise AssertionError('unmodeled nft rule: ' + line)
    return int(output['uid']), int(output['priority']), rules


def verdict(text, *, source='172.19.0.2', destination='8.8.8.8', protocol='udp',
            sport=60300, state='new', interface='eth0', uid=10001):
    owner, _, rules = parse_policy(text)
    # ACCEPT means only this owned table does not reject; other VM tables still apply.
    if uid != owner:
        return 'ACCEPT'
    source, destination = ipaddress.ip_address(source), ipaddress.ip_address(destination)
    for kind, value in rules:
        if kind == 'state' and state in value:
            return 'ACCEPT'
        if kind == 'interface' and interface == value:
            return 'ACCEPT'
        if kind == 'private4' and destination.version == 4 and any(destination in net for net in value):
            return 'REJECT'
        if kind == 'reject':
            return 'REJECT'
    raise AssertionError('missing final reject')


class StartupChecks(unittest.TestCase):
    def probe(self, fail='', bind='172.19.0.2'):
        with tempfile.TemporaryDirectory(prefix='trill-fly-policy-') as temp:
            root = Path(temp)
            bins = root / 'bin'
            bins.mkdir()
            for name in ('getent', 'nft'):
                path = bins / name
                path.write_text('#!' + sys.executable + '\n' + FAKE_TOOL)
                path.chmod(0o755)
            source = (HERE / 'entrypoint.sh').read_text()
            body = source[source.index('# Native nftables'):source.index('\nexec setpriv')]
            env = dict(os.environ, PATH=str(bins) + ':/usr/bin:/bin', PROBE_ROOT=temp,
                       PROBE_FAIL=fail, PROBE_BIND=bind)
            result = subprocess.run(['sh', '-c', 'set -eu\nfail() { exit 91; }\n' + body + '\nprintf ready'],
                                    env=env, text=True, capture_output=True, timeout=10)
            files = {p.name: p.read_text() for p in root.iterdir() if p.is_file()}
            calls = [json.loads(line) for line in files['calls.jsonl'].splitlines()]
            return result, files, calls

    def test_private_volume_and_script_syntax(self):
        source = (HERE / 'entrypoint.sh').read_text()
        self.assertIn('chown 10001:10001 /data\nchmod 0700 /data', source)
        self.assertNotIn('chmod 0770 /data', source)
        subprocess.run(['sh', '-n', str(HERE / 'entrypoint.sh')], check=True)

    def test_single_atomic_transaction_replaces_only_owned_table(self):
        result, files, calls = self.probe()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'ready')
        self.assertEqual(calls, [['nft', '-f', '-']])
        rules = files['nft.rules']
        owner, priority, policy = parse_policy(rules)
        self.assertEqual(owner, 10001)
        self.assertEqual(priority, -10)
        self.assertEqual([kind for kind, _ in policy], ['state', 'interface', 'private4', 'reject'])
        self.assertEqual(policy[0][1], {'established', 'related'})
        self.assertEqual(policy[1][1], 'lo')
        # Strictly reject global flushes, foreign table changes, split transactions,
        # extra hooks/UID rules, and rules added after the final reject/table close.
        for changed in ('flush ruleset\n' + rules,
                        rules.replace('delete table inet trill_demo', 'delete table inet foreign'),
                        rules + '\nadd table inet foreign\n',
                        rules.replace('meta skuid 10001 jump service', 'meta skuid 10001 jump service\naccept'),
                        rules.replace('counter reject', 'counter reject\nip6 saddr fdaa::1 accept')):
            with self.subTest(changed=changed):
                with self.assertRaises(AssertionError):
                    parse_policy(changed)

    def test_ipv4_positive_and_negative_policy_probes(self):
        result, files, _ = self.probe(bind='172.19.0.9')
        self.assertEqual(result.returncode, 0, result.stderr)
        rules = files['nft.rules']
        for sport in (60300, 60301):
            self.assertEqual(verdict(rules, source='172.19.0.9', sport=sport), 'REJECT')
        for state in ('established', 'related'):
            self.assertEqual(verdict(rules, destination='10.0.0.1', protocol='tcp', state=state), 'ACCEPT')
        self.assertEqual(verdict(rules, destination='127.0.0.1', interface='lo'), 'ACCEPT')
        for destination in ('0.1.2.3', '10.1.2.3', '100.100.100.100', '127.0.0.1',
                            '169.254.169.254', '172.16.1.1', '192.0.0.1', '192.0.2.1',
                            '192.168.1.1', '198.18.0.1', '198.51.100.1', '203.0.113.1',
                            '224.0.0.1', '255.255.255.255'):
            with self.subTest(destination=destination):
                # Correct source and port must still lose to earlier private rejection.
                self.assertEqual(verdict(rules, source='172.19.0.9', destination=destination), 'REJECT')
        self.assertEqual(verdict(rules, source='172.19.0.2'), 'REJECT')
        for sport in (0, 53, 60299, 60302, 65535):
            self.assertEqual(verdict(rules, source='172.19.0.9', sport=sport), 'REJECT')
        for state in ('new', 'invalid', 'untracked'):
            self.assertEqual(verdict(rules, protocol='tcp', state=state), 'REJECT')

    def test_ipv6_no_new_udp_or_tcp_exception(self):
        result, files, _ = self.probe()
        self.assertEqual(result.returncode, 0, result.stderr)
        rules = files['nft.rules']
        for destination in ('fdaa::1', 'fe80::1', '2606:4700:4700::1111', '::ffff:169.254.169.254'):
            for protocol in ('tcp', 'udp'):
                with self.subTest(destination=destination, protocol=protocol):
                    self.assertEqual(verdict(rules, source='fdaa::2', destination=destination, protocol=protocol), 'REJECT')
        self.assertEqual(verdict(rules, source='::1', destination='::1', interface='lo'), 'ACCEPT')
        self.assertEqual(verdict(rules, source='fdaa::2', destination='fdaa::1', state='established'), 'ACCEPT')
        # Even a modeled IPv4-looking socket source cannot make ip saddr match IPv6.
        self.assertEqual(verdict(rules, destination='fdaa::1'), 'REJECT')

    def test_other_uids_bypass_only_this_owned_table(self):
        _, files, _ = self.probe()
        for uid in (0, 1000, 10002):
            for destination in ('169.254.169.254', 'fdaa::1'):
                self.assertEqual(verdict(files['nft.rules'], uid=uid, destination=destination, protocol='tcp'), 'ACCEPT')

    def test_transaction_failure_blocks_startup(self):
        result, files, calls = self.probe(fail='nft')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, '')
        self.assertEqual(calls, [['nft', '-f', '-']])
        parse_policy(files['nft.rules'])


if __name__ == '__main__':
    unittest.main(verbosity=2)
