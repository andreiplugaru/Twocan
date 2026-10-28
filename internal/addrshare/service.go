// Package addrshare tells contacts where we are.
//
// libp2p passes on only public addresses over a connection that comes from
// a public address, and a relayed connection counts as one. So two contacts
// on the same LAN who meet through the helper's relay never learn each
// other's LAN addresses from libp2p, nor from the helper, which only sees
// public ones. Without mDNS (as on Android) they would stay on the relay.
//
// When a contact connects, each side sends its own direct addresses, LAN
// ones included, as one Addrs frame on a stream of its own. The receiver
// makes them the contact's direct addresses, dropping ones that are out of
// date, and calls back so the node can try a direct connection. Frames are
// accepted only from accepted contacts, who see our LAN address on our card
// anyway.
package addrshare

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"twocan/internal/logging"
	pb "twocan/internal/proto"
	"twocan/internal/store"
	"twocan/internal/svc"
	"twocan/internal/wire"
)

// ProtocolID is the stream protocol for Addrs frames.
const ProtocolID = "/twocan/addrs/1.0.0"

const (
	// exchangeTimeout bounds sending or receiving one frame.
	exchangeTimeout = 15 * time.Second
	// MaxAddrs caps the addresses sent in, and kept from, one frame.
	MaxAddrs = 16
)

// errNotContact rejects a frame from a peer that is not an accepted contact.
var errNotContact = errors.New("addrshare: sender is not an accepted contact")

// Service sends our addresses to contacts and takes in theirs.
type Service struct {
	h  host.Host
	st *store.Store
	// onAddrs is called after a contact's addresses have been taken in.
	onAddrs func(peer.ID)
	// own returns the addresses to send; h.Addrs unless a test replaces it.
	own func() []ma.Multiaddr

	tasks *svc.Tracker
	log   *slog.Logger
}

// New returns a Service. onAddrs (if not nil) is called, on the stream
// handler's goroutine, after a contact's addresses arrived. Call Start to
// accept inbound streams.
func New(h host.Host, st *store.Store, onAddrs func(peer.ID)) *Service {
	return &Service{h: h, st: st, onAddrs: onAddrs, own: h.Addrs, tasks: svc.NewTracker(), log: logging.Discard()}
}

// SetLogger sets where the service logs; call it before Start.
func (s *Service) SetLogger(l *slog.Logger) { s.log = l.With("component", "addrshare") }

// Start registers the inbound stream handler.
func (s *Service) Start() { s.h.SetStreamHandler(ProtocolID, s.handleStream) }

// Close removes the stream handler and waits for in-flight handlers.
func (s *Service) Close() {
	s.h.RemoveStreamHandler(ProtocolID)
	s.tasks.Close()
}

// Send sends our direct addresses to id, over a relayed connection too. It
// does nothing if we have none.
func (s *Service) Send(ctx context.Context, id peer.ID) error {
	addrs := Direct(s.own())
	if len(addrs) == 0 {
		return nil
	}
	f := &pb.Addrs{}
	for _, a := range addrs {
		f.Addrs = append(f.Addrs, a.Bytes())
	}
	ctx, cancel := context.WithTimeout(network.WithAllowLimitedConn(ctx, "twocan-addrs"), exchangeTimeout)
	defer cancel()
	str, err := s.h.NewStream(ctx, id, ProtocolID)
	if err != nil {
		return fmt.Errorf("addrshare: open stream: %w", err)
	}
	defer svc.Guard(ctx, str)()
	if err := wire.WriteFrame(str, f); err != nil {
		str.Reset()
		return fmt.Errorf("addrshare: send: %w", err)
	}
	s.log.Debug("sent our addresses", "peer", id, "addrs", addrStrings(addrs))
	return str.Close()
}

func (s *Service) handleStream(str network.Stream) {
	if !s.tasks.Track() {
		str.Reset()
		return
	}
	defer s.tasks.Done()
	remote := str.Conn().RemotePeer()
	ctx, cancel := svc.Timeout(s.tasks.Context(), str, exchangeTimeout)
	defer cancel()
	addrs, err := s.receive(ctx, str, remote)
	if err != nil {
		s.log.Info("addresses not taken", "peer", remote, "err", err)
		str.Reset()
		return
	}
	str.Close()
	s.take(remote, addrs)
	s.log.Info("contact sent their addresses", "peer", remote, "addrs", addrStrings(addrs))
	if s.onAddrs != nil {
		s.onAddrs(remote)
	}
}

// receive checks that remote is an accepted contact and reads its frame.
func (s *Service) receive(ctx context.Context, str network.Stream, remote peer.ID) ([]ma.Multiaddr, error) {
	c, err := s.st.GetContact(ctx, remote)
	if err != nil || c.State != store.StateAccepted {
		return nil, errNotContact
	}
	var f pb.Addrs
	if err := wire.ReadFrame(bufio.NewReader(str), &f); err != nil {
		return nil, err
	}
	var addrs []ma.Multiaddr
	for _, b := range f.Addrs {
		if a, err := ma.NewMultiaddrBytes(b); err == nil {
			addrs = append(addrs, a)
		}
	}
	return Direct(addrs), nil
}

// take makes addrs id's direct addresses: other direct ones in the
// peerstore are out of date and dropped; relay addresses stay.
func (s *Service) take(id peer.ID, addrs []ma.Multiaddr) {
	ps := s.h.Peerstore()
	fresh := map[string]bool{}
	for _, a := range addrs {
		fresh[string(a.Bytes())] = true
	}
	for _, a := range ps.Addrs(id) {
		if !isCircuit(a) && !fresh[string(a.Bytes())] {
			ps.SetAddr(id, a, 0) // a TTL of 0 removes it
		}
	}
	ps.AddAddrs(id, addrs, peerstore.AddressTTL)
}

// Direct returns at most MaxAddrs of addrs that another device could dial
// directly: no relay, loopback or link-local addresses.
func Direct(addrs []ma.Multiaddr) []ma.Multiaddr {
	var out []ma.Multiaddr
	for _, a := range addrs {
		if len(out) == MaxAddrs {
			break
		}
		if isCircuit(a) || manet.IsIPLoopback(a) || manet.IsIP6LinkLocal(a) || isIP4LinkLocal(a) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func isCircuit(a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}

// isIP4LinkLocal reports a 169.254.0.0/16 address.
func isIP4LinkLocal(a ma.Multiaddr) bool {
	ip, err := manet.ToIP(a)
	return err == nil && ip.To4() != nil && ip.IsLinkLocalUnicast()
}

func addrStrings(addrs []ma.Multiaddr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}
