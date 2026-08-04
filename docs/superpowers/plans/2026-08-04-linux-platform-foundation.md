# Linux Platform Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish a Linux-only, compiling OpenSurge baseline with a Debian/Ubuntu configuration contract and reusable Linux networking primitives.

**Architecture:** Replace macOS configuration and PF concepts with explicit management, nftables, and Linux TUN settings while retaining device policies and imported mihomo profiles. Introduce focused `linuxnet` and `nftables` packages first; gateway lifecycle integration is implemented by the next plan.

**Tech Stack:** Go 1.25, standard-library process execution, Linux `iproute2`, `sysctl`, `nftables`, dnsmasq, mihomo, pnpm/React.

## Global Constraints

- Support Debian 12+ and Ubuntu 22.04+ on amd64 and arm64 only.
- Product naming is **OpenSurge for Linux**; mihomo is the proxy engine, not the product name.
- The final product is Linux-only: do not retain macOS runtime, PF, launchd, SwiftUI, menu-bar, `networksetup`, or system-proxy behavior.
- IPv4 is the supported gateway protocol. `same_lan`/`same_wifi_dhcp` warn about unmanaged IPv6; `isolated_lan` drops downstream IPv6 forwarding.
- `transparent.mode: tun` is the only transparent path; `mihomo.redir_port` remains zero and no REDIRECT/TPROXY path is introduced.
- OpenSurge may alter only its own `nftables` table, named `opensurge` by default; never flush global rules.
- Make every code change test-first, run the named test after each task, and commit only the task's files.

---

## Target File Structure

| Path | Responsibility |
| --- | --- |
| `go.mod` and all Go imports | Public Linux repository module path: `github.com/three-b0dy/OpenSurge-for-Linux`. |
| `cmd/opensurge/main.go` | Linux product CLI entrypoint, copied from the existing CLI behavior before later service-client changes. |
| `cmd/opensurge/main_test.go` | CLI contract tests for product naming, Linux default config path, and JSON output. |
| `internal/config/config.go` | Linux-only config types/defaults: management TLS, nftables table, Linux TUN device and auto-redirect. |
| `internal/config/{loader,validator,render}.go` and tests | YAML parsing, semantic validation, config rendering and removal of macOS-only fields. |
| `internal/config/migration.go` and `migration_test.go` | macOS config candidate migration without modifying the source file. |
| `internal/linuxnet/{interfaces,iproute}.go` and tests | Exact `ip` command adapter for interfaces, addresses, routes and neighbours. |
| `internal/nftables/{template,manager}.go` and tests | OpenSurge-only nftables rendering, syntax checking and idempotent table load/unload. |
| `internal/sysctl/ipforward.go` and test | Linux `net.ipv4.ip_forward` manager. |
| `internal/runtime/{paths,state}.go` and tests | Linux artifact paths and nftables runtime state; no PF/system-proxy snapshot. |
| `examples/config.example.yaml` | Valid Linux-only isolated-LAN example. |
| `Makefile` | Linux build/test targets and removal of menu-bar/package targets. |

### Task 1: Rename the Go module and establish Linux CLI entrypoints

**Files:**
- Modify: `go.mod`
- Modify: every `*.go` file importing `open-mihomo-gateway/...`
- Create: `cmd/opensurge/main.go`
- Create: `cmd/opensurge/main_test.go`
- Delete: `cmd/omg/main.go`, `cmd/omg/main_test.go`
- Modify: `Makefile`

**Interfaces:**
- Produces `func run(args []string) int` in `cmd/opensurge/main.go`.
- Produces `const defaultConfigPath = "/etc/opensurge/config.yaml"`.

- [ ] **Step 1: Write the failing CLI contract tests**

```go
func TestRunNoArgumentsPrintsOpenSurgeUsage(t *testing.T) {
    code := run(nil)
    if code != 2 { t.Fatalf("run(nil) = %d, want 2", code) }
}

func TestDefaultConfigPath(t *testing.T) {
    if defaultConfigPath != "/etc/opensurge/config.yaml" {
        t.Fatalf("defaultConfigPath = %q", defaultConfigPath)
    }
}
```

- [ ] **Step 2: Run the new test before the entrypoint exists**

