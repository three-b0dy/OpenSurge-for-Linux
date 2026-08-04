package linuxnet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

const maxInterfaceNameLength = 15

type IPRoute struct {
	run CommandRunner
}

func NewIPRoute(run CommandRunner) *IPRoute {
	return &IPRoute{run: run}
}

func (r *IPRoute) Addresses(ctx context.Context, name string) ([]netip.Prefix, error) {
	if err := validateInterfaceName(name); err != nil {
		return nil, err
	}
	if r == nil || r.run == nil {
		return nil, fmt.Errorf("iproute command runner is required")
	}
	output, err := r.run(ctx, "ip", "-j", "addr", "show", "dev", name)
	if err != nil {
		return nil, fmt.Errorf("inspect addresses for interface %s: %w", name, err)
	}
	return parseAddresses(output)
}

func (r *IPRoute) Neighbors(ctx context.Context, name string) ([]Neighbor, error) {
	if err := validateInterfaceName(name); err != nil {
		return nil, err
	}
	if r == nil || r.run == nil {
		return nil, fmt.Errorf("iproute command runner is required")
	}
	output, err := r.run(ctx, "ip", "-j", "neigh", "show", "dev", name)
	if err != nil {
		return nil, fmt.Errorf("inspect neighbors for interface %s: %w", name, err)
	}
	return parseNeighbors(output)
}

type addrLink struct {
	AddrInfo []addrInfo `json:"addr_info"`
}

type addrInfo struct {
	Family    string `json:"family"`
	Local     string `json:"local"`
	PrefixLen int    `json:"prefixlen"`
}

func parseAddresses(data []byte) ([]netip.Prefix, error) {
	var links []addrLink
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, fmt.Errorf("parse ip address JSON: %w", err)
	}

	addresses := make([]netip.Prefix, 0)
	for _, link := range links {
		for _, info := range link.AddrInfo {
			if info.Family != "inet" || info.PrefixLen < 0 || info.PrefixLen > 32 {
				continue
			}
			address, err := netip.ParseAddr(info.Local)
			if err != nil || !address.Is4() {
				continue
			}
			addresses = append(addresses, netip.PrefixFrom(address, info.PrefixLen))
		}
	}
	return addresses, nil
}

type neighborInfo struct {
	Destination string          `json:"dst"`
	LinkAddress string          `json:"lladdr"`
	State       json.RawMessage `json:"state"`
}

func parseNeighbors(data []byte) ([]Neighbor, error) {
	var entries []neighborInfo
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse ip neighbor JSON: %w", err)
	}

	neighbors := make([]Neighbor, 0)
	for _, entry := range entries {
		address, err := netip.ParseAddr(entry.Destination)
		if err != nil || !address.Is4() {
			continue
		}
		mac, err := net.ParseMAC(entry.LinkAddress)
		if err != nil {
			continue
		}
		state := ""
		if len(entry.State) > 0 {
			var states []string
			if err := json.Unmarshal(entry.State, &states); err == nil {
				state = strings.Join(states, " ")
			} else {
				_ = json.Unmarshal(entry.State, &state)
			}
		}
		neighbors = append(neighbors, Neighbor{IPv4: address, MAC: strings.ToLower(mac.String()), State: state})
	}
	return neighbors, nil
}

func validateInterfaceName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid interface name %q", name)
	}
	if len(name) > maxInterfaceNameLength {
		return fmt.Errorf("invalid interface name %q: exceeds %d bytes", name, maxInterfaceNameLength)
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("invalid interface name %q", name)
	}
	return nil
}
