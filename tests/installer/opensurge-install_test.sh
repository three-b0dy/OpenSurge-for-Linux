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
fixture_installer="$fixture_root/opensurge-install"
fixture_checksums="$fixture_root/SHA256SUMS"
fixture_checksums_content=''
fixture_server_port="$test_root/release-server.port"
fixture_server_log="$test_root/release-server.log"
fixture_server_pid=''
release_base_url=''
config_path="$test_root/root/etc/opensurge/config.yaml"
observed_installer_marker="$test_root/observed-installer-marker"

cleanup() {
	local status=$?

	trap - EXIT
	if test -n "$fixture_server_pid"; then
		kill "$fixture_server_pid" 2>/dev/null || true
		wait "$fixture_server_pid" 2>/dev/null || true
	fi
	chmod -R u+w "$test_root" 2>/dev/null || true
	rm -rf "$test_root"
	exit "$status"
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

assert_file_equals() {
	local file=$1
	local expected=$2

	test -f "$file" || fail "missing file: $file"
	printf '%s' "$expected" | cmp -s - "$file" || fail "unexpected contents in $file"
}

assert_file_missing() {
	local file=$1
	test ! -e "$file" && test ! -L "$file" || fail "unexpected file: $file"
}

assert_file_mode() {
	local file=$1
	local expected=$2
	local actual

	actual=$(stat -c '%a' "$file" 2>/dev/null || stat -f '%Lp' "$file")
	test "$actual" = "$expected" || fail "mode for $file = $actual, want $expected"
}

assert_symlink_target() {
	local file=$1
	local expected=$2
	local actual

	test -L "$file" || fail "expected symbolic link: $file"
	actual=$(readlink "$file")
	test "$actual" = "$expected" || fail "link target for $file = $actual, want $expected"
}

assert_regular_resolv_conf() {
	local expected=$1
	local resolver_path="$test_root/root/etc/resolv.conf"

	test -f "$resolver_path" && ! test -L "$resolver_path" || fail 'resolv.conf is not a regular file'
	assert_contains "$resolver_path" "$expected"
}

assert_fake_service_state() {
	local service=$1
	local expected=$2
	local state_file="$test_root/root/.systemctl-state/$service"

	test -f "$state_file" || fail "missing fake service state: $service"
	test "$(<"$state_file")" = "$expected" || \
		fail "state for $service = $(<"$state_file"), want $expected"
}

assert_manifest() {
	local expected=$1
	local manifest="$test_root/root/var/lib/opensurge/install-state/manifest"

	assert_contains "$manifest" "$expected"
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
if test "${OPENSURGE_INSTALLER_TEST_APT_FAIL:-}" = "${1:-}"; then
	exit 42
fi
exit 0
EOF
	chmod 0755 "$fake_bin/apt-get"
}

make_fake_timeout() {
	cat >"$fake_bin/timeout" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"
if test "${1:-}" = --foreground; then
	shift
fi
duration=${1:-}
case "$duration" in
	''|*[!0-9]*) exit 64 ;;
esac
shift
exec "$@"
EOF
	chmod 0755 "$fake_bin/timeout"
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
if test "$#" -eq 2 && test "$1" = -i; then
	marker=${OPENSURGE_INSTALLER_MARKER:-}
	test -n "$marker" || exit 45
	case "$marker" in
		"$OPENSURGE_INSTALLER_ROOT/run/opensurge/installer/"transaction-*.marker) ;;
		*) exit 46 ;;
	esac
	test -f "$marker" && ! test -L "$marker" || exit 47
	marker_mode=$(stat -c '%a' "$marker" 2>/dev/null || stat -f '%Lp' "$marker")
	test "$marker_mode" = 600 || exit 48
	marker_name=${marker##*/}
	transaction_id=${marker_name#transaction-}
	transaction_id=${transaction_id%.marker}
	test "$(sed -n '1p' "$marker")" = opensurge-installer-marker-v1 || exit 49
	test "$(sed -n '2p' "$marker")" = "transaction_id=$transaction_id" || exit 50
	test "$(wc -l <"$marker" | tr -d '[:space:]')" = 2 || exit 51
	printf '%s\n' "$marker" >"$OPENSURGE_INSTALLER_TEST_MARKER_OBSERVED_PATH"
	if test "${OPENSURGE_INSTALLER_TEST_EXPECT_FRESH_CONFIG:-0}" = 1; then
		test ! -e "$OPENSURGE_INSTALLER_TEST_CONFIG_PATH" || exit 1
	fi
	if test "${OPENSURGE_INSTALLER_TEST_DPKG_FAIL:-}" = 1; then
		exit 43
	fi
	if test "${OPENSURGE_INSTALLER_TEST_SETUP_BINARY_UNAVAILABLE:-}" != 1; then
		mkdir -p "$OPENSURGE_INSTALLER_ROOT/usr/bin"
		ln -sf "$OPENSURGE_INSTALLER_BIN_DIR/opensurge-setup" "$OPENSURGE_INSTALLER_ROOT/usr/bin/opensurge-setup"
	fi
	touch "$OPENSURGE_INSTALLER_TEST_PACKAGE_PHASE_PATH"
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

make_fake_ip() {
	cat >"$fake_bin/ip" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"

scenario=${OPENSURGE_INSTALLER_TEST_IP_SCENARIO:-ens18}
case "$1:$2:$3" in
	-4:route:get)
		case "$scenario" in
			missing-device) printf '%s\n' '1.1.1.1 via 192.0.2.1 src 192.0.2.10 uid 0' ;;
			missing-source) printf '%s\n' '1.1.1.1 via 192.0.2.1 dev ens18 uid 0' ;;
			no-via) printf '%s\n' '1.1.1.1 dev ens18 src 192.0.2.10 uid 0' ;;
			vlan) printf '%s\n' '1.1.1.1 via 198.51.100.1 dev enp1s0.50 src 198.51.100.2 uid 0' ;;
			bridge) printf '%s\n' '1.1.1.1 via 192.0.2.1 dev br-lan src 192.0.2.10 uid 0' ;;
			*) printf '%s\n' '1.1.1.1 via 192.0.2.1 dev ens18 src 192.0.2.10 uid 0' ;;
		esac
		;;
	-4:route:show)
		case "$scenario" in
			missing-device) printf '%s\n' 'default via 192.0.2.1 proto dhcp src 192.0.2.10 metric 100' ;;
			missing-source) printf '%s\n' 'default via 192.0.2.1 dev ens18 proto dhcp metric 100' ;;
			no-via) printf '%s\n' 'default dev ens18 proto dhcp src 192.0.2.10 metric 100' ;;
			vlan) printf '%s\n' 'default via 198.51.100.1 dev enp1s0.50 proto dhcp src 198.51.100.2 metric 100' ;;
			bridge) printf '%s\n' 'default via 192.0.2.1 dev br-lan proto dhcp src 192.0.2.10 metric 100' ;;
			*) printf '%s\n' 'default via 192.0.2.1 dev ens18 proto dhcp src 192.0.2.10 metric 100' ;;
		esac
		;;
	link:show:dev)
		# Real iproute2 (6.15+) rejects a "--" end-of-options marker for this
		# subcommand's positional syntax, so the installer must never pass one;
		# match the trailing argument regardless of position to catch it.
		case "$*" in
			*' --'*) exit 1 ;;
		esac
		case "${*: -1}" in
			eth0|ens18|enp1s0.50|br-lan) exit 0 ;;
			*) exit 1 ;;
		esac
		;;
	-4:addr:show)
		case "$*" in
			*' --'*) exit 1 ;;
		esac
		case "${*: -1}" in
			br-lan)
				case "$scenario" in
					missing-lan-address) ;;
					lan-ip-in-dhcp-range) printf '%s\n' '    inet 192.168.50.150/24 scope global br-lan' ;;
					*) printf '%s\n' '    inet 192.168.50.1/24 scope global br-lan' ;;
				esac
				;;
			enp1s0.50) printf '%s\n' '    inet 198.51.100.2/24 scope global enp1s0.50' ;;
			esac
		;;
	esac