Run: `go test ./cmd/opensurge`

Expected: FAIL because `cmd/opensurge` does not exist.

- [ ] **Step 3: Move the CLI implementation and rename the module**

```go
// go.mod
module github.com/three-b0dy/OpenSurge-for-Linux

// cmd/opensurge/main.go
const defaultConfigPath = "/etc/opensurge/config.yaml"

func main() { os.Exit(run(os.Args[1:])) }
```

Copy the existing command switch and its tests to `cmd/opensurge`; replace every internal import with the new module path. Rename all user-facing `omg` messages and binary references to `opensurge`. Change `Makefile` `build`, `doctor`, and `status` targets to use `./cmd/opensurge`.

- [ ] **Step 4: Run the focused and repository tests**

Run: `go test ./cmd/opensurge && go test ./...`

Expected: PASS; no import path contains `open-mihomo-gateway`.

- [ ] **Step 5: Commit the module and CLI move**

```bash
git add go.mod cmd/opensurge cmd/omg Makefile $(rg -l 'open-mihomo-gateway' --glob '*.go')
git commit -m "refactor: establish opensurge Linux CLI"
```

### Task 2: Define Linux-only configuration and migration contracts

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/loader.go`
- Modify: `internal/config/validator.go`
- Modify: `internal/config/{loader,validator,render}_test.go`
- Create: `internal/config/migration.go`
- Create: `internal/config/migration_test.go`
- Modify: `examples/config.example.yaml`

**Interfaces:**
- Produces `type ManagementConfig struct { Listen, TLSCertFile, TLSKeyFile string }`.
- Produces `type NftablesConfig struct { Table string }` and `GatewayConfig.RouterDHCPDisabledConfirmed bool`.
- Produces `func MigrateMacConfig(source []byte) ([]byte, []string, error)`; it returns a candidate YAML payload and human-readable required mappings.

- [ ] **Step 1: Write validation and migration tests**

```go
func TestValidateLinuxTUNRequiresAutoRedirect(t *testing.T) {
    cfg := Default()
    cfg.Transparent.Mode = TransparentModeTUN
    cfg.Transparent.TUNAutoRedirect = false
    if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "transparent.tun_auto_redirect") {
        t.Fatalf("Validate() error = %v", err)
    }
}

func TestMigrateMacConfigRemovesPlatformFields(t *testing.T) {
    out, notes, err := MigrateMacConfig([]byte("pf:\n  anchor_name: x\nlocal_system_proxy:\n  enabled: true\n"))
    if err != nil || bytes.Contains(out, []byte("pf:")) || len(notes) == 0 { t.Fatalf("out=%s notes=%v err=%v", out, notes, err) }
}
```

- [ ] **Step 2: Run the tests to establish the missing Linux contract**

Run: `go test ./internal/config -run 'LinuxTUN|MigrateMacConfig'`

Expected: FAIL because the types and migrator are absent.

- [ ] **Step 3: Add the exact config fields and reject removed fields**

```go
type ManagementConfig struct {
    Listen      string
    TLSCertFile string
    TLSKeyFile  string
}

type NftablesConfig struct { Table string }

type GatewayConfig struct {
    Mode, Interface, LANIP, UpstreamInterface string
    RouterDHCPDisabledConfirmed bool
}

