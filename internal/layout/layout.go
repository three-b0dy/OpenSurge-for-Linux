// Package layout is the single description of the on-disk paths OpenSurge owns,
// together with the ownership and modes each one needs.
//
// The invariant it encodes: root writes almost everything, but the control
// service runs as the unprivileged opensurge account and has to read a subset of
// it. The root gateway runs without CAP_CHOWN, so it cannot hand a file to the
// opensurge group itself — directories carry the setgid bit and new files inherit
// the group from them. A directory that loses setgid, or a file that ends up
// root:root, is enough to break Web UI login, gateway status, or startup.
package layout

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
)

// ServiceGroup is the local group shared by the root writers and the
// unprivileged control service.
const ServiceGroup = "opensurge"

// setgidDir is 2750 as chmod would write it. os.FileMode keeps setgid in a high
// bit rather than the Unix 0o2000 position, so an octal literal like 0o2750
// passed to os.Chmod would apply 0750 and silently drop setgid — the very
// regression this package exists to repair.
const setgidDir = os.FileMode(0o750) | os.ModeSetgid

// Kind distinguishes what an entry is and, for files, whether this tool may
// create it when missing.
type Kind int

const (
	// Directory is created when absent.
	Directory Kind = iota
	// CreatableFile may be created empty, because an empty one is meaningful
	// (process logs the control service tails).
	CreatableFile
	// ReportedFile is reported when absent but never fabricated: an empty
	// config or administrator record would be worse than a missing one.
	ReportedFile
	// OptionalFile is repaired when present and ignored when absent. The gateway
	// creates and deletes these across its lifecycle, so absence carries no
	// information — but while they exist their ownership still matters.
	OptionalFile
)

// Entry is one path OpenSurge owns.
type Entry struct {
	Path string
	Kind Kind
	// Mode is the permission set, including setgid on directories whose children
	// must inherit the service group.
	Mode os.FileMode
	// GroupOwned marks paths that must belong to ServiceGroup rather than root.
	GroupOwned bool
	Purpose    string
}

// Paths are the packaged locations. Roots stay in variables so tests can point
// the whole layout at a temporary tree.
var (
	ConfigRoot = "/etc/opensurge"
	StateRoot  = "/var/lib/opensurge"
)

// Entries returns the layout in creation order: every parent precedes its
// children, so applying the slice in order never needs a second pass.
func Entries() []Entry {
	return []Entry{
		{Path: ConfigRoot, Kind: Directory, Mode: setgidDir, GroupOwned: true,
			Purpose: "gateway configuration; setgid keeps rewritten files readable by the control service"},
		{Path: filepath.Join(ConfigRoot, "tls"), Kind: Directory, Mode: setgidDir, GroupOwned: true,
			Purpose: "managed HTTPS certificate and private key"},
		{Path: filepath.Join(ConfigRoot, "data"), Kind: Directory, Mode: setgidDir, GroupOwned: true,
			Purpose: "imported profiles and the generated device policy; the gateway helper requires it to exist"},
		{Path: filepath.Join(ConfigRoot, "config.yaml"), Kind: ReportedFile, Mode: 0o640, GroupOwned: true,
			Purpose: "gateway configuration read by the control service"},

		// 1770 with the sticky bit: the opensurge account manages its own files
		// here without being able to replace root-owned entries such as
		// install-state.
		{Path: StateRoot, Kind: Directory, Mode: 0o770 | os.ModeSticky, GroupOwned: true,
			Purpose: "control-plane state"},
		{Path: filepath.Join(StateRoot, "admin.json"), Kind: ReportedFile, Mode: 0o640, GroupOwned: true,
			Purpose: "administrator record; unreadable here means every Web UI login fails"},

		// runtime/ must stay root-owned: the gateway helper refuses to act on a
		// runtime directory an unprivileged account could tamper with.
		{Path: filepath.Join(StateRoot, "runtime"), Kind: Directory, Mode: setgidDir, GroupOwned: true,
			Purpose: "gateway runtime state written by root and read by the control service"},
		{Path: filepath.Join(StateRoot, "runtime", "logs"), Kind: Directory, Mode: setgidDir, GroupOwned: true,
			Purpose: "dnsmasq and mihomo logs shown in the diagnostics view"},
		{Path: filepath.Join(StateRoot, "runtime", "logs", "dnsmasq.log"), Kind: CreatableFile, Mode: 0o640, GroupOwned: true,
			Purpose: "dnsmasq output tailed by the diagnostics view"},
		{Path: filepath.Join(StateRoot, "runtime", "logs", "mihomo.log"), Kind: CreatableFile, Mode: 0o640, GroupOwned: true,
			Purpose: "mihomo output tailed by the diagnostics view"},

		// Written by the root gateway and deleted when it stops. state.json is the
		// one the control service reads on every status query: a root:root 0600
		// copy of it is what leaves the Web UI with an empty gateway status.
		{Path: filepath.Join(StateRoot, "runtime", "state.json"), Kind: OptionalFile, Mode: 0o640, GroupOwned: true,
			Purpose: "gateway runtime state read by the control service for every status query"},
		{Path: filepath.Join(StateRoot, "runtime", "dnsmasq.conf"), Kind: OptionalFile, Mode: 0o640, GroupOwned: true,
			Purpose: "generated dnsmasq configuration"},
		{Path: filepath.Join(StateRoot, "runtime", "nftables.rules"), Kind: OptionalFile, Mode: 0o640, GroupOwned: true,
			Purpose: "generated nftables ruleset"},
		{Path: filepath.Join(StateRoot, "runtime", "device-policy.applied.json"), Kind: OptionalFile, Mode: 0o640, GroupOwned: true,
			Purpose: "applied device policy snapshot read by the control service"},
	}
}

