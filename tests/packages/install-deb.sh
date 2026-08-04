#!/usr/bin/env bash
set -Eeuo pipefail

fail() {
	printf 'package-lifecycle: %s\n' "$*" >&2
	exit 1
}

require_disposable_root() {
	if [[ "${OPENSURGE_PACKAGE_TEST_ALLOW_HOST:-}" != 1 ]]; then
		printf '%s\n' \
			'refusing destructive package lifecycle test' \
			'set OPENSURGE_PACKAGE_TEST_ALLOW_HOST=1 only inside a disposable Debian/Ubuntu root' >&2
		exit 2
	fi
	[[ $(uname -s) == Linux ]] || fail 'package lifecycle test requires a disposable Linux root'
	[[ ${EUID:-$(id -u)} -eq 0 ]] || fail 'package lifecycle test requires root after explicit opt-in'
	if dpkg-query -W -f='${db:Status-Status}' opensurge 2>/dev/null | grep -F 'installed' >/dev/null; then
		fail 'refusing to replace an existing OpenSurge installation'
	fi
	for path in /etc/opensurge /var/lib/opensurge /usr/bin/opensurge /usr/lib/opensurge; do
		[[ ! -e "$path" && ! -L "$path" ]] || fail "refusing to overwrite existing path: $path"
	done
}

assert_contains() {
	local file=$1
	local expected=$2
	grep -F -- "$expected" "$file" >/dev/null || fail "missing text in $file: $expected"
}

assert_not_contains() {
	local file=$1
	local unexpected=$2
	if [[ -f "$file" ]] && grep -F -- "$unexpected" "$file" >/dev/null; then
		fail "unexpected text in $file: $unexpected"
	fi
}

assert_secret_not_in_file() {
	local file=$1
	local secret=$2
	if [[ -f "$file" ]] && grep -F -- "$secret" "$file" >/dev/null; then
		fail "administrator password was exposed in $file"
	fi
}

assert_file_mode() {
	local path=$1
	local expected=$2
	local actual
	actual=$(stat -c '%a' "$path")
	[[ $actual == "$expected" ]] || fail "mode for $path is $actual, want $expected"
}

assert_file_equals() {
	local path=$1
	local expected=$2
	[[ -f "$path" && ! -L "$path" ]] || fail "expected regular file: $path"
	printf '%s' "$expected" | cmp -s - "$path" || fail "unexpected contents in $path"
}

assert_service_state() {
	local service=$1
	local expected=$2
	local path="$service_state_dir/$service"
	[[ -f "$path" ]] || fail "missing fake service state: $service"
	[[ $(<"$path") == "$expected" ]] || fail "state for $service is $(<"$path"), want $expected"
}

set_service_state() {
	local service=$1
	local state=$2
	printf '%s\n' "$state" >"$service_state_dir/$service"
}

snapshot_resolver() {
	original_resolver_kind=absent
	if [[ -L /etc/resolv.conf ]]; then
		original_resolver_kind=symlink
		cp -a /etc/resolv.conf "$original_resolver_backup"
	elif [[ -f /etc/resolv.conf ]]; then
		original_resolver_kind=regular
		cp -a /etc/resolv.conf "$original_resolver_backup"
	elif [[ -e /etc/resolv.conf ]]; then
		fail '/etc/resolv.conf must be absent, regular, or a symbolic link'
	fi
}

restore_test_entry_resolver() {
	[[ -n ${original_resolver_kind:-} ]] || return 0
	rm -f /etc/resolv.conf
	case "$original_resolver_kind" in
		absent) ;;
		regular|symlink) cp -a "$original_resolver_backup" /etc/resolv.conf ;;
	esac
}

cleanup() {
	local status=$?
	trap - EXIT
	set +e
	if [[ -n ${fixture_bin:-} ]]; then
		PATH="$fixture_bin:$PATH" dpkg --purge opensurge >/dev/null 2>&1
	fi
	restore_test_entry_resolver >/dev/null 2>&1
	if [[ -n ${test_root:-} ]]; then
		rm -rf -- "$test_root"
	fi
	exit "$status"
}

