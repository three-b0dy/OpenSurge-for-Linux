package nftables

import (
	"errors"
	"os"
	"reflect"
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
