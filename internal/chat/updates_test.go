package chat

import (
	"bufio"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"

	"twocan/internal/event"
	pb "twocan/internal/proto"
	"twocan/internal/store"
	"twocan/internal/wire"
)

// sendDelivered sends text from a to b and waits until b has it.
func sendDelivered(t *testing.T, a, b *testPeer, text string, replyTo []byte) store.Message {
	t.Helper()
	m, err := a.svc.Send(t.Context(), b.id(), text, replyTo)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := b.st.GetMessage(t.Context(), a.id(), m.ID)
		return err == nil && got.Text == text
	}, waitFor, tick)
	return m
}

// get returns p's copy of message id in its conversation with other.
func get(t *testing.T, p *testPeer, other *testPeer, id []byte) store.Message {
	t.Helper()
	m, err := p.st.GetMessage(t.Context(), other.id(), id)
	require.NoError(t, err)
	return m
}

func TestReply(t *testing.T) {
	_, a, b := pair(t)
	first := sendDelivered(t, a, b, "lunch?", nil)
	reply := sendDelivered(t, b, a, "yes!", first.ID)
	require.Equal(t, first.ID, get(t, a, b, reply.ID).ReplyTo)

	_, err := a.svc.Send(t.Context(), b.id(), "to nothing", testID(9))
	require.ErrorIs(t, err, ErrNoSuchMessage)
}

func TestEdit(t *testing.T) {
	_, a, b := pair(t)
	bEvents, cancel := b.bus.Subscribe()
	defer cancel()
	m := sendDelivered(t, a, b, "helo", nil)

	require.NoError(t, a.svc.Edit(t.Context(), b.id(), m.ID, "hello"))
	require.Equal(t, "hello", get(t, a, b, m.ID).Text)
	waitEvent(t, bEvents, func(e event.MessagesUpdated) bool { return len(e.IDs) == 1 && string(e.IDs[0]) == string(m.ID) })
	got := get(t, b, a, m.ID)
	require.Equal(t, "hello", got.Text)
	require.False(t, got.EditedAt.IsZero())

	require.ErrorIs(t, b.svc.Edit(t.Context(), a.id(), m.ID, "mine now"), ErrNotYours)
	require.ErrorIs(t, a.svc.Edit(t.Context(), b.id(), testID(9), "x"), ErrNoSuchMessage)
	require.ErrorIs(t, a.svc.Edit(t.Context(), b.id(), m.ID, "  "), ErrEmpty)
}

