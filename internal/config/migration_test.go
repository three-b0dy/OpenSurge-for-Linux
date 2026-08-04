package config

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMigrateMacConfigRemovesPlatformFields(t *testing.T) {
	out, notes, err := MigrateMacConfig([]byte("pf:\n  anchor_name: x\nlocal_system_proxy:\n  enabled: true\n"))
	if err != nil || bytes.Contains(out, []byte("pf:")) || len(notes) == 0 {
		t.Fatalf("out=%s notes=%v err=%v", out, notes, err)
	}
}

func TestMigrateMacConfigPreservesManagedSectionsAndMapsDefaults(t *testing.T) {
	source := []byte(`gateway:
  mode: "isolated_lan"
  interface: "en0"
  upstream_interface: "en0"
management:
  listen: "127.0.0.1:61767"
mihomo:
  redir_port: 7892
  profile_mode: "imported"
  profile: "./profiles/home.yaml"
device_policy:
  file: "./devices.json"
pf:
  anchor_name: "com.apple/open_mihomo_gateway"
local_system_proxy:
  enabled: true
network_service: "Ethernet"
transparent:
  tun_device: "utun123"
`)
	out, notes, err := MigrateMacConfig(source)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(out, source) {
		t.Fatal("migration returned the source payload unchanged")
	}
	var migrated map[string]any
	if err := yaml.Unmarshal(out, &migrated); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"pf", "local_system_proxy", "network_service"} {
		if _, ok := migrated[removed]; ok {
			t.Fatalf("migration retained %s: %s", removed, out)
		}
	}
	if migrated["device_policy"].(map[string]any)["file"] != "./devices.json" {
		t.Fatalf("device_policy was not preserved: %s", out)
	}
	if migrated["mihomo"].(map[string]any)["profile"] != "./profiles/home.yaml" {
		t.Fatalf("mihomo profile was not preserved: %s", out)
	}
	gateway := migrated["gateway"].(map[string]any)
	if gateway["interface"] != "lan0" || gateway["upstream_interface"] != "wan0" {
		t.Fatalf("gateway mapping = %#v", gateway)
	}
	management := migrated["management"].(map[string]any)
	if management["listen"] != defaultManagementListen {
		t.Fatalf("management mapping = %#v", management)
	}
	transparent := migrated["transparent"].(map[string]any)
	if transparent["tun_device"] != "opensurge-tun" || transparent["tun_auto_redirect"] != true {
		t.Fatalf("transparent mapping = %#v", transparent)
	}
	if migrated["nftables"].(map[string]any)["table"] != "opensurge" {
		t.Fatalf("nftables mapping = %#v", migrated["nftables"])
	}
	redir := migrated["mihomo"].(map[string]any)["redir_port"]
	if redir != 0 {
		t.Fatalf("redir_port = %#v, want 0", redir)
	}
	for _, field := range []string{"gateway.interface", "gateway.upstream_interface", "management.listen"} {
		found := false
		for _, note := range notes {
			if strings.Contains(note, field) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("notes %v do not mention %s", notes, field)
		}
	}
}
