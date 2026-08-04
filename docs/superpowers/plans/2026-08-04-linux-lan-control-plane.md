# Linux LAN Control Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the Control API and React GUI safely on the configured LAN through HTTPS and a simple single-administrator login while preserving gateway, source, device, policy, and diagnostics functions.

**Architecture:** The root gateway service is the only process that changes host networking. An unprivileged `opensurge` Control API process serves HTTPS, owns browser authentication, and sends a fixed JSON protocol through a root-owned Unix socket; the GUI adds first-run/login states and drops macOS-only recovery/menubar behavior.

**Tech Stack:** Go `net/http`, `crypto/tls`, `golang.org/x/crypto/argon2`, Unix sockets, systemd, React, TypeScript, Vitest.

## Global Constraints

- Bind HTTPS only to the explicit RFC1918 `management.listen` IPv4 on the declared LAN interface; reject wildcard, loopback, and WAN-interface addresses.
- Keep one administrator account. Hash passwords with Argon2id and never log a password, hash, session identifier, or private key.
- Sessions expire after 12 hours idle and use `HttpOnly`, `Secure`, `SameSite=Strict` cookies; mutations require same-origin checks.
- Lock a source IP after five failed logins in 15 minutes; remove the lock after 15 minutes.
- Generate an RSA-3072 self-signed certificate valid for 10 years during first setup; accept only a validated custom replacement pair.
- Replace macOS static-IP and DHCP mutations with Linux interface discovery and a manual router-DHCP recovery checklist.
- Keep mihomo's external controller on loopback; do not add a public bearer-token API.

---

## Target File Structure

| Path | Responsibility |
| --- | --- |
| `internal/controlapi/auth.go` and test | Administrator record, Argon2id verification, IP limiter, server-side sessions. |
| `internal/controlapi/tls.go` and test | Self-signed certificate creation, pair validation, HTTPS TLS config. |
| `internal/controlapi/server.go` and test | LAN listener, login/logout/setup, security headers, gateway client. |
| `internal/controlapi/{actions,models}.go` and tests | Fixed Linux helper protocol and non-macOS DTOs. |
| `cmd/opensurge-{control,gateway,setup}/main.go` | Unprivileged control service, root socket daemon, setup/reset CLI. |
| `packaging/systemd/*` | systemd process identity and Unix socket permissions. |
| `web/src/{App,api,types}.tsx` | Authentication-aware application shell and API client. |
| `web/src/pages/LoginPage.tsx` and test | First-run and administrator login page. |
| `web/src/pages/NetworkPage.tsx` and test | Linux interface names, three gateway modes, acknowledgements. |

### Task 1: Implement single-admin credentials, sessions, and login throttling

**Files:**
- Create: `internal/controlapi/auth.go`
- Create: `internal/controlapi/auth_test.go`
- Modify: `internal/controlapi/store.go`, `internal/controlapi/server.go`, `internal/controlapi/server_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces `type AdminStore interface { Initialized() (bool, error); Set(username, password string) error; Authenticate(username, password string) error }`.
- Produces `POST /api/v1/auth/setup`, `POST /api/v1/auth/login`, `POST /api/v1/auth/logout`, and `GET /api/v1/auth/status`.

- [ ] **Step 1: Write failing authentication tests**

```go
func TestLoginSetsSecureSessionCookie(t *testing.T) {
    server := newTestServer(t, withAdmin("admin", "correct horse battery staple"))
    response := postJSON(t, server.Handler(), "/api/v1/auth/login", `{"username":"admin","password":"correct horse battery staple"}`)
    if response.Code != http.StatusNoContent { t.Fatal(response.Code) }
    cookie := firstCookie(response.Result(), "opensurge_session")
    if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode { t.Fatalf("%+v", cookie) }
}

func TestFailedLoginLocksSourceIPAfterFiveAttempts(t *testing.T) {
    server := newTestServer(t, withAdmin("admin", "correct horse battery staple"))
    for attempt := 0; attempt < 5; attempt++ {
        response := postJSONFrom(t, server.Handler(), "192.168.50.9", "/api/v1/auth/login", `{"username":"admin","password":"wrong password value"}`)
        if response.Code != http.StatusUnauthorized { t.Fatalf("attempt %d: %d", attempt, response.Code) }
    }
    response := postJSONFrom(t, server.Handler(), "192.168.50.9", "/api/v1/auth/login", `{"username":"admin","password":"wrong password value"}`)
    if response.Code != http.StatusTooManyRequests { t.Fatal(response.Code) }
}
```

- [ ] **Step 2: Run the test before implementation**

Run: `go test ./internal/controlapi -run 'Login|FailedLogin'`

Expected: FAIL because login endpoints and the admin store are absent.

- [ ] **Step 3: Implement credentials and sessions**

```go
type adminRecord struct {
    Username string `json:"username"`
    Salt     string `json:"salt"`
    Hash     string `json:"hash"`
}

