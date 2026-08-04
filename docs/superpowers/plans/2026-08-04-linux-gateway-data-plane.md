# Linux Gateway Data Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a fail-closed Linux gateway lifecycle for all three modes, including nftables NAT, mihomo TUN auto-redirect, dnsmasq, rollback, and namespace-based evidence.

**Architecture:** `gateway.Manager` owns the ordered root lifecycle and persists only OpenSurge-managed state. The manager delegates forwarding to `sysctl`, transparent interception to mihomo, DHCP/DNS to dnsmasq, and NAT/filter cleanup to `internal/nftables`; Linux network namespaces exercise the same binaries and rules without a real LAN.

**Tech Stack:** Go 1.25, mihomo TUN, dnsmasq, nftables, `ip`, `ip netns`, veth pairs, curl/dig.

## Global Constraints

- Consume the Linux config and `internal/nftables.Manager` produced by `2026-08-04-linux-platform-foundation.md`.
- Never flush or disable non-OpenSurge firewall state.
- Start order is preflight → candidate artifacts → saved state → forwarding → mihomo TUN ready → dnsmasq when enabled → nftables; stop is the inverse.
- TUN readiness timeout is 10 seconds; failed process gets 3 seconds SIGTERM grace before SIGKILL.
- `same_lan` never starts DHCP; `same_wifi_dhcp` requires explicit router-DHCP-disabled confirmation; `isolated_lan` requires distinct wired/VLAN upstream/downstream interfaces.
- Do not claim a network data path is verified without the named Linux lab command having passed.

---

## Target File Structure

| Path | Responsibility |
| --- | --- |
| `internal/gateway/manager.go` | Linux lifecycle, rollback, reload and restart-mihomo orchestration. |
| `internal/gateway/manager_test.go` | Ordered lifecycle and cleanup-failure unit tests using dependency fakes. |
| `internal/gateway/{preflight,topology}.go` and tests | Interface/address/subnet/port/policy-route preflight checks. |
| `internal/gateway/status.go` and test | Linux runtime state, nftables status and TUN readiness status. |
| `internal/mihomo/config.go` and test | Linux TUN rendering with `auto-redirect: true`. |
| `internal/mihomo/manager.go` and test | TUN readiness/termination behavior. |
| `internal/dhcp/{template,dnsmasq}.go` and tests | Mode-aware DNS-only/DHCP dnsmasq configuration. |
| `tests/linux-lab/{lab.sh,config.yaml.tmpl,README.md}` | Reproducible root-required namespace lab. |
| `tests/linux-real-device/{smoke.sh,README.md}` | Manual evidence runner for same-LAN and isolated-LAN hardware. |
| `Makefile` | `linux-lab-test`, `linux-lab-test-tun`, and `linux-real-device-smoke` targets. |

### Task 1: Render Linux mihomo TUN configuration and preserve profile ownership

**Files:**
- Modify: `internal/mihomo/config.go`
- Modify: `internal/mihomo/config_test.go`
- Modify: `internal/mihomo/profile.go`
- Modify: `internal/mihomo/profile_test.go`

**Interfaces:**
- Produces `tun.auto-redirect: true` whenever `transparent.mode == "tun"`.
- Produces `func ValidateWrittenConfig() error` that runs `<mihomo> -t -f <runtime config>`.

- [ ] **Step 1: Add failing Linux TUN rendering tests**

```go
func TestRenderTUNEnablesLinuxAutoRedirect(t *testing.T) {
    cfg := config.Default()
    cfg.Transparent.Mode = config.TransparentModeTUN
    rendered, err := RenderConfig(cfg)
    if err != nil || !strings.Contains(rendered, "auto-redirect: true") { t.Fatalf("%s: %v", rendered, err) }
}

func TestImportedProfileCannotOverrideAutoRedirect(t *testing.T) {
    // Imported `tun.auto-redirect: false` still renders the OpenSurge-owned true value.
}
```

- [ ] **Step 2: Run the focused tests**

Run: `go test ./internal/mihomo -run 'AutoRedirect|ImportedProfile'`

