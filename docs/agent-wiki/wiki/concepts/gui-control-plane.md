# Linux control plane

The Web GUI and Control API are reusable Linux control-plane foundations. They
must bind to the configured management IPv4 address and expose explicit
topology, configuration, device-policy, diagnostics, and gateway states.

The control plane may prepare and validate candidate configuration, but it must
not imply that a candidate was applied until the later Linux lifecycle reports
success. Configuration writes require revision checks and validation before an
active configuration changes.

The UI must show the three Linux modes, the second wired interface or VLAN
requirement for `isolated_lan`, the upstream DHCP confirmation requirement for
`same_wifi_dhcp`, and the unmanaged IPv6 warning for the supported IPv4-only
foundation. Migration remains candidate-only and never changes an upstream
router's DHCP service.

Use `make web-test` for the Web GUI package checks. These checks do not prove
real gateway traffic.
