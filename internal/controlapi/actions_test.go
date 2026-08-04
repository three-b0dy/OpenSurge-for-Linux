package controlapi

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGatewaySocketRejectsUnknownAction(t *testing.T) {
	response := callGateway(t, HelperRequest{Action: "network-" + "set-" + "manual"})
	if response.OK || !strings.Contains(response.Error, "not allowed") {
		t.Fatalf("gateway response = %+v", response)
	}
}

func TestNetworkInterfacesReturnsLinuxNames(t *testing.T) {
	server := newTestServer(t)
	server.listInterfaces = func(context.Context) ([]InterfaceOption, error) {
		return []InterfaceOption{{Name: "br-lan", IPv4: []string{"192.168.50.1/24"}}}, nil
	}
	response := performAuthorized(server, "GET", "/api/v1/network/interfaces", nil)
	legacyNetworkServiceKey := "network_" + "service"
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"name":"br-lan"`) || strings.Contains(response.Body.String(), legacyNetworkServiceKey) {
		t.Fatalf("network interfaces response = %s", response.Body.String())
	}
}

func TestLinuxControlModelsDoNotSerializeRetiredNetworkState(t *testing.T) {
	payload, err := json.Marshal(RecoveryState{Stage: RecoveryGatewayActive, Required: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"network_" + "snapshot", "network_" + "service", "original_" + "ipv4"} {
		if strings.Contains(string(payload), retired) {
			t.Fatalf("recovery JSON retained %q: %s", retired, payload)
		}
	}
	server := newTestServer(t)
	legacyMenuPath := "/api/v1/" + "menu" + "bar"
	if response := performAuthorized(server, "GET", legacyMenuPath, nil); response.Code != 404 {
		t.Fatalf("retired menu endpoint status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSameWiFiConfirmationIsRecordedWithoutNetworkMutation(t *testing.T) {
	server := newTestServer(t)
	response := performAuthorized(server, "POST", "/api/v1/recovery", []byte(`{"stage":"router_dhcp_disabled_confirmed","recovery_notes":"operator confirmed in router UI"}`))
	if response.Code != 200 {
		t.Fatalf("recovery confirmation status=%d body=%s", response.Code, response.Body.String())
	}
	state, err := server.store.Recovery()
	if err != nil {
		t.Fatal(err)
	}
	if state.Stage != RecoveryRouterDHCPDisabledConfirmed || !state.Required {
		t.Fatalf("recovery state = %#v", state)
	}
	if response := performAuthorized(server, "POST", "/api/v1/network/apply-static", nil); response.Code != 404 {
		t.Fatalf("retired network mutation status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSystemdSocketActivationUsesPassedListener(t *testing.T) {
	path := filepath.Join(os.TempDir(), "opensurge-gateway-test-"+strconv.Itoa(os.Getpid())+".sock")
	_ = os.Remove(path)
	t.Cleanup(func() { _ = os.Remove(path) })
	base, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	file, err := base.(*net.UnixListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	listener, activated, err := listenerFromSystemdFile(path, strconv.Itoa(os.Getpid()), "1", file)
	if err != nil {
		t.Fatal(err)
	}
	if !activated {
		t.Fatal("systemd listener was not recognized")
	}
	defer listener.Close()
	if listener.Addr().String() != path {
		t.Fatalf("listener address = %q, want %q", listener.Addr(), path)
	}
}

func callGateway(t *testing.T, request HelperRequest) HelperResponse {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go handleGatewayConn(context.Background(), server, t.TempDir())
	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response HelperResponse
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}
