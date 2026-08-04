# Linux validation gates

`go test ./...` is the repository unit/build gate and `make web-test` is the
Web GUI package gate. Neither proves host networking.

The later Linux lab gate must use Debian/Ubuntu-compatible network namespaces
or an equivalent isolated topology to prove DHCP, DNS, IPv4 forwarding, NAT,
nftables ownership, mihomo TUN traffic, and rollback. It must keep an explicit
record of the interfaces, addresses, and commands used.

Configuration-only work is validated with `opensurge config validate` and the
candidate migration tests. `config migrate` never writes a file and never
changes an upstream router's DHCP state. A result that only passes unit tests
must not be described as real gateway or TUN evidence.
