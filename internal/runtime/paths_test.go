package runtime

import (
	"path/filepath"
	"testing"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
)

func TestNewPathsUsesNftablesRulesPath(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.Dir = t.TempDir()
	paths := NewPaths(cfg)
	if got, want := paths.NftablesRules, filepath.Join(cfg.Runtime.Dir, "nftables.rules"); got != want {
		t.Fatalf("NftablesRules = %q, want %q", got, want)
	}
}
