package p2p

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	// maxTrackedPeers bounds the origins tables; past it they start over, so
	// a LAN full of mDNS peers cannot grow them without limit.
	maxTrackedPeers = 256
	// maxTrackedAddrs bounds the addrs remembered per peer and table.
	maxTrackedAddrs = 32
	// punchWindow is how close a direct connection's opening must be to a
	// successful hole punch to count as its result.
	punchWindow = 10 * time.Second
)

// Source is how a connection to a peer came about: who dialled, and where
// the address came from.
type Source int

const (
	SourceUnknown   Source = iota
	SourceInbound          // they dialled us
	SourceMDNS             // we dialled an address found on the local network
	SourceHelper           // we dialled an address from the helper: a DHT lookup or its relay
	SourceHolePunch        // hole punching upgraded a relayed connection
	SourceKnown            // we dialled an address we already had: saved, from their card, or learned earlier
)

// String describes s for the user.
func (s Source) String() string {
	switch s {
	case SourceInbound:
		return "they connected to you"
	case SourceMDNS:
		return "your local network (mDNS)"
	case SourceHelper:
		return "the helper"
	case SourceHolePunch:
		return "hole punching (upgraded from the relay)"
	case SourceKnown:
		return "an address you already had (saved or from their card)"
	}
	return "unknown"
}

// Conn describes one open connection to a peer.
type Conn struct {
	Remote    ma.Multiaddr
	Transport string // e.g. "QUIC/IPv4", "TCP/IPv6", "relay"
	Relayed   bool
	Inbound   bool
	Opened    time.Time
	Source    Source
}

// HolePunch is the outcome of the latest hole punch with a peer.
type HolePunch struct {
	At  time.Time // zero: none this session
	OK  bool
	Err string
}

// origins remembers where peers' addrs came from and how hole punches went,
// so a connection can be traced back to how the peers found each other.
type origins struct {
	mu      sync.Mutex
	mdns    map[peer.ID]map[string]struct{}
	helper  map[peer.ID]map[string]struct{}
	punches map[peer.ID]HolePunch
}

var _ holepunch.EventTracer = (*origins)(nil)

func newOrigins() *origins {
	return &origins{
		mdns:    make(map[peer.ID]map[string]struct{}),
		helper:  make(map[peer.ID]map[string]struct{}),
		punches: make(map[peer.ID]HolePunch),
	}
}

func (o *origins) addMDNS(id peer.ID, addrs []ma.Multiaddr)   { o.add(o.mdns, id, addrs) }
func (o *origins) addHelper(id peer.ID, addrs []ma.Multiaddr) { o.add(o.helper, id, addrs) }

func (o *origins) add(table map[peer.ID]map[string]struct{}, id peer.ID, addrs []ma.Multiaddr) {
	o.mu.Lock()
	defer o.mu.Unlock()
	set, ok := table[id]
	if !ok {
		if len(table) >= maxTrackedPeers {
			clear(table)
		}
		set = make(map[string]struct{})
		table[id] = set
	}
	for _, a := range addrs {
		if len(set) >= maxTrackedAddrs {
			return
		}
		set[addrKey(id, a)] = struct{}{}
	}
}

// Trace records the end of each hole punch (holepunch.EventTracer).
func (o *origins) Trace(evt *holepunch.Event) {
	var hp HolePunch
	switch e := evt.Evt.(type) {
	case *holepunch.EndHolePunchEvt:
		hp = HolePunch{OK: e.Success, Err: e.Error}
	case *holepunch.DirectDialEvt:
		if !e.Success {
			return // a hole punch follows
		}
		hp = HolePunch{OK: true}
	default:
		return
	}
	hp.At = time.Unix(0, evt.Timestamp)
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.punches[evt.Remote]; !ok && len(o.punches) >= maxTrackedPeers {
		clear(o.punches)
	}
	o.punches[evt.Remote] = hp
}

