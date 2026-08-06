package gateway

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/device"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/runtime"
)

func TestStartRollsBackWhenMihomoStartFails(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "lan0"
	cfg.Gateway.UpstreamInterface = "wan0"
	cfg.Gateway.LANIP = "192.168.50.1"
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)

	dhcpManager := &fakeDHCP{startPID: 111}
	mihomoManager := &fakeMihomo{startErr: errors.New("mihomo start failed")}
	nftManager := &fakeNft{}
	sysctlManager := &fakeSysctl{current: "0"}

	manager := Manager{
		cfg:   cfg,
		paths: paths,
		deps: gatewayDeps{
			geteuid:     func() int { return 0 },
			loadState:   runtime.LoadState,
			saveState:   runtime.SaveState,
			removeState: runtime.RemoveState,
			ensure:      runtime.Ensure,
			newDHCP: func(config.Config, runtime.Paths) dhcpService {
				return dhcpManager
			},
			newMihomo: func(config.Config, runtime.Paths) mihomoService {
				return mihomoManager
			},
			newNft: func(config.Config, runtime.Paths) nftService {
				return nftManager
			},
			newSysctl: func() sysctlService {
				return sysctlManager
			},
			interfaces: func() ([]net.Interface, error) {
				return []net.Interface{{Name: cfg.Gateway.Interface}}, nil
			},
			interfaceByName: func(name string) (*net.Interface, error) {
				return &net.Interface{Name: name}, nil
			},
			interfaceAddrs: func(*net.Interface) ([]net.Addr, error) {
				return []net.Addr{&net.IPNet{
					IP:   net.ParseIP(cfg.Gateway.LANIP),
					Mask: net.CIDRMask(24, 32),
				}}, nil
			},
			now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		},
	}

	err := manager.Start(context.Background())
	if err == nil {
		t.Fatalf("Start() succeeded")
	}
	if !strings.Contains(err.Error(), "mihomo start failed") {
		t.Fatalf("Start() error = %q", err)
	}
	if !sysctlManager.enableCalled {
		t.Fatalf("sysctl Enable() was not called")
	}
	if sysctlManager.restoreValue != "0" {
		t.Fatalf("sysctl Restore() = %q, want 0", sysctlManager.restoreValue)
	}
	if dhcpManager.startCalled {
		t.Fatalf("dnsmasq Start() was called after mihomo failure")
	}
	if !dhcpManager.stopCalled {
		t.Fatalf("dnsmasq Stop() was not called during rollback")
	}
	if !mihomoManager.stopCalled {
		t.Fatalf("mihomo Stop() was not called during rollback")
	}
	if nftManager.loadCalled {
		t.Fatalf("nftables Load() was called before mihomo succeeded")
	}
	if nftManager.unloadCalled {
		t.Fatalf("nftables Unload() was called before nftables load was attempted")
	}
	if _, exists, err := runtime.LoadState(paths.StateFile); err != nil {
		t.Fatalf("LoadState() error = %v", err)
	} else if exists {
		t.Fatalf("runtime state still exists after rollback")
	}
}

func TestStartLoadsNftablesAfterMihomoAndDNSMasq(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "lan0"
	cfg.Gateway.UpstreamInterface = "wan0"
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)
	events := []string{}
	dhcpManager := &fakeDHCP{startPID: 11, events: &events}
	mihomoManager := &fakeMihomo{startPID: 22, events: &events}
	nftManager := &fakeNft{loaded: true, events: &events}
	sysctlManager := &fakeSysctl{current: "0", events: &events}
	manager := newLifecycleTestManager(cfg, paths, &events, dhcpManager, mihomoManager, nftManager, sysctlManager)

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	want := []string{"forwarding-on", "mihomo-start", "mihomo-ready", "dnsmasq-start", "nft-load"}
	if got := lifecycleEvents(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle events = %#v, want %#v", got, want)
	}
}

func TestNftablesLoadFailureRollsBackOpenSurgeResources(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "lan0"
	cfg.Gateway.UpstreamInterface = "wan0"
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)
	events := []string{}
	dhcpManager := &fakeDHCP{startPID: 11, events: &events}
	mihomoManager := &fakeMihomo{startPID: 22, events: &events}
	nftManager := &fakeNft{loadErr: errors.New("nft load failed"), events: &events}
	sysctlManager := &fakeSysctl{current: "0", events: &events}
	manager := newLifecycleTestManager(cfg, paths, &events, dhcpManager, mihomoManager, nftManager, sysctlManager)

	err := manager.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nft load failed") {
		t.Fatalf("Start() error = %v", err)
	}
	if !nftManager.unloadCalled || !dhcpManager.stopCalled || !mihomoManager.stopCalled || sysctlManager.restoreValue != "0" {
		t.Fatalf("rollback calls nft-unload=%v dnsmasq-stop=%v mihomo-stop=%v forwarding-restore=%q", nftManager.unloadCalled, dhcpManager.stopCalled, mihomoManager.stopCalled, sysctlManager.restoreValue)
	}
	if _, exists, loadErr := runtime.LoadState(paths.StateFile); loadErr != nil || exists {
		t.Fatalf("runtime state after successful rollback exists=%v err=%v", exists, loadErr)
	}
}

