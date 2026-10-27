#!/bin/bash
# Reuse main's validation for this source; never sign after missing/failed CI.
set -euo pipefail
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
release_commit="$(git rev-parse HEAD)"
check_run_id=""
# The tag may arrive just before GitHub creates the main push run.
for _attempt in {1..12}; do
  check_run_id="$(gh run list --repo "$GITHUB_REPOSITORY" --workflow checks.yml --commit "$release_commit" --branch main --event push --json databaseId --jq '.[0].databaseId // empty')"
  [[ -z "$check_run_id" ]] || break
  sleep 5
done
[[ -n "$check_run_id" ]] || { echo 'No main Checks run exists for this release commit.' >&2; exit 1; }
gh run watch "$check_run_id" --repo "$GITHUB_REPOSITORY" --exit-status --interval 15 --compact
check_conclusion="$(gh run view "$check_run_id" --repo "$GITHUB_REPOSITORY" --json conclusion --jq .conclusion)"
[[ "$check_conclusion" == success ]] || { echo "Main checks did not succeed: $check_conclusion" >&2; exit 1; }
