package contacts

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/require"

	"twocan/internal/event"
	"twocan/internal/identity"
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
	h   host.Host
	st  *store.Store
	svc *Service
}

func (p *testPeer) id() peer.ID { return p.h.ID() }

func (p *testPeer) card(name string) identity.Card {
	return identity.Card{PeerID: p.h.ID(), DisplayName: name, Addrs: p.h.Addrs()}
}

func (p *testPeer) contact(t *testing.T, id peer.ID) (store.Contact, bool) {
	t.Helper()
	c, err := p.st.GetContact(t.Context(), id)
	if err == store.ErrNotFound {
		return store.Contact{}, false
	}
	require.NoError(t, err)
	return c, true
}

func (p *testPeer) waitContact(t *testing.T, id peer.ID, cond func(store.Contact) bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		c, ok := p.contact(t, id)
		return ok && cond(c)
	}, waitFor, tick)
}

func newNet(t *testing.T) mocknet.Mocknet {
	t.Helper()
	mn := mocknet.New()
	t.Cleanup(func() { mn.Close() })
	return mn
}

// newPeer creates a mocknet host with its own store and a started Service.
// Call mn.LinkAll() after creating all peers.
func newPeer(t *testing.T, mn mocknet.Mocknet, name string) *testPeer {
	t.Helper()
	h, err := mn.GenPeer()
	require.NoError(t, err)
	st, err := store.Open(filepath.Join(t.TempDir(), "twocan.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	svc := New(h, st, mnReacher{mn: mn, self: h.ID()}, event.NewBus(), func() string { return name })
	svc.Start()
	t.Cleanup(svc.Close) // runs before st.Close (cleanup is LIFO)
	return &testPeer{h: h, st: st, svc: svc}
}

func pair(t *testing.T) (mocknet.Mocknet, *testPeer, *testPeer) {
	mn := newNet(t)
	a := newPeer(t, mn, "Alice")
	b := newPeer(t, mn, "Bob")
	require.NoError(t, mn.LinkAll())
	return mn, a, b
}

func goOffline(t *testing.T, mn mocknet.Mocknet, a, b *testPeer) {
	t.Helper()
	require.NoError(t, mn.UnlinkPeers(a.id(), b.id()))
	require.NoError(t, mn.DisconnectPeers(a.id(), b.id()))
}

func isState(st store.ContactState) func(store.Contact) bool {
	return func(c store.Contact) bool { return c.State == st }
}

func settled(st store.ContactState) func(store.Contact) bool {
	return func(c store.Contact) bool { return c.State == st && !c.PendingSend }
}

// sendRaw opens a stream from h to id, writes f and returns the reply frame.
func sendRaw(t *testing.T, mn mocknet.Mocknet, h host.Host, id peer.ID, f *pb.ContactFrame) *pb.ContactFrame {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, mnReacher{mn: mn, self: h.ID()}.Reach(ctx, id))
	s, err := h.NewStream(ctx, id, ProtocolID)
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, wire.WriteFrame(s, f))
	var reply pb.ContactFrame
	require.NoError(t, wire.ReadFrame(bufio.NewReader(s), &reply))
	return &reply
}

func requestFrame(name string) *pb.ContactFrame {
	return &pb.ContactFrame{Body: &pb.ContactFrame_Request{Request: &pb.ContactRequest{DisplayName: name}}}
}

func TestAddAcceptHappyPath(t *testing.T) {
	_, a, b := pair(t)
	events, cancel := b.svc.bus.Subscribe()
	defer cancel()

	require.NoError(t, a.svc.Add(t.Context(), b.card("Bob")))
	c, ok := a.contact(t, b.id())
	require.True(t, ok)
	require.Equal(t, store.StatePendingOutgoing, c.State)
	require.Equal(t, "Bob", c.DisplayName)
	require.NotEmpty(t, c.Addrs)

	b.waitContact(t, a.id(), func(c store.Contact) bool {
		return c.State == store.StatePendingIncoming && c.DisplayName == "Alice" && !c.PendingSend
	})
	a.waitContact(t, b.id(), settled(store.StatePendingOutgoing))

	select {
	case e := <-events:
		require.Equal(t, event.ContactChanged{Peer: a.id()}, e)
	case <-time.After(waitFor):
		t.Fatal("no ContactChanged on B")
	}

	require.NoError(t, b.svc.Accept(t.Context(), a.id()))
	a.waitContact(t, b.id(), settled(store.StateAccepted))
	b.waitContact(t, a.id(), settled(store.StateAccepted))
	ca, _ := a.contact(t, b.id())
	require.Equal(t, "Bob", ca.DisplayName)
}

