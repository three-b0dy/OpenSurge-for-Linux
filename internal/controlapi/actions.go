package controlapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/gateway"
)

// GatewayAction is the small, fixed set of privileged operations exposed by
// the Linux gateway socket. Configuration mutations use the same socket but
// are deliberately separate actions so the daemon can authorize them without
// accepting arbitrary commands.
type GatewayAction string

const (
	GatewayStart              GatewayAction = "start"
	GatewayStop               GatewayAction = "stop"
	GatewayReload             GatewayAction = "reload"
	GatewayRestartMihomo      GatewayAction = "restart-mihomo"
	GatewayApplyProfile       GatewayAction = "config-apply-profile"
	GatewayApplyDevicePolicy  GatewayAction = "config-apply-device-policy"
	GatewayApplyControlConfig GatewayAction = "config-apply-control"
)

type GatewayClient interface {
	Run(context.Context, GatewayAction, string) error
}

type ConfigurationRunner interface {
	ApplyProfile(context.Context, string, string, []byte) (ProfileApplyResult, error)
	ApplyDevicePolicy(context.Context, string, string, []byte) (string, error)
	ApplyControlConfig(context.Context, string, string, []byte) (string, error)
}

type ProfileApplyResult struct {
	Revision string
	Reloaded bool
}

type DirectRunner struct{}

func (DirectRunner) Run(ctx context.Context, action GatewayAction, configPath string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("privileged gateway service is not installed or reachable")
	}
	var (
		cfg config.Config
		err error
	)
	if action == GatewayStart {
		cfg, err = config.Load(configPath)
	} else {
		cfg, err = config.LoadRuntime(configPath)
	}
	if err != nil {
		return err
	}
	manager := gateway.New(cfg)
	switch action {
	case GatewayStart:
		return manager.Start(ctx)
	case GatewayStop:
		return manager.Stop(ctx)
	case GatewayReload:
		return manager.Reload(ctx)
	case GatewayRestartMihomo:
		return manager.RestartMihomo(ctx)
	default:
		return fmt.Errorf("unsupported privileged action %q", action)
	}
}

// UnixGatewayClient is the unprivileged-side client for the root-owned
// gateway socket. The same client also carries configuration operations so
// the Control API has one privilege boundary.
type UnixGatewayClient struct {
	SocketPath string
}

type HelperRequest struct {
	Action     string `json:"action"`
	ConfigPath string `json:"config_path"`
	Revision   string `json:"revision,omitempty"`
	Payload    []byte `json:"payload,omitempty"`
}

type HelperResponse struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Revision string `json:"revision,omitempty"`
	Reloaded bool   `json:"reloaded,omitempty"`
}

func (c UnixGatewayClient) Run(ctx context.Context, action GatewayAction, configPath string) error {
	_, err := c.call(ctx, HelperRequest{Action: string(action), ConfigPath: configPath})
	return err
}

func (c UnixGatewayClient) ApplyProfile(ctx context.Context, configPath, revision string, payload []byte) (ProfileApplyResult, error) {
	response, err := c.call(ctx, HelperRequest{Action: string(GatewayApplyProfile), ConfigPath: configPath, Revision: revision, Payload: payload})
	return ProfileApplyResult{Revision: response.Revision, Reloaded: response.Reloaded}, err
}

func (c UnixGatewayClient) ApplyDevicePolicy(ctx context.Context, configPath, revision string, payload []byte) (string, error) {
	response, err := c.call(ctx, HelperRequest{Action: string(GatewayApplyDevicePolicy), ConfigPath: configPath, Revision: revision, Payload: payload})
	return response.Revision, err
}

func (c UnixGatewayClient) ApplyControlConfig(ctx context.Context, configPath, revision string, payload []byte) (string, error) {
	response, err := c.call(ctx, HelperRequest{Action: string(GatewayApplyControlConfig), ConfigPath: configPath, Revision: revision, Payload: payload})
	return response.Revision, err
}

func (c UnixGatewayClient) call(ctx context.Context, request HelperRequest) (HelperResponse, error) {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return HelperResponse{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return HelperResponse{}, err
	}
	var response HelperResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		return HelperResponse{}, err
	}
	if !response.OK {
		return HelperResponse{}, fmt.Errorf("%s", response.Error)
	}
	return response, nil
}

