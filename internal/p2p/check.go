package p2p

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	// lookupTimeout bounds the check's DHT lookup.
	lookupTimeout = 15 * time.Second
	// relayDialTimeout bounds the check's dial through the relay.
	relayDialTimeout = 15 * time.Second
	// defaultPunchWait is how long the check waits for a relayed connection
	// to be upgraded to a direct one.
	defaultPunchWait = 20 * time.Second
	// punchPoll is how often the check looks for that upgrade.
	punchPoll = 250 * time.Millisecond
)

// StepStatus is the state of one step of a connection check.
type StepStatus int

const (
	StepRunning StepStatus = iota
	StepOK
	StepInfo // neither good nor bad: a fact worth knowing
	StepWarn
	StepFail
	StepSkipped
)

// Step is one step of a connection check. Title and Detail are for the user.
type Step struct {
	Title  string
	Status StepStatus
	Detail string
}

// The check's steps, in order.
const (
	stepHelper = iota
	stepReach
	stepSlot
	stepLookup
	stepDirect
	stepRelay
	stepPunch
	numSteps
)

var stepTitles = [numSteps]string{
	stepHelper: "Helper",
	stepReach:  "Your reachability",
	stepSlot:   "Your relay slot",
	stepLookup: "Helper knows them",
	stepDirect: "Direct connection",
	stepRelay:  "Relay connection",
	stepPunch:  "Hole punch",
}

// CheckResult is the outcome of a connection check.
type CheckResult struct {
	Steps              []Step
	Verdict            string // one line for the user
	Connected, Relayed bool   // the connection state at the end
}

// checker runs one connection check.
type checker struct {
	n      *Node
	id     peer.ID
	report func(int, Step)
	steps  [numSteps]Step

	helpers   []peer.AddrInfo // helpers we are connected to
	found     bool            // the helper knew them
	relayErr  error           // why the relay dial failed
	lastErr   error           // the latest dial or lookup error
	start     time.Time
	punchWait time.Duration
}

// Check works out, step by step, whether and how id can be reached: the
// helper, our own reachability and relay slot, a lookup, a direct dial, a
// dial through the relay and a hole punch. report (if not nil) is called
// with each step's index as it starts and again as it ends. Successful
// dials leave their connections open.
func (n *Node) Check(ctx context.Context, id peer.ID, report func(int, Step)) CheckResult {
	c := &checker{n: n, id: id, report: report, start: time.Now(), punchWait: n.punchWait}
	if c.punchWait == 0 {
		c.punchWait = defaultPunchWait
	}
	for i, run := range []func(context.Context) (StepStatus, string){
		stepHelper: c.helper,
		stepReach:  c.reachability,
		stepSlot:   c.slot,
		stepLookup: c.lookup,
		stepDirect: c.direct,
		stepRelay:  c.relay,
		stepPunch:  c.holePunch,
	} {
		c.set(i, StepRunning, "")
		if ctx.Err() != nil {
			c.set(i, StepSkipped, "Cancelled.")
			continue
		}
		status, detail := run(ctx)
		c.set(i, status, detail)
	}
	connected, relayed := n.ConnInfo(id)
	res := CheckResult{Steps: c.steps[:], Connected: connected, Relayed: relayed, Verdict: c.verdict(connected, relayed)}
	n.log.Info("connection check", "peer", id, "connected", connected, "relayed", relayed, "verdict", res.Verdict)
	return res
}

func (c *checker) set(i int, status StepStatus, detail string) {
	c.steps[i] = Step{Title: stepTitles[i], Status: status, Detail: detail}
	if c.report != nil {
		c.report(i, c.steps[i])
	}
}

func (c *checker) helper(ctx context.Context) (StepStatus, string) {
	if len(c.n.boot) == 0 {
		return StepSkipped, "No helper configured (set one with --bootstrap)."
	}
	var err error
	for _, b := range c.n.boot {
		if c.n.host.Network().Connectedness(b.ID) != network.Connected {
			c.clearBackoff(b.ID)
			if e := c.n.connect(ctx, b); e != nil {
				err = e
				continue
			}
		}
		c.helpers = append(c.helpers, b)
	}
	if len(c.helpers) == 0 {
		c.lastErr = err
		return StepFail, "Can't reach the helper: " + Explain(err) + " Check its address, and that TCP and UDP port 4001 are open on it."
	}
	detail := "Connected to the helper"
	if conns := c.n.host.Network().ConnsToPeer(c.helpers[0].ID); len(conns) > 0 {
		detail += " at " + conns[0].RemoteMultiaddr().String()
	}
	return StepOK, detail + "."
}

