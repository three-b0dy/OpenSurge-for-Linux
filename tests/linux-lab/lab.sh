#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT_DIR="$ROOT/tests/linux-lab"
GW_NS="opensurge-lab-gw"
CLIENT_NS="opensurge-lab-client"
UPSTREAM_NS="opensurge-lab-upstream"
LAN_PREFIX="192.168.50.0/24"
LAN_IP="192.168.50.1"
UPSTREAM_PREFIX="198.51.100.0/24"
UPSTREAM_GW_IP="198.51.100.1"
UPSTREAM_SERVER_IP="198.51.100.2"
ORIGIN_PORT="443"
PROXY_PORT="18080"

IP_BIN=""
NFT_BIN=""
SYSCTL_BIN=""
DIG_BIN=""
CURL_BIN=""
OPENSSL_BIN=""
DNSMASQ_BIN=""
DHCP_CLIENT_BIN=""
MIHOMO_BIN=""
ORIGINAL_PATH="${PATH}"
RUNTIME_DIR=""
ARTIFACT_DIR=""
CONFIG_FILE=""
OPENSURGE_BIN=""
HELPER_BIN=""
DHCP_SCRIPT=""
CLIENT_MAC=""
CLIENT_IP=""
FORWARDING_BEFORE=""
UPSTREAM_DNS_PID=""
ORIGIN_PID=""
PROXY_PID=""
LAB_MODE="off"

die() {
  echo "linux namespace lab: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

require_linux_root() {
  local system
	system="$(uname -s)"
	if [[ "$system" != "Linux" ]]; then
		echo "linux namespace lab not run: requires Linux network namespaces and root privileges; detected $system" >&2
    exit 77
  fi
  if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
    if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
      if [[ -n "${OPENSURGE_LAB_MIHOMO_BIN:-}" ]]; then
        exec sudo -n env "PATH=$ORIGINAL_PATH" "OPENSURGE_LAB_MIHOMO_BIN=$OPENSURGE_LAB_MIHOMO_BIN" bash "$0" "$@"
      fi
      exec sudo -n env "PATH=$ORIGINAL_PATH" bash "$0" "$@"
    fi
    echo "linux namespace lab not run: requires root; run 'sudo -v && make linux-lab-test'" >&2
    exit 77
  fi
}

resolve_tools() {
	for command in ip nft sysctl dig curl openssl dnsmasq go; do
		require_command "$command"
	done
	IP_BIN="$(command -v ip)"
  NFT_BIN="$(command -v nft)"
  SYSCTL_BIN="$(command -v sysctl)"
  DIG_BIN="$(command -v dig)"
  CURL_BIN="$(command -v curl)"
	OPENSSL_BIN="$(command -v openssl)"
	DNSMASQ_BIN="$(command -v dnsmasq)"
  if command -v dhclient >/dev/null 2>&1; then
    DHCP_CLIENT_BIN="$(command -v dhclient)"
  elif command -v udhcpc >/dev/null 2>&1; then
    DHCP_CLIENT_BIN="$(command -v udhcpc)"
  else
    die "required DHCP client not found: install dhclient or udhcpc"
  fi
  if [[ -n "${OPENSURGE_LAB_MIHOMO_BIN:-}" ]]; then
    MIHOMO_BIN="$OPENSURGE_LAB_MIHOMO_BIN"
  else
    MIHOMO_BIN="$(command -v mihomo || true)"
	fi
	[[ -x "$MIHOMO_BIN" ]] || die "mihomo binary not found; set OPENSURGE_LAB_MIHOMO_BIN"
	local curl_help
	curl_help="$("$CURL_BIN" --help all 2>/dev/null)"
	[[ "$curl_help" == *"--dns-servers"* ]] || die "curl must support --dns-servers"
	"$IP_BIN" netns help >/dev/null 2>&1 || die "iproute2 netns support is required"
}

make_runtime() {
  mkdir -p "$ROOT/runtime" "$ROOT/artifacts/linux-lab"
  RUNTIME_DIR="$(mktemp -d "$ROOT/runtime/linux-lab.XXXXXX")"
  ARTIFACT_DIR="$ROOT/artifacts/linux-lab/$(date +%Y%m%d-%H%M%S)-$$"
  mkdir -p "$ARTIFACT_DIR"
  CONFIG_FILE="$RUNTIME_DIR/config.yaml"
  OPENSURGE_BIN="$RUNTIME_DIR/opensurge"
  HELPER_BIN="$RUNTIME_DIR/http-connect-proxy"
  DHCP_SCRIPT="$RUNTIME_DIR/dhcp-client-script"
}

write_dhcp_client_script() {
  cat >"$DHCP_SCRIPT" <<'EOF'
#!/bin/sh
set -eu

prefix_from_mask() {
  old_ifs=$IFS
  IFS=.
  set -- $1
  IFS=$old_ifs
  prefix=0
  for octet in "$@"; do
    case "$octet" in
      255) prefix=$((prefix + 8)) ;;
      254) prefix=$((prefix + 7)) ;;
      252) prefix=$((prefix + 6)) ;;
      248) prefix=$((prefix + 5)) ;;
      240) prefix=$((prefix + 4)) ;;
      224) prefix=$((prefix + 3)) ;;
      192) prefix=$((prefix + 2)) ;;
      128) prefix=$((prefix + 1)) ;;
      0) ;;
      *) return 1 ;;
    esac
  done
  printf '%s\n' "$prefix"
}