make_fake_apt_get() {
	cat >"$fixture_bin/apt-get" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
test -z "${OPENSURGE_INSTALLER_MARKER:-}" || exit 91
printf 'apt-get' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf ' %q' "$@" >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf '\n' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
if test "${1:-}" = install; then
	test -f "$OPENSURGE_INSTALLER_BIN_DIR/policy-rc.d"
	test ! -L "$OPENSURGE_INSTALLER_BIN_DIR/policy-rc.d"
	test "$(stat -c '%a' "$OPENSURGE_INSTALLER_BIN_DIR/policy-rc.d")" = 755
	grep -Fx 'exit 101' "$OPENSURGE_INSTALLER_BIN_DIR/policy-rc.d" >/dev/null
	printf 'suppressed\n' >"$OPENSURGE_PACKAGE_APT_SUPPRESSED"
fi
EOF
	chmod 0755 "$fixture_bin/apt-get"
}

make_account_tools() {
	cat >"$fixture_bin/addgroup" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
test "${1:-}" = --system
test "${2:-}" = opensurge
exec groupadd --system opensurge
EOF
	cat >"$fixture_bin/adduser" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
test "${1:-}" = --system
test "${2:-}" = --home
test "${3:-}" = /var/lib/opensurge
test "${4:-}" = --no-create-home
test "${5:-}" = --ingroup
test "${6:-}" = opensurge
test "${7:-}" = --disabled-login
test "${8:-}" = --disabled-password
test "${9:-}" = --shell
test "${10:-}" = /usr/sbin/nologin
test "${11:-}" = opensurge
exec useradd --system --home-dir /var/lib/opensurge --no-create-home \
	--gid opensurge --shell /usr/sbin/nologin opensurge
EOF
	chmod 0755 "$fixture_bin/addgroup" "$fixture_bin/adduser"
}

make_fake_ip() {
	cat >"$fixture_bin/ip" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'ip' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf ' %q' "$@" >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf '\n' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
case "${1:-}:${2:-}:${3:-}" in
	-4:route:get) printf '%s\n' '1.1.1.1 via 192.0.2.1 dev eth0 src 192.0.2.10 uid 0' ;;
	-4:route:show) printf '%s\n' 'default via 192.0.2.1 dev eth0 proto dhcp src 192.0.2.10 metric 100' ;;
	link:show:dev) test "${5:-}" = eth0 ;;
	*) exit 1 ;;
esac
EOF
	chmod 0755 "$fixture_bin/ip"
}

make_fake_ss() {
	cat >"$fixture_bin/ss" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
test -z "${OPENSURGE_INSTALLER_MARKER:-}" || exit 92
printf 'ss' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf ' %q' "$@" >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf '\n' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
EOF
	chmod 0755 "$fixture_bin/ss"
}

make_fake_curl() {
	cat >"$fixture_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'curl' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf ' %q' "$@" >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf '\n' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"

for argument in "$@"; do
	case "$argument" in
		*/api/v1/auth/status)
			printf '%s\n' '{"initialized":true,"authenticated":false}'
			exit 0
			;;
	esac
done
exit 1
EOF
	chmod 0755 "$fixture_bin/curl"
}

make_fake_systemctl() {
	cat >"$fixture_bin/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
	start|enable) test -z "${OPENSURGE_INSTALLER_MARKER:-}" || exit 93 ;;
esac
printf 'systemctl' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf ' %q' "$@" >>"$OPENSURGE_PACKAGE_COMMAND_LOG"
printf '\n' >>"$OPENSURGE_PACKAGE_COMMAND_LOG"

state_path() {
	printf '%s/%s' "$OPENSURGE_PACKAGE_SYSTEMCTL_STATE_DIR" "$1"
}

read_state() {
	local path
	path=$(state_path "$1")
	if test -f "$path"; then
		cat "$path"
	else
		printf '%s' disabled-inactive
	fi
}