func derivePassword(password string, salt []byte) []byte {
    return argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
}
```

Persist the record atomically with mode `0600`. Setup works only before the record exists and requires a nonempty username plus at least 12 UTF-8 password characters. Keep sessions server-side, rotate the identifier at login, purge expired records at every authentication request, return 401 for invalid credentials, and return 429 for a locked IP.

- [ ] **Step 4: Replace browser bootstrap-token authentication**

Remove `/bootstrap`, native launcher bearer authentication, `Store.Token`, and `POST /api/v1/session/bootstrap`. Guard every non-auth API route with `RequireSession`; unauthenticated `GET /api/v1/auth/status` returns `{"initialized":true,"authenticated":false}`. Keep ETag and idempotency behavior unchanged.

- [ ] **Step 5: Run focused, race, and full tests**

Run: `go test ./internal/controlapi -run 'Auth|Login|Session' && go test -race ./internal/controlapi && go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit administrator authentication**

```bash
git add go.mod go.sum internal/controlapi
git commit -m "feat: add LAN administrator authentication"
```

### Task 2: Generate, validate, and serve TLS certificates on the LAN

**Files:**
- Create: `internal/controlapi/tls.go`, `internal/controlapi/tls_test.go`
- Modify: `internal/controlapi/server.go`, `internal/controlapi/server_test.go`
- Create: `cmd/opensurge-setup/main.go`, `cmd/opensurge-setup/main_test.go`

**Interfaces:**
- Produces `func EnsureSelfSigned(certPath, keyPath string, ips []net.IP, now time.Time) error`.
- Produces `func ValidateKeyPair(certPath, keyPath string) error` and `func ReplaceCertificate(certPath, keyPath string, certPEM, keyPEM []byte) error`.

- [ ] **Step 1: Write TLS and listener contract tests**

```go
func TestEnsureSelfSignedUsesTenYearRSA3072Certificate(t *testing.T) {
    cert, key := tempTLSPaths(t)
    now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
    if err := EnsureSelfSigned(cert, key, []net.IP{net.ParseIP("192.168.50.1")}, now); err != nil { t.Fatal(err) }
    parsed := loadCertificate(t, cert)
    if parsed.PublicKeyAlgorithm != x509.RSA || parsed.NotAfter.Sub(now) < 3650*24*time.Hour { t.Fatal(parsed) }
}

func TestNewRejectsWildcardManagementListener(t *testing.T) {
    _, err := New(Options{ConfigPath: testConfigPath(t), Addr: "0.0.0.0:61767", StoreDir: t.TempDir()})
    if err == nil || !strings.Contains(err.Error(), "management listener") { t.Fatalf("%v", err) }
}
```

- [ ] **Step 2: Run the TLS test before implementation**

Run: `go test ./internal/controlapi -run 'SelfSigned|ManagementListener'`

Expected: FAIL because certificate helpers and LAN listener support are absent.

- [ ] **Step 3: Implement certificate lifecycle and strict HTTPS serving**

```go
func (s *Server) Serve(ctx context.Context) error {
    listener, err := tls.Listen("tcp4", s.addr, s.tlsConfig)
    if err != nil { return err }
    return s.serveWithListener(ctx, listener)
}
```

Generate an RSA-3072 certificate with the listener IP in Subject Alternative Names and 3,650-day validity. Store `/etc/opensurge/tls` as `0750` and the cert/key as `0640` owned by `root:opensurge`. Before replacing files, verify PEM parsing, matching public key, non-expiry, and listener-IP SAN. Atomically replace only after validation. Restrict Host header to exact `host` or `host:port`, and set HSTS, `X-Content-Type-Options`, `X-Frame-Options: DENY`, and same-origin CSP.

- [ ] **Step 4: Implement root-only setup commands**

```text
opensurge-setup init --config /etc/opensurge/config.yaml --username <name>
opensurge-setup reset-password --username <name>
opensurge-setup replace-certificate --cert <pem> --key <pem>
```

Read passwords only from a TTY with echo disabled. Refuse non-root callers and private-key paths outside the managed TLS directory after installation.

- [ ] **Step 5: Run TLS/setup/full tests**

