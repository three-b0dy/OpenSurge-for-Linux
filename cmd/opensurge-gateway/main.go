package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/controlapi"
)

func main() {
	socket := flag.String("socket", "/run/opensurge/gateway.sock", "Linux gateway socket path")
	configRoot := flag.String("config-root", "/etc/opensurge", "only configs below this directory are accepted")
	socketGroup := flag.String("socket-group", "opensurge", "local group allowed to connect to the gateway socket")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := controlapi.ServeGateway(ctx, *socket, *configRoot, *socketGroup); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
