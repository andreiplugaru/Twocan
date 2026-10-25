package core

import (
	"context"
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"

	"twocan/internal/p2p"
	"twocan/internal/store"
)

// pingTimeout bounds Ping.
const pingTimeout = 10 * time.Second

var (
	// ErrNotConnected is returned by Ping when there is no connection to ping over.
	ErrNotConnected = errors.New("not connected")
	// errNoDiagnostics is returned when the reacher cannot run a check
	// (only under startWith in tests).
	errNoDiagnostics = errors.New("connection checks are not available")
)

// diagnoser is what ContactDetails and CheckConnection need from the libp2p
// node; *p2p.Node implements it.
type diagnoser interface {
	Conns(id peer.ID) []p2p.Conn
	LastHolePunch(id peer.ID) (p2p.HolePunch, bool)
	Check(ctx context.Context, id peer.ID, report func(int, p2p.Step)) p2p.CheckResult
}

var _ diagnoser = (*p2p.Node)(nil)

// ContactDetails is what the node knows about its connection to a contact.
// LastSeen and the last problem are kept in memory, so they cover this
// session only.
type ContactDetails struct {
	Contact store.Contact // Addrs are the saved addresses, tried after a restart
	Conns   []p2p.Conn    // open connections, oldest first
	Latency time.Duration // smoothed ping time; 0 if never measured
	// LastSeen is when the last connection closed; zero if connected or not
	// seen this session.
	LastSeen time.Time
	// LastProblem says in plain words why the latest attempt to reach them
	// failed; "" if none since they were last connected.
	LastProblem   string
	LastProblemAt time.Time
	// NextAttempt is when the node next tries to reach them; zero when
	// connected or when nothing is scheduled.
	NextAttempt time.Time
	HolePunch   p2p.HolePunch // the latest hole punch; zero At if none
}

// health is the in-memory connection history of one contact.
type health struct {
	lastSeen  time.Time
	problem   error
	problemAt time.Time
}

// healthOf returns id's entry, creating it. n.mu must be held.
func (n *Node) healthOf(id peer.ID) *health {
	h, ok := n.health[id]
	if !ok {
		h = &health{}
		n.health[id] = h
	}
	return h
}

// noteConnected clears id's last problem: it is reachable now.
func (n *Node) noteConnected(id peer.ID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	h := n.healthOf(id)
	h.lastSeen, h.problem, h.problemAt = time.Time{}, nil, time.Time{}
}

// noteDisconnected records when id was last seen.
func (n *Node) noteDisconnected(id peer.ID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.healthOf(id).lastSeen = time.Now()
}

// noteProblemLocked records why reaching id failed. n.mu must be held.
func (n *Node) noteProblemLocked(id peer.ID, err error) {
	h := n.healthOf(id)
	h.problem, h.problemAt = err, time.Now()
}

// ContactDetails describes the connection to contact id; store.ErrNotFound
// if id is not a contact.
func (n *Node) ContactDetails(ctx context.Context, id peer.ID) (ContactDetails, error) {
	c, err := n.st.GetContact(ctx, id)
	if err != nil {
		return ContactDetails{}, err
	}
	d := ContactDetails{Contact: c, Latency: n.h.Peerstore().LatencyEWMA(id)}
	if n.diag != nil {
		d.Conns = n.diag.Conns(id)
		d.HolePunch, _ = n.diag.LastHolePunch(id)
	}
	connected, _ := n.connInfo(id)

	n.mu.Lock()
	defer n.mu.Unlock()
	if h, ok := n.health[id]; ok {
		d.LastSeen = h.lastSeen
		if h.problem != nil {
			d.LastProblem, d.LastProblemAt = p2p.Explain(h.problem), h.problemAt
		}
	}
	if !connected {
		d.NextAttempt = n.nextAttemptLocked(id, c.State, time.Now())
	}
	return d, nil
}

// nextAttemptLocked is when id is next reached: its retry, if it has
// pending work, or the presence sweep for an accepted contact, whichever
// comes first. n.mu must be held.
func (n *Node) nextAttemptLocked(id peer.ID, state store.ContactState, now time.Time) time.Time {
	var next time.Time
	if ps, ok := n.peers[id]; ok && !ps.parked {
		next = ps.due
		if next.Before(now) {
			next = now // due already: on the next tick
		}
	}
	if state == store.StateAccepted && !n.nextSweep.IsZero() && (next.IsZero() || n.nextSweep.Before(next)) {
		next = n.nextSweep
	}
	return next
}

// Ping measures the round trip to a connected peer (relayed connections
// count). The result also feeds ContactDetails' Latency.
func (n *Node) Ping(ctx context.Context, id peer.ID) (time.Duration, error) {
	if connected, _ := n.connInfo(id); !connected {
		return 0, ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(network.WithAllowLimitedConn(ctx, "twocan-ping"), pingTimeout)
	defer cancel()
	res := <-ping.Ping(ctx, n.h, id)
	return res.RTT, res.Error
}

// CheckConnection runs a step-by-step connection check to contact id (see
// p2p.Node.Check); report gets each step as it starts and ends. It returns
// store.ErrNotFound if id is not a contact.
func (n *Node) CheckConnection(ctx context.Context, id peer.ID, report func(int, p2p.Step)) (p2p.CheckResult, error) {
	if _, err := n.st.GetContact(ctx, id); err != nil {
		return p2p.CheckResult{}, err
	}
	if n.diag == nil {
		return p2p.CheckResult{}, errNoDiagnostics
	}
	return n.diag.Check(ctx, id, report), nil
}
