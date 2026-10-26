#!/bin/sh
# Xcode Cloud discovers this hook beside the selected Xcode project.
set -eu
ROOT=${CI_PRIMARY_REPOSITORY_PATH:-$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)}
if [ -n "${CI_TAG:-}" ]; then
  python3 "$ROOT/scripts/release.py" check --tag "$CI_TAG"
else
  python3 "$ROOT/scripts/release.py" check
fi
