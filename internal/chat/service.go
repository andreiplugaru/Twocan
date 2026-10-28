// Package chat implements Twocan's message protocols: an outbox of pending
// messages per peer, delivered in order over one stream and acknowledged one
// by one, with receiver-side deduplication; and, in updates.go, changes to
// messages already sent (edits, deletions, reactions, read receipts) and
// typing indicators.
//
// The sender writes a ChatMessage and waits for the matching Ack before
// marking the message delivered and sending the next. The receiver acks only
// after the message is stored (or was already stored), so a message is never
// marked delivered unless the peer has it; anything else resets the stream
// and the sender retries later. Duplicates caused by retries are dropped by
// the store's (peer, id) key.
package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"twocan/internal/event"
	"twocan/internal/logging"
	"twocan/internal/p2p"
	pb "twocan/internal/proto"
	"twocan/internal/store"
	"twocan/internal/svc"
	"twocan/internal/wire"
)

// ProtocolID is the stream protocol for chat frames.
const ProtocolID = "/twocan/msg/1.0.0"

// MaxTextBytes is the longest message text in bytes.
const MaxTextBytes = 16 << 10

const (
	// frameTimeout bounds reaching a peer, each message/ack round trip on
	// the sender, and each idle wait for the next frame on the receiver.
	frameTimeout = 15 * time.Second
	// idLen is the length of a message ID (a UUIDv7).
	idLen = 16
)

var (
	// ErrNotContact is returned when the peer is not an accepted contact.
	ErrNotContact = errors.New("chat: not an accepted contact")
	// ErrEmpty is returned for empty or whitespace-only text.
	ErrEmpty = errors.New("chat: message is empty")
	// ErrTooLong is returned for text longer than MaxTextBytes.
	ErrTooLong = errors.New("chat: message is too long")
	// ErrInvalidUTF8 is returned for text that is not valid UTF-8.
	ErrInvalidUTF8 = errors.New("chat: message is not valid UTF-8")
	// errBadAck is returned by Flush when the reply is not an Ack for the message sent.
	errBadAck = errors.New("chat: peer did not acknowledge the message")
	// errBadFrame is an inbound frame that is not a valid ChatMessage.
	errBadFrame = errors.New("chat: invalid frame")
	// ErrNoSuchMessage is returned for a message that is not in the conversation.
	ErrNoSuchMessage = errors.New("chat: no such message")
	// ErrNotYours is returned for editing or deleting for everyone a message the contact sent.
	ErrNotYours = errors.New("chat: only your own messages can be changed")
	// ErrBadReaction is returned for a reaction that is not a short emoji.
	ErrBadReaction = errors.New("chat: a reaction must be a single emoji")
)

// Service runs the chat protocol for one host.
type Service struct {
	h   host.Host
	st  *store.Store
	r   p2p.Reacher
	bus *event.Bus

	// readTimeout bounds each round trip / idle wait (see frameTimeout).
	readTimeout time.Duration
	// afterAck, if set, is consulted by the inbound handler before it handles
	// a stream's next frame, with the number of acks sent on that stream so
	// far; returning true resets the stream (tests).
	afterAck func(n int) bool

	tasks      *svc.Tracker // async flushes and inbound handlers
	flushLocks svc.Locks[peer.ID]

	typingMu   sync.Mutex
	lastTyping map[peer.ID]time.Time // when we last told each peer we are typing

	log *slog.Logger // never given message text
}

// New returns a Service. Call Start to accept inbound streams.
func New(h host.Host, st *store.Store, r p2p.Reacher, bus *event.Bus) *Service {
	return &Service{
		h:           h,
		st:          st,
		r:           r,
		bus:         bus,
		readTimeout: frameTimeout,
		tasks:       svc.NewTracker(),
		lastTyping:  make(map[peer.ID]time.Time),
		log:         logging.Discard(),
	}
}

// SetLogger sets where the service logs; call it before Start. Message text
// is never logged, only IDs and counts.
func (s *Service) SetLogger(l *slog.Logger) { s.log = l.With("component", "chat") }

// msgID renders a message ID for logs: the same string on both devices.
func msgID(id []byte) string {
	u, err := uuid.FromBytes(id)
	if err != nil {
		return fmt.Sprintf("%x", id)
	}
	return u.String()
}

// Start registers the inbound stream handlers.
func (s *Service) Start() {
	s.h.SetStreamHandler(ProtocolID, s.handleStream)
	s.h.SetStreamHandler(UpdateProtocolID, s.handleUpdates)
	s.h.SetStreamHandler(TypingProtocolID, s.handleTyping)
}

// Close removes the stream handler, cancels background flushes and waits for
// them and any in-flight inbound handlers to finish.
func (s *Service) Close() {
	s.h.RemoveStreamHandler(ProtocolID)
	s.h.RemoveStreamHandler(UpdateProtocolID)
	s.h.RemoveStreamHandler(TypingProtocolID)
	s.tasks.Close()
}

// flushAsync flushes id in the background. Errors are dropped: the caller's
// reconnect loop retries later.
func (s *Service) flushAsync(id peer.ID) {
	s.tasks.Go(func(ctx context.Context) { _ = s.Flush(ctx, id) })
}

