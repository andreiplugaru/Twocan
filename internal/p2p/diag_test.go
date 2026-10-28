package p2p

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestExplain(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{fmt.Errorf("%w: x: %w", ErrUnreachable, routing.ErrNotFound), "The helper doesn't know where they are"},
		{errors.New("failed to find any peer in table"), "No helper is connected"},
		{errors.New("failed to dial: error opening relay circuit: NO_RESERVATION (204)"), "They have no relay slot"},
		{errors.New("dial tcp4 1.2.3.4:5: connect: connection refused"), "refused the connection"},
		{fmt.Errorf("failed to dial: %w", context.DeadlineExceeded), "No answer"},
		{errors.New("dial udp: connect: network is unreachable"), "no route to their addresses"},
		{errors.New("no good addresses"), "No usable address"},
		{errors.New("all dials failed\n  * [/ip4/1.2.3.4/tcp/1] dial backoff"), "failed moments ago"},
		{errors.New("error opening relay circuit: CONNECTION_FAILED (203)"), "couldn't pass the connection on"},
		{errors.New("error opening relay circuit: RESOURCE_LIMIT_EXCEEDED (201)"), "at its limit"},
		{errors.New("error opening relay circuit: PERMISSION_DENIED (202)"), "refused to relay"},
		{errors.New("something else"), "something else"},
	} {
		require.Contains(t, Explain(tc.err), tc.want, "%v", tc.err)
	}
}

func TestTransportName(t *testing.T) {
	for addr, want := range map[string]string{
		"/ip4/1.2.3.4/udp/5/quic-v1": "QUIC/IPv4",
		"/ip6/::1/tcp/5":             "TCP/IPv6",
		"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWHxukoUg4QZmSWciogRpYmRaoaQi8PPxE4JG8TUmtw5Zf/p2p-circuit": "relay",
	} {
		require.Equal(t, want, transportName(ma.StringCast(addr)), addr)
	}
}

// dial connects a to b using only b's listen addrs.
func dial(t *testing.T, a, b *Node) {
	t.Helper()
	require.NoError(t, a.Host().Connect(t.Context(), peer.AddrInfo{ID: b.Host().ID(), Addrs: b.Host().Addrs()}))
}

func TestConnsSources(t *testing.T) {
	a, b, c, d := newTestNode(t), newTestNode(t), newTestNode(t), newTestNode(t)

	dial(t, a, b)
	conns := a.Conns(b.Host().ID())
	require.Len(t, conns, 1)
	require.Equal(t, SourceKnown, conns[0].Source)
	require.False(t, conns[0].Inbound)
	require.False(t, conns[0].Relayed)
	require.Equal(t, "TCP/IPv4", conns[0].Transport)
	require.WithinDuration(t, time.Now(), conns[0].Opened, time.Minute)

	in := b.Conns(a.Host().ID())
	require.Len(t, in, 1)
	require.Equal(t, SourceInbound, in[0].Source)
	require.True(t, in[0].Inbound)

	a.origins.addMDNS(c.Host().ID(), c.Host().Addrs())
	dial(t, a, c)
	require.Equal(t, SourceMDNS, a.Conns(c.Host().ID())[0].Source)

	// Helper addrs come with a /p2p/<id> suffix sometimes; it does not matter.
	var withID []ma.Multiaddr
	for _, addr := range d.Host().Addrs() {
		withID = append(withID, addr.Encapsulate(ma.StringCast("/p2p/"+d.Host().ID().String())))
	}
	a.origins.addHelper(d.Host().ID(), withID)
	dial(t, a, d)
	require.Equal(t, SourceHelper, a.Conns(d.Host().ID())[0].Source)

	require.Empty(t, a.Conns(peer.ID("nobody")))
}

