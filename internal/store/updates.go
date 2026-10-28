package store

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Update is a change still to be sent to a contact: an edit, a deletion, a
// reaction or a read receipt, as a serialized pb.Update.
type Update struct {
	ID    []byte // a UUIDv7: updates are sent in ID order
	Frame []byte
}

// QueueUpdate stores an update to send to id.
func (s *Store) QueueUpdate(ctx context.Context, id peer.ID, u Update) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO updates (peer_id, id, frame) VALUES (?, ?, ?)`,
		id.String(), u.ID, u.Frame)
	return err
}

// PendingUpdates returns the updates still to send to id, oldest first.
func (s *Store) PendingUpdates(ctx context.Context, id peer.ID) ([]Update, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, frame FROM updates WHERE peer_id = ? ORDER BY id`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	for rows.Next() {
		var u Update
		if err := rows.Scan(&u.ID, &u.Frame); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// RemoveUpdate forgets an update the contact has acknowledged.
func (s *Store) RemoveUpdate(ctx context.Context, id peer.ID, updateID []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM updates WHERE peer_id = ? AND id = ?`, id.String(), updateID)
	return err
}
