# Linux traffic policy boundary

OpenSurge keeps mihomo in `mode: rule` so gateway traffic is not changed by a
desktop-only policy switch. Transparent traffic enters through the managed TUN
device, while source-scoped rules preserve local and private IPv4 reachability
before device policy and imported profile rules are evaluated.

The Linux CLI does not expose a separate local-routing command. Policy changes
must use the documented gateway/control-plane interfaces and remain distinct
from the three gateway topology modes:

- `isolated_lan` uses a separate wired interface or VLAN for downstream clients.
- `same_lan` keeps the upstream DHCP service and does not generate OpenSurge DHCP
  leases.
- `same_wifi_dhcp` requires explicit confirmation that the upstream router DHCP
  service is disabled before takeover.

The TUN path is the only transparent path in this foundation. `redir_port`
remains zero, and IPv4 local/private destinations are protected from accidental
proxy selection. Imported profiles may add proxy groups and rules but may not
override gateway-owned TUN, DNS, controller, or listener fields.

Unit tests cover policy composition and selector validation. Host-network
claims require the later Linux lab gate; `go test ./...` alone is not traffic
evidence.
