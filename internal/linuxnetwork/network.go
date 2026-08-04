package linuxnetwork

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/linuxnet"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/process"
)

// Snapshot is the network observation shape retained by the control API while
// Linux network discovery is being integrated with the gateway preflight.
type Snapshot struct {
	NetworkService string   `json:"network_service"`
	Interface      string   `json:"interface"`
	IPv4Mode       string   `json:"-"`
	HardwareAddr   string   `json:"hardware_address,omitempty"`
	IPv4           string   `json:"ipv4,omitempty"`
	SubnetMask     string   `json:"subnet_mask,omitempty"`
	Router         string   `json:"router,omitempty"`
	DNS            []string `json:"dns"`
	IPv6Default    bool     `json:"ipv6_default"`
}

const (
	IPv4ModeDHCP   = "dhcp"
	IPv4ModeManual = "manual"
)

type ManualConfig struct {
	NetworkService string   `json:"network_service"`
	Interface      string   `json:"interface"`
	IPv4           string   `json:"ipv4"`
	SubnetMask     string   `json:"subnet_mask"`
	Router         string   `json:"router"`
	DNS            []string `json:"dns"`
}

type InterfaceOption struct {
	Interface      string `json:"interface"`
	NetworkService string `json:"network_service"`
}

type Neighbor struct {
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	Interface string `json:"interface"`
}

type RouteSelection struct {
	Interface string
	Gateway   string
}

func Discover(ctx context.Context, networkService, interfaceName string) (Snapshot, error) {
	if strings.TrimSpace(interfaceName) == "" {
		interfaceName = strings.TrimSpace(networkService)
	}
	if err := validateInterface(interfaceName); err != nil {
		return Snapshot{}, err
	}
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		NetworkService: strings.TrimSpace(networkService),
		Interface:      interfaceName,
		HardwareAddr:   iface.HardwareAddr.String(),
		IPv4Mode:       IPv4ModeManual,
		DNS:            []string{},
	}
	if snapshot.NetworkService == "" {
		snapshot.NetworkService = interfaceName
	}
	for _, addr := range mustInterfaceAddrs(iface) {
		ip, network, err := net.ParseCIDR(addr)
		if err == nil && ip.To4() != nil {
			snapshot.IPv4 = ip.To4().String()
			mask := network.Mask
			snapshot.SubnetMask = net.IP(mask).To4().String()
			break
		}
	}
	if snapshot.IPv4 == "" {
		return Snapshot{}, fmt.Errorf("interface %q has no IPv4 address", interfaceName)
	}
	if route, err := LookupRoute(ctx, "1.1.1.1"); err == nil {
		snapshot.Router = route.Gateway
		snapshot.IPv6Default = route.Interface == interfaceName && strings.Contains(route.Gateway, ":")
	}
	return snapshot, nil
}

func ListInterfaces(context.Context) ([]InterfaceOption, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := make([]InterfaceOption, 0, len(interfaces))
	for _, iface := range interfaces {
		result = append(result, InterfaceOption{Interface: iface.Name, NetworkService: iface.Name})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Interface < result[j].Interface })
	return result, nil
}

func DiscoverNeighbors(ctx context.Context, interfaceName string) ([]Neighbor, error) {
	if err := validateInterface(interfaceName); err != nil {
		return nil, err
	}
	route := linuxnet.NewIPRoute(func(_ context.Context, name string, args ...string) ([]byte, error) {
		return process.Output(name, args...)
	})
	entries, err := route.Neighbors(ctx, interfaceName)
	if err != nil {
		return nil, err
	}
	neighbors := make([]Neighbor, 0, len(entries))
	for _, entry := range entries {
		neighbors = append(neighbors, Neighbor{IP: entry.IPv4.String(), MAC: entry.MAC, Interface: interfaceName})
	}
	return neighbors, nil
}

