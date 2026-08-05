#!/usr/bin/env bash
set -euo pipefail

die() {
	printf 'build-deb: %s\n' "$*" >&2
	exit 1
}

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
arch=${ARCH:-${1:-}}
version=${VERSION:-${2:-}}
version=${version#v}
case "$arch" in
	amd64|arm64) ;;
	*) die "ARCH must be amd64 or arm64" ;;
esac
[[ "$version" =~ ^[0-9][0-9A-Za-z.+:~-]*$ ]] || die "VERSION must be a Debian-compatible version"
command -v dpkg-deb >/dev/null 2>&1 || die "dpkg-deb is required to build a Debian package"
command -v go >/dev/null 2>&1 || die "Go is required to build a Debian package"
command -v pnpm >/dev/null 2>&1 || die "pnpm is required to build a Debian package"

mihomo_bin=${OPENSURGE_MIHOMO_BIN:-$repo_root/runtime/release-tools/$arch/mihomo}
if [[ ! -x "$mihomo_bin" ]]; then
	bash "$repo_root/scripts/prepare-linux-release-deps.sh" "$arch"
fi
[[ -x "$mihomo_bin" ]] || die "staged mihomo binary is missing: $mihomo_bin"

source_epoch=${SOURCE_DATE_EPOCH:-}
if [[ -z "$source_epoch" ]]; then
	source_epoch=$(git -C "$repo_root" show -s --format=%ct HEAD 2>/dev/null || true)
fi
source_epoch=${source_epoch:-0}
[[ "$source_epoch" =~ ^[0-9]+$ ]] || die "SOURCE_DATE_EPOCH must be an integer"

(cd "$repo_root" && pnpm --dir web run build)

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/opensurge-deb-build.XXXXXX")
trap 'rm -rf "$build_dir"' EXIT
pkg_root="$build_dir/root"
mkdir -p "$pkg_root"

build_binary() {
	local name=$1
	local package_path=$2
	CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
		go build -trimpath -o "$build_dir/$name" "./$package_path"
}

build_binary opensurge cmd/opensurge
build_binary opensurge-control cmd/opensurge-control
build_binary opensurge-gateway cmd/opensurge-gateway
build_binary opensurge-setup cmd/opensurge-setup

install -d -m 0755 "$pkg_root/usr/bin" "$pkg_root/usr/lib/opensurge"
install -m 0755 "$build_dir/opensurge" "$pkg_root/usr/bin/opensurge"
ln -s ../lib/opensurge/opensurge-setup "$pkg_root/usr/bin/opensurge-setup"
install -m 0755 "$build_dir/opensurge-control" "$pkg_root/usr/lib/opensurge/opensurge-control"
install -m 0755 "$build_dir/opensurge-gateway" "$pkg_root/usr/lib/opensurge/opensurge-gateway"
install -m 0755 "$build_dir/opensurge-setup" "$pkg_root/usr/lib/opensurge/opensurge-setup"
install -m 0755 "$mihomo_bin" "$pkg_root/usr/lib/opensurge/mihomo"

install -d -m 0755 "$pkg_root/usr/lib/systemd/system/opensurge-control.service.d"
install -m 0644 "$repo_root/packaging/systemd/opensurge-control.service" "$pkg_root/usr/lib/systemd/system/opensurge-control.service"
install -m 0644 "$repo_root/packaging/systemd/opensurge-gateway.service" "$pkg_root/usr/lib/systemd/system/opensurge-gateway.service"
install -m 0644 "$repo_root/packaging/systemd/opensurge-gateway.socket" "$pkg_root/usr/lib/systemd/system/opensurge-gateway.socket"
install -m 0644 "$repo_root/packaging/systemd/opensurge-control.service.d/security.conf" "$pkg_root/usr/lib/systemd/system/opensurge-control.service.d/security.conf"

install -d -m 0755 "$pkg_root/etc/opensurge" "$pkg_root/usr/share/doc/opensurge"
install -m 0644 "$repo_root/examples/config.example.yaml" "$pkg_root/usr/share/doc/opensurge/config.example.yaml"
sed \
	-e 's|^  binary: "./bin/mihomo"$|  binary: "/usr/lib/opensurge/mihomo"|' \
	-e 's|^  config: "./runtime/mihomo.yaml"$|  config: "/run/opensurge/mihomo.yaml"|' \
	-e 's|^  dir: "./runtime"$|  dir: "/var/lib/opensurge/runtime"|' \
	"$repo_root/examples/config.example.yaml" >"$pkg_root/etc/opensurge/config.yaml"
chmod 0640 "$pkg_root/etc/opensurge/config.yaml"

install -d -m 0755 "$pkg_root/DEBIAN"
sed -e "s/__VERSION__/$version/g" -e "s/__ARCH__/$arch/g" \
	"$repo_root/packaging/debian/DEBIAN/control" >"$pkg_root/DEBIAN/control"
for script in preinst postinst prerm postrm; do
	install -m 0755 "$repo_root/packaging/debian/DEBIAN/$script" "$pkg_root/DEBIAN/$script"
done
install -m 0644 "$repo_root/packaging/debian/DEBIAN/conffiles" "$pkg_root/DEBIAN/conffiles"

find "$pkg_root" -exec touch -h -d "@$source_epoch" {} +
output_dir=${OPENSURGE_DEB_OUTPUT_DIR:-$repo_root/artifacts/release}
mkdir -p "$output_dir"
artifact="$output_dir/opensurge_${version}_${arch}.deb"
rm -f "$artifact"
dpkg-deb --build --root-owner-group -Zgzip -z9 "$pkg_root" "$artifact" >/dev/null
printf 'built %s\n' "$artifact"
