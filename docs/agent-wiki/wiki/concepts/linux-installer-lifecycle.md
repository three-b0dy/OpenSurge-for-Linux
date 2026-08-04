# Linux installer DNS ownership

The release installer treats DNS handoff as a small, persisted host-ownership
transaction. It does not assume that an arbitrary port-53 listener belongs to
OpenSurge, and it does not replace a resolver unless it has a safe upstream to
write first.

The standalone `opensurge-install` asset is the sole supported Debian/Ubuntu
entry point. It must run as root with a writable controlling TTY, downloads or
accepts an offline architecture-matched package only after `SHA256SUMS`
verification, and uses the package-manager marker only for its own child
operation. The package rejects direct `dpkg -i`, `apt install ./package.deb`,
and unattended upgrade paths. The installer displays a generated one-time
`admin` password only on the controlling TTY; it never writes that password to
arguments, environment, state, or logs.

For a fresh host, the installer renders only `same_lan` from the IPv4 default
route's literal interface and source address, keeping DHCP and transparent mode
off. It preserves existing OpenSurge configuration and administrator state and
does not infer an isolated-LAN or same-Wi-Fi-DHCP topology. An isolated-LAN
run requires explicit existing downstream/upstream interface names, a
downstream address already configured on the host, and a CIDR (plus an explicit
DHCP range for non-`/24` networks); it never creates a VLAN or adds an address.

## Resolver handoff

When `systemd-resolved.service` is active, the installer selects the first
valid non-local `nameserver` from the effective `/etc/resolv.conf`. Both
unicast IPv4 and non-local IPv6 nameservers are eligible; loopback,
unspecified, multicast, and link-local addresses are not. If no eligible
nameserver exists, the installer uses only the parsed IPv4 `via` address from
the default route. If neither source is available, installation stops before
dependency installation or resolver modification.

The installer snapshots `/etc/resolv.conf` before replacing it. A symlink is
archived as a symlink, a regular file is copied with its metadata, and an
absent path is recorded as absent. The replacement is a regular file containing
the selected resolver. This preserves the pre-install representation for a
later restore rather than treating a symlink target as the original file.

## Service and package ownership

The installer first records whether `systemd-resolved.service` and the generic
`dnsmasq.service` were enabled and active. An active `systemd-resolved` is
disabled and stopped only after a safe resolver has been selected. Generic
`dnsmasq.service` is disabled and stopped after dependencies are installed;
an already disabled and inactive service is left untouched.

Package dependencies are installed under a temporary, marked
`/usr/sbin/policy-rc.d` only when the host does not already have that policy.
It returns `101` to suppress package autostart. A pre-existing policy is never
changed. The temporary policy is removed before the OpenSurge package is
installed, but only if its owner, mode, identity, and contents still match the
file created by the installer. A changed replacement is retained for the host
administrator to review.

## Port 53 boundary

After the known resolver and generic dnsmasq services are stopped, a fresh
install checks TCP and UDP listeners on port 53. Any remaining listener causes
an abort that reports protocol, local address, PID, and process name. The
installer never sends a stop or kill command to that listener. An upgrade is
identified before this check using existing OpenSurge package, state, or
configuration facts, so it does not reject an already-owned OpenSurge listener.

## Persisted recovery state

The state directory is `/var/lib/opensurge/install-state/` with mode `0700`.
Its root-only `manifest` is mode `0600` and contains only fixed, validated
fields: schema/installer version, transaction ID, install phase, original
service states, whether OpenSurge altered them, resolver backup kind/existence,
and the selected IP resolver or gateway fallback. Resolver content is never
placed in the manifest. `resolv.conf.before` holds the archived original form.

On an installer failure after the transaction begins, the recovery path stops
OpenSurge units first, restores the resolver representation, restores only the
recorded `systemd-resolved` and generic dnsmasq states, and then removes the
manifest and backup. The recovery log is `/var/log/opensurge-install.log`. A
missing state file makes repeated restoration a no-op; an invalid manifest is
not trusted and is retained for manual recovery.

Package remove and purge handling use the same restore rule: only state that
the manifest proves OpenSurge changed is restored. The installer does not
recreate a service that was originally disabled or inactive, and it does not
alter a pre-existing package policy.
