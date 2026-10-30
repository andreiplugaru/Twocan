package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A contact that disconnects is reached again at once, not at the next
// presence sweep (minutes away with the default intervals).
func TestReconnectsRightAfterDisconnect(t *testing.T) {
	mn := newMocknet(t)
	a := newMockNode(t, mn, intervals{})
	b := newMockNode(t, mn, intervals{})
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)

	require.NoError(t, mn.DisconnectPeers(a.PeerID(), b.PeerID()))
	require.Eventually(t, func() bool {
		connected, _ := a.connInfo(b.PeerID())
		return connected
	}, waitFor, poll, "reconnected without waiting for the presence sweep")
}

// Reconnecting keeps trying for a while: right after a network switch the
// first try usually fails.
func TestReconnectRetries(t *testing.T) {
	mn := newMocknet(t)
	iv := intervals{reconnect: 20 * time.Millisecond}
	a := newMockNode(t, mn, iv)
	b := newMockNode(t, mn, iv)
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)

	require.NoError(t, mn.UnlinkPeers(a.PeerID(), b.PeerID()))
	require.NoError(t, mn.DisconnectPeers(a.PeerID(), b.PeerID()))
	time.Sleep(100 * time.Millisecond) // the first tries fail
	_, err := mn.LinkPeers(a.PeerID(), b.PeerID())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		connected, _ := a.connInfo(b.PeerID())
		return connected
	}, waitFor, poll, "a later try reconnected")
}

// While reconnecting, the next try is recorded for the details to show.
func TestReconnectRecordsNextTry(t *testing.T) {
	mn := newMocknet(t)
	iv := intervals{reconnect: time.Minute}
	a := newMockNode(t, mn, iv)
	b := newMockNode(t, mn, iv)
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)

	require.NoError(t, mn.UnlinkPeers(a.PeerID(), b.PeerID()))
	require.NoError(t, mn.DisconnectPeers(a.PeerID(), b.PeerID()))
	require.Eventually(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		next := time.Until(a.rejoining[b.PeerID()])
		return next > 50*time.Second && next <= time.Minute
	}, waitFor, poll, "the next try is a minute after the first")
}