// checkText validates message text, for sending and receiving alike.
func checkText(text string) error {
	switch {
	case len(text) > MaxTextBytes:
		return ErrTooLong
	case !utf8.ValidString(text):
		return ErrInvalidUTF8
	case strings.TrimSpace(text) == "":
		return ErrEmpty
	}
	return nil
}

// requireAccepted returns ErrNotContact unless id is an accepted contact.
func (s *Service) requireAccepted(ctx context.Context, id peer.ID) error {
	c, err := s.st.GetContact(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotContact
	}
	if err != nil {
		return err
	}
	if c.State != store.StateAccepted {
		return ErrNotContact
	}
	return nil
}

// Send stores text as a pending message to an accepted contact, starts a
// background flush and returns the stored message. replyTo, if not nil, is
// the message in this conversation it answers (ErrNoSuchMessage if there is
// none). Delivery is reported later with a MessageDelivered event.
func (s *Service) Send(ctx context.Context, to peer.ID, text string, replyTo []byte) (store.Message, error) {
	if err := checkText(text); err != nil {
		return store.Message{}, err
	}
	if err := s.requireAccepted(ctx, to); err != nil {
		return store.Message{}, err
	}
	if replyTo != nil {
		if _, err := s.st.GetMessage(ctx, to, replyTo); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.Message{}, ErrNoSuchMessage
			}
			return store.Message{}, err
		}
	}
	id := uuid.Must(uuid.NewV7())
	m := store.Message{
		ID:        id[:],
		PeerID:    to,
		Direction: store.DirOut,
		Text:      text,
		// The ID's own millisecond timestamp: UUIDv7s are monotonic within
		// the process, so the outbox order (sent_at, id) is the send order.
		SentAt:  time.UnixMilli(v7Millis(id)),
		Status:  store.StatusPending,
		ReplyTo: replyTo,
	}
	if err := s.st.InsertOutgoing(ctx, m); err != nil {
		s.log.Warn("could not queue message", "peer", to, "err", err)
		return store.Message{}, err
	}
	s.log.Info("message queued", "peer", to, "id", msgID(m.ID))
	s.flushAsync(to)
	return m, nil
}

// v7Millis returns the Unix millisecond timestamp in a UUIDv7's first 48 bits.
func v7Millis(id uuid.UUID) int64 {
	var b [8]byte
	copy(b[2:], id[:6])
	return int64(binary.BigEndian.Uint64(b[:]))
}

// Flush delivers id's pending messages and then its pending updates (see
// flushUpdates). Concurrent flushes to the same peer run one at a time.
func (s *Service) Flush(ctx context.Context, id peer.ID) error {
	defer s.flushLocks.Lock(id)()
	if err := s.flushMessages(ctx, id); err != nil {
		return err
	}
	return s.flushUpdates(ctx, id)
}

// flushMessages delivers id's pending messages, oldest first, over one
// stream. Each message is marked delivered (and MessageDelivered published)
// as soon as its Ack arrives. It stops at the first error and returns it;
// what was not acked stays pending. The caller holds id's flush lock.
func (s *Service) flushMessages(ctx context.Context, id peer.ID) error {
	pending, err := s.st.PendingOutgoing(ctx, id)
	if err != nil || len(pending) == 0 {
		return err
	}
	// Never deliver to a peer that is no longer an accepted contact (e.g.
	// blocked after the messages were queued).
	if err := s.requireAccepted(ctx, id); err != nil {
		s.log.Info("not delivering: peer is not an accepted contact", "peer", id, "pending", len(pending), "err", err)
		return err
	}
	str, err := s.open(ctx, id)
	if err != nil {
		s.log.Info("delivery postponed: could not open chat stream", "peer", id, "pending", len(pending), "err", err)
		return err
	}
	s.log.Info("delivering messages", "peer", id, "pending", len(pending))
	r := bufio.NewReader(str)
	for i, m := range pending {
		if err := s.deliver(ctx, str, r, m); err != nil {
			s.log.Info("delivery interrupted", "peer", id, "delivered", i, "still_pending", len(pending)-i, "id", msgID(m.ID), "err", err)
			str.Reset()
			return err
		}
		// Only Flush changes an outgoing row's status, and it holds the
		// per-peer lock, so every row in pending is still pending here: a
		// successful update is a real pending -> delivered transition.
		ok, err := s.st.MarkDelivered(ctx, id, m.ID)
		if err != nil {
			s.log.Warn("could not mark message delivered", "peer", id, "id", msgID(m.ID), "err", err)
			str.Reset()
			return err
		}
		if ok {
			s.log.Debug("message delivered", "peer", id, "id", msgID(m.ID))
			s.bus.Publish(event.MessageDelivered{Peer: id, ID: m.ID})
		}
	}
	// Everything was acked; a failing close changes nothing.
	_ = str.Close()
	s.log.Info("messages delivered", "peer", id, "count", len(pending))
	return nil
}