func (o *origins) punch(id peer.ID) (HolePunch, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	hp, ok := o.punches[id]
	return hp, ok
}

// source classifies one connection to id.
func (o *origins) source(id peer.ID, c network.Conn) Source {
	remote := c.RemoteMultiaddr()
	stat := c.Stat()
	relayed := isCircuit(remote)
	if hp, ok := o.punch(id); ok && hp.OK && !relayed && absDuration(stat.Opened.Sub(hp.At)) < punchWindow {
		return SourceHolePunch
	}
	if stat.Direction == network.DirInbound {
		return SourceInbound
	}
	if relayed {
		return SourceHelper // the helper is the only relay
	}
	key := addrKey(id, remote)
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.mdns[id][key]; ok {
		return SourceMDNS
	}
	if _, ok := o.helper[id][key]; ok {
		return SourceHelper
	}
	return SourceKnown
}

// addrKey is a's string without a trailing /p2p/<id>.
func addrKey(id peer.ID, a ma.Multiaddr) string {
	return strings.TrimSuffix(a.String(), "/p2p/"+id.String())
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// Conns describes the open connections to id, oldest first.
func (n *Node) Conns(id peer.ID) []Conn {
	var out []Conn
	for _, c := range n.host.Network().ConnsToPeer(id) {
		remote := c.RemoteMultiaddr()
		stat := c.Stat()
		out = append(out, Conn{
			Remote:    remote,
			Transport: transportName(remote),
			Relayed:   isCircuit(remote),
			Inbound:   stat.Direction == network.DirInbound,
			Opened:    stat.Opened,
			Source:    n.origins.source(id, c),
		})
	}
	for i := 1; i < len(out); i++ { // few conns: insertion sort by Opened
		for j := i; j > 0 && out[j].Opened.Before(out[j-1].Opened); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// LastHolePunch returns the latest hole punch with id this session.
func (n *Node) LastHolePunch(id peer.ID) (HolePunch, bool) { return n.origins.punch(id) }

func isCircuit(a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}

// transportName names a's transport and IP version, e.g. "QUIC/IPv4".
func transportName(a ma.Multiaddr) string {
	if isCircuit(a) {
		return "relay"
	}
	var ip, tr string
	for _, c := range a {
		switch c.Protocol().Code {
		case ma.P_IP4:
			ip = "IPv4"
		case ma.P_IP6:
			ip = "IPv6"
		case ma.P_TCP:
			tr = "TCP"
		case ma.P_QUIC_V1:
			tr = "QUIC"
		case ma.P_WEBTRANSPORT:
			tr = "WebTransport"
		}
	}
	switch {
	case tr == "" && ip == "":
		return "unknown"
	case tr == "":
		return ip
	case ip == "":
		return tr
	}
	return tr + "/" + ip
}

// Explain turns an error from reaching a peer into plain words. Unknown
// errors come back as they are.
func Explain(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case errors.Is(err, routing.ErrNotFound):
		return "The helper doesn't know where they are: they're offline, or their app isn't connected to the helper."
	case strings.Contains(s, "failed to find any peer in table"):
		return "No helper is connected, so there is nobody to ask where they are."
	case strings.Contains(s, "NO_RESERVATION"):
		return "They have no relay slot on the helper, so the relay can't pass the connection on to them."
	case strings.Contains(s, "no addresses") || strings.Contains(s, "no good addresses"):
		return "No usable address is known for them."
	case strings.Contains(s, "connection refused"):
		return "Their address refused the connection: the app isn't running there any more (it picks new ports at each start)."
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "timeout"):
		return "No answer: they may be offline, or a firewall drops the connection."
	case strings.Contains(s, "no route to host") || strings.Contains(s, "network is unreachable"):
		return "Your computer has no route to their addresses (for example, no IPv6)."
	case strings.Contains(s, "dial backoff"):
		return "A dial to them failed moments ago; Twocan waits a little before trying again."
	case errors.Is(err, context.Canceled):
		return "Cancelled."
	}
	return s
}
