package store

import (
	"context"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type Direction string

const (
	DirIn  Direction = "in"
	DirOut Direction = "out"
)

type MessageStatus string

const (
	StatusPending   MessageStatus = "pending"
	StatusDelivered MessageStatus = "delivered"
	// StatusRead is an outgoing message the contact has read.
	StatusRead     MessageStatus = "read"
	StatusReceived MessageStatus = "received"
)

// Message is a stored chat message.
type Message struct {
	ID         []byte
	PeerID     peer.ID
	Direction  Direction
	Text       string // "" once deleted for everyone
	SentAt     time.Time
	ReceivedAt time.Time
	Status     MessageStatus
	Read       bool
	ReplyTo    []byte    // the message this one answers; nil if none
	EditedAt   time.Time // zero if never edited
	Deleted    bool      // deleted for everyone: only a tombstone is left
	// MyReaction and TheirReaction are the emoji each side reacted with;
	// "" if none.
	MyReaction, TheirReaction string
}

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func timeToMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

const msgColList = `peer_id, id, direction, text, sent_at, received_at, status, read, reply_to`
const msgValues = ` INTO messages (` + msgColList + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

func msgArgs(m Message) []any {
	return []any{m.PeerID.String(), m.ID, string(m.Direction), m.Text, m.SentAt.UnixMilli(),
		timeToMs(m.ReceivedAt), string(m.Status), b2i(m.Read), m.ReplyTo}
}

// InsertOutgoing stores a message we are sending.
func (s *Store) InsertOutgoing(ctx context.Context, m Message) error {
	_, err := s.db.ExecContext(ctx, `INSERT`+msgValues, msgArgs(m)...)
	return err
}

// InsertIncoming stores a received message, ignoring duplicates of (peer, id).
func (s *Store) InsertIncoming(ctx context.Context, m Message) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE`+msgValues, msgArgs(m)...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkDelivered marks a pending outgoing message delivered; false if there
// is no such pending row (a read receipt may have come first).
func (s *Store) MarkDelivered(ctx context.Context, id peer.ID, msgID []byte) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE messages SET status = ? WHERE peer_id = ? AND id = ? AND direction = ? AND status = ?`,
		string(StatusDelivered), id.String(), msgID, string(DirOut), string(StatusPending))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// msgSelect reads messages with both sides' reactions; callers add the
// WHERE and ORDER BY clauses, naming the messages table m.
const msgSelect = `SELECT m.peer_id, m.id, m.direction, m.text, m.sent_at, m.received_at, m.status,
m.read, m.reply_to, m.edited_at, m.deleted, COALESCE(mine.emoji, ''), COALESCE(theirs.emoji, '')
FROM messages m
LEFT JOIN reactions mine ON mine.peer_id = m.peer_id AND mine.msg_id = m.id AND mine.mine = 1
LEFT JOIN reactions theirs ON theirs.peer_id = m.peer_id AND theirs.msg_id = m.id AND theirs.mine = 0 `

func (s *Store) queryMessages(ctx context.Context, q string, args ...any) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, msgSelect+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var (
			m                  Message
			pid, dir, st       string
			sent, recv, edited int64
			read, deleted      int
		)
		if err := rows.Scan(&pid, &m.ID, &dir, &m.Text, &sent, &recv, &st, &read,
			&m.ReplyTo, &edited, &deleted, &m.MyReaction, &m.TheirReaction); err != nil {
			return nil, err
		}
		if m.PeerID, err = peer.Decode(pid); err != nil {
			return nil, err
		}
		m.Direction, m.Status, m.Read, m.Deleted = Direction(dir), MessageStatus(st), read != 0, deleted != 0
		m.SentAt, m.ReceivedAt, m.EditedAt = msToTime(sent), msToTime(recv), msToTime(edited)
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMessage returns the message msgID in the conversation with id;
// ErrNotFound if there is none.
func (s *Store) GetMessage(ctx context.Context, id peer.ID, msgID []byte) (Message, error) {
	ms, err := s.queryMessages(ctx, `WHERE m.peer_id = ? AND m.id = ?`, id.String(), msgID)
	if err != nil {
		return Message{}, err
	}
	if len(ms) == 0 {
		return Message{}, ErrNotFound
	}
	return ms[0], nil
}

// PendingOutgoing returns undelivered outgoing messages to a peer, oldest
// first. Messages deleted before they went out are never sent.
func (s *Store) PendingOutgoing(ctx context.Context, id peer.ID) ([]Message, error) {
	return s.queryMessages(ctx, `WHERE m.peer_id = ? AND m.direction = ? AND m.status = ? AND m.deleted = 0
ORDER BY m.sent_at, m.id`, id.String(), string(DirOut), string(StatusPending))
}

// History returns the full conversation with a peer, oldest first.
func (s *Store) History(ctx context.Context, id peer.ID) ([]Message, error) {
	return s.queryMessages(ctx, `WHERE m.peer_id = ? ORDER BY m.sent_at, m.id`, id.String())
}

