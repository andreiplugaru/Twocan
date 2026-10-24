package chat

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"twocan/internal/event"
	"twocan/internal/logtest"
	pb "twocan/internal/proto"
	"twocan/internal/store"
	"twocan/internal/wire"
)

const (
	waitFor = 5 * time.Second
	tick    = 10 * time.Millisecond
)

// mnReacher implements p2p.Reacher over a mocknet: reaching a peer means
// connecting to it, which fails when the two peers are unlinked.
type mnReacher struct {
	mn   mocknet.Mocknet
	self peer.ID
}

func (r mnReacher) Reach(_ context.Context, id peer.ID) error {
	if r.mn.Net(r.self).Connectedness(id) == network.Connected {
		return nil
	}
	_, err := r.mn.ConnectPeers(r.self, id)
	return err
}

type testPeer struct {
	h      host.Host
	st     *store.Store
	dbPath string
	bus    *event.Bus
	svc    *Service
}

func (p *testPeer) id() peer.ID { return p.h.ID() }

func (p *testPeer) history(t *testing.T, id peer.ID) []store.Message {
	t.Helper()
	ms, err := p.st.History(t.Context(), id)
	require.NoError(t, err)
	return ms
}

func (p *testPeer) pending(t *testing.T, id peer.ID) []store.Message {
	t.Helper()
	ms, err := p.st.PendingOutgoing(t.Context(), id)
	require.NoError(t, err)
	return ms
}

func (p *testPeer) setContact(t *testing.T, id peer.ID, st store.ContactState) {
	t.Helper()
	require.NoError(t, p.st.UpsertContact(t.Context(), store.Contact{PeerID: id, State: st}))
}

// newPeer creates a mocknet host with its own store and a started Service.
// Call mn.LinkAll() after creating all peers.
func newPeer(t *testing.T, mn mocknet.Mocknet) *testPeer {
	t.Helper()
	h, err := mn.GenPeer()
	require.NoError(t, err)
	dbPath := filepath.Join(t.TempDir(), "twocan.db")
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	bus := event.NewBus()
	svc := New(h, st, mnReacher{mn: mn, self: h.ID()}, bus)
	svc.Start()
	t.Cleanup(svc.Close) // runs before st.Close (cleanup is LIFO)
	return &testPeer{h: h, st: st, dbPath: dbPath, bus: bus, svc: svc}
}

// pair returns two linked peers that are accepted contacts of each other.
func pair(t *testing.T) (mocknet.Mocknet, *testPeer, *testPeer) {
	t.Helper()
	mn := mocknet.New()
	t.Cleanup(func() { mn.Close() })
	a, b := newPeer(t, mn), newPeer(t, mn)
	require.NoError(t, mn.LinkAll())
	a.setContact(t, b.id(), store.StateAccepted)
	b.setContact(t, a.id(), store.StateAccepted)
	return mn, a, b
}

func goOffline(t *testing.T, mn mocknet.Mocknet, a, b *testPeer) {
	t.Helper()
	require.NoError(t, mn.UnlinkPeers(a.id(), b.id()))
	require.NoError(t, mn.DisconnectPeers(a.id(), b.id()))
}

func goOnline(t *testing.T, mn mocknet.Mocknet, a, b *testPeer) {
	t.Helper()
	_, err := mn.LinkPeers(a.id(), b.id())
	require.NoError(t, err)
}

func randomPeer(t *testing.T) peer.ID {
	t.Helper()
	_, pub, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	id, err := peer.IDFromPublicKey(pub)
	require.NoError(t, err)
	return id
}

// waitEvent returns the first event on ch matching match, failing after waitFor.
func waitEvent[T any](t *testing.T, ch <-chan any, match func(T) bool) T {
	t.Helper()
	deadline := time.After(waitFor)
	for {
		select {
		case e := <-ch:
			if v, ok := e.(T); ok && match(v) {
				return v
			}
		case <-deadline:
			var zero T
			t.Fatalf("no %T event", zero)
			return zero
		}
	}
}

