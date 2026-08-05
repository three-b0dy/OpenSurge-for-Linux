package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/device"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/dhcp"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/linuxnet"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/mihomo"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/nftables"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/process"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/runtime"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/sysctl"
)

type Manager struct {
	cfg   config.Config
	paths runtime.Paths
	deps  gatewayDeps
}

func New(cfg config.Config) Manager {
	return Manager{cfg: cfg, paths: runtime.NewPaths(cfg), deps: defaultGatewayDeps()}
}

type dhcpService interface {
	Check() error
	WriteConfig() error
	Start() (int, error)
	Stop(int) error
	Running(int) bool
}

type mihomoService interface {
	Check() error
	WriteConfig() error
	ValidateWrittenConfig() error
	Start() (int, error)
	Stop(int) error
	Running(int) bool
}

type nftService interface {
	Check() error
	WriteRuleset() error
	Load() error
	Loaded() (bool, error)
	Unload() error
}

type sysctlService interface {
	Check() error
	Current() (string, error)
	Enable() error
	Restore(string) error
}

type gatewayDeps struct {
	geteuid            func() int
	loadState          func(string) (runtime.State, bool, error)
	saveState          func(string, runtime.State) error
	removeState        func(string) error
	ensure             func(runtime.Paths) error
	newDHCP            func(config.Config, runtime.Paths) dhcpService
	newMihomo          func(config.Config, runtime.Paths) mihomoService
	newNft             func(config.Config, runtime.Paths) nftService
	newSysctl          func() sysctlService
	interfaces         func() ([]net.Interface, error)
	interfaceByName    func(string) (*net.Interface, error)
	interfaceAddrs     func(*net.Interface) ([]net.Addr, error)
	interfaceInspector linuxnet.InterfaceInspector
	policyRouteRunner  process.Runner
	probeReservationIP func(ip string, expectedMAC string) error
	now                func() time.Time
}

func defaultGatewayDeps() gatewayDeps {
	commandRunner := process.NewRunner()
	return gatewayDeps{
		geteuid:     os.Geteuid,
		loadState:   runtime.LoadState,
		saveState:   runtime.SaveState,
		removeState: runtime.RemoveState,
		ensure:      runtime.Ensure,
		newDHCP: func(cfg config.Config, paths runtime.Paths) dhcpService {
			return dhcp.New(cfg, paths)
		},
		newMihomo: func(cfg config.Config, paths runtime.Paths) mihomoService {
			return mihomo.New(cfg, paths)
		},
		newNft: func(cfg config.Config, paths runtime.Paths) nftService {
			return nftables.New(cfg, paths, nil)
		},
		newSysctl: func() sysctlService {
			return sysctl.New()
		},
		interfaces:      net.Interfaces,
		interfaceByName: net.InterfaceByName,
		interfaceAddrs: func(iface *net.Interface) ([]net.Addr, error) {
			return iface.Addrs()
		},
		interfaceInspector: linuxnet.NewIPRoute(commandRunner.Output),
		policyRouteRunner:  commandRunner,
		probeReservationIP: probeReservationIPConflict,
		now:                time.Now,
	}
}

func (m Manager) gatewayDeps() gatewayDeps {
	if m.deps.geteuid == nil {
		return defaultGatewayDeps()
	}
	return m.deps
}

