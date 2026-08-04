# Automated Linux Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `opensurge-install` the sole supported Debian/Ubuntu installation path: it retrieves a verified architecture-matched release, safely takes ownership of DNS and generic dnsmasq, writes a safe first-run `same_lan` configuration, initializes the local `admin` account, and starts the LAN HTTPS control plane without exposing secrets.

**Architecture:** Ship a standalone Bash installer as a GitHub Release asset and use it as the transaction coordinator. It preflights the existing host, captures only system state it will mutate, suppresses package-service autostart during dependency installation, installs a marker-guarded `.deb`, configures OpenSurge, then either proves the HTTPS control listener or rolls back installation-owned DNS/service changes. Debian maintainer scripts enforce the controlled entry point and restore saved state after a completed removal; the Go setup binary gains a pipe-only password input for the installer while retaining existing interactive administrator operations.

**Tech Stack:** Bash, `apt-get`, `dpkg`, `systemctl`, `iproute2`, `ss`, SHA-256, GitHub Releases, Go 1.25, GitHub Actions, Debian/Ubuntu disposable test targets, Orb arm64 test VM.

## Global Constraints

- Support Debian 12+ and Ubuntu 22.04+ on `amd64` and `arm64` only. The supported command is `sudo bash ./opensurge-install`.
- Exact Linux link names (`eth0`, `ensX`, `enp*`, bridges and VLAN interfaces) are configuration values. `lan0` and `wan0` remain examples, not aliases or discovery targets.
- A fresh default install creates only `same_lan`: its default-route interface is both `gateway.interface` and `gateway.upstream_interface`; it uses that interface IPv4 as `gateway.lan_ip`, `dns.listen`, and `management.listen`; DHCP and transparent mode remain disabled. Do not infer isolated-LAN topology.
- `--mode isolated_lan` must require explicit downstream interface/VLAN, upstream interface, LAN IPv4 and LAN CIDR options. For non-/24 LANs it must additionally require an explicit DHCP range; the installer never adds host addresses or creates a VLAN. Existing `/etc/opensurge/config.yaml` and existing administrator state are never overwritten.
- The `.deb` must not declare `dnsmasq`, `nftables`, `iproute2`, `ca-certificates`, or `systemd` as runtime `Depends`. The controlled installer installs them, plus its transfer-client prerequisite, under temporary no-autostart policy.
- Direct `dpkg -i`, `apt install ./package.deb`, and unattended package upgrades must fail in `preinst` unless the installer marker is present. This is an operational guard, not a privilege boundary against root.
- Never kill or reconfigure an unknown port-53 listener. Inspect both TCP and UDP after stopping only installation-owned generic services and before a fresh package install.
- If `systemd-resolved` was active, the installer may disable and stop it, then change `/etc/resolv.conf` to a regular file containing the first pre-install valid non-local nameserver (IPv4 or IPv6) or, when absent, the IPv4 default gateway. It must record and restore only its own changes.
- The installer writes `/var/log/opensurge-install.log` with non-secret diagnostics. Do not log, export, persist, or pass the generated administrator password in a command argument. Require a controlling local TTY before displaying it.
- Preserve the ten-year self-signed certificate and later custom-certificate replacement behavior. Use mihomo TUN only; `mihomo.redir_port` remains zero.
- `make test`, `make web-test`, `make linux-ci-check`, package tests, `make linux-lab-test`, `make linux-lab-test-tun`, an Orb arm64 package build, and the designated QA host are separate evidence gates. Do not claim a gate passed without its actual output.

---

## Target File Structure

| Path | Responsibility |
| --- | --- |
| `scripts/opensurge-install` | Standalone Bash installer, release download/checksum verification, host preflight, transaction/rollback coordinator and first-run startup. |
| `tests/installer/opensurge-install_test.sh` | Stubbed, non-destructive installer contract tests for all preflight, release, state, rollback and secret-handling paths. |
| `cmd/opensurge-setup/main.go` | Adds a protected inherited-file-descriptor path for installer-created one-time passwords. |
| `cmd/opensurge-setup/main_test.go` | Tests parser and password-source rules without creating real credentials. |
| `packaging/debian/DEBIAN/{control,preinst,postinst,prerm,postrm}` | Marker guard, package-owned directories, service stop and idempotent resolver/dnsmasq restoration. |
| `packaging/debian/build-deb.sh` | Includes the new maintainer script in the deterministic package. |
| `tests/packages/install-deb.sh` | Disposable root lifecycle tests for guarded install, installer install/upgrade/remove/purge, and restoration. |
| `.github/workflows/release-linux.yml` | Publishes the standalone installer, both `.deb` files and matching `SHA256SUMS`. |
| `scripts/check-linux-repository.sh`, `Makefile` | Static/repository and shell-syntax gates for the new installer surface. |
| `README.md`, `docs/app-user-guide.md`, `docs/linux-migration.md`, `docs/agent-wiki/wiki/concepts/gateway-lifecycle.md` | User and future-agent installation, recovery, topology and ownership contract. |

## Transaction Contract

