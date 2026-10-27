#!/bin/sh
# Start only a fresh, owned Herdr lab; use demo-lab.py stop with the same directory.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 "$ROOT/scripts/demo-lab.py" start "$@"
