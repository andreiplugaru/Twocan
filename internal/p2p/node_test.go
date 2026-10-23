package p2p

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func newTestKey(t *testing.T) crypto.PrivKey {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	return priv
}

func newTestNodeCfg(t *testing.T, cfg Config) *Node {
	t.Helper()
	n, err := NewNode(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, n.Close()) })
	return n
}

func newTestNode(t *testing.T) *Node {
	t.Helper()
	return newTestNodeCfg(t, Config{
		PrivKey:     newTestKey(t),
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
}

func TestReachViaPeerstoreAddrs(t *testing.T) {
	a, b := newTestNode(t), newTestNode(t)
	a.Host().Peerstore().AddAddrs(b.Host().ID(), b.Host().Addrs(), time.Hour)

	require.NoError(t, a.Reach(t.Context(), b.Host().ID()))
	c, r := a.ConnInfo(b.Host().ID())
	require.True(t, c)
	require.False(t, r)

	// Already connected: Reach is a no-op success.
	require.NoError(t, a.Reach(t.Context(), b.Host().ID()))
}

func TestConnInfoNotConnected(t *testing.T) {
	a := newTestNode(t)
	id, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)
	c, r := a.ConnInfo(id)
	require.False(t, c)
	require.False(t, r)
}

func TestReachUnknownPeerFails(t *testing.T) {
	a := newTestNode(t)
	id, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)

	start := time.Now()
	err = a.Reach(t.Context(), id)
	require.ErrorIs(t, err, ErrUnreachable)
	require.Less(t, time.Since(start), 15*time.Second)
}

func TestNewNodeDoesNotBlockOnDeadBootstrap(t *testing.T) {
	deadID, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)
	dead := peer.AddrInfo{
		ID:    deadID,
		Addrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1")},
	}

	start := time.Now()
	newTestNodeCfg(t, Config{
		PrivKey:     newTestKey(t),
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		Bootstrap:   []peer.AddrInfo{dead},
	})
	require.Less(t, time.Since(start), 2*time.Second)
}

// requireNoDirectListeners asserts n listens on nothing but the relay
// transport's implicit /p2p-circuit address (no TCP/QUIC).
func requireNoDirectListeners(t *testing.T, n *Node) {
	t.Helper()
	for _, a := range n.Host().Network().ListenAddresses() {
		require.Equal(t, "/p2p-circuit", a.String())
	}
}

func TestNewNodeNoListenAddrs(t *testing.T) {
	n := newTestNodeCfg(t, Config{PrivKey: newTestKey(t)})
	requireNoDirectListeners(t, n)
}

// A node with no listen addrs and a bootstrap relay must still run autorelay:
// it gets a reservation and advertises a /p2p-circuit address.
func TestNewNodeNoListenAddrsWithBootstrap(t *testing.T) {
	// Autorelay builds circuit addrs only from a relay's public addrs, so the
	// loopback relay also advertises a never-dialled public one.
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

	n := newTestNodeCfg(t, Config{
		PrivKey:      newTestKey(t),
		Bootstrap:    []peer.AddrInfo{{ID: relay.ID(), Addrs: relay.Addrs()}},
		ForcePrivate: true,
	})
	requireNoDirectListeners(t, n)
	require.Eventually(t, func() bool {
		for _, a := range n.Host().Addrs() {
			if _, err := a.ValueForProtocol(ma.P_CIRCUIT); err == nil {
				return true
			}
		}
		return false
	}, 15*time.Second, 50*time.Millisecond, "no relay reservation")
}

func TestMDNSNotifeeAddsAddrsWithoutDialing(t *testing.T) {
	a, b := newTestNode(t), newTestNode(t)
	mdnsNotifee{a.Host()}.HandlePeerFound(peer.AddrInfo{ID: b.Host().ID(), Addrs: b.Host().Addrs()})

	require.ElementsMatch(t, b.Host().Addrs(), a.Host().Peerstore().Addrs(b.Host().ID()))
	c, _ := a.ConnInfo(b.Host().ID())
	require.False(t, c)
}

func TestParseBootstrap(t *testing.T) {
	id, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)
	valid := "/ip4/1.2.3.4/tcp/4001/p2p/" + id.String()

	infos, err := ParseBootstrap([]string{valid, "", "   "})
	require.NoError(t, err)
	require.Len(t, infos, 1)
	require.Equal(t, id, infos[0].ID)
	require.Len(t, infos[0].Addrs, 1)
	require.Equal(t, "/ip4/1.2.3.4/tcp/4001", infos[0].Addrs[0].String())

	_, err = ParseBootstrap([]string{"nonsense"})
	require.Error(t, err)

	// A multiaddr without a /p2p/ component is also invalid.
	_, err = ParseBootstrap([]string{"/ip4/1.2.3.4/tcp/4001"})
	require.Error(t, err)
}

func TestReachabilityInitiallyUnknown(t *testing.T) {
	n := newTestNode(t)
	require.Equal(t, network.ReachabilityUnknown, n.Reachability())
}

func TestReachabilityTracksEvents(t *testing.T) {
	n := newTestNodeCfg(t, Config{
		PrivKey:      newTestKey(t),
		ListenAddrs:  []string{"/ip4/127.0.0.1/tcp/0"},
		ForcePrivate: true,
	})
	require.Eventually(t, func() bool {
		return n.Reachability() == network.ReachabilityPrivate
	}, 5*time.Second, 20*time.Millisecond)
}