EOF
	chmod 0755 "$fake_bin/ip"
}

make_fake_systemctl() {
	cat >"$fake_bin/systemctl" <<'EOF'
#!/usr/bin/env bash
test -z "${OPENSURGE_INSTALLER_MARKER:-}" || exit 98
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"

state_directory="$OPENSURGE_INSTALLER_ROOT/.systemctl-state"
service="${!#}"
state_file="$state_directory/${service//\//_}"
mkdir -p "$state_directory"

initial_state() {
	case "$service" in
		systemd-resolved.service) printf '%s' "${OPENSURGE_INSTALLER_TEST_RESOLVED_STATE:-disabled-inactive}" ;;
		dnsmasq.service) printf '%s' "${OPENSURGE_INSTALLER_TEST_DNSMASQ_STATE:-disabled-inactive}" ;;
		*) printf '%s' 'disabled-inactive' ;;
	esac
}

read_state() {
	if test -f "$state_file"; then
		cat "$state_file"
	else
		initial_state
	fi
}

write_state() {
	printf '%s\n' "$1" >"$state_file"
}

state=$(read_state)
case "${1:-}" in
	is-enabled)
		if test "${state%-*}" = enabled; then
			printf '%s\n' enabled
			exit 0
		fi
		printf '%s\n' disabled
		exit 1
		;;
	is-active)
		if test "${state#*-}" = active; then
			printf '%s\n' active
			exit 0
		fi
		printf '%s\n' inactive
		exit 3
		;;
	disable|enable|start|stop)
		action=$1
		now=0
		for argument in "$@"; do
			test "$argument" = --now && now=1
		done
		for target in "$@"; do
			case "$target" in
				*.service|*.socket)
					target_state=$(read_state "$target")
					case "$action" in
						disable)
							target_state="disabled-${target_state#*-}"
							test "$now" -eq 0 || target_state=disabled-inactive
							;;
						enable)
							target_state="enabled-${target_state#*-}"
							test "$now" -eq 0 || target_state=enabled-active
							;;
						start) target_state="${target_state%-*}-active" ;;
						stop) target_state="${target_state%-*}-inactive" ;;
					esac
					service=$target
					state_file="$state_directory/${service//\//_}"
					write_state "$target_state"
					if test "$action" = disable && test "${OPENSURGE_INSTALLER_TEST_SYSTEMCTL_FAIL_AFTER_MUTATION:-}" = "$target"; then
						exit 44
					fi
					;;
			esac
		done
		;;
esac
EOF
	chmod 0755 "$fake_bin/systemctl"
}

make_fake_ss() {
	cat >"$fake_bin/ss" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"

case " $* " in
	*' -ltnp '*) if test -n "${OPENSURGE_INSTALLER_TEST_PORT53_TCP:-}"; then printf '%s\n' "$OPENSURGE_INSTALLER_TEST_PORT53_TCP"; fi ;;
	*' -lunp '*) if test -n "${OPENSURGE_INSTALLER_TEST_PORT53_UDP:-}"; then printf '%s\n' "$OPENSURGE_INSTALLER_TEST_PORT53_UDP"; fi ;;
esac
EOF
	chmod 0755 "$fake_bin/ss"
}

make_fake_curl() {
	cat >"$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"

for argument in "$@"; do
	case "$argument" in
		*/api/v1/auth/status)
			if test "${OPENSURGE_INSTALLER_TEST_CONTROL_HEALTH:-available}" = failing; then
				exit 22
			fi
			printf '%s\n' '{"initialized":true,"authenticated":false}'
			exit 0
			;;
	esac
done
exec "$OPENSURGE_INSTALLER_TEST_REAL_CURL" "$@"
EOF
	chmod 0755 "$fake_bin/curl"
}

