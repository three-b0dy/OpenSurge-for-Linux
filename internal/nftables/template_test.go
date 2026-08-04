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
	if got := strings.Count(rules, "table inet "); got != 1 {
		t.Fatalf("ruleset table count = %d, want 1:\n%s", got, rules)
	}
	if strings.Contains(rules, "flush ruleset") {
		t.Fatalf("ruleset must not flush unrelated tables:\n%s", rules)
	}
	if !strings.Contains(rules, "chain forward") || !strings.Contains(rules, "masquerade") {
		t.Fatalf("ruleset is missing forwarding or masquerade rules:\n%s", rules)
	}
	if !strings.Contains(rules, `iifname "lan0" ip6`) || !strings.Contains(rules, "drop") {
		t.Fatalf("isolated_lan ruleset is missing downstream IPv6 drop:\n%s", rules)
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
