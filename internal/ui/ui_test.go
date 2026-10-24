package ui

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"

	"twocan/internal/chat"
	"twocan/internal/event"
	"twocan/internal/store"
)

var (
	ann = peer.ID("ann")
	bob = peer.ID("bob")
	zed = peer.ID("zed")
	pat = peer.ID("pat")
	bad = peer.ID("bad")
)

// standardFake has, in store order: Zed asking to add us, then Ann and Bob
// (accepted), Pat (our request sent) and a blocked peer. Ann has two
// messages.
func standardFake() *fakeAPI {
	f := newFake()
	f.addContact(zed, "Zed", store.StatePendingIncoming)
	f.addContact(ann, "Ann", store.StateAccepted)
	f.addContact(bob, "Bob", store.StateAccepted)
	f.addContact(pat, "Pat", store.StatePendingOutgoing)
	f.addContact(bad, "Bad", store.StateBlocked)
	in := f.newMsg(ann, store.DirIn, "hi there")
	out := f.newMsg(ann, store.DirOut, "hello back")
	f.history[ann] = []store.Message{in, out}
	return f
}

func newTestView(t *testing.T, f *fakeAPI) *MainView {
	t.Helper()
	a := test.NewTempApp(t)
	w := a.NewWindow("Twocan")
	t.Cleanup(w.Close)
	v := NewMainView(f, w)
	w.SetContent(v.Content())
	w.Resize(fyne.NewSize(900, 600))
	return v
}

// walk visits o and every visible object under it, descending into
// containers and widget renderers.
func walk(o fyne.CanvasObject, fn func(fyne.CanvasObject)) {
	if o == nil || !o.Visible() {
		return
	}
	fn(o)
	switch c := o.(type) {
	case *fyne.Container:
		for _, ch := range c.Objects {
			walk(ch, fn)
		}
	case fyne.Widget:
		for _, ch := range test.WidgetRenderer(c).Objects() {
			walk(ch, fn)
		}
	}
}

// rows returns the contact rows currently on screen, by name.
func rows(v *MainView) map[string]*contactRow {
	out := make(map[string]*contactRow)
	walk(v.contactList, func(o fyne.CanvasObject) {
		if r, ok := o.(*contactRow); ok {
			out[r.name.Text] = r
		}
	})
	return out
}

// bubbles returns the conversation bubbles on screen, top to bottom.
func bubbles(v *MainView) []*bubble {
	var out []*bubble
	walk(v.convo, func(o fyne.CanvasObject) {
		if b, ok := o.(*bubble); ok {
			out = append(out, b)
		}
	})
	// The list reuses rows, so order them by where they are on screen.
	d := fyne.CurrentApp().Driver()
	sort.SliceStable(out, func(i, j int) bool {
		return d.AbsolutePositionForObject(out[i]).Y < d.AbsolutePositionForObject(out[j]).Y
	})
	return out
}

func bubbleTexts(v *MainView) []string {
	var out []string
	for _, b := range bubbles(v) {
		out = append(out, b.text.Text)
	}
	return out
}

func selectContact(t *testing.T, v *MainView, id peer.ID) {
	t.Helper()
	for i, c := range v.contacts {
		if c.PeerID == id {
			v.contactList.Select(i)
			return
		}
	}
	t.Fatalf("contact %s not in list", id)
}

func typeAndEnter(v *MainView, text string) {
	test.Type(v.entry, text)
	v.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
}

func TestPendingRequestShowsAcceptReject(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)

	rs := rows(v)
	require.NotContains(t, rs, "Bad", "blocked contacts are hidden")
	require.Equal(t, zed, v.contacts[0].PeerID, "pending_incoming sorts first")

	z := rs["Zed"]
	require.NotNil(t, z)
	require.True(t, z.note.Visible())
	require.Equal(t, "wants to add you", z.note.Text)
	require.True(t, z.accept.Visible())
	require.True(t, z.reject.Visible())
	require.Equal(t, "Accept", z.accept.Text)
	require.Equal(t, "Reject", z.reject.Text)

	a := rs["Ann"]
	require.NotNil(t, a)
	require.False(t, a.accept.Visible())
	require.False(t, a.reject.Visible())
	require.True(t, a.more.Visible(), "accepted rows have the ⋯ button")

	p := rs["Pat"]
	require.NotNil(t, p)
	require.Equal(t, "request sent", p.note.Text)
	require.False(t, p.accept.Visible())

	test.Tap(z.accept)
	require.Equal(t, []peer.ID{zed}, f.AcceptCalls)
	require.Empty(t, f.RejectCalls)
}

func TestSelectContactLoadsHistoryAndMarksRead(t *testing.T) {
	f := standardFake()
	f.contact(ann).Unread = 1
	v := newTestView(t, f)
	require.Equal(t, "(1)", rows(v)["Ann"].unread.Text)

	selectContact(t, v, ann)

	require.Equal(t, []string{"hi there", "hello back"}, bubbleTexts(v))
	require.Equal(t, []peer.ID{ann}, f.MarkReadCalls)
	require.False(t, rows(v)["Ann"].unread.Visible(), "unread badge clears")

	bs := bubbles(v)
	require.False(t, bs[0].out)
	require.True(t, bs[1].out)
	require.Regexp(t, `^\d\d:\d\d$`, bs[0].meta.Text)
	require.Regexp(t, `^\d\d:\d\d 🕓$`, bs[1].meta.Text)
}

