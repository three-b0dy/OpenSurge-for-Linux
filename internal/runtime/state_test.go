package runtime

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoadState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := State{
		PIDDNSMasq:            101,
		PIDMihomo:             202,
		IPForwardingBefore:    "0",
		NftablesLoaded:        true,
		FirewallEnabledBefore: true,
		StartedAt:             time.Unix(1_700_000_000, 0).UTC(),
	}

	if err := SaveState(path, want); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}
	got, exists, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if !exists {
		t.Fatalf("LoadState() exists = false")
	}
	if got != want {
		t.Fatalf("LoadState() = %+v, want %+v", got, want)
	}
}

func TestSaveStateReplacesExistingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first := State{PIDDNSMasq: 101, IPForwardingBefore: "0"}
	second := State{PIDMihomo: 202, IPForwardingBefore: "1", NftablesLoaded: true}

	if err := SaveState(path, first); err != nil {
		t.Fatalf("SaveState(first) error = %v", err)
	}
	if err := SaveState(path, second); err != nil {
		t.Fatalf("SaveState(second) error = %v", err)
	}
	got, exists, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if !exists {
		t.Fatalf("LoadState() exists = false")
	}
	if got != second {
		t.Fatalf("LoadState() = %+v, want %+v", got, second)
	}
}

func TestStateDropsLegacySystemProxyDuringRoundTrip(t *testing.T) {
	var state State
	if err := json.Unmarshal([]byte(`{"nftables_loaded":true,"firewall_enabled_before":false,"local_system_proxy":{"network_service":"Wi-Fi"}}`), &state); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"nftables_loaded":true`)) {
		t.Fatalf("state JSON = %s, missing nftables_loaded", data)
	}
	if !bytes.Contains(data, []byte(`"firewall_enabled_before":false`)) {
		t.Fatalf("state JSON = %s, missing firewall_enabled_before", data)
	}
	if bytes.Contains(data, []byte("local_system_proxy")) {
		t.Fatalf("state JSON retained removed platform fields: %s", data)
	}
}

func TestStateSerializesFirewallRestoreFlag(t *testing.T) {
	data, err := json.Marshal(State{FirewallEnabledBefore: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"firewall_enabled_before":true`)) {
		t.Fatalf("state JSON = %s, missing firewall_enabled_before", data)
	}
}

func TestSaveStateFailureLeavesExistingState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	existing := State{PIDDNSMasq: 101, IPForwardingBefore: "0"}

	if err := SaveState(path, existing); err != nil {
		t.Fatalf("SaveState(existing) error = %v", err)
	}
	err := SaveState(filepath.Join(dir, "missing", "state.json"), State{PIDMihomo: 202})
	if err == nil {
		t.Fatalf("SaveState() succeeded with missing parent directory")
	}
	got, exists, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState(existing) error = %v", err)
	}
	if !exists {
		t.Fatalf("LoadState(existing) exists = false")
	}
	if got != existing {
		t.Fatalf("LoadState(existing) = %+v, want %+v", got, existing)
	}
}