make_fake_readlink() {
	cat >"$fake_bin/readlink" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"
exec /usr/bin/readlink "$@"
EOF
	chmod 0755 "$fake_bin/readlink"
}

make_fake_cp() {
	cat >"$fake_bin/cp" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"
exec /bin/cp "$@"
EOF
	chmod 0755 "$fake_bin/cp"
}

make_fake_opensurge() {
	cat >"$fake_bin/opensurge" <<'EOF'
#!/usr/bin/env bash
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"
if test "$#" -eq 4 && test "$1" = config && test "$2" = validate && test "$3" = --config; then
	test -f "$4" && test -f "$OPENSURGE_INSTALLER_TEST_PACKAGE_PHASE_PATH"
	fi
EOF
	chmod 0755 "$fake_bin/opensurge"
}

make_fake_opensurge_setup() {
	cat >"$fake_bin/opensurge-setup" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s' "$(basename "$0")" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf ' %q' "$@" >>"$OPENSURGE_INSTALLER_COMMANDS"
printf '\n' >>"$OPENSURGE_INSTALLER_COMMANDS"

test "$#" -eq 5
test "$1" = init
test "$2" = --username
test "$3" = admin
test "$4" = --password-fd
password_fd=$5
case "$password_fd" in ''|*[!0-9]*) exit 41 ;; esac
test -p "/dev/fd/$password_fd"
test -z "${OPENSURGE_INSTALLER_TEST_ADMIN_PASSWORD:-}"
IFS= read -r password <&"$password_fd"
test "${#password}" -ge 12
test "${OPENSURGE_INSTALLER_TEST_SETUP_FAIL:-0}" != 1
	mkdir -p "$OPENSURGE_INSTALLER_ROOT/var/lib/opensurge"
	printf '%s\n' '{"username":"admin","hash":"fixture"}' >"$OPENSURGE_INSTALLER_ROOT/var/lib/opensurge/admin.json"
	chmod 0600 "$OPENSURGE_INSTALLER_ROOT/var/lib/opensurge/admin.json"
	chown root:opensurge "$OPENSURGE_INSTALLER_ROOT/var/lib/opensurge/admin.json"
EOF
	chmod 0755 "$fake_bin/opensurge-setup"
}

start_release_fixture() {
	mkdir -p "$fixture_root"
	printf 'approved release installer\n' >"$fixture_installer"
	printf 'approved test package\n' >"$fixture_deb"
	fixture_checksums_content="$(
		cd "$fixture_root"
		sha256sum opensurge-install opensurge_1.2.3_amd64.deb | LC_ALL=C sort -k2,2
	)"
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
	local expect_fresh_config=0
	if test ! -e "$config_path" && test ! -L "$config_path"; then
		expect_fresh_config=1
	fi

	OPENSURGE_INSTALLER_TEST=1 \
		OPENSURGE_INSTALLER_ROOT="$test_root/root" \
		OPENSURGE_INSTALLER_BIN_DIR="$fake_bin" \
		OPENSURGE_INSTALLER_LOG="$installer_log" \
		OPENSURGE_INSTALLER_TTY="$fake_tty" \
		OPENSURGE_INSTALLER_COMMANDS="$captured_commands" \
		OPENSURGE_INSTALLER_TEST_RELEASE_BASE_URL="$release_base_url" \
		OPENSURGE_INSTALLER_TEST_TRANSFER_PREREQUISITES="$transfer_prerequisites" \
		OPENSURGE_INSTALLER_TEST_POLICY_MUTATION="${OPENSURGE_TEST_POLICY_MUTATION:-}" \
		OPENSURGE_INSTALLER_TEST_APT_FAIL="${OPENSURGE_TEST_APT_FAIL:-}" \
		OPENSURGE_INSTALLER_TEST_DPKG_FAIL="${OPENSURGE_TEST_DPKG_FAIL:-}" \
		OPENSURGE_INSTALLER_TEST_RESOLVED_STATE="${OPENSURGE_TEST_RESOLVED_STATE:-}" \
		OPENSURGE_INSTALLER_TEST_DNSMASQ_STATE="${OPENSURGE_TEST_DNSMASQ_STATE:-}" \
		OPENSURGE_INSTALLER_TEST_PORT53_TCP="${OPENSURGE_TEST_PORT53_TCP:-}" \
		OPENSURGE_INSTALLER_TEST_PORT53_UDP="${OPENSURGE_TEST_PORT53_UDP:-}" \
		OPENSURGE_INSTALLER_TEST_SYSTEMCTL_FAIL_AFTER_MUTATION="${OPENSURGE_TEST_SYSTEMCTL_FAIL_AFTER_MUTATION:-}" \
		OPENSURGE_INSTALLER_TEST_SETUP_FAIL="${OPENSURGE_TEST_SETUP_FAIL:-}" \
		OPENSURGE_INSTALLER_TEST_SETUP_BINARY_UNAVAILABLE="${OPENSURGE_TEST_SETUP_BINARY_UNAVAILABLE:-}" \
		OPENSURGE_INSTALLER_TEST_CONTROL_HEALTH="${OPENSURGE_TEST_CONTROL_HEALTH:-available}" \
		OPENSURGE_INSTALLER_TEST_ADMIN_PASSWORD="$test_secret" \
		OPENSURGE_INSTALLER_TEST_REAL_CURL="$real_curl" \
		OPENSURGE_INSTALLER_TEST_IP_SCENARIO="${OPENSURGE_INSTALLER_TEST_IP_SCENARIO:-ens18}" \
		OPENSURGE_INSTALLER_TEST_CONFIG_PATH="$config_path" \
		OPENSURGE_INSTALLER_TEST_PACKAGE_PHASE_PATH="$test_root/package-phase-complete" \
		OPENSURGE_INSTALLER_TEST_MARKER_OBSERVED_PATH="$observed_installer_marker" \
		OPENSURGE_INSTALLER_TEST_EXPECT_FRESH_CONFIG="$expect_fresh_config" \
		OPENSURGE_TEST_SECRET="$test_secret" \
		PATH="$fake_bin:$PATH" \
		bash "$installer" "$@" >"$captured_stdout" 2>"$captured_stderr"
}

