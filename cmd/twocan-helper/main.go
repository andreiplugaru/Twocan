// Command twocan-helper runs the Twocan bootstrap/relay/AutoNAT node.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/helper"
	"twocan/internal/identity"
	"twocan/internal/logbox"
)

const defaultListen = "/ip4/0.0.0.0/tcp/4001,/ip4/0.0.0.0/udp/4001/quic-v1,/ip6/::/tcp/4001,/ip6/::/udp/4001/quic-v1"

func main() {
	keyPath := flag.String("key", "./helper.key", "path to the helper's private key file")
	listen := flag.String("listen", defaultListen, "comma-separated listen multiaddrs")
	def := logbox.DefaultLimits()
	readersFlag := flag.String("log-readers", "", "comma-separated peer IDs that may read log reports; empty: do not receive logs")
	maxUpload := flag.Int64("log-max-upload", def.MaxUpload, "largest log report, in bytes")
	maxTotal := flag.Int64("log-max-total", def.MaxTotal, "space for all log reports, in bytes; the oldest are deleted first")
	maxAge := flag.Duration("log-max-age", def.MaxAge, "how long log reports are kept")
	flag.Parse()

	var readers []peer.ID
	for _, s := range strings.Split(*readersFlag, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		id, err := peer.Decode(s)
		if err != nil {
			log.Fatalf("twocan-helper: --log-readers: %q is not a peer ID: %v", s, err)
		}
		readers = append(readers, id)
	}
	lim := logbox.Limits{MaxUpload: *maxUpload, MaxTotal: *maxTotal, MaxAge: *maxAge}
	// The logs live next to the key, in the service's state directory.
	logsDir := filepath.Join(filepath.Dir(*keyPath), "logs")
	if err := run(*keyPath, strings.Split(*listen, ","), logsDir, readers, lim); err != nil {
		log.Fatalf("twocan-helper: %v", err)
	}
}

func run(keyPath string, listen []string, logsDir string, readers []peer.ID, lim logbox.Limits) error {
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

	stopLogs, err := helper.StartLogs(ctx, h, logsDir, readers, lim, slog.Default())
	if err != nil {
		return fmt.Errorf("log reports: %w", err)
	}
	defer stopLogs()
	if len(readers) > 0 {
		log.Printf("receiving log reports for %d reader(s)", len(readers))
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
