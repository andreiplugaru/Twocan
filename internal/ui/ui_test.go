package ui

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
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

// requireFits fails unless l is laid out at least as wide as its text needs
// untruncated.
func requireFits(t *testing.T, l *widget.Label) {
	t.Helper()
	full := widget.NewLabelWithStyle(l.Text, l.Alignment, l.TextStyle).MinSize()
	require.GreaterOrEqual(t, l.Size().Width, full.Width, "%q is cut off", l.Text)
	require.GreaterOrEqual(t, l.Size().Height, full.Height, "%q is cut off", l.Text)
}

// In the default window every row shows its name, note and buttons whole,
// and each row is as tall as it needs: no more, no less.
func TestContactRowsFitDefaultWindow(t *testing.T) {
	f := standardFake()
	f.contact(ann).Unread = 3
	v := newTestView(t, f)
	rs := rows(v)
	z, a, p := rs["Zed"], rs["Ann"], rs["Pat"]
	require.NotNil(t, z)
	require.NotNil(t, a)
	require.NotNil(t, p)

	for _, l := range []*widget.Label{z.name, z.note, a.name, a.unread, p.name, p.note} {
		require.True(t, l.Visible(), "%q visible", l.Text)
		requireFits(t, l)
	}
	for _, b := range []*widget.Button{z.accept, z.reject, a.more} {
		require.GreaterOrEqual(t, b.Size().Width, b.MinSize().Width, "%q button", b.Text)
	}
	for _, r := range []*contactRow{z, a, p} {
		require.Equal(t, r.MinSize().Height, r.Size().Height, "row %q height", r.name.Text)
	}
	require.Greater(t, z.Size().Height, a.Size().Height, "a request row is taller than a plain one")
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

// requireBottomVisible checks that the newest bubble is fully inside the
// conversation's viewport.
func requireBottomVisible(t *testing.T, v *MainView, wantText string) {
	t.Helper()
	d := fyne.CurrentApp().Driver()
	bs := bubbles(v)
	last := bs[len(bs)-1]
	require.Equal(t, wantText, last.text.Text)
	bottom := d.AbsolutePositionForObject(last).Y + last.Size().Height
	viewEnd := d.AbsolutePositionForObject(v.convo).Y + v.convo.Size().Height
	require.LessOrEqual(t, bottom, viewEnd+0.5, "newest bubble is cut off below the viewport")
	require.GreaterOrEqual(t, last.Size().Height, last.MinSize().Height, "newest bubble's row is too short")
}

func TestNewLongMessageScrolledIntoView(t *testing.T) {
	f := standardFake()
	for i := range 30 {
		f.history[ann] = append(f.history[ann], f.newMsg(ann, store.DirIn, fmt.Sprintf("short %d", i)))
	}
	v := newTestView(t, f)
	selectContact(t, v, ann)

	received := strings.Repeat("word ", 300)
	v.apply(f.receive(ann, received))
	requireBottomVisible(t, v, received)

	sent := strings.Repeat("mine ", 300)
	typeAndEnter(v, sent)
	requireBottomVisible(t, v, sent)
}

func TestRowsRemeasuredOnResize(t *testing.T) {
	f := standardFake()
	long := strings.Repeat("word ", 60)
	f.history[ann] = append(f.history[ann], f.newMsg(ann, store.DirIn, long))
	v := newTestView(t, f)
	selectContact(t, v, ann)
	wide := bubbles(v)[2]
	require.Equal(t, long, wide.text.Text)
	wideHeight := wide.Size().Height

	v.win.Resize(fyne.NewSize(500, 600))

	narrow := bubbles(v)[2]
	require.Equal(t, long, narrow.text.Text)
	require.Greater(t, narrow.Size().Height, wideHeight, "the row grows when the text wraps more")
	require.GreaterOrEqual(t, narrow.Size().Height, narrow.MinSize().Height, "the row fits the wrapped text")
}

func TestSwitchingConversationsDropsOldHeights(t *testing.T) {
	// Ann's first message is long and is measured when her conversation
	// opens; she has enough short ones after it that it is off screen once
	// the view scrolls to the bottom. Bob has only short messages.
	f := standardFake()
	f.history[ann] = []store.Message{f.newMsg(ann, store.DirIn, strings.Repeat("word ", 300))}
	for i := range 40 {
		f.history[ann] = append(f.history[ann], f.newMsg(ann, store.DirIn, fmt.Sprintf("ann %d", i)))
		f.history[bob] = append(f.history[bob], f.newMsg(bob, store.DirIn, fmt.Sprintf("bob %d", i)))
	}

	fresh := newTestView(t, f)
	selectContact(t, fresh, bob)
	want := fresh.convo.GetScrollOffset()

	v := newTestView(t, f)
	selectContact(t, v, ann)
	selectContact(t, v, bob)

	require.InDelta(t, want, v.convo.GetScrollOffset(), 0.5,
		"Bob's scroll extent must not include the height of Ann's long first message")
}

func TestReceivedMessageAlreadyInHistoryNotDuplicated(t *testing.T) {
	f := standardFake()
	// The node stores the message, the user opens the conversation, and only
	// then does the queued event arrive.
	ev := f.receive(ann, "racing")
	v := newTestView(t, f)
	selectContact(t, v, ann)

	v.apply(ev)

	require.Equal(t, []string{"hi there", "hello back", "racing"}, bubbleTexts(v))
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

func TestDraftKeptPerContact(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	selectContact(t, v, ann)
	test.Type(v.entry, "for ann")

	selectContact(t, v, bob)
	require.Empty(t, v.entry.Text, "Ann's draft does not follow to Bob")
	test.Type(v.entry, "for bob")

	selectContact(t, v, ann)
	require.Equal(t, "for ann", v.entry.Text)
	selectContact(t, v, bob)
	require.Equal(t, "for bob", v.entry.Text)
	require.Empty(t, f.SendCalls)

	// A sent message is not kept as a draft.
	v.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	require.Equal(t, []sendCall{{To: bob, Text: "for bob"}}, f.SendCalls)
	selectContact(t, v, ann)
	selectContact(t, v, bob)
	require.Empty(t, v.entry.Text)

	// Bob is blocked while his conversation is open: the entry clears.
	test.Type(v.entry, "never sent")
	f.contact(bob).State = store.StateBlocked
	v.apply(event.ContactChanged{Peer: bob})
	require.Empty(t, v.entry.Text)
	selectContact(t, v, ann)
	require.Equal(t, "for ann", v.entry.Text)
}

func TestConversationHeader(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	require.Empty(t, v.peerName.Text, "nothing selected")

	selectContact(t, v, ann)
	require.Equal(t, "Ann", v.peerName.Text)
	require.True(t, v.peerName.Visible())
	selectContact(t, v, pat)
	require.Equal(t, "Pat", v.peerName.Text)

	f.contact(pat).Nickname = "Patricia"
	v.apply(event.ContactChanged{Peer: pat})
	require.Equal(t, "Patricia", v.peerName.Text)

	f.contact(pat).State = store.StateBlocked
	v.apply(event.ContactChanged{Peer: pat})
	require.Empty(t, v.peerName.Text)
}
