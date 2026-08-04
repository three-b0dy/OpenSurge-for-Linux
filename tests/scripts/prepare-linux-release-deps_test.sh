#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/opensurge-release-deps.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT

fixture="$work_dir/mihomo-fixture"
archive="$work_dir/mihomo-linux-amd64-v1.19.29.gz"
checksums="$work_dir/mihomo-checksums.txt"
staging="$work_dir/staging"

cat >"$fixture" <<'EOF'
#!/bin/sh
echo fixture-mihomo
EOF
chmod 0755 "$fixture"
gzip -c "$fixture" >"$archive"
archive_sha=$(sha256sum "$archive" | awk '{print $1}')

cat >"$checksums" <<EOF
v1.19.29 amd64 mihomo-linux-amd64-v1.19.29.gz $archive_sha
EOF

OPENSURGE_RELEASE_DEPS_CHECKSUMS="$checksums" \
OPENSURGE_RELEASE_DEPS_OUTPUT_ROOT="$staging/runtime/release-tools" \
OPENSURGE_RELEASE_DEPS_TEST_URL="file://$archive" \
bash "$repo_root/scripts/prepare-linux-release-deps.sh" amd64

binary="$staging/runtime/release-tools/amd64/mihomo"
test -x "$binary"
mode=$(stat -c '%a' "$binary" 2>/dev/null || stat -f '%Lp' "$binary")
test "$mode" = "755"
test "$(sha256sum "$binary" | awk '{print $1}')" = "$(sha256sum "$fixture" | awk '{print $1}')"

if OPENSURGE_RELEASE_DEPS_TEST_URL="file://$archive" \
  OPENSURGE_RELEASE_DEPS_CHECKSUMS="$checksums" \
  OPENSURGE_RELEASE_DEPS_OUTPUT_ROOT="$staging/runtime/release-tools" \
  bash "$repo_root/scripts/prepare-linux-release-deps.sh" riscv64; then
  echo "unsupported architecture was accepted" >&2
  exit 1
fi

echo "release dependency fixture passed"