// MarkRead marks all incoming messages from a peer as read and returns the
// IDs of those that were unread (deleted ones left out), for read receipts.
func (s *Store) MarkRead(ctx context.Context, id peer.ID) ([][]byte, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM messages WHERE peer_id = ? AND direction = ? AND read = 0 AND deleted = 0 ORDER BY sent_at, id`,
		id.String(), string(DirIn))
	if err != nil {
		return nil, err
	}
	var ids [][]byte
	for rows.Next() {
		var mid []byte
		if err := rows.Scan(&mid); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, mid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE messages SET read = 1 WHERE peer_id = ? AND direction = ? AND read = 0`, id.String(), string(DirIn)); err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}

// MarkSeen marks the given outgoing messages read by the contact and returns
// the IDs that changed.
func (s *Store) MarkSeen(ctx context.Context, id peer.ID, msgIDs [][]byte) ([][]byte, error) {
	var changed [][]byte
	for _, mid := range msgIDs {
		res, err := s.db.ExecContext(ctx,
			`UPDATE messages SET status = ? WHERE peer_id = ? AND id = ? AND direction = ? AND status IN (?, ?)`,
			string(StatusRead), id.String(), mid, string(DirOut), string(StatusPending), string(StatusDelivered))
		if err != nil {
			return changed, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed = append(changed, mid)
		}
	}
	return changed, nil
}

// EditMessage replaces the text of message msgID (of direction dir, not
// deleted) and records when it was edited; false if there is no such message.
func (s *Store) EditMessage(ctx context.Context, id peer.ID, msgID []byte, dir Direction, text string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE messages SET text = ?, edited_at = ? WHERE peer_id = ? AND id = ? AND direction = ? AND deleted = 0`,
		text, at.UnixMilli(), id.String(), msgID, string(dir))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteForEveryone leaves a tombstone of message msgID (of direction dir):
// its text and reactions are removed, the row stays. False if there was no
// such message, or it was already deleted.
func (s *Store) DeleteForEveryone(ctx context.Context, id peer.ID, msgID []byte, dir Direction) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE messages SET text = '', deleted = 1 WHERE peer_id = ? AND id = ? AND direction = ? AND deleted = 0`,
		id.String(), msgID, string(dir))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reactions WHERE peer_id = ? AND msg_id = ?`, id.String(), msgID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// DeleteForMe removes message msgID and its reactions from this device only;
// false if there was no such message.
func (s *Store) DeleteForMe(ctx context.Context, id peer.ID, msgID []byte) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE peer_id = ? AND id = ?`, id.String(), msgID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM reactions WHERE peer_id = ? AND msg_id = ?`, id.String(), msgID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// SetReaction records the reaction to message msgID by us (mine) or by the
// contact; "" removes it.
func (s *Store) SetReaction(ctx context.Context, id peer.ID, msgID []byte, mine bool, emoji string) error {
	if emoji == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM reactions WHERE peer_id = ? AND msg_id = ? AND mine = ?`,
			id.String(), msgID, b2i(mine))
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO reactions (peer_id, msg_id, mine, emoji) VALUES (?, ?, ?, ?)
ON CONFLICT (peer_id, msg_id, mine) DO UPDATE SET emoji = excluded.emoji`, id.String(), msgID, b2i(mine), emoji)
	return err
}

// UnreadCount counts unread incoming messages from a peer.
func (s *Store) UnreadCount(ctx context.Context, id peer.ID) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE peer_id = ? AND direction = ? AND read = 0 AND deleted = 0`,
		id.String(), string(DirIn)).Scan(&n)
	return n, err
}

// PeersWithPendingWork lists peers that owe a contact frame, have pending
// outgoing messages or have updates to send.
func (s *Store) PeersWithPendingWork(ctx context.Context) ([]peer.ID, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT peer_id FROM contacts WHERE pending_send = 1
UNION
SELECT peer_id FROM messages WHERE direction = ? AND status = ? AND deleted = 0
UNION
SELECT peer_id FROM updates
ORDER BY peer_id`, string(DirOut), string(StatusPending))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []peer.ID
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		id, err := peer.Decode(p)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// maxSearchResults caps what Search returns.
const maxSearchResults = 200

// Search finds messages, in every conversation, containing all the words of
// query (each also as a prefix: "mou" finds "mountain"); newest first, at
// most 200. Accents and case are ignored. Deleted messages are left out.
func (s *Store) Search(ctx context.Context, query string) ([]Message, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	return s.queryMessages(ctx, `WHERE m.rowid IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)
AND m.deleted = 0 ORDER BY m.sent_at DESC, m.id DESC LIMIT ?`, match, maxSearchResults)
}

// ftsQuery turns what the user typed into an FTS5 query: every word must
// match, as a prefix. Words are quoted, so FTS5 syntax (AND, *, ")
// typed by the user is taken literally.
func ftsQuery(q string) string {
	var terms []string
	for _, w := range strings.Fields(q) {
		w = strings.ReplaceAll(w, `"`, `""`)
		terms = append(terms, `"`+w+`"*`)
	}
	return strings.Join(terms, " ")
}