// open reaches id and opens a chat stream to it.
func (s *Service) open(ctx context.Context, id peer.ID) (network.Stream, error) {
	return s.openProto(ctx, id, ProtocolID)
}

// openProto reaches id and opens a stream of protocol proto to it.
func (s *Service) openProto(ctx context.Context, id peer.ID, proto protocol.ID) (network.Stream, error) {
	ctx, cancel := context.WithTimeout(ctx, s.readTimeout)
	defer cancel()
	if err := s.r.Reach(ctx, id); err != nil {
		return nil, err
	}
	return s.h.NewStream(network.WithAllowLimitedConn(ctx, "twocan"), id, proto)
}

// deliver writes m on str and waits for its Ack, within readTimeout.
func (s *Service) deliver(ctx context.Context, str network.Stream, r *bufio.Reader, m store.Message) error {
	_, cancel := svc.Timeout(ctx, str, s.readTimeout)
	defer cancel()

	f := &pb.ChatFrame{Body: &pb.ChatFrame_Message{Message: &pb.ChatMessage{
		Id: m.ID, SentAt: m.SentAt.UnixMilli(), Text: m.Text, ReplyTo: m.ReplyTo,
	}}}
	if err := wire.WriteFrame(str, f); err != nil {
		return err
	}
	var reply pb.ChatFrame
	if err := wire.ReadFrame(r, &reply); err != nil {
		return err
	}
	if ack := reply.GetAck(); ack == nil || !bytes.Equal(ack.GetId(), m.ID) {
		return errBadAck
	}
	return nil
}

// handleStream receives messages from an accepted contact until the peer
// closes the stream. Each valid message is stored and then acked; a
// duplicate is acked without being stored again. A non-contact, an invalid
// frame, a store failure or an idle peer resets the stream: the sender keeps
// the message pending and retries, and a reset looks like any transport
// failure.
func (s *Service) handleStream(str network.Stream) {
	if !s.tasks.Track() {
		str.Reset()
		return
	}
	defer s.tasks.Done()
	remote := str.Conn().RemotePeer()

	// Each step gets its own timeout, so a silent peer can't hold the stream.
	ctx, cancel := svc.Timeout(s.tasks.Context(), str, s.readTimeout)
	err := s.requireAccepted(ctx, remote)
	cancel()
	if err != nil {
		s.log.Info("chat stream rejected: sender is not an accepted contact", "peer", remote, "err", err)
		str.Reset()
		return
	}
	r := bufio.NewReader(str)
	for acked := 0; ; acked++ {
		ctx, cancel := svc.Timeout(s.tasks.Context(), str, s.readTimeout)
		err := s.receive(ctx, str, r, remote, acked)
		cancel()
		if errors.Is(err, io.EOF) {
			s.log.Debug("chat stream closed by peer", "peer", remote, "acked", acked)
			str.Close()
			return
		}
		if err != nil {
			s.log.Info("chat stream reset", "peer", remote, "acked", acked, "err", err)
			str.Reset()
			return
		}
	}
}

// receive reads, stores and acks one ChatMessage frame. It returns io.EOF
// when the peer has closed the stream cleanly.
func (s *Service) receive(ctx context.Context, str network.Stream, r *bufio.Reader, remote peer.ID, acked int) error {
	var f pb.ChatFrame
	if err := wire.ReadFrame(r, &f); err != nil {
		return err
	}
	if s.afterAck != nil && s.afterAck(acked) {
		return errBadFrame
	}
	cm := f.GetMessage()
	if problem := frameProblem(cm); problem != "" {
		return fmt.Errorf("%w: %s", errBadFrame, problem)
	}
	m := store.Message{
		ID:         cm.GetId(),
		PeerID:     remote,
		Direction:  store.DirIn,
		Text:       cm.GetText(),
		SentAt:     time.UnixMilli(cm.GetSentAt()),
		ReceivedAt: time.UnixMilli(time.Now().UnixMilli()),
		Status:     store.StatusReceived,
	}
	// A reply to something malformed is kept as a plain message.
	if r := cm.GetReplyTo(); len(r) == idLen {
		m.ReplyTo = r
	}
	inserted, err := s.st.InsertIncoming(ctx, m)
	if err != nil {
		return fmt.Errorf("store incoming message: %w", err)
	}
	if inserted {
		s.log.Info("message received", "peer", remote, "id", msgID(m.ID))
		s.bus.Publish(event.MessageReceived{Msg: m})
	} else {
		s.log.Info("duplicate message acked again", "peer", remote, "id", msgID(m.ID))
	}
	ack := &pb.ChatFrame{Body: &pb.ChatFrame_Ack{Ack: &pb.Ack{Id: m.ID}}}
	return wire.WriteFrame(str, ack)
}

// frameProblem says what is wrong with an inbound message ("" if nothing).
func frameProblem(cm *pb.ChatMessage) string {
	switch {
	case cm == nil:
		return "not a message"
	case len(cm.GetId()) != idLen:
		return "bad message id length"
	case cm.GetSentAt() <= 0:
		return "missing send time"
	}
	if err := checkText(cm.GetText()); err != nil {
		return err.Error()
	}
	return ""
}
