package config

import (
	"fmt"
	"net"
	"strings"

	"gopkg.in/yaml.v3"
)

// MigrateMacConfig returns a Linux candidate configuration and notes for the
// mappings that still require operator confirmation. It never writes source
// or candidate data to disk.
func MigrateMacConfig(source []byte) ([]byte, []string, error) {
	var root map[string]any
	if err := yaml.Unmarshal(source, &root); err != nil {
		return nil, nil, err
	}
	if root == nil {
		root = map[string]any{}
	}

	delete(root, "pf")
	delete(root, "local_system_proxy")
	delete(root, "network_service")

	var notes []string
	gateway, err := migrationMapping(root, "gateway")
	if err != nil {
		return nil, nil, err
	}
	setMigrationDefault(gateway, "mode", GatewayModeIsolatedLAN)
	setMigrationDefault(gateway, "lan_ip", "192.168.50.1")
	setMigrationDefault(gateway, "router_dhcp_disabled_confirmed", false)
	mapLegacyInterface(gateway, "interface", "lan0", &notes)
	mapLegacyInterface(gateway, "upstream_interface", "wan0", &notes)

	management, err := migrationMapping(root, "management")
	if err != nil {
		return nil, nil, err
	}
	if listen, ok := migrationString(management["listen"]); !ok || strings.TrimSpace(listen) == "" {
		management["listen"] = defaultManagementListen
		notes = append(notes, "map management.listen to the Linux LAN IPv4 address if the default does not match the host")
	} else if legacyManagementListen(listen) {
		management["listen"] = defaultManagementListen
		notes = append(notes, "map management.listen from the macOS loopback default to a reachable Linux IPv4 address")
	}
	setMigrationDefault(management, "tls_cert_file", "")
	setMigrationDefault(management, "tls_key_file", "")

	nftables, err := migrationMapping(root, "nftables")
	if err != nil {
		return nil, nil, err
	}
	setMigrationDefault(nftables, "table", "opensurge")

	mihomo, err := migrationMapping(root, "mihomo")
	if err != nil {
		return nil, nil, err
	}
	if redir, ok := mihomo["redir_port"]; ok && strings.TrimSpace(fmt.Sprint(redir)) != "0" {
		notes = append(notes, "mihomo.redir_port was reset to zero because Linux uses TUN only")
	}
	mihomo["redir_port"] = 0

	transparent, err := migrationMapping(root, "transparent")
	if err != nil {
		return nil, nil, err
	}
	if device, ok := migrationString(transparent["tun_device"]); !ok || isMacInterface(device) {
		transparent["tun_device"] = "opensurge-tun"
		if ok {
			notes = append(notes, "map transparent.tun_device from the macOS utun device to opensurge-tun")
		}
	}
	setMigrationDefault(transparent, "mode", TransparentModeOff)
	setMigrationDefault(transparent, "tun_stack", "mixed")
	setMigrationDefault(transparent, "tun_auto_route", true)
	transparent["tun_auto_redirect"] = true
	setMigrationDefault(transparent, "tun_auto_detect_interface", false)
	setMigrationDefault(transparent, "tun_strict_route", false)

	data, err := yaml.Marshal(root)
	if err != nil {
		return nil, nil, err
	}
	return data, notes, nil
}

func migrationMapping(root map[string]any, key string) (map[string]any, error) {
	value, ok := root[key]
	if !ok || value == nil {
		mapping := map[string]any{}
		root[key] = mapping
		return mapping, nil
	}
	if mapping, ok := value.(map[string]any); ok {
		return mapping, nil
	}
	return nil, fmt.Errorf("%s must be a mapping", key)
}

func setMigrationDefault(mapping map[string]any, key string, value any) {
	if current, ok := mapping[key]; !ok || current == nil || strings.TrimSpace(fmt.Sprint(current)) == "" {
		mapping[key] = value
	}
}

func migrationString(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func mapLegacyInterface(mapping map[string]any, key, linuxDefault string, notes *[]string) {
	value, ok := migrationString(mapping[key])
	if !ok || strings.TrimSpace(value) == "" {
		mapping[key] = linuxDefault
		*notes = append(*notes, fmt.Sprintf("map %s to the Linux interface that owns the gateway path", "gateway."+key))
		return
	}
	if isMacInterface(value) {
		mapping[key] = linuxDefault
		*notes = append(*notes, fmt.Sprintf("map %s from macOS interface %q to the Linux interface that owns the gateway path", "gateway."+key, value))
	}
}

func isMacInterface(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "en") {
		suffix := value[2:]
		if suffix != "" && strings.Trim(suffix, "0123456789") == "" {
			return true
		}
	}
	for _, prefix := range []string{"utun", "awdl", "llw", "p2p"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func legacyManagementListen(value string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return true
	}
	ip := net.ParseIP(host).To4()
	return ip == nil || ip.IsLoopback() || ip.IsUnspecified()
}