func TestNftablesCleanupFailureRetainsRecoveryState(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "lan0"
	cfg.Gateway.UpstreamInterface = "wan0"
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)
	nftManager := &fakeNft{
		loadErr:   errors.New("nft load failed"),
		unloadErr: errors.New("cannot delete opensurge table"),
	}
	manager := newLifecycleTestManager(cfg, paths, nil, &fakeDHCP{startPID: 11}, &fakeMihomo{startPID: 22}, nftManager, &fakeSysctl{current: "0"})

	err := manager.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cleanup-required") {
		t.Fatalf("Start() error = %v, want cleanup-required recovery error", err)
	}
	state, exists, loadErr := runtime.LoadState(paths.StateFile)
	if loadErr != nil || !exists {
		t.Fatalf("recovery state exists=%v err=%v", exists, loadErr)
	}
	if !state.CleanupRequired || !strings.Contains(state.CleanupError, "cannot delete opensurge table") {
		t.Fatalf("recovery state = %#v", state)
	}
}

func TestStopUnloadsNftablesBeforeServices(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{PIDDNSMasq: 11, PIDMihomo: 22, NftablesLoaded: true, IPForwardingBefore: "0", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	dhcpManager := &fakeDHCP{events: &events}
	mihomoManager := &fakeMihomo{events: &events}
	nftManager := &fakeNft{events: &events}
	sysctlManager := &fakeSysctl{events: &events}
	manager := newLifecycleTestManager(cfg, paths, &events, dhcpManager, mihomoManager, nftManager, sysctlManager)

	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	want := []string{"nft-unload", "dnsmasq-stop", "mihomo-stop", "forwarding-restore"}
	if got := lifecycleEvents(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("stop events = %#v, want %#v", got, want)
	}
}

func TestPreflightRejectsSameGatewayAndUpstreamInterface(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "en0"
	cfg.Gateway.UpstreamInterface = " en0 "
	manager := Manager{cfg: cfg, paths: runtime.NewPaths(cfg), deps: gatewayDeps{}}

	err := manager.preflight(context.Background(), &fakeDHCP{}, &fakeMihomo{}, &fakeNft{}, &fakeSysctl{}, manager.deps)
	if err == nil {
		t.Fatalf("preflight() succeeded")
	}
	if !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("preflight() error = %q", err)
	}
}

func TestPreflightAcceptsSameInterfaceInSameLANMode(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Mode = config.GatewayModeSameLAN
	cfg.Gateway.Interface = "en0"
	cfg.Gateway.UpstreamInterface = " en0 "
	cfg.Gateway.LANIP = "192.168.1.20"
	cfg.DHCP.Enabled = false
	cfg.Transparent.Mode = config.TransparentModeTUN
	manager := Manager{
		cfg:   cfg,
		paths: runtime.NewPaths(cfg),
		deps: gatewayDeps{
			interfaceByName: func(name string) (*net.Interface, error) {
				return &net.Interface{Name: strings.TrimSpace(name)}, nil
			},
			interfaces: func() ([]net.Interface, error) {
				return []net.Interface{{Name: "en0"}}, nil
			},
			interfaceAddrs: func(iface *net.Interface) ([]net.Addr, error) {
				return []net.Addr{&net.IPNet{
					IP:   net.ParseIP(cfg.Gateway.LANIP),
					Mask: net.CIDRMask(24, 32),
				}}, nil
			},
		},
	}

	err := manager.preflight(context.Background(), &fakeDHCP{}, &fakeMihomo{}, &fakeNft{}, &fakeSysctl{}, manager.deps)
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
}

func TestPreflightRejectsSameLANWithoutTUN(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Mode = config.GatewayModeSameLAN
	cfg.Gateway.Interface = "en0"
	cfg.Gateway.UpstreamInterface = "en0"
	cfg.Gateway.LANIP = "192.168.1.20"
	cfg.DHCP.Enabled = false
	cfg.Transparent.Mode = config.TransparentModeOff
	manager := Manager{
		cfg:   cfg,
		paths: runtime.NewPaths(cfg),
		deps: gatewayDeps{
			interfaceByName: func(name string) (*net.Interface, error) {
				return &net.Interface{Name: strings.TrimSpace(name)}, nil
			},
			interfaces: func() ([]net.Interface, error) {
				return []net.Interface{{Name: "en0"}}, nil
			},
			interfaceAddrs: func(iface *net.Interface) ([]net.Addr, error) {
				return []net.Addr{&net.IPNet{IP: net.ParseIP(cfg.Gateway.LANIP), Mask: net.CIDRMask(24, 32)}}, nil
			},
		},
	}

	err := manager.preflight(context.Background(), &fakeDHCP{}, &fakeMihomo{}, &fakeNft{}, &fakeSysctl{}, manager.deps)
	if err == nil || !strings.Contains(err.Error(), `gateway.mode same_lan requires transparent.mode: "tun"`) {
		t.Fatalf("preflight() error = %v", err)
	}
}