func TestLongMessageWraps(t *testing.T) {
	f := standardFake()
	f.history[ann] = append(f.history[ann], f.newMsg(ann, store.DirIn, strings.Repeat("word ", 200)))
	v := newTestView(t, f)
	selectContact(t, v, ann)

	bs := bubbles(v)
	require.Len(t, bs, 3)
	require.Greater(t, bs[2].Size().Height, 3*bs[0].Size().Height, "the row grows to fit the wrapped text")
	require.Less(t, bs[2].text.Size().Width, v.convo.Size().Width, "text wraps inside the bubble")
}

func TestSendFromEntry(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	selectContact(t, v, ann)

	typeAndEnter(v, "  hello ")

	require.Equal(t, []sendCall{{To: ann, Text: "  hello "}}, f.SendCalls)
	require.Empty(t, v.entry.Text)
	bs := bubbles(v)
	require.Len(t, bs, 3)
	last := bs[2]
	require.Equal(t, "  hello ", last.text.Text)
	require.True(t, last.out)
	require.Contains(t, last.meta.Text, "🕓")
}

func TestSendErrorShown(t *testing.T) {
	f := standardFake()
	f.sendErr = fmt.Errorf("chat: send: %w", chat.ErrTooLong)
	v := newTestView(t, f)
	selectContact(t, v, ann)

	typeAndEnter(v, "x")

	require.Len(t, f.SendCalls, 1)
	require.True(t, v.sendErr.Visible())
	require.Equal(t, "Message is too long (max 16 KB)", v.sendErr.Text)
	require.Equal(t, "x", v.entry.Text, "entry text is kept")
	require.Len(t, bubbles(v), 2)

	// A successful send clears the error.
	f.sendErr = nil
	v.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	require.False(t, v.sendErr.Visible())
}

func TestIncomingEventAppends(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	selectContact(t, v, ann)

	v.apply(f.receive(ann, "new one"))
	require.Equal(t, []string{"hi there", "hello back", "new one"}, bubbleTexts(v))
	require.False(t, rows(v)["Ann"].unread.Visible(), "shown messages are read")

	v.apply(f.receive(bob, "from bob"))
	b := rows(v)["Bob"]
	require.True(t, b.unread.Visible())
	require.Equal(t, "(1)", b.unread.Text)
	require.Equal(t, []string{"hi there", "hello back", "new one"}, bubbleTexts(v), "Ann's pane unchanged")
}

func TestDeliveredEventUpdatesTick(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	selectContact(t, v, ann)
	typeAndEnter(v, "hey")
	sent := f.history[ann][len(f.history[ann])-1]
	require.Contains(t, bubbles(v)[2].meta.Text, "🕓")

	v.apply(event.MessageDelivered{Peer: ann, ID: sent.ID})

	meta := bubbles(v)[2].meta.Text
	require.Contains(t, meta, "✓")
	require.NotContains(t, meta, "🕓")
	require.Contains(t, bubbles(v)[1].meta.Text, "🕓", "other messages keep their status")
}

func TestEntryDisabledForPending(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	require.True(t, v.entry.Disabled(), "nothing selected")

	selectContact(t, v, pat)
	require.True(t, v.entry.Disabled())
	require.True(t, v.send.Disabled())

	selectContact(t, v, ann)
	require.False(t, v.entry.Disabled())
	require.False(t, v.send.Disabled())

	// Pat accepts: the entry enables without reselecting.
	selectContact(t, v, pat)
	f.contact(pat).State = store.StateAccepted
	v.apply(event.ContactChanged{Peer: pat})
	require.False(t, v.entry.Disabled())
}

func TestStatusBar(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	require.Equal(t, "You are reachable: checking…", v.status.Text)

	v.apply(event.Reachability{Status: network.ReachabilityPrivate})
	require.Equal(t, "You are reachable: behind NAT", v.status.Text)

	v.apply(event.Reachability{Status: network.ReachabilityPublic})
	selectContact(t, v, ann)
	require.Equal(t, "You are reachable: public · offline", v.status.Text)

	f.contact(ann).Connected, f.contact(ann).Relayed = true, true
	v.apply(event.Connectivity{Peer: ann, Connected: true, Relayed: true})
	require.Equal(t, "You are reachable: public · via relay", v.status.Text)
	require.Equal(t, connectedColor(), rows(v)["Ann"].dot.FillColor)

	f.contact(ann).Relayed = false
	v.apply(event.Connectivity{Peer: ann, Connected: true})
	require.Equal(t, "You are reachable: public · direct", v.status.Text)
	require.Equal(t, offlineColor(), rows(v)["Bob"].dot.FillColor)
}