// drain returns every event already buffered on ch.
func drain(ch <-chan any) []any {
	var out []any
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

func msgFrame(id []byte, sentAt int64, text string) *pb.ChatFrame {
	return &pb.ChatFrame{Body: &pb.ChatFrame_Message{Message: &pb.ChatMessage{Id: id, SentAt: sentAt, Text: text}}}
}

func testID(b byte) []byte { return bytes.Repeat([]byte{b}, 16) }

// rawStream opens a chat stream from h to id without going through a Service.
func rawStream(t *testing.T, mn mocknet.Mocknet, h host.Host, id peer.ID) (network.Stream, *bufio.Reader) {
	t.Helper()
	require.NoError(t, mnReacher{mn: mn, self: h.ID()}.Reach(t.Context(), id))
	s, err := h.NewStream(t.Context(), id, ProtocolID)
	require.NoError(t, err)
	t.Cleanup(func() { s.Reset() })
	return s, bufio.NewReader(s)
}

// requireReset asserts that the remote reset the stream instead of replying.
func requireReset(t *testing.T, r *bufio.Reader) {
	t.Helper()
	var reply pb.ChatFrame
	err := wire.ReadFrame(r, &reply)
	require.ErrorIs(t, err, network.ErrReset, "reply: %v", &reply)
}

func TestSendDelivered(t *testing.T) {
	_, a, b := pair(t)
	aEvents, cancelA := a.bus.Subscribe()
	defer cancelA()
	bEvents, cancelB := b.bus.Subscribe()
	defer cancelB()

	m, err := a.svc.Send(t.Context(), b.id(), "hi")
	require.NoError(t, err)
	require.Len(t, m.ID, 16)
	require.Equal(t, b.id(), m.PeerID)
	require.Equal(t, store.DirOut, m.Direction)
	require.Equal(t, store.StatusPending, m.Status)
	require.Equal(t, "hi", m.Text)
	require.False(t, m.SentAt.IsZero())

	got := waitEvent(t, bEvents, func(e event.MessageReceived) bool { return true })
	require.Equal(t, "hi", got.Msg.Text)
	require.Equal(t, m.ID, got.Msg.ID)
	require.Equal(t, a.id(), got.Msg.PeerID)
	require.Equal(t, store.DirIn, got.Msg.Direction)
	require.Equal(t, store.StatusReceived, got.Msg.Status)
	require.Equal(t, m.SentAt.UnixMilli(), got.Msg.SentAt.UnixMilli())
	require.False(t, got.Msg.ReceivedAt.IsZero())

	d := waitEvent(t, aEvents, func(event.MessageDelivered) bool { return true })
	require.Equal(t, event.MessageDelivered{Peer: b.id(), ID: m.ID}, d)

	ha := a.history(t, b.id())
	require.Len(t, ha, 1)
	require.Equal(t, store.StatusDelivered, ha[0].Status)
	hb := b.history(t, a.id())
	require.Len(t, hb, 1)
	require.Equal(t, "hi", hb[0].Text)
	require.Equal(t, store.StatusReceived, hb[0].Status)
}

func TestLogsDeliveryWithoutMessageText(t *testing.T) {
	_, a, b := pair(t)
	alog, alogs := logtest.New()
	blog, blogs := logtest.New()
	a.svc.SetLogger(alog)
	b.svc.SetLogger(blog)
	aEvents, cancelA := a.bus.Subscribe()
	defer cancelA()

	const secret = "secret-text-7f3a"
	m, err := a.svc.Send(t.Context(), b.id(), secret)
	require.NoError(t, err)
	waitEvent(t, aEvents, func(event.MessageDelivered) bool { return true })
	require.Eventually(t, func() bool {
		return strings.Contains(alogs.String(), `msg="messages delivered"`)
	}, 5*time.Second, 10*time.Millisecond)

	id := msgID(m.ID)
	require.Contains(t, alogs.String(), `msg="message queued" component=chat peer=`+b.id().String()+` id=`+id)
	require.Contains(t, alogs.String(), `msg="message delivered" component=chat peer=`+b.id().String()+` id=`+id)
	require.Contains(t, blogs.String(), `msg="message received" component=chat peer=`+a.id().String()+` id=`+id)
	require.NotContains(t, alogs.String(), secret)
	require.NotContains(t, blogs.String(), secret)
}

func TestLogsWhyAnInboundFrameWasRejected(t *testing.T) {
	mn, a, b := pair(t)
	blog, blogs := logtest.New()
	b.svc.SetLogger(blog)
	s, r := rawStream(t, mn, a.h, b.id())
	require.NoError(t, wire.WriteFrame(s, msgFrame(testID(1), 0, "hello")))
	requireReset(t, r)
	require.Eventually(t, func() bool {
		return strings.Contains(blogs.String(), `msg="chat stream reset"`) &&
			strings.Contains(blogs.String(), "missing send time")
	}, 5*time.Second, 10*time.Millisecond)
}

func TestSendValidation(t *testing.T) {
	_, a, b := pair(t)
	ctx := t.Context()

	_, err := a.svc.Send(ctx, b.id(), "   \n")
	require.ErrorIs(t, err, ErrEmpty)
	_, err = a.svc.Send(ctx, b.id(), "")
	require.ErrorIs(t, err, ErrEmpty)
	_, err = a.svc.Send(ctx, b.id(), strings.Repeat("x", MaxTextBytes+1))
	require.ErrorIs(t, err, ErrTooLong)
	_, err = a.svc.Send(ctx, b.id(), "bad \xff text")
	require.ErrorIs(t, err, ErrInvalidUTF8)

	_, err = a.svc.Send(ctx, randomPeer(t), "hi")
	require.ErrorIs(t, err, ErrNotContact)
	pendingPeer := randomPeer(t)
	a.setContact(t, pendingPeer, store.StatePendingOutgoing)
	_, err = a.svc.Send(ctx, pendingPeer, "hi")
	require.ErrorIs(t, err, ErrNotContact)
	require.Empty(t, a.history(t, pendingPeer))

	// Nothing invalid was stored.
	require.Empty(t, a.history(t, b.id()))

	big := strings.Repeat("y", MaxTextBytes)
	m, err := a.svc.Send(ctx, b.id(), big)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		hb := b.history(t, a.id())
		return len(hb) == 1 && hb[0].Text == big && bytes.Equal(hb[0].ID, m.ID)
	}, waitFor, tick)
}

