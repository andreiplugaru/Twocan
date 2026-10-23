package store

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

func newPeer(t *testing.T) peer.ID {
	t.Helper()
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPublicKey(pub)
	require.NoError(t, err)
	return id
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenTwiceRunsMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&n))
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestContactUpsertGetList(t *testing.T) {
	s := openTemp(t)
	zed, ann := newPeer(t), newPeer(t)
	addr := ma.StringCast("/ip4/1.2.3.4/tcp/4001")
	require.NoError(t, s.UpsertContact(ctx, Contact{PeerID: ann, DisplayName: "Ann", State: StateAccepted, Addrs: []ma.Multiaddr{addr}, CreatedAt: time.Now()}))
	require.NoError(t, s.UpsertContact(ctx, Contact{PeerID: zed, DisplayName: "Zed", State: StatePendingIncoming, PendingSend: true}))
	list, err := s.ListContacts(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, zed, list[0].PeerID)
	require.True(t, list[0].PendingSend)
	require.Equal(t, ann, list[1].PeerID)
	require.Len(t, list[1].Addrs, 1)
	require.True(t, addr.Equal(list[1].Addrs[0]))

	require.NoError(t, s.SetNickname(ctx, ann, "Annie"))
	addr2 := ma.StringCast("/ip4/5.6.7.8/udp/4001/quic-v1")
	require.NoError(t, s.SetAddrs(ctx, ann, []ma.Multiaddr{addr2}))
	c, err := s.GetContact(ctx, ann)
	require.NoError(t, err)
	require.Equal(t, "Annie", c.Name())
	require.True(t, addr2.Equal(c.Addrs[0]))
	n, err := s.CountContactsInState(ctx, StateAccepted)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	_, err = s.GetContact(ctx, newPeer(t))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestContactName(t *testing.T) {
	p := newPeer(t)
	require.Equal(t, "nick", Contact{PeerID: p, DisplayName: "d", Nickname: "nick"}.Name())
	require.Equal(t, "d", Contact{PeerID: p, DisplayName: "d"}.Name())
	require.NotEmpty(t, Contact{PeerID: p}.Name())
}

func TestInsertIncomingDedupes(t *testing.T) {
	s := openTemp(t)
	p1, p2 := newPeer(t), newPeer(t)
	m := Message{ID: []byte{1}, PeerID: p1, Direction: DirIn, Text: "hi", SentAt: time.UnixMilli(10), ReceivedAt: time.UnixMilli(11), Status: StatusReceived}
	ok, err := s.InsertIncoming(ctx, m)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.InsertIncoming(ctx, m)
	require.NoError(t, err)
	require.False(t, ok)
	h, err := s.History(ctx, p1)
	require.NoError(t, err)
	require.Len(t, h, 1)
	require.Equal(t, "hi", h[0].Text)
	m.PeerID = p2
	ok, err = s.InsertIncoming(ctx, m)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestOutgoingLifecycle(t *testing.T) {
	s := openTemp(t)
	p := newPeer(t)
	m1 := Message{ID: []byte{1}, PeerID: p, Direction: DirOut, Text: "a", SentAt: time.UnixMilli(2), Status: StatusPending}
	m2 := Message{ID: []byte{2}, PeerID: p, Direction: DirOut, Text: "b", SentAt: time.UnixMilli(1), Status: StatusPending}
	require.NoError(t, s.InsertOutgoing(ctx, m1))
	require.NoError(t, s.InsertOutgoing(ctx, m2))
	pend, err := s.PendingOutgoing(ctx, p)
	require.NoError(t, err)
	require.Len(t, pend, 2)
	require.Equal(t, m2.ID, pend[0].ID)
	require.Equal(t, m1.ID, pend[1].ID)
	peers, err := s.PeersWithPendingWork(ctx)
	require.NoError(t, err)
	require.Contains(t, peers, p)

	ok, err := s.MarkDelivered(ctx, p, m1.ID)
	require.NoError(t, err)
	require.True(t, ok)
	h, err := s.History(ctx, p)
	require.NoError(t, err)
	require.Equal(t, StatusDelivered, h[1].Status)
	pend, err = s.PendingOutgoing(ctx, p)
	require.NoError(t, err)
	require.Len(t, pend, 1)
	require.Equal(t, m2.ID, pend[0].ID)

	_, err = s.InsertIncoming(ctx, Message{ID: []byte{9}, PeerID: p, Direction: DirIn, Text: "x", SentAt: time.UnixMilli(5), Status: StatusReceived})
	require.NoError(t, err)
	ok, err = s.MarkDelivered(ctx, p, []byte{9})
	require.NoError(t, err)
	require.False(t, ok)
	h, err = s.History(ctx, p)
	require.NoError(t, err)
	for _, m := range h {
		if m.Direction == DirIn {
			require.Equal(t, StatusReceived, m.Status)
		}
	}
}

func TestPeersWithPendingWorkIncludesPendingSend(t *testing.T) {
	s := openTemp(t)
	p := newPeer(t)
	require.NoError(t, s.UpsertContact(ctx, Contact{PeerID: p, State: StatePendingOutgoing, PendingSend: true}))
	peers, err := s.PeersWithPendingWork(ctx)
	require.NoError(t, err)
	require.Equal(t, []peer.ID{p}, peers)
}

func TestUnreadAndMarkRead(t *testing.T) {
	s := openTemp(t)
	p := newPeer(t)
	for i := byte(1); i <= 2; i++ {
		_, err := s.InsertIncoming(ctx, Message{ID: []byte{i}, PeerID: p, Direction: DirIn, Text: "x", SentAt: time.UnixMilli(int64(i)), Status: StatusReceived})
		require.NoError(t, err)
	}
	require.NoError(t, s.InsertOutgoing(ctx, Message{ID: []byte{7}, PeerID: p, Direction: DirOut, Text: "o", SentAt: time.UnixMilli(3), Status: StatusPending}))
	n, err := s.UnreadCount(ctx, p)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.NoError(t, s.MarkRead(ctx, p))
	n, err = s.UnreadCount(ctx, p)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestSettings(t *testing.T) {
	s := openTemp(t)
	_, ok, err := s.GetSetting(ctx, SettingDisplayName)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, s.SetSetting(ctx, SettingDisplayName, "A"))
	v, ok, err := s.GetSetting(ctx, SettingDisplayName)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "A", v)
	require.NoError(t, s.SetSetting(ctx, SettingDisplayName, "B"))
	v, _, _ = s.GetSetting(ctx, SettingDisplayName)
	require.Equal(t, "B", v)
}
