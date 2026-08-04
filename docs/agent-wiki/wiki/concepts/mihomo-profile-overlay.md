# mihomo profile overlay

When an existing mihomo or Clash-style profile is imported, mihomo remains the
proxy engine while OpenSurge owns the Linux gateway overlay.

## Imported sections

`mihomo.profile_mode: imported` reads the configured profile and preserves its
ordinary `proxies`, `proxy-providers`, `proxy-groups`, `rule-providers`, and
`rules` sections. Relative provider paths are resolved from the imported
profile directory. OpenSurge does not rewrite the source snapshot; only the
generated runtime config may normalize YAML style.

The imported DNS section is merged field by field. OpenSurge retains the
resolver and filtering fields it can safely compose while owning the DNS/TUN
settings required by the Linux gateway contract.

## Gateway-owned fields

The overlay owns LAN binding, the external controller, selected-policy
persistence, DNS/TUN enablement, IPv4-only gateway behavior, and private-route
exclusions. The resulting config must keep `redir_port` inactive and must not
discard ordinary imported policy groups or providers.

Device policy is an independent overlay. It generates `device/<id>/...`
selectors and source-address rules while preserving normal imported policy
selection and provider behavior. The generated `device/` and
`open-surge-ruleset-` namespaces are reserved.

## Validation

Use `make test` for code-level coverage. `doctor` and
`opensurge validate-mihomo` render the final config and can run mihomo's own
validation when a binary is configured. The policy, provider, connection, and
snapshot commands inspect ordinary live control-plane resources; they do not
claim a complete gateway lifecycle.

If a change affects DNS, TUN, forwarding, or real proxy egress, use the
matching later Linux network gate and report the gate that actually ran.
