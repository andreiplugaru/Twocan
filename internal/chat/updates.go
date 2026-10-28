package chat

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"

	"twocan/internal/event"
	pb "twocan/internal/proto"
	"twocan/internal/store"
	"twocan/internal/svc"
	"twocan/internal/wire"
)

// Changes to messages already sent (edits, deletions for everyone,
// reactions and read receipts) are updates. Each is applied locally at
// once, then queued in the store and delivered like messages: in order,
// over a stream of their own (UpdateProtocolID), each acked once the
// contact has applied it. A separate protocol and queue mean an older
// Twocan that knows no updates never holds up messages. Applying an update
// twice changes nothing, so a retry after a lost ack is harmless.
//
// Typing indicators are not queued: one frame on a stream of its own
// (TypingProtocolID), only over a connection that is already open.

// UpdateProtocolID is the stream protocol for update frames.
const UpdateProtocolID = "/twocan/update/1.0.0"

// TypingProtocolID is the stream protocol for typing indicators.
const TypingProtocolID = "/twocan/typing/1.0.0"

const (
	// TypingFor is how long a typing indicator holds on the receiving side.
	TypingFor = 5 * time.Second
	// typingEvery is how often we tell a contact we are still typing.
	typingEvery = 3 * time.Second
	// maxReadIDs caps the message IDs in one read receipt.
	maxReadIDs = 500
	// maxEmojiBytes caps a reaction ("👨‍👩‍👧‍👦" is 25 bytes).
	maxEmojiBytes = 32
)

// Edit replaces the text of one of our messages to id, on both sides.
func (s *Service) Edit(ctx context.Context, id peer.ID, msgID []byte, text string) error {
	if err := checkText(text); err != nil {
		return err
	}
	if err := s.requireOwn(ctx, id, msgID); err != nil {
		return err
	}
	at := time.Now()
	if _, err := s.st.EditMessage(ctx, id, msgID, store.DirOut, text, at); err != nil {
		return err
	}
	// If it is still pending, the new text goes out with it anyway; the
	// edit then tells the contact nothing new, which is harmless.
	return s.queue(ctx, id, &pb.Update{Change: &pb.Update_Edit{Edit: &pb.Edit{
		MsgId: msgID, Text: text, EditedAt: at.UnixMilli(),
	}}})
}

// Delete deletes message msgID. For everyone (our own messages only), a
// tombstone stays on both sides, and a message not yet sent never is; for
// me, it disappears from this device only.
func (s *Service) Delete(ctx context.Context, id peer.ID, msgID []byte, forEveryone bool) error {
	if !forEveryone {
		ok, err := s.st.DeleteForMe(ctx, id, msgID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoSuchMessage
		}
		s.bus.Publish(event.MessagesUpdated{Peer: id, IDs: [][]byte{msgID}})
		return nil
	}
	if err := s.requireOwn(ctx, id, msgID); err != nil {
		return err
	}
	if _, err := s.st.DeleteForEveryone(ctx, id, msgID, store.DirOut); err != nil {
		return err
	}
	return s.queue(ctx, id, &pb.Update{Change: &pb.Update_Delete{Delete: &pb.Delete{MsgId: msgID}}})
}

// React sets our reaction to message msgID; "" removes it.
func (s *Service) React(ctx context.Context, id peer.ID, msgID []byte, emoji string) error {
	if !validEmoji(emoji) {
		return ErrBadReaction
	}
	m, err := s.getMessage(ctx, id, msgID)
	if err != nil {
		return err
	}
	if m.Deleted {
		return ErrNoSuchMessage
	}
	if err := s.requireAccepted(ctx, id); err != nil {
		return err
	}
	if err := s.st.SetReaction(ctx, id, msgID, true, emoji); err != nil {
		return err
	}
	return s.queue(ctx, id, &pb.Update{Change: &pb.Update_Reaction{Reaction: &pb.Reaction{MsgId: msgID, Emoji: emoji}}})
}

