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
)

type ipRule struct {
	Priority *int `json:"priority"`
}

// DetectPolicyRouteConflict rejects an existing ip-rule priority in the
// range reserved by mihomo TUN auto-route/auto-redirect.
func DetectPolicyRouteConflict(ctx context.Context, runner process.Runner) error {
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
		if *rule.Priority >= mihomoPolicyRulePriorityMin && *rule.Priority <= mihomoPolicyRulePriorityMax {
			return fmt.Errorf("ip rule priority %d conflicts with mihomo reserved priority range %d-%d", *rule.Priority, mihomoPolicyRulePriorityMin, mihomoPolicyRulePriorityMax)
		}
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