case "${reason:-}" in
  BOUND|REBOOT|RENEW|REBIND)
    address="${new_ip_address:-${ip:-}}"
    mask="${new_subnet_mask:-${subnet:-255.255.255.0}}"
    router="${new_routers:-${router:-}}"
    dns_servers="${new_domain_name_servers:-${dns:-}}"
    [[ -n "$address" ]] || exit 0
    prefix="$(prefix_from_mask "$mask")"
    if [[ -n "${OPENSURGE_DHCP_EVIDENCE_FILE:-}" ]]; then
      printf 'address=%s\nrouter=%s\ndns=%s\n' "$address" "$router" "$dns_servers" >"$OPENSURGE_DHCP_EVIDENCE_FILE"
    fi
    ip addr flush dev "$interface" scope global
    ip addr add "$address/$prefix" dev "$interface"
    if [[ -n "$router" ]]; then
      set -- $router
      ip route replace default via "$1" dev "$interface"
    fi
    ;;
esac
EOF
  chmod 0755 "$DHCP_SCRIPT"
}

build_binaries() {
  (cd "$ROOT" && go build -o "$OPENSURGE_BIN" ./cmd/opensurge)
  (cd "$ROOT" && go build -o "$HELPER_BIN" ./tests/linux-lab/helpers/http-connect-proxy.go)
}

write_imported_profile() {
  cat >"$RUNTIME_DIR/profile.yaml" <<EOF
dns:
  nameserver:
    - $UPSTREAM_SERVER_IP
rules:
  - MATCH,DIRECT
EOF
}

sed_escape() {
  printf '%s' "$1" | sed 's/[&|]/\\&/g'
}