type TransparentConfig struct {
    Mode, TUNDevice, TUNStack string
    TUNAutoRoute, TUNAutoRedirect, TUNAutoDetectInterface, TUNStrictRoute bool
}
```

Set defaults to `management.listen: "192.168.50.1:61767"`, `nftables.table: "opensurge"`, TUN device `opensurge-tun`, `TUNAutoRoute: true`, and `TUNAutoRedirect: true`. Remove `PF` and `LocalSystemProxy` from `Config`, loader keys, rendered config, examples, and API-facing config serialization. Reject a TUN device containing whitespace, any non-zero `mihomo.redir_port`, a wildcard/loopback management listener, and a management listener that is not IPv4 host:port.

- [ ] **Step 4: Implement candidate-only migration and update example configuration**

```go
func MigrateMacConfig(source []byte) ([]byte, []string, error) {
    // Decode YAML to a mapping, delete pf/local_system_proxy/network_service keys,
    // set Linux defaults, and return notes requiring explicit interface mapping.
}
```

The migrator must never write a file. It must preserve profile/device-policy fields and return notes for `gateway.interface`, `gateway.upstream_interface`, and `management.listen` whenever their existing values are macOS defaults.

- [ ] **Step 5: Verify config behavior**

Run: `go test ./internal/config && go run ./cmd/opensurge config validate --config examples/config.example.yaml`

Expected: PASS; the example has no `pf`, `local_system_proxy`, `utun`, or macOS interface name.

- [ ] **Step 6: Commit configuration foundation**

```bash
git add internal/config examples/config.example.yaml
git commit -m "feat: define Linux gateway configuration"
```

### Task 3: Build Linux system adapters and runtime state

**Files:**
- Create: `internal/linuxnet/interfaces.go`
- Create: `internal/linuxnet/iproute.go`
- Create: `internal/linuxnet/iproute_test.go`
- Modify: `internal/sysctl/ipforward.go`
- Modify: `internal/sysctl/ipforward_test.go`
- Modify: `internal/runtime/paths.go`
- Modify: `internal/runtime/state.go`
- Modify: `internal/runtime/{paths,state}_test.go`

**Interfaces:**
- Produces `type InterfaceInspector interface { Addresses(context.Context, string) ([]netip.Prefix, error); Neighbors(context.Context, string) ([]Neighbor, error) }`.
- Produces `type Neighbor struct { IPv4 netip.Addr; MAC string; State string }`.
- Produces `func NewIPRoute(run CommandRunner) *IPRoute`.

- [ ] **Step 1: Write parser and state tests**

```go
func TestAddressesParsesIPv4Prefixes(t *testing.T) {
    got, err := NewIPRoute(fakeRunner(`[{"ifname":"lan0","addr_info":[{"family":"inet","local":"192.168.50.1","prefixlen":24}]}]`)).Addresses(context.Background(), "lan0")
    if err != nil || len(got) != 1 || got[0].String() != "192.168.50.1/24" { t.Fatalf("got=%v err=%v", got, err) }
}

func TestStateDoesNotSerializePFOrSystemProxy(t *testing.T) {
    data, _ := json.Marshal(State{NftablesLoaded: true})
    if bytes.Contains(data, []byte("pf_")) || bytes.Contains(data, []byte("system_proxy")) { t.Fatal(string(data)) }
}
```

- [ ] **Step 2: Run focused tests before implementation**

Run: `go test ./internal/linuxnet ./internal/runtime ./internal/sysctl`

Expected: FAIL because `linuxnet`, `NftablesLoaded`, and Linux sysctl key do not exist.

- [ ] **Step 3: Implement fixed-command Linux adapters**

```go
const keyIPForwarding = "net.ipv4.ip_forward"

func (r *IPRoute) Addresses(ctx context.Context, name string) ([]netip.Prefix, error) {
    return parseAddresses(r.run(ctx, "ip", "-j", "addr", "show", "dev", name))
}
```

Use `ip -j addr show dev <name>` and `ip -j neigh show dev <name>` only; validate interface names before command construction and never invoke a shell. Replace `PFAnchor` paths with `NftablesRules` paths and replace `PFEnabledBefore`/`PFAnchorLoaded` with `NftablesLoaded` in runtime state.

- [ ] **Step 4: Run adapter and full unit tests**

Run: `go test ./internal/linuxnet ./internal/runtime ./internal/sysctl && go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit Linux primitives**

```bash
git add internal/linuxnet internal/sysctl internal/runtime
git commit -m "feat: add Linux network runtime primitives"
```

### Task 4: Add isolated OpenSurge nftables rendering and management

**Files:**
- Create: `internal/nftables/template.go`
- Create: `internal/nftables/template_test.go`
- Create: `internal/nftables/manager.go`
- Create: `internal/nftables/manager_test.go`
- Modify: `internal/doctor/doctor.go`
- Modify: `internal/doctor/doctor_test.go`

**Interfaces:**
- Produces `func RenderRuleset(cfg config.Config) (string, error)`.
- Produces `type Manager struct` with methods `Check() error`, `WriteRuleset() error`, `Load() error`, `Loaded() (bool, error)`, and `Unload() error`.

