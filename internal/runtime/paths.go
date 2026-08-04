package runtime

import (
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
	return os.MkdirAll(filepath.Dir(paths.MihomoConfig), 0o755)
}
