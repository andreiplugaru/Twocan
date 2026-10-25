package cli

import (
	"context"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	"twocan/internal/core"
	"twocan/internal/event"
	"twocan/internal/identity"
	"twocan/internal/imgtest"
	"twocan/internal/p2p"
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
	images   []image.Image // AddContactFromImage calls
	events   chan any
	details  core.ContactDetails
	check    p2p.CheckResult
	pings    int
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

func (f *fakeAPI) PeerID() peer.ID                          { return "" }
func (f *fakeAPI) DisplayName() string                      { return "me" }
func (f *fakeAPI) SetDisplayName(string) error              { return nil }
func (f *fakeAPI) MyCard() (string, error)                  { return "twocan:abc", nil }
func (f *fakeAPI) AddContact(context.Context, string) error { return f.addErr }
func (f *fakeAPI) AddContactFromImage(_ context.Context, img image.Image) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images = append(f.images, img)
	return nil
}
func (f *fakeAPI) AcceptContact(context.Context, peer.ID) error         { return nil }
func (f *fakeAPI) RejectContact(context.Context, peer.ID) error         { return nil }
func (f *fakeAPI) RenameContact(context.Context, peer.ID, string) error { return nil }
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
func (f *fakeAPI) ContactDetails(context.Context, peer.ID) (core.ContactDetails, error) {
	return f.details, nil
}
func (f *fakeAPI) Ping(context.Context, peer.ID) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	return time.Millisecond, nil
}
func (f *fakeAPI) CheckConnection(_ context.Context, _ peer.ID, report func(int, p2p.Step)) (p2p.CheckResult, error) {
	for i, s := range f.check.Steps {
		report(i, p2p.Step{Title: s.Title, Status: p2p.StepRunning})
		report(i, s)
	}
	return f.check, nil
}

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
	require.Contains(t, out, "error: Card is incomplete or damaged")
}

func TestCLIAddQR(t *testing.T) {
	bob := newPeer(t)
	card, err := identity.EncodeCard(identity.Card{PeerID: bob, DisplayName: "Bob"})
	require.NoError(t, err)
	b, err := identity.CardQRPNG(card, 256)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "bob card.png")
	require.NoError(t, os.WriteFile(path, b, 0o600))

	f := newFake(view(bob, "Bob"))
	out := runScript(t, f, "/addqr "+path)
	require.Len(t, f.images, 1)
	require.Equal(t, image.Rect(0, 0, 256, 256), f.images[0].Bounds())
	require.Contains(t, out, "added Bob (request pending)")
}

func TestCLIAddQRErrors(t *testing.T) {
	f := newFake()
	require.Contains(t, runScript(t, f, "/addqr"), "usage: /addqr <image file>")
	missing := filepath.Join(t.TempDir(), "nope.png")
	require.Contains(t, runScript(t, f, "/addqr "+missing), "error: ")
	huge := filepath.Join(t.TempDir(), "huge.png")
	require.NoError(t, os.WriteFile(huge, imgtest.HugePNGHeader(100000, 100000), 0o600))
	require.Contains(t, runScript(t, f, "/addqr "+huge), "error: That image is too large")
	require.Empty(t, f.images)
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

func TestCLIInfo(t *testing.T) {
	ann := newPeer(t)
	v := view(ann, "Ann")
	v.Connected = true
	f := newFake(v)
	f.details = core.ContactDetails{
		Contact: store.Contact{Addrs: []ma.Multiaddr{
			ma.StringCast("/ip4/192.168.1.5/tcp/1"), ma.StringCast("/ip4/192.168.1.5/udp/2/quic-v1"),
		}},
		Conns: []p2p.Conn{{Remote: ma.StringCast("/ip4/192.168.1.5/tcp/1"), Transport: "TCP/IPv4", Opened: time.Now(), Source: p2p.SourceMDNS}},
	}
	out := runScript(t, f, "/info an")
	require.Equal(t, 1, f.pings, "a connected contact is pinged first")
	require.Contains(t, out, "Ann ("+ann.String()+")\n")
	require.Contains(t, out, "  Status: Connected directly\n")
	require.Contains(t, out, "  Found via: your local network (mDNS)\n")
	require.Contains(t, out, "  Saved addresses: /ip4/192.168.1.5/tcp/1\n    /ip4/192.168.1.5/udp/2/quic-v1\n")

	require.Contains(t, runScript(t, f, "/info"), "usage: /info <who>")
}

func TestCLICheckPrintsFinishedStepsAndVerdict(t *testing.T) {
	f := newFake(view(newPeer(t), "Ann"))
	f.check = p2p.CheckResult{
		Steps: []p2p.Step{
			{Title: "Helper", Status: p2p.StepOK, Detail: "Connected to the helper."},
			{Title: "Direct connection", Status: p2p.StepFail, Detail: "No answer."},
		},
		Verdict: "Couldn't connect.",
	}
	out := runScript(t, f, "/check ann")
	require.Contains(t, out, "checking the connection to Ann")
	require.Contains(t, out, "  ✓ Helper: Connected to the helper.\n  ✗ Direct connection: No answer.\nverdict: Couldn't connect.\n")
	require.NotContains(t, out, "… Helper", "running steps are not printed")
}
