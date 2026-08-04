# Linux Release, Validation, and Upstream Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver reproducible amd64/arm64 Debian packages, Linux CI/release validation, complete Linux documentation, and a daily direct mirror of the upstream macOS branch into `upstream`.

**Architecture:** Package OpenSurge binaries, web assets, systemd units, and pinned mihomo into architecture-specific `.deb` artifacts; declare dnsmasq/nftables/iproute2 as system dependencies. CI runs non-root checks on GitHub-hosted Ubuntu and package lifecycle checks in disposable targets, while network evidence remains an explicit Linux lab. A separate scheduled workflow moves only the `upstream` ref and never writes Linux `master`.

**Tech Stack:** GitHub Actions, Go cross-compilation, pnpm, `dpkg-deb`, Debian/Ubuntu containers or VMs, shellcheck, Git.

## Global Constraints

- Release `.deb` packages for amd64 and arm64 only, targeting Debian 12+ and Ubuntu 22.04+.
- Bundle checksum-pinned mihomo by architecture; install dnsmasq, nftables, iproute2, ca-certificates, and systemd through package dependencies.
- Package install must not start a gateway until the operator completes network configuration.
- Upgrade preserves `/etc/opensurge` and `/var/lib/opensurge`; ordinary remove preserves conffiles and purge removes credentials/certificates.
- `make linux-lab-test` and `make linux-lab-test-tun` are the mandatory evidence gates for applicable data-plane claims.
- `upstream` is bot-owned and mirrors `YTwsy/OpenSurge-for-Mac:master` daily at `03:17 UTC` or on manual dispatch. It never merges into Linux `master`.
- Only the mirror and release jobs receive `contents: write`.

---

## Target File Structure

| Path | Responsibility |
| --- | --- |
| `scripts/prepare-linux-release-deps.sh` | Download, verify, and stage mihomo for one GOARCH. |
| `packaging/debian/build-deb.sh` | Create an architecture-specific package staging tree and invoke `dpkg-deb`. |
| `packaging/debian/DEBIAN/{control,postinst,prerm,postrm}` | Package dependencies and install/remove/purge behavior. |
| `tests/packages/install-deb.sh` | Install, upgrade, remove, and purge assertions. |
| `.github/workflows/ci.yml` | Linux Go, web, and static checks. |
| `.github/workflows/release-linux.yml` | Tagged two-architecture package build, test, and upload. |
| `.github/workflows/sync-upstream.yml` | Daily/manual guarded ref mirror. |
| `docs/linux-installation*.md` | Operator installation, authentication, topology, and recovery guide. |
| `docs/agent-wiki/wiki/concepts/linux-*.md` | Linux lifecycle, TUN, and validation facts. |

### Task 1: Build checksum-pinned Linux release dependencies

**Files:**
- Create: `scripts/prepare-linux-release-deps.sh`
- Create: `tests/scripts/prepare-linux-release-deps_test.sh`
- Create: `packaging/debian/mihomo-checksums.txt`
- Modify: `Makefile`

**Interfaces:**
- Produces `runtime/release-tools/<arch>/mihomo`.
- Produces `make linux-release-deps ARCH=amd64` and `make linux-release-deps ARCH=arm64`.

- [ ] **Step 1: Write local-archive fixture assertions**

```sh
test -x "$staging/runtime/release-tools/amd64/mihomo"
test "$(sha256sum "$staging/runtime/release-tools/amd64/mihomo" | awk '{print $1}')" = "$expected_sha"
```

- [ ] **Step 2: Run the test before staging code exists**

Run: `tests/scripts/prepare-linux-release-deps_test.sh`

Expected: FAIL because the script and checksum manifest are absent.

- [ ] **Step 3: Implement architecture-safe dependency staging**

Accept only `amd64` and `arm64`; map them to exact published mihomo Linux archive names; use `curl --fail --location --proto '=https'`; verify an entry in `mihomo-checksums.txt`; reject an archive with an unexpected executable count; install only the expected binary mode `0755`. The test fixture may use `OPENSURGE_RELEASE_DEPS_TEST_URL=file://...`; production mode must reject a non-HTTPS URL.

- [ ] **Step 4: Run fixture and staging checks**

Run: `tests/scripts/prepare-linux-release-deps_test.sh && make linux-release-deps ARCH=amd64`

Expected: PASS; production staging prints the pinned version and SHA-256.

- [ ] **Step 5: Commit release dependency staging**

```bash
git add scripts/prepare-linux-release-deps.sh tests/scripts/prepare-linux-release-deps_test.sh packaging/debian/mihomo-checksums.txt Makefile
git commit -m "build: stage pinned Linux mihomo releases"
```

### Task 2: Create and test Debian package lifecycle

