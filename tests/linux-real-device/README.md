# Linux real-device smoke plan

`smoke.sh` is a Linux-only operator-input validator and read-only plan
printer. Its default behavior does not create VLANs, change host addresses or
routes, toggle forwarding, modify nftables, or start services. It is intended
to make the hardware inputs explicit before a separately reviewed procedure
performs a real gateway run.

The required inputs are:

```sh
UPSTREAM_IFACE=eno1
DOWNSTREAM_IFACE=eno2       # set this or DOWNSTREAM_VLAN, not both
LAN_CIDR=192.168.50.0/24
MODE=tun                    # isolated_lan, same_lan, same_wifi_dhcp, tun, off
```

For a VLAN downstream, use either a VLAN ID or its interface name:

```sh
UPSTREAM_IFACE=eno1 DOWNSTREAM_VLAN=eno1.50 \
LAN_CIDR=192.168.50.0/24 MODE=isolated_lan \
bash tests/linux-real-device/smoke.sh
```

The same-Wi-Fi DHCP mode is refused unless the upstream router DHCP service
has been disabled and the operator explicitly confirms it:

```sh
UPSTREAM_IFACE=wlan0 DOWNSTREAM_IFACE=eno2 \
LAN_CIDR=192.168.50.0/24 MODE=same_wifi_dhcp \
ROUTER_DHCP_DISABLED=confirmed bash tests/linux-real-device/smoke.sh
```

Without that confirmation the runner prints a warning and exits before any
network operation. Run `make linux-real-device-smoke --help` for the Make
entrypoint, or pass the variables above to `smoke.sh` for Linux-side
validation and a read-only plan. The help/validation path does not claim to
have run on macOS; actual hardware checks require a Linux host and operator
approval.

For transparent TUN evidence, use `make linux-lab-test-tun` on a Linux host
with root privileges and the required lab tools. That namespace gate removes
explicit proxy environment/configuration, checks the client default route and
gateway DNS, confirms mihomo reports `opensurge-tun` enabled, makes an HTTPS
request without an explicit proxy, and requires `example.com:443` in the
mihomo log before cleanup.