func TestOfflineQueueThenFlush(t *testing.T) {
	mn, a, b := pair(t)
	goOffline(t, mn, a, b)

	texts := []string{"one", "two", "three"}
	var ids [][]byte
	for _, txt := range texts {
		m, err := a.svc.Send(t.Context(), b.id(), txt)
		require.NoError(t, err)
		ids = append(ids, m.ID)
	}
	require.Len(t, a.pending(t, b.id()), 3)
	require.Error(t, a.svc.Flush(t.Context(), b.id()))
	require.Len(t, a.pending(t, b.id()), 3)
	require.Empty(t, b.history(t, a.id()))

	goOnline(t, mn, a, b)
	require.NoError(t, a.svc.Flush(t.Context(), b.id()))

	require.Empty(t, a.pending(t, b.id()))
	for _, m := range a.history(t, b.id()) {
		require.Equal(t, store.StatusDelivered, m.Status)
	}
	hb := b.history(t, a.id())
	require.Len(t, hb, 3)
	for i, m := range hb {
		require.Equal(t, texts[i], m.Text)
		require.Equal(t, ids[i], m.ID)
	}
}

func TestDuplicateDeliveryDeduped(t *testing.T) {
	mn, a, b := pair(t)
	events, cancel := b.bus.Subscribe()
	defer cancel()

	s, r := rawStream(t, mn, a.h, b.id())
	f := msgFrame(testID(7), time.Now().UnixMilli(), "once")
	for range 2 {
		require.NoError(t, wire.WriteFrame(s, f))
		var reply pb.ChatFrame
		require.NoError(t, wire.ReadFrame(r, &reply))
		require.NotNil(t, reply.GetAck())
		require.Equal(t, testID(7), reply.GetAck().GetId())
	}
	require.NoError(t, s.Close())

	require.Len(t, b.history(t, a.id()), 1)
	// MessageReceived is published before the ack is written.
	n := 0
	for _, e := range drain(events) {
		if _, ok := e.(event.MessageReceived); ok {
			n++
		}
	}
	require.Equal(t, 1, n)
}

func TestNonContactDropped(t *testing.T) {
	mn := mocknet.New()
	t.Cleanup(func() { mn.Close() })
	b, c := newPeer(t, mn), newPeer(t, mn)
	require.NoError(t, mn.LinkAll())
	c.setContact(t, b.id(), store.StateAccepted) // one-sided: B doesn't know C

	s, r := rawStream(t, mn, c.h, b.id())
	_ = wire.WriteFrame(s, msgFrame(testID(1), time.Now().UnixMilli(), "hello"))
	requireReset(t, r)
	require.Empty(t, b.history(t, c.id()))

	// A pending (not yet accepted) contact is dropped too.
	b.setContact(t, c.id(), store.StatePendingIncoming)
	s, r = rawStream(t, mn, c.h, b.id())
	_ = wire.WriteFrame(s, msgFrame(testID(2), time.Now().UnixMilli(), "hello"))
	requireReset(t, r)
	require.Empty(t, b.history(t, c.id()))
}