write_state() {
	printf '%s\n' "$2" >"$(state_path "$1")"
}

last_argument=${!#-}
case "${1:-}" in
	is-enabled)
		state=$(read_state "$last_argument")
		test "${state%-*}" = enabled
		;;
	is-active)
		state=$(read_state "$last_argument")
		test "${state#*-}" = active
		;;
	disable|enable|start|stop)
		action=$1
		now=0
		for argument in "$@"; do
			test "$argument" = --now && now=1
		done
		for service in "$@"; do
			case "$service" in
				*.service|*.socket)
					state=$(read_state "$service")
					case "$action" in
						disable)
							state="disabled-${state#*-}"
							test "$now" -eq 0 || state=disabled-inactive
							;;
						enable)
							state="enabled-${state#*-}"
							test "$now" -eq 0 || state=enabled-active
							;;
						start) state="${state%-*}-active" ;;
						stop) state="${state%-*}-inactive" ;;
					esac
					write_state "$service" "$state"
					;;
			esac
		done
		;;
	daemon-reload) ;;
	*) exit 1 ;;
esac
EOF
	chmod 0755 "$fixture_bin/systemctl"
}

prepare_resolver_symlink() {
	local contents=$1
	mkdir -p /run/systemd/resolve
	printf '%s' "$contents" >/run/systemd/resolve/opensurge-package-test-resolv.conf
	rm -f /etc/resolv.conf
	ln -s ../run/systemd/resolve/opensurge-package-test-resolv.conf /etc/resolv.conf
}

prepare_resolver_regular() {
	local contents=$1
	rm -f /etc/resolv.conf
	printf '%s' "$contents" >/etc/resolv.conf
	chmod 0644 /etc/resolv.conf
}

run_controlled_installer_fixture() {
	: >"$installer_stdout"
	: >"$installer_stderr"
	OPENSURGE_INSTALLER_TEST=1 \
		OPENSURGE_INSTALLER_ROOT=/ \
		OPENSURGE_INSTALLER_BIN_DIR="$fixture_bin" \
		OPENSURGE_INSTALLER_LOG="$installer_log" \
		OPENSURGE_INSTALLER_TTY="$fake_tty" \
		OPENSURGE_INSTALLER_TEST_TRANSFER_PREREQUISITES=available \
		OPENSURGE_PACKAGE_COMMAND_LOG="$command_log" \
		OPENSURGE_PACKAGE_APT_SUPPRESSED="$apt_suppressed" \
		OPENSURGE_PACKAGE_SYSTEMCTL_STATE_DIR="$service_state_dir" \
		PATH="$fixture_bin:$PATH" \
		bash "$installer" --deb "$fixture_package" --checksums "$fixture_checksums" \
		>"$installer_stdout" 2>"$installer_stderr"
}

expect_direct_dpkg_install_failure() {
	local output="$test_root/direct-install.output"
	local before_resolver_target
	before_resolver_target=$(readlink /etc/resolv.conf)
	if PATH="$fixture_bin:$PATH" dpkg -i "$package" >"$output" 2>&1; then
		fail 'direct dpkg installation unexpectedly succeeded'
	fi
	assert_contains "$output" 'opensurge-install'
	[[ $(readlink /etc/resolv.conf) == "$before_resolver_target" ]] || fail 'direct install changed resolver representation'
	[[ ! -e /usr/bin/opensurge && ! -L /usr/bin/opensurge ]] || fail 'direct install unpacked OpenSurge files'
	[[ ! -e /lib/systemd/system/opensurge-gateway.service ]] || fail 'direct install unpacked systemd units'
	[[ ! -e /var/lib/opensurge/install-state/manifest ]] || fail 'direct install created ownership state'
	assert_not_contains "$command_log" 'systemctl'
	PATH="$fixture_bin:$PATH" dpkg --purge opensurge >/dev/null 2>&1 || true
}

