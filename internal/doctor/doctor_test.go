package doctor

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/linuxnet"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/mihomo"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/process"
)

type doctorTopologyInspector map[string][]netip.Prefix

func (f doctorTopologyInspector) Addresses(_ context.Context, name string) ([]netip.Prefix, error) {
	addresses, ok := f[name]
	if !ok {
		return nil, errors.New("interface not found")
	}
	return addresses, nil
}

func (doctorTopologyInspector) Neighbors(context.Context, string) ([]linuxnet.Neighbor, error) {
	return nil, nil
}

type doctorPolicyRunner struct {
	err   error
	calls [][]string
}

func (r *doctorPolicyRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil, r.err
}

var _ process.Runner = (*doctorPolicyRunner)(nil)

func TestCheckTopologyReportsSameLANIPv6Limitation(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Mode = config.GatewayModeSameLAN
	cfg.Gateway.Interface = "lan0"
	cfg.Gateway.UpstreamInterface = "lan0"
	cfg.Gateway.LANIP = "192.168.50.1"
	cfg.DHCP.Enabled = false
	check := checkTopology(context.Background(), cfg, doctorTopologyInspector{
		"lan0": {netip.MustParsePrefix("192.168.50.1/24")},
	})
	if !check.OK {
		t.Fatalf("checkTopology() OK = false: %s", check.Message)
	}
	if !strings.Contains(strings.ToLower(check.Message), "ipv6") {
		t.Fatalf("checkTopology() message = %q, want IPv6 limitation", check.Message)
	}
}

func TestCheckPolicyRouteConflictNamesIPRuleCommand(t *testing.T) {
	runner := &doctorPolicyRunner{err: errors.New("ip unavailable")}
	check := checkPolicyRouteConflict(context.Background(), runner)
	if check.OK || !strings.Contains(check.Message, "ip -j rule show") {
		t.Fatalf("checkPolicyRouteConflict() = %#v", check)
	}
	if want := [][]string{{"ip", "-j", "rule", "show"}}; !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("runner calls = %#v, want %#v", runner.calls, want)
	}
}

func TestCheckGatewayInterfaceTopology(t *testing.T) {
	cfg := config.Default()
	cfg.Gateway.Interface = "en0"
	cfg.Gateway.UpstreamInterface = " en0 "
	check := checkGatewayInterfaceTopology(cfg.Gateway)
	if check.OK {
		t.Fatalf("checkGatewayInterfaceTopology() OK = true")
	}
	if check.Message == "" {
		t.Fatalf("checkGatewayInterfaceTopology() missing failure message")
	}

	cfg.Gateway.Interface = "en7"
	cfg.Gateway.UpstreamInterface = "en0"
	check = checkGatewayInterfaceTopology(cfg.Gateway)
	if !check.OK {
		t.Fatalf("checkGatewayInterfaceTopology() OK = false: %s", check.Message)
	}

	cfg.Gateway.Mode = config.GatewayModeSameLAN
	cfg.Gateway.Interface = "en0"
	cfg.Gateway.UpstreamInterface = " en0 "
	check = checkGatewayInterfaceTopology(cfg.Gateway)
	if !check.OK {
		t.Fatalf("checkGatewayInterfaceTopology() same_lan OK = false: %s", check.Message)
	}

	cfg.Gateway.UpstreamInterface = "en7"
	check = checkGatewayInterfaceTopology(cfg.Gateway)
	if check.OK {
		t.Fatalf("checkGatewayInterfaceTopology() same_lan with different interfaces OK = true")
	}

	cfg.Gateway.Mode = config.GatewayModeSameWiFiDHCP
	cfg.Gateway.UpstreamInterface = "en0"
	check = checkGatewayInterfaceTopology(cfg.Gateway)
	if !check.OK || !strings.Contains(check.Message, "same_wifi_dhcp") {
		t.Fatalf("checkGatewayInterfaceTopology() same_wifi_dhcp = %#v", check)
	}
}

func TestCheckInterfaceIPv4RejectsInvalidIP(t *testing.T) {
	check := checkInterfaceIPv4("en0", "not-an-ip")
	if check.OK {
		t.Fatalf("checkInterfaceIPv4() OK = true")
	}
	if check.Message != "invalid IPv4 address" {
		t.Fatalf("checkInterfaceIPv4() message = %q", check.Message)
	}
}

func TestCheckNftablesRulesRender(t *testing.T) {
	cfg := config.Default()
	check := checkNftablesRulesRender(cfg)
	if !check.OK {
		t.Fatalf("checkNftablesRulesRender() OK = false: %s", check.Message)
	}

	cfg.Nftables.Table = "invalid table"
	check = checkNftablesRulesRender(cfg)
	if check.OK {
		t.Fatal("checkNftablesRulesRender() OK = true for invalid table name")
	}
	if !strings.Contains(check.Message, "table") {
		t.Fatalf("checkNftablesRulesRender() message = %q", check.Message)
	}
}

func TestCheckMihomoConfigRenderAcceptsImportedProfile(t *testing.T) {
	useRenderOnlyValidation(t)
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(profilePath, []byte("rules:\n  - MATCH,DIRECT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mihomo.ProfileMode = config.MihomoProfileModeImported
	cfg.Mihomo.Profile = profilePath

	check := checkMihomoConfigRender(cfg)
	if !check.OK {
		t.Fatalf("checkMihomoConfigRender() OK = false: %s", check.Message)
	}
}

func TestCheckMihomoConfigRenderRejectsMissingImportedProfile(t *testing.T) {
	useRenderOnlyValidation(t)
	cfg := config.Default()
	cfg.Mihomo.ProfileMode = config.MihomoProfileModeImported
	cfg.Mihomo.Profile = filepath.Join(t.TempDir(), "missing.yaml")

	check := checkMihomoConfigRender(cfg)
	if check.OK {
		t.Fatalf("checkMihomoConfigRender() OK = true")
	}
	if !strings.Contains(check.Message, "read imported mihomo profile") {
		t.Fatalf("checkMihomoConfigRender() message = %q", check.Message)
	}
}

func TestValidateMihomoConfigWithEngineRunsMihomoTestMode(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	binary := filepath.Join(dir, "mihomo")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mihomo.Binary = binary
	cfg.Runtime.Dir = filepath.Join(dir, "runtime")
	cfg.Mihomo.Config = filepath.Join(cfg.Runtime.Dir, "mihomo.yaml")
	if err := validateMihomoConfigWithEngine(cfg); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "-t\n") || !strings.Contains(string(args), "-f\n") {
		t.Fatalf("mihomo arguments = %q", args)
	}
}

func useRenderOnlyValidation(t *testing.T) {
	t.Helper()
	previous := validateMihomoConfig
	validateMihomoConfig = func(cfg config.Config) error {
		_, err := mihomo.RenderConfig(cfg)
		return err
	}
	t.Cleanup(func() { validateMihomoConfig = previous })
}
