# OpenSurge for Linux user guide

OpenSurge is a Linux gateway control plane for Debian 12 or later and Ubuntu
22.04 or later on amd64 and arm64. Release `.deb` packages install the CLI,
root gateway data plane, unprivileged LAN HTTPS control plane, React Web GUI,
systemd units, and a pinned mihomo binary.

## First configuration

Start with `examples/config.example.yaml`. Choose one of these modes:

- `isolated_lan` needs a second wired NIC or VLAN for the downstream network.
- `same_lan` uses a shared interface and requires OpenSurge DHCP to be off.
- `same_wifi_dhcp` requires explicit confirmation that the upstream router
  DHCP service is off.

IPv4 is the supported gateway protocol. mihomo TUN is the only transparent
proxy path; keep `redir_port` at zero. The isolated mode drops downstream IPv6
forwarding. Other modes report IPv6 as unmanaged. The Linux TUN configuration
must not exclude `255.255.255.255/32`: this can cause auto-redirect's nftables
initialization to fail with `EEXIST`. Limited-broadcast discovery is not a
validated traffic path.

### Release-installer initial topology

On a fresh installation, the release installer reads the IPv4 default route
and generates a `same_lan` control-plane configuration using that exact Linux
link name and source IPv4. It uses the same link for gateway and upstream,
while keeping OpenSurge DHCP and transparent proxying off. It does not turn an
arbitrary single-NIC host into an isolated-LAN gateway and it never maps sample
names such as `lan0` or `wan0` to host interfaces.

Choose `isolated_lan` only with an explicit existing downstream link or VLAN,
an explicit upstream link, a LAN IPv4 already configured on the downstream
link, and its CIDR. The installer neither creates VLANs nor adds addresses nor
guesses the upstream. A non-`/24` isolated CIDR also requires explicit,
in-prefix DHCP range endpoints in ascending order.

## Install, initialize, and sign in

Install the matching GitHub Release package, review the configuration, then
initialize from a local TTY:

```sh
sudo dpkg -i opensurge_<version>_$(dpkg --print-architecture).deb
sudo opensurge-setup init --username admin
sudo systemctl enable --now opensurge-gateway.socket opensurge-control.service
```

The gateway is root-owned; the control service runs as the restricted
`opensurge` account and serves the Control API and GUI as HTTPS only on the
configured LAN `management.listen` IPv4 address. Initialization creates a
ten-year RSA-3072 self-signed certificate and an administrator account. Accept
the browser warning only after verifying the displayed certificate fingerprint,
or replace it with a validated certificate/key pair whose SAN includes the
listener IP:

```sh
sudo opensurge-setup replace-certificate \
  --cert /etc/opensurge/tls/custom-cert.pem \
  --key /etc/opensurge/tls/custom-key.pem
```

Use `sudo opensurge-setup reset-password --username admin` from a local TTY to
recover administrator access.

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

The gateway uses iproute2 for inspection, sysctl for IPv4 forwarding, dnsmasq
for the configured DHCP/DNS role, nftables for the named OpenSurge firewall
table, and systemd for service lifecycle. The gateway never flushes global
firewall state and restores its recorded forwarding state during stop/rollback.
Run `sudo -v && make linux-lab-test` for namespace DHCP/DNS/NAT/rollback
evidence, and `sudo -v && make linux-lab-test-tun` for no-explicit-proxy HTTPS
traffic evidenced in mihomo's TUN log.
