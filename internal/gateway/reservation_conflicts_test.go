package gateway

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestProbeReservationIPConflictUsesLinuxIPNeighborJSON(t *testing.T) {
	runner := &policyRuleRunner{output: []byte(`[{"dst":"192.168.1.101","lladdr":"AA:BB:CC:DD:EE:01","state":["REACHABLE"]}]`)}
	if err := probeReservationIPConflictWithRunner(context.Background(), runner, "192.168.1.101", "aa:bb:cc:dd:ee:01"); err != nil {
		t.Fatalf("probeReservationIPConflictWithRunner() error = %v", err)
	}
	want := [][]string{{"ip", "-j", "neigh", "show", "192.168.1.101"}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("runner calls = %#v, want %#v", runner.calls, want)
	}
}

func TestProbeReservationIPConflictRejectsDifferentLinuxNeighborMAC(t *testing.T) {
	runner := &policyRuleRunner{output: []byte(`[{"dst":"192.168.1.101","lladdr":"AA:BB:CC:DD:EE:02","state":["REACHABLE"]}]`)}
	err := probeReservationIPConflictWithRunner(context.Background(), runner, "192.168.1.101", "aa:bb:cc:dd:ee:01")
	if err == nil || !strings.Contains(err.Error(), "already present") {
		t.Fatalf("probeReservationIPConflictWithRunner() error = %v", err)
	}
}

func TestProbeReservationIPConflictTreatsMissingNeighborAsVacant(t *testing.T) {
	runner := &policyRuleRunner{output: []byte(`[]`)}
	if err := probeReservationIPConflictWithRunner(context.Background(), runner, "192.168.1.101", "aa:bb:cc:dd:ee:01"); err != nil {
		t.Fatalf("probeReservationIPConflictWithRunner() error = %v, want nil", err)
	}
}
