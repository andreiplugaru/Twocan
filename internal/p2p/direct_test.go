package p2p

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

// A reaches B through a relay only, then dials it directly: the direct
// connection replaces the relayed one.
func TestDialDirectReplacesRelayedConnection(t *testing.T) {
	relay := newTestRelay(t)
	a := newTestNodeCfg(t, Config{
		PrivKey:     newTestKey(t),
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
		Bootstrap:   []peer.AddrInfo{relay},
	})
	b := newTestNodeCfg(t, Config{
		PrivKey:      newTestKey(t),
		ListenAddrs:  []string{"/ip4/127.0.0.1/tcp/0"},
		Bootstrap:    []peer.AddrInfo{relay},
		ForcePrivate: true,
	})
	require.Eventually(t, func() bool { return hasCircuitAddr(b) }, 15*time.Second, 50*time.Millisecond, "B got no relay reservation")

	bID := b.Host().ID()
	circuit := relay.Addrs[0].Encapsulate(ma.StringCast("/p2p/" + relay.ID.String() + "/p2p-circuit"))
	ctx := network.WithAllowLimitedConn(t.Context(), "test")
	require.NoError(t, a.Host().Connect(ctx, peer.AddrInfo{ID: bID, Addrs: []ma.Multiaddr{circuit}}))
	connected, relayed := a.ConnInfo(bID)
	require.True(t, connected)
	require.True(t, relayed)

	// B's direct address, as addrshare would deliver it.
	a.Host().Peerstore().AddAddrs(bID, b.Host().Network().ListenAddresses()[:1], time.Hour)
	require.NoError(t, a.DialDirect(t.Context(), bID))
	connected, relayed = a.ConnInfo(bID)
	require.True(t, connected)
	require.False(t, relayed)
	require.Eventually(t, func() bool {
		for _, c := range a.Host().Network().ConnsToPeer(bID) {
			if isCircuit(c.RemoteMultiaddr()) {
				return false
			}
		}
		return true
	}, 5*time.Second, 50*time.Millisecond, "the relayed connection was not closed")

	// Already direct: nothing to do.
	require.NoError(t, a.DialDirect(t.Context(), bID))
}

func TestDialDirectWithoutAddresses(t *testing.T) {
	a := newTestNode(t)
	nobody, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)
	require.ErrorIs(t, a.DialDirect(t.Context(), nobody), errNoDirectAddrs)
}

func TestDialFailures(t *testing.T) {
	a := newTestNode(t)
	ghost, err := peer.IDFromPrivateKey(newTestKey(t))
	require.NoError(t, err)
	a.Host().Peerstore().AddAddrs(ghost, []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1")}, time.Hour)
	err = a.DialDirect(t.Context(), ghost)
	require.Error(t, err)
	require.Equal(t, []string{"/ip4/127.0.0.1/tcp/1: refused (nothing listens there)"}, DialFailures(err))
	require.Nil(t, DialFailures(errNoDirectAddrs))
}