func listenerFromSystemdFile(socketPath, listenPID, listenFDS string, file *os.File) (net.Listener, bool, error) {
	if listenPID == "" && listenFDS == "" {
		return nil, false, nil
	}
	if listenPID != strconv.Itoa(os.Getpid()) {
		return nil, false, fmt.Errorf("systemd socket activation has unexpected LISTEN_PID")
	}
	count, err := strconv.Atoi(listenFDS)
	if err != nil || count != 1 {
		return nil, false, fmt.Errorf("systemd socket activation requires exactly one listener")
	}
	if file == nil {
		return nil, false, fmt.Errorf("systemd socket activation listener is unavailable")
	}
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, false, fmt.Errorf("open systemd gateway listener: %w", err)
	}
	unixAddr, ok := listener.Addr().(*net.UnixAddr)
	if !ok || unixAddr.Name != socketPath {
		_ = listener.Close()
		return nil, false, fmt.Errorf("systemd listener is not %s", socketPath)
	}
	return listener, true, nil
}

func systemdGatewayListener(socketPath string) (net.Listener, bool, error) {
	listenPID := os.Getenv("LISTEN_PID")
	listenFDS := os.Getenv("LISTEN_FDS")
	if listenPID == "" && listenFDS == "" {
		return nil, false, nil
	}
	file := os.NewFile(uintptr(3), "opensurge-gateway.socket")
	if file == nil {
		return nil, false, fmt.Errorf("systemd socket activation listener is unavailable")
	}
	defer file.Close()
	return listenerFromSystemdFile(socketPath, listenPID, listenFDS, file)
}

func ServeGateway(ctx context.Context, socketPath, allowedRoot, socketGroup string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("opensurge-gateway must run as root")
	}
	listener, activated, err := systemdGatewayListener(socketPath)
	if err != nil {
		return err
	}
	if !activated {
		if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
			return err
		}
		_ = os.Remove(socketPath)
		listener, err = net.Listen("unix", socketPath)
		if err != nil {
			return err
		}
	}
	defer listener.Close()
	if !activated {
		defer os.Remove(socketPath)
	}
	if !activated && socketGroup != "" {
		group, err := user.LookupGroup(socketGroup)
		if err != nil {
			return fmt.Errorf("lookup gateway socket group: %w", err)
		}
		gid, err := strconv.Atoi(group.Gid)
		if err != nil {
			return fmt.Errorf("parse gateway socket group: %w", err)
		}
		if err := os.Chown(socketPath, 0, gid); err != nil {
			return fmt.Errorf("set gateway socket group: %w", err)
		}
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handleGatewayConn(ctx, conn, newTrustedRoots(allowedRoot))
	}
}

func handleGatewayConn(ctx context.Context, conn net.Conn, roots trustedRoots) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	var request HelperRequest
	if err := json.NewDecoder(ioLimitReader(conn, 15<<20)).Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(HelperResponse{Error: err.Error()})
		return
	}
	action := GatewayAction(request.Action)
	if !helperActionAllowed(action) {
		_ = json.NewEncoder(conn).Encode(HelperResponse{Error: "action is not allowed"})
		return
	}
	configPath, err := filepath.Abs(request.ConfigPath)
	if err == nil && configPath != "" {
		root, rootErr := filepath.Abs(roots.config)
		if rootErr != nil || (configPath != root && !strings.HasPrefix(configPath, root+string(os.PathSeparator))) {
			err = fmt.Errorf("config path is outside allowed root")
		}
	}
	if err == nil && configPath != "" {
		err = requireRootOwnedConfig(configPath)
	}
	var cfg config.Config
	if err == nil {
		cfg, err = loadGatewayConfig(action, configPath)
	}
	if err == nil {
		err = requireTrustedRuntime(cfg, roots)
	}
	if err == nil && (action == GatewayStart || action == GatewayReload || action == GatewayRestartMihomo || action == GatewayApplyProfile) {
		err = requireTrustedStartInputs(cfg, roots)
	}
	if err == nil && (action == GatewayApplyProfile || action == GatewayApplyControlConfig) {
		err = requireTrustedDirectory(filepath.Join(filepath.Dir(configPath), "data"), roots.config)
	}
	if err == nil && action == GatewayApplyDevicePolicy {
		if cfg.DevicePolicy.File == "" {
			err = fmt.Errorf("device_policy.file is not configured")
		} else {
			err = requireTrustedFile(cfg.DevicePolicy.File, false, roots.config, roots.state)
		}
	}
	if err == nil && action == GatewayApplyDevicePolicy {
		err = requireTrustedStartInputs(cfg, roots)
	}
	response := HelperResponse{}
	if err == nil {
		runner := DirectRunner{}
		switch action {
		case GatewayStart, GatewayStop, GatewayReload, GatewayRestartMihomo:
			err = runner.Run(ctx, action, configPath)
		case GatewayApplyProfile:
			result, applyErr := runner.ApplyProfile(ctx, configPath, request.Revision, request.Payload)
			response.Revision, response.Reloaded, err = result.Revision, result.Reloaded, applyErr
		case GatewayApplyDevicePolicy:
			response.Revision, err = runner.ApplyDevicePolicy(ctx, configPath, request.Revision, request.Payload)
		case GatewayApplyControlConfig:
			response.Revision, err = runner.ApplyControlConfig(ctx, configPath, request.Revision, request.Payload)
		}
	}
	response.OK = err == nil
	if err != nil {
		response.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(response)
}