func TestPreflightAcceptsSameInterfaceInSameWiFiDHCPMode(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Mode = config.GatewayModeSameWiFiDHCP
	cfg.Gateway.Interface = "en0"
	cfg.Gateway.UpstreamInterface = " en0 "
	cfg.Gateway.LANIP = "192.168.1.20"
	cfg.DHCP.Enabled = true
	cfg.DHCP.RangeStart = "192.168.1.120"
	cfg.DHCP.RangeEnd = "192.168.1.199"
	cfg.Transparent.Mode = config.TransparentModeTUN
	manager := Manager{
		cfg:   cfg,
		paths: runtime.NewPaths(cfg),
		deps: gatewayDeps{
			interfaceByName: func(name string) (*net.Interface, error) {
				return &net.Interface{Name: strings.TrimSpace(name)}, nil
			},
			interfaces: func() ([]net.Interface, error) {
				return []net.Interface{{Name: "en0"}}, nil
			},
			interfaceAddrs: func(iface *net.Interface) ([]net.Addr, error) {
				return []net.Addr{&net.IPNet{
					IP:   net.ParseIP(cfg.Gateway.LANIP),
					Mask: net.CIDRMask(24, 32),
				}}, nil
			},
		},
	}

	err := manager.preflight(context.Background(), &fakeDHCP{}, &fakeMihomo{}, &fakeNft{}, &fakeSysctl{}, manager.deps)
	if err != nil {
		t.Fatalf("preflight() error = %v", err)
	}
}

func TestPreflightRejectsDifferentInterfacesInSameLANMode(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Mode = config.GatewayModeSameLAN
	cfg.Gateway.Interface = "en0"
	cfg.Gateway.UpstreamInterface = "en7"
	cfg.DHCP.Enabled = false
	cfg.Transparent.Mode = config.TransparentModeTUN
	manager := Manager{cfg: cfg, paths: runtime.NewPaths(cfg), deps: gatewayDeps{}}

	err := manager.preflight(context.Background(), &fakeDHCP{}, &fakeMihomo{}, &fakeNft{}, &fakeSysctl{}, manager.deps)
	if err == nil {
		t.Fatalf("preflight() succeeded")
	}
	if !strings.Contains(err.Error(), "same_lan requires gateway and upstream interfaces to match") {
		t.Fatalf("preflight() error = %q", err)
	}
}

func TestPreflightRejectsLANIPOnAnotherInterface(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "bridge102"
	cfg.Gateway.UpstreamInterface = "en0"
	cfg.Gateway.LANIP = "192.168.50.1"
	manager := Manager{
		cfg:   cfg,
		paths: runtime.NewPaths(cfg),
		deps: gatewayDeps{
			interfaceByName: func(name string) (*net.Interface, error) {
				return &net.Interface{Name: name}, nil
			},
			interfaces: func() ([]net.Interface, error) {
				return []net.Interface{
					{Name: "bridge102"},
					{Name: "en7"},
				}, nil
			},
			interfaceAddrs: func(iface *net.Interface) ([]net.Addr, error) {
				switch iface.Name {
				case "bridge102", "en7":
					return []net.Addr{&net.IPNet{
						IP:   net.ParseIP(cfg.Gateway.LANIP),
						Mask: net.CIDRMask(24, 32),
					}}, nil
				default:
					return nil, nil
				}
			},
		},
	}

	err := manager.preflight(context.Background(), &fakeDHCP{}, &fakeMihomo{}, &fakeNft{}, &fakeSysctl{}, manager.deps)
	if err == nil {
		t.Fatalf("preflight() succeeded")
	}
	if !strings.Contains(err.Error(), "also configured on interface en7") {
		t.Fatalf("preflight() error = %q", err)
	}
}

func TestPreflightUsesInjectedLinuxTopologyCheck(t *testing.T) {
	cfg := topologyTestConfig(config.GatewayModeIsolatedLAN, "lan0", "wan0")
	manager := Manager{cfg: cfg, deps: gatewayDeps{
		interfaceInspector: topologyInspector{
			"lan0": {netip.MustParsePrefix("192.168.50.1/24")},
			"wan0": {netip.MustParsePrefix("192.168.50.2/24")},
		},
		interfaceByName: func(name string) (*net.Interface, error) {
			return &net.Interface{Name: name}, nil
		},
	}}

	err := manager.preflight(context.Background(), &fakeDHCP{}, &fakeMihomo{}, &fakeNft{}, &fakeSysctl{}, manager.deps)
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("preflight() error = %v, want upstream prefix overlap", err)
	}
}

