package controlapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
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

func ServeGateway(ctx context.Context, socketPath, allowedRoot, socketGroup string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("opensurge-gateway must run as root")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if socketGroup != "" {
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
		go handleGatewayConn(ctx, conn, allowedRoot)
	}
}

func handleGatewayConn(ctx context.Context, conn net.Conn, allowedRoot string) {
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
		root, rootErr := filepath.Abs(allowedRoot)
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
		err = requireTrustedRuntime(cfg, allowedRoot)
	}
	if err == nil && (action == GatewayStart || action == GatewayReload || action == GatewayRestartMihomo || action == GatewayApplyProfile) {
		err = requireTrustedStartInputs(cfg, allowedRoot)
	}
	if err == nil && (action == GatewayApplyProfile || action == GatewayApplyControlConfig) {
		err = requireTrustedDirectory(filepath.Join(filepath.Dir(configPath), "data"), allowedRoot)
	}
	if err == nil && action == GatewayApplyDevicePolicy {
		if cfg.DevicePolicy.File == "" {
			err = fmt.Errorf("device_policy.file is not configured")
		} else {
			err = requireTrustedFile(cfg.DevicePolicy.File, allowedRoot, false)
		}
	}
	if err == nil && action == GatewayApplyDevicePolicy {
		err = requireTrustedStartInputs(cfg, allowedRoot)
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

func requireTrustedRuntime(cfg config.Config, allowedRoot string) error {
	if err := requireTrustedDirectory(cfg.Runtime.Dir, allowedRoot); err != nil {
		return fmt.Errorf("runtime.dir: %w", err)
	}
	if err := requireTrustedOutputPath(cfg.Mihomo.Config, allowedRoot); err != nil {
		return fmt.Errorf("mihomo.config: %w", err)
	}
	return nil
}

func requireTrustedStartInputs(cfg config.Config, allowedRoot string) error {
	for name, path := range map[string]string{"mihomo.binary": cfg.Mihomo.Binary} {
		if err := requireTrustedFile(path, allowedRoot, true); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if cfg.DHCP.Enabled {
		if err := requireTrustedFile(cfg.DHCP.Binary, allowedRoot, true); err != nil {
			return fmt.Errorf("dhcp.binary: %w", err)
		}
	}
	if cfg.Mihomo.Profile != "" {
		if err := requireTrustedFile(cfg.Mihomo.Profile, allowedRoot, false); err != nil {
			return fmt.Errorf("mihomo.profile: %w", err)
		}
	}
	if cfg.DevicePolicy.File != "" {
		if err := requireTrustedFile(cfg.DevicePolicy.File, allowedRoot, false); err != nil {
			return fmt.Errorf("device_policy.file: %w", err)
		}
	}
	return nil
}

func requireTrustedDirectory(path, allowedRoot string) error {
	resolved, err := trustedResolvedPath(path, allowedRoot)
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

func requireTrustedFile(path, allowedRoot string, executable bool) error {
	resolved, err := trustedResolvedPath(path, allowedRoot)
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

func requireTrustedOutputPath(path, allowedRoot string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("must be absolute")
	}
	if _, err := trustedPathWithinRoot(path, allowedRoot); err != nil {
		return err
	}
	return requireTrustedDirectory(filepath.Dir(path), allowedRoot)
}

func trustedResolvedPath(path, allowedRoot string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return trustedPathWithinRoot(resolved, allowedRoot)
}

func trustedPathWithinRoot(path, allowedRoot string) (string, error) {
	root, err := filepath.EvalSymlinks(allowedRoot)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path is outside allowed root")
	}
	return absolute, nil
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