The implementation must make these phases explicit in `scripts/opensurge-install`; each transition writes a non-secret line to the installer log and is safe to repeat after a failed attempt.

1. Parse and validate exactly one source mode: online latest (default), `--version <vX.Y.Z>`, or `--deb <absolute-or-relative-path>`. Reject unsupported architecture, non-root execution, no local TTY, a non-Debian/Ubuntu host, missing required tools after bootstrap, and conflicting topology options before host networking changes.
2. Obtain the default-route device, source IPv4 and `via` IPv4 gateway using `ip -4 route`. Capture the exact pre-change resolver representation (including a symlink), select the first valid non-loopback/non-unspecified `nameserver` address (IPv4 or IPv6), and use the route gateway only when no usable resolver exists. Fail before package work if neither resolver nor gateway can be selected.
3. Determine whether this is a fresh install from package status and existing OpenSurge configuration/state. On fresh install, collect topology facts before package installation. On upgrade, preserve configuration/admin/DNS ownership and skip the port-53 rejection for an OpenSurge listener.
4. Write root-only `/var/lib/opensurge/install-state/manifest` and resolver backup before any resolver or generic-dnsmasq mutation. The manifest uses fixed keys and validated enum/IP values only; do not `source` host-controlled files. A missing resolver is recorded distinctly from a regular file and a symlink.
5. If needed, install a temporary `/usr/sbin/policy-rc.d` that exits `101`, with a unique owner marker and `trap` cleanup. Never overwrite a pre-existing policy file or symlink. Bootstrap `ca-certificates` and a transfer client as needed, then install `dnsmasq nftables iproute2 ca-certificates systemd` without service autostart.
6. Snapshot generic `dnsmasq.service`; explicitly `disable --now` it; stop/disable active `systemd-resolved` when present; replace `/etc/resolv.conf` only when OpenSurge changed resolved state. Restore the installer-owned policy before installing the OpenSurge package.
7. On a fresh install, use `ss -Hlnptu` (or equivalent `ss` invocations) to enumerate TCP and UDP port-53 listeners. Permit no remaining listener; report protocol, local address, PID and process name for each conflict and roll back without stopping it.
8. When a TLS-capable transfer client is already available, verify the release asset before the mutation sequence; when one is absent, the only allowed pre-verification mutation is a temporary no-autostart bootstrap of `ca-certificates` and `curl`. In both cases verify the asset before resolver/dnsmasq/OpenSurge package mutations. Invoke `apt-get install`/`dpkg` with the transient installer marker only for the package-manager child. `preinst` verifies the marker and does no mutation itself.
9. Atomically render and install the generated config only if no pre-existing config exists; create `admin` exactly once using an inherited pipe file descriptor; enable/start the socket and control service; query the unauthenticated HTTPS status endpoint on the configured LAN address until it responds.
10. On any failure after ownership capture, stop OpenSurge units, purge a package first installed by this failed transaction when safe to do so, undo only manifest-recorded resolver/dnsmasq changes, remove the temporary policy if it is still ours, and leave a concise failure plus the log path. Successful removal/purge performs the same idempotent restoration after OpenSurge is stopped; package upgrade does not restore host DNS mid-upgrade.

## Task 1: Add a testable, standalone installer skeleton and argument contract

**Files:**
- Create: `scripts/opensurge-install`
- Create: `tests/installer/opensurge-install_test.sh`
- Modify: `Makefile`
- Modify: `scripts/check-linux-repository.sh`

**Interfaces:**
- `sudo bash ./opensurge-install`
- `sudo bash ./opensurge-install --version vX.Y.Z`
- `sudo bash ./opensurge-install --deb /path/to/opensurge_X_arch.deb`
- `sudo bash ./opensurge-install --mode isolated_lan --downstream-interface <ifname> --upstream-interface <ifname> --lan-ip <IPv4> --lan-cidr <IPv4/prefix> [--dhcp-range-start <IPv4> --dhcp-range-end <IPv4>]`
- `make installer-test`

- [ ] **Step 1: Add initial black-box test harness before installer behavior**

Create a disposable test root, fake commands on `PATH`, a fake `/dev/tty`, and helpers which assert captured stdout, stderr, log content, commands and files. The harness must run the installer as a subprocess instead of sourcing it, so shell options and traps match production. Define deterministic test-only environment roots (`OPENSURGE_INSTALLER_ROOT`, `OPENSURGE_INSTALLER_BIN_DIR`, `OPENSURGE_INSTALLER_LOG`, `OPENSURGE_INSTALLER_TTY`) that production rejects or ignores unless an explicit test gate is set.

Cover parser failures first:

```bash
expect_fail --deb /tmp/a.deb --version v1.2.3
expect_fail --mode isolated_lan --downstream-interface lan.20
expect_fail --mode isolated_lan --downstream-interface lan.20 --upstream-interface eth0 --lan-ip 192.168.50.1
expect_fail --mode unsupported
expect_fail --version 'not a tag'
assert_not_contains "$captured_commands" 'apt-get'
assert_not_contains "$captured_commands" 'dpkg'
```