reset_install_root() {
	rm -rf -- "$test_root/root"
	rm -f -- "$test_root/package-phase-complete" "$observed_installer_marker"
	mkdir -p "$test_root/root/etc"
	printf '%s\n' "${OPENSURGE_INSTALLER_TEST_RESOLVER:-nameserver 192.0.2.53}" >"$test_root/root/etc/resolv.conf"
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
	reset_install_root
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	run_installer "$@" || {
		cat "$captured_stderr" >&2
		fail "installer rejected valid invocation: $*"
	}
	assert_contains "$captured_commands" 'timeout --foreground 600 apt-get install --yes --no-install-recommends adduser ca-certificates curl dnsmasq nftables iproute2 systemd'
	assert_contains "$captured_commands" 'apt-get install --yes --no-install-recommends adduser ca-certificates curl dnsmasq nftables iproute2 systemd'
	assert_contains "$captured_commands" 'dpkg -i'
	assert_not_contains "$captured_stdout" "$test_secret"
	assert_not_contains "$captured_stderr" "$test_secret"
	assert_not_contains "$installer_log" "$test_secret"
	test -s "$observed_installer_marker" || fail 'dpkg did not receive an installer marker'
	assert_file_missing "$(<"$observed_installer_marker")"
	assert_file_equals "$test_root/root/var/lib/opensurge/admin.json" $'{"username":"admin","hash":"fixture"}\n'
	assert_contains "$captured_commands" "chown root:opensurge $test_root/root/var/lib/opensurge/admin.json"
	assert_fake_service_state opensurge-gateway.socket enabled-active
	assert_fake_service_state opensurge-control.service enabled-active
}

expect_fresh_administrator_setup_is_pipe_only_and_redacted() {
	local setup_count
	local secret_count

	reset_install_root
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	: >"$fake_tty"
	run_installer --version v1.2.3 || fail 'installer rejected fresh administrator initialization'
	assert_contains "$captured_commands" 'opensurge-setup init --username admin --password-fd'
	setup_count=$(grep -F -c -- 'opensurge-setup init --username admin --password-fd' "$captured_commands" || true)
	test "$setup_count" -eq 1 || fail "expected one setup invocation, got $setup_count"
	secret_count=$(grep -F -c -- "$test_secret" "$fake_tty" || true)
	test "$secret_count" -eq 1 || fail "expected generated password exactly once on controlling TTY, got $secret_count"
	assert_contains "$fake_tty" 'Change this one-time password immediately in the Web UI'
	assert_not_contains "$captured_stdout" "$test_secret"
	assert_not_contains "$captured_stderr" "$test_secret"
	assert_not_contains "$captured_commands" "$test_secret"
	assert_not_contains "$installer_log" "$test_secret"
	assert_not_contains "$config_path" "$test_secret"
	assert_not_contains "$test_root/root/var/lib/opensurge/install-state/manifest" "$test_secret"
	assert_contains "$captured_commands" 'curl --fail --silent --show-error --insecure --connect-timeout 5 --max-time 10 https://192.0.2.10:61767/api/v1/auth/status'
	assert_fake_service_state opensurge-gateway.socket enabled-active
	assert_fake_service_state opensurge-control.service enabled-active
}

expect_existing_administrator_skips_setup() {
	reset_install_root
	mkdir -p "$test_root/root/var/lib/opensurge"
	printf '%s\n' '{"username":"admin","hash":"preserved"}' >"$test_root/root/var/lib/opensurge/admin.json"
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	: >"$fake_tty"
	run_installer --version v1.2.3 || fail 'installer rejected an existing administrator state'
	assert_command_not_invoked "$captured_commands" opensurge-setup
	assert_file_equals "$test_root/root/var/lib/opensurge/admin.json" $'{"username":"admin","hash":"preserved"}\n'
	assert_not_contains "$captured_commands" 'chown opensurge:opensurge'
	assert_not_contains "$fake_tty" "$test_secret"
	assert_contains "$installer_log" 'preserved existing OpenSurge administrator state'
}

expect_control_health_failure_rolls_back_owned_state() {
	begin_host_state_case
	: >"$fake_tty"
	if OPENSURGE_TEST_RESOLVED_STATE=enabled-active \
		OPENSURGE_TEST_CONTROL_HEALTH=failing run_installer --version v1.2.3; then
		fail 'installer accepted an unavailable HTTPS control endpoint'
	fi
	assert_contains "$captured_stderr" 'OpenSurge HTTPS control endpoint did not become available'
	assert_contains "$captured_commands" 'systemctl enable --now opensurge-gateway.socket opensurge-control.service'
	assert_contains "$captured_commands" 'systemctl status --no-pager opensurge-gateway.socket opensurge-control.service'
	assert_contains "$captured_commands" 'journalctl --no-pager --lines 50 --unit opensurge-gateway.service --unit opensurge-control.service'
	assert_file_equals "$test_root/root/etc/resolv.conf" $'nameserver 192.0.2.53\n'
	assert_file_missing "$test_root/root/var/lib/opensurge/install-state/manifest"
	assert_not_contains "$captured_stdout" "$test_secret"
	assert_not_contains "$captured_stderr" "$test_secret"
	assert_not_contains "$captured_commands" "$test_secret"
	assert_not_contains "$installer_log" "$test_secret"
}