Run: `go test ./internal/controlapi ./cmd/opensurge-setup && go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit LAN TLS support**

```bash
git add internal/controlapi cmd/opensurge-setup
git commit -m "feat: serve OpenSurge control UI over LAN HTTPS"
```

### Task 3: Convert helper protocol and API models from macOS to Linux

**Files:**
- Modify: `internal/controlapi/actions.go`, `internal/controlapi/actions_test.go`
- Modify: `internal/controlapi/models.go`, `internal/controlapi/server.go`, `internal/controlapi/server_test.go`
- Modify: `internal/controlapi/configuration_actions.go`
- Create: `cmd/opensurge-gateway/main.go`
- Delete: `cmd/opensurge-helper/main.go`, `cmd/opensurge-network/main.go`, `cmd/opensurge-install-config/main.go`, `internal/macosnetwork/`, `internal/mihomo/local_routing.go`, `internal/mihomo/local_routing_test.go`
- Modify: `cmd/opensurge/main.go`, `cmd/opensurge/main_test.go`

**Interfaces:**
- Produces `type GatewayClient interface { Run(context.Context, GatewayAction, string) error }`.
- Produces `type GatewayAction string` values `start`, `stop`, `reload`, `restart-mihomo`.
- Produces Linux `InterfaceOption` and `Neighbor` DTOs from `internal/linuxnet`.

- [ ] **Step 1: Write socket authorization and Linux DTO tests**

```go
func TestGatewaySocketRejectsUnknownAction(t *testing.T) {
    response := callGateway(t, HelperRequest{Action: "network-set-manual"})
    if response.OK || !strings.Contains(response.Error, "not allowed") { t.Fatalf("%+v", response) }
}