- [ ] **Step 2: Run the new test before the implementation exists**

Run: `bash tests/installer/opensurge-install_test.sh`

Expected: FAIL because the installer entry point and test target have not yet been implemented.

- [ ] **Step 3: Implement the standalone Bash entry point and safe diagnostics**

Use `#!/usr/bin/env bash`, `set -Eeuo pipefail`, `umask 077`, a single `main`, `die`, `log`, and a cleanup trap. Keep it self-contained so the released `opensurge-install` has no repository-relative dependency. Make all production paths absolute (`/etc`, `/var/lib/opensurge`, `/var/log`, `/usr/sbin`, `/run`) and provide only narrowly gated path overrides for tests.

Accept zero or one package source, validate `--version` as a tag-safe release identifier, validate mode/isolated arguments, and reject all unexpected positional arguments. Require root and an open writable controlling TTY before any mutation because the one-time password must be displayed locally. Emit a short terminal result; append timestamped, redacted diagnostics to `/var/log/opensurge-install.log` without `set -x` or generic command echoing.

Add `installer-test` to `Makefile`; add the installer and its tests to `bash -n`/static repository checks. Update the release repository check to assert the later release asset names rather than the old dependency model.

- [ ] **Step 4: Run parser, syntax and repository gates**

Run: `make installer-test && bash -n scripts/opensurge-install && make linux-ci-check`

Expected: PASS; tests prove invalid invocation exits before network/package/service commands and diagnostics never include a test secret.

- [ ] **Step 5: Commit installer foundation**

```bash
git add scripts/opensurge-install tests/installer/opensurge-install_test.sh Makefile scripts/check-linux-repository.sh
git commit -m "feat: add controlled Linux installer entry point"
```

## Task 2: Implement release asset resolution and mandatory checksum verification

**Files:**
- Modify: `scripts/opensurge-install`
- Modify: `tests/installer/opensurge-install_test.sh`

**Interfaces:**
- Latest online source resolves to `releases/download/<tag>/opensurge_<version>_<arch>.deb` and same-release `SHA256SUMS`.
- `--version` uses exactly the requested tag.
- `--deb /path/pkg.deb` requires an adjacent or explicitly supplied local `SHA256SUMS`; no bypass option exists.

- [ ] **Step 1: Add failing fixture cases for asset safety**

Use a local HTTP/file fixture only behind test overrides, with synthetic release redirects, package payloads and checksum files. Add assertions for:

```bash
expect_fail_when_checksum_missing
expect_fail_when_checksum_does_not_match
expect_fail_when_deb_name_does_not_match_arch
expect_fail_when_dpkg_deb_arch_is_wrong
expect_fail_when_online_url_is_not_https
assert_not_contains "$captured_commands" 'apt-get install'
```

The positive test must assert all three checks: selected filename exactly matches `opensurge_<version>_<dpkg-arch>.deb`, `dpkg-deb -f` reports that architecture, and `sha256sum --check` succeeds for the exact package entry.

- [ ] **Step 2: Implement release discovery/download without an unverified package path**

Default to `three-b0dy/OpenSurge-for-Linux`. Resolve latest by following the GitHub `releases/latest` redirect and extracting the release tag; use a supplied tag verbatim after validation. When a suitable TLS client and CA store already exist, download only HTTPS GitHub release URLs in production with `curl --fail --location --proto '=https' --tlsv1.2` into a temporary root-only directory before opening host-state ownership. If a client/CA store is missing, use the narrowly scoped no-autostart bootstrap described below, log that unavoidable prerequisite mutation, and still verify the release before resolver, generic-dnsmasq or OpenSurge package mutation. Download `SHA256SUMS` first, then the matching package, and reject duplicate/missing checksum lines or filenames with whitespace/control characters.

For an offline package, require `SHA256SUMS` beside the package by default and support only an explicit `--checksums <path>` override if it is needed for offline media. Canonicalize paths before use and require a regular readable checksum file. Never generate a checksum from the selected package or accept a `--no-verify` switch.

Bootstrap `ca-certificates` and `curl` only through the transaction's temporary no-autostart policy, before online fetches. Preserve an existing user `policy-rc.d`; log that it controlled package service policy. Make test overrides unavailable in a normal release invocation.

- [ ] **Step 3: Run verification tests including a forged-asset case**

Run: `make installer-test`

Expected: PASS; every forged, missing, wrong-name and wrong-architecture asset fails before the OpenSurge `.deb` is invoked, while a verified fixture yields one approved package-manager invocation.

- [ ] **Step 4: Commit verified release retrieval**

```bash
git add scripts/opensurge-install tests/installer/opensurge-install_test.sh
git commit -m "feat: verify Linux installer release assets"
```

## Task 3: Preflight topology and render a non-destructive first-run configuration

**Files:**
- Modify: `scripts/opensurge-install`
- Modify: `tests/installer/opensurge-install_test.sh`
- Modify: `examples/config.example.yaml` (only if comments must distinguish shipped isolated example from installer-generated `same_lan` defaults)
- Modify: `README.md`, `docs/app-user-guide.md`

