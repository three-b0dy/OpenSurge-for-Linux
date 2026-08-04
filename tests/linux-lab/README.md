# Linux namespace gateway lab

This is the Phase 2 data-plane gate for a Linux host. It creates three root
network namespaces:

```text
opensurge-lab-client -- 192.168.50.0/24 -- opensurge-lab-gw -- 198.51.100.0/24 -- opensurge-lab-upstream
```

The gateway namespace owns `lan0` (`192.168.50.1`) and `wan0`
(`198.51.100.1`). The client receives a DHCP lease from the OpenSurge
dnsmasq instance. The upstream namespace runs a controlled DNS answer, a
self-signed HTTPS origin, and a small HTTP CONNECT proxy. No public internet
is required for the assertions.

## Requirements

Run on Linux as root, or with a cached sudo credential so the script can
re-exec itself through `sudo -n`:

- `iproute2` (`ip` with network namespaces), `nft`, and `sysctl`;
- `dnsmasq`, `dig`, `curl` with `--dns-servers`, `openssl`, `go`;
- a DHCP client (`dhclient` or `udhcpc`); and
- a Linux mihomo binary in `PATH`, or `OPENSURGE_LAB_MIHOMO_BIN=/absolute/path`.

On macOS the Make targets stop before creating anything and report that Linux
network namespaces and root are required. A macOS/Lima lab is not a valid
result for these targets.

## Gates

```sh
sudo -v && make linux-lab-test
sudo -v && make linux-lab-test-tun
```

The regular gate checks the client DHCP lease, gateway DNS, direct HTTPS NAT,
the named `inet opensurge` table, normal stop cleanup, and rollback after a
deliberately failed nftables load. The TUN gate additionally checks that the
client default route and DHCP-advertised DNS point at the gateway, proxy environment variables
are removed and no client proxy setting is present, mihomo reports
`opensurge-tun` enabled through its loopback API, and an HTTPS request made
without an explicit proxy reaches the controlled origin. The TUN request must
also produce `example.com:443` in `logs/mihomo.log`, which is the transparent
connection evidence; the gate prints `transparent TUN log observed` before
cleanup. The controlled origin listens on port 443 in the upstream namespace.

Every run saves disposable configs, logs, namespace addresses/routes, and the
final ruleset under `artifacts/linux-lab/`. Namespace deletion and runtime
cleanup run from an EXIT trap, including after a failed assertion.
