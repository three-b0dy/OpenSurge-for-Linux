package gateway

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/linuxnet"
)

const mihomoDNSPort = 1053

// ValidateTopology checks the host-network facts that make each supported
// gateway mode safe to start. It deliberately receives an inspector so unit
// tests and future Linux lifecycle code can share the same command boundary.
func ValidateTopology(ctx context.Context, cfg config.Config, inspector linuxnet.InterfaceInspector) error {
	if inspector == nil {
		return fmt.Errorf("topology interface inspector is required")
	}

	gatewayInterface := strings.TrimSpace(cfg.Gateway.Interface)
	upstreamInterface := strings.TrimSpace(cfg.Gateway.UpstreamInterface)
	sameInterface := gatewayInterface == upstreamInterface
	switch cfg.Gateway.Mode {
	case config.GatewayModeSameLAN:
		if !sameInterface {
			return fmt.Errorf("gateway.mode %s requires gateway and upstream interfaces to match", cfg.Gateway.Mode)
		}
		if cfg.DHCP.Enabled {
			return fmt.Errorf("gateway.mode same_lan requires dhcp.enabled: false")
		}
	case config.GatewayModeSameWiFiDHCP:
		if !sameInterface {
			return fmt.Errorf("gateway.mode %s requires gateway and upstream interfaces to match", cfg.Gateway.Mode)
		}
		if !cfg.DHCP.Enabled {
			return fmt.Errorf("gateway.mode same_wifi_dhcp requires dhcp.enabled: true")
		}
		if !cfg.Gateway.RouterDHCPDisabledConfirmed {
			return fmt.Errorf("gateway.mode same_wifi_dhcp requires gateway.router_dhcp_disabled_confirmed: true")
		}
	case config.GatewayModeIsolatedLAN:
		if sameInterface {
			return fmt.Errorf("isolated_lan requires separate downstream and upstream interfaces")
		}
	default:
		return fmt.Errorf("unsupported gateway mode %q", cfg.Gateway.Mode)
	}

	if err := validateListenerPortConflicts(cfg); err != nil {
		return err
	}

	lanIP, err := netip.ParseAddr(strings.TrimSpace(cfg.Gateway.LANIP))
	if err != nil || !lanIP.Is4() {
		return fmt.Errorf("gateway LAN IP %s is not IPv4", cfg.Gateway.LANIP)
	}
	gatewayPrefixes, err := inspector.Addresses(ctx, gatewayInterface)
	if err != nil {
		return fmt.Errorf("inspect gateway interface %s addresses: %w", gatewayInterface, err)
	}
	lanPrefix, ok := configuredLANPrefix(gatewayPrefixes, lanIP)
	if !ok {
		return fmt.Errorf("LAN IP %s is not configured on interface %s", cfg.Gateway.LANIP, gatewayInterface)
	}

	if cfg.DHCP.Enabled {
		if err := validateDHCPPool(cfg, lanPrefix, lanIP); err != nil {
			return err
		}
	}

	if cfg.Gateway.Mode == config.GatewayModeIsolatedLAN {
		upstreamPrefixes, err := inspector.Addresses(ctx, upstreamInterface)
		if err != nil {
			return fmt.Errorf("inspect upstream interface %s addresses: %w", upstreamInterface, err)
		}
		for _, upstreamPrefix := range upstreamPrefixes {
			if !upstreamPrefix.Addr().Is4() {
				continue
			}
			upstreamPrefix = upstreamPrefix.Masked()
			if lanPrefix.Overlaps(upstreamPrefix) {
				return fmt.Errorf("LAN prefix %s overlaps upstream interface %s prefix %s", lanPrefix, upstreamInterface, upstreamPrefix)
			}
		}
	}

	return nil
}

func configuredLANPrefix(prefixes []netip.Prefix, lanIP netip.Addr) (netip.Prefix, bool) {
	for _, prefix := range prefixes {
		if !prefix.Addr().Is4() || prefix.Addr() != lanIP {
			continue
		}
		return prefix.Masked(), true
	}
	return netip.Prefix{}, false
}

func validateDHCPPool(cfg config.Config, lanPrefix netip.Prefix, lanIP netip.Addr) error {
	start, err := netip.ParseAddr(strings.TrimSpace(cfg.DHCP.RangeStart))
	if err != nil || !start.Is4() {
		return fmt.Errorf("DHCP range start %s is not IPv4", cfg.DHCP.RangeStart)
	}
	end, err := netip.ParseAddr(strings.TrimSpace(cfg.DHCP.RangeEnd))
	if err != nil || !end.Is4() {
		return fmt.Errorf("DHCP range end %s is not IPv4", cfg.DHCP.RangeEnd)
	}
	if start.Compare(end) > 0 {
		return fmt.Errorf("DHCP range start %s must not be after end %s", cfg.DHCP.RangeStart, cfg.DHCP.RangeEnd)
	}
	if !lanPrefix.Contains(start) || !lanPrefix.Contains(end) {
		return fmt.Errorf("DHCP range %s-%s is outside LAN prefix %s", cfg.DHCP.RangeStart, cfg.DHCP.RangeEnd, lanPrefix)
	}
	if lanIP.Compare(start) >= 0 && lanIP.Compare(end) <= 0 {
		return fmt.Errorf("DHCP range must not include gateway LAN IP %s", cfg.Gateway.LANIP)
	}
	return nil
}

func validateListenerPortConflicts(cfg config.Config) error {
	ports := []struct {
		name string
		port int
	}{
		{name: "dnsmasq DNS", port: cfg.DNS.Port},
		{name: "mihomo DNS", port: mihomoDNSPort},
		{name: "mihomo mixed", port: cfg.Mihomo.MixedPort},
	}
	if port, ok := listenPort(cfg.Management.Listen); ok {
		ports = append(ports, struct {
			name string
			port int
		}{name: "management", port: port})
	}
	if port, ok := listenPort(cfg.Mihomo.APIAddr); ok {
		ports = append(ports, struct {
			name string
			port int
		}{name: "mihomo API", port: port})
	}
	for index, left := range ports {
		if left.port <= 0 {
			continue
		}
		for _, right := range ports[index+1:] {
			if right.port == left.port {
				return fmt.Errorf("port conflict: %s and %s both use port %d", left.name, right.name, left.port)
			}
		}
	}
	return nil
}

func listenPort(value string) (int, bool) {
	_, portText, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}
