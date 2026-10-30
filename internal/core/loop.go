package core

import (
	"context"
	"errors"
	"time"

	libp2pevent "github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"twocan/internal/chat"
	"twocan/internal/event"
	"twocan/internal/p2p"
	"twocan/internal/store"
)

const (
	// defaultTick is how often the reconnect loop looks for pending work.
	defaultTick = 5 * time.Second
	// defaultPresence is how often accepted, unconnected contacts are reached.
	defaultPresence = 2 * time.Minute
	// Retry backoff per peer: starts at backoffMin, doubles, caps at backoffMax.
	backoffMin = 5 * time.Second
	backoffMax = 5 * time.Minute
	// defaultDirect is how often contacts connected only through the relay
	// are looked at, and how long after a failed direct dial the next one
	// is due. The wait doubles after each failure, up to maxDirectFactor
	// times this: on mobile data most direct dials fail, and each costs
	// battery and counts towards libp2p's "black hole" giving up on UDP
	// and IPv6.
	defaultDirect   = time.Minute
	maxDirectFactor = 30
	// directTimeout bounds one such dial.
	directTimeout = 30 * time.Second
	// A contact that disconnects is reached again at once, then after
	// defaultReconnect, doubling, reconnectTries times in all (about a
	// minute) before the presence sweep takes over: a network switch takes
	// a few seconds to settle, on either side.
	defaultReconnect = 2 * time.Second
	reconnectTries   = 6
	// reconnectTimeout bounds one such attempt.
	reconnectTimeout = 30 * time.Second
	// maxSavedAddrs caps the addrs stored for a contact on connect.
	maxSavedAddrs = 8
)

// intervals are the loop periods; tests shorten them (zero: default).
type intervals struct {
	tick, presence, direct, reconnect, settle time.Duration
}

// directDialer is what moving a contact off the relay needs from the libp2p
// node; *p2p.Node implements it.
type directDialer interface {
	DialDirect(ctx context.Context, id peer.ID) error
}

var _ directDialer = (*p2p.Node)(nil)

// addrSource gives the addresses we send contacts; *p2p.Node implements it.
type addrSource interface {
	ShareableAddrs() []ma.Multiaddr
}

var _ addrSource = (*p2p.Node)(nil)

// leaveState is the direct dialling of one contact we reach only through
// the relay.
type leaveState struct {
	busy  bool          // a dial is running
	delay time.Duration // the wait after the last failure; 0 before any
	due   time.Time     // no dial before this, unless new addresses arrive
}

// peerState is the reconnect loop's bookkeeping for one peer with pending work.
type peerState struct {
	delay  time.Duration // current backoff; 0 after success or reset
	due    time.Time     // no flush before this
	busy   bool          // a flush is running
	parked bool          // chat said ErrNotContact: wait for a contact change
	gen    uint64        // bumped by reset, so a running flush knows its result is stale
}

// nextBackoff returns the backoff after a failure, given the current one.
func nextBackoff(d time.Duration) time.Duration {
	if d == 0 {
		return backoffMin
	}
	return min(2*d, backoffMax)
}

// run is the node's event loop: libp2p connectedness and reachability
// events, contact changes, the reconnect tick and the presence sweep.
func (n *Node) run(ctx context.Context, p2pEvents <-chan any, busEvents <-chan any) {
	tick := time.NewTicker(n.iv.tick)
	defer tick.Stop()
	presence := time.NewTicker(n.iv.presence)
	defer presence.Stop()
	direct := time.NewTicker(n.iv.direct)
	defer direct.Stop()

	n.sweepPresence(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-p2pEvents:
			if !ok {
				return
			}
			switch e := e.(type) {
			case libp2pevent.EvtPeerConnectednessChanged:
				n.onConnectedness(ctx, e.Peer)
			case libp2pevent.EvtLocalReachabilityChanged:
				n.reach.Store(int32(e.Reachability))
				n.bus.Publish(event.Reachability{Status: e.Reachability})
			}
		case e, ok := <-busEvents:
			if !ok {
				return
			}
			if e, ok := e.(event.ContactChanged); ok {
				n.resetPeer(e.Peer)
			}
		case <-tick.C:
			n.flushDue(ctx)
		case <-presence.C:
			n.unparkAll()
			n.sweepPresence(ctx)
		case <-direct.C:
			n.leaveRelay(ctx)
		}
	}
}

