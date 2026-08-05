package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"
)

type State struct {
	PIDDNSMasq         int       `json:"pid_dnsmasq,omitempty"`
	PIDMihomo          int       `json:"pid_mihomo,omitempty"`
	IPForwardingBefore string    `json:"ip_forwarding_before,omitempty"`
	NftablesLoaded     bool      `json:"nftables_loaded"`
	CleanupRequired    bool      `json:"cleanup_required,omitempty"`
	CleanupError       string    `json:"cleanup_error,omitempty"`
	DevicePolicyDigest string    `json:"device_policy_digest,omitempty"`
	ProfileDigest      string    `json:"profile_digest,omitempty"`
	StartedAt          time.Time `json:"started_at"`
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
	// The gateway writes this state as root, but the unprivileged control service
	// reads it to report gateway status. Without the group, 0640 root:root makes
	// every status read fail with a permission error.
	if err := grantServiceGroup(tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// grantServiceGroup tries to hand a root-written file to the opensurge group so
// the unprivileged control service can read it.
//
// This is a best-effort second line of defence, not the primary mechanism: the
// gateway runs with a capability set that excludes CAP_CHOWN, so chown fails
// with EPERM there. The packaged runtime directory carries the setgid bit, which
// makes new files inherit the group without any privileged call. Callers that
// do run with CAP_CHOWN (opensurge-setup from a TTY) still benefit, and a
// missing group on a source build is not an error.
func grantServiceGroup(path string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	group, err := user.LookupGroup("opensurge")
	if err != nil {
		return nil
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return nil
	}
	if err := os.Chown(path, 0, gid); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil
		}
		return fmt.Errorf("set root:opensurge ownership on %s: %w", path, err)
	}
	return nil
}

func RemoveState(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