func TestPreflightUsesInjectedPolicyRouteCheck(t *testing.T) {
	cfg := topologyTestConfig(config.GatewayModeIsolatedLAN, "lan0", "wan0")
	manager := Manager{cfg: cfg, deps: gatewayDeps{
		interfaceInspector: topologyInspector{
			"lan0": {netip.MustParsePrefix("192.168.50.1/24")},
			"wan0": {netip.MustParsePrefix("198.51.100.2/24")},
		},
		policyRouteRunner: &policyRuleRunner{output: []byte(`[{"priority":9001}]`)},
		interfaceByName: func(name string) (*net.Interface, error) {
			return &net.Interface{Name: name}, nil
		},
	}}

	err := manager.preflight(context.Background(), &fakeDHCP{}, &fakeMihomo{}, &fakeNft{}, &fakeSysctl{}, manager.deps)
	if err == nil || !strings.Contains(err.Error(), "9001") {
		t.Fatalf("preflight() error = %v, want policy priority conflict", err)
	}
}

func TestCheckReservationConflictsRejectsObservedDifferentMACInSameWiFiDHCP(t *testing.T) {
	bundle, err := device.CompilePolicyBundle(device.PolicySet{
		Profiles: []device.Profile{{ID: "home", DefaultPolicies: []string{"DIRECT"}}},
		Devices:  []device.ManagedDevice{{ID: "phone", MAC: "aa:bb:cc:dd:ee:01", IPv4: "192.168.1.101", Profile: "home"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Gateway.Mode = config.GatewayModeSameWiFiDHCP
	cfg.DevicePolicy.Bundle = &bundle
	manager := Manager{cfg: cfg, deps: gatewayDeps{
		probeReservationIP: func(ip, expectedMAC string) error {
			if ip != "192.168.1.101" || expectedMAC != "aa:bb:cc:dd:ee:01" {
				t.Fatalf("probe args = %q/%q", ip, expectedMAC)
			}
			return errors.New("reserved IPv4 already present")
		},
	}}
	if err := manager.checkReservationConflicts(manager.deps); err == nil || !strings.Contains(err.Error(), "already present") {
		t.Fatalf("checkReservationConflicts() error = %v", err)
	}
}

func TestStartValidatesMihomoBeforeEnablingForwarding(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "lan0"
	cfg.Gateway.UpstreamInterface = "wan0"
	cfg.Gateway.LANIP = "192.168.50.1"
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	mihomoManager := &fakeMihomo{validateErr: errors.New("duplicate group name")}
	sysctlManager := &fakeSysctl{current: "0"}
	manager := Manager{
		cfg:   cfg,
		paths: runtime.NewPaths(cfg),
		deps: gatewayDeps{
			geteuid:     func() int { return 0 },
			loadState:   runtime.LoadState,
			saveState:   runtime.SaveState,
			removeState: runtime.RemoveState,
			ensure:      runtime.Ensure,
			newDHCP:     func(config.Config, runtime.Paths) dhcpService { return &fakeDHCP{} },
			newMihomo:   func(config.Config, runtime.Paths) mihomoService { return mihomoManager },
			newNft:      func(config.Config, runtime.Paths) nftService { return &fakeNft{} },
			newSysctl:   func() sysctlService { return sysctlManager },
			interfaces:  func() ([]net.Interface, error) { return []net.Interface{{Name: "lan0"}, {Name: "wan0"}}, nil },
			interfaceByName: func(name string) (*net.Interface, error) {
				return &net.Interface{Name: name}, nil
			},
			interfaceAddrs: func(iface *net.Interface) ([]net.Addr, error) {
				if iface.Name != "lan0" {
					return nil, nil
				}
				return []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.50.1"), Mask: net.CIDRMask(24, 32)}}, nil
			},
		},
	}
	if err := manager.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate group") {
		t.Fatalf("Start() error = %v", err)
	}
	if sysctlManager.enableCalled {
		t.Fatal("Start() enabled forwarding before mihomo validation")
	}
	if _, exists, err := runtime.LoadState(manager.paths.StateFile); err != nil || exists {
		t.Fatalf("runtime state after validation failure = exists=%v err=%v", exists, err)
	}
}

func TestReloadValidationFailureLeavesRunningGatewayUntouched(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "lan0"
	cfg.Gateway.UpstreamInterface = "wan0"
	cfg.Gateway.LANIP = "192.168.50.1"
	cfg.Runtime.Dir = filepath.Join(t.TempDir(), "runtime")
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mihomoManager := &fakeMihomo{validateErr: errors.New("candidate rejected"), running: true}
	dhcpManager := &fakeDHCP{running: true}
	manager := Manager{
		cfg: cfg, paths: paths,
		deps: gatewayDeps{
			geteuid: func() int { return 0 }, loadState: runtime.LoadState, saveState: runtime.SaveState,
			removeState: runtime.RemoveState, ensure: runtime.Ensure,
			newDHCP:         func(config.Config, runtime.Paths) dhcpService { return dhcpManager },
			newMihomo:       func(config.Config, runtime.Paths) mihomoService { return mihomoManager },
			newNft:          func(config.Config, runtime.Paths) nftService { return &fakeNft{} },
			newSysctl:       func() sysctlService { return &fakeSysctl{} },
			interfaces:      func() ([]net.Interface, error) { return []net.Interface{{Name: "lan0"}, {Name: "wan0"}}, nil },
			interfaceByName: func(name string) (*net.Interface, error) { return &net.Interface{Name: name}, nil },
			interfaceAddrs: func(iface *net.Interface) ([]net.Addr, error) {
				if iface.Name == "lan0" {
					return []net.Addr{&net.IPNet{IP: net.ParseIP(cfg.Gateway.LANIP), Mask: net.CIDRMask(24, 32)}}, nil
				}
				return nil, nil
			},
		},
	}
	err := manager.Reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), "candidate rejected") {
		t.Fatalf("Reload() error=%v", err)
	}
	if dhcpManager.stopCalled || mihomoManager.stopCalled {
		t.Fatal("reload stopped live services after candidate validation failed")
	}
	if _, exists, err := runtime.LoadState(paths.StateFile); err != nil || !exists {
		t.Fatalf("runtime state exists=%v err=%v", exists, err)
	}
}

func TestReloadStopsBeforeRestartAndWritesFreshState(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "lan0"
	cfg.Gateway.UpstreamInterface = "wan0"
	cfg.Gateway.LANIP = "192.168.50.1"
	cfg.Runtime.Dir = filepath.Join(t.TempDir(), "runtime")
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	profile := filepath.Join(filepath.Dir(cfg.Runtime.Dir), "imported.yaml")
	profileData := []byte("proxies: []\nproxy-groups: []\nrules: []\n")
	if err := os.WriteFile(profile, profileData, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Mihomo.ProfileMode = config.MihomoProfileModeImported
	cfg.Mihomo.Profile = profile
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{PIDDNSMasq: 11, PIDMihomo: 12, IPForwardingBefore: "0", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	dhcpManager := &fakeDHCP{running: true, startPID: 21, events: &events}
	mihomoManager := &fakeMihomo{running: true, startPID: 22, events: &events}
	manager := Manager{cfg: cfg, paths: paths, deps: gatewayDeps{
		geteuid: func() int { return 0 }, loadState: runtime.LoadState, saveState: runtime.SaveState,
		removeState: runtime.RemoveState, ensure: runtime.Ensure,
		newDHCP:   func(config.Config, runtime.Paths) dhcpService { return dhcpManager },
		newMihomo: func(config.Config, runtime.Paths) mihomoService { return mihomoManager },
		newNft:    func(config.Config, runtime.Paths) nftService { return &fakeNft{loaded: true} },
		newSysctl: func() sysctlService { return &fakeSysctl{current: "0"} },
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: "lan0"}, {Name: "wan0"}}, nil
		},
		interfaceByName: func(name string) (*net.Interface, error) { return &net.Interface{Name: name}, nil },
		interfaceAddrs: func(iface *net.Interface) ([]net.Addr, error) {
			if iface.Name == "lan0" {
				return []net.Addr{&net.IPNet{IP: net.ParseIP(cfg.Gateway.LANIP), Mask: net.CIDRMask(24, 32)}}, nil
			}
			return nil, nil
		},
		now: time.Now,
	}}
	if err := manager.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopIndex, startIndex := -1, -1
	for index, event := range events {
		if event == "dnsmasq-stop" && stopIndex == -1 {
			stopIndex = index
		}
		if event == "mihomo-start" && startIndex == -1 {
			startIndex = index
		}
	}
	if stopIndex == -1 || startIndex == -1 || stopIndex >= startIndex {
		t.Fatalf("reload events=%v", events)
	}
	state, exists, err := runtime.LoadState(paths.StateFile)
	profileDigest, digestErr := config.MihomoProfileDigest(cfg)
	if digestErr != nil {
		t.Fatal(digestErr)
	}
	if err != nil || !exists || state.PIDDNSMasq != 21 || state.PIDMihomo != 22 || state.ProfileDigest != profileDigest {
		t.Fatalf("fresh runtime state=%#v exists=%v err=%v", state, exists, err)
	}
}

