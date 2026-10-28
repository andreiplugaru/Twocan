package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func out(id byte, text string, at int64) Message {
	return Message{ID: []byte{id}, Direction: DirOut, Text: text, SentAt: time.UnixMilli(at), Status: StatusPending}
}

func TestReplyEditAndRead(t *testing.T) {
	s := openTemp(t)
	p := newPeer(t)
	_, err := s.InsertIncoming(ctx, Message{ID: []byte{1}, PeerID: p, Direction: DirIn, Text: "hi", SentAt: time.UnixMilli(1), Status: StatusReceived})
	require.NoError(t, err)
	m := out(2, "hello", 2)
	m.PeerID, m.ReplyTo = p, []byte{1}
	require.NoError(t, s.InsertOutgoing(ctx, m))

	got, err := s.GetMessage(ctx, p, []byte{2})
	require.NoError(t, err)
	require.Equal(t, []byte{1}, got.ReplyTo)
	_, err = s.GetMessage(ctx, p, []byte{3})
	require.ErrorIs(t, err, ErrNotFound)

	at := time.UnixMilli(50)
	ok, err := s.EditMessage(ctx, p, []byte{2}, DirOut, "hello!", at)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.EditMessage(ctx, p, []byte{2}, DirIn, "nope", at)
	require.NoError(t, err)
	require.False(t, ok, "only a message of the given direction is edited")
	got, _ = s.GetMessage(ctx, p, []byte{2})
	require.Equal(t, "hello!", got.Text)
	require.Equal(t, at, got.EditedAt)

	// A read receipt beats a late delivery ack, never the other way round.
	changed, err := s.MarkSeen(ctx, p, [][]byte{{2}, {9}})
	require.NoError(t, err)
	require.Equal(t, [][]byte{{2}}, changed)
	ok, err = s.MarkDelivered(ctx, p, []byte{2})
	require.NoError(t, err)
	require.False(t, ok)
	got, _ = s.GetMessage(ctx, p, []byte{2})
	require.Equal(t, StatusRead, got.Status)
}

