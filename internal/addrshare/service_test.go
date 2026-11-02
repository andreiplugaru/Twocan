package addrshare

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	"twocan/internal/store"
)

const (
	waitFor = 5 * time.Second
	tick    = 10 * time.Millisecond
)

type testPeer struct {
	svc   *Service
	st    *store.Store
	addrs chan peer.ID // onAddrs calls
}

func newPeer(t *testing.T, mn mocknet.Mocknet) *testPeer {
	t.Helper()
	h, err := mn.GenPeer()
	require.NoError(t, err)
	st, err := store.Open(filepath.Join(t.TempDir(), "twocan.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	p := &testPeer{st: st, addrs: make(chan peer.ID, 10)}
	p.svc = New(h, st, func(id peer.ID) { p.addrs <- id })
	p.svc.Start()
	t.Cleanup(p.svc.Close)
	return p
}

func (p *testPeer) id() peer.ID { return p.svc.h.ID() }

func (p *testPeer) setContact(t *testing.T, id peer.ID, state store.ContactState) {
	t.Helper()
	require.NoError(t, p.st.UpsertContact(t.Context(), store.Contact{PeerID: id, State: state}))
}

func strs(addrs []ma.Multiaddr) []string { return addrStrings(addrs) }

func pair(t *testing.T) (a, b *testPeer) {
	t.Helper()
	mn := mocknet.New()
	t.Cleanup(func() { mn.Close() })
	a, b = newPeer(t, mn), newPeer(t, mn)
	require.NoError(t, mn.LinkAll())
	_, err := mn.ConnectPeers(a.id(), b.id())
	require.NoError(t, err)
	return a, b
}

func TestContactAddressesReplaceStaleOnes(t *testing.T) {
	a, b := pair(t)
	b.setContact(t, a.id(), store.StateAccepted)

	circuit := ma.StringCast("/ip4/203.0.113.7/tcp/4001/p2p/12D3KooWK5VRzX6ZXVBUEFp7RdUxSVKY5JN7dNWJcjRzjw8wwJVH/p2p-circuit")
	a.svc.own = func() []ma.Multiaddr {
		return []ma.Multiaddr{
			ma.StringCast("/ip4/192.168.1.194/tcp/4100"),
			ma.StringCast("/ip4/192.168.1.194/udp/4100/quic-v1"),
			ma.StringCast("/ip4/127.0.0.1/tcp/4100"),   // loopback: not sent
			ma.StringCast("/ip6/fe80::1/tcp/4100"),     // link-local: not sent
			ma.StringCast("/ip4/169.254.3.4/tcp/4100"), // link-local: not sent
			circuit, // relay: not sent
		}
	}
	// B still has an address from before A's restart, and a relay address.
	stale := ma.StringCast("/ip4/192.168.1.194/udp/38518/quic-v1")
	ps := b.svc.h.Peerstore()
	ps.AddAddrs(a.id(), []ma.Multiaddr{stale, circuit}, time.Hour)

	require.NoError(t, a.svc.Send(t.Context(), b.id()))
	select {
	case id := <-b.addrs:
		require.Equal(t, a.id(), id)
	case <-time.After(waitFor):
		t.Fatal("B was not told about A's addresses")
	}
	got := strs(ps.Addrs(a.id()))
	require.Contains(t, got, "/ip4/192.168.1.194/tcp/4100")
	require.Contains(t, got, "/ip4/192.168.1.194/udp/4100/quic-v1")
	require.Contains(t, got, circuit.String(), "relay addresses stay")
	require.NotContains(t, got, stale.String(), "the stale address is dropped")
	require.NotContains(t, got, "/ip4/127.0.0.1/tcp/4100")
	require.NotContains(t, got, "/ip6/fe80::1/tcp/4100")
	require.NotContains(t, got, "/ip4/169.254.3.4/tcp/4100")
}

func TestOnlyAcceptedContactsAreHeard(t *testing.T) {
	for _, state := range []store.ContactState{"", store.StatePendingIncoming, store.StateBlocked} {
		t.Run(string(state), func(t *testing.T) {
			a, b := pair(t)
			if state != "" {
				b.setContact(t, a.id(), state)
			}
			a.svc.own = func() []ma.Multiaddr { return []ma.Multiaddr{ma.StringCast("/ip4/10.0.0.9/tcp/1")} }
			_ = a.svc.Send(t.Context(), b.id()) // B resets the stream; whether A sees that is a race
			require.Never(t, func() bool { return len(b.addrs) > 0 }, 300*time.Millisecond, tick)
			require.NotContains(t, strs(b.svc.h.Peerstore().Addrs(a.id())), "/ip4/10.0.0.9/tcp/1")
		})
	}
}

func TestNothingToSend(t *testing.T) {
	a, b := pair(t)
	b.setContact(t, a.id(), store.StateAccepted)
	a.svc.own = func() []ma.Multiaddr { return []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1")} }
	require.NoError(t, a.svc.Send(t.Context(), b.id()))
	require.Never(t, func() bool { return len(b.addrs) > 0 }, 300*time.Millisecond, tick)
}

func TestDirectCapsAndFilters(t *testing.T) {
	var many []ma.Multiaddr
	for i := range MaxAddrs + 5 {
		many = append(many, ma.StringCast(fmt.Sprintf("/ip4/10.0.0.1/tcp/%d", 1000+i)))
	}
	require.Len(t, Direct(many), MaxAddrs)
	require.Empty(t, Direct([]ma.Multiaddr{ma.StringCast("/ip6/::1/tcp/1")}))
}