write_config() {
  local mode="$1"
  local dns_upstream="${UPSTREAM_SERVER_IP}"
  local profile_mode="managed"
  local profile_path=""
  if [[ "$mode" == "tun" ]]; then
    dns_upstream="127.0.0.1#1053"
    profile_mode="imported"
    profile_path="$RUNTIME_DIR/profile.yaml"
    write_imported_profile
  fi
  sed \
    -e "s|__GATEWAY_INTERFACE__|$(sed_escape lan0)|g" \
    -e "s|__UPSTREAM_INTERFACE__|$(sed_escape wan0)|g" \
    -e "s|__DNSMASQ_BINARY__|$(sed_escape "$DNSMASQ_BIN")|g" \
    -e "s|__MIHOMO_BINARY__|$(sed_escape "$MIHOMO_BIN")|g" \
    -e "s|__MIHOMO_PROFILE_MODE__|$(sed_escape "$profile_mode")|g" \
    -e "s|__MIHOMO_PROFILE__|$(sed_escape "$profile_path")|g" \
    -e "s|__DNS_UPSTREAM__|$(sed_escape "$dns_upstream")|g" \
    -e "s|__TRANSPARENT_MODE__|$(sed_escape "$mode")|g" \
    -e "s|__MIHOMO_CONFIG__|$(sed_escape "$RUNTIME_DIR/mihomo.yaml")|g" \
    -e "s|__MANAGEMENT_LISTEN__|$(sed_escape "$LAN_IP:61767")|g" \
    -e "s|__RUNTIME_DIR__|$(sed_escape "$RUNTIME_DIR")|g" \
    "$SCRIPT_DIR/config.yaml.tmpl" >"$CONFIG_FILE"
}

namespace_exists() {
  "$IP_BIN" netns list | awk -v wanted="$1" '$1 == wanted { found=1 } END { exit !found }'
}

delete_namespace() {
  local namespace="$1" pid
  if ! namespace_exists "$namespace"; then
    return 0
  fi
	while read -r pid; do
	    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
	  done < <("$IP_BIN" netns pids "$namespace" 2>/dev/null || true)
	local attempt
	for ((attempt = 0; attempt < 10; attempt++)); do
	    if "$IP_BIN" netns del "$namespace" 2>/dev/null; then
	      return 0
	    fi
	    sleep 1
	done
	"$IP_BIN" netns del "$namespace" 2>/dev/null || true
}

delete_lab_namespaces() {
  delete_namespace "$GW_NS"
  delete_namespace "$CLIENT_NS"
  delete_namespace "$UPSTREAM_NS"
}

setup_network() {
  delete_lab_namespaces
  "$IP_BIN" netns add "$GW_NS"
  "$IP_BIN" netns add "$CLIENT_NS"
  "$IP_BIN" netns add "$UPSTREAM_NS"

  "$IP_BIN" link add opensurge-lab-client-veth type veth peer name opensurge-lab-gw-lan
  "$IP_BIN" link set opensurge-lab-client-veth netns "$CLIENT_NS"
  "$IP_BIN" link set opensurge-lab-gw-lan netns "$GW_NS"
  "$IP_BIN" link add opensurge-lab-gw-wan type veth peer name opensurge-lab-upstream-veth
  "$IP_BIN" link set opensurge-lab-gw-wan netns "$GW_NS"
  "$IP_BIN" link set opensurge-lab-upstream-veth netns "$UPSTREAM_NS"

  "$IP_BIN" netns exec "$GW_NS" "$IP_BIN" link set opensurge-lab-gw-lan name lan0
  "$IP_BIN" netns exec "$GW_NS" "$IP_BIN" link set opensurge-lab-gw-wan name wan0
  "$IP_BIN" netns exec "$CLIENT_NS" "$IP_BIN" link set opensurge-lab-client-veth name eth0
  "$IP_BIN" netns exec "$UPSTREAM_NS" "$IP_BIN" link set opensurge-lab-upstream-veth name eth0

  for namespace in "$GW_NS" "$CLIENT_NS" "$UPSTREAM_NS"; do
    "$IP_BIN" netns exec "$namespace" "$IP_BIN" link set lo up
  done
  "$IP_BIN" netns exec "$GW_NS" "$IP_BIN" link set lan0 up
  "$IP_BIN" netns exec "$GW_NS" "$IP_BIN" link set wan0 up
  "$IP_BIN" netns exec "$CLIENT_NS" "$IP_BIN" link set eth0 up
  "$IP_BIN" netns exec "$UPSTREAM_NS" "$IP_BIN" link set eth0 up

  "$IP_BIN" netns exec "$GW_NS" "$IP_BIN" addr add "$LAN_IP/24" dev lan0
  "$IP_BIN" netns exec "$GW_NS" "$IP_BIN" addr add "$UPSTREAM_GW_IP/24" dev wan0
  "$IP_BIN" netns exec "$UPSTREAM_NS" "$IP_BIN" addr add "$UPSTREAM_SERVER_IP/24" dev eth0
  FORWARDING_BEFORE="$($IP_BIN netns exec "$GW_NS" "$SYSCTL_BIN" -n net.ipv4.ip_forward)"
  [[ "$FORWARDING_BEFORE" == "0" ]] || die "new gateway namespace forwarding is not disabled"
  CLIENT_MAC="$($IP_BIN netns exec "$CLIENT_NS" "$IP_BIN" link show eth0 | awk '/link\/ether/ { print $2; exit }')"
  [[ -n "$CLIENT_MAC" ]] || die "could not read client MAC address"
}

