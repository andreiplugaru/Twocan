package ui

import (
	"context"
	"encoding/binary"
	"image"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/core"
	"twocan/internal/event"
	"twocan/internal/p2p"
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
	details  core.ContactDetails
	check    p2p.CheckResult
	checkErr error
	addErr   error // returned by AddContact and AddContactFromImage
	nextID   uint32

	AcceptCalls   []peer.ID
	RejectCalls   []peer.ID
	MarkReadCalls []peer.ID
	SendCalls     []sendCall
	SetNameCalls  []string
	AddCalls      []string
	AddImageCalls []image.Image
	RenameCalls   []renameCall
	ReplyCalls    []replyCall
	EditCalls     []sendCall // To is the conversation, Text the new text
	DeleteCalls   []deleteCall
	ReactCalls    []string // "emoji" per call, "" for a removal
	TypingCalls   []peer.ID
	SearchCalls   []string
	searchResult  []store.Message
}

type replyCall struct {
	To      peer.ID
	ReplyTo []byte
	Text    string
}

type deleteCall struct {
	ID          []byte
	ForEveryone bool
}

type renameCall struct {
	ID   peer.ID
	Name string
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

func (f *fakeAPI) PeerID() peer.ID     { return peer.ID("me") }
func (f *fakeAPI) DisplayName() string { return f.name }
func (f *fakeAPI) SetDisplayName(name string) error {
	f.SetNameCalls = append(f.SetNameCalls, name)
	f.name = name
	return nil
}
func (f *fakeAPI) MyCard() (string, error) { return "twocan:fake", nil }

func (f *fakeAPI) AddContact(_ context.Context, card string) error {
	f.AddCalls = append(f.AddCalls, card)
	return f.addErr
}
func (f *fakeAPI) AddContactFromImage(_ context.Context, img image.Image) error {
	f.AddImageCalls = append(f.AddImageCalls, img)
	return f.addErr
}

func (f *fakeAPI) AcceptContact(_ context.Context, id peer.ID) error {
	f.AcceptCalls = append(f.AcceptCalls, id)
	return nil
}

func (f *fakeAPI) RejectContact(_ context.Context, id peer.ID) error {
	f.RejectCalls = append(f.RejectCalls, id)
	return nil
}

func (f *fakeAPI) RenameContact(_ context.Context, id peer.ID, nickname string) error {
	f.RenameCalls = append(f.RenameCalls, renameCall{ID: id, Name: nickname})
	f.contact(id).Nickname = nickname
	return nil
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

func (f *fakeAPI) Reply(_ context.Context, to peer.ID, replyTo []byte, text string) (store.Message, error) {
	f.ReplyCalls = append(f.ReplyCalls, replyCall{To: to, ReplyTo: replyTo, Text: text})
	m := f.newMsg(to, store.DirOut, text)
	m.ReplyTo = replyTo
	f.history[to] = append(f.history[to], m)
	return m, nil
}

// message returns a pointer to message msgID of conversation id, or nil.
func (f *fakeAPI) message(id peer.ID, msgID []byte) *store.Message {
	for i := range f.history[id] {
		if string(f.history[id][i].ID) == string(msgID) {
			return &f.history[id][i]
		}
	}
	return nil
}

func (f *fakeAPI) Edit(_ context.Context, id peer.ID, msgID []byte, text string) error {
	f.EditCalls = append(f.EditCalls, sendCall{To: id, Text: text})
	if m := f.message(id, msgID); m != nil {
		m.Text, m.EditedAt = text, time.Now()
	}
	return nil
}

func (f *fakeAPI) Delete(_ context.Context, id peer.ID, msgID []byte, forEveryone bool) error {
	f.DeleteCalls = append(f.DeleteCalls, deleteCall{ID: msgID, ForEveryone: forEveryone})
	if forEveryone {
		if m := f.message(id, msgID); m != nil {
			m.Text, m.Deleted = "", true
		}
		return nil
	}
	var kept []store.Message
	for _, m := range f.history[id] {
		if string(m.ID) != string(msgID) {
			kept = append(kept, m)
		}
	}
	f.history[id] = kept
	return nil
}

func (f *fakeAPI) React(_ context.Context, id peer.ID, msgID []byte, emoji string) error {
	f.ReactCalls = append(f.ReactCalls, emoji)
	if m := f.message(id, msgID); m != nil {
		m.MyReaction = emoji
	}
	return nil
}

func (f *fakeAPI) Typing(id peer.ID) { f.TypingCalls = append(f.TypingCalls, id) }

func (f *fakeAPI) Search(_ context.Context, q string) ([]store.Message, error) {
	f.SearchCalls = append(f.SearchCalls, q)
	return f.searchResult, nil
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

func (f *fakeAPI) ContactDetails(_ context.Context, id peer.ID) (core.ContactDetails, error) {
	d := f.details
	if c := f.contact(id); c != nil {
		d.Contact = c.Contact
	}
	return d, nil
}

func (f *fakeAPI) Ping(context.Context, peer.ID) (time.Duration, error) { return time.Millisecond, nil }

func (f *fakeAPI) CheckConnection(_ context.Context, _ peer.ID, report func(int, p2p.Step)) (p2p.CheckResult, error) {
	for i, s := range f.check.Steps {
		report(i, p2p.Step{Title: s.Title, Status: p2p.StepRunning})
		report(i, s)
	}
	return f.check, f.checkErr
}
func (f *fakeAPI) Stop() error { return nil }
