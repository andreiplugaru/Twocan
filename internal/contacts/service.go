// Package contacts implements Twocan's contact-request protocol: sending and
// answering contact requests and keeping each contact's state in the store.
//
// Each exchange is one stream carrying one frame each way: the initiator sends
// a ContactRequest or ContactAccept, the responder always replies ContactAck,
// and only then does the initiator clear the contact's PendingSend flag.
package contacts

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"golang.org/x/time/rate"

	"twocan/internal/event"
	"twocan/internal/identity"
	"twocan/internal/p2p"
	pb "twocan/internal/proto"
	"twocan/internal/store"
	"twocan/internal/svc"
	"twocan/internal/wire"
)

// ProtocolID is the stream protocol for contact frames.
const ProtocolID = "/twocan/contact/1.0.0"

const (
	// exchangeTimeout bounds one request/ack exchange in either direction.
	exchangeTimeout = 15 * time.Second
	// maxPendingIncoming caps unanswered incoming requests; beyond it new
	// requests are acked but dropped.
	maxPendingIncoming = 100
	// maxNameRunes is the longest display name we keep from a peer.
	maxNameRunes = 64
	// maxLimiters bounds the per-peer rate limiter map; idle limiters are
	// pruned when it is exceeded.
	maxLimiters = 1024
)

// Per remote peer: a burst of 5 requests, then one every 12 s.
var (
	requestRate  = rate.Every(12 * time.Second)
	requestBurst = 5
)

var (
	// ErrSelf is returned when adding our own card.
	ErrSelf = errors.New("contacts: cannot add yourself")
	// ErrNotPending is returned by Accept when there is no incoming request.
	ErrNotPending = errors.New("contacts: no pending request from this peer")
	// errNoAck is returned by Flush when the peer replied with something other than an ack.
	errNoAck = errors.New("contacts: peer did not acknowledge")
	// errUnexpectedFrame is an inbound frame that is neither Request nor Accept.
	errUnexpectedFrame = errors.New("contacts: unexpected frame")
	// errConcurrentInsert means a row appeared between our read and insert.
	errConcurrentInsert = errors.New("contacts: contact was created concurrently")
)

// Service runs the contact protocol for one host.
type Service struct {
	h           host.Host
	st          *store.Store
	r           p2p.Reacher
	bus         *event.Bus
	displayName func() string

	// readTimeout bounds each exchange; tests shorten it.
	readTimeout time.Duration
	// beforeWrite, if set, runs between reading and writing a contact (tests).
	beforeWrite func(peer.ID)

	tasks      *svc.Tracker // async flushes and inbound handlers
	flushLocks svc.Locks[peer.ID]

	// stateMu serialises every get-modify-upsert of a contact row.
	stateMu sync.Mutex

	mu       sync.Mutex // guards limiters
	limiters map[peer.ID]*rate.Limiter
}

// New returns a Service. displayName returns our current display name, sent
// with every Request and Accept. Call Start to accept inbound streams.
func New(h host.Host, st *store.Store, r p2p.Reacher, bus *event.Bus, displayName func() string) *Service {
	return &Service{
		h:           h,
		st:          st,
		r:           r,
		bus:         bus,
		displayName: displayName,
		readTimeout: exchangeTimeout,
		tasks:       svc.NewTracker(),
		limiters:    make(map[peer.ID]*rate.Limiter),
	}
}

// Start registers the inbound stream handler.
func (s *Service) Start() {
	s.h.SetStreamHandler(ProtocolID, s.handleStream)
}

// Close removes the stream handler, cancels background flushes and waits for
// them and any in-flight inbound handlers to finish.
func (s *Service) Close() {
	s.h.RemoveStreamHandler(ProtocolID)
	s.tasks.Close()
}

// flushAsync flushes id in the background. Errors are dropped: the caller's
// reconnect loop retries later.
func (s *Service) flushAsync(id peer.ID) {
	s.tasks.Go(func(ctx context.Context) { _ = s.Flush(ctx, id) })
}

