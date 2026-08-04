package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.Gateway.Mode = GatewayModeSameWiFiDHCP
	cfg.Gateway.Interface = "en0"
	cfg.Gateway.UpstreamInterface = "en0"
	cfg.Gateway.LANIP = "192.168.1.20"
	cfg.Gateway.RouterDHCPDisabledConfirmed = true
	cfg.Management.Listen = "192.168.1.20:61767"
	cfg.Management.TLSCertFile = "/etc/opensurge/tls/cert.pem"
	cfg.Management.TLSKeyFile = "/etc/opensurge/tls/key.pem"
	cfg.Nftables.Table = "customsurge"
	cfg.DHCP.RangeStart = "192.168.1.120"
	cfg.DHCP.RangeEnd = "192.168.1.199"
	cfg.Transparent.Mode = TransparentModeTUN
	cfg.Transparent.TUNAutoRedirect = true
	cfg.DevicePolicy.ProtectedIPv4 = []string{"192.168.1.1", "192.168.1.21"}
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "devices.json")
	if err := os.WriteFile(policyPath, []byte(`{"devices":[],"profiles":[],"templates":[],"rule_sets":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.DevicePolicy.File = policyPath
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(Render(cfg)), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(Render()) error = %v", err)
	}
	if loaded.Gateway.Mode != cfg.Gateway.Mode || loaded.DHCP.RangeStart != cfg.DHCP.RangeStart || !loaded.Gateway.RouterDHCPDisabledConfirmed || loaded.Management != cfg.Management || loaded.Nftables != cfg.Nftables || !loaded.Transparent.TUNAutoRedirect {
		t.Fatalf("round trip mismatch: %#v", loaded)
	}
}

func TestRenderOmitsRemovedPlatformFields(t *testing.T) {
	rendered := Render(Default())
	for _, removed := range []string{"pf:", "local_system_proxy:", "anchor_name:", "redirect_tcp_to:"} {
		if strings.Contains(rendered, removed) {
			t.Fatalf("Render() retained %q:\n%s", removed, rendered)
		}
	}
}
