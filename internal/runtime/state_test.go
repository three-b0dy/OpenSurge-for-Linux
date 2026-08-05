package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoadState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := State{
		PIDDNSMasq:         101,
		PIDMihomo:          202,
		IPForwardingBefore: "0",
		NftablesLoaded:     true,
		StartedAt:          time.Unix(1_700_000_000, 0).UTC(),
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

// The gateway writes this file as root and the unprivileged control service
// reads it for every status query, so it must stay group-readable.
func TestSaveStateKeepsTheFileGroupReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(path, State{PIDMihomo: 202, NftablesLoaded: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("state mode = %o, want 640", info.Mode().Perm())
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

func TestStateIgnoresRemovedSystemProxyFieldDuringRoundTrip(t *testing.T) {
	var state State
	legacyProxyKey := strings.Join([]string{"local", "system", "proxy"}, "_")
	data := []byte(`{"nftables_loaded":true,"` + legacyProxyKey + `":{"network_service":"Wi-Fi"}}`)
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"nftables_loaded":true`)) {
		t.Fatalf("state JSON = %s, missing nftables_loaded", data)
	}
	if bytes.Contains(data, []byte(strings.Join([]string{"local", "system", "proxy"}, "_"))) {
		t.Fatalf("state JSON retained removed platform fields: %s", data)
	}
}

func TestStateDoesNotSerializeRemovedFirewallRestoreFlag(t *testing.T) {
	data, err := json.Marshal(State{NftablesLoaded: true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("firewall_enabled_before")) {
		t.Fatalf("state JSON retained removed firewall restore state: %s", data)
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
