package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/process"
)

const (
	mihomoPolicyRulePriorityMin = 9000
	mihomoPolicyRulePriorityMax = 9999
	// mihomoPolicyRouteTable is the routing table mihomo's TUN auto-route
	// installs its rules against. `ip -j rule show` reports the table as a
	// string, and an unnamed table appears as its numeric id.
	mihomoPolicyRouteTable = "2022"
)

type ipRule struct {
	Priority *int   `json:"priority"`
	Table    string `json:"table"`
	IIF      string `json:"iif"`
	Goto     *int   `json:"goto"`
	// keys records which fields the rule actually carried. `ip -j rule show`
	// renders valueless attributes as explicit nulls (`"nop": null`), which no
	// typed field can distinguish from an absent key.
	keys map[string]bool
}

func (r *ipRule) UnmarshalJSON(data []byte) error {
	type plain ipRule
	var typed plain
	if err := json.Unmarshal(data, &typed); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = ipRule(typed)
	r.keys = make(map[string]bool, len(raw))
	for key := range raw {
		r.keys[key] = true
	}
	return nil
}

// ownedByRunningTUN reports whether a rule in the reserved range belongs to the
// TUN device this gateway operates, rather than to a foreign tool.
//
// mihomo's auto-route installs a fixed set of rules. Most carry its own routing
// table or the TUN interface name, but two carry neither: a `suppress_prefixlen`
// DNS rule pointing at main, and a terminal `nop` that its goto jumps to. Both
// are recognised by shape, so a genuinely foreign rule in the range is still
// reported.
func (r ipRule) ownedByRunningTUN(tunDevice string) bool {
	switch {
	case r.Table == mihomoPolicyRouteTable:
		return true
	case tunDevice != "" && r.IIF == tunDevice:
		return true
	case r.Goto != nil, r.keys["goto"]:
		return true
	case r.keys["suppress_prefixlen"]:
		return true
	case r.keys["nop"]:
		return true
	default:
		return false
	}
}

// DetectPolicyRouteConflict rejects an existing ip-rule priority in the range
// reserved by mihomo TUN auto-route/auto-redirect.
//
// tunDevice names the TUN interface this gateway owns; rules belonging to it are
// not conflicts. Reload validates a candidate while the current gateway is still
// running, so without that exemption mihomo's own rules would be reported as a
// foreign conflict and every reload after TUN came up would fail.
func DetectPolicyRouteConflict(ctx context.Context, runner process.Runner, tunDevice string) error {
	if runner == nil {
		return fmt.Errorf("ip rule runner is required")
	}
	output, err := runner.Output(ctx, "ip", "-j", "rule", "show")
	if err != nil {
		return fmt.Errorf("ip -j rule show failed: %w", err)
	}
	var rules []ipRule
	if err := json.Unmarshal(output, &rules); err != nil {
		return fmt.Errorf("parse ip -j rule show output: %w", err)
	}
	for _, rule := range rules {
		if rule.Priority == nil {
			continue
		}
		if *rule.Priority < mihomoPolicyRulePriorityMin || *rule.Priority > mihomoPolicyRulePriorityMax {
			continue
		}
		if rule.ownedByRunningTUN(tunDevice) {
			continue
		}
		return fmt.Errorf("ip rule priority %d conflicts with mihomo reserved priority range %d-%d", *rule.Priority, mihomoPolicyRulePriorityMin, mihomoPolicyRulePriorityMax)
	}
	return nil
}

// checkReservationConflicts is intentionally limited to same-WiFi DHCP
// takeover. Isolated lab LANs have no pre-existing neighbour population, while
// a real shared L2 needs a final live check in addition to declarative
// protected_ipv4 validation.
func (m Manager) checkReservationConflicts(deps gatewayDeps) error {
	if m.cfg.Gateway.Mode != config.GatewayModeSameWiFiDHCP || m.cfg.DevicePolicy.Bundle == nil {
		return nil
	}
	probe := deps.probeReservationIP
	if probe == nil {
		probe = probeReservationIPConflict
	}
	for _, reservation := range m.cfg.DevicePolicy.Bundle.Compiled.Reservations {
		if err := probe(reservation.IPv4, reservation.MAC); err != nil {
			return err
		}
	}
	return nil
}

type ipNeighbor struct {
	LinkAddress string `json:"lladdr"`
}

// probeReservationIPConflict checks the Linux neighbour table and treats a
// different observed MAC as a hard conflict. No neighbour is not a proof of
// vacancy (sleeping hosts and firewalls exist), so it remains non-fatal.
func probeReservationIPConflict(ip string, expectedMAC string) error {
	ctx, cancel := context.WithTimeout(context.Background(), process.DefaultCommandTimeout)
	defer cancel()
	return probeReservationIPConflictWithRunner(ctx, process.NewRunner(), ip, expectedMAC)
}

func probeReservationIPConflictWithRunner(ctx context.Context, runner process.Runner, ip string, expectedMAC string) error {
	if runner == nil {
		return fmt.Errorf("neighbor probe runner is required")
	}
	output, err := runner.Output(ctx, "ip", "-j", "neigh", "show", ip)
	if err != nil {
		return nil
	}
	expected, err := net.ParseMAC(expectedMAC)
	if err != nil || len(expected) != 6 {
		return nil
	}

	var neighbors []ipNeighbor
	if err := json.Unmarshal(output, &neighbors); err != nil {
		return nil
	}
	for _, neighbor := range neighbors {
		observed, err := net.ParseMAC(strings.TrimSpace(neighbor.LinkAddress))
		if err != nil || len(observed) != 6 {
			continue
		}
		if !strings.EqualFold(observed.String(), expected.String()) {
			return &reservationConflictError{ip: ip, observedMAC: observed.String(), expectedMAC: expected.String()}
		}
		return nil
	}
	return nil
}

type reservationConflictError struct {
	ip          string
	observedMAC string
	expectedMAC string
}

func (e *reservationConflictError) Error() string {
	return "reserved IPv4 " + e.ip + " is already present at MAC " + e.observedMAC + "; expected " + e.expectedMAC
}
