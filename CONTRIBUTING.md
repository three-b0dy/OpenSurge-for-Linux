# Contributing to OpenSurge for Linux

Thanks for helping build a Linux gateway and control plane.

## Before changing code

- Read `README.md`, `AGENTS.md`, and the relevant pages in `docs/`.
- Keep changes scoped and preserve the configuration and network ownership
  contracts documented by the focused tests.
- Update user or agent documentation when a durable behavior or validation
  boundary changes.

## Upstream mirror

The `upstream` branch is a direct mirror maintained by the scheduled or manually
dispatched GitHub Actions workflow. It is updated with an exact-ref lease and is
the only branch that workflow may write; Linux branches are never rewritten and
the workflow does not publish releases.

## Verification

Run at least:

```sh
make test
make web-test
```

For network, TUN, DHCP, DNS, nftables, rollback, or lifecycle changes, report
the additional Linux validation gate that was actually run. Unit tests alone
do not prove a real host-network path.

Do not commit subscriptions, credentials, private network data, or unsanitized
logs. Keep commits small and describe any known limitation in the handoff.