start_upstream_fixtures() {
  "$OPENSSL_BIN" req -x509 -newkey rsa:2048 -nodes \
    -keyout "$RUNTIME_DIR/origin.key" \
    -out "$RUNTIME_DIR/origin.crt" \
    -subj "/CN=example.com" -days 1 >/dev/null 2>&1

	"$IP_BIN" netns exec "$UPSTREAM_NS" "$DNSMASQ_BIN" \
	    --no-daemon --no-resolv --no-hosts \
    --interface=eth0 --bind-interfaces --listen-address="$UPSTREAM_SERVER_IP" \
    --port=53 --address=/example.com/$UPSTREAM_SERVER_IP \
    --pid-file="$RUNTIME_DIR/upstream-dnsmasq.pid" \
    --log-queries --log-facility="$RUNTIME_DIR/upstream-dnsmasq.log" \
    >"$RUNTIME_DIR/upstream-dnsmasq.stderr" 2>&1 &
  UPSTREAM_DNS_PID=$!
  "$IP_BIN" netns exec "$UPSTREAM_NS" "$HELPER_BIN" \
    -mode origin -listen "$UPSTREAM_SERVER_IP:$ORIGIN_PORT" \
    -tls-cert "$RUNTIME_DIR/origin.crt" -tls-key "$RUNTIME_DIR/origin.key" \
    -log "$RUNTIME_DIR/origin.log" \
    >"$RUNTIME_DIR/origin.stderr" 2>&1 &
  ORIGIN_PID=$!
  "$IP_BIN" netns exec "$UPSTREAM_NS" "$HELPER_BIN" \
    -mode proxy -listen "$UPSTREAM_SERVER_IP:$PROXY_PORT" \
    -log "$RUNTIME_DIR/proxy.log" \
    >"$RUNTIME_DIR/proxy.stderr" 2>&1 &
  PROXY_PID=$!

  wait_for 20 gateway_fixtures_ready || die "upstream DNS/HTTPS fixture did not become ready"
}

gateway_exec() {
  "$IP_BIN" netns exec "$GW_NS" "$@"
}

client_exec() {
  "$IP_BIN" netns exec "$CLIENT_NS" "$@"
}

wait_for() {
  local attempts="$1"
  shift
  local attempt
  for ((attempt = 0; attempt < attempts; attempt++)); do
    if "$@"; then
      return 0
    fi
    sleep 1
  done
  "$@"
}

gateway_fixtures_ready() {
  gateway_exec "$CURL_BIN" -ksf --max-time 1 "https://$UPSTREAM_SERVER_IP:$ORIGIN_PORT/healthz" >/dev/null 2>&1 &&
    gateway_exec "$DIG_BIN" +short +time=1 +tries=1 "@$UPSTREAM_SERVER_IP" example.com A 2>/dev/null | grep -qx "$UPSTREAM_SERVER_IP"
}

