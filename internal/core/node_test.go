package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	"twocan/internal/contacts"
	"twocan/internal/event"
	"twocan/internal/identity"
	"twocan/internal/store"
)

const (
	waitFor = 5 * time.Second
	poll    = 20 * time.Millisecond
)

// mnReacher implements p2p.Reacher over a mocknet: reaching a peer means
// connecting to it, which fails while the two peers are unlinked.
type mnReacher struct {
	mn   mocknet.Mocknet
	self peer.ID
}

func (r mnReacher) Reach(_ context.Context, id peer.ID) error {
	if r.mn.Net(r.self).Connectedness(id) == network.Connected {
		return nil
	}
	_, err := r.mn.ConnectPeers(r.self, id)
	return err
}

// newMockNode starts a Node on a fresh mocknet host with its own store. Call
// mn.LinkAll() after creating all nodes.
func newMockNode(t *testing.T, mn mocknet.Mocknet, iv intervals) *Node {
	t.Helper()
	h, err := mn.GenPeer()
	require.NoError(t, err)
	st, err := store.Open(filepath.Join(t.TempDir(), "twocan.db"))
	require.NoError(t, err)
	connInfo := func(id peer.ID) (bool, bool) {
		return h.Network().Connectedness(id) == network.Connected, false
	}
	n, err := startWith(t.Context(), h, st, mnReacher{mn: mn, self: h.ID()}, connInfo, iv)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, n.Stop()) })
	return n
}

func newMocknet(t *testing.T) mocknet.Mocknet {
	t.Helper()
	mn := mocknet.New()
	t.Cleanup(func() { mn.Close() })
	return mn
}

func randomPeer(t *testing.T) peer.ID {
	t.Helper()
	_, pub, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	id, err := peer.IDFromPublicKey(pub)
	require.NoError(t, err)
	return id
}

// waitEvent returns the first event on ch of type T matching match.
func waitEvent[T any](t *testing.T, ch <-chan any, match func(T) bool) T {
	t.Helper()
	deadline := time.After(waitFor)
	for {
		select {
		case e := <-ch:
			if v, ok := e.(T); ok && match(v) {
				return v
			}
		case <-deadline:
			var zero T
			t.Fatalf("no matching %T event", zero)
			return zero
		}
	}
}

// view returns n's ContactView of id, failing if there is none.
func view(t *testing.T, n *Node, id peer.ID) ContactView {
	t.Helper()
	vs, err := n.Contacts(t.Context())
	require.NoError(t, err)
	for _, v := range vs {
		if v.PeerID == id {
			return v
		}
	}
	t.Fatalf("no contact %s", id)
	return ContactView{}
}

func stateOf(n *Node, id peer.ID) store.ContactState {
	c, err := n.st.GetContact(context.Background(), id)
	if err != nil {
		return ""
	}
	return c.State
}

// befriend makes a and b accepted contacts through the real protocol:
// a adds b's card, b sees the request and accepts it.
func befriend(t *testing.T, a, b *Node) {
	t.Helper()
	bEvents, cancel := b.Subscribe()
	defer cancel()

	card, err := b.MyCard()
	require.NoError(t, err)
	require.NoError(t, a.AddContact(t.Context(), card))

	waitEvent(t, bEvents, func(e event.ContactChanged) bool { return e.Peer == a.PeerID() })
	require.Eventually(t, func() bool { return stateOf(b, a.PeerID()) == store.StatePendingIncoming }, waitFor, poll)
	require.Equal(t, a.DisplayName(), view(t, b, a.PeerID()).DisplayName)

	require.NoError(t, b.AcceptContact(t.Context(), a.PeerID()))
	require.Eventually(t, func() bool { return stateOf(a, b.PeerID()) == store.StateAccepted }, waitFor, poll)
	require.Equal(t, store.StateAccepted, stateOf(b, a.PeerID()))
}