Expected: FAIL because the renderer does not emit `auto-redirect`.

- [ ] **Step 3: Render the Linux-owned TUN fields**

```yaml
tun:
  enable: true
  device: opensurge-tun
  stack: mixed
  auto-route: true
  auto-redirect: true
  auto-detect-interface: false
  dns-hijack:
    - any:53
    - tcp://any:53
```

Set `interface-name` from the configured upstream interface. Retain private/LAN route exclusions, but calculate the configured LAN prefix rather than hard-coding an address. Keep `external-controller` loopback-only even though the Control API listens on LAN HTTPS.

- [ ] **Step 4: Verify renderer and engine validation**

Run: `go test ./internal/mihomo && make test`

Expected: PASS.

- [ ] **Step 5: Commit the TUN rendering change**

```bash
git add internal/mihomo
git commit -m "feat: render Linux mihomo auto redirect"
```

### Task 2: Replace PF dependencies in the gateway lifecycle with nftables

**Files:**
- Modify: `internal/gateway/manager.go`
- Modify: `internal/gateway/manager_test.go`
- Modify: `internal/gateway/status.go`
- Modify: `internal/gateway/status_test.go`
- Modify: `internal/runtime/state.go`
- Delete: `internal/pf/`

**Interfaces:**
- Consumes `nftables.Manager` from the platform-foundation plan.
- Produces `type nftService interface { Check() error; WriteRuleset() error; Load() error; Loaded() (bool, error); Unload() error }`.
- Produces runtime field `NftablesLoaded bool` and status field `Nftables string`.

- [ ] **Step 1: Write ordering and rollback tests**

```go
func TestStartLoadsNftablesAfterMihomoAndDNSMasq(t *testing.T) {
    events := &[]string{}
    manager := testManager(events, fakeMihomo{ready: true}, fakeDHCP{}, fakeNft{})
    if err := manager.Start(context.Background()); err != nil { t.Fatal(err) }
    if got := strings.Join(*events, ","); got != "forwarding-on,mihomo-start,mihomo-ready,dnsmasq-start,nft-load" { t.Fatal(got) }
}

func TestNftablesLoadFailureRollsBackOnlyOpenSurgeState(t *testing.T) {
    // Assert dnsmasq/mihomo stop, nft unload, forwarding restore, and retained state on cleanup error.
}
```

- [ ] **Step 2: Run the gateway test before replacing PF**

Run: `go test ./internal/gateway -run 'Nftables|StartLoads'`

Expected: FAIL because the manager still depends on `pfService`.

- [ ] **Step 3: Implement the Linux lifecycle sequence**

```go
if err := forwarding.Enable(); err != nil { return m.rollback(err, state, services) }
state.PIDMihomo, err = mihomo.Start()
if err == nil { err = mihomo.WaitForTUN(ctx, 10*time.Second) }
if err == nil && cfg.DHCP.Enabled { state.PIDDNSMasq, err = dhcp.Start() }
if err == nil { err = firewall.Load(); state.NftablesLoaded = err == nil }
```

Persist state before enabling forwarding and after every acquired resource. On a mihomo startup error, call `Stop` with a 3-second SIGTERM window before forced termination. For `same_lan`, use DNS-only dnsmasq status as a healthy service; do not require DHCP leases for gateway health. Remove PF/system-proxy branches and set status to `cleanup-required` when state cannot be safely removed.

- [ ] **Step 4: Run manager, status, and full Go tests**

Run: `go test ./internal/gateway && go test ./...`

Expected: PASS; neither source nor status JSON contains `pf_anchor`.

- [ ] **Step 5: Commit Linux lifecycle migration**

```bash
git add internal/gateway internal/runtime internal/pf
git commit -m "feat: manage Linux nftables gateway lifecycle"
```

### Task 3: Enforce the three Linux topology preflight contracts

**Files:**
- Create: `internal/gateway/preflight.go`
- Create: `internal/gateway/preflight_test.go`
- Modify: `internal/gateway/manager.go`
- Modify: `internal/gateway/reservation_conflicts.go`
- Modify: `internal/doctor/doctor.go`
- Modify: `internal/doctor/doctor_test.go`