func TestDeleteForEveryone(t *testing.T) {
	mn, a, b := pair(t)
	m := sendDelivered(t, a, b, "oops", nil)
	require.NoError(t, b.svc.React(t.Context(), a.id(), m.ID, "😂"))

	require.NoError(t, a.svc.Delete(t.Context(), b.id(), m.ID, true))
	require.True(t, get(t, a, b, m.ID).Deleted)
	require.Eventually(t, func() bool { return get(t, b, a, m.ID).Deleted }, waitFor, tick)
	got := get(t, b, a, m.ID)
	require.Empty(t, got.Text)
	require.Empty(t, got.TheirReaction)
	require.ErrorIs(t, b.svc.Delete(t.Context(), a.id(), m.ID, true), ErrNotYours)

	// Deleted before it went out: it never does.
	goOffline(t, mn, a, b)
	pending, err := a.svc.Send(t.Context(), b.id(), "never mind", nil)
	require.NoError(t, err)
	require.NoError(t, a.svc.Delete(t.Context(), b.id(), pending.ID, true))
	goOnline(t, mn, a, b)
	require.NoError(t, a.svc.Flush(t.Context(), b.id()))
	_, err = b.st.GetMessage(t.Context(), a.id(), pending.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestDeleteForMe(t *testing.T) {
	_, a, b := pair(t)
	m := sendDelivered(t, a, b, "keep or not", nil)
	require.NoError(t, b.svc.Delete(t.Context(), a.id(), m.ID, false), "anyone's message, for me")
	_, err := b.st.GetMessage(t.Context(), a.id(), m.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
	require.Equal(t, "keep or not", get(t, a, b, m.ID).Text, "the other copy stays")
	require.ErrorIs(t, b.svc.Delete(t.Context(), a.id(), m.ID, false), ErrNoSuchMessage)
}

func TestReact(t *testing.T) {
	_, a, b := pair(t)
	m := sendDelivered(t, a, b, "news", nil)

	require.NoError(t, b.svc.React(t.Context(), a.id(), m.ID, "❤️"))
	require.Equal(t, "❤️", get(t, b, a, m.ID).MyReaction)
	require.Eventually(t, func() bool { return get(t, a, b, m.ID).TheirReaction == "❤️" }, waitFor, tick)
	require.NoError(t, b.svc.React(t.Context(), a.id(), m.ID, ""))
	require.Eventually(t, func() bool { return get(t, a, b, m.ID).TheirReaction == "" }, waitFor, tick)

	require.ErrorIs(t, b.svc.React(t.Context(), a.id(), m.ID, "a b"), ErrBadReaction)
	require.ErrorIs(t, b.svc.React(t.Context(), a.id(), m.ID, string(make([]byte, 40))), ErrBadReaction)
	require.ErrorIs(t, b.svc.React(t.Context(), a.id(), testID(9), "👍"), ErrNoSuchMessage)
}

func TestReadReceipts(t *testing.T) {
	_, a, b := pair(t)
	aEvents, cancel := a.bus.Subscribe()
	defer cancel()
	m := sendDelivered(t, a, b, "read me", nil)
	require.Eventually(t, func() bool { return get(t, a, b, m.ID).Status == store.StatusDelivered }, waitFor, tick)

	require.NoError(t, b.svc.MarkRead(t.Context(), a.id()))
	waitEvent(t, aEvents, func(e event.MessagesUpdated) bool { return len(e.IDs) == 1 && string(e.IDs[0]) == string(m.ID) })
	require.Equal(t, store.StatusRead, get(t, a, b, m.ID).Status)
}

// Updates made while the contact is offline go out when they are back.
func TestUpdatesQueueWhileOffline(t *testing.T) {
	mn, a, b := pair(t)
	m := sendDelivered(t, a, b, "first", nil)
	goOffline(t, mn, a, b)
	require.NoError(t, a.svc.Edit(t.Context(), b.id(), m.ID, "second"))
	require.NoError(t, a.svc.React(t.Context(), b.id(), m.ID, "👍"))
	require.Error(t, a.svc.Flush(t.Context(), b.id()))
	us, err := a.st.PendingUpdates(t.Context(), b.id())
	require.NoError(t, err)
	require.Len(t, us, 2)

	goOnline(t, mn, a, b)
	require.NoError(t, a.svc.Flush(t.Context(), b.id()))
	got := get(t, b, a, m.ID)
	require.Equal(t, "second", got.Text)
	require.Equal(t, "👍", got.TheirReaction)
	us, err = a.st.PendingUpdates(t.Context(), b.id())
	require.NoError(t, err)
	require.Empty(t, us)
}

// A contact on an older Twocan without updates still gets messages.
func TestOldPeerStillGetsMessages(t *testing.T) {
	_, a, b := pair(t)
	m := sendDelivered(t, a, b, "hi", nil)
	b.h.RemoveStreamHandler(UpdateProtocolID)
	require.NoError(t, a.svc.React(t.Context(), b.id(), m.ID, "👍"))
	second := sendDelivered(t, a, b, "still works", nil)
	require.Equal(t, "still works", get(t, b, a, second.ID).Text)
}

func TestTyping(t *testing.T) {
	mn, a, b := pair(t)
	bEvents, cancel := b.bus.Subscribe()
	defer cancel()
	a.svc.Typing(b.id())
	require.Never(t, func() bool { return len(drain(bEvents)) > 0 }, 200*time.Millisecond, tick, "not connected: nothing is sent")

	a.svc.lastTyping = map[peer.ID]time.Time{} // forget the throttle
	_, err := mn.ConnectPeers(a.id(), b.id())
	require.NoError(t, err)
	a.svc.Typing(b.id())
	waitEvent(t, bEvents, func(e event.Typing) bool { return e.Peer == a.id() })
	a.svc.Typing(b.id())
	require.Never(t, func() bool {
		for _, e := range drain(bEvents) {
			if _, ok := e.(event.Typing); ok {
				return true
			}
		}
		return false
	}, 300*time.Millisecond, tick, "throttled")
}

func TestUpdateStreamRejectsStrangers(t *testing.T) {
	mn, _, b := pair(t)
	stranger := newPeer(t, mn)
	require.NoError(t, mn.LinkAll())
	require.NoError(t, mnReacher{mn: mn, self: stranger.id()}.Reach(t.Context(), b.id()))
	us, err := stranger.h.NewStream(t.Context(), b.id(), UpdateProtocolID)
	require.NoError(t, err)
	defer us.Reset()
	require.NoError(t, wire.WriteFrame(us, &pb.UpdateFrame{Body: &pb.UpdateFrame_Update{Update: &pb.Update{
		Id: testID(1), Change: &pb.Update_Delete{Delete: &pb.Delete{MsgId: testID(2)}},
	}}}))
	var reply pb.UpdateFrame
	require.ErrorIs(t, wire.ReadFrame(bufio.NewReader(us), &reply), network.ErrReset)
}