// writeInvalidUTF8 writes a ChatMessage frame whose text is not valid UTF-8.
// proto.Marshal refuses such strings, so the bytes are patched after marshalling.
func writeInvalidUTF8(t *testing.T, s network.Stream) {
	t.Helper()
	b, err := proto.Marshal(msgFrame(testID(3), time.Now().UnixMilli(), "ok@@ok"))
	require.NoError(t, err)
	b = bytes.Replace(b, []byte("@@"), []byte{0xff, 0xfe}, 1)
	_, err = s.Write(append(protowire.AppendVarint(nil, uint64(len(b))), b...))
	require.NoError(t, err)
}

func TestHandlerRejectsInvalidFrames(t *testing.T) {
	mn, a, b := pair(t)
	now := time.Now().UnixMilli()
	cases := map[string]func(network.Stream){
		"15-byte id": func(s network.Stream) {
			_ = wire.WriteFrame(s, msgFrame(testID(1)[:15], now, "hi"))
		},
		"empty text": func(s network.Stream) {
			_ = wire.WriteFrame(s, msgFrame(testID(2), now, ""))
		},
		"blank text": func(s network.Stream) {
			_ = wire.WriteFrame(s, msgFrame(testID(3), now, " \t\n"))
		},
		"too long": func(s network.Stream) {
			_ = wire.WriteFrame(s, msgFrame(testID(4), now, strings.Repeat("z", MaxTextBytes+1)))
		},
		"invalid utf-8": func(s network.Stream) { writeInvalidUTF8(t, s) },
		"sent_at 0": func(s network.Stream) {
			_ = wire.WriteFrame(s, msgFrame(testID(5), 0, "hi"))
		},
		"ack frame": func(s network.Stream) {
			_ = wire.WriteFrame(s, &pb.ChatFrame{Body: &pb.ChatFrame_Ack{Ack: &pb.Ack{Id: testID(6)}}})
		},
		"empty frame": func(s network.Stream) {
			_ = wire.WriteFrame(s, &pb.ChatFrame{})
		},
	}
	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			s, r := rawStream(t, mn, a.h, b.id())
			write(s)
			requireReset(t, r)
			require.Empty(t, b.history(t, a.id()))
		})
	}

	m, err := a.svc.Send(t.Context(), b.id(), "still works")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		hb := b.history(t, a.id())
		return len(hb) == 1 && bytes.Equal(hb[0].ID, m.ID)
	}, waitFor, tick)
}

func TestFlushInterruptedThenResumed(t *testing.T) {
	mn, a, b := pair(t)
	var interrupt atomic.Bool
	interrupt.Store(true)
	// Set before any traffic; toggled through the atomic afterwards.
	b.svc.afterAck = func(n int) bool { return interrupt.Load() && n == 2 }

	goOffline(t, mn, a, b)
	for i := range 5 {
		_, err := a.svc.Send(t.Context(), b.id(), strings.Repeat("m", i+1))
		require.NoError(t, err)
	}
	a.svc.tasks.Wait() // let the background flushes started by Send fail first
	goOnline(t, mn, a, b)

	require.Error(t, a.svc.Flush(t.Context(), b.id()))
	require.Len(t, a.pending(t, b.id()), 3)
	delivered := 0
	for _, m := range a.history(t, b.id()) {
		if m.Status == store.StatusDelivered {
			delivered++
		}
	}
	require.Equal(t, 2, delivered)
	require.Len(t, b.history(t, a.id()), 2)

	interrupt.Store(false)
	require.NoError(t, a.svc.Flush(t.Context(), b.id()))
	require.Empty(t, a.pending(t, b.id()))
	for _, m := range a.history(t, b.id()) {
		require.Equal(t, store.StatusDelivered, m.Status)
	}
	hb := b.history(t, a.id())
	require.Len(t, hb, 5)
	for i, m := range hb {
		require.Equal(t, strings.Repeat("m", i+1), m.Text)
	}
}

// failMessageInserts makes every insert into p's messages table fail, through
// a second connection to the same database; contact lookups keep working.
func failMessageInserts(t *testing.T, p *testPeer) {
	t.Helper()
	db, err := sql.Open("sqlite", p.dbPath)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.ExecContext(t.Context(),
		`CREATE TRIGGER fail_inserts BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	require.NoError(t, err)
}

func TestReceiverStoreFailureNotAcked(t *testing.T) {
	_, a, b := pair(t)
	events, cancel := b.bus.Subscribe()
	defer cancel()
	failMessageInserts(t, b)

	m, err := a.svc.Send(t.Context(), b.id(), "not acked")
	require.NoError(t, err)
	a.svc.tasks.Wait() // the background flush fails too
	require.Error(t, a.svc.Flush(t.Context(), b.id()))
	p := a.pending(t, b.id())
	require.Len(t, p, 1)
	require.Equal(t, m.ID, p[0].ID)
	require.Empty(t, drain(events))
}