expect_preinst_rejection() {
	local marker=$1
	local label=$2
	local output="$test_root/preinst-$label.output"
	if OPENSURGE_INSTALLER_MARKER="$marker" "$extracted_preinst" install >"$output" 2>&1; then
		fail "preinst accepted invalid installer marker: $label"
	fi
	assert_contains "$output" 'opensurge-install'
}

test_preinst_rejects_invalid_markers() {
	local marker_directory=/run/opensurge/installer
	local marker="$marker_directory/transaction-lifecycle-test.marker"
	local outside_marker="$test_root/transaction-lifecycle-test.marker"

	mkdir -p "$marker_directory"
	chmod 0700 "$marker_directory"
	printf '%s\n' opensurge-installer-marker-v1 'transaction_id=lifecycle-test' >"$outside_marker"
	chmod 0600 "$outside_marker"
	expect_preinst_rejection "$outside_marker" outside-runtime-directory

	cp "$outside_marker" "$marker"
	chmod 0644 "$marker"
	expect_preinst_rejection "$marker" wrong-mode
	chmod 0600 "$marker"
	printf '%s\n' opensurge-installer-marker-v1 'transaction_id=different-transaction' >"$marker"
	expect_preinst_rejection "$marker" mismatched-transaction
	printf '%s\n' opensurge-installer-marker-v1 'transaction_id=lifecycle-test' >"$marker"
	chown 65534:65534 "$marker"
	expect_preinst_rejection "$marker" wrong-owner
	chown root:root "$marker"
	chmod 0755 "$marker_directory"
	expect_preinst_rejection "$marker" unprotected-directory
	chmod 0700 "$marker_directory"
	rm -f "$marker"
	ln -s "$outside_marker" "$marker"
	expect_preinst_rejection "$marker" symbolic-link
	rm -f "$marker" "$outside_marker"
	rmdir "$marker_directory"
}

assert_controlled_install() {
	[[ -x /usr/bin/opensurge ]] || fail 'controlled installer did not install opensurge'
	[[ -x /usr/bin/opensurge-setup ]] || fail 'controlled installer did not install opensurge-setup'
	[[ -x /usr/lib/opensurge/mihomo ]] || fail 'controlled installer did not install mihomo'
	[[ -f /lib/systemd/system/opensurge-gateway.socket ]] || fail 'controlled installer did not install gateway socket'
	[[ -f /var/lib/opensurge/admin.json && ! -L /var/lib/opensurge/admin.json ]] || fail 'installer did not initialize an administrator'
	assert_file_mode /var/lib/opensurge/admin.json 600
	assert_file_mode /var/lib/opensurge/install-state/manifest 600
	assert_service_state dnsmasq.service disabled-inactive
	assert_service_state opensurge-gateway.socket enabled-active
	assert_service_state opensurge-control.service enabled-active
	[[ -f "$apt_suppressed" ]] || fail 'dependency installation did not prove policy-rc.d suppression'
	assert_not_contains "$command_log" 'systemctl start dnsmasq.service'
	assert_contains "$command_log" 'systemctl enable --now opensurge-gateway.socket opensurge-control.service'
	assert_contains "$command_log" 'curl --fail --silent --show-error --insecure https://192.0.2.10:61767/api/v1/auth/status'
	if [[ -d /run/opensurge/installer ]] && find /run/opensurge/installer -mindepth 1 -print -quit | grep -q .; then
		fail 'installer marker survived package-manager completion'
	fi
}

assert_generated_password_is_tty_only() {
	local password
	local password_count

	password=$(sed -n '2p' "$fake_tty")
	[[ $password =~ ^[A-Za-z0-9_-]{12,128}$ ]] || fail 'installer did not display a URL-safe one-time password on its controlling TTY'
	password_count=$(grep -F -c -- "$password" "$fake_tty" || true)
	[[ $password_count == 1 ]] || fail 'one-time password was not displayed exactly once on the controlling TTY'
	for file in "$installer_stdout" "$installer_stderr" "$installer_log" "$command_log" /etc/opensurge/config.yaml /var/lib/opensurge/install-state/manifest; do
		assert_secret_not_in_file "$file" "$password"
	done
}

