package layout

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func redirectRoots(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	originalConfig, originalState := ConfigRoot, StateRoot
	ConfigRoot = filepath.Join(base, "etc/opensurge")
	StateRoot = filepath.Join(base, "var/lib/opensurge")
	t.Cleanup(func() { ConfigRoot, StateRoot = originalConfig, originalState })
	return base
}

// os.FileMode keeps setgid outside the Unix 0o2000 slot, so an octal literal
// handed to os.Chmod applies 0750 and drops setgid. Every directory whose
// children must inherit the service group has to survive that round trip.
func TestDirectoryModesCarrySetgidThroughChmod(t *testing.T) {
	redirectRoots(t)
	for _, entry := range Entries() {
		if entry.Kind != Directory || entry.Path == StateRoot {
			continue
		}
		if entry.Mode&os.ModeSetgid == 0 {
			t.Errorf("%s has mode %v without setgid; new files there would not join the service group", entry.Path, entry.Mode)
		}
		if got := modeBits(entry.Mode); got != 0o2750 {
			t.Errorf("%s mode bits = %04o, want 2750", entry.Path, got)
		}
	}
}

func TestStateRootKeepsTheStickyBitForGroupOwnedFiles(t *testing.T) {
	redirectRoots(t)
	for _, entry := range Entries() {
		if entry.Path != StateRoot {
			continue
		}
		if got := modeBits(entry.Mode); got != 0o1770 {
			t.Fatalf("state root mode bits = %04o, want 1770", got)
		}
		return
	}
	t.Fatal("the state root is not part of the layout")
}

func TestEntriesListParentsBeforeChildren(t *testing.T) {
	redirectRoots(t)
	seen := map[string]bool{}
	for _, entry := range Entries() {
		parent := filepath.Dir(entry.Path)
		if strings.HasPrefix(entry.Path, ConfigRoot+string(os.PathSeparator)) || strings.HasPrefix(entry.Path, StateRoot+string(os.PathSeparator)) {
			if !seen[parent] {
				t.Errorf("%s appears before its parent %s, so a single ordered pass cannot create it", entry.Path, parent)
			}
		}
		seen[entry.Path] = true
	}
}

// state.json is deleted when the gateway stops, so its absence says nothing —
// but a root:root 0600 copy of it is what leaves the Web UI with an empty
// gateway status, so it must be covered while it exists.
func TestRuntimeStateIsCoveredButNotRequired(t *testing.T) {
	redirectRoots(t)
	statePath := filepath.Join(StateRoot, "runtime", "state.json")
	var found *Entry
	for _, entry := range Entries() {
		if entry.Path == statePath {
			found = &entry
			break
		}
	}
	if found == nil {
		t.Fatal("runtime state.json is not part of the layout")
	}
	if found.Kind != OptionalFile {
		t.Errorf("state.json kind = %v, want OptionalFile so a stopped gateway is not reported as broken", found.Kind)
	}
	if !found.GroupOwned || modeBits(found.Mode) != 0o640 {
		t.Errorf("state.json = group-owned %t mode %04o, want group-owned 0640", found.GroupOwned, modeBits(found.Mode))
	}
}

func TestCheckIgnoresAbsentOptionalRuntimeFiles(t *testing.T) {
	redirectRoots(t)
	if _, err := Check(); err != nil {
		t.Skipf("host has no %q group: %v", ServiceGroup, err)
	}
	problems, err := Check()
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		for _, ephemeral := range []string{"state.json", "dnsmasq.conf", "nftables.rules", "device-policy.applied.json"} {
			if strings.Contains(problem, ephemeral) && strings.Contains(problem, "missing") {
				t.Errorf("Check() reported an absent ephemeral file as a problem: %s", problem)
			}
		}
	}
}

func TestCheckReportsMissingPathsWithoutChangingAnything(t *testing.T) {
	base := redirectRoots(t)
	if _, err := Check(); err != nil {
		t.Skipf("host has no %q group: %v", ServiceGroup, err)
	}
	problems, err := Check()
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("Check() reported no problems for an empty tree")
	}
	if _, err := os.Stat(ConfigRoot); !os.IsNotExist(err) {
		t.Fatalf("Check() created %s", ConfigRoot)
	}
	if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
		t.Fatalf("Check() wrote into the tree: %v entries, err=%v", len(entries), err)
	}
}

func TestApplyRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	redirectRoots(t)
	if _, err := Apply(io.Discard); err == nil || !strings.Contains(err.Error(), "requires root") {
		t.Fatalf("Apply() error = %v, want a root requirement", err)
	}
}
