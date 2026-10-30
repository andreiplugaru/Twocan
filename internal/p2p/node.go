// Package p2p runs Twocan's libp2p host: a private Kademlia DHT for finding
// peers, mDNS for the local network, NAT traversal and relay fallback.
package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	ma "github.com/multiformats/go-multiaddr"

	"twocan/internal/logging"
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
	// InterfaceIPs, if set, lists this machine's IPs for the addresses we
	// advertise, where libp2p cannot list them itself (see withInterfaceIPs).
	InterfaceIPs func() ([]net.IP, error)
	// Log receives connection, discovery and reachability events (nil: discard).
	Log *slog.Logger
}

// Node is a running libp2p host plus its DHT and discovery services.
type Node struct {
	host  host.Host
	dht   *dht.IpfsDHT
	mdns  mdns.Service
	reach atomic.Int32 // network.Reachability
	log   *slog.Logger

	boot         []peer.AddrInfo                     // the helpers
	addrsFactory func([]ma.Multiaddr) []ma.Multiaddr // adds Config.InterfaceIPs; nil if unset
	origins      *origins
	punchWait    time.Duration // Check's wait for a hole punch (0: default; tests shorten it)

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
	log := cfg.Log
	if log == nil {
		log = logging.Discard()
	}
	n := &Node{log: log.With("component", "p2p"), boot: cfg.Bootstrap, origins: newOrigins()}
	n.reach.Store(int32(network.ReachabilityUnknown))

	opts := []libp2p.Option{
		libp2p.Identity(cfg.PrivKey),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(holepunch.WithTracer(n.origins)),
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
	if cfg.InterfaceIPs != nil {
		n.addrsFactory = withInterfaceIPs(cfg.InterfaceIPs, n.log)
		opts = append(opts, libp2p.AddrsFactory(n.addrsFactory))
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		if n.dht != nil {
			_ = n.dht.Close()
		}
		return nil, fmt.Errorf("libp2p host: %w", err)
	}
	n.host = h
	n.log.Info("node started", "peer", h.ID(), "listen", addrStrings(h.Addrs()),
		"bootstrap", bootstrapStrings(cfg.Bootstrap), "mdns", cfg.EnableMDNS, "force_private", cfg.ForcePrivate)

	sub, err := h.EventBus().Subscribe([]any{
		new(event.EvtLocalReachabilityChanged),
		new(event.EvtLocalAddressesUpdated),
	})
	if err != nil {
		_ = n.dht.Close()
		_ = h.Close()
		return nil, fmt.Errorf("subscribe reachability: %w", err)
	}

	if cfg.EnableMDNS {
		n.mdns = mdns.NewMdnsService(h, MDNSService, mdnsNotifee{h: h, log: n.log, origins: n.origins})
		if err := n.mdns.Start(); err != nil {
			_ = sub.Close()
			_ = n.dht.Close()
			_ = h.Close()
			return nil, fmt.Errorf("start mdns: %w", err)
		}
	}

	bgCtx, cancel := context.WithCancel(ctx)
	n.cancel = cancel
	h.Network().Notify(n.newConnWatcher(bgCtx))
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
// to the addresses already in the peerstore and to id's relay address on
// each helper, else a dial to addresses found through the DHT. Relayed
// (limited) connections count. Earlier failed dials do not hold it back:
// its callers space out their own retries.
func (n *Node) Reach(ctx context.Context, id peer.ID) error {
	if connected, _ := n.ConnInfo(id); connected {
		return nil
	}
	ctx = network.WithAllowLimitedConn(ctx, "twocan")
	start := time.Now()
	n.clearBackoff(id)
	// The relay addresses need no lookup, which fails while the connection
	// to the helper is down or new (after a network switch). libp2p dials
	// them shortly after the direct ones, so stale direct addresses do not
	// hold the relay up.
	var helpers []peer.AddrInfo
	for _, b := range n.boot {
		if b.ID != id {
			n.clearBackoff(b.ID)
			helpers = append(helpers, b)
		}
	}
	relay := circuitAddrs(helpers)
	n.origins.addHelper(id, relay)

	// Dial errors from libp2p list every address tried and why it failed.
	if addrs := ma.Unique(append(n.host.Peerstore().Addrs(id), relay...)); len(addrs) > 0 {
		err := n.connect(ctx, peer.AddrInfo{ID: id, Addrs: addrs})
		if err == nil {
			n.log.Info("reached peer", "peer", id, "via", "known addresses", "took", time.Since(start).Round(time.Millisecond))
			return nil
		}
		n.log.Info("dial to known addresses failed", "peer", id, "addrs", addrStrings(addrs), "err", err)
	} else {
		n.log.Info("no known addresses for peer", "peer", id)
	}

	info, err := n.dht.FindPeer(ctx, id)
	if err == nil {
		n.log.Info("dht found peer", "peer", id, "addrs", addrStrings(info.Addrs))
		n.origins.addHelper(id, info.Addrs)
		if err = n.connect(ctx, info); err == nil {
			n.log.Info("reached peer", "peer", id, "via", "dht", "took", time.Since(start).Round(time.Millisecond))
			return nil
		}
	}
	n.log.Warn("peer unreachable", "peer", id, "took", time.Since(start).Round(time.Millisecond), "err", err)
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
		if !isCircuit(c.RemoteMultiaddr()) {
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
			switch e := e.(type) {
			case event.EvtLocalReachabilityChanged:
				n.reach.Store(int32(e.Reachability))
				n.log.Info("reachability changed", "status", e.Reachability.String())
			case event.EvtLocalAddressesUpdated:
				n.log.Info("own addresses changed", "addrs", addrStrings(n.host.Addrs()))
			}
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
			// Failures are retried by the DHT's refresh.
			if err := n.connect(ctx, p); err != nil {
				n.log.Warn("bootstrap peer unreachable", "peer", p.ID, "addrs", addrStrings(p.Addrs), "err", err)
			} else {
				n.log.Info("bootstrap peer connected", "peer", p.ID)
			}
		}()
	}
	wg.Wait()
	if ctx.Err() == nil {
		_ = n.dht.Bootstrap(ctx)
	}
}

// mdnsNotifee records peers found on the local network; it does not dial.
type mdnsNotifee struct {
	h       host.Host
	log     *slog.Logger
	origins *origins
}

func (m mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == m.h.ID() {
		return
	}
	m.log.Info("mdns found peer", "peer", pi.ID, "addrs", addrStrings(pi.Addrs))
	m.origins.addMDNS(pi.ID, pi.Addrs)
	m.h.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.TempAddrTTL)
}

// addrStrings renders multiaddrs for logging.
func addrStrings(addrs []ma.Multiaddr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

// bootstrapStrings renders bootstrap peers for logging.
func bootstrapStrings(peers []peer.AddrInfo) []string {
	out := make([]string, len(peers))
	for i, p := range peers {
		out[i] = p.String()
	}
	return out
}