**Interfaces:**
- The default fresh install discovers route facts via `ip -4 route get`/`ip -4 route show default` and writes `same_lan`.
- Existing config causes the installer to leave the file byte-for-byte unchanged.
- Isolated mode requires exact explicit interface and LAN IPv4 inputs.

- [ ] **Step 1: Add route/config fixture assertions**

Stub `ip` output for realistic `eth0`, `ens18`, `enp1s0.50` and bridge/VLAN names. Assert the generated configuration is valid and has these essential values:

```yaml
gateway:
  mode: "same_lan"
  interface: "ens18"
  upstream_interface: "ens18"
  lan_ip: "192.0.2.10"
dhcp:
  enabled: false
dns:
  listen: "192.0.2.10"
management:
  listen: "192.0.2.10:61767"
transparent:
  mode: "off"
```

Also assert no configuration is written when default-route device/source IPv4 is missing, the route has no `via` fallback and no existing non-local resolver, isolated inputs are incomplete, or an existing config is present.

- [ ] **Step 2: Implement topology collection and atomic config output**

Parse the kernel route output rather than mapping friendly aliases. Validate every discovered or supplied interface with `ip link show dev -- "$name"`; validate all IPv4 values as unicast IPv4. For isolated mode, require `--lan-ip` to be configured already on the explicit downstream interface, require it to fall in `--lan-cidr`, derive the conventional `.100`–`.200` pool only for a `/24`, and otherwise require an explicit in-prefix ordered `--dhcp-range-start`/`--dhcp-range-end`. Keep default route interface, source IP and gateway in variables only until the transaction manifest is created.

For a fresh default install, render the full package-runtime configuration into a root-only sibling temporary file, setting installed mihomo and runtime paths exactly as the package does, then `mv` it into `/etc/opensurge/config.yaml` after package creation. Run `opensurge config validate --config /etc/opensurge/config.yaml` before setup/start. Do not change an existing conffile, including one retained after an earlier package removal. For isolated mode, render the caller-provided exact interfaces, configured LAN IP/CIDR and validated DHCP range; it does not create a VLAN, configure the interface, or infer an upstream.

Document that the release installer chooses the default-route interface for the initial `same_lan` control plane only; it does not convert an arbitrary single-NIC host into an isolated LAN gateway. Keep `lan0`/`wan0` labelled as examples in the sample configuration and user documentation.

- [ ] **Step 3: Run generated-config tests and Go validation**

Run: `make installer-test && go test ./internal/config ./cmd/opensurge`

Expected: PASS; every generated configuration passes static config validation, preserves TUN/redirect constraints, and no existing configuration is rewritten.

- [ ] **Step 4: Commit topology-safe configuration**

```bash
git add scripts/opensurge-install tests/installer/opensurge-install_test.sh examples/config.example.yaml README.md docs/app-user-guide.md
git commit -m "feat: configure safe same-LAN installer defaults"
```

## Task 4: Capture DNS/service ownership, suppress package autostart, and detect port conflicts

**Files:**
- Modify: `scripts/opensurge-install`
- Modify: `tests/installer/opensurge-install_test.sh`
- Create: `docs/agent-wiki/wiki/concepts/linux-installer-lifecycle.md`

**Interfaces:**
- `/var/lib/opensurge/install-state/manifest` is root-owned `0600` and contains only fixed state fields.
- `/var/lib/opensurge/install-state/resolv.conf.before` preserves the original symlink or regular-file representation.
- Generic `dnsmasq.service` is disabled/stopped after dependencies are installed; an existing `policy-rc.d` is untouched.
- A fresh TCP or UDP port-53 conflict produces an actionable abort with listener ownership information.

- [ ] **Step 1: Add failing state-transition tests**

Build fake `systemctl`, `ss`, `apt-get`, `readlink`, `cp`, and filesystem fixtures to cover:

```bash
# active+enabled resolved; symlinked resolv.conf; external resolver selected
assert_manifest resolved_was_active=1
assert_manifest resolv_conf_kind=symlink
assert_regular_resolv_conf 'nameserver 9.9.9.9'

# preserve a pre-existing non-local IPv6 resolver rather than replacing it
assert_regular_resolv_conf 'nameserver 2001:4860:4860::8888'

# no usable nameserver falls back only to the parsed IPv4 default gateway
assert_regular_resolv_conf 'nameserver 192.0.2.1'

# never replace a resolver when neither approved source exists
expect_fail_without_package_or_resolver_mutation

# unknown UDP/TCP listener remains running and reports PID/command
expect_fail_port_53 'udp 0.0.0.0:53 users:(("unbound",pid=123,fd=6))'
assert_not_contains "$captured_commands" 'kill 123'
```

Add cases for an existing policy file, temporary policy restoration on `apt-get` failure, pre-existing disabled/inactive `dnsmasq`, and upgrade with an OpenSurge-owned listener (no fresh port-conflict rejection).

- [ ] **Step 2: Implement state recording and no-autostart dependency installation**