func (m Manager) Start(ctx context.Context) error {
	deps := m.gatewayDeps()
	if deps.geteuid() != 0 {
		return fmt.Errorf("start requires sudo/root privileges")
	}
	if _, exists, err := deps.loadState(m.paths.StateFile); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("gateway state already exists; run stop first")
	}
	if err := config.PrepareDevicePolicy(&m.cfg); err != nil {
		return err
	}
	if err := config.Validate(m.cfg); err != nil {
		return err
	}
	if err := deps.ensure(m.paths); err != nil {
		return err
	}

	dhcpManager := deps.newDHCP(m.cfg, m.paths)
	mihomoManager := deps.newMihomo(m.cfg, m.paths)
	nftManager := deps.newNft(m.cfg, m.paths)
	sysctlManager := deps.newSysctl()
	if err := m.preflight(ctx, dhcpManager, mihomoManager, nftManager, sysctlManager, deps); err != nil {
		return err
	}
	if err := m.checkReservationConflicts(deps); err != nil {
		return err
	}
	if err := mihomoManager.WriteConfig(); err != nil {
		return err
	}
	if err := dhcpManager.WriteConfig(); err != nil {
		return err
	}
	if err := nftManager.WriteRuleset(); err != nil {
		return err
	}
	if err := mihomoManager.ValidateWrittenConfig(); err != nil {
		return err
	}
	ipForwardingBefore, err := sysctlManager.Current()
	if err != nil {
		return err
	}
	profileDigest, err := config.MihomoProfileDigest(m.cfg)
	if err != nil {
		return fmt.Errorf("digest imported mihomo profile: %w", err)
	}
	if bundle := m.cfg.DevicePolicy.Bundle; bundle != nil {
		if err := dhcp.ReconcilePolicyLeases(m.paths.LeaseFile, bundle.Compiled.Reservations); err != nil {
			return err
		}
		if err := device.WritePolicyBundleSnapshot(m.paths.DevicePolicyApplied, *bundle); err != nil {
			return err
		}
	}

	state := runtime.State{
		StartedAt:          deps.now(),
		IPForwardingBefore: ipForwardingBefore,
		ProfileDigest:      profileDigest,
	}
	if bundle := m.cfg.DevicePolicy.Bundle; bundle != nil {
		state.DevicePolicyDigest = bundle.Digest
	}
	if err := deps.saveState(m.paths.StateFile, state); err != nil {
		_ = device.RemovePolicyBundleSnapshot(m.paths.DevicePolicyApplied)
		return err
	}

	if err := sysctlManager.Enable(); err != nil {
		return m.rollback(ctx, err, state, dhcpManager, mihomoManager, nftManager, sysctlManager, false)
	}
	if err := deps.saveState(m.paths.StateFile, state); err != nil {
		return m.rollback(ctx, err, state, dhcpManager, mihomoManager, nftManager, sysctlManager, false)
	}

	mihomoPID, err := mihomoManager.Start()
	if err != nil {
		return m.rollback(ctx, err, state, dhcpManager, mihomoManager, nftManager, sysctlManager, false)
	}
	state.PIDMihomo = mihomoPID
	if err := deps.saveState(m.paths.StateFile, state); err != nil {
		return m.rollback(ctx, err, state, dhcpManager, mihomoManager, nftManager, sysctlManager, false)
	}

	pid, err := dhcpManager.Start()
	if err != nil {
		return m.rollback(ctx, err, state, dhcpManager, mihomoManager, nftManager, sysctlManager, false)
	}
	state.PIDDNSMasq = pid
	if err := deps.saveState(m.paths.StateFile, state); err != nil {
		return m.rollback(ctx, err, state, dhcpManager, mihomoManager, nftManager, sysctlManager, false)
	}

	nftLoadAttempted := true
	if err := nftManager.Load(); err != nil {
		return m.rollback(ctx, err, state, dhcpManager, mihomoManager, nftManager, sysctlManager, nftLoadAttempted)
	}
	state.NftablesLoaded = true
	if err := deps.saveState(m.paths.StateFile, state); err != nil {
		return m.rollback(ctx, err, state, dhcpManager, mihomoManager, nftManager, sysctlManager, nftLoadAttempted)
	}
	loaded, err := nftManager.Loaded()
	if err != nil {
		return m.rollback(ctx, err, state, dhcpManager, mihomoManager, nftManager, sysctlManager, nftLoadAttempted)
	}
	if !loaded {
		return m.rollback(ctx, fmt.Errorf("nftables table %s did not become visible after load", m.cfg.Nftables.Table), state, dhcpManager, mihomoManager, nftManager, sysctlManager, nftLoadAttempted)
	}

	fmt.Printf("Gateway runtime prepared in %s\n", m.paths.Dir)
	if mihomoPID > 0 {
		fmt.Printf("mihomo started with pid %d\n", mihomoPID)
	}
	if pid > 0 {
		fmt.Printf("dnsmasq started with pid %d\n", pid)
	}
	fmt.Printf("nftables table %s loaded\n", m.cfg.Nftables.Table)
	return nil
}

