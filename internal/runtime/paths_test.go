package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
)

// The root gateway launches dnsmasq and mihomo, but the unprivileged control
// service tails their logs for the diagnostics view.
func TestEnsureCreatesGroupReadableProcessLogs(t *testing.T) {
	dir := t.TempDir()
	paths := NewPaths(config.Config{
		Runtime: config.RuntimeConfig{Dir: dir},
		Mihomo:  config.MihomoConfig{Config: filepath.Join(dir, "mihomo.yaml")},
	})
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	for _, logPath := range []string{paths.DNSMasqLog, paths.MihomoLog} {
		info, err := os.Stat(logPath)
		if err != nil {
			t.Fatalf("log %s: %v", logPath, err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Errorf("log %s mode = %o, want 640", logPath, info.Mode().Perm())
		}
	}
}

func TestNewPathsUsesNftablesRulesPath(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	paths := NewPaths(cfg)
	if got, want := paths.NftablesRules, filepath.Join(cfg.Runtime.Dir, "nftables.rules"); got != want {
		t.Fatalf("NftablesRules = %q, want %q", got, want)
	}
}