func (c *checker) reachability(context.Context) (StepStatus, string) {
	switch c.n.Reachability() {
	case network.ReachabilityPublic:
		return StepInfo, "Public: others can connect to you directly."
	case network.ReachabilityPrivate:
		return StepInfo, "Behind a NAT: people outside your network reach you through the relay or hole punching."
	}
	if len(c.helpers) == 0 {
		return StepWarn, "Unknown: it takes a helper to find out."
	}
	return StepWarn, "Unknown: the helper is still checking."
}

func (c *checker) slot(context.Context) (StepStatus, string) {
	for _, a := range c.n.host.Addrs() {
		if isCircuit(a) {
			return StepOK, "You have a relay address, so contacts outside your network can reach you through the helper."
		}
	}
	switch {
	case len(c.helpers) == 0:
		return StepSkipped, "No helper to hold one on."
	case c.n.Reachability() == network.ReachabilityPublic:
		return StepSkipped, "Not needed: you're publicly reachable."
	case c.n.Reachability() == network.ReachabilityPrivate:
		return StepFail, "None yet: people outside your network can't reach you until you have one."
	}
	return StepWarn, "None yet: you take one once the helper finds you're behind a NAT."
}

func (c *checker) lookup(ctx context.Context) (StepStatus, string) {
	if connected, _ := c.n.ConnInfo(c.id); connected {
		return StepSkipped, "Already connected, so no lookup is needed."
	}
	if len(c.helpers) == 0 {
		return StepSkipped, "No helper to ask."
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	info, err := c.n.dht.FindPeer(ctx, c.id)
	if err != nil {
		c.lastErr = err
		if errors.Is(err, routing.ErrNotFound) {
			return StepFail, "No: they're offline, or their app isn't connected to the helper."
		}
		return StepFail, Explain(err)
	}
	c.found = true
	c.n.origins.addHelper(c.id, info.Addrs)
	c.n.host.Peerstore().AddAddrs(c.id, info.Addrs, peerstore.TempAddrTTL)
	for _, a := range info.Addrs {
		if isCircuit(a) {
			return StepOK, fmt.Sprintf("Yes: %d addresses, including a relay address.", len(info.Addrs))
		}
	}
	return StepOK, fmt.Sprintf("Yes: %d addresses, but no relay address (they hold no relay slot).", len(info.Addrs))
}

func (c *checker) direct(ctx context.Context) (StepStatus, string) {
	if conn := c.directConn(); conn != nil {
		return StepOK, fmt.Sprintf("Already connected directly (%s to %s).", transportName(conn.RemoteMultiaddr()), conn.RemoteMultiaddr())
	}
	var addrs []ma.Multiaddr
	for _, a := range c.n.host.Peerstore().Addrs(c.id) {
		if !isCircuit(a) {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return StepSkipped, "No direct address is known for them."
	}
	c.clearBackoff(c.id)
	ctx = network.WithForceDirectDial(ctx, "twocan-check")
	if err := c.n.connect(ctx, peer.AddrInfo{ID: c.id, Addrs: addrs}); err != nil {
		c.lastErr = err
		return StepFail, fmt.Sprintf("%s Tried %d addresses.", Explain(err), len(addrs))
	}
	if conn := c.directConn(); conn != nil {
		return StepOK, fmt.Sprintf("Connected directly (%s to %s).", transportName(conn.RemoteMultiaddr()), conn.RemoteMultiaddr())
	}
	return StepOK, "Connected directly."
}

func (c *checker) relay(ctx context.Context) (StepStatus, string) {
	connected, relayed := c.n.ConnInfo(c.id)
	switch {
	case connected && !relayed:
		return StepSkipped, "Not needed: you're connected directly."
	case connected:
		return StepOK, "Already connected through the helper's relay."
	case len(c.helpers) == 0:
		return StepSkipped, "No helper connected."
	}
	// The helper is the only relay, so its circuit address for them is
	// known even when the lookup gave none.
	var addrs []ma.Multiaddr
	for _, h := range c.helpers {
		for _, a := range h.Addrs {
			addrs = append(addrs, a.Encapsulate(ma.StringCast("/p2p/"+h.ID.String()+"/p2p-circuit")))
		}
	}
	c.n.origins.addHelper(c.id, addrs)
	ctx, cancel := context.WithTimeout(network.WithAllowLimitedConn(ctx, "twocan-check"), relayDialTimeout)
	defer cancel()
	if err := c.n.host.Connect(ctx, peer.AddrInfo{ID: c.id, Addrs: addrs}); err != nil {
		c.relayErr, c.lastErr = err, err
		return StepFail, Explain(err)
	}
	if _, relayed := c.n.ConnInfo(c.id); !relayed {
		return StepOK, "Connected (directly, on a second try)."
	}
	return StepOK, "Connected through the helper's relay."
}

func (c *checker) holePunch(ctx context.Context) (StepStatus, string) {
	connected, relayed := c.n.ConnInfo(c.id)
	switch {
	case !connected:
		return StepSkipped, "Not connected."
	case !relayed:
		return StepSkipped, "Not needed: you're connected directly."
	}
	if !c.hasDirectAddrs() {
		return StepSkipped, "They have no direct address to punch through to, so you stay on the relay."
	}
	var relayOpened time.Time
	for _, conn := range c.n.host.Network().ConnsToPeer(c.id) {
		if relayOpened.IsZero() || conn.Stat().Opened.Before(relayOpened) {
			relayOpened = conn.Stat().Opened
		}
	}
	deadline := time.NewTimer(c.punchWait)
	defer deadline.Stop()
	tick := time.NewTicker(punchPoll)
	defer tick.Stop()
	for {
		if c.directConn() != nil {
			return StepOK, "Upgraded to a direct connection."
		}
		if hp, ok := c.n.origins.punch(c.id); ok && !hp.OK && hp.At.After(relayOpened) {
			return StepFail, "Hole punching failed (" + hp.Err + "), so you stay on the relay. Both routers may be strict, or a firewall blocks it."
		}
		select {
		case <-ctx.Done():
			return StepSkipped, "Cancelled."
		case <-deadline.C:
			return StepWarn, fmt.Sprintf("No upgrade within %s, so you stay on the relay.", c.punchWait)
		case <-tick.C:
		}
	}
}

// clearBackoff forgets recent failed dials to id: the user asked for this
// check, so it dials afresh instead of reporting an earlier failure.
func (c *checker) clearBackoff(id peer.ID) {
	if sw, ok := c.n.host.Network().(*swarm.Swarm); ok {
		sw.Backoff().Clear(id)
	}
}

// directConn returns an open direct connection to the peer, or nil.
func (c *checker) directConn() network.Conn {
	for _, conn := range c.n.host.Network().ConnsToPeer(c.id) {
		if !isCircuit(conn.RemoteMultiaddr()) {
			return conn
		}
	}
	return nil
}

func (c *checker) hasDirectAddrs() bool {
	for _, a := range c.n.host.Peerstore().Addrs(c.id) {
		if !isCircuit(a) {
			return true
		}
	}
	return false
}

// verdict sums the check up in one line.
func (c *checker) verdict(connected, relayed bool) string {
	switch {
	case connected && !relayed && c.steps[stepPunch].Status == StepOK:
		return "Connected: hole punching upgraded the relayed connection to a direct one."
	case connected && !relayed:
		return "Connected directly."
	case connected:
		return "Connected through the helper's relay. It works, but each relayed connection is limited (30 min, 4 MB) until a direct one succeeds."
	case len(c.n.boot) == 0:
		return "Not reachable directly, and no helper is configured: outside your local network you need one (--bootstrap)."
	case len(c.helpers) == 0:
		return "Can't reach the helper. Check its address, and that TCP and UDP port 4001 are open on it."
	case c.relayErr != nil && strings.Contains(c.relayErr.Error(), "NO_RESERVATION"):
		if c.found {
			return "They have no relay slot, and their direct addresses don't answer. A firewall on their side is likely, or their app wrongly thinks it's publicly reachable (for example when it runs on the helper's own server)."
		}
		return "The helper doesn't know them and holds no relay slot for them: they're offline, or their app isn't connected to the helper."
	case c.steps[stepLookup].Status == StepFail && !c.found:
		return "The helper doesn't know them: they're offline, or their app isn't connected to the helper."
	}
	return "Couldn't connect: " + Explain(c.lastErr)
}
