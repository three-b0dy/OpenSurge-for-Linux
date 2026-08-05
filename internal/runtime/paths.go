package runtime

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
)

type Paths struct {
	Dir                 string
	LogDir              string
	StateFile           string
	DNSMasqConf         string
	DNSMasqPIDFile      string
	DNSMasqLog          string
	MihomoConfig        string
	MihomoLog           string
	NftablesRules       string
	LeaseFile           string
	DevicePolicyApplied string
}

func NewPaths(cfg config.Config) Paths {
	dir := cfg.Runtime.Dir
	return Paths{
		Dir:                 dir,
		LogDir:              filepath.Join(dir, "logs"),
		StateFile:           filepath.Join(dir, "state.json"),
		DNSMasqConf:         filepath.Join(dir, "dnsmasq.conf"),
		DNSMasqPIDFile:      filepath.Join(dir, "dnsmasq.pid"),
		DNSMasqLog:          filepath.Join(dir, "logs", "dnsmasq.log"),
		MihomoConfig:        cfg.Mihomo.Config,
		MihomoLog:           filepath.Join(dir, "logs", "mihomo.log"),
		NftablesRules:       filepath.Join(dir, "nftables.rules"),
		LeaseFile:           filepath.Join(dir, "dnsmasq.leases"),
		DevicePolicyApplied: filepath.Join(dir, "device-policy.applied.json"),
	}
}

func Ensure(paths Paths) error {
	if err := os.MkdirAll(paths.Dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.LogDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.MihomoConfig), 0o755); err != nil {
		return err
	}
	// The control service reads state and logs from these directories. Carrying
	// the setgid bit is what keeps that working: the gateway runs without
	// CAP_CHOWN, so files it creates can only reach the opensurge group by
	// inheriting it from the directory.
	for _, dir := range []string{paths.Dir, paths.LogDir} {
		if err := grantServiceGroupDir(dir); err != nil {
			return err
		}
	}
	// dnsmasq and mihomo are launched by the root gateway, which would create
	// these logs as root:root under its umask. The unprivileged control service
	// tails them for the diagnostics view, so pre-create them with the service
	// group: an existing file keeps its mode when the launcher reopens it.
	for _, logPath := range []string{paths.DNSMasqLog, paths.MihomoLog} {
		if err := ensureServiceReadableLog(logPath); err != nil {
			return err
		}
	}
	return nil
}

// grantServiceGroupDir marks a directory setgid to the opensurge group, so files
// the root gateway creates inside it are readable by the control service without
// any per-file chown. Best-effort: the packaged postinst establishes this, and a
// gateway without CAP_CHOWN simply leaves the existing ownership in place.
func grantServiceGroupDir(dir string) error {
	if err := grantServiceGroup(dir); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSetgid != 0 {
		return nil
	}
	if err := os.Chmod(dir, info.Mode().Perm()|os.ModeSetgid); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	return nil
}

func ensureServiceReadableLog(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o640); err != nil {
		return err
	}
	return grantServiceGroup(path)
}
