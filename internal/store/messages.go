package store

import (
	"context"
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
	StatusReceived  MessageStatus = "received"
)

// Message is a stored chat message.
type Message struct {
	ID         []byte
	PeerID     peer.ID
	Direction  Direction
	Text       string
	SentAt     time.Time
	ReceivedAt time.Time
	Status     MessageStatus
	Read       bool
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

const msgColList = `peer_id, id, direction, text, sent_at, received_at, status, read`
const msgValues = ` INTO messages (` + msgColList + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

func msgArgs(m Message) []any {
	return []any{m.PeerID.String(), m.ID, string(m.Direction), m.Text, m.SentAt.UnixMilli(),
		timeToMs(m.ReceivedAt), string(m.Status), b2i(m.Read)}
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

// MarkDelivered marks an outgoing message delivered; false if no such outgoing row.
func (s *Store) MarkDelivered(ctx context.Context, id peer.ID, msgID []byte) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE messages SET status = ? WHERE peer_id = ? AND id = ? AND direction = ?`,
		string(StatusDelivered), id.String(), msgID, string(DirOut))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) queryMessages(ctx context.Context, q string, args ...any) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var (
			m            Message
			pid, dir, st string
			sent, recv   int64
			read         int
		)
		if err := rows.Scan(&pid, &m.ID, &dir, &m.Text, &sent, &recv, &st, &read); err != nil {
			return nil, err
		}
		if m.PeerID, err = peer.Decode(pid); err != nil {
			return nil, err
		}
		m.Direction, m.Status, m.Read = Direction(dir), MessageStatus(st), read != 0
		m.SentAt, m.ReceivedAt = msToTime(sent), msToTime(recv)
		out = append(out, m)
	}
	return out, rows.Err()
}

// PendingOutgoing returns undelivered outgoing messages to a peer, oldest first.
func (s *Store) PendingOutgoing(ctx context.Context, id peer.ID) ([]Message, error) {
	return s.queryMessages(ctx, `SELECT `+msgColList+` FROM messages
WHERE peer_id = ? AND direction = ? AND status = ? ORDER BY sent_at, id`,
		id.String(), string(DirOut), string(StatusPending))
}

// History returns the full conversation with a peer, oldest first.
func (s *Store) History(ctx context.Context, id peer.ID) ([]Message, error) {
	return s.queryMessages(ctx, `SELECT `+msgColList+` FROM messages WHERE peer_id = ? ORDER BY sent_at, id`, id.String())
}

// MarkRead marks all incoming messages from a peer as read.
func (s *Store) MarkRead(ctx context.Context, id peer.ID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE messages SET read = 1 WHERE peer_id = ? AND direction = ? AND read = 0`, id.String(), string(DirIn))
	return err
}

// UnreadCount counts unread incoming messages from a peer.
func (s *Store) UnreadCount(ctx context.Context, id peer.ID) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE peer_id = ? AND direction = ? AND read = 0`,
		id.String(), string(DirIn)).Scan(&n)
	return n, err
}

// PeersWithPendingWork lists peers that owe a contact frame or have pending outgoing messages.
func (s *Store) PeersWithPendingWork(ctx context.Context) ([]peer.ID, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT peer_id FROM contacts WHERE pending_send = 1
UNION
SELECT peer_id FROM messages WHERE direction = ? AND status = ?
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
