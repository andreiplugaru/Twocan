package core

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"

	"twocan/internal/store"
)

// After a network change, contacts are reached at once, not at their next
// reconnect try or presence sweep.
func TestNetworkChangeReachesContactsAtOnce(t *testing.T) {
	mn := newMocknet(t)
	iv := intervals{reconnect: time.Minute, settle: 10 * time.Millisecond}
	a := newMockNode(t, mn, iv)
	b := newMockNode(t, mn, iv)
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)

	require.NoError(t, mn.UnlinkPeers(a.PeerID(), b.PeerID()))
	require.NoError(t, mn.DisconnectPeers(a.PeerID(), b.PeerID()))
	require.Eventually(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return time.Until(a.rejoining[b.PeerID()]) > 50*time.Second
	}, waitFor, poll, "the first reconnect try failed; the next is a minute away")
	_, err := mn.LinkPeers(a.PeerID(), b.PeerID())
	require.NoError(t, err)

	a.NetworkChanged()
	require.Eventually(t, func() bool {
		connected, _ := a.connInfo(b.PeerID())
		return connected
	}, waitFor, poll, "reconnected right after the network change")
}

// watchingReacher is a mocknet reacher that counts the network checks.
type watchingReacher struct {
	mnReacher
	probes, helpers atomic.Int32
}

func (r *watchingReacher) CloseUnresponsive(context.Context, time.Duration) []peer.ID {
	r.probes.Add(1)
	return nil
}

func (r *watchingReacher) ReconnectHelpers(context.Context) { r.helpers.Add(1) }

// The burst of notifications one network change brings is handled once.
func TestNetworkChangesAreCoalesced(t *testing.T) {
	mn := newMocknet(t)
	h, err := mn.GenPeer()
	require.NoError(t, err)
	st, err := store.Open(filepath.Join(t.TempDir(), "twocan.db"))
	require.NoError(t, err)
	r := &watchingReacher{mnReacher: mnReacher{mn: mn, self: h.ID()}}
	connInfo := func(id peer.ID) (bool, bool) { return h.Network().Connectedness(id) == network.Connected, false }
	n, err := startWith(t.Context(), h, st, r, connInfo, intervals{settle: 50 * time.Millisecond}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, n.Stop()) })

	for range 5 {
		n.NetworkChanged()
		time.Sleep(10 * time.Millisecond)
	}
	require.Eventually(t, func() bool { return r.probes.Load() == 1 && r.helpers.Load() == 1 }, waitFor, poll)
	time.Sleep(200 * time.Millisecond)
	require.EqualValues(t, 1, r.probes.Load(), "one check for the burst")

	n.NetworkChanged()
	require.Eventually(t, func() bool { return r.probes.Load() == 2 && r.helpers.Load() == 2 }, waitFor, poll, "a later change is checked again")
}
