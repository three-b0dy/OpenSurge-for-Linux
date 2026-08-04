#!/usr/bin/env bash
set -euo pipefail

die() {
	printf 'prepare-linux-release-deps: %s\n' "$*" >&2
	exit 1
}

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
arch=${1:-}
case "$arch" in
	amd64|arm64) ;;
	*) die "architecture must be amd64 or arm64" ;;
esac

version=${OPENSURGE_MIHOMO_VERSION:-v1.19.29}
case "$arch" in
	amd64) asset="mihomo-linux-amd64-${version}.gz" ;;
	arm64) asset="mihomo-linux-arm64-${version}.gz" ;;
esac

checksums=${OPENSURGE_RELEASE_DEPS_CHECKSUMS:-$repo_root/packaging/debian/mihomo-checksums.txt}
test -r "$checksums" || die "checksum manifest is not readable: $checksums"
expected_sha=$(awk -v version="$version" -v arch="$arch" -v asset="$asset" \
	'$1 == version && $2 == arch && $3 == asset { print $4; exit }' "$checksums")
if [[ ! "$expected_sha" =~ ^[[:xdigit:]]{64}$ ]]; then
	die "no valid checksum for $version $arch $asset"
fi

test_url=${OPENSURGE_RELEASE_DEPS_TEST_URL:-}
if [[ -n "$test_url" ]]; then
	url=$test_url
else
	url="https://github.com/MetaCubeX/mihomo/releases/download/${version}/${asset}"
fi
if [[ -z "$test_url" && "$url" != https://* ]]; then
	die "release downloads must use HTTPS"
fi

output_root=${OPENSURGE_RELEASE_DEPS_OUTPUT_ROOT:-$repo_root/runtime/release-tools}
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/opensurge-mihomo.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT
archive="$tmp_dir/$asset"

if [[ -n "$test_url" ]]; then
	curl --fail --silent --show-error --location "$url" --output "$archive"
else
	curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' "$url" --output "$archive"
fi
actual_sha=$(sha256sum "$archive" | awk '{print $1}')
[[ "$actual_sha" == "$expected_sha" ]] || die "checksum mismatch for $asset"

payload="$tmp_dir/payload"
gzip --decompress --stdout "$archive" >"$payload" || die "invalid gzip archive: $asset"
candidate="$payload"
if tar --list --file "$payload" >/dev/null 2>&1; then
	unpack_dir="$tmp_dir/unpack"
	mkdir "$unpack_dir"
	while IFS= read -r member; do
		case "$member" in
			/*|../*|*/../*|*/..|.) die "archive contains an unsafe path: $member" ;;
		esac
	done < <(tar --list --file "$payload")
	tar --extract --no-same-owner --file "$payload" --directory "$unpack_dir"
	if find "$unpack_dir" -type l -print -quit | grep -q .; then
		die "archive contains a symlink"
	fi
	mapfile -t executables < <(find "$unpack_dir" -type f -perm -u+x -print)
	if (( ${#executables[@]} != 1 )); then
		die "archive contains ${#executables[@]} executable files; expected one"
	fi
	candidate=${executables[0]}
fi

test -s "$candidate" || die "archive did not contain a mihomo binary"
destination="$output_root/$arch"
mkdir -p "$destination"
install -m 0755 "$candidate" "$destination/mihomo"
printf 'staged mihomo %s for %s (%s)\n' "$version" "$arch" "$actual_sha"
