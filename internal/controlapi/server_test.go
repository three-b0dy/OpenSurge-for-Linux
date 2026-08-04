package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/device"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/doctor"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/linuxnet"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/mihomo"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/runtime"
)

const (
	testManagementAddr = "192.168.50.1:61767"
	testManagementURL  = "https://" + testManagementAddr
)

func TestDoctorChecksForControlHidesRootPrivileges(t *testing.T) {
	checks := []doctor.Check{
		{Name: "root privileges", OK: false, Message: "start/stop require sudo"},
		{Name: "dnsmasq", OK: true},
	}

	visible := doctorChecksForControl(checks)
	if len(visible) != 1 || visible[0].Name != "dnsmasq" {
		t.Fatalf("doctorChecksForControl() = %#v", visible)
	}
	if !doctorHealthyForControl(visible) {
		t.Fatal("root privileges must not make the GUI control-plane health check fail")
	}
}

func TestInspectSourceInventory(t *testing.T) {
	data := []byte(`proxies:
  - name: edge
    type: http
proxy-groups:
  - name: Main
    type: select
    proxies: [DIRECT, edge]
proxy-providers:
  subscription: {type: http, url: "https://example.com/sub"}
rule-providers:
  media: {type: http, behavior: domain, url: "https://example.com/rules"}
rules:
  - RULE-SET,media,Main
  - MATCH,DIRECT
`)
	inv, err := inspectSource(data, "mihomo_profile")
	if err != nil {
		t.Fatalf("inspectSource() error = %v", err)
	}
	if !inv.TerminalMatch || inv.RuleCount != 2 || len(inv.ProxyGroups) != 1 || inv.ProxyGroups[0] != "Main" {
		t.Fatalf("inventory = %#v", inv)
	}
}

func TestInspectSourceFlowStyleInventoryMatchesRendererValidation(t *testing.T) {
	data := []byte(`'proxy-groups': [{name: Zeta, type: select, proxies: [DIRECT]}, {name: Alpha, type: select, proxies: [DIRECT]}]
'rule-providers': {zeta: {type: inline, behavior: domain, payload: [zeta.example]}, alpha: {type: inline, behavior: domain, payload: [alpha.example]}}
'rules': ['RULE-SET,zeta,Zeta', 'MATCH,Zeta']
`)
	inv, err := inspectSource(data, "mihomo_profile")
	if err != nil {
		t.Fatalf("inspectSource() error = %v", err)
	}
	if !inv.TerminalMatch || inv.RuleCount != 2 ||
		!reflect.DeepEqual(inv.ProxyGroups, []string{"Zeta", "Alpha"}) ||
		!reflect.DeepEqual(inv.RuleProviders, []string{"zeta", "alpha"}) ||
		len(inv.Warnings) != 0 {
		t.Fatalf("inventory = %#v", inv)
	}
}

func TestInspectSourceInvalidTopLevelReturnsEmptyCollections(t *testing.T) {
	inventory, err := inspectSource([]byte("c3M6Ly9leGFtcGxlLmNvbQo="), "mihomo_profile")
	if err == nil || !strings.Contains(err.Error(), "top-level YAML must be a mapping") {
		t.Fatalf("inspectSource() error = %v", err)
	}
	if inventory.Proxies == nil || inventory.ProxyProviders == nil || inventory.ProxyGroups == nil || inventory.RuleProviders == nil || inventory.Warnings == nil {
		t.Fatalf("invalid source inventory contains nil collections: %#v", inventory)
	}
}

func TestInspectSourceRejectsReservedNamespace(t *testing.T) {
	tests := map[string]string{
		"device group": `proxy-groups:
  - name: device/phone/default
    type: select
    proxies: [DIRECT]
rules: ["MATCH,DIRECT"]
`,
		"managed namespace": `proxies:
  - name: open-surge-ruleset-imported
    type: direct
rules: ["MATCH,DIRECT"]
`,
	}
	for name, profile := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := inspectSource([]byte(profile), "mihomo_profile")
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("inspectSource() error = %v", err)
			}
		})
	}
}

