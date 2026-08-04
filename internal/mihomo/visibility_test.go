package mihomo

import "testing"

func TestVisibleProxyGroupsKeepsOrdinaryGroupsAndOptions(t *testing.T) {
	groups := []ProxyGroup{{Name: "Main", Selected: "Proxy-A", Options: []string{"Proxy-A", "DIRECT"}}}
	visible := VisibleProxyGroups(groups)
	if len(visible) != 1 || visible[0].Name != "Main" || len(visible[0].Options) != 2 {
		t.Fatalf("VisibleProxyGroups() = %#v", visible)
	}
	visible[0].Options[0] = "changed"
	if groups[0].Options[0] != "Proxy-A" {
		t.Fatal("VisibleProxyGroups() returned aliased options")
	}
}

func TestVisibleProvidersKeepsOrdinaryProviderProxies(t *testing.T) {
	snapshot := VisibleProviders(ProvidersSnapshot{ProxyProviders: []ProxyProvider{{
		Name:       "subscription",
		ProxyCount: 1,
		Proxies:    []ProviderProxy{{Name: "Proxy-A"}},
	}}})
	if len(snapshot.ProxyProviders) != 1 || len(snapshot.ProxyProviders[0].Proxies) != 1 || snapshot.ProxyProviders[0].Proxies[0].Name != "Proxy-A" {
		t.Fatalf("VisibleProviders() = %#v", snapshot)
	}
}