func TestOriginsRecordsHolePunches(t *testing.T) {
	o := newOrigins()
	id := peer.ID("x")
	_, ok := o.punch(id)
	require.False(t, ok)

	at := time.Now().Truncate(time.Millisecond)
	o.Trace(&holepunch.Event{Remote: id, Timestamp: at.UnixNano(), Evt: &holepunch.DirectDialEvt{Success: false}})
	_, ok = o.punch(id)
	require.False(t, ok, "a failed direct dial is followed by a hole punch; it is not an outcome")

	o.Trace(&holepunch.Event{Remote: id, Timestamp: at.UnixNano(), Evt: &holepunch.EndHolePunchEvt{Success: false, Error: "all retries failed"}})
	hp, ok := o.punch(id)
	require.True(t, ok)
	require.Equal(t, HolePunch{At: at, OK: false, Err: "all retries failed"}, hp)

	o.Trace(&holepunch.Event{Remote: id, Timestamp: at.UnixNano(), Evt: &holepunch.EndHolePunchEvt{Success: true}})
	hp, _ = o.punch(id)
	require.True(t, hp.OK)
}

func TestOriginsAreBounded(t *testing.T) {
	o := newOrigins()
	addrs := make([]ma.Multiaddr, 2*maxTrackedAddrs)
	for i := range addrs {
		addrs[i] = ma.StringCast(fmt.Sprintf("/ip4/10.0.0.1/tcp/%d", i+1))
	}
	for i := range 2 * maxTrackedPeers {
		o.addMDNS(peer.ID(fmt.Sprint(i)), addrs)
	}
	require.LessOrEqual(t, len(o.mdns), maxTrackedPeers)
	for _, set := range o.mdns {
		require.LessOrEqual(t, len(set), maxTrackedAddrs)
	}
}

// checkWithReports runs Check and returns its result and every report.
func checkWithReports(ctx context.Context, n *Node, id peer.ID) (CheckResult, []Step) {
	var reports []Step
	res := n.Check(ctx, id, func(_ int, s Step) { reports = append(reports, s) })
	return res, reports
}

func statuses(steps []Step) []StepStatus {
	out := make([]StepStatus, len(steps))
	for i, s := range steps {
		out[i] = s.Status
	}
	return out
}

func TestCheckDirectWithoutHelper(t *testing.T) {
	a, b := newTestNode(t), newTestNode(t)
	a.Host().Peerstore().AddAddrs(b.Host().ID(), b.Host().Addrs(), time.Hour)

	res, reports := checkWithReports(t.Context(), a, b.Host().ID())
	require.Equal(t, []StepStatus{StepSkipped, StepWarn, StepSkipped, StepSkipped, StepOK, StepSkipped, StepSkipped}, statuses(res.Steps))
	require.Contains(t, res.Steps[stepHelper].Detail, "No helper configured")
	require.Contains(t, res.Steps[stepDirect].Detail, "Connected directly (TCP/IPv4 to /ip4/127.0.0.1/tcp/")
	require.Equal(t, "Connected directly.", res.Verdict)
	require.True(t, res.Connected)
	require.False(t, res.Relayed)

	// Every step is reported as running, then with its outcome.
	require.Len(t, reports, 2*numSteps)
	for i := range numSteps {
		require.Equal(t, StepRunning, reports[2*i].Status)
		require.Equal(t, stepTitles[i], reports[2*i].Title)
		require.Equal(t, res.Steps[i], reports[2*i+1])
	}

	// Run again while connected: the direct step sees the open connection.
	res, _ = checkWithReports(t.Context(), a, b.Host().ID())
	require.Contains(t, res.Steps[stepDirect].Detail, "Already connected directly")
	require.Contains(t, res.Steps[stepLookup].Detail, "Already connected")
}

func TestCheckUnreachableWithoutHelper(t *testing.T) {
	a := newTestNode(t)
	ghost, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)
	a.Host().Peerstore().AddAddrs(ghost, []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1")}, time.Hour)

	res, _ := checkWithReports(t.Context(), a, ghost)
	require.Equal(t, StepFail, res.Steps[stepDirect].Status)
	require.Contains(t, res.Steps[stepDirect].Detail, "refused the connection")
	require.Contains(t, res.Steps[stepDirect].Detail, "Tried 1 address:\n/ip4/127.0.0.1/tcp/1: refused (nothing listens there)")
	require.False(t, res.Connected)
	require.Contains(t, res.Verdict, "no helper is configured")
}