**Interfaces:**
- Produces `func ValidateTopology(ctx context.Context, cfg config.Config, inspector linuxnet.InterfaceInspector) error`.
- Produces `func DetectPolicyRouteConflict(ctx context.Context, runner process.Runner) error`.

- [ ] **Step 1: Write exact mode rejection tests**

```go
func TestValidateTopologySameLANRejectsDifferentInterfaces(t *testing.T) {
    cfg := testConfig(config.GatewayModeSameLAN, "lan0", "wan0")
    err := ValidateTopology(context.Background(), cfg, fakeInspector{"lan0": {netip.MustParsePrefix("192.168.50.1/24")}, "wan0": {netip.MustParsePrefix("198.51.100.2/24")}})
    if err == nil || !strings.Contains(err.Error(), "requires gateway and upstream interfaces to match") { t.Fatalf("%v", err) }
}
func TestValidateTopologySameWiFiDHCPRequiresConfirmation(t *testing.T) {
    cfg := testConfig(config.GatewayModeSameWiFiDHCP, "lan0", "lan0")
    err := ValidateTopology(context.Background(), cfg, fakeInspector{"lan0": {netip.MustParsePrefix("192.168.50.1/24")}})
    if err == nil || !strings.Contains(err.Error(), "router_dhcp_disabled") { t.Fatalf("%v", err) }
}
func TestValidateTopologyIsolatedLANRejectsOverlappingPrefixes(t *testing.T) {
    cfg := testConfig(config.GatewayModeIsolatedLAN, "lan0", "wan0")
    err := ValidateTopology(context.Background(), cfg, fakeInspector{"lan0": {netip.MustParsePrefix("192.168.50.1/24")}, "wan0": {netip.MustParsePrefix("192.168.50.2/24")}})
    if err == nil || !strings.Contains(err.Error(), "overlaps") { t.Fatalf("%v", err) }
}
```

- [ ] **Step 2: Run preflight tests before extracting the code**

Run: `go test ./internal/gateway -run 'ValidateTopology|PolicyRoute'`

Expected: FAIL because `ValidateTopology` and policy-route detection do not exist.

- [ ] **Step 3: Implement topology validation and doctor output**

```go
type fakeInspector map[string][]netip.Prefix
func (f fakeInspector) Addresses(_ context.Context, name string) ([]netip.Prefix, error) { return f[name], nil }
func (f fakeInspector) Neighbors(context.Context, string) ([]linuxnet.Neighbor, error) { return nil, nil }

switch cfg.Gateway.Mode {
case config.GatewayModeSameLAN:
    requireEqual(cfg.Gateway.Interface, cfg.Gateway.UpstreamInterface)
    requireFalse(cfg.DHCP.Enabled)
case config.GatewayModeSameWiFiDHCP:
    requireEqual(cfg.Gateway.Interface, cfg.Gateway.UpstreamInterface)
    requireTrue(cfg.DHCP.Enabled)
    requireTrue(cfg.Gateway.RouterDHCPDisabledConfirmed)
case config.GatewayModeIsolatedLAN:
    requireDifferent(cfg.Gateway.Interface, cfg.Gateway.UpstreamInterface)
    requireDisjointIPv4Prefixes(lanPrefix, upstreamPrefixes)
}
```

Check interface existence, configured LAN IP, DNS/mixed/TUN port conflicts, duplicate LAN IPs, dnsmasq pool collisions, and `ip rule` ownership conflicts with mihomo's reserved priority range. The doctor report must name the actual command/check that failed and, for same-LAN modes, state that IPv6 is not protected.

- [ ] **Step 4: Run topology and doctor tests**

