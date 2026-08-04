package linuxnet

import (
	"context"
	"net/netip"
)

type InterfaceInspector interface {
	Addresses(context.Context, string) ([]netip.Prefix, error)
	Neighbors(context.Context, string) ([]Neighbor, error)
}

type InterfaceOption struct {
	Name string   `json:"name"`
	IPv4 []string `json:"ipv4,omitempty"`
}

type Neighbor struct {
	IPv4  netip.Addr
	MAC   string
	State string
}

type CommandRunner func(context.Context, string, ...string) ([]byte, error)
