#!/usr/bin/env python3
"""Inspect or prepare Trill release versions; never upload, tag, push or sign."""
import argparse
from datetime import date
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
VERSION = ROOT / 'ios/Version.xcconfig'


def validate_version(value):
    if not re.fullmatch(r'[0-9]{1,4}\.[0-9]{1,2}\.[0-9]{1,2}', value):
        raise ValueError('Use three numeric components, e.g. 0.1.0 or 2026.10.25')
    return value


def read_version(path=VERSION):
    text = path.read_text()
    versions = re.findall(r'^MARKETING_VERSION\s*=\s*(\S+)\s*$', text, re.M)
    builds = re.findall(r'^CURRENT_PROJECT_VERSION\s*=\s*(\S+)\s*$', text, re.M)
    if len(versions) != 1 or len(builds) != 1 or not re.fullmatch(r'[1-9][0-9]*', builds[0]):
        raise ValueError('Version.xcconfig needs one marketing version and one positive integer build')
    return validate_version(versions[0]), int(builds[0])


def check_tag(tag, version):
    if not re.fullmatch(r'v' + re.escape(version) + r'-build\.[0-9a-f]{12}', tag):
        raise ValueError('Release tag must match the configured version and vVERSION-build.COMMIT12 format')


def set_version(value, path=VERSION):
    if not re.fullmatch(r'[0-9]{4}\.[0-9]{2}\.[0-9]{2}', value):
        raise ValueError('New release trains use zero-padded YYYY.MM.DD, e.g. 2026.10.25')
    try:
        date(*map(int, value.split('.')))
    except ValueError as error:
        raise ValueError('New release trains need a valid calendar date') from error
    current, _ = read_version(path)
    if tuple(map(int, value.split('.'))) <= tuple(map(int, current.split('.'))):
        raise ValueError('A new marketing version must increase; minor TestFlight updates keep it unchanged')
    text = path.read_text()
    path.write_text(re.sub(r'^MARKETING_VERSION\s*=\s*\S+\s*$', 'MARKETING_VERSION = ' + value, text, flags=re.M))


def git(*args):
    return subprocess.check_output(['git', '-C', str(ROOT), *args], text=True).strip()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    check = sub.add_parser('check', help='Validate version configuration and an optional Cloud release tag')
    check.add_argument('--tag')
    sub.add_parser('plan', help='Print the current version, commit and proposed immutable tag without mutation')
    version = sub.add_parser('set-version', help='Explicitly start a new marketing version; use only for a new release train')
    version.add_argument('version')
    args = parser.parse_args(argv)
    try:
        if args.command == 'set-version':
            set_version(args.version)
        marketing, build = read_version()
        result = {'marketing_version': marketing, 'local_build_number': build,
                  'archive_build_number': 'Assigned by Xcode Cloud; confirm in App Store Connect'}
        if args.command == 'check' and args.tag:
            check_tag(args.tag, marketing)
            # A tag's suffix identifies the actual source being archived, not its Cloud build number.
            if args.tag.rsplit('.', 1)[1] != git('rev-parse', 'HEAD')[:12]:
                raise ValueError('Release tag suffix does not match the checked-out commit')
        if args.command == 'plan':
            commit = git('rev-parse', 'HEAD')
            result.update(commit=commit, dirty=bool(git('status', '--porcelain')),
                          tag='v' + marketing + '-build.' + commit[:12])
        print(json.dumps(result, indent=2))
        return 0
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print('Release check failed: ' + str(error), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