- [ ] **Step 1: Write ruleset ownership tests**

```go
func TestRenderRulesetUsesOnlyOpenSurgeTable(t *testing.T) {
    rules, err := RenderRuleset(testConfig())
    if err != nil || !strings.Contains(rules, "table inet opensurge") || strings.Contains(rules, "flush ruleset") { t.Fatalf("%s: %v", rules, err) }
}

func TestUnloadDeletesOnlyNamedTable(t *testing.T) {
    runner := &recordingRunner{}
    _ = New(testConfig(), testPaths(), runner).Unload()
    if got := runner.Args(); !reflect.DeepEqual(got, []string{"nft", "delete", "table", "inet", "opensurge"}) { t.Fatalf("%v", got) }
}
```

- [ ] **Step 2: Run the focused tests before package creation**

Run: `go test ./internal/nftables ./internal/doctor`

Expected: FAIL because `internal/nftables` is absent and doctor checks `pfctl`.

- [ ] **Step 3: Implement render/check/load/unload semantics**

```nft
table inet opensurge {
  chain forward { type filter hook forward priority filter; policy accept; }
  chain postrouting { type nat hook postrouting priority srcnat; oifname "wan0" ip saddr 192.168.50.0/24 masquerade }
}
```

Render mode-specific forward permits and, for `isolated_lan`, an IPv6 forward-drop chain for the downstream interface. `WriteRuleset` writes mode `0640`; `Load` runs `nft --check --file <path>` before `nft --file <path>`; `Loaded` uses `nft list table inet <table>`; `Unload` runs only `nft delete table inet <table>` and treats absence as success. Update doctor to check `nft` and rendered rules rather than `pfctl`.

- [ ] **Step 4: Verify firewall package behavior**

Run: `go test ./internal/nftables ./internal/doctor && go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit nftables foundation**

```bash
git add internal/nftables internal/doctor
git commit -m "feat: add isolated nftables manager"
```

### Task 5: Remove unreferenced macOS build surface and document the transition

**Files:**
- Delete: `apps/menubar/`
- Delete: `packaging/launchd/`
- Delete: `scripts/build-menubar-app.sh`, `scripts/check-menubar.sh`, `scripts/prepare-gui-release-deps.sh`, `scripts/build-gui-installer.sh`, `scripts/notarize-gui-installer.sh`, `scripts/verify-unsigned-gui-installer.sh`, `scripts/uninstall-gui.sh`
- Modify: `Makefile`
- Modify: `README.md`, `README.en.md`, `docs/agent-wiki/wiki/index.md`
- Create: `docs/linux-migration.md`

**Interfaces:**
- Produces documented commands `opensurge config migrate` and `opensurge config validate`.

- [ ] **Step 1: Write a documentation-link test**

```sh
! rg -n 'OpenSurge for Mac|menubar-build|launchd|pfctl|networksetup' README.md README.en.md docs/agent-wiki/wiki
```

- [ ] **Step 2: Run the test before editing docs**

Run: `sh -c '! rg -n "OpenSurge for Mac|menubar-build|launchd|pfctl|networksetup" README.md README.en.md docs/agent-wiki/wiki'`

Expected: FAIL because the repository still documents the macOS product.

- [ ] **Step 3: Delete macOS-only surface and replace user documentation**

Replace the README opening, installation section, topology descriptions, validation names, and agent wiki index with Linux-only statements from the approved design. `docs/linux-migration.md` must show the candidate-only migration command, the required manual interface mappings, and the warning that it does not change a router's DHCP state.

- [ ] **Step 4: Run documentation, Go, and web checks**

Run: `sh -c '! rg -n "OpenSurge for Mac|menubar-build|launchd|pfctl|networksetup" README.md README.en.md docs/agent-wiki/wiki' && go test ./... && make web-test`

Expected: PASS; macOS command names are absent from current user/agent entrypoints.

- [ ] **Step 5: Commit the Linux-only baseline**

```bash
git add README.md README.en.md Makefile docs apps packaging scripts
git commit -m "docs: convert project baseline to OpenSurge for Linux"
```
