// Command twocan-helper runs the Twocan bootstrap/relay/AutoNAT node.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"twocan/internal/helper"
	"twocan/internal/identity"
)

const defaultListen = "/ip4/0.0.0.0/tcp/4001,/ip4/0.0.0.0/udp/4001/quic-v1,/ip6/::/tcp/4001,/ip6/::/udp/4001/quic-v1"

func main() {
	keyPath := flag.String("key", "./helper.key", "path to the helper's private key file")
	listen := flag.String("listen", defaultListen, "comma-separated listen multiaddrs")
	flag.Parse()

	if err := run(*keyPath, strings.Split(*listen, ",")); err != nil {
		log.Fatalf("twocan-helper: %v", err)
	}
}

func run(keyPath string, listen []string) error {
	key, err := identity.LoadOrCreateKey(keyPath)
	if err != nil {
		return fmt.Errorf("key: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h, d, err := helper.Start(ctx, key, listen)
	if err != nil {
		return err
	}
	defer func() {
		_ = d.Close()
		_ = h.Close()
	}()

	for _, a := range h.Addrs() {
		fmt.Printf("%s/p2p/%s\n", a, h.ID())
	}

	// Counts only: never log peer IDs.
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Print("shutting down")
			return nil
		case <-tick.C:
			log.Printf("connected peers: %d", len(h.Network().Peers()))
		}
	}
}