func TestHandlerDoesNotExposeRetiredRoutingEndpoint(t *testing.T) {
	server := newTestServer(t)
	path := "/api/v1/" + "local-" + "routing"
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := performAuthorized(server, method, path, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s status=%d body=%s", method, path, response.Code, response.Body.String())
		}
	}
}

func TestSourceRequestUsesMihomoCompatibleUserAgent(t *testing.T) {
	request, err := newSourceRequest(t.Context(), "https://example.com/subscription")
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("User-Agent"); got != "clash.meta" {
		t.Fatalf("User-Agent = %q, want clash.meta", got)
	}
}

func TestAuthenticatedWebSessionSlidesIdleExpiry(t *testing.T) {
	server := newTestServer(t)
	const session = "browser-session"
	server.sessions[session] = time.Now().Add(time.Minute)
	started := time.Now()

	request := httptest.NewRequest(http.MethodGet, testManagementURL+"/api/test", nil)
	request.Host = testManagementAddr
	request.AddCookie(&http.Cookie{Name: "opensurge_session", Value: session})
	response := httptest.NewRecorder()
	server.auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("session request status=%d body=%s", response.Code, response.Body.String())
	}
	server.mu.Lock()
	expires := server.sessions[session]
	server.mu.Unlock()
	if expires.Before(started.Add(webSessionIdleTimeout - time.Minute)) {
		t.Fatalf("session expiry was not renewed: %s", expires)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "opensurge_session" || cookies[0].Value != session {
		t.Fatalf("renewal cookies=%v", cookies)
	}
	if cookies[0].MaxAge != int(webSessionIdleTimeout/time.Second) || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("renewal cookie=%#v", cookies[0])
	}
}

func TestExpiredWebSessionIsRejectedWithoutRenewal(t *testing.T) {
	server := newTestServer(t)
	called := false
	request := httptest.NewRequest(http.MethodGet, testManagementURL+"/api/test", nil)
	request.Host = testManagementAddr
	request.AddCookie(&http.Cookie{Name: "opensurge_session", Value: "expired"})
	response := httptest.NewRecorder()
	server.auth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || called {
		t.Fatalf("expired session status=%d handler_called=%t", response.Code, called)
	}
	if cookies := response.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("expired session was renewed: %v", cookies)
	}
	server.mu.Lock()
	_, exists := server.sessions["expired"]
	server.mu.Unlock()
	if exists {
		t.Fatal("expired session was not removed")
	}
}

func TestSafeDialRejectsLoopback(t *testing.T) {
	ctx := t.Context()
	_, err := safeDialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", "443"))
	if err == nil {
		t.Fatal("safeDialContext() accepted loopback")
	}
}

func TestOperationHistoryIsNewestFirstAndLimited(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Ensure(); err != nil {
		t.Fatal(err)
	}
	older := Operation{ID: "older", Kind: "start", State: "succeeded", UpdatedAt: time.Now().Add(-time.Minute)}
	newer := Operation{ID: "newer", Kind: "stop", State: "failed", UpdatedAt: time.Now()}
	if err := store.SaveOperation(older); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOperation(newer); err != nil {
		t.Fatal(err)
	}
	operations, err := store.Operations(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].ID != "newer" {
		t.Fatalf("operations=%#v", operations)
	}
}

func TestGatewayRejectsUserOwnedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("gateway: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("test requires a non-root process")
	}
	if err := requireRootOwnedConfig(path); err == nil {
		t.Fatal("requireRootOwnedConfig() accepted a user-owned file")
	}
}