func TestCheckDeadHelper(t *testing.T) {
	deadID, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)
	a := newTestNodeCfg(t, Config{
		PrivKey:     newTestKey(t),
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		Bootstrap:   []peer.AddrInfo{{ID: deadID, Addrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1")}}},
	})
	other, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)

	res, _ := checkWithReports(t.Context(), a, other)
	require.Equal(t, StepFail, res.Steps[stepHelper].Status)
	require.Contains(t, res.Steps[stepHelper].Detail, "refused the connection")
	require.Equal(t, StepSkipped, res.Steps[stepLookup].Status)
	require.Equal(t, StepSkipped, res.Steps[stepRelay].Status)
	require.Contains(t, res.Verdict, "Can't reach the helper")
}

func TestCheckCancelled(t *testing.T) {
	a := newTestNode(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, _ := checkWithReports(ctx, a, peer.ID("x"))
	for _, s := range res.Steps {
		require.Equal(t, StepSkipped, s.Status)
		require.Equal(t, "Cancelled.", s.Detail)
	}
	require.Equal(t, "Cancelled.", res.Verdict)
}

// newTestRelay starts a relay that is not a DHT server, like a helper whose
// lookups fail. Autorelay builds circuit addrs only from a relay's public
// addrs, so it also advertises a never-dialled public one.
func newTestRelay(t *testing.T) peer.AddrInfo {
	t.Helper()
	fakePublic := ma.StringCast("/ip4/1.2.3.4/tcp/4001")
	relay, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.EnableRelayService(),
		libp2p.ForceReachabilityPublic(),
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
			return append(addrs, fakePublic)
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	var listen []ma.Multiaddr
	for _, a := range relay.Network().ListenAddresses() {
		if !isCircuit(a) {
			listen = append(listen, a)
		}
	}
	return peer.AddrInfo{ID: relay.ID(), Addrs: listen}
}

func hasCircuitAddr(n *Node) bool {
	for _, a := range n.Host().Addrs() {
		if isCircuit(a) {
			return true
		}
	}
	return false
}

// The lookup fails, but the check builds the relay address from the
// helper's own address and reaches B through it anyway.
func TestCheckReachesThroughRelayWithoutLookup(t *testing.T) {
	relay := newTestRelay(t)
	a := newTestNodeCfg(t, Config{
		PrivKey:     newTestKey(t),
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		Bootstrap:   []peer.AddrInfo{relay},
	})
	b := newTestNodeCfg(t, Config{
		PrivKey:      newTestKey(t),
		Bootstrap:    []peer.AddrInfo{relay},
		ForcePrivate: true,
	})
	require.Eventually(t, func() bool { return hasCircuitAddr(b) }, 15*time.Second, 50*time.Millisecond, "B got no relay reservation")

	res, _ := checkWithReports(t.Context(), a, b.Host().ID())
	require.Equal(t, StepOK, res.Steps[stepHelper].Status, res.Steps[stepHelper].Detail)
	require.Equal(t, StepFail, res.Steps[stepLookup].Status)
	require.Equal(t, StepSkipped, res.Steps[stepDirect].Status)
	require.Equal(t, "Connected through the helper's relay.", res.Steps[stepRelay].Detail)
	require.Equal(t, StepOK, res.Steps[stepRelay].Status)
	require.Equal(t, StepSkipped, res.Steps[stepPunch].Status)
	require.True(t, res.Connected)
	require.True(t, res.Relayed)
	require.Contains(t, res.Verdict, "Connected through the helper's relay")

	conns := a.Conns(b.Host().ID())
	require.Len(t, conns, 1)
	require.Equal(t, SourceHelper, conns[0].Source)
	require.Equal(t, "relay", conns[0].Transport)
}

// A peer without a reservation: the relay answers NO_RESERVATION and the
// check says so.
func TestCheckReportsMissingRelaySlot(t *testing.T) {
	relay := newTestRelay(t)
	a := newTestNodeCfg(t, Config{
		PrivKey:     newTestKey(t),
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		Bootstrap:   []peer.AddrInfo{relay},
	})
	nobody, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)

	res, _ := checkWithReports(t.Context(), a, nobody)
	require.Equal(t, StepFail, res.Steps[stepRelay].Status)
	require.Contains(t, res.Steps[stepRelay].Detail, "They have no relay slot")
	require.False(t, res.Connected)
	require.Contains(t, res.Verdict, "holds no relay slot for them")
}

