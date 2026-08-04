#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="$repo_root/.github/workflows/sync-upstream.yml"

test -f "$workflow"
grep -F '17 3 * * *' "$workflow" >/dev/null
grep -F 'workflow_dispatch:' "$workflow" >/dev/null
grep -F 'contents: write' "$workflow" >/dev/null
grep -F 'group: upstream-mirror' "$workflow" >/dev/null
grep -F 'cancel-in-progress: false' "$workflow" >/dev/null
grep -F 'ref: upstream' "$workflow" >/dev/null
grep -F 'github.com/YTwsy/OpenSurge-for-Mac.git' "$workflow" >/dev/null
grep -F 'refs/heads/upstream' "$workflow" >/dev/null
grep -F 'refs/remotes/upstream-source/master' "$workflow" >/dev/null
grep -F 'force-with-lease=upstream:' "$workflow" >/dev/null

! grep -Fq 'origin refs/remotes/upstream-source/master:refs/heads/master' "$workflow"
! grep -Fq 'refs/remotes/upstream-source/master:refs/heads/master' "$workflow"
! grep -Eq '(^|[[:space:];])git (checkout|merge|rebase)([[:space:]]|$)' "$workflow"
! grep -Fq 'gh release' "$workflow"
! grep -Fq 'softprops/action-gh-release' "$workflow"

echo "sync-upstream workflow contract passed"