func (s *Service) changed(id peer.ID) {
	s.bus.Publish(event.ContactChanged{Peer: id})
}

// getContact returns the stored contact, or found=false if there is none.
func (s *Service) getContact(ctx context.Context, id peer.ID) (c store.Contact, found bool, err error) {
	c, err = s.st.GetContact(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Contact{PeerID: id}, false, nil
	}
	if err != nil {
		return store.Contact{}, false, err
	}
	return c, true, nil
}

// mutate applies one transition to id's contact under stateMu. decide sees the
// current row (found=false: there is none yet) and returns only the columns to
// change; an empty update writes nothing. Existing rows get a column-targeted
// update, so edits made outside the service (nickname, addrs) are never
// overwritten. It reports whether anything was written.
func (s *Service) mutate(ctx context.Context, id peer.ID,
	decide func(c store.Contact, found bool) (store.ContactUpdate, error)) (bool, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	c, found, err := s.getContact(ctx, id)
	if err != nil {
		return false, err
	}
	u, err := decide(c, found)
	if err != nil || u.IsZero() {
		return false, err
	}
	if s.beforeWrite != nil {
		s.beforeWrite(id)
	}
	if found {
		if err := s.st.UpdateContact(ctx, id, u); err != nil {
			return false, err
		}
		return true, nil
	}
	inserted, err := s.st.InsertContactIfAbsent(ctx, applyUpdate(store.Contact{PeerID: id}, u))
	if err != nil {
		return false, err
	}
	if !inserted {
		return false, errConcurrentInsert
	}
	return true, nil
}

// applyUpdate returns c with u's fields applied.
func applyUpdate(c store.Contact, u store.ContactUpdate) store.Contact {
	if u.State != nil {
		c.State = *u.State
	}
	if u.PendingSend != nil {
		c.PendingSend = *u.PendingSend
	}
	if u.DisplayName != nil {
		c.DisplayName = *u.DisplayName
	}
	if u.Addrs != nil {
		c.Addrs = u.Addrs
	}
	return c
}

func ptr[T any](v T) *T { return &v }

// Add adds the peer on card as a contact (see the package state table).
func (s *Service) Add(ctx context.Context, card identity.Card) error {
	if err := card.PeerID.Validate(); err != nil {
		return fmt.Errorf("contacts: invalid peer ID: %w", err)
	}
	if card.PeerID == s.h.ID() {
		return ErrSelf
	}
	id := card.PeerID
	if len(card.Addrs) > 0 {
		s.h.Peerstore().AddAddrs(id, card.Addrs, peerstore.AddressTTL)
	}

	flush := false
	changed, err := s.mutate(ctx, id, func(c store.Contact, found bool) (store.ContactUpdate, error) {
		var u store.ContactUpdate
		if len(card.Addrs) > 0 {
			u.Addrs = card.Addrs
		}
		switch {
		case !found || c.State == store.StateBlocked:
			u.State, u.PendingSend = ptr(store.StatePendingOutgoing), ptr(true)
			if name := sanitizeName(card.DisplayName); name != "" {
				u.DisplayName = &name
			}
			flush = true
		case c.State == store.StatePendingIncoming: // same as Accept
			u.State, u.PendingSend = ptr(store.StateAccepted), ptr(true)
			flush = true
		default: // pending_outgoing or accepted: refresh addrs only
		}
		return u, nil
	})
	if err != nil {
		return err
	}
	if changed {
		s.changed(id)
	}
	if flush {
		s.flushAsync(id)
	}
	return nil
}

// Accept accepts a pending incoming request; ErrNotPending otherwise.
func (s *Service) Accept(ctx context.Context, id peer.ID) error {
	_, err := s.mutate(ctx, id, func(c store.Contact, found bool) (store.ContactUpdate, error) {
		if !found || c.State != store.StatePendingIncoming {
			return store.ContactUpdate{}, ErrNotPending
		}
		return store.ContactUpdate{State: ptr(store.StateAccepted), PendingSend: ptr(true)}, nil
	})
	if err != nil {
		return err
	}
	s.changed(id)
	s.flushAsync(id)
	return nil
}

