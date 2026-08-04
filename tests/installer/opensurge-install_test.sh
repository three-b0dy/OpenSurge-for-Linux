#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
installer="$repo_root/scripts/opensurge-install"
test_root=$(mktemp -d "${TMPDIR:-/tmp}/opensurge-installer-test.XXXXXX")
fake_bin="$test_root/bin"
captured_stdout="$test_root/stdout"
captured_stderr="$test_root/stderr"
captured_commands="$test_root/commands"
installer_log="$test_root/opensurge-install.log"
fake_tty="$test_root/tty"
test_secret='installer-test-secret-must-not-leak'
fixture_root="$test_root/release-fixture"
fixture_deb="$fixture_root/opensurge_1.2.3_amd64.deb"
fixture_checksums="$fixture_root/SHA256SUMS"
fixture_checksums_content=''
fixture_server_port="$test_root/release-server.port"
fixture_server_log="$test_root/release-server.log"
fixture_server_pid=''
release_base_url=''

cleanup() {
	if test -n "$fixture_server_pid"; then
		kill "$fixture_server_pid" 2>/dev/null || true
		wait "$fixture_server_pid" 2>/dev/null || true
	fi
	chmod -R u+w "$test_root" 2>/dev/null || true
	rm -rf "$test_root"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

assert_not_contains() {
	local file=$1
	local unwanted=$2
	if test -f "$file" && grep -F -- "$unwanted" "$file" >/dev/null; then
		fail "unexpected text in $file: $unwanted"
	fi
}

assert_contains() {
	local file=$1
	local expected=$2
	grep -F -- "$expected" "$file" >/dev/null || fail "missing text in $file: $expected"
}

assert_command_not_invoked() {
	local file=$1
	local command_name=$2
	if test -f "$file" && grep -E "^${command_name}([[:space:]]|$)" "$file" >/dev/null; then
		fail "unexpected command in $file: $command_name"
	fi
}

assert_command_count() {
	local expected=$1
	local count
	count=$(grep -F -c -- 'dpkg --print-architecture' "$captured_commands" || true)
	test "$count" -eq "$expected" || fail "expected $expected dpkg architecture queries, got $count"
}

make_fake_command() {
	local command_name=$1
	cat >"$fake_bin/$command_name" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"
exit 0
EOF
	chmod 0755 "$fake_bin/$command_name"
}

make_fake_apt_get() {
	cat >"$fake_bin/apt-get" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"
if test "${1:-}" = update; then
	case "${OPENSURGE_INSTALLER_TEST_POLICY_MUTATION:-}" in
		modified)
			printf '#!/bin/sh\n# modified after installer creation\nexit 0\n' >"$OPENSURGE_INSTALLER_BIN_DIR/policy-rc.d"
			chmod 0755 "$OPENSURGE_INSTALLER_BIN_DIR/policy-rc.d"
			;;
		replaced)
			replacement="$OPENSURGE_INSTALLER_BIN_DIR/policy-rc.d.replacement"
			printf '#!/bin/sh\n# replacement user policy\nexit 0\n' >"$replacement"
			chmod 0755 "$replacement"
			mv "$replacement" "$OPENSURGE_INSTALLER_BIN_DIR/policy-rc.d"
			;;
	esac
fi
exit 0
EOF
	chmod 0755 "$fake_bin/apt-get"
}

make_fake_dpkg() {
	cat >"$fake_bin/dpkg" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"
if test "$#" -eq 1 && test "$1" = --print-architecture; then
	printf '%s\n' "${OPENSURGE_INSTALLER_TEST_DPKG_ARCH:-amd64}"
fi
EOF
	chmod 0755 "$fake_bin/dpkg"
}

make_fake_dpkg_deb() {
	cat >"$fake_bin/dpkg-deb" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"
if test "$#" -eq 3 && test "$1" = -f && test "$3" = Architecture; then
	printf '%s\n' "${OPENSURGE_INSTALLER_TEST_DEB_ARCH:-amd64}"
	fi
EOF
	chmod 0755 "$fake_bin/dpkg-deb"
}

