package sysctl

import "testing"

func TestFormatForwarding(t *testing.T) {
	tests := map[string]string{
		"1": "enabled",
		"0": "disabled",
		"":  "unknown",
	}

	for input, want := range tests {
		if got := FormatForwarding(input); got != want {
			t.Fatalf("FormatForwarding(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIPForwardingUsesLinuxKey(t *testing.T) {
	if keyIPForwarding != "net.ipv4.ip_forward" {
		t.Fatalf("keyIPForwarding = %q, want net.ipv4.ip_forward", keyIPForwarding)
	}
}
