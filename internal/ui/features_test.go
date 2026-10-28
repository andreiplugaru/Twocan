package ui

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/require"

	"twocan/internal/event"
	"twocan/internal/store"
)

// menuLabels lists a menu's item labels ("-" for a separator), with
// "(off)" after disabled ones.
func menuLabels(m *fyne.Menu) []string {
	var out []string
	for _, it := range m.Items {
		switch {
		case it.IsSeparator:
			out = append(out, "-")
		case it.Disabled:
			out = append(out, it.Label+" (off)")
		default:
			out = append(out, it.Label)
		}
	}
	return out
}

func menuItem(t *testing.T, m *fyne.Menu, label string) *fyne.MenuItem {
	t.Helper()
	for _, it := range m.Items {
		if it.Label == label {
			return it
		}
	}
	t.Fatalf("no menu item %q in %v", label, menuLabels(m))
	return nil
}

// tapDialogButton taps the button labelled label in the topmost dialog.
func tapDialogButton(t *testing.T, v *MainView, label string) {
	t.Helper()
	var found *widget.Button
	walk(v.win.Canvas().Overlays().Top(), func(o fyne.CanvasObject) {
		if b, ok := o.(*widget.Button); ok && b.Text == label {
			found = b
		}
	})
	require.NotNil(t, found, "no %q button", label)
	test.Tap(found)
}

func openAnn(t *testing.T) (*fakeAPI, *MainView) {
	t.Helper()
	f := standardFake()
	v := newTestView(t, f)
	selectContact(t, v, ann)
	return f, v
}

func TestBubbleShowsFormattingLinksAndMarks(t *testing.T) {
	f := standardFake()
	m := f.newMsg(ann, store.DirOut, "*bold* and _soft_, see https://twocan.app/x.")
	m.Status, m.EditedAt, m.TheirReaction, m.MyReaction = store.StatusRead, time.Now(), "❤️", "👍"
	f.history[ann] = append(f.history[ann], m)
	v := newTestView(t, f)
	selectContact(t, v, ann)

	b := bubbles(v)[2]
	require.Equal(t, "bold and soft, see https://twocan.app/x.", b.plainText())
	segs := b.text.Segments
	require.True(t, segs[0].(*widget.TextSegment).Style.TextStyle.Bold)
	require.True(t, segs[2].(*widget.TextSegment).Style.TextStyle.Italic)
	link := segs[4].(*widget.HyperlinkSegment)
	require.Equal(t, "https://twocan.app/x", link.URL.String())
	require.Regexp(t, `^edited · \d\d:\d\d ✓✓$`, b.meta.Text)
	require.Equal(t, "❤️ 👍", b.reactions.Text)
	require.True(t, b.reactions.Visible())
	require.False(t, bubbles(v)[0].reactions.Visible(), "no reactions, no line")
}

func TestDeletedBubble(t *testing.T) {
	f := standardFake()
	f.history[ann][1].Deleted, f.history[ann][1].Text = true, ""
	v := newTestView(t, f)
	selectContact(t, v, ann)
	b := bubbles(v)[1]
	require.Equal(t, "This message was deleted", b.plainText())
	require.NotContains(t, b.meta.Text, "🕓")
	require.Equal(t, []string{"Delete for me"}, menuLabels(v.messageMenu(f.history[ann][1])))
}

func TestMessageMenus(t *testing.T) {
	f, v := openAnn(t)
	in, out := f.history[ann][0], f.history[ann][1]
	require.Equal(t, []string{"Reply", "React", "Copy text", "-", "Delete for me"}, menuLabels(v.messageMenu(in)))
	require.Equal(t, []string{"Reply", "React", "Copy text", "Edit", "-", "Delete for me", "Delete for everyone"},
		menuLabels(v.messageMenu(out)))

	menuItem(t, v.messageMenu(in), "Copy text").Action()
	require.Equal(t, "hi there", fyne.CurrentApp().Clipboard().Content())
}

func TestReply(t *testing.T) {
	f, v := openAnn(t)
	in := f.history[ann][0]
	menuItem(t, v.messageMenu(in), "Reply").Action()
	require.True(t, v.composeBar.Visible())
	require.Equal(t, "Replying to Ann: hi there", v.composeLabel.Text)

	typeAndEnter(v, "sure")
	require.Equal(t, []replyCall{{To: ann, ReplyTo: in.ID, Text: "sure"}}, f.ReplyCalls)
	require.Empty(t, f.SendCalls)
	require.False(t, v.composeBar.Visible())
	last := bubbles(v)[2]
	require.Equal(t, "» Ann: hi there", last.quote.Text)
	require.True(t, last.quote.Visible())

	// Cancelling sends a plain message again.
	v.startReply(in)
	v.cancelCompose()
	typeAndEnter(v, "plain")
	require.Len(t, f.SendCalls, 1)
}