func TestEndToEndOverMocknet(t *testing.T) {
	mn := newMocknet(t)
	a, b := newMockNode(t, mn, intervals{}), newMockNode(t, mn, intervals{})
	require.NoError(t, mn.LinkAll())
	require.NoError(t, a.SetDisplayName("Alice"))
	require.NoError(t, b.SetDisplayName("Bob"))

	aEvents, cancel := a.Subscribe()
	defer cancel()

	befriend(t, a, b)
	conn := waitEvent(t, aEvents, func(e event.Connectivity) bool { return e.Peer == b.PeerID() })
	require.True(t, conn.Connected)
	require.Equal(t, "Bob", view(t, a, b.PeerID()).Name())
	require.True(t, view(t, a, b.PeerID()).Connected)

	m, err := a.Send(t.Context(), b.PeerID(), "hello")
	require.NoError(t, err)
	waitEvent(t, aEvents, func(e event.MessageDelivered) bool { return bytes.Equal(e.ID, m.ID) })

	hist, err := b.History(t.Context(), a.PeerID())
	require.NoError(t, err)
	require.Len(t, hist, 1)
	require.Equal(t, "hello", hist[0].Text)
	require.Equal(t, store.DirIn, hist[0].Direction)

	sent, err := a.History(t.Context(), b.PeerID())
	require.NoError(t, err)
	require.Len(t, sent, 1)
	require.Equal(t, store.StatusDelivered, sent[0].Status)

	require.Equal(t, 1, view(t, b, a.PeerID()).Unread)
	require.NoError(t, b.MarkRead(t.Context(), a.PeerID()))
	require.Equal(t, 0, view(t, b, a.PeerID()).Unread)

	require.NoError(t, a.RenameContact(t.Context(), b.PeerID(), "  Bobby "))
	require.Equal(t, "Bobby", view(t, a, b.PeerID()).Name())
}

func TestReconnectLoopDeliversAfterOutage(t *testing.T) {
	mn := newMocknet(t)
	iv := intervals{tick: 100 * time.Millisecond}
	a, b := newMockNode(t, mn, iv), newMockNode(t, mn, iv)
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)

	require.NoError(t, mn.UnlinkPeers(a.PeerID(), b.PeerID()))
	require.NoError(t, mn.DisconnectPeers(a.PeerID(), b.PeerID()))
	_, err := a.Send(t.Context(), b.PeerID(), "one")
	require.NoError(t, err)
	_, err = a.Send(t.Context(), b.PeerID(), "two")
	require.NoError(t, err)

	// Let the first attempts fail while the link is down.
	time.Sleep(500 * time.Millisecond)
	pending, err := a.st.PendingOutgoing(t.Context(), b.PeerID())
	require.NoError(t, err)
	require.Len(t, pending, 2)

	_, err = mn.LinkPeers(a.PeerID(), b.PeerID())
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		hist, err := b.History(t.Context(), a.PeerID())
		return err == nil && len(hist) == 2
	}, 15*time.Second, poll)
	require.Eventually(t, func() bool {
		pending, err := a.st.PendingOutgoing(t.Context(), b.PeerID())
		return err == nil && len(pending) == 0
	}, waitFor, poll)
}

func (n *Node) parked(id peer.ID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	ps, ok := n.peers[id]
	return ok && ps.parked
}

// A peer blocked while messages to it were queued is parked by the reconnect
// loop (chat.ErrNotContact), not retried with backoff; re-adding the peer
// unparks it and the queued message is delivered.
func TestReconnectLoopParksNonContact(t *testing.T) {
	mn := newMocknet(t)
	iv := intervals{tick: 50 * time.Millisecond}
	a, b := newMockNode(t, mn, iv), newMockNode(t, mn, iv)
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)

	require.NoError(t, mn.UnlinkPeers(a.PeerID(), b.PeerID()))
	require.NoError(t, mn.DisconnectPeers(a.PeerID(), b.PeerID()))
	_, err := a.Send(t.Context(), b.PeerID(), "queued")
	require.NoError(t, err)
	require.NoError(t, a.RejectContact(t.Context(), b.PeerID()))

	require.Eventually(t, func() bool { return a.parked(b.PeerID()) }, waitFor, poll)
	time.Sleep(200 * time.Millisecond) // several ticks: stays parked
	require.True(t, a.parked(b.PeerID()))
	pending, err := a.st.PendingOutgoing(t.Context(), b.PeerID())
	require.NoError(t, err)
	require.Len(t, pending, 1)

	_, err = mn.LinkPeers(a.PeerID(), b.PeerID())
	require.NoError(t, err)
	card, err := b.MyCard()
	require.NoError(t, err)
	require.NoError(t, a.AddContact(t.Context(), card)) // blocked -> pending_outgoing; b still accepts us

	require.Eventually(t, func() bool {
		hist, err := b.History(t.Context(), a.PeerID())
		return err == nil && len(hist) == 1
	}, 15*time.Second, poll)
}

