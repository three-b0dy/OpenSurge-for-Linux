# OpenSurge for Linux

OpenSurge is a Surge-style home-gateway control plane for Linux. This repository
currently establishes the configuration contract, mihomo rendering, Linux
network adapters, and an OpenSurge-owned nftables table. The complete gateway
lifecycle and installed systemd units are planned for a later phase.

## Supported scope

- Debian 12+ and Ubuntu 22.04+.
- amd64 and arm64.
- IPv4 is the supported gateway data-plane protocol.
- mihomo TUN is the only transparent-proxy path; `mihomo.redir_port` must stay
  at `0`.
- The firewall manager touches only the `inet opensurge` nftables table and
  never flushes the global ruleset.

## Gateway modes

| Mode | Use case | Constraint |
| --- | --- | --- |
| `isolated_lan` | Separate downstream network | Requires a second wired NIC or VLAN; OpenSurge provides downstream IPv4, DHCP, DNS, and NAT. |
| `same_lan` | Same-LAN side gateway | Uses one interface and requires DHCP to be disabled; only explicitly configured IPv4 paths are covered. |
| `same_wifi_dhcp` | Shared interface after upstream DHCP confirmation | Requires explicit confirmation that the router DHCP service is disabled; stopping OpenSurge never re-enables router DHCP for the operator. |

`isolated_lan` provides no downstream IPv6 configuration and drops downstream
IPv6 forwarding. The other modes warn about unmanaged IPv6 paths instead of
claiming that they were intercepted.

## CLI

```sh
go run ./cmd/opensurge config validate --config examples/config.example.yaml
go run ./cmd/opensurge config migrate --config /path/to/old-config.yaml > candidate.yaml
go run ./cmd/opensurge doctor --config examples/config.example.yaml
```

`config migrate` reads the source only, writes candidate YAML to stdout, and
writes human-required mapping notes to stderr. It never writes or overwrites a
file. Manually map the downstream interface, upstream interface, and management
listener IPv4 address, then run `config validate`. Migration does not change the
upstream router's DHCP state.

The default configuration path is `/etc/opensurge/config.yaml`. The default
runtime data directory is `/var/lib/opensurge`, with runtime sockets under
`/run/opensurge`.

## Development checks

```sh
make test
make web-test
make build
```

The current phase provides a reusable Control API/Web GUI foundation and Linux
network primitives. Debian packages, installed systemd gateway services, and
production deployment units are not implemented yet and must not be presented
as installable release artifacts. The planned Linux service foundation uses
nftables, iproute2, and systemd; gateway lifecycle integration and Linux lab
validation follow in later phases.

See [docs/linux-migration.md](docs/linux-migration.md) for the migration guide.