func loadGatewayConfig(action GatewayAction, configPath string) (config.Config, error) {
	if action == GatewayStop || action == GatewayRestartMihomo {
		return config.LoadRuntime(configPath)
	}
	return config.Load(configPath)
}

func helperActionAllowed(action GatewayAction) bool {
	switch action {
	case GatewayStart, GatewayStop, GatewayReload, GatewayRestartMihomo, GatewayApplyProfile, GatewayApplyDevicePolicy, GatewayApplyControlConfig:
		return true
	default:
		return false
	}
}

func requireRootOwnedConfig(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("config path is not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fmt.Errorf("gateway config must be owned by root")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("gateway config must not be writable by group or other")
	}
	return nil
}

// trustedRoots enumerates the directories the privileged helper accepts paths
// from. The packaged layout deliberately spreads config, persistent state,
// volatile runtime files, and executables across separate trees, so a single
// root cannot describe it: /etc/opensurge alone rejects the runtime directory
// under /var/lib/opensurge and the generated mihomo config under /run.
type trustedRoots struct {
	config   string
	state    string
	volatile string
	programs []string
}

const (
	defaultStateRoot    = "/var/lib/opensurge"
	defaultVolatileRoot = "/run/opensurge"
)

func newTrustedRoots(configRoot string) trustedRoots {
	return trustedRoots{
		config:   configRoot,
		state:    defaultStateRoot,
		volatile: defaultVolatileRoot,
		// Executables shipped by the package plus the distribution directories
		// holding dnsmasq. Every candidate still has to be root-owned and not
		// group- or other-writable.
		programs: []string{"/usr/lib/opensurge", "/usr/sbin", "/usr/bin", "/sbin", "/bin"},
	}
}

// singleRoot builds a roots set where every purpose shares one directory. Tests
// and source layouts keep their whole tree under a single path.
func singleRoot(root string) trustedRoots {
	return trustedRoots{config: root, state: root, volatile: root, programs: []string{root}}
}

func (r trustedRoots) stateRoots() []string {
	return []string{r.state, r.volatile, r.config}
}

func requireTrustedRuntime(cfg config.Config, roots trustedRoots) error {
	// The runtime directory holds persistent state; the generated mihomo config
	// normally lands on tmpfs under /run. Accept either tree for both, so source
	// layouts that keep them together still validate.
	if err := requireTrustedDirectory(cfg.Runtime.Dir, roots.stateRoots()...); err != nil {
		return fmt.Errorf("runtime.dir: %w", err)
	}
	if err := requireTrustedOutputPath(cfg.Mihomo.Config, roots.stateRoots()...); err != nil {
		return fmt.Errorf("mihomo.config: %w", err)
	}
	return nil
}

