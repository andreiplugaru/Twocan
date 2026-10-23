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

	ctx    context.Context // service lifetime; cancelled by Close
	cancel context.CancelFunc

	// stateMu serialises every get-modify-upsert of a contact row.
	stateMu sync.Mutex

	mu         sync.Mutex // guards the fields below
	closed     bool
	wg         sync.WaitGroup // async flushes and inbound handlers
	flushLocks map[peer.ID]*sync.Mutex
	limiters   map[peer.ID]*rate.Limiter
}

// New returns a Service. displayName returns our current display name, sent
// with every Request and Accept. Call Start to accept inbound streams.
func New(h host.Host, st *store.Store, r p2p.Reacher, bus *event.Bus, displayName func() string) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		h:           h,
		st:          st,
		r:           r,
		bus:         bus,
		displayName: displayName,
		readTimeout: exchangeTimeout,
		ctx:         ctx,
		cancel:      cancel,
		flushLocks:  make(map[peer.ID]*sync.Mutex),
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
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// track registers one background task; it returns false once closed.
func (s *Service) track() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

// flushAsync flushes id in the background. Errors are dropped: the caller's
// reconnect loop retries later.
func (s *Service) flushAsync(id peer.ID) {
	if !s.track() {
		return
	}
	go func() {
		defer s.wg.Done()
		_ = s.Flush(s.ctx, id)
	}()
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

	s.stateMu.Lock()
	c, found, err := s.getContact(ctx, id)
	if err != nil {
		s.stateMu.Unlock()
		return err
	}
	if len(card.Addrs) > 0 {
		c.Addrs = card.Addrs
	}
	flush := false
	switch {
	case !found || c.State == store.StateBlocked:
		c.State = store.StatePendingOutgoing
		c.PendingSend = true
		if name := sanitizeName(card.DisplayName); name != "" {
			c.DisplayName = name
		}
		flush = true
	case c.State == store.StatePendingIncoming:
		c.State = store.StateAccepted
		c.PendingSend = true
		flush = true
	default: // pending_outgoing or accepted: refresh addrs only
	}
	err = s.st.UpsertContact(ctx, c)
	s.stateMu.Unlock()
	if err != nil {
		return err
	}
	s.changed(id)
	if flush {
		s.flushAsync(id)
	}
	return nil
}

// Accept accepts a pending incoming request; ErrNotPending otherwise.
func (s *Service) Accept(ctx context.Context, id peer.ID) error {
	s.stateMu.Lock()
	c, found, err := s.getContact(ctx, id)
	if err == nil && (!found || c.State != store.StatePendingIncoming) {
		err = ErrNotPending
	}
	if err == nil {
		c.State = store.StateAccepted
		c.PendingSend = true
		err = s.st.UpsertContact(ctx, c)
	}
	s.stateMu.Unlock()
	if err != nil {
		return err
	}
	s.changed(id)
	s.flushAsync(id)
	return nil
}

// Reject blocks the peer. Nothing is sent: the requester is never told.
func (s *Service) Reject(ctx context.Context, id peer.ID) error {
	s.stateMu.Lock()
	c, _, err := s.getContact(ctx, id)
	if err == nil {
		c.State = store.StateBlocked
		c.PendingSend = false
		err = s.st.UpsertContact(ctx, c)
	}
	s.stateMu.Unlock()
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
	l := s.flushLock(id)
	l.Lock()
	defer l.Unlock()

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
	s.stateMu.Lock()
	cur, found, err := s.getContact(ctx, id)
	cleared := false
	if err == nil && found && cur.PendingSend && cur.State == c.State {
		cur.PendingSend = false
		err = s.st.UpsertContact(ctx, cur)
		cleared = err == nil
	}
	s.stateMu.Unlock()
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
	// The deadline is not enough on its own: some transports (mocknet among
	// them) ignore it. Resetting on ctx expiry also unblocks us on Close.
	if dl, ok := ctx.Deadline(); ok {
		_ = str.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { str.Reset() })
	defer stop()
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

func (s *Service) flushLock(id peer.ID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.flushLocks[id]
	if !ok {
		l = new(sync.Mutex)
		s.flushLocks[id] = l
	}
	return l
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

// handleStream answers one inbound frame. Request and Accept frames are
// always acked once read, even when dropped, so a rejected or rate-limited
// peer learns nothing.
func (s *Service) handleStream(str network.Stream) {
	if !s.track() {
		str.Reset()
		return
	}
	defer s.wg.Done()

	// A silent peer must not hold the stream open: reset it when the exchange
	// times out (deadlines alone are ignored by some transports) or on Close.
	_ = str.SetDeadline(time.Now().Add(s.readTimeout))
	ctx, cancel := context.WithTimeout(s.ctx, s.readTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { str.Reset() })
	defer stop()
	remote := str.Conn().RemotePeer()

	var f pb.ContactFrame
	if err := wire.ReadFrame(bufio.NewReader(str), &f); err != nil {
		str.Reset()
		return
	}
	switch body := f.Body.(type) {
	case *pb.ContactFrame_Request:
		if s.allowRequest(remote) {
			s.onRequest(ctx, remote, sanitizeName(body.Request.GetDisplayName()))
		}
	case *pb.ContactFrame_Accept:
		s.onAccept(ctx, remote, sanitizeName(body.Accept.GetDisplayName()))
	default:
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

// onRequest applies a received ContactRequest. Store errors are dropped: the
// peer still gets its ack, as with any other dropped request.
func (s *Service) onRequest(ctx context.Context, id peer.ID, name string) {
	s.stateMu.Lock()
	c, found, err := s.getContact(ctx, id)
	if err != nil {
		s.stateMu.Unlock()
		return
	}
	flush := false
	switch {
	case !found:
		n, err := s.st.CountContactsInState(ctx, store.StatePendingIncoming)
		if err != nil || n >= maxPendingIncoming {
			s.stateMu.Unlock()
			return
		}
		c.State = store.StatePendingIncoming
	case c.State == store.StatePendingOutgoing: // mutual add
		c.State = store.StateAccepted
		c.PendingSend = true
		flush = true
	case c.State == store.StateAccepted: // they lost state; resend Accept
		c.PendingSend = true
		flush = true
	case c.State == store.StatePendingIncoming && name != "" && c.DisplayName != name:
	default: // blocked, or pending_incoming with an unchanged name
		s.stateMu.Unlock()
		return
	}
	if name != "" {
		c.DisplayName = name
	}
	err = s.st.UpsertContact(ctx, c)
	s.stateMu.Unlock()
	if err != nil {
		return
	}
	s.changed(id)
	if flush {
		s.flushAsync(id)
	}
}

// onAccept applies a received ContactAccept: only a pending outgoing request
// can be accepted.
func (s *Service) onAccept(ctx context.Context, id peer.ID, name string) {
	s.stateMu.Lock()
	c, found, err := s.getContact(ctx, id)
	if err != nil || !found || c.State != store.StatePendingOutgoing {
		s.stateMu.Unlock()
		return
	}
	c.State = store.StateAccepted
	c.PendingSend = false // our Request is answered; nothing more is owed
	if name != "" {
		c.DisplayName = name
	}
	err = s.st.UpsertContact(ctx, c)
	s.stateMu.Unlock()
	if err == nil {
		s.changed(id)
	}
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
