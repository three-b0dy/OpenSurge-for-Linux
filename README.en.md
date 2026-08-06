# OpenSurge for Linux

OpenSurge for Linux is a fork of [OpenSurge for Mac](https://github.com/YTwsy/OpenSurge-for-Mac),
with the goal of porting the complete OpenSurge feature set to Linux while
adapting the implementation to Linux networking and service boundaries. It is a
Surge-style home-gateway control plane for Linux. This repository establishes
the configuration contract, mihomo rendering, Linux network adapters, and an
OpenSurge-owned nftables table.

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

## Installation

The supported Debian/Ubuntu installation entry point is the GitHub Release
installer. It downloads and verifies the architecture-matched package and then
completes dependency installation, initial configuration, administrator setup,
and service startup.

Install the latest release with one `curl` command:

```sh
curl -fsSL https://github.com/three-b0dy/OpenSurge-for-Linux/releases/latest/download/opensurge-install | sudo bash
```

The installer requires a controlling TTY so it can display the one-time
administrator password.

## Development checks

```sh
make test
make web-test
make build
```

The current phase provides a reusable Control API/Web GUI foundation, Linux
network primitives, release packages, and installed systemd gateway services.
The Linux service foundation uses nftables, iproute2, and systemd.

See [docs/linux-migration.md](docs/linux-migration.md) for the migration guide.
