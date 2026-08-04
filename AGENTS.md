# Agent guide

OpenSurge for Linux is a Linux gateway and control-plane project for Debian 12+
or later and Ubuntu 22.04 or later on amd64 and arm64. This repository is
building the configuration contract, Linux network primitives, mihomo
rendering, and the reusable Web/Control API foundation. The complete gateway
lifecycle and installed systemd units are later-phase work.

## Read first

1. Read `README.md` for the supported modes and current CLI workflow.
2. Read `docs/agent-wiki/wiki/index.md` for durable engineering context.
3. For gateway behavior, read the gateway lifecycle and validation-gate pages.
4. For configuration or transparent proxy changes, read
   `docs/linux-migration.md` and the relevant config tests.

## Linux product contract

- The product name is `OpenSurge for Linux`; mihomo is the proxy engine.
- Linux network ownership is built around nftables, iproute2, sysctl, and the
  later systemd service direction.
- `isolated_lan` needs a second wired NIC or VLAN. `same_lan` and
  `same_wifi_dhcp` require explicit DHCP and topology confirmation.
- IPv4 is the supported gateway protocol in this phase.
- mihomo TUN with automatic route/redirect is the only transparent path;
  `redir_port` remains inactive.
- OpenSurge may operate only its named nftables table and must not flush global
  firewall state or touch another table.
- `config migrate` produces a candidate on stdout and mapping notes on stderr;
  it never writes a file or changes upstream router DHCP.

The legacy PF package has been removed. PF is not a Linux product path and must
not be reintroduced; Linux gateway lifecycle code operates only the named
nftables table.

## Validation

`make test` is the default unit/build gate and is equivalent to `go test ./...`.
`make web-test` validates the Web package, and `make web-build` refreshes the
embedded Web assets.

These commands do not prove DHCP, DNS, forwarding, NAT, TUN traffic, rollback,
or a real host-network path. Use the later Linux lab gates when they exist and
state exactly which gate was run. If Go cannot write its cache, point
`GOCACHE` at a writable temporary directory.

## Wiki maintenance

Record durable decisions in `docs/agent-wiki/wiki/` and their stable sources
when appropriate. Do not add one-off logs, guesses, or ordinary TODOs.