expect_setup_failure_rolls_back_owned_state() {
	begin_host_state_case
	: >"$fake_tty"
	if OPENSURGE_TEST_RESOLVED_STATE=enabled-active \
		OPENSURGE_TEST_SETUP_FAIL=1 run_installer --version v1.2.3; then
		fail 'installer accepted a failed administrator setup operation'
	fi
	assert_contains "$captured_stderr" 'cannot initialize the OpenSurge administrator account'
	assert_not_contains "$captured_commands" 'systemctl enable --now opensurge-gateway.socket opensurge-control.service'
	assert_file_equals "$test_root/root/etc/resolv.conf" $'nameserver 192.0.2.53\n'
	assert_file_missing "$test_root/root/var/lib/opensurge/install-state/manifest"
	assert_not_contains "$captured_stdout" "$test_secret"
	assert_not_contains "$captured_stderr" "$test_secret"
	assert_not_contains "$captured_commands" "$test_secret"
	assert_not_contains "$installer_log" "$test_secret"
}

expect_missing_setup_binary_fails_before_password_output() {
	reset_install_root
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	: >"$fake_tty"
	if OPENSURGE_TEST_SETUP_BINARY_UNAVAILABLE=1 run_installer --version v1.2.3; then
		fail 'installer accepted a missing installed setup binary'
	fi
	assert_contains "$captured_stderr" 'installed OpenSurge setup binary is unavailable'
	assert_command_not_invoked "$captured_commands" opensurge-setup
	assert_file_missing "$test_root/root/var/lib/opensurge/admin.json"
	test ! -s "$fake_tty" || fail 'installer displayed an unusable one-time password before detecting the missing setup binary'
	assert_not_contains "$captured_stdout" "$test_secret"
	assert_not_contains "$captured_stderr" "$test_secret"
	assert_not_contains "$captured_commands" "$test_secret"
	assert_not_contains "$installer_log" "$test_secret"
}

expect_topology_failure() {
	reset_install_root
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if run_installer "$@"; then
		fail "installer accepted invalid topology: $*"
	fi
	assert_not_contains "$captured_commands" 'dpkg -i'
	assert_file_missing "$config_path"
}

assert_generated_same_lan_config() {
	assert_contains "$config_path" 'mode: "same_lan"'
	assert_contains "$config_path" 'interface: "ens18"'
	assert_contains "$config_path" 'lan_ip: "192.0.2.10"'
	assert_contains "$config_path" 'upstream_interface: "ens18"'
	assert_contains "$config_path" 'enabled: false'
	assert_contains "$config_path" 'listen: "192.0.2.10"'
	assert_contains "$config_path" 'listen: "192.0.2.10:61767"'
	assert_contains "$config_path" 'mode: "off"'
	assert_contains "$config_path" 'binary: "/usr/lib/opensurge/mihomo"'
	assert_contains "$config_path" 'config: "/run/opensurge/mihomo.yaml"'
	assert_contains "$config_path" 'dir: "/var/lib/opensurge/runtime"'
	assert_file_mode "$config_path" 640
	assert_contains "$captured_commands" 'chown root:opensurge'
	go run "$repo_root/cmd/opensurge" config validate --config "$config_path" >/dev/null || \
		fail 'generated same_lan configuration did not pass opensurge config validate'
}

expect_existing_config_is_preserved() {
	local original=$'management:\n  listen: "192.0.2.10:61767"\n# preserve every byte\n'

	reset_install_root
	mkdir -p "$(dirname "$config_path")"
	printf '%s' "$original" >"$config_path"
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	run_installer --version v1.2.3 || fail 'installer rejected an existing config fixture'
	assert_file_equals "$config_path" "$original"
}

expect_isolated_config_uses_exact_link_names() {
	reset_install_root
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	run_installer --version v1.2.3 --mode isolated_lan \
		--downstream-interface br-lan --upstream-interface enp1s0.50 \
		--lan-ip 192.168.50.1 --lan-cidr 192.168.50.0/24 || \
		fail 'installer rejected valid isolated topology'
	assert_contains "$config_path" 'mode: "isolated_lan"'
	assert_contains "$config_path" 'interface: "br-lan"'
	assert_contains "$config_path" 'upstream_interface: "enp1s0.50"'
	assert_contains "$config_path" 'range_start: "192.168.50.100"'
	assert_contains "$config_path" 'range_end: "192.168.50.200"'
	go run "$repo_root/cmd/opensurge" config validate --config "$config_path" >/dev/null || \
		fail 'generated isolated configuration did not pass opensurge config validate'
}

expect_default_route_resolver_fallback() {
	reset_install_root
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	run_installer --version v1.2.3 || fail 'installer rejected a valid IPv6 resolver fallback'
	assert_generated_same_lan_config
}

prepare_symlinked_resolver() {
	local target_relative='../run/systemd/resolve/stub-resolv.conf'
	local target="$test_root/root/run/systemd/resolve/stub-resolv.conf"

	mkdir -p "$(dirname "$target")"
	printf '%s\n' "${1:-nameserver 9.9.9.9}" >"$target"
	rm -f "$test_root/root/etc/resolv.conf"
	ln -s "$target_relative" "$test_root/root/etc/resolv.conf"
}

begin_host_state_case() {
	reset_install_root
	rm -f "$fake_bin/policy-rc.d"
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
}

expect_resolved_symlink_is_snapshotted_and_replaced() {
	local state_root="$test_root/root/var/lib/opensurge/install-state"

	begin_host_state_case
	prepare_symlinked_resolver 'nameserver 9.9.9.9'
	OPENSURGE_TEST_RESOLVED_STATE=enabled-active run_installer --version v1.2.3 || \
		fail 'installer did not coordinate an active systemd-resolved service'
	assert_manifest 'state_version=1'
	assert_manifest 'resolved_was_active=1'
	assert_manifest 'resolv_conf_kind=symlink'
	assert_manifest 'resolv_conf_backup_exists=1'
	assert_not_contains "$state_root/manifest" 'nameserver 9.9.9.9'
	assert_file_mode "$state_root" 700
	assert_file_mode "$state_root/manifest" 600
	assert_symlink_target "$state_root/resolv.conf.before" '../run/systemd/resolve/stub-resolv.conf'
	assert_regular_resolv_conf 'nameserver 9.9.9.9'
}

