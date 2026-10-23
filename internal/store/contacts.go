package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// ContactState is a contact's relationship state.
type ContactState string

const (
	StatePendingOutgoing ContactState = "pending_outgoing"
	StatePendingIncoming ContactState = "pending_incoming"
	StateAccepted        ContactState = "accepted"
	StateBlocked         ContactState = "blocked"
)

// Contact is a known peer.
type Contact struct {
	PeerID      peer.ID
	DisplayName string
	Nickname    string
	State       ContactState
	// PendingSend means we owe this peer a contact frame: a ContactRequest when
	// pending_outgoing, a ContactAccept when accepted.
	PendingSend bool
	Addrs       []ma.Multiaddr
	CreatedAt   time.Time
}

// Name returns the nickname, else the display name, else a short peer ID.
func (c Contact) Name() string {
	if c.Nickname != "" {
		return c.Nickname
	}
	if c.DisplayName != "" {
		return c.DisplayName
	}
	s := c.PeerID.String()
	if len(s) > 6 {
		s = s[len(s)-6:]
	}
	return s
}

func encodeAddrs(addrs []ma.Multiaddr) (string, error) {
	strs := make([]string, 0, len(addrs))
	for _, a := range addrs {
		strs = append(strs, a.String())
	}
	b, err := json.Marshal(strs)
	return string(b), err
}

func decodeAddrs(s string) ([]ma.Multiaddr, error) {
	var strs []string
	if err := json.Unmarshal([]byte(s), &strs); err != nil {
		return nil, err
	}
	var out []ma.Multiaddr
	for _, str := range strs {
		a, err := ma.NewMultiaddr(str)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpsertContact inserts or updates a contact (created_at is kept on update).
func (s *Store) UpsertContact(ctx context.Context, c Contact) error {
	addrs, err := encodeAddrs(c.Addrs)
	if err != nil {
		return err
	}
	created := c.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO contacts (peer_id, display_name, nickname, state, pending_send, addrs, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(peer_id) DO UPDATE SET
  display_name = excluded.display_name,
  nickname = excluded.nickname,
  state = excluded.state,
  pending_send = excluded.pending_send,
  addrs = excluded.addrs`,
		c.PeerID.String(), c.DisplayName, c.Nickname, string(c.State), b2i(c.PendingSend), addrs, created.UnixMilli())
	return err
}

// InsertContactIfAbsent inserts c unless a row for c.PeerID already exists, in
// which case nothing is written and it returns false.
func (s *Store) InsertContactIfAbsent(ctx context.Context, c Contact) (bool, error) {
	addrs, err := encodeAddrs(c.Addrs)
	if err != nil {
		return false, err
	}
	created := c.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO contacts (peer_id, display_name, nickname, state, pending_send, addrs, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(peer_id) DO NOTHING`,
		c.PeerID.String(), c.DisplayName, c.Nickname, string(c.State), b2i(c.PendingSend), addrs, created.UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ContactUpdate names the contact columns to change. Nil fields (and nil
// Addrs) are left as they are, so concurrent writes to other columns, such as
// a nickname edit, are never overwritten.
type ContactUpdate struct {
	State       *ContactState
	PendingSend *bool
	DisplayName *string
	Addrs       []ma.Multiaddr
}

// IsZero reports whether u changes nothing.
func (u ContactUpdate) IsZero() bool {
	return u.State == nil && u.PendingSend == nil && u.DisplayName == nil && u.Addrs == nil
}

// UpdateContact writes only the columns set in u; ErrNotFound if the contact
// is unknown. An empty update does nothing.
func (s *Store) UpdateContact(ctx context.Context, id peer.ID, u ContactUpdate) error {
	if u.IsZero() {
		return nil
	}
	var (
		sets []string
		args []any
	)
	if u.State != nil {
		sets, args = append(sets, "state = ?"), append(args, string(*u.State))
	}
	if u.PendingSend != nil {
		sets, args = append(sets, "pending_send = ?"), append(args, b2i(*u.PendingSend))
	}
	if u.DisplayName != nil {
		sets, args = append(sets, "display_name = ?"), append(args, *u.DisplayName)
	}
	if u.Addrs != nil {
		enc, err := encodeAddrs(u.Addrs)
		if err != nil {
			return err
		}
		sets, args = append(sets, "addrs = ?"), append(args, enc)
	}
	args = append(args, id.String())
	return s.mustUpdate(ctx, `UPDATE contacts SET `+strings.Join(sets, ", ")+` WHERE peer_id = ?`, args...)
}

type rowScanner interface{ Scan(...any) error }

const contactCols = `peer_id, display_name, nickname, state, pending_send, addrs, created_at`

func scanContact(r rowScanner) (Contact, error) {
	var (
		c         Contact
		pid, st   string
		ps        int
		addrs     string
		createdMs int64
	)
	if err := r.Scan(&pid, &c.DisplayName, &c.Nickname, &st, &ps, &addrs, &createdMs); err != nil {
		return Contact{}, err
	}
	id, err := peer.Decode(pid)
	if err != nil {
		return Contact{}, fmt.Errorf("store: bad peer id %q: %w", pid, err)
	}
	c.PeerID = id
	c.State = ContactState(st)
	c.PendingSend = ps != 0
	c.CreatedAt = time.UnixMilli(createdMs)
	if c.Addrs, err = decodeAddrs(addrs); err != nil {
		return Contact{}, fmt.Errorf("store: bad addrs: %w", err)
	}
	return c, nil
}

// GetContact returns the contact or ErrNotFound.
func (s *Store) GetContact(ctx context.Context, id peer.ID) (Contact, error) {
	c, err := scanContact(s.db.QueryRowContext(ctx,
		`SELECT `+contactCols+` FROM contacts WHERE peer_id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return Contact{}, ErrNotFound
	}
	return c, err
}

// ListContacts returns all contacts: pending_incoming first, then by Name.
func (s *Store) ListContacts(ctx context.Context) ([]Contact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+contactCols+` FROM contacts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].State == StatePendingIncoming, out[j].State == StatePendingIncoming
		if pi != pj {
			return pi
		}
		ni, nj := strings.ToLower(out[i].Name()), strings.ToLower(out[j].Name())
		if ni != nj {
			return ni < nj
		}
		return out[i].PeerID.String() < out[j].PeerID.String()
	})
	return out, nil
}

// SetNickname sets the local nickname; ErrNotFound if the contact is unknown.
func (s *Store) SetNickname(ctx context.Context, id peer.ID, nick string) error {
	return s.mustUpdate(ctx, `UPDATE contacts SET nickname = ? WHERE peer_id = ?`, nick, id.String())
}

// SetAddrs replaces the contact's stored addresses.
func (s *Store) SetAddrs(ctx context.Context, id peer.ID, addrs []ma.Multiaddr) error {
	enc, err := encodeAddrs(addrs)
	if err != nil {
		return err
	}
	return s.mustUpdate(ctx, `UPDATE contacts SET addrs = ? WHERE peer_id = ?`, enc, id.String())
}

func (s *Store) mustUpdate(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountContactsInState counts contacts in the given state.
func (s *Store) CountContactsInState(ctx context.Context, st ContactState) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE state = ?`, string(st)).Scan(&n)
	return n, err
}
