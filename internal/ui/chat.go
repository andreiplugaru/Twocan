package ui

import (
	"bytes"
	"net/url"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/errtext"
	"twocan/internal/richtext"
	"twocan/internal/store"
)

// quoteRunes is how much of a quoted message a reply shows.
const quoteRunes = 60

// bubbleWidth is the share of the conversation width a bubble takes.
const bubbleWidth = 0.75

// buildChat creates the right pane's widgets.
func (v *MainView) buildChat() {
	v.peerName = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	v.peerName.Truncation = fyne.TextTruncateEllipsis
	v.detailsBtn = widget.NewButton("Details", func() {
		if c, ok := v.selectedContact(); ok && v.showDetails != nil {
			v.showDetails(c.PeerID, c.Name())
		}
	})
	v.detailsBtn.Hide()
	v.drafts = make(map[peer.ID]string)
	v.convo = widget.NewList(
		func() int { return len(v.msgs) },
		func() fyne.CanvasObject { return newBubble(v.showMessageMenu) },
		func(id widget.ListItemID, o fyne.CanvasObject) { o.(*bubble).set(v.msgs[id], v.quoteOf(v.msgs[id])) })
	v.convo.HideSeparators = true
	v.convo.OnSelected = func(widget.ListItemID) { v.convo.UnselectAll() }
	v.convoBox = container.New(&widthLayout{onWidth: v.convoResized}, v.convo)
	v.measurer = newBubble(nil)

	v.entry = widget.NewEntry()
	v.entry.SetPlaceHolder("Write a message")
	v.entry.OnSubmitted = func(string) { v.sendEntry() }
	v.entry.OnChanged = func(text string) {
		if text != "" && v.selected != "" && v.editing == nil {
			v.api.Typing(v.selected)
		}
	}
	v.send = widget.NewButton("Send", v.sendEntry)
	v.sendErr = widget.NewLabel("")
	v.sendErr.Importance = widget.DangerImportance
	v.sendErr.Wrapping = fyne.TextWrapWord
	v.sendErr.Hide()
	v.buildCompose()
	v.typing = widget.NewLabelWithStyle("typing…", fyne.TextAlignTrailing, fyne.TextStyle{Italic: true})
	v.typing.Hide()
}

// Row heights. widget.List gives every row the template's height unless
// told otherwise, so each message's row height is measured, at the current
// conversation width, before the list needs it: for every message when a
// conversation opens or the width changes, and for a new message before
// the list scrolls to it. Every row of the shown conversation gets a height,
// so none is left over from the previous conversation.

// convoResized re-measures every row for the new conversation width.
func (v *MainView) convoResized(width float32) {
	v.convoWidth = width
	v.measureAll()
}

func (v *MainView) measureAll() {
	for id := range v.msgs {
		v.measure(id)
	}
}

// measure sets row id's height to what its wrapped text needs. The list
// only refreshes the row if the height changed.
func (v *MainView) measure(id widget.ListItemID) {
	if v.convoWidth <= 0 {
		return // not laid out yet; convoResized measures everything
	}
	b := v.measurer
	b.set(v.msgs[id], v.quoteOf(v.msgs[id]))
	b.Resize(fyne.NewSize(v.convoWidth, b.Size().Height))
	v.convo.SetItemHeight(id, b.MinSize().Height)
}

// showConversation replaces the conversation pane's messages.
func (v *MainView) showConversation(msgs []store.Message) {
	v.msgs = msgs
	v.measureAll()
	v.convo.Refresh()
	v.convo.ScrollToBottom()
}

// appendMessage adds m to the bottom of the conversation.
func (v *MainView) appendMessage(m store.Message) {
	v.msgs = append(v.msgs, m)
	v.measure(len(v.msgs) - 1)
	v.convo.Refresh()
	v.convo.ScrollToBottom()
}