func TestGatewayRejectsActionOutsideWhitelist(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go handleGatewayConn(t.Context(), serverConn, t.TempDir())
	if err := json.NewEncoder(clientConn).Encode(HelperRequest{Action: "shell"}); err != nil {
		t.Fatal(err)
	}
	var response HelperResponse
	if err := json.NewDecoder(clientConn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error != "action is not allowed" {
		t.Fatalf("response = %#v", response)
	}
}

func TestGatewayAllowlistIncludesNamedLifecycleActions(t *testing.T) {
	if !helperActionAllowed(GatewayReload) {
		t.Fatal("reload is not available to the privileged gateway")
	}
	if !helperActionAllowed(GatewayRestartMihomo) {
		t.Fatal("restart-mihomo is not available to the privileged gateway")
	}
	for _, action := range []string{"hot-reload", "restart", "shell"} {
		if helperActionAllowed(GatewayAction(action)) {
			t.Fatalf("unexpected gateway action %q", action)
		}
	}
}

func TestGatewayRestartMihomoDefersInvalidDesiredDevicePolicy(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(filepath.Join(dir, "device-policy.json"), []byte("{invalid-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("device_policy:\n  file: ./device-policy.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGatewayConfig(GatewayRestartMihomo, configPath); err != nil {
		t.Fatalf("restart-mihomo runtime config error=%v", err)
	}
	if _, err := loadGatewayConfig(GatewayStart, configPath); err == nil {
		t.Fatal("start accepted an invalid desired device policy")
	}
}

func TestTrustedPathRejectsEscapesAndUserOwnedFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "mihomo")
	if err := os.WriteFile(outside, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := trustedPathWithinRoot(outside, root); err == nil {
		t.Fatal("outside path was accepted")
	}
	inside := filepath.Join(root, "mihomo")
	if err := os.WriteFile(inside, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if err := requireTrustedFile(inside, root, true); err == nil {
			t.Fatal("user-owned executable was accepted")
		}
	}
}

func TestPublicSourcesKeepsEmptyArray(t *testing.T) {
	if sources := publicSources([]Source{}); sources == nil {
		t.Fatal("publicSources returned nil for an empty collection")
	}
}

func TestPublicSourcesNormalizesCurrentAndHistoricalInventory(t *testing.T) {
	public := publicSources([]Source{{
		Inventory: Inventory{},
		Versions:  []SourceVersion{{Inventory: Inventory{}}},
	}})
	data, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"proxies", "proxy_providers", "proxy_groups", "rule_providers", "warnings"} {
		if strings.Contains(string(data), `"`+field+`":null`) {
			t.Fatalf("public source contains null %s: %s", field, data)
		}
	}
}

func TestPublicSourcesRedactsFetchURLAndPath(t *testing.T) {
	public := publicSources([]Source{{Origin: "https://example.com/profile", FetchURL: "https://token@example.com/profile?secret=1", SnapshotPath: "/private/source.yaml"}})
	if public[0].FetchURL != "" || public[0].SnapshotPath != "" {
		t.Fatalf("public source leaked private fields: %#v", public[0])
	}
}

func TestHTTPSSourceMetadataNeverPersistsFetchURL(t *testing.T) {
	server := newTestServer(t)
	source, err := server.importReader("subscription", "mihomo_profile", "https://example.com/profile", strings.NewReader("rules:\n  - MATCH,DIRECT\n"))
	if err != nil {
		t.Fatal(err)
	}
	if source.FetchURL != "" {
		t.Fatal("import result retained a fetch URL")
	}
	stored, err := server.store.Sources()
	if err != nil || len(stored) != 1 || stored[0].FetchURL != "" {
		t.Fatalf("stored sources = %#v err=%v", stored, err)
	}
}

func TestLegacySourceCredentialMigratesOutOfJSON(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSources([]Source{{ID: "source-1", FetchURL: "https://token@example.com/profile?secret=1"}}); err != nil {
		t.Fatal(err)
	}
	credentials := &memoryCredentialStore{}
	if err := migrateSourceCredentials(t.Context(), store, credentials); err != nil {
		t.Fatal(err)
	}
	if value, err := credentials.Get(t.Context(), "source-1"); err != nil || value != "https://token@example.com/profile?secret=1" {
		t.Fatalf("credential=%q err=%v", value, err)
	}
	sources, err := store.Sources()
	if err != nil || sources[0].FetchURL != "" {
		t.Fatalf("sources=%#v err=%v", sources, err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir(), "sources.json"))
	if err != nil || strings.Contains(string(raw), "secret=1") {
		t.Fatalf("legacy secret remains: %s err=%v", raw, err)
	}
}

func TestSourceRefreshPreservesAppliedVersionAndBuildsInventoryDiff(t *testing.T) {
	server := newTestServer(t)
	first, err := server.importReader("home", "mihomo_profile", "file:home.yaml", strings.NewReader("proxies:\n  - {name: old, type: direct}\nproxy-groups:\n  - {name: Main, type: select, proxies: [DIRECT]}\nrules:\n  - MATCH,DIRECT\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadRuntime(server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Mihomo.ProfileMode = config.MihomoProfileModeImported
	cfg.Mihomo.Profile = first.SnapshotPath
	if err := os.WriteFile(server.configPath, []byte(config.Render(cfg)), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := runtime.NewPaths(cfg)
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{ProfileDigest: first.Digest, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	second, err := server.importReader("home", "mihomo_profile", "file:home.yaml", strings.NewReader("proxies:\n  - {name: new, type: direct}\nproxy-groups:\n  - {name: Main, type: select, proxies: [DIRECT]}\nrules:\n  - DOMAIN,example.com,DIRECT\n  - MATCH,DIRECT\n"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Applied || len(second.Versions) != 1 || !second.Versions[0].Applied {
		t.Fatalf("versions = %#v", second)
	}
	if second.Diff.PreviousDigest != first.Digest || len(second.Diff.ProxiesAdded) != 1 || second.Diff.ProxiesAdded[0] != "new" || second.Diff.RuleCountDelta != 1 {
		t.Fatalf("diff = %#v", second.Diff)
	}
	public := publicSources([]Source{second})[0]
	if public.Versions[0].SnapshotPath != "" {
		t.Fatal("public version leaked snapshot path")
	}
}

func TestSourceApplyDelegatesAuthoritativeEngineValidationToRunner(t *testing.T) {
	server := newTestServer(t)
	source, err := server.importReader("home", "mihomo_profile", "file:home.yaml", strings.NewReader("rules:\n  - MATCH,DIRECT\n"))
	if err != nil {
		t.Fatal(err)
	}
	revision := fileDigest(server.configPath)
	request := httptest.NewRequest(http.MethodPost, testManagementURL+"/api/v1/sources/"+source.ID+"/apply", nil)
	request.Host = testManagementAddr
	request.SetPathValue("id", source.ID)
	authorizeTestRequest(server, request)
	request.Header.Set("If-Match", `"`+revision+`"`)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}

	server.configRunner = fakeConfigurationRunner{profileErr: errors.New("mihomo config validation failed: geodata unavailable")}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "mihomo_validation_failed") {
		t.Fatalf("engine failure status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDevicePolicyUsesOptimisticRevisionAndConfigurationRunner(t *testing.T) {
	server := newTestServer(t)
	get := performAuthorized(server, http.MethodGet, "/api/v1/device-policy", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	var document DevicePolicyResponse
	if err := json.Unmarshal(get.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	conflict := performAuthorized(server, http.MethodPut, "/api/v1/device-policy", []byte(`{"devices":[],"profiles":[],"templates":[],"rule_sets":[]}`))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	request := httptest.NewRequest(http.MethodPut, testManagementURL+"/api/v1/device-policy", strings.NewReader(`{"devices":[],"profiles":[],"templates":[],"rule_sets":[]}`))
	request.Host = testManagementAddr
	authorizeTestRequest(server, request)
	request.Header.Set("If-Match", `"`+document.Revision+`"`)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestChangedDeviceIDsTracksResolvedPrivateProfileChanges(t *testing.T) {
	applied := device.PolicySet{
		Devices: []device.ManagedDevice{
			{ID: "alice", MAC: "aa:bb:cc:dd:ee:01", IPv4: "192.168.1.121", Profile: "alice-policy"},
			{ID: "bob", MAC: "aa:bb:cc:dd:ee:02", IPv4: "192.168.1.122", Profile: "bob-policy"},
		},
		Profiles: []device.Profile{
			{ID: "alice-policy", DefaultPolicies: []string{"DIRECT"}},
			{ID: "bob-policy", DefaultPolicies: []string{"DIRECT"}},
		},
	}
	desired := applied
	desired.Profiles = append([]device.Profile(nil), applied.Profiles...)
	desired.Profiles[0].Rules = []device.Rule{{ID: "youtube", Match: device.RuleMatch{Domains: []string{"youtube.example"}}, Action: "REJECT"}}
	changed := changedDeviceIDs(desired, applied)
	if !reflect.DeepEqual(changed, []string{"alice"}) {
		t.Fatalf("changed devices=%v", changed)
	}

	desired = applied
	desired.Devices = append([]device.ManagedDevice(nil), applied.Devices...)
	desired.Devices[0].EgressMode = device.EgressModeDedicated
	changed = changedDeviceIDs(desired, applied)
	if !reflect.DeepEqual(changed, []string{"alice"}) {
		t.Fatalf("egress-mode changed devices=%v", changed)
	}
}

func TestControlConfigUsesRevisionAndAppliesTopology(t *testing.T) {
	server := newTestServer(t)
	get := performAuthorized(server, http.MethodGet, "/api/v1/config", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	var current ControlConfig
	if err := json.Unmarshal(get.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	current.Gateway.Mode = config.GatewayModeSameLAN
	current.DHCP.Enabled = false
	requestBody, _ := json.Marshal(current)
	conflict := performAuthorized(server, http.MethodPut, "/api/v1/config", requestBody)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	request := httptest.NewRequest(http.MethodPut, testManagementURL+"/api/v1/config", bytes.NewReader(requestBody))
	request.Host = testManagementAddr
	authorizeTestRequest(server, request)
	request.Header.Set("If-Match", `"`+current.Revision+`"`)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err := config.Load(server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Gateway.Mode != config.GatewayModeSameLAN || updated.DHCP.Enabled {
		t.Fatalf("updated config=%#v", updated)
	}
}

func TestControlConfigShowsMihomoDNSForLegacyEmptyUpstream(t *testing.T) {
	cfg := config.Default()
	cfg.DNS.Upstream = ""
	if got := controlConfigFrom(cfg, "revision").DNS.Upstream; got != config.MihomoDNSUpstream {
		t.Fatalf("DNS upstream = %q, want %q", got, config.MihomoDNSUpstream)
	}
}

func TestStateEventCarriesConfigGatewayAndRecoveryState(t *testing.T) {
	server := newTestServer(t)
	state, err := server.stateEvent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision == "" || state.Gateway == "" || state.Recovery.Stage != RecoveryIdle {
		t.Fatalf("state event = %#v", state)
	}
}

func TestControlConfigCanInitializeDevicePolicyFile(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Runtime.Dir = filepath.Join(dir, "runtime")
	cfg.Mihomo.Config = filepath.Join(dir, "runtime", "mihomo.yaml")
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(config.Render(cfg)), 0o600); err != nil {
		t.Fatal(err)
	}
	input := controlConfigFrom(cfg, fileDigest(path))
	input.DevicePolicy.Enabled = true
	payload, _ := json.Marshal(input)
	if _, err := applyControlConfig(path, input.Revision, payload); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DevicePolicy.File == "" {
		t.Fatal("device policy file was not initialized")
	}
	if _, err := os.Stat(updated.DevicePolicy.File); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticLogTailRedactsKnownCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo.log")
	if err := os.WriteFile(path, []byte("secret-token proxy-user proxy-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mihomo.Secret = "secret-token"
	cfg.UpstreamProxy.Username = "proxy-user"
	cfg.UpstreamProxy.Password = "proxy-password"
	lines := tailLines(path, 10, cfg)
	if len(lines) != 1 || strings.Contains(lines[0], "secret") || strings.Contains(lines[0], "proxy-user") || strings.Contains(lines[0], "proxy-password") {
		t.Fatalf("redacted lines = %#v", lines)
	}
}

func TestDeviceTrafficKeepsLeaseInventoryWhenMihomoIsUnavailable(t *testing.T) {
	server := newTestServer(t)
	cfg, err := config.LoadRuntime(server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	policy := `{"devices":[{"id":"iphone-15","name":"Living Room iPhone","mac":"aa:bb:cc:dd:ee:ff","ipv4":"192.168.1.151","profile":"home","egress_mode":"inherit_global"}],"profiles":[{"id":"home","default_policies":["DIRECT"]}],"templates":[],"rule_sets":[]}`
	if err := os.WriteFile(cfg.DevicePolicy.File, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	server.fetchConnections = func(context.Context, config.Config) (mihomo.ConnectionsSnapshot, error) {
		return mihomo.ConnectionsSnapshot{}, errors.New("mihomo unavailable")
	}
	paths := runtime.NewPaths(cfg)
	if err := os.MkdirAll(filepath.Dir(paths.LeaseFile), 0o700); err != nil {
		t.Fatal(err)
	}
	lease := fmt.Sprintf("%d aa:bb:cc:dd:ee:ff 192.168.1.151 iPhone-15 *\n", time.Now().Add(time.Hour).Unix())
	if err := os.WriteFile(paths.LeaseFile, []byte(lease), 0o600); err != nil {
		t.Fatal(err)
	}

	response := performAuthorized(server, http.MethodGet, "/api/v1/device-traffic", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("device traffic status=%d body=%s", response.Code, response.Body.String())
	}
	var payload DeviceTrafficResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Scope != deviceTrafficScope || len(payload.Devices) != 1 || payload.Devices[0].Hostname != "iPhone-15" || payload.Devices[0].Name != "Living Room iPhone" {
		t.Fatalf("device traffic = %#v", payload)
	}
	if payload.ConnectionError == "" || payload.Totals.Devices != 1 || payload.Totals.ActiveConnections != 0 {
		t.Fatalf("unavailable mihomo response = %#v", payload)
	}
	if payload.GatewayLocal.IP != "192.168.1.20" || payload.GatewayLocal.IdentitySource != identitySourceGatewayLocal || payload.GatewayLocal.Transport != localTransportTUN {
		t.Fatalf("gateway local fallback = %#v", payload.GatewayLocal)
	}
}

func TestDeviceTrafficEndpointAttributesLiveMihomoConnections(t *testing.T) {
	server := newTestServer(t)
	cfg, err := config.LoadRuntime(server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	server.fetchConnections = func(context.Context, config.Config) (mihomo.ConnectionsSnapshot, error) {
		return mihomo.ConnectionsSnapshot{UploadTotal: 100, DownloadTotal: 900, Connections: []mihomo.Connection{
			{ID: "one", Upload: 100, Download: 900, Chains: []string{"流媒体组", "美国-02"}, Metadata: map[string]any{"sourceIP": "192.168.1.188"}},
			{ID: "local", Upload: 20, Download: 80, Chains: []string{"Proxy", "edge"}, Metadata: map[string]any{"sourceIP": "198.18.0.1", "type": "Tun", "process": "Safari"}},
			{ID: "observed", Upload: 10, Download: 40, Chains: []string{"DIRECT"}, Metadata: map[string]any{"sourceIP": "192.168.1.189"}},
		}}, nil
	}
	paths := runtime.NewPaths(cfg)
	if err := os.MkdirAll(filepath.Dir(paths.LeaseFile), 0o700); err != nil {
		t.Fatal(err)
	}
	lease := fmt.Sprintf("%d aa:bb:cc:dd:ee:88 192.168.1.188 Apple-TV *\n", time.Now().Add(time.Hour).Unix())
	if err := os.WriteFile(paths.LeaseFile, []byte(lease), 0o600); err != nil {
		t.Fatal(err)
	}

	response := performAuthorized(server, http.MethodGet, "/api/v1/device-traffic", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("device traffic status=%d body=%s", response.Code, response.Body.String())
	}
	var payload DeviceTrafficResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ConnectionError != "" || len(payload.Devices) != 2 {
		t.Fatalf("device traffic = %#v", payload)
	}
	var device DeviceTraffic
	for _, candidate := range payload.Devices {
		if candidate.IP == "192.168.1.188" {
			device = candidate
			break
		}
	}
	if device.ActiveConnections != 1 || device.Upload != 100 || device.Download != 900 || device.PrimaryEgress != "流媒体组 → 美国-02" {
		t.Fatalf("attributed device = %#v", device)
	}
	if payload.GatewayLocal.IP != "192.168.1.20" || payload.GatewayLocal.ActiveConnections != 1 || payload.GatewayLocal.Transport != localTransportTUN {
		t.Fatalf("gateway local = %#v", payload.GatewayLocal)
	}
	if payload.UnidentifiedDeviceConnections != 1 || payload.UnclassifiedConnections != 0 || payload.UnmatchedConnections != 1 {
		t.Fatalf("connection categories = %#v", payload)
	}
}

func TestSameLANDevicesEndpointListsSourcesCurrentlyPassingThroughGateway(t *testing.T) {
	server := newTestServer(t)
	cfg, err := config.LoadRuntime(server.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Gateway.Mode = config.GatewayModeSameLAN
	cfg.DHCP.Enabled = false
	policy := `{"devices":[{"id":"living-room","name":"Living Room","mac":"aa:bb:cc:dd:ee:37","ipv4":"192.168.1.137","profile":"home","egress_mode":"inherit_global"}],"profiles":[{"id":"home","default_policies":["DIRECT"]}],"templates":[],"rule_sets":[]}`
	if err := os.WriteFile(cfg.DevicePolicy.File, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server.configPath, []byte(config.Render(cfg)), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := device.LoadPolicyBundle(cfg.DevicePolicy.File)
	if err != nil {
		t.Fatal(err)
	}
	paths := runtime.NewPaths(cfg)
	if err := device.WritePolicyBundleSnapshot(paths.DevicePolicyApplied, bundle); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{DevicePolicyDigest: bundle.Digest}); err != nil {
		t.Fatal(err)
	}
	server.fetchConnections = func(context.Context, config.Config) (mihomo.ConnectionsSnapshot, error) {
		return mihomo.ConnectionsSnapshot{Connections: []mihomo.Connection{
			{Upload: 100, Download: 900, Chains: []string{"Proxy", "edge"}, Metadata: map[string]any{"sourceIP": "192.168.1.137"}},
			{Metadata: map[string]any{"sourceIP": "192.168.1.137"}},
			{Metadata: map[string]any{"sourceIP": "192.168.2.20"}},
		}}, nil
	}
	server.discoverNeighbors = func(context.Context, string) ([]linuxnet.Neighbor, error) {
		return []linuxnet.Neighbor{{IPv4: netip.MustParseAddr("192.168.1.137"), MAC: "AA:BB:CC:DD:EE:37"}}, nil
	}

	response := performAuthorized(server, http.MethodGet, "/api/v1/devices", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("devices status=%d body=%s", response.Code, response.Body.String())
	}
	var payload DevicesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ObservationError != "" || len(payload.ObservedDevices) != 1 {
		t.Fatalf("observed devices response = %#v", payload)
	}
	observed := payload.ObservedDevices[0]
	if observed.IP != "192.168.1.137" || observed.MAC != "aa:bb:cc:dd:ee:37" || !observed.NeighborObserved || observed.ActiveConnections != 2 {
		t.Fatalf("observed device = %#v", observed)
	}

	trafficResponse := performAuthorized(server, http.MethodGet, "/api/v1/device-traffic", nil)
	if trafficResponse.Code != http.StatusOK {
		t.Fatalf("device traffic status=%d body=%s", trafficResponse.Code, trafficResponse.Body.String())
	}
	var traffic DeviceTrafficResponse
	if err := json.Unmarshal(trafficResponse.Body.Bytes(), &traffic); err != nil {
		t.Fatal(err)
	}
	if len(traffic.Devices) != 1 || traffic.Devices[0].Name != "Living Room" || traffic.Devices[0].MAC != "aa:bb:cc:dd:ee:37" || traffic.Devices[0].IdentitySource != identitySourceRegisteredStatic || traffic.Devices[0].ActiveConnections != 2 || traffic.Devices[0].Upload != 100 || traffic.Devices[0].PrimaryEgress != "Proxy → edge" {
		t.Fatalf("same-LAN device traffic = %#v", traffic)
	}
}

func newTestServer(t *testing.T) *Server {
	server, _ := newTestServerWithLinuxDiscovery(t)
	return server
}

func newTestServerWithLinuxDiscovery(t *testing.T) (*Server, *fakeInterfaceDeps) {
	t.Helper()
	dir := t.TempDir()
	mihomoAPI := newReadyMihomoTestServer(t)
	configPath := filepath.Join(dir, "config.yaml")
	policyPath := filepath.Join(dir, "device-policy.json")
	if err := os.WriteFile(policyPath, []byte(`{"devices":[],"profiles":[],"templates":[],"rule_sets":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`gateway:
  mode: "same_wifi_dhcp"
  interface: "en0"
  lan_ip: "192.168.1.20"
  upstream_interface: "en0"
dhcp:
  enabled: true
  range_start: "192.168.1.120"
  range_end: "192.168.1.199"
device_policy:
  file: "`+policyPath+`"
transparent:
  mode: "tun"
mihomo:
  api_addr: "`+mihomoAPI.URL+`"
runtime:
  dir: "`+filepath.Join(dir, "runtime")+`"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	network := &fakeInterfaceDeps{}
	server, err := New(Options{ConfigPath: configPath, Addr: testManagementAddr, StoreDir: filepath.Join(dir, "store"), Runner: fakeRunner{}, ConfigRunner: fakeConfigurationRunner{}, ListInterfaces: func(context.Context) ([]InterfaceOption, error) {
		return []InterfaceOption{{Name: "en0", IPv4: []string{"192.168.1.20/24"}}, {Name: "en7", IPv4: []string{}}}, nil
	}, DiscoverNeighbors: func(context.Context, string) ([]linuxnet.Neighbor, error) { return []linuxnet.Neighbor{}, nil }, Static: http.NotFoundHandler(), Credentials: &memoryCredentialStore{}})
	if err != nil {
		t.Fatal(err)
	}
	server.sessions["expired"] = time.Now().Add(-time.Minute)
	return server, network
}

func newReadyMihomoTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"version":"test","meta":true}`))
		case "/configs":
			_, _ = w.Write([]byte(`{"tun":{"enable":true,"device":"utun-test"}}`))
		case "/proxies", "/providers/proxies", "/providers/rules":
			_, _ = w.Write([]byte(`{"proxies":{},"providers":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

type fakeRunner struct{}

func (fakeRunner) Run(_ context.Context, _ GatewayAction, _ string) error { return nil }

type recordingActionRunner struct {
	action GatewayAction
	count  int
}

func (r *recordingActionRunner) Run(_ context.Context, action GatewayAction, _ string) error {
	r.action = action
	r.count++
	return nil
}

type actionRunnerFunc func(context.Context, GatewayAction, string) error

func (f actionRunnerFunc) Run(ctx context.Context, action GatewayAction, configPath string) error {
	return f(ctx, action, configPath)
}

func waitForStoredOperation(t *testing.T, server *Server, id, state string) Operation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, err := server.store.Operation(id)
		if err == nil && op.State == state {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	op, err := server.store.Operation(id)
	t.Fatalf("operation %q did not reach %q: op=%#v err=%v", id, state, op, err)
	return Operation{}
}

type fakeConfigurationRunner struct {
	profileErr      error
	profileReloaded bool
}

func (f fakeConfigurationRunner) ApplyProfile(_ context.Context, _, revision string, _ []byte) (ProfileApplyResult, error) {
	if f.profileErr != nil {
		return ProfileApplyResult{}, f.profileErr
	}
	return ProfileApplyResult{Revision: revision + "-applied", Reloaded: f.profileReloaded}, nil
}

func (fakeConfigurationRunner) ApplyDevicePolicy(_ context.Context, _, _ string, payload []byte) (string, error) {
	var policy device.PolicySet
	if err := json.Unmarshal(payload, &policy); err != nil {
		return "", err
	}
	bundle, err := device.CompilePolicyBundle(policy)
	return bundle.Digest, err
}

func (fakeConfigurationRunner) ApplyControlConfig(_ context.Context, path, revision string, payload []byte) (string, error) {
	return applyControlConfig(path, revision, payload)
}

type fakeInterfaceDeps struct{}

func performAuthorized(server *Server, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, testManagementURL+path, bytes.NewReader(body))
	request.Host = testManagementAddr
	authorizeTestRequest(server, request)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func authorizeTestRequest(server *Server, request *http.Request) {
	const session = "test-session"
	server.mu.Lock()
	server.sessions[session] = time.Now().Add(webSessionIdleTimeout)
	server.mu.Unlock()
	request.AddCookie(&http.Cookie{Name: "opensurge_session", Value: session, Path: "/"})
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		request.Header.Set("Origin", testManagementURL)
	}
}