func TestRestartMihomoValidatesBeforeStoppingLiveProcess(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	original := runtime.State{PIDDNSMasq: 11, PIDMihomo: 12, NftablesLoaded: true, StartedAt: time.Now()}
	if err := runtime.SaveState(paths.StateFile, original); err != nil {
		t.Fatal(err)
	}
	mihomoManager := &fakeMihomo{validateErr: errors.New("invalid prepared config")}
	manager := Manager{cfg: cfg, paths: paths, deps: gatewayDeps{
		geteuid: func() int { return 0 }, loadState: runtime.LoadState, saveState: runtime.SaveState,
		newMihomo: func(config.Config, runtime.Paths) mihomoService { return mihomoManager }, now: time.Now,
	}}

	err := manager.RestartMihomo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid prepared config") {
		t.Fatalf("RestartMihomo() error=%v", err)
	}
	if mihomoManager.stopCalled || mihomoManager.startCalled {
		t.Fatal("restart touched the live process before prepared config validation passed")
	}
	state, exists, loadErr := runtime.LoadState(paths.StateFile)
	if loadErr != nil || !exists || state.PIDMihomo != original.PIDMihomo || state.PIDDNSMasq != original.PIDDNSMasq {
		t.Fatalf("runtime state=%#v exists=%v err=%v", state, exists, loadErr)
	}
}

