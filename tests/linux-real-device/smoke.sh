#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat <<'EOF'
Usage: tests/linux-real-device/smoke.sh [--help]

Linux-only, non-destructive real-device smoke-plan validator. By default it
validates the operator inputs and prints read-only inspection steps; it does
not change host links, addresses, routes, forwarding, firewall rules, or
services.

Required environment:
  UPSTREAM_IFACE       Physical upstream interface.
  DOWNSTREAM_IFACE     Downstream interface (mutually exclusive with VLAN).
  DOWNSTREAM_VLAN       VLAN interface name or VLAN ID (mutually exclusive).
  LAN_CIDR              Downstream LAN IPv4 CIDR, for example 192.168.50.0/24.
  MODE                  isolated_lan, same_lan, same_wifi_dhcp, tun, or off.

For MODE=same_wifi_dhcp, also set:
  ROUTER_DHCP_DISABLED=confirmed

Example:
  UPSTREAM_IFACE=eno1 DOWNSTREAM_IFACE=eno2 \
    LAN_CIDR=192.168.50.0/24 MODE=tun \
    bash tests/linux-real-device/smoke.sh

The runner prints a plan only. Any actual host-networking operation requires a
separate, explicitly reviewed operator procedure.
EOF
}

die() {
  echo "linux real-device smoke: $*" >&2
  exit 2
}

require_linux() {
  local system
  system="$(uname -s)"
  if [[ "$system" != "Linux" ]]; then
    echo "linux real-device smoke not run: Linux is required; detected $system" >&2
    exit 77
  fi
}

valid_interface_name() {
  local value="$1"
  [[ -n "$value" && ${#value} -le 15 ]] || return 1
  [[ "$value" =~ ^[[:alnum:]_.-]+$ ]]
}

valid_lan_cidr() {
  local cidr="$1"
  local address prefix octet
  local -a octets

  [[ "$cidr" == */* ]] || return 1
  address="${cidr%/*}"
  prefix="${cidr#*/}"
  [[ "$prefix" =~ ^[0-9]+$ ]] || return 1
  ((10#$prefix <= 32)) || return 1
  IFS=. read -r -a octets <<<"$address"
  [[ ${#octets[@]} -eq 4 ]] || return 1
  for octet in "${octets[@]}"; do
    [[ "$octet" =~ ^[0-9]{1,3}$ ]] || return 1
    ((10#$octet <= 255)) || return 1
  done
}

resolve_downstream() {
  local vlan_id

  if [[ -n "${DOWNSTREAM_IFACE:-}" && -n "${DOWNSTREAM_VLAN:-}" ]]; then
    die "set only one of DOWNSTREAM_IFACE or DOWNSTREAM_VLAN"
  fi
  if [[ -n "${DOWNSTREAM_IFACE:-}" ]]; then
    DOWNSTREAM_RESOLVED="$DOWNSTREAM_IFACE"
    valid_interface_name "$DOWNSTREAM_RESOLVED" ||
      die "invalid DOWNSTREAM_IFACE: $DOWNSTREAM_RESOLVED"
    return 0
  fi
  [[ -n "${DOWNSTREAM_VLAN:-}" ]] ||
    die "set DOWNSTREAM_IFACE or DOWNSTREAM_VLAN"
  if [[ "$DOWNSTREAM_VLAN" =~ ^[0-9]+$ ]]; then
    vlan_id="$DOWNSTREAM_VLAN"
    ((10#$vlan_id >= 1 && 10#$vlan_id <= 4094)) ||
      die "DOWNSTREAM_VLAN ID must be between 1 and 4094"
    DOWNSTREAM_RESOLVED="${UPSTREAM_IFACE}.${vlan_id}"
  elif [[ "$DOWNSTREAM_VLAN" =~ ^[[:alnum:]_.-]+\.([0-9]+)$ ]]; then
    vlan_id="${BASH_REMATCH[1]}"
    ((10#$vlan_id >= 1 && 10#$vlan_id <= 4094)) ||
      die "DOWNSTREAM_VLAN ID must be between 1 and 4094"
    DOWNSTREAM_RESOLVED="$DOWNSTREAM_VLAN"
  else
    die "DOWNSTREAM_VLAN must be a VLAN ID or interface.vlan-id"
  fi
  valid_interface_name "$DOWNSTREAM_RESOLVED" ||
    die "invalid resolved downstream VLAN interface: $DOWNSTREAM_RESOLVED"
}

validate_inputs() {
  [[ -n "${UPSTREAM_IFACE:-}" ]] || die "UPSTREAM_IFACE is required"
  valid_interface_name "$UPSTREAM_IFACE" || die "invalid UPSTREAM_IFACE: $UPSTREAM_IFACE"
  resolve_downstream
  [[ "$UPSTREAM_IFACE" != "$DOWNSTREAM_RESOLVED" ]] ||
    die "upstream and downstream interfaces must be different"
  [[ -n "${LAN_CIDR:-}" ]] || die "LAN_CIDR is required"
  valid_lan_cidr "$LAN_CIDR" || die "invalid LAN_CIDR: $LAN_CIDR"
  [[ -n "${MODE:-}" ]] || die "MODE is required"
  case "$MODE" in
    isolated_lan|same_lan|same_wifi_dhcp|tun|off) ;;
    *) die "unsupported MODE: $MODE" ;;
  esac
  if [[ "$MODE" == "same_wifi_dhcp" && "${ROUTER_DHCP_DISABLED:-}" != "confirmed" ]]; then
    echo "WARNING: MODE=same_wifi_dhcp can conflict with the upstream router DHCP service." >&2
    die "refusing same_wifi_dhcp without ROUTER_DHCP_DISABLED=confirmed"
  fi
  command -v ip >/dev/null 2>&1 || die "iproute2 'ip' command is required"
}

print_plan() {
  echo "Linux real-device smoke plan (validation only; no host networking changed)"
  echo "  upstream interface:   $UPSTREAM_IFACE"
  echo "  downstream interface: $DOWNSTREAM_RESOLVED"
  echo "  LAN CIDR:             $LAN_CIDR"
  echo "  mode:                 $MODE"
  echo
  echo "Read-only checks an operator should perform:"
  printf '  ip -j addr show dev %q\n' "$UPSTREAM_IFACE"
  printf '  ip -j addr show dev %q\n' "$DOWNSTREAM_RESOLVED"
  printf '  ip -j route show\n'
  printf '  ip -j neigh show dev %q\n' "$DOWNSTREAM_RESOLVED"
  echo "  validate the OpenSurge candidate configuration before any start"
  echo "  verify the selected LAN address/prefix does not overlap upstream routes"
  if [[ "$MODE" == "same_wifi_dhcp" ]]; then
    echo "  confirm the upstream router DHCP service is disabled"
  fi
  echo
  echo "No ip link, ip addr, ip route, sysctl, nftables, or service command was run."
}

main() {
  case "${1:-}" in
    --help|-h)
      usage
      return 0
      ;;
    "") ;;
    *) die "unsupported argument: $1 (use --help)" ;;
  esac
  require_linux
  validate_inputs
  print_plan
}

main "$@"
