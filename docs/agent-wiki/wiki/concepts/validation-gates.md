# Linux validation gates

`go test ./...` (or `make test`) is the repository unit/build gate and
`make web-test` is the Web GUI package gate. `make installer-test` is the
non-destructive release-installer contract gate, while `make linux-ci-check`
checks the Linux package/repository surface. Disposable Debian/Ubuntu package
lifecycle tests separately cover the marker-guarded package, installer-driven
install/upgrade/remove/purge, and DNS-state recovery. None of these gates
proves host networking.

The later Linux lab gate must use Debian/Ubuntu-compatible network namespaces
or an equivalent isolated topology to prove DHCP, DNS, IPv4 forwarding, NAT,
nftables ownership, mihomo TUN traffic, and rollback. It must keep an explicit
record of the interfaces, addresses, and commands used.

Configuration-only work is validated with `opensurge config validate` and the
candidate migration tests. `config migrate` never writes a file and never
changes an upstream router's DHCP state. A result that only passes unit tests
must not be described as real gateway or TUN evidence.

The TUN namespace gate performs its DNS assertion with `dig`, then uses the
returned test address with `curl --resolve` for the HTTPS request. It does not
depend on curl builds supporting `--dns-servers`. A package/config/startup
smoke (including a successful unauthenticated HTTPS auth-status response) is
also not downstream traffic evidence.

Build and installation on Orb arm64, plus CLI/log acceptance on the designated
QA host, are separate physical-host evidence gates. Record the exact command,
host/topology, and output for each. Do not infer that any of these gates ran
from unit, static, package, or namespace-test results.
