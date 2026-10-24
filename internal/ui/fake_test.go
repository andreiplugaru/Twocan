package ui

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/core"
	"twocan/internal/event"
	"twocan/internal/store"
)

// fakeAPI is an in-memory core.API. Tests run on one goroutine, so it has no
// locking.
type fakeAPI struct {
	name     string
	contacts []core.ContactView // in store order
	history  map[peer.ID][]store.Message
	reach    network.Reachability
	sendErr  error
	nextID   uint32

	AcceptCalls   []peer.ID
	RejectCalls   []peer.ID
	MarkReadCalls []peer.ID
	SendCalls     []sendCall
}

type sendCall struct {
	To   peer.ID
	Text string
}

var _ core.API = (*fakeAPI)(nil)

func newFake() *fakeAPI {
	return &fakeAPI{name: "Me", history: make(map[peer.ID][]store.Message)}
}

// addContact appends a contact in store order.
func (f *fakeAPI) addContact(id peer.ID, name string, state store.ContactState) {
	f.contacts = append(f.contacts, core.ContactView{Contact: store.Contact{
		PeerID: id, DisplayName: name, State: state,
	}})
}

func (f *fakeAPI) contact(id peer.ID) *core.ContactView {
	for i := range f.contacts {
		if f.contacts[i].PeerID == id {
			return &f.contacts[i]
		}
	}
	return nil
}

func (f *fakeAPI) newMsg(id peer.ID, dir store.Direction, text string) store.Message {
	f.nextID++
	msgID := make([]byte, 16)
	binary.BigEndian.PutUint32(msgID[12:], f.nextID)
	now := time.Now()
	m := store.Message{ID: msgID, PeerID: id, Direction: dir, Text: text, SentAt: now}
	if dir == store.DirIn {
		m.ReceivedAt, m.Status = now, store.StatusReceived
	} else {
		m.Status = store.StatusPending
	}
	return m
}

// receive stores an incoming message as the node would, bumps the unread
// count and returns the event the node would publish.
func (f *fakeAPI) receive(id peer.ID, text string) event.MessageReceived {
	m := f.newMsg(id, store.DirIn, text)
	f.history[id] = append(f.history[id], m)
	f.contact(id).Unread++
	return event.MessageReceived{Msg: m}
}

func (f *fakeAPI) PeerID() peer.ID                  { return peer.ID("me") }
func (f *fakeAPI) DisplayName() string              { return f.name }
func (f *fakeAPI) SetDisplayName(name string) error { f.name = name; return nil }
func (f *fakeAPI) MyCard() (string, error)          { return "twocan:fake", nil }

func (f *fakeAPI) AddContact(context.Context, string) error { return errors.New("not in fake") }
func (f *fakeAPI) AddContactFromImage(context.Context, image.Image) error {
	return errors.New("not in fake")
}

func (f *fakeAPI) AcceptContact(_ context.Context, id peer.ID) error {
	f.AcceptCalls = append(f.AcceptCalls, id)
	return nil
}

func (f *fakeAPI) RejectContact(_ context.Context, id peer.ID) error {
	f.RejectCalls = append(f.RejectCalls, id)
	return nil
}

func (f *fakeAPI) RenameContact(context.Context, peer.ID, string) error {
	return errors.New("not in fake")
}

func (f *fakeAPI) Contacts(context.Context) ([]core.ContactView, error) {
	return append([]core.ContactView(nil), f.contacts...), nil
}

func (f *fakeAPI) Send(_ context.Context, to peer.ID, text string) (store.Message, error) {
	f.SendCalls = append(f.SendCalls, sendCall{To: to, Text: text})
	if f.sendErr != nil {
		return store.Message{}, f.sendErr
	}
	m := f.newMsg(to, store.DirOut, text)
	f.history[to] = append(f.history[to], m)
	return m, nil
}

func (f *fakeAPI) History(_ context.Context, id peer.ID) ([]store.Message, error) {
	return append([]store.Message(nil), f.history[id]...), nil
}

func (f *fakeAPI) MarkRead(_ context.Context, id peer.ID) error {
	f.MarkReadCalls = append(f.MarkReadCalls, id)
	if c := f.contact(id); c != nil {
		c.Unread = 0
	}
	return nil
}

func (f *fakeAPI) Subscribe() (<-chan any, func()) {
	ch := make(chan any)
	return ch, func() {}
}

func (f *fakeAPI) Reachability() network.Reachability { return f.reach }
func (f *fakeAPI) Stop() error                        { return nil }
