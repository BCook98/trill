#!/bin/sh
# Build only; does not start services or install into a user's PATH.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUTPUT=${TRILL_DEMO_OUTPUT:-"$ROOT/.build/demo/trill"}
case "$OUTPUT" in /*) ;; *) echo 'TRILL_DEMO_OUTPUT must be absolute' >&2; exit 1;; esac
mkdir -p "$(dirname -- "$OUTPUT")"
cd "$ROOT/sidecar"
go build -trimpath -o "$OUTPUT" ./cmd/trillctl
printf 'Built %s\n' "$OUTPUT"
printf 'Runtime prerequisite: mosh-server. No services were started.\n'
