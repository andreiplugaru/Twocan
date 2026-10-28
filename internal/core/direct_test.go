package core

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	"twocan/internal/event"
	"twocan/internal/logtest"
	"twocan/internal/store"
)

// Contacts send each other their addresses when they connect, and save
// what they get.
func TestContactsShareAddresses(t *testing.T) {
	mn := newMocknet(t)
	log, logs := logtest.New()
	a := newMockNode(t, mn, intervals{})
	b := newMockNodeLog(t, mn, intervals{}, log)
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)

	// Reconnect: now that they are accepted contacts, each side shares.
	require.NoError(t, mn.DisconnectPeers(a.PeerID(), b.PeerID()))
	_, err := mn.ConnectPeers(a.PeerID(), b.PeerID())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), `msg="contact sent their addresses" component=addrshare peer=`+a.PeerID().String())
	}, waitFor, poll)
	require.Eventually(t, func() bool {
		c, err := b.st.GetContact(t.Context(), a.PeerID())
		return err == nil && len(c.Addrs) > 0 && c.Addrs[0].Equal(a.h.Addrs()[0])
	}, waitFor, poll, "B saved A's address")
}

// fakeDialer is a reacher whose contacts are all connected through the
// relay until DialDirect "connects directly".
type fakeDialer struct {
	mu      sync.Mutex
	relayed map[peer.ID]bool
	dials   map[peer.ID]int
}

func (f *fakeDialer) Reach(context.Context, peer.ID) error { return nil }

func (f *fakeDialer) DialDirect(_ context.Context, id peer.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials[id]++
	f.relayed[id] = false
	return nil
}

func (f *fakeDialer) connInfo(id peer.ID) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.relayed[id]
	return ok, r
}

func (f *fakeDialer) dialsTo(id peer.ID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials[id]
}

func TestLeavesTheRelay(t *testing.T) {
	mn := newMocknet(t)
	h, err := mn.GenPeer()
	require.NoError(t, err)
	st, err := store.Open(filepath.Join(t.TempDir(), "twocan.db"))
	require.NoError(t, err)
	friend, stranger := randomPeer(t), randomPeer(t)
	require.NoError(t, st.UpsertContact(t.Context(), store.Contact{PeerID: friend, State: store.StateAccepted}))
	require.NoError(t, st.UpsertContact(t.Context(), store.Contact{PeerID: stranger, State: store.StatePendingOutgoing}))
	f := &fakeDialer{
		relayed: map[peer.ID]bool{friend: true, stranger: true},
		dials:   map[peer.ID]int{},
	}
	n, err := startWith(t.Context(), h, st, f, f.connInfo, intervals{direct: 20 * time.Millisecond}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, n.Stop()) })
	events, cancel := n.Subscribe()
	defer cancel()

	e := waitEvent(t, events, func(e event.Connectivity) bool { return e.Peer == friend })
	require.True(t, e.Connected)
	require.False(t, e.Relayed)
	require.Equal(t, 1, f.dialsTo(friend))

	// Direct now: no more dials. Only accepted contacts are moved.
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, f.dialsTo(friend))
	require.Zero(t, f.dialsTo(stranger))
}

func TestSaveAddrsPutsDirectFirst(t *testing.T) {
	mn := newMocknet(t)
	n := newMockNode(t, mn, intervals{})
	id := randomPeer(t)
	require.NoError(t, n.st.UpsertContact(t.Context(), store.Contact{PeerID: id, State: store.StateAccepted}))
	var addrs []ma.Multiaddr
	for range maxSavedAddrs {
		addrs = append(addrs, ma.StringCast("/ip4/62.83.39.196/tcp/4001/p2p/"+randomPeer(t).String()+"/p2p-circuit"))
	}
	lan := ma.StringCast("/ip4/192.168.1.194/tcp/4100")
	n.h.Peerstore().AddAddrs(id, append(addrs, lan), time.Hour)

	n.saveAddrs(t.Context(), id)
	c, err := n.st.GetContact(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, c.Addrs, maxSavedAddrs)
	require.True(t, c.Addrs[0].Equal(lan), "the LAN address is kept, ahead of relay addresses")
}
