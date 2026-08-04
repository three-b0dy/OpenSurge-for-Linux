#!/usr/bin/env bash
set -euo pipefail

package=${1:-}
if [[ -z "$package" ]]; then
	echo "usage: install-deb.sh /path/to/opensurge_<version>_<arch>.deb" >&2
	exit 2
fi
if [[ ! -f "$package" ]]; then
	echo "package does not exist: $package" >&2
	exit 1
fi
if [[ "${OPENSURGE_PACKAGE_TEST_ALLOW_HOST:-}" != 1 ]]; then
	echo "refusing package lifecycle test; set OPENSURGE_PACKAGE_TEST_ALLOW_HOST=1 only in a disposable root environment" >&2
	exit 2
fi
if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
	echo "package lifecycle test requires root after explicit opt-in" >&2
	exit 2
fi
command -v dpkg >/dev/null 2>&1 || { echo "dpkg is required for package lifecycle testing" >&2; exit 2; }
command -v dpkg-deb >/dev/null 2>&1 || { echo "dpkg-deb is required for package lifecycle testing" >&2; exit 2; }

expected_package=opensurge
test "$(dpkg-deb -f "$package" Package)" = "$expected_package"
architecture=$(dpkg-deb -f "$package" Architecture)
case "$architecture" in
	amd64|arm64) ;;
	*) echo "unexpected package architecture: $architecture" >&2; exit 1 ;;
esac
depends=$(dpkg-deb -f "$package" Depends)
for dependency in dnsmasq nftables iproute2 ca-certificates systemd; do
	grep -F "$dependency" <<<"$depends" >/dev/null || {
		echo "missing dependency $dependency in package metadata" >&2
		exit 1
	}
done

dpkg --unpack "$package"
dpkg --configure opensurge
test -x /usr/bin/opensurge
test -x /usr/bin/opensurge-setup
test -x /usr/lib/opensurge/opensurge-control
test -x /usr/lib/opensurge/opensurge-gateway
test -x /usr/lib/opensurge/opensurge-setup
test -x /usr/lib/opensurge/mihomo
test -f /lib/systemd/system/opensurge-control.service
test -f /lib/systemd/system/opensurge-gateway.service
test -f /lib/systemd/system/opensurge-gateway.socket
test -d /etc/opensurge
test -d /var/lib/opensurge
test ! -e /var/lib/opensurge/admin.json
systemctl is-enabled opensurge-control.service >/dev/null 2>&1 && {
	echo "control service was enabled before setup" >&2
	exit 1
}
systemctl is-active opensurge-control.service >/dev/null 2>&1 && {
	echo "control service was started during package install" >&2
	exit 1
}

dpkg -i "$package"
dpkg -r opensurge
test -d /etc/opensurge
test ! -e /usr/bin/opensurge

dpkg -i "$package"
test -d /etc/opensurge
dpkg --purge opensurge
test ! -e /var/lib/opensurge/admin.json
test ! -e /var/lib/opensurge
test ! -e /etc/opensurge/tls/key.pem

echo "package lifecycle assertions passed for $architecture"