// onConnectedness handles a connection change to id. For a contact it
// publishes Connectivity; on disconnect an accepted contact is reached
// again; on connect it also protects the connection, saves the peer's
// addrs, sends an accepted contact our addresses and flushes right away.
func (n *Node) onConnectedness(ctx context.Context, id peer.ID) {
	c, err := n.st.GetContact(ctx, id)
	if err != nil {
		return // not a contact (or the store failed): nothing to do
	}
	connected, relayed := n.connInfo(id)
	// Log before publishing, so the log line precedes anything the event triggers.
	if !connected {
		n.noteDisconnected(id)
		n.log.Info("contact disconnected", "peer", id)
		n.bus.Publish(event.Connectivity{Peer: id})
		if c.State == store.StateAccepted {
			n.reconnect(id)
		}
		return
	}
	var remotes []ma.Multiaddr
	for _, conn := range n.h.Network().ConnsToPeer(id) {
		remotes = append(remotes, conn.RemoteMultiaddr())
	}
	n.noteConnected(id)
	n.log.Info("contact connected", "peer", id, "relayed", relayed, "remote", addrStrings(remotes))
	n.bus.Publish(event.Connectivity{Peer: id, Connected: true, Relayed: relayed})
	if c.State != store.StateBlocked {
		n.h.ConnManager().Protect(id, protectTag)
	}
	n.saveAddrs(ctx, id)
	if c.State == store.StateAccepted {
		n.loops.Go(func(ctx context.Context) {
			if err := n.addrs.Send(ctx, id); err != nil {
				n.log.Info("could not send our addresses", "peer", id, "err", err)
			}
		})
	}
	n.resetPeer(id)
	n.schedule(id, time.Now())
}

// reconnect reaches id, an accepted contact that just disconnected, in the
// background: at once, then a few more times at growing intervals until it
// is connected again. At most one runs per contact.
func (n *Node) reconnect(id peer.ID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.rejoining[id]; ok {
		return
	}
	n.rejoining[id] = time.Now()
	if !n.loops.Go(func(ctx context.Context) { n.rejoin(ctx, id) }) {
		delete(n.rejoining, id)
	}
}