// indexOfMsg returns the index of the shown message with this ID and
// direction, or -1.
func (v *MainView) indexOfMsg(id []byte, dir store.Direction) int {
	for i, m := range v.msgs {
		if m.Direction == dir && bytes.Equal(m.ID, id) {
			return i
		}
	}
	return -1
}

// updateHeader shows the selected contact's name above the conversation,
// with the Details button next to it.
func (v *MainView) updateHeader() {
	name := ""
	c, ok := v.selectedContact()
	if ok {
		name = c.Name()
	}
	v.peerName.SetText(name)
	showIf(v.detailsBtn, ok)
}

// updateEntry enables the entry and Send only for an accepted contact.
func (v *MainView) updateEntry() {
	c, ok := v.selectedContact()
	if ok && c.State == store.StateAccepted {
		v.entry.Enable()
		v.send.Enable()
	} else {
		v.entry.Disable()
		v.send.Disable()
	}
}

// sendEntry sends the entry's text to the selected contact: as a reply if
// one is being written, or as the new text of the message being edited. On
// error the text stays in the entry and the error shows above it.
func (v *MainView) sendEntry() {
	if v.selected == "" || v.entry.Disabled() {
		return
	}
	var (
		m   store.Message
		err error
	)
	switch {
	case v.editing != nil:
		err = v.api.Edit(v.ctx, v.selected, v.editing.ID, v.entry.Text)
	case v.replyTo != nil:
		m, err = v.api.Reply(v.ctx, v.selected, v.replyTo.ID, v.entry.Text)
	default:
		m, err = v.api.Send(v.ctx, v.selected, v.entry.Text)
	}
	if err != nil {
		v.sendErr.SetText(errtext.FriendlyError(err))
		v.sendErr.Show()
		v.relayout()
		return
	}
	if v.sendErr.Visible() {
		v.sendErr.Hide()
		v.relayout()
	}
	v.entry.SetText("")
	if v.editing != nil {
		id := v.editing.ID
		v.endCompose()
		v.messagesUpdated(v.selected, [][]byte{id})
		return
	}
	v.endCompose()
	v.appendMessage(m)
}

// quoteOf is the line a reply shows above its text: who wrote the message
// it answers, and how it began; "" if m answers nothing.
func (v *MainView) quoteOf(m store.Message) string {
	if m.ReplyTo == nil {
		return ""
	}
	i := v.indexOfID(m.ReplyTo)
	if i < 0 {
		return "» a message no longer here"
	}
	return "» " + v.snippet(v.msgs[i])
}

// snippet is "Name: start of the text" for message m.
func (v *MainView) snippet(m store.Message) string {
	who := "You"
	if m.Direction == store.DirIn {
		who = v.peerName.Text
		if c, ok := v.selectedContact(); ok {
			who = c.Name()
		}
	}
	text := strings.Join(strings.Fields(richtext.Plain(m.Text)), " ")
	if m.Deleted {
		text = "deleted message"
	}
	if r := []rune(text); len(r) > quoteRunes {
		text = string(r[:quoteRunes]) + "…"
	}
	return who + ": " + text
}

// indexOfID returns the index of the shown message with this ID, or -1.
func (v *MainView) indexOfID(id []byte) int {
	for i, m := range v.msgs {
		if bytes.Equal(m.ID, id) {
			return i
		}
	}
	return -1
}

// messageReceived shows m if its conversation is open (and marks it read);
// otherwise it reloads the contacts so the sender's unread count shows. The
// node stores a message before publishing its event, so a conversation
// opened in between already shows it; it is not added twice.
func (v *MainView) messageReceived(m store.Message) {
	if m.PeerID != v.selected {
		v.reloadContacts()
		return
	}
	v.hideTyping()
	if v.indexOfMsg(m.ID, store.DirIn) >= 0 {
		return
	}
	v.appendMessage(m)
	v.markRead()
}

