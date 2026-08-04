#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
scan_paths=("$repo_root/.github" "$repo_root/Makefile" "$repo_root/scripts" "$repo_root/packaging")

if rg -n --glob '!check-linux-repository.sh' 'runs-on: macos|GOOS=darwin|OpenSurge-for-Mac|notarize|xcode' "${scan_paths[@]}"; then
	echo "macOS release/build automation remains in active release paths" >&2
	exit 1
fi

ci="$repo_root/.github/workflows/ci.yml"
release="$repo_root/.github/workflows/release-linux.yml"
test -f "$ci"
test -f "$release"
grep -Fx '    runs-on: ubuntu-24.04' "$ci"
grep -F 'make test' "$ci" >/dev/null
grep -F 'make web-test' "$ci" >/dev/null
grep -F 'make linux-ci-check' "$ci" >/dev/null
grep -F "'v*.*.*'" "$release" >/dev/null
grep -F 'amd64' "$release" >/dev/null
grep -F 'arm64' "$release" >/dev/null
grep -F 'make linux-release-deps' "$release" >/dev/null
grep -F 'make deb' "$release" >/dev/null
grep -F 'contents: write' "$release" >/dev/null
test ! -e "$repo_root/.github/workflows/release-unsigned.yml"

control="$repo_root/packaging/debian/DEBIAN/control"
postinst="$repo_root/packaging/debian/DEBIAN/postinst"
prerm="$repo_root/packaging/debian/DEBIAN/prerm"
postrm="$repo_root/packaging/debian/DEBIAN/postrm"
grep -F 'Architecture: __ARCH__' "$control" >/dev/null
grep -F 'dnsmasq, nftables, iproute2, ca-certificates, systemd' "$control" >/dev/null
if grep -Eq 'systemctl (start|enable --now)' "$postinst"; then
	echo "postinst must not start or immediately enable a service" >&2
	exit 1
fi
grep -F 'systemctl stop' "$prerm" >/dev/null
grep -F 'rm -rf /var/lib/opensurge' "$postrm" >/dev/null

echo "Linux repository checks passed"