func TestRestartMihomoRejectsImportedProfileDrift(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	cfg.Mihomo.ProfileMode = config.MihomoProfileModeImported
	cfg.Mihomo.Profile = filepath.Join(cfg.Runtime.Dir, "imported.yaml")
	if err := os.WriteFile(cfg.Mihomo.Profile, []byte("proxies: []\nproxy-groups: []\nrules: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{PIDDNSMasq: 11, PIDMihomo: 12, ProfileDigest: "older-applied-digest", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mihomoManager := &fakeMihomo{}
	manager := Manager{cfg: cfg, paths: paths, deps: gatewayDeps{
		geteuid: func() int { return 0 }, loadState: runtime.LoadState, saveState: runtime.SaveState,
		newMihomo: func(config.Config, runtime.Paths) mihomoService { return mihomoManager }, now: time.Now,
	}}

	err := manager.RestartMihomo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "differs from the applied runtime") {
		t.Fatalf("RestartMihomo() error=%v", err)
	}
	if mihomoManager.stopCalled || mihomoManager.startCalled {
		t.Fatal("restart touched mihomo while desired imported profile was not applied")
	}
}

func TestRestartMihomoReplacesOnlyProxyEngineAndArchivesLog(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	original := runtime.State{PIDDNSMasq: 11, PIDMihomo: 12, NftablesLoaded: true, IPForwardingBefore: "0", StartedAt: time.Now()}
	if err := runtime.SaveState(paths.StateFile, original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.MihomoLog, []byte("link-down evidence\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	restartedAt := time.Date(2026, 7, 16, 12, 53, 33, 123456789, time.UTC)
	mihomoManager := &fakeMihomo{startPID: 22}
	manager := Manager{cfg: cfg, paths: paths, deps: gatewayDeps{
		geteuid: func() int { return 0 }, loadState: runtime.LoadState, saveState: runtime.SaveState,
		newMihomo: func(config.Config, runtime.Paths) mihomoService { return mihomoManager }, now: func() time.Time { return restartedAt },
	}}

	if err := manager.RestartMihomo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !mihomoManager.stopCalled || mihomoManager.stoppedPID != original.PIDMihomo || !mihomoManager.startCalled {
		t.Fatalf("mihomo calls stop=%v stoppedPID=%d start=%v", mihomoManager.stopCalled, mihomoManager.stoppedPID, mihomoManager.startCalled)
	}
	state, exists, err := runtime.LoadState(paths.StateFile)
	if err != nil || !exists || state.PIDMihomo != 22 || state.PIDDNSMasq != original.PIDDNSMasq || !state.NftablesLoaded || state.IPForwardingBefore != original.IPForwardingBefore {
		t.Fatalf("runtime state=%#v exists=%v err=%v", state, exists, err)
	}
	archive := filepath.Join(paths.LogDir, "mihomo-before-restart-20260716T125333.123456789Z.log")
	data, err := os.ReadFile(archive)
	if err != nil || string(data) != "link-down evidence\n" {
		t.Fatalf("archived log=%q err=%v", data, err)
	}
}

func TestRestartMihomoStartFailureLeavesRetryableRuntimeState(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{PIDDNSMasq: 11, PIDMihomo: 12, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.MihomoLog, []byte("incident\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	mihomoManager := &fakeMihomo{startErr: errors.New("replacement failed")}
	manager := Manager{cfg: cfg, paths: paths, deps: gatewayDeps{
		geteuid: func() int { return 0 }, loadState: runtime.LoadState, saveState: runtime.SaveState,
		newMihomo: func(config.Config, runtime.Paths) mihomoService { return mihomoManager }, now: time.Now,
	}}

	err := manager.RestartMihomo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "replacement failed") {
		t.Fatalf("RestartMihomo() error=%v", err)
	}
	state, exists, loadErr := runtime.LoadState(paths.StateFile)
	if loadErr != nil || !exists || state.PIDMihomo != 0 || state.PIDDNSMasq != 11 {
		t.Fatalf("retryable runtime state=%#v exists=%v err=%v", state, exists, loadErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(paths.LogDir, "mihomo-before-restart-*.log"))
	if globErr != nil || len(matches) != 1 {
		t.Fatalf("archived logs=%v err=%v", matches, globErr)
	}
}

func TestRestartMihomoStopFailureRestoresLivePID(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{PIDDNSMasq: 11, PIDMihomo: 12, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mihomoManager := &fakeMihomo{stopErr: errors.New("old process is busy"), running: true}
	manager := Manager{cfg: cfg, paths: paths, deps: gatewayDeps{
		geteuid: func() int { return 0 }, loadState: runtime.LoadState, saveState: runtime.SaveState,
		newMihomo: func(config.Config, runtime.Paths) mihomoService { return mihomoManager }, now: time.Now,
	}}

	err := manager.RestartMihomo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "old process is busy") {
		t.Fatalf("RestartMihomo() error=%v", err)
	}
	state, exists, loadErr := runtime.LoadState(paths.StateFile)
	if loadErr != nil || !exists || state.PIDMihomo != 12 || state.PIDDNSMasq != 11 {
		t.Fatalf("restored runtime state=%#v exists=%v err=%v", state, exists, loadErr)
	}
	if mihomoManager.startCalled {
		t.Fatal("replacement started while the old process was still alive")
	}
}

func TestStopFailureRetainsRuntimeStateForRetryAndRecovery(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	paths := runtime.NewPaths(cfg)
	if err := runtime.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SaveState(paths.StateFile, runtime.State{PIDDNSMasq: 11, PIDMihomo: 12, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	manager := Manager{cfg: cfg, paths: paths, deps: gatewayDeps{
		geteuid: func() int { return 0 }, loadState: runtime.LoadState, saveState: runtime.SaveState, removeState: runtime.RemoveState,
		newDHCP: func(config.Config, runtime.Paths) dhcpService {
			return &fakeDHCP{stopErr: errors.New("dnsmasq did not stop")}
		},
		newMihomo: func(config.Config, runtime.Paths) mihomoService { return &fakeMihomo{} },
		newNft:    func(config.Config, runtime.Paths) nftService { return &fakeNft{} },
		newSysctl: func() sysctlService { return &fakeSysctl{} },
	}}
	if err := manager.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "dnsmasq did not stop") {
		t.Fatalf("Stop() error=%v", err)
	}
	state, exists, err := runtime.LoadState(paths.StateFile)
	if err != nil || !exists {
		t.Fatalf("runtime state exists=%v err=%v", exists, err)
	}
	if !state.CleanupRequired {
		t.Fatalf("runtime state = %#v, want cleanup-required recovery state", state)
	}
}

type fakeDHCP struct {
	checkErr    error
	writeErr    error
	startPID    int
	startErr    error
	stopErr     error
	startCalled bool
	stopCalled  bool
	running     bool
	events      *[]string
}

func (f *fakeDHCP) Check() error {
	return f.checkErr
}

func (f *fakeDHCP) WriteConfig() error {
	if f.events != nil {
		*f.events = append(*f.events, "dnsmasq-write")
	}
	return f.writeErr
}

func (f *fakeDHCP) Start() (int, error) {
	f.startCalled = true
	if f.events != nil {
		*f.events = append(*f.events, "dnsmasq-start")
	}
	return f.startPID, f.startErr
}

func (f *fakeDHCP) Stop(int) error {
	f.stopCalled = true
	if f.events != nil {
		*f.events = append(*f.events, "dnsmasq-stop")
	}
	return f.stopErr
}

func (f *fakeDHCP) Running(int) bool { return f.running }

type fakeMihomo struct {
	checkErr    error
	writeErr    error
	validateErr error
	startPID    int
	startErr    error
	stopErr     error
	startCalled bool
	stopCalled  bool
	stoppedPID  int
	running     bool
	events      *[]string
}

func (f *fakeMihomo) Check() error {
	return f.checkErr
}

func (f *fakeMihomo) WriteConfig() error {
	if f.events != nil {
		*f.events = append(*f.events, "mihomo-write")
	}
	return f.writeErr
}

func (f *fakeMihomo) ValidateWrittenConfig() error {
	if f.events != nil {
		*f.events = append(*f.events, "mihomo-validate")
	}
	return f.validateErr
}

func (f *fakeMihomo) Start() (int, error) {
	f.startCalled = true
	if f.events != nil {
		*f.events = append(*f.events, "mihomo-start")
		*f.events = append(*f.events, "mihomo-ready")
	}
	return f.startPID, f.startErr
}

func (f *fakeMihomo) Stop(pid int) error {
	f.stopCalled = true
	f.stoppedPID = pid
	if f.events != nil {
		*f.events = append(*f.events, "mihomo-stop")
	}
	return f.stopErr
}

func (f *fakeMihomo) Running(int) bool { return f.running }

type fakeNft struct {
	checkErr     error
	writeErr     error
	loadErr      error
	loaded       bool
	loadedErr    error
	unloadErr    error
	loadCalled   bool
	unloadCalled bool
	events       *[]string
}

func (f *fakeNft) Check() error {
	return f.checkErr
}

func (f *fakeNft) WriteRuleset() error {
	if f.events != nil {
		*f.events = append(*f.events, "nft-write")
	}
	return f.writeErr
}

func (f *fakeNft) Load() error {
	f.loadCalled = true
	if f.events != nil {
		*f.events = append(*f.events, "nft-load")
	}
	return f.loadErr
}

func (f *fakeNft) Loaded() (bool, error) {
	return f.loaded, f.loadedErr
}

func (f *fakeNft) Unload() error {
	f.unloadCalled = true
	if f.events != nil {
		*f.events = append(*f.events, "nft-unload")
	}
	return f.unloadErr
}

type fakeSysctl struct {
	checkErr     error
	current      string
	currentErr   error
	enableErr    error
	restoreErr   error
	enableCalled bool
	restoreValue string
	events       *[]string
}

func (f *fakeSysctl) Check() error {
	return f.checkErr
}

func (f *fakeSysctl) Current() (string, error) {
	return f.current, f.currentErr
}

func (f *fakeSysctl) Enable() error {
	f.enableCalled = true
	if f.events != nil {
		*f.events = append(*f.events, "forwarding-on")
	}
	return f.enableErr
}

func (f *fakeSysctl) Restore(value string) error {
	f.restoreValue = value
	if f.events != nil {
		*f.events = append(*f.events, "forwarding-restore")
	}
	return f.restoreErr
}

func newLifecycleTestManager(cfg config.Config, paths runtime.Paths, _ *[]string, dhcpManager *fakeDHCP, mihomoManager *fakeMihomo, nftManager *fakeNft, sysctlManager *fakeSysctl) Manager {
	return Manager{cfg: cfg, paths: paths, deps: gatewayDeps{
		geteuid:     func() int { return 0 },
		loadState:   runtime.LoadState,
		saveState:   runtime.SaveState,
		removeState: runtime.RemoveState,
		ensure:      runtime.Ensure,
		newDHCP:     func(config.Config, runtime.Paths) dhcpService { return dhcpManager },
		newMihomo:   func(config.Config, runtime.Paths) mihomoService { return mihomoManager },
		newNft:      func(config.Config, runtime.Paths) nftService { return nftManager },
		newSysctl:   func() sysctlService { return sysctlManager },
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: cfg.Gateway.Interface}, {Name: cfg.Gateway.UpstreamInterface}}, nil
		},
		interfaceByName: func(name string) (*net.Interface, error) {
			return &net.Interface{Name: name}, nil
		},
		interfaceAddrs: func(iface *net.Interface) ([]net.Addr, error) {
			if iface.Name != cfg.Gateway.Interface {
				return nil, nil
			}
			return []net.Addr{&net.IPNet{IP: net.ParseIP(cfg.Gateway.LANIP), Mask: net.CIDRMask(24, 32)}}, nil
		},
		interfaceInspector: topologyInspector{
			cfg.Gateway.Interface:         {netip.MustParsePrefix(cfg.Gateway.LANIP + "/24")},
			cfg.Gateway.UpstreamInterface: {netip.MustParsePrefix("198.51.100.2/24")},
		},
		policyRouteRunner: &policyRuleRunner{output: []byte(`[{"priority":0},{"priority":32766}]`)},
		now:               time.Now,
	}}
}

func lifecycleEvents(events []string) []string {
	allowed := map[string]bool{
		"forwarding-on":      true,
		"mihomo-start":       true,
		"mihomo-ready":       true,
		"dnsmasq-start":      true,
		"nft-load":           true,
		"nft-unload":         true,
		"dnsmasq-stop":       true,
		"mihomo-stop":        true,
		"forwarding-restore": true,
	}
	filtered := make([]string, 0, len(events))
	for _, event := range events {
		if allowed[event] {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func indexOfEvent(events []string, target string) int {
	for index, event := range events {
		if event == target {
			return index
		}
	}
	return -1
}
