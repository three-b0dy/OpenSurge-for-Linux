# Linux migration guide

This guide converts an existing configuration into a candidate for OpenSurge on
Linux. It does not perform an installation or change a live network.

## Platform contract

The supported target is Debian 12+ or Ubuntu 22.04+ on amd64 or arm64. The
Linux foundation uses `iproute2` for interface and route inspection, nftables for
the OpenSurge-owned firewall table, and systemd as the later service direction.
Debian packages and installed gateway systemd units are not implemented in this
phase.

The supported gateway modes are:

- `isolated_lan`: requires a second wired NIC or a VLAN. OpenSurge owns the
  isolated downstream IPv4, DHCP, DNS, forwarding, and NAT path.
- `same_lan`: uses the same interface for the selected side-gateway path and
  requires OpenSurge DHCP to be disabled.
- `same_wifi_dhcp`: uses the same interface with DHCP enabled only after the
  operator explicitly confirms that the upstream router DHCP service is off.

IPv4 is the supported gateway protocol. mihomo TUN with automatic route and
redirect is the only transparent path; `redir_port`, REDIRECT, and TPROXY are
not migration targets. `isolated_lan` drops downstream IPv6 forwarding, while
the other modes warn that IPv6 is not managed by this foundation.

## Candidate migration

Run the CLI with the old source file:

```sh
opensurge config migrate --config /path/to/source.yaml > /tmp/opensurge-candidate.yaml
```

The command reads the source, writes candidate YAML to stdout, and writes
mapping notes to stderr. It never writes or replaces a file. A non-zero status
means the source could not be read or decoded.

The candidate removes obsolete platform keys, sets Linux defaults, keeps
`mihomo.profile` and `device_policy` sections, and resets any non-zero
`mihomo.redir_port` to zero with a note. Treat the output as a review artifact,
not as an applied configuration.

## Required manual mapping

Before using the candidate, inspect and map:

1. `gateway.interface` to the Linux downstream interface. `isolated_lan`
   requires a separate wired interface or VLAN from the upstream interface.
2. `gateway.upstream_interface` to the interface carrying the upstream route.
3. `management.listen` to an explicit non-loopback IPv4 address on the LAN.
4. `gateway.mode` and the DHCP settings to the actual topology.
5. `transparent.tun_device` and TUN settings if TUN is enabled.

The migration command does not disable or re-enable the upstream router's DHCP
service. In `same_wifi_dhcp`, the operator must perform and record that router
DHCP change separately, and must restore it separately after stopping the
future gateway lifecycle.

Validate only after the manual review:

```sh
opensurge config validate --config /tmp/opensurge-candidate.yaml
```

Validation proves configuration semantics and rendering constraints. It does
not prove real DHCP, DNS, forwarding, NAT, TUN traffic, or rollback; those need
the Linux lab gates implemented in a later phase.
