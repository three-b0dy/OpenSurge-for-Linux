package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	PIDDNSMasq            int       `json:"pid_dnsmasq,omitempty"`
	PIDMihomo             int       `json:"pid_mihomo,omitempty"`
	IPForwardingBefore    string    `json:"ip_forwarding_before,omitempty"`
	NftablesLoaded        bool      `json:"nftables_loaded"`
	FirewallEnabledBefore bool      `json:"firewall_enabled_before"`
	DevicePolicyDigest    string    `json:"device_policy_digest,omitempty"`
	ProfileDigest         string    `json:"profile_digest,omitempty"`
	StartedAt             time.Time `json:"started_at"`
}

// SystemProxySnapshot and SystemProxySetting remain as legacy type definitions
// for the retained macOS package. Linux runtime State no longer stores them.
type SystemProxySnapshot struct {
	NetworkService       string             `json:"network_service"`
	Interface            string             `json:"interface"`
	HTTP                 SystemProxySetting `json:"http"`
	HTTPS                SystemProxySetting `json:"https"`
	AutoConfigEnabled    bool               `json:"auto_config_enabled,omitempty"`
	AutoDiscoveryEnabled bool               `json:"auto_discovery_enabled,omitempty"`
}

type SystemProxySetting struct {
	Enabled       bool   `json:"enabled"`
	Server        string `json:"server,omitempty"`
	Port          int    `json:"port,omitempty"`
	Authenticated bool   `json:"authenticated,omitempty"`
}

func LoadState(path string) (State, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func SaveState(path string, state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func RemoveState(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
