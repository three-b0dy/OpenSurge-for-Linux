package linuxnet

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
)

type recordingRunner struct {
	output []byte
	calls  [][]string
}

func (r *recordingRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.output, nil
}

func TestAddressesParsesIPv4Prefixes(t *testing.T) {
	runner := &recordingRunner{output: []byte(`[{"ifname":"lan0","addr_info":[{"family":"inet","local":"192.168.50.1","prefixlen":24},{"family":"inet6","local":"fe80::1","prefixlen":64}]}]`)}
	got, err := NewIPRoute(runner.run).Addresses(context.Background(), "lan0")
	if err != nil {
		t.Fatalf("Addresses() error = %v", err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("192.168.50.1/24")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Addresses() = %v, want %v", got, want)
	}
	if wantCall := []string{"ip", "-j", "addr", "show", "dev", "lan0"}; !reflect.DeepEqual(runner.calls, [][]string{wantCall}) {
		t.Fatalf("runner calls = %v, want %v", runner.calls, [][]string{wantCall})
	}
}

func TestNeighborsParsesIPv4Entries(t *testing.T) {
	runner := &recordingRunner{output: []byte(`[{"dst":"192.168.50.101","lladdr":"AA:BB:CC:DD:EE:01","state":["REACHABLE"]},{"dst":"fe80::1","lladdr":"AA:BB:CC:DD:EE:02","state":["STALE"]},{"dst":"192.168.50.102","state":["FAILED"]}]`)}
	got, err := NewIPRoute(runner.run).Neighbors(context.Background(), "lan0")
	if err != nil {
		t.Fatalf("Neighbors() error = %v", err)
	}
	want := []Neighbor{{IPv4: netip.MustParseAddr("192.168.50.101"), MAC: "aa:bb:cc:dd:ee:01", State: "REACHABLE"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Neighbors() = %v, want %v", got, want)
	}
	if wantCall := []string{"ip", "-j", "neigh", "show", "dev", "lan0"}; !reflect.DeepEqual(runner.calls, [][]string{wantCall}) {
		t.Fatalf("runner calls = %v, want %v", runner.calls, [][]string{wantCall})
	}
}

func TestIPRouteRejectsUnsafeInterfaceNameBeforeRunningCommand(t *testing.T) {
	runner := &recordingRunner{output: []byte(`[]`)}
	for _, name := range []string{"", "lan0;touch /tmp/pwned", " lan0", "lan0 "} {
		if _, err := NewIPRoute(runner.run).Addresses(context.Background(), name); err == nil {
			t.Fatalf("Addresses(%q) succeeded", name)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %v, want none", runner.calls)
	}
}

func TestIPRouteReturnsRunnerError(t *testing.T) {
	wantErr := errors.New("ip unavailable")
	run := func(context.Context, string, ...string) ([]byte, error) { return nil, wantErr }
	_, err := NewIPRoute(run).Addresses(context.Background(), "lan0")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Addresses() error = %v, want %v", err, wantErr)
	}
}

func TestIPRouteRejectsMalformedJSON(t *testing.T) {
	run := func(context.Context, string, ...string) ([]byte, error) {
		return bytes.TrimSpace([]byte("not-json")), nil
	}
	if _, err := NewIPRoute(run).Addresses(context.Background(), "lan0"); err == nil {
		t.Fatal("Addresses() succeeded with malformed JSON")
	}
}
