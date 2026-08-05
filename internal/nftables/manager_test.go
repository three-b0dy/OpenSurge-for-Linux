package nftables

import (
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/runtime"
)

type recordingRunner struct {
	calls     [][]string
	output    []byte
	runErr    error
	outputErr error
}

func (r *recordingRunner) Run(name string, args ...string) error {
	r.call(name, args...)
	return r.runErr
}

func (r *recordingRunner) Output(name string, args ...string) ([]byte, error) {
	r.call(name, args...)
	return r.output, r.outputErr
}

func (r *recordingRunner) call(name string, args ...string) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
}

func (r *recordingRunner) Args() []string {
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[0]
}

func nftTestConfig() config.Config {
	return config.Default()
}

func nftTestPaths() runtime.Paths {
	return runtime.Paths{NftablesRules: "/tmp/opensurge-nftables.rules"}
}

func TestUnloadDeletesOnlyNamedTable(t *testing.T) {
	runner := &recordingRunner{}
	manager := New(nftTestConfig(), nftTestPaths(), runner)

	if err := manager.Unload(); err != nil {
		t.Fatalf("Unload() error = %v", err)
	}
	want := []string{"nft", "delete", "table", "inet", "opensurge"}
	if got := runner.Args(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Unload() args = %#v, want %#v", got, want)
	}
}

func TestManagerLoadChecksThenLoadsRuleset(t *testing.T) {
	runner := &recordingRunner{}
	paths := nftTestPaths()
	manager := New(nftTestConfig(), paths, runner)

	if err := manager.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := [][]string{
		{"nft", "--check", "--file", paths.NftablesRules},
		{"nft", "--file", paths.NftablesRules},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("Load() calls = %#v, want %#v", runner.calls, want)
	}
}

func TestManagerLoadStopsAfterCheckFailure(t *testing.T) {
	runner := &recordingRunner{runErr: errors.New("invalid ruleset")}
	manager := New(nftTestConfig(), nftTestPaths(), runner)

	if err := manager.Load(); err == nil {
		t.Fatal("Load() error = nil, want check failure")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("Load() calls after check failure = %#v, want one call", runner.calls)
	}
}

func TestManagerLoadedListsNamedTable(t *testing.T) {
	runner := &recordingRunner{output: []byte("table inet opensurge {\n}")}
	manager := New(nftTestConfig(), nftTestPaths(), runner)

	loaded, err := manager.Loaded()
	if err != nil {
		t.Fatalf("Loaded() error = %v", err)
	}
	if !loaded {
		t.Fatal("Loaded() = false, want true")
	}
	want := []string{"nft", "list", "table", "inet", "opensurge"}
	if got := runner.Args(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Loaded() args = %#v, want %#v", got, want)
	}
}

// The control service lists the table without CAP_NET_ADMIN on every status
// read. That has to stay distinguishable from a missing or broken ruleset.
func TestUnprivilegedListFailureIsDistinguishableFromAMissingTable(t *testing.T) {
	unprivileged := errors.New("Operation not permitted (you must be root)\nnetlink: Error: cache initialization failed: Operation not permitted")
	if !IsUnprivileged(unprivileged) {
		t.Error("IsUnprivileged() = false for an nft privilege failure")
	}
	if IsUnprivileged(errors.New("Error: table opensurge does not exist")) {
		t.Error("IsUnprivileged() = true for a missing table")
	}
	if IsUnprivileged(nil) {
		t.Error("IsUnprivileged(nil) = true")
	}

	manager := New(nftTestConfig(), nftTestPaths(), &recordingRunner{outputErr: unprivileged})
	loaded, err := manager.Loaded()
	if err == nil || !IsUnprivileged(err) {
		t.Fatalf("Loaded() error = %v, want a wrapped privilege failure", err)
	}
	if loaded {
		t.Error("Loaded() = true despite failing to list the table")
	}
}

// exec.Cmd.Output reports only "exit status N"; the reason nft failed is in
// ExitError.Stderr. Matching on Error() alone made every real nft failure
// indistinguishable, so an unprivileged status read looked like a fault.
func TestCommandFailureClassificationReadsCapturedStderr(t *testing.T) {
	exitErr := &exec.ExitError{
		ProcessState: &os.ProcessState{},
		Stderr:       []byte("Operation not permitted (you must be root)"),
	}
	if got := exitErr.Error(); strings.Contains(strings.ToLower(got), "not permitted") {
		t.Skipf("this Go version already surfaces stderr in Error(): %q", got)
	}
	if !IsUnprivileged(exitErr) {
		t.Error("IsUnprivileged() ignored the captured stderr")
	}
	missing := &exec.ExitError{
		ProcessState: &os.ProcessState{},
		Stderr:       []byte("Error: No such file or directory"),
	}
	if !isMissingTableError(missing) {
		t.Error("isMissingTableError() ignored the captured stderr")
	}
	if isMissingTableError(exitErr) {
		t.Error("a privilege failure was classified as a missing table")
	}
}

func TestManagerUnloadTreatsMissingTableAsSuccess(t *testing.T) {
	runner := &recordingRunner{runErr: errors.New("Error: table opensurge does not exist")}
	manager := New(nftTestConfig(), nftTestPaths(), runner)

	if err := manager.Unload(); err != nil {
		t.Fatalf("Unload() missing table error = %v", err)
	}
}

func TestWriteRulesetUses0640(t *testing.T) {
	paths := nftTestPaths()
	paths.NftablesRules = t.TempDir() + "/nftables.rules"
	manager := New(nftTestConfig(), paths, &recordingRunner{})

	if err := manager.WriteRuleset(); err != nil {
		t.Fatalf("WriteRuleset() error = %v", err)
	}
	info, err := os.Stat(paths.NftablesRules)
	if err != nil {
		t.Fatalf("stat ruleset: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("ruleset mode = %04o, want 0640", got)
	}
}