func TestAddKeepsNickname(t *testing.T) {
	_, a, b := pair(t)
	require.NoError(t, a.svc.Add(t.Context(), b.card("Bob")))
	b.waitContact(t, a.id(), isState(store.StatePendingIncoming))
	require.NoError(t, b.st.SetNickname(t.Context(), a.id(), "Ally"))

	require.NoError(t, b.svc.Accept(t.Context(), a.id()))
	a.waitContact(t, b.id(), settled(store.StateAccepted))
	b.waitContact(t, a.id(), settled(store.StateAccepted))
	c, _ := b.contact(t, a.id())
	require.Equal(t, "Ally", c.Nickname)
}

func TestMutualAdd(t *testing.T) {
	_, a, b := pair(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = a.svc.Add(t.Context(), b.card("Bob")) }()
	go func() { defer wg.Done(); errs[1] = b.svc.Add(t.Context(), a.card("Alice")) }()
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	a.waitContact(t, b.id(), settled(store.StateAccepted))
	b.waitContact(t, a.id(), settled(store.StateAccepted))
}

func TestRequestQueuedWhileOffline(t *testing.T) {
	mn, a, b := pair(t)
	goOffline(t, mn, a, b)

	require.NoError(t, a.svc.Add(t.Context(), b.card("Bob")))
	require.Error(t, a.svc.Flush(t.Context(), b.id()))
	c, ok := a.contact(t, b.id())
	require.True(t, ok)
	require.Equal(t, store.StatePendingOutgoing, c.State)
	require.True(t, c.PendingSend)
	_, ok = b.contact(t, a.id())
	require.False(t, ok)

	_, err := mn.LinkPeers(a.id(), b.id())
	require.NoError(t, err)
	require.NoError(t, a.svc.Flush(t.Context(), b.id()))
	b.waitContact(t, a.id(), isState(store.StatePendingIncoming))
	a.waitContact(t, b.id(), settled(store.StatePendingOutgoing))
}

func TestAcceptQueuedWhileRequesterOffline(t *testing.T) {
	mn, a, b := pair(t)
	require.NoError(t, a.svc.Add(t.Context(), b.card("Bob")))
	b.waitContact(t, a.id(), isState(store.StatePendingIncoming))
	a.waitContact(t, b.id(), settled(store.StatePendingOutgoing))
	goOffline(t, mn, a, b)

	require.NoError(t, b.svc.Accept(t.Context(), a.id()))
	c, _ := b.contact(t, a.id())
	require.Equal(t, store.StateAccepted, c.State)
	require.True(t, c.PendingSend)
	require.Error(t, b.svc.Flush(t.Context(), a.id()))
	c, _ = b.contact(t, a.id())
	require.True(t, c.PendingSend)

	_, err := mn.LinkPeers(a.id(), b.id())
	require.NoError(t, err)
	require.NoError(t, b.svc.Flush(t.Context(), a.id()))
	a.waitContact(t, b.id(), settled(store.StateAccepted))
	b.waitContact(t, a.id(), settled(store.StateAccepted))
}

func TestRejectIsSilent(t *testing.T) {
	_, a, b := pair(t)
	require.NoError(t, a.svc.Add(t.Context(), b.card("Bob")))
	b.waitContact(t, a.id(), isState(store.StatePendingIncoming))
	a.waitContact(t, b.id(), settled(store.StatePendingOutgoing))

	require.NoError(t, b.svc.Reject(t.Context(), a.id()))
	b.waitContact(t, a.id(), settled(store.StateBlocked))

	// A re-requests: mark the Request as owed again and flush it.
	c, _ := a.contact(t, b.id())
	c.PendingSend = true
	require.NoError(t, a.st.UpsertContact(t.Context(), c))
	require.NoError(t, a.svc.Flush(t.Context(), b.id()))

	a.waitContact(t, b.id(), settled(store.StatePendingOutgoing))
	cb, _ := b.contact(t, a.id())
	require.Equal(t, store.StateBlocked, cb.State)
	require.False(t, cb.PendingSend)
}

func TestAddOwnCard(t *testing.T) {
	_, a, _ := pair(t)
	err := a.svc.Add(t.Context(), a.card("Me"))
	require.ErrorIs(t, err, ErrSelf)
	_, ok := a.contact(t, a.id())
	require.False(t, ok)
}

func TestRequestRateLimit(t *testing.T) {
	mn, a, b := pair(t)
	// b sends raw frames (bypassing its Service) with distinct names.
	for i := range 20 {
		reply := sendRaw(t, mn, b.h, a.id(), requestFrame(fmt.Sprintf("n%d", i)))
		require.NotNil(t, reply.GetAck(), "frame %d not acked", i)
	}
	contacts, err := a.st.ListContacts(t.Context())
	require.NoError(t, err)
	require.Len(t, contacts, 1)
	require.Equal(t, store.StatePendingIncoming, contacts[0].State)
	// Burst of 5: frames 0..4 are processed, the rest are acked but dropped.
	require.Equal(t, "n4", contacts[0].DisplayName)
}

