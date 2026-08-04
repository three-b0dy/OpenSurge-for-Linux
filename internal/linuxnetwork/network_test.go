package linuxnetwork

import (
	"context"
	"strings"
	"testing"
)

func TestValidateManualAcceptsLinuxIPv4Config(t *testing.T) {
	err := ValidateManual(ManualConfig{
		NetworkService: "uplink0",
		Interface:      "uplink0",
		IPv4:           "192.0.2.20",
		SubnetMask:     "255.255.255.0",
		Router:         "192.0.2.1",
		DNS:            []string{"192.0.2.53"},
	})
	if err != nil {
		t.Fatalf("ValidateManual() error = %v", err)
	}
}

func TestVerifyManualAcceptsMatchingLinuxSnapshot(t *testing.T) {
	expected := ManualConfig{
		NetworkService: "uplink0",
		Interface:      "uplink0",
		IPv4:           "192.0.2.20",
		SubnetMask:     "255.255.255.0",
		Router:         "192.0.2.1",
	}
	snapshot := Snapshot{
		NetworkService: expected.NetworkService,
		Interface:      expected.Interface,
		IPv4Mode:       IPv4ModeManual,
		IPv4:           expected.IPv4,
		SubnetMask:     expected.SubnetMask,
		Router:         expected.Router,
	}
	if err := VerifyManual(snapshot, expected); err != nil {
		t.Fatalf("VerifyManual() error = %v", err)
	}
}

func TestLinuxInterfaceNamesRejectShellSyntax(t *testing.T) {
	for _, name := range []string{"uplink0", "br0.10", "mesh-test"} {
		if err := validateInterface(name); err != nil {
			t.Fatalf("validateInterface(%q) error = %v", name, err)
		}
	}
	for _, name := range []string{" uplink0", "uplink0 ", "uplink;ip", "uplink/0"} {
		if err := validateInterface(name); err == nil {
			t.Fatalf("validateInterface(%q) unexpectedly succeeded", name)
		}
	}
}

func TestUnsupportedNetworkActionsReportLifecycleOwnership(t *testing.T) {
	actions := []struct {
		name string
		call func() error
	}{
		{name: "manual", call: func() error { return SetManual(context.Background(), ManualConfig{}) }},
		{name: "dhcp", call: func() error { return SetDHCP(context.Background(), "uplink0") }},
		{name: "probe", call: func() error { _, err := ProbeDHCPServers(context.Background(), "uplink0", 0); return err }},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			err := action.call()
			if err == nil || !strings.Contains(err.Error(), "Linux gateway lifecycle") {
				t.Fatalf("error = %v, want Linux gateway lifecycle ownership", err)
			}
		})
	}
}
