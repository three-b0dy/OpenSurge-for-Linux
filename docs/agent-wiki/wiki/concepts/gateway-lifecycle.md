# Linux gateway lifecycle

The current foundation does not yet own the complete gateway lifecycle. The
later Linux lifecycle will coordinate forwarding, mihomo TUN readiness,
dnsmasq, and the named OpenSurge nftables table.

Planned start order is preflight, candidate artifact validation, persisted
startup state, IPv4 forwarding, mihomo, dnsmasq when the selected mode needs
it, and finally the OpenSurge nftables table. Stop and rollback run the inverse
order and remove only the named table. Existing forwarding state must be
restored exactly.

The three topology contracts are explicit: `isolated_lan` needs a second wired
interface or VLAN; `same_lan` disables OpenSurge DHCP; and `same_wifi_dhcp`
requires explicit confirmation that the upstream router DHCP service is off.
No lifecycle action silently changes that router setting.

Unit tests cover ordering and persisted state. Real DHCP, DNS, forwarding,
NAT, TUN, and rollback claims require the Linux lab gate.