test_remove_restores_only_owned_state() {
	local original_target='../run/systemd/resolve/opensurge-package-test-resolv.conf'
	local managed_resolver=$'nameserver 9.9.9.9\n'
	local administrator_digest

	prepare_resolver_symlink "$managed_resolver"
	set_service_state systemd-resolved.service enabled-active
	set_service_state dnsmasq.service disabled-inactive
	: >"$command_log"
	run_controlled_installer_fixture || {
		cat "$installer_stderr" >&2
		fail 'controlled installer fixture failed for remove case'
	}
	assert_controlled_install
	assert_generated_password_is_tty_only
	administrator_digest=$(sha256sum /var/lib/opensurge/admin.json)
	[[ -f /etc/resolv.conf && ! -L /etc/resolv.conf ]] || fail 'installer did not take resolver ownership'

	: >"$command_log"
	run_controlled_installer_fixture || {
		cat "$installer_stderr" >&2
		fail 'controlled upgrade fixture failed'
	}
	[[ -f /etc/resolv.conf && ! -L /etc/resolv.conf ]] || fail 'upgrade restored resolver mid-transaction'
	[[ -f /var/lib/opensurge/install-state/manifest ]] || fail 'upgrade removed ownership manifest'
	[[ $(sha256sum /var/lib/opensurge/admin.json) == "$administrator_digest" ]] || fail 'upgrade overwrote the existing administrator state'
	assert_not_contains "$command_log" 'systemctl enable systemd-resolved.service'
	assert_not_contains "$command_log" 'systemctl start systemd-resolved.service'

	: >"$command_log"
	PATH="$fixture_bin:$PATH" dpkg -r opensurge
	[[ -L /etc/resolv.conf ]] || fail 'ordinary remove did not restore resolver symlink'
	[[ $(readlink /etc/resolv.conf) == "$original_target" ]] || fail 'ordinary remove restored wrong resolver target'
	assert_file_equals /run/systemd/resolve/opensurge-package-test-resolv.conf "$managed_resolver"
	assert_service_state systemd-resolved.service enabled-active
	assert_service_state dnsmasq.service disabled-inactive
	assert_contains "$command_log" 'systemctl enable systemd-resolved.service'
	assert_contains "$command_log" 'systemctl start systemd-resolved.service'
	assert_not_contains "$command_log" 'systemctl enable dnsmasq.service'
	[[ ! -e /var/lib/opensurge/install-state/manifest ]] || fail 'remove retained ownership manifest'
	[[ ! -e /var/lib/opensurge/install-state/resolv.conf.before && ! -L /var/lib/opensurge/install-state/resolv.conf.before ]] || fail 'remove retained resolver backup'
	PATH="$fixture_bin:$PATH" "$extracted_postrm" remove
	PATH="$fixture_bin:$PATH" dpkg --purge opensurge
}

test_purge_restores_only_owned_state_and_credentials() {
	local original_resolver=$'nameserver 8.8.8.8\n'

	prepare_resolver_regular "$original_resolver"
	set_service_state systemd-resolved.service disabled-inactive
	set_service_state dnsmasq.service enabled-active
	: >"$command_log"
	run_controlled_installer_fixture || {
		cat "$installer_stderr" >&2
		fail 'controlled installer fixture failed for purge case'
	}
	assert_controlled_install
	assert_file_equals /etc/resolv.conf "$original_resolver"
	printf '%s\n' '{"credential":"fixture"}' >/var/lib/opensurge/admin.json
	printf '%s\n' fixture-cert >/etc/opensurge/tls/cert.pem
	printf '%s\n' fixture-key >/etc/opensurge/tls/key.pem

	: >"$command_log"
	PATH="$fixture_bin:$PATH" dpkg --purge opensurge
	assert_file_equals /etc/resolv.conf "$original_resolver"
	assert_service_state systemd-resolved.service disabled-inactive
	assert_service_state dnsmasq.service enabled-active
	assert_not_contains "$command_log" 'systemctl enable systemd-resolved.service'
	assert_not_contains "$command_log" 'systemctl start systemd-resolved.service'
	assert_contains "$command_log" 'systemctl enable dnsmasq.service'
	assert_contains "$command_log" 'systemctl start dnsmasq.service'
	[[ ! -e /var/lib/opensurge ]] || fail 'purge retained OpenSurge state or credentials'
	[[ ! -e /etc/opensurge/tls/key.pem ]] || fail 'purge retained TLS private key'
	PATH="$fixture_bin:$PATH" "$extracted_postrm" purge
}