func requireTrustedStartInputs(cfg config.Config, roots trustedRoots) error {
	if err := requireTrustedFile(cfg.Mihomo.Binary, true, roots.programs...); err != nil {
		return fmt.Errorf("mihomo.binary: %w", err)
	}
	if cfg.DHCP.Enabled {
		// dhcp.binary is conventionally the bare name "dnsmasq", which the DHCP
		// manager resolves through PATH at start. Resolve it the same way here so
		// the trust check applies to the executable that will actually run.
		binary, err := resolveTrustedProgram(cfg.DHCP.Binary)
		if err != nil {
			return fmt.Errorf("dhcp.binary: %w", err)
		}
		if err := requireTrustedFile(binary, true, roots.programs...); err != nil {
			return fmt.Errorf("dhcp.binary: %w", err)
		}
	}
	if cfg.Mihomo.Profile != "" {
		if err := requireTrustedFile(cfg.Mihomo.Profile, false, roots.config, roots.state); err != nil {
			return fmt.Errorf("mihomo.profile: %w", err)
		}
	}
	if cfg.DevicePolicy.File != "" {
		if err := requireTrustedFile(cfg.DevicePolicy.File, false, roots.config, roots.state); err != nil {
			return fmt.Errorf("device_policy.file: %w", err)
		}
	}
	return nil
}

// resolveTrustedProgram turns a bare program name into the absolute path PATH
// resolution would pick. Absolute paths are returned unchanged so the caller's
// containment and ownership checks still decide whether it is acceptable.
func resolveTrustedProgram(name string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		return name, nil
	}
	resolved, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%q was not found in PATH", name)
	}
	return resolved, nil
}

func requireTrustedDirectory(path string, allowedRoots ...string) error {
	resolved, err := trustedResolvedPath(path, allowedRoots...)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("must be a directory")
	}
	return requireRootOwnedMode(info)
}

func requireTrustedFile(path string, executable bool, allowedRoots ...string) error {
	resolved, err := trustedResolvedPath(path, allowedRoots...)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("must be a regular file")
	}
	if err := requireRootOwnedMode(info); err != nil {
		return err
	}
	if executable && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("must be executable")
	}
	return nil
}

func requireTrustedOutputPath(path string, allowedRoots ...string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("must be absolute")
	}
	if _, err := trustedPathWithinRoot(path, allowedRoots...); err != nil {
		return err
	}
	return requireTrustedDirectory(filepath.Dir(path), allowedRoots...)
}

func trustedResolvedPath(path string, allowedRoots ...string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return trustedPathWithinRoot(resolved, allowedRoots...)
}

// trustedPathWithinRoot accepts a path contained by any one of allowedRoots.
// Each root is resolved through symlinks first, so a symlinked path cannot
// escape the tree it appears to be in.
func trustedPathWithinRoot(path string, allowedRoots ...string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Roots are compared after symlink resolution, so the candidate has to be
	// resolved the same way or a symlinked ancestor makes a contained path look
	// external. The path itself may not exist yet (generated config files), so
	// resolve its deepest existing ancestor and re-append the rest.
	resolved, err := resolveExistingAncestor(absolute)
	if err != nil {
		return "", err
	}
	var resolveErr error
	resolvedAnyRoot := false
	for _, allowedRoot := range allowedRoots {
		if allowedRoot == "" {
			continue
		}
		root, err := filepath.EvalSymlinks(allowedRoot)
		if err != nil {
			// A root that does not exist on this host cannot contain the path, but
			// it must not mask a real containment failure either.
			resolveErr = err
			continue
		}
		resolvedAnyRoot = true
		relative, err := filepath.Rel(root, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			continue
		}
		return absolute, nil
	}
	if !resolvedAnyRoot && resolveErr != nil {
		return "", resolveErr
	}
	return "", fmt.Errorf("path is outside allowed root")
}

// resolveExistingAncestor resolves the symlinks of the deepest ancestor of path
// that exists, then re-appends the components below it. A path whose ancestors
// are all real directories resolves to itself.
func resolveExistingAncestor(path string) (string, error) {
	remainder := ""
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if remainder == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, remainder), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without finding anything that exists.
			return path, nil
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

func requireRootOwnedMode(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fmt.Errorf("must be owned by root")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("must not be writable by group or other")
	}
	return nil
}

type limitedReader struct {
	r net.Conn
	n int64
}

func ioLimitReader(conn net.Conn, n int64) *limitedReader { return &limitedReader{r: conn, n: n} }

func (r *limitedReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, fmt.Errorf("gateway request too large")
	}
	if int64(len(p)) > r.n {
		p = p[:r.n]
	}
	n, err := r.r.Read(p)
	r.n -= int64(n)
	return n, err
}
