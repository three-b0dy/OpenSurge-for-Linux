# Automated Linux installer design

## Goal

Provide one supported, non-interactive installation entry point for OpenSurge
on Debian and Ubuntu. It downloads a verified architecture-matched release,
installs its runtime prerequisites without starting the distribution dnsmasq
service, makes the host DNS state compatible with the gateway, creates a safe
first-run configuration, creates an administrator account, and starts the LAN
HTTPS control plane.

The user runs one command after downloading the release installer:

```sh
sudo bash ./opensurge-install
```

The installer also supports `--version <tag>` and `--deb <local-deb>`.

## Release assets and verification

Each GitHub Release publishes these assets:

- `opensurge-install`, an architecture-independent POSIX shell installer;
- `opensurge_<version>_amd64.deb` and `opensurge_<version>_arm64.deb`;
- `SHA256SUMS` containing every release asset hash.

For online installation the installer determines `dpkg --print-architecture`,
downloads the requested/latest release metadata, the matching package and its
same-release `SHA256SUMS`, then verifies the package name, declared Debian
architecture, and SHA-256 before changing host state. Offline `--deb` requires
the matching local `SHA256SUMS` file. It never provides a checksum-bypass flag.

## Interface selection and initial configuration

OpenSurge accepts exact Linux interface names; `eth0`, `ensX`, `enpXsY`, bridge
and VLAN names are already valid configuration values. `lan0` and `wan0` are
examples only and are never aliases for another interface.

On a fresh installation, the installer finds the IPv4 default-route interface
and its source address. It writes a `same_lan` initial configuration with that
exact interface as both gateway and upstream interface, DHCP disabled,
transparent mode off, and DNS/management listeners bound to the detected IPv4
address. It never overwrites an existing OpenSurge configuration.

An isolated topology is not inferred. `--mode isolated_lan` requires explicit
downstream interface/VLAN, upstream interface, and LAN IPv4 arguments. If the
default-route interface or source IPv4 cannot be found, installation stops
before installing the package.

## Dependency and service ownership

The package no longer declares runtime network services as Debian `Depends`:
doing so lets APT install and enable the generic dnsmasq service before
OpenSurge can establish ownership. The installer installs `dnsmasq`, `nftables`,
`iproute2`, `ca-certificates`, and `systemd` itself.

Before APT runs, it creates a temporary `policy-rc.d` only when none already
exists. The policy prevents package-maintainer scripts from starting services.
The installer restores or removes only the temporary policy it created, even on
failure. It then explicitly disables and stops `dnsmasq.service`; OpenSurge's
gateway process remains the sole owner of its runtime dnsmasq instance.

If an existing `policy-rc.d` is present, the installer preserves it and logs
that system policy. It still verifies that generic dnsmasq is disabled and
inactive before proceeding.

## DNS and port 53 transition

The installer captures, in order:

- the current `systemd-resolved` enable and active state;
- an exact backup of `/etc/resolv.conf`, including its symlink form;
- the first non-local nameserver in that file; and
- the IPv4 default gateway as fallback.

When `systemd-resolved` is active, the installer disables and stops it. It
writes a regular `/etc/resolv.conf` using the captured non-local nameserver, or
the default gateway if no usable nameserver existed. A missing fallback is a
hard failure before package installation.

After generic dnsmasq and resolved are stopped, the installer queries TCP and
UDP listeners on port 53. A remaining listener is reported with protocol,
address, PID and process name; installation stops without terminating an
unknown service. This check applies only to a fresh installation. An upgrade
does not reject OpenSurge's own active DNS listener.

## Package guard and lifecycle restoration

`preinst` rejects direct `dpkg -i` and `apt install ./package.deb` operations
unless the controlled installer marker is present. Its failure output points to
`opensurge-install`; it makes no host changes. The marker is passed only by the
installer to the package-manager child process.

Before changes, the installer writes root-owned state under
`/var/lib/opensurge/install-state/`. It records exactly which resolver and
generic dnsmasq state it changed plus the resolver backup path. On a failed
installation it stops any OpenSurge unit that started and restores these
recorded states. On remove or purge, the package lifecycle scripts likewise
restore only state changed by this installer. They do not modify pre-existing
user choices.

## Administrator and service startup

After a successful fresh package installation, the installer generates a
strong one-time password for username `admin`. It passes the password directly
to `opensurge-setup init`, stores only the resulting hash, and prints the
password once to the controlling local TTY. It is excluded from logs, command
arguments, state files, and systemd environment.

The installer enables and starts `opensurge-gateway.socket` and
`opensurge-control.service`, then verifies the HTTPS listener and reports its
LAN URL. The user changes the one-time password through the Web UI. Existing
administrator state is never replaced on upgrade.

## Diagnostics and error handling

The installer writes non-secret lifecycle and preflight information to
`/var/log/opensurge-install.log`: release/version/architecture, detected
interface, DNS backup location, service state transitions, command failures and
port-conflict diagnostics. It keeps the terminal result concise and never logs
the one-time password or upstream credentials.

Every mutation has a rollback boundary. Missing release assets, failed checksum
verification, failed APT operation, unavailable fallback DNS, port conflicts,
package-manager failure, setup failure, and service-listener failure stop the
flow and restore only installation-owned changes.

## Validation

- Unit and shell tests cover release asset selection, architecture checks,
  checksum enforcement, network-interface discovery, DNS selection, state
  capture/restoration, and secret redaction.
- Package tests prove direct package installation is guarded, generic dnsmasq
  does not start, resolved handling and port-53 conflict behavior are correct,
  automatic first-run configuration is safe, and removal/purge restores saved
  host DNS/service state.
- The existing namespace gates continue to validate standard and TUN gateway
  data paths.
- `orb -m tproxy` builds and tests the arm64 package, followed by a QA-host
  installation that verifies CLI, systemd status and installer logs. Web UI
  interaction remains user-operated.

## Non-goals

- Infer an isolated two-interface or VLAN topology.
- Kill or reconfigure an unknown process occupying port 53.
- Auto-install unsigned or checksum-unverified release assets.
- Expose, persist, or reuse the generated administrator password.
