#!/usr/bin/env python3
"""Build companion ZIPs; signed mode requires Developer ID and notarization."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time
import zipfile

ROOT = Path(__file__).resolve().parents[1]
VERSION_PATTERN = re.compile(r'(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})')


def run(arguments, **kwargs):
    return subprocess.run(arguments, check=True, **kwargs)


def configuration(unsigned):
    version = (ROOT / 'sidecar/COMPANION_VERSION').read_text().strip()
    if not VERSION_PATTERN.fullmatch(version):
        raise ValueError('COMPANION_VERSION must use numeric MAJOR.MINOR.PATCH')
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    team = (ROOT / 'sidecar/distribution/signing-team.txt').read_text().strip()
    if not unsigned:
        if not re.fullmatch(r'[A-Z0-9]{10}', team):
            raise ValueError('Set the public Apple team ID in sidecar/distribution/signing-team.txt before signed releases')
        if subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT, text=True).strip():
            raise ValueError('Signed releases require a clean checkout')
        if not os.environ.get('COMPANION_SIGNING_IDENTITY', '').startswith('Developer ID Application:'):
            raise ValueError('Set COMPANION_SIGNING_IDENTITY to a Developer ID Application identity')
        if not os.environ.get('COMPANION_NOTARY_PROFILE') and not all(os.environ.get(name) for name in
                ('COMPANION_NOTARY_KEY_PATH', 'COMPANION_NOTARY_KEY_ID', 'COMPANION_NOTARY_ISSUER_ID')):
            raise ValueError('Configure a notary Keychain profile or App Store Connect notarization key')
    return version, commit, team


def notarize(archive):
    command = ['xcrun', 'notarytool', 'submit', str(archive), '--wait', '--timeout', '30m', '--output-format', 'json']
    if os.environ.get('COMPANION_NOTARY_PROFILE'):
        command += ['--keychain-profile', os.environ['COMPANION_NOTARY_PROFILE']]
    else:
        command += ['--key', os.environ['COMPANION_NOTARY_KEY_PATH'], '--key-id', os.environ['COMPANION_NOTARY_KEY_ID'],
                    '--issuer', os.environ['COMPANION_NOTARY_ISSUER_ID']]
    result = run(command, capture_output=True, text=True)
    if json.loads(result.stdout).get('status') != 'Accepted':
        raise ValueError('Apple did not accept notarization; inspect the submission with notarytool log before retrying')


def build(output, unsigned=False):
    version, commit, team = configuration(unsigned)
    if sys.platform != 'darwin':
        raise ValueError('Companion packaging must run on macOS')
    output = output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    manifest = {'schema': 1, 'version': version, 'commit': commit, 'artifacts': {}}
    for architecture in ('arm64', 'amd64'):
        binaries = output / architecture
        binaries.mkdir()
        environment = dict(os.environ, GOOS='darwin', GOARCH=architecture, CGO_ENABLED='0')
        # Unsigned fixtures intentionally identify as dev and cannot self-update.
        embedded_version = 'dev' if unsigned else version
        linker_flags = f'-X trill/sidecar/buildinfo.Version={embedded_version} -X trill/sidecar/buildinfo.Commit={commit}'
        for binary, package, identifier in (('trill', './cmd/trillctl', 'io.bcook.trill.cli'),
                                             ('trilld', './cmd/trilld', 'io.bcook.trill.daemon')):
            path = binaries / binary
            run(['go', 'build', '-trimpath', '-ldflags', linker_flags, '-o', str(path), package], cwd=ROOT / 'sidecar', env=environment)
            if not unsigned:
                run(['codesign', '--force', '--timestamp', '--options', 'runtime', '--identifier', identifier,
                     '--sign', os.environ['COMPANION_SIGNING_IDENTITY'], str(path)])
                requirement = (f'anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists '
                               f'and certificate leaf[field.1.2.840.113635.100.6.1.13] exists '
                               f'and certificate leaf[subject.OU] = "{team}" and identifier "{identifier}"')
                run(['codesign', '--verify', '--strict', '--all-architectures', '--test-requirement', requirement, str(path)])
        shutil.copy2(binaries / 'trill', binaries / 'trillctl')
        name = f'trill-companion-{version}-darwin-{architecture}.zip'
        archive = output / name
        with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as zipped:
            for binary in ('trill', 'trillctl', 'trilld'):
                zipped.write(binaries / binary, binary)
        if not unsigned:
            notarize(archive)
            for binary in ('trill', 'trillctl', 'trilld'):
                for attempt in range(10):
                    result = subprocess.run(['codesign', '--verify', '--check-notarization', str(binaries / binary)], capture_output=True)
                    if result.returncode == 0:
                        break
                    if attempt == 9:
                        raise ValueError('Accepted notarization ticket is not yet verifiable; no manifest will be published')
                    time.sleep(2)
        manifest['artifacts'][architecture] = {'name': name, 'sha256': hashlib.sha256(archive.read_bytes()).hexdigest()}
    if unsigned:
        (output / 'UNSIGNED-NOT-FOR-DISTRIBUTION.json').write_text(json.dumps(manifest, indent=2) + '\n')
    else:
        # Publish metadata only when every artifact has passed notarization.
        (output / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        installer = (ROOT / 'scripts/install-companion.sh').read_text().replace('UNCONFIGURED_SIGNING_TEAM', team)
        (output / 'install-companion.sh').write_text(installer)
    print(f'Built {"UNSIGNED development fixtures" if unsigned else "signed and notarized release"}: {output}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True, help='new output directory (must not exist)')
    parser.add_argument('--unsigned', action='store_true', help='local development fixtures only; no publishable manifest or installer')
    arguments = parser.parse_args()
    try:
        build(arguments.output, arguments.unsigned)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        # Do not echo subprocess arguments or Apple response bodies containing account details.
        print('Companion release failed: ' + (str(error) if not isinstance(error, subprocess.CalledProcessError) else 'a build/signing/notarization command failed'), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