// rejoin is reconnect's work.
func (n *Node) rejoin(ctx context.Context, id peer.ID) {
	defer func() {
		n.mu.Lock()
		delete(n.rejoining, id)
		n.mu.Unlock()
	}()
	for try := range reconnectTries {
		if connected, _ := n.connInfo(id); connected {
			return
		}
		rctx, cancel := context.WithTimeout(ctx, reconnectTimeout)
		err := n.r.Reach(rctx, id)
		cancel()
		if err == nil {
			n.log.Info("reconnected", "peer", id, "try", try+1)
			return
		}
		if ctx.Err() != nil {
			return
		}
		if try == reconnectTries-1 {
			n.log.Info("could not reconnect; the presence sweep tries again", "peer", id, "err", err)
			return
		}
		wait := n.iv.reconnect << try
		n.mu.Lock()
		n.noteProblemLocked(id, err)
		n.rejoining[id] = time.Now().Add(wait)
		n.mu.Unlock()
		n.log.Info("could not reconnect yet", "peer", id, "try", try+1, "next_in", wait, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// contactAddrsArrived runs when contact id sent its addresses: they are
// saved and, if we are on the relay, tried at once.
func (n *Node) contactAddrsArrived(id peer.ID) {
	n.loops.Go(func(ctx context.Context) { n.saveAddrs(ctx, id) })
	n.tryDirect(id, true)
}

// leaveRelay tries a direct connection to every accepted contact we are
// connected to only through the relay.
func (n *Node) leaveRelay(ctx context.Context) {
	cs, err := n.st.ListContacts(ctx)
	if err != nil {
		return
	}
	for _, c := range cs {
		if c.State == store.StateAccepted {
			n.tryDirect(c.PeerID, false)
		}
	}
}

// tryDirect dials id directly in the background if we are connected to it
// only through the relay, no such dial is running and, unless now is set
// (new addresses), the wait after the last failure is over. On success the
// relayed connection is closed and Connectivity published.
func (n *Node) tryDirect(id peer.ID, now bool) {
	if n.dialer == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if connected, relayed := n.connInfo(id); !connected || !relayed {
		delete(n.leaving, id)
		return
	}
	ls, ok := n.leaving[id]
	if !ok {
		ls = &leaveState{}
		n.leaving[id] = ls
	}
	if ls.busy || (!now && time.Now().Before(ls.due)) {
		return
	}
	ls.busy = true
	if !n.loops.Go(func(ctx context.Context) { n.dialDirect(ctx, id, ls) }) {
		ls.busy = false
	}
}

// dialDirect is tryDirect's dial.
func (n *Node) dialDirect(ctx context.Context, id peer.ID, ls *leaveState) {
	dctx, cancel := context.WithTimeout(ctx, directTimeout)
	err := n.dialer.DialDirect(dctx, id)
	cancel()

	n.mu.Lock()
	ls.busy = false
	if err != nil {
		ls.delay = min(max(2*ls.delay, n.iv.direct), maxDirectFactor*n.iv.direct)
		ls.due = time.Now().Add(ls.delay)
		next := ls.delay
		n.mu.Unlock()
		n.log.Debug("still on the relay: direct dial failed", "peer", id, "next_in", next, "err", err)
		return
	}
	delete(n.leaving, id)
	n.mu.Unlock()
	n.log.Info("moved off the relay: connected directly", "peer", id)
	n.saveAddrs(ctx, id)
	connected, relayed := n.connInfo(id)
	n.bus.Publish(event.Connectivity{Peer: id, Connected: connected, Relayed: relayed})
}

// saveAddrs stores up to maxSavedAddrs of id's non-loopback peerstore
// addrs, direct ones first: relay addresses can be rebuilt from the helper,
// so they must not crowd out the direct ones.
func (n *Node) saveAddrs(ctx context.Context, id peer.ID) {
	// Saves run one at a time, so a slow one cannot store its older view
	// of the peerstore over a newer one (such as the addresses a contact
	// just sent).
	n.saveMu.Lock()
	defer n.saveMu.Unlock()
	var direct, relay []ma.Multiaddr
	for _, a := range n.h.Peerstore().Addrs(id) {
		switch {
		case manet.IsIPLoopback(a):
		case isCircuit(a):
			relay = append(relay, a)
		default:
			direct = append(direct, a)
		}
	}
	addrs := append(direct, relay...)
	addrs = addrs[:min(len(addrs), maxSavedAddrs)]
	if len(addrs) > 0 {
		// Best effort; the next connect retries.
		if err := n.st.SetAddrs(ctx, id, addrs); err != nil {
			n.log.Warn("could not save contact addresses", "peer", id, "err", err)
			return
		}
		n.log.Debug("saved contact addresses", "peer", id, "addrs", addrStrings(addrs))
	}
}

// flushDue starts a flush for every peer with pending work whose backoff is due.
func (n *Node) flushDue(ctx context.Context) {
	ids, err := n.st.PeersWithPendingWork(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for _, id := range ids {
		n.schedule(id, now)
	}
}

// schedule starts a background flush of id unless one is running, id is
// parked or its backoff is not yet due at now.
func (n *Node) schedule(id peer.ID, now time.Time) {
	n.mu.Lock()
	ps, ok := n.peers[id]
	if !ok {
		ps = &peerState{}
		n.peers[id] = ps
	}
	if ps.busy || ps.parked || now.Before(ps.due) {
		n.mu.Unlock()
		return
	}
	ps.busy = true
	gen := ps.gen
	n.mu.Unlock()

	if !n.loops.Go(func(ctx context.Context) { n.flushPeer(ctx, id, gen) }) {
		n.mu.Lock()
		ps.busy = false
		n.mu.Unlock()
	}
}

// flushPeer sends what we owe id: the contact frame first, then queued
// messages. A failure backs off; chat.ErrNotContact (id is no longer an
// accepted contact; its messages stay queued) parks id until its contact
// changes, without backoff; success forgets id.
func (n *Node) flushPeer(ctx context.Context, id peer.ID, gen uint64) {
	err := n.contacts.Flush(ctx, id)
	notContact := false
	if err == nil {
		err = n.chat.Flush(ctx, id)
		if errors.Is(err, chat.ErrNotContact) {
			notContact, err = true, nil
		}
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	ps := n.peers[id] // busy entries are never removed
	ps.busy = false
	switch {
	case ps.gen != gen:
		// Reset while we ran (connect or contact change): our result is
		// stale, so retry on the next tick without penalty.
	case err != nil:
		if ctx.Err() == nil {
			n.noteProblemLocked(id, err)
		}
		ps.delay = nextBackoff(ps.delay)
		ps.due = time.Now().Add(ps.delay)
		n.log.Info("send failed; will retry", "peer", id, "retry_in", ps.delay, "err", err)
	case notContact:
		ps.parked = true
		n.log.Info("queued messages held: peer is not an accepted contact", "peer", id)
	default:
		delete(n.peers, id)
	}
}

// resetPeer clears id's backoff and parking: something changed (a new
// connection or a contact change), so it is worth trying again now.
func (n *Node) resetPeer(id peer.ID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ps, ok := n.peers[id]
	if !ok {
		return
	}
	if !ps.busy {
		delete(n.peers, id)
		return
	}
	ps.gen++
	ps.delay, ps.due, ps.parked = 0, time.Time{}, false
}

// unparkAll lets parked peers be checked again, in case a ContactChanged
// event was dropped. Checking a parked peer costs only store reads.
func (n *Node) unparkAll() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ps := range n.peers {
		ps.parked = false
	}
}

// sweepPresence reaches every accepted contact we are not connected to; a
// resulting connection triggers onConnectedness.
func (n *Node) sweepPresence(ctx context.Context) {
	n.mu.Lock()
	n.nextSweep = time.Now().Add(n.iv.presence)
	n.mu.Unlock()
	cs, err := n.st.ListContacts(ctx)
	if err != nil {
		return
	}
	for _, c := range cs {
		if c.State != store.StateAccepted {
			continue
		}
		if connected, _ := n.connInfo(c.PeerID); connected {
			continue
		}
		n.log.Debug("checking on offline contact", "peer", c.PeerID)
		id := c.PeerID
		n.loops.Go(func(ctx context.Context) {
			rctx, cancel := context.WithTimeout(ctx, n.iv.presence)
			defer cancel()
			// Only shutting down (ctx done) is not a problem worth showing.
			if err := n.r.Reach(rctx, id); err != nil && ctx.Err() == nil {
				n.mu.Lock()
				n.noteProblemLocked(id, err)
				n.mu.Unlock()
			}
		})
	}
}

// addrStrings renders multiaddrs for logging.
func addrStrings(addrs []ma.Multiaddr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}
