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

cleanup() {
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

mkdir -p "$fake_bin"
: >"$captured_commands"
: >"$fake_tty"
chmod 0600 "$fake_tty"
for command_name in apt-get dpkg systemctl curl ip ss; do
	make_fake_command "$command_name"
done

expect_fail() {
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if OPENSURGE_INSTALLER_TEST=1 \
		OPENSURGE_INSTALLER_ROOT="$test_root/root" \
		OPENSURGE_INSTALLER_BIN_DIR="$fake_bin" \
		OPENSURGE_INSTALLER_LOG="$installer_log" \
		OPENSURGE_INSTALLER_TTY="$fake_tty" \
		OPENSURGE_INSTALLER_COMMANDS="$captured_commands" \
		OPENSURGE_TEST_SECRET="$test_secret" \
		PATH="$fake_bin:$PATH" \
		bash "$installer" "$@" >"$captured_stdout" 2>"$captured_stderr"; then
		fail "installer accepted invalid invocation: $*"
	fi

	assert_not_contains "$captured_commands" 'apt-get'
	assert_not_contains "$captured_commands" 'dpkg'
	assert_not_contains "$captured_commands" 'systemctl'
	assert_not_contains "$captured_commands" 'curl'
	assert_not_contains "$captured_commands" 'ip'
	assert_not_contains "$captured_commands" 'ss'
	assert_not_contains "$captured_stdout" "$test_secret"
	assert_not_contains "$captured_stderr" "$test_secret"
	assert_not_contains "$installer_log" "$test_secret"
}

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

echo "opensurge installer parser tests passed"