// Reject blocks the peer. Nothing is sent: the requester is never told.
func (s *Service) Reject(ctx context.Context, id peer.ID) error {
	_, err := s.mutate(ctx, id, func(store.Contact, bool) (store.ContactUpdate, error) {
		return store.ContactUpdate{State: ptr(store.StateBlocked), PendingSend: ptr(false)}, nil
	})
	if err != nil {
		return err
	}
	s.changed(id)
	return nil
}

// Flush sends the frame we owe id, if any: a Request when pending_outgoing,
// an Accept when accepted. On ack it clears PendingSend. Concurrent flushes
// to the same peer run one at a time.
func (s *Service) Flush(ctx context.Context, id peer.ID) error {
	defer s.flushLocks.Lock(id)()

	c, found, err := s.getContact(ctx, id)
	if err != nil || !found || !c.PendingSend {
		return err
	}
	name := s.displayName()
	var f pb.ContactFrame
	switch c.State {
	case store.StatePendingOutgoing:
		f.Body = &pb.ContactFrame_Request{Request: &pb.ContactRequest{DisplayName: name}}
	case store.StateAccepted:
		f.Body = &pb.ContactFrame_Accept{Accept: &pb.ContactAccept{DisplayName: name}}
	default:
		return nil
	}
	if err := s.exchange(ctx, id, &f); err != nil {
		return err
	}

	// Clear PendingSend only if we still owe the same frame: the state may
	// have moved on (e.g. a mutual add) while we were sending.
	cleared, err := s.mutate(ctx, id, func(cur store.Contact, found bool) (store.ContactUpdate, error) {
		if !found || !cur.PendingSend || cur.State != c.State {
			return store.ContactUpdate{}, nil
		}
		return store.ContactUpdate{PendingSend: ptr(false)}, nil
	})
	if cleared {
		s.changed(id)
	}
	return err
}

// exchange reaches id, sends f on a new stream and waits for the ack.
func (s *Service) exchange(ctx context.Context, id peer.ID, f *pb.ContactFrame) error {
	ctx, cancel := context.WithTimeout(ctx, s.readTimeout)
	defer cancel()
	if err := s.r.Reach(ctx, id); err != nil {
		return err
	}
	str, err := s.h.NewStream(network.WithAllowLimitedConn(ctx, "twocan"), id, ProtocolID)
	if err != nil {
		return err
	}
	defer svc.Guard(ctx, str)()
	if err := wire.WriteFrame(str, f); err != nil {
		str.Reset()
		return err
	}
	var reply pb.ContactFrame
	if err := wire.ReadFrame(bufio.NewReader(str), &reply); err != nil {
		str.Reset()
		return err
	}
	str.Close()
	if reply.GetAck() == nil {
		return errNoAck
	}
	return nil
}

// allowRequest applies the per-peer request rate limit.
func (s *Service) allowRequest(id peer.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.limiters[id]
	if !ok {
		if len(s.limiters) >= maxLimiters {
			s.pruneLimiters()
		}
		l = rate.NewLimiter(requestRate, requestBurst)
		s.limiters[id] = l
	}
	return l.Allow()
}

// pruneLimiters drops limiters that have refilled completely: forgetting them
// loses nothing. Called with s.mu held.
func (s *Service) pruneLimiters() {
	for id, l := range s.limiters {
		if l.Tokens() >= float64(requestBurst) {
			delete(s.limiters, id)
		}
	}
}

