# Per-device policy overlays

OpenSurge runs one mihomo process. Device policy adds selector groups and
source-address rules to that process; it does not start a process per device
or replace an imported profile.

## Configuration

```yaml
device_policy:
  file: ./devices.json
```

The JSON file may contain `devices`, `profiles`, `templates`, and `rule_sets`.
An empty file is valid. Device IPv4 addresses must be unique, inside the
gateway `/24`, and different from the network, broadcast, and gateway
addresses. `protected_ipv4` may reserve router or recovery addresses.

Each device has an id, optional display name, optional MAC, an IPv4 address,
an optional profile, and an `egress_mode`:

- `inherit_global` keeps device overrides first, then follows the imported or
  managed gateway rules and terminal `MATCH`.
- `dedicated` creates `device/<id>/default` for unmatched public traffic while
  keeping local/private destinations direct.

Rules can select existing policy groups or explicit built-ins such as `DIRECT`,
`REJECT`, `REJECT-DROP`, and `REJECT-TINYGIF`. A rule with `policies` creates
`device/<id>/<rule-id>`. The generated `device/` and
`open-surge-ruleset-` namespaces remain reserved; ordinary imported policy
groups, providers, profile choices, and device policies are preserved.

## Matching and validation

Domain, IP, protocol, port, and rule-set fields combine as documented by the
JSON schema. Source-address rules precede device overrides, and device rules
precede the gateway rules. Imported `MATCH` must remain terminal. Selector
rules fail closed for unsupported UDP by adding a matching `REJECT`, unless a
profile or rule explicitly requests `fallthrough`.

```sh
opensurge devices --config ./config.yaml --format json
opensurge device-policy-select --config ./config.yaml \
  --device alice-phone --slot default --policy Proxy
```

The selector command changes only the named device slot. Desired edits are
applied by the later gateway lifecycle; until then, the API reports validation
and lifecycle ownership instead of claiming a live network change.

## Identity limits

The current data plane matches IPv4 source addresses. DHCP-backed entries can
use a MAC-bound lease; shared-LAN entries may use a fixed IPv4 with optional
identity metadata. An observed neighbor or connection is evidence for review,
not a replacement for a DHCP lease. IPv6 device identity is not implemented.