client_dhcp_ready() {
  CLIENT_IP="$(client_exec "$IP_BIN" -4 -o addr show dev eth0 scope global 2>/dev/null | awk 'NR == 1 { split($4, value, "/"); print value[1] }' || true)"
  [[ "$CLIENT_IP" =~ ^192\.168\.50\.[0-9]+$ ]] || return 1
  local octet="${CLIENT_IP##*.}"
  ((octet >= 100 && octet <= 200)) || return 1
  awk -v mac="$CLIENT_MAC" -v ip="$CLIENT_IP" '$2 == mac && $3 == ip { found=1 } END { exit !found }' "$RUNTIME_DIR/dnsmasq.leases" 2>/dev/null || return 1
  grep -Eq '^dns=.*192\.168\.50\.1' "$RUNTIME_DIR/dhcp-evidence" 2>/dev/null
}

obtain_client_lease() {
  if [[ "$DHCP_CLIENT_BIN" == *"dhclient" ]]; then
    client_exec env "OPENSURGE_DHCP_EVIDENCE_FILE=$RUNTIME_DIR/dhcp-evidence" \
      "$DHCP_CLIENT_BIN" -4 -1 -v \
      -sf "$DHCP_SCRIPT" -lf "$RUNTIME_DIR/dhclient.leases" \
      -pf "$RUNTIME_DIR/dhclient.pid" eth0 >/dev/null 2>&1
	else
	    client_exec env "OPENSURGE_DHCP_EVIDENCE_FILE=$RUNTIME_DIR/dhcp-evidence" \
      "$DHCP_CLIENT_BIN" -n -q -i eth0 -s "$DHCP_SCRIPT" \
      -p "$RUNTIME_DIR/udhcpc.pid" >/dev/null 2>&1
  fi
  wait_for 20 client_dhcp_ready || die "client did not receive a DHCP lease"
}

assert_client_dns() {
  local answer
  answer="$(client_exec "$DIG_BIN" +short +time=2 +tries=1 "@$LAN_IP" example.com A 2>/dev/null)" ||
    die "client DNS query failed"
  if [[ "$LAB_MODE" == "tun" ]]; then
    [[ "$answer" == 198.18.* ]] || die "TUN DNS did not return a fake IP: $answer"
  else
    printf '%s\n' "$answer" | grep -qx "$UPSTREAM_SERVER_IP" ||
      die "client DNS returned unexpected answer: $answer"
  fi
}

client_without_proxy() {
  client_exec env \
    -u http_proxy -u https_proxy -u ftp_proxy -u all_proxy -u no_proxy \
    -u HTTP_PROXY -u HTTPS_PROXY -u FTP_PROXY -u ALL_PROXY -u NO_PROXY \
    "$@"
}

assert_client_nat() {
  local response
  response="$(client_without_proxy \
    "$CURL_BIN" --noproxy '*' --insecure --fail --show-error --max-time 15 \
    --resolve "example.com:$ORIGIN_PORT:$UPSTREAM_SERVER_IP" \
    "https://example.com:$ORIGIN_PORT/nat" 2>/dev/null)" || die "client HTTPS NAT request failed"
  printf '%s\n' "$response" | grep -qx "remote=$UPSTREAM_GW_IP" ||
    die "NAT endpoint observed an unexpected peer: $response"
}

assert_client_default_route() {
  client_exec "$IP_BIN" -4 route show default | grep -Eq \
    "^default via $LAN_IP dev " || die "client default route does not point to gateway $LAN_IP"
}

assert_no_explicit_proxy() {
  local proxy_env
  proxy_env="$(client_without_proxy env | grep -Ei '(^|_)(http|https|ftp|all|no)_proxy=' || true)"
  [[ -z "$proxy_env" ]] || die "client retained proxy environment: $proxy_env"
  if grep -Eiq '^[[:space:]]*(http|https|ftp|all|no)_proxy[[:space:]:=]' "$CONFIG_FILE"; then
    die "lab config contains an explicit client proxy setting"
  fi
  echo "no explicit proxy environment or client config observed"
}

