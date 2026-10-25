package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"twocan/internal/store"
)

func TestContactDetailsTracksLastSeenAndProblems(t *testing.T) {
	mn := newMocknet(t)
	iv := intervals{tick: 100 * time.Millisecond}
	a, b := newMockNode(t, mn, iv), newMockNode(t, mn, iv)
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)

	d, err := a.ContactDetails(t.Context(), b.PeerID())
	require.NoError(t, err)
	require.Equal(t, store.StateAccepted, d.Contact.State)
	require.True(t, d.LastSeen.IsZero())
	require.Empty(t, d.LastProblem)
	require.True(t, d.NextAttempt.IsZero(), "connected: nothing to schedule")

	// Outage: the next send fails, which shows as the last problem.
	require.NoError(t, mn.UnlinkPeers(a.PeerID(), b.PeerID()))
	require.NoError(t, mn.DisconnectPeers(a.PeerID(), b.PeerID()))
	_, err = a.Send(t.Context(), b.PeerID(), "hi")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		d, err = a.ContactDetails(t.Context(), b.PeerID())
		return err == nil && d.LastProblem != "" && !d.LastSeen.IsZero()
	}, waitFor, poll)
	require.WithinDuration(t, time.Now(), d.LastSeen, waitFor)
	require.WithinDuration(t, time.Now(), d.LastProblemAt, waitFor)
	require.False(t, d.NextAttempt.IsZero(), "a retry is scheduled")

	// Back online: the problem and last-seen time are cleared.
	_, err = mn.LinkPeers(a.PeerID(), b.PeerID())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		d, err = a.ContactDetails(t.Context(), b.PeerID())
		connected, _ := a.connInfo(b.PeerID())
		return err == nil && connected && d.LastProblem == "" && d.LastSeen.IsZero()
	}, 15*time.Second, poll)
}

func TestContactDetailsAndCheckNeedAContact(t *testing.T) {
	mn := newMocknet(t)
	a := newMockNode(t, mn, intervals{})
	stranger := randomPeer(t)

	_, err := a.ContactDetails(t.Context(), stranger)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = a.CheckConnection(t.Context(), stranger, nil)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = a.Ping(t.Context(), stranger)
	require.ErrorIs(t, err, ErrNotConnected)
}

func TestCheckConnectionUnavailableOverMocknet(t *testing.T) {
	mn := newMocknet(t)
	a, b := newMockNode(t, mn, intervals{}), newMockNode(t, mn, intervals{})
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)
	_, err := a.CheckConnection(t.Context(), b.PeerID(), nil)
	require.ErrorIs(t, err, errNoDiagnostics)
}
