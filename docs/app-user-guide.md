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

The only supported Debian/Ubuntu entry point is the GitHub Release installer.
It downloads the architecture-matched package, verifies it against the
same-release `SHA256SUMS`, installs its prerequisites without allowing their
generic services to start, and then completes first-run configuration,
administrator initialization, and control-plane startup. Run it from a session
with a writable controlling TTY: the generated one-time administrator password
is displayed only there, so unattended installation is intentionally refused.

```sh
curl -fLO https://github.com/three-b0dy/OpenSurge-for-Linux/releases/latest/download/opensurge-install
sudo bash ./opensurge-install
```

The default is the latest Release. To select an exact release, pass its tag:

```sh
sudo bash ./opensurge-install --version vX.Y.Z
```

For an offline installation, keep `opensurge-install`, the exact
`opensurge_<version>_$(dpkg --print-architecture).deb`, and its `SHA256SUMS`
together. The installer never creates a checksum from a local package and has
no verification-bypass option. Use `--checksums /path/to/SHA256SUMS` only when
the verified checksum file is not adjacent to the package:

```sh
sudo bash ./opensurge-install \
  --deb ./opensurge_<version>_$(dpkg --print-architecture).deb \
  --checksums /media/opensurge/SHA256SUMS
```

Do not invoke `dpkg -i` or `apt install ./package.deb` directly, including for
upgrades. The package intentionally rejects that path in `preinst`, because it
would bypass the installer-owned dependency, DNS, port-53, and rollback
transaction.

On a fresh install, the installer derives a safe `same_lan` control-plane
configuration from the IPv4 default route: its exact Linux device name and
source address become the gateway and upstream interface, while DHCP and
transparent proxying remain off. `eth0`, `ens18`, `enp1s0.50`, bridges, and
VLANs are literal Linux link names; example names such as `lan0` and `wan0`
are never aliases. Existing `/etc/opensurge/config.yaml` and administrator
state remain unchanged.

For a first-time isolated LAN deployment, create the VLAN and configure the
downstream address before installation, then specify the actual topology:

```sh
sudo bash ./opensurge-install --mode isolated_lan \
  --downstream-interface enp2s0.50 \
  --upstream-interface enp1s0 \
  --lan-ip 192.168.50.1 \
  --lan-cidr 192.168.50.0/24
```

The two interfaces must be distinct; the LAN IPv4 must already be configured
on the downstream link. The installer neither creates VLANs nor adds addresses
nor infers the upstream. A non-`/24` isolated CIDR additionally requires
ascending, in-CIDR `--dhcp-range-start` and `--dhcp-range-end` values. It does
not infer a fresh `same_wifi_dhcp` topology.

The gateway is root-owned; the control service runs as the restricted
`opensurge` account and serves the Control API and GUI as HTTPS only on the
configured LAN `management.listen` IPv4 address. The installer creates the
single `admin` account and a ten-year RSA-3072 self-signed certificate, then
prints the one-time password only to the controlling TTY. Sign in and change
it immediately in the Web GUI; it is not written to installer logs, command
arguments, or environment variables. Accept the browser warning only after
verifying the displayed certificate fingerprint, or replace it with a validated
certificate/key pair whose SAN includes the listener IP:

```sh
sudo opensurge-setup replace-certificate \
  --cert /etc/opensurge/tls/custom-cert.pem \
  --key /etc/opensurge/tls/custom-key.pem
```

Use `sudo opensurge-setup reset-password --username admin` from a host with a
controlling TTY to recover administrator access.

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

The installer persists only the host state it may change in
`/var/lib/opensurge/install-state/` and records non-secret diagnostics in
`/var/log/opensurge-install.log`. If `systemd-resolved.service` is active, it
selects the first valid non-local pre-install `nameserver` (IPv4 or IPv6), or
the IPv4 default-route gateway when no such nameserver exists. Only after that
selection may it disable and stop `systemd-resolved` and replace
`/etc/resolv.conf` with a regular file. If neither source is valid, it stops
before dependency installation or resolver changes.

The installer records the generic `dnsmasq.service` state and disables/stops it
when needed so the distribution-wide service does not compete with OpenSurge.
It suppresses dependency-package autostart with a temporary installer-owned
policy and leaves a pre-existing policy alone. After these known services are
stopped, a fresh install refuses any remaining TCP or UDP port-53 listener and
reports its protocol, address, PID, and process name. It never kills or
reconfigures that unknown process.

On installer failure, package removal, or package purge, recovery stops
OpenSurge first and restores only resolver and generic-service state proved by
the root-only manifest. An invalid manifest is retained for manual recovery,
not trusted automatically. This ownership transaction is why direct package
installation is rejected.

## Evidence boundaries

`make test`, `make web-test`, `make installer-test`, and `make linux-ci-check`
are deterministic repository gates. A verified package installation, a valid
configuration, or a responding HTTPS status endpoint is a package/configuration
or startup smoke only; none proves downstream DHCP, DNS, forwarding, NAT,
rollback, or transparent traffic.

Run `sudo -v && make linux-lab-test` for Linux namespace DHCP/DNS/NAT/rollback
evidence, and `sudo -v && make linux-lab-test-tun` for no-explicit-proxy HTTPS
traffic evidenced in mihomo's TUN log. The TUN gate resolves DNS separately
with `dig` and pins the returned test address with `curl --resolve`; it does
not rely on curl's optional `--dns-servers` support. These Linux-root gates,
an Orb arm64 package build/install, and designated real-host QA acceptance are
separate evidence records and must not be claimed as run without their own
command output.