start_release_fixture() {
	mkdir -p "$fixture_root"
	printf 'approved test package\n' >"$fixture_deb"
	fixture_checksums_content="$(sha256sum "$fixture_deb" | awk '{print $1 "  opensurge_1.2.3_amd64.deb"}')"
	printf '%s\n' "$fixture_checksums_content" >"$fixture_checksums"

	python3 - "$fixture_root" >"$fixture_server_port" 2>"$fixture_server_log" <<'PY' &
import http.server
import sys

root = sys.argv[1]
prefix = "/three-b0dy/OpenSurge-for-Linux"

class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=root, **kwargs)

    def do_GET(self):
        if self.path == prefix + "/releases/latest":
            self.send_response(302)
            self.send_header("Location", prefix + "/releases/tag/v1.2.3")
            self.end_headers()
            return
        if self.path == prefix + "/releases/tag/v1.2.3":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"release v1.2.3\n")
            return
        download_prefix = prefix + "/releases/download/v1.2.3/"
        if self.path.startswith(download_prefix):
            self.path = "/" + self.path.removeprefix(download_prefix)
        return super().do_GET()

server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
print(server.server_port, flush=True)
server.serve_forever()
PY
	fixture_server_pid=$!
	for _ in $(seq 1 50); do
		test -s "$fixture_server_port" && break
		sleep 0.05
	done
	test -s "$fixture_server_port" || fail 'release fixture did not start'
	release_base_url="http://127.0.0.1:$(<"$fixture_server_port")/three-b0dy/OpenSurge-for-Linux"
}

restore_checksums() {
	printf '%s\n' "$fixture_checksums_content" >"$fixture_checksums"
	printf 'approved test package\n' >"$fixture_deb"
}

run_installer() {
	local transfer_prerequisites=${OPENSURGE_TEST_TRANSFER_PREREQUISITES:-available}

	OPENSURGE_INSTALLER_TEST=1 \
		OPENSURGE_INSTALLER_ROOT="$test_root/root" \
		OPENSURGE_INSTALLER_BIN_DIR="$fake_bin" \
		OPENSURGE_INSTALLER_LOG="$installer_log" \
		OPENSURGE_INSTALLER_TTY="$fake_tty" \
		OPENSURGE_INSTALLER_COMMANDS="$captured_commands" \
		OPENSURGE_INSTALLER_TEST_RELEASE_BASE_URL="$release_base_url" \
		OPENSURGE_INSTALLER_TEST_TRANSFER_PREREQUISITES="$transfer_prerequisites" \
		OPENSURGE_INSTALLER_TEST_POLICY_MUTATION="${OPENSURGE_TEST_POLICY_MUTATION:-}" \
		OPENSURGE_TEST_SECRET="$test_secret" \
		PATH="$fake_bin:$PATH" \
		bash "$installer" "$@" >"$captured_stdout" 2>"$captured_stderr"
}

expect_fail() {
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if run_installer "$@"; then
		fail "installer accepted invalid invocation: $*"
	fi

	assert_not_contains "$captured_commands" 'apt-get install'
	assert_not_contains "$captured_commands" 'dpkg -i'
	assert_command_not_invoked "$captured_commands" systemctl
	assert_command_not_invoked "$captured_commands" ip
	assert_command_not_invoked "$captured_commands" ss
	assert_not_contains "$captured_stdout" "$test_secret"
	assert_not_contains "$captured_stderr" "$test_secret"
	assert_not_contains "$installer_log" "$test_secret"
}

expect_success() {
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	run_installer "$@" || fail "installer rejected valid invocation: $*"
	assert_not_contains "$captured_commands" 'apt-get install'
	assert_not_contains "$captured_commands" 'dpkg -i'
	assert_not_contains "$captured_stdout" "$test_secret"
	assert_not_contains "$captured_stderr" "$test_secret"
	assert_not_contains "$installer_log" "$test_secret"
}

expect_fail_when_checksum_missing() {
	rm -f "$fixture_checksums"
	expect_fail
	assert_contains "$captured_stderr" 'download failed'
	restore_checksums
}

expect_fail_when_checksum_does_not_match() {
	printf 'forged test package\n' >"$fixture_deb"
	expect_fail --version v1.2.3
	assert_contains "$captured_stderr" 'checksum verification failed'
	restore_checksums
}

expect_fail_when_checksum_entry_is_duplicated() {
	printf '%s\n%s\n' "$fixture_checksums_content" "$fixture_checksums_content" >"$fixture_checksums"
	expect_fail --version v1.2.3
	assert_contains "$captured_stderr" 'exactly one matching package entry'
	restore_checksums
}

expect_fail_when_checksum_filename_has_whitespace() {
	sha256sum "$fixture_deb" | awk '{print $1 "  opensurge_1.2.3 amd64.deb"}' >"$fixture_checksums"
	expect_fail --version v1.2.3
	assert_contains "$captured_stderr" 'invalid filename or checksum line'
	restore_checksums
}

