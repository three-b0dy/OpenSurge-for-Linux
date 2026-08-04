# OpenSurge for Linux user guide

OpenSurge is a Linux gateway control plane for Debian 12 or later and Ubuntu
22.04 or later on amd64 and arm64. This phase provides the CLI, configuration
validation, candidate migration, Web GUI foundation, mihomo rendering, and
Linux network primitives. A packaged gateway service is not yet available.

## First configuration

Start with `examples/config.example.yaml`. Choose one of these modes:

- `isolated_lan` needs a second wired NIC or VLAN for the downstream network.
- `same_lan` uses a shared interface and requires OpenSurge DHCP to be off.
- `same_wifi_dhcp` requires explicit confirmation that the upstream router
  DHCP service is off.

IPv4 is the supported gateway protocol. mihomo TUN is the only transparent
proxy path; keep `redir_port` at zero. The isolated mode drops downstream IPv6
forwarding. Other modes report IPv6 as unmanaged by this phase.

## Validate and migrate

```sh
opensurge config validate --config /etc/opensurge/config.yaml
opensurge config migrate --config /path/to/source.yaml > candidate.yaml
```

Migration reads the source and writes a candidate YAML to stdout. Mapping notes
go to stderr. It never writes or replaces a file and never changes the
upstream router DHCP state. Review and manually map the downstream interface,
upstream interface, and non-loopback management address before validation.

## Network ownership

The Linux direction uses iproute2 for inspection, nftables for the named
OpenSurge firewall table, and systemd for the later service lifecycle. The
current commands do not promise DHCP, DNS, forwarding, NAT, rollback, or a
host-network result until the corresponding Linux lifecycle and lab gates are
implemented.