// Reload validates a complete desired candidate before touching the running
// gateway, then performs the same audited stop/start lifecycle as the normal
// commands. The Manager owns an immutable Config value, so the configuration
// that passed validation is also the configuration applied after stop.
func (m Manager) Reload(ctx context.Context) error {
	deps := m.gatewayDeps()
	if deps.geteuid() != 0 {
		return fmt.Errorf("reload requires sudo/root privileges")
	}
	state, exists, err := deps.loadState(m.paths.StateFile)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("gateway is not running; run start instead")
	}
	if !deps.newDHCP(m.cfg, m.paths).Running(state.PIDDNSMasq) || !deps.newMihomo(m.cfg, m.paths).Running(state.PIDMihomo) {
		return fmt.Errorf("gateway is degraded; reload requires both DHCP/DNS and mihomo to be running")
	}
	if err := m.validateReloadCandidate(ctx); err != nil {
		return fmt.Errorf("reload candidate validation failed: %w", err)
	}
	if err := m.Stop(ctx); err != nil {
		return fmt.Errorf("reload stop failed: %w", err)
	}
	if err := m.Start(ctx); err != nil {
		return fmt.Errorf("reload start failed after gateway stop: %w", err)
	}
	return nil
}

// RestartMihomo rebuilds only the proxy engine process. It deliberately keeps
// dnsmasq, nftables, IPv4 forwarding, and the host network configuration untouched,
// so an upstream link recovery does not turn into a full gateway takeover
// transition. The existing rendered configuration is validated before the
// live process is stopped, and the previous log is archived for diagnosis.
func (m Manager) RestartMihomo(ctx context.Context) error {
	deps := m.gatewayDeps()
	if deps.geteuid() != 0 {
		return fmt.Errorf("restart-mihomo requires sudo/root privileges")
	}
	state, exists, err := deps.loadState(m.paths.StateFile)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("gateway is not running; run start instead")
	}
	desiredProfileDigest, err := config.MihomoProfileDigest(m.cfg)
	if err != nil {
		return fmt.Errorf("digest current imported mihomo profile: %w", err)
	}
	if desiredProfileDigest != state.ProfileDigest {
		return fmt.Errorf("desired imported mihomo profile differs from the applied runtime; run reload instead")
	}

	mihomoManager := deps.newMihomo(m.cfg, m.paths)
	if err := mihomoManager.ValidateWrittenConfig(); err != nil {
		return fmt.Errorf("prepared mihomo config validation failed: %w", err)
	}

	previousPID := state.PIDMihomo
	state.PIDMihomo = 0
	if err := deps.saveState(m.paths.StateFile, state); err != nil {
		return fmt.Errorf("mark mihomo restart in runtime state: %w", err)
	}
	if err := mihomoManager.Stop(previousPID); err != nil {
		if mihomoManager.Running(previousPID) {
			state.PIDMihomo = previousPID
			return errors.Join(fmt.Errorf("stop mihomo pid %d: %w", previousPID, err), deps.saveState(m.paths.StateFile, state))
		}
		return errors.Join(fmt.Errorf("stop mihomo pid %d: %w", previousPID, err), deps.saveState(m.paths.StateFile, state))
	}

	archivedLog, err := archiveMihomoLog(m.paths.MihomoLog, deps.now())
	if err != nil {
		return fmt.Errorf("archive mihomo log before restart: %w", err)
	}
	newPID, err := mihomoManager.Start()
	if err != nil {
		return fmt.Errorf("start replacement mihomo process: %w", err)
	}
	state.PIDMihomo = newPID
	if err := deps.saveState(m.paths.StateFile, state); err != nil {
		stopErr := mihomoManager.Stop(newPID)
		return errors.Join(fmt.Errorf("save replacement mihomo pid: %w", err), stopErr)
	}

	fmt.Printf("mihomo restarted with pid %d\n", newPID)
	if archivedLog != "" {
		fmt.Printf("previous mihomo log archived at %s\n", archivedLog)
	}
	return nil
}

func archiveMihomoLog(path string, now time.Time) (string, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	archive := filepath.Join(filepath.Dir(path), fmt.Sprintf("%s-before-restart-%s%s", base, now.UTC().Format("20060102T150405.000000000Z"), ext))
	if err := os.Rename(path, archive); err != nil {
		return "", err
	}
	return archive, nil
}

