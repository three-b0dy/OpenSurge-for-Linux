package linuxnet

import (
	"context"
	"net/netip"
)

type InterfaceInspector interface {
	Addresses(context.Context, string) ([]netip.Prefix, error)
	Neighbors(context.Context, string) ([]Neighbor, error)
}

type Neighbor struct {
	IPv4  netip.Addr
	MAC   string
	State string
}

type CommandRunner func(context.Context, string, ...string) ([]byte, error)