Create the install-state directory root-owned and mode `0700`; snapshot `/etc/resolv.conf` with archive semantics that preserve a symlink and store no resolver text in the manifest. Record separately whether the path was absent, a regular file or symlink; use a backup payload/path sufficient to restore exactly that form. The manifest must contain versioned, parseable fixed keys for: installer version/transaction ID; whether resolver/dnsmasq was altered; resolved enabled/active states; dnsmasq enabled/active states; resolver backup existence/type; selected non-local IP resolver (IPv4 or IPv6) or IPv4 gateway fallback; and install phase. Validate every parsed field before restoration.

If no `/usr/sbin/policy-rc.d` exists, install a marked temporary root-owned executable returning `101`; record its marker and remove it in the exit trap only if its contents/mode/owner still match the installer-created file. If one exists, leave it untouched. Run `apt-get update` and noninteractive `apt-get install` for `ca-certificates`, `curl`, `dnsmasq`, `nftables`, `iproute2`, and `systemd`, then explicitly verify `dnsmasq.service` is neither enabled nor active before proceeding.

Capture systemd enable state without letting expected `is-enabled` exit values terminate the script. When resolved is active, record its former state, run `systemctl disable --now systemd-resolved.service`, replace `/etc/resolv.conf` atomically with the selected non-local resolver or route-gateway IPv4, and mark resolver ownership. Snapshot and `disable --now dnsmasq.service`, marking only actual changes. Restore the temporary policy before OpenSurge package installation.

After those known services are stopped, enumerate `ss` TCP and UDP listeners for port 53. Format all remaining listener records with protocol, address, PID and process name; never issue a stop/kill command for them. Only run that check during a fresh installation; identify an upgrade from package/state/config facts before it executes.

- [ ] **Step 3: Implement and test idempotent rollback helpers**

Add explicit `rollback_host_state` and `restore_host_state` functions. They must stop OpenSurge units first, restore resolver file shape/content, restore only recorded dnsmasq/resolved enable/active states, remove the installation manifest/backup only after successful restoration, and tolerate repeated invocation. Rollback must preserve a pre-existing custom policy and never enable/start a service that the manifest says was originally disabled/inactive.

Run: `make installer-test`

Expected: PASS; all state permutations restore only what the installer changed, and a fake unknown resolver listener is still present after failure.

- [ ] **Step 4: Document the host-ownership boundary**

Document in the new agent-wiki page the resolver decision order, `systemd-resolved` and generic dnsmasq lifecycle, port-53 abort behavior, exact state location, recovery log, and remove/purge restoration rule. Link it from `docs/agent-wiki/wiki/index.md` and `docs/agent-wiki/wiki/concepts/gateway-lifecycle.md`.

- [ ] **Step 5: Commit transactional DNS ownership**

```bash
git add scripts/opensurge-install tests/installer/opensurge-install_test.sh docs/agent-wiki/wiki
git commit -m "feat: coordinate DNS ownership during installation"
```

## Task 5: Guard the Debian package and make removal/upgrade restoration correct

**Files:**
- Modify: `packaging/debian/DEBIAN/control`
- Create: `packaging/debian/DEBIAN/preinst`
- Modify: `packaging/debian/DEBIAN/postinst`
- Modify: `packaging/debian/DEBIAN/prerm`
- Modify: `packaging/debian/DEBIAN/postrm`
- Modify: `packaging/debian/build-deb.sh`
- Modify: `tests/packages/install-deb.sh`
- Modify: `scripts/check-linux-repository.sh`

**Interfaces:**
- `preinst install|upgrade` accepts only `OPENSURGE_INSTALLER_MARKER=<validated transient marker>` supplied by `opensurge-install`.
- Direct local package installation exits nonzero before package files, resolver or units are mutated and tells the operator to run `opensurge-install`.
- `postrm remove|purge` restores only manifest-owned host state; `prerm upgrade` must not restore it mid-upgrade.

- [ ] **Step 1: Extend package lifecycle tests before changing package scripts**

Refactor `tests/packages/install-deb.sh` for a disposable root target with command fixtures or add a companion test root. Assert all of these explicit outcomes:

```bash
expect_direct_dpkg_install_failure "$package"
assert_preinst_left_no_opensurge_files_or_system_state

run_controlled_installer_fixture "$package"
assert_dnsmasq_never_started_by_apt
assert_systemd_unit_disabled dnsmasq.service
assert_file_mode /var/lib/opensurge/install-state/manifest 600

run_controlled_upgrade_fixture "$package"
assert_no_resolver_restore_during_upgrade

dpkg -r opensurge
assert_original_resolv_conf_restored
assert_original_dnsmasq_and_resolved_states_restored
```

Test ordinary remove and purge independently. Include manifests proving that a pre-existing inactive/disabled resolver or dnsmasq service stays that way, and an idempotent postrm path does not fail after an installer rollback already restored state.

- [ ] **Step 2: Implement the marker-only `preinst` guard**

