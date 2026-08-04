# Linux migration guide

This guide converts an existing configuration into a candidate for OpenSurge on
Linux. It does not perform an installation or change a live network.

## Platform contract

The supported target is Debian 12+ or Ubuntu 22.04+ on amd64 or arm64. The
installed product uses `iproute2` for interface and route inspection, nftables
for the OpenSurge-owned firewall table, and systemd service boundaries. GitHub
Releases provide the standalone `opensurge-install` asset and architecture-
matched `.deb` packages containing the gateway, LAN control service, Web GUI
assets, systemd units, and a pinned mihomo binary. The release installer is the
only supported package entry point: it obtains and verifies the matching `.deb`
with the release `SHA256SUMS`; direct `dpkg -i` and `apt install ./package.deb`
are intentionally rejected.

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
the other modes warn that IPv6 is not managed by OpenSurge. Do not add
`255.255.255.255/32` to mihomo's `route-exclude-address`: with Linux
auto-route/auto-redirect it can make nftables/netlink initialization fail with
`EEXIST`. Limited-broadcast service discovery is consequently unverified.

## Installation boundary

Migration is deliberately separate from installation. Run the release installer
from a session with a writable controlling TTY so it can display the generated
one-time `admin` password without logging it:

```sh
curl -fLO https://github.com/three-b0dy/OpenSurge-for-Linux/releases/latest/download/opensurge-install
sudo bash ./opensurge-install
```

Use `--version vX.Y.Z` for an exact release. For offline media, invoke the same
installer with `--deb /path/to/opensurge_<version>_<arch>.deb`; an adjacent
`SHA256SUMS` is mandatory unless `--checksums /path/to/SHA256SUMS` names the
verified file explicitly. The installer preserves an existing OpenSurge
configuration and administrator state. On a fresh host it creates a safe
`same_lan` control-plane configuration from the IPv4 default route's literal
interface name and source address, with DHCP and transparent mode off. It does
not infer a fresh isolated-LAN or same-Wi-Fi-DHCP topology.

For `isolated_lan`, create the downstream VLAN if needed and configure the
downstream IPv4 first. Then provide exact existing link names, the configured
LAN address, and CIDR to `opensurge-install --mode isolated_lan`; interfaces
such as `eth0`, `ens18`, and `enp1s0.50` are values, not aliases. A non-`/24`
CIDR also requires explicit ordered, in-CIDR DHCP range endpoints. The installer
does not add addresses, create VLANs, or alter the upstream router's DHCP
setting.

During installation, a running `systemd-resolved` may be disabled/stopped only
after the installer selects the first valid non-local pre-existing nameserver,
or the IPv4 default gateway as fallback, and snapshots `/etc/resolv.conf`.
It records and suppresses a generic `dnsmasq.service` instead of allowing the
distribution service to start. A fresh install refuses, but never kills, an
unknown TCP or UDP port-53 listener. State needed for restoration is root-only
under `/var/lib/opensurge/install-state/`; diagnostics are in
`/var/log/opensurge-install.log`. Installer failure, package removal, and purge
restore only state that the transaction recorded as OpenSurge-owned.

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
OpenSurge gateway.

Validate only after the manual review:

```sh
opensurge config validate --config /tmp/opensurge-candidate.yaml
```

Validation proves configuration semantics and rendering constraints. It does
not replace host-network evidence: run `sudo -v && make linux-lab-test` for
DHCP, DNS, forwarding, NAT and rollback, and
`sudo -v && make linux-lab-test-tun` for the transparent TUN path. Both gates
require a Linux host with root network-namespace support.