**Files:**
- Create: `packaging/debian/build-deb.sh`
- Create: `packaging/debian/DEBIAN/control`, `packaging/debian/DEBIAN/postinst`, `packaging/debian/DEBIAN/prerm`, `packaging/debian/DEBIAN/postrm`
- Create: `tests/packages/install-deb.sh`
- Modify: `Makefile`

**Interfaces:**
- Produces `artifacts/release/opensurge_<version>_<arch>.deb`.
- Produces `make deb ARCH=amd64 VERSION=<version>`.

- [ ] **Step 1: Write package lifecycle assertions**

```sh
dpkg -i "$package"
test -x /usr/bin/opensurge
test -f /lib/systemd/system/opensurge-control.service
test -d /etc/opensurge
dpkg -r opensurge
test -d /etc/opensurge
dpkg --purge opensurge
test ! -e /var/lib/opensurge/admin.json
```

- [ ] **Step 2: Run the package test before the builder exists**

Run: `sudo tests/packages/install-deb.sh artifacts/release/opensurge_0.0.0_amd64.deb`

Expected: FAIL because the package file does not exist.

- [ ] **Step 3: Implement deterministic `.deb` assembly**

Build Go binaries using `CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH`, run `pnpm --dir web build`, and copy Linux units, staged mihomo, CLI/control/gateway/setup binaries, config example, and web assets into an isolated package root. `control` declares `Depends: dnsmasq, nftables, iproute2, ca-certificates, systemd`. `postinst` creates the `opensurge` user/group and state/config directories but does not start gateway; it runs daemon-reload and enables control only after explicit setup. `prerm` stops all OpenSurge units. `postrm purge` deletes `/var/lib/opensurge`, TLS private key, and credentials while ordinary remove keeps `/etc/opensurge` conffiles.

- [ ] **Step 4: Build and test a disposable package target**

Run: `make deb ARCH=amd64 VERSION=0.0.0 && sudo tests/packages/install-deb.sh artifacts/release/opensurge_0.0.0_amd64.deb`

Expected: PASS; install, remove, reinstall, and purge preserve exactly the declared state.

- [ ] **Step 5: Commit Debian packaging**

```bash
git add packaging/debian tests/packages Makefile
git commit -m "build: package OpenSurge for Debian and Ubuntu"
```

### Task 3: Replace macOS automation with Linux CI and dual-architecture release workflow

**Files:**
- Modify: `.github/workflows/ci.yml`
- Delete: `.github/workflows/release-unsigned.yml`
- Create: `.github/workflows/release-linux.yml`
- Create: `scripts/check-linux-repository.sh`
- Modify: `Makefile`

**Interfaces:**
- Produces `make linux-ci-check`.
- Produces release assets `opensurge_<version>_amd64.deb` and `opensurge_<version>_arm64.deb`.

- [ ] **Step 1: Write repository static assertions**

```sh
! rg -n 'runs-on: macos|GOOS=darwin|OpenSurge-for-Mac|notarize|xcode' .github Makefile scripts packaging
rg -F 'make test' .github/workflows/ci.yml
rg -F 'make web-test' .github/workflows/ci.yml
```

- [ ] **Step 2: Run static assertions before converting automation**

Run: `scripts/check-linux-repository.sh`

Expected: FAIL because macOS release automation and labels remain.

- [ ] **Step 3: Implement Linux CI and release matrix**

Make `ci.yml` run on `ubuntu-24.04`, set Go from `go.mod` and Node 22/pnpm, then run `make test`, `make web-test`, and `make linux-ci-check`. `release-linux.yml` triggers on `v*.*.*`, uses an amd64/arm64 matrix, stages dependencies, builds packages, runs package assertions in matching architecture containers or VMs, uploads artifacts, creates the GitHub Release, and attaches both packages plus checksums. Only release job has `contents: write`.

- [ ] **Step 4: Validate repository check and primary checks**

Run: `make linux-ci-check && make test && make web-test`

Expected: PASS; no macOS release workflow remains.

- [ ] **Step 5: Commit Linux automation**

```bash
git add .github/workflows scripts/check-linux-repository.sh Makefile
git commit -m "ci: release Linux Debian packages"
```

### Task 4: Add daily direct upstream mirror workflow

**Files:**
- Create: `.github/workflows/sync-upstream.yml`
- Create: `tests/workflows/sync-upstream_test.sh`
- Modify: `README.md`, `CONTRIBUTING.md`

**Interfaces:**
- Produces workflow **Sync upstream mirror**.
- Updates only `upstream` from `https://github.com/YTwsy/OpenSurge-for-Mac.git` branch `master`.

- [ ] **Step 1: Write workflow structural test**

```sh
grep -F '17 3 * * *' .github/workflows/sync-upstream.yml
grep -F 'workflow_dispatch:' .github/workflows/sync-upstream.yml
grep -F 'contents: write' .github/workflows/sync-upstream.yml
grep -F 'refs/heads/upstream' .github/workflows/sync-upstream.yml
! grep -Fq 'origin refs/remotes/upstream-source/master:refs/heads/master' .github/workflows/sync-upstream.yml
```