func TestAcceptNotPending(t *testing.T) {
	_, a, b := pair(t)
	require.ErrorIs(t, a.svc.Accept(t.Context(), b.id()), ErrNotPending)

	require.NoError(t, a.st.UpsertContact(t.Context(), store.Contact{PeerID: b.id(), State: store.StateAccepted}))
	require.ErrorIs(t, a.svc.Accept(t.Context(), b.id()), ErrNotPending)
}

func TestPendingIncomingCap(t *testing.T) {
	mn, a, b := pair(t)
	for range maxPendingIncoming {
		priv, _, err := crypto.GenerateEd25519Key(nil)
		require.NoError(t, err)
		id, err := peer.IDFromPrivateKey(priv)
		require.NoError(t, err)
		require.NoError(t, a.st.UpsertContact(t.Context(), store.Contact{PeerID: id, State: store.StatePendingIncoming}))
	}
	reply := sendRaw(t, mn, b.h, a.id(), requestFrame("Bob"))
	require.NotNil(t, reply.GetAck())
	_, ok := a.contact(t, b.id())
	require.False(t, ok)
}

func TestReceivedDisplayNameSanitized(t *testing.T) {
	mn, a, b := pair(t)
	long := "evil\x1b[31m\n" + strings.Repeat("é", 100)
	reply := sendRaw(t, mn, b.h, a.id(), requestFrame(long))
	require.NotNil(t, reply.GetAck())
	c, ok := a.contact(t, b.id())
	require.True(t, ok)
	require.Equal(t, 64, utf8.RuneCountInString(c.DisplayName))
	require.True(t, strings.HasPrefix(c.DisplayName, "evil[31m"))
	require.NotContains(t, c.DisplayName, "\x1b")
	require.NotContains(t, c.DisplayName, "\n")
}

func TestSilentPeerTimesOut(t *testing.T) {
	mn, a, b := pair(t)
	a.svc.readTimeout = 100 * time.Millisecond
	require.NoError(t, mnReacher{mn: mn, self: b.id()}.Reach(t.Context(), a.id()))
	s, err := b.h.NewStream(t.Context(), a.id(), ProtocolID)
	require.NoError(t, err)
	defer s.Close()
	// Send nothing; A must give up and drop the stream.
	require.NoError(t, s.SetReadDeadline(time.Now().Add(waitFor)))
	start := time.Now()
	_, err = s.Read(make([]byte, 1))
	require.Error(t, err)
	require.Less(t, time.Since(start), waitFor/2, "A held the silent stream open")
}

// A responder that cannot store a frame must not ack it, so the sender keeps
// PendingSend and retries later.
func TestResponderStoreFailureIsNotAcked(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		_, a, b := pair(t)
		require.NoError(t, b.st.Close())
		require.NoError(t, a.svc.Add(t.Context(), b.card("Bob")))
		require.Error(t, a.svc.Flush(t.Context(), b.id()))
		c, _ := a.contact(t, b.id())
		require.Equal(t, store.StatePendingOutgoing, c.State)
		require.True(t, c.PendingSend)
	})
	t.Run("accept", func(t *testing.T) {
		_, a, b := pair(t)
		require.NoError(t, a.svc.Add(t.Context(), b.card("Bob")))
		b.waitContact(t, a.id(), isState(store.StatePendingIncoming))
		a.waitContact(t, b.id(), settled(store.StatePendingOutgoing))
		require.NoError(t, a.st.Close())
		require.NoError(t, b.svc.Accept(t.Context(), a.id()))
		require.Error(t, b.svc.Flush(t.Context(), a.id()))
		c, _ := b.contact(t, a.id())
		require.Equal(t, store.StateAccepted, c.State)
		require.True(t, c.PendingSend)
	})
}

// A nickname set while the service is between reading and writing a contact
// must survive the service's write.
func TestConcurrentNicknameEditSurvives(t *testing.T) {
	mn, a, b := pair(t)
	require.NoError(t, a.svc.Add(t.Context(), b.card("Bob")))
	b.waitContact(t, a.id(), isState(store.StatePendingIncoming))
	a.waitContact(t, b.id(), settled(store.StatePendingOutgoing))

	var nick string
	b.svc.beforeWrite = func(id peer.ID) {
		if id != a.id() || nick == "" {
			return
		}
		if err := b.st.SetNickname(context.Background(), id, nick); err != nil {
			t.Errorf("SetNickname: %v", err)
		}
	}

	// Inbound path: a renamed Request updates display_name only.
	nick = "Ally"
	reply := sendRaw(t, mn, a.h, b.id(), requestFrame("Alice 2"))
	require.NotNil(t, reply.GetAck())
	c, _ := b.contact(t, a.id())
	require.Equal(t, "Alice 2", c.DisplayName)
	require.Equal(t, "Ally", c.Nickname)

	// Local path: Accept changes state and pending_send only.
	nick = "Al"
	require.NoError(t, b.svc.Accept(t.Context(), a.id()))
	c, _ = b.contact(t, a.id())
	require.Equal(t, store.StateAccepted, c.State)
	require.Equal(t, "Al", c.Nickname)
}