func TestNextBackoff(t *testing.T) {
	d := time.Duration(0)
	var got []time.Duration
	for range 9 {
		d = nextBackoff(d)
		got = append(got, d)
	}
	s := time.Second
	require.Equal(t, []time.Duration{5 * s, 10 * s, 20 * s, 40 * s, 80 * s, 160 * s, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute}, got)
}

func TestCardAddrs(t *testing.T) {
	relay := "/ip4/203.0.113.9/tcp/4001/p2p/" + randomPeer(t).String() + "/p2p-circuit"
	mas := func(ss ...string) []ma.Multiaddr {
		var out []ma.Multiaddr
		for _, s := range ss {
			out = append(out, ma.StringCast(s))
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		in, want []ma.Multiaddr
	}{
		{
			name: "loopback dropped when others exist; public, circuit, private order",
			in:   mas("/ip4/127.0.0.1/tcp/1", "/ip4/192.168.1.5/tcp/1", relay, "/ip4/93.184.216.34/tcp/1"),
			want: mas("/ip4/93.184.216.34/tcp/1", relay, "/ip4/192.168.1.5/tcp/1"),
		},
		{
			name: "circuit kept despite many private addrs",
			in: mas("/ip4/192.168.1.5/tcp/1", "/ip4/192.168.1.5/udp/1/quic-v1", "/ip4/10.0.0.2/tcp/1",
				"/ip4/10.0.0.2/udp/1/quic-v1", relay),
			want: mas(relay, "/ip4/192.168.1.5/tcp/1", "/ip4/192.168.1.5/udp/1/quic-v1", "/ip4/10.0.0.2/tcp/1"),
		},
		{
			name: "loopback only when nothing else",
			in:   mas("/ip4/127.0.0.1/tcp/1", "/ip6/::1/tcp/1"),
			want: mas("/ip4/127.0.0.1/tcp/1", "/ip6/::1/tcp/1"),
		},
		{name: "empty", in: nil, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, strs(tc.want), strs(cardAddrs(tc.in)))
		})
	}
}

func strs(addrs []ma.Multiaddr) []string {
	var out []string
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

func startTestNode(t *testing.T, dir string) *Node {
	t.Helper()
	n, err := Start(t.Context(), Options{
		DataDir:     dir,
		Bootstrap:   []string{},
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	require.NoError(t, err)
	return n
}

func TestStartWithUnreachableBootstrap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	start := time.Now()
	n, err := Start(t.Context(), Options{
		DataDir:     dir,
		Bootstrap:   []string{"/ip4/127.0.0.1/tcp/1/p2p/" + randomPeer(t).String()},
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, network.ReachabilityUnknown, n.Reachability())

	st, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), st.Mode().Perm())
	require.FileExists(t, filepath.Join(dir, "identity.key"))
	require.FileExists(t, filepath.Join(dir, "twocan.db"))

	require.NoError(t, n.Stop())
	require.NoError(t, n.Stop()) // idempotent
}

func TestStartPersistsIdentity(t *testing.T) {
	dir := t.TempDir()
	n := startTestNode(t, dir)
	id := n.PeerID()
	require.NoError(t, n.SetDisplayName("  Alice "))
	require.Equal(t, "Alice", n.DisplayName())
	require.NoError(t, n.Stop())

	n = startTestNode(t, dir)
	defer func() { require.NoError(t, n.Stop()) }()
	require.Equal(t, id, n.PeerID())
	require.Equal(t, "Alice", n.DisplayName())
}

func TestMyCardDecodes(t *testing.T) {
	n := startTestNode(t, t.TempDir())
	defer func() { require.NoError(t, n.Stop()) }()
	require.NoError(t, n.SetDisplayName("Alice"))

	s, err := n.MyCard()
	require.NoError(t, err)
	c, err := identity.DecodeCard(s)
	require.NoError(t, err)
	require.Equal(t, n.PeerID(), c.PeerID)
	require.Equal(t, "Alice", c.DisplayName)
	require.NotEmpty(t, c.Addrs)
}

func TestAddContactErrors(t *testing.T) {
	mn := newMocknet(t)
	a := newMockNode(t, mn, intervals{})

	require.ErrorIs(t, a.AddContact(t.Context(), "hello"), identity.ErrNotACard)
	require.ErrorIs(t, a.AddContact(t.Context(), "twocan:!!!"), identity.ErrDamagedCard)
	own, err := a.MyCard()
	require.NoError(t, err)
	require.ErrorIs(t, a.AddContact(t.Context(), own), contacts.ErrSelf)

	require.Error(t, a.SetDisplayName(string(bytes.Repeat([]byte("x"), 65))))
}