// MarkRead marks the conversation with id read and tells the contact which
// of their messages we have now read.
func (s *Service) MarkRead(ctx context.Context, id peer.ID) error {
	ids, err := s.st.MarkRead(ctx, id)
	if err != nil || len(ids) == 0 {
		return err
	}
	if s.requireAccepted(ctx, id) != nil {
		return nil // no receipts to someone who is not (or no longer) a contact
	}
	for len(ids) > 0 {
		n := min(len(ids), maxReadIDs)
		if err := s.queueOnly(ctx, id, &pb.Update{Change: &pb.Update_Read{Read: &pb.Read{MsgIds: ids[:n]}}}); err != nil {
			return err
		}
		ids = ids[n:]
	}
	s.flushAsync(id)
	return nil
}

// Typing tells id we are typing, at most every few seconds and only over a
// connection that is already open: it never dials, and is never queued.
func (s *Service) Typing(id peer.ID) {
	s.typingMu.Lock()
	if time.Since(s.lastTyping[id]) < typingEvery {
		s.typingMu.Unlock()
		return
	}
	s.lastTyping[id] = time.Now()
	s.typingMu.Unlock()
	if s.h.Network().Connectedness(id) == network.NotConnected {
		return
	}
	s.tasks.Go(func(ctx context.Context) {
		ctx = network.WithNoDial(network.WithAllowLimitedConn(ctx, "twocan-typing"), "twocan-typing")
		ctx, cancel := context.WithTimeout(ctx, s.readTimeout)
		defer cancel()
		str, err := s.h.NewStream(ctx, id, TypingProtocolID)
		if err != nil {
			return // an older Twocan, or the connection just closed
		}
		defer svc.Guard(ctx, str)()
		if err := wire.WriteFrame(str, &pb.Typing{}); err != nil {
			str.Reset()
			return
		}
		_ = str.Close()
	})
}

// requireOwn checks that msgID is one of our messages to id, not deleted.
func (s *Service) requireOwn(ctx context.Context, id peer.ID, msgID []byte) error {
	m, err := s.getMessage(ctx, id, msgID)
	if err != nil {
		return err
	}
	if m.Direction != store.DirOut {
		return ErrNotYours
	}
	if m.Deleted {
		return ErrNoSuchMessage
	}
	return s.requireAccepted(ctx, id)
}

func (s *Service) getMessage(ctx context.Context, id peer.ID, msgID []byte) (store.Message, error) {
	m, err := s.st.GetMessage(ctx, id, msgID)
	if errors.Is(err, store.ErrNotFound) {
		return m, ErrNoSuchMessage
	}
	return m, err
}