Make `preinst` POSIX `sh`, side-effect-free and narrow: for `install` and `upgrade`, validate a root-owned, mode-`0600`, transient marker file under `/run/opensurge/installer/` whose content carries a fixed format and current transaction ID, and require the matching environment marker supplied only to the package-manager child. The temporary policy prevents any generic service from inheriting a useful installation context. Reject missing/invalid marker with a concise `opensurge-install` instruction. Do not use the marker as an authorization boundary against root; delete it in the installer trap after the package manager completes.

Remove the runtime networking packages from `control` while retaining only package metadata actually needed by Debian package semantics. Update the description to explain that the package is installed through the verified installer and must not configure or start the gateway by itself.

- [ ] **Step 3: Implement lifecycle restoration without clobbering user choices**

Keep `postinst` limited to account/directory ownership, config mode and `daemon-reload`; it must not start or enable OpenSurge units. In `prerm`, stop OpenSurge units for remove/upgrade, but retain host DNS ownership during an upgrade. In `postrm`, use its own restricted restoration function for `remove` and `purge` (it must not depend on package payload files that may already be removed): validate manifest fields; restore resolver and generic service state if and only if the matching mutation flags are set; remove only installer-owned backup/manifest after success. On purge, then remove OpenSurge credentials/TLS as today. Treat missing state as normal and make the function safe under maintainer-script retry.

Add `preinst` to the builder's maintainer-script copy loop. Assert its mode after `dpkg-deb --contents`/unpack. Update static checks to require the guard and to forbid network-service `Depends` and postinst service starts.

- [ ] **Step 4: Build and run package lifecycle tests**

Run: `make deb ARCH=amd64 VERSION=0.0.0-installer && OPENSURGE_PACKAGE_TEST_ALLOW_HOST=1 sudo tests/packages/install-deb.sh artifacts/release/opensurge_0.0.0-installer_amd64.deb`

Expected: PASS in a disposable Debian/Ubuntu environment; direct install is rejected, controlled install/upgrade works, `dnsmasq.service` was never auto-started, and removal/purge restore only recorded pre-install state.

- [ ] **Step 5: Commit package safety boundaries**

```bash
git add packaging/debian tests/packages scripts/check-linux-repository.sh
git commit -m "build: guard OpenSurge package installation"
```

## Task 6: Initialize the administrator via a non-persistent inherited pipe and start services

**Files:**
- Modify: `cmd/opensurge-setup/main.go`
- Modify: `cmd/opensurge-setup/main_test.go`
- Modify: `scripts/opensurge-install`
- Modify: `tests/installer/opensurge-install_test.sh`
- Modify: `tests/packages/install-deb.sh`

**Interfaces:**
- Existing `opensurge-setup init --username admin` remains local-TTY interactive.
- New installer-only compatible form: `opensurge-setup init --username admin --password-fd <inherited-fd>`.
- The installer displays the generated one-time password only on its controlling TTY, then verifies `https://<detected-ip>:61767/api/v1/auth/status` before returning success.

- [ ] **Step 1: Add Go tests for password-source policy**

Add table tests covering: normal `init` still requires an interactive TTY; `--password-fd` accepts a readable inherited descriptor and no confirmation prompt; missing, negative, closed or non-pipe descriptors fail with a clear error; `--password-fd` is rejected for reset/certificate commands; and both password paths result in the same admin-store/TLS initialization behavior. Capture output and assert it never contains the supplied test password.

- [ ] **Step 2: Implement a tightly scoped `--password-fd` setup path**

Extend `setupOptions` with a password file-descriptor field valid only for `init`. When supplied, duplicate/read only that already-open descriptor, enforce a bounded single-line password format and length, reject terminal descriptors, and consume it without echoing it. Do not add a password command-line option, environment variable, state file, log field or systemd environment entry. Retain the current two-entry `term.ReadPassword` flow unchanged when no descriptor is supplied. Continue to call the existing self-signed-certificate and password-hash paths, preserving root/group ownership and ten-year certificate policy.

- [ ] **Step 3: Add installer startup and secret-redaction fixtures**

Use fake `opensurge-setup`, `systemctl`, `curl`, `ss`, and `/dev/tty` endpoints. Prove a fresh install invokes setup once as username `admin` with a descriptor—not password text—and that its secret appears exactly once in the fake TTY capture and nowhere in installer stdout/stderr/log/manifest/command trace. Prove an existing `admin.json` skips initialization and that failed setup/service health causes rollback.

- [ ] **Step 4: Implement first-run initialization and service proof**

After a controlled package installation and valid config, generate a cryptographically strong URL-safe one-time password in shell process memory. Print it only through the already-open controlling TTY after a clear warning to change it in the Web UI. Use an anonymous pipe duplicated to a dedicated inherited FD when invoking `opensurge-setup init --username admin --password-fd <fd>`, then immediately unset the shell variable and close descriptors. Never send it through an environment variable, argument, log, temp file or command substitution output.

When `admin.json` is absent, initialization is required; when it is present, skip it. Enable and start `opensurge-gateway.socket` and `opensurge-control.service` only after setup succeeds. Poll the configured non-loopback IPv4 HTTPS endpoint with certificate verification deliberately limited to availability (`curl --insecure` only for this newly self-signed certificate) and require a valid response from `GET /api/v1/auth/status`. If it fails, include `systemctl` status/journal diagnostics without secret data and roll back host ownership. Report the exact URL and one-time-password change instruction on success.

