package nftables

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
)

// RenderRuleset renders only the OpenSurge-owned nftables table. It deliberately
// avoids global ruleset operations so that unrelated firewall configuration is
// left untouched.
func RenderRuleset(cfg config.Config) (string, error) {
	table, err := nftToken("nftables table", cfg.Nftables.Table)
	if err != nil {
		return "", err
	}
	interfaceName, err := nftToken("gateway interface", cfg.Gateway.Interface)
	if err != nil {
		return "", err
	}
	upstreamInterface, err := nftToken("upstream interface", cfg.Gateway.UpstreamInterface)
	if err != nil {
		return "", err
	}
	lanPrefix, err := cfg.LANPrefix24()
	if err != nil {
		return "", err
	}

	managementPort, err := managementPort(cfg.Management.Listen)
	if err != nil {
		return "", err
	}

	var rules strings.Builder
	fmt.Fprintf(&rules, "table inet %s {\n", table)
	rules.WriteString("  chain forward {\n")
	rules.WriteString("    type filter hook forward priority filter; policy accept;\n")
	rules.WriteString("    ct state established,related accept\n")
	fmt.Fprintf(&rules, "    iifname %q udp dport { 53, 67 } accept\n", interfaceName)
	fmt.Fprintf(&rules, "    iifname %q tcp dport 53 accept\n", interfaceName)
	if managementPort > 0 {
		fmt.Fprintf(&rules, "    iifname %q tcp dport %d accept\n", interfaceName, managementPort)
	}

	switch cfg.Gateway.Mode {
	case config.GatewayModeIsolatedLAN:
		fmt.Fprintf(&rules, "    iifname %q oifname %q ip saddr %s accept\n", interfaceName, upstreamInterface, lanPrefix)
	case config.GatewayModeSameLAN, config.GatewayModeSameWiFiDHCP:
		fmt.Fprintf(&rules, "    iifname %q oifname %q accept\n", interfaceName, upstreamInterface)
	default:
		return "", fmt.Errorf("unsupported gateway mode %q", cfg.Gateway.Mode)
	}
	rules.WriteString("  }\n\n")

	if cfg.Gateway.Mode == config.GatewayModeIsolatedLAN {
		fmt.Fprintf(&rules, "  chain isolated_ipv6_forward {\n")
		rules.WriteString("    type filter hook forward priority filter; policy accept;\n")
		fmt.Fprintf(&rules, "    iifname %q ip6 saddr ::/0 drop\n", interfaceName)
		rules.WriteString("  }\n\n")
	}

	if cfg.Gateway.Mode == config.GatewayModeIsolatedLAN {
		rules.WriteString("  chain postrouting {\n")
		rules.WriteString("    type nat hook postrouting priority srcnat; policy accept;\n")
		fmt.Fprintf(&rules, "    oifname %q ip saddr %s masquerade\n", upstreamInterface, lanPrefix)
		rules.WriteString("  }\n")
	}
	rules.WriteString("}\n")
	return rules.String(), nil
}

func managementPort(listen string) (int, error) {
	if strings.TrimSpace(listen) == "" {
		return 0, nil
	}
	_, portText, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("management.listen: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("management.listen port must be between 1 and 65535")
	}
	return port, nil
}

func nftToken(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return "", fmt.Errorf("%s contains invalid character %q", label, r)
	}
	return value, nil
}
