#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
scan_paths=("$repo_root/.github" "$repo_root/Makefile" "$repo_root/scripts" "$repo_root/packaging")

if rg -n --glob '!check-linux-repository.sh' \
  'runs-on: macos|GOOS=darwin|notarize|xcode' "${scan_paths[@]}"; then
	echo "macOS release/build automation remains in active release paths" >&2
	exit 1
fi

# The upstream mirror deliberately names its external source repository
# OpenSurge-for-Mac. It is a Linux-hosted ref mirror, not a release/build path.
if rg -n --glob '!check-linux-repository.sh' --glob '!sync-upstream.yml' \
  'OpenSurge-for-Mac' "${scan_paths[@]}"; then
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
grep -F 'opensurge_${{ steps.version.outputs.version }}_${{ matrix.arch }}.deb' "$release" >/dev/null
grep -F 'opensurge_*.deb' "$release" >/dev/null
grep -F 'SHA256SUMS' "$release" >/dev/null
grep -F 'cp scripts/opensurge-install release-assets/opensurge-install' "$release" >/dev/null
grep -F 'chmod 0755 release-assets/opensurge-install' "$release" >/dev/null
grep -F 'sha256sum opensurge-install opensurge_*.deb | LC_ALL=C sort -k2,2 > SHA256SUMS' "$release" >/dev/null
grep -F 'release-assets/opensurge-install release-assets/opensurge_*.deb release-assets/SHA256SUMS' "$release" >/dev/null
grep -F 'apt-get install -y --no-install-recommends debootstrap' "$release" >/dev/null
grep -F 'debootstrap --variant=minbase --include=adduser noble "$test_root" "$mirror"' "$release" >/dev/null
grep -F 'chroot "$test_root" /bin/bash -euc' "$release" >/dev/null
grep -F 'OPENSURGE_PACKAGE_TEST_ALLOW_HOST=1 /workspace/tests/packages/install-deb.sh' "$release" >/dev/null
release_apt_install_lines=$(grep -E '^[[:space:]]*apt-get install ' "$release" || true)
if test "$release_apt_install_lines" != '              apt-get install -y --no-install-recommends debootstrap'; then
	echo "release package test must install only the debootstrap prerequisite in the outer container" >&2
	exit 1
fi
test ! -e "$repo_root/.github/workflows/release-unsigned.yml"

control="$repo_root/packaging/debian/DEBIAN/control"
preinst="$repo_root/packaging/debian/DEBIAN/preinst"
postinst="$repo_root/packaging/debian/DEBIAN/postinst"
prerm="$repo_root/packaging/debian/DEBIAN/prerm"
postrm="$repo_root/packaging/debian/DEBIAN/postrm"
build_deb="$repo_root/packaging/debian/build-deb.sh"
package_test="$repo_root/tests/packages/install-deb.sh"
grep -F 'Architecture: __ARCH__' "$control" >/dev/null
if grep -E '^Depends:.*(^|[,[:space:]])(dnsmasq|nftables|iproute2|ca-certificates|systemd)([[:space:],]|$)' "$control"; then
	echo "OpenSurge package must not declare installer-owned networking runtime dependencies" >&2
	exit 1
fi
test -x "$preinst"
grep -F 'OPENSURGE_INSTALLER_MARKER' "$preinst" >/dev/null
grep -F '/run/opensurge/installer' "$preinst" >/dev/null
grep -F 'stat -c' "$preinst" >/dev/null
grep -F 'opensurge-install' "$preinst" >/dev/null
if grep -Eq '(^|[[:space:]])(rm|mv|cp|mkdir|install|systemctl|service)([[:space:]]|$)' "$preinst"; then
	echo "preinst guard must remain side-effect-free" >&2
	exit 1
fi
grep -F 'for script in preinst postinst prerm postrm' "$build_deb" >/dev/null
grep -F 'OPENSURGE_PACKAGE_TEST_ALLOW_HOST' "$package_test" >/dev/null
grep -F 'installer_fixture=${2:-}' "$package_test" >/dev/null
grep -F -- '--installer-fixture' "$package_test" >/dev/null
grep -F 'OPENSURGE_INSTALLER_TEST=1' "$package_test" >/dev/null
if grep -Eq 'systemctl (start|enable)([[:space:]]|$)' "$postinst"; then
	echo "postinst must not start or enable a service" >&2
	exit 1
fi
grep -F 'systemctl stop' "$prerm" >/dev/null
grep -F 'remove|purge)' "$postrm" >/dev/null
grep -F 'load_install_manifest' "$postrm" >/dev/null
grep -F 'resolver_was_altered' "$postrm" >/dev/null
grep -F 'dnsmasq_was_altered' "$postrm" >/dev/null
grep -F 'rm -rf /var/lib/opensurge' "$postrm" >/dev/null

echo "Linux repository checks passed"