// validateReloadCandidate renders every generated artifact into an isolated
// temporary runtime and runs the real mihomo validator. It deliberately does
// not write applied policy state or alter host networking.
func (m Manager) validateReloadCandidate(ctx context.Context) error {
	parent := filepath.Dir(m.paths.Dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(parent, ".opensurge-reload-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)

	candidateConfig := m.cfg
	candidateConfig.Runtime.Dir = temp
	candidateConfig.Mihomo.Config = filepath.Join(temp, "mihomo.yaml")
	if err := config.PrepareDevicePolicy(&candidateConfig); err != nil {
		return err
	}
	if err := config.Validate(candidateConfig); err != nil {
		return err
	}
	candidate := Manager{cfg: candidateConfig, paths: runtime.NewPaths(candidateConfig), deps: m.gatewayDeps()}
	deps := candidate.gatewayDeps()
	if err := deps.ensure(candidate.paths); err != nil {
		return err
	}
	dhcpManager := deps.newDHCP(candidate.cfg, candidate.paths)
	mihomoManager := deps.newMihomo(candidate.cfg, candidate.paths)
	nftManager := deps.newNft(candidate.cfg, candidate.paths)
	sysctlManager := deps.newSysctl()
	if err := candidate.preflight(ctx, dhcpManager, mihomoManager, nftManager, sysctlManager, deps); err != nil {
		return err
	}
	if err := candidate.checkReservationConflicts(deps); err != nil {
		return err
	}
	if err := mihomoManager.WriteConfig(); err != nil {
		return err
	}
	if err := dhcpManager.WriteConfig(); err != nil {
		return err
	}
	if err := nftManager.WriteRuleset(); err != nil {
		return err
	}
	return mihomoManager.ValidateWrittenConfig()
}

func (m Manager) Stop(ctx context.Context) error {
	deps := m.gatewayDeps()
	if deps.geteuid() != 0 {
		return fmt.Errorf("stop requires sudo/root privileges")
	}
	state, exists, err := deps.loadState(m.paths.StateFile)
	if err != nil {
		return err
	}
	var cleanupErr error
	nftManager := deps.newNft(m.cfg, m.paths)
	sysctlManager := deps.newSysctl()
	if exists {
		dhcpManager := deps.newDHCP(m.cfg, m.paths)
		if state.NftablesLoaded || state.CleanupRequired {
			cleanupErr = errors.Join(cleanupErr, nftManager.Unload())
		}
		cleanupErr = errors.Join(cleanupErr, dhcpManager.Stop(state.PIDDNSMasq))
		mihomoManager := deps.newMihomo(m.cfg, m.paths)
		cleanupErr = errors.Join(cleanupErr, mihomoManager.Stop(state.PIDMihomo))
		cleanupErr = errors.Join(cleanupErr, sysctlManager.Restore(state.IPForwardingBefore))
	}
	if cleanupErr != nil {
		return m.retainCleanupState(nil, state, cleanupErr, deps)
	}
	cleanupErr = errors.Join(cleanupErr, deps.removeState(m.paths.StateFile))
	cleanupErr = errors.Join(cleanupErr, device.RemovePolicyBundleSnapshot(m.paths.DevicePolicyApplied))
	if cleanupErr != nil {
		return m.retainCleanupState(nil, state, cleanupErr, deps)
	}

	fmt.Println("Gateway stopped and runtime state cleared.")
	return nil
}

func (m Manager) preflight(ctx context.Context, dhcpManager dhcpService, mihomoManager mihomoService, nftManager nftService, sysctlManager sysctlService, deps gatewayDeps) error {
	if err := dhcpManager.Check(); err != nil {
		return err
	}
	if err := mihomoManager.Check(); err != nil {
		return err
	}
	if err := nftManager.Check(); err != nil {
		return err
	}
	if err := sysctlManager.Check(); err != nil {
		return err
	}
	if deps.interfaceInspector != nil {
		if err := ValidateTopology(ctx, m.cfg, deps.interfaceInspector); err != nil {
			return err
		}
	}
	if deps.policyRouteRunner != nil {
		if err := DetectPolicyRouteConflict(ctx, deps.policyRouteRunner, m.cfg.Transparent.TUNDevice); err != nil {
			return err
		}
	}
	sameInterface := strings.TrimSpace(m.cfg.Gateway.Interface) == strings.TrimSpace(m.cfg.Gateway.UpstreamInterface)
	if m.cfg.Gateway.SameLAN() {
		if !sameInterface {
			return fmt.Errorf("gateway.mode %s requires gateway and upstream interfaces to match", m.cfg.Gateway.Mode)
		}
	} else if sameInterface {
		return fmt.Errorf("gateway and upstream interfaces must differ")
	}
	if _, err := deps.interfaceByName(m.cfg.Gateway.Interface); err != nil {
		return fmt.Errorf("interface %s: %w", m.cfg.Gateway.Interface, err)
	}
	if _, err := deps.interfaceByName(m.cfg.Gateway.UpstreamInterface); err != nil {
		return fmt.Errorf("upstream interface %s: %w", m.cfg.Gateway.UpstreamInterface, err)
	}
	return m.checkLANIP(deps)
}

func (m Manager) checkLANIP(deps gatewayDeps) error {
	target := net.ParseIP(m.cfg.Gateway.LANIP).To4()
	if target == nil {
		return fmt.Errorf("gateway LAN IP %s is not IPv4", m.cfg.Gateway.LANIP)
	}
	iface, err := deps.interfaceByName(m.cfg.Gateway.Interface)
	if err != nil {
		return err
	}
	addrs, err := deps.interfaceAddrs(iface)
	if err != nil {
		return err
	}
	found := false
	for _, addr := range addrs {
		if addrHasIPv4(addr, target) {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("LAN IP %s is not configured on interface %s", m.cfg.Gateway.LANIP, m.cfg.Gateway.Interface)
	}
	return m.checkLANIPConflicts(target, iface.Name, deps)
}

func (m Manager) checkLANIPConflicts(target net.IP, gatewayInterface string, deps gatewayDeps) error {
	interfaces := deps.interfaces
	if interfaces == nil {
		interfaces = net.Interfaces
	}
	ifaces, err := interfaces()
	if err != nil {
		return err
	}
	for _, candidate := range ifaces {
		if candidate.Name == gatewayInterface {
			continue
		}
		addrs, err := deps.interfaceAddrs(&candidate)
		if err != nil {
			return fmt.Errorf("interface %s addresses: %w", candidate.Name, err)
		}
		for _, addr := range addrs {
			if addrHasIPv4(addr, target) {
				return fmt.Errorf("LAN IP %s is also configured on interface %s; remove the duplicate address before starting the gateway", m.cfg.Gateway.LANIP, candidate.Name)
			}
		}
	}
	return nil
}

func addrHasIPv4(addr net.Addr, target net.IP) bool {
	switch value := addr.(type) {
	case *net.IPNet:
		return value.IP.To4() != nil && value.IP.Equal(target)
	case *net.IPAddr:
		return value.IP.To4() != nil && value.IP.Equal(target)
	default:
		return false
	}
}

func (m Manager) rollback(_ context.Context, cause error, state runtime.State, dhcpManager dhcpService, mihomoManager mihomoService, nftManager nftService, sysctlManager sysctlService, nftCleanup bool) error {
	deps := m.gatewayDeps()
	var cleanupErr error
	if nftCleanup {
		cleanupErr = errors.Join(cleanupErr, nftManager.Unload())
	}
	cleanupErr = errors.Join(cleanupErr, dhcpManager.Stop(state.PIDDNSMasq))
	cleanupErr = errors.Join(cleanupErr, mihomoManager.Stop(state.PIDMihomo))
	cleanupErr = errors.Join(cleanupErr, sysctlManager.Restore(state.IPForwardingBefore))
	if cleanupErr != nil {
		return m.retainCleanupState(cause, state, cleanupErr, deps)
	}
	cleanupErr = errors.Join(cleanupErr, deps.removeState(m.paths.StateFile))
	cleanupErr = errors.Join(cleanupErr, device.RemovePolicyBundleSnapshot(m.paths.DevicePolicyApplied))
	if cleanupErr != nil {
		return m.retainCleanupState(cause, state, cleanupErr, deps)
	}
	return cause
}

func (m Manager) retainCleanupState(cause error, state runtime.State, cleanupErr error, deps gatewayDeps) error {
	state.CleanupRequired = true
	state.CleanupError = cleanupErr.Error()
	stateSaveErr := deps.saveState(m.paths.StateFile, state)
	if stateSaveErr != nil {
		if cause != nil {
			return fmt.Errorf("%w; cleanup-required: %v; save recovery state: %w", cause, cleanupErr, stateSaveErr)
		}
		return fmt.Errorf("cleanup-required: %v; save recovery state: %w", cleanupErr, stateSaveErr)
	}
	if cause == nil {
		return fmt.Errorf("cleanup-required: %v", cleanupErr)
	}
	return fmt.Errorf("%w; cleanup-required: %v", cause, cleanupErr)
}