func TestDeleteForEveryoneAndForMe(t *testing.T) {
	s := openTemp(t)
	p := newPeer(t)
	m := out(1, "secret", 1)
	m.PeerID = p
	require.NoError(t, s.InsertOutgoing(ctx, m))
	require.NoError(t, s.SetReaction(ctx, p, []byte{1}, false, "👍"))

	ok, err := s.DeleteForEveryone(ctx, p, []byte{1}, DirOut)
	require.NoError(t, err)
	require.True(t, ok)
	got, err := s.GetMessage(ctx, p, []byte{1})
	require.NoError(t, err)
	require.True(t, got.Deleted)
	require.Empty(t, got.Text)
	require.Empty(t, got.TheirReaction, "reactions go with the message")
	ok, err = s.DeleteForEveryone(ctx, p, []byte{1}, DirOut)
	require.NoError(t, err)
	require.False(t, ok, "already deleted")

	pend, err := s.PendingOutgoing(ctx, p)
	require.NoError(t, err)
	require.Empty(t, pend, "a message deleted before it went out is never sent")
	peers, err := s.PeersWithPendingWork(ctx)
	require.NoError(t, err)
	require.NotContains(t, peers, p)
	ok, err = s.EditMessage(ctx, p, []byte{1}, DirOut, "x", time.Now())
	require.NoError(t, err)
	require.False(t, ok, "a deleted message can't be edited")

	ok, err = s.DeleteForMe(ctx, p, []byte{1})
	require.NoError(t, err)
	require.True(t, ok)
	_, err = s.GetMessage(ctx, p, []byte{1})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestReactions(t *testing.T) {
	s := openTemp(t)
	p := newPeer(t)
	_, err := s.InsertIncoming(ctx, Message{ID: []byte{1}, PeerID: p, Direction: DirIn, Text: "hi", SentAt: time.UnixMilli(1), Status: StatusReceived})
	require.NoError(t, err)
	require.NoError(t, s.SetReaction(ctx, p, []byte{1}, true, "👍"))
	require.NoError(t, s.SetReaction(ctx, p, []byte{1}, true, "❤️")) // replaces
	require.NoError(t, s.SetReaction(ctx, p, []byte{1}, false, "😂"))
	h, err := s.History(ctx, p)
	require.NoError(t, err)
	require.Equal(t, "❤️", h[0].MyReaction)
	require.Equal(t, "😂", h[0].TheirReaction)
	require.NoError(t, s.SetReaction(ctx, p, []byte{1}, true, ""))
	h, _ = s.History(ctx, p)
	require.Empty(t, h[0].MyReaction)
	require.Equal(t, "😂", h[0].TheirReaction)
}

func TestUpdatesQueue(t *testing.T) {
	s := openTemp(t)
	p := newPeer(t)
	require.NoError(t, s.QueueUpdate(ctx, p, Update{ID: []byte{2}, Frame: []byte("b")}))
	require.NoError(t, s.QueueUpdate(ctx, p, Update{ID: []byte{1}, Frame: []byte("a")}))
	peers, err := s.PeersWithPendingWork(ctx)
	require.NoError(t, err)
	require.Contains(t, peers, p)
	us, err := s.PendingUpdates(ctx, p)
	require.NoError(t, err)
	require.Equal(t, []Update{{ID: []byte{1}, Frame: []byte("a")}, {ID: []byte{2}, Frame: []byte("b")}}, us)
	require.NoError(t, s.RemoveUpdate(ctx, p, []byte{1}))
	us, _ = s.PendingUpdates(ctx, p)
	require.Len(t, us, 1)
}

func TestSearch(t *testing.T) {
	s := openTemp(t)
	ana, ben := newPeer(t), newPeer(t)
	add := func(p peer.ID, id byte, text string, at int64) {
		_, err := s.InsertIncoming(ctx, Message{ID: []byte{id}, PeerID: p, Direction: DirIn, Text: text, SentAt: time.UnixMilli(at), Status: StatusReceived})
		require.NoError(t, err)
	}
	add(ana, 1, "Mâine mergem la munte", 1)
	add(ben, 2, "The mountain was cold", 2)
	add(ana, 3, `weird "quotes" AND stars*`, 3)

	ids := func(q string) []byte {
		ms, err := s.Search(ctx, q)
		require.NoError(t, err)
		var out []byte
		for _, m := range ms {
			out = append(out, m.ID[0])
		}
		return out
	}
	require.Equal(t, []byte{1}, ids("maine"), "accents are ignored")
	require.Equal(t, []byte{2}, ids("moun"), "prefixes match")
	require.Equal(t, []byte{2, 1}, ids("m"), "newest first, across conversations")
	require.Equal(t, []byte{2}, ids("mountain COLD"), "every word must match")
	require.Equal(t, []byte{3}, ids(`"quotes" AND`), "FTS syntax is taken literally")
	require.Empty(t, ids("   "))

	ok, err := s.EditMessage(ctx, ben, []byte{2}, DirIn, "The hill was warm", time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, ids("moun"), "the index follows edits")
	require.Equal(t, []byte{2}, ids("hill"))
	_, err = s.DeleteForEveryone(ctx, ana, []byte{1}, DirIn)
	require.NoError(t, err)
	require.Empty(t, ids("munte"), "deleted messages are not found")
}

// Upgrading a v1 database keeps its messages and indexes them for search.
func TestMigrationIndexesExistingMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	require.NoError(t, err)
	p := newPeer(t)
	_, err = s.InsertIncoming(ctx, Message{ID: []byte{1}, PeerID: p, Direction: DirIn, Text: "old message", SentAt: time.UnixMilli(1), Status: StatusReceived})
	require.NoError(t, err)
	// Pretend the message predates the search index.
	_, err = s.db.Exec(`INSERT INTO messages_fts (messages_fts) VALUES ('delete-all')`)
	require.NoError(t, err)
	_, err = s.db.Exec(`DELETE FROM schema_version WHERE version = 2`)
	require.NoError(t, err)
	_, err = s.db.Exec(`DROP TRIGGER messages_fts_insert; DROP TRIGGER messages_fts_delete; DROP TRIGGER messages_fts_update;
DROP TABLE messages_fts; DROP TABLE reactions; DROP TABLE updates;
ALTER TABLE messages DROP COLUMN reply_to; ALTER TABLE messages DROP COLUMN edited_at; ALTER TABLE messages DROP COLUMN deleted`)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	ms, err := s.Search(ctx, "old")
	require.NoError(t, err)
	require.Len(t, ms, 1)
}
