package ui

import (
	"bytes"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/errtext"
	"twocan/internal/store"
)

// bubbleWidth is the share of the conversation width a bubble takes.
const bubbleWidth = 0.75

// buildChat creates the right pane's widgets.
func (v *MainView) buildChat() {
	v.convo = widget.NewList(
		func() int { return len(v.msgs) },
		func() fyne.CanvasObject { return newBubble() },
		func(id widget.ListItemID, o fyne.CanvasObject) { o.(*bubble).set(v.msgs[id]) })
	v.convo.HideSeparators = true
	v.convo.OnSelected = func(widget.ListItemID) { v.convo.UnselectAll() }
	v.convoBox = container.New(&widthLayout{onWidth: v.convoResized}, v.convo)
	v.measurer = newBubble()

	v.entry = widget.NewEntry()
	v.entry.SetPlaceHolder("Write a message")
	v.entry.OnSubmitted = func(string) { v.sendEntry() }
	v.send = widget.NewButton("Send", v.sendEntry)
	v.sendErr = widget.NewLabel("")
	v.sendErr.Importance = widget.DangerImportance
	v.sendErr.Wrapping = fyne.TextWrapWord
	v.sendErr.Hide()
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
	b.set(v.msgs[id])
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

// sendEntry sends the entry's text to the selected contact. On error the
// text stays in the entry and the error shows above it.
func (v *MainView) sendEntry() {
	if v.selected == "" || v.entry.Disabled() {
		return
	}
	m, err := v.api.Send(v.ctx, v.selected, v.entry.Text)
	if err != nil {
		v.sendErr.SetText(errtext.FriendlyError(err))
		v.sendErr.Show()
		return
	}
	v.sendErr.Hide()
	v.entry.SetText("")
	v.appendMessage(m)
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
	if i := v.indexOfMsg(id, store.DirOut); i >= 0 {
		v.msgs[i].Status = store.StatusDelivered
		v.convo.RefreshItem(i)
	}
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
// right, incoming ones on the left.
type bubble struct {
	widget.BaseWidget
	bg   *canvas.Rectangle
	text *widget.Label
	meta *widget.Label // "15:04", plus 🕓 or ✓ when outgoing
	out  bool

	// shown identifies the message and status on display, so the list's
	// frequent row updates skip re-setting (and re-wrapping) the same text.
	shown string
}

func newBubble() *bubble {
	b := &bubble{
		bg:   canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground)),
		text: widget.NewLabel(""),
		meta: widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Italic: true}),
	}
	b.bg.CornerRadius = theme.Size(theme.SizeNameInputRadius)
	b.text.Wrapping = fyne.TextWrapWord
	b.meta.SizeName = theme.SizeNameCaptionText
	b.ExtendBaseWidget(b)
	return b
}

func (b *bubble) set(m store.Message) {
	key := string(m.Direction) + string(m.Status) + string(m.ID)
	if key == b.shown {
		return
	}
	b.shown = key
	b.out = m.Direction == store.DirOut
	at, mark := m.ReceivedAt, ""
	if b.out {
		at, mark = m.SentAt, " 🕓"
		if m.Status == store.StatusDelivered {
			mark = " ✓"
		}
	}
	b.text.SetText(m.Text)
	b.meta.SetText(at.Format("15:04") + mark)
	b.Refresh()
}

func (b *bubble) CreateRenderer() fyne.WidgetRenderer {
	return &bubbleRenderer{b: b, box: container.NewVBox(b.text, b.meta)}
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