func TestOnlyBackoff(t *testing.T) {
	require.True(t, OnlyBackoff(errors.New("all dials failed\n  * [/ip4/1.2.3.4/tcp/1] dial backoff")))
	require.False(t, OnlyBackoff(errors.New("all dials failed\n  * [/ip4/1.2.3.4/tcp/1] dial backoff\n  * [/ip4/1.2.3.4/tcp/2] connection refused")))
	require.False(t, OnlyBackoff(nil))
}

// The direct addrs' "dial backoff" must not hide why the relay dial failed.
func TestCircuitCause(t *testing.T) {
	circuit := ma.StringCast("/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWHxukoUg4QZmSWciogRpYmRaoaQi8PPxE4JG8TUmtw5Zf/p2p-circuit")
	err := fmt.Errorf("failed to dial: %w", &swarm.DialError{DialErrors: []swarm.TransportError{
		{Address: ma.StringCast("/ip4/1.2.3.4/tcp/5"), Cause: errors.New("dial backoff")},
		{Address: circuit, Cause: errors.New("error opening hop stream to relay: protocols not supported")},
	}})
	require.Equal(t, explainedBackoff, Explain(err), "the whole error reads as a backoff")
	require.Equal(t, "error opening hop stream to relay: protocols not supported", Explain(circuitCause(err)))

	plain := errors.New("x")
	require.Equal(t, plain, circuitCause(plain))
	noCircuit := &swarm.DialError{DialErrors: []swarm.TransportError{{Address: ma.StringCast("/ip4/1.2.3.4/tcp/5"), Cause: plain}}}
	require.Equal(t, error(noCircuit), circuitCause(noCircuit))
}

func TestPunchOutcome(t *testing.T) {
	opened := time.Now().Add(-time.Minute)
	type want struct {
		status StepStatus
		done   bool
	}
	for name, tc := range map[string]struct {
		now     time.Time
		hp      HolePunch
		punched bool
		direct  bool
		want    want
	}{
		"direct connection":                {now: opened.Add(time.Second), direct: true, want: want{StepOK, true}},
		"just opened, nothing yet":         {now: opened.Add(time.Second), want: want{0, false}},
		"nothing attempted after grace":    {now: opened.Add(punchStartGrace + time.Second), want: want{StepWarn, true}},
		"older punch is for another conn":  {now: opened.Add(punchStartGrace + time.Second), punched: true, hp: HolePunch{At: opened.Add(-time.Hour)}, want: want{StepWarn, true}},
		"failed attempt, retries may come": {now: opened.Add(3 * time.Second), punched: true, hp: HolePunch{At: opened.Add(2 * time.Second), Err: "x"}, want: want{0, false}},
		"failed and quiet since":           {now: opened.Add(2*time.Second + punchQuiet + time.Second), punched: true, hp: HolePunch{At: opened.Add(2 * time.Second), Err: "x"}, want: want{StepFail, true}},
		"succeeded, direct conn not seen":  {now: opened.Add(punchStartGrace + time.Minute), punched: true, hp: HolePunch{At: opened.Add(2 * time.Second), OK: true}, want: want{0, false}},
	} {
		status, detail, done := punchOutcome(tc.now, opened, tc.hp, tc.punched, tc.direct)
		require.Equal(t, tc.want, want{status, done}, name)
		require.Equal(t, done, detail != "", name)
	}
}