mihomo_tun_ready() {
  local body
  body="$(gateway_exec "$CURL_BIN" -fsS --max-time 2 http://127.0.0.1:19090/configs 2>/dev/null)" || return 1
  printf '%s\n' "$body" | grep -Eq \
    '"tun"[[:space:]]*:[[:space:]]*\{[^}]*"enable"[[:space:]]*:[[:space:]]*true[^}]*"device"[[:space:]]*:[[:space:]]*"opensurge-tun"'
}

assert_mihomo_tun_ready() {
  wait_for 20 mihomo_tun_ready || die "mihomo did not report an enabled opensurge-tun device"
  echo "mihomo TUN ready: opensurge-tun"
}

assert_tun_connection_log() {
  local log_file="$RUNTIME_DIR/logs/mihomo.log"
  local attempt
  for ((attempt = 0; attempt < 20; attempt++)); do
    if [[ -f "$log_file" ]] && grep -Fq 'example.com:443' "$log_file"; then
      echo "transparent TUN log observed for example.com:443"
      return 0
    fi
    sleep 1
  done
  echo "mihomo did not log transparent TUN traffic for example.com:443" >&2
  tail -80 "$log_file" >&2 2>/dev/null || true
  return 1
}

assert_tun_flow() {
  local response
  assert_mihomo_tun_ready
  assert_client_default_route
  assert_client_dns
  assert_no_explicit_proxy
  response="$(client_without_proxy \
    "$CURL_BIN" -q --dns-servers "$LAN_IP" --insecure --fail --show-error --max-time 15 \
    "https://example.com/" 2>/dev/null)" || die "client no-proxy TUN HTTPS request failed"
  printf '%s\n' "$response" | grep -qx "OpenSurge Linux lab origin" ||
    die "TUN HTTPS request did not reach the controlled origin: $response"
  printf '%s\n' "$response" | grep -qx "remote=$UPSTREAM_GW_IP" ||
    die "TUN endpoint observed an unexpected peer: $response"
  assert_tun_connection_log || die "TUN HTTPS request had no mihomo connection log evidence"
}

assert_nft_loaded() {
  gateway_exec "$NFT_BIN" list table inet opensurge >/dev/null 2>&1 || die "OpenSurge nftables table is not loaded"
}

assert_forwarding_restored() {
  local current
  current="$(gateway_exec "$SYSCTL_BIN" -n net.ipv4.ip_forward)"
  [[ "$current" == "$FORWARDING_BEFORE" ]] ||
    die "IPv4 forwarding remained $current; expected $FORWARDING_BEFORE"
}

assert_cleanup() {
  if gateway_exec "$NFT_BIN" list table inet opensurge >/dev/null 2>&1; then
    die "OpenSurge nftables table remained after cleanup"
  fi
  [[ ! -e "$RUNTIME_DIR/state.json" ]] || die "runtime state remained after cleanup"
  assert_forwarding_restored
  echo "Linux lab cleanup verified: nftables table absent, forwarding restored, runtime state removed"
}

start_gateway() {
  local path="$1"
  env PATH="$path" "$IP_BIN" netns exec "$GW_NS" "$OPENSURGE_BIN" start --config "$CONFIG_FILE" \
    >"$RUNTIME_DIR/start-$LAB_MODE.log" 2>&1
}

stop_gateway() {
  if [[ -e "$RUNTIME_DIR/state.json" ]]; then
    gateway_exec "$OPENSURGE_BIN" stop --config "$CONFIG_FILE" \
      >"$RUNTIME_DIR/stop-$LAB_MODE.log" 2>&1 || true
  fi
}

write_failing_nft_path() {
  local fail_path="$RUNTIME_DIR/failing-bin"
  mkdir -p "$fail_path"
  cat >"$fail_path/nft" <<EOF
#!/usr/bin/env bash
if [[ "\$1" == "--file" ]]; then
  echo "intentional linux-lab nft load failure" >&2
  exit 42
fi
exec "$NFT_BIN" "\$@"
EOF
  chmod 0755 "$fail_path/nft"
  printf '%s:%s\n' "$fail_path" "$ORIGINAL_PATH"
}

