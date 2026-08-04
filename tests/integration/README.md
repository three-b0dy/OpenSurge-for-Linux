# Integration tests

The policy-control gate exercises the reusable Linux control-plane contract
against a live mihomo external controller without claiming a complete gateway
network path:

```sh
make policy-control-test
```

It renders an imported profile, starts mihomo with file and HTTP providers,
checks ordinary policies, policy selection, connections, provider refresh, and
the aggregate snapshot, then restarts mihomo to verify selected-policy
persistence. It also checks source-scoped local/private rules and device
policy overlays.

The gate does not prove DHCP, DNS, nftables forwarding, TUN traffic, rollback,
or a remote proxy exit. Those claims require the later Linux lab gates. Do not
enable a DHCP server on an ordinary home or office LAN during testing.