// validEmoji accepts "" (no reaction) or a short run of non-space symbols.
func validEmoji(e string) bool {
	if e == "" {
		return true
	}
	if len(e) > maxEmojiBytes || !utf8.ValidString(e) {
		return false
	}
	for _, r := range e {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// queue stores u for id, publishes the local change and starts a flush.
func (s *Service) queue(ctx context.Context, id peer.ID, u *pb.Update) error {
	if err := s.queueOnly(ctx, id, u); err != nil {
		return err
	}
	s.bus.Publish(event.MessagesUpdated{Peer: id, IDs: updatedIDs(u)})
	s.flushAsync(id)
	return nil
}

// queueOnly gives u an ID and stores it to send to id.
func (s *Service) queueOnly(ctx context.Context, id peer.ID, u *pb.Update) error {
	uid := uuid.Must(uuid.NewV7())
	u.Id = uid[:]
	b, err := proto.Marshal(u)
	if err != nil {
		return err
	}
	if err := s.st.QueueUpdate(ctx, id, store.Update{ID: u.Id, Frame: b}); err != nil {
		s.log.Warn("could not queue update", "peer", id, "err", err)
		return err
	}
	s.log.Info("update queued", "peer", id, "kind", updateKind(u), "id", msgID(u.Id))
	return nil
}

// flushUpdates delivers id's pending updates, oldest first, over one
// stream, removing each once acked. The caller holds id's flush lock.
func (s *Service) flushUpdates(ctx context.Context, id peer.ID) error {
	pending, err := s.st.PendingUpdates(ctx, id)
	if err != nil || len(pending) == 0 {
		return err
	}
	if err := s.requireAccepted(ctx, id); err != nil {
		return err
	}
	str, err := s.openProto(ctx, id, UpdateProtocolID)
	if err != nil {
		s.log.Info("updates postponed: could not open update stream", "peer", id, "pending", len(pending), "err", err)
		return err
	}
	r := bufio.NewReader(str)
	for i, u := range pending {
		if err := s.deliverUpdate(ctx, str, r, u); err != nil {
			s.log.Info("updates interrupted", "peer", id, "sent", i, "still_pending", len(pending)-i, "err", err)
			str.Reset()
			return err
		}
		if err := s.st.RemoveUpdate(ctx, id, u.ID); err != nil {
			str.Reset()
			return err
		}
	}
	_ = str.Close()
	s.log.Info("updates sent", "peer", id, "count", len(pending))
	return nil
}

// deliverUpdate writes u on str and waits for its Ack, within readTimeout.
func (s *Service) deliverUpdate(ctx context.Context, str network.Stream, r *bufio.Reader, u store.Update) error {
	_, cancel := svc.Timeout(ctx, str, s.readTimeout)
	defer cancel()
	var up pb.Update
	if err := proto.Unmarshal(u.Frame, &up); err != nil {
		return fmt.Errorf("stored update: %w", err)
	}
	if err := wire.WriteFrame(str, &pb.UpdateFrame{Body: &pb.UpdateFrame_Update{Update: &up}}); err != nil {
		return err
	}
	var reply pb.UpdateFrame
	if err := wire.ReadFrame(r, &reply); err != nil {
		return err
	}
	if ack := reply.GetAck(); ack == nil || !bytes.Equal(ack.GetId(), u.ID) {
		return errBadAck
	}
	return nil
}

// handleUpdates applies updates from an accepted contact until the peer
// closes the stream, acking each once applied. Like handleStream, anything
// wrong resets the stream and the sender retries.
func (s *Service) handleUpdates(str network.Stream) {
	if !s.tasks.Track() {
		str.Reset()
		return
	}
	defer s.tasks.Done()
	remote := str.Conn().RemotePeer()
	ctx, cancel := svc.Timeout(s.tasks.Context(), str, s.readTimeout)
	err := s.requireAccepted(ctx, remote)
	cancel()
	if err != nil {
		s.log.Info("update stream rejected: sender is not an accepted contact", "peer", remote, "err", err)
		str.Reset()
		return
	}
	r := bufio.NewReader(str)
	for {
		ctx, cancel := svc.Timeout(s.tasks.Context(), str, s.readTimeout)
		err := s.receiveUpdate(ctx, str, r, remote)
		cancel()
		if errors.Is(err, io.EOF) {
			str.Close()
			return
		}
		if err != nil {
			s.log.Info("update stream reset", "peer", remote, "err", err)
			str.Reset()
			return
		}
	}
}

// receiveUpdate reads, applies and acks one update.
func (s *Service) receiveUpdate(ctx context.Context, str network.Stream, r *bufio.Reader, remote peer.ID) error {
	var f pb.UpdateFrame
	if err := wire.ReadFrame(r, &f); err != nil {
		return err
	}
	u := f.GetUpdate()
	if u == nil || len(u.GetId()) != idLen {
		return fmt.Errorf("%w: not an update", errBadFrame)
	}
	changed, err := s.apply(ctx, remote, u)
	if err != nil {
		return err
	}
	s.log.Info("update applied", "peer", remote, "kind", updateKind(u), "id", msgID(u.Id), "changed", len(changed))
	if len(changed) > 0 {
		s.bus.Publish(event.MessagesUpdated{Peer: remote, IDs: changed})
	}
	return wire.WriteFrame(str, &pb.UpdateFrame{Body: &pb.UpdateFrame_Ack{Ack: &pb.Ack{Id: u.Id}}})
}

// apply makes the change u from remote and returns the IDs of the messages
// that changed. A change to a message we do not have changes nothing (we
// may have deleted it) but is still acked.
func (s *Service) apply(ctx context.Context, remote peer.ID, u *pb.Update) ([][]byte, error) {
	one := func(id []byte, ok bool, err error) ([][]byte, error) {
		if err != nil || !ok {
			return nil, err
		}
		return [][]byte{id}, nil
	}
	switch c := u.GetChange().(type) {
	case *pb.Update_Edit:
		e := c.Edit
		if len(e.GetMsgId()) != idLen || e.GetEditedAt() <= 0 {
			return nil, fmt.Errorf("%w: bad edit", errBadFrame)
		}
		if err := checkText(e.GetText()); err != nil {
			return nil, fmt.Errorf("%w: %v", errBadFrame, err)
		}
		ok, err := s.st.EditMessage(ctx, remote, e.MsgId, store.DirIn, e.Text, time.UnixMilli(e.EditedAt))
		return one(e.MsgId, ok, err)
	case *pb.Update_Delete:
		id := c.Delete.GetMsgId()
		if len(id) != idLen {
			return nil, fmt.Errorf("%w: bad delete", errBadFrame)
		}
		ok, err := s.st.DeleteForEveryone(ctx, remote, id, store.DirIn)
		return one(id, ok, err)
	case *pb.Update_Reaction:
		re := c.Reaction
		if len(re.GetMsgId()) != idLen || !validEmoji(re.GetEmoji()) {
			return nil, fmt.Errorf("%w: bad reaction", errBadFrame)
		}
		m, err := s.st.GetMessage(ctx, remote, re.MsgId)
		if errors.Is(err, store.ErrNotFound) || (err == nil && m.Deleted) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return one(re.MsgId, true, s.st.SetReaction(ctx, remote, re.MsgId, false, re.Emoji))
	case *pb.Update_Read:
		ids := c.Read.GetMsgIds()
		if len(ids) > maxReadIDs {
			return nil, fmt.Errorf("%w: read receipt too long", errBadFrame)
		}
		for _, id := range ids {
			if len(id) != idLen {
				return nil, fmt.Errorf("%w: bad read receipt", errBadFrame)
			}
		}
		return s.st.MarkSeen(ctx, remote, ids)
	}
	return nil, fmt.Errorf("%w: unknown update", errBadFrame)
}

// handleTyping publishes Typing for an accepted contact's typing frame.
func (s *Service) handleTyping(str network.Stream) {
	if !s.tasks.Track() {
		str.Reset()
		return
	}
	defer s.tasks.Done()
	remote := str.Conn().RemotePeer()
	ctx, cancel := svc.Timeout(s.tasks.Context(), str, s.readTimeout)
	defer cancel()
	if s.requireAccepted(ctx, remote) != nil {
		str.Reset()
		return
	}
	var f pb.Typing
	if err := wire.ReadFrame(bufio.NewReader(str), &f); err != nil {
		str.Reset()
		return
	}
	str.Close()
	s.bus.Publish(event.Typing{Peer: remote})
}

// updatedIDs are the messages u changes.
func updatedIDs(u *pb.Update) [][]byte {
	switch c := u.GetChange().(type) {
	case *pb.Update_Edit:
		return [][]byte{c.Edit.GetMsgId()}
	case *pb.Update_Delete:
		return [][]byte{c.Delete.GetMsgId()}
	case *pb.Update_Reaction:
		return [][]byte{c.Reaction.GetMsgId()}
	case *pb.Update_Read:
		return c.Read.GetMsgIds()
	}
	return nil
}

// updateKind names u's change for the log (never its text).
func updateKind(u *pb.Update) string {
	switch u.GetChange().(type) {
	case *pb.Update_Edit:
		return "edit"
	case *pb.Update_Delete:
		return "delete"
	case *pb.Update_Reaction:
		return "reaction"
	case *pb.Update_Read:
		return "read"
	}
	return "unknown"
}