func TestNetworkInterfacesReturnsLinuxNames(t *testing.T) {
    server := newTestServer(t)
    server.listInterfaces = func(context.Context) ([]InterfaceOption, error) { return []InterfaceOption{{Name: "br-lan", IPv4: []string{"192.168.50.1/24"}}}, nil }
    response := getJSON(t, server.Handler(), "/api/v1/network/interfaces")
    if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"br-lan"`) || strings.Contains(response.Body.String(), "network_service") { t.Fatal(response.Body.String()) }
}
```

- [ ] **Step 2: Run focused tests before deleting macOS protocol paths**

Run: `go test ./internal/controlapi -run 'GatewaySocket|NetworkInterfaces'`

Expected: FAIL because the protocol still accepts macOS network actions and DTOs.

- [ ] **Step 3: Restrict the root socket to fixed Linux actions**

```go
func helperActionAllowed(action GatewayAction) bool {
    switch action {
    case GatewayStart, GatewayStop, GatewayReload, GatewayRestartMihomo,
         GatewayApplyProfile, GatewayApplyDevicePolicy, GatewayApplyControlConfig:
        return true
    default:
        return false
    }
}
```

Retain root-owned config-root validation and socket mode `0660` with group `opensurge`. Remove static-IP mutation, DHCP probing, macOS recovery snapshots, menubar endpoint, Mac-local routing, and local-system-proxy fields. Remove `local-routing` and `local-routing-set` CLI commands and their tests. For `same_wifi_dhcp`, retain only an operator confirmation/checklist recording router DHCP disabled, client validation, and router DHCP restored; it never changes the router.

- [ ] **Step 4: Wire Linux discovery into device and network endpoints**

Use `linuxnet.InterfaceInspector` and neighbor data for interface choices and optional device MAC observation. Preserve device policy CRUD, source import/refresh/apply, selector changes, connections, traffic, health, connectivity, providers, diagnostics, ETags, and idempotent operations.

- [ ] **Step 5: Run regression tests and search removed APIs**

Run: `go test ./internal/controlapi ./cmd/opensurge-gateway && sh -c '! rg -n "macosnetwork|networksetup|network-set-manual|local_system_proxy|menubar" internal cmd' && go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit Linux control-service protocol**

```bash
git add internal/controlapi internal/macosnetwork cmd
git commit -m "refactor: move Control API to Linux gateway service"
```

### Task 4: Add GUI login and Linux topology controls

**Files:**
- Create: `web/src/pages/LoginPage.tsx`, `web/src/pages/LoginPage.test.tsx`
- Modify: `web/src/App.tsx`, `web/src/App.test.tsx`, `web/src/api.ts`, `web/src/api.test.ts`, `web/src/types.ts`
- Modify: `web/src/pages/NetworkPage.tsx`, `web/src/pages/NetworkPage.test.tsx`
- Delete: `web/src/components/LocalRoutingCard.tsx`
- Modify: `web/src/styles.css`

**Interfaces:**
- Consumes `GET /api/v1/auth/status`, `POST /api/v1/auth/setup`, `POST /api/v1/auth/login`, `POST /api/v1/auth/logout`.
- Produces `type AuthStatus = { initialized: boolean; authenticated: boolean }`.

- [ ] **Step 1: Write failing UI tests**

```tsx
it('shows first-run account creation before the dashboard', async () => {
  vi.mocked(api.authStatus).mockResolvedValue({ initialized: false, authenticated: false })
  render(<App />)
  expect(await screen.findByRole('heading', { name: '创建管理员账户' })).toBeTruthy()
})

it('returns to login after a 401 response', async () => {
  vi.mocked(api.authStatus).mockResolvedValue({ initialized: true, authenticated: true })
  vi.mocked(api.overview).mockRejectedValue(new RequestError(401, 'authentication_required', 'expired'))
  render(<App />)
  expect(await screen.findByRole('heading', { name: '登录 OpenSurge' })).toBeTruthy()
})
```

- [ ] **Step 2: Run UI tests before component creation**

Run: `pnpm --dir web test -- LoginPage App`

Expected: FAIL because `LoginPage` and auth API methods are absent.

- [ ] **Step 3: Implement an authentication-aware shell**

```tsx
if (!auth.initialized || !auth.authenticated) {
  return <LoginPage initialized={auth.initialized} onAuthenticated={refreshAuth} />
}
```

Fetch auth status before overview polling. Login sends only JSON username/password and stores neither in browser storage. Change `for Mac` to `for Linux`; remove menu-bar recovery language and Mac-local routing controls.

- [ ] **Step 4: Update the network page contract**

Show Linux interface names. `same_lan` shows DNS-only behavior plus IPv6 bypass warning. `same_wifi_dhcp` disables Start until the operator confirms the router DHCP service is already disabled and presents manual restore instructions after stop. `isolated_lan` requires a distinct wired interface or VLAN and has no Wi-Fi AP choice.

- [ ] **Step 5: Run frontend and combined tests**

Run: `make web-test && go test ./...`

Expected: PASS; rendered product text has no “for Mac”, “menu bar”, or automatic router-DHCP promise.

- [ ] **Step 6: Commit Linux GUI control plane**

```bash
git add web
git commit -m "feat: add Linux LAN control UI authentication"
```

### Task 5: Install and validate systemd service boundaries

**Files:**
- Create: `packaging/systemd/opensurge-gateway.service`, `packaging/systemd/opensurge-gateway.socket`, `packaging/systemd/opensurge-control.service`
- Create: `packaging/systemd/opensurge-control.service.d/security.conf`
- Create: `tests/systemd/units_test.sh`
- Modify: `Makefile`

**Interfaces:**
- Produces socket `/run/opensurge/gateway.sock` with group `opensurge`.
- Produces `make systemd-unit-test`.

- [ ] **Step 1: Write static unit assertions**

```sh
grep -Fx 'User=opensurge' packaging/systemd/opensurge-control.service
grep -Fx 'Group=opensurge' packaging/systemd/opensurge-control.service
grep -Fx 'SocketMode=0660' packaging/systemd/opensurge-gateway.socket
grep -Fx 'SocketGroup=opensurge' packaging/systemd/opensurge-gateway.socket
! grep -q 'User=' packaging/systemd/opensurge-gateway.service
```

- [ ] **Step 2: Run assertions before creating units**

Run: `make systemd-unit-test`

Expected: FAIL because unit files and target are absent.

- [ ] **Step 3: Create least-privilege units**

Gateway runs root with `ExecStart=/usr/lib/opensurge/opensurge-gateway --socket /run/opensurge/gateway.sock --config-root /etc/opensurge`. Control runs as `opensurge`, has `SupplementaryGroups=opensurge`, `NoNewPrivileges=true`, `PrivateTmp=true`, `ProtectSystem=strict`, `ReadWritePaths=/var/lib/opensurge /run/opensurge`, and certificate read access through the group. The socket unit creates the runtime directory and starts the gateway service on demand.

- [ ] **Step 4: Run unit syntax/security validation**

Run: `make systemd-unit-test`

Expected: PASS; on a systemd host also run `systemd-analyze verify packaging/systemd/*.service packaging/systemd/*.socket`.

- [ ] **Step 5: Commit systemd services**

```bash
git add packaging/systemd tests/systemd Makefile
git commit -m "feat: add privileged Linux gateway services"
```