// handleStream answers one inbound frame. Request and Accept frames are acked
// once stored, and also when deliberately dropped (blocked, rate-limited,
// over the pending cap), so a rejected peer learns nothing. If the frame
// could not be stored the stream is reset instead: the sender keeps
// PendingSend and retries, and a reset looks like any transport failure.
func (s *Service) handleStream(str network.Stream) {
	if !s.tasks.Track() {
		str.Reset()
		return
	}
	defer s.tasks.Done()

	// A silent peer must not hold the stream open: it is reset when the
	// exchange times out or on Close.
	ctx, cancel := svc.Timeout(s.tasks.Context(), str, s.readTimeout)
	defer cancel()
	remote := str.Conn().RemotePeer()

	var f pb.ContactFrame
	if err := wire.ReadFrame(bufio.NewReader(str), &f); err != nil {
		str.Reset()
		return
	}
	var err error
	switch body := f.Body.(type) {
	case *pb.ContactFrame_Request:
		if s.allowRequest(remote) {
			err = s.onRequest(ctx, remote, sanitizeName(body.Request.GetDisplayName()))
		}
	case *pb.ContactFrame_Accept:
		err = s.onAccept(ctx, remote, sanitizeName(body.Accept.GetDisplayName()))
	default:
		err = errUnexpectedFrame
	}
	if err != nil {
		str.Reset()
		return
	}
	ack := &pb.ContactFrame{Body: &pb.ContactFrame_Ack{Ack: &pb.ContactAck{}}}
	if err := wire.WriteFrame(str, ack); err != nil {
		str.Reset()
		return
	}
	str.Close()
}

// onRequest applies a received ContactRequest. It returns an error only when
// the request could not be stored; deliberate drops (blocked, over the
// pending cap) return nil.
func (s *Service) onRequest(ctx context.Context, id peer.ID, name string) error {
	flush := false
	changed, err := s.mutate(ctx, id, func(c store.Contact, found bool) (store.ContactUpdate, error) {
		var u store.ContactUpdate
		if name != "" && name != c.DisplayName {
			u.DisplayName = &name
		}
		switch {
		case !found:
			n, err := s.st.CountContactsInState(ctx, store.StatePendingIncoming)
			if err != nil {
				return store.ContactUpdate{}, err
			}
			if n >= maxPendingIncoming {
				return store.ContactUpdate{}, nil
			}
			u.State = ptr(store.StatePendingIncoming)
		case c.State == store.StatePendingOutgoing: // mutual add
			u.State, u.PendingSend = ptr(store.StateAccepted), ptr(true)
			flush = true
		case c.State == store.StateAccepted: // they lost state; resend Accept
			u.PendingSend = ptr(true)
			flush = true
		case c.State == store.StateBlocked:
			return store.ContactUpdate{}, nil
		default: // pending_incoming: update the name only
		}
		return u, nil
	})
	if err != nil {
		return err
	}
	if changed {
		s.changed(id)
	}
	if flush {
		s.flushAsync(id)
	}
	return nil
}

// onAccept applies a received ContactAccept: only a pending outgoing request
// can be accepted. It returns an error only when the accept could not be stored.
func (s *Service) onAccept(ctx context.Context, id peer.ID, name string) error {
	changed, err := s.mutate(ctx, id, func(c store.Contact, found bool) (store.ContactUpdate, error) {
		if !found || c.State != store.StatePendingOutgoing {
			return store.ContactUpdate{}, nil
		}
		// Our Request is answered; nothing more is owed.
		u := store.ContactUpdate{State: ptr(store.StateAccepted), PendingSend: ptr(false)}
		if name != "" {
			u.DisplayName = &name
		}
		return u, nil
	})
	if err != nil {
		return err
	}
	if changed {
		s.changed(id)
	}
	return nil
}

// sanitizeName strips control characters and invalid UTF-8 from a peer's
// display name and truncates it to maxNameRunes runes.
func sanitizeName(name string) string {
	name = strings.ToValidUTF8(name, "")
	var b strings.Builder
	n := 0
	for _, r := range name {
		if n == maxNameRunes {
			break
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