- [ ] **Step 2: Run structural test before workflow creation**

Run: `tests/workflows/sync-upstream_test.sh`

Expected: FAIL because the workflow is absent.

- [ ] **Step 3: Implement guarded exact-ref mirroring**

```yaml
on:
  schedule: [{ cron: '17 3 * * *' }]
  workflow_dispatch:
permissions: { contents: write }
```

Checkout with `fetch-depth: 0`; record the current `refs/heads/upstream`; fetch upstream into `refs/remotes/upstream-source/master`; exit if SHA matches; otherwise run `git push --force-with-lease=upstream:<recorded-sha> origin refs/remotes/upstream-source/master:refs/heads/upstream`. If the lease fails, exit nonzero and leave refs untouched. Emit old/new SHA to job summary. Never check out, merge, rebase, or push `master`.

- [ ] **Step 4: Run workflow test and whitespace validation**

Run: `tests/workflows/sync-upstream_test.sh && git diff --check -- .github/workflows/sync-upstream.yml`

Expected: PASS.

- [ ] **Step 5: Commit upstream synchronization**

```bash
git add .github/workflows/sync-upstream.yml tests/workflows/sync-upstream_test.sh README.md CONTRIBUTING.md
git commit -m "ci: mirror upstream branch daily"
```

### Task 5: Replace user/agent documentation and state evidence boundaries

**Files:**
- Modify: `AGENTS.md`, `README.md`, `README.en.md`, `docs/usage-topologies.zh-CN.md`, `docs/faq.md`, `docs/faq.zh-CN.md`
- Create: `docs/linux-installation.md`, `docs/linux-installation.zh-CN.md`
- Modify: `docs/agent-wiki/wiki/index.md`, `docs/agent-wiki/wiki/concepts/validation-gates.md`
- Create: `docs/agent-wiki/wiki/concepts/linux-gateway-lifecycle.md`, `docs/agent-wiki/wiki/concepts/linux-tun-transparent-proxy.md`
- Delete: `docs/local-mac-routing.md`, `docs/local-mac-routing.zh-CN.md`, `docs/gui-architecture.zh-CN.md`, `docs/agent-wiki/wiki/concepts/macos-tun-transparent-proxy.md`, `docs/agent-wiki/wiki/concepts/local-system-proxy-coordination.md`, `docs/agent-wiki/wiki/concepts/local-mac-routing-modes.md`
- Modify: `docs/agent-wiki/sources/project-brief.md`, `docs/agent-wiki/sources/validation/lab-gates.md`
- Delete: `docs/agent-wiki/sources/decisions/local-mac-routing-modes.md`, `docs/agent-wiki/sources/decisions/local-system-proxy-coordination.md`
- Create: `tests/docs/linux-docs_test.sh`

**Interfaces:**
- Produces documented commands `sudo dpkg -i opensurge_<version>_<arch>.deb`, `opensurge-setup init`, `systemctl status opensurge-control`, `make linux-lab-test`, and `make linux-lab-test-tun`.

- [ ] **Step 1: Write documentation consistency assertions**

```sh
rg -F 'OpenSurge for Linux' AGENTS.md README.md README.en.md docs/linux-installation.md
rg -F 'make linux-lab-test' docs/agent-wiki/wiki/concepts/validation-gates.md
rg -F 'make linux-lab-test-tun' docs/agent-wiki/wiki/concepts/linux-tun-transparent-proxy.md
! rg -n 'PF|pfctl|networksetup|launchd|menu bar|OpenSurge for Mac' AGENTS.md README.md README.en.md docs/linux-installation.md docs/agent-wiki/wiki
```

- [ ] **Step 2: Run assertions before writing Linux documents**

Run: `sh tests/docs/linux-docs_test.sh`

Expected: FAIL because the Linux install and lifecycle documents are absent.

- [ ] **Step 3: Write operator and agent documentation**

Document certificate browser warning/custom replacement, login/password reset, all topology preconditions, IPv6 limits, systemd start/stop/reload, manual router-DHCP recovery, `cleanup-required`, nftables coexistence, `.deb` upgrade/remove/purge, upstream policy, and every gate's proof boundary. Update `AGENTS.md` to make Linux lifecycle/TUN/validation pages the mandatory starting context. Rewrite stable project/lab source pages as Linux facts. Delete the named macOS-only documents and update all incoming wiki/README links in the same task. Documentation must say that a gate has not been run unless the validation report includes its actual output.

- [ ] **Step 4: Run documentation and repository checks**

Run: `sh tests/docs/linux-docs_test.sh && make linux-ci-check && git diff --check`

Expected: PASS.

- [ ] **Step 5: Commit Linux documentation and agent context**

```bash
git add AGENTS.md README.md README.en.md docs tests/docs
git commit -m "docs: publish Linux gateway operation guide"
```
