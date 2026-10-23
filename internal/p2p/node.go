// Package p2p runs Twocan's libp2p host: a private Kademlia DHT for finding
// peers, mDNS for the local network, NAT traversal and relay fallback.
package p2p

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	// DHTPrefix keeps Twocan's DHT separate from the public IPFS DHT.
	DHTPrefix = "/twocan"
	// MDNSService is the mDNS service name Twocan nodes advertise.
	MDNSService = "twocan"

	// dialTimeout bounds each connection attempt.
	dialTimeout = 10 * time.Second
)

// DefaultListenAddrs listens on all interfaces, TCP and QUIC, IPv4 and IPv6.
var DefaultListenAddrs = []string{
	"/ip4/0.0.0.0/tcp/0",
	"/ip4/0.0.0.0/udp/0/quic-v1",
	"/ip6/::/tcp/0",
	"/ip6/::/udp/0/quic-v1",
}

// ErrUnreachable is returned by Reach when no route to the peer worked.
var ErrUnreachable = errors.New("peer unreachable")

// Reacher establishes a connection to a peer.
type Reacher interface {
	Reach(ctx context.Context, id peer.ID) error
}

// Config configures a Node.
type Config struct {
	PrivKey     crypto.PrivKey
	ListenAddrs []string // empty: do not listen
	Bootstrap   []peer.AddrInfo
	EnableMDNS  bool
	// ForcePrivate makes the node believe it is behind a NAT (tests use it to
	// force relaying).
	ForcePrivate bool
}

// Node is a running libp2p host plus its DHT and discovery services.
type Node struct {
	host  host.Host
	dht   *dht.IpfsDHT
	mdns  mdns.Service
	reach atomic.Int32 // network.Reachability

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

var _ Reacher = (*Node)(nil)

// NewNode starts a node. Connecting to bootstrap peers and bootstrapping the
// DHT happen in the background; NewNode never waits for them. Background
// work stops when ctx is cancelled or Close is called.
func NewNode(ctx context.Context, cfg Config) (*Node, error) {
	cm, err := connmgr.NewConnManager(50, 100)
	if err != nil {
		return nil, fmt.Errorf("conn manager: %w", err)
	}
	n := &Node{}
	n.reach.Store(int32(network.ReachabilityUnknown))

	opts := []libp2p.Option{
		libp2p.Identity(cfg.PrivKey),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		libp2p.ConnectionManager(cm),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			d, err := newDHT(h, cfg.Bootstrap)
			n.dht = d
			return d, err
		}),
	}
	if len(cfg.ListenAddrs) == 0 {
		// NoListenAddrs also disables the relay transport; re-enable it (must
		// come after) so a non-listening node can still be reached via a relay.
		opts = append(opts, libp2p.NoListenAddrs, libp2p.EnableRelay())
	} else {
		opts = append(opts, libp2p.ListenAddrStrings(cfg.ListenAddrs...))
	}
	if len(cfg.Bootstrap) > 0 {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(cfg.Bootstrap))
	}
	if cfg.ForcePrivate {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		if n.dht != nil {
			_ = n.dht.Close()
		}
		return nil, fmt.Errorf("libp2p host: %w", err)
	}
	n.host = h

	sub, err := h.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		_ = n.dht.Close()
		_ = h.Close()
		return nil, fmt.Errorf("subscribe reachability: %w", err)
	}

	if cfg.EnableMDNS {
		n.mdns = mdns.NewMdnsService(h, MDNSService, mdnsNotifee{h})
		if err := n.mdns.Start(); err != nil {
			_ = sub.Close()
			_ = n.dht.Close()
			_ = h.Close()
			return nil, fmt.Errorf("start mdns: %w", err)
		}
	}

	bgCtx, cancel := context.WithCancel(ctx)
	n.cancel = cancel
	n.wg.Add(2)
	go func() {
		defer n.wg.Done()
		n.trackReachability(bgCtx, sub)
	}()
	go func() {
		defer n.wg.Done()
		n.bootstrap(bgCtx, cfg.Bootstrap)
	}()
	return n, nil
}

// newDHT is the single place the DHT and its options are built.
func newDHT(h host.Host, bootstrap []peer.AddrInfo) (*dht.IpfsDHT, error) {
	return dht.New(h,
		dht.ProtocolPrefix(DHTPrefix),
		dht.Mode(dht.ModeAuto),
		dht.BootstrapPeers(bootstrap...),
	)
}

// Host returns the underlying libp2p host.
func (n *Node) Host() host.Host { return n.host }

// Close stops background work and shuts down mDNS, the DHT and the host.
func (n *Node) Close() error {
	n.closeOnce.Do(func() {
		n.cancel()
		n.wg.Wait()
		var errs []error
		if n.mdns != nil {
			errs = append(errs, n.mdns.Close())
		}
		errs = append(errs, n.dht.Close(), n.host.Close())
		n.closeErr = errors.Join(errs...)
	})
	return n.closeErr
}

// Reach makes sure there is a connection to id: an existing one, else a dial
// to the addresses already in the peerstore, else a dial to addresses found
// through the DHT. Relayed (limited) connections count.
func (n *Node) Reach(ctx context.Context, id peer.ID) error {
	if connected, _ := n.ConnInfo(id); connected {
		return nil
	}
	ctx = network.WithAllowLimitedConn(ctx, "twocan")

	if addrs := n.host.Peerstore().Addrs(id); len(addrs) > 0 {
		if err := n.connect(ctx, peer.AddrInfo{ID: id, Addrs: addrs}); err == nil {
			return nil
		}
	}

	info, err := n.dht.FindPeer(ctx, id)
	if err == nil {
		if err = n.connect(ctx, info); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: %s: %w", ErrUnreachable, id, err)
}

func (n *Node) connect(ctx context.Context, info peer.AddrInfo) error {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	return n.host.Connect(ctx, info)
}

// ConnInfo reports whether there is an open connection to id, and whether
// every open connection to id goes through a relay (/p2p-circuit).
func (n *Node) ConnInfo(id peer.ID) (connected, relayed bool) {
	conns := n.host.Network().ConnsToPeer(id)
	if len(conns) == 0 {
		return false, false
	}
	for _, c := range conns {
		if _, err := c.RemoteMultiaddr().ValueForProtocol(ma.P_CIRCUIT); err != nil {
			return true, false
		}
	}
	return true, true
}

// Reachability returns the latest reachability reported by libp2p (AutoNAT),
// ReachabilityUnknown until the first report.
func (n *Node) Reachability() network.Reachability {
	return network.Reachability(n.reach.Load())
}

func (n *Node) trackReachability(ctx context.Context, sub event.Subscription) {
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.Out():
			if !ok {
				return
			}
			n.reach.Store(int32(e.(event.EvtLocalReachabilityChanged).Reachability))
		}
	}
}

// bootstrap connects to the bootstrap peers, then kicks off a DHT refresh.
func (n *Node) bootstrap(ctx context.Context, peers []peer.AddrInfo) {
	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = n.connect(ctx, p) // failures are retried by the DHT's refresh
		}()
	}
	wg.Wait()
	if ctx.Err() == nil {
		_ = n.dht.Bootstrap(ctx)
	}
}

// mdnsNotifee records peers found on the local network; it does not dial.
type mdnsNotifee struct{ h host.Host }

func (m mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == m.h.ID() {
		return
	}
	m.h.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.TempAddrTTL)
}