run_success_case() {
  write_config "$LAB_MODE"
  start_gateway "$ORIGINAL_PATH"
  assert_nft_loaded
  obtain_client_lease
  if [[ "$LAB_MODE" == "tun" ]]; then
    assert_tun_flow
  else
    assert_client_dns
    assert_client_nat
  fi
  stop_gateway
  assert_cleanup
}

run_rollback_case() {
  local failing_path
  write_config "$LAB_MODE"
  failing_path="$(write_failing_nft_path)"
  if start_gateway "$failing_path"; then
    die "expected the deliberately failing nft load to reject gateway start"
  fi
  assert_cleanup
}

collect_artifacts() {
  local source
  [[ -n "$ARTIFACT_DIR" ]] || return 0
  [[ -d "$RUNTIME_DIR" ]] || return 0
  cp -a "$RUNTIME_DIR/." "$ARTIFACT_DIR/" 2>/dev/null || true
  if namespace_exists "$GW_NS"; then
    gateway_exec "$IP_BIN" -j addr show >"$ARTIFACT_DIR/gateway-addresses.json" 2>&1 || true
    gateway_exec "$IP_BIN" -j route show >"$ARTIFACT_DIR/gateway-routes.json" 2>&1 || true
    gateway_exec "$NFT_BIN" list ruleset >"$ARTIFACT_DIR/gateway-ruleset.txt" 2>&1 || true
  fi
  if namespace_exists "$CLIENT_NS"; then
    client_exec "$IP_BIN" -j addr show >"$ARTIFACT_DIR/client-addresses.json" 2>&1 || true
    client_exec "$IP_BIN" -j route show >"$ARTIFACT_DIR/client-routes.json" 2>&1 || true
  fi
  if namespace_exists "$UPSTREAM_NS"; then
    "$IP_BIN" netns exec "$UPSTREAM_NS" "$IP_BIN" -j addr show >"$ARTIFACT_DIR/upstream-addresses.json" 2>&1 || true
  fi
  source="${LAB_MODE:-unknown}"
  printf 'mode=%s\n' "$source" >"$ARTIFACT_DIR/result.txt"
  echo "Linux lab artifacts: $ARTIFACT_DIR"
}

cleanup() {
  set +e
  stop_gateway
  [[ -n "$UPSTREAM_DNS_PID" ]] && kill "$UPSTREAM_DNS_PID" 2>/dev/null || true
  [[ -n "$ORIGIN_PID" ]] && kill "$ORIGIN_PID" 2>/dev/null || true
  [[ -n "$PROXY_PID" ]] && kill "$PROXY_PID" 2>/dev/null || true
  delete_lab_namespaces
  [[ -n "$RUNTIME_DIR" && -d "$RUNTIME_DIR" ]] && rm -rf "$RUNTIME_DIR"
}

on_exit() {
  local status=$?
  set +e
  collect_artifacts
  cleanup
  exit "$status"
}

run_lab() {
  LAB_MODE="$1"
  make_runtime
  write_dhcp_client_script
  build_binaries
  setup_network
  start_upstream_fixtures
  run_success_case
  run_rollback_case
}

usage() {
  cat >&2 <<'EOF'
Usage: tests/linux-lab/lab.sh test|test-tun

Runs the root-required Linux network-namespace gateway lab. The lab creates
opensurge-lab-gw, opensurge-lab-client, and opensurge-lab-upstream and removes
them on exit.
EOF
}

main() {
  case "${1:-}" in
    test|test-tun) ;;
    *) usage; exit 2 ;;
  esac
  require_linux_root "$@"
  resolve_tools
  trap on_exit EXIT
  if [[ "$1" == "test-tun" ]]; then
    run_lab tun
  else
    run_lab off
  fi
}

main "$@"
