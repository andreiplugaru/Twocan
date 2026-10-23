package helper

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/stretchr/testify/require"

	"twocan/internal/identity"
)

var loopback = []string{"/ip4/127.0.0.1/tcp/0"}

func TestResourcesLimits(t *testing.T) {
	r := Resources()
	require.NotNil(t, r.Limit)
	require.Equal(t, 30*time.Minute, r.Limit.Duration)
	require.Equal(t, int64(4<<20), r.Limit.Data)
	require.Equal(t, relay.DefaultResources().MaxCircuits, r.MaxCircuits)
}

func TestStartStableIdentity(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "helper.key")
	ids := make([]peer.ID, 2)
	for i := range ids {
		key, err := identity.LoadOrCreateKey(keyPath)
		require.NoError(t, err)
		h, d, err := Start(context.Background(), key, loopback)
		require.NoError(t, err)
		ids[i] = h.ID()
		require.NoError(t, d.Close())
		require.NoError(t, h.Close())
	}
	require.Equal(t, ids[0], ids[1])
}

func TestHelperSpeaksTwocanDHT(t *testing.T) {
	key, err := identity.LoadOrCreateKey(filepath.Join(t.TempDir(), "helper.key"))
	require.NoError(t, err)
	h, d, err := Start(context.Background(), key, loopback)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close(); _ = h.Close() })

	c, err := libp2p.New(libp2p.ListenAddrStrings(loopback...))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, c.Connect(ctx, peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}))
	require.Eventually(t, func() bool {
		ps, err := c.Peerstore().GetProtocols(h.ID())
		if err != nil {
			return false
		}
		for _, p := range ps {
			if p == "/twocan/kad/1.0.0" {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
}