test_invalid_manifest_is_retained() {
	mkdir -p /var/lib/opensurge/install-state
	chmod 0700 /var/lib/opensurge/install-state
	cat >/var/lib/opensurge/install-state/manifest <<'EOF'
state_version=1
installer_version=1
transaction_id=invalid/value
install_phase=complete
resolver_was_altered=0
resolved_was_altered=0
resolved_was_enabled=0
resolved_was_active=0
dnsmasq_was_altered=0
dnsmasq_was_enabled=0
dnsmasq_was_active=0
resolv_conf_backup_exists=0
resolv_conf_kind=absent
selected_resolver=none
resolver_selection=none
EOF
	chmod 0600 /var/lib/opensurge/install-state/manifest
	: >"$command_log"
	if PATH="$fixture_bin:$PATH" "$extracted_postrm" remove; then
		fail 'postrm trusted an invalid ownership manifest'
	fi
	[[ -f /var/lib/opensurge/install-state/manifest ]] || fail 'postrm removed invalid recovery state'
	assert_not_contains "$command_log" 'systemctl enable systemd-resolved.service'
	assert_not_contains "$command_log" 'systemctl start dnsmasq.service'
	rm -rf /var/lib/opensurge
}

test_expanded_local_ipv6_manifest_is_retained() {
	local selected_resolver
	local current_resolver=$'nameserver 192.0.2.53\n'
	local backup_resolver=$'nameserver 203.0.113.53\n'

	for selected_resolver in 0:0:0:0:0:0:0:0 0:0:0:0:0:0:0:1; do
		prepare_resolver_regular "$current_resolver"
		rm -rf /var/lib/opensurge/install-state
		mkdir -p /var/lib/opensurge/install-state
		chmod 0700 /var/lib/opensurge/install-state
		printf '%s' "$backup_resolver" >/var/lib/opensurge/install-state/resolv.conf.before
		cat >/var/lib/opensurge/install-state/manifest <<EOF
state_version=1
installer_version=1
transaction_id=expanded-ipv6-test
install_phase=complete
resolver_was_altered=1
resolved_was_altered=1
resolved_was_enabled=1
resolved_was_active=1
dnsmasq_was_altered=1
dnsmasq_was_enabled=1
dnsmasq_was_active=1
resolv_conf_backup_exists=1
resolv_conf_kind=regular
selected_resolver=$selected_resolver
resolver_selection=nameserver
EOF
		chmod 0600 /var/lib/opensurge/install-state/manifest
		set_service_state systemd-resolved.service disabled-inactive
		set_service_state dnsmasq.service disabled-inactive
		: >"$command_log"
		if PATH="$fixture_bin:$PATH" "$extracted_postrm" remove; then
			fail "postrm trusted local expanded IPv6 resolver: $selected_resolver"
		fi
		[[ -f /var/lib/opensurge/install-state/manifest ]] || fail 'postrm removed invalid expanded-IPv6 manifest'
		[[ -f /var/lib/opensurge/install-state/resolv.conf.before ]] || fail 'postrm removed invalid expanded-IPv6 backup'
		assert_file_equals /etc/resolv.conf "$current_resolver"
		assert_service_state systemd-resolved.service disabled-inactive
		assert_service_state dnsmasq.service disabled-inactive
		assert_not_contains "$command_log" 'systemctl enable systemd-resolved.service'
		assert_not_contains "$command_log" 'systemctl start dnsmasq.service'
	done
	rm -rf /var/lib/opensurge
}

