# Linux TUN transparent proxy

Linux transparency uses mihomo TUN as the single supported path. The managed
configuration enables automatic route and redirect behavior, and keeps
`mihomo.redir_port` at zero. No parallel redirect or policy path is part of the
foundation contract.

The gateway owns the configuration values required for TUN. Imported profiles
may supply proxy and rule content but may not override those gateway-owned
fields. A real TUN traffic claim requires the Linux TUN lab gate.