- [ ] **Step 5: Run setup, installer and package tests**

Run: `go test ./cmd/opensurge-setup ./internal/controlapi && make installer-test && OPENSURGE_PACKAGE_TEST_ALLOW_HOST=1 sudo tests/packages/install-deb.sh artifacts/release/opensurge_0.0.0-installer_amd64.deb`

Expected: PASS; a generated password is never persisted/logged/argument-visible, the HTTPS endpoint is live on the detected LAN address, and pre-existing credentials remain untouched during an upgrade.

- [ ] **Step 6: Commit safe first-run control-plane startup**

```bash
git add cmd/opensurge-setup scripts/opensurge-install tests/installer tests/packages
git commit -m "feat: initialize Linux installer administrator safely"
```

## Task 7: Publish the installer with release checksums and update CI package targets

**Files:**
- Modify: `.github/workflows/release-linux.yml`
- Modify: `tests/packages/install-deb.sh`
- Modify: `scripts/check-linux-repository.sh`
- Modify: `tests/installer/opensurge-install_test.sh`

**Interfaces:**
- Every tagged release contains `opensurge-install`, `opensurge_<version>_amd64.deb`, `opensurge_<version>_arm64.deb`, and `SHA256SUMS`.
- `SHA256SUMS` covers the installer and both package assets (the checksum manifest itself is not self-hashed).

- [ ] **Step 1: Add static workflow assertions**

Add tests that require release asset staging of `scripts/opensurge-install`, deterministic `SHA256SUMS` generation after both architecture artifacts are downloaded, and `gh release create` attachment of all three artifact classes. Assert the job no longer preinstalls `dnsmasq`, `nftables`, `iproute2`, `ca-certificates` or `systemd` merely to make the old direct package test pass; it must exercise the controlled installer in an isolated matching-architecture container/VM fixture instead.

- [ ] **Step 2: Update the release matrix**

Keep the amd64/arm64 build matrix and pinned mihomo staging. In the release job, download both packages, copy the exact repository installer into `release-assets/opensurge-install`, make it executable, calculate sorted `SHA256SUMS` for installer plus packages, and attach all files using `gh release create`. Ensure the release's architecture-independent installer gets no embedded local build path/version assumption; it discovers tags at run time or uses `--version`.

Change package test containers to install only the minimal package-manager/bootstrap prerequisites and invoke the checked-in installer in explicit test-fixture/offline mode. This must prove the package's guard is actually enforced; do not set the production marker directly in CI. Keep all package tests disposable and noninteractive with a fake or controlled TTY for generated credential output.

- [ ] **Step 3: Run workflow/repository validation**

Run: `make linux-ci-check && make installer-test && git diff --check -- .github/workflows/release-linux.yml`

Expected: PASS; static checks show all install assets are published and CI no longer relies on the removed Debian runtime dependency declaration.

- [ ] **Step 4: Commit release distribution changes**

```bash
git add .github/workflows/release-linux.yml tests/packages/install-deb.sh tests/installer/opensurge-install_test.sh scripts/check-linux-repository.sh
git commit -m "ci: publish verified OpenSurge installer"
```

## Task 8: Publish operator documentation and stable agent context

**Files:**
- Modify: `README.md`
- Modify: `docs/app-user-guide.md`
- Modify: `docs/linux-migration.md`
- Modify: `docs/agent-wiki/wiki/index.md`
- Modify: `docs/agent-wiki/wiki/concepts/gateway-lifecycle.md`
- Modify: `docs/agent-wiki/wiki/concepts/validation-gates.md`
- Create or modify: `docs/agent-wiki/wiki/concepts/linux-installer-lifecycle.md`

- [ ] **Step 1: Replace direct-package installation instructions**

Replace every supported `sudo dpkg -i ...`, manual `opensurge-setup init`, and manual unit-enable sequence in the README/user guide with:

```sh
curl -fLO https://github.com/three-b0dy/OpenSurge-for-Linux/releases/latest/download/opensurge-install
sudo bash ./opensurge-install
```

Document `--version`, `--deb` plus adjacent `SHA256SUMS`, the local-TTY requirement, generated `admin` credential display/change, initial `same_lan` choice, exact interface-name support, custom certificate replacement, and the explicit isolated-LAN invocation/requirements.

- [ ] **Step 2: Document DNS transition and recovery expectations**

Explain the approved resolver selection order (pre-existing non-local nameserver, else default IPv4 gateway); the authorized `systemd-resolved` disable/stop and regular `/etc/resolv.conf` replacement; generic dnsmasq ownership; port-53 conflict refusal; state/log locations; and automatic restoration on installer failure, package remove and package purge. State clearly that unknown port-53 services are never killed, existing config/admin state is untouched, and direct package installation is intentionally rejected.

- [ ] **Step 3: State validation boundaries accurately**