package=${1:-}
[[ -n "$package" ]] || { printf 'usage: install-deb.sh /path/to/opensurge_<version>_<arch>.deb\n' >&2; exit 2; }
[[ -f "$package" ]] || fail "package does not exist: $package"
package=$(cd "$(dirname "$package")" && pwd)/$(basename "$package")
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
installer="$repo_root/scripts/opensurge-install"

for command_name in dpkg dpkg-deb dpkg-query sha256sum; do
	command -v "$command_name" >/dev/null 2>&1 || fail "$command_name is required for package lifecycle testing"
done
require_disposable_root

test_root=$(mktemp -d /tmp/opensurge-package-lifecycle.XXXXXX)
fixture_bin="$test_root/bin"
service_state_dir="$test_root/systemctl-state"
command_log="$test_root/commands"
apt_suppressed="$test_root/apt-suppressed"
installer_log="$test_root/installer.log"
installer_stdout="$test_root/installer.stdout"
installer_stderr="$test_root/installer.stderr"
fake_tty="$test_root/tty"
control_root="$test_root/control"
original_resolver_backup="$test_root/resolv.conf.entry"
original_resolver_kind=''
export OPENSURGE_PACKAGE_COMMAND_LOG="$command_log"
export OPENSURGE_PACKAGE_APT_SUPPRESSED="$apt_suppressed"
export OPENSURGE_PACKAGE_SYSTEMCTL_STATE_DIR="$service_state_dir"
export OPENSURGE_INSTALLER_BIN_DIR="$fixture_bin"
mkdir -p "$fixture_bin" "$service_state_dir" "$control_root"
: >"$command_log"
: >"$fake_tty"
chmod 0600 "$fake_tty"
snapshot_resolver
trap cleanup EXIT

make_fake_apt_get
make_account_tools
make_fake_ip
make_fake_ss
make_fake_systemctl
make_fake_curl

expected_package=opensurge
[[ $(dpkg-deb -f "$package" Package) == "$expected_package" ]] || fail 'unexpected package name'
architecture=$(dpkg-deb -f "$package" Architecture)
case "$architecture" in amd64|arm64) ;; *) fail "unexpected package architecture: $architecture" ;; esac
[[ $(dpkg --print-architecture) == "$architecture" ]] || fail 'package architecture does not match disposable root'
depends=$(dpkg-deb -f "$package" Depends)
for forbidden_dependency in dnsmasq nftables iproute2 ca-certificates systemd; do
	if grep -Eq "(^|[,[:space:]])${forbidden_dependency}([[:space:],]|$)" <<<"$depends"; then
		fail "forbidden runtime dependency: $forbidden_dependency"
	fi
done

dpkg-deb -e "$package" "$control_root"
extracted_preinst="$control_root/preinst"
extracted_postrm="$control_root/postrm"
[[ -x "$extracted_preinst" ]] || fail 'package preinst is missing or not executable'
[[ -x "$extracted_postrm" ]] || fail 'package postrm is missing or not executable'
assert_file_mode "$extracted_preinst" 755
assert_file_mode "$extracted_postrm" 755

fixture_package="$test_root/$(basename "$package")"
fixture_checksums="$test_root/SHA256SUMS"
cp "$package" "$fixture_package"
(cd "$test_root" && sha256sum "$(basename "$fixture_package")" >SHA256SUMS)

prepare_resolver_symlink $'nameserver 9.9.9.9\n'
test_preinst_rejects_invalid_markers
expect_direct_dpkg_install_failure
test_remove_restores_only_owned_state
test_purge_restores_only_owned_state_and_credentials
test_expanded_local_ipv6_manifest_is_retained
test_invalid_manifest_is_retained

printf 'package lifecycle assertions passed for %s\n' "$architecture"