expect_nonlocal_ipv6_resolver_is_preserved() {
	begin_host_state_case
	printf '%s\n' 'nameserver 2001:4860:4860::8888' >"$test_root/root/etc/resolv.conf"
	OPENSURGE_TEST_RESOLVED_STATE=enabled-active run_installer --version v1.2.3 || \
		fail 'installer rejected a non-local IPv6 resolver'
	assert_manifest 'selected_resolver=2001:4860:4860::8888'
	assert_regular_resolv_conf 'nameserver 2001:4860:4860::8888'
}

expect_gateway_is_used_when_resolver_is_local() {
	begin_host_state_case
	printf '%s\n' 'nameserver 127.0.0.53' >"$test_root/root/etc/resolv.conf"
	OPENSURGE_TEST_RESOLVED_STATE=enabled-active run_installer --version v1.2.3 || \
		fail 'installer rejected IPv4 default-gateway resolver fallback'
	assert_manifest 'selected_resolver=192.0.2.1'
	assert_regular_resolv_conf 'nameserver 192.0.2.1'
}

expect_orphaned_resolv_conf_backup_is_reclaimed() {
	begin_host_state_case
	mkdir -p "$test_root/root/var/lib/opensurge/install-state"
	printf 'nameserver 198.51.100.9\n' >"$test_root/root/var/lib/opensurge/install-state/resolv.conf.before"
	run_installer --version v1.2.3 || \
		fail 'installer rejected an orphaned resolv.conf backup left without an installation manifest'
	assert_contains "$installer_log" 'removing an orphaned resolv.conf backup left by an earlier interrupted installation'
}

expect_no_resolver_source_aborts_before_host_mutation() {
	begin_host_state_case
	mkdir -p "$(dirname "$config_path")"
	printf '%s\n' 'existing config establishes upgrade facts' >"$config_path"
	printf '%s\n' 'nameserver 127.0.0.53' >"$test_root/root/etc/resolv.conf"
	if OPENSURGE_TEST_RESOLVED_STATE=enabled-active \
		OPENSURGE_INSTALLER_TEST_IP_SCENARIO=no-via run_installer --version v1.2.3; then
		fail 'installer accepted an active resolved service without a safe resolver source'
	fi
	assert_contains "$captured_stderr" 'no non-local resolver or IPv4 default gateway is available'
	assert_not_contains "$captured_commands" 'apt-get install'
	assert_not_contains "$captured_commands" 'systemctl disable'
	assert_file_equals "$test_root/root/etc/resolv.conf" $'nameserver 127.0.0.53\n'
	assert_file_missing "$test_root/root/var/lib/opensurge/install-state/manifest"
}

expect_fresh_port_53_conflict_aborts_without_killing_listener() {
	begin_host_state_case
	if OPENSURGE_TEST_PORT53_UDP='udp UNCONN 0 0 0.0.0.0:53 0.0.0.0:* users:(("unbound",pid=123,fd=6))' \
		run_installer --version v1.2.3; then
		fail 'installer accepted an unknown UDP port 53 listener during fresh install'
	fi
	assert_contains "$captured_stderr" 'udp 0.0.0.0:53 pid=123 process=unbound'
	assert_not_contains "$captured_commands" 'kill 123'
	assert_contains "$captured_commands" 'ss -H -lunp'
	assert_file_missing "$test_root/root/var/lib/opensurge/install-state/manifest"
}

expect_fresh_port_53_conflict_reports_each_protocol() {
	begin_host_state_case
	if OPENSURGE_TEST_PORT53_TCP='tcp LISTEN 0 4096 127.0.0.1:53 0.0.0.0:* users:(("named",pid=12,fd=4))' \
		OPENSURGE_TEST_PORT53_UDP='udp UNCONN 0 0 0.0.0.0:53 0.0.0.0:* users:(("unbound",pid=123,fd=6))' \
		run_installer --version v1.2.3; then
		fail 'installer accepted TCP and UDP port 53 listeners during fresh install'
	fi
	assert_contains "$captured_stderr" $'tcp 127.0.0.1:53 pid=12 process=named\nudp 0.0.0.0:53 pid=123 process=unbound'
	assert_not_contains "$captured_stderr" 'process=namedudp'
	assert_not_contains "$captured_commands" 'kill 12'
	assert_not_contains "$captured_commands" 'kill 123'
}

expect_disabled_dnsmasq_is_not_touched() {
	begin_host_state_case
	OPENSURGE_TEST_DNSMASQ_STATE=disabled-inactive run_installer --version v1.2.3 || \
		fail 'installer rejected a host with disabled generic dnsmasq'
	assert_not_contains "$captured_commands" 'systemctl disable --now dnsmasq.service'
	assert_manifest 'dnsmasq_was_active=0'
	assert_manifest 'dnsmasq_was_enabled=0'
	assert_manifest 'dnsmasq_was_altered=0'
}

expect_existing_policy_survives_host_dependency_install() {
	begin_host_state_case
	printf '#!/bin/sh\n# user policy\nexit 0\n' >"$fake_bin/policy-rc.d"
	chmod 0755 "$fake_bin/policy-rc.d"
	run_installer --version v1.2.3 || fail 'installer rejected an existing package service policy'
	assert_contains "$fake_bin/policy-rc.d" '# user policy'
	assert_not_contains "$captured_commands" 'rm'
	rm -f "$fake_bin/policy-rc.d"
}

expect_temporary_policy_is_removed_after_dependency_failure() {
	begin_host_state_case
	if OPENSURGE_TEST_APT_FAIL=install run_installer --version v1.2.3; then
		fail 'installer accepted a failed host dependency installation'
	fi
	assert_contains "$captured_commands" 'apt-get install --yes --no-install-recommends adduser ca-certificates curl dnsmasq nftables iproute2 systemd'
	assert_file_missing "$fake_bin/policy-rc.d"
	assert_not_contains "$captured_commands" 'dpkg -i'
}

