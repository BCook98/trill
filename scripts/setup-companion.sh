#!/bin/bash
# Build the public CLI and private compatibility tools, then run guided setup.
set -euo pipefail
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'Companion setup requires macOS.' >&2
  exit 1
fi
command -v go >/dev/null || { echo 'Install Go 1.26.10 or a newer patched release, then retry.' >&2; exit 1; }
repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
bin_dir="${TRILL_BIN_DIR:-$HOME/.local/share/trill/bin}"
case "$bin_dir" in /*) ;; *) echo 'TRILL_BIN_DIR must be absolute.' >&2; exit 1;; esac
if [[ -L "$bin_dir" || -L "$bin_dir/.trill-companion-bin" ]]; then
  echo 'Refusing a symlinked companion installation.' >&2; exit 1
fi
if [[ -e "$bin_dir" && ! -f "$bin_dir/.trill-companion-bin" ]]; then
  echo 'Binary directory already exists without a Trill ownership marker. Choose TRILL_BIN_DIR.' >&2; exit 1
fi
if [[ -f "$bin_dir/.trill-companion-bin" ]] && [[ "$(cat -- "$bin_dir/.trill-companion-bin")" != trill-companion-v1 ]]; then
  echo 'Binary directory ownership marker does not match Trill. Choose TRILL_BIN_DIR.' >&2; exit 1
fi
public_dir="$HOME/.local/bin"
public_link="$public_dir/trill"
# Only our exact managed link is reusable, including when its target is missing.
if [[ -L "$public_link" ]]; then
  if [[ "$(readlink -- "$public_link")" != "$bin_dir/trill" ]]; then
    echo "Refusing to overwrite foreign link $public_link." >&2; exit 1
  fi
elif [[ -e "$public_link" ]]; then
  echo "Refusing to overwrite $public_link." >&2; exit 1
fi
umask 077
mkdir -p -- "$bin_dir"
chmod 700 "$bin_dir"
if [[ ! -f "$bin_dir/.trill-companion-bin" ]]; then
  printf '%s\n' 'trill-companion-v1' > "$bin_dir/.trill-companion-bin"
fi
for binary in trill trillctl trilld; do
  if [[ -L "$bin_dir/$binary" || ( -e "$bin_dir/$binary" && ! -f "$bin_dir/$binary" ) ]]; then
    echo "Refusing to overwrite $bin_dir/$binary." >&2; exit 1
  fi
done
build_dir="$(mktemp -d "$bin_dir/.build.XXXXXX")"
trap 'rm -rf -- "$build_dir"' EXIT
cd -- "$repo_dir/sidecar"
go build -trimpath -o "$build_dir/trilld" ./cmd/trilld
go build -trimpath -o "$build_dir/trill" ./cmd/trillctl
# Keep the old path for configurations and LaunchAgents already using trillctl.
cp -- "$build_dir/trill" "$build_dir/trillctl"
# All required binaries must be ready before publishing with atomic renames.
chmod 700 "$build_dir/trill" "$build_dir/trillctl" "$build_dir/trilld"
mv -f -- "$build_dir/trilld" "$bin_dir/trilld"
mv -f -- "$build_dir/trillctl" "$bin_dir/trillctl"
mv -f -- "$build_dir/trill" "$bin_dir/trill"
mkdir -p -- "$public_dir"
if [[ ! -L "$public_link" ]]; then
  # ln without -f also refuses a destination created since the check above.
  ln -s -- "$bin_dir/trill" "$public_link"
fi
printf 'Installed trill: %s\n' "$public_link" >&2
case ":$PATH:" in
  *":$public_dir:"*) ;;
  *)
    printf 'Add the public command directory to PATH: export PATH="%s:$PATH"\n' "$public_dir" >&2
    printf 'Until then, run the full path: "%s" status (or another subcommand).\n' "$bin_dir/trill" >&2
    ;;
esac
"$bin_dir/trill" setup "$@"