expect_fail_when_deb_name_does_not_match_arch() {
	local wrong_name="$fixture_root/opensurge_1.2.3_arm64.deb"
	local wrong_checksums="$fixture_root/wrong-name-SHA256SUMS"
	cp "$fixture_deb" "$wrong_name"
	sha256sum "$wrong_name" | awk '{print $1 "  opensurge_1.2.3_arm64.deb"}' >"$wrong_checksums"
	expect_fail --deb "$wrong_name" --checksums "$wrong_checksums"
	assert_contains "$captured_stderr" 'package filename does not match architecture'
}

expect_fail_when_dpkg_deb_arch_is_wrong() {
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if OPENSURGE_INSTALLER_TEST_DEB_ARCH=arm64 run_installer --deb "$fixture_deb"; then
		fail 'installer accepted a package with the wrong Debian architecture'
	fi
	assert_contains "$captured_stderr" 'Debian package architecture does not match host architecture'
	assert_not_contains "$captured_commands" 'apt-get install'
	assert_not_contains "$captured_commands" 'dpkg -i'
}

expect_fail_when_offline_checksum_is_missing() {
	local offline_dir="$test_root/offline-without-checksums"
	local offline_deb="$offline_dir/opensurge_1.2.3_amd64.deb"
	mkdir -p "$offline_dir"
	cp "$fixture_deb" "$offline_deb"
	expect_fail --deb "$offline_deb"
	assert_contains "$captured_stderr" 'regular readable file is required'
}

expect_fail_when_online_url_is_not_https() {
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if OPENSURGE_INSTALLER_TEST_RELEASE_BASE_URL='http://127.0.0.1:1/three-b0dy/OpenSurge-for-Linux' \
		PATH="$fake_bin:$PATH" bash "$installer" --version v1.2.3 >"$captured_stdout" 2>"$captured_stderr"; then
		fail 'installer accepted a non-HTTPS online release URL outside test mode'
	fi
	assert_contains "$captured_stderr" 'test path overrides require OPENSURGE_INSTALLER_TEST=1'
}

expect_transfer_bootstrap_is_no_autostart_and_redacted() {
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if ! OPENSURGE_TEST_TRANSFER_PREREQUISITES=missing run_installer --version v1.2.3; then
		fail 'installer could not bootstrap transfer prerequisites in the fixture'
	fi
	assert_contains "$captured_commands" 'apt-get update'
	assert_contains "$captured_commands" 'apt-get install --yes --no-install-recommends ca-certificates curl'
	assert_contains "$installer_log" 'bootstrapping curl and CA certificates before release retrieval'
	test ! -e "$fake_bin/policy-rc.d" || fail 'temporary no-autostart policy was not removed'
	assert_not_contains "$installer_log" "$test_secret"
}

expect_transfer_bootstrap_preserves_modified_temporary_policy() {
	rm -f "$fake_bin/policy-rc.d"
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if ! OPENSURGE_TEST_TRANSFER_PREREQUISITES=missing OPENSURGE_TEST_POLICY_MUTATION=modified run_installer --version v1.2.3; then
		fail 'installer rejected a modified temporary package service policy in the fixture'
	fi
	assert_contains "$fake_bin/policy-rc.d" '# modified after installer creation'
	assert_not_contains "$installer_log" "$test_secret"
}

expect_transfer_bootstrap_preserves_replaced_temporary_policy() {
	rm -f "$fake_bin/policy-rc.d"
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if ! OPENSURGE_TEST_TRANSFER_PREREQUISITES=missing OPENSURGE_TEST_POLICY_MUTATION=replaced run_installer --version v1.2.3; then
		fail 'installer rejected a replacement package service policy in the fixture'
	fi
	assert_contains "$fake_bin/policy-rc.d" '# replacement user policy'
	assert_not_contains "$installer_log" "$test_secret"
}

expect_transfer_bootstrap_preserves_existing_policy() {
	printf '#!/bin/sh\n# user policy\nexit 0\n' >"$fake_bin/policy-rc.d"
	chmod 0755 "$fake_bin/policy-rc.d"
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if ! OPENSURGE_TEST_TRANSFER_PREREQUISITES=missing run_installer --version v1.2.3; then
		fail 'installer rejected an existing package service policy in the fixture'
	fi
	assert_contains "$fake_bin/policy-rc.d" '# user policy'
	assert_contains "$installer_log" 'existing policy-rc.d controls package service policy during transfer bootstrap'
	assert_not_contains "$installer_log" "$test_secret"
}

