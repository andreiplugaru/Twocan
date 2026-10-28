package p2p

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/observedaddrs"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

func init() {
	// libp2p counts an address others see us at (behind a NAT, our public
	// one) only once 4 peers have reported it. A Twocan network has a
	// single helper, the only peer that reports it to us, so it never
	// counted. One report is enough here.
	observedaddrs.ActivationThresh = 1
}

// ShareableAddrs returns every address a contact might reach us at, public
// ones included. Host().Addrs() leaves public addresses out while we are
// behind a NAT and have a relay address (it offers the relay instead), but
// a contact may still get through to them: an IPv6 address, a port the
// router forwards, or one a hole punch opens.
func (n *Node) ShareableAddrs() []ma.Multiaddr {
	all := n.host.Addrs()
	if h, ok := n.host.(interface{ AllAddrs() []ma.Multiaddr }); ok {
		direct := h.AllAddrs()
		if n.addrsFactory != nil {
			direct = n.addrsFactory(direct)
		}
		all = append(all, direct...)
	}
	return ma.Unique(all)
}

// errNoDirectAddrs is returned by DialDirect when no direct address is known.
var errNoDirectAddrs = errors.New("no addresses: no direct address is known")

// DialDirect connects to id without a relay, through id's known direct
// addresses (it does not look them up). Once a direct connection is open,
// the relayed ones are closed, freeing their slot on the helper's relay. If
// a direct connection is already open, it only closes the relayed ones.
func (n *Node) DialDirect(ctx context.Context, id peer.ID) error {
	if n.directConn(id) == nil {
		addrs := n.directAddrs(id)
		if len(addrs) == 0 {
			return errNoDirectAddrs
		}
		n.clearBackoff(id)
		ctx = network.WithForceDirectDial(ctx, "twocan-direct")
		if err := n.connect(ctx, peer.AddrInfo{ID: id, Addrs: addrs}); err != nil {
			return err
		}
		if n.directConn(id) == nil {
			return errors.New("connected, but not directly")
		}
	}
	n.closeRelayed(id)
	return nil
}

// closeRelayed closes the relayed connections to id.
func (n *Node) closeRelayed(id peer.ID) {
	for _, c := range n.host.Network().ConnsToPeer(id) {
		if isCircuit(c.RemoteMultiaddr()) {
			n.log.Info("closing relayed connection: direct one open", "peer", id, "remote", c.RemoteMultiaddr())
			_ = c.Close()
		}
	}
}

// directAddrs returns id's known addresses that are not through a relay.
func (n *Node) directAddrs(id peer.ID) []ma.Multiaddr {
	var addrs []ma.Multiaddr
	for _, a := range n.host.Peerstore().Addrs(id) {
		if !isCircuit(a) {
			addrs = append(addrs, a)
		}
	}
	return addrs
}

// directConn returns an open direct connection to id, or nil.
func (n *Node) directConn(id peer.ID) network.Conn {
	for _, conn := range n.host.Network().ConnsToPeer(id) {
		if !isCircuit(conn.RemoteMultiaddr()) {
			return conn
		}
	}
	return nil
}

// clearBackoff forgets recent failed dials to id, so the next dial tries
// every address again.
func (n *Node) clearBackoff(id peer.ID) {
	if sw, ok := n.host.Network().(*swarm.Swarm); ok {
		sw.Backoff().Clear(id)
	}
}

// DialFailures lists, one line per address, why each address in a failed
// dial did not work, e.g. "/ip4/192.168.1.5/tcp/4001: refused". It is
// empty if err does not list addresses.
func DialFailures(err error) []string {
	var de *swarm.DialError
	if !errors.As(err, &de) {
		return nil
	}
	out := make([]string, 0, len(de.DialErrors))
	for _, te := range de.DialErrors {
		out = append(out, fmt.Sprintf("%s: %s", te.Address, shortCause(te.Cause)))
	}
	return out
}

// shortCause says in a few words why one address failed.
func shortCause(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "connection refused"):
		return "refused (nothing listens there)"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "timeout"):
		return "no answer"
	case strings.Contains(s, "black hole"):
		return "not tried: too many failed UDP or IPv6 dials lately"
	case strings.Contains(s, "no route to host") || strings.Contains(s, "network is unreachable"):
		return "no route"
	case strings.Contains(s, "dial backoff"):
		return "not tried: it failed moments ago"
	case strings.Contains(s, "peer id mismatch"):
		return "someone else answers there"
	}
	return s
}
