#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
unit_dir="$repo_root/packaging/systemd"

for unit in \
  opensurge-gateway.service \
  opensurge-gateway.socket \
  opensurge-control.service; do
  test -f "$unit_dir/$unit"
done
test -f "$unit_dir/opensurge-control.service.d/security.conf"

grep -Fx 'User=opensurge' "$unit_dir/opensurge-control.service"
grep -Fx 'Group=opensurge' "$unit_dir/opensurge-control.service"
grep -Fx 'ExecStart=/usr/lib/opensurge/opensurge-control --config /etc/opensurge/config.yaml --gateway-socket /run/opensurge/gateway.sock' "$unit_dir/opensurge-control.service"
grep -Fx 'SocketMode=0660' "$unit_dir/opensurge-gateway.socket"
grep -Fx 'SocketGroup=opensurge' "$unit_dir/opensurge-gateway.socket"
grep -Fx 'ListenStream=/run/opensurge/gateway.sock' "$unit_dir/opensurge-gateway.socket"
grep -Fx 'Service=opensurge-gateway.service' "$unit_dir/opensurge-gateway.socket"

if grep -Eq '^(User|Group)=' "$unit_dir/opensurge-gateway.service"; then
  echo "gateway service must retain root identity" >&2
  exit 1
fi
grep -Fx 'ExecStart=/usr/lib/opensurge/opensurge-gateway --socket /run/opensurge/gateway.sock --config-root /etc/opensurge' "$unit_dir/opensurge-gateway.service"

security_conf="$unit_dir/opensurge-control.service.d/security.conf"
grep -Fx 'SupplementaryGroups=opensurge' "$security_conf"
grep -Fx 'NoNewPrivileges=true' "$security_conf"
grep -Fx 'PrivateTmp=true' "$security_conf"
grep -Fx 'ProtectSystem=strict' "$security_conf"
grep -Fx 'ReadWritePaths=/var/lib/opensurge /run/opensurge' "$security_conf"
grep -Fx 'CapabilityBoundingSet=' "$security_conf"
grep -Fx 'AmbientCapabilities=' "$security_conf"

if command -v systemd-analyze >/dev/null 2>&1; then
  systemd-analyze verify \
    "$unit_dir/opensurge-gateway.service" \
    "$unit_dir/opensurge-gateway.socket" \
    "$unit_dir/opensurge-control.service"
else
  echo "SKIP: systemd-analyze is unavailable; static unit assertions passed"
fi

echo "systemd unit assertions passed"