// messageDelivered ticks the delivered message if it is on screen.
func (v *MainView) messageDelivered(from peer.ID, id []byte) {
	if from != v.selected {
		return
	}
	if i := v.indexOfMsg(id, store.DirOut); i >= 0 && v.msgs[i].Status == store.StatusPending {
		v.msgs[i].Status = store.StatusDelivered
		v.convo.RefreshItem(i)
	}
}

// messagesUpdated shows messages that were edited, deleted, reacted to or
// read, if their conversation is open, without moving the scroll position.
func (v *MainView) messagesUpdated(id peer.ID, ids [][]byte) {
	if id != v.selected {
		v.reloadContacts() // unread counts may have changed
		return
	}
	msgs, err := v.api.History(v.ctx, id)
	if err != nil {
		v.showError(err)
		return
	}
	v.msgs = msgs
	// What is being replied to or edited may be gone, or changed.
	if v.replyTo != nil && v.indexOfID(v.replyTo.ID) < 0 {
		v.endCompose()
	}
	if v.editing != nil {
		if i := v.indexOfID(v.editing.ID); i < 0 || v.msgs[i].Deleted {
			v.endCompose()
		}
	}
	v.measureAll()
	v.convo.Refresh()
	v.reloadContacts()
}

// widthLayout fills its single object and calls onWidth when its width
// changes.
type widthLayout struct {
	width   float32
	onWidth func(float32)
}

func (l *widthLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	objs[0].Move(fyne.NewPos(0, 0))
	objs[0].Resize(size)
	if size.Width != l.width {
		l.width = size.Width
		l.onWidth(size.Width)
	}
}

func (l *widthLayout) MinSize(objs []fyne.CanvasObject) fyne.Size { return objs[0].MinSize() }

// bubble is one message in the conversation. Outgoing bubbles sit on the
// right, incoming ones on the left. Tapping it (or right-clicking) opens
// its menu; links in it open in the browser.
type bubble struct {
	widget.BaseWidget
	bg        *canvas.Rectangle
	quote     *widget.Label    // "» Ann: …" for a reply; hidden otherwise
	text      *widget.RichText // the formatted text, or "This message was deleted"
	meta      *widget.Label    // "15:04", "edited", plus 🕓, ✓ or ✓✓ when outgoing
	reactions *widget.Label    // the emoji each side reacted with; hidden if none
	out       bool

	msg    store.Message
	onMenu func(m store.Message, at fyne.Position) // nil for the off-screen measurer

	// shown identifies what is on display, so the list's frequent row
	// updates skip re-setting (and re-wrapping) the same message.
	shown string
}

var (
	_ fyne.Tappable          = (*bubble)(nil)
	_ fyne.SecondaryTappable = (*bubble)(nil)
)

func newBubble(onMenu func(store.Message, fyne.Position)) *bubble {
	b := &bubble{
		bg:        canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground)),
		quote:     widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		text:      widget.NewRichText(),
		meta:      widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Italic: true}),
		reactions: widget.NewLabel(""),
		onMenu:    onMenu,
	}
	b.bg.CornerRadius = theme.Size(theme.SizeNameInputRadius)
	b.quote.SizeName = theme.SizeNameCaptionText
	b.quote.Truncation = fyne.TextTruncateEllipsis
	b.quote.Hide()
	b.text.Wrapping = fyne.TextWrapWord
	b.meta.SizeName = theme.SizeNameCaptionText
	b.reactions.Hide()
	b.ExtendBaseWidget(b)
	return b
}

