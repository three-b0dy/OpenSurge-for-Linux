package nftables

import (
	"strings"
	"testing"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
)

func TestRenderRulesetUsesOnlyOpenSurgeTable(t *testing.T) {
	cfg := config.Default()
	cfg.Nftables.Table = "opensurge"

	rules, err := RenderRuleset(cfg)
	if err != nil {
		t.Fatalf("RenderRuleset() error = %v", err)
	}
	if !strings.Contains(rules, "table inet opensurge") {
		t.Fatalf("ruleset does not contain the opensurge table:\n%s", rules)
	}
	if got := strings.Count(rules, "\ntable inet "); got != 1 {
		t.Fatalf("ruleset table count = %d, want 1:\n%s", got, rules)
	}
	if strings.Contains(rules, "flush ruleset") {
		t.Fatalf("ruleset must not flush unrelated tables:\n%s", rules)
	}
	if !strings.Contains(rules, "chain forward") || !strings.Contains(rules, "masquerade") {
		t.Fatalf("ruleset is missing forwarding or masquerade rules:\n%s", rules)
	}
	forward := chainBody(t, rules, "forward")
	jump := `iifname "lan0" ip6 saddr ::/0 jump isolated_ipv6_forward`
	if !strings.Contains(forward, jump) {
		t.Fatalf("forward chain is missing the isolated IPv6 jump:\n%s", rules)
	}
	headerEnd := strings.Index(forward, "policy accept;\n")
	if headerEnd >= 0 {
		headerEnd += len("policy accept;\n")
	}
	if jumpAt := strings.Index(forward, jump); jumpAt < 0 || headerEnd < 0 || jumpAt != headerEnd+4 {
		t.Fatalf("isolated IPv6 jump must be the first forward rule:\n%s", forward)
	}
	ipv6Chain := chainBody(t, rules, "isolated_ipv6_forward")
	if strings.Contains(ipv6Chain, "type filter hook") || strings.Contains(ipv6Chain, "priority") || strings.Contains(ipv6Chain, "policy") {
		t.Fatalf("isolated IPv6 chain must be an ordinary chain:\n%s", ipv6Chain)
	}
	if !strings.Contains(ipv6Chain, "drop") {
		t.Fatalf("isolated_lan ruleset is missing downstream IPv6 drop:\n%s", rules)
	}
}

func TestRenderRulesetDestroysOnlyNamedTableBeforeCreate(t *testing.T) {
	cfg := config.Default()
	cfg.Nftables.Table = "custom_table"

	rules, err := RenderRuleset(cfg)
	if err != nil {
		t.Fatalf("RenderRuleset() error = %v", err)
	}
	wantPrefix := "destroy table inet custom_table\n\ntable inet custom_table {"
	if !strings.HasPrefix(rules, wantPrefix) {
		prefix := rules
		if len(prefix) > len(wantPrefix) {
			prefix = prefix[:len(wantPrefix)]
		}
		t.Fatalf("ruleset prefix = %q, want %q:\n%s", prefix, wantPrefix, rules)
	}
	if got := strings.Count(rules, "destroy table inet "); got != 1 {
		t.Fatalf("destroy command count = %d, want 1:\n%s", got, rules)
	}
	if strings.Contains(rules, "flush ruleset") {
		t.Fatalf("ruleset must not flush unrelated tables:\n%s", rules)
	}
}

func TestRenderRulesetUsesModeSpecificForwardPermits(t *testing.T) {
	cases := []struct {
		name string
		mode string
		want string
	}{
		{name: "same lan", mode: config.GatewayModeSameLAN, want: `iifname "lan0" oifname "lan0" accept`},
		{name: "same wifi dhcp", mode: config.GatewayModeSameWiFiDHCP, want: `iifname "lan0" oifname "lan0" accept`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Gateway.Mode = tc.mode
			cfg.Gateway.UpstreamInterface = cfg.Gateway.Interface

			rules, err := RenderRuleset(cfg)
			if err != nil {
				t.Fatalf("RenderRuleset() error = %v", err)
			}
			if !strings.Contains(rules, tc.want) {
				t.Fatalf("ruleset does not contain mode-specific permit %q:\n%s", tc.want, rules)
			}
			if strings.Contains(rules, "ip6 saddr ::/0 drop") {
				t.Fatalf("non-isolated mode unexpectedly drops downstream IPv6 forwarding:\n%s", rules)
			}
		})
	}
}

func chainBody(t *testing.T, rules, name string) string {
	t.Helper()
	startMarker := "chain " + name + " {"
	start := strings.Index(rules, startMarker)
	if start < 0 {
		t.Fatalf("ruleset is missing %s:\n%s", startMarker, rules)
	}
	end := strings.Index(rules[start+len(startMarker):], "\n  }")
	if end < 0 {
		t.Fatalf("ruleset has unterminated %s:\n%s", startMarker, rules)
	}
	return rules[start : start+len(startMarker)+end]
}