expect_orphaned_installer_policy_is_reclaimed() {
	begin_host_state_case
	printf '#!/bin/sh\n# OpenSurge installer temporary no-autostart policy 19700101T000000Z-1\nexit 101\n' \
		>"$fake_bin/policy-rc.d"
	chmod 0755 "$fake_bin/policy-rc.d"
	run_installer --version v1.2.3 || \
		fail 'installer rejected an orphaned no-autostart policy from an earlier interrupted run'
	assert_contains "$installer_log" 'reclaiming an orphaned no-autostart policy left by an earlier interrupted installation'
	assert_file_missing "$fake_bin/policy-rc.d"
}

expect_apt_index_is_refreshed_once_when_bootstrap_runs() {
	local update_count

	begin_host_state_case
	OPENSURGE_TEST_TRANSFER_PREREQUISITES=missing run_installer --version v1.2.3 || \
		fail 'installer rejected bootstrap with missing transfer prerequisites'
	update_count=$(grep -E -c '^apt-get update([[:space:]]|$)' "$captured_commands" || true)
	test "$update_count" -eq 1 || fail "expected exactly one apt-get update invocation, got $update_count"
}

expect_upgrade_skips_port_53_rejection() {
	begin_host_state_case
	mkdir -p "$(dirname "$config_path")"
	printf '%s\n' 'management:' '  listen: "192.0.2.10:61767"' >"$config_path"
	OPENSURGE_TEST_PORT53_TCP='tcp LISTEN 0 4096 0.0.0.0:53 0.0.0.0:* users:(("opensurge-gateway",pid=77,fd=3))' \
		run_installer --version v1.2.3 || fail 'upgrade rejected its existing port 53 listener'
	assert_command_not_invoked "$captured_commands" ss
}

expect_failure_rolls_back_owned_dns_state() {
	local state_root="$test_root/root/var/lib/opensurge/install-state"

	begin_host_state_case
	prepare_symlinked_resolver 'nameserver 9.9.9.9'
	if OPENSURGE_TEST_RESOLVED_STATE=enabled-active \
		OPENSURGE_TEST_DNSMASQ_STATE=enabled-active \
		OPENSURGE_TEST_DPKG_FAIL=1 run_installer --version v1.2.3; then
		fail 'installer accepted a failed OpenSurge package installation'
	fi
	assert_symlink_target "$test_root/root/etc/resolv.conf" '../run/systemd/resolve/stub-resolv.conf'
	assert_contains "$test_root/root/run/systemd/resolve/stub-resolv.conf" 'nameserver 9.9.9.9'
	assert_contains "$captured_commands" 'systemctl enable systemd-resolved.service'
	assert_contains "$captured_commands" 'systemctl start systemd-resolved.service'
	assert_contains "$captured_commands" 'systemctl enable dnsmasq.service'
	assert_contains "$captured_commands" 'systemctl start dnsmasq.service'
	assert_file_missing "$state_root/manifest"
	assert_file_missing "$state_root/resolv.conf.before"
	test -s "$observed_installer_marker" || fail 'failing dpkg did not receive an installer marker'
	assert_file_missing "$(<"$observed_installer_marker")"
}

expect_failed_resolved_disable_restores_recorded_state() {
	local state_root="$test_root/root/var/lib/opensurge/install-state"

	begin_host_state_case
	prepare_symlinked_resolver 'nameserver 9.9.9.9'
	if OPENSURGE_TEST_RESOLVED_STATE=enabled-active \
		OPENSURGE_TEST_SYSTEMCTL_FAIL_AFTER_MUTATION=systemd-resolved.service \
		run_installer --version v1.2.3; then
		fail 'installer accepted a failed systemd-resolved disable operation'
	fi
	assert_fake_service_state systemd-resolved.service enabled-active
	assert_contains "$captured_commands" 'systemctl enable systemd-resolved.service'
	assert_contains "$captured_commands" 'systemctl start systemd-resolved.service'
	assert_file_missing "$state_root/manifest"
	assert_file_missing "$state_root/resolv.conf.before"
}