expect_offline_final_symlink_uses_target_and_adjacent_checksum() {
	local target_dir="$test_root/offline-target"
	local target_deb="$target_dir/opensurge_1.2.3_amd64.deb"
	local canonical_target_deb
	local link_dir="$test_root/offline-links"
	local linked_deb="$link_dir/opensurge_1.2.3_amd64.deb"

	mkdir -p "$target_dir" "$link_dir"
	cp "$fixture_deb" "$target_deb"
	sha256sum "$target_deb" | awk '{print $1 "  opensurge_1.2.3_amd64.deb"}' >"$target_dir/SHA256SUMS"
	ln -s '../offline-target/opensurge_1.2.3_amd64.deb' "$linked_deb"
	canonical_target_deb="$(cd -P "$target_dir" && pwd)/opensurge_1.2.3_amd64.deb"
	expect_success --deb "$linked_deb"
	assert_contains "$captured_commands" "$canonical_target_deb"
	assert_not_contains "$captured_commands" "$linked_deb"
}

mkdir -p "$fake_bin"
: >"$captured_commands"
: >"$fake_tty"
chmod 0600 "$fake_tty"
for command_name in systemctl ip ss; do
	make_fake_command "$command_name"
done
make_fake_apt_get
make_fake_dpkg
make_fake_dpkg_deb
start_release_fixture

# These cases catch a parser that begins host work before rejecting an invalid
# source or incomplete topology contract.
expect_fail --deb /tmp/a.deb --version v1.2.3
assert_contains "$captured_stderr" 'choose only one package source'

expect_fail --mode isolated_lan --downstream-interface lan.20
assert_contains "$captured_stderr" 'requires --downstream-interface, --upstream-interface, --lan-ip, and --lan-cidr'

expect_fail --mode isolated_lan --downstream-interface lan.20 --upstream-interface eth0 --lan-ip 192.168.50.1
assert_contains "$captured_stderr" 'requires --downstream-interface, --upstream-interface, --lan-ip, and --lan-cidr'

expect_fail --mode unsupported
assert_contains "$captured_stderr" 'unsupported mode: unsupported'

expect_fail --version 'not a tag'
assert_contains "$captured_stderr" 'invalid release tag'

# A latest-release redirect produces the exact release filename, calls
# dpkg-deb for its Architecture field, and checks the matching SHA256SUMS line.
expect_success
assert_contains "$fixture_server_log" 'GET /three-b0dy/OpenSurge-for-Linux/releases/latest'
assert_contains "$fixture_server_log" 'GET /three-b0dy/OpenSurge-for-Linux/releases/download/v1.2.3/SHA256SUMS'
assert_contains "$fixture_server_log" 'GET /three-b0dy/OpenSurge-for-Linux/releases/download/v1.2.3/opensurge_1.2.3_amd64.deb'
assert_contains "$captured_commands" 'dpkg-deb -f'
assert_command_count 1
assert_contains "$installer_log" 'verified release asset opensurge_1.2.3_amd64.deb for amd64'

# A supplied tag must bypass discovery and be used verbatim in both asset URLs.
: >"$fixture_server_log"
expect_success --version v1.2.3
assert_not_contains "$fixture_server_log" '/releases/latest'
assert_contains "$fixture_server_log" '/releases/download/v1.2.3/SHA256SUMS'
assert_contains "$fixture_server_log" '/releases/download/v1.2.3/opensurge_1.2.3_amd64.deb'

expect_fail_when_checksum_missing
expect_fail_when_checksum_does_not_match
expect_fail_when_checksum_entry_is_duplicated
expect_fail_when_checksum_filename_has_whitespace
expect_fail_when_deb_name_does_not_match_arch
expect_fail_when_dpkg_deb_arch_is_wrong
expect_fail_when_offline_checksum_is_missing
expect_fail_when_online_url_is_not_https
expect_transfer_bootstrap_is_no_autostart_and_redacted
expect_transfer_bootstrap_preserves_modified_temporary_policy
expect_transfer_bootstrap_preserves_replaced_temporary_policy
expect_transfer_bootstrap_preserves_existing_policy
expect_offline_final_symlink_uses_target_and_adjacent_checksum

echo "opensurge installer release asset tests passed"