func LookupRoute(ctx context.Context, destination string) (RouteSelection, error) {
	if net.ParseIP(destination) == nil {
		return RouteSelection{}, fmt.Errorf("invalid route destination %q", destination)
	}
	output, err := process.Output("ip", "route", "get", destination)
	if err != nil {
		return RouteSelection{}, err
	}
	fields := strings.Fields(string(output))
	selection := RouteSelection{}
	for index, field := range fields {
		switch field {
		case "dev":
			if index+1 < len(fields) {
				selection.Interface = fields[index+1]
			}
		case "via":
			if index+1 < len(fields) {
				selection.Gateway = fields[index+1]
			}
		}
	}
	if selection.Interface == "" {
		return RouteSelection{}, fmt.Errorf("route lookup for %s did not return an interface", destination)
	}
	return selection, nil
}

func ServiceInterface(_ context.Context, networkService string) (string, error) {
	if strings.TrimSpace(networkService) == "" {
		return "", fmt.Errorf("network service is required")
	}
	return networkService, nil
}

func ValidateManual(cfg ManualConfig) error {
	ip := net.ParseIP(cfg.IPv4).To4()
	maskIP := net.ParseIP(cfg.SubnetMask).To4()
	router := net.ParseIP(cfg.Router).To4()
	if ip == nil || maskIP == nil || router == nil {
		return fmt.Errorf("manual network configuration requires valid IPv4, subnet mask, and router")
	}
	mask := net.IPMask(maskIP)
	ones, bits := mask.Size()
	if bits != 32 || ones <= 0 || ones >= 32 {
		return fmt.Errorf("manual network configuration requires a contiguous unicast subnet mask")
	}
	if !ip.Mask(mask).Equal(router.Mask(mask)) {
		return fmt.Errorf("manual IPv4 and router must share a subnet")
	}
	if ip.Equal(router) {
		return fmt.Errorf("manual IPv4 must differ from router")
	}
	if strings.TrimSpace(cfg.NetworkService) == "" || strings.TrimSpace(cfg.Interface) == "" {
		return fmt.Errorf("network service and interface are required")
	}
	for _, server := range cfg.DNS {
		if net.ParseIP(server) == nil {
			return fmt.Errorf("invalid DNS server %q", server)
		}
	}
	return nil
}

func VerifyManual(snapshot Snapshot, expected ManualConfig) error {
	if snapshot.NetworkService != expected.NetworkService || snapshot.Interface != expected.Interface {
		return fmt.Errorf("network service or interface changed during fixed IPv4 setup")
	}
	if snapshot.IPv4Mode != IPv4ModeManual {
		return fmt.Errorf("network service %q did not report manual IPv4 configuration", expected.NetworkService)
	}
	if snapshot.IPv4 != expected.IPv4 {
		return fmt.Errorf("network service %q reports IPv4 %s instead of %s", expected.NetworkService, snapshot.IPv4, expected.IPv4)
	}
	if snapshot.SubnetMask != expected.SubnetMask || snapshot.Router != expected.Router {
		return fmt.Errorf("network service %q reports an unexpected subnet mask or router", expected.NetworkService)
	}
	return nil
}

func SetManual(context.Context, ManualConfig) error {
	return fmt.Errorf("manual network changes are managed by the Linux gateway lifecycle")
}

func SetDHCP(context.Context, string) error {
	return fmt.Errorf("DHCP restoration is managed by the Linux gateway lifecycle")
}

func ProbeDHCPServers(context.Context, string, time.Duration) ([]string, error) {
	return nil, fmt.Errorf("DHCP probing is managed by the Linux gateway lifecycle")
}

func PingRouter(ctx context.Context, router string) error {
	if net.ParseIP(router) == nil {
		return fmt.Errorf("invalid router address %q", router)
	}
	if _, err := os.Stat("/sbin/ping"); err == nil {
		return process.Run("/sbin/ping", "-c", "1", "-W", "1", router)
	}
	return process.Run("ping", "-c", "1", "-W", "1", router)
}

func validateInterface(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid interface name %q", name)
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("invalid interface name %q", name)
	}
	return nil
}

func mustInterfaceAddrs(iface *net.Interface) []string {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	result := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		result = append(result, addr.String())
	}
	return result
}
