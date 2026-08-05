package nftables

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/process"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/runtime"
)

// CommandRunner is the command boundary used by Manager. Keeping it
// injectable makes command arguments testable without invoking a shell or nft.
type CommandRunner interface {
	Run(name string, args ...string) error
	Output(name string, args ...string) ([]byte, error)
}

type processCommandRunner struct{}

func (processCommandRunner) Run(name string, args ...string) error {
	return process.Run(name, args...)
}

func (processCommandRunner) Output(name string, args ...string) ([]byte, error) {
	return process.Output(name, args...)
}

type Manager struct {
	cfg    config.Config
	paths  runtime.Paths
	runner CommandRunner
}

func New(cfg config.Config, paths runtime.Paths, runner CommandRunner) Manager {
	if runner == nil {
		runner = processCommandRunner{}
	}
	return Manager{cfg: cfg, paths: paths, runner: runner}
}

func (m Manager) Check() error {
	if _, err := exec.LookPath("nft"); err != nil {
		return fmt.Errorf("nft not found in PATH")
	}
	return nil
}

func (m Manager) WriteRuleset() error {
	rendered, err := RenderRuleset(m.cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.paths.NftablesRules, []byte(rendered), 0o640); err != nil {
		return err
	}
	return os.Chmod(m.paths.NftablesRules, 0o640)
}

func (m Manager) Load() error {
	if err := m.runner.Run("nft", "--check", "--file", m.paths.NftablesRules); err != nil {
		return fmt.Errorf("check nftables ruleset: %w", err)
	}
	if err := m.runner.Run("nft", "--file", m.paths.NftablesRules); err != nil {
		return fmt.Errorf("load nftables ruleset: %w", err)
	}
	return nil
}

func (m Manager) Loaded() (bool, error) {
	_, err := m.runner.Output("nft", "list", "table", "inet", m.cfg.Nftables.Table)
	if err == nil {
		return true, nil
	}
	if isMissingTableError(err) {
		return false, nil
	}
	return false, fmt.Errorf("list nftables table: %w", err)
}

func (m Manager) Unload() error {
	if err := m.runner.Run("nft", "delete", "table", "inet", m.cfg.Nftables.Table); err != nil && !isMissingTableError(err) {
		return fmt.Errorf("delete nftables table: %w", err)
	}
	return nil
}

// ErrUnprivileged reports that the caller cannot query nftables at all, as
// opposed to querying it and learning the table is absent. The control service
// runs without CAP_NET_ADMIN, so it hits this on every status read and must not
// mistake it for a missing or broken ruleset.
var ErrUnprivileged = errors.New("listing nftables requires root or CAP_NET_ADMIN")

// IsUnprivileged reports whether err is a privilege failure from nft.
func IsUnprivileged(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnprivileged) {
		return true
	}
	return matchesCommandOutput(err, "operation not permitted", "must be root", "permission denied")
}

// matchesCommandOutput looks for a marker in the error text and, for a failed
// command, in its captured standard error. exec.Cmd.Output reports only
// "exit status N" from Error(); the reason nft failed is in ExitError.Stderr.
func matchesCommandOutput(err error, markers ...string) bool {
	haystack := strings.ToLower(err.Error())
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		haystack += "\n" + strings.ToLower(string(exitErr.Stderr))
	}
	for _, marker := range markers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

func isMissingTableError(err error) bool {
	if err == nil {
		return false
	}
	// A privilege failure is not a missing table: without this guard an
	// unprivileged caller would conclude the ruleset had been unloaded.
	if IsUnprivileged(err) {
		return false
	}
	return matchesCommandOutput(err, "does not exist", "no such file or directory", "not found")
}
