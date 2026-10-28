package core

import (
	"testing"

	"github.com/stretchr/testify/require"

	"twocan/internal/event"
	"twocan/internal/store"
)

// The new chat features through core.API, between two nodes on a mocknet.
func TestChatFeaturesEndToEnd(t *testing.T) {
	mn := newMocknet(t)
	a := newMockNode(t, mn, intervals{})
	b := newMockNode(t, mn, intervals{})
	require.NoError(t, mn.LinkAll())
	befriend(t, a, b)
	aEvents, cancelA := a.Subscribe()
	defer cancelA()

	msg := func(n *Node, other *Node, id []byte) store.Message {
		m, err := n.st.GetMessage(t.Context(), other.PeerID(), id)
		require.NoError(t, err)
		return m
	}
	eventually := func(cond func() bool, what string) {
		t.Helper()
		require.Eventually(t, cond, waitFor, poll, what)
	}

	q, err := a.Send(t.Context(), b.PeerID(), "Shall we hike up the mountain?")
	require.NoError(t, err)
	eventually(func() bool { _, err := b.st.GetMessage(t.Context(), a.PeerID(), q.ID); return err == nil }, "B got the question")

	r, err := b.Reply(t.Context(), a.PeerID(), q.ID, "Yes, *saturday*")
	require.NoError(t, err)
	eventually(func() bool { _, err := a.st.GetMessage(t.Context(), b.PeerID(), r.ID); return err == nil }, "A got the reply")
	require.Equal(t, q.ID, msg(a, b, r.ID).ReplyTo)

	require.NoError(t, b.MarkRead(t.Context(), a.PeerID()))
	waitEvent(t, aEvents, func(e event.MessagesUpdated) bool { return len(e.IDs) == 1 && string(e.IDs[0]) == string(q.ID) })
	require.Equal(t, store.StatusRead, msg(a, b, q.ID).Status)

	require.NoError(t, a.Edit(t.Context(), b.PeerID(), q.ID, "Shall we hike up the hill?"))
	eventually(func() bool { return msg(b, a, q.ID).Text == "Shall we hike up the hill?" }, "B sees the edit")

	require.NoError(t, a.React(t.Context(), b.PeerID(), r.ID, "👍"))
	eventually(func() bool { return msg(b, a, r.ID).TheirReaction == "👍" }, "B sees the reaction")

	found, err := b.Search(t.Context(), "hill")
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, q.ID, found[0].ID)

	require.NoError(t, a.Delete(t.Context(), b.PeerID(), q.ID, true))
	eventually(func() bool { return msg(b, a, q.ID).Deleted }, "B sees the deletion")
	found, err = b.Search(t.Context(), "hill")
	require.NoError(t, err)
	require.Empty(t, found)
}
