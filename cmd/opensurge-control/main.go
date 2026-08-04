package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/controlapi"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/webui"
)

func main() {
	configPath := flag.String("config", "examples/config.example.yaml", "path to gateway config")
	addr := flag.String("addr", "", "management listen address; empty uses management.listen from the config")
	storeDir := flag.String("store", "", "application support directory")
	gatewaySocket := flag.String("gateway-socket", "/run/opensurge/gateway.sock", "privileged Linux gateway socket")
	direct := flag.Bool("direct-root", false, "run actions directly; requires root and is intended for development")
	flag.Parse()

	runner := controlapi.GatewayClient(controlapi.UnixGatewayClient{SocketPath: *gatewaySocket})
	if *direct {
		runner = controlapi.DirectRunner{}
	}
	server, err := controlapi.New(controlapi.Options{
		ConfigPath: *configPath,
		Addr:       *addr,
		StoreDir:   *storeDir,
		Runner:     runner,
		Static:     webui.Handler(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fmt.Printf("OpenSurge Control API: %s\n", *addr)
	fmt.Printf("Open Web GUI: %s/\n", server.URL())
	if err := server.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
