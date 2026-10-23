// Package helper builds the Twocan helper node: a private-DHT bootstrap peer
// (server mode), a circuit relay v2 with raised limits, and AutoNAT. It keeps
// no state except its own key.
package helper

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"twocan/internal/p2p"
)

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