expect_failed_dnsmasq_disable_restores_recorded_state() {
	local state_root="$test_root/root/var/lib/opensurge/install-state"

	begin_host_state_case
	if OPENSURGE_TEST_DNSMASQ_STATE=enabled-active \
		OPENSURGE_TEST_SYSTEMCTL_FAIL_AFTER_MUTATION=dnsmasq.service \
		run_installer --version v1.2.3; then
		fail 'installer accepted a failed generic dnsmasq disable operation'
	fi
	assert_fake_service_state dnsmasq.service enabled-active
	assert_contains "$captured_commands" 'systemctl enable dnsmasq.service'
	assert_contains "$captured_commands" 'systemctl start dnsmasq.service'
	assert_file_missing "$state_root/manifest"
	assert_file_missing "$state_root/resolv.conf.before"
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

expect_clean_production_environment_reaches_root_check() {
	: >"$captured_stdout"
	: >"$captured_stderr"
	: >"$captured_commands"
	if PATH="$fake_bin:$PATH" bash "$installer" --version v1.2.3 >"$captured_stdout" 2>"$captured_stderr"; then
		fail 'installer accepted invocation without root privileges outside test mode'
	fi
	assert_contains "$captured_stderr" 'must be run as root'
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
	assert_contains "$installer_log" 'existing policy-rc.d controls package service policy during dependency installation'
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
	assert_contains "$captured_commands" "cp -p -- $canonical_target_deb"
	assert_not_contains "$captured_commands" "dpkg-deb -f $linked_deb Architecture"
	assert_not_contains "$captured_commands" "dpkg -i $linked_deb"
	assert_not_contains "$captured_commands" "dpkg-deb -f $canonical_target_deb Architecture"
	assert_not_contains "$captured_commands" "dpkg -i $canonical_target_deb"
}

mkdir -p "$fake_bin"
: >"$captured_commands"
: >"$fake_tty"
chmod 0600 "$fake_tty"
real_curl=$(command -v curl)
for command_name in chown journalctl; do
	make_fake_command "$command_name"
done
make_fake_apt_get
make_fake_timeout
make_fake_dpkg
make_fake_dpkg_deb
make_fake_ip
make_fake_systemctl
make_fake_ss
make_fake_curl
make_fake_readlink
make_fake_cp
make_fake_opensurge
make_fake_opensurge_setup
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
assert_generated_same_lan_config
expect_fresh_administrator_setup_is_pipe_only_and_redacted
expect_existing_administrator_skips_setup

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
expect_clean_production_environment_reaches_root_check
expect_transfer_bootstrap_is_no_autostart_and_redacted
expect_transfer_bootstrap_preserves_modified_temporary_policy
expect_transfer_bootstrap_preserves_replaced_temporary_policy
expect_transfer_bootstrap_preserves_existing_policy
expect_offline_final_symlink_uses_target_and_adjacent_checksum

# DNS ownership starts only after the release asset is verified. These cases
# exercise state-file representation, resolver selection, service ownership,
# port safety, and automatic rollback through the installer itself.
expect_resolved_symlink_is_snapshotted_and_replaced
expect_nonlocal_ipv6_resolver_is_preserved
expect_gateway_is_used_when_resolver_is_local
expect_orphaned_resolv_conf_backup_is_reclaimed
expect_no_resolver_source_aborts_before_host_mutation
expect_fresh_port_53_conflict_aborts_without_killing_listener
expect_fresh_port_53_conflict_reports_each_protocol
expect_disabled_dnsmasq_is_not_touched
expect_existing_policy_survives_host_dependency_install
expect_temporary_policy_is_removed_after_dependency_failure
expect_orphaned_installer_policy_is_reclaimed
expect_apt_index_is_refreshed_once_when_bootstrap_runs
expect_upgrade_skips_port_53_rejection
expect_failure_rolls_back_owned_dns_state
expect_failed_resolved_disable_restores_recorded_state
expect_failed_dnsmasq_disable_restores_recorded_state
expect_missing_setup_binary_fails_before_password_output
expect_setup_failure_rolls_back_owned_state
expect_control_health_failure_rolls_back_owned_state

# Fresh configurations use the exact kernel default-route link name. They do
# not assign a friendly alias or implicitly create a downstream network.
expect_isolated_config_uses_exact_link_names
expect_existing_config_is_preserved

OPENSURGE_INSTALLER_TEST_IP_SCENARIO=missing-device expect_topology_failure --version v1.2.3
assert_contains "$captured_stderr" 'default-route interface is required'

OPENSURGE_INSTALLER_TEST_IP_SCENARIO=missing-source expect_topology_failure --version v1.2.3
assert_contains "$captured_stderr" 'default-route source IPv4 is required'

OPENSURGE_INSTALLER_TEST_IP_SCENARIO=no-via \
	OPENSURGE_INSTALLER_TEST_RESOLVER='nameserver 127.0.0.53' \
	expect_topology_failure --version v1.2.3
assert_contains "$captured_stderr" 'no default-route gateway or non-local resolver is available'

OPENSURGE_INSTALLER_TEST_IP_SCENARIO=missing-lan-address expect_topology_failure --version v1.2.3 \
	--mode isolated_lan --downstream-interface br-lan --upstream-interface eth0 \
	--lan-ip 192.168.50.1 --lan-cidr 192.168.50.0/24
assert_contains "$captured_stderr" 'LAN IPv4 is not configured on downstream interface'

expect_topology_failure --version v1.2.3 --mode isolated_lan \
	--downstream-interface br-lan --upstream-interface eth0 \
	--lan-ip 192.168.50.1 --lan-cidr 192.168.48.0/20
assert_contains "$captured_stderr" 'non-/24 isolated LAN requires an explicit DHCP range'

expect_topology_failure --version v1.2.3 --mode isolated_lan \
	--downstream-interface br-lan --upstream-interface eth0 \
	--lan-ip 192.168.50.1 --lan-cidr 192.168.50.0/24 \
	--dhcp-range-start 192.168.51.100 --dhcp-range-end 192.168.51.200
assert_contains "$captured_stderr" 'DHCP range must remain inside LAN CIDR'

OPENSURGE_INSTALLER_TEST_IP_SCENARIO=lan-ip-in-dhcp-range expect_topology_failure --version v1.2.3 \
	--mode isolated_lan --downstream-interface br-lan --upstream-interface eth0 \
	--lan-ip 192.168.50.150 --lan-cidr 192.168.50.0/24 \
	--dhcp-range-start 192.168.50.100 --dhcp-range-end 192.168.50.200
assert_contains "$captured_stderr" 'DHCP range must not include LAN IPv4'

expect_topology_failure --version v1.2.3 --mode isolated_lan \
	--downstream-interface br-lan --upstream-interface br-lan \
	--lan-ip 192.168.50.1 --lan-cidr 192.168.50.0/24
assert_contains "$captured_stderr" 'isolated_lan requires distinct downstream and upstream interfaces'

OPENSURGE_INSTALLER_TEST_IP_SCENARIO=no-via \
	OPENSURGE_INSTALLER_TEST_RESOLVER='nameserver 1::2::3' \
	expect_topology_failure --version v1.2.3
assert_contains "$captured_stderr" 'no default-route gateway or non-local resolver is available'

OPENSURGE_INSTALLER_TEST_IP_SCENARIO=no-via \
	OPENSURGE_INSTALLER_TEST_RESOLVER='nameserver fe90::1' \
	expect_topology_failure --version v1.2.3
assert_contains "$captured_stderr" 'no default-route gateway or non-local resolver is available'

OPENSURGE_INSTALLER_TEST_IP_SCENARIO=no-via \
	OPENSURGE_INSTALLER_TEST_RESOLVER='nameserver 2001:4860:4860::8888' \
	expect_default_route_resolver_fallback

echo "opensurge installer release asset tests passed"
