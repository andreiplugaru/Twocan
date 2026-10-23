package cli

import (
	"context"
	"image"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"

	"twocan/internal/core"
	"twocan/internal/event"
	"twocan/internal/identity"
	"twocan/internal/store"
)

func newPeer(t *testing.T) peer.ID {
	t.Helper()
	k, _, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(k)
	require.NoError(t, err)
	return id
}

type sentMsg struct {
	to   peer.ID
	text string
}

type fakeAPI struct {
	mu       sync.Mutex
	contacts []core.ContactView
	sent     []sentMsg
	addErr   error
	events   chan any
}

func newFake(cs ...core.ContactView) *fakeAPI {
	return &fakeAPI{contacts: cs, events: make(chan any, 8)}
}

func view(id peer.ID, name string) core.ContactView {
	return core.ContactView{Contact: store.Contact{PeerID: id, DisplayName: name, State: store.StateAccepted}}
}

func (f *fakeAPI) sentCopy() []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMsg(nil), f.sent...)
}

func (f *fakeAPI) PeerID() peer.ID                                        { return "" }
func (f *fakeAPI) DisplayName() string                                    { return "me" }
func (f *fakeAPI) SetDisplayName(string) error                            { return nil }
func (f *fakeAPI) MyCard() (string, error)                                { return "twocan:abc", nil }
func (f *fakeAPI) AddContact(context.Context, string) error               { return f.addErr }
func (f *fakeAPI) AddContactFromImage(context.Context, image.Image) error { return nil }
func (f *fakeAPI) AcceptContact(context.Context, peer.ID) error           { return nil }
func (f *fakeAPI) RejectContact(context.Context, peer.ID) error           { return nil }
func (f *fakeAPI) RenameContact(context.Context, peer.ID, string) error   { return nil }
func (f *fakeAPI) Contacts(context.Context) ([]core.ContactView, error) {
	return f.contacts, nil
}
func (f *fakeAPI) Send(_ context.Context, to peer.ID, text string) (store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{to, text})
	return store.Message{ID: []byte{1, 2, 3, 4, 5, 6}, PeerID: to, Text: text}, nil
}
func (f *fakeAPI) History(context.Context, peer.ID) ([]store.Message, error) { return nil, nil }
func (f *fakeAPI) MarkRead(context.Context, peer.ID) error                   { return nil }
func (f *fakeAPI) Subscribe() (<-chan any, func())                           { return f.events, func() {} }
func (f *fakeAPI) Reachability() network.Reachability                        { return network.ReachabilityUnknown }
func (f *fakeAPI) Stop() error                                               { return nil }

// safeBuf is a goroutine-safe output buffer.
type safeBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// runScript feeds a command to the CLI, then /quit, and returns the output.
func runScript(t *testing.T, api core.API, script string) string {
	t.Helper()
	var out safeBuf
	require.NoError(t, Run(context.Background(), api, strings.NewReader(script+"\n/quit\n"), &out))
	return out.String()
}

func TestCLIMsgResolvesPrefix(t *testing.T) {
	ann, bob := newPeer(t), newPeer(t)
	f := newFake(view(ann, "Ann"), view(bob, "Bob"))
	out := runScript(t, f, "/msg an hi there")
	require.Equal(t, []sentMsg{{ann, "hi there"}}, f.sentCopy())
	require.Contains(t, out, "queued 03040506")
}

func TestCLIAmbiguousPrefix(t *testing.T) {
	f := newFake(view(newPeer(t), "Ann"), view(newPeer(t), "Andy"))
	out := runScript(t, f, "/msg an x")
	require.Contains(t, out, `error: "an" matches 2 contacts`)
	require.Empty(t, f.sentCopy())
}

func TestCLIAddPrintsError(t *testing.T) {
	f := newFake()
	f.addErr = identity.ErrDamagedCard
	out := runScript(t, f, "/add twocan:zzz")
	require.Contains(t, out, "card is incomplete or damaged")
}

func TestCLIPrintsEvents(t *testing.T) {
	ann := newPeer(t)
	f := newFake(view(ann, "Ann"))
	pr, pw := io.Pipe()
	var out safeBuf
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), f, pr, &out) }()
	f.events <- event.MessageReceived{Msg: store.Message{PeerID: ann, Text: "yo"}}
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "<- Ann: yo") },
		2*time.Second, 10*time.Millisecond)
	_, err := pw.Write([]byte("/quit\n"))
	require.NoError(t, err)
	require.NoError(t, <-done)
}

func TestCLIUnknownCommand(t *testing.T) {
	out := runScript(t, newFake(), "/nope")
	require.Contains(t, out, "unknown command, try /help")
}
