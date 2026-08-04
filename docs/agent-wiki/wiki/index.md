# OpenSurge for Linux agent context

OpenSurge is a Linux gateway and control-plane project for Debian 12+ and
Ubuntu 22.04+ on amd64 and arm64. The installed product owns configuration,
Linux network inspection, sysctl forwarding, dnsmasq when required, mihomo TUN,
an OpenSurge-owned nftables table, and systemd service boundaries. GitHub
Releases provide matching `.deb` packages.

## Required invariants

- Product name: OpenSurge for Linux; mihomo is the proxy engine.
- IPv4 is the supported gateway protocol.
- `transparent.mode: tun` is the only transparent path and `mihomo.redir_port`
  remains zero.
- Linux TUN uses `auto-route` and `auto-redirect`; do not add
  `255.255.255.255/32` to `route-exclude-address`, because it can make
  netlink/nftables setup fail with `EEXIST`. Limited-broadcast discovery is
  outside the verified gateway contract.
- `isolated_lan` requires a second wired interface or VLAN and drops downstream
  IPv6 forwarding. `same_lan` and `same_wifi_dhcp` warn about unmanaged IPv6.
- OpenSurge may change only its named `inet opensurge` nftables table. It must
  never flush global firewall state or alter another table.
- `config migrate` produces a candidate on stdout and notes on stderr. It never
  writes a source or destination file and never changes an upstream router's
  DHCP state.

## Repository map

- `internal/config`: Linux defaults, validation, rendering, and candidate migration.
- `internal/linuxnet`: fixed-command `iproute2` interface/address/neighbor parsing.
- `internal/nftables`: owned ruleset rendering and named-table management.
- `internal/sysctl`: `net.ipv4.ip_forward` observation and restoration.
- `internal/runtime`: Linux artifact paths and persisted runtime state.
- `internal/controlapi` and `web`: LAN HTTPS control plane and React GUI with
  single-administrator authentication.
- `cmd/opensurge-gateway`, `cmd/opensurge-control`, and `cmd/opensurge-setup`:
  root gateway daemon, restricted control service, and local administrator/TLS
  setup commands.
- `packaging`: `.deb` assembly and systemd service/socket units.

## Verification boundaries

`make test` and `go test ./...` are unit/build checks. `make web-test` checks the
web package. Claims about DHCP, DNS, mihomo traffic, nftables forwarding, TUN,
rollback, or a real host network require the corresponding Linux lab gate:
`make linux-lab-test` for DHCP/DNS/NAT/rollback, and
`make linux-lab-test-tun` for no-explicit-proxy HTTPS traffic observed in the
mihomo TUN log. Both gates require a Linux host with root network namespaces.

Use [docs/linux-migration.md](../../../docs/linux-migration.md) for migration
semantics and manual mapping requirements.

The release-installer resolver, service, port-53, and recovery contract is in
[Linux installer DNS ownership](concepts/linux-installer-lifecycle.md).
