# OpenSurge agent wiki

This directory is a small, hand-maintained context layer for future agents.
`sources/` contains durable source material; `wiki/` contains reviewed,
task-oriented summaries. Do not add one-off logs, guesses, or ordinary TODOs.

The current product contract is Linux: Debian 12+ or Ubuntu 22.04+, amd64 or
arm64, IPv4 gateway behavior, mihomo TUN, iproute2, nftables, sysctl, and the
later systemd lifecycle direction. The gateway lifecycle is not complete yet.

When a change moves a responsibility boundary, update the appropriate wiki
page and its stable source. Keep validation claims precise: unit tests and Web
tests do not prove DHCP, DNS, NAT, TUN traffic, rollback, or a real host-network
path.
