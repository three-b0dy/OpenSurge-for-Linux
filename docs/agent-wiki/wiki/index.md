# OpenSurge for Linux agent context

OpenSurge is a Linux gateway and control-plane project for Debian 12+ and
Ubuntu 22.04+ on amd64 and arm64. The current phase establishes configuration,
Linux network inspection, sysctl forwarding, and an OpenSurge-owned nftables
table. The gateway lifecycle and installed systemd units are later-phase work.

## Required invariants

- Product name: OpenSurge for Linux; mihomo is the proxy engine.
- IPv4 is the supported gateway protocol in this phase.
- `transparent.mode: tun` is the only transparent path and `mihomo.redir_port`
  remains zero.
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
- `internal/controlapi` and `web`: reusable control-plane foundation.

## Verification boundaries

`make test` and `go test ./...` are unit/build checks. `make web-test` checks the
web package. Claims about DHCP, DNS, mihomo traffic, nftables forwarding, TUN,
rollback, or a real host network require the corresponding Linux lab gate once
that later-phase harness is available.

Use [docs/linux-migration.md](../../../docs/linux-migration.md) for migration
semantics and manual mapping requirements.
