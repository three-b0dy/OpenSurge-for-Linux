# Linux gateway lifecycle

The installed Linux lifecycle coordinates preflight, persisted startup state,
IPv4 forwarding, mihomo TUN readiness, dnsmasq when the selected mode needs it,
and the named OpenSurge nftables table. The root
`opensurge-gateway.service` owns these changes; the `opensurge` control service
uses its restricted Unix socket for fixed privileged actions.

This lifecycle starts only after the release installer has completed its
separate host-ownership transaction. The standalone `opensurge-install` is the
supported package entry point; its marker-guarded package rejects direct
`dpkg`/APT installation and unattended upgrades. On a fresh host, the installer
uses the literal IPv4 default-route interface and source address to render only
a non-disruptive `same_lan` control-plane configuration. DHCP and transparent
mode stay off. It does not translate `lan0`/`wan0` example names, create a
VLAN, add addresses, or infer an isolated-LAN or same-Wi-Fi-DHCP topology.
Existing OpenSurge configuration and administrator state are preserved.

Start order is preflight, candidate artifact validation, persisted startup
state, IPv4 forwarding, mihomo, dnsmasq when the selected mode needs it, and
finally the OpenSurge nftables table. Stop and rollback run the inverse order
and remove only the named table. Existing forwarding state is restored exactly.

The three topology contracts are explicit: `isolated_lan` needs a second wired
interface or VLAN; `same_lan` disables OpenSurge DHCP; and `same_wifi_dhcp`
requires explicit confirmation that the upstream router DHCP service is off.
No lifecycle action silently changes that router setting.

The TUN renderer enables Linux `auto-route` and `auto-redirect`, but deliberately
does not exclude `255.255.255.255/32`: that exclusion can make netlink/nftables
setup fail with `EEXIST`. Limited-broadcast discovery is therefore outside the
verified gateway contract.

Unit tests cover ordering and persisted state. `make linux-lab-test` provides
the Linux namespace evidence for DHCP, DNS, forwarding, NAT and rollback;
`make linux-lab-test-tun` additionally requires no-explicit-proxy HTTPS traffic
to appear in the mihomo TUN log. Both gates require a Linux host with root
network-namespace support.

Before this gateway lifecycle begins, the release installer performs the
separate host DNS ownership transaction described in
[Linux installer DNS ownership](linux-installer-lifecycle.md). It preserves
the prior resolver shape and only suppresses the generic host DNS services
whose previous state it records. A package/config/startup smoke, including a
responding Control API status endpoint, is not evidence that this gateway
lifecycle has carried downstream traffic; apply the Linux lab and real-host
validation boundary documented here.
