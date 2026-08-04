# OpenSurge for Linux FAQ

## What platforms are supported?

The target is Debian 12+ or Ubuntu 22.04+ on amd64 or arm64. The repository
provides an installable Linux gateway and LAN control plane through
architecture-matched GitHub Release `.deb` packages.

## Which topology should I choose?

Use `isolated_lan` with a second wired NIC or VLAN. Use `same_lan` when DHCP is
disabled and the host shares an interface with the existing LAN. Use
`same_wifi_dhcp` only after confirming that the upstream router DHCP service is
off. The project does not change that router's DHCP state during migration.

## How does transparent proxying work?

mihomo TUN with automatic route/redirect is the only supported transparent
path. `redir_port` must remain zero. Do not add `255.255.255.255/32` to
`route-exclude-address`: it can make Linux auto-redirect initialization fail
with `EEXIST`. Limited-broadcast discovery is not yet a validated traffic path.

## Does migration apply changes?

No. `config migrate` emits a candidate YAML on stdout and mapping notes on
stderr. It never writes a file. Manually map interfaces and the management
listen address, then run `config validate`.

## How do I install and access the gateway?

Download the Release installer and run it from a session with a writable
controlling TTY:

```sh
curl -fLO https://github.com/three-b0dy/OpenSurge-for-Linux/releases/latest/download/opensurge-install
sudo bash ./opensurge-install
```

It verifies the matching `.deb`, creates a safe fresh `same_lan` control-plane
configuration when none exists, starts the services, and displays the one-time
`admin` password only on that TTY. Existing config and admin state are kept.
Use `--version vX.Y.Z` for an exact release, or `--deb /path/package.deb` with
its `SHA256SUMS` for offline media. Direct `dpkg -i` and `apt install
./package.deb` are deliberately rejected. The control plane serves HTTPS only
on the configured LAN `management.listen` address and uses a single
administrator login. The installer creates a ten-year self-signed certificate;
replace it later with `opensurge-setup replace-certificate --cert ... --key
...` using a certificate whose SAN includes the listener IP.