Keep operator commands for static tests, namespace gateway/TUN gates, Orb arm64 packaging, and real QA acceptance. Distinguish a package/config/startup smoke from actual downstream traffic evidence. Update agent wiki pages only with stable, reusable installation contract facts.

- [ ] **Step 4: Run documentation consistency checks**

Run: `! rg -n 'sudo dpkg -i|opensurge-setup init --username admin|enable --now opensurge-gateway.socket' README.md docs/app-user-guide.md && make linux-ci-check && git diff --check`

Expected: the search returns no obsolete supported install flow; repository checks and whitespace validation pass.

- [ ] **Step 5: Commit documentation**

```bash
git add README.md docs/agent-wiki/wiki docs/app-user-guide.md docs/linux-migration.md
git commit -m "docs: document automated Linux gateway installation"
```

## Task 9: Run layered verification and physical arm64 QA acceptance

**Files:**
- Modify only as required by defects discovered through the following evidence gates.

- [ ] **Step 1: Run local deterministic gates**

Run:

```bash
make test
make web-test
make installer-test
make linux-ci-check
make deb ARCH=amd64 VERSION=0.0.0-installer
```

Expected: PASS. Record exact commands/output and do not state that host networking was exercised by these gates.

- [ ] **Step 2: Run disposable Debian/Ubuntu package lifecycle gates**

Run the controlled installer/package test in fresh Debian 12+/Ubuntu 22.04+ amd64 environments, including all explicit scenarios: absent dependencies, active resolved symlink state, no pre-existing resolver with route-gateway fallback, generic dnsmasq state, external TCP/UDP 53 conflict, direct package rejection, fresh install, upgrade, remove, purge, and rollback after setup/listener failure.

Expected: PASS; capture package manager and installer log evidence with credentials redacted.

- [ ] **Step 3: Run Linux namespace gateway evidence**

Run:

```bash
sudo -v && make linux-lab-test
sudo -v && make linux-lab-test-tun
```

Expected: PASS. These retain the existing DHCP/DNS/NAT/rollback and no-explicit-proxy HTTPS TUN evidence; report separately from installer tests.

- [ ] **Step 4: Build and install on Orb arm64**

On `orb -m tproxy`, use the shared macOS workspace (no Git clone), build with the pinned arm64 mihomo binary and current Node/Go toolchain, and produce `opensurge_<version>_arm64.deb`. Verify architecture and checksum, then use the release installer in offline mode with its matching `SHA256SUMS` against the arm64 artifact. Capture `opensurge status`, `opensurge doctor`, systemd status, `/var/log/opensurge-install.log`, generated config and listener evidence. Never place the one-time password in test logs.

- [ ] **Step 5: Run the designated QA host CLI/log acceptance**

Copy only the verified arm64 package/checksum/installer to the QA host reachable at the current approved test address. The user performs Web GUI interactions; perform CLI-only checks: installer status, `opensurge config validate`, `opensurge doctor`, `opensurge status`, DNS/service state, generic dnsmasq state, resolver content, control listener, and `journalctl`/installer log redaction. Do not start an isolated topology on the QA host without explicitly supplied interface/VLAN topology. Report exact expected failures if host topology does not meet the generated config.

- [ ] **Step 6: Review, remediate, and commit evidence-driven fixes**

For each failed gate, use the systematic-debugging workflow: preserve the failing output, identify root cause, add a regression test before the fix, rerun the smallest relevant gate and then the full affected suite. Finish only when every required test has an actual result and docs match behavior.

## Coverage Review

| Approved requirement | Planned enforcement |
| --- | --- |
| One installer file, latest/version/offline | Tasks 1–2, 7; standalone release asset and required checksums. |
| amd64/arm64 GitHub `.deb` | Task 7 release matrix and Task 9 Orb arm64 build. |
| Exact link-name compatibility | Task 3 route parsing/validation and updated operator docs. |
| Safe default same-LAN configuration | Task 3 full generated config plus static validation. |
| Isolated LAN only with explicit topology | Tasks 1 and 3 parser/config rejection tests. |
| Auto dependency installation without generic dnsmasq service | Task 4 temporary `policy-rc.d`, explicit service disable/stop; Task 5 lifecycle tests. |
| Authorized resolved disable and resolver fallback | Task 4 state/backup/port test matrix and Task 5 restoration. |
| Unknown port-53 safety | Task 4 TCP/UDP inspection/refusal tests. |
| Reject direct `.deb` installation | Task 5 side-effect-free `preinst` and container proof. |
| Restore only owned host state | Tasks 4–5 idempotent manifest restoration on failure/remove/purge, not upgrade. |
| Simple admin authentication / one-time generated credential | Task 6 inherited password FD, existing admin hash store and Web UI password-change flow. |
| Ten-year self-signed TLS and replacement | Task 6 retains existing setup/certificate behavior; Task 8 documents replacement. |
| LAN Web GUI/control service startup | Task 6 starts/verifies the configured HTTPS status endpoint. |
| Required traffic/real-host evidence | Task 9 preserves namespace/TUN gates and executes arm64/QA CLI acceptance. |
