# OpenSurge for Linux FAQ

## What platforms are supported?

The target is Debian 12+ or Ubuntu 22.04+ on amd64 or arm64. The repository
currently provides a Linux foundation rather than an installable gateway
service.

## Which topology should I choose?

Use `isolated_lan` with a second wired NIC or VLAN. Use `same_lan` when DHCP is
disabled and the host shares an interface with the existing LAN. Use
`same_wifi_dhcp` only after confirming that the upstream router DHCP service is
off. The project does not change that router's DHCP state during migration.

## How does transparent proxying work?

mihomo TUN with automatic route/redirect is the only supported transparent
path in this phase. `redir_port` must remain zero.

## Does migration apply changes?

No. `config migrate` emits a candidate YAML on stdout and mapping notes on
stderr. It never writes a file. Manually map interfaces and the management
listen address, then run `config validate`.

## Is a gateway service package available?

Not yet. nftables, iproute2, and the systemd service direction are being built;
the complete lifecycle and distribution packaging are later work.
