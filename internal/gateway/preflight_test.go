package gateway

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/linuxnet"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/process"
)

type topologyInspector map[string][]netip.Prefix

func (f topologyInspector) Addresses(_ context.Context, name string) ([]netip.Prefix, error) {
	addresses, ok := f[name]
	if !ok {
		return nil, errors.New("interface not found")
	}
	return addresses, nil
}

func (topologyInspector) Neighbors(context.Context, string) ([]linuxnet.Neighbor, error) {
	return nil, nil
}

type policyRuleRunner struct {
	output []byte
	err    error
	calls  [][]string
}

func (r *policyRuleRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.output, r.err
}

var _ process.Runner = (*policyRuleRunner)(nil)

func topologyTestConfig(mode, gatewayInterface, upstreamInterface string) config.Config {
	cfg := config.Default()
	cfg.Gateway.Mode = mode
	cfg.Gateway.Interface = gatewayInterface
	cfg.Gateway.UpstreamInterface = upstreamInterface
	cfg.Gateway.LANIP = "192.168.50.1"
	cfg.DHCP.Enabled = mode != config.GatewayModeSameLAN
	cfg.Gateway.RouterDHCPDisabledConfirmed = false
	return cfg
}

func TestValidateTopologySameLANRejectsDifferentInterfaces(t *testing.T) {
	cfg := topologyTestConfig(config.GatewayModeSameLAN, "lan0", "wan0")
	err := ValidateTopology(context.Background(), cfg, topologyInspector{
		"lan0": {netip.MustParsePrefix("192.168.50.1/24")},
		"wan0": {netip.MustParsePrefix("198.51.100.2/24")},
	})
	if err == nil || !strings.Contains(err.Error(), "requires gateway and upstream interfaces to match") {
		t.Fatalf("ValidateTopology() error = %v", err)
	}
}

func TestValidateTopologySameWiFiDHCPRequiresConfirmation(t *testing.T) {
	cfg := topologyTestConfig(config.GatewayModeSameWiFiDHCP, "lan0", "lan0")
	err := ValidateTopology(context.Background(), cfg, topologyInspector{
		"lan0": {netip.MustParsePrefix("192.168.50.1/24")},
	})
	if err == nil || !strings.Contains(err.Error(), "router_dhcp_disabled_confirmed") {
		t.Fatalf("ValidateTopology() error = %v", err)
	}
}

func TestValidateTopologyIsolatedLANRejectsOverlappingPrefixes(t *testing.T) {
	cfg := topologyTestConfig(config.GatewayModeIsolatedLAN, "lan0", "wan0")
	err := ValidateTopology(context.Background(), cfg, topologyInspector{
		"lan0": {netip.MustParsePrefix("192.168.50.1/24")},
		"wan0": {netip.MustParsePrefix("192.168.50.2/24")},
	})
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("ValidateTopology() error = %v", err)
	}
}

func TestValidateTopologyRejectsUnconfiguredLANAddress(t *testing.T) {
	cfg := topologyTestConfig(config.GatewayModeIsolatedLAN, "lan0", "wan0")
	err := ValidateTopology(context.Background(), cfg, topologyInspector{
		"lan0": {netip.MustParsePrefix("192.168.50.2/24")},
		"wan0": {netip.MustParsePrefix("198.51.100.2/24")},
	})
	if err == nil || !strings.Contains(err.Error(), "LAN IP 192.168.50.1 is not configured") {
		t.Fatalf("ValidateTopology() error = %v", err)
	}
}

func TestValidateTopologyRejectsDHCPPoolContainingGatewayAddress(t *testing.T) {
	cfg := topologyTestConfig(config.GatewayModeIsolatedLAN, "lan0", "wan0")
	cfg.DHCP.RangeStart = cfg.Gateway.LANIP
	err := ValidateTopology(context.Background(), cfg, topologyInspector{
		"lan0": {netip.MustParsePrefix("192.168.50.1/24")},
		"wan0": {netip.MustParsePrefix("198.51.100.2/24")},
	})
	if err == nil || !strings.Contains(err.Error(), "DHCP range must not include gateway LAN IP") {
		t.Fatalf("ValidateTopology() error = %v", err)
	}
}

func TestValidateTopologyRejectsListenerPortConflict(t *testing.T) {
	cfg := topologyTestConfig(config.GatewayModeIsolatedLAN, "lan0", "wan0")
	cfg.DNS.Port = cfg.Mihomo.MixedPort
	err := ValidateTopology(context.Background(), cfg, topologyInspector{
		"lan0": {netip.MustParsePrefix("192.168.50.1/24")},
		"wan0": {netip.MustParsePrefix("198.51.100.2/24")},
	})
	if err == nil || !strings.Contains(err.Error(), "port conflict") {
		t.Fatalf("ValidateTopology() error = %v", err)
	}
}

func TestDetectPolicyRouteConflictRejectsMihomoReservedPriority(t *testing.T) {
	runner := &policyRuleRunner{output: []byte(`[{"priority":9001,"table":2022}]`)}
	err := DetectPolicyRouteConflict(context.Background(), runner)
	if err == nil || !strings.Contains(err.Error(), "9001") {
		t.Fatalf("DetectPolicyRouteConflict() error = %v", err)
	}
	if want := [][]string{{"ip", "-j", "rule", "show"}}; !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("runner calls = %#v, want %#v", runner.calls, want)
	}
}

func TestDetectPolicyRouteConflictAcceptsDefaultRules(t *testing.T) {
	runner := &policyRuleRunner{output: []byte(`[{"priority":0,"table":"local"},{"priority":32766,"table":"main"}]`)}
	if err := DetectPolicyRouteConflict(context.Background(), runner); err != nil {
		t.Fatalf("DetectPolicyRouteConflict() error = %v", err)
	}
}

func TestDetectPolicyRouteConflictReportsCommandFailure(t *testing.T) {
	runner := &policyRuleRunner{err: errors.New("ip unavailable")}
	err := DetectPolicyRouteConflict(context.Background(), runner)
	if err == nil || !strings.Contains(err.Error(), "ip -j rule show") {
		t.Fatalf("DetectPolicyRouteConflict() error = %v", err)
	}
}
