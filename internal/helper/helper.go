// Package helper builds the Twocan helper node: a private-DHT bootstrap peer
// (server mode), a circuit relay v2 with raised limits, and AutoNAT. Its only
// state is its own key and, if it receives them, log reports.
package helper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"twocan/internal/logbox"
	"twocan/internal/logging"
	"twocan/internal/p2p"
)

// pruneEvery is how often old log reports are deleted.
const pruneEvery = time.Hour

// Resources returns the relay limits: libp2p defaults, but relayed
// connections may last 30 minutes and carry 4 MiB.
func Resources() relay.Resources {
	r := relay.DefaultResources()
	r.Limit = &relay.RelayLimit{Duration: 30 * time.Minute, Data: 4 << 20}
	return r
}

// Start runs the helper on the given listen addresses. extra options are
// appended after the standard ones (tests use this; production passes none).
// The caller must close the DHT and then the host.
func Start(ctx context.Context, key crypto.PrivKey, listen []string, extra ...libp2p.Option) (host.Host, *dht.IpfsDHT, error) {
	var d *dht.IpfsDHT
	opts := []libp2p.Option{
		libp2p.Identity(key),
		libp2p.ListenAddrStrings(listen...),
		libp2p.ForceReachabilityPublic(),
		libp2p.EnableRelayService(relay.WithResources(Resources())),
		libp2p.EnableNATService(),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			var err error
			d, err = dht.New(h,
				dht.ProtocolPrefix(p2p.DHTPrefix),
				dht.Mode(dht.ModeServer),
			)
			return d, err
		}),
	}
	opts = append(opts, extra...)
	h, err := libp2p.New(opts...)
	if err != nil {
		if d != nil {
			_ = d.Close()
		}
		return nil, nil, fmt.Errorf("helper host: %w", err)
	}
	return h, d, nil
}

// StartLogs receives log reports into dir when readers is non-empty, and
// deletes old ones (now and every hour, until ctx ends or stop is called)
// whenever dir exists or readers is non-empty: turning the feature off
// leaves nothing behind for longer than lim.MaxAge. log may be nil.
func StartLogs(ctx context.Context, h host.Host, dir string, readers []peer.ID, lim logbox.Limits, log *slog.Logger) (stop func(), err error) {
	if log == nil {
		log = logging.Discard()
	}
	if len(readers) == 0 {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			return func() {}, nil
		}
	}
	st, err := logbox.OpenStore(dir, lim)
	if err != nil {
		return nil, err
	}
	prune := func() {
		if err := st.Prune(time.Now()); err != nil {
			log.Warn("could not delete old log reports", "err", err)
		}
	}
	prune()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(pruneEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				prune()
			}
		}
	}()
	stopServe := func() {}
	if len(readers) > 0 {
		stopServe = logbox.Serve(h, st, logbox.ServerConfig{Readers: readers, Log: log})
	}
	return func() {
		stopServe()
		cancel()
		<-done
	}, nil
}
