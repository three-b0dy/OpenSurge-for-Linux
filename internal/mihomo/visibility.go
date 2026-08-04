package mihomo

// VisibleProxyGroups returns the policy groups supplied by mihomo without
// hiding ordinary imported or managed groups. The returned slice owns its
// option lists so callers can shape API responses without mutating a runtime
// snapshot.
func VisibleProxyGroups(groups []ProxyGroup) []ProxyGroup {
	visible := make([]ProxyGroup, len(groups))
	for index, group := range groups {
		visible[index] = group
		visible[index].Options = append([]string(nil), group.Options...)
	}
	return visible
}

func VisibleProxyHealth(proxies []ProxyHealth) []ProxyHealth {
	visible := make([]ProxyHealth, len(proxies))
	copy(visible, proxies)
	return visible
}

func VisibleProviders(snapshot ProvidersSnapshot) ProvidersSnapshot {
	providers := make([]ProxyProvider, len(snapshot.ProxyProviders))
	for index, provider := range snapshot.ProxyProviders {
		providers[index] = provider
		providers[index].Proxies = append([]ProviderProxy(nil), provider.Proxies...)
	}
	snapshot.ProxyProviders = providers
	return snapshot
}