Run: `go test ./internal/gateway ./internal/doctor && go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit preflight contracts**

```bash
git add internal/gateway internal/doctor internal/config
git commit -m "feat: enforce Linux gateway topology contracts"
```

### Task 4: Build the namespace DHCP/DNS/NAT/rollback laboratory

**Files:**
- Create: `tests/linux-lab/lab.sh`
- Create: `tests/linux-lab/config.yaml.tmpl`
- Create: `tests/linux-lab/README.md`
- Create: `tests/linux-lab/helpers/http-connect-proxy.go`
- Modify: `Makefile`

**Interfaces:**
- Produces `make linux-lab-test` and `make linux-lab-test-tun`.
- Produces root-required lab namespaces `opensurge-lab-gw`, `opensurge-lab-client`, and `opensurge-lab-upstream`.

- [ ] **Step 1: Write the lab assertion functions before test flow**

```sh
assert_client_dns() { ip netns exec opensurge-lab-client dig @192.168.50.1 example.com A >/dev/null; }
assert_client_nat() { ip netns exec opensurge-lab-client curl --fail --max-time 15 https://example.com/ >/dev/null; }
assert_cleanup() { ! ip netns exec opensurge-lab-gw nft list table inet opensurge; test ! -e "$runtime_dir/state.json"; }
```

- [ ] **Step 2: Run the command before the lab exists**

Run: `sudo -v && make linux-lab-test`

Expected: FAIL because the Make target and namespace runner are absent.

- [ ] **Step 3: Implement isolated namespace topology**

Create two veth pairs: client↔gateway using `192.168.50.0/24` and gateway↔upstream using `198.51.100.0/24`. Bind dnsmasq only to the client-side gateway veth. Start a local controlled HTTPS-capable endpoint or CONNECT proxy in the upstream namespace. Run `opensurge start`, assert DHCP lease/DNS/NAT, deliberately fail a later start step, then assert forwarding and `inet opensurge` cleanup.

- [ ] **Step 4: Run the standard data-plane gate**

Run: `sudo -v && make linux-lab-test`

Expected: PASS; artifacts are saved under `artifacts/linux-lab/`.

- [ ] **Step 5: Commit the standard Linux lab**

```bash
git add tests/linux-lab Makefile
git commit -m "test: add Linux gateway namespace lab"
```

### Task 5: Add no-explicit-proxy TUN evidence and real-device runner

**Files:**
- Modify: `tests/linux-lab/lab.sh`
- Modify: `tests/linux-lab/README.md`
- Create: `tests/linux-real-device/smoke.sh`
- Create: `tests/linux-real-device/README.md`
- Modify: `Makefile`

**Interfaces:**
- Produces `make linux-lab-test-tun` and `make linux-real-device-smoke`.

- [ ] **Step 1: Add a TUN-only assertion that does not set a client proxy**

```sh
assert_tun_flow() {
  ip netns exec opensurge-lab-client env -u http_proxy -u https_proxy curl --fail --max-time 15 https://example.com/ >/dev/null
  grep -F 'example.com:443' "$runtime_dir/logs/mihomo.log"
}
```

- [ ] **Step 2: Run the TUN target before implementation**

Run: `sudo -v && make linux-lab-test-tun`

Expected: FAIL because no TUN-specific route and log assertion exists.

- [ ] **Step 3: Implement TUN test and hardware smoke checks**

Enable `transparent.mode: tun` only for the TUN target. Assert client default route and DNS point to gateway, no explicit proxy environment or config exists, mihomo reports TUN enabled, and log records the HTTPS host. The hardware runner must take `UPSTREAM_IFACE`, `DOWNSTREAM_IFACE` (or `DOWNSTREAM_VLAN`), `LAN_CIDR`, and `MODE`; it must print an explicit warning and refuse `same_wifi_dhcp` unless `ROUTER_DHCP_DISABLED=confirmed` is supplied.

- [ ] **Step 4: Run both evidence gates**

Run: `sudo -v && make linux-lab-test-tun`

Expected: PASS; output contains `transparent TUN log observed` and cleanup evidence.

Run: `make linux-real-device-smoke --help`

Expected: PASS; usage documents operator-provided hardware inputs.

- [ ] **Step 5: Commit transparent-path verification**

```bash
git add tests/linux-lab tests/linux-real-device Makefile
git commit -m "test: verify Linux transparent TUN gateway path"
```