// Check reports layout problems without changing anything. Unlike Apply it does
// not need root, so an operator can inspect a host before repairing it.
func Check() ([]string, error) {
	gid, err := serviceGroupID()
	if err != nil {
		return nil, err
	}
	problems := []string{}
	for _, entry := range Entries() {
		info, err := os.Lstat(entry.Path)
		if os.IsNotExist(err) {
			if entry.Kind == OptionalFile {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s is missing — %s", entry.Path, entry.Purpose))
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s cannot be inspected: %v", entry.Path, err))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			problems = append(problems, fmt.Sprintf("%s is a symlink and must be a real %s", entry.Path, kindNoun(entry.Kind)))
			continue
		}
		if entry.Kind == Directory && !info.IsDir() {
			problems = append(problems, fmt.Sprintf("%s must be a directory", entry.Path))
			continue
		}
		if entry.Kind != Directory && !info.Mode().IsRegular() {
			problems = append(problems, fmt.Sprintf("%s must be a regular file", entry.Path))
			continue
		}
		wantGID := 0
		if entry.GroupOwned {
			wantGID = gid
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && (stat.Uid != 0 || int(stat.Gid) != wantGID) {
			problems = append(problems, fmt.Sprintf("%s is owned by %d:%d, want 0:%d", entry.Path, stat.Uid, stat.Gid, wantGID))
		}
		if modeBits(info.Mode()) != modeBits(entry.Mode) {
			problems = append(problems, fmt.Sprintf("%s has mode %04o, want %04o", entry.Path, modeBits(info.Mode()), modeBits(entry.Mode)))
		}
	}
	return problems, nil
}

func kindNoun(kind Kind) string {
	if kind == Directory {
		return "directory"
	}
	return "file"
}

// Change is one repair Apply made, or one problem it could only report.
type Change struct {
	Path   string
	Action string
	Detail string
}

// Result reports what Apply did. Missing lists paths it deliberately did not
// fabricate, so the caller can decide whether that is an error.
type Result struct {
	Changes  []Change
	Missing  []Entry
	Verified int
}

// Apply creates missing directories and creatable files, then repairs ownership
// and modes to match the layout. It must run as root.
func Apply(out io.Writer) (Result, error) {
	if os.Geteuid() != 0 {
		return Result{}, fmt.Errorf("repairing OpenSurge paths requires root")
	}
	gid, err := serviceGroupID()
	if err != nil {
		return Result{}, err
	}
	result := Result{}
	for _, entry := range Entries() {
		if err := applyEntry(entry, gid, &result); err != nil {
			return result, fmt.Errorf("%s: %w", entry.Path, err)
		}
	}
	sort.SliceStable(result.Changes, func(i, j int) bool { return result.Changes[i].Path < result.Changes[j].Path })
	for _, change := range result.Changes {
		_, _ = fmt.Fprintf(out, "  %s %s%s\n", change.Action, change.Path, change.Detail)
	}
	return result, nil
}

func serviceGroupID() (int, error) {
	group, err := user.LookupGroup(ServiceGroup)
	if err != nil {
		return 0, fmt.Errorf("group %q does not exist; reinstall the package to create it: %w", ServiceGroup, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return 0, fmt.Errorf("group %q has an unusable id %q", ServiceGroup, group.Gid)
	}
	return gid, nil
}

func applyEntry(entry Entry, gid int, result *Result) error {
	info, err := os.Lstat(entry.Path)
	switch {
	case os.IsNotExist(err):
		if entry.Kind == OptionalFile {
			return nil
		}
		created, err := create(entry)
		if err != nil {
			return err
		}
		if !created {
			result.Missing = append(result.Missing, entry)
			return nil
		}
		result.Changes = append(result.Changes, Change{Path: entry.Path, Action: "created", Detail: ""})
		info, err = os.Lstat(entry.Path)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		// A symlink here would let the target escape the reviewed layout, and
		// silently retargeting it is worse than refusing.
		return fmt.Errorf("is a symlink; remove it and re-run so the real path can be restored")
	case entry.Kind == Directory && !info.IsDir():
		return fmt.Errorf("must be a directory but is a file")
	case entry.Kind != Directory && !info.Mode().IsRegular():
		return fmt.Errorf("must be a regular file")
	}
	return repair(entry, info, gid, result)
}

func create(entry Entry) (bool, error) {
	switch entry.Kind {
	case Directory:
		if err := os.MkdirAll(entry.Path, entry.Mode.Perm()); err != nil {
			return false, err
		}
		return true, nil
	case CreatableFile:
		file, err := os.OpenFile(entry.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, entry.Mode.Perm())
		if err != nil {
			return false, err
		}
		return true, file.Close()
	default:
		return false, nil
	}
}

func repair(entry Entry, info os.FileInfo, gid int, result *Result) error {
	repaired := false
	wantGID := 0
	if entry.GroupOwned {
		wantGID = gid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && (stat.Uid != 0 || int(stat.Gid) != wantGID) {
		if err := os.Chown(entry.Path, 0, wantGID); err != nil {
			return err
		}
		result.Changes = append(result.Changes, Change{
			Path: entry.Path, Action: "reowned",
			Detail: fmt.Sprintf(" (%d:%d → 0:%d)", stat.Uid, stat.Gid, wantGID),
		})
		repaired = true
	}
	// Compare the full mode, not just Perm: dropping setgid on a directory is the
	// exact regression that leaves new files unreadable by the control service.
	if wantMode := entry.Mode; modeBits(info.Mode()) != modeBits(wantMode) {
		if err := os.Chmod(entry.Path, wantMode); err != nil {
			return err
		}
		result.Changes = append(result.Changes, Change{
			Path: entry.Path, Action: "remoded",
			Detail: fmt.Sprintf(" (%04o → %04o)", modeBits(info.Mode()), modeBits(wantMode)),
		})
		repaired = true
	}
	if !repaired {
		result.Verified++
	}
	return nil
}

// modeBits reduces a FileMode to the permission and setuid/setgid/sticky bits in
// the same 4-digit shape chmod uses, so comparisons and messages line up.
func modeBits(mode os.FileMode) uint32 {
	bits := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		bits |= syscall.S_ISUID
	}
	if mode&os.ModeSetgid != 0 {
		bits |= syscall.S_ISGID
	}
	if mode&os.ModeSticky != 0 {
		bits |= syscall.S_ISVTX
	}
	return bits
}
