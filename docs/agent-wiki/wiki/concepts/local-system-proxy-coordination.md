# Local application routing boundary

OpenSurge for Linux does not manage an operating-system-wide application proxy.
Transparent interception uses mihomo TUN only, with automatic route and
redirect enabled when TUN mode is selected. `mihomo.redir_port` remains zero.

Applications that need a proxy must use the gateway's documented proxy or TUN
path. Control-plane state must not snapshot or mutate unrelated application
settings.
