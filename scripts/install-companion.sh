#!/bin/bash
# The release builder embeds the public signing team in this standalone script.
# Download this script from an official companion release and inspect it first.
set -euo pipefail
expected_signing_team="UNCONFIGURED_SIGNING_TEAM"
if [[ ! "$expected_signing_team" =~ ^[A-Z0-9]{10}$ ]]; then
  echo 'Signed companion distribution is not configured. Use scripts/setup-companion.sh for source installation.' >&2
  exit 1
fi
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'The Trill companion requires macOS.' >&2; exit 1
fi
if [[ $# != 1 || ! "$1" =~ ^(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})$ ]]; then
  echo 'Usage: bash install-companion.sh MAJOR.MINOR.PATCH' >&2; exit 1
fi
release_version="$1"
case "$(uname -m)" in
  arm64) architecture=arm64 ;;
  x86_64) architecture=amd64 ;;
  *) echo 'Unsupported Mac architecture.' >&2; exit 1 ;;
esac
umask 077
staging_directory="$(mktemp -d)"
trap 'rm -rf -- "$staging_directory"' EXIT
archive_url="https://github.com/BCook98/trill/releases/download/companion-v${release_version}/trill-companion-${release_version}-darwin-${architecture}.zip"
/usr/bin/curl --fail --location --proto '=https' --proto-redir '=https' --max-time 120 --max-filesize 209715200 "$archive_url" -o "$staging_directory/release.zip"
# Extract only the named CLI to a fixed private path; never follow archive paths.
/usr/bin/unzip -l "$staging_directory/release.zip" trill | /usr/bin/awk '
  $4 == "trill" && $1 ~ /^[0-9]+$/ { count++; if ($1 > 67108864) oversized = 1 }
  END { if (count != 1 || oversized) exit 1 }
'
(
  ulimit -f 65536
  /usr/bin/unzip -P '' -p "$staging_directory/release.zip" trill > "$staging_directory/trill"
)
requirement="anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = \"$expected_signing_team\" and identifier \"io.bcook.trill.cli\""
/usr/bin/codesign --verify --strict --all-architectures --test-requirement "$requirement" "$staging_directory/trill"
/usr/bin/codesign --verify --check-notarization "$staging_directory/trill"
chmod 700 "$staging_directory/trill"
"$staging_directory/trill" update --install --version "$release_version"
printf '\nInstalled. Add ~/.local/bin to PATH if needed, then run:\n  trill setup\n  trill doctor\n'