// set shows m; quote is its reply line ("" if it answers nothing).
func (b *bubble) set(m store.Message, quote string) {
	key := strings.Join([]string{string(m.Direction), string(m.Status), string(m.ID), m.Text,
		m.EditedAt.String(), boolKey(m.Deleted), m.MyReaction, m.TheirReaction, quote}, "\x00")
	if key == b.shown {
		return
	}
	b.shown, b.msg = key, m
	b.out = m.Direction == store.DirOut

	b.quote.SetText(quote)
	showIf(b.quote, quote != "")
	b.text.Segments = textSegments(m)
	b.text.Refresh()

	at, mark := m.ReceivedAt, ""
	if b.out {
		at, mark = m.SentAt, " 🕓"
		switch m.Status {
		case store.StatusDelivered:
			mark = " ✓"
		case store.StatusRead:
			mark = " ✓✓"
		}
	}
	meta := at.Format("15:04")
	if !m.EditedAt.IsZero() && !m.Deleted {
		meta = "edited · " + meta
	}
	if m.Deleted {
		mark = ""
	}
	b.meta.SetText(meta + mark)

	b.reactions.SetText(strings.TrimSpace(m.TheirReaction + " " + m.MyReaction))
	showIf(b.reactions, b.reactions.Text != "")
	b.Refresh()
}

// plainText is the text on display, without formatting (tests).
func (b *bubble) plainText() string {
	var sb strings.Builder
	for _, s := range b.text.Segments {
		sb.WriteString(s.Textual())
	}
	return sb.String()
}

// textSegments formats m's text: bold, italic and links.
func textSegments(m store.Message) []widget.RichTextSegment {
	if m.Deleted {
		return []widget.RichTextSegment{&widget.TextSegment{Text: "This message was deleted", Style: widget.RichTextStyle{
			Inline: true, ColorName: theme.ColorNamePlaceHolder, TextStyle: fyne.TextStyle{Italic: true},
		}}}
	}
	var segs []widget.RichTextSegment
	for _, s := range richtext.Parse(m.Text) {
		if s.URL != "" {
			if u, err := url.Parse(s.URL); err == nil {
				segs = append(segs, &widget.HyperlinkSegment{Text: s.Text, URL: u})
				continue
			}
		}
		segs = append(segs, &widget.TextSegment{Text: s.Text, Style: widget.RichTextStyle{
			Inline: true, TextStyle: fyne.TextStyle{Bold: s.Bold, Italic: s.Italic},
		}})
	}
	return segs
}

func boolKey(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// Tapped opens the message's menu.
func (b *bubble) Tapped(e *fyne.PointEvent) { b.openMenu(e) }

// TappedSecondary (a right-click) opens the message's menu too.
func (b *bubble) TappedSecondary(e *fyne.PointEvent) { b.openMenu(e) }

func (b *bubble) openMenu(e *fyne.PointEvent) {
	if b.onMenu != nil && b.msg.ID != nil {
		b.onMenu(b.msg, e.AbsolutePosition)
	}
}

func (b *bubble) CreateRenderer() fyne.WidgetRenderer {
	return &bubbleRenderer{b: b, box: container.NewVBox(b.quote, b.text, b.reactions, b.meta)}
}

type bubbleRenderer struct {
	b   *bubble
	box *fyne.Container
}

// Layout places the bubble at bubbleWidth of the row, on its side.
func (r *bubbleRenderer) Layout(size fyne.Size) {
	w := size.Width * bubbleWidth
	x := float32(0)
	if r.b.out {
		x = size.Width - w
	}
	r.box.Move(fyne.NewPos(x, 0))
	r.box.Resize(fyne.NewSize(w, size.Height))
	r.b.bg.Move(r.box.Position())
	r.b.bg.Resize(r.box.Size())
}

func (r *bubbleRenderer) MinSize() fyne.Size { return r.box.MinSize() }

func (r *bubbleRenderer) Refresh() {
	r.b.bg.FillColor = theme.Color(theme.ColorNameInputBackground)
	if r.b.out {
		r.b.bg.FillColor = theme.Color(theme.ColorNameSelection)
	}
	r.Layout(r.b.Size())
	r.b.bg.Refresh()
	r.box.Refresh()
}

func (r *bubbleRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.b.bg, r.box}
}

func (r *bubbleRenderer) Destroy() {}