func TestEditKeepsDraft(t *testing.T) {
	f, v := openAnn(t)
	out := f.history[ann][1]
	test.Type(v.entry, "half-written")
	menuItem(t, v.messageMenu(out), "Edit").Action()
	require.Equal(t, "hello back", v.entry.Text)
	require.Equal(t, "Save", v.send.Text)

	v.entry.SetText("hello there")
	v.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	require.Equal(t, []sendCall{{To: ann, Text: "hello there"}}, f.EditCalls)
	require.Equal(t, "half-written", v.entry.Text, "the draft comes back")
	require.Equal(t, "Send", v.send.Text)
	b := bubbles(v)[1]
	require.Equal(t, "hello there", b.plainText())
	require.Contains(t, b.meta.Text, "edited")

	v.startEdit(out)
	v.cancelCompose()
	require.Equal(t, "half-written", v.entry.Text)
	require.Len(t, f.EditCalls, 1)
}

func TestReactFromMenu(t *testing.T) {
	f, v := openAnn(t)
	in := f.history[ann][0]
	react := menuItem(t, v.messageMenu(in), "React")
	menuItem(t, react.ChildMenu, "😂").Action()
	require.Equal(t, []string{"😂"}, f.ReactCalls)
	require.Equal(t, "😂", bubbles(v)[0].reactions.Text)

	react = menuItem(t, v.messageMenu(f.history[ann][0]), "React")
	require.True(t, menuItem(t, react.ChildMenu, "😂").Checked)
	menuItem(t, react.ChildMenu, "Remove reaction").Action()
	require.Equal(t, []string{"😂", ""}, f.ReactCalls)
	require.False(t, bubbles(v)[0].reactions.Visible())
}

func TestDeleteAsksFirst(t *testing.T) {
	f, v := openAnn(t)
	out := f.history[ann][1]
	menuItem(t, v.messageMenu(out), "Delete for everyone").Action()
	tapDialogButton(t, v, "No")
	require.Empty(t, f.DeleteCalls)

	menuItem(t, v.messageMenu(out), "Delete for everyone").Action()
	tapDialogButton(t, v, "Yes")
	require.Equal(t, []deleteCall{{ID: out.ID, ForEveryone: true}}, f.DeleteCalls)
	require.Equal(t, "This message was deleted", bubbles(v)[1].plainText())

	menuItem(t, v.messageMenu(f.history[ann][0]), "Delete for me").Action()
	tapDialogButton(t, v, "Yes")
	require.Equal(t, []string{"This message was deleted"}, bubbleTexts(v))
}

func TestUpdatesEventRefreshesOpenConversation(t *testing.T) {
	f, v := openAnn(t)
	f.history[ann][0].Text, f.history[ann][0].EditedAt = "hi there!", time.Now()
	v.apply(event.MessagesUpdated{Peer: ann, IDs: [][]byte{f.history[ann][0].ID}})
	require.Equal(t, []string{"hi there!", "hello back"}, bubbleTexts(v))

	// An update in another conversation leaves this one alone.
	f.history[bob] = []store.Message{f.newMsg(bob, store.DirIn, "yo")}
	v.apply(event.MessagesUpdated{Peer: bob, IDs: [][]byte{f.history[bob][0].ID}})
	require.Equal(t, []string{"hi there!", "hello back"}, bubbleTexts(v))
}

func TestTypingIndicator(t *testing.T) {
	f, v := openAnn(t)
	require.False(t, v.typing.Visible())
	v.apply(event.Typing{Peer: bob})
	require.False(t, v.typing.Visible(), "only for the open conversation")
	v.apply(event.Typing{Peer: ann})
	require.True(t, v.typing.Visible())
	v.apply(f.receive(ann, "done typing"))
	require.False(t, v.typing.Visible(), "a message from them ends it")

	test.Type(v.entry, "x")
	require.Equal(t, ann, f.TypingCalls[0], "typing tells the contact")
}

func TestSearchOpensMessage(t *testing.T) {
	f := standardFake()
	f.searchResult = []store.Message{f.history[ann][0]}
	v := newTestView(t, f)
	v.search.SetText("hi")
	v.search.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	require.Equal(t, []string{"hi"}, f.SearchCalls)
	require.NotNil(t, v.searchResults)
	require.Contains(t, v.resultLine(f.searchResult[0]), "Ann · ")
	require.Contains(t, v.resultLine(f.searchResult[0]), " — hi there")

	v.searchResults.Select(0)
	require.Equal(t, ann, v.selected)
	require.Equal(t, []string{"hi there", "hello back"}, bubbleTexts(v))
}
