#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SMOKE="$ROOT/tests/linux-real-device/smoke.sh"

fail() {
  echo "linux real-device contract test: $*" >&2
  exit 1
}

assert_contains() {
  local haystack="$1" needle="$2"
  [[ "$haystack" == *"$needle"* ]] || fail "missing: $needle"
}

assert_not_contains() {
  local haystack="$1" needle="$2"
  [[ "$haystack" != *"$needle"* ]] || fail "unexpected: $needle"
}

help_output="$(bash "$SMOKE" --help)"
source_text="$(<"$SMOKE")"

assert_contains "$help_output" "MODE                  isolated_lan, same_lan, same_wifi_dhcp"
assert_contains "$help_output" "TRANSPARENT_MODE      off or tun (optional; defaults to off)"
assert_not_contains "$help_output" "MODE                  isolated_lan, same_lan, same_wifi_dhcp, tun, or off"
assert_contains "$source_text" 'same_lan requires DOWNSTREAM_IFACE to equal UPSTREAM_IFACE'
assert_contains "$source_text" 'same_wifi_dhcp requires DOWNSTREAM_IFACE to equal UPSTREAM_IFACE'
assert_contains "$source_text" 'TRANSPARENT_MODE'

source "$SMOKE"

if ! (UPSTREAM_IFACE=eno1 DOWNSTREAM_IFACE=eno2 LAN_CIDR=192.168.50.0/24 \
  MODE=isolated_lan TRANSPARENT_MODE=tun validate_topology); then
  fail "isolated_lan with distinct interfaces should validate"
fi
if ! (UPSTREAM_IFACE=eno1 DOWNSTREAM_IFACE=eno1 LAN_CIDR=192.168.50.0/24 \
  MODE=same_lan validate_topology); then
  fail "same_lan with one interface should validate"
fi
if ! (UPSTREAM_IFACE=wlan0 DOWNSTREAM_IFACE=wlan0 LAN_CIDR=192.168.50.0/24 \
  MODE=same_wifi_dhcp ROUTER_DHCP_DISABLED=confirmed validate_topology); then
  fail "same_wifi_dhcp with one interface and confirmation should validate"
fi
if (UPSTREAM_IFACE=eno1 DOWNSTREAM_IFACE=eno1 LAN_CIDR=192.168.50.0/24 \
  MODE=isolated_lan validate_topology) 2>/dev/null; then
  fail "isolated_lan should reject one interface"
fi
if (UPSTREAM_IFACE=eno1 DOWNSTREAM_IFACE=eno2 LAN_CIDR=192.168.50.0/24 \
  MODE=same_lan validate_topology) 2>/dev/null; then
  fail "same_lan should require one interface"
fi
if (UPSTREAM_IFACE=eno1 DOWNSTREAM_IFACE=eno1 LAN_CIDR=192.168.50.0/24 \
  MODE=tun validate_topology) 2>/dev/null; then
  fail "tun must not be accepted as Gateway.Mode"
fi
if (UPSTREAM_IFACE=eno1 DOWNSTREAM_IFACE=eno1 LAN_CIDR=192.168.50.0/24 \
  MODE=same_wifi_dhcp validate_topology) 2>/dev/null; then
  fail "same_wifi_dhcp should require router DHCP confirmation"
fi

echo "linux real-device topology contract: static assertions passed"
